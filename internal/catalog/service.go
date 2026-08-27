package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type cachedItems struct {
	values      []Item
	generatedAt time.Time
}
type cachedCategories struct {
	values      []Category
	generatedAt time.Time
}

type Service struct {
	repository    Repository
	clock         func() time.Time
	staleTTL      time.Duration
	mu            sync.RWMutex
	itemCache     map[string]cachedItems
	categoryCache map[string]cachedCategories
	zones         []Zone
}

type Zone struct {
	ID, Country, Locality              string
	MinimumLatitude, MaximumLatitude   float64
	MinimumLongitude, MaximumLongitude float64
}

func NewService(repository Repository, zones []Zone, staleTTL time.Duration, clock func() time.Time) (*Service, error) {
	if repository == nil || clock == nil || staleTTL <= 0 || staleTTL > 24*time.Hour {
		return nil, fmt.Errorf("invalid catalog service")
	}
	for _, zone := range zones {
		if !safeID(zone.ID) || len(zone.Country) != 2 || strings.TrimSpace(zone.Locality) == "" ||
			zone.MinimumLatitude < -90 || zone.MaximumLatitude > 90 || zone.MinimumLatitude > zone.MaximumLatitude ||
			zone.MinimumLongitude < -180 || zone.MaximumLongitude > 180 || zone.MinimumLongitude > zone.MaximumLongitude {
			return nil, fmt.Errorf("invalid serviceability zone")
		}
	}
	return &Service{repository: repository, zones: append([]Zone(nil), zones...), staleTTL: staleTTL, clock: clock,
		itemCache: map[string]cachedItems{}, categoryCache: map[string]cachedCategories{}}, nil
}

func (service *Service) Categories(ctx context.Context, tenantID, country string) Page[Category] {
	now := service.clock().UTC()
	values, err := service.repository.Categories(ctx, tenantID, country)
	key := scopeKey(tenantID, country)
	status, generatedAt := ProjectionFresh, now
	if err == nil {
		service.mu.Lock()
		service.categoryCache[key] = cachedCategories{values: cloneCategories(values), generatedAt: now}
		service.mu.Unlock()
	} else {
		service.mu.RLock()
		cached, found := service.categoryCache[key]
		service.mu.RUnlock()
		if !found || now.Sub(cached.generatedAt) > service.staleTTL {
			return Page[Category]{Items: []Category{}, ProjectionStatus: ProjectionDegraded, GeneratedAt: now}
		}
		values, status, generatedAt = cloneCategories(cached.values), ProjectionStale, cached.generatedAt
	}
	return Page[Category]{Items: values, ProjectionStatus: status, GeneratedAt: generatedAt}
}

func (service *Service) Items(ctx context.Context, tenantID, country, categoryID, query, cursor string, limit int) (Page[Item], error) {
	if limit < 1 || limit > 50 || (categoryID != "" && !safeID(categoryID)) || len(query) > 100 {
		return Page[Item]{}, ErrInvalidRequest
	}
	values, status, generatedAt := service.loadItems(ctx, tenantID, country)
	query = strings.ToLower(strings.TrimSpace(query))
	filtered := make([]Item, 0, len(values))
	for _, item := range values {
		if categoryID != "" && item.CategoryID != categoryID {
			continue
		}
		if query != "" && !matches(item, query) {
			continue
		}
		filtered = append(filtered, item)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	after, err := decodeCursor(cursor)
	if err != nil {
		return Page[Item]{}, err
	}
	start := sort.Search(len(filtered), func(index int) bool { return filtered[index].ID > after })
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	pageValues := cloneItems(filtered[start:end])
	hasMore := end < len(filtered)
	next := ""
	if hasMore && len(pageValues) > 0 {
		next = encodeCursor(pageValues[len(pageValues)-1].ID)
	}
	return Page[Item]{Items: pageValues, NextCursor: next, HasMore: hasMore, ProjectionStatus: status, GeneratedAt: generatedAt}, nil
}

func (service *Service) Item(ctx context.Context, tenantID, country, itemID string) (Item, ProjectionStatus, error) {
	if !safeID(itemID) {
		return Item{}, "", ErrInvalidRequest
	}
	items, status, _ := service.loadItems(ctx, tenantID, country)
	for _, item := range items {
		if item.ID == itemID {
			return item, status, nil
		}
	}
	return Item{}, status, ErrNotFound
}

func (service *Service) loadItems(ctx context.Context, tenantID, country string) ([]Item, ProjectionStatus, time.Time) {
	now := service.clock().UTC()
	values, err := service.repository.Items(ctx, tenantID, country)
	key := scopeKey(tenantID, country)
	if err == nil {
		service.mu.Lock()
		service.itemCache[key] = cachedItems{values: cloneItems(values), generatedAt: now}
		service.mu.Unlock()
		return values, ProjectionFresh, now
	}
	service.mu.RLock()
	cached, found := service.itemCache[key]
	service.mu.RUnlock()
	if !found || now.Sub(cached.generatedAt) > service.staleTTL {
		return []Item{}, ProjectionDegraded, now
	}
	return cloneItems(cached.values), ProjectionStale, cached.generatedAt
}

func (service *Service) Home(ctx context.Context, tenantID, country string) (Home, error) {
	categories := service.Categories(ctx, tenantID, country)
	items, err := service.Items(ctx, tenantID, country, "", "", "", 12)
	if err != nil {
		return Home{}, err
	}
	status := worstStatus(categories.ProjectionStatus, items.ProjectionStatus)
	generatedAt := categories.GeneratedAt
	if items.GeneratedAt.Before(generatedAt) {
		generatedAt = items.GeneratedAt
	}
	return Home{Categories: categories.Items, FeaturedItems: items.Items, ProjectionStatus: status, GeneratedAt: generatedAt}, nil
}

func (service *Service) CheckServiceability(country string, point GeoPoint) (Serviceability, error) {
	if len(country) != 2 || math.IsNaN(point.Latitude) || math.IsNaN(point.Longitude) || point.Latitude < -90 || point.Latitude > 90 ||
		point.Longitude < -180 || point.Longitude > 180 || point.AccuracyMetres < 0 || point.AccuracyMetres > 50000 ||
		point.CapturedAt.IsZero() || point.Purpose != "LOCATION_SERVICEABILITY" {
		return Serviceability{}, ErrInvalidRequest
	}
	for _, zone := range service.zones {
		if zone.Country == country && point.Latitude >= zone.MinimumLatitude && point.Latitude <= zone.MaximumLatitude &&
			point.Longitude >= zone.MinimumLongitude && point.Longitude <= zone.MaximumLongitude {
			return Serviceability{Serviceable: true, ZoneID: zone.ID, Locality: zone.Locality, ReasonCode: "SERVICEABLE"}, nil
		}
	}
	return Serviceability{Serviceable: false, ReasonCode: "OUTSIDE_SERVICE_AREA"}, nil
}

func matches(item Item, query string) bool {
	if strings.Contains(strings.ToLower(item.Name), query) || strings.Contains(strings.ToLower(item.Summary), query) {
		return true
	}
	for _, term := range item.SearchTerms {
		if strings.Contains(strings.ToLower(term), query) {
			return true
		}
	}
	return false
}

func worstStatus(left, right ProjectionStatus) ProjectionStatus {
	if left == ProjectionDegraded || right == ProjectionDegraded {
		return ProjectionDegraded
	}
	if left == ProjectionStale || right == ProjectionStale {
		return ProjectionStale
	}
	return ProjectionFresh
}

func scopeKey(tenantID, country string) string { return tenantID + "\x00" + country }

func encodeCursor(after string) string {
	encoded, _ := json.Marshal(map[string]string{"v": "1", "after": after})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 512 {
		return "", ErrInvalidRequest
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", ErrInvalidRequest
	}
	var envelope map[string]string
	if json.Unmarshal(decoded, &envelope) != nil || envelope["v"] != "1" || !safeID(envelope["after"]) || len(envelope) != 2 {
		return "", ErrInvalidRequest
	}
	return envelope["after"], nil
}
