package supply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const supplyOperationTimeout = 20 * time.Second

// PostgresService is the durable vendor, field-operations and catalog control
// plane. Every revision and idempotency response is committed with the domain
// mutation that produced it.
type PostgresService struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time) (*PostgresService, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock}, nil
}

func (service *PostgresService) Now() time.Time { return service.clock().UTC() }

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	if err := service.pool.QueryRow(ctx, `SELECT to_regclass('supply.vendor_applications') IS NOT NULL AND to_regclass('supply.application_timeline') IS NOT NULL AND to_regclass('supply.catalog_items') IS NOT NULL AND to_regclass('supply.idempotency_records') IS NOT NULL`).Scan(&ready); err != nil {
		return fmt.Errorf("check supply schema readiness: %w", err)
	}
	if !ready {
		return errors.New("supply schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Register(actor Actor, key string, request RegisterRequest) (Application, bool, error) {
	if !postgresSupplyActor(actor) || !hasRole(actor, "VENDOR") || !validRegister(request) || !validKey(key) {
		return Application{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Application{}, false, fmt.Errorf("begin vendor registration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := supplyCommandLock(ctx, tx, actor, "register", key); err != nil {
		return Application{}, false, err
	}
	fingerprint := digest(request)
	var replay Application
	if found, err := loadSupplyReplay(ctx, tx, actor, "register", key, fingerprint, &replay); err != nil {
		return Application{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return replay, true, nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supply.vendor_applications WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3)`, actor.TenantID, actor.Country, actor.Subject).Scan(&exists); err != nil {
		return Application{}, false, fmt.Errorf("check vendor registration: %w", err)
	}
	if exists {
		return Application{}, false, ErrConflict
	}
	now := service.Now()
	value := Application{ID: supplyUUID("application", actor.TenantID+":"+actor.Country+":"+actor.Subject), VendorID: actor.Subject, Revision: 1, Status: StatusRegistered, BusinessName: strings.TrimSpace(request.BusinessName), BusinessType: strings.TrimSpace(request.BusinessType), ContactName: strings.TrimSpace(request.ContactName), AllowedActions: []string{"SUBMIT_DOCUMENTS"}, Timeline: []TimelineEvent{{Status: StatusRegistered, Actor: actor.Subject, CreatedAt: now}}, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	if _, err := tx.Exec(ctx, `INSERT INTO supply.vendor_applications (id,tenant_id,country,vendor_identity_id,revision,status,business_name,business_type,contact_name,verified,created_at,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,false,$9,$9)`, value.ID, actor.TenantID, actor.Country, actor.Subject, value.Status, value.BusinessName, value.BusinessType, value.ContactName, now); err != nil {
		return Application{}, false, mapSupplyError(err)
	}
	if err := appendApplicationTimeline(ctx, tx, value.ID, value.Timeline); err != nil {
		return Application{}, false, err
	}
	if err := storeSupplyReplay(ctx, tx, actor, "register", key, fingerprint, value, now); err != nil {
		return Application{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Application{}, false, mapSupplyError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Application(actor Actor) (Application, error) {
	if !postgresSupplyActor(actor) || !hasRole(actor, "VENDOR") {
		return Application{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	value, err := service.loadApplicationByVendor(ctx, service.pool, actor.TenantID, actor.Country, actor.Subject, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, ErrNotFound
	}
	if err != nil {
		return Application{}, err
	}
	return roleApplication(actor, value), nil
}

func (service *PostgresService) SubmitDocuments(actor Actor, key string, revision int64, request DocumentsRequest) (Application, bool, error) {
	if len(request.Documents) < 2 || len(request.Documents) > 10 {
		return Application{}, false, ErrInvalidRequest
	}
	for _, document := range request.Documents {
		if !postgresSupplyUUID(document.AssetID) || !regexp.MustCompile(`^[A-Z_]{2,40}$`).MatchString(document.Kind) {
			return Application{}, false, ErrInvalidRequest
		}
	}
	return service.mutateApplication(actor, actor.Subject, key, revision, "documents", request, func(value *Application) error {
		if !hasRole(actor, "VENDOR") || value.Status != StatusRegistered {
			return ErrInvalidTransition
		}
		value.Documents = cloneDocuments(request.Documents)
		for index := range value.Documents {
			value.Documents[index].OCRStatus = "PENDING"
			value.Documents[index].ExtractedFields = nil
		}
		value.Status = StatusDocumentsSubmitted
		return nil
	})
}

func (service *PostgresService) Transition(actor Actor, key string, revision int64, request TransitionRequest) (Application, bool, error) {
	return service.TransitionForVendor(actor, key, actor.Subject, revision, request)
}

func (service *PostgresService) TransitionForVendor(actor Actor, key, vendorID string, revision int64, request TransitionRequest) (Application, bool, error) {
	if !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN", "FIELD_OFFICER", "FINANCE") || len(strings.TrimSpace(request.Reason)) < 8 {
		return Application{}, false, ErrForbidden
	}
	return service.mutateApplication(actor, vendorID, key, revision, "transition", request, func(value *Application) error {
		if !allowedApplicationTransition(value.Status, request.Status, actor) {
			return ErrInvalidTransition
		}
		if request.Status == StatusOCRReview {
			for index := range value.Documents {
				value.Documents[index].OCRStatus = "REVIEW_REQUIRED"
				value.Documents[index].ExtractedFields = map[string]string{"business_name": value.BusinessName}
			}
		}
		if request.Status == StatusKYCReview {
			for index := range value.Documents {
				value.Documents[index].OCRStatus = "VERIFIED"
			}
		}
		if request.Status == StatusApproved {
			if value.Bank == nil || value.Bank.Status != "VERIFIED" || len(value.Zones) == 0 || value.Visit == nil || value.Visit.CheckedInAt == nil {
				return ErrConflict
			}
			value.Verified = true
		}
		if request.Status == StatusRejected {
			value.Verified = false
		}
		value.Status = request.Status
		return nil
	})
}

func (service *PostgresService) ScheduleVisit(actor Actor, key string, revision int64, request VisitRequest) (Application, bool, error) {
	if !hasAnyRole(actor, "VENDOR", "OPS_ADMIN", "SUPER_ADMIN") || request.ScheduledAt.Before(service.Now().Add(time.Hour)) || !validPoint(request.Latitude, request.Longitude) || request.AllowedRadiusM < 20 || request.AllowedRadiusM > 2000 {
		return Application{}, false, ErrInvalidRequest
	}
	return service.mutateApplication(actor, actor.Subject, key, revision, "visit", request, func(value *Application) error {
		if value.Status != StatusFieldVisitRequired && value.Status != StatusFieldVisitScheduled {
			return ErrInvalidTransition
		}
		value.Visit = &FieldVisit{ID: supplyUUID("visit", value.ID+":"+key), ScheduledAt: request.ScheduledAt.UTC(), Latitude: request.Latitude, Longitude: request.Longitude, AllowedRadiusM: request.AllowedRadiusM}
		value.Status = StatusFieldVisitScheduled
		return nil
	})
}

func (service *PostgresService) FieldCheckIn(actor Actor, key, vendorID string, revision int64, request CheckInRequest) (Application, bool, error) {
	if !hasRole(actor, "FIELD_OFFICER") || !validPoint(request.Latitude, request.Longitude) {
		return Application{}, false, ErrForbidden
	}
	return service.mutateApplication(actor, vendorID, key, revision, "checkin", request, func(value *Application) error {
		if value.Status != StatusFieldVisitScheduled || value.Visit == nil {
			return ErrInvalidTransition
		}
		distance := haversineMeters(request.Latitude, request.Longitude, value.Visit.Latitude, value.Visit.Longitude)
		if distance > value.Visit.AllowedRadiusM {
			return ErrForbidden
		}
		now := service.Now()
		value.Visit.OfficerID, value.Visit.CheckedInAt, value.Visit.CheckInDistance = actor.Subject, &now, math.Round(distance)
		value.Status = StatusFieldVisitPassed
		return nil
	})
}

func (service *PostgresService) SetZones(actor Actor, key string, revision int64, zones []ServiceZone) (Application, bool, error) {
	if len(zones) == 0 || len(zones) > 20 {
		return Application{}, false, ErrInvalidRequest
	}
	for _, zone := range zones {
		if !postgresSupplyUUID(zone.ID) || !validPoint(zone.Latitude, zone.Longitude) || zone.RadiusKM <= 0 || zone.RadiusKM > 100 || len(zone.PostalCodes) == 0 || !safeID(zone.PolicyVersion) {
			return Application{}, false, ErrInvalidRequest
		}
	}
	return service.mutateApplication(actor, actor.Subject, key, revision, "zones", zones, func(value *Application) error {
		if !hasRole(actor, "VENDOR") || (value.Status != StatusFieldVisitPassed && value.Status != StatusBankReview) {
			return ErrInvalidTransition
		}
		value.Zones = cloneZones(zones)
		return nil
	})
}

func (service *PostgresService) SubmitBank(actor Actor, key string, revision int64, bank BankAccount) (Application, bool, error) {
	if !hasRole(actor, "VENDOR") || !regexp.MustCompile(`^bankref_[A-Za-z0-9_-]{8,100}$`).MatchString(bank.Reference) || !regexp.MustCompile(`^[0-9]{4}$`).MatchString(bank.Last4) || !regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`).MatchString(bank.IFSC) || len(strings.TrimSpace(bank.HolderName)) < 3 {
		return Application{}, false, ErrInvalidRequest
	}
	return service.mutateApplication(actor, actor.Subject, key, revision, "bank", bank, func(value *Application) error {
		if value.Status != StatusFieldVisitPassed && value.Status != StatusBankReview {
			return ErrInvalidTransition
		}
		bank.Status = "PENDING_VERIFICATION"
		value.Bank, value.Status = &bank, StatusBankReview
		return nil
	})
}

func (service *PostgresService) VerifyBank(actor Actor, key, vendorID string, revision int64, reason string) (Application, bool, error) {
	if !hasAnyRole(actor, "FINANCE", "SUPER_ADMIN") || len(strings.TrimSpace(reason)) < 8 {
		return Application{}, false, ErrForbidden
	}
	return service.mutateApplication(actor, vendorID, key, revision, "bank-verify", reason, func(value *Application) error {
		if value.Status != StatusBankReview || value.Bank == nil {
			return ErrInvalidTransition
		}
		value.Bank.Status = "VERIFIED"
		return nil
	})
}

func (service *PostgresService) UpsertCatalog(actor Actor, key, currentID string, revision int64, request CatalogRequest) (CatalogItem, bool, error) {
	if !postgresSupplyActor(actor) || !hasRole(actor, "VENDOR") || !validCatalog(request) || !validKey(key) || currentID != "" && !postgresSupplyUUID(currentID) {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return CatalogItem{}, false, fmt.Errorf("begin vendor catalog upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "catalog"
	if currentID != "" {
		operation += ":" + currentID
	}
	if err := supplyCommandLock(ctx, tx, actor, operation, key); err != nil {
		return CatalogItem{}, false, err
	}
	fingerprint := digest(struct {
		ID      string
		Request CatalogRequest
	}{currentID, request})
	var replay CatalogItem
	if found, err := loadSupplyReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return CatalogItem{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return replay, true, nil
	}
	application, err := service.loadApplicationByVendor(ctx, tx, actor.TenantID, actor.Country, actor.Subject, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !application.Verified {
		return CatalogItem{}, false, ErrForbidden
	}
	if err != nil {
		return CatalogItem{}, false, err
	}
	now := service.Now()
	value := CatalogItem{ID: currentID, VendorID: actor.Subject, Revision: 1, Stock: 0, tenantID: actor.TenantID, country: actor.Country}
	if currentID == "" {
		value.ID = supplyUUID("catalog", actor.TenantID+":"+actor.Country+":"+actor.Subject+":"+key)
	} else {
		value, err = loadCatalogItem(ctx, tx, currentID, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return CatalogItem{}, false, ErrNotFound
		}
		if err != nil {
			return CatalogItem{}, false, err
		}
		if value.tenantID != actor.TenantID || value.country != actor.Country || value.VendorID != actor.Subject {
			return CatalogItem{}, false, ErrForbidden
		}
		if value.Revision != revision {
			return CatalogItem{}, false, ErrConflict
		}
		value.Revision++
	}
	value.Kind, value.Name, value.Description, value.SKU, value.Price = request.Kind, strings.TrimSpace(request.Name), strings.TrimSpace(request.Description), strings.TrimSpace(request.SKU), request.Price
	value.ApprovalStatus, value.Active, value.UpdatedAt = "PENDING_APPROVAL", false, now
	value.AllowedActions = []string{"EDIT", "SET_INVENTORY", "SET_SCHEDULE"}
	if currentID == "" {
		_, err = tx.Exec(ctx, `INSERT INTO supply.catalog_items (id,tenant_id,country,vendor_identity_id,revision,kind,name,description,sku,price_minor,currency,stock,approval_status,active,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,0,$11,false,$12)`, value.ID, actor.TenantID, actor.Country, actor.Subject, value.Kind, value.Name, value.Description, value.SKU, value.Price.AmountMinor, value.Price.Currency, value.ApprovalStatus, now)
	} else {
		_, err = tx.Exec(ctx, `UPDATE supply.catalog_items SET revision=$2,kind=$3,name=$4,description=$5,sku=$6,price_minor=$7,currency=$8,approval_status=$9,active=false,updated_at=$10 WHERE id=$1`, value.ID, value.Revision, value.Kind, value.Name, value.Description, value.SKU, value.Price.AmountMinor, value.Price.Currency, value.ApprovalStatus, now)
	}
	if err != nil {
		return CatalogItem{}, false, mapSupplyError(err)
	}
	if err := storeSupplyReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return CatalogItem{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CatalogItem{}, false, mapSupplyError(err)
	}
	return value, false, nil
}

func (service *PostgresService) Catalog(actor Actor) ([]CatalogItem, error) {
	if !postgresSupplyActor(actor) || !hasAnyRole(actor, "VENDOR", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	query := `SELECT id::text FROM supply.catalog_items WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3 ORDER BY updated_at DESC`
	if hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") {
		query = `SELECT id::text FROM supply.catalog_items WHERE tenant_id=$1 AND country=$2 AND ($3::uuid IS NOT NULL) ORDER BY updated_at DESC`
	}
	rows, err := service.pool.Query(ctx, query, actor.TenantID, actor.Country, actor.Subject)
	if err != nil {
		return nil, fmt.Errorf("list vendor catalog: %w", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	result := make([]CatalogItem, 0, len(ids))
	for _, id := range ids {
		value, err := loadCatalogItem(ctx, service.pool, id, false)
		if err != nil {
			return nil, err
		}
		value.AllowedActions = catalogActions(actor, value)
		result = append(result, value)
	}
	return result, nil
}

func (service *PostgresService) ApproveCatalog(actor Actor, key, itemID string, revision int64, approved bool, reason string) (CatalogItem, bool, error) {
	if !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN") || len(strings.TrimSpace(reason)) < 8 {
		return CatalogItem{}, false, ErrForbidden
	}
	return service.mutateCatalog(actor, key, itemID, revision, "catalog-approval", struct {
		Approved bool
		Reason   string
	}{approved, reason}, func(value *CatalogItem) error {
		if approved {
			value.ApprovalStatus, value.Active = "APPROVED", true
		} else {
			value.ApprovalStatus, value.Active = "REJECTED", false
		}
		return nil
	})
}

func (service *PostgresService) SetInventory(actor Actor, key, itemID string, revision int64, stock int) (CatalogItem, bool, error) {
	if stock < 0 || stock > 1000000 {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	return service.mutateCatalog(actor, key, itemID, revision, "inventory", stock, func(value *CatalogItem) error {
		if !hasRole(actor, "VENDOR") || value.VendorID != actor.Subject {
			return ErrForbidden
		}
		value.Stock = stock
		return nil
	})
}

func (service *PostgresService) SetSchedule(actor Actor, key, itemID string, revision int64, windows []ScheduleWindow) (CatalogItem, bool, error) {
	if !validSchedule(windows) {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	return service.mutateCatalog(actor, key, itemID, revision, "schedule", windows, func(value *CatalogItem) error {
		if !hasRole(actor, "VENDOR") || value.VendorID != actor.Subject {
			return ErrForbidden
		}
		value.Schedules = append([]ScheduleWindow(nil), windows...)
		return nil
	})
}

func (service *PostgresService) Work(actor Actor) ([]WorkItem, error) {
	if !postgresSupplyActor(actor) || !hasRole(actor, "VENDOR") {
		return nil, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	rows, err := service.pool.Query(ctx, `SELECT id::text,vendor_identity_id::text,reference_type,reference_id::text,status,total_minor,currency,customer_label,updated_at FROM supply.work_items WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3 ORDER BY updated_at DESC`, actor.TenantID, actor.Country, actor.Subject)
	if err != nil {
		return nil, fmt.Errorf("list vendor work: %w", err)
	}
	defer rows.Close()
	result := []WorkItem{}
	for rows.Next() {
		var value WorkItem
		if err := rows.Scan(&value.ID, &value.VendorID, &value.ReferenceType, &value.ReferenceID, &value.Status, &value.Total.AmountMinor, &value.Total.Currency, &value.CustomerLabel, &value.UpdatedAt); err != nil {
			return nil, err
		}
		value.tenantID, value.country, value.AllowedActions = actor.TenantID, actor.Country, workActions(value.Status)
		result = append(result, value)
	}
	return result, rows.Err()
}

func (service *PostgresService) SeedWork(actor Actor, item WorkItem) error {
	if !postgresSupplyActor(actor) || !postgresSupplyUUID(item.ID) || !postgresSupplyUUID(item.VendorID) || !postgresSupplyUUID(item.ReferenceID) {
		return ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	_, err := service.pool.Exec(ctx, `INSERT INTO supply.work_items (id,tenant_id,country,vendor_identity_id,reference_type,reference_id,status,total_minor,currency,customer_label,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`, item.ID, actor.TenantID, actor.Country, item.VendorID, item.ReferenceType, item.ReferenceID, item.Status, item.Total.AmountMinor, item.Total.Currency, item.CustomerLabel, item.UpdatedAt)
	return err
}

func (service *PostgresService) TransitionWork(actor Actor, key, workID, status string) (WorkItem, bool, error) {
	if !postgresSupplyActor(actor) || !hasRole(actor, "VENDOR") || !validKey(key) || !postgresSupplyUUID(workID) {
		return WorkItem{}, false, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WorkItem{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "work:" + workID
	if err := supplyCommandLock(ctx, tx, actor, operation, key); err != nil {
		return WorkItem{}, false, err
	}
	fingerprint := digest(status)
	var replay WorkItem
	if found, err := loadSupplyReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return WorkItem{}, false, err
	} else if found {
		return replay, true, nil
	}
	var value WorkItem
	err = tx.QueryRow(ctx, `SELECT id::text,vendor_identity_id::text,reference_type,reference_id::text,status,total_minor,currency,customer_label,updated_at FROM supply.work_items WHERE id=$1 AND tenant_id=$2 AND country=$3 FOR UPDATE`, workID, actor.TenantID, actor.Country).Scan(&value.ID, &value.VendorID, &value.ReferenceType, &value.ReferenceID, &value.Status, &value.Total.AmountMinor, &value.Total.Currency, &value.CustomerLabel, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkItem{}, false, ErrNotFound
	}
	if err != nil {
		return WorkItem{}, false, err
	}
	if value.VendorID != actor.Subject {
		return WorkItem{}, false, ErrForbidden
	}
	if !allowedWorkTransition(value.Status, status) {
		return WorkItem{}, false, ErrInvalidTransition
	}
	value.Status, value.UpdatedAt, value.AllowedActions = status, service.Now(), workActions(status)
	if _, err := tx.Exec(ctx, `UPDATE supply.work_items SET status=$2,updated_at=$3 WHERE id=$1`, value.ID, value.Status, value.UpdatedAt); err != nil {
		return WorkItem{}, false, err
	}
	if err := storeSupplyReplay(ctx, tx, actor, operation, key, fingerprint, value, value.UpdatedAt); err != nil {
		return WorkItem{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WorkItem{}, false, mapSupplyError(err)
	}
	return value, false, nil
}

func (service *PostgresService) UpsertPromotion(actor Actor, key string, request Promotion) (Promotion, bool, error) {
	if !postgresSupplyActor(actor) || !hasRole(actor, "VENDOR") || !validKey(key) || len(strings.TrimSpace(request.Title)) < 3 || !request.EndsAt.After(request.StartsAt) || request.Budget.AmountMinor < 0 || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(request.Budget.Currency) {
		return Promotion{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Promotion{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := supplyCommandLock(ctx, tx, actor, "promotion", key); err != nil {
		return Promotion{}, false, err
	}
	fingerprint := digest(request)
	var replay Promotion
	if found, err := loadSupplyReplay(ctx, tx, actor, "promotion", key, fingerprint, &replay); err != nil {
		return Promotion{}, false, err
	} else if found {
		return replay, true, nil
	}
	request.ID, request.VendorID, request.Status = supplyUUID("promotion", actor.TenantID+":"+actor.Subject+":"+key), actor.Subject, "DRAFT"
	request.tenantID, request.country = actor.TenantID, actor.Country
	if _, err := tx.Exec(ctx, `INSERT INTO supply.promotions (id,tenant_id,country,vendor_identity_id,title,kind,budget_minor,currency,status,starts_at,ends_at,impressions,conversions) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,0,0)`, request.ID, actor.TenantID, actor.Country, actor.Subject, request.Title, request.Kind, request.Budget.AmountMinor, request.Budget.Currency, request.Status, request.StartsAt, request.EndsAt); err != nil {
		return Promotion{}, false, mapSupplyError(err)
	}
	if err := storeSupplyReplay(ctx, tx, actor, "promotion", key, fingerprint, request, service.Now()); err != nil {
		return Promotion{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Promotion{}, false, mapSupplyError(err)
	}
	return request, false, nil
}

func (service *PostgresService) Dashboard(actor Actor) (Dashboard, error) {
	application, err := service.Application(actor)
	if err != nil {
		return Dashboard{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	result := Dashboard{Application: application, Sales: Money{Currency: "INR"}, Recommendations: []string{}, OfflineSnapshotAt: service.Now()}
	if err := service.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE stock<5) FROM supply.catalog_items WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3`, actor.TenantID, actor.Country, actor.Subject).Scan(&result.CatalogItems, &result.LowStockItems); err != nil {
		return Dashboard{}, err
	}
	if err := service.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status NOT IN ('COMPLETED','CANCELLED')),COALESCE(sum(total_minor) FILTER (WHERE status='COMPLETED'),0) FROM supply.work_items WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3`, actor.TenantID, actor.Country, actor.Subject).Scan(&result.OpenWorkItems, &result.Sales.AmountMinor); err != nil {
		return Dashboard{}, err
	}
	if result.LowStockItems > 0 {
		result.Recommendations = append(result.Recommendations, "Restock low inventory before the next demand window.")
	}
	if result.CatalogItems == 0 {
		result.Recommendations = append(result.Recommendations, "Add the first approved catalog item.")
	}
	return result, nil
}

type applicationMutation func(*Application) error

func (service *PostgresService) mutateApplication(actor Actor, vendorID, key string, revision int64, operation string, input any, mutation applicationMutation) (Application, bool, error) {
	if !postgresSupplyActor(actor) || !postgresSupplyUUID(vendorID) || !validKey(key) || revision < 1 {
		return Application{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Application{}, false, fmt.Errorf("begin vendor application mutation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	commandOperation := operation + ":" + vendorID
	if err := supplyCommandLock(ctx, tx, actor, commandOperation, key); err != nil {
		return Application{}, false, err
	}
	fingerprint := digest(input)
	var replay Application
	if found, err := loadSupplyReplay(ctx, tx, actor, commandOperation, key, fingerprint, &replay); err != nil {
		return Application{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return roleApplication(actor, replay), true, nil
	}
	value, err := service.loadApplicationByVendor(ctx, tx, actor.TenantID, actor.Country, vendorID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, false, ErrNotFound
	}
	if err != nil {
		return Application{}, false, err
	}
	if !hasAnyRole(actor, "OPS_ADMIN", "SUPER_ADMIN", "FIELD_OFFICER", "FINANCE") && value.VendorID != actor.Subject {
		return Application{}, false, ErrForbidden
	}
	if value.Revision != revision {
		return Application{}, false, ErrConflict
	}
	previousStatus, previousTimelineLength := value.Status, len(value.Timeline)
	if err := mutation(&value); err != nil {
		return Application{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.Now()
	value.AllowedActions = applicationActions(value)
	if value.Status != previousStatus {
		reason := ""
		if transition, ok := input.(TransitionRequest); ok {
			reason = strings.TrimSpace(transition.Reason)
		}
		value.Timeline = append(value.Timeline, TimelineEvent{Status: value.Status, Actor: actor.Subject, Reason: reason, CreatedAt: value.UpdatedAt})
	}
	if err := service.saveApplication(ctx, tx, value); err != nil {
		return Application{}, false, err
	}
	if err := appendApplicationTimeline(ctx, tx, value.ID, value.Timeline[previousTimelineLength:]); err != nil {
		return Application{}, false, err
	}
	response := roleApplication(actor, value)
	if err := storeSupplyReplay(ctx, tx, actor, commandOperation, key, fingerprint, response, value.UpdatedAt); err != nil {
		return Application{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Application{}, false, mapSupplyError(err)
	}
	return response, false, nil
}

type catalogMutation func(*CatalogItem) error

func (service *PostgresService) mutateCatalog(actor Actor, key, itemID string, revision int64, operation string, input any, mutation catalogMutation) (CatalogItem, bool, error) {
	if !postgresSupplyActor(actor) || !postgresSupplyUUID(itemID) || !validKey(key) || revision < 1 {
		return CatalogItem{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), supplyOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return CatalogItem{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	commandOperation := operation + ":" + itemID
	if err := supplyCommandLock(ctx, tx, actor, commandOperation, key); err != nil {
		return CatalogItem{}, false, err
	}
	fingerprint := digest(input)
	var replay CatalogItem
	if found, err := loadSupplyReplay(ctx, tx, actor, commandOperation, key, fingerprint, &replay); err != nil {
		return CatalogItem{}, false, err
	} else if found {
		replay.tenantID, replay.country = actor.TenantID, actor.Country
		return replay, true, nil
	}
	value, err := loadCatalogItem(ctx, tx, itemID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogItem{}, false, ErrNotFound
	}
	if err != nil {
		return CatalogItem{}, false, err
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return CatalogItem{}, false, ErrForbidden
	}
	if value.Revision != revision {
		return CatalogItem{}, false, ErrConflict
	}
	if err := mutation(&value); err != nil {
		return CatalogItem{}, false, err
	}
	value.Revision++
	value.UpdatedAt = service.Now()
	value.AllowedActions = catalogActions(actor, value)
	if _, err := tx.Exec(ctx, `UPDATE supply.catalog_items SET revision=$2,stock=$3,approval_status=$4,active=$5,updated_at=$6 WHERE id=$1`, value.ID, value.Revision, value.Stock, value.ApprovalStatus, value.Active, value.UpdatedAt); err != nil {
		return CatalogItem{}, false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM supply.schedule_windows WHERE catalog_item_id=$1`, value.ID); err != nil {
		return CatalogItem{}, false, err
	}
	for _, window := range value.Schedules {
		if _, err := tx.Exec(ctx, `INSERT INTO supply.schedule_windows (catalog_item_id,weekday,starts_minute,ends_minute,timezone,capacity,buffer_minutes) VALUES ($1,$2,$3,$4,$5,$6,$7)`, value.ID, window.Weekday, window.StartsMinute, window.EndsMinute, window.TimeZone, window.Capacity, window.BufferMinute); err != nil {
			return CatalogItem{}, false, err
		}
	}
	if err := storeSupplyReplay(ctx, tx, actor, commandOperation, key, fingerprint, value, value.UpdatedAt); err != nil {
		return CatalogItem{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CatalogItem{}, false, mapSupplyError(err)
	}
	return value, false, nil
}

type supplyQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (service *PostgresService) loadApplicationByVendor(ctx context.Context, query supplyQuerier, tenantID, country, vendorID string, forUpdate bool) (Application, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	var value Application
	var bankReference, bankHolder, bankLast4, bankIFSC, bankStatus *string
	err := query.QueryRow(ctx, `SELECT id::text,vendor_identity_id::text,revision,status,business_name,business_type,contact_name,bank_reference,bank_holder_name,bank_last4,bank_ifsc,bank_status,verified,created_at,updated_at FROM supply.vendor_applications WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3`+suffix, tenantID, country, vendorID).Scan(&value.ID, &value.VendorID, &value.Revision, &value.Status, &value.BusinessName, &value.BusinessType, &value.ContactName, &bankReference, &bankHolder, &bankLast4, &bankIFSC, &bankStatus, &value.Verified, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return Application{}, err
	}
	value.tenantID, value.country = tenantID, country
	if bankReference != nil {
		value.Bank = &BankAccount{Reference: *bankReference, HolderName: valueOrEmpty(bankHolder), Last4: valueOrEmpty(bankLast4), IFSC: valueOrEmpty(bankIFSC), Status: valueOrEmpty(bankStatus)}
	}
	documents, err := query.Query(ctx, `SELECT kind,private_asset_id::text,ocr_status,extracted_fields,COALESCE(review_reason,'') FROM supply.vendor_documents WHERE application_id=$1 ORDER BY created_at,id`, value.ID)
	if err != nil {
		return Application{}, err
	}
	for documents.Next() {
		var document Document
		var extracted []byte
		if err := documents.Scan(&document.Kind, &document.AssetID, &document.OCRStatus, &extracted, &document.ReviewReason); err != nil {
			documents.Close()
			return Application{}, err
		}
		if err := json.Unmarshal(extracted, &document.ExtractedFields); err != nil {
			documents.Close()
			return Application{}, err
		}
		value.Documents = append(value.Documents, document)
	}
	documents.Close()
	var visit FieldVisit
	var officerID *string
	err = query.QueryRow(ctx, `SELECT id::text,officer_identity_id::text,scheduled_at,latitude,longitude,allowed_radius_m,checked_in_at,COALESCE(check_in_distance_m,0) FROM supply.field_visits WHERE application_id=$1 ORDER BY scheduled_at DESC LIMIT 1`, value.ID).Scan(&visit.ID, &officerID, &visit.ScheduledAt, &visit.Latitude, &visit.Longitude, &visit.AllowedRadiusM, &visit.CheckedInAt, &visit.CheckInDistance)
	if err == nil {
		if officerID != nil {
			visit.OfficerID = *officerID
		}
		value.Visit = &visit
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Application{}, err
	}
	zones, err := query.Query(ctx, `SELECT id::text,postal_codes,latitude,longitude,radius_km,policy_version FROM supply.service_zones WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3 ORDER BY id`, tenantID, country, vendorID)
	if err != nil {
		return Application{}, err
	}
	for zones.Next() {
		var zone ServiceZone
		if err := zones.Scan(&zone.ID, &zone.PostalCodes, &zone.Latitude, &zone.Longitude, &zone.RadiusKM, &zone.PolicyVersion); err != nil {
			zones.Close()
			return Application{}, err
		}
		value.Zones = append(value.Zones, zone)
	}
	zones.Close()
	timeline, err := query.Query(ctx, `SELECT status,actor_subject_id::text,COALESCE(reason,''),created_at FROM supply.application_timeline WHERE application_id=$1 ORDER BY id`, value.ID)
	if err != nil {
		return Application{}, err
	}
	for timeline.Next() {
		var event TimelineEvent
		if err := timeline.Scan(&event.Status, &event.Actor, &event.Reason, &event.CreatedAt); err != nil {
			timeline.Close()
			return Application{}, err
		}
		value.Timeline = append(value.Timeline, event)
	}
	timeline.Close()
	value.AllowedActions = applicationActions(value)
	return value, nil
}

func (service *PostgresService) saveApplication(ctx context.Context, tx pgx.Tx, value Application) error {
	var reference, holder, last4, ifsc, status any
	if value.Bank != nil {
		reference, holder, last4, ifsc, status = value.Bank.Reference, value.Bank.HolderName, value.Bank.Last4, value.Bank.IFSC, value.Bank.Status
	}
	if _, err := tx.Exec(ctx, `UPDATE supply.vendor_applications SET revision=$2,status=$3,bank_reference=$4,bank_holder_name=$5,bank_last4=$6,bank_ifsc=$7,bank_status=$8,verified=$9,updated_at=$10 WHERE id=$1`, value.ID, value.Revision, value.Status, reference, holder, last4, ifsc, status, value.Verified, value.UpdatedAt); err != nil {
		return fmt.Errorf("update vendor application: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM supply.vendor_documents WHERE application_id=$1`, value.ID); err != nil {
		return err
	}
	for _, document := range value.Documents {
		extracted, err := json.Marshal(document.ExtractedFields)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO supply.vendor_documents (id,application_id,kind,private_asset_id,ocr_status,extracted_fields,review_reason,created_at) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8)`, supplyUUID("document", value.ID+":"+document.Kind+":"+document.AssetID), value.ID, document.Kind, document.AssetID, document.OCRStatus, extracted, document.ReviewReason, value.UpdatedAt); err != nil {
			return fmt.Errorf("store vendor document: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM supply.field_visits WHERE application_id=$1`, value.ID); err != nil {
		return err
	}
	if value.Visit != nil {
		var officer any
		if value.Visit.OfficerID != "" {
			officer = value.Visit.OfficerID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO supply.field_visits (id,application_id,officer_identity_id,scheduled_at,latitude,longitude,allowed_radius_m,checked_in_at,check_in_distance_m) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, value.Visit.ID, value.ID, officer, value.Visit.ScheduledAt, value.Visit.Latitude, value.Visit.Longitude, value.Visit.AllowedRadiusM, value.Visit.CheckedInAt, value.Visit.CheckInDistance); err != nil {
			return fmt.Errorf("store field visit: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM supply.service_zones WHERE tenant_id=$1 AND country=$2 AND vendor_identity_id=$3`, value.tenantID, value.country, value.VendorID); err != nil {
		return err
	}
	for _, zone := range value.Zones {
		if _, err := tx.Exec(ctx, `INSERT INTO supply.service_zones (id,vendor_identity_id,tenant_id,country,postal_codes,latitude,longitude,radius_km,policy_version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, zone.ID, value.VendorID, value.tenantID, value.country, zone.PostalCodes, zone.Latitude, zone.Longitude, zone.RadiusKM, zone.PolicyVersion); err != nil {
			return fmt.Errorf("store vendor service zone: %w", err)
		}
	}
	return nil
}

func appendApplicationTimeline(ctx context.Context, tx pgx.Tx, applicationID string, values []TimelineEvent) error {
	for _, value := range values {
		if _, err := tx.Exec(ctx, `INSERT INTO supply.application_timeline (application_id,status,actor_subject_id,reason,created_at) VALUES ($1,$2,$3,NULLIF($4,''),$5)`, applicationID, value.Status, value.Actor, value.Reason, value.CreatedAt); err != nil {
			return fmt.Errorf("append vendor application timeline: %w", err)
		}
	}
	return nil
}

func loadCatalogItem(ctx context.Context, query supplyQuerier, itemID string, forUpdate bool) (CatalogItem, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	var value CatalogItem
	err := query.QueryRow(ctx, `SELECT id::text,tenant_id::text,country,vendor_identity_id::text,revision,kind,name,description,sku,price_minor,currency,stock,approval_status,active,updated_at FROM supply.catalog_items WHERE id=$1`+suffix, itemID).Scan(&value.ID, &value.tenantID, &value.country, &value.VendorID, &value.Revision, &value.Kind, &value.Name, &value.Description, &value.SKU, &value.Price.AmountMinor, &value.Price.Currency, &value.Stock, &value.ApprovalStatus, &value.Active, &value.UpdatedAt)
	if err != nil {
		return CatalogItem{}, err
	}
	rows, err := query.Query(ctx, `SELECT weekday,starts_minute,ends_minute,timezone,capacity,buffer_minutes FROM supply.schedule_windows WHERE catalog_item_id=$1 ORDER BY weekday,starts_minute`, itemID)
	if err != nil {
		return CatalogItem{}, err
	}
	for rows.Next() {
		var window ScheduleWindow
		if err := rows.Scan(&window.Weekday, &window.StartsMinute, &window.EndsMinute, &window.TimeZone, &window.Capacity, &window.BufferMinute); err != nil {
			rows.Close()
			return CatalogItem{}, err
		}
		value.Schedules = append(value.Schedules, window)
	}
	rows.Close()
	return value, nil
}

func supplyCommandLock(ctx context.Context, tx pgx.Tx, actor Actor, operation, key string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":"+actor.Subject+":"+operation+":"+key); err != nil {
		return fmt.Errorf("lock supply command: %w", err)
	}
	return nil
}

func loadSupplyReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, destination any) (bool, error) {
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM supply.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND operation=$4 AND idempotency_key=$5`, actor.TenantID, actor.Country, actor.Subject, operation, key).Scan(&storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedFingerprint != fingerprint {
		return false, ErrIdempotencyConflict
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return false, fmt.Errorf("decode supply replay: %w", err)
	}
	return true, nil
}

func storeSupplyReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, value any, now time.Time) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO supply.idempotency_records (tenant_id,country,subject_id,operation,idempotency_key,request_fingerprint,response_payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, actor.Subject, operation, key, fingerprint, payload, now); err != nil {
		return fmt.Errorf("store supply replay: %w", err)
	}
	return nil
}

func supplyUUID(kind, source string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("planext4u:supply:"+kind+":"+source)).String()
}

func postgresSupplyUUID(value string) bool { return uuid.Validate(strings.TrimSpace(value)) == nil }

func postgresSupplyActor(actor Actor) bool {
	return postgresSupplyUUID(actor.TenantID) && postgresSupplyUUID(actor.Subject) && len(actor.Country) == 2 && actor.Country == strings.ToUpper(actor.Country) && len(actor.Roles) > 0
}

func mapSupplyError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505":
			return ErrConflict
		case "40001", "40P01":
			return ErrConflict
		}
	}
	return err
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
