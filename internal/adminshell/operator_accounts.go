package adminshell

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrOperatorConflict = errors.New("operator revision or identity conflict")
	ErrOperatorNotFound = errors.New("operator not found")
)

type OperatorAccount struct {
	SubjectID        string   `json:"subject_id"`
	ProviderSubject  string   `json:"provider_subject"`
	DisplayName      string   `json:"display_name"`
	Roles            []Role   `json:"roles"`
	AllowedCountries []string `json:"allowed_countries"`
	Active           bool     `json:"active"`
	Revision         int64    `json:"revision"`
}
type OperatorChange struct {
	DisplayName      string   `json:"display_name"`
	Roles            []Role   `json:"roles"`
	AllowedCountries []string `json:"allowed_countries"`
	Active           *bool    `json:"active"`
	Revision         int64    `json:"revision"`
	Reason           string   `json:"reason"`
}
type OperatorCreate struct {
	ProviderSubject string `json:"provider_subject"`
	OperatorChange
}
type OperatorPage struct {
	Items     []OperatorAccount `json:"items"`
	NextAfter string            `json:"next_after,omitempty"`
}
type OperatorEvent struct {
	ID               string    `json:"id"`
	ActorID          string    `json:"actor_id"`
	Country          string    `json:"country"`
	Action           string    `json:"action"`
	Revision         int64     `json:"revision"`
	Roles            []Role    `json:"roles"`
	AllowedCountries []string  `json:"allowed_countries"`
	Active           bool      `json:"active"`
	Reason           string    `json:"reason"`
	OccurredAt       time.Time `json:"occurred_at"`
}
type OperatorEventPage struct {
	Items      []OperatorEvent `json:"items"`
	NextBefore int64           `json:"next_before,omitempty"`
}

func operatorText(value string, min, max int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) >= min && utf8.RuneCountInString(value) <= max && !strings.ContainsFunc(value, unicode.IsControl)
}
func validOperatorChange(input OperatorChange) bool {
	if !operatorText(input.DisplayName, 1, 128) || !operatorText(input.Reason, 8, 512) || input.Active == nil || len(input.Roles) < 1 || len(input.Roles) > 5 || len(input.AllowedCountries) < 1 || len(input.AllowedCountries) > 32 || input.Revision < 0 {
		return false
	}
	roles, countries := map[Role]bool{}, map[string]bool{}
	for _, role := range input.Roles {
		// Root accounts stay under deployment recovery governance. This workflow
		// cannot create another root, edit one, or change its own authority.
		if !validRole(role) || role == RoleSuperAdmin || roles[role] {
			return false
		}
		roles[role] = true
	}
	if roles[RoleFinance] && !roles[RoleCountryAdmin] {
		return false
	}
	for _, country := range input.AllowedCountries {
		if !validCountry(country) || countries[country] {
			return false
		}
		countries[country] = true
	}
	return true
}

// Every directory request checks the current server-owned actor, even when a
// trusted session exchange (rather than Firebase login) issued the cookie.
// The tenant lock serializes operator changes, including concurrent revocation
// of the administrator performing a change. It is held through audit commit.
func (accounts *OperatorAccounts) withAuthority(ctx context.Context, actor Principal, fresh bool, now time.Time, work func(context.Context, pgx.Tx, []string) error) error {
	assurance := assuranceFor(actor, now)
	if !validPrincipal(actor) || !canonicalUUID(actor.SubjectID) || !canonicalUUID(actor.TenantID) || !containsRole(actor.Roles, RoleSuperAdmin) || !assurance.MFASatisfied || fresh && !assurance.FreshAuth {
		return ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, adminSessionTimeout)
	defer cancel()
	tx, err := accounts.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, actor.TenantID+":operators"); err != nil {
		return err
	}
	var roles []Role
	var countries []string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT roles,allowed_countries,revision FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 AND active FOR SHARE`, actor.TenantID, actor.SubjectID).Scan(&roles, &countries, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !containsRole(roles, RoleSuperAdmin) || !contains(countries, actor.SelectedCountry) || contains(actor.AuthMethods, "firebase_verified") && revision != actor.OperatorRevision {
		return ErrForbidden
	}
	effective := []string{}
	for _, country := range countries {
		if contains(actor.AllowedCountries, country) {
			effective = append(effective, country)
		}
	}
	if err = work(ctx, tx, effective); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func containsRole(roles []Role, target Role) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

const operatorColumns = `subject_id::text,provider_subject,display_name,roles,allowed_countries,active,revision`

func scanOperator(row pgx.Row) (OperatorAccount, error) {
	var value OperatorAccount
	err := row.Scan(&value.SubjectID, &value.ProviderSubject, &value.DisplayName, &value.Roles, &value.AllowedCountries, &value.Active, &value.Revision)
	return value, err
}

func (accounts *OperatorAccounts) List(ctx context.Context, actor Principal, search, after string, now time.Time) (OperatorPage, error) {
	page := OperatorPage{Items: []OperatorAccount{}}
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 128 || strings.ContainsFunc(search, unicode.IsControl) || after != "" && !canonicalUUID(after) {
		return page, ErrInvalidRequest
	}
	err := accounts.withAuthority(ctx, actor, false, now, func(ctx context.Context, tx pgx.Tx, countries []string) error {
		rows, err := tx.Query(ctx, `SELECT `+operatorColumns+` FROM admin.operator_accounts WHERE tenant_id=$1 AND $2=ANY(allowed_countries) AND allowed_countries <@ $3::text[] AND ($4='' OR subject_id > NULLIF($4,'')::uuid) AND ($5='' OR strpos(lower(display_name),lower($5))>0 OR strpos(subject_id::text,$5)>0) ORDER BY subject_id LIMIT 51`, actor.TenantID, actor.SelectedCountry, countries, after, search)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, err := scanOperator(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, value)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > 50 {
			page.Items = page.Items[:50]
			page.NextAfter = page.Items[49].SubjectID
		}
		return nil
	})
	return page, err
}

func (accounts *OperatorAccounts) Save(ctx context.Context, actor Principal, id, providerSubject string, input OperatorChange, now time.Time) (OperatorAccount, error) {
	var saved OperatorAccount
	create := id == ""
	if !validOperatorChange(input) || create && (input.Revision != 0 || !operatorText(providerSubject, 1, 128)) || !create && (!canonicalUUID(id) || input.Revision < 1 || providerSubject != "") {
		return saved, ErrInvalidRequest
	}
	if !create && id == actor.SubjectID {
		return saved, ErrForbidden
	}
	err := accounts.withAuthority(ctx, actor, true, now, func(ctx context.Context, tx pgx.Tx, countries []string) error {
		if !contains(input.AllowedCountries, actor.SelectedCountry) {
			return ErrForbidden
		}
		for _, country := range input.AllowedCountries {
			if !contains(countries, country) {
				return ErrForbidden
			}
		}
		if create {
			id = uuid.NewString()
			var err error
			saved, err = scanOperator(tx.QueryRow(ctx, `INSERT INTO admin.operator_accounts(tenant_id,subject_id,provider_subject,display_name,roles,allowed_countries,active) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+operatorColumns, actor.TenantID, id, providerSubject, input.DisplayName, input.Roles, input.AllowedCountries, *input.Active))
			if err != nil {
				var conflict *pgconn.PgError
				if errors.As(err, &conflict) && conflict.Code == "23505" {
					return ErrOperatorConflict
				}
				return err
			}
		} else {
			current, err := scanOperator(tx.QueryRow(ctx, `SELECT `+operatorColumns+` FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 AND $3=ANY(allowed_countries) AND allowed_countries <@ $4::text[] FOR UPDATE`, actor.TenantID, id, actor.SelectedCountry, countries))
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrOperatorNotFound
			}
			if err != nil {
				return err
			}
			if containsRole(current.Roles, RoleSuperAdmin) {
				return ErrForbidden
			}
			if current.Revision != input.Revision {
				return ErrOperatorConflict
			}
			saved, err = scanOperator(tx.QueryRow(ctx, `UPDATE admin.operator_accounts SET display_name=$3,roles=$4,allowed_countries=$5,active=$6,revision=revision+1 WHERE tenant_id=$1 AND subject_id=$2 RETURNING `+operatorColumns, actor.TenantID, id, input.DisplayName, input.Roles, input.AllowedCountries, *input.Active))
			if err != nil {
				return err
			}
			// A revision check also rejects sessions issued concurrently from an
			// earlier login lookup, and prevents revival after reactivation.
			if _, err = tx.Exec(ctx, `DELETE FROM admin.sessions WHERE tenant_id=$1 AND subject_id=$2`, actor.TenantID, id); err != nil {
				return err
			}
		}
		action := "UPDATED"
		if create {
			action = "CREATED"
		}
		_, err := tx.Exec(ctx, `INSERT INTO admin.operator_events(id,tenant_id,subject_id,actor_id,country,action,revision,roles,allowed_countries,active,reason,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, uuid.NewString(), actor.TenantID, id, actor.SubjectID, actor.SelectedCountry, action, saved.Revision, saved.Roles, saved.AllowedCountries, saved.Active, input.Reason, now.UTC())
		return err
	})
	if err != nil {
		return OperatorAccount{}, err
	}
	return saved, nil
}

func (accounts *OperatorAccounts) Events(ctx context.Context, actor Principal, id string, before int64, now time.Time) (OperatorEventPage, error) {
	page := OperatorEventPage{Items: []OperatorEvent{}}
	if !canonicalUUID(id) || before < 0 {
		return page, ErrInvalidRequest
	}
	err := accounts.withAuthority(ctx, actor, false, now, func(ctx context.Context, tx pgx.Tx, countries []string) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin.operator_accounts WHERE tenant_id=$1 AND subject_id=$2 AND $3=ANY(allowed_countries) AND allowed_countries <@ $4::text[])`, actor.TenantID, id, actor.SelectedCountry, countries).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrOperatorNotFound
		}
		rows, err := tx.Query(ctx, `SELECT id,actor_id,country,action,revision,roles,allowed_countries,active,reason,occurred_at FROM admin.operator_events WHERE tenant_id=$1 AND subject_id=$2 AND $3=ANY(allowed_countries) AND allowed_countries <@ $4::text[] AND ($5=0 OR revision<$5) ORDER BY revision DESC LIMIT 51`, actor.TenantID, id, actor.SelectedCountry, countries, before)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event OperatorEvent
			if err := rows.Scan(&event.ID, &event.ActorID, &event.Country, &event.Action, &event.Revision, &event.Roles, &event.AllowedCountries, &event.Active, &event.Reason, &event.OccurredAt); err != nil {
				return err
			}
			page.Items = append(page.Items, event)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > 50 {
			page.Items = page.Items[:50]
			page.NextBefore = page.Items[49].Revision
		}
		return nil
	})
	return page, err
}
