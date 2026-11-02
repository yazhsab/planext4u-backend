package fulfillment

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (service *PostgresService) Territories(actor Actor) ([]Territory, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "FRANCHISE_ADMIN", "REGIONAL_ADMIN", "FIELD_OFFICER", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	global, err := fulfillmentReadScope(ctx, tx, actor)
	if err != nil {
		return nil, err
	}
	values, err := visibleFulfillmentTerritories(ctx, tx, actor, global)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapFulfillmentError(err)
	}
	return values, nil
}

// The admin-owned function locks the live account and source session. The
// caller holds this transaction through every regional counter read so a
// logout, role change or country switch cannot race the response.
func fulfillmentReadScope(ctx context.Context, tx pgx.Tx, actor Actor) (bool, error) {
	if !hasAnyRole(actor, "REGIONAL_ADMIN", "FIELD_OFFICER", "OPS_ADMIN", "SUPER_ADMIN") {
		return false, nil // Mobile franchise owner reads use their own identity.
	}
	if !actor.MFAVerified {
		return false, ErrMFARequired
	}
	if !fulfillmentIsUUID(actor.OperatorSessionID) || actor.OperatorRevision < 1 {
		return false, ErrForbidden
	}
	var scope *string
	if err := tx.QueryRow(ctx, `SELECT admin.fulfillment_read_scope($1,$2,$3,$4,$5)`, actor.TenantID, actor.Country, actor.Subject, actor.OperatorSessionID, actor.OperatorRevision).Scan(&scope); err != nil {
		return false, err
	}
	if scope == nil || (*scope != "COUNTRY" && *scope != "SCOPED") {
		return false, ErrForbidden
	}
	if *scope == "SCOPED" {
		// Assignment and binding changes take the exclusive organization lock.
		// Hold its shared counterpart through the response transaction, then
		// read assignments from a fresh read-committed statement snapshot.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":organization"); err != nil {
			return false, err
		}
	}
	return *scope == "COUNTRY", nil
}

func visibleFulfillmentTerritories(ctx context.Context, tx pgx.Tx, actor Actor, global bool) ([]Territory, error) {
	query := `SELECT id::text,region_id,franchise_identity_id::text,name,center_latitude,center_longitude,radius_km,postal_codes,tenant_id::text,country FROM fulfillment.territories WHERE tenant_id=$1 AND country=$2`
	args := []any{actor.TenantID, actor.Country}
	if !global {
		query += ` AND (admin.fulfillment_territory_allowed($1,$2,$3,id,'resource.read') OR ($4::boolean AND franchise_identity_id=$3))`
		args = append(args, actor.Subject, hasRole(actor, "FRANCHISE_ADMIN"))
	}
	query += ` ORDER BY name`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Territory{}
	for rows.Next() {
		var value Territory
		if err := rows.Scan(&value.ID, &value.RegionID, &value.FranchiseID, &value.Name, &value.Center.Latitude, &value.Center.Longitude, &value.RadiusKM, &value.PostalCodes, &value.tenantID, &value.country); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (service *PostgresService) FieldCheckIn(actor Actor, key, territoryID string, point Point) (FieldCheckIn, bool, error) {
	if !postgresFulfillmentActor(actor) || !hasRole(actor, "FIELD_OFFICER") || !validKey(key) || !fulfillmentIsUUID(territoryID) || !validPoint(point) {
		return FieldCheckIn{}, false, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	// Each statement must obtain a fresh snapshot after the shared organization
	// lock is acquired. A serializable snapshot taken while waiting for that lock
	// could retain a grant revoked by the lock holder. Territory rows, command
	// replay and authority are locked explicitly through command commit.
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return FieldCheckIn{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := authorizeFieldTerritory(ctx, tx, actor, territoryID); err != nil {
		return FieldCheckIn{}, false, err
	}
	operation := "field-checkin:" + territoryID
	if err := fulfillmentCommandLock(ctx, tx, actor, operation, key); err != nil {
		return FieldCheckIn{}, false, err
	}
	fingerprint := digest(point)
	var replay FieldCheckIn
	if found, err := loadFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return FieldCheckIn{}, false, err
	} else if found {
		return replay, true, nil
	}
	var territory Territory
	err = tx.QueryRow(ctx, `SELECT id::text,region_id,franchise_identity_id::text,name,center_latitude,center_longitude,radius_km,postal_codes,tenant_id::text,country FROM fulfillment.territories WHERE tenant_id=$1 AND country=$2 AND id=$3 FOR UPDATE`, actor.TenantID, actor.Country, territoryID).Scan(&territory.ID, &territory.RegionID, &territory.FranchiseID, &territory.Name, &territory.Center.Latitude, &territory.Center.Longitude, &territory.RadiusKM, &territory.PostalCodes, &territory.tenantID, &territory.country)
	if errors.Is(err, pgx.ErrNoRows) {
		return FieldCheckIn{}, false, ErrNotFound
	}
	if err != nil {
		return FieldCheckIn{}, false, err
	}
	distance := haversineMeters(point, territory.Center)
	if distance > territory.RadiusKM*1000 {
		return FieldCheckIn{}, false, ErrForbidden
	}
	now := service.Now()
	value := FieldCheckIn{ID: uuid.NewString(), OfficerID: actor.Subject, TerritoryID: territoryID, Point: point, DistanceM: distance, RecordedAt: now, tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO fulfillment.field_check_ins (id,tenant_id,country,officer_identity_id,territory_id,latitude,longitude,distance_m,recorded_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, value.ID, actor.TenantID, actor.Country, actor.Subject, territoryID, point.Latitude, point.Longitude, distance, now)
	if err != nil {
		return FieldCheckIn{}, false, err
	}
	if err := insertFulfillmentAttendance(ctx, tx, actor, value.ID, "FIELD_CHECK_IN", point, "FIELD_APP", now); err != nil {
		return FieldCheckIn{}, false, err
	}
	if err := insertFulfillmentAudit(ctx, tx, actor, "FIELD_CHECK_IN", "TERRITORY", territoryID, "Geofence verified", now); err != nil {
		return FieldCheckIn{}, false, err
	}
	if err := storeFulfillmentReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return FieldCheckIn{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FieldCheckIn{}, false, mapFulfillmentError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Attendance(actor Actor, subjectID string) ([]AttendanceEntry, error) {
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(subjectID) {
		return nil, ErrForbidden
	}
	operator := hasAnyRole(actor, "REGIONAL_ADMIN", "FIELD_OFFICER", "OPS_ADMIN", "SUPER_ADMIN")
	if actor.Subject != subjectID && !hasAnyRole(actor, "REGIONAL_ADMIN", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	global := false
	if operator {
		global, err = fulfillmentReadScope(ctx, tx, actor)
		if err != nil {
			return nil, err
		}
	}
	query := `SELECT a.id::text,a.subject_identity_id::text,a.session_id::text,a.kind,coalesce(a.latitude,0),coalesce(a.longitude,0),a.recorded_at,a.source,a.tenant_id::text,a.country FROM fulfillment.attendance_entries a WHERE a.tenant_id=$1 AND a.country=$2 AND a.subject_identity_id=$3`
	args := []any{actor.TenantID, actor.Country, subjectID}
	if actor.Subject != subjectID && !global {
		// A regional operator can inspect only field check-ins in a currently
		// assigned territory. Rider duty sessions have postal zones rather than
		// an exclusive territory and cannot safely establish ownership here.
		query += ` AND a.kind='FIELD_CHECK_IN' AND EXISTS(SELECT 1 FROM fulfillment.field_check_ins f WHERE f.id=a.session_id AND f.tenant_id=a.tenant_id AND f.country=a.country AND f.officer_identity_id=a.subject_identity_id AND admin.fulfillment_territory_allowed($1,$2,$4,f.territory_id,'resource.read'))`
		args = append(args, actor.Subject)
	}
	query += ` ORDER BY a.recorded_at DESC`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	values := []AttendanceEntry{}
	for rows.Next() {
		var value AttendanceEntry
		if err := rows.Scan(&value.ID, &value.SubjectID, &value.SessionID, &value.Kind, &value.Point.Latitude, &value.Point.Longitude, &value.RecordedAt, &value.Source, &value.tenantID, &value.country); err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mapFulfillmentError(err)
	}
	return values, nil
}

func (service *PostgresService) RegionalDashboard(actor Actor, regionID string) (RegionalDashboard, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "FRANCHISE_ADMIN", "REGIONAL_ADMIN", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !safeID(regionID) {
		if !actor.MFAVerified {
			return RegionalDashboard{}, ErrMFARequired
		}
		return RegionalDashboard{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return RegionalDashboard{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	global, err := fulfillmentReadScope(ctx, tx, actor)
	if err != nil {
		return RegionalDashboard{}, err
	}
	visible, err := visibleFulfillmentTerritories(ctx, tx, actor, global)
	if err != nil {
		return RegionalDashboard{}, err
	}
	territories := []string{}
	zones := []string{}
	for _, value := range visible {
		if value.RegionID != regionID {
			continue
		}
		territories = append(territories, value.ID)
		zones = append(zones, value.PostalCodes...)
	}
	if len(territories) == 0 {
		return RegionalDashboard{}, ErrForbidden
	}
	if !global {
		// Postal codes are shared operational indexes, not ownership. Suppress
		// live rider counts/locations for any code also used by an unseen territory.
		zoneRows, queryErr := tx.Query(ctx, `SELECT DISTINCT z FROM unnest($3::text[]) AS z WHERE NOT EXISTS(SELECT 1 FROM fulfillment.territories t WHERE t.tenant_id=$1 AND t.country=$2 AND NOT(t.id=ANY($4::uuid[])) AND z=ANY(t.postal_codes))`, actor.TenantID, actor.Country, zones, territories)
		if queryErr != nil {
			return RegionalDashboard{}, queryErr
		}
		zones = nil
		for zoneRows.Next() {
			var zone string
			if queryErr = zoneRows.Scan(&zone); queryErr != nil {
				zoneRows.Close()
				return RegionalDashboard{}, queryErr
			}
			zones = append(zones, zone)
		}
		queryErr = zoneRows.Err()
		zoneRows.Close()
		if queryErr != nil {
			return RegionalDashboard{}, queryErr
		}
	}
	now := service.Now()
	result := RegionalDashboard{RegionID: regionID, Territories: len(territories), GeneratedAt: now, LatestLocations: []RiderLocation{}, RecentFieldCheckIns: []FieldCheckIn{}}
	err = tx.QueryRow(ctx, `SELECT count(DISTINCT rider_identity_id) FROM fulfillment.duty_sessions WHERE tenant_id=$1 AND country=$2 AND status='ACTIVE' AND zone_id=ANY($3) AND identity.delivery_consent_allowed($1::text,$2,rider_identity_id::text,configuration.delivery_consent_policy($1,$2))`, actor.TenantID, actor.Country, zones).Scan(&result.OnlineRiders)
	if err != nil {
		return RegionalDashboard{}, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('READY_FOR_DISPATCH','OFFERED','REASSIGNMENT_REQUIRED')),count(*) FILTER (WHERE status='ASSIGNED' AND (assigned_rider_identity_id IS NULL OR NOT EXISTS(SELECT 1 FROM fulfillment.rider_location_history l WHERE l.rider_identity_id=delivery_tasks.assigned_rider_identity_id AND l.tenant_id=delivery_tasks.tenant_id AND l.country=delivery_tasks.country AND l.expires_at>$4))),count(*) FILTER (WHERE status='DELIVERED' AND delivered_at>=$5) FROM fulfillment.delivery_tasks WHERE tenant_id=$1 AND country=$2 AND territory_id=ANY($3)`, actor.TenantID, actor.Country, territories, now, now.Truncate(24*time.Hour)).Scan(&result.UnassignedTasks, &result.AtRiskTasks, &result.CompletedToday)
	if err != nil {
		return RegionalDashboard{}, err
	}
	// Payouts are account-wide and can cover multiple territories. Do not expose
	// the country total as a regional amount or manufacture a zero amount.
	if global {
		result.PayoutsPending = &Money{Currency: "INR"}
		if err = tx.QueryRow(ctx, `SELECT coalesce(sum(amount_minor),0),coalesce(max(currency),'INR') FROM fulfillment.payouts WHERE tenant_id=$1 AND country=$2 AND status NOT IN ('PAID','FAILED')`, actor.TenantID, actor.Country).Scan(&result.PayoutsPending.AmountMinor, &result.PayoutsPending.Currency); err != nil {
			return RegionalDashboard{}, err
		}
	}
	locationRows, err := tx.Query(ctx, `SELECT rider_identity_id::text,sequence,latitude,longitude,accuracy_m,updated_at,expires_at,tenant_id::text,country FROM fulfillment.rider_location_history WHERE tenant_id=$1 AND country=$2 AND expires_at>$3 AND identity.delivery_consent_allowed($1::text,$2,rider_identity_id::text,configuration.delivery_consent_policy($1,$2)) AND rider_identity_id IN (SELECT rider_identity_id FROM fulfillment.duty_sessions WHERE tenant_id=$1 AND country=$2 AND status='ACTIVE' AND zone_id=ANY($4)) ORDER BY updated_at DESC`, actor.TenantID, actor.Country, now, zones)
	if err != nil {
		return RegionalDashboard{}, err
	}
	for locationRows.Next() {
		var value RiderLocation
		if err := locationRows.Scan(&value.RiderID, &value.Sequence, &value.Point.Latitude, &value.Point.Longitude, &value.AccuracyM, &value.UpdatedAt, &value.ExpiresAt, &value.tenantID, &value.country); err != nil {
			locationRows.Close()
			return RegionalDashboard{}, err
		}
		result.LatestLocations = append(result.LatestLocations, value)
	}
	if err = locationRows.Err(); err != nil {
		locationRows.Close()
		return RegionalDashboard{}, err
	}
	locationRows.Close()
	checkRows, err := tx.Query(ctx, `SELECT id::text,officer_identity_id::text,territory_id::text,latitude,longitude,distance_m,recorded_at,tenant_id::text,country FROM fulfillment.field_check_ins WHERE tenant_id=$1 AND country=$2 AND territory_id=ANY($3) ORDER BY recorded_at DESC LIMIT 20`, actor.TenantID, actor.Country, territories)
	if err != nil {
		return RegionalDashboard{}, err
	}
	for checkRows.Next() {
		var value FieldCheckIn
		if err := checkRows.Scan(&value.ID, &value.OfficerID, &value.TerritoryID, &value.Point.Latitude, &value.Point.Longitude, &value.DistanceM, &value.RecordedAt, &value.tenantID, &value.country); err != nil {
			checkRows.Close()
			return RegionalDashboard{}, err
		}
		result.RecentFieldCheckIns = append(result.RecentFieldCheckIns, value)
	}
	if err = checkRows.Err(); err != nil {
		checkRows.Close()
		return RegionalDashboard{}, err
	}
	checkRows.Close()
	if err := tx.Commit(ctx); err != nil {
		return RegionalDashboard{}, mapFulfillmentError(err)
	}
	return result, nil
}

func (service *PostgresService) Audits(actor Actor) ([]AuditEvent, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "REGIONAL_ADMIN", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		if !actor.MFAVerified {
			return nil, ErrMFARequired
		}
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	global, err := fulfillmentReadScope(ctx, tx, actor)
	if err != nil {
		return nil, err
	}
	query := `SELECT e.id::text,e.actor_identity_id::text,e.action,e.resource_type,e.resource_id,coalesce(e.reason,''),e.created_at,e.tenant_id::text,e.country FROM fulfillment.audit_events e WHERE e.tenant_id=$1 AND e.country=$2`
	args := []any{actor.TenantID, actor.Country}
	if !global {
		// Only field check-ins have a fixed, nonsensitive audit reason and an
		// unambiguous territory resource. Other audit reasons can be freeform.
		query += ` AND e.action='FIELD_CHECK_IN' AND e.resource_type='TERRITORY' AND EXISTS(SELECT 1 FROM fulfillment.territories t WHERE t.tenant_id=e.tenant_id AND t.country=e.country AND t.id::text=e.resource_id AND admin.fulfillment_territory_allowed($1,$2,$3,t.id,'resource.read'))`
		args = append(args, actor.Subject)
	}
	query += ` ORDER BY e.created_at DESC LIMIT 1000`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	values := []AuditEvent{}
	for rows.Next() {
		var value AuditEvent
		if err := rows.Scan(&value.ID, &value.ActorID, &value.Action, &value.ResourceType, &value.ResourceID, &value.Reason, &value.CreatedAt, &value.tenantID, &value.country); err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, mapFulfillmentError(err)
	}
	return values, nil
}

func (service *PostgresService) SweepStaleAssignments(actor Actor, regionID, reason string) (int, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !safeID(regionID) || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return 0, ErrMFARequired
		}
		return 0, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := service.Now()
	rows, err := tx.Query(ctx, `UPDATE fulfillment.delivery_tasks t SET status='REASSIGNMENT_REQUIRED',revision=revision+1,updated_at=$4 WHERE tenant_id=$1 AND country=$2 AND region_id=$3 AND status='ASSIGNED' AND NOT EXISTS(SELECT 1 FROM fulfillment.rider_location_history l WHERE l.rider_identity_id=t.assigned_rider_identity_id AND l.tenant_id=t.tenant_id AND l.country=t.country AND l.expires_at>$4) RETURNING id::text`, actor.TenantID, actor.Country, regionID, now)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := insertFulfillmentAudit(ctx, tx, actor, "STALE_ASSIGNMENT_FLAGGED", "DELIVERY_TASK", id, reason, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, mapFulfillmentError(err)
	}
	return len(ids), nil
}
