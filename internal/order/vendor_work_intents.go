package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxVendorWorkClaimBatch = 100
	maxVendorWorkAttempts   = 20
	vendorWorkLease         = time.Minute
)

var ErrVendorWorkClaimLost = errors.New("vendor work intent claim was lost")

// VendorWorkIntent is an immutable placed-order handoff plus its delivery
// lease. Lines contain only this vendor's checkout lines; shared delivery fees
// and wallet allocation are intentionally not assigned to a vendor here.
type VendorWorkIntent struct {
	ID                  string         `json:"id"`
	TenantID            string         `json:"tenant_id"`
	Country             string         `json:"country"`
	CustomerID          string         `json:"customer_id"`
	OrderID             string         `json:"order_id"`
	VendorID            string         `json:"vendor_id"`
	OrderRevision       int64          `json:"order_revision"`
	Lines               []LineSnapshot `json:"lines"`
	Currency            string         `json:"currency"`
	ProductTaxTreatment string         `json:"product_tax_treatment"`
	Attempts            int            `json:"attempts"`
	ClaimToken          string         `json:"claim_token"`
	ClaimUntil          time.Time      `json:"claim_until"`
	CreatedAt           time.Time      `json:"created_at"`
}

func queueVendorWorkIntents(ctx context.Context, tx pgx.Tx, value Order, now time.Time) error {
	byVendor := make(map[string][]LineSnapshot)
	for _, line := range value.Snapshot.Lines {
		byVendor[line.VendorID] = append(byVendor[line.VendorID], line)
	}
	vendorIDs := make([]string, 0, len(byVendor))
	for vendorID := range byVendor {
		vendorIDs = append(vendorIDs, vendorID)
	}
	sort.Strings(vendorIDs)
	for _, vendorID := range vendorIDs {
		lines, err := json.Marshal(byVendor[vendorID])
		if err != nil {
			return fmt.Errorf("encode vendor work lines: %w", err)
		}
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("order-vendor-work:"+value.ID+":"+vendorID)).String()
		if _, err := tx.Exec(ctx, `
			INSERT INTO ordering.vendor_work_intents
				(id,tenant_id,country,customer_identity_id,order_id,vendor_id,order_revision,
				 lines,currency,product_tax_treatment,next_attempt_at,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$11,$11)`,
			id, value.scope.TenantID, value.scope.Country, value.scope.CustomerID,
			value.ID, vendorID, value.Revision, lines, value.Snapshot.Total.Currency,
			value.Snapshot.ProductTaxTreatment, now); err != nil {
			return fmt.Errorf("queue vendor work intent: %w", err)
		}
	}
	return nil
}

// ClaimVendorWorkIntents leases at most 100 due intents. A worker must use the
// returned token when acknowledging or retrying. An expired lease can be
// reclaimed; the supply owner must therefore deduplicate by intent ID.
func (service *PostgresService) ClaimVendorWorkIntents(ctx context.Context, limit int) ([]VendorWorkIntent, error) {
	if ctx == nil || limit < 1 || limit > maxVendorWorkClaimBatch {
		return nil, ErrInvalidRequest
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin vendor work claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.clock().UTC()
	// A worker may crash on its final lease. Move those expired claims to the
	// observable dead-letter state in bounded batches as well.
	if _, err := tx.Exec(ctx, `
		WITH exhausted AS (
			SELECT id FROM ordering.vendor_work_intents
			WHERE status='PENDING' AND attempts >= $2
			  AND (claim_until IS NULL OR claim_until <= $1)
			ORDER BY next_attempt_at,created_at,id
			LIMIT $3 FOR UPDATE SKIP LOCKED
		)
		UPDATE ordering.vendor_work_intents AS intent
		SET status='DEAD_LETTER',dead_letter_at=$1,claim_token=NULL,claim_until=NULL,
		    last_error_code=COALESCE(last_error_code,'CLAIM_EXHAUSTED'),updated_at=$1
		FROM exhausted WHERE intent.id=exhausted.id`, now, maxVendorWorkAttempts, limit); err != nil {
		return nil, fmt.Errorf("expire vendor work claims: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text,tenant_id::text,country,customer_identity_id::text,
		       order_id::text,vendor_id,order_revision,lines,currency,
		       product_tax_treatment,attempts,created_at
		FROM ordering.vendor_work_intents
		WHERE status='PENDING' AND attempts < $2 AND next_attempt_at <= $1
		  AND (claim_until IS NULL OR claim_until <= $1)
		ORDER BY next_attempt_at,created_at,id
		LIMIT $3 FOR UPDATE SKIP LOCKED`, now, maxVendorWorkAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("select vendor work claims: %w", err)
	}
	values := make([]VendorWorkIntent, 0, limit)
	for rows.Next() {
		var value VendorWorkIntent
		var lines []byte
		if err := rows.Scan(&value.ID, &value.TenantID, &value.Country, &value.CustomerID,
			&value.OrderID, &value.VendorID, &value.OrderRevision, &lines, &value.Currency,
			&value.ProductTaxTreatment, &value.Attempts, &value.CreatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan vendor work claim: %w", err)
		}
		if err := json.Unmarshal(lines, &value.Lines); err != nil || len(value.Lines) == 0 {
			rows.Close()
			return nil, fmt.Errorf("decode vendor work lines: %w", ErrInvalidRequest)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate vendor work claims: %w", err)
	}
	rows.Close()
	for index := range values {
		values[index].Attempts++
		values[index].ClaimToken = uuid.NewString()
		values[index].ClaimUntil = now.Add(vendorWorkLease)
		if _, err := tx.Exec(ctx, `
			UPDATE ordering.vendor_work_intents
			SET attempts=$2,claim_token=$3,claim_until=$4,updated_at=$5
			WHERE id=$1`, values[index].ID, values[index].Attempts,
			values[index].ClaimToken, values[index].ClaimUntil, now); err != nil {
			return nil, fmt.Errorf("lease vendor work intent: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit vendor work claims: %w", err)
	}
	return values, nil
}

// AckVendorWorkIntent is safe after a lease expires if no other worker has
// reclaimed it. A stale token cannot acknowledge a newer worker's claim.
func (service *PostgresService) AckVendorWorkIntent(ctx context.Context, intentID, claimToken string) error {
	if ctx == nil || !orderUUID(intentID) || !orderUUID(claimToken) {
		return ErrInvalidRequest
	}
	now := service.clock().UTC()
	command, err := service.pool.Exec(ctx, `
		UPDATE ordering.vendor_work_intents
		SET status='DELIVERED',delivered_at=$3,claim_token=NULL,claim_until=NULL,
		    last_error_code=NULL,updated_at=$3
		WHERE id=$1 AND claim_token=$2 AND status='PENDING'`, intentID, claimToken, now)
	if err != nil {
		return fmt.Errorf("acknowledge vendor work intent: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrVendorWorkClaimLost
	}
	return nil
}

// RetryVendorWorkIntent records a stable machine-readable failure code and
// uses capped exponential backoff. The twentieth failed delivery is visible
// as DEAD_LETTER and requires explicit repair before replay.
func (service *PostgresService) RetryVendorWorkIntent(ctx context.Context, intentID, claimToken, errorCode string) error {
	if ctx == nil || !orderUUID(intentID) || !orderUUID(claimToken) || !validVendorWorkErrorCode(errorCode) {
		return ErrInvalidRequest
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin vendor work retry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT attempts FROM ordering.vendor_work_intents
		WHERE id=$1 AND claim_token=$2 AND status='PENDING' FOR UPDATE`, intentID, claimToken).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVendorWorkClaimLost
	}
	if err != nil {
		return fmt.Errorf("load vendor work retry: %w", err)
	}
	now := service.clock().UTC()
	status, nextAttempt, deadLetter := "PENDING", now.Add(vendorWorkRetryDelay(attempts)), any(nil)
	if attempts >= maxVendorWorkAttempts {
		status, deadLetter = "DEAD_LETTER", now
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ordering.vendor_work_intents
		SET status=$2,next_attempt_at=$3,claim_token=NULL,claim_until=NULL,
		    dead_letter_at=$4,last_error_code=$5,updated_at=$6
		WHERE id=$1`, intentID, status, nextAttempt, deadLetter, errorCode, now); err != nil {
		return fmt.Errorf("release vendor work intent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit vendor work retry: %w", err)
	}
	return nil
}

func vendorWorkRetryDelay(attempts int) time.Duration {
	return min(time.Second<<min(attempts, 12), time.Hour)
}

func validVendorWorkErrorCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}
