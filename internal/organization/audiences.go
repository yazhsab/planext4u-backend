package organization

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Audience criteria only narrow content targeting. Membership grants no API or
// organization capability; those continue to resolve current assignments.
type AudienceGroup struct {
	ID                 string   `json:"id"`
	NodeID             string   `json:"node_id"`
	Name               string   `json:"name"`
	RoleIDs            []string `json:"role_ids"`
	IncludeDescendants bool     `json:"include_descendants"`
	Active             bool     `json:"active"`
	Revision           int64    `json:"revision"`
}

func validAudienceGroup(value AudienceGroup) bool {
	if !validName(value.Name) || uuid.Validate(value.NodeID) != nil || len(value.RoleIDs) < 1 || len(value.RoleIDs) > 32 || value.Revision < 0 {
		return false
	}
	if value.ID == "" && value.Revision != 0 || value.ID != "" && (uuid.Validate(value.ID) != nil || value.Revision < 1) {
		return false
	}
	seen := map[uuid.UUID]bool{}
	for _, role := range value.RoleIDs {
		id, err := uuid.Parse(role)
		if err != nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (service *Service) SaveAudienceGroup(ctx context.Context, actor Actor, input AudienceGroup) (AudienceGroup, error) {
	if !validAudienceGroup(input) {
		return AudienceGroup{}, ErrInvalid
	}
	expected := input.Revision
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	input.Revision = expected + 1
	err := service.mutateWithOperator(ctx, actor, "AUDIENCE_GROUP_SAVED", input.ID, true, func(tx pgx.Tx) error {
		if expected > 0 {
			var priorNode string
			err := tx.QueryRow(ctx, `SELECT node_id::text FROM admin.audience_groups WHERE tenant_id=$1 AND country=$2 AND id=$3`, actor.TenantID, actor.Country, input.ID).Scan(&priorNode)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if err = require(ctx, tx, actor, priorNode, StaffManage); err != nil {
				return err
			}
		}
		if err := require(ctx, tx, actor, input.NodeID, StaffManage); err != nil {
			return err
		}
		var found int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM admin.job_roles WHERE tenant_id=$1 AND country=$2 AND id=ANY($3::uuid[])`, actor.TenantID, actor.Country, input.RoleIDs).Scan(&found); err != nil {
			return err
		}
		if found != len(input.RoleIDs) {
			return ErrInvalid
		}
		if expected == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO admin.audience_groups(tenant_id,country,id,node_id,name,role_ids,include_descendants,active,revision) VALUES($1,$2,$3,$4,$5,$6::uuid[],$7,$8,$9)`, actor.TenantID, actor.Country, input.ID, input.NodeID, input.Name, input.RoleIDs, input.IncludeDescendants, input.Active, input.Revision); err != nil {
				return err
			}
		} else {
			result, err := tx.Exec(ctx, `UPDATE admin.audience_groups SET node_id=$4,name=$5,role_ids=$6::uuid[],include_descendants=$7,active=$8,revision=$9 WHERE tenant_id=$1 AND country=$2 AND id=$3 AND revision=$10`, actor.TenantID, actor.Country, input.ID, input.NodeID, input.Name, input.RoleIDs, input.IncludeDescendants, input.Active, input.Revision, expected)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return ErrConflict
			}
		}
		definition, err := json.Marshal(input)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO admin.audience_group_versions(tenant_id,country,group_id,revision,definition,actor_id,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, actor.TenantID, actor.Country, input.ID, input.Revision, definition, actor.Subject, service.clock().UTC())
		return err
	})
	return input, err
}

const AudiencePageSize = 100

type AudienceGroupPage struct {
	Groups     []AudienceGroup `json:"groups"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// AudienceGroups uses immutable IDs for keyset pagination. Authorization is
// applied before LIMIT, so neither hidden groups nor their IDs enter a page or
// cursor. Every page resolves current scope in its own consistent snapshot.
func (service *Service) AudienceGroups(ctx context.Context, actor Actor, after string) (AudienceGroupPage, error) {
	if !validActor(actor) {
		return AudienceGroupPage{}, ErrForbidden
	}
	if after != "" && (uuid.Validate(after) != nil || uuid.MustParse(after).String() != after) {
		return AudienceGroupPage{}, ErrInvalid
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AudienceGroupPage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `SELECT g.id::text,g.node_id::text,g.name,g.role_ids::text[],g.include_descendants,g.active,g.revision
 FROM admin.audience_groups g
 WHERE g.tenant_id=$1 AND g.country=$2 AND (NULLIF($5,'')::uuid IS NULL OR g.id>NULLIF($5,'')::uuid)
 AND EXISTS (
  WITH RECURSIVE path AS (
   SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n
   WHERE n.tenant_id=$1 AND n.country=$2 AND n.id=g.node_id
   UNION ALL SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n
   JOIN path p ON n.id=p.parent_id WHERE n.tenant_id=$1 AND n.country=$2
  ) SELECT 1 FROM path HAVING count(*)>0 AND ($4::boolean OR (bool_and(active) AND EXISTS (
   SELECT 1 FROM admin.staff_assignments s JOIN admin.job_roles r USING(tenant_id,country)
   WHERE s.tenant_id=$1 AND s.country=$2 AND s.subject_id=$3 AND s.status='ACTIVE'
   AND r.id=s.role_id AND s.node_id IN(SELECT id FROM path) AND 'organization.read'=ANY(r.capabilities)
  )))
 ) ORDER BY g.id LIMIT $6`, actor.TenantID, actor.Country, actor.Subject, actor.PlatformAdmin, after, AudiencePageSize+1)
	if err != nil {
		return AudienceGroupPage{}, err
	}
	all := []AudienceGroup{}
	for rows.Next() {
		var g AudienceGroup
		if err = rows.Scan(&g.ID, &g.NodeID, &g.Name, &g.RoleIDs, &g.IncludeDescendants, &g.Active, &g.Revision); err != nil {
			rows.Close()
			return AudienceGroupPage{}, err
		}
		all = append(all, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return AudienceGroupPage{}, err
	}
	result := AudienceGroupPage{Groups: all}
	if len(all) > AudiencePageSize {
		result.Groups = all[:AudiencePageSize]
		result.NextCursor = result.Groups[len(result.Groups)-1].ID
	}
	return result, tx.Commit(ctx)
}

func (service *Service) PreviewAudienceMember(ctx context.Context, actor Actor, groupID, subjectID string) (bool, error) {
	if !validActor(actor) || uuid.Validate(groupID) != nil || uuid.Validate(subjectID) != nil {
		return false, ErrForbidden
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockSourceOperator(ctx, tx, actor); err != nil {
		return false, err
	}
	// Scope changes and group edits take the exclusive counterpart.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":organization"); err != nil {
		return false, err
	}
	var node string
	if err = tx.QueryRow(ctx, `SELECT node_id::text FROM admin.audience_groups WHERE tenant_id=$1 AND country=$2 AND id=$3`, actor.TenantID, actor.Country, groupID).Scan(&node); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, err
	}
	if err = require(ctx, tx, actor, node, StaffManage); err != nil {
		return false, err
	}
	var member bool
	if err = tx.QueryRow(ctx, `SELECT admin.audience_member($1,$2,$3,$4)`, actor.TenantID, actor.Country, subjectID, groupID).Scan(&member); err != nil {
		return false, err
	}
	return member, tx.Commit(ctx)
}

func lockSourceOperator(ctx context.Context, tx pgx.Tx, actor Actor) error {
	if uuid.Validate(actor.OperatorSessionID) != nil || actor.OperatorRevision < 1 {
		return ErrForbidden
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT admin.lock_fulfillment_operator($1,$2,$3,$4,$5)`, actor.TenantID, actor.Country, actor.Subject, actor.OperatorSessionID, actor.OperatorRevision).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrForbidden
	}
	return nil
}
