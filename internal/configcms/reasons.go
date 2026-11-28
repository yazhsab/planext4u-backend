package configcms

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Reason struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	NameEN         string `json:"name_en"`
	NameTA         string `json:"name_ta"`
	MasterRevision int64  `json:"master_revision"`
}

type ReasonPage struct {
	Country   string   `json:"country"`
	Items     []Reason `json:"items"`
	NextAfter string   `json:"next_after,omitempty"`
}

type ReasonDirectory interface {
	Reasons(context.Context, string, string, string, string) (ReasonPage, error)
}

type PostgresReasonDirectory struct{ pool *pgxpool.Pool }

func NewPostgresReasonDirectory(pool *pgxpool.Pool) (*PostgresReasonDirectory, error) {
	if pool == nil {
		return nil, ErrInvalidRepository
	}
	return &PostgresReasonDirectory{pool: pool}, nil
}

func (directory *PostgresReasonDirectory) Reasons(ctx context.Context, tenant, country, search, after string) (ReasonPage, error) {
	page := ReasonPage{Country: country, Items: []Reason{}}
	if !validOccupationQuery(tenant, country, search, after) {
		return page, ErrInvalidSnapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := directory.pool.Query(ctx, `SELECT id::text,code,name_en,name_ta,master_revision FROM admin.active_reasons($1,$2,$3,NULLIF($4,'')::uuid)`, tenant, country, search, after)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Reason
		if err = rows.Scan(&item.ID, &item.Code, &item.NameEN, &item.NameTA, &item.MasterRevision); err != nil {
			return ReasonPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return ReasonPage{}, err
	}
	if len(page.Items) > 50 {
		page.NextAfter = page.Items[49].ID
		page.Items = page.Items[:50]
	}
	return page, nil
}

func (handler *Handler) reasonList(writer http.ResponseWriter, request *http.Request) {
	tenant, country := request.Header.Get("X-Planext4u-Tenant"), request.Header.Get("X-Planext4u-Country")
	query := request.URL.Query()
	for key, values := range query {
		if len(values) != 1 || key != "search" && key != "after" {
			writeProblem(writer, request, 422, "REASON_QUERY_INVALID", "Check the reason search and try again.")
			return
		}
	}
	if !validOccupationQuery(tenant, country, query.Get("search"), query.Get("after")) {
		writeProblem(writer, request, 422, "REASON_QUERY_INVALID", "Check the reason search and try again.")
		return
	}
	if handler.reasons == nil {
		writeProblem(writer, request, 503, "REASONS_UNAVAILABLE", "Reasons are temporarily unavailable. Try again.")
		return
	}
	page, err := handler.reasons.Reasons(request.Context(), tenant, country, query.Get("search"), query.Get("after"))
	if err != nil {
		writeProblem(writer, request, 503, "REASONS_UNAVAILABLE", "Reasons are temporarily unavailable. Try again.")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(page)
}
