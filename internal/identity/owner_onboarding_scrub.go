package identity

import "context"

// OwnerOnboardingScrub is partial erasure evidence. RETAINED means the owner
// still has operational, financial, audit or private-media data. It must never
// be interpreted as a completed account deletion.
type OwnerOnboardingScrub struct {
	Outcome           string
	EvidenceReference string
	ApplicationRows   int
	ProfileRows       int
	DocumentRows      int
	SubmissionRows    int
}

// ScrubSupplyOnboarding is opt-in. It is not invoked by the scheduled deletion
// coordinator or an HTTP route. The owner function derives tenant and subject
// from a due deletion request, enforces policy/hold/task readiness, and records
// a RETAINED receipt atomically with bounded onboarding redaction.
func (repository *PostgresRepository) ScrubSupplyOnboarding(ctx context.Context, requestID string) (OwnerOnboardingScrub, error) {
	if !validSafeIdentifier(requestID, 128) {
		return OwnerOnboardingScrub{}, ErrInvalidInput
	}
	var result OwnerOnboardingScrub
	err := repository.pool.QueryRow(ctx, `SELECT outcome,evidence_reference,application_rows,document_rows,submission_rows
	 FROM supply.privacy_scrub_onboarding($1)`, requestID).Scan(
		&result.Outcome, &result.EvidenceReference, &result.ApplicationRows,
		&result.DocumentRows, &result.SubmissionRows)
	if err != nil {
		return OwnerOnboardingScrub{}, err
	}
	return result, nil
}

// ScrubFulfillmentOnboarding has the same explicit, partial scope for rider
// profile and submission data. Live location, delivery, chat and ledger records
// are not part of this operation.
func (repository *PostgresRepository) ScrubFulfillmentOnboarding(ctx context.Context, requestID string) (OwnerOnboardingScrub, error) {
	if !validSafeIdentifier(requestID, 128) {
		return OwnerOnboardingScrub{}, ErrInvalidInput
	}
	var result OwnerOnboardingScrub
	err := repository.pool.QueryRow(ctx, `SELECT outcome,evidence_reference,profile_rows,submission_rows
	 FROM fulfillment.privacy_scrub_onboarding($1)`, requestID).Scan(
		&result.Outcome, &result.EvidenceReference, &result.ProfileRows, &result.SubmissionRows)
	if err != nil {
		return OwnerOnboardingScrub{}, err
	}
	return result, nil
}
