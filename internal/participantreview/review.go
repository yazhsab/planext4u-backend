// Package participantreview defines the deliberately small administration
// projection. Private assets, bank references and extracted identity data never
// belong in queue responses.
package participantreview

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrForbidden = errors.New("current participant reviewer required")
var ErrInvalid = errors.New("invalid participant review filter")

type Scope struct {
	TenantID, Country, Subject, SourceSession string
	Revision                                  int64
	MFA                                       bool
}

func UUID(value string) bool { id, err := uuid.Parse(value); return err == nil && id.String() == value }

// Lock holds the source directory and session through the owning transaction.
// Call again before commit to check freshness against the database clock.
func Lock(ctx context.Context, tx pgx.Tx, scope Scope) error {
	if !scope.MFA || !UUID(scope.TenantID) || !UUID(scope.Subject) || !UUID(scope.SourceSession) || len(scope.Country) != 2 || scope.Revision < 1 {
		return ErrForbidden
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT admin.lock_participant_reviewer($1,$2,$3,$4,$5)`, scope.TenantID, scope.Country, scope.Subject, scope.SourceSession, scope.Revision).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

// LockAccess serializes current delegated participation with assignment,
// territory and binding changes. A nil target opens a filtered read only.
func LockAccess(ctx context.Context, tx pgx.Tx, scope Scope, kind, target, capability string) error {
	if !scope.MFA || !UUID(scope.TenantID) || !UUID(scope.Subject) || !UUID(scope.SourceSession) || scope.Revision < 1 || len(scope.Country) != 2 {
		return ErrForbidden
	}
	var participant any
	if target != "" {
		if !UUID(target) {
			return ErrForbidden
		}
		participant = target
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT admin.lock_participant_access($1,$2,$3,$4,$5,$6,$7,$8)`, scope.TenantID, scope.Country, scope.Subject, scope.SourceSession, scope.Revision, kind, participant, capability).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

type Filter struct{ Status, Search, After string }

func Parse(raw string, statuses []string) (Filter, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return Filter{}, ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 || key != "status" && key != "search" && key != "after" {
			return Filter{}, ErrInvalid
		}
	}
	value := Filter{Status: query.Get("status"), Search: strings.TrimSpace(query.Get("search")), After: query.Get("after")}
	if value.Status != "" {
		found := false
		for _, status := range statuses {
			found = found || value.Status == status
		}
		if !found {
			return Filter{}, ErrInvalid
		}
	}
	if !utf8.ValidString(value.Search) || utf8.RuneCountInString(value.Search) > 128 || value.After != "" && !UUID(value.After) {
		return Filter{}, ErrInvalid
	}
	for _, r := range value.Search {
		if unicode.IsControl(r) {
			return Filter{}, ErrInvalid
		}
	}
	return value, nil
}
func (f Filter) Validate(statuses []string) error {
	q := url.Values{"status": {f.Status}, "search": {f.Search}, "after": {f.After}}
	_, err := Parse(q.Encode(), statuses)
	return err
}

type Summary struct {
	ID            string    `json:"id"`
	ParticipantID string    `json:"participant_id"`
	Kind          string    `json:"kind"`
	Revision      int64     `json:"revision"`
	Status        string    `json:"status"`
	DisplayName   string    `json:"display_name"`
	Category      string    `json:"category"`
	DocumentCount int       `json:"document_count"`
	BankStatus    string    `json:"bank_status"`
	UpdatedAt     time.Time `json:"updated_at"`
	Actions       []string  `json:"actions"`
}
type Page struct {
	Items     []Summary `json:"items"`
	NextAfter string    `json:"next_after,omitempty"`
}

// RegionalSummary is the only queue wire shape for regional reviewers. Keep
// private review metadata out of the owner response as well as the admin BFF.
type RegionalSummary struct {
	ID            string    `json:"id"`
	ParticipantID string    `json:"participant_id"`
	Kind          string    `json:"kind"`
	Revision      int64     `json:"revision"`
	Status        string    `json:"status"`
	DisplayName   string    `json:"display_name"`
	Category      string    `json:"category"`
	UpdatedAt     time.Time `json:"updated_at"`
	Actions       []string  `json:"actions"`
}

type RegionalPage struct {
	Items     []RegionalSummary `json:"items"`
	NextAfter string            `json:"next_after,omitempty"`
}

func MinimalRegionalPage(page Page) RegionalPage {
	result := RegionalPage{Items: make([]RegionalSummary, 0, len(page.Items)), NextAfter: page.NextAfter}
	for _, item := range page.Items {
		result.Items = append(result.Items, RegionalSummary{
			ID: item.ID, ParticipantID: item.ParticipantID, Kind: item.Kind,
			Revision: item.Revision, Status: item.Status, DisplayName: item.DisplayName,
			Category: item.Category, UpdatedAt: item.UpdatedAt,
			Actions: append([]string{}, item.Actions...),
		})
	}
	return result
}

func Bounded(items []Summary) Page {
	page := Page{Items: items}
	if len(items) > 50 {
		page.Items = items[:50]
		page.NextAfter = page.Items[49].ID
	}
	return page
}
