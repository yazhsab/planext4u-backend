package social

import (
	"encoding/base64"
	"encoding/json"
)

const (
	defaultSocialPageLimit = 20
	maximumSocialPageLimit = 50
)

type socialListCursor struct {
	Scope  string `json:"scope"`
	Offset int    `json:"offset"`
}

func paginateSocial[T any](values []T, cursor string, limit int, scope string) ([]T, string, error) {
	if limit == 0 {
		limit = defaultSocialPageLimit
	}
	if limit < 1 || limit > maximumSocialPageLimit {
		return nil, "", ErrInvalidRequest
	}
	offset := 0
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", ErrInvalidRequest
		}
		var value socialListCursor
		if json.Unmarshal(decoded, &value) != nil || value.Scope != scope || value.Offset < 0 {
			return nil, "", ErrInvalidRequest
		}
		offset = value.Offset
	}
	if offset > len(values) {
		return nil, "", ErrInvalidRequest
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	next := ""
	if end < len(values) {
		payload, err := json.Marshal(socialListCursor{Scope: scope, Offset: end})
		if err != nil {
			return nil, "", ErrInvalidRequest
		}
		next = base64.RawURLEncoding.EncodeToString(payload)
	}
	return append([]T(nil), values[offset:end]...), next, nil
}
