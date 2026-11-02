package identity

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DeletionGovernor is populated only from an authenticated administrator
// session. The SQL commands recheck the live directory row, source session,
// country, revision and fresh MFA under locks before any state change.
type DeletionGovernor struct {
	TenantID          string
	Country           string
	SubjectID         string
	OperatorSessionID string
	OperatorRevision  int64
}

type DeletionPolicyProposal struct {
	ID               string
	Owner            string
	Version          string
	Retention        time.Duration
	ExpectedRevision int64
	ReasonReference  string
}

func validDeletionGovernor(actor DeletionGovernor) bool {
	return uuid.Validate(actor.TenantID) == nil && validCountry(actor.Country) &&
		uuid.Validate(actor.SubjectID) == nil && uuid.Validate(actor.OperatorSessionID) == nil &&
		actor.OperatorRevision > 0
}

func validGovernanceReason(reason string) bool {
	return reason == strings.TrimSpace(reason) && len(reason) >= 8 && len(reason) <= 256
}

// ProposeDeletionPolicy records a proposed, inactive tenant/country/owner
// retention version. Publication requires a different, currently authorized
// administrator. This method never enables owner scrubs by itself.
func (repository *PostgresRepository) ProposeDeletionPolicy(ctx context.Context, actor DeletionGovernor, proposal DeletionPolicyProposal) (string, error) {
	if !validDeletionGovernor(actor) || uuid.Validate(proposal.ID) != nil ||
		!validSafeIdentifier(proposal.Owner, 128) || !validSafeIdentifier(proposal.Version, 128) ||
		proposal.Retention < 0 || proposal.Retention%time.Microsecond != 0 ||
		proposal.ExpectedRevision < 0 || !validGovernanceReason(proposal.ReasonReference) {
		return "", ErrInvalidInput
	}
	var id string
	err := repository.pool.QueryRow(ctx, `SELECT identity.propose_deletion_policy(
	 $1,$2,$3,$4,$5,$6::bigint * interval '1 microsecond',$7,$8,$9,$10,$11)`,
		proposal.ID, actor.TenantID, actor.Country, proposal.Owner, proposal.Version,
		proposal.Retention.Microseconds(), proposal.ExpectedRevision, proposal.ReasonReference,
		actor.SubjectID, actor.OperatorSessionID, actor.OperatorRevision).Scan(&id)
	return id, err
}

func (repository *PostgresRepository) PublishDeletionPolicy(ctx context.Context, actor DeletionGovernor, proposalID string) (int64, error) {
	if !validDeletionGovernor(actor) || uuid.Validate(proposalID) != nil {
		return 0, ErrInvalidInput
	}
	var revision int64
	err := repository.pool.QueryRow(ctx, `SELECT identity.publish_deletion_policy($1,$2,$3,$4,$5,$6)`,
		proposalID, actor.TenantID, actor.Country, actor.SubjectID,
		actor.OperatorSessionID, actor.OperatorRevision).Scan(&revision)
	return revision, err
}

// PlaceDeletionHold immediately blocks all owners for a scheduled request.
// It does not require a published retention policy.
func (repository *PostgresRepository) PlaceDeletionHold(ctx context.Context, actor DeletionGovernor, requestID, owner, reason string) (int64, error) {
	if !validDeletionGovernor(actor) || !validSafeIdentifier(requestID, 128) ||
		!validSafeIdentifier(owner, 128) || !validGovernanceReason(reason) {
		return 0, ErrInvalidInput
	}
	var holdID int64
	err := repository.pool.QueryRow(ctx, `SELECT identity.place_deletion_hold($1,$2,$3,$4,$5,$6,$7,$8)`,
		requestID, actor.TenantID, actor.Country, owner, reason,
		actor.SubjectID, actor.OperatorSessionID, actor.OperatorRevision).Scan(&holdID)
	return holdID, err
}

// ReleaseDeletionHold requires a different live administrator than the holder.
// Stale revisions and historical holds without an attested holder fail closed.
func (repository *PostgresRepository) ReleaseDeletionHold(ctx context.Context, actor DeletionGovernor, holdID, expectedRevision int64, reason string) (int64, error) {
	if !validDeletionGovernor(actor) || holdID < 1 || expectedRevision < 1 || !validGovernanceReason(reason) {
		return 0, ErrInvalidInput
	}
	var revision int64
	err := repository.pool.QueryRow(ctx, `SELECT identity.release_deletion_hold($1,$2,$3,$4,$5,$6,$7,$8)`,
		holdID, actor.TenantID, actor.Country, expectedRevision, reason,
		actor.SubjectID, actor.OperatorSessionID, actor.OperatorRevision).Scan(&revision)
	return revision, err
}
