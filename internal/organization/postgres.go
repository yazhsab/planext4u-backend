package organization

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}
type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func NewService(pool *pgxpool.Pool, clock func() time.Time) (*Service, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalid
	}
	return &Service{pool: pool, clock: clock}, nil
}

func (service *Service) Ready(ctx context.Context) error {
	var exists bool
	err := service.pool.QueryRow(ctx, `SELECT to_regclass('admin.staff_assignments') IS NOT NULL`).Scan(&exists)
	if err == nil && !exists {
		return ErrNotFound
	}
	return err
}

func validActor(actor Actor) bool {
	return uuid.Validate(actor.TenantID) == nil && uuid.Validate(actor.Subject) == nil && len(actor.Country) == 2 && actor.Country[0] >= 'A' && actor.Country[0] <= 'Z' && actor.Country[1] >= 'A' && actor.Country[1] <= 'Z' && actor.MFA
}
func validName(value string) bool {
	return strings.TrimSpace(value) == value && utf8.RuneCountInString(value) > 0 && utf8.RuneCountInString(value) <= 128 && !strings.ContainsAny(value, "\x00\r\n")
}
func validCapability(value string) bool {
	switch value {
	case Read, Manage, StaffManage, ResourceRead, ResourceExport, ResourceCommand:
		return true
	}
	return false
}

// Every traversal remains in the composite tenant/country scope, including
// inherited authority. An inactive ancestor invalidates all descendants.
const ancestors = `WITH RECURSIVE path AS (
 SELECT id,parent_id,active FROM admin.organization_nodes WHERE tenant_id=$1 AND country=$2 AND id=$3
 UNION ALL SELECT n.id,n.parent_id,n.active FROM admin.organization_nodes n JOIN path p ON n.id=p.parent_id WHERE n.tenant_id=$1 AND n.country=$2
) `

func allowed(ctx context.Context, db queryer, actor Actor, nodeID, capability string) (bool, error) {
	if !validActor(actor) || uuid.Validate(nodeID) != nil || !validCapability(capability) {
		return false, ErrForbidden
	}
	var exists, active bool
	if err := db.QueryRow(ctx, ancestors+`SELECT count(*)>0,coalesce(bool_and(active),false) FROM path`, actor.TenantID, actor.Country, nodeID).Scan(&exists, &active); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	// Operators must still be able to inspect/export a disabled subtree and
	// reactivate it. Inactivity continues to block business/delegated commands.
	if actor.PlatformAdmin && (capability == Read || capability == ResourceExport) {
		return true, nil
	}
	if !active {
		return false, nil
	}
	if actor.PlatformAdmin {
		return true, nil
	}
	var result bool
	err := db.QueryRow(ctx, ancestors+`SELECT EXISTS (
 SELECT 1 FROM admin.staff_assignments s JOIN admin.job_roles r USING(tenant_id,country)
 WHERE s.tenant_id=$1 AND s.country=$2 AND s.subject_id=$4 AND s.status='ACTIVE'
 AND r.id=s.role_id AND s.node_id IN (SELECT id FROM path) AND $5=ANY(r.capabilities))`, actor.TenantID, actor.Country, nodeID, actor.Subject, capability).Scan(&result)
	return result, err
}

func (service *Service) Authorize(ctx context.Context, actor Actor, nodeID, capability string) error {
	ok, err := allowed(ctx, service.pool, actor, nodeID, capability)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func lock(ctx context.Context, tx pgx.Tx, actor Actor) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":organization")
	return err
}
func (service *Service) mutate(ctx context.Context, actor Actor, action, target string, work func(pgx.Tx) error) error {
	return service.mutateWithOperator(ctx, actor, action, target, false, work)
}

func (service *Service) mutateWithOperator(ctx context.Context, actor Actor, action, target string, liveOperator bool, work func(pgx.Tx) error) error {
	if !validActor(actor) {
		return ErrForbidden
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if liveOperator || actor.RequireLiveOperator {
		if err = lockSourceOperator(ctx, tx, actor); err != nil {
			return err
		}
	}
	if err = lock(ctx, tx, actor); err != nil {
		return err
	}
	if err = work(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin.organization_events(tenant_id,country,actor_id,action,target_id,occurred_at) VALUES($1,$2,$3,$4,$5,$6)`, actor.TenantID, actor.Country, actor.Subject, action, target, service.clock().UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func require(ctx context.Context, tx pgx.Tx, actor Actor, node, capability string) error {
	ok, err := allowed(ctx, tx, actor, node, capability)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

func (service *Service) CreateNode(ctx context.Context, actor Actor, input Node) (Node, error) {
	if !validName(input.Name) || input.Kind != "ORGANIZATION" && input.Kind != "TERRITORY" || input.ParentID != "" && uuid.Validate(input.ParentID) != nil {
		return Node{}, ErrInvalid
	}
	input.ID = uuid.NewString()
	input.Active = true
	input.Revision = 1
	err := service.mutate(ctx, actor, "NODE_CREATED", input.ID, func(tx pgx.Tx) error {
		if input.ParentID == "" {
			if !actor.PlatformAdmin {
				return ErrForbidden
			}
		} else if err := require(ctx, tx, actor, input.ParentID, Manage); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO admin.organization_nodes(tenant_id,country,id,parent_id,kind,name) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6)`, actor.TenantID, actor.Country, input.ID, input.ParentID, input.Kind, input.Name)
		return err
	})
	return input, err
}

func (service *Service) SetNodeActive(ctx context.Context, actor Actor, id string, revision int64, active bool) error {
	if uuid.Validate(id) != nil || revision < 1 {
		return ErrInvalid
	}
	return service.mutate(ctx, actor, "NODE_STATUS_CHANGED", id, func(tx pgx.Tx) error {
		// Reactivation cannot use the disabled node as an authority source.
		if !actor.PlatformAdmin {
			return ErrForbidden
		}
		result, err := tx.Exec(ctx, `UPDATE admin.organization_nodes SET active=$5,revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND id=$3 AND revision=$4`, actor.TenantID, actor.Country, id, revision, active)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (service *Service) CreateRole(ctx context.Context, actor Actor, input JobRole, nodeID string) (JobRole, error) {
	if !validName(input.Name) || len(input.Capabilities) == 0 || len(input.Capabilities) > 32 {
		return JobRole{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, capability := range input.Capabilities {
		if !validCapability(capability) || seen[capability] {
			return JobRole{}, ErrInvalid
		}
		seen[capability] = true
	}
	input.ID = uuid.NewString()
	err := service.mutate(ctx, actor, "JOB_ROLE_CREATED", input.ID, func(tx pgx.Tx) error {
		if err := require(ctx, tx, actor, nodeID, StaffManage); err != nil {
			return err
		}
		for _, capability := range input.Capabilities {
			if err := require(ctx, tx, actor, nodeID, capability); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO admin.job_roles(tenant_id,country,id,name,capabilities) VALUES($1,$2,$3,$4,$5)`, actor.TenantID, actor.Country, input.ID, input.Name, input.Capabilities)
		return err
	})
	return input, err
}

func delegation(ctx context.Context, tx pgx.Tx, actor Actor, node, role string) error {
	if err := require(ctx, tx, actor, node, StaffManage); err != nil {
		return err
	}
	var capabilities []string
	if err := tx.QueryRow(ctx, `SELECT capabilities FROM admin.job_roles WHERE tenant_id=$1 AND country=$2 AND id=$3`, actor.TenantID, actor.Country, role).Scan(&capabilities); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	for _, capability := range capabilities {
		if err := require(ctx, tx, actor, node, capability); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) SetAssignment(ctx context.Context, actor Actor, input Assignment) (Assignment, error) {
	if uuid.Validate(input.Subject) != nil || uuid.Validate(input.NodeID) != nil || uuid.Validate(input.RoleID) != nil || input.Revision < 0 || input.Status != "ACTIVE" && input.Status != "SUSPENDED" && input.Status != "LEFT" {
		return Assignment{}, ErrInvalid
	}
	err := service.mutate(ctx, actor, "STAFF_STATUS_CHANGED", input.Subject, func(tx pgx.Tx) error {
		if actor.Subject == input.Subject {
			return ErrForbidden
		}
		if err := delegation(ctx, tx, actor, input.NodeID, input.RoleID); err != nil {
			return err
		}
		if input.Revision == 0 {
			result, err := tx.Exec(ctx, `INSERT INTO admin.staff_assignments(tenant_id,country,subject_id,node_id,role_id,status) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, actor.TenantID, actor.Country, input.Subject, input.NodeID, input.RoleID, input.Status)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return ErrConflict
			}
		} else {
			result, err := tx.Exec(ctx, `UPDATE admin.staff_assignments SET status=$6,revision=revision+1 WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND node_id=$4 AND role_id=$5 AND revision=$7`, actor.TenantID, actor.Country, input.Subject, input.NodeID, input.RoleID, input.Status, input.Revision)
			if err != nil {
				return err
			}
			if result.RowsAffected() != 1 {
				return ErrConflict
			}
		}
		return nil
	})
	if err == nil {
		input.Revision++
	}
	return input, err
}

func (service *Service) Invite(ctx context.Context, actor Actor, subject, node, role string, ttl time.Duration) (Invitation, error) {
	if uuid.Validate(subject) != nil || uuid.Validate(node) != nil || uuid.Validate(role) != nil || ttl < time.Minute || ttl > 7*24*time.Hour {
		return Invitation{}, ErrInvalid
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Invitation{}, err
	}
	invitation := Invitation{ID: uuid.NewString(), Token: base64.RawURLEncoding.EncodeToString(raw), Subject: subject, NodeID: node, RoleID: role, ExpiresAt: service.clock().UTC().Add(ttl)}
	err := service.mutate(ctx, actor, "STAFF_INVITED", subject, func(tx pgx.Tx) error {
		if actor.Subject == subject {
			return ErrForbidden
		}
		if err := delegation(ctx, tx, actor, node, role); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO admin.staff_invitations(tenant_id,country,id,token_digest,subject_id,node_id,role_id,invited_by,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, actor.TenantID, actor.Country, invitation.ID, digest(invitation.Token), subject, node, role, actor.Subject, invitation.ExpiresAt)
		return err
	})
	if err != nil {
		return Invitation{}, err
	}
	return invitation, nil
}
func digest(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }

func (service *Service) AcceptInvitation(ctx context.Context, actor Actor, token string) error {
	if len(token) != 43 {
		return ErrInvalid
	}
	return service.mutate(ctx, actor, "STAFF_INVITATION_ACCEPTED", actor.Subject, func(tx pgx.Tx) error {
		var node, role, inviter string
		var expires time.Time
		var accepted *time.Time
		err := tx.QueryRow(ctx, `SELECT node_id,role_id,invited_by,expires_at,accepted_at FROM admin.staff_invitations WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND token_digest=$4 FOR UPDATE`, actor.TenantID, actor.Country, actor.Subject, digest(token)).Scan(&node, &role, &inviter, &expires, &accepted)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		if accepted != nil || !service.clock().UTC().Before(expires) {
			return ErrConflict
		}
		// A revoked inviter cannot leave usable grants behind. Platform bootstrap
		// invitations are checked through a persisted delegating assignment too.
		invitingActor := Actor{TenantID: actor.TenantID, Country: actor.Country, Subject: inviter, MFA: true}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 AND active AND 'SUPER_ADMIN'=ANY(roles) AND $3=ANY(allowed_countries))`, actor.TenantID, inviter, actor.Country).Scan(&invitingActor.PlatformAdmin); err != nil {
			return err
		}
		if err := delegation(ctx, tx, invitingActor, node, role); err != nil {
			return ErrForbidden
		}
		result, err := tx.Exec(ctx, `INSERT INTO admin.staff_assignments(tenant_id,country,subject_id,node_id,role_id,status) VALUES($1,$2,$3,$4,$5,'ACTIVE') ON CONFLICT DO NOTHING`, actor.TenantID, actor.Country, actor.Subject, node, role)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE admin.staff_invitations SET accepted_at=$5 WHERE tenant_id=$1 AND country=$2 AND subject_id=$3 AND token_digest=$4`, actor.TenantID, actor.Country, actor.Subject, digest(token), service.clock().UTC())
		return err
	})
}

// Workspace filters before returning data. Callers cannot supply a tenant,
// subject or a broader scope in search/export query parameters.
func (service *Service) Workspace(ctx context.Context, actor Actor, search string) (Workspace, error) {
	if !validActor(actor) || len(search) > 128 {
		return Workspace{}, ErrForbidden
	}
	result := Workspace{Nodes: []Node{}, Roles: []JobRole{}, Staff: []Assignment{}}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lock(ctx, tx, actor); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT id,coalesce(parent_id::text,''),kind,name,active,revision FROM admin.organization_nodes WHERE tenant_id=$1 AND country=$2 ORDER BY name,id`, actor.TenantID, actor.Country)
	if err != nil {
		return result, err
	}
	var all []Node
	for rows.Next() {
		var node Node
		if err = rows.Scan(&node.ID, &node.ParentID, &node.Kind, &node.Name, &node.Active, &node.Revision); err != nil {
			rows.Close()
			return result, err
		}
		all = append(all, node)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	visible := map[string]bool{}
	for _, node := range all {
		ok, accessErr := allowed(ctx, tx, actor, node.ID, Read)
		if accessErr != nil {
			return result, accessErr
		}
		if ok {
			visible[node.ID] = true
			if search == "" || strings.Contains(strings.ToLower(node.Name), strings.ToLower(search)) {
				result.Nodes = append(result.Nodes, node)
			}
		}
	}
	if !actor.PlatformAdmin && len(visible) == 0 {
		return result, ErrForbidden
	}
	rows, err = tx.Query(ctx, `SELECT subject_id,node_id,role_id,status,revision FROM admin.staff_assignments WHERE tenant_id=$1 AND country=$2 ORDER BY node_id,subject_id`, actor.TenantID, actor.Country)
	if err != nil {
		return result, err
	}
	roleIDs := map[string]bool{}
	for rows.Next() {
		var item Assignment
		if err = rows.Scan(&item.Subject, &item.NodeID, &item.RoleID, &item.Status, &item.Revision); err != nil {
			rows.Close()
			return result, err
		}
		if visible[item.NodeID] {
			result.Staff = append(result.Staff, item)
			roleIDs[item.RoleID] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.Query(ctx, `SELECT id,name,capabilities FROM admin.job_roles WHERE tenant_id=$1 AND country=$2 ORDER BY name,id`, actor.TenantID, actor.Country)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var role JobRole
		if err = rows.Scan(&role.ID, &role.Name, &role.Capabilities); err != nil {
			rows.Close()
			return result, err
		}
		if actor.PlatformAdmin || roleIDs[role.ID] {
			result.Roles = append(result.Roles, role)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
