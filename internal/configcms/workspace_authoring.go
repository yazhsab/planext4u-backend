package configcms

import (
	"context"
	"reflect"
	"sync"
	"time"
)

type WorkspaceDraftRepository interface {
	GetWorkspaceDraft(context.Context, string, string) (WorkspaceDraft, error)
	SaveWorkspaceDraft(context.Context, WorkspaceDraft, int64) error
}

type MemoryWorkspaceDraftRepository struct {
	mu     sync.RWMutex
	drafts map[string]WorkspaceDraft
}

func NewMemoryWorkspaceDraftRepository() *MemoryWorkspaceDraftRepository {
	return &MemoryWorkspaceDraftRepository{drafts: map[string]WorkspaceDraft{}}
}

func (repository *MemoryWorkspaceDraftRepository) GetWorkspaceDraft(_ context.Context, tenantID, country string) (WorkspaceDraft, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	value, found := repository.drafts[snapshotKey(tenantID, country)]
	if !found {
		return WorkspaceDraft{}, ErrNotFound
	}
	return cloneWorkspaceDraft(value), nil
}

func (repository *MemoryWorkspaceDraftRepository) SaveWorkspaceDraft(_ context.Context, draft WorkspaceDraft, expectedRevision int64) error {
	if !validWorkspaceDraft(draft) || expectedRevision < 0 || draft.Revision != expectedRevision+1 {
		return ErrInvalidSnapshot
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	key := snapshotKey(draft.TenantID, draft.Country)
	current, found := repository.drafts[key]
	if (!found && expectedRevision != 0) || (found && current.Revision != expectedRevision) {
		return ErrRevisionConflict
	}
	repository.drafts[key] = cloneWorkspaceDraft(draft)
	return nil
}

type WorkspaceAuthoringService struct {
	repository WorkspaceDraftRepository
	clock      func() time.Time
}

func NewWorkspaceAuthoringService(repository WorkspaceDraftRepository, clock func() time.Time) (*WorkspaceAuthoringService, error) {
	if repository == nil || clock == nil {
		return nil, ErrInvalidRepository
	}
	return &WorkspaceAuthoringService{repository: repository, clock: clock}, nil
}

func (service *WorkspaceAuthoringService) Get(ctx context.Context, tenantID, country string) (WorkspaceDraft, error) {
	if !safeID(tenantID) || len(country) != 2 {
		return WorkspaceDraft{}, ErrInvalidSnapshot
	}
	return service.repository.GetWorkspaceDraft(ctx, tenantID, country)
}

func (service *WorkspaceAuthoringService) Save(ctx context.Context, tenantID, country, actorID string, workspace Workspace, expectedRevision int64) (WorkspaceDraft, error) {
	value := WorkspaceDraft{TenantID: tenantID, Country: country, Workspace: workspace, Revision: expectedRevision + 1, UpdatedBy: actorID, UpdatedAt: service.clock().UTC()}
	if !validWorkspaceDraft(value) || expectedRevision < 0 {
		return WorkspaceDraft{}, ErrInvalidSnapshot
	}
	if err := service.repository.SaveWorkspaceDraft(ctx, value, expectedRevision); err != nil {
		return WorkspaceDraft{}, err
	}
	return cloneWorkspaceDraft(value), nil
}

func validWorkspaceDraft(draft WorkspaceDraft) bool {
	if !safeID(draft.TenantID) || len(draft.Country) != 2 || !safeID(draft.UpdatedBy) || draft.Revision < 1 || draft.UpdatedAt.IsZero() {
		return false
	}
	snapshot := Snapshot{TenantID: draft.TenantID, Country: draft.Country, Revision: 1, PublishedAt: draft.UpdatedAt, Pages: []Page{}}
	applyWorkspace(&snapshot, draft.Workspace)
	return validateSnapshot(snapshot) == nil
}

func workspaceFromSnapshot(snapshot Snapshot) Workspace {
	return cloneWorkspace(Workspace{
		MinimumVersions: snapshot.MinimumVersions, LatestVersions: snapshot.LatestVersions,
		SupportedLocales: snapshot.SupportedLocales, DefaultLocale: snapshot.DefaultLocale,
		ConsentPolicies: snapshot.ConsentPolicies, Flags: snapshot.Flags, HomeSections: snapshot.HomeSections,
		MaintenanceWindow: snapshot.MaintenanceWindow,
	})
}

func applyWorkspace(snapshot *Snapshot, workspace Workspace) {
	cloned := cloneWorkspace(workspace)
	snapshot.MinimumVersions, snapshot.LatestVersions = cloned.MinimumVersions, cloned.LatestVersions
	snapshot.SupportedLocales, snapshot.DefaultLocale = cloned.SupportedLocales, cloned.DefaultLocale
	snapshot.ConsentPolicies, snapshot.Flags, snapshot.HomeSections = cloned.ConsentPolicies, cloned.Flags, cloned.HomeSections
	snapshot.MaintenanceWindow = cloned.MaintenanceWindow
}

func workspaceMatches(snapshot Snapshot, workspace Workspace) bool {
	return reflect.DeepEqual(workspaceFromSnapshot(snapshot), cloneWorkspace(workspace))
}

func cloneWorkspace(value Workspace) Workspace {
	snapshot := Snapshot{
		MinimumVersions: value.MinimumVersions, LatestVersions: value.LatestVersions,
		SupportedLocales: value.SupportedLocales, DefaultLocale: value.DefaultLocale,
		ConsentPolicies: value.ConsentPolicies, Flags: value.Flags, HomeSections: value.HomeSections,
		MaintenanceWindow: value.MaintenanceWindow,
	}
	cloned := cloneSnapshot(snapshot)
	return Workspace{
		MinimumVersions: cloned.MinimumVersions, LatestVersions: cloned.LatestVersions,
		SupportedLocales: cloned.SupportedLocales, DefaultLocale: cloned.DefaultLocale,
		ConsentPolicies: cloned.ConsentPolicies, Flags: cloned.Flags, HomeSections: cloned.HomeSections,
		MaintenanceWindow: cloned.MaintenanceWindow,
	}
}

func cloneWorkspaceDraft(value WorkspaceDraft) WorkspaceDraft {
	value.Workspace = cloneWorkspace(value.Workspace)
	return value
}
