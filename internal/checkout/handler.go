package checkout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yazhsab/planext4u-backend/internal/order"
	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/addresses", handler.addresses)
	mux.HandleFunc("POST /v1/addresses", handler.createAddress)
	mux.HandleFunc("PATCH /v1/addresses/{address_id}", handler.updateAddress)
	mux.HandleFunc("DELETE /v1/addresses/{address_id}", handler.deleteAddress)
	mux.HandleFunc("GET /v1/delivery-slots", handler.slots)
	mux.HandleFunc("POST /v1/checkout/quotes", handler.quote)
	mux.HandleFunc("POST /v1/checkout/orders", handler.place)
	mux.HandleFunc("GET /v1/payments/{payment_id}", handler.getPayment)
	mux.HandleFunc("POST /v1/payments/{payment_id}/retry", handler.retryPayment)
	mux.HandleFunc("POST /v1/payments/webhooks/razorpay", func(writer http.ResponseWriter, request *http.Request) {
		handler.webhook(writer, request, payment.MethodRazorpay)
	})
	mux.HandleFunc("POST /v1/payments/webhooks/paystack", func(writer http.ResponseWriter, request *http.Request) {
		handler.webhook(writer, request, payment.MethodPaystack)
	})
	mux.HandleFunc("GET /v1/orders", handler.listOrders)
	mux.HandleFunc("GET /v1/orders/{order_id}", handler.getOrder)
	mux.HandleFunc("POST /v1/orders/{order_id}/cancel", handler.cancelOrder)
	mux.HandleFunc("POST /v1/orders/{order_id}/confirm-delivery", handler.confirmDelivery)
	mux.HandleFunc("POST /v1/orders/{order_id}/returns", handler.requestReturn)
	mux.HandleFunc("POST /v1/orders/{order_id}/rating", handler.rateOrder)
	mux.HandleFunc("GET /v1/wallet", handler.getWallet)
	mux.HandleFunc("GET /v1/wallet/experience", handler.getWalletExperience)
	mux.HandleFunc("POST /v1/wallet/referrals", handler.applyReferral)
	mux.HandleFunc("POST /v1/wallet/refills", handler.createWalletRefill)
	return mux, nil
}

func (handler *Handler) createAddress(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input AddressInput
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADDRESS_REQUEST_INVALID", "Check the address details.")
		return
	}
	value, replayed, err := handler.service.CreateAddress(scope, strings.TrimSpace(request.Header.Get("Idempotency-Key")), input)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatInt(value.Revision, 10)))
	writeJSON(writer, http.StatusCreated, value, replayed)
}

func (handler *Handler) updateAddress(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input AddressInput
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADDRESS_REQUEST_INVALID", "Check the address details.")
		return
	}
	revision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADDRESS_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return
	}
	value, replayed, err := handler.service.UpdateAddress(scope, strings.TrimSpace(request.Header.Get("Idempotency-Key")), request.PathValue("address_id"), revision, input)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatInt(value.Revision, 10)))
	writeJSON(writer, http.StatusOK, value, replayed)
}

func (handler *Handler) deleteAddress(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	revision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ADDRESS_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return
	}
	replayed, err := handler.service.DeleteAddress(scope, strings.TrimSpace(request.Header.Get("Idempotency-Key")), request.PathValue("address_id"), revision)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	if replayed {
		writer.Header().Set("X-Idempotent-Replay", "true")
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) retryPayment(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	if request.Body != nil && request.Body != http.NoBody {
		var input struct{}
		if !decodeJSON(request, &input) {
			writeProblem(writer, request, http.StatusUnprocessableEntity, "PAYMENT_RETRY_INVALID", "The payment retry request is invalid.")
			return
		}
	}
	value, replayed, err := handler.service.deps.Payment.RetryProvider(
		request.Context(),
		payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID},
		strings.TrimSpace(request.Header.Get("Idempotency-Key")), request.PathValue("payment_id"),
	)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value, replayed)
}

func (handler *Handler) addresses(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Addresses(scope)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CHECKOUT_REQUEST_INVALID", "The checkout request is invalid.")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": value}, false)
}

func (handler *Handler) slots(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.DeliverySlots(scope)
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CHECKOUT_REQUEST_INVALID", "The checkout request is invalid.")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": value}, false)
}

func (handler *Handler) quote(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input QuoteRequest
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CHECKOUT_REQUEST_INVALID", "The checkout request is invalid.")
		return
	}
	value, replayed, err := handler.service.Quote(request.Context(), scope, strings.TrimSpace(request.Header.Get("Idempotency-Key")), input)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value, replayed)
}

func (handler *Handler) place(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input struct {
		QuoteID       string         `json:"quote_id"`
		PaymentMethod payment.Method `json:"payment_method"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CHECKOUT_REQUEST_INVALID", "The checkout request is invalid.")
		return
	}
	value, replayed, err := handler.service.Place(request.Context(), scope, strings.TrimSpace(request.Header.Get("Idempotency-Key")), input.QuoteID, input.PaymentMethod)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value, replayed)
}

func (handler *Handler) getPayment(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.deps.Payment.Get(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, request.PathValue("payment_id"))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value, false)
}

func (handler *Handler) webhook(writer http.ResponseWriter, request *http.Request, method payment.Method) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "PAYMENT_WEBHOOK_INVALID", "The payment event is invalid.")
		return
	}
	signature := request.Header.Get("X-Razorpay-Signature")
	eventID := request.Header.Get("X-Razorpay-Event-Id")
	if method == payment.MethodPaystack {
		signature = request.Header.Get("X-Paystack-Signature")
		eventID = ""
	}
	value, replayed, err := handler.service.deps.Payment.HandleProviderWebhook(method, signature, eventID, body)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, payment.ErrSignatureInvalid) {
			status = http.StatusUnauthorized
		}
		writeProblem(writer, request, status, "PAYMENT_WEBHOOK_REJECTED", "The payment event could not be accepted.")
		return
	}
	if value.Status == payment.StatusCaptured || value.Status == payment.StatusReconciled {
		digest := sha256.Sum256(body)
		if handler.service.HasWalletRefill(value.ID) {
			_, _, err = handler.service.FinalizeProviderWalletRefill(value.ID)
		} else {
			_, _, err = handler.service.FinalizeProviderPayment("webhook-"+hex.EncodeToString(digest[:8]), value.ID)
		}
		if err != nil {
			writeProblem(writer, request, http.StatusConflict, "PAYMENT_FINALIZATION_PENDING", "Payment was accepted and order finalization will be retried.")
			return
		}
	}
	if value.Status == payment.StatusRefunded {
		digest := sha256.Sum256(body)
		if _, _, err = handler.service.FinalizeProviderRefund("refund-webhook-"+hex.EncodeToString(digest[:8]), value.ID); err != nil {
			writeProblem(writer, request, http.StatusConflict, "REFUND_FINALIZATION_PENDING", "Refund was accepted and order finalization will be retried.")
			return
		}
	}
	writeJSON(writer, http.StatusOK, value, replayed)
}

func (handler *Handler) listOrders(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.deps.Orders.List(orderScope(scope))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": values}, false)
}

func (handler *Handler) getOrder(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.deps.Orders.Get(orderScope(scope), request.PathValue("order_id"))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeOrder(writer, http.StatusOK, value, false)
}

func (handler *Handler) cancelOrder(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ORDER_REQUEST_INVALID", "The order request is invalid.")
		return
	}
	handler.transitionOrder(writer, request, order.StatusCancelRequested, input.Reason)
}

func (handler *Handler) confirmDelivery(writer http.ResponseWriter, request *http.Request) {
	if request.Body != nil && request.Body != http.NoBody {
		var input struct{}
		if !decodeJSON(request, &input) {
			writeProblem(writer, request, http.StatusUnprocessableEntity, "ORDER_REQUEST_INVALID", "The order request is invalid.")
			return
		}
	}
	handler.transitionOrder(writer, request, order.StatusCompleted, "")
}

func (handler *Handler) transitionOrder(writer http.ResponseWriter, request *http.Request, target order.Status, reason string) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	revision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ORDER_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return
	}
	value, replayed, err := handler.service.deps.Orders.Transition(orderScope(scope), strings.TrimSpace(request.Header.Get("Idempotency-Key")), request.PathValue("order_id"), revision, target, "CUSTOMER", reason)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeOrder(writer, http.StatusOK, value, replayed)
}

func (handler *Handler) requestReturn(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input struct {
		Lines  []order.ReturnLine `json:"lines"`
		Reason string             `json:"reason"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ORDER_REQUEST_INVALID", "The order request is invalid.")
		return
	}
	revision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ORDER_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return
	}
	value, replayed, err := handler.service.deps.Orders.RequestReturn(orderScope(scope), strings.TrimSpace(request.Header.Get("Idempotency-Key")), request.PathValue("order_id"), revision, input.Lines, input.Reason)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeOrder(writer, http.StatusCreated, value, replayed)
}

func (handler *Handler) rateOrder(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input struct {
		Score   int    `json:"score"`
		Comment string `json:"comment"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ORDER_REQUEST_INVALID", "The order request is invalid.")
		return
	}
	revision, err := parseRevision(request.Header.Get("If-Match"))
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "ORDER_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return
	}
	value, replayed, err := handler.service.deps.Orders.Rate(orderScope(scope), strings.TrimSpace(request.Header.Get("Idempotency-Key")), request.PathValue("order_id"), revision, input.Score, input.Comment)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeOrder(writer, http.StatusCreated, value, replayed)
}

func (handler *Handler) getWallet(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.deps.Wallet.Account(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID})
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value, false)
}

func (handler *Handler) getWalletExperience(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.WalletExperience(scope)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, value, false)
}

func (handler *Handler) applyReferral(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "REFERRAL_REQUEST_INVALID", "Check the referral code.")
		return
	}
	value, replayed, err := handler.service.ApplyReferral(scope, strings.TrimSpace(request.Header.Get("Idempotency-Key")), input.Code)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value, replayed)
}

func (handler *Handler) createWalletRefill(writer http.ResponseWriter, request *http.Request) {
	scope, ok := customerScope(writer, request)
	if !ok {
		return
	}
	var input struct {
		OfferID string         `json:"offer_id"`
		Method  payment.Method `json:"payment_method"`
	}
	if !decodeJSON(request, &input) {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "WALLET_REFILL_INVALID", "Check the refill selection.")
		return
	}
	value, replayed, err := handler.service.CreateWalletRefill(request.Context(), scope, strings.TrimSpace(request.Header.Get("Idempotency-Key")), input.OfferID, input.Method)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, value, replayed)
}

func (handler *Handler) writeError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, ErrQuoteNotFound), errors.Is(err, ErrAddressNotFound), errors.Is(err, order.ErrOrderNotFound), errors.Is(err, payment.ErrPaymentNotFound):
		writeProblem(writer, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "The requested resource was not found.")
	case errors.Is(err, ErrIdempotencyConflict), errors.Is(err, order.ErrIdempotencyConflict), errors.Is(err, payment.ErrIdempotencyConflict):
		writeProblem(writer, request, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "The idempotency key was already used for a different command.")
	case errors.Is(err, ErrAddressConflict):
		writeProblem(writer, request, http.StatusConflict, "ADDRESS_REVISION_CONFLICT", "The address changed. Refresh it and try again.")
	case errors.Is(err, ErrQuoteExpired), errors.Is(err, ErrQuoteStale), errors.Is(err, ErrSlotNotAvailable), errors.Is(err, ErrPromotionInvalid), errors.Is(err, ErrPaymentMethod), errors.Is(err, order.ErrRevisionConflict), errors.Is(err, order.ErrInvalidTransition):
		writeProblem(writer, request, http.StatusConflict, "CHECKOUT_STATE_CONFLICT", "The checkout or order state changed. Refresh and try again.")
	case errors.Is(err, payment.ErrProviderUnavailable):
		writeProblem(writer, request, http.StatusServiceUnavailable, "PAYMENT_PROVIDER_UNAVAILABLE", "The payment provider is temporarily unavailable.")
	default:
		writeProblem(writer, request, http.StatusUnprocessableEntity, "CHECKOUT_REQUEST_INVALID", "The checkout request is invalid.")
	}
}

func customerScope(writer http.ResponseWriter, request *http.Request) (Scope, bool) {
	scope := Scope{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), CustomerID: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject"))}
	roles := strings.Split(request.Header.Get("X-Planext4u-Roles"), ",")
	customer := false
	for _, role := range roles {
		customer = customer || strings.TrimSpace(role) == "CUSTOMER"
	}
	if !validScope(scope) || !customer {
		writeProblem(writer, request, http.StatusForbidden, "CUSTOMER_ACCESS_DENIED", "This customer resource is not available for the identity.")
		return Scope{}, false
	}
	return scope, true
}

func orderScope(value Scope) order.Scope {
	return order.Scope{TenantID: value.TenantID, Country: value.Country, CustomerID: value.CustomerID}
}
func parseRevision(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, ErrInvalidRequest
	}
	revision, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || revision < 1 {
		return 0, ErrInvalidRequest
	}
	return revision, nil
}
func decodeJSON(request *http.Request, target any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}
func writeOrder(writer http.ResponseWriter, status int, value order.Order, replayed bool) {
	writer.Header().Set("ETag", strconv.Quote(strconv.FormatInt(value.Revision, 10)))
	writeJSON(writer, status, value, replayed)
}
func writeJSON(writer http.ResponseWriter, status int, value any, replayed bool) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	if replayed {
		writer.Header().Set("X-Idempotent-Replay", "true")
	}
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
func writeProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlation := request.Header.Get("X-Correlation-ID")
	if correlation == "" {
		correlation = "unavailable"
	}
	writeJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlation, "retryable": false, "field_errors": []any{}, "details": map[string]any{}}}, false)
}
