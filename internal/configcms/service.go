package configcms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	repository Repository
	clock      func() time.Time
}

func NewService(repository Repository, clock func() time.Time) (*Service, error) {
	if repository == nil || clock == nil {
		return nil, fmt.Errorf("invalid configuration service")
	}
	return &Service{repository: repository, clock: clock}, nil
}

func (service *Service) Bootstrap(ctx context.Context, tenantID, country string, platform Platform, appVersion, requestedLocale string) (Bootstrap, error) {
	if !safeID(tenantID) || len(country) != 2 || (platform != PlatformAndroid && platform != PlatformIOS) || parseVersion(appVersion) == nil {
		return Bootstrap{}, ErrInvalidSnapshot
	}
	snapshot, err := service.repository.Get(ctx, tenantID, country)
	if err != nil {
		return Bootstrap{}, err
	}
	minimum := snapshot.MinimumVersions[platform]
	latest := snapshot.LatestVersions[platform]
	gate := UpdateNone
	if compareVersion(appVersion, minimum) < 0 {
		gate = UpdateRequired
	} else if compareVersion(appVersion, latest) < 0 {
		gate = UpdateOptional
	}
	locale := requestedLocale
	if !containsUnsorted(snapshot.SupportedLocales, locale) {
		locale = snapshot.DefaultLocale
	}
	now := service.clock().UTC()
	maintenance := false
	var maintenanceUntil *time.Time
	maintenanceText := ""
	if window := snapshot.MaintenanceWindow; window != nil && !now.Before(window.StartsAt) && now.Before(window.EndsAt) {
		maintenance = true
		end := window.EndsAt
		maintenanceUntil = &end
		maintenanceText = window.Message
	}
	sections := append([]HomeSection(nil), snapshot.HomeSections...)
	sort.SliceStable(sections, func(left, right int) bool { return sections[left].Priority < sections[right].Priority })
	return Bootstrap{
		Revision: snapshot.Revision, PublishedAt: snapshot.PublishedAt, UpdateGate: gate, LatestVersion: latest,
		Maintenance: maintenance, MaintenanceUntil: maintenanceUntil, MaintenanceText: maintenanceText,
		Locale: locale, SupportedLocales: append([]string(nil), snapshot.SupportedLocales...),
		ConsentPolicies: append([]ConsentPolicy(nil), snapshot.ConsentPolicies...), Flags: cloneMap(snapshot.Flags), HomeSections: sections, Pages: publishedPages(snapshot.Pages),
	}, nil
}

func (service *Service) Page(ctx context.Context, tenantID, country, pageID, requestedLocale string) (PageDocument, error) {
	if !safeID(tenantID) || len(country) != 2 || !safeID(pageID) {
		return PageDocument{}, ErrInvalidSnapshot
	}
	snapshot, err := service.repository.Get(ctx, tenantID, country)
	if err != nil {
		return PageDocument{}, err
	}
	locale := requestedLocale
	if !containsUnsorted(snapshot.SupportedLocales, locale) {
		locale = snapshot.DefaultLocale
	}
	for _, page := range snapshot.Pages {
		if page.ID == pageID && page.Enabled {
			pages := publishedPages([]Page{page})
			return PageDocument{Revision: snapshot.Revision, PublishedAt: snapshot.PublishedAt, Locale: locale, Page: pages[0]}, nil
		}
	}
	return PageDocument{}, ErrNotFound
}

func publishedPages(source []Page) []Page {
	result := make([]Page, 0, len(source))
	for _, page := range source {
		if !page.Enabled {
			continue
		}
		cloned := cloneSnapshot(Snapshot{Pages: []Page{page}}).Pages[0]
		cloned.Blocks = cloned.Blocks[:0]
		for _, block := range page.Blocks {
			if block.Enabled {
				block.Content = cloneJSONMap(block.Content)
				cloned.Blocks = append(cloned.Blocks, block)
			}
		}
		sort.SliceStable(cloned.Blocks, func(left, right int) bool { return cloned.Blocks[left].Priority < cloned.Blocks[right].Priority })
		result = append(result, cloned)
	}
	return result
}

type Publisher struct {
	repository WritableRepository
	cache      *CachedRepository
	audit      AuditSink
	clock      func() time.Time
}

func NewPublisher(repository WritableRepository, cache *CachedRepository, audit AuditSink, clock func() time.Time) (*Publisher, error) {
	_, atomic := repository.(AtomicPublicationRepository)
	if repository == nil || cache == nil || (!atomic && audit == nil) || clock == nil {
		return nil, fmt.Errorf("invalid configuration publisher")
	}
	return &Publisher{repository: repository, cache: cache, audit: audit, clock: clock}, nil
}

func (publisher *Publisher) Publish(ctx context.Context, snapshot Snapshot, expectedRevision int64, actorID, reason string) error {
	if !safeID(actorID) || strings.TrimSpace(reason) == "" || len(reason) > 240 {
		return ErrInvalidSnapshot
	}
	record := AuditRecord{
		EventID: newID(), ActorID: actorID, TenantID: snapshot.TenantID, Country: snapshot.Country,
		PreviousRevision: expectedRevision, NewRevision: snapshot.Revision, Reason: reason, RecordedAt: publisher.clock().UTC(),
	}
	if repository, ok := publisher.repository.(AtomicPublicationRepository); ok {
		if err := repository.PublishWithAudit(ctx, snapshot, expectedRevision, record); err != nil {
			return err
		}
	} else {
		if err := publisher.repository.Publish(ctx, snapshot, expectedRevision); err != nil {
			return err
		}
		if err := publisher.audit.Append(ctx, record); err != nil {
			return fmt.Errorf("append configuration audit: %w", err)
		}
	}
	publisher.cache.Invalidate(snapshot.TenantID, snapshot.Country)
	return nil
}

func parseVersion(value string) []int {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return nil
	}
	result := make([]int, 3)
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 || parsed > 999999 || (len(part) > 1 && strings.HasPrefix(part, "0")) {
			return nil
		}
		result[index] = parsed
	}
	return result
}

func compareVersion(left, right string) int {
	leftParts, rightParts := parseVersion(left), parseVersion(right)
	if leftParts == nil || rightParts == nil {
		return 1
	}
	for index := range leftParts {
		if leftParts[index] < rightParts[index] {
			return -1
		}
		if leftParts[index] > rightParts[index] {
			return 1
		}
	}
	return 0
}

func newID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("audit-%d", time.Now().UnixNano())
	}
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
