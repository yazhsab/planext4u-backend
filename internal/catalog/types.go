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

type ResponsiveMediaVariant struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type MediaPresentation struct {
	AssetID     string                   `json:"asset_id"`
	URL         string                   `json:"url"`
	ContentType string                   `json:"content_type"`
	Width       int                      `json:"width"`
	Height      int                      `json:"height"`
	AltText     string                   `json:"alt_text"`
	Variants    []ResponsiveMediaVariant `json:"variants"`
	ExpiresAt   *time.Time               `json:"expires_at"`
}

type Category struct {
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	IconRef  string             `json:"icon_ref,omitempty"`
	Icon     *MediaPresentation `json:"icon,omitempty"`
	Priority int                `json:"priority"`
}

type Review struct {
	ID                string    `json:"id"`
	AuthorDisplayName string    `json:"author_display_name"`
	Score             int       `json:"score"`
	Body              string    `json:"body"`
	VerifiedPurchase  bool      `json:"verified_purchase"`
	CreatedAt         time.Time `json:"created_at"`
}

type Question struct {
	ID         string     `json:"id"`
	Question   string     `json:"question"`
	AskedBy    string     `json:"asked_by"`
	AskedByID  string     `json:"-"`
	AskedAt    time.Time  `json:"asked_at"`
	Answer     string     `json:"answer,omitempty"`
	AnsweredBy string     `json:"answered_by,omitempty"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
}

type AskQuestionInput struct {
	Question string `json:"question"`
}

type Item struct {
	ID                  string              `json:"id"`
	VendorID            string              `json:"vendor_id,omitempty"`
	CategoryID          string              `json:"category_id"`
	Name                string              `json:"name"`
	Summary             string              `json:"summary"`
	MediaRef            string              `json:"media_ref,omitempty"`
	MediaRefs           []string            `json:"media_refs,omitempty"`
	Media               []MediaPresentation `json:"media,omitempty"`
	Price               Money               `json:"price"`
	Available           bool                `json:"available"`
	SellerName          string              `json:"seller_name,omitempty"`
	VerifiedLocalSeller bool                `json:"verified_local_seller,omitempty"`
	Description         string              `json:"description,omitempty"`
	Specifications      map[string]string   `json:"specifications,omitempty"`
	RatingAverage       float64             `json:"rating_average,omitempty"`
	ReviewCount         int                 `json:"review_count,omitempty"`
	Variants            []Variant           `json:"variants,omitempty"`
	DeliveryEstimate    string              `json:"delivery_estimate,omitempty"`
	Reviews             []Review            `json:"reviews,omitempty"`
	Questions           []Question          `json:"questions,omitempty"`
	RelatedItemIDs      []string            `json:"related_item_ids,omitempty"`
	SearchTerms         []string            `json:"-"`
}

type Page[T any] struct {
	Items            []T              `json:"items"`
	NextCursor       string           `json:"next_cursor,omitempty"`
	HasMore          bool             `json:"has_more"`
	ProjectionStatus ProjectionStatus `json:"projection_status"`
	GeneratedAt      time.Time        `json:"generated_at"`
}

type Home struct {
	Categories         []Category                   `json:"categories"`
	FeaturedItems      []Item                       `json:"featured_items"`
	Recommendations    []Item                       `json:"recommendations"`
	Leaderboard        []SellerLeader               `json:"leaderboard"`
	HelpShortcuts      []HelpShortcut               `json:"help_shortcuts"`
	ServiceCollections map[string]ServiceCollection `json:"service_collections"`
	ProjectionStatus   ProjectionStatus             `json:"projection_status"`
	GeneratedAt        time.Time                    `json:"generated_at"`
}

type SellerLeader struct {
	SellerName    string  `json:"seller_name"`
	Verified      bool    `json:"verified"`
	RatingAverage float64 `json:"rating_average"`
	ReviewCount   int     `json:"review_count"`
}

type HelpShortcut struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Route string `json:"route"`
}

type ServiceTrustSummary struct {
	VerifiedProvider  bool    `json:"verified_provider"`
	RatingAverage     float64 `json:"rating_average"`
	CompletedBookings int     `json:"completed_bookings"`
}

type ServiceCollectionItem struct {
	ServiceID        string              `json:"service_id"`
	ProviderID       string              `json:"provider_id"`
	Title            string              `json:"title"`
	Summary          string              `json:"summary"`
	Media            *MediaPresentation  `json:"media"`
	Price            Money               `json:"price"`
	PriceDisplay     string              `json:"price_display"`
	Serviceable      bool                `json:"serviceable"`
	Trust            ServiceTrustSummary `json:"trust"`
	NavigationTarget string              `json:"navigation_target"`
}

type ServiceCollection struct {
	CollectionID string                  `json:"collection_id"`
	Title        string                  `json:"title"`
	Items        []ServiceCollectionItem `json:"items"`
}

// ServiceCollectionProjection is the internal materialized view populated from
// published CMS collection IDs and booking-owned offering data. Postal codes
// are deliberately excluded from the public home response.
type ServiceCollectionProjection struct {
	Collection         ServiceCollection
	ServicePostalCodes map[string][]string
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

type GeocodeCandidate struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Locality   string  `json:"locality"`
	PostalCode string  `json:"postal_code"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
}

type Suggestion struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Label    string `json:"label"`
	Subtitle string `json:"subtitle,omitempty"`
	ItemID   string `json:"item_id,omitempty"`
}
