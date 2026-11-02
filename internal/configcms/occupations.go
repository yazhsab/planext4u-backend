package configcms

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Occupation struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	NameEN         string `json:"name_en"`
	NameTA         string `json:"name_ta"`
	MasterRevision int64  `json:"master_revision"`
}
type OccupationPage struct {
	Country   string       `json:"country"`
	Items     []Occupation `json:"items"`
	NextAfter string       `json:"next_after,omitempty"`
}
type OccupationDirectory interface {
	Occupations(context.Context, string, string, string, string) (OccupationPage, error)
}
type PostgresOccupationDirectory struct{ pool *pgxpool.Pool }

func NewPostgresOccupationDirectory(pool *pgxpool.Pool) (*PostgresOccupationDirectory, error) {
	if pool == nil {
		return nil, ErrInvalidRepository
	}
	return &PostgresOccupationDirectory{pool: pool}, nil
}
func validOccupationQuery(tenant, country, search, after string) bool {
	return uuid.Validate(tenant) == nil && len(country) == 2 && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z' && utf8.ValidString(search) && utf8.RuneCountInString(search) <= 128 && !strings.ContainsFunc(search, unicode.IsControl) && (after == "" || uuid.Validate(after) == nil)
}
func (directory *PostgresOccupationDirectory) Occupations(ctx context.Context, tenant, country, search, after string) (OccupationPage, error) {
	value := OccupationPage{Country: country, Items: []Occupation{}}
	if !validOccupationQuery(tenant, country, search, after) {
		return value, ErrInvalidSnapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := directory.pool.Query(ctx, `SELECT id::text,code,name_en,name_ta,master_revision FROM admin.active_occupations($1,$2,$3,NULLIF($4,'')::uuid)`, tenant, country, search, after)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Occupation
		if err = rows.Scan(&item.ID, &item.Code, &item.NameEN, &item.NameTA, &item.MasterRevision); err != nil {
			return OccupationPage{}, err
		}
		value.Items = append(value.Items, item)
	}
	if err = rows.Err(); err != nil {
		return OccupationPage{}, err
	}
	if len(value.Items) > 50 {
		value.NextAfter = value.Items[49].ID
		value.Items = value.Items[:50]
	}
	return value, nil
}
func (handler *Handler) occupationList(writer http.ResponseWriter, request *http.Request) {
	tenant, country := request.Header.Get("X-Planext4u-Tenant"), request.Header.Get("X-Planext4u-Country")
	query := request.URL.Query()
	for key, values := range query {
		if len(values) != 1 || key != "search" && key != "after" {
			writeProblem(writer, request, 422, "OCCUPATION_QUERY_INVALID", "Check the occupation search and try again.")
			return
		}
	}
	if !validOccupationQuery(tenant, country, query.Get("search"), query.Get("after")) {
		writeProblem(writer, request, 422, "OCCUPATION_QUERY_INVALID", "Check the occupation search and try again.")
		return
	}
	if handler.occupations == nil {
		writeProblem(writer, request, 503, "OCCUPATIONS_UNAVAILABLE", "Occupations are temporarily unavailable. Try again.")
		return
	}
	result, err := handler.occupations.Occupations(request.Context(), tenant, country, query.Get("search"), query.Get("after"))
	if err != nil {
		writeProblem(writer, request, 503, "OCCUPATIONS_UNAVAILABLE", "Occupations are temporarily unavailable. Try again.")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
}
