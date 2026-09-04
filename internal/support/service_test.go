package support

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var supportTestNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestBE004TicketOwnershipAndCrossRoleDenial(t *testing.T) {
	service := newSupportTestService(t)
	customer := supportTestActor(RoleCustomer, "customer-001")
	created, first, err := service.Create(context.Background(), customer, CreateTicketRequest{OwnerRole: RoleCustomer, Category: CategoryOrder, Subject: "Order did not arrive", Description: "The order is past its delivery window.", Priority: PriorityHigh, RelatedReference: "order-001"}, "ticket-create-0001")
	if err != nil || !first {
		t.Fatalf("create ticket: created=%v first=%v err=%v", created, first, err)
	}
	if created.OwnerRole != RoleCustomer || len(created.Messages) != 1 || created.Messages[0].AuthorType != AuthorRequester {
		t.Fatalf("created ticket = %#v", created)
	}

	listed, err := service.List(context.Background(), customer, RoleCustomer, 20, "")
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != created.ID {
		t.Fatalf("list customer tickets = %#v, err=%v", listed, err)
	}

	rider := supportTestActor(RoleRider, "customer-001")
	if _, err = service.Get(context.Background(), rider, created.ID); err != ErrNotFound {
		t.Fatalf("cross-role get error = %v, want not found", err)
	}
	if _, _, err = service.AddMessage(context.Background(), rider, created.ID, AddMessageRequest{Body: "unauthorized"}, "message-cross-role-0001"); err != ErrNotFound {
		t.Fatalf("cross-role message error = %v, want not found", err)
	}
	otherCustomer := supportTestActor(RoleCustomer, "customer-002")
	if _, err = service.Get(context.Background(), otherCustomer, created.ID); err != ErrNotFound {
		t.Fatalf("cross-subject get error = %v, want not found", err)
	}
}

func TestBE004CreateAndMessageIdempotency(t *testing.T) {
	service := newSupportTestService(t)
	actor := supportTestActor(RoleVendor, "vendor-001")
	request := CreateTicketRequest{OwnerRole: RoleVendor, Category: CategoryVendorOperations, Subject: "Catalogue approval delayed", Description: "The catalogue item has been pending since yesterday."}
	first, created, err := service.Create(context.Background(), actor, request, "ticket-create-0002")
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	replay, created, err := service.Create(context.Background(), actor, request, "ticket-create-0002")
	if err != nil || created || replay.ID != first.ID || len(replay.Messages) != 1 {
		t.Fatalf("create replay=%#v created=%v err=%v", replay, created, err)
	}
	conflict := request
	conflict.Subject = "Different content"
	if _, _, err = service.Create(context.Background(), actor, conflict, "ticket-create-0002"); err != ErrIdempotencyConflict {
		t.Fatalf("create conflict error = %v", err)
	}

	message := AddMessageRequest{Body: "Please let me know whether more details are needed."}
	updated, inserted, err := service.AddMessage(context.Background(), actor, first.ID, message, "ticket-message-0001")
	if err != nil || !inserted || len(updated.Messages) != 2 {
		t.Fatalf("first message=%#v inserted=%v err=%v", updated, inserted, err)
	}
	replayed, inserted, err := service.AddMessage(context.Background(), actor, first.ID, message, "ticket-message-0001")
	if err != nil || inserted || len(replayed.Messages) != 2 {
		t.Fatalf("message replay=%#v inserted=%v err=%v", replayed, inserted, err)
	}
	if _, _, err = service.AddMessage(context.Background(), actor, first.ID, AddMessageRequest{Body: "Different message"}, "ticket-message-0001"); err != ErrIdempotencyConflict {
		t.Fatalf("message conflict error = %v", err)
	}
}

func TestBE004AdminProjectionIsCapabilityAndCountryScoped(t *testing.T) {
	service := newSupportTestService(t)
	actor := supportTestActor(RoleRider, "rider-001")
	created, _, err := service.Create(context.Background(), actor, CreateTicketRequest{OwnerRole: RoleRider, Category: CategoryRiderOperations, Subject: "Payout is not visible", Description: "The completed delivery payout is not listed."}, "ticket-create-0003")
	if err != nil {
		t.Fatal(err)
	}
	principal := AdminPrincipal{TenantID: actor.TenantID, Country: actor.Country, Subject: "support-admin-001", Capabilities: map[string]bool{CapabilitySupportManage: true}}
	page, err := service.AdminList(context.Background(), principal, AdminListFilter{OwnerRole: RoleRider, Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.ID || page.Items[0].OwnerReference != actor.Subject || page.Items[0].MessageCount != 1 {
		t.Fatalf("admin page=%#v err=%v", page, err)
	}
	principal.Country = "US"
	page, err = service.AdminList(context.Background(), principal, AdminListFilter{Limit: 50})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("cross-country admin page=%#v err=%v", page, err)
	}
	principal.Capabilities = map[string]bool{}
	if _, err = service.AdminList(context.Background(), principal, AdminListFilter{Limit: 50}); err != ErrForbidden {
		t.Fatalf("admin capability error=%v", err)
	}
}

func TestBE004HTTPContractRequiresScopeAndReplaysMessages(t *testing.T) {
	service := newSupportTestService(t)
	handler, err := NewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/support/tickets?owner_role=CUSTOMER", nil))
	assertSupportProblem(t, unauthorized, http.StatusForbidden, "SUPPORT_SCOPE_REQUIRED")

	created := serveSupport(t, handler, http.MethodPost, "/v1/support/tickets", `{"owner_role":"CUSTOMER","category":"ORDER","subject":"Delivery status is stale","description":"The order tracker has not moved."}`, RoleCustomer, "ticket-http-0001")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var ticket Ticket
	if err = json.Unmarshal(created.Body.Bytes(), &ticket); err != nil {
		t.Fatal(err)
	}
	messagePath := "/v1/support/tickets/" + ticket.ID + "/messages"
	message := serveSupport(t, handler, http.MethodPost, messagePath, `{"body":"This is still unresolved."}`, RoleCustomer, "message-http-0001")
	if message.Code != http.StatusCreated {
		t.Fatalf("message status=%d body=%s", message.Code, message.Body.String())
	}
	replay := serveSupport(t, handler, http.MethodPost, messagePath, `{"body":"This is still unresolved."}`, RoleCustomer, "message-http-0001")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay status=%d header=%q body=%s", replay.Code, replay.Header().Get("Idempotency-Replayed"), replay.Body.String())
	}
}

func newSupportTestService(t *testing.T) *Service {
	t.Helper()
	service, err := NewService(NewMemoryRepository(), func() time.Time { return supportTestNow })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func supportTestActor(role Role, subject string) Actor {
	return Actor{TenantID: "018d6f26-4c4d-7f72-b142-87c09824f6d3", Country: "IN", Subject: subject, Roles: []Role{role}}
}

func serveSupport(t *testing.T, handler http.Handler, method, path, body string, role Role, key string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Planext4u-Tenant", "018d6f26-4c4d-7f72-b142-87c09824f6d3")
	request.Header.Set("X-Planext4u-Country", "IN")
	request.Header.Set("X-Planext4u-Subject", "customer-001")
	request.Header.Set("X-Planext4u-Roles", string(role))
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertSupportProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var problem struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Error.Code != code {
		t.Fatalf("problem=%#v err=%v", problem, err)
	}
}
