package food

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type idempotentResult struct {
	fingerprint string
	resourceID  string
}

type Service struct {
	clock         func() time.Time
	configuration Configuration
	mu            sync.Mutex
	sequence      int64
	restaurants   map[string]Restaurant
	menu          map[string]MenuItem
	carts         map[string]*Cart
	orders        map[string]*Order
	idempotency   map[string]idempotentResult
}

func NewService(configuration Configuration, clock func() time.Time) (*Service, error) {
	if clock == nil || len(configuration.Restaurants) == 0 || len(configuration.Menu) == 0 {
		return nil, ErrInvalidRequest
	}
	if configuration.CartTTL <= 0 {
		configuration.CartTTL = 20 * time.Minute
	}
	if configuration.AcceptanceTTL <= 0 {
		configuration.AcceptanceTTL = 3 * time.Minute
	}
	if configuration.TaxBasisPoints < 0 || configuration.TaxBasisPoints > 10000 {
		return nil, ErrInvalidRequest
	}
	service := &Service{clock: clock, configuration: configuration, restaurants: map[string]Restaurant{}, menu: map[string]MenuItem{}, carts: map[string]*Cart{}, orders: map[string]*Order{}, idempotency: map[string]idempotentResult{}}
	for _, restaurant := range configuration.Restaurants {
		if restaurant.tenantID == "" {
			restaurant.tenantID = configuration.TenantID
		}
		if restaurant.country == "" {
			restaurant.country = configuration.Country
		}
		if !safeID(restaurant.ID) || !safeID(restaurant.OwnerID) || len(restaurant.PostalCodes) == 0 || restaurant.DeliveryFee.Currency != restaurant.MinimumOrder.Currency {
			return nil, ErrInvalidRequest
		}
		service.restaurants[restaurant.ID] = cloneRestaurant(restaurant)
	}
	for _, item := range configuration.Menu {
		if item.tenantID == "" {
			item.tenantID = configuration.TenantID
		}
		if item.country == "" {
			item.country = configuration.Country
		}
		if !safeID(item.ID) || service.restaurants[item.RestaurantID].ID == "" || item.BasePrice.AmountMinor < 0 {
			return nil, ErrInvalidRequest
		}
		service.menu[item.ID] = cloneMenuItem(item)
	}
	return service, nil
}

func (service *Service) Restaurants(actor Actor, postalCode, cuisine string) ([]Restaurant, error) {
	if !validActor(actor) || !hasRole(actor, "CUSTOMER") || !validPostal(postalCode) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	values := []Restaurant{}
	for _, restaurant := range service.restaurants {
		if restaurant.tenantID != actor.TenantID || restaurant.country != actor.Country || !contains(restaurant.PostalCodes, postalCode) || !restaurant.Verified {
			continue
		}
		if cuisine != "" && !containsFold(restaurant.Cuisine, cuisine) {
			continue
		}
		values = append(values, publicRestaurant(restaurant))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Rating > values[j].Rating })
	return values, nil
}

func (service *Service) Menu(actor Actor, restaurantID, postalCode string) ([]MenuItem, error) {
	if !validActor(actor) || !hasRole(actor, "CUSTOMER") || !validPostal(postalCode) {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	restaurant := service.restaurants[restaurantID]
	if restaurant.ID == "" || restaurant.tenantID != actor.TenantID || restaurant.country != actor.Country || !contains(restaurant.PostalCodes, postalCode) {
		return nil, ErrNotFound
	}
	values := []MenuItem{}
	for _, item := range service.menu {
		if item.RestaurantID == restaurantID {
			values = append(values, cloneMenuItem(item))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values, nil
}

func (service *Service) SetCart(actor Actor, key string, request CartRequest) (Cart, bool, error) {
	if !validActor(actor) || !hasRole(actor, "CUSTOMER") || !validKey(key) || !validPostal(request.PostalCode) || len(request.Lines) == 0 || len(request.Lines) > 30 {
		return Cart{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "cart", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Cart{}, false, ErrIdempotencyConflict
		}
		return cloneCart(*service.carts[previous.resourceID]), true, nil
	}
	restaurant := service.restaurants[request.RestaurantID]
	if restaurant.ID == "" || restaurant.tenantID != actor.TenantID || restaurant.country != actor.Country || !contains(restaurant.PostalCodes, request.PostalCode) {
		return Cart{}, false, ErrNotFound
	}
	if !restaurant.Open || !beforeCutoff(service.clock(), restaurant.AcceptUntilMinute) {
		return Cart{}, false, ErrRestaurantClosed
	}
	lines := make([]CartLine, 0, len(request.Lines))
	subtotal := int64(0)
	for _, requested := range request.Lines {
		line, err := service.priceLine(restaurant, requested)
		if err != nil {
			return Cart{}, false, err
		}
		lines = append(lines, line)
		subtotal += line.LineTotal.AmountMinor
	}
	if subtotal < restaurant.MinimumOrder.AmountMinor {
		return Cart{}, false, ErrInvalidRequest
	}
	now := service.clock().UTC()
	service.sequence++
	tax := subtotal * service.configuration.TaxBasisPoints / 10000
	value := &Cart{ID: "food-cart-" + sequenceID(service.sequence), CustomerID: actor.Subject, Restaurant: publicRestaurant(restaurant), PostalCode: request.PostalCode, Revision: 1, Lines: lines, Subtotal: Money{AmountMinor: subtotal, Currency: restaurant.MinimumOrder.Currency}, DeliveryFee: restaurant.DeliveryFee, Tax: Money{AmountMinor: tax, Currency: restaurant.MinimumOrder.Currency}, Total: Money{AmountMinor: subtotal + restaurant.DeliveryFee.AmountMinor + tax, Currency: restaurant.MinimumOrder.Currency}, PricingVersion: "food-pricing-v1", ExpiresAt: now.Add(service.configuration.CartTTL), UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.carts[value.ID] = value
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneCart(*value), false, nil
}

func (service *Service) CreateOrder(actor Actor, key string, request CreateOrderRequest) (Order, bool, error) {
	if !validActor(actor) || !hasRole(actor, "CUSTOMER") || !validKey(key) || !safeID(request.CartID) || (request.PaymentMethod != "WALLET" && request.PaymentMethod != "RAZORPAY" && request.PaymentMethod != "PAYSTACK") {
		return Order{}, false, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "order", key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Order{}, false, ErrIdempotencyConflict
		}
		return cloneOrder(*service.orders[previous.resourceID]), true, nil
	}
	cart := service.carts[request.CartID]
	if cart == nil {
		return Order{}, false, ErrNotFound
	}
	if cart.CustomerID != actor.Subject || cart.tenantID != actor.TenantID || cart.country != actor.Country {
		return Order{}, false, ErrForbidden
	}
	now := service.clock().UTC()
	if !cart.ExpiresAt.After(now) {
		return Order{}, false, ErrConflict
	}
	restaurant := service.restaurants[cart.Restaurant.ID]
	if !restaurant.Open || !beforeCutoff(now, restaurant.AcceptUntilMinute) {
		return Order{}, false, ErrRestaurantClosed
	}
	service.sequence++
	value := &Order{ID: "food-order-" + sequenceID(service.sequence), CustomerID: actor.Subject, RestaurantID: restaurant.ID, Revision: 1, Status: StatusPendingRestaurant, Lines: cloneLines(cart.Lines), Subtotal: cart.Subtotal, DeliveryFee: cart.DeliveryFee, Tax: cart.Tax, Total: cart.Total, Payment: Payment{Method: request.PaymentMethod, Status: "CAPTURED", Reference: "payment-food-" + sequenceID(service.sequence)}, PostalCode: cart.PostalCode, PricingVersion: cart.PricingVersion, AcceptBy: now.Add(service.configuration.AcceptanceTTL), AllowedActions: []string{"CANCEL"}, Timeline: []OrderEvent{{Status: StatusPendingRestaurant, Actor: actor.Subject, CreatedAt: now}}, CreatedAt: now, UpdatedAt: now, tenantID: actor.TenantID, country: actor.Country}
	service.orders[value.ID] = value
	delete(service.carts, cart.ID)
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneOrder(*value), false, nil
}

func (service *Service) Orders(actor Actor) ([]Order, error) {
	if !validActor(actor) || !hasAnyRole(actor, "CUSTOMER", "RESTAURANT_VENDOR", "OPS_ADMIN", "SUPER_ADMIN") {
		return nil, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expirePendingLocked()
	values := []Order{}
	for _, order := range service.orders {
		if order.tenantID != actor.TenantID || order.country != actor.Country {
			continue
		}
		if hasRole(actor, "CUSTOMER") && order.CustomerID != actor.Subject {
			continue
		}
		if hasRole(actor, "RESTAURANT_VENDOR") {
			restaurant := service.restaurants[order.RestaurantID]
			if restaurant.OwnerID != actor.Subject {
				continue
			}
		}
		values = append(values, cloneOrder(*order))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].CreatedAt.After(values[j].CreatedAt) })
	return values, nil
}

func (service *Service) Order(actor Actor, orderID string) (Order, error) {
	if !validActor(actor) {
		return Order{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expirePendingLocked()
	value := service.orders[orderID]
	if value == nil {
		return Order{}, ErrNotFound
	}
	if err := service.authorizeOrder(actor, value); err != nil {
		return Order{}, err
	}
	return cloneOrder(*value), nil
}

func (service *Service) RestaurantTransition(actor Actor, key, orderID string, revision int64, request RestaurantTransitionRequest) (Order, bool, error) {
	if !validActor(actor) || !hasRole(actor, "RESTAURANT_VENDOR") || !validKey(key) {
		return Order{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expirePendingLocked()
	value := service.orders[orderID]
	if value == nil {
		return Order{}, false, ErrNotFound
	}
	restaurant := service.restaurants[value.RestaurantID]
	if restaurant.OwnerID != actor.Subject || value.tenantID != actor.TenantID || value.country != actor.Country {
		return Order{}, false, ErrForbidden
	}
	fingerprint := digest(request)
	scope := idempotencyScope(actor, "restaurant-transition:"+orderID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != fingerprint {
			return Order{}, false, ErrIdempotencyConflict
		}
		return cloneOrder(*value), true, nil
	}
	if value.Revision != revision {
		return Order{}, false, ErrConflict
	}
	if !allowedRestaurantTransition(value.Status, request.Status) {
		return Order{}, false, ErrInvalidTransition
	}
	if request.Status == StatusRejected && len(strings.TrimSpace(request.Reason)) < 8 {
		return Order{}, false, ErrInvalidRequest
	}
	now := service.clock().UTC()
	value.Status, value.Revision, value.UpdatedAt = request.Status, value.Revision+1, now
	value.AllowedActions = restaurantActions(request.Status)
	if request.Status == StatusAccepted {
		readyAt := now.Add(time.Duration(restaurant.PreparationMinutes) * time.Minute)
		value.EstimatedReadyAt = &readyAt
	}
	if request.Status == StatusRejected {
		value.RejectionReason = strings.TrimSpace(request.Reason)
		refund(value)
	}
	value.Timeline = append(value.Timeline, OrderEvent{Status: request.Status, Actor: actor.Subject, Reason: strings.TrimSpace(request.Reason), CreatedAt: now})
	service.idempotency[scope] = idempotentResult{fingerprint: fingerprint, resourceID: value.ID}
	return cloneOrder(*value), false, nil
}

func (service *Service) DispatchTransition(actor Actor, key, orderID string, revision int64, status OrderStatus) (Order, bool, error) {
	if !validActor(actor) || !hasAnyRole(actor, "DISPATCH", "RIDER", "OPS_ADMIN", "SUPER_ADMIN") || !validKey(key) {
		return Order{}, false, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value := service.orders[orderID]
	if value == nil {
		return Order{}, false, ErrNotFound
	}
	if value.tenantID != actor.TenantID || value.country != actor.Country || value.Revision != revision {
		return Order{}, false, ErrConflict
	}
	allowed := map[OrderStatus]OrderStatus{StatusReady: StatusRiderAssigned, StatusRiderAssigned: StatusPickedUp, StatusPickedUp: StatusDelivered}
	if allowed[value.Status] != status {
		return Order{}, false, ErrInvalidTransition
	}
	scope := idempotencyScope(actor, "dispatch:"+orderID, key)
	if previous, ok := service.idempotency[scope]; ok {
		if previous.fingerprint != digest(status) {
			return Order{}, false, ErrIdempotencyConflict
		}
		return cloneOrder(*value), true, nil
	}
	now := service.clock().UTC()
	value.Status, value.Revision, value.UpdatedAt = status, value.Revision+1, now
	value.Timeline = append(value.Timeline, OrderEvent{Status: status, Actor: actor.Subject, CreatedAt: now})
	service.idempotency[scope] = idempotentResult{fingerprint: digest(status), resourceID: value.ID}
	return cloneOrder(*value), false, nil
}

func (service *Service) priceLine(restaurant Restaurant, request CartLineRequest) (CartLine, error) {
	if request.Quantity < 1 || request.Quantity > 20 || len(request.Note) > 240 {
		return CartLine{}, ErrInvalidRequest
	}
	item := service.menu[request.MenuItemID]
	if item.ID == "" || item.RestaurantID != restaurant.ID || item.tenantID != restaurant.tenantID || item.country != restaurant.country {
		return CartLine{}, ErrNotFound
	}
	if !item.Available {
		return CartLine{}, ErrUnavailable
	}
	selected := map[string]bool{}
	for _, optionID := range request.OptionIDs {
		if selected[optionID] {
			return CartLine{}, ErrInvalidRequest
		}
		selected[optionID] = true
	}
	unit := item.BasePrice.AmountMinor
	options := []PricedOption{}
	for _, group := range item.OptionGroups {
		count := 0
		for _, option := range group.Options {
			if !selected[option.ID] {
				continue
			}
			if !option.Available {
				return CartLine{}, ErrUnavailable
			}
			count++
			unit += option.PriceDelta.AmountMinor
			options = append(options, PricedOption{ID: option.ID, Name: option.Name, PriceDelta: option.PriceDelta})
			delete(selected, option.ID)
		}
		if count < group.Minimum || count > group.Maximum {
			return CartLine{}, ErrInvalidRequest
		}
	}
	if len(selected) != 0 {
		return CartLine{}, ErrInvalidRequest
	}
	currency := item.BasePrice.Currency
	return CartLine{MenuItemID: item.ID, Name: item.Name, Quantity: request.Quantity, UnitPrice: Money{AmountMinor: unit, Currency: currency}, Options: options, Note: strings.TrimSpace(request.Note), LineTotal: Money{AmountMinor: unit * int64(request.Quantity), Currency: currency}}, nil
}

func (service *Service) expirePendingLocked() {
	now := service.clock().UTC()
	for _, value := range service.orders {
		if value.Status == StatusPendingRestaurant && !value.AcceptBy.After(now) {
			value.Status, value.Revision, value.UpdatedAt = StatusTimedOut, value.Revision+1, now
			value.RejectionReason = "Restaurant acceptance window expired"
			value.AllowedActions = nil
			refund(value)
			value.Timeline = append(value.Timeline, OrderEvent{Status: StatusTimedOut, Actor: "food-timeout-worker", Reason: value.RejectionReason, CreatedAt: now})
		}
	}
}

func (service *Service) authorizeOrder(actor Actor, value *Order) error {
	if value.tenantID != actor.TenantID || value.country != actor.Country {
		return ErrForbidden
	}
	if hasRole(actor, "CUSTOMER") && value.CustomerID == actor.Subject {
		return nil
	}
	if hasRole(actor, "RESTAURANT_VENDOR") && service.restaurants[value.RestaurantID].OwnerID == actor.Subject {
		return nil
	}
	if hasAnyRole(actor, "DISPATCH", "OPS_ADMIN", "SUPER_ADMIN", "RIDER") {
		return nil
	}
	return ErrForbidden
}

func allowedRestaurantTransition(from, to OrderStatus) bool {
	allowed := map[OrderStatus][]OrderStatus{StatusPendingRestaurant: {StatusAccepted, StatusRejected}, StatusAccepted: {StatusPreparing}, StatusPreparing: {StatusReady}}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

func restaurantActions(status OrderStatus) []string {
	switch status {
	case StatusPendingRestaurant:
		return []string{"ACCEPT", "REJECT"}
	case StatusAccepted:
		return []string{"START_PREPARING"}
	case StatusPreparing:
		return []string{"MARK_READY"}
	default:
		return nil
	}
}

func refund(value *Order) {
	value.Payment.Status = "REFUND_SUBMITTED"
	value.Payment.RefundState = "SUBMITTED"
}

func beforeCutoff(now time.Time, cutoff int) bool {
	if cutoff <= 0 || cutoff > 1440 {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	return minute < cutoff
}

func validActor(value Actor) bool {
	return safeID(value.TenantID) && regexp.MustCompile(`^[A-Z]{2}$`).MatchString(value.Country) && safeID(value.Subject) && len(value.Roles) > 0
}
func validKey(value string) bool { return len(value) >= 16 && len(value) <= 128 && safeID(value) }
func validPostal(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9 -]{3,12}$`).MatchString(value)
}
func safeID(value string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`).MatchString(value)
}
func hasRole(actor Actor, role string) bool {
	for _, candidate := range actor.Roles {
		if strings.TrimSpace(candidate) == role {
			return true
		}
	}
	return false
}
func hasAnyRole(actor Actor, roles ...string) bool {
	for _, role := range roles {
		if hasRole(actor, role) {
			return true
		}
	}
	return false
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}
func sequenceID(value int64) string {
	return strings.TrimLeft(time.Unix(value, 0).UTC().Format("150405000"), "0")
}
func idempotencyScope(actor Actor, operation, key string) string {
	return actor.TenantID + ":" + actor.Country + ":" + actor.Subject + ":" + operation + ":" + key
}
func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
func cloneRestaurant(value Restaurant) Restaurant {
	value.Cuisine = append([]string(nil), value.Cuisine...)
	value.PostalCodes = append([]string(nil), value.PostalCodes...)
	return value
}
func publicRestaurant(value Restaurant) Restaurant { value.OwnerID = ""; return cloneRestaurant(value) }
func cloneMenuItem(value MenuItem) MenuItem {
	value.OptionGroups = append([]OptionGroup(nil), value.OptionGroups...)
	for index := range value.OptionGroups {
		value.OptionGroups[index].Options = append([]Option(nil), value.OptionGroups[index].Options...)
	}
	return value
}
func cloneLines(values []CartLine) []CartLine {
	result := append([]CartLine(nil), values...)
	for index := range result {
		result[index].Options = append([]PricedOption(nil), values[index].Options...)
	}
	return result
}
func cloneCart(value Cart) Cart {
	value.Restaurant = cloneRestaurant(value.Restaurant)
	value.Lines = cloneLines(value.Lines)
	return value
}
func cloneOrder(value Order) Order {
	value.Lines = cloneLines(value.Lines)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	value.Timeline = append([]OrderEvent(nil), value.Timeline...)
	if value.EstimatedReadyAt != nil {
		copy := *value.EstimatedReadyAt
		value.EstimatedReadyAt = &copy
	}
	return value
}
