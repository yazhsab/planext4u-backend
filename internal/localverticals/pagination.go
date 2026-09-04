package localverticals

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const (
	defaultListingPageSize = 20
	maximumListingPageSize = 50
)

type listingCursor struct {
	Featured bool   `json:"featured"`
	Updated  int64  `json:"updated"`
	ID       string `json:"id"`
}

func listingPageBounds(limit int, encoded string) (int, *listingCursor, error) {
	if limit == 0 {
		limit = defaultListingPageSize
	}
	if limit < 1 || limit > maximumListingPageSize {
		return 0, nil, ErrInvalidRequest
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return limit, nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, nil, ErrInvalidRequest
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var cursor listingCursor
	if err := decoder.Decode(&cursor); err != nil || cursor.Updated < 1 || !safeID(cursor.ID) {
		return 0, nil, ErrInvalidRequest
	}
	return limit, &cursor, nil
}

func encodeListingCursor(featured bool, updatedAt time.Time, id string) string {
	payload, _ := json.Marshal(listingCursor{Featured: featured, Updated: updatedAt.UTC().UnixNano(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func listingIsAfterCursor(featured bool, updatedAt time.Time, id string, cursor *listingCursor) bool {
	if cursor == nil {
		return true
	}
	if featured != cursor.Featured {
		return cursor.Featured && !featured
	}
	updated := updatedAt.UTC().UnixNano()
	return updated < cursor.Updated || updated == cursor.Updated && id < cursor.ID
}

func homeFeatured(value HomeListing, now time.Time) bool {
	return value.FeaturedUntil != nil && value.FeaturedUntil.After(now)
}

func classifiedFeatured(value ClassifiedListing, now time.Time) bool {
	return value.FeaturedUntil != nil && value.FeaturedUntil.After(now)
}

func publicHome(value HomeListing) PublicHomeListing {
	return PublicHomeListing{
		ID: value.ID, Revision: value.Revision, Title: value.Title, PropertyType: value.PropertyType,
		Purpose: value.Purpose, Locality: value.Locality, AreaSqFt: value.AreaSqFt, Bedrooms: value.Bedrooms,
		Price: value.Price, Amenities: append([]string{}, value.Amenities...), Media: []MediaPresentation{},
		KYCVerified: value.KYCVerified, Plan: value.Plan, FeaturedUntil: value.FeaturedUntil, Estimate: value.Estimate,
		AllowedActions: append([]string{}, value.AllowedActions...), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func publicHomes(page HomePage) PublicHomePage {
	result := PublicHomePage{Items: make([]PublicHomeListing, len(page.Items)), NextCursor: page.NextCursor}
	for index, value := range page.Items {
		result.Items[index] = publicHome(value)
	}
	return result
}

func publicClassified(value ClassifiedListing) PublicClassifiedListing {
	return PublicClassifiedListing{
		ID: value.ID, Revision: value.Revision, Category: value.Category, Title: value.Title,
		Description: value.Description, Price: value.Price, Locality: value.Locality, Media: []MediaPresentation{},
		Plan: value.Plan, ContactMasked: value.ContactMasked, WhatsAppEnabled: value.WhatsAppEnabled,
		ExpiresAt: value.ExpiresAt, FeaturedUntil: value.FeaturedUntil,
		AllowedActions: append([]string{}, value.AllowedActions...), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func publicClassifieds(page ClassifiedPage) PublicClassifiedPage {
	result := PublicClassifiedPage{Items: make([]PublicClassifiedListing, len(page.Items)), NextCursor: page.NextCursor}
	for index, value := range page.Items {
		result.Items[index] = publicClassified(value)
	}
	return result
}
