package privateevidence

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Claim joins the domain's transaction: a rejected/rolled-back application
// never leaves a retained orphan, and accepted documents cannot be deleted
// between the HTTP ownership check and the business commit.
func Claim(ctx context.Context, tx pgx.Tx, scope Scope, consumer, resource string, ids []string) error {
	query := ""
	switch consumer {
	case "VENDOR_APPLICATION":
		query = `SELECT media.claim_vendor_identity_evidence($1,$2,$3,$4,$5::uuid[])`
	case "RIDER_APPLICATION":
		query = `SELECT media.claim_rider_identity_evidence($1,$2,$3,$4,$5::uuid[])`
	default:
		return ErrRejected
	}
	var claimed bool
	if err := tx.QueryRow(ctx, query, scope.TenantID, scope.Country, scope.OwnerID, resource, ids).Scan(&claimed); err != nil {
		return ErrUnavailable
	}
	if !claimed {
		return ErrRejected
	}
	return nil
}
