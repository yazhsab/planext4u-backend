package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrQuestionLimit = fmt.Errorf("question submission limit reached")

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
	PostalCodes                        []string
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
		for _, postalCode := range zone.PostalCodes {
			if strings.TrimSpace(postalCode) == "" || len(postalCode) > 12 {
				return nil, fmt.Errorf("invalid serviceability postal code")
			}
		}
	}
	return &Service{repository: repository, zones: append([]Zone(nil), zones...), staleTTL: staleTTL, clock: clock,
		itemCache: map[string]cachedItems{}, categoryCache: map[string]cachedCategories{}}, nil
}

func (service *Service) Geocode(country, query string) ([]GeocodeCandidate, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if len(country) != 2 || len(query) < 2 || len(query) > 100 {
		return nil, ErrInvalidRequest
	}
	result := []GeocodeCandidate{}
	for _, zone := range service.zones {
		if zone.Country != country {
			continue
		}
		postalCodes := zone.PostalCodes
		if len(postalCodes) == 0 {
			postalCodes = []string{""}
		}
		for index, postalCode := range postalCodes {
			if !strings.Contains(strings.ToLower(zone.Locality), query) && !strings.Contains(strings.ToLower(postalCode), query) {
				continue
			}
			label := zone.Locality
			if postalCode != "" {
				label += " · " + postalCode
			}
			result = append(result, GeocodeCandidate{
				ID: fmt.Sprintf("%s-%d", zone.ID, index), Label: label, Locality: zone.Locality, PostalCode: postalCode,
				Latitude:  (zone.MinimumLatitude + zone.MaximumLatitude) / 2,
				Longitude: (zone.MinimumLongitude + zone.MaximumLongitude) / 2,
			})
			if len(result) == 10 {
				return result, nil
			}
		}
	}
	return result, nil
}

func (service *Service) Suggestions(ctx context.Context, tenantID, country, query string) ([]Suggestion, ProjectionStatus, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if len(query) > 100 {
		return nil, "", ErrInvalidRequest
	}
	items, status, _ := service.loadItems(ctx, tenantID, country)
	result := []Suggestion{}
	seen := map[string]bool{}
	appendSuggestion := func(value Suggestion) {
		key := value.Type + "\x00" + strings.ToLower(value.Label)
		if !seen[key] && len(result) < 12 {
			seen[key] = true
			result = append(result, value)
		}
	}
	if query == "" {
		sort.SliceStable(items, func(left, right int) bool {
			if items[left].ReviewCount == items[right].ReviewCount {
				return items[left].RatingAverage > items[right].RatingAverage
			}
			return items[left].ReviewCount > items[right].ReviewCount
		})
		for _, item := range items {
			appendSuggestion(Suggestion{Type: "TRENDING", ID: "trending-" + item.ID, Label: item.Name, Subtitle: item.SellerName, ItemID: item.ID})
		}
		return result, status, nil
	}
	for _, item := range items {
		if matches(item, query) {
			appendSuggestion(Suggestion{Type: "PRODUCT", ID: "product-" + item.ID, Label: item.Name, Subtitle: item.SellerName, ItemID: item.ID})
		}
		if strings.Contains(strings.ToLower(item.SellerName), query) {
			appendSuggestion(Suggestion{Type: "VENDOR", ID: "vendor-" + item.ID, Label: item.SellerName, Subtitle: "Local seller"})
		}
		for index, term := range item.SearchTerms {
			if strings.Contains(strings.ToLower(term), query) {
				appendSuggestion(Suggestion{Type: "TAG", ID: fmt.Sprintf("tag-%s-%d", item.ID, index), Label: term, Subtitle: "Search tag"})
			}
		}
	}
	return result, status, nil
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

func (service *Service) AskQuestion(ctx context.Context, tenantID, country, subjectID, itemID, idempotencyKey, body string) (Question, error) {
	body = strings.TrimSpace(body)
	if !safeID(tenantID) || len(country) != 2 || !safeID(subjectID) || !safeID(itemID) || !safeID(idempotencyKey) || len(body) < 5 || len(body) > 500 {
		return Question{}, ErrInvalidRequest
	}
	digest := sha256.Sum256([]byte(tenantID + "\x00" + subjectID + "\x00" + idempotencyKey))
	questionID := "question-" + hex.EncodeToString(digest[:12])
	items, _, _ := service.loadItems(ctx, tenantID, country)
	found := false
	pending := 0
	for _, item := range items {
		if item.ID != itemID {
			continue
		}
		found = true
		for _, question := range item.Questions {
			if question.ID == questionID {
				if question.Question != body {
					return Question{}, ErrIdempotencyConflict
				}
				return question, nil
			}
			if question.AskedByID == subjectID && question.Answer == "" {
				pending++
			}
		}
		break
	}
	if !found {
		return Question{}, ErrNotFound
	}
	if pending >= 5 {
		return Question{}, ErrQuestionLimit
	}
	question := Question{
		ID: questionID, Question: body, AskedBy: "Planext4u customer",
		AskedByID: subjectID, AskedAt: service.clock().UTC(),
	}
	created, err := service.repository.AddQuestion(ctx, tenantID, country, itemID, question)
	if err != nil {
		return Question{}, err
	}
	service.mu.Lock()
	delete(service.itemCache, scopeKey(tenantID, country))
	service.mu.Unlock()
	return created, nil
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
	recommendations := cloneItems(items.Items)
	sort.SliceStable(recommendations, func(left, right int) bool {
		if recommendations[left].RatingAverage == recommendations[right].RatingAverage {
			return recommendations[left].ReviewCount > recommendations[right].ReviewCount
		}
		return recommendations[left].RatingAverage > recommendations[right].RatingAverage
	})
	if len(recommendations) > 6 {
		recommendations = recommendations[:6]
	}
	leadersBySeller := map[string]SellerLeader{}
	for _, item := range items.Items {
		if strings.TrimSpace(item.SellerName) == "" {
			continue
		}
		current := leadersBySeller[item.SellerName]
		if item.ReviewCount > current.ReviewCount || (item.ReviewCount == current.ReviewCount && item.RatingAverage > current.RatingAverage) {
			leadersBySeller[item.SellerName] = SellerLeader{SellerName: item.SellerName, Verified: item.VerifiedLocalSeller, RatingAverage: item.RatingAverage, ReviewCount: item.ReviewCount}
		}
	}
	leaders := make([]SellerLeader, 0, len(leadersBySeller))
	for _, value := range leadersBySeller {
		leaders = append(leaders, value)
	}
	sort.SliceStable(leaders, func(left, right int) bool {
		if leaders[left].RatingAverage == leaders[right].RatingAverage {
			return leaders[left].ReviewCount > leaders[right].ReviewCount
		}
		return leaders[left].RatingAverage > leaders[right].RatingAverage
	})
	return Home{
		Categories: categories.Items, FeaturedItems: items.Items, Recommendations: recommendations, Leaderboard: leaders,
		HelpShortcuts:    []HelpShortcut{{ID: "orders", Title: "Order help", Route: "/app/orders"}, {ID: "payments", Title: "Payment help", Route: "/app/orders"}, {ID: "browse", Title: "Browse categories", Route: "/app/catalog"}},
		ProjectionStatus: status, GeneratedAt: generatedAt,
	}, nil
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
