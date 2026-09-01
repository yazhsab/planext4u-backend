package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const paymentOperationTimeout = 30 * time.Second

// PostgresService durably records a provider operation before crossing the
// network boundary. Provider failures become retryable payment states instead
// of disappearing with the process, and signed events are applied exactly once.
type PostgresService struct {
	pool      *pgxpool.Pool
	clock     func() time.Time
	secrets   map[Method][]byte
	providers map[Method]ProviderInitializer
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time, providerSecrets map[Method][]byte, providers map[Method]ProviderInitializer) (*PostgresService, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	secrets := map[Method][]byte{}
	for method, secret := range providerSecrets {
		if (method != MethodRazorpay && method != MethodPaystack) || len(secret) < 32 {
			return nil, ErrInvalidRequest
		}
		secrets[method] = append([]byte(nil), secret...)
	}
	configured := map[Method]ProviderInitializer{}
	for method, provider := range providers {
		if provider == nil || (method != MethodRazorpay && method != MethodPaystack) || len(secrets[method]) == 0 {
			return nil, ErrInvalidRequest
		}
		configured[method] = provider
	}
	for method := range secrets {
		if configured[method] == nil {
			return nil, ErrInvalidRequest
		}
	}
	return &PostgresService{pool: pool, clock: clock, secrets: secrets, providers: configured}, nil
}

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `
		SELECT to_regclass('payment.payments') IS NOT NULL
		   AND to_regclass('payment.provider_events') IS NOT NULL
		   AND to_regclass('payment.command_records') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check payment schema readiness: %w", err)
	}
	if !ready {
		return errors.New("payment schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Create(scope Scope, idempotencyKey, orderReference string, method Method, amount Money) (Payment, bool, error) {
	return service.CreateWithPayer(context.Background(), scope, idempotencyKey, orderReference, method, amount, Payer{})
}

func (service *PostgresService) CreateWithPayer(ctx context.Context, scope Scope, idempotencyKey, orderReference string, method Method, amount Money, payer Payer) (Payment, bool, error) {
	if ctx == nil || !postgresPaymentScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !safeID(orderReference) || !validMoney(amount) || !service.supported(scope.Country, method) {
		return Payment{}, false, ErrInvalidRequest
	}
	fingerprint := paymentFingerprint(fmt.Sprintf("%s\x00%s\x00%d\x00%s", orderReference, method, amount.AmountMinor, amount.Currency))
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, false, fmt.Errorf("begin payment creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockPaymentCommand(ctx, tx, scope, idempotencyKey); err != nil {
		return Payment{}, false, err
	}
	if replay, found, err := loadPaymentCommand(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return Payment{}, false, err
	} else if found {
		return replay, true, nil
	}
	if existing, found, err := loadPaymentByIdempotency(ctx, tx, scope, idempotencyKey, true); err != nil {
		return Payment{}, false, err
	} else if found {
		if existingRequestFingerprint(ctx, tx, existing.ID) != fingerprint {
			return Payment{}, false, ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	now := service.clock().UTC()
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(scope.TenantID+":"+scope.Country+":"+scope.CustomerID+":"+idempotencyKey)).String()
	status, operationState := StatusFailedRetryable, "INITIALIZING"
	if method == MethodCOD {
		status, operationState = StatusAuthorisationPending, "READY"
	} else if method == MethodWallet {
		status, operationState = StatusCaptured, "READY"
	}
	value := Payment{ID: id, OrderReference: orderReference, Method: method, Status: status,
		Amount: amount, AllowedActions: allowedActions(status), CreatedAt: now, UpdatedAt: now, scope: scope, payer: payer}
	if err := insertPostgresPayment(ctx, tx, value, idempotencyKey, fingerprint, operationState); err != nil {
		return Payment{}, false, err
	}
	if operationState == "READY" {
		if err := storePaymentCommand(ctx, tx, scope, idempotencyKey, fingerprint, value, now); err != nil {
			return Payment{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, false, fmt.Errorf("commit payment creation intent: %w", err)
	}
	if operationState == "READY" {
		return value, false, nil
	}

	session, providerErr := service.providers[method].Initialize(ctx, ProviderInitialization{
		PaymentID: id, OrderReference: orderReference, Amount: amount, Payer: payer,
	})
	if providerErr == nil && !validProviderSession(method, session) {
		providerErr = ErrProviderResponse
	}
	finalStatus := StatusProviderOrderCreated
	if providerErr != nil {
		finalStatus = StatusFailedRetryable
		if errors.Is(providerErr, ErrProviderResponse) || errors.Is(providerErr, ErrInvalidRequest) {
			finalStatus = StatusFailedFinal
		}
	}
	final, err := service.finalizeInitialization(ctx, scope, idempotencyKey, fingerprint, id, finalStatus, session)
	if err != nil {
		return Payment{}, false, err
	}
	// A recorded failure is a valid payment result. Checkout can create a
	// pending order and expose RETRY without losing the provider attempt.
	return final, false, nil
}

func (service *PostgresService) finalizeInitialization(ctx context.Context, scope Scope, key, fingerprint, paymentID string, status Status, session ProviderSession) (Payment, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, fmt.Errorf("begin payment initialization result: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, found, err := loadPostgresPayment(ctx, tx, scope, paymentID, true)
	if err != nil || !found {
		if err == nil {
			err = ErrPaymentNotFound
		}
		return Payment{}, err
	}
	if value.Status == StatusProviderOrderCreated && value.ProviderReference != "" {
		return value, nil
	}
	value.Status, value.UpdatedAt = status, service.clock().UTC()
	if status == StatusProviderOrderCreated {
		value.ProviderReference = session.ProviderReference
		handoff := session.ClientHandoff
		value.ClientHandoff = &handoff
	}
	value.AllowedActions = allowedActions(value.Status)
	if err := updatePostgresPayment(ctx, tx, value, "READY"); err != nil {
		return Payment{}, err
	}
	if err := storePaymentCommand(ctx, tx, scope, key, fingerprint, value, value.UpdatedAt); err != nil {
		return Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, fmt.Errorf("commit payment initialization result: %w", err)
	}
	return value, nil
}

func (service *PostgresService) RetryProvider(ctx context.Context, scope Scope, idempotencyKey, paymentID string) (Payment, bool, error) {
	if ctx == nil || !postgresPaymentScope(scope) || !safeID(idempotencyKey) || len(idempotencyKey) < 16 || !paymentUUID(paymentID) {
		return Payment{}, false, ErrInvalidRequest
	}
	fingerprint := paymentFingerprint("retry\x00" + paymentID)
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, false, fmt.Errorf("begin provider retry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockPaymentCommand(ctx, tx, scope, idempotencyKey); err != nil {
		return Payment{}, false, err
	}
	if replay, found, err := loadPaymentCommand(ctx, tx, scope, idempotencyKey, fingerprint); err != nil {
		return Payment{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, found, err := loadPostgresPayment(ctx, tx, scope, paymentID, true)
	if err != nil || !found {
		if err == nil {
			err = ErrPaymentNotFound
		}
		return Payment{}, false, err
	}
	provider := service.providers[value.Method]
	if value.Status != StatusFailedRetryable || provider == nil {
		return Payment{}, false, ErrInvalidTransition
	}
	if _, err := tx.Exec(ctx, `UPDATE payment.payments SET operation_state='INITIALIZING' WHERE id=$1`, value.ID); err != nil {
		return Payment{}, false, fmt.Errorf("mark payment retry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, false, fmt.Errorf("commit payment retry intent: %w", err)
	}
	digest := sha256.Sum256([]byte(scope.TenantID + ":" + idempotencyKey))
	session, providerErr := provider.Initialize(ctx, ProviderInitialization{
		PaymentID: value.ID, OrderReference: value.OrderReference + "-" + hex.EncodeToString(digest[:4]), Amount: value.Amount, Payer: value.payer,
	})
	if providerErr == nil && !validProviderSession(value.Method, session) {
		providerErr = ErrProviderResponse
	}
	if providerErr != nil {
		_, _ = service.pool.Exec(ctx, `UPDATE payment.payments SET operation_state='READY',updated_at=$2 WHERE id=$1`, value.ID, service.clock().UTC())
		return Payment{}, false, providerErr
	}
	final, err := service.finalizeInitialization(ctx, scope, idempotencyKey, fingerprint, value.ID, StatusProviderOrderCreated, session)
	return final, false, err
}

func (service *PostgresService) Get(scope Scope, paymentID string) (Payment, error) {
	if !postgresPaymentScope(scope) || !paymentUUID(paymentID) {
		return Payment{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), paymentOperationTimeout)
	defer cancel()
	value, found, err := loadPostgresPayment(ctx, service.pool, scope, paymentID, false)
	if err != nil {
		return Payment{}, err
	}
	if !found {
		return Payment{}, ErrPaymentNotFound
	}
	return value, nil
}

type ReconciliationCandidate struct {
	Scope     Scope
	PaymentID string
}

// ReconciliationCandidates returns stale provider payments without claiming
// them. ReconcileWithProvider locks and rechecks the row, so multiple workers
// may safely process the same bounded page.
func (service *PostgresService) ReconciliationCandidates(ctx context.Context, olderThan time.Duration, limit int) ([]ReconciliationCandidate, error) {
	if ctx == nil || olderThan < time.Minute || olderThan > 24*time.Hour || limit < 1 || limit > 500 {
		return nil, ErrInvalidRequest
	}
	rows, err := service.pool.Query(ctx, `
		SELECT tenant_id::text,country,customer_identity_id::text,id::text
		FROM payment.payments
		WHERE method IN ('RAZORPAY','PAYSTACK')
		  AND status IN ('PROVIDER_ORDER_CREATED','AUTHORISATION_PENDING','AUTHORISED','CAPTURED')
		  AND operation_state='READY' AND updated_at <= $1
		ORDER BY updated_at,id LIMIT $2`, service.clock().UTC().Add(-olderThan), limit)
	if err != nil {
		return nil, fmt.Errorf("list reconciliation candidates: %w", err)
	}
	defer rows.Close()
	result := []ReconciliationCandidate{}
	for rows.Next() {
		var value ReconciliationCandidate
		if err := rows.Scan(&value.Scope.TenantID, &value.Scope.Country, &value.Scope.CustomerID, &value.PaymentID); err != nil {
			return nil, fmt.Errorf("scan reconciliation candidate: %w", err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (service *PostgresService) ReconcileWithProvider(ctx context.Context, scope Scope, paymentID string) (Payment, error) {
	if ctx == nil || !postgresPaymentScope(scope) || !paymentUUID(paymentID) {
		return Payment{}, ErrInvalidRequest
	}
	value, err := service.Get(scope, paymentID)
	if err != nil {
		return Payment{}, err
	}
	provider := service.providers[value.Method]
	verifier, supported := provider.(ProviderVerifier)
	if provider == nil || !supported {
		return Payment{}, ErrProviderUnavailable
	}
	verification, err := verifier.Verify(ctx, value)
	if err != nil {
		return Payment{}, err
	}
	matching := verification.Status == StatusCaptured && verification.Amount == value.Amount &&
		verification.ProviderReference == value.ProviderReference &&
		(value.ProviderTransactionReference == "" || verification.ProviderTransactionReference == value.ProviderTransactionReference)
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, fmt.Errorf("begin payment reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	latest, found, err := loadPostgresPayment(ctx, tx, scope, paymentID, true)
	if err != nil || !found {
		if err == nil {
			err = ErrPaymentNotFound
		}
		return Payment{}, err
	}
	now := service.clock().UTC()
	if !matching || latest.Amount != verification.Amount || latest.ProviderReference != verification.ProviderReference {
		event := ProviderEvent{EventID: "reconciliation-" + paymentID, PaymentID: paymentID, ProviderReference: verification.ProviderReference, ProviderTransactionReference: verification.ProviderTransactionReference, Status: verification.Status, AmountMinor: verification.Amount.AmountMinor, Currency: verification.Amount.Currency}
		if err := recordReconciliationException(ctx, tx, paymentID, "PROVIDER_STATE_MISMATCH", event, now); err != nil {
			return Payment{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Payment{}, fmt.Errorf("commit reconciliation exception: %w", err)
		}
		return Payment{}, ErrReconciliation
	}
	if latest.Status == StatusReconciled {
		return latest, nil
	}
	if latest.Status != StatusProviderOrderCreated && latest.Status != StatusAuthorisationPending && latest.Status != StatusAuthorised && latest.Status != StatusCaptured {
		return Payment{}, ErrInvalidTransition
	}
	latest.Status, latest.ProviderTransactionReference, latest.UpdatedAt = StatusReconciled, verification.ProviderTransactionReference, now
	latest.AllowedActions = allowedActions(latest.Status)
	if err := updatePostgresPayment(ctx, tx, latest, "READY"); err != nil {
		return Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, fmt.Errorf("commit payment reconciliation: %w", err)
	}
	return latest, nil
}

func (service *PostgresService) HandleProviderWebhook(method Method, signature, providerEventID string, body []byte) (Payment, bool, error) {
	secret, exists := service.secrets[method]
	if !exists || len(body) == 0 || len(body) > 64*1024 || !verifyProviderSignature(method, secret, signature, body) {
		if exists {
			return Payment{}, false, ErrSignatureInvalid
		}
		return Payment{}, false, ErrInvalidRequest
	}
	event, err := normalizeProviderEvent(method, providerEventID, body)
	if err != nil || !paymentUUID(event.PaymentID) {
		return Payment{}, false, ErrInvalidRequest
	}
	digestBytes := sha256.Sum256(body)
	return service.applyProviderEvent(method, event, hex.EncodeToString(digestBytes[:]))
}

func (service *PostgresService) HandleWebhook(method Method, signature string, body []byte) (Payment, bool, error) {
	secret, exists := service.secrets[method]
	if !exists || len(body) == 0 || len(body) > 64*1024 {
		return Payment{}, false, ErrInvalidRequest
	}
	expected := hmac.New(sha256.New, secret)
	_, _ = expected.Write(body)
	provided, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || !hmac.Equal(provided, expected.Sum(nil)) {
		return Payment{}, false, ErrSignatureInvalid
	}
	var event ProviderEvent
	if json.Unmarshal(body, &event) != nil || !paymentUUID(event.PaymentID) {
		return Payment{}, false, ErrInvalidRequest
	}
	digest := sha256.Sum256(body)
	return service.applyProviderEvent(method, event, hex.EncodeToString(digest[:]))
}

func (service *PostgresService) applyProviderEvent(method Method, event ProviderEvent, digest string) (Payment, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), paymentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, false, fmt.Errorf("begin provider event: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := string(method) + ":" + event.EventID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return Payment{}, false, fmt.Errorf("lock provider event: %w", err)
	}
	var storedDigest string
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT body_digest,response_payload FROM payment.provider_events WHERE provider=$1 AND event_id=$2`, method, event.EventID).Scan(&storedDigest, &payload)
	if err == nil {
		if storedDigest != digest {
			return Payment{}, false, ErrProviderEventReuse
		}
		var replay Payment
		if len(payload) == 0 || json.Unmarshal(payload, &replay) != nil {
			return Payment{}, false, fmt.Errorf("decode provider event replay: %w", ErrInvalidRequest)
		}
		return replay, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, fmt.Errorf("load provider event: %w", err)
	}
	var scope Scope
	if err := tx.QueryRow(ctx, `SELECT tenant_id::text,country,customer_identity_id::text FROM payment.payments WHERE id=$1`, event.PaymentID).Scan(&scope.TenantID, &scope.Country, &scope.CustomerID); errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, ErrReconciliation
	} else if err != nil {
		return Payment{}, false, fmt.Errorf("locate provider event payment: %w", err)
	}
	value, found, err := loadPostgresPayment(ctx, tx, scope, event.PaymentID, true)
	if err != nil || !found {
		return Payment{}, false, ErrReconciliation
	}
	expectedAmount := value.Amount
	if (value.Status == StatusRefundRequested || value.Status == StatusRefundSubmitted || value.Status == StatusRefundFailed) && value.RefundAmount != nil {
		expectedAmount = *value.RefundAmount
	}
	referenceMatches := value.ProviderReference == event.ProviderReference || (event.ProviderReference == "" && value.Status == StatusRefundSubmitted)
	transactionMatches := event.ProviderTransactionReference == "" || value.ProviderTransactionReference == "" || value.ProviderTransactionReference == event.ProviderTransactionReference
	if value.Method != method || !referenceMatches || !transactionMatches || expectedAmount.AmountMinor != event.AmountMinor || expectedAmount.Currency != event.Currency {
		if err := recordReconciliationException(ctx, tx, value.ID, "PROVIDER_EVENT_MISMATCH", event, service.clock().UTC()); err != nil {
			return Payment{}, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Payment{}, false, fmt.Errorf("commit reconciliation exception: %w", err)
		}
		return Payment{}, false, ErrReconciliation
	}
	if !validProviderTransition(value.Status, event.Status) {
		return Payment{}, false, ErrInvalidTransition
	}
	value.Status, value.UpdatedAt = event.Status, service.clock().UTC()
	if event.ProviderTransactionReference != "" {
		value.ProviderTransactionReference = event.ProviderTransactionReference
	}
	value.AllowedActions = allowedActions(value.Status)
	if err := updatePostgresPayment(ctx, tx, value, "READY"); err != nil {
		return Payment{}, false, err
	}
	payload, _ = json.Marshal(value)
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment.provider_events (provider,event_id,payment_id,body_digest,received_at,response_payload)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb)`, method, event.EventID, value.ID, digest, value.UpdatedAt, payload); err != nil {
		return Payment{}, false, fmt.Errorf("store provider event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, false, fmt.Errorf("commit provider event: %w", err)
	}
	return value, false, nil
}

func (service *PostgresService) RequestRefund(scope Scope, paymentID string, amount Money) (Payment, error) {
	return service.RequestRefundWithProvider(context.Background(), scope, paymentID, amount, "Customer return approved")
}

func (service *PostgresService) RequestRefundWithProvider(ctx context.Context, scope Scope, paymentID string, amount Money, reason string) (Payment, error) {
	reason = strings.TrimSpace(reason)
	if ctx == nil || !postgresPaymentScope(scope) || !paymentUUID(paymentID) || !validMoney(amount) || reason == "" || len(reason) > 500 {
		return Payment{}, ErrInvalidRequest
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, fmt.Errorf("begin payment refund: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, found, err := loadPostgresPayment(ctx, tx, scope, paymentID, true)
	if err != nil || !found {
		if err == nil {
			err = ErrPaymentNotFound
		}
		return Payment{}, err
	}
	if value.RefundAmount != nil && *value.RefundAmount == amount && (value.Status == StatusRefundRequested || value.Status == StatusRefundSubmitted || value.Status == StatusRefunded) {
		return value, nil
	}
	if value.Status != StatusCaptured && value.Status != StatusReconciled && value.Status != StatusRefundFailed {
		return Payment{}, ErrInvalidTransition
	}
	if amount.AmountMinor < 1 || amount.Currency != value.Amount.Currency || amount.AmountMinor > value.Amount.AmountMinor {
		return Payment{}, ErrInvalidRequest
	}
	value.Status, value.RefundAmount, value.UpdatedAt = StatusRefundRequested, &amount, service.clock().UTC()
	value.AllowedActions = allowedActions(value.Status)
	if err := updatePostgresPayment(ctx, tx, value, "REFUNDING"); err != nil {
		return Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, fmt.Errorf("commit payment refund intent: %w", err)
	}
	provider := service.providers[value.Method]
	refunder, providerRefund := provider.(ProviderRefunder)
	var submission ProviderRefundSubmission
	var providerErr error
	if providerRefund {
		submission, providerErr = refunder.Refund(ctx, ProviderRefundRequest{
			PaymentID: paymentID, ProviderReference: value.ProviderReference,
			ProviderTransactionReference: value.ProviderTransactionReference,
			Amount:                       amount, Reason: reason,
		})
	} else if value.Method == MethodRazorpay || value.Method == MethodPaystack {
		providerErr = ErrProviderUnavailable
	} else {
		submission.Status = StatusRefundSubmitted
	}
	return service.finalizeRefund(ctx, scope, paymentID, amount, submission, providerErr)
}

func (service *PostgresService) finalizeRefund(ctx context.Context, scope Scope, paymentID string, amount Money, submission ProviderRefundSubmission, providerErr error) (Payment, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, fmt.Errorf("begin payment refund result: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, found, err := loadPostgresPayment(ctx, tx, scope, paymentID, true)
	if err != nil || !found || value.RefundAmount == nil || *value.RefundAmount != amount {
		return Payment{}, ErrReconciliation
	}
	resultErr := providerErr
	if providerErr != nil {
		value.Status = StatusRefundFailed
	} else if submission.Status != StatusRefundSubmitted && submission.Status != StatusRefunded {
		value.Status, resultErr = StatusRefundFailed, ErrProviderResponse
	} else if submission.ProviderRefundReference != "" && !safeID(submission.ProviderRefundReference) {
		value.Status, resultErr = StatusRefundFailed, ErrProviderResponse
	} else {
		value.Status, value.ProviderRefundReference = submission.Status, submission.ProviderRefundReference
	}
	value.UpdatedAt, value.AllowedActions = service.clock().UTC(), allowedActions(value.Status)
	if err := updatePostgresPayment(ctx, tx, value, "READY"); err != nil {
		return Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, fmt.Errorf("commit payment refund result: %w", err)
	}
	return value, resultErr
}

func (service *PostgresService) MarkCODCollected(scope Scope, paymentID string) (Payment, error) {
	return service.simpleMutation(scope, paymentID, func(value *Payment) error {
		if value.Method != MethodCOD || value.Status != StatusAuthorisationPending {
			return ErrInvalidTransition
		}
		value.Status = StatusCaptured
		return nil
	})
}

func (service *PostgresService) CancelUncaptured(scope Scope, paymentID string) (Payment, error) {
	return service.simpleMutation(scope, paymentID, func(value *Payment) error {
		if value.Status == StatusCancelled {
			return nil
		}
		switch value.Status {
		case StatusProviderOrderCreated, StatusAuthorisationPending, StatusAuthorised, StatusFailedRetryable, StatusFailedFinal:
			value.Status = StatusCancelled
			return nil
		default:
			return ErrInvalidTransition
		}
	})
}

func (service *PostgresService) simpleMutation(scope Scope, paymentID string, apply func(*Payment) error) (Payment, error) {
	if !postgresPaymentScope(scope) || !paymentUUID(paymentID) || apply == nil {
		return Payment{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), paymentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Payment{}, fmt.Errorf("begin payment mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	value, found, err := loadPostgresPayment(ctx, tx, scope, paymentID, true)
	if err != nil || !found {
		if err == nil {
			err = ErrPaymentNotFound
		}
		return Payment{}, err
	}
	if err := apply(&value); err != nil {
		return Payment{}, err
	}
	value.UpdatedAt, value.AllowedActions = service.clock().UTC(), allowedActions(value.Status)
	if err := updatePostgresPayment(ctx, tx, value, "READY"); err != nil {
		return Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, fmt.Errorf("commit payment mutation: %w", err)
	}
	return value, nil
}

func (service *PostgresService) supported(country string, method Method) bool {
	switch method {
	case MethodCOD, MethodWallet:
		return true
	case MethodRazorpay:
		return country == "IN" && len(service.secrets[method]) > 0 && service.providers[method] != nil
	case MethodPaystack:
		return country == "NG" && len(service.secrets[method]) > 0 && service.providers[method] != nil
	default:
		return false
	}
}

type paymentQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadPostgresPayment(ctx context.Context, querier paymentQuerier, scope Scope, paymentID string, lock bool) (Payment, bool, error) {
	query := `SELECT id::text,order_reference,method,status,amount_minor,currency,
		refund_amount_minor,provider_reference,provider_transaction_reference,
		provider_refund_reference,client_handoff,payer,created_at,updated_at
		FROM payment.payments WHERE id=$1 AND tenant_id=$2 AND country=$3 AND customer_identity_id=$4`
	if lock {
		query += ` FOR UPDATE`
	}
	var value Payment
	var refund *int64
	var providerReference, transactionReference, refundReference *string
	var handoffPayload, payerPayload []byte
	err := querier.QueryRow(ctx, query, paymentID, scope.TenantID, scope.Country, scope.CustomerID).Scan(
		&value.ID, &value.OrderReference, &value.Method, &value.Status,
		&value.Amount.AmountMinor, &value.Amount.Currency, &refund,
		&providerReference, &transactionReference, &refundReference,
		&handoffPayload, &payerPayload, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, nil
	}
	if err != nil {
		return Payment{}, false, fmt.Errorf("load payment: %w", err)
	}
	if refund != nil {
		value.RefundAmount = &Money{AmountMinor: *refund, Currency: value.Amount.Currency}
	}
	if providerReference != nil {
		value.ProviderReference = *providerReference
	}
	if transactionReference != nil {
		value.ProviderTransactionReference = *transactionReference
	}
	if refundReference != nil {
		value.ProviderRefundReference = *refundReference
	}
	if len(handoffPayload) > 0 && json.Unmarshal(handoffPayload, &value.ClientHandoff) != nil {
		return Payment{}, false, fmt.Errorf("decode payment handoff: %w", ErrInvalidRequest)
	}
	if len(payerPayload) > 0 && json.Unmarshal(payerPayload, &value.payer) != nil {
		return Payment{}, false, fmt.Errorf("decode payment payer: %w", ErrInvalidRequest)
	}
	value.scope, value.AllowedActions = scope, allowedActions(value.Status)
	return value, true, nil
}

func loadPaymentByIdempotency(ctx context.Context, tx pgx.Tx, scope Scope, key string, lock bool) (Payment, bool, error) {
	query := `SELECT id::text FROM payment.payments WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND idempotency_key=$4`
	if lock {
		query += ` FOR UPDATE`
	}
	var id string
	err := tx.QueryRow(ctx, query, scope.TenantID, scope.Country, scope.CustomerID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, nil
	}
	if err != nil {
		return Payment{}, false, fmt.Errorf("load payment idempotency row: %w", err)
	}
	return loadPostgresPayment(ctx, tx, scope, id, false)
}

func existingRequestFingerprint(ctx context.Context, tx pgx.Tx, paymentID string) string {
	var value string
	_ = tx.QueryRow(ctx, `SELECT request_fingerprint FROM payment.payments WHERE id=$1`, paymentID).Scan(&value)
	return value
}

func insertPostgresPayment(ctx context.Context, tx pgx.Tx, value Payment, key, fingerprint, operationState string) error {
	var payer any
	if value.payer != (Payer{}) {
		payload, err := json.Marshal(value.payer)
		if err != nil {
			return fmt.Errorf("encode payment payer: %w", err)
		}
		payer = payload
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment.payments
			(id,tenant_id,country,customer_identity_id,order_reference,method,status,
			 amount_minor,currency,idempotency_key,request_fingerprint,created_at,updated_at,payer,operation_state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12,$13::jsonb,$14)`,
		value.ID, value.scope.TenantID, value.scope.Country, value.scope.CustomerID,
		value.OrderReference, value.Method, value.Status, value.Amount.AmountMinor,
		value.Amount.Currency, key, fingerprint, value.CreatedAt, payer, operationState); err != nil {
		return fmt.Errorf("insert payment: %w", err)
	}
	return nil
}

func updatePostgresPayment(ctx context.Context, tx pgx.Tx, value Payment, operationState string) error {
	var refund any
	if value.RefundAmount != nil {
		refund = value.RefundAmount.AmountMinor
	}
	var handoff any
	if value.ClientHandoff != nil {
		payload, err := json.Marshal(value.ClientHandoff)
		if err != nil {
			return fmt.Errorf("encode payment handoff: %w", err)
		}
		handoff = payload
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payment.payments SET status=$2,refund_amount_minor=$3,
			provider_reference=NULLIF($4,''),provider_transaction_reference=NULLIF($5,''),
			provider_refund_reference=NULLIF($6,''),client_handoff=$7::jsonb,
			updated_at=$8,operation_state=$9 WHERE id=$1`,
		value.ID, value.Status, refund, value.ProviderReference,
		value.ProviderTransactionReference, value.ProviderRefundReference,
		handoff, value.UpdatedAt, operationState); err != nil {
		return fmt.Errorf("update payment: %w", err)
	}
	return nil
}

func lockPaymentCommand(ctx context.Context, tx pgx.Tx, scope Scope, key string) error {
	lockKey := scope.TenantID + ":" + scope.Country + ":" + scope.CustomerID + ":" + key
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return fmt.Errorf("lock payment command: %w", err)
	}
	return nil
}

func loadPaymentCommand(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string) (Payment, bool, error) {
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT request_fingerprint,response_payload FROM payment.command_records
		WHERE tenant_id=$1 AND country=$2 AND customer_identity_id=$3 AND idempotency_key=$4`,
		scope.TenantID, scope.Country, scope.CustomerID, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, false, nil
	}
	if err != nil {
		return Payment{}, false, fmt.Errorf("load payment command: %w", err)
	}
	if storedFingerprint != fingerprint {
		return Payment{}, false, ErrIdempotencyConflict
	}
	var value Payment
	if json.Unmarshal(payload, &value) != nil {
		return Payment{}, false, fmt.Errorf("decode payment command: %w", ErrInvalidRequest)
	}
	value.scope = scope
	return value, true, nil
}

func storePaymentCommand(ctx context.Context, tx pgx.Tx, scope Scope, key, fingerprint string, value Payment, now time.Time) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode payment command: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment.command_records
			(tenant_id,country,customer_identity_id,idempotency_key,request_fingerprint,payment_id,response_payload,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`,
		scope.TenantID, scope.Country, scope.CustomerID, key, fingerprint, value.ID, payload, now); err != nil {
		return fmt.Errorf("store payment command: %w", err)
	}
	return nil
}

func recordReconciliationException(ctx context.Context, tx pgx.Tx, paymentID, reason string, event ProviderEvent, now time.Time) error {
	payload, _ := json.Marshal(event)
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment.reconciliation_exceptions (id,payment_id,reason_code,provider_payload,created_at)
		VALUES ($1,$2,$3,$4::jsonb,$5)`, uuid.NewString(), paymentID, reason, payload, now); err != nil {
		return fmt.Errorf("record reconciliation exception: %w", err)
	}
	return nil
}

func paymentFingerprint(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func postgresPaymentScope(scope Scope) bool {
	return paymentUUID(scope.TenantID) && paymentUUID(scope.CustomerID) && len(scope.Country) == 2 && scope.Country == strings.ToUpper(scope.Country)
}

func paymentUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
