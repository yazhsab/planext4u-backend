package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const governanceOperationTimeout = 15 * time.Second

type PostgresService struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresService(pool *pgxpool.Pool, clock func() time.Time) (*PostgresService, error) {
	if pool == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresService{pool: pool, clock: clock}, nil
}

func (service *PostgresService) Ready(ctx context.Context) error {
	var ready bool
	err := service.pool.QueryRow(ctx, `SELECT to_regclass('governance.country_controls') IS NOT NULL AND to_regclass('governance.report_cards') IS NOT NULL AND to_regclass('governance.map_cells') IS NOT NULL AND to_regclass('governance.leaderboard_entries') IS NOT NULL AND to_regclass('governance.insights') IS NOT NULL`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check governance schema readiness: %w", err)
	}
	if !ready {
		return errors.New("governance schema is unavailable")
	}
	return nil
}

func (service *PostgresService) Dashboard(actor Actor) (Dashboard, error) {
	if !actor.MFAVerified {
		return Dashboard{}, ErrMFARequired
	}
	if !postgresGovernanceActor(actor) || !allowed(actor) {
		return Dashboard{}, ErrForbidden
	}
	ctx, cancel := context.WithTimeout(context.Background(), governanceOperationTimeout)
	defer cancel()
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Dashboard{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	countries := []CountryControl{}
	query := `SELECT country,currency,locales,feature_flags,policy_version FROM governance.country_controls WHERE tenant_id=$1 AND country=$2 ORDER BY country`
	arguments := []any{actor.TenantID, actor.Country}
	if hasRole(actor, "SUPER_ADMIN") {
		query = `SELECT country,currency,locales,feature_flags,policy_version FROM governance.country_controls WHERE tenant_id=$1 ORDER BY country`
		arguments = []any{actor.TenantID}
	}
	rows, err := tx.Query(ctx, query, arguments...)
	if err != nil {
		return Dashboard{}, err
	}
	for rows.Next() {
		var value CountryControl
		var flags []byte
		if err := rows.Scan(&value.Country, &value.Currency, &value.Locales, &flags, &value.PolicyVersion); err != nil {
			rows.Close()
			return Dashboard{}, err
		}
		if err := json.Unmarshal(flags, &value.FeatureFlags); err != nil {
			rows.Close()
			return Dashboard{}, fmt.Errorf("decode governance feature flags: %w", err)
		}
		countries = append(countries, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Dashboard{}, err
	}
	rows.Close()
	if len(countries) == 0 {
		return Dashboard{}, ErrForbidden
	}

	reports := []ReportCard{}
	rows, err = tx.Query(ctx, `SELECT id,title,domain,metric,value,unit,freshness FROM governance.report_cards WHERE tenant_id=$1 AND country=$2 AND published ORDER BY domain,id`, actor.TenantID, actor.Country)
	if err != nil {
		return Dashboard{}, err
	}
	for rows.Next() {
		var value ReportCard
		if err := rows.Scan(&value.ID, &value.Title, &value.Domain, &value.Metric, &value.Value, &value.Unit, &value.Freshness); err != nil {
			rows.Close()
			return Dashboard{}, err
		}
		value.Masked, value.ExportPolicy = true, "MFA_AND_AUDIT_REQUIRED"
		reports = append(reports, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Dashboard{}, err
	}
	rows.Close()

	mapCells := []MapCell{}
	rows, err = tx.Query(ctx, `SELECT region_code,label,count,intensity FROM governance.map_cells WHERE tenant_id=$1 AND country=$2 AND published ORDER BY region_code`, actor.TenantID, actor.Country)
	if err != nil {
		return Dashboard{}, err
	}
	for rows.Next() {
		var value MapCell
		if err := rows.Scan(&value.RegionCode, &value.Label, &value.Count, &value.Intensity); err != nil {
			rows.Close()
			return Dashboard{}, err
		}
		value.Precision = "aggregate_region"
		mapCells = append(mapCells, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Dashboard{}, err
	}
	rows.Close()

	leaders := []LeaderboardEntry{}
	rows, err = tx.Query(ctx, `SELECT rank,masked_label,score,badge FROM governance.leaderboard_entries WHERE tenant_id=$1 AND country=$2 AND published ORDER BY board,rank`, actor.TenantID, actor.Country)
	if err != nil {
		return Dashboard{}, err
	}
	for rows.Next() {
		var value LeaderboardEntry
		if err := rows.Scan(&value.Rank, &value.Label, &value.Score, &value.Badge); err != nil {
			rows.Close()
			return Dashboard{}, err
		}
		value.PIIMasked = true
		leaders = append(leaders, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Dashboard{}, err
	}
	rows.Close()

	insights := []Insight{}
	rows, err = tx.Query(ctx, `SELECT id,title,summary,confidence,evidence,generated_at FROM governance.insights WHERE tenant_id=$1 AND country=$2 AND published ORDER BY generated_at DESC,id`, actor.TenantID, actor.Country)
	if err != nil {
		return Dashboard{}, err
	}
	for rows.Next() {
		var value Insight
		if err := rows.Scan(&value.ID, &value.Title, &value.Summary, &value.Confidence, &value.Evidence, &value.GeneratedAt); err != nil {
			rows.Close()
			return Dashboard{}, err
		}
		insights = append(insights, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Dashboard{}, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return Dashboard{}, err
	}
	return Dashboard{GeneratedAt: service.clock().UTC(), Reports: reports, MapCells: mapCells, Leaderboard: leaders, Insights: insights, Countries: countries, PrivacyMode: "aggregate_and_masked"}, nil
}

func postgresGovernanceActor(actor Actor) bool {
	return uuid.Validate(actor.TenantID) == nil && uuid.Validate(actor.Subject) == nil && len(actor.Country) == 2 && actor.Country[0] >= 'A' && actor.Country[0] <= 'Z' && actor.Country[1] >= 'A' && actor.Country[1] <= 'Z' && len(actor.Roles) > 0
}
