package adminops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresControlPlaneExecutor struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresControlPlaneExecutor(pool *pgxpool.Pool, clock func() time.Time) (*PostgresControlPlaneExecutor, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresControlPlaneExecutor{pool: pool, clock: clock}, nil
}

func (executor *PostgresControlPlaneExecutor) Ready(ctx context.Context) error {
	var ready bool
	err := executor.pool.QueryRow(ctx, `SELECT to_regclass('admin.domain_executions') IS NOT NULL AND to_regclass('governance.country_controls') IS NOT NULL AND to_regclass('booking.policies') IS NOT NULL AND to_regclass('food.policies') IS NOT NULL AND to_regclass('fulfillment.policies') IS NOT NULL AND to_regclass('emergency.policies') IS NOT NULL AND to_regclass('local_verticals.policies') IS NOT NULL AND to_regclass('social.policies') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check published control plane readiness: %w", err)
	}
	if !ready {
		return errors.New("published control plane schema is unavailable")
	}
	return nil
}

func (executor *PostgresControlPlaneExecutor) Execute(principal Principal, change Change) error {
	if !postgresPrincipal(principal) || principal.TenantID != change.TenantID || principal.Country != change.Country || change.Status == StatusRejected {
		return ErrForbidden
	}
	encoded, err := json.Marshal(change.Command)
	if err != nil {
		return ErrInvalidRequest
	}
	digest := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(digest[:])
	ctx, cancel := context.WithTimeout(context.Background(), adminOperationTimeout)
	defer cancel()
	tx, err := executor.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin control publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, change.ID); err != nil {
		return fmt.Errorf("lock control publication: %w", err)
	}
	var previous string
	err = tx.QueryRow(ctx, `SELECT request_fingerprint FROM admin.domain_executions WHERE change_id=$1`, change.ID).Scan(&previous)
	if err == nil {
		if previous != fingerprint {
			return ErrRevisionConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load control publication: %w", err)
	}
	now := executor.clock().UTC()
	switch change.Command.Domain {
	case DomainPolicy:
		err = executor.publishPolicy(ctx, tx, principal, change, now)
	case DomainCountry:
		err = executor.publishCountry(ctx, tx, principal, change, now)
	case DomainIntelligence:
		err = executor.publishInsight(ctx, tx, principal, change, now)
	case DomainContent:
		err = executor.moderateContent(ctx, tx, change, now)
	case DomainEmergency:
		err = executor.publishEmergencySLA(ctx, tx, change, now)
	default:
		return ErrInvalidRequest
	}
	if err != nil {
		return err
	}
	state := publishedControlState(change.Command)
	if err := persistPublishedDomainRecord(ctx, tx, principal, change, state, encoded, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO admin.domain_executions (change_id,request_fingerprint,executed_at) VALUES ($1,$2,$3)`, change.ID, fingerprint, now); err != nil {
		return fmt.Errorf("record control publication: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit control publication: %w", err)
	}
	return nil
}

func (executor *PostgresControlPlaneExecutor) publishPolicy(ctx context.Context, tx pgx.Tx, principal Principal, change Change, now time.Time) error {
	if change.Command.Action != ActionPolicyPublish && change.Command.Action != ActionPolicyUpsert {
		return ErrInvalidRequest
	}
	var envelope struct {
		Vertical string          `json:"vertical"`
		Policy   json.RawMessage `json:"policy"`
	}
	if !decodeControlPayload(change.Command.Payload, &envelope) || len(envelope.Policy) == 0 {
		return ErrInvalidRequest
	}
	switch strings.ToUpper(strings.TrimSpace(envelope.Vertical)) {
	case "BOOKING", "SERVICES":
		var value struct {
			Version                   string `json:"version"`
			HoldTTLSeconds            int    `json:"hold_ttl_seconds"`
			CancellationCutoffSeconds int    `json:"cancellation_cutoff_seconds"`
			MaximumFreeReschedules    int    `json:"maximum_free_reschedules"`
			StartOTPValiditySeconds   int    `json:"start_otp_validity_seconds"`
			CompletionConfirmSeconds  int    `json:"completion_confirm_seconds"`
			WalletPointValueMinor     int64  `json:"wallet_point_value_minor"`
		}
		if !decodeRawControl(envelope.Policy, &value) || value.Version == "" {
			return ErrInvalidRequest
		}
		_, err := tx.Exec(ctx, `INSERT INTO booking.policies (tenant_id,country,version,hold_ttl_seconds,cancellation_cutoff_seconds,maximum_free_reschedules,start_otp_validity_seconds,completion_confirm_seconds,wallet_point_value_minor,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,hold_ttl_seconds=excluded.hold_ttl_seconds,cancellation_cutoff_seconds=excluded.cancellation_cutoff_seconds,maximum_free_reschedules=excluded.maximum_free_reschedules,start_otp_validity_seconds=excluded.start_otp_validity_seconds,completion_confirm_seconds=excluded.completion_confirm_seconds,wallet_point_value_minor=excluded.wallet_point_value_minor,updated_at=excluded.updated_at`, change.TenantID, change.Country, value.Version, value.HoldTTLSeconds, value.CancellationCutoffSeconds, value.MaximumFreeReschedules, value.StartOTPValiditySeconds, value.CompletionConfirmSeconds, value.WalletPointValueMinor, now)
		return mapPublishedControlError(err)
	case "FOOD":
		var value struct {
			Version               string `json:"version"`
			CartTTLSeconds        int    `json:"cart_ttl_seconds"`
			AcceptanceTTLSeconds  int    `json:"acceptance_ttl_seconds"`
			TaxBasisPoints        int    `json:"tax_basis_points"`
			WalletPointValueMinor int64  `json:"wallet_point_value_minor"`
		}
		if !decodeRawControl(envelope.Policy, &value) || value.Version == "" {
			return ErrInvalidRequest
		}
		_, err := tx.Exec(ctx, `INSERT INTO food.policies (tenant_id,country,version,cart_ttl_seconds,acceptance_ttl_seconds,tax_basis_points,wallet_point_value_minor,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,cart_ttl_seconds=excluded.cart_ttl_seconds,acceptance_ttl_seconds=excluded.acceptance_ttl_seconds,tax_basis_points=excluded.tax_basis_points,wallet_point_value_minor=excluded.wallet_point_value_minor,updated_at=excluded.updated_at`, change.TenantID, change.Country, value.Version, value.CartTTLSeconds, value.AcceptanceTTLSeconds, value.TaxBasisPoints, value.WalletPointValueMinor, now)
		return mapPublishedControlError(err)
	case "FULFILLMENT", "DRIVER":
		var value struct {
			Version                  string `json:"version"`
			OfferTTLSeconds          int    `json:"offer_ttl_seconds"`
			LocationTTLSeconds       int    `json:"location_ttl_seconds"`
			ChatAfterDeliverySeconds int    `json:"chat_after_delivery_seconds"`
			SettlementCoolingSeconds int    `json:"settlement_cooling_seconds"`
			CommissionBasisPoints    int    `json:"commission_basis_points"`
			TaxBasisPoints           int    `json:"tax_basis_points"`
		}
		if !decodeRawControl(envelope.Policy, &value) || value.Version == "" {
			return ErrInvalidRequest
		}
		_, err := tx.Exec(ctx, `INSERT INTO fulfillment.policies (tenant_id,country,version,offer_ttl_seconds,location_ttl_seconds,chat_after_delivery_seconds,settlement_cooling_seconds,commission_basis_points,tax_basis_points,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,offer_ttl_seconds=excluded.offer_ttl_seconds,location_ttl_seconds=excluded.location_ttl_seconds,chat_after_delivery_seconds=excluded.chat_after_delivery_seconds,settlement_cooling_seconds=excluded.settlement_cooling_seconds,commission_basis_points=excluded.commission_basis_points,tax_basis_points=excluded.tax_basis_points,updated_at=excluded.updated_at`, change.TenantID, change.Country, value.Version, value.OfferTTLSeconds, value.LocationTTLSeconds, value.ChatAfterDeliverySeconds, value.SettlementCoolingSeconds, value.CommissionBasisPoints, value.TaxBasisPoints, now)
		return mapPublishedControlError(err)
	case "EMERGENCY":
		var value struct {
			Version               string `json:"version"`
			AssignmentSLASeconds  int    `json:"assignment_sla_seconds"`
			LocationMaxAgeSeconds int    `json:"location_max_age_seconds"`
		}
		if !decodeRawControl(envelope.Policy, &value) || value.Version == "" {
			return ErrInvalidRequest
		}
		_, err := tx.Exec(ctx, `INSERT INTO emergency.policies (tenant_id,country,version,assignment_sla_seconds,location_max_age_seconds,updated_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,assignment_sla_seconds=excluded.assignment_sla_seconds,location_max_age_seconds=excluded.location_max_age_seconds,updated_at=excluded.updated_at`, change.TenantID, change.Country, value.Version, value.AssignmentSLASeconds, value.LocationMaxAgeSeconds, now)
		return mapPublishedControlError(err)
	case "MARKETPLACE", "HOMES", "CLASSIFIEDS":
		var value struct {
			Version                   string   `json:"version"`
			Currency                  string   `json:"currency"`
			EstimatorVersion          string   `json:"estimator_version"`
			ReviewTerms               []string `json:"review_terms"`
			ClassifiedLifetimeSeconds int      `json:"classified_lifetime_seconds"`
			FeatureLifetimeSeconds    int      `json:"feature_lifetime_seconds"`
			ReportReviewThreshold     int      `json:"report_review_threshold"`
		}
		if !decodeRawControl(envelope.Policy, &value) || value.Version == "" {
			return ErrInvalidRequest
		}
		_, err := tx.Exec(ctx, `INSERT INTO local_verticals.policies (tenant_id,country,version,currency,estimator_version,review_terms,classified_lifetime_seconds,feature_lifetime_seconds,report_review_threshold,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,currency=excluded.currency,estimator_version=excluded.estimator_version,review_terms=excluded.review_terms,classified_lifetime_seconds=excluded.classified_lifetime_seconds,feature_lifetime_seconds=excluded.feature_lifetime_seconds,report_review_threshold=excluded.report_review_threshold,updated_at=excluded.updated_at`, change.TenantID, change.Country, value.Version, value.Currency, value.EstimatorVersion, value.ReviewTerms, value.ClassifiedLifetimeSeconds, value.FeatureLifetimeSeconds, value.ReportReviewThreshold, now)
		return mapPublishedControlError(err)
	case "SOCIAL", "SOCIO":
		var value struct {
			Version               string   `json:"version"`
			RankingModel          string   `json:"ranking_model"`
			ReviewTerms           []string `json:"review_terms"`
			StoryTTLSeconds       int      `json:"story_ttl_seconds"`
			ReelTTLSeconds        int      `json:"reel_ttl_seconds"`
			MediaRetentionSeconds int      `json:"media_retention_seconds"`
			PresenceTTLSeconds    int      `json:"presence_ttl_seconds"`
			CallTTLSeconds        int      `json:"call_ttl_seconds"`
			LikeRewardPoints      int      `json:"like_reward_points"`
			FollowRewardPoints    int      `json:"follow_reward_points"`
			ShareRewardPoints     int      `json:"share_reward_points"`
			RewardExpirySeconds   int      `json:"reward_expiry_seconds"`
		}
		if !decodeRawControl(envelope.Policy, &value) || value.Version == "" {
			return ErrInvalidRequest
		}
		_, err := tx.Exec(ctx, `INSERT INTO social.policies (tenant_id,country,version,ranking_model,review_terms,story_ttl_seconds,reel_ttl_seconds,media_retention_seconds,presence_ttl_seconds,call_ttl_seconds,like_reward_points,follow_reward_points,share_reward_points,reward_expiry_seconds,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,ranking_model=excluded.ranking_model,review_terms=excluded.review_terms,story_ttl_seconds=excluded.story_ttl_seconds,reel_ttl_seconds=excluded.reel_ttl_seconds,media_retention_seconds=excluded.media_retention_seconds,presence_ttl_seconds=excluded.presence_ttl_seconds,call_ttl_seconds=excluded.call_ttl_seconds,like_reward_points=excluded.like_reward_points,follow_reward_points=excluded.follow_reward_points,share_reward_points=excluded.share_reward_points,reward_expiry_seconds=excluded.reward_expiry_seconds,updated_at=excluded.updated_at`, change.TenantID, change.Country, value.Version, value.RankingModel, value.ReviewTerms, value.StoryTTLSeconds, value.ReelTTLSeconds, value.MediaRetentionSeconds, value.PresenceTTLSeconds, value.CallTTLSeconds, value.LikeRewardPoints, value.FollowRewardPoints, value.ShareRewardPoints, value.RewardExpirySeconds, now)
		return mapPublishedControlError(err)
	default:
		return ErrInvalidRequest
	}
}

func (executor *PostgresControlPlaneExecutor) publishCountry(ctx context.Context, tx pgx.Tx, principal Principal, change Change, now time.Time) error {
	if change.Command.Action != ActionCountryUpdate {
		return ErrInvalidRequest
	}
	var value struct {
		Currency         string          `json:"currency"`
		Locales          []string        `json:"locales"`
		FeatureFlags     map[string]bool `json:"feature_flags"`
		PolicyVersion    string          `json:"policy_version"`
		ExpectedRevision int64           `json:"expected_revision"`
	}
	if !decodeControlPayload(change.Command.Payload, &value) || len(value.Currency) != 3 || len(value.Locales) == 0 || len(value.FeatureFlags) == 0 || value.PolicyVersion == "" || value.ExpectedRevision < 0 {
		return ErrInvalidRequest
	}
	flags, _ := json.Marshal(value.FeatureFlags)
	revision := value.ExpectedRevision + 1
	if value.ExpectedRevision == 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO governance.country_controls (tenant_id,country,currency,locales,feature_flags,policy_version,revision,published_at,published_by_identity_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, change.TenantID, change.Country, strings.ToUpper(value.Currency), value.Locales, flags, value.PolicyVersion, revision, now, principal.SubjectID); err != nil {
			if isUniqueViolation(err) {
				return ErrRevisionConflict
			}
			return mapPublishedControlError(err)
		}
	} else {
		tag, err := tx.Exec(ctx, `UPDATE governance.country_controls SET currency=$3,locales=$4,feature_flags=$5,policy_version=$6,revision=revision+1,published_at=$7,published_by_identity_id=$8 WHERE tenant_id=$1 AND country=$2 AND revision=$9`, change.TenantID, change.Country, strings.ToUpper(value.Currency), value.Locales, flags, value.PolicyVersion, now, principal.SubjectID, value.ExpectedRevision)
		if err != nil {
			return mapPublishedControlError(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrRevisionConflict
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO governance.publication_audit (tenant_id,country,policy_version,revision,published_by_identity_id,correlation_id,published_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, change.TenantID, change.Country, value.PolicyVersion, revision, principal.SubjectID, change.Command.CorrelationID, now)
	return err
}

func (executor *PostgresControlPlaneExecutor) publishInsight(ctx context.Context, tx pgx.Tx, _ Principal, change Change, now time.Time) error {
	if change.Command.Action == ActionIntelligenceDismiss {
		tag, err := tx.Exec(ctx, `UPDATE governance.insights SET published=false WHERE tenant_id=$1 AND country=$2 AND id=$3`, change.TenantID, change.Country, change.Command.TargetID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidRequest
		}
		return nil
	}
	if change.Command.Action != ActionIntelligencePublish {
		return ErrInvalidRequest
	}
	var value struct {
		Title       string    `json:"title"`
		Summary     string    `json:"summary"`
		Confidence  string    `json:"confidence"`
		Evidence    []string  `json:"evidence"`
		GeneratedAt time.Time `json:"generated_at"`
	}
	if !decodeControlPayload(change.Command.Payload, &value) || len(strings.TrimSpace(value.Title)) < 2 || len(strings.TrimSpace(value.Summary)) < 8 || !map[string]bool{"LOW": true, "MEDIUM": true, "HIGH": true}[strings.ToUpper(value.Confidence)] || len(value.Evidence) == 0 {
		return ErrInvalidRequest
	}
	if value.GeneratedAt.IsZero() {
		value.GeneratedAt = now
	}
	_, err := tx.Exec(ctx, `INSERT INTO governance.insights (tenant_id,country,id,title,summary,confidence,evidence,generated_at,published) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true) ON CONFLICT (tenant_id,country,id) DO UPDATE SET title=excluded.title,summary=excluded.summary,confidence=excluded.confidence,evidence=excluded.evidence,generated_at=excluded.generated_at,published=true`, change.TenantID, change.Country, change.Command.TargetID, strings.TrimSpace(value.Title), strings.TrimSpace(value.Summary), strings.ToUpper(value.Confidence), value.Evidence, value.GeneratedAt.UTC())
	return mapPublishedControlError(err)
}

func (executor *PostgresControlPlaneExecutor) moderateContent(ctx context.Context, tx pgx.Tx, change Change, now time.Time) error {
	var value struct {
		ResourceType string `json:"resource_type"`
		Decision     string `json:"decision"`
		Reason       string `json:"reason"`
	}
	if !decodeControlPayload(change.Command.Payload, &value) || len(strings.TrimSpace(value.Reason)) < 8 {
		return ErrInvalidRequest
	}
	decision := strings.ToUpper(strings.TrimSpace(value.Decision))
	switch strings.ToUpper(strings.TrimSpace(value.ResourceType)) {
	case "SOCIAL_POST":
		status := "REMOVED"
		if change.Command.Action == ActionContentRestore || decision == "RESTORE" {
			status = "PUBLISHED"
		}
		tag, err := tx.Exec(ctx, `UPDATE social.posts SET status=$4,moderation_reason=$5,revision=revision+1,updated_at=$6 WHERE tenant_id=$1 AND country=$2 AND id=$3`, change.TenantID, change.Country, change.Command.TargetID, status, strings.TrimSpace(value.Reason), now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidRequest
		}
		return nil
	case "CLASSIFIED":
		status := "REMOVED"
		if change.Command.Action == ActionContentRestore || decision == "RESTORE" {
			status = "PUBLISHED"
		}
		tag, err := tx.Exec(ctx, `UPDATE local_verticals.classified_listings SET status=$4,revision=revision+1,updated_at=$5 WHERE tenant_id=$1 AND country=$2 AND id=$3`, change.TenantID, change.Country, change.Command.TargetID, status, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidRequest
		}
		return nil
	default:
		return ErrInvalidRequest
	}
}

func (executor *PostgresControlPlaneExecutor) publishEmergencySLA(ctx context.Context, tx pgx.Tx, change Change, now time.Time) error {
	if change.Command.Action == ActionEmergencyEscalate {
		return ErrInvalidRequest
	}
	var value struct {
		Version               string `json:"version"`
		AssignmentSLASeconds  int    `json:"assignment_sla_seconds"`
		LocationMaxAgeSeconds int    `json:"location_max_age_seconds"`
	}
	if !decodeControlPayload(change.Command.Payload, &value) || value.Version == "" {
		return ErrInvalidRequest
	}
	_, err := tx.Exec(ctx, `INSERT INTO emergency.policies (tenant_id,country,version,assignment_sla_seconds,location_max_age_seconds,updated_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (tenant_id,country) DO UPDATE SET version=excluded.version,assignment_sla_seconds=excluded.assignment_sla_seconds,location_max_age_seconds=excluded.location_max_age_seconds,updated_at=excluded.updated_at`, change.TenantID, change.Country, value.Version, value.AssignmentSLASeconds, value.LocationMaxAgeSeconds, now)
	return mapPublishedControlError(err)
}

func persistPublishedDomainRecord(ctx context.Context, tx pgx.Tx, principal Principal, change Change, state string, payload []byte, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO admin.domain_records (tenant_id,country,domain,target_id,state,revision,payload,last_change,updated_by,updated_at) VALUES ($1,$2,$3,$4,$5,1,$6,$7,$8,$9) ON CONFLICT (tenant_id,country,domain,target_id) DO UPDATE SET state=excluded.state,revision=admin.domain_records.revision+1,payload=excluded.payload,last_change=excluded.last_change,updated_by=excluded.updated_by,updated_at=excluded.updated_at`, change.TenantID, change.Country, change.Command.Domain, change.Command.TargetID, state, payload, change.ID, principal.SubjectID, now)
	if err != nil {
		return fmt.Errorf("persist published domain record: %w", err)
	}
	return nil
}

func publishedControlState(command Command) string {
	switch command.Action {
	case ActionContentModerate:
		return "MODERATED"
	case ActionContentRestore:
		return "RESTORED"
	case ActionIntelligenceDismiss:
		return "DISMISSED"
	default:
		return "PUBLISHED"
	}
}

func decodeControlPayload(payload map[string]any, destination any) bool {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return decodeRawControl(encoded, destination)
}
func decodeRawControl(encoded []byte, destination any) bool {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination) == nil && decoder.Decode(&struct{}{}) == io.EOF
}
func mapPublishedControlError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("publish control configuration: %w", err)
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
