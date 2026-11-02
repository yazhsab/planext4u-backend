package fulfillment

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// RegionalExport contains only operational counters. Country-wide payouts,
// rider identifiers/locations and other-person attendance are excluded.
type RegionalExport struct {
	Country         string    `json:"country"`
	RegionID        string    `json:"region_id"`
	Territories     int       `json:"territories"`
	OnlineRiders    int       `json:"online_riders"`
	UnassignedTasks int       `json:"unassigned_tasks"`
	AtRiskTasks     int       `json:"at_risk_tasks"`
	CompletedToday  int       `json:"completed_today"`
	GeneratedAt     time.Time `json:"generated_at"`
}

func (service *PostgresService) RegionalSummaryExport(actor Actor, regionID string) (RegionalExport, error) {
	if !postgresFulfillmentActor(actor) || !actor.MFAVerified || !safeID(regionID) || !fulfillmentIsUUID(actor.OperatorSessionID) || actor.OperatorRevision < 1 {
		return RegionalExport{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	// A fresh statement snapshot after waiting for authority locks sees any
	// revocation that committed while the export was queued.
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RegionalExport{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var scope *string
	if err = tx.QueryRow(ctx, `SELECT admin.fulfillment_export_scope($1,$2,$3,$4,$5)`, actor.TenantID, actor.Country, actor.Subject, actor.OperatorSessionID, actor.OperatorRevision).Scan(&scope); err != nil {
		return RegionalExport{}, err
	}
	if scope == nil || (*scope != "COUNTRY" && *scope != "SCOPED") {
		return RegionalExport{}, ErrForbidden
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":organization"); err != nil {
		return RegionalExport{}, err
	}
	global := *scope == "COUNTRY"
	rows, err := tx.Query(ctx, `SELECT id::text,postal_codes,($5::boolean OR admin.fulfillment_territory_allowed($1,$2,$3,id,'resource.export')) FROM fulfillment.territories WHERE tenant_id=$1 AND country=$2 AND region_id=$4 AND ($5::boolean OR admin.fulfillment_territory_allowed($1,$2,$3,id,'resource.read')) ORDER BY id`, actor.TenantID, actor.Country, actor.Subject, regionID, global)
	if err != nil {
		return RegionalExport{}, err
	}
	ids, zones := []string{}, []string{}
	for rows.Next() {
		var id string
		var postal []string
		var allowed bool
		if err = rows.Scan(&id, &postal, &allowed); err != nil {
			rows.Close()
			return RegionalExport{}, err
		}
		if !allowed {
			rows.Close()
			return RegionalExport{}, ErrForbidden
		}
		ids = append(ids, id)
		zones = append(zones, postal...)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RegionalExport{}, err
	}
	if len(ids) == 0 {
		return RegionalExport{}, ErrForbidden
	}
	if !global {
		// Shared postcodes cannot expose riders belonging to hidden territories.
		zoneRows, queryErr := tx.Query(ctx, `SELECT DISTINCT z FROM unnest($3::text[]) AS z WHERE NOT EXISTS(SELECT 1 FROM fulfillment.territories t WHERE t.tenant_id=$1 AND t.country=$2 AND NOT(t.id=ANY($4::uuid[])) AND z=ANY(t.postal_codes))`, actor.TenantID, actor.Country, zones, ids)
		if queryErr != nil {
			return RegionalExport{}, queryErr
		}
		zones = nil
		for zoneRows.Next() {
			var zone string
			if queryErr = zoneRows.Scan(&zone); queryErr != nil {
				zoneRows.Close()
				return RegionalExport{}, queryErr
			}
			zones = append(zones, zone)
		}
		queryErr = zoneRows.Err()
		zoneRows.Close()
		if queryErr != nil {
			return RegionalExport{}, queryErr
		}
	}
	now := service.Now()
	result := RegionalExport{Country: actor.Country, RegionID: regionID, Territories: len(ids), GeneratedAt: now}
	err = tx.QueryRow(ctx, `SELECT count(DISTINCT rider_identity_id) FROM fulfillment.duty_sessions WHERE tenant_id=$1 AND country=$2 AND status='ACTIVE' AND zone_id=ANY($3) AND identity.delivery_consent_allowed($1::text,$2,rider_identity_id::text,configuration.delivery_consent_policy($1,$2))`, actor.TenantID, actor.Country, zones).Scan(&result.OnlineRiders)
	if err != nil {
		return RegionalExport{}, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('READY_FOR_DISPATCH','OFFERED','REASSIGNMENT_REQUIRED')),count(*) FILTER (WHERE status='ASSIGNED' AND (assigned_rider_identity_id IS NULL OR NOT EXISTS(SELECT 1 FROM fulfillment.rider_location_history l WHERE l.rider_identity_id=delivery_tasks.assigned_rider_identity_id AND l.tenant_id=delivery_tasks.tenant_id AND l.country=delivery_tasks.country AND l.expires_at>$4))),count(*) FILTER (WHERE status='DELIVERED' AND delivered_at>=$5) FROM fulfillment.delivery_tasks WHERE tenant_id=$1 AND country=$2 AND territory_id=ANY($3)`, actor.TenantID, actor.Country, ids, now, now.Truncate(24*time.Hour)).Scan(&result.UnassignedTasks, &result.AtRiskTasks, &result.CompletedToday)
	if err != nil {
		return RegionalExport{}, err
	}
	if err = insertFulfillmentAudit(ctx, tx, actor, "REGIONAL_SUMMARY_EXPORTED", "REGION", regionID, "Current directory and territory export authority verified", now); err != nil {
		return RegionalExport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RegionalExport{}, mapFulfillmentError(err)
	}
	return result, nil
}
