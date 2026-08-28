package fulfillment

import (
	"sort"
	"strings"
	"time"
)

func (service *Service) Territories(actor Actor) ([]Territory, error) {
	if !validActor(actor) || !hasAnyRole(actor, "FRANCHISE_ADMIN", "REGIONAL_ADMIN", "FIELD_OFFICER", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []Territory{}
	for _, value := range service.territories {
		if value.tenantID != actor.TenantID || value.country != actor.Country {
			continue
		}
		if hasRole(actor, "FRANCHISE_ADMIN") && value.FranchiseID != actor.Subject {
			continue
		}
		values = append(values, cloneTerritory(value))
	}
	return values, nil
}

func (service *Service) FieldCheckIn(actor Actor, key, territoryID string, point Point) (FieldCheckIn, bool, error) {
	if !validActor(actor) || !hasRole(actor, "FIELD_OFFICER") || !validKey(key) || !validPoint(point) {
		return FieldCheckIn{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	territory := service.territories[territoryID]
	if territory.ID == "" {
		return FieldCheckIn{}, false, ErrNotFound
	}
	if territory.tenantID != actor.TenantID || territory.country != actor.Country {
		return FieldCheckIn{}, false, ErrForbidden
	}
	fingerprint := digest(point)
	scope := idempotencyScope(actor, "field-checkin:"+territoryID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return FieldCheckIn{}, false, ErrIdempotencyConflict
		}
		return *service.fieldCheckIns[previous.resourceID], true, nil
	}
	distance := haversineMeters(point, territory.Center)
	if distance > territory.RadiusKM*1000 {
		return FieldCheckIn{}, false, ErrForbidden
	}
	service.sequence++
	value := FieldCheckIn{ID: "field-checkin-" + sequenceID(service.sequence), OfficerID: actor.Subject, TerritoryID: territory.ID, Point: point, DistanceM: distance, RecordedAt: service.clock().UTC(), tenantID: actor.TenantID, country: actor.Country}
	service.fieldCheckIns[value.ID] = &value
	service.recordAttendanceLocked(actor, value.ID, "FIELD_CHECK_IN", point, "FIELD_APP")
	service.recordAuditLocked(actor, "FIELD_CHECK_IN", "TERRITORY", territory.ID, "Geofence verified")
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return value, false, nil
}

func (service *Service) Attendance(actor Actor, subjectID string) ([]AttendanceEntry, error) {
	if !validActor(actor) || (!hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN", "REGIONAL_ADMIN", "FRANCHISE_ADMIN") && actor.Subject != subjectID) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []AttendanceEntry{}
	for _, value := range service.attendance {
		if value.SubjectID == subjectID && value.tenantID == actor.TenantID && value.country == actor.Country {
			values = append(values, *value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].RecordedAt.After(values[j].RecordedAt) })
	return values, nil
}

func (service *Service) RegionalDashboard(actor Actor, regionID string) (RegionalDashboard, error) {
	if !validActor(actor) || !hasAnyRole(actor, "FRANCHISE_ADMIN", "REGIONAL_ADMIN", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || !safeID(regionID) {
		if !actor.MFAVerified {
			return RegionalDashboard{}, ErrMFARequired
		}
		return RegionalDashboard{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.clock().UTC()
	result := RegionalDashboard{RegionID: regionID, PayoutsPending: Money{Currency: "INR"}, GeneratedAt: now}
	allowedTerritories := map[string]bool{}
	for _, territory := range service.territories {
		if territory.tenantID != actor.TenantID || territory.country != actor.Country || territory.RegionID != regionID {
			continue
		}
		if hasRole(actor, "FRANCHISE_ADMIN") && territory.FranchiseID != actor.Subject {
			continue
		}
		allowedTerritories[territory.ID] = true
		result.Territories++
	}
	if len(allowedTerritories) == 0 {
		return RegionalDashboard{}, ErrForbidden
	}
	for riderID, duty := range service.duty {
		if duty.Status != "ACTIVE" || duty.tenantID != actor.TenantID || duty.country != actor.Country {
			continue
		}
		for _, territory := range service.territories {
			if allowedTerritories[territory.ID] && contains(territory.PostalCodes, duty.ZoneID) {
				result.OnlineRiders++
				if location := service.locations[riderID]; location != nil && location.ExpiresAt.After(now) {
					result.LatestLocations = append(result.LatestLocations, *location)
				}
				break
			}
		}
	}
	for _, task := range service.tasks {
		if !allowedTerritories[task.TerritoryID] {
			continue
		}
		switch task.Status {
		case "READY_FOR_DISPATCH", "OFFERED", "REASSIGNMENT_REQUIRED":
			result.UnassignedTasks++
		case "ASSIGNED":
			location := service.locations[task.AssignedRiderID]
			if location == nil || !location.ExpiresAt.After(now) {
				result.AtRiskTasks++
			}
		case "DELIVERED":
			if task.DeliveredAt != nil && sameDay(*task.DeliveredAt, now) {
				result.CompletedToday++
			}
		}
	}
	for _, payout := range service.payouts {
		if payout.tenantID == actor.TenantID && payout.country == actor.Country && payout.Status != "PAID" {
			result.PayoutsPending.AmountMinor += payout.Amount.AmountMinor
			result.PayoutsPending.Currency = payout.Amount.Currency
		}
	}
	for _, checkIn := range service.fieldCheckIns {
		if allowedTerritories[checkIn.TerritoryID] {
			result.RecentFieldCheckIns = append(result.RecentFieldCheckIns, *checkIn)
		}
	}
	sort.Slice(result.RecentFieldCheckIns, func(i, j int) bool {
		return result.RecentFieldCheckIns[i].RecordedAt.After(result.RecentFieldCheckIns[j].RecordedAt)
	})
	if len(result.RecentFieldCheckIns) > 20 {
		result.RecentFieldCheckIns = result.RecentFieldCheckIns[:20]
	}
	return result, nil
}

func (service *Service) Audits(actor Actor) ([]AuditEvent, error) {
	if !validActor(actor) || !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified {
		if !actor.MFAVerified {
			return nil, ErrMFARequired
		}
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []AuditEvent{}
	for _, event := range service.audit {
		if event.tenantID == actor.TenantID && event.country == actor.Country {
			values = append(values, *event)
		}
	}
	return values, nil
}

func (service *Service) SweepStaleAssignments(actor Actor, regionID, reason string) (int, error) {
	if !validActor(actor) || !hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN") || !actor.MFAVerified || len(strings.TrimSpace(reason)) < 8 {
		if !actor.MFAVerified {
			return 0, ErrMFARequired
		}
		return 0, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.clock().UTC()
	count := 0
	for _, task := range service.tasks {
		if task.RegionID != regionID || task.Status != "ASSIGNED" || task.tenantID != actor.TenantID || task.country != actor.Country {
			continue
		}
		location := service.locations[task.AssignedRiderID]
		if location != nil && location.ExpiresAt.After(now) {
			continue
		}
		task.Status, task.Revision, task.UpdatedAt = "REASSIGNMENT_REQUIRED", task.Revision+1, now
		task.AllowedActions = []string{"REASSIGN"}
		service.recordAuditLocked(actor, "STALE_ASSIGNMENT_FLAGGED", "DELIVERY_TASK", task.ID, reason)
		count++
	}
	return count, nil
}

func sameDay(first, second time.Time) bool {
	a, b := first.UTC(), second.UTC()
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
