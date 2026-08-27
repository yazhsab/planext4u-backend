package catalog

import "time"

type ProjectionStatus string

const (
	ProjectionFresh    ProjectionStatus = "FRESH"
	ProjectionStale    ProjectionStatus = "STALE"
	ProjectionDegraded ProjectionStatus = "DEGRADED"
)

type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type Variant struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Price          Money  `json:"price"`
	CompareAtPrice *Money `json:"compare_at_price,omitempty"`
	Available      bool   `json:"available"`
	StockQuantity  int    `json:"stock_quantity"`
	MaxPerOrder    int    `json:"max_per_order"`
}

type Category struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IconRef  string `json:"icon_ref,omitempty"`
	Priority int    `json:"priority"`
}

type Item struct {
	ID                  string            `json:"id"`
	CategoryID          string            `json:"category_id"`
	Name                string            `json:"name"`
	Summary             string            `json:"summary"`
	MediaRef            string            `json:"media_ref,omitempty"`
	Price               Money             `json:"price"`
	Available           bool              `json:"available"`
	SellerName          string            `json:"seller_name,omitempty"`
	VerifiedLocalSeller bool              `json:"verified_local_seller,omitempty"`
	Description         string            `json:"description,omitempty"`
	Specifications      map[string]string `json:"specifications,omitempty"`
	RatingAverage       float64           `json:"rating_average,omitempty"`
	ReviewCount         int               `json:"review_count,omitempty"`
	Variants            []Variant         `json:"variants,omitempty"`
	SearchTerms         []string          `json:"-"`
}

type Page[T any] struct {
	Items            []T              `json:"items"`
	NextCursor       string           `json:"next_cursor,omitempty"`
	HasMore          bool             `json:"has_more"`
	ProjectionStatus ProjectionStatus `json:"projection_status"`
	GeneratedAt      time.Time        `json:"generated_at"`
}

type Home struct {
	Categories       []Category       `json:"categories"`
	FeaturedItems    []Item           `json:"featured_items"`
	ProjectionStatus ProjectionStatus `json:"projection_status"`
	GeneratedAt      time.Time        `json:"generated_at"`
}

type GeoPoint struct {
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	AccuracyMetres float64   `json:"accuracy_metres"`
	CapturedAt     time.Time `json:"captured_at"`
	Purpose        string    `json:"purpose"`
}

type Serviceability struct {
	Serviceable bool   `json:"serviceable"`
	ZoneID      string `json:"zone_id,omitempty"`
	Locality    string `json:"locality,omitempty"`
	ReasonCode  string `json:"reason_code"`
}
