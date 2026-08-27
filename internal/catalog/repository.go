package catalog

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

var (
	ErrNotFound       = errors.New("catalog item not found")
	ErrInvalidRequest = errors.New("invalid catalog request")
)

type Repository interface {
	Categories(context.Context, string, string) ([]Category, error)
	Items(context.Context, string, string) ([]Item, error)
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
			item.Price.AmountMinor < 0 || len(item.Price.Currency) != 3 {
			return nil, ErrInvalidRequest
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
	}
	return result
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
