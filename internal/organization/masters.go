package organization

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Masters are country-wide reference data. Regional delegation never grants
// authority over a country's complete directory. All entry points recheck the
// current operator directory and source session through their transaction.
type Master struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Code     string `json:"code"`
	ParentID string `json:"parent_id,omitempty"`
	NameEN   string `json:"name_en"`
	NameTA   string `json:"name_ta"`
	Active   bool   `json:"active"`
	Revision int64  `json:"revision"`
}
type MasterChange struct {
	Master
	Reason string `json:"reason"`
}
type MasterFilter struct{ Kind, Search, Status, ParentID, After string }
type MasterPage struct {
	Items     []Master `json:"items"`
	NextAfter string   `json:"next_after,omitempty"`
}
type MasterVersion struct {
	Definition Master    `json:"definition"`
	ActorID    string    `json:"actor_id"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}
type MasterHistory struct {
	Items      []MasterVersion `json:"items"`
	NextBefore int64           `json:"next_before,omitempty"`
}

var masterCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func masterParent(kind string) (string, bool) {
	switch kind {
	case "COUNTRY", "OCCUPATION", "REASON":
		return "", true
	case "STATE":
		return "COUNTRY", true
	case "DISTRICT":
		return "STATE", true
	case "CITY":
		return "DISTRICT", true
	case "AREA":
		return "CITY", true
	}
	return "", false
}
func masterText(value string, min, max int) bool {
	count := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && count >= min && count <= max && !strings.ContainsFunc(value, unicode.IsControl)
}
func validMaster(input MasterChange, country string) bool {
	parent, ok := masterParent(input.Kind)
	return ok && uuid.Validate(input.ID) == nil && input.Revision >= 0 && input.Revision < 1<<62 && masterCode.MatchString(input.Code) &&
		masterText(input.NameEN, 1, 128) && masterText(input.NameTA, 1, 128) && masterText(input.Reason, 8, 500) &&
		(parent == "" && input.ParentID == "" || parent != "" && uuid.Validate(input.ParentID) == nil && input.ID != input.ParentID) &&
		(input.Kind != "COUNTRY" || input.Code == country)
}

func masterAuthority(ctx context.Context, tx pgx.Tx, actor Actor, write bool) error {
	if !validActor(actor) || uuid.Validate(actor.OperatorSessionID) != nil || actor.OperatorRevision < 1 {
		return ErrForbidden
	}
	var active bool
	var revision int64
	var roles, countries []string
	err := tx.QueryRow(ctx, `SELECT active,revision,roles,allowed_countries FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 FOR SHARE`, actor.TenantID, actor.Subject).Scan(&active, &revision, &roles, &countries)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !active || revision != actor.OperatorRevision || !masterContains(countries, actor.Country) {
		return ErrForbidden
	}
	var tenant, subject, country string
	var sessionRevision int64
	var expires, authenticated, now time.Time
	var sessionRoles, methods []string
	err = tx.QueryRow(ctx, `SELECT tenant_id::text,subject_id::text,selected_country,operator_revision,expires_at,authenticated_at,roles,auth_methods,clock_timestamp() FROM admin.sessions WHERE session_id=$1 FOR SHARE`, actor.OperatorSessionID).Scan(&tenant, &subject, &country, &sessionRevision, &expires, &authenticated, &sessionRoles, &methods, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if tenant != actor.TenantID || subject != actor.Subject || country != actor.Country || sessionRevision != revision || !expires.After(now) || authenticated.After(now.Add(time.Minute)) || write && authenticated.Before(now.Add(-5*time.Minute)) {
		return ErrForbidden
	}
	permitted := false
	for _, role := range sessionRoles {
		if !masterContains(roles, role) {
			return ErrForbidden
		}
		if role == "SUPER_ADMIN" || role == "COUNTRY_ADMIN" || !write && role == "AUDITOR" {
			permitted = true
		}
	}
	mfa := false
	for _, method := range methods {
		switch strings.ToLower(method) {
		case "mfa", "otp", "totp", "webauthn":
			mfa = true
		}
	}
	if !permitted || !mfa {
		return ErrForbidden
	}
	return nil
}
func masterContains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

const masterColumns = `id::text,kind,code,coalesce(parent_id::text,''),name_en,name_ta,active,revision`

func scanMaster(row pgx.Row) (Master, error) {
	var value Master
	err := row.Scan(&value.ID, &value.Kind, &value.Code, &value.ParentID, &value.NameEN, &value.NameTA, &value.Active, &value.Revision)
	return value, err
}

func (service *Service) Masters(ctx context.Context, actor Actor, filter MasterFilter) (MasterPage, error) {
	page := MasterPage{Items: []Master{}}
	if _, ok := masterParent(filter.Kind); !ok {
		return page, ErrInvalid
	}
	if !masterText(filter.Search, 0, 128) || filter.Status != "" && filter.Status != "ALL" && filter.Status != "ACTIVE" && filter.Status != "INACTIVE" || filter.After != "" && uuid.Validate(filter.After) != nil || filter.ParentID != "" && uuid.Validate(filter.ParentID) != nil {
		return page, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = masterAuthority(ctx, tx, actor, false); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT `+masterColumns+` FROM admin.master_data WHERE tenant_id=$1 AND country=$2 AND kind=$3 AND ($4='' OR strpos(lower(name_en||' '||name_ta||' '||code),lower($4))>0) AND ($5='' OR $5='ALL' OR active=($5='ACTIVE')) AND ($6='' OR parent_id=NULLIF($6,'')::uuid) AND ($7='' OR id>NULLIF($7,'')::uuid) ORDER BY id LIMIT 51`, actor.TenantID, actor.Country, filter.Kind, filter.Search, filter.Status, filter.ParentID, filter.After)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		value, scanErr := scanMaster(rows)
		if scanErr != nil {
			rows.Close()
			return page, scanErr
		}
		page.Items = append(page.Items, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Items) > 50 {
		page.NextAfter = page.Items[49].ID
		page.Items = page.Items[:50]
	}
	return page, tx.Commit(ctx)
}

func (service *Service) SaveMaster(ctx context.Context, actor Actor, input MasterChange) (Master, error) {
	if !validMaster(input, actor.Country) {
		return Master{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Master{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = masterAuthority(ctx, tx, actor, true); err != nil {
		return Master{}, err
	}
	// Serialize parent activation and child edits under a bounded country lock.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":"+actor.Country+":masters"); err != nil {
		return Master{}, err
	}
	current, err := scanMaster(tx.QueryRow(ctx, `SELECT `+masterColumns+` FROM admin.master_data WHERE tenant_id=$1 AND country=$2 AND id=$3 FOR UPDATE`, actor.TenantID, actor.Country, input.ID))
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Master{}, err
	}
	if exists && (current.Revision != input.Revision || current.Kind != input.Kind || current.Code != input.Code || current.ParentID != input.ParentID) || !exists && input.Revision != 0 {
		return Master{}, ErrConflict
	}
	parentKind, _ := masterParent(input.Kind)
	if parentKind != "" {
		parent, lookupErr := scanMaster(tx.QueryRow(ctx, `SELECT `+masterColumns+` FROM admin.master_data WHERE tenant_id=$1 AND country=$2 AND id=$3`, actor.TenantID, actor.Country, input.ParentID))
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return Master{}, ErrInvalid
		}
		if lookupErr != nil {
			return Master{}, lookupErr
		}
		if parent.Kind != parentKind || input.Active && !parent.Active {
			return Master{}, ErrInvalid
		}
	}
	if !input.Active {
		var activeChild bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin.master_data WHERE tenant_id=$1 AND country=$2 AND parent_id=$3 AND active)`, actor.TenantID, actor.Country, input.ID).Scan(&activeChild); err != nil {
			return Master{}, err
		}
		if activeChild {
			return Master{}, ErrConflict
		}
	}
	value := input.Master
	value.Revision++
	_, err = tx.Exec(ctx, `INSERT INTO admin.master_data(tenant_id,country,id,kind,code,parent_id,name_en,name_ta,active,revision) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8,$9,$10) ON CONFLICT(tenant_id,country,id) DO UPDATE SET name_en=EXCLUDED.name_en,name_ta=EXCLUDED.name_ta,active=EXCLUDED.active,revision=EXCLUDED.revision`, actor.TenantID, actor.Country, value.ID, value.Kind, value.Code, value.ParentID, value.NameEN, value.NameTA, value.Active, value.Revision)
	if err != nil {
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23505" {
			return Master{}, ErrConflict
		}
		return Master{}, err
	}
	definition, err := json.Marshal(value)
	if err != nil {
		return Master{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin.master_data_versions(tenant_id,country,id,revision,definition,actor_id,reason,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor.TenantID, actor.Country, value.ID, value.Revision, definition, actor.Subject, input.Reason, service.clock().UTC()); err != nil {
		return Master{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Master{}, err
	}
	return value, nil
}

func (service *Service) MasterHistory(ctx context.Context, actor Actor, id string, before int64) (MasterHistory, error) {
	page := MasterHistory{Items: []MasterVersion{}}
	if uuid.Validate(id) != nil || before < 0 {
		return page, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = masterAuthority(ctx, tx, actor, false); err != nil {
		return page, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin.master_data WHERE tenant_id=$1 AND country=$2 AND id=$3)`, actor.TenantID, actor.Country, id).Scan(&exists); err != nil {
		return page, err
	}
	if !exists {
		return page, ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT definition,actor_id::text,reason,occurred_at FROM admin.master_data_versions WHERE tenant_id=$1 AND country=$2 AND id=$3 AND ($4::bigint=0 OR revision<$4) ORDER BY revision DESC LIMIT 51`, actor.TenantID, actor.Country, id, before)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var value MasterVersion
		var raw []byte
		if err = rows.Scan(&raw, &value.ActorID, &value.Reason, &value.OccurredAt); err != nil {
			rows.Close()
			return page, err
		}
		if err = json.Unmarshal(raw, &value.Definition); err != nil {
			rows.Close()
			return page, err
		}
		page.Items = append(page.Items, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Items) > 50 {
		page.NextBefore = page.Items[49].Definition.Revision
		page.Items = page.Items[:50]
	}
	return page, tx.Commit(ctx)
}
