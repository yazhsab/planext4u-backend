package fulfillment

import (
	"context"
	"github.com/jackc/pgx/v5"
)

func globalTerritoryOperator(actor Actor) bool { return hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") }

func (service *Service) memoryTerritoryAllowed(actor Actor, territory Territory, capability string) bool {
	if territory.tenantID != actor.TenantID || territory.country != actor.Country {
		return false
	}
	if globalTerritoryOperator(actor) {
		return true
	}
	if !actor.MFAVerified {
		return false
	}
	if capability == "resource.read" && hasRole(actor, "FRANCHISE_ADMIN") && territory.FranchiseID == actor.Subject {
		return true
	}
	return service.configuration.TerritoryAuthority != nil && service.configuration.TerritoryAuthority(actor, territory.ID, capability)
}

// Assignment changes and binding changes take the exclusive organization lock.
// Holding its shared counterpart until command commit prevents a revocation
// from racing a successful field command, including idempotency readback.
func authorizeFieldTerritory(ctx context.Context, tx pgx.Tx, actor Actor, territoryID string) error {
	if !actor.MFAVerified {
		return ErrMFARequired
	}
	if !fulfillmentIsUUID(actor.OperatorSessionID) || actor.OperatorRevision < 1 {
		return ErrForbidden
	}
	var operatorAllowed bool
	if err := tx.QueryRow(ctx, `SELECT admin.lock_fulfillment_operator($1,$2,$3,$4,$5)`, actor.TenantID, actor.Country, actor.Subject, actor.OperatorSessionID, actor.OperatorRevision).Scan(&operatorAllowed); err != nil {
		return err
	}
	if !operatorAllowed {
		return ErrForbidden
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":organization"); err != nil {
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT admin.fulfillment_territory_allowed($1,$2,$3,$4,'resource.command')`, actor.TenantID, actor.Country, actor.Subject, territoryID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
