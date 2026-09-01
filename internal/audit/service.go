package audit

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	repository Repository
	clock      func() time.Time
	newID      func() (string, error)
	mu         sync.Mutex
}

func NewService(repository Repository, clock func() time.Time) (*Service, error) {
	if repository == nil || clock == nil {
		return nil, errors.New("invalid audit service")
	}
	return &Service{repository: repository, clock: clock, newID: secureID}, nil
}

func (service *Service) Record(ctx context.Context, principal Principal, request RecordRequest) (Entry, error) {
	if !principal.Capabilities["audit.write"] || !safeID(principal.TenantID) || request.Actor.SubjectID != principal.SubjectID || !validRecord(request) {
		return Entry{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for attempt := 0; attempt < 8; attempt++ {
		last, found, err := service.repository.Last(ctx, principal.TenantID)
		if err != nil {
			return Entry{}, err
		}
		id, err := service.newID()
		if err != nil {
			return Entry{}, err
		}
		now := service.clock().UTC().Truncate(time.Microsecond)
		entry := Entry{ID: id, TenantID: principal.TenantID, Country: request.Country, Actor: request.Actor, Action: request.Action,
			Target: request.Target, Outcome: request.Outcome, ReasonCode: request.ReasonCode, CorrelationID: request.CorrelationID,
			OccurredAt: request.OccurredAt.UTC().Truncate(time.Microsecond), RecordedAt: now, Before: redact(request.Before), After: redact(request.After), Sequence: 1}
		if found {
			entry.Sequence, entry.PreviousHash = last.Sequence+1, last.Hash
		}
		entry.Hash = hashEntry(entry)
		if err := service.repository.Append(ctx, entry); err != nil {
			if errors.Is(err, ErrConflict) {
				continue
			}
			return Entry{}, err
		}
		return cloneEntry(entry), nil
	}
	return Entry{}, ErrConflict
}

func (service *Service) Search(ctx context.Context, principal Principal, filter SearchFilter) (Page, error) {
	if !principal.Capabilities["audit.read"] || !safeID(principal.TenantID) {
		return Page{}, ErrForbidden
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 200 || (!filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To)) {
		return Page{}, ErrInvalidRequest
	}
	filter.TenantID = principal.TenantID
	entries, err := service.repository.Search(ctx, filter)
	if err != nil {
		return Page{}, err
	}
	hasMore := len(entries) > filter.Limit
	if hasMore {
		entries = entries[:filter.Limit]
	}
	next := ""
	if hasMore {
		next = encodeCursor(entries[len(entries)-1].Sequence)
	}
	return Page{Entries: entries, NextCursor: next, HasMore: hasMore}, nil
}

func (service *Service) Export(ctx context.Context, principal Principal, reason string, filter SearchFilter) (Export, error) {
	now := service.clock().UTC()
	authAge := now.Sub(principal.AuthenticatedAt.UTC())
	if !principal.Capabilities["audit.export"] || principal.AuthenticatedAt.IsZero() || authAge < -time.Minute || authAge > 5*time.Minute || !safeReasonText(reason) {
		return Export{}, ErrForbidden
	}
	filter.TenantID, filter.After, filter.Limit = principal.TenantID, 0, 200
	entries, err := service.repository.Search(ctx, filter)
	if err != nil {
		return Export{}, err
	}
	return Export{GeneratedAt: now, Reason: reason, Entries: entries, ChainValid: verify(entries)}, nil
}

func DecodeCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, ErrInvalidRequest
	}
	var sequence int64
	if _, err := fmt.Sscanf(string(decoded), "v1:%d", &sequence); err != nil || sequence < 1 {
		return 0, ErrInvalidRequest
	}
	return sequence, nil
}

func encodeCursor(sequence int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("v1:%d", sequence)))
}

func verify(entries []Entry) bool {
	if len(entries) == 0 {
		return true
	}
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Sequence < sorted[j].Sequence })
	for index, entry := range sorted {
		if entry.Hash != hashEntry(entry) {
			return false
		}
		if index > 0 && (entry.Sequence != sorted[index-1].Sequence+1 || entry.PreviousHash != sorted[index-1].Hash) {
			return false
		}
	}
	return true
}

func hashEntry(entry Entry) string {
	canonical := struct {
		ID, TenantID, Country, ActorID, ActorType, Action, TargetType, TargetID, Outcome, Reason, Correlation string
		OccurredAt, RecordedAt                                                                                string
		Before, After                                                                                         map[string]any
		PreviousHash                                                                                          string
		Sequence                                                                                              int64
	}{entry.ID, entry.TenantID, entry.Country, entry.Actor.SubjectID, entry.Actor.ActorType, entry.Action, entry.Target.Type,
		entry.Target.ID, string(entry.Outcome), entry.ReasonCode, entry.CorrelationID, entry.OccurredAt.UTC().Format(time.RFC3339Nano),
		entry.RecordedAt.UTC().Format(time.RFC3339Nano), entry.Before, entry.After, entry.PreviousHash, entry.Sequence}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func redact(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		normalized := strings.ToLower(key)
		if sensitiveKey(normalized) {
			result[key] = "[REDACTED]"
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			result[key] = redact(typed)
		case string:
			if len(typed) > 512 {
				typed = typed[:512]
			}
			result[key] = typed
		case nil, bool, float64, int, int64, json.Number:
			result[key] = typed
		default:
			result[key] = "[UNSUPPORTED]"
		}
	}
	return result
}

func sensitiveKey(key string) bool {
	for _, segment := range regexp.MustCompile(`[._-]`).Split(key, -1) {
		if map[string]bool{"authorization": true, "cookie": true, "email": true, "password": true, "phone": true, "secret": true, "token": true}[segment] {
			return true
		}
	}
	return false
}

func validRecord(request RecordRequest) bool {
	return regexp.MustCompile(`^[A-Z]{2}$`).MatchString(request.Country) && safeID(request.Actor.SubjectID) &&
		regexp.MustCompile(`^(USER|SERVICE|SYSTEM)$`).MatchString(request.Actor.ActorType) &&
		regexp.MustCompile(`^[a-z][a-z0-9_.]{2,127}$`).MatchString(request.Action) && safeID(request.Target.Type) && safeID(request.Target.ID) &&
		(request.Outcome == OutcomeSucceeded || request.Outcome == OutcomeDenied || request.Outcome == OutcomeFailed) &&
		regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`).MatchString(request.ReasonCode) && safeID(request.CorrelationID) && !request.OccurredAt.IsZero()
}

func safeID(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(value)
}
func safeReasonText(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= 8 && len(trimmed) <= 256 && !strings.ContainsAny(trimmed, "\r\n")
}

func secureID() (string, error) {
	value, err := uuid.NewRandom()
	return value.String(), err
}
