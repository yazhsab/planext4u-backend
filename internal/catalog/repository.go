package catalog

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
)

var (
	ErrNotFound            = errors.New("catalog item not found")
	ErrInvalidRequest      = errors.New("invalid catalog request")
	ErrIdempotencyConflict = errors.New("idempotency key was reused with different input")
)

type Repository interface {
	Categories(context.Context, string, string) ([]Category, error)
	Items(context.Context, string, string) ([]Item, error)
	ServiceCollections(context.Context, string, string, string) ([]ServiceCollection, error)
	AddQuestion(context.Context, string, string, string, Question) (Question, error)
}

func (repository *MemoryRepository) AddQuestion(_ context.Context, _, _, itemID string, question Question) (Question, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.err != nil {
		return Question{}, repository.err
	}
	for itemIndex := range repository.items {
		if repository.items[itemIndex].ID != itemID {
			continue
		}
		for _, existing := range repository.items[itemIndex].Questions {
			if existing.ID == question.ID {
				if existing.Question != question.Question || existing.AskedByID != question.AskedByID {
					return Question{}, ErrIdempotencyConflict
				}
				return existing, nil
			}
		}
		pending := 0
		for _, existing := range repository.items[itemIndex].Questions {
			if existing.AskedByID == question.AskedByID && existing.Answer == "" {
				pending++
			}
		}
		if pending >= 5 {
			return Question{}, ErrQuestionLimit
		}
		repository.items[itemIndex].Questions = append(repository.items[itemIndex].Questions, question)
		return question, nil
	}
	return Question{}, ErrNotFound
}

type MemoryRepository struct {
	mu                 sync.RWMutex
	categories         []Category
	items              []Item
	serviceCollections []ServiceCollectionProjection
	err                error
}

func NewMemoryRepository(categories []Category, items []Item) (*MemoryRepository, error) {
	return NewMemoryRepositoryWithServiceCollections(categories, items, nil)
}

func NewMemoryRepositoryWithServiceCollections(categories []Category, items []Item, projections []ServiceCollectionProjection) (*MemoryRepository, error) {
	if len(categories) == 0 || len(categories) > 1000 || len(items) > 100000 {
		return nil, ErrInvalidRequest
	}
	categoryIDs := map[string]struct{}{}
	for _, category := range categories {
		if !safeID(category.ID) || strings.TrimSpace(category.Name) == "" || category.Priority < 0 || (category.Icon != nil && !validMediaPresentation(*category.Icon)) {
			return nil, ErrInvalidRequest
		}
		categoryIDs[category.ID] = struct{}{}
	}
	itemIDs := map[string]struct{}{}
	for _, item := range items {
		if !safeID(item.ID) || !safeID(item.CategoryID) || strings.TrimSpace(item.Name) == "" ||
			item.Price.AmountMinor < 0 || len(item.Price.Currency) != 3 || item.RatingAverage < 0 || item.RatingAverage > 5 || item.ReviewCount < 0 {
			return nil, ErrInvalidRequest
		}
		if len(item.MediaRefs) > 20 || len(item.Media) > 20 || len(item.Reviews) > 100 || len(item.Questions) > 100 || len(item.RelatedItemIDs) > 50 || len(item.DeliveryEstimate) > 240 {
			return nil, ErrInvalidRequest
		}
		for _, value := range item.MediaRefs {
			if !validMediaReference(value) {
				return nil, ErrInvalidRequest
			}
		}
		for _, value := range item.Media {
			if !validMediaPresentation(value) {
				return nil, ErrInvalidRequest
			}
		}
		for _, review := range item.Reviews {
			if !safeID(review.ID) || strings.TrimSpace(review.AuthorDisplayName) == "" || review.Score < 1 || review.Score > 5 || strings.TrimSpace(review.Body) == "" || len(review.Body) > 4000 || review.CreatedAt.IsZero() {
				return nil, ErrInvalidRequest
			}
		}
		for _, question := range item.Questions {
			if !safeID(question.ID) || strings.TrimSpace(question.Question) == "" || strings.TrimSpace(question.AskedBy) == "" || question.AskedAt.IsZero() || (question.Answer == "") != (question.AnsweredAt == nil) || (question.Answer != "" && strings.TrimSpace(question.AnsweredBy) == "") {
				return nil, ErrInvalidRequest
			}
		}
		variantIDs := map[string]struct{}{}
		for _, variant := range item.Variants {
			if !safeID(variant.ID) || strings.TrimSpace(variant.Label) == "" || variant.Price.AmountMinor < 0 || len(variant.Price.Currency) != 3 ||
				variant.Price.Currency != item.Price.Currency || variant.StockQuantity < 0 || variant.MaxPerOrder < 1 || variant.MaxPerOrder > 999 {
				return nil, ErrInvalidRequest
			}
			if variant.CompareAtPrice != nil && (variant.CompareAtPrice.AmountMinor < variant.Price.AmountMinor || variant.CompareAtPrice.Currency != variant.Price.Currency) {
				return nil, ErrInvalidRequest
			}
			if _, duplicate := variantIDs[variant.ID]; duplicate {
				return nil, ErrInvalidRequest
			}
			variantIDs[variant.ID] = struct{}{}
		}
		if _, exists := categoryIDs[item.CategoryID]; !exists {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := itemIDs[item.ID]; duplicate {
			return nil, ErrInvalidRequest
		}
		itemIDs[item.ID] = struct{}{}
	}
	collectionIDs := map[string]struct{}{}
	for _, projection := range projections {
		if !validServiceCollection(projection.Collection) {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := collectionIDs[projection.Collection.CollectionID]; duplicate {
			return nil, ErrInvalidRequest
		}
		collectionIDs[projection.Collection.CollectionID] = struct{}{}
		for _, item := range projection.Collection.Items {
			postalCodes, found := projection.ServicePostalCodes[item.ServiceID]
			if !found || len(postalCodes) == 0 || len(postalCodes) > 10000 {
				return nil, ErrInvalidRequest
			}
			for _, postalCode := range postalCodes {
				if !validPostalCode(postalCode) {
					return nil, ErrInvalidRequest
				}
			}
		}
	}
	result := &MemoryRepository{categories: cloneCategories(categories), items: cloneItems(items), serviceCollections: cloneServiceCollectionProjections(projections)}
	sort.SliceStable(result.categories, func(i, j int) bool {
		if result.categories[i].Priority == result.categories[j].Priority {
			return result.categories[i].ID < result.categories[j].ID
		}
		return result.categories[i].Priority < result.categories[j].Priority
	})
	sort.SliceStable(result.items, func(i, j int) bool { return result.items[i].ID < result.items[j].ID })
	return result, nil
}

func (repository *MemoryRepository) ServiceCollections(_ context.Context, _, _, postalCode string) ([]ServiceCollection, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if repository.err != nil {
		return nil, repository.err
	}
	result := make([]ServiceCollection, 0, len(repository.serviceCollections))
	for _, projection := range repository.serviceCollections {
		collection := cloneServiceCollection(projection.Collection)
		for index := range collection.Items {
			collection.Items[index].Serviceable = postalCode != "" && containsString(projection.ServicePostalCodes[collection.Items[index].ServiceID], postalCode)
		}
		result = append(result, collection)
	}
	return result, nil
}

func (repository *MemoryRepository) Categories(_ context.Context, _, _ string) ([]Category, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if repository.err != nil {
		return nil, repository.err
	}
	return cloneCategories(repository.categories), nil
}

func (repository *MemoryRepository) Items(_ context.Context, _, _ string) ([]Item, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	if repository.err != nil {
		return nil, repository.err
	}
	return cloneItems(repository.items), nil
}

func (repository *MemoryRepository) SetError(err error) {
	repository.mu.Lock()
	repository.err = err
	repository.mu.Unlock()
}

func cloneCategories(values []Category) []Category {
	result := append([]Category(nil), values...)
	for index := range result {
		if values[index].Icon != nil {
			icon := cloneMediaPresentation(*values[index].Icon)
			result[index].Icon = &icon
		}
	}
	return result
}

func cloneItems(values []Item) []Item {
	result := append([]Item(nil), values...)
	for index := range result {
		result[index].SearchTerms = append([]string(nil), result[index].SearchTerms...)
		result[index].MediaRefs = append([]string(nil), result[index].MediaRefs...)
		result[index].Media = make([]MediaPresentation, len(values[index].Media))
		for mediaIndex := range values[index].Media {
			result[index].Media[mediaIndex] = cloneMediaPresentation(values[index].Media[mediaIndex])
		}
		result[index].Reviews = append([]Review(nil), result[index].Reviews...)
		result[index].Questions = append([]Question(nil), result[index].Questions...)
		for questionIndex := range result[index].Questions {
			if answeredAt := result[index].Questions[questionIndex].AnsweredAt; answeredAt != nil {
				copyTime := *answeredAt
				result[index].Questions[questionIndex].AnsweredAt = &copyTime
			}
		}
		result[index].RelatedItemIDs = append([]string(nil), result[index].RelatedItemIDs...)
		result[index].Variants = append([]Variant(nil), result[index].Variants...)
		for variantIndex := range result[index].Variants {
			if price := result[index].Variants[variantIndex].CompareAtPrice; price != nil {
				copyPrice := *price
				result[index].Variants[variantIndex].CompareAtPrice = &copyPrice
			}
		}
		if result[index].Specifications != nil {
			result[index].Specifications = make(map[string]string, len(values[index].Specifications))
			for key, value := range values[index].Specifications {
				result[index].Specifications[key] = value
			}
		}
	}
	return result
}

func cloneMediaPresentation(value MediaPresentation) MediaPresentation {
	value.Variants = append([]ResponsiveMediaVariant(nil), value.Variants...)
	if value.ExpiresAt != nil {
		expiresAt := *value.ExpiresAt
		value.ExpiresAt = &expiresAt
	}
	return value
}

func cloneServiceCollection(value ServiceCollection) ServiceCollection {
	value.Items = append([]ServiceCollectionItem(nil), value.Items...)
	for index := range value.Items {
		if value.Items[index].Media != nil {
			media := cloneMediaPresentation(*value.Items[index].Media)
			value.Items[index].Media = &media
		}
	}
	return value
}

func cloneServiceCollectionProjections(values []ServiceCollectionProjection) []ServiceCollectionProjection {
	result := make([]ServiceCollectionProjection, len(values))
	for index, value := range values {
		result[index].Collection = cloneServiceCollection(value.Collection)
		result[index].ServicePostalCodes = make(map[string][]string, len(value.ServicePostalCodes))
		for serviceID, postalCodes := range value.ServicePostalCodes {
			result[index].ServicePostalCodes[serviceID] = append([]string(nil), postalCodes...)
		}
	}
	return result
}

func validServiceCollection(value ServiceCollection) bool {
	if !safeID(value.CollectionID) || strings.TrimSpace(value.Title) == "" || len(value.Title) > 120 || len(value.Items) == 0 || len(value.Items) > 24 {
		return false
	}
	serviceIDs := map[string]struct{}{}
	for _, item := range value.Items {
		if !safeID(item.ServiceID) || !safeID(item.ProviderID) || strings.TrimSpace(item.Title) == "" || len(item.Title) > 120 || strings.TrimSpace(item.Summary) == "" || len(item.Summary) > 500 ||
			item.Price.AmountMinor < 0 || len(item.Price.Currency) != 3 || strings.TrimSpace(item.PriceDisplay) == "" || len(item.PriceDisplay) > 80 ||
			item.Trust.RatingAverage < 0 || item.Trust.RatingAverage > 5 || item.Trust.CompletedBookings < 0 ||
			item.NavigationTarget != "/app/services/"+item.ServiceID || (item.Media != nil && !validMediaPresentation(*item.Media)) {
			return false
		}
		if _, duplicate := serviceIDs[item.ServiceID]; duplicate {
			return false
		}
		serviceIDs[item.ServiceID] = struct{}{}
	}
	return true
}

func validPostalCode(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 3 && len(value) <= 12 && !strings.ContainsAny(value, "\r\n\t ")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validMediaPresentation(value MediaPresentation) bool {
	if !safeID(value.AssetID) || !safeMediaURL(value.URL) || !strings.HasPrefix(value.ContentType, "image/") ||
		value.Width < 1 || value.Width > 16384 || value.Height < 1 || value.Height > 16384 ||
		len(strings.TrimSpace(value.AltText)) < 1 || len(value.AltText) > 240 || len(value.Variants) > 10 {
		return false
	}
	for _, variant := range value.Variants {
		if !safeMediaURL(variant.URL) || variant.Width < 1 || variant.Width > 16384 || variant.Height < 1 || variant.Height > 16384 {
			return false
		}
	}
	return true
}

func safeMediaURL(value string) bool {
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.ContainsAny(value, "\r\n") {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && !strings.ContainsAny(value, "\r\n")
}

func validMediaReference(value string) bool {
	return strings.HasPrefix(value, "https://") && len(value) <= 2048 && !strings.ContainsAny(value, "\r\n")
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}
