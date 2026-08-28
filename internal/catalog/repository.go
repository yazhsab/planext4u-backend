package catalog

import (
	"context"
	"errors"
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
	mu         sync.RWMutex
	categories []Category
	items      []Item
	err        error
}

func NewMemoryRepository(categories []Category, items []Item) (*MemoryRepository, error) {
	if len(categories) == 0 || len(categories) > 1000 || len(items) > 100000 {
		return nil, ErrInvalidRequest
	}
	categoryIDs := map[string]struct{}{}
	for _, category := range categories {
		if !safeID(category.ID) || strings.TrimSpace(category.Name) == "" || category.Priority < 0 {
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
		if len(item.MediaRefs) > 20 || len(item.Reviews) > 100 || len(item.Questions) > 100 || len(item.RelatedItemIDs) > 50 || len(item.DeliveryEstimate) > 240 {
			return nil, ErrInvalidRequest
		}
		for _, value := range item.MediaRefs {
			if !validMediaReference(value) {
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
	result := &MemoryRepository{categories: cloneCategories(categories), items: cloneItems(items)}
	sort.SliceStable(result.categories, func(i, j int) bool {
		if result.categories[i].Priority == result.categories[j].Priority {
			return result.categories[i].ID < result.categories[j].ID
		}
		return result.categories[i].Priority < result.categories[j].Priority
	})
	sort.SliceStable(result.items, func(i, j int) bool { return result.items[i].ID < result.items[j].ID })
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

func cloneCategories(values []Category) []Category { return append([]Category(nil), values...) }

func cloneItems(values []Item) []Item {
	result := append([]Item(nil), values...)
	for index := range result {
		result[index].SearchTerms = append([]string(nil), result[index].SearchTerms...)
		result[index].MediaRefs = append([]string(nil), result[index].MediaRefs...)
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
