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
	query := `SELECT id::text,region_id,franchise_identity_id::text,name,center_latitude,center_longitude,radius_km,postal_codes,tenant_id::text,country FROM fulfillment.territories WHERE tenant_id=$1 AND country=$2`
	args := []any{actor.TenantID, actor.Country}
	if hasRole(actor, "FRANCHISE_ADMIN") {
		query += ` AND franchise_identity_id=$3`
		args = append(args, actor.Subject)
	}
	query += ` ORDER BY name`
	rows, err := service.pool.Query(ctx, query, args...)
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
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return FieldCheckIn{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
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
	if !postgresFulfillmentActor(actor) || !fulfillmentIsUUID(subjectID) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN", "REGIONAL_ADMIN", "FRANCHISE_ADMIN") && actor.Subject != subjectID {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `SELECT id::text,subject_identity_id::text,session_id::text,kind,coalesce(latitude,0),coalesce(longitude,0),recorded_at,source,tenant_id::text,country FROM fulfillment.attendance_entries WHERE tenant_id=$1 AND country=$2 AND subject_identity_id=$3 ORDER BY recorded_at DESC`, actor.TenantID, actor.Country, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []AttendanceEntry{}
	for rows.Next() {
		var value AttendanceEntry
		if err := rows.Scan(&value.ID, &value.SubjectID, &value.SessionID, &value.Kind, &value.Point.Latitude, &value.Point.Longitude, &value.RecordedAt, &value.Source, &value.tenantID, &value.country); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
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
	query := `SELECT id::text,postal_codes FROM fulfillment.territories WHERE tenant_id=$1 AND country=$2 AND region_id=$3`
	args := []any{actor.TenantID, actor.Country, regionID}
	if hasRole(actor, "FRANCHISE_ADMIN") {
		query += ` AND franchise_identity_id=$4`
		args = append(args, actor.Subject)
	}
	rows, err := service.pool.Query(ctx, query, args...)
	if err != nil {
		return RegionalDashboard{}, err
	}
	territories := []string{}
	zones := []string{}
	for rows.Next() {
		var id string
		var postalCodes []string
		if err := rows.Scan(&id, &postalCodes); err != nil {
			rows.Close()
			return RegionalDashboard{}, err
		}
		territories = append(territories, id)
		zones = append(zones, postalCodes...)
	}
	rows.Close()
	if len(territories) == 0 {
		return RegionalDashboard{}, ErrForbidden
	}
	now := service.Now()
	result := RegionalDashboard{RegionID: regionID, Territories: len(territories), PayoutsPending: Money{Currency: "INR"}, GeneratedAt: now}
	err = service.pool.QueryRow(ctx, `SELECT count(DISTINCT rider_identity_id) FROM fulfillment.duty_sessions WHERE tenant_id=$1 AND country=$2 AND status='ACTIVE' AND zone_id=ANY($3)`, actor.TenantID, actor.Country, zones).Scan(&result.OnlineRiders)
	if err != nil {
		return RegionalDashboard{}, err
	}
	err = service.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('READY_FOR_DISPATCH','OFFERED','REASSIGNMENT_REQUIRED')),count(*) FILTER (WHERE status='ASSIGNED' AND (assigned_rider_identity_id IS NULL OR NOT EXISTS(SELECT 1 FROM fulfillment.rider_locations l WHERE l.rider_identity_id=delivery_tasks.assigned_rider_identity_id AND l.expires_at>$4))),count(*) FILTER (WHERE status='DELIVERED' AND delivered_at>=$5) FROM fulfillment.delivery_tasks WHERE tenant_id=$1 AND country=$2 AND territory_id=ANY($3)`, actor.TenantID, actor.Country, territories, now, now.Truncate(24*time.Hour)).Scan(&result.UnassignedTasks, &result.AtRiskTasks, &result.CompletedToday)
	if err != nil {
		return RegionalDashboard{}, err
	}
	_ = service.pool.QueryRow(ctx, `SELECT coalesce(sum(amount_minor),0),coalesce(max(currency),'INR') FROM fulfillment.payouts WHERE tenant_id=$1 AND country=$2 AND status NOT IN ('PAID','FAILED')`, actor.TenantID, actor.Country).Scan(&result.PayoutsPending.AmountMinor, &result.PayoutsPending.Currency)
	locationRows, err := service.pool.Query(ctx, `SELECT rider_identity_id::text,sequence,latitude,longitude,accuracy_m,updated_at,expires_at,tenant_id::text,country FROM fulfillment.rider_locations WHERE tenant_id=$1 AND country=$2 AND expires_at>$3 AND rider_identity_id IN (SELECT rider_identity_id FROM fulfillment.duty_sessions WHERE tenant_id=$1 AND country=$2 AND status='ACTIVE' AND zone_id=ANY($4)) ORDER BY updated_at DESC`, actor.TenantID, actor.Country, now, zones)
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
	locationRows.Close()
	checkRows, err := service.pool.Query(ctx, `SELECT id::text,officer_identity_id::text,territory_id::text,latitude,longitude,distance_m,recorded_at,tenant_id::text,country FROM fulfillment.field_check_ins WHERE tenant_id=$1 AND country=$2 AND territory_id=ANY($3) ORDER BY recorded_at DESC LIMIT 20`, actor.TenantID, actor.Country, territories)
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
	checkRows.Close()
	return result, nil
}

func (service *PostgresService) Audits(actor Actor) ([]AuditEvent, error) {
	if !postgresFulfillmentActor(actor) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		if !actor.MFAVerified {
			return nil, ErrMFARequired
		}
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), fulfillmentOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `SELECT id::text,actor_identity_id::text,action,resource_type,resource_id,coalesce(reason,''),created_at,tenant_id::text,country FROM fulfillment.audit_events WHERE tenant_id=$1 AND country=$2 ORDER BY created_at DESC LIMIT 1000`, actor.TenantID, actor.Country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []AuditEvent{}
	for rows.Next() {
		var value AuditEvent
		if err := rows.Scan(&value.ID, &value.ActorID, &value.Action, &value.ResourceType, &value.ResourceID, &value.Reason, &value.CreatedAt, &value.tenantID, &value.country); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
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
	rows, err := tx.Query(ctx, `UPDATE fulfillment.delivery_tasks t SET status='REASSIGNMENT_REQUIRED',revision=revision+1,updated_at=$4 WHERE tenant_id=$1 AND country=$2 AND region_id=$3 AND status='ASSIGNED' AND NOT EXISTS(SELECT 1 FROM fulfillment.rider_locations l WHERE l.rider_identity_id=t.assigned_rider_identity_id AND l.expires_at>$4) RETURNING id::text`, actor.TenantID, actor.Country, regionID, now)
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
