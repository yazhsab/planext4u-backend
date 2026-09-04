package localverticals

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const localVerticalOperationTimeout = 20 * time.Second

type marketplacePolicy struct {
	version          string
	currency         string
	estimatorVersion string
	reviewTerms      []string
	classifiedTTL    time.Duration
	featureTTL       time.Duration
	reportThreshold  int
}

type PostgresService struct {
	pool  *pgxpool.Pool
	clock func() time.Time
	aead  cipher.AEAD
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time, contactKey []byte) (*PostgresService, error) {
	if pool == nil || clock == nil || len(contactKey) != 32 {
		return nil, ErrInvalidRequest
	}
	block, err := aes.NewCipher(contactKey)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock, aead: aead}, nil
}

func (service *PostgresService) Now() time.Time { return service.clock().UTC() }

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	err := service.pool.QueryRow(ctx, `SELECT to_regclass('local_verticals.home_listings') IS NOT NULL AND to_regclass('local_verticals.classified_listings') IS NOT NULL AND to_regclass('local_verticals.policies') IS NOT NULL AND to_regclass('local_verticals.idempotency_records') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check local vertical schema readiness: %w", err)
	}
	if !ready {
		return errors.New("local vertical schema is unavailable")
	}
	return nil
}

func (service *PostgresService) SearchHomes(actor Actor, filter HomeSearch) (HomePage, error) {
	limit, cursor, cursorErr := listingPageBounds(filter.Limit, filter.Cursor)
	if !postgresLocalActor(actor) || !browserReader(actor) {
		return HomePage{}, ErrForbidden
	}
	if cursorErr != nil || filter.MinPrice < 0 || filter.MaxPrice < 0 || filter.MaxPrice > 0 && filter.MinPrice > filter.MaxPrice {
		return HomePage{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	now := service.Now()
	cursorPresent, cursorFeatured, cursorUpdated, cursorID := cursor != nil, 0, time.Unix(0, 0).UTC(), ""
	if cursor != nil {
		if cursor.Featured {
			cursorFeatured = 1
		}
		cursorUpdated, cursorID = time.Unix(0, cursor.Updated).UTC(), cursor.ID
	}
	rows, err := service.pool.Query(ctx, homeSelect+` WHERE tenant_id=$1 AND country=$2 AND status='ACTIVE' AND ($3='' OR title ILIKE '%'||$3||'%' OR locality ILIKE '%'||$3||'%' OR array_to_string(amenities,' ') ILIKE '%'||$3||'%') AND ($4='' OR locality ILIKE '%'||$4||'%') AND ($5='' OR property_type=upper($5)) AND ($6='' OR purpose=upper($6)) AND ($7=0 OR price_amount_minor>=$7) AND ($8=0 OR price_amount_minor<=$8) AND (NOT $10 OR CASE WHEN featured_until>$9 THEN 1 ELSE 0 END<$11 OR (CASE WHEN featured_until>$9 THEN 1 ELSE 0 END=$11 AND (updated_at<$12 OR (updated_at=$12 AND id::text<$13)))) ORDER BY (featured_until>$9) DESC,updated_at DESC,id DESC LIMIT $14`, actor.TenantID, actor.Country, strings.TrimSpace(filter.Query), strings.TrimSpace(filter.Locality), strings.TrimSpace(filter.PropertyType), strings.TrimSpace(filter.Purpose), filter.MinPrice, filter.MaxPrice, now, cursorPresent, cursorFeatured, cursorUpdated, cursorID, limit+1)
	if err != nil {
		return HomePage{}, err
	}
	values, err := scanHomes(rows, actor)
	if err != nil {
		return HomePage{}, err
	}
	page := HomePage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeListingCursor(homeFeatured(last, now), last.UpdatedAt, last.ID)
	}
	return page, nil
}

func (service *PostgresService) Home(actor Actor, id string) (HomeListing, error) {
	if !postgresLocalActor(actor) || !browserReader(actor) || !localUUID(id) {
		return HomeListing{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	value, err := loadHome(ctx, service.pool, actor.TenantID, actor.Country, id, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && value.Status != "ACTIVE" && value.OwnerID != actor.Subject {
		return HomeListing{}, ErrNotFound
	}
	if err != nil {
		return HomeListing{}, err
	}
	return presentHome(actor, value), nil
}

func (service *PostgresService) CreateHome(actor Actor, key string, request HomeListingRequest) (HomeListing, bool, error) {
	if !postgresLocalActor(actor) || !customer(actor) || !validKey(key) || !validHome(request.Title, request.PropertyType, request.Purpose, request.Locality, request.AreaSqFt, request.Bedrooms, request.Price, request.Latitude, request.Longitude) || len(request.MediaAssetIDs) > 20 || !allLocalUUIDs(request.MediaAssetIDs) {
		return HomeListing{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return HomeListing{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := localCommandLock(ctx, tx, actor, "home-create", key); err != nil {
		return HomeListing{}, false, err
	}
	fingerprint := digest(request)
	var replay HomeListing
	if found, err := loadLocalReplay(ctx, tx, actor, "home-create", key, fingerprint, &replay); err != nil {
		return HomeListing{}, false, err
	} else if found {
		return replay, true, nil
	}
	policy, err := loadMarketplacePolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil || request.Price.Currency != policy.currency {
		return HomeListing{}, false, ErrInvalidRequest
	}
	var verified bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM local_verticals.owner_verifications WHERE tenant_id=$1 AND country=$2 AND owner_identity_id=$3 AND status='VERIFIED')`, actor.TenantID, actor.Country, actor.Subject).Scan(&verified); err != nil {
		return HomeListing{}, false, err
	}
	now := service.Now()
	value := HomeListing{ID: uuid.NewString(), Revision: 1, OwnerID: actor.Subject, Title: strings.TrimSpace(request.Title), PropertyType: strings.ToUpper(request.PropertyType), Purpose: strings.ToUpper(request.Purpose), Locality: strings.TrimSpace(request.Locality), Latitude: request.Latitude, Longitude: request.Longitude, AreaSqFt: request.AreaSqFt, Bedrooms: request.Bedrooms, Price: request.Price, Amenities: cleanList(request.Amenities), MediaAssetIDs: cleanList(request.MediaAssetIDs), Status: "DRAFT", KYCVerified: verified, Plan: "STANDARD", CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	value.Estimate = estimatePostgresHome(value, policy.estimatorVersion, now)
	estimate, _ := json.Marshal(value.Estimate)
	_, err = tx.Exec(ctx, `INSERT INTO local_verticals.home_listings (id,tenant_id,country,owner_identity_id,revision,title,property_type,purpose,locality,latitude,longitude,area_sq_ft,bedrooms,price_amount_minor,currency,amenities,media_asset_ids,status,kyc_verified,plan,estimate,created_at,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'DRAFT',$17,'STANDARD',$18,$19,$19)`, value.ID, actor.TenantID, actor.Country, actor.Subject, value.Title, value.PropertyType, value.Purpose, value.Locality, value.Latitude, value.Longitude, value.AreaSqFt, value.Bedrooms, value.Price.AmountMinor, value.Price.Currency, value.Amenities, value.MediaAssetIDs, verified, estimate, now)
	if err != nil {
		return HomeListing{}, false, mapLocalError(err)
	}
	value = presentHome(actor, value)
	if err := storeLocalReplay(ctx, tx, actor, "home-create", key, fingerprint, value, now); err != nil {
		return HomeListing{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return HomeListing{}, false, mapLocalError(err)
	}
	return value, false, nil
}

func (service *PostgresService) PublishHome(actor Actor, key, id string, revision int64) (HomeListing, bool, error) {
	return service.mutateHome(actor, key, id, "home-publish", digest(revision), func(value *HomeListing, policy marketplacePolicy, now time.Time) error {
		if value.Revision != revision || value.Status != "DRAFT" {
			return ErrConflict
		}
		if !value.KYCVerified {
			return ErrForbidden
		}
		value.Status = "ACTIVE"
		return nil
	})
}

func (service *PostgresService) EstimateHome(actor Actor, id string) (HomeEstimate, error) {
	value, err := service.Home(actor, id)
	if err != nil {
		return HomeEstimate{}, err
	}
	return value.Estimate, nil
}

func (service *PostgresService) Inquire(actor Actor, key, id, message string) (Inquiry, bool, error) {
	message = strings.TrimSpace(message)
	if !postgresLocalActor(actor) || !customer(actor) || !validKey(key) || !localUUID(id) || len([]rune(message)) < 4 || len([]rune(message)) > 1000 {
		return Inquiry{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Inquiry{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "home-inquiry:" + id
	if err := localCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Inquiry{}, false, err
	}
	fingerprint := digest(message)
	var replay Inquiry
	if found, err := loadLocalReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Inquiry{}, false, err
	} else if found {
		return replay, true, nil
	}
	listing, err := loadHome(ctx, tx, actor.TenantID, actor.Country, id, true)
	if err != nil || listing.Status != "ACTIVE" || listing.OwnerID == actor.Subject {
		return Inquiry{}, false, ErrForbidden
	}
	now := service.Now()
	value := Inquiry{ID: uuid.NewString(), ListingID: id, BuyerID: actor.Subject, Message: message, Status: "OPEN", CreatedAt: now}
	_, err = tx.Exec(ctx, `INSERT INTO local_verticals.home_inquiries (id,tenant_id,country,listing_id,buyer_identity_id,message,status,created_at) VALUES ($1,$2,$3,$4,$5,$6,'OPEN',$7)`, value.ID, actor.TenantID, actor.Country, id, actor.Subject, message, now)
	if err != nil {
		return Inquiry{}, false, err
	}
	if err := storeLocalReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Inquiry{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Inquiry{}, false, mapLocalError(err)
	}
	return value, false, nil
}

func (service *PostgresService) ScheduleVisit(actor Actor, key, id string, scheduledAt time.Time) (Visit, bool, error) {
	if !postgresLocalActor(actor) || !customer(actor) || !validKey(key) || !localUUID(id) || !scheduledAt.After(service.Now().Add(time.Hour)) {
		return Visit{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Visit{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "home-visit:" + id
	if err := localCommandLock(ctx, tx, actor, operation, key); err != nil {
		return Visit{}, false, err
	}
	fingerprint := digest(scheduledAt.UTC())
	var replay Visit
	if found, err := loadLocalReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return Visit{}, false, err
	} else if found {
		return replay, true, nil
	}
	listing, err := loadHome(ctx, tx, actor.TenantID, actor.Country, id, true)
	if err != nil || listing.Status != "ACTIVE" || listing.OwnerID == actor.Subject {
		return Visit{}, false, ErrForbidden
	}
	now := service.Now()
	value := Visit{ID: uuid.NewString(), ListingID: id, VisitorID: actor.Subject, ScheduledAt: scheduledAt.UTC(), Status: "REQUESTED", CreatedAt: now}
	_, err = tx.Exec(ctx, `INSERT INTO local_verticals.home_visits (id,tenant_id,country,listing_id,visitor_identity_id,scheduled_at,status,created_at) VALUES ($1,$2,$3,$4,$5,$6,'REQUESTED',$7)`, value.ID, actor.TenantID, actor.Country, id, actor.Subject, value.ScheduledAt, now)
	if err != nil {
		return Visit{}, false, err
	}
	if err := storeLocalReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return Visit{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Visit{}, false, mapLocalError(err)
	}
	return value, false, nil
}

func (service *PostgresService) UpgradeHome(actor Actor, key, id, plan string) (HomeListing, bool, error) {
	plan = strings.ToUpper(strings.TrimSpace(plan))
	if !map[string]bool{"STANDARD": true, "FEATURED": true, "PREMIUM": true}[plan] {
		return HomeListing{}, false, ErrInvalidRequest
	}
	return service.mutateHome(actor, key, id, "home-upgrade", digest(plan), func(value *HomeListing, policy marketplacePolicy, now time.Time) error {
		value.Plan = plan
		if plan == "STANDARD" {
			value.FeaturedUntil = nil
		} else {
			until := now.Add(policy.featureTTL)
			value.FeaturedUntil = &until
		}
		return nil
	})
}

func (service *PostgresService) mutateHome(actor Actor, key, id, operation, fingerprint string, mutation func(*HomeListing, marketplacePolicy, time.Time) error) (HomeListing, bool, error) {
	if !postgresLocalActor(actor) || !customer(actor) || !validKey(key) || !localUUID(id) {
		return HomeListing{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return HomeListing{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	scope := operation + ":" + id
	if err := localCommandLock(ctx, tx, actor, scope, key); err != nil {
		return HomeListing{}, false, err
	}
	var replay HomeListing
	if found, err := loadLocalReplay(ctx, tx, actor, scope, key, fingerprint, &replay); err != nil {
		return HomeListing{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadHome(ctx, tx, actor.TenantID, actor.Country, id, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && value.OwnerID != actor.Subject {
		return HomeListing{}, false, ErrNotFound
	}
	if err != nil {
		return HomeListing{}, false, err
	}
	policy, err := loadMarketplacePolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return HomeListing{}, false, err
	}
	now := service.Now()
	if err := mutation(&value, policy, now); err != nil {
		return HomeListing{}, false, err
	}
	value.Revision++
	value.UpdatedAt = now
	_, err = tx.Exec(ctx, `UPDATE local_verticals.home_listings SET status=$2,plan=$3,featured_until=$4,revision=$5,updated_at=$6 WHERE id=$1`, id, value.Status, value.Plan, value.FeaturedUntil, value.Revision, now)
	if err != nil {
		return HomeListing{}, false, err
	}
	value = presentHome(actor, value)
	if err := storeLocalReplay(ctx, tx, actor, scope, key, fingerprint, value, now); err != nil {
		return HomeListing{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return HomeListing{}, false, mapLocalError(err)
	}
	return value, false, nil
}

func (service *PostgresService) BrowseClassifieds(actor Actor, filter ClassifiedSearch) (ClassifiedPage, error) {
	limit, cursor, cursorErr := listingPageBounds(filter.Limit, filter.Cursor)
	if !postgresLocalActor(actor) || !browserReader(actor) {
		return ClassifiedPage{}, ErrForbidden
	}
	if cursorErr != nil {
		return ClassifiedPage{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	now := service.Now()
	_, _ = service.pool.Exec(ctx, `UPDATE local_verticals.classified_listings SET status='EXPIRED',revision=revision+1,updated_at=$3 WHERE tenant_id=$1 AND country=$2 AND status='PUBLISHED' AND expires_at<=$3`, actor.TenantID, actor.Country, now)
	cursorPresent, cursorFeatured, cursorUpdated, cursorID := cursor != nil, 0, time.Unix(0, 0).UTC(), ""
	if cursor != nil {
		if cursor.Featured {
			cursorFeatured = 1
		}
		cursorUpdated, cursorID = time.Unix(0, cursor.Updated).UTC(), cursor.ID
	}
	rows, err := service.pool.Query(ctx, classifiedSelect+` WHERE tenant_id=$1 AND country=$2 AND status='PUBLISHED' AND ($3='' OR title ILIKE '%'||$3||'%' OR description ILIKE '%'||$3||'%') AND ($4='' OR category=upper($4)) AND ($5='' OR locality ILIKE '%'||$5||'%') AND (NOT $7 OR CASE WHEN featured_until>$6 THEN 1 ELSE 0 END<$8 OR (CASE WHEN featured_until>$6 THEN 1 ELSE 0 END=$8 AND (updated_at<$9 OR (updated_at=$9 AND id::text<$10)))) ORDER BY (featured_until>$6) DESC,updated_at DESC,id DESC LIMIT $11`, actor.TenantID, actor.Country, strings.TrimSpace(filter.Query), strings.TrimSpace(filter.Category), strings.TrimSpace(filter.Locality), now, cursorPresent, cursorFeatured, cursorUpdated, cursorID, limit+1)
	if err != nil {
		return ClassifiedPage{}, err
	}
	values, err := scanClassifieds(rows, actor)
	if err != nil {
		return ClassifiedPage{}, err
	}
	page := ClassifiedPage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeListingCursor(classifiedFeatured(last, now), last.UpdatedAt, last.ID)
	}
	return page, nil
}

func (service *PostgresService) Classified(actor Actor, id string) (ClassifiedListing, error) {
	if !postgresLocalActor(actor) || !browserReader(actor) || !localUUID(id) {
		return ClassifiedListing{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	value, err := loadClassified(ctx, service.pool, actor.TenantID, actor.Country, id, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && value.Status != "PUBLISHED" && value.OwnerID != actor.Subject {
		return ClassifiedListing{}, ErrNotFound
	}
	if err != nil {
		return ClassifiedListing{}, err
	}
	return presentClassified(actor, value), nil
}

func (service *PostgresService) CreateClassified(actor Actor, key string, request ClassifiedRequest) (ClassifiedListing, bool, error) {
	if !postgresLocalActor(actor) || !customer(actor) || !validKey(key) || !validClassified(request) || !allLocalUUIDs(request.MediaAssetIDs) {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := localCommandLock(ctx, tx, actor, "classified-create", key); err != nil {
		return ClassifiedListing{}, false, err
	}
	fingerprint := digest(request)
	var replay ClassifiedListing
	if found, err := loadLocalReplay(ctx, tx, actor, "classified-create", key, fingerprint, &replay); err != nil {
		return ClassifiedListing{}, false, err
	} else if found {
		return replay, true, nil
	}
	policy, err := loadMarketplacePolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil || request.Price.Currency != policy.currency {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	status := "PUBLISHED"
	for _, term := range policy.reviewTerms {
		if strings.Contains(strings.ToLower(request.Title+" "+request.Description), strings.ToLower(term)) {
			status = "PENDING_REVIEW"
		}
	}
	now, id := service.Now(), uuid.NewString()
	contact := strings.TrimSpace(request.Contact)
	ciphertext, err := service.encryptContact([]byte(contact), id)
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	last4 := contact
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	value := ClassifiedListing{ID: id, Revision: 1, OwnerID: actor.Subject, Category: strings.ToUpper(request.Category), Title: strings.TrimSpace(request.Title), Description: strings.TrimSpace(request.Description), Price: request.Price, Locality: strings.TrimSpace(request.Locality), MediaAssetIDs: cleanList(request.MediaAssetIDs), Status: status, Plan: "STANDARD", ContactMasked: maskContact(contact), WhatsAppEnabled: request.WhatsAppEnabled, ExpiresAt: now.Add(policy.classifiedTTL), CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	_, err = tx.Exec(ctx, `INSERT INTO local_verticals.classified_listings (id,tenant_id,country,owner_identity_id,revision,category,title,description,price_amount_minor,currency,locality,media_asset_ids,status,plan,encrypted_contact,contact_last4,whatsapp_enabled,expires_at,report_count,created_at,updated_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7,$8,$9,$10,$11,$12,'STANDARD',$13,$14,$15,$16,0,$17,$17)`, id, actor.TenantID, actor.Country, actor.Subject, value.Category, value.Title, value.Description, value.Price.AmountMinor, value.Price.Currency, value.Locality, value.MediaAssetIDs, status, ciphertext, last4, value.WhatsAppEnabled, value.ExpiresAt, now)
	if err != nil {
		return ClassifiedListing{}, false, mapLocalError(err)
	}
	value = presentClassified(actor, value)
	if err := storeLocalReplay(ctx, tx, actor, "classified-create", key, fingerprint, value, now); err != nil {
		return ClassifiedListing{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClassifiedListing{}, false, mapLocalError(err)
	}
	return value, false, nil
}

func (service *PostgresService) RevealContact(actor Actor, id string, request ContactRequest) (ClassifiedListing, error) {
	channel := strings.ToUpper(strings.TrimSpace(request.Channel))
	if !postgresLocalActor(actor) || !customer(actor) || !localUUID(id) || !request.Consent || !map[string]bool{"PHONE": true, "WHATSAPP": true}[channel] {
		return ClassifiedListing{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	value, err := loadClassified(ctx, service.pool, actor.TenantID, actor.Country, id, false)
	if err != nil || value.Status != "PUBLISHED" || value.OwnerID == actor.Subject || channel == "WHATSAPP" && !value.WhatsAppEnabled {
		return ClassifiedListing{}, ErrForbidden
	}
	plaintext, err := service.decryptContact(value.encryptedContact, id)
	if err != nil {
		return ClassifiedListing{}, err
	}
	result := presentClassified(actor, value)
	result.ContactRevealed = string(plaintext)
	return result, nil
}

func (service *PostgresService) RepostClassified(actor Actor, key, id string) (ClassifiedListing, bool, error) {
	return service.mutateClassified(actor, key, id, "classified-repost", digest(id), func(value *ClassifiedListing, policy marketplacePolicy, now time.Time) error {
		if value.Status != "EXPIRED" && now.Before(value.ExpiresAt) {
			return ErrConflict
		}
		value.Status, value.ExpiresAt = "PUBLISHED", now.Add(policy.classifiedTTL)
		return nil
	})
}

func (service *PostgresService) ReportClassified(actor Actor, key, id string, request ReportRequest) (ClassifiedListing, bool, error) {
	if !postgresLocalActor(actor) || !customer(actor) || !validKey(key) || !localUUID(id) || len(strings.TrimSpace(request.Reason)) < 3 || len(request.Details) > 1000 {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation := "classified-report:" + id
	if err := localCommandLock(ctx, tx, actor, operation, key); err != nil {
		return ClassifiedListing{}, false, err
	}
	fingerprint := digest(request)
	var replay ClassifiedListing
	if found, err := loadLocalReplay(ctx, tx, actor, operation, key, fingerprint, &replay); err != nil {
		return ClassifiedListing{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadClassified(ctx, tx, actor.TenantID, actor.Country, id, true)
	if err != nil || value.OwnerID == actor.Subject || value.Status != "PUBLISHED" {
		return ClassifiedListing{}, false, ErrForbidden
	}
	policy, err := loadMarketplacePolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	now := service.Now()
	_, err = tx.Exec(ctx, `INSERT INTO local_verticals.reports (id,tenant_id,country,listing_id,reporter_identity_id,reason,details,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.NewString(), actor.TenantID, actor.Country, id, actor.Subject, strings.TrimSpace(request.Reason), strings.TrimSpace(request.Details), now)
	if err != nil {
		return ClassifiedListing{}, false, mapLocalError(err)
	}
	value.ReportCount++
	if value.ReportCount >= policy.reportThreshold {
		value.Status = "PENDING_REVIEW"
	}
	value.Revision++
	value.UpdatedAt = now
	_, err = tx.Exec(ctx, `UPDATE local_verticals.classified_listings SET report_count=$2,status=$3,revision=$4,updated_at=$5 WHERE id=$1`, id, value.ReportCount, value.Status, value.Revision, now)
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	value = presentClassified(actor, value)
	if err := storeLocalReplay(ctx, tx, actor, operation, key, fingerprint, value, now); err != nil {
		return ClassifiedListing{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClassifiedListing{}, false, mapLocalError(err)
	}
	return value, false, nil
}

func (service *PostgresService) UpgradeClassified(actor Actor, key, id, plan string) (ClassifiedListing, bool, error) {
	plan = strings.ToUpper(strings.TrimSpace(plan))
	if !map[string]bool{"STANDARD": true, "FEATURED": true}[plan] {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	return service.mutateClassified(actor, key, id, "classified-upgrade", digest(plan), func(value *ClassifiedListing, policy marketplacePolicy, now time.Time) error {
		value.Plan = plan
		if plan == "FEATURED" {
			until := now.Add(policy.featureTTL)
			value.FeaturedUntil = &until
		} else {
			value.FeaturedUntil = nil
		}
		return nil
	})
}

func (service *PostgresService) mutateClassified(actor Actor, key, id, operation, fingerprint string, mutation func(*ClassifiedListing, marketplacePolicy, time.Time) error) (ClassifiedListing, bool, error) {
	if !postgresLocalActor(actor) || !customer(actor) || !validKey(key) || !localUUID(id) {
		return ClassifiedListing{}, false, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	scope := operation + ":" + id
	if err := localCommandLock(ctx, tx, actor, scope, key); err != nil {
		return ClassifiedListing{}, false, err
	}
	var replay ClassifiedListing
	if found, err := loadLocalReplay(ctx, tx, actor, scope, key, fingerprint, &replay); err != nil {
		return ClassifiedListing{}, false, err
	} else if found {
		return replay, true, nil
	}
	value, err := loadClassified(ctx, tx, actor.TenantID, actor.Country, id, true)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && value.OwnerID != actor.Subject {
		return ClassifiedListing{}, false, ErrNotFound
	}
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	policy, err := loadMarketplacePolicy(ctx, tx, actor.TenantID, actor.Country)
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	now := service.Now()
	if err := mutation(&value, policy, now); err != nil {
		return ClassifiedListing{}, false, err
	}
	value.Revision++
	value.UpdatedAt = now
	_, err = tx.Exec(ctx, `UPDATE local_verticals.classified_listings SET status=$2,plan=$3,expires_at=$4,featured_until=$5,revision=$6,updated_at=$7 WHERE id=$1`, id, value.Status, value.Plan, value.ExpiresAt, value.FeaturedUntil, value.Revision, now)
	if err != nil {
		return ClassifiedListing{}, false, err
	}
	value = presentClassified(actor, value)
	if err := storeLocalReplay(ctx, tx, actor, scope, key, fingerprint, value, now); err != nil {
		return ClassifiedListing{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ClassifiedListing{}, false, mapLocalError(err)
	}
	return value, false, nil
}

func (service *PostgresService) ExpireClassifieds(actor Actor) (int, error) {
	if !postgresLocalActor(actor) || !admin(actor) {
		if !actor.MFAVerified {
			return 0, ErrMFARequired
		}
		return 0, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), localVerticalOperationTimeout)
	defer cancel()
	result, err := service.pool.Exec(ctx, `UPDATE local_verticals.classified_listings SET status='EXPIRED',revision=revision+1,updated_at=$3 WHERE tenant_id=$1 AND country=$2 AND status='PUBLISHED' AND expires_at<=$3`, actor.TenantID, actor.Country, service.Now())
	if err != nil {
		return 0, err
	}
	return int(result.RowsAffected()), nil
}

type localRow interface{ Scan(...any) error }
type localQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const homeSelect = `SELECT id::text,revision,owner_identity_id::text,title,property_type,purpose,locality,latitude,longitude,area_sq_ft,bedrooms,price_amount_minor,currency,amenities,media_asset_ids::text[],status,kyc_verified,plan,featured_until,estimate,created_at,updated_at,tenant_id::text,country FROM local_verticals.home_listings`
const classifiedSelect = `SELECT id::text,revision,owner_identity_id::text,category,title,description,price_amount_minor,currency,locality,media_asset_ids::text[],status,plan,encrypted_contact,contact_last4,whatsapp_enabled,expires_at,featured_until,report_count,created_at,updated_at,tenant_id::text,country FROM local_verticals.classified_listings`

func loadHome(ctx context.Context, query localQuerier, tenantID, country, id string, lock bool) (HomeListing, error) {
	suffix := ` WHERE tenant_id=$1 AND country=$2 AND id=$3`
	if lock {
		suffix += ` FOR UPDATE`
	}
	return scanHome(query.QueryRow(ctx, homeSelect+suffix, tenantID, country, id))
}
func scanHome(row localRow) (HomeListing, error) {
	var value HomeListing
	var estimate []byte
	err := row.Scan(&value.ID, &value.Revision, &value.OwnerID, &value.Title, &value.PropertyType, &value.Purpose, &value.Locality, &value.Latitude, &value.Longitude, &value.AreaSqFt, &value.Bedrooms, &value.Price.AmountMinor, &value.Price.Currency, &value.Amenities, &value.MediaAssetIDs, &value.Status, &value.KYCVerified, &value.Plan, &value.FeaturedUntil, &estimate, &value.CreatedAt, &value.UpdatedAt, &value.tenantID, &value.country)
	if err == nil {
		err = json.Unmarshal(estimate, &value.Estimate)
	}
	return value, err
}
func scanHomes(rows pgx.Rows, actor Actor) ([]HomeListing, error) {
	defer rows.Close()
	values := []HomeListing{}
	for rows.Next() {
		value, err := scanHome(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, presentHome(actor, value))
	}
	return values, rows.Err()
}

func loadClassified(ctx context.Context, query localQuerier, tenantID, country, id string, lock bool) (ClassifiedListing, error) {
	suffix := ` WHERE tenant_id=$1 AND country=$2 AND id=$3`
	if lock {
		suffix += ` FOR UPDATE`
	}
	return scanClassified(query.QueryRow(ctx, classifiedSelect+suffix, tenantID, country, id))
}
func scanClassified(row localRow) (ClassifiedListing, error) {
	var value ClassifiedListing
	var last4 string
	err := row.Scan(&value.ID, &value.Revision, &value.OwnerID, &value.Category, &value.Title, &value.Description, &value.Price.AmountMinor, &value.Price.Currency, &value.Locality, &value.MediaAssetIDs, &value.Status, &value.Plan, &value.encryptedContact, &last4, &value.WhatsAppEnabled, &value.ExpiresAt, &value.FeaturedUntil, &value.ReportCount, &value.CreatedAt, &value.UpdatedAt, &value.tenantID, &value.country)
	value.ContactMasked = "****" + last4
	return value, err
}
func scanClassifieds(rows pgx.Rows, actor Actor) ([]ClassifiedListing, error) {
	defer rows.Close()
	values := []ClassifiedListing{}
	for rows.Next() {
		value, err := scanClassified(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, presentClassified(actor, value))
	}
	return values, rows.Err()
}

func loadMarketplacePolicy(ctx context.Context, query localQuerier, tenantID, country string) (marketplacePolicy, error) {
	var value marketplacePolicy
	var classified, feature int64
	err := query.QueryRow(ctx, `SELECT version,currency,estimator_version,review_terms,classified_lifetime_seconds,feature_lifetime_seconds,report_review_threshold FROM local_verticals.policies WHERE tenant_id=$1 AND country=$2`, tenantID, country).Scan(&value.version, &value.currency, &value.estimatorVersion, &value.reviewTerms, &classified, &feature, &value.reportThreshold)
	value.classifiedTTL, value.featureTTL = time.Duration(classified)*time.Second, time.Duration(feature)*time.Second
	return value, err
}

func estimatePostgresHome(value HomeListing, version string, now time.Time) HomeEstimate {
	adjustment := int64(100)
	if strings.EqualFold(value.PropertyType, "VILLA") {
		adjustment = 112
	}
	amount := value.Price.AmountMinor * adjustment / 100
	return HomeEstimate{Version: version, Amount: Money{AmountMinor: amount, Currency: value.Price.Currency}, PricePerArea: Money{AmountMinor: amount / int64(value.AreaSqFt), Currency: value.Price.Currency}, Factors: map[string]string{"locality": "weighted", "property_type": strings.ToLower(value.PropertyType), "confidence": "indicative"}, GeneratedAt: now}
}

func localCommandLock(ctx context.Context, tx pgx.Tx, actor Actor, operation, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Subject+":"+operation+":"+key)
	return err
}
func loadLocalReplay(ctx context.Context, query localQuerier, actor Actor, operation, key, fingerprint string, output any) (bool, error) {
	var stored string
	var payload []byte
	err := query.QueryRow(ctx, `SELECT request_fingerprint,response_payload FROM local_verticals.idempotency_records WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND operation=$4 AND idempotency_key=$5`, actor.TenantID, actor.Country, actor.Subject, operation, key).Scan(&stored, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != fingerprint {
		return false, ErrIdempotencyConflict
	}
	return true, json.Unmarshal(payload, output)
}
func storeLocalReplay(ctx context.Context, tx pgx.Tx, actor Actor, operation, key, fingerprint string, response any, now time.Time) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO local_verticals.idempotency_records (tenant_id,country,subject_id,operation,idempotency_key,request_fingerprint,response_payload,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, actor.Subject, operation, key, fingerprint, payload, now)
	return mapLocalError(err)
}

func (service *PostgresService) encryptContact(plaintext []byte, id string) ([]byte, error) {
	nonce := make([]byte, service.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return service.aead.Seal(nonce, nonce, plaintext, []byte("classified:"+id)), nil
}
func (service *PostgresService) decryptContact(ciphertext []byte, id string) ([]byte, error) {
	if len(ciphertext) <= service.aead.NonceSize() {
		return nil, errors.New("classified contact ciphertext is invalid")
	}
	nonce := ciphertext[:service.aead.NonceSize()]
	return service.aead.Open(nil, nonce, ciphertext[service.aead.NonceSize():], []byte("classified:"+id))
}

func mapLocalError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505", "40001", "40P01":
			return ErrConflict
		case "23503", "23514", "22P02":
			return ErrInvalidRequest
		}
	}
	return err
}
func localUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }
func postgresLocalActor(actor Actor) bool {
	return validActor(actor) && localUUID(actor.TenantID) && localUUID(actor.Subject)
}
func allLocalUUIDs(values []string) bool {
	for _, value := range values {
		if !localUUID(value) {
			return false
		}
	}
	return true
}

var _ Application = (*PostgresService)(nil)
