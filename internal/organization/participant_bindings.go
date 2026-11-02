package organization

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"unicode/utf8"
)

type ParticipantBinding struct {
	Kind          string `json:"kind"`
	ParticipantID string `json:"participant_id"`
	NodeID        string `json:"node_id,omitempty"`
	Active        bool   `json:"active"`
	Revision      int64  `json:"revision"`
}
type ParticipantTerritory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func lockParticipantAdministrator(ctx context.Context, tx pgx.Tx, a Actor) error {
	if !validActor(a) || !a.PlatformAdmin || uuid.Validate(a.OperatorSessionID) != nil || a.OperatorRevision < 1 {
		return ErrForbidden
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT admin.lock_participant_reviewer($1,$2,$3,$4,$5)`, a.TenantID, a.Country, a.Subject, a.OperatorSessionID, a.OperatorRevision).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrForbidden
	}
	return nil
}
func participantTarget(ctx context.Context, tx pgx.Tx, a Actor, kind, id string) error {
	table := "supply.participant_access_targets"
	if kind == "RIDER" {
		table = "fulfillment.participant_access_targets"
	} else if kind != "VENDOR" {
		return ErrInvalid
	}
	if uuid.Validate(id) != nil {
		return ErrInvalid
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE tenant_id=$1 AND country=$2 AND id=$3)`, a.TenantID, a.Country, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}
func (service *Service) ParticipantBinding(ctx context.Context, a Actor, kind, id string) (ParticipantBinding, error) {
	value := ParticipantBinding{Kind: kind, ParticipantID: id}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return value, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockParticipantAdministrator(ctx, tx, a); err != nil {
		return value, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, a.TenantID+":"+a.Country+":organization"); err != nil {
		return value, err
	}
	if err = participantTarget(ctx, tx, a, kind, id); err != nil {
		return value, err
	}
	err = tx.QueryRow(ctx, `SELECT node_id::text,active,revision FROM admin.participant_territory_bindings WHERE tenant_id=$1 AND country=$2 AND kind=$3 AND participant_id=$4`, a.TenantID, a.Country, kind, id).Scan(&value.NodeID, &value.Active, &value.Revision)
	if err != nil && err != pgx.ErrNoRows {
		return value, err
	}
	if err = lockParticipantAdministrator(ctx, tx, a); err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}
func (service *Service) BindParticipant(ctx context.Context, a Actor, value ParticipantBinding, reason string) error {
	reason = strings.TrimSpace(reason)
	if uuid.Validate(value.NodeID) != nil || uuid.Validate(value.ParticipantID) != nil || value.Revision < 0 || utf8.RuneCountInString(reason) < 8 || utf8.RuneCountInString(reason) > 500 {
		return ErrInvalid
	}
	return service.mutateWithOperator(ctx, a, "PARTICIPANT_TERRITORY_BOUND", value.ParticipantID, true, func(tx pgx.Tx) error {
		if err := lockParticipantAdministrator(ctx, tx, a); err != nil {
			return err
		}
		if err := participantTarget(ctx, tx, a, value.Kind, value.ParticipantID); err != nil {
			return err
		}
		var territory bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin.organization_nodes WHERE tenant_id=$1 AND country=$2 AND id=$3 AND kind='TERRITORY')`, a.TenantID, a.Country, value.NodeID).Scan(&territory); err != nil {
			return err
		}
		if !territory {
			return ErrInvalid
		}
		if value.Active {
			if err := require(ctx, tx, a, value.NodeID, ResourceCommand); err != nil {
				return err
			}
		}
		query := `INSERT INTO admin.participant_territory_bindings(tenant_id,country,kind,participant_id,node_id,active,revision,reason) VALUES($1,$2,$3,$4,$5,$6,1,$7) ON CONFLICT DO NOTHING`
		args := []any{a.TenantID, a.Country, value.Kind, value.ParticipantID, value.NodeID, value.Active, reason}
		if value.Revision > 0 {
			query = `UPDATE admin.participant_territory_bindings SET node_id=$5,active=$6,revision=revision+1,reason=$7 WHERE tenant_id=$1 AND country=$2 AND kind=$3 AND participant_id=$4 AND revision=$8`
			args = append(args, value.Revision)
		}
		result, err := tx.Exec(ctx, query, args...)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO admin.participant_territory_events(tenant_id,country,kind,participant_id,revision,node_id,active,actor_id,reason,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a.TenantID, a.Country, value.Kind, value.ParticipantID, value.Revision+1, value.NodeID, value.Active, a.Subject, reason, service.clock().UTC()); err != nil {
			return err
		}
		return lockParticipantAdministrator(ctx, tx, a)
	})
}
func (service *Service) ParticipantTerritories(ctx context.Context, a Actor) ([]ParticipantTerritory, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockParticipantAdministrator(ctx, tx, a); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, a.TenantID+":"+a.Country+":organization"); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text,name FROM admin.organization_nodes WHERE tenant_id=$1 AND country=$2 AND kind='TERRITORY' ORDER BY name,id LIMIT 1001`, a.TenantID, a.Country)
	if err != nil {
		return nil, err
	}
	values := []ParticipantTerritory{}
	for rows.Next() {
		var v ParticipantTerritory
		if err = rows.Scan(&v.ID, &v.Name); err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(values) > 1000 {
		return nil, ErrInvalid
	}
	active := values[:0]
	for _, value := range values {
		ok, e := allowed(ctx, tx, a, value.ID, ResourceCommand)
		if e != nil {
			return nil, e
		}
		if ok {
			active = append(active, value)
		}
	}
	if err = lockParticipantAdministrator(ctx, tx, a); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return active, nil
}
