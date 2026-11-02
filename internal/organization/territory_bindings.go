package organization

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TerritoryBinding struct {
	TerritoryID string `json:"territory_id"`
	Name        string `json:"name"`
	RegionID    string `json:"region_id"`
	NodeID      string `json:"node_id,omitempty"`
	Revision    int64  `json:"revision"`
}

// FieldCommandTerritories is advisory UI metadata derived from the current
// explicit assignment. Global operator roles do not create field attendance
// authority; the fulfillment command independently checks again at commit.
func (service *Service) FieldCommandTerritories(ctx context.Context, actor Actor) ([]string, error) {
	if !validActor(actor) || !actor.MFA {
		return nil, ErrForbidden
	}
	rows, err := service.pool.Query(ctx, `SELECT t.id::text FROM fulfillment.territory_access_targets t WHERE t.tenant_id=$1 AND t.country=$2 AND admin.fulfillment_territory_allowed($1::uuid,$2::char(2),$3::uuid,t.id,'resource.command') ORDER BY t.id LIMIT 2001`, actor.TenantID, actor.Country, actor.Subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		values = append(values, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(values) > 2000 {
		return nil, ErrInvalid
	}
	return values, nil
}

// Resource ownership is verified through the fulfillment owner's read model.
// Only platform administrators may attach/reassign authority; staff cannot
// use existing delegated rights to claim another business territory.
func (service *Service) TerritoryBindings(ctx context.Context, actor Actor) ([]TerritoryBinding, error) {
	if !validActor(actor) || !actor.PlatformAdmin {
		return nil, ErrForbidden
	}
	rows, err := service.pool.Query(ctx, `SELECT t.id::text,t.name,t.region_id,coalesce(b.node_id::text,''),coalesce(b.revision,0) FROM fulfillment.territory_access_targets t LEFT JOIN admin.fulfillment_territory_bindings b ON (b.tenant_id,b.country,b.territory_id)=(t.tenant_id,t.country,t.id) WHERE t.tenant_id=$1 AND t.country=$2 ORDER BY t.name,t.id`, actor.TenantID, actor.Country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TerritoryBinding{}
	for rows.Next() {
		var value TerritoryBinding
		if err = rows.Scan(&value.TerritoryID, &value.Name, &value.RegionID, &value.NodeID, &value.Revision); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (service *Service) BindTerritory(ctx context.Context, actor Actor, territoryID, nodeID string, revision int64, reason string) error {
	if !validActor(actor) || !actor.PlatformAdmin {
		return ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	if uuid.Validate(territoryID) != nil || uuid.Validate(nodeID) != nil || revision < 0 || utf8.RuneCountInString(reason) < 8 || utf8.RuneCountInString(reason) > 500 {
		return ErrInvalid
	}
	return service.mutate(ctx, actor, "FULFILLMENT_TERRITORY_BOUND", territoryID, func(tx pgx.Tx) error {
		if err := require(ctx, tx, actor, nodeID, ResourceCommand); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fulfillment.territory_access_targets WHERE tenant_id=$1 AND country=$2 AND id=$3)`, actor.TenantID, actor.Country, territoryID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM admin.organization_nodes WHERE tenant_id=$1 AND country=$2 AND id=$3`, actor.TenantID, actor.Country, nodeID).Scan(&kind); err != nil {
			return err
		}
		if kind != "TERRITORY" {
			return ErrInvalid
		}
		query := `INSERT INTO admin.fulfillment_territory_bindings(tenant_id,country,territory_id,node_id,revision,reason) VALUES($1,$2,$3,$4,1,$5) ON CONFLICT DO NOTHING`
		args := []any{actor.TenantID, actor.Country, territoryID, nodeID, reason}
		if revision > 0 {
			query = `UPDATE admin.fulfillment_territory_bindings SET node_id=$4,revision=revision+1,reason=$5 WHERE tenant_id=$1 AND country=$2 AND territory_id=$3 AND revision=$6`
			args = append(args, revision)
		}
		command, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO admin.fulfillment_territory_binding_events(tenant_id,country,territory_id,node_id,revision,actor_id,reason,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, territoryID, nodeID, revision+1, actor.Subject, reason, service.clock().UTC())
		return err
	})
}
