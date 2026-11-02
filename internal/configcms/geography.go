package configcms

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"time"
)

type GeographyReference struct {
	ID             string  `json:"id"`
	Country        string  `json:"country"`
	Kind           string  `json:"kind"`
	Code           string  `json:"code"`
	ParentID       *string `json:"parent_id"`
	NameEN         string  `json:"name_en"`
	NameTA         string  `json:"name_ta"`
	MasterRevision int64   `json:"master_revision"`
}
type GeographyPage struct {
	Country   string               `json:"country"`
	Kind      string               `json:"kind"`
	ParentID  string               `json:"parent_id,omitempty"`
	Items     []GeographyReference `json:"items"`
	NextAfter string               `json:"next_after,omitempty"`
}
type GeographyDirectory interface {
	Geography(context.Context, string, string, string, string, string, string) (GeographyPage, error)
}
type PostgresGeographyDirectory struct{ pool *pgxpool.Pool }

func NewPostgresGeographyDirectory(pool *pgxpool.Pool) (*PostgresGeographyDirectory, error) {
	if pool == nil {
		return nil, ErrInvalidRepository
	}
	return &PostgresGeographyDirectory{pool: pool}, nil
}
func validGeographyQuery(tenant, country, kind, parent, search, after string) bool {
	if !validOccupationQuery(tenant, country, search, after) {
		return false
	}
	switch kind {
	case "COUNTRY":
		return parent == ""
	case "STATE", "DISTRICT", "CITY", "AREA":
		return uuid.Validate(parent) == nil
	}
	return false
}
func (directory *PostgresGeographyDirectory) Geography(ctx context.Context, tenant, country, kind, parent, search, after string) (GeographyPage, error) {
	result := GeographyPage{Country: country, Kind: kind, ParentID: parent, Items: []GeographyReference{}}
	if !validGeographyQuery(tenant, country, kind, parent, search, after) {
		return result, ErrInvalidSnapshot
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := directory.pool.Query(ctx, `SELECT id::text,kind,code,parent_id::text,name_en,name_ta,master_revision FROM admin.active_geography($1,$2,$3,NULLIF($4,'')::uuid,$5,NULLIF($6,'')::uuid)`, tenant, country, kind, parent, search, after)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item GeographyReference
		item.Country = country
		if err = rows.Scan(&item.ID, &item.Kind, &item.Code, &item.ParentID, &item.NameEN, &item.NameTA, &item.MasterRevision); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Items) > 50 {
		result.NextAfter = result.Items[49].ID
		result.Items = result.Items[:50]
	}
	return result, nil
}
func (handler *Handler) geographyList(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	tenant, country := request.Header.Get("X-Planext4u-Tenant"), request.Header.Get("X-Planext4u-Country")
	valid := true
	for key, values := range query {
		if len(values) != 1 || key != "kind" && key != "parent_id" && key != "search" && key != "after" {
			valid = false
		}
	}
	if !valid || !validGeographyQuery(tenant, country, query.Get("kind"), query.Get("parent_id"), query.Get("search"), query.Get("after")) {
		writeProblem(writer, request, 422, "GEOGRAPHY_QUERY_INVALID", "Choose a geography level and its parent.")
		return
	}
	if handler.geography == nil {
		writeProblem(writer, request, 503, "GEOGRAPHY_UNAVAILABLE", "Geography is temporarily unavailable. Try again.")
		return
	}
	result, err := handler.geography.Geography(request.Context(), tenant, country, query.Get("kind"), query.Get("parent_id"), query.Get("search"), query.Get("after"))
	if err != nil {
		writeProblem(writer, request, 503, "GEOGRAPHY_UNAVAILABLE", "Geography is temporarily unavailable. Try again.")
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
}
