package configcms

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errAudienceUnavailable = errors.New("current audience membership unavailable")

type AudienceMembershipResolver interface {
	Memberships(context.Context, string, string, string, []string) (map[string]bool, error)
}

type PostgresAudienceResolver struct{ pool *pgxpool.Pool }

func NewPostgresAudienceResolver(pool *pgxpool.Pool) (*PostgresAudienceResolver, error) {
	if pool == nil {
		return nil, ErrInvalidRepository
	}
	return &PostgresAudienceResolver{pool: pool}, nil
}

func (resolver *PostgresAudienceResolver) Memberships(ctx context.Context, tenant, country, subject string, groups []string) (map[string]bool, error) {
	result := map[string]bool{}
	if uuid.Validate(tenant) != nil || uuid.Validate(subject) != nil || len(country) != 2 {
		return result, nil
	}
	if len(groups) > 1024 {
		return nil, errAudienceUnavailable
	}
	if len(groups) == 0 {
		return result, nil
	}
	for _, group := range groups {
		if uuid.Validate(group) != nil {
			return nil, errAudienceUnavailable
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := resolver.pool.Query(ctx, `SELECT value::text,admin.audience_member($1,$2,$3,value) FROM unnest($4::uuid[]) AS requested(value)`, tenant, country, subject, groups)
	if err != nil {
		return nil, errAudienceUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var group string
		var allowed bool
		if err = rows.Scan(&group, &allowed); err != nil {
			return nil, errAudienceUnavailable
		}
		result[group] = allowed
	}
	if rows.Err() != nil || len(result) != len(groups) {
		return nil, errAudienceUnavailable
	}
	return result, nil
}

// Base role visibility remains mandatory. Groups only narrow it, with OR
// semantics among a page's groups. Resolve every read; never cache membership
// alongside a published snapshot or accept caller query/body authority.
func (handler *Handler) visibleAudiencePages(request *http.Request, tenant, country string, pages []Page) ([]Page, error) {
	eligible := make([]Page, 0, len(pages))
	groupSet := map[string]bool{}
	for _, page := range pages {
		if pageVisible(page, request.Header.Get("X-Planext4u-Roles")) {
			eligible = append(eligible, page)
			for _, group := range page.AudienceGroups {
				groupSet[group] = true
			}
		}
	}
	memberships := map[string]bool{}
	subject := request.Header.Get("X-Planext4u-Subject")
	if len(groupSet) > 0 && subject != "" {
		if handler.audiences == nil {
			return nil, errAudienceUnavailable
		}
		groups := make([]string, 0, len(groupSet))
		for group := range groupSet {
			groups = append(groups, group)
		}
		sort.Strings(groups)
		var err error
		memberships, err = handler.audiences.Memberships(request.Context(), tenant, country, subject, groups)
		if err != nil {
			return nil, errAudienceUnavailable
		}
	}
	visible := make([]Page, 0, len(eligible))
	for _, page := range eligible {
		allowed := len(page.AudienceGroups) == 0
		for _, group := range page.AudienceGroups {
			allowed = allowed || memberships[group]
		}
		if allowed {
			visible = append(visible, page)
		}
	}
	return visible, nil
}
