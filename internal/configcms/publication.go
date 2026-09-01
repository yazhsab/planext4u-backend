package configcms

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/adminops"
)

type PublicationService struct {
	snapshots Repository
	drafts    DraftRepository
	workspace WorkspaceDraftRepository
	publisher *Publisher
	clock     func() time.Time
}

func NewPublicationService(snapshots Repository, drafts DraftRepository, publisher *Publisher, clock func() time.Time) (*PublicationService, error) {
	if snapshots == nil || drafts == nil || publisher == nil || clock == nil {
		return nil, ErrInvalidRepository
	}
	return &PublicationService{snapshots: snapshots, drafts: drafts, publisher: publisher, clock: clock}, nil
}

func NewPublicationServiceWithWorkspace(snapshots Repository, drafts DraftRepository, workspace WorkspaceDraftRepository, publisher *Publisher, clock func() time.Time) (*PublicationService, error) {
	if snapshots == nil || drafts == nil || workspace == nil || publisher == nil || clock == nil {
		return nil, ErrInvalidRepository
	}
	return &PublicationService{snapshots: snapshots, drafts: drafts, workspace: workspace, publisher: publisher, clock: clock}, nil
}

func (service *PublicationService) PublishWorkspace(ctx context.Context, tenantID, country string, expectedDraftRevision, expectedSnapshotRevision int64, actorID, reason string) error {
	if service.workspace == nil || !safeID(tenantID) || len(country) != 2 || expectedDraftRevision < 1 || expectedSnapshotRevision < 0 || !safeID(actorID) {
		return ErrInvalidSnapshot
	}
	draft, err := service.workspace.GetWorkspaceDraft(ctx, tenantID, country)
	if err != nil {
		return err
	}
	if draft.Revision != expectedDraftRevision {
		return ErrRevisionConflict
	}
	current, err := service.snapshots.Get(ctx, tenantID, country)
	if errors.Is(err, ErrNotFound) {
		if expectedSnapshotRevision != 0 {
			return ErrRevisionConflict
		}
		current = Snapshot{TenantID: tenantID, Country: country, Pages: []Page{}}
	} else if err != nil {
		return err
	}
	if current.Revision == expectedSnapshotRevision+1 && workspaceMatches(current, draft.Workspace) {
		return nil
	}
	if current.Revision != expectedSnapshotRevision {
		return ErrRevisionConflict
	}
	next := cloneSnapshot(current)
	applyWorkspace(&next, draft.Workspace)
	next.Revision, next.PublishedAt = expectedSnapshotRevision+1, service.clock().UTC()
	return service.publisher.Publish(ctx, next, expectedSnapshotRevision, actorID, reason)
}

func (service *PublicationService) PublishPage(ctx context.Context, tenantID, country, pageID string, expectedDraftRevision, expectedSnapshotRevision int64, actorID, reason string) error {
	if !safeID(tenantID) || len(country) != 2 || !safeID(pageID) || expectedDraftRevision < 1 || expectedSnapshotRevision < 1 || !safeID(actorID) {
		return ErrInvalidSnapshot
	}
	draft, err := service.drafts.GetPageDraft(ctx, tenantID, country, pageID)
	if err != nil {
		return err
	}
	if draft.Revision != expectedDraftRevision {
		return ErrRevisionConflict
	}
	current, err := service.snapshots.Get(ctx, tenantID, country)
	if err != nil {
		return err
	}
	if current.Revision == expectedSnapshotRevision+1 && pageMatches(current.Pages, draft.Page) {
		return nil
	}
	if current.Revision != expectedSnapshotRevision {
		return ErrRevisionConflict
	}
	next := cloneSnapshot(current)
	next.Revision, next.PublishedAt = current.Revision+1, service.clock().UTC()
	replaced := false
	for index := range next.Pages {
		if next.Pages[index].ID == pageID {
			next.Pages[index], replaced = cloneSnapshot(Snapshot{Pages: []Page{draft.Page}}).Pages[0], true
			break
		}
	}
	if !replaced {
		next.Pages = append(next.Pages, cloneSnapshot(Snapshot{Pages: []Page{draft.Page}}).Pages[0])
	}
	return service.publisher.Publish(ctx, next, expectedSnapshotRevision, actorID, reason)
}

type AdminOperationExecutor struct {
	publication *PublicationService
}

func NewAdminOperationExecutor(publication *PublicationService) (*AdminOperationExecutor, error) {
	if publication == nil {
		return nil, ErrInvalidRepository
	}
	return &AdminOperationExecutor{publication: publication}, nil
}

func (executor *AdminOperationExecutor) Execute(principal adminops.Principal, change adminops.Change) error {
	if change.Command.Domain != adminops.DomainCMS || change.Command.Action != adminops.ActionCMSPublish || principal.TenantID != change.TenantID || principal.Country != change.Country {
		return adminops.ErrInvalidRequest
	}
	draftRevision, draftOK := payloadInt64(change.Command.Payload["expected_draft_revision"])
	snapshotRevision, snapshotOK := payloadInt64(change.Command.Payload["expected_snapshot_revision"])
	if !draftOK || !snapshotOK {
		return adminops.ErrInvalidRequest
	}
	if publicationType, _ := change.Command.Payload["publication_type"].(string); publicationType == "WORKSPACE" {
		if change.Command.TargetID != "workspace" {
			return adminops.ErrInvalidRequest
		}
		if err := executor.publication.PublishWorkspace(context.Background(), change.TenantID, change.Country, draftRevision, snapshotRevision, principal.SubjectID, change.Command.Reason); err != nil {
			return err
		}
		return nil
	}
	if err := executor.publication.PublishPage(context.Background(), change.TenantID, change.Country, change.Command.TargetID, draftRevision, snapshotRevision, principal.SubjectID, change.Command.Reason); err != nil {
		return err
	}
	return nil
}

func pageMatches(pages []Page, candidate Page) bool {
	for _, page := range pages {
		if page.ID == candidate.ID {
			return reflect.DeepEqual(page, candidate)
		}
	}
	return false
}

func payloadInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), typed >= 0
	case int64:
		return typed, typed >= 0
	case float64:
		return int64(typed), typed >= 0 && typed <= math.MaxInt64 && typed == math.Trunc(typed)
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil && parsed >= 0
	default:
		return 0, false
	}
}
