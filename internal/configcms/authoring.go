package configcms

import (
	"context"
	"sort"
	"sync"
	"time"
)

type DraftRepository interface {
	ListPageDrafts(context.Context, string, string) ([]PageDraft, error)
	GetPageDraft(context.Context, string, string, string) (PageDraft, error)
	SavePageDraft(context.Context, PageDraft, int64) error
}

type MemoryDraftRepository struct {
	mu     sync.RWMutex
	drafts map[string]PageDraft
}

func NewMemoryDraftRepository() *MemoryDraftRepository {
	return &MemoryDraftRepository{drafts: map[string]PageDraft{}}
}

func (repository *MemoryDraftRepository) ListPageDrafts(_ context.Context, tenantID, country string) ([]PageDraft, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := []PageDraft{}
	for _, draft := range repository.drafts {
		if draft.TenantID == tenantID && draft.Country == country {
			result = append(result, clonePageDraft(draft))
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Page.ID < result[right].Page.ID })
	return result, nil
}

func (repository *MemoryDraftRepository) GetPageDraft(_ context.Context, tenantID, country, pageID string) (PageDraft, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	draft, exists := repository.drafts[draftKey(tenantID, country, pageID)]
	if !exists {
		return PageDraft{}, ErrNotFound
	}
	return clonePageDraft(draft), nil
}

func (repository *MemoryDraftRepository) SavePageDraft(_ context.Context, draft PageDraft, expectedRevision int64) error {
	if !validPageDraft(draft) || expectedRevision < 0 || draft.Revision != expectedRevision+1 {
		return ErrInvalidSnapshot
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := draftKey(draft.TenantID, draft.Country, draft.Page.ID)
	current, exists := repository.drafts[key]
	if (!exists && expectedRevision != 0) || (exists && current.Revision != expectedRevision) {
		return ErrRevisionConflict
	}
	repository.drafts[key] = clonePageDraft(draft)
	return nil
}

type AuthoringService struct {
	repository DraftRepository
	clock      func() time.Time
}

func NewAuthoringService(repository DraftRepository, clock func() time.Time) (*AuthoringService, error) {
	if repository == nil || clock == nil {
		return nil, ErrInvalidRepository
	}
	return &AuthoringService{repository: repository, clock: clock}, nil
}

func (service *AuthoringService) List(ctx context.Context, tenantID, country string) ([]PageDraft, error) {
	if !safeID(tenantID) || len(country) != 2 {
		return nil, ErrInvalidSnapshot
	}
	return service.repository.ListPageDrafts(ctx, tenantID, country)
}

func (service *AuthoringService) Save(ctx context.Context, tenantID, country, actorID string, page Page, expectedRevision int64) (PageDraft, error) {
	value := PageDraft{TenantID: tenantID, Country: country, Page: page, Revision: expectedRevision + 1, UpdatedBy: actorID, UpdatedAt: service.clock().UTC()}
	if !validPageDraft(value) || expectedRevision < 0 {
		return PageDraft{}, ErrInvalidSnapshot
	}
	if err := service.repository.SavePageDraft(ctx, value, expectedRevision); err != nil {
		return PageDraft{}, err
	}
	return clonePageDraft(value), nil
}

func validPageDraft(draft PageDraft) bool {
	if !safeID(draft.TenantID) || len(draft.Country) != 2 || !safeID(draft.UpdatedBy) || draft.Revision < 1 || draft.UpdatedAt.IsZero() {
		return false
	}
	return validatePages([]Page{draft.Page}) == nil
}

func validatePages(pages []Page) error {
	snapshot := Snapshot{
		TenantID: "validation", Country: "IN", Revision: 1, PublishedAt: time.Unix(1, 0).UTC(),
		MinimumVersions: map[Platform]string{PlatformAndroid: "1.0.0", PlatformIOS: "1.0.0", PlatformWeb: "1.0.0"}, LatestVersions: map[Platform]string{PlatformAndroid: "1.0.0", PlatformIOS: "1.0.0", PlatformWeb: "1.0.0"},
		SupportedLocales: []string{"en"}, DefaultLocale: "en", Flags: map[string]bool{}, Pages: pages,
	}
	return validateSnapshot(snapshot)
}

func clonePageDraft(draft PageDraft) PageDraft {
	cloned := cloneSnapshot(Snapshot{Pages: []Page{draft.Page}}).Pages[0]
	draft.Page = cloned
	return draft
}

func draftKey(tenantID, country, pageID string) string {
	return tenantID + "\x00" + country + "\x00" + pageID
}
