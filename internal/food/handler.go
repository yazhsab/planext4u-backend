package food

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Application interface {
	Restaurants(Actor, string, string) ([]Restaurant, error)
	Menu(Actor, string, string) ([]MenuItem, error)
	SetCart(Actor, string, CartRequest) (Cart, bool, error)
	CreateOrder(Actor, string, CreateOrderRequest) (Order, bool, error)
	Orders(Actor) ([]Order, error)
	Order(Actor, string) (Order, error)
	RestaurantTransition(Actor, string, string, int64, RestaurantTransitionRequest) (Order, bool, error)
	DispatchTransition(Actor, string, string, int64, OrderStatus) (Order, bool, error)
}

type Handler struct{ service Application }

func NewHandler(service Application) (http.Handler, error) {
	if service == nil {
		return nil, ErrInvalidRequest
	}
	handler := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/restaurants", handler.restaurants)
	mux.HandleFunc("GET /v1/restaurants/{restaurant_id}/menu", handler.menu)
	mux.HandleFunc("POST /v1/food-carts", handler.cart)
	mux.HandleFunc("GET /v1/food-orders", handler.orders)
	mux.HandleFunc("POST /v1/food-orders", handler.createOrder)
	mux.HandleFunc("GET /v1/food-orders/{order_id}", handler.order)
	mux.HandleFunc("POST /v1/food-orders/{order_id}/restaurant-transition", handler.restaurantTransition)
	mux.HandleFunc("POST /v1/food-orders/{order_id}/dispatch-transition", handler.dispatchTransition)
	return mux, nil
}

func (handler *Handler) restaurants(writer http.ResponseWriter, request *http.Request) {
	actor, ok := foodActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Restaurants(actor, request.URL.Query().Get("postal_code"), request.URL.Query().Get("cuisine"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFoodJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) menu(writer http.ResponseWriter, request *http.Request) {
	actor, ok := foodActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Menu(actor, request.PathValue("restaurant_id"), request.URL.Query().Get("postal_code"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFoodJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) cart(writer http.ResponseWriter, request *http.Request) {
	actor, ok := foodActor(writer, request)
	if !ok {
		return
	}
	var input CartRequest
	if !decodeFoodJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.SetCart(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeFoodReplay(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) orders(writer http.ResponseWriter, request *http.Request) {
	actor, ok := foodActor(writer, request)
	if !ok {
		return
	}
	values, err := handler.service.Orders(actor)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFoodJSON(writer, http.StatusOK, map[string]any{"items": values})
}

func (handler *Handler) createOrder(writer http.ResponseWriter, request *http.Request) {
	actor, ok := foodActor(writer, request)
	if !ok {
		return
	}
	var input CreateOrderRequest
	if !decodeFoodJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.CreateOrder(actor, request.Header.Get("Idempotency-Key"), input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFoodOrder(writer, http.StatusCreated, value, replay)
}

func (handler *Handler) order(writer http.ResponseWriter, request *http.Request) {
	actor, ok := foodActor(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Order(actor, request.PathValue("order_id"))
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFoodOrder(writer, http.StatusOK, value, false)
}

func (handler *Handler) restaurantTransition(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := foodMutation(writer, request)
	if !ok {
		return
	}
	var input RestaurantTransitionRequest
	if !decodeFoodJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.RestaurantTransition(actor, request.Header.Get("Idempotency-Key"), request.PathValue("order_id"), revision, input)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFoodOrder(writer, http.StatusOK, value, replay)
}

func (handler *Handler) dispatchTransition(writer http.ResponseWriter, request *http.Request) {
	actor, revision, ok := foodMutation(writer, request)
	if !ok {
		return
	}
	var input struct {
		Status OrderStatus `json:"status"`
	}
	if !decodeFoodJSON(writer, request, &input) {
		return
	}
	value, replay, err := handler.service.DispatchTransition(actor, request.Header.Get("Idempotency-Key"), request.PathValue("order_id"), revision, input.Status)
	if err != nil {
		handler.problem(writer, request, err)
		return
	}
	writeFoodOrder(writer, http.StatusOK, value, replay)
}

func (handler *Handler) problem(writer http.ResponseWriter, request *http.Request, err error) {
	status, code, detail := http.StatusInternalServerError, "FOOD_INTERNAL", "The food request could not be completed."
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code, detail = http.StatusUnprocessableEntity, "FOOD_REQUEST_INVALID", "The food request is invalid."
	case errors.Is(err, ErrNotFound):
		status, code, detail = http.StatusNotFound, "FOOD_NOT_FOUND", "The food resource was not found."
	case errors.Is(err, ErrForbidden):
		status, code, detail = http.StatusForbidden, "FOOD_FORBIDDEN", "The actor cannot access this food resource."
	case errors.Is(err, ErrConflict):
		status, code, detail = http.StatusConflict, "FOOD_REVISION_CONFLICT", "The food resource changed; refresh before retrying."
	case errors.Is(err, ErrInvalidTransition):
		status, code, detail = http.StatusConflict, "FOOD_TRANSITION_INVALID", "The requested food lifecycle transition is not allowed."
	case errors.Is(err, ErrIdempotencyConflict):
		status, code, detail = http.StatusConflict, "FOOD_IDEMPOTENCY_CONFLICT", "The idempotency key was already used for another command."
	case errors.Is(err, ErrRestaurantClosed):
		status, code, detail = http.StatusConflict, "FOOD_RESTAURANT_CLOSED", "The restaurant is outside its order acceptance window."
	case errors.Is(err, ErrUnavailable):
		status, code, detail = http.StatusConflict, "FOOD_ITEM_UNAVAILABLE", "A selected menu item or option is unavailable."
	}
	writeFoodProblem(writer, request, status, code, detail)
}

func foodMutation(writer http.ResponseWriter, request *http.Request) (Actor, int64, bool) {
	actor, ok := foodActor(writer, request)
	if !ok {
		return Actor{}, 0, false
	}
	revision, err := foodRevision(request.Header.Get("If-Match"))
	if err != nil || !validKey(request.Header.Get("Idempotency-Key")) {
		writeFoodProblem(writer, request, http.StatusUnprocessableEntity, "FOOD_REQUEST_INVALID", "Idempotency-Key and If-Match are required.")
		return Actor{}, 0, false
	}
	return actor, revision, true
}

func foodActor(writer http.ResponseWriter, request *http.Request) (Actor, bool) {
	actor := Actor{TenantID: strings.TrimSpace(request.Header.Get("X-Planext4u-Tenant")), Country: strings.TrimSpace(request.Header.Get("X-Planext4u-Country")), Subject: strings.TrimSpace(request.Header.Get("X-Planext4u-Subject")), Roles: strings.Split(request.Header.Get("X-Planext4u-Roles"), ",")}
	if !validActor(actor) {
		writeFoodProblem(writer, request, http.StatusForbidden, "FOOD_SCOPE_REQUIRED", "The authenticated food scope is incomplete.")
		return Actor{}, false
	}
	return actor, true
}

func foodRevision(value string) (int64, error) {
	revision, err := strconv.ParseInt(strings.TrimSpace(strings.Trim(value, `"`)), 10, 64)
	if err != nil || revision < 1 {
		return 0, ErrInvalidRequest
	}
	return revision, nil
}

func decodeFoodJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	defer request.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeFoodProblem(writer, request, http.StatusUnprocessableEntity, "FOOD_REQUEST_INVALID", "The food request is invalid.")
		return false
	}
	return true
}

func writeFoodOrder(writer http.ResponseWriter, status int, value Order, replay bool) {
	writer.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Revision))
	writeFoodReplay(writer, status, value, replay)
}

func writeFoodReplay(writer http.ResponseWriter, status int, value any, replay bool) {
	if replay {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	writeFoodJSON(writer, status, value)
}

func writeFoodJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeFoodProblem(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	correlationID := request.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = "corr-unavailable"
	}
	writeFoodJSON(writer, status, map[string]any{"error": map[string]any{"code": code, "message": message, "correlation_id": correlationID, "retryable": status >= 500, "field_errors": []any{}, "details": map[string]any{}}})
}
