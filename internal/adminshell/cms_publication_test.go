package adminshell

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/configcms"
)

type publicationSnapshotProbe struct {
	tenant, country string
	revision        int64
	err             error
	foreign         bool
}

func (probe *publicationSnapshotProbe) Get(_ context.Context, tenant, country string) (configcms.Snapshot, error) {
	probe.tenant, probe.country = tenant, country
	if probe.err != nil {
		return configcms.Snapshot{}, probe.err
	}
	if probe.foreign {
		tenant = "foreign-tenant"
	}
	return configcms.Snapshot{TenantID: tenant, Country: country, Revision: probe.revision}, nil
}

func TestCMSPublicationMetadataUsesCurrentSessionScopeAndFailsClosed(t *testing.T) {
	store, _ := NewMemorySessionStore(func() time.Time { return testNow })
	token := issue(t, store, testPrincipal(RoleCountryAdmin))
	authoring, _ := configcms.NewAuthoringService(configcms.NewMemoryDraftRepository(), func() time.Time { return testNow })
	probe := &publicationSnapshotProbe{revision: 7}
	handler := &Handler{sessions: store, cms: authoring, cmsPublished: probe, clock: func() time.Time { return testNow }, requireMFA: true}
	read := func() *httptest.ResponseRecorder {
		request := requestWithCookie(token)
		request.URL.RawQuery = "tenant_id=foreign&country=NG"
		request.Header.Set("X-Planext4u-Tenant", "foreign")
		request.Header.Set("X-Planext4u-Country", "NG")
		response := httptest.NewRecorder()
		handler.listCMSPages(response, request)
		return response
	}
	response := read()
	var result struct {
		PublishedRevision int64 `json:"published_revision"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || result.PublishedRevision != 7 || probe.tenant != "tenant-1" || probe.country != "IN" {
		t.Fatalf("publication metadata escaped session scope: %d %s %v", response.Code, response.Body.String(), err)
	}
	probe.err = configcms.ErrNotFound
	if response = read(); response.Code != 200 || !strings.Contains(response.Body.String(), `"published_revision":0`) {
		t.Fatalf("first workspace state unavailable: %d %s", response.Code, response.Body.String())
	}
	probe.err = errors.New("private repository connection details")
	if response = read(); response.Code != 503 || strings.Contains(response.Body.String(), "private repository") || strings.Contains(response.Body.String(), `"items"`) {
		t.Fatalf("publication outage exposed partial metadata: %d %s", response.Code, response.Body.String())
	}
	probe.err, probe.foreign = nil, true
	if response = read(); response.Code != 503 {
		t.Fatalf("foreign owner metadata accepted: %d %s", response.Code, response.Body.String())
	}
}
