package booking

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/payment"
	"github.com/yazhsab/planext4u-backend/internal/wallet"
)

type holdReplay struct {
	fingerprint string
	value       SlotHold
}

type bookingReplay struct {
	fingerprint string
	value       Booking
}

type Service struct {
	payments *payment.Service
	wallet   *wallet.Service
	clock    func() time.Time

	mu              sync.Mutex
	offerings       map[string]Offering
	slots           map[string]Slot
	policies        map[string]Policy
	holds           map[string]SlotHold
	bookings        map[string]Booking
	reserved        map[string]int
	holdRequests    map[string]holdReplay
	bookingRequests map[string]bookingReplay
}

func NewService(payments *payment.Service, walletService *wallet.Service, configuration Configuration, clock func() time.Time) (*Service, error) {
	if payments == nil || clock == nil || !validConfiguration(configuration) {
		return nil, ErrInvalidRequest
	}
	service := &Service{
		payments: payments, wallet: walletService, clock: clock,
		offerings: map[string]Offering{}, slots: map[string]Slot{}, policies: map[string]Policy{},
		holds: map[string]SlotHold{}, bookings: map[string]Booking{}, reserved: map[string]int{},
		holdRequests: map[string]holdReplay{}, bookingRequests: map[string]bookingReplay{},
	}
	for _, offering := range configuration.Offerings {
		offering.ServicePostalCodes = append([]string(nil), offering.ServicePostalCodes...)
		service.offerings[offering.ID] = offering
	}
	for _, slot := range configuration.Slots {
		slot.Remaining = slot.Capacity
		slot.AllowedActions = []string{"HOLD"}
		service.slots[slot.ID] = slot
	}
	for _, policy := range configuration.Policies {
		service.policies[policy.Country] = policy
	}
	return service, nil
}

func (service *Service) Offerings(scope Scope, postalCode, categoryID string) ([]Offering, error) {
	if !validScope(scope) || !safePostalCode(postalCode) || (categoryID != "" && !safeID(categoryID)) {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	result := []Offering{}
	for _, offering := range service.offerings {
		if !offering.Active || (categoryID != "" && offering.CategoryID != categoryID) || !containsFold(offering.ServicePostalCodes, postalCode) {
			continue
		}
		copy := cloneOffering(offering)
		result = append(result, copy)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].VerifiedProvider != result[right].VerifiedProvider {
			return result[left].VerifiedProvider
		}
		if result[left].RatingAverage != result[right].RatingAverage {
			return result[left].RatingAverage > result[right].RatingAverage
		}
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func (service *Service) Offering(scope Scope, offeringID, postalCode string) (Offering, error) {
	if !validScope(scope) || !safeID(offeringID) || !safePostalCode(postalCode) {
		return Offering{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	offering, exists := service.offerings[offeringID]
	if !exists || !offering.Active || !containsFold(offering.ServicePostalCodes, postalCode) {
		return Offering{}, ErrOfferingNotFound
	}
	return cloneOffering(offering), nil
}

func (service *Service) Slots(scope Scope, offeringID string, from, to time.Time) ([]Slot, error) {
	if !validScope(scope) || !safeID(offeringID) || from.IsZero() || to.IsZero() || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	if _, exists := service.offerings[offeringID]; !exists {
		return nil, ErrOfferingNotFound
	}
	result := []Slot{}
	for _, slot := range service.slots {
		if slot.OfferingID != offeringID || slot.StartsAt.Before(from) || !slot.StartsAt.Before(to) || !slot.StartsAt.After(service.clock().UTC()) {
			continue
		}
		copy := service.slotLocked(slot.ID)
		if copy.Remaining == 0 {
			copy.AllowedActions = nil
		}
		result = append(result, copy)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].StartsAt.Before(result[right].StartsAt) })
	return result, nil
}

func (service *Service) Hold(scope Scope, idempotencyKey, slotID, postalCode string) (SlotHold, bool, error) {
	if !validScope(scope) || !validIdempotencyKey(idempotencyKey) || !safeID(slotID) || !safePostalCode(postalCode) {
		return SlotHold{}, false, ErrInvalidRequest
	}
	requestKey := "hold\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	fingerprint := slotID + "\x00" + strings.ToUpper(strings.TrimSpace(postalCode))
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	if replay, exists := service.holdRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return SlotHold{}, false, ErrIdempotencyConflict
		}
		return cloneHold(replay.value), true, nil
	}
	slot, exists := service.slots[slotID]
	if !exists || !slot.StartsAt.After(service.clock().UTC()) {
		return SlotHold{}, false, ErrSlotNotFound
	}
	offering, exists := service.offerings[slot.OfferingID]
	if !exists || !offering.Active || !containsFold(offering.ServicePostalCodes, postalCode) {
		return SlotHold{}, false, ErrOfferingNotFound
	}
	if service.reserved[slotID] >= slot.Capacity {
		return SlotHold{}, false, ErrSlotUnavailable
	}
	policy := service.policies[scope.Country]
	now := service.clock().UTC()
	hold := SlotHold{
		ID: deterministicID("service-hold", requestKey), SlotID: slotID, OfferingID: slot.OfferingID,
		Status: HoldActive, ExpiresAt: now.Add(policy.HoldTTL), CreatedAt: now,
		AllowedActions: []string{"CREATE_BOOKING", "RELEASE"}, scope: scope,
	}
	service.holds[hold.ID] = hold
	service.reserved[slotID]++
	service.holdRequests[requestKey] = holdReplay{fingerprint: fingerprint, value: cloneHold(hold)}
	return cloneHold(hold), false, nil
}

func (service *Service) ReleaseHold(scope Scope, holdID string) error {
	if !validScope(scope) || !safeID(holdID) {
		return ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	hold, exists := service.holds[holdID]
	if !exists || hold.scope != scope {
		return ErrHoldNotFound
	}
	if hold.Status == HoldReleased {
		return nil
	}
	if hold.Status != HoldActive {
		return ErrInvalidTransition
	}
	hold.Status = HoldReleased
	hold.AllowedActions = nil
	service.holds[holdID] = hold
	service.releaseSlotLocked(hold.SlotID)
	return nil
}

func (service *Service) Create(ctx context.Context, scope Scope, idempotencyKey string, request CreateBookingRequest) (Booking, bool, error) {
	if !validScope(scope) || !validIdempotencyKey(idempotencyKey) || !safeID(request.HoldID) || !validBookingMethod(request.PaymentMethod) {
		return Booking{}, false, ErrInvalidRequest
	}
	requestKey := "booking-create\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	fingerprint := request.HoldID + "\x00" + string(request.PaymentMethod)
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	if replay, exists := service.bookingRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Booking{}, false, ErrIdempotencyConflict
		}
		return cloneBooking(replay.value), true, nil
	}
	hold, exists := service.holds[request.HoldID]
	if !exists || hold.scope != scope {
		return Booking{}, false, ErrHoldNotFound
	}
	if hold.Status == HoldExpired || !hold.ExpiresAt.After(service.clock().UTC()) {
		return Booking{}, false, ErrHoldExpired
	}
	if hold.Status != HoldActive {
		return Booking{}, false, ErrInvalidTransition
	}
	offering := service.offerings[hold.OfferingID]
	slot := service.slotLocked(hold.SlotID)
	amountDue := offering.Price
	if offering.PaymentMode == PaymentAdvance {
		amountDue = offering.Advance
	}
	bookingID := deterministicID("service-booking", requestKey)
	var walletDebit *wallet.LedgerEntry
	if request.PaymentMethod == payment.MethodWallet {
		if service.wallet == nil {
			return Booking{}, false, ErrPolicyDenied
		}
		policy := service.policies[scope.Country]
		points := (amountDue.AmountMinor + policy.WalletPointValueMinor - 1) / policy.WalletPointValueMinor
		entry, _, err := service.wallet.Redeem(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet", bookingID, points)
		if err != nil {
			return Booking{}, false, err
		}
		walletDebit = &entry
	}
	paymentValue, _, err := service.payments.Create(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-payment", bookingID, request.PaymentMethod, payment.Money{AmountMinor: amountDue.AmountMinor, Currency: amountDue.Currency})
	if err != nil {
		if walletDebit != nil {
			_, _, _ = service.wallet.ReverseDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet-rollback", walletDebit.ID, bookingID)
		}
		return Booking{}, false, err
	}
	now := service.clock().UTC()
	status := StatusPendingPayment
	if paymentCaptured(paymentValue.Status) {
		status = StatusRequested
	}
	policy := service.policies[scope.Country]
	value := Booking{
		ID: bookingID, Revision: 1, Status: status, Offering: cloneOffering(offering), Slot: slot,
		Price: offering.Price, AmountDue: amountDue, Payment: paymentValue,
		RescheduleCount: 0, FreeReschedulesLeft: policy.MaximumFreeReschedules,
		Timeline:  []TimelineEvent{{Status: status, Actor: "CUSTOMER", CreatedAt: now}},
		CreatedAt: now, UpdatedAt: now, scope: scope, walletDebit: walletDebit,
		paymentExpiresAt: hold.ExpiresAt,
	}
	value.AllowedActions = customerActions(value)
	hold.Status = HoldConsumed
	hold.AllowedActions = nil
	service.holds[hold.ID] = hold
	service.bookings[value.ID] = cloneBooking(value)
	service.bookingRequests[requestKey] = bookingReplay{fingerprint: fingerprint, value: cloneBooking(value)}
	return cloneBooking(value), false, nil
}

func (service *Service) ConfirmPayment(scope Scope, idempotencyKey, bookingID string, expectedRevision int64) (Booking, bool, error) {
	return service.mutateCustomer(scope, idempotencyKey, bookingID, expectedRevision, "confirm-payment", func(value *Booking) error {
		if value.Status == StatusRequested {
			return nil
		}
		if value.Status != StatusPendingPayment {
			return ErrInvalidTransition
		}
		paymentValue, err := service.payments.Get(payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, value.Payment.ID)
		if err != nil || !paymentCaptured(paymentValue.Status) {
			return ErrPolicyDenied
		}
		value.Payment = paymentValue
		service.transitionLocked(value, StatusRequested, "PAYMENT", "")
		return nil
	})
}

func (service *Service) Reschedule(scope Scope, idempotencyKey, bookingID string, expectedRevision int64, request RescheduleRequest) (Booking, bool, error) {
	reason := strings.TrimSpace(request.Reason)
	if !safeID(request.HoldID) || len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	fingerprint := request.HoldID + "\x00" + reason
	return service.mutateCustomerWithFingerprint(scope, idempotencyKey, bookingID, expectedRevision, "reschedule", fingerprint, func(value *Booking) error {
		if value.Status != StatusRequested && value.Status != StatusAccepted {
			return ErrInvalidTransition
		}
		policy := service.policies[scope.Country]
		if value.RescheduleCount >= policy.MaximumFreeReschedules || value.Slot.StartsAt.Sub(service.clock().UTC()) < policy.CancellationCutoff {
			return ErrPolicyDenied
		}
		hold, exists := service.holds[request.HoldID]
		if !exists || hold.scope != scope || hold.Status != HoldActive || !hold.ExpiresAt.After(service.clock().UTC()) || hold.OfferingID != value.Offering.ID || hold.SlotID == value.Slot.ID {
			return ErrSlotUnavailable
		}
		oldSlotID := value.Slot.ID
		hold.Status = HoldConsumed
		hold.AllowedActions = nil
		service.holds[hold.ID] = hold
		value.Slot = service.slotLocked(hold.SlotID)
		value.RescheduleCount++
		value.FreeReschedulesLeft = policy.MaximumFreeReschedules - value.RescheduleCount
		service.transitionLocked(value, StatusRescheduleRequested, "CUSTOMER", reason)
		service.transitionLocked(value, StatusRequested, "SYSTEM", "Reschedule accepted")
		service.releaseSlotLocked(oldSlotID)
		return nil
	})
}

func (service *Service) Cancel(scope Scope, idempotencyKey, bookingID string, expectedRevision int64, reason string) (Booking, bool, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	return service.mutateCustomerWithFingerprint(scope, idempotencyKey, bookingID, expectedRevision, "cancel", reason, func(value *Booking) error {
		if value.Status != StatusPendingPayment && value.Status != StatusRequested && value.Status != StatusAccepted {
			return ErrInvalidTransition
		}
		policy := service.policies[scope.Country]
		if value.Slot.StartsAt.Sub(service.clock().UTC()) < policy.CancellationCutoff {
			return ErrPolicyDenied
		}
		service.transitionLocked(value, StatusCancelRequested, "CUSTOMER", reason)
		paymentScope := payment.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}
		if value.walletDebit != nil {
			if _, _, err := service.wallet.ReverseDebit(wallet.Scope{TenantID: scope.TenantID, Country: scope.Country, CustomerID: scope.CustomerID}, idempotencyKey+"-wallet-refund", value.walletDebit.ID, value.ID); err != nil && !errors.Is(err, wallet.ErrAlreadyReversed) {
				return err
			}
			paymentValue, err := service.payments.RequestRefund(paymentScope, value.Payment.ID, payment.Money{AmountMinor: value.AmountDue.AmountMinor, Currency: value.AmountDue.Currency})
			if err != nil {
				return err
			}
			value.Payment = paymentValue
		} else if paymentCaptured(value.Payment.Status) {
			paymentValue, err := service.payments.RequestRefund(paymentScope, value.Payment.ID, payment.Money{AmountMinor: value.AmountDue.AmountMinor, Currency: value.AmountDue.Currency})
			if err != nil {
				return err
			}
			value.Payment = paymentValue
		} else {
			paymentValue, err := service.payments.CancelUncaptured(paymentScope, value.Payment.ID)
			if err != nil {
				return err
			}
			value.Payment = paymentValue
		}
		service.releaseSlotLocked(value.Slot.ID)
		service.transitionLocked(value, StatusCancelled, "SYSTEM", "Financial compensation recorded")
		return nil
	})
}

func (service *Service) ProviderTransition(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, target Status, reason string) (Booking, bool, error) {
	if !validActor(actor) || !validIdempotencyKey(idempotencyKey) || !safeID(bookingID) || expectedRevision < 1 {
		return Booking{}, false, ErrInvalidRequest
	}
	reason = strings.TrimSpace(reason)
	requestKey := "provider-transition\x00" + actorKey(actor) + "\x00" + idempotencyKey
	fingerprint := fmt.Sprintf("%s\x00%d\x00%s\x00%s", bookingID, expectedRevision, target, reason)
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	if replay, exists := service.bookingRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Booking{}, false, ErrIdempotencyConflict
		}
		return cloneBooking(replay.value), true, nil
	}
	value, exists := service.bookings[bookingID]
	if !exists || value.scope.TenantID != actor.TenantID || value.scope.Country != actor.Country {
		return Booking{}, false, ErrBookingNotFound
	}
	if !providerOwns(actor, value) {
		return Booking{}, false, ErrForbidden
	}
	if value.Revision != expectedRevision {
		return Booking{}, false, ErrRevisionConflict
	}
	if !validProviderTransition(value.Status, target) {
		return Booking{}, false, ErrInvalidTransition
	}
	if target == StatusAccepted {
		otp := deterministicOTP(value.ID, service.clock().UTC())
		value.StartOTP = otp
		value.startOTPDigest = sha256.Sum256([]byte(otp))
		value.otpExpiresAt = service.clock().UTC().Add(service.policies[value.scope.Country].StartOTPValidity)
		if value.otpExpiresAt.After(value.Slot.EndsAt) {
			value.otpExpiresAt = value.Slot.EndsAt
		}
	}
	service.transitionLocked(&value, target, "PROVIDER", reason)
	service.bookings[value.ID] = cloneBooking(value)
	service.bookingRequests[requestKey] = bookingReplay{fingerprint: fingerprint, value: cloneBooking(value)}
	return cloneBooking(value), false, nil
}

func (service *Service) Start(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, otp string) (Booking, bool, error) {
	if !validActor(actor) || !validIdempotencyKey(idempotencyKey) || !safeID(bookingID) || expectedRevision < 1 || len(otp) != 6 {
		return Booking{}, false, ErrInvalidRequest
	}
	requestKey := "service-start\x00" + actorKey(actor) + "\x00" + idempotencyKey
	fingerprint := fmt.Sprintf("%s\x00%d\x00%s", bookingID, expectedRevision, otp)
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.bookingRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Booking{}, false, ErrIdempotencyConflict
		}
		return cloneBooking(replay.value), true, nil
	}
	value, exists := service.bookings[bookingID]
	if !exists || !providerOwns(actor, value) {
		return Booking{}, false, ErrForbidden
	}
	if value.Status != StatusStartOTPRequired {
		return Booking{}, false, ErrInvalidTransition
	}
	if value.Revision != expectedRevision {
		return Booking{}, false, ErrRevisionConflict
	}
	provided := sha256.Sum256([]byte(otp))
	if service.clock().UTC().After(value.otpExpiresAt) || subtle.ConstantTimeCompare(provided[:], value.startOTPDigest[:]) != 1 {
		return Booking{}, false, ErrOTPInvalid
	}
	value.StartOTP = ""
	service.transitionLocked(&value, StatusInProgress, "PROVIDER", "Start OTP verified")
	service.bookings[value.ID] = cloneBooking(value)
	service.bookingRequests[requestKey] = bookingReplay{fingerprint: fingerprint, value: cloneBooking(value)}
	return cloneBooking(value), false, nil
}

func (service *Service) Complete(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, photoAssetID string) (Booking, bool, error) {
	if !validActor(actor) || !validIdempotencyKey(idempotencyKey) || !safeID(bookingID) || expectedRevision < 1 || !safeID(photoAssetID) {
		return Booking{}, false, ErrInvalidRequest
	}
	requestKey := "service-complete\x00" + actorKey(actor) + "\x00" + idempotencyKey
	fingerprint := fmt.Sprintf("%s\x00%d\x00%s", bookingID, expectedRevision, photoAssetID)
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.bookingRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Booking{}, false, ErrIdempotencyConflict
		}
		return cloneBooking(replay.value), true, nil
	}
	value, exists := service.bookings[bookingID]
	if !exists || !providerOwns(actor, value) {
		return Booking{}, false, ErrForbidden
	}
	if value.Status != StatusCompletionEvidenceRequired {
		return Booking{}, false, ErrInvalidTransition
	}
	if value.Revision != expectedRevision {
		return Booking{}, false, ErrRevisionConflict
	}
	now := service.clock().UTC()
	value.CompletionEvidence = &CompletionEvidence{PhotoAssetID: photoAssetID, CapturedAt: now, SubmittedBy: actor.Subject}
	service.transitionLocked(&value, StatusCompletedPendingConfirmation, "PROVIDER", "Completion evidence submitted")
	service.bookings[value.ID] = cloneBooking(value)
	service.bookingRequests[requestKey] = bookingReplay{fingerprint: fingerprint, value: cloneBooking(value)}
	return cloneBooking(value), false, nil
}

func (service *Service) ConfirmCompletion(scope Scope, idempotencyKey, bookingID string, expectedRevision int64) (Booking, bool, error) {
	return service.mutateCustomer(scope, idempotencyKey, bookingID, expectedRevision, "confirm-completion", func(value *Booking) error {
		if value.Status != StatusCompletedPendingConfirmation || value.CompletionEvidence == nil {
			return ErrEvidenceRequired
		}
		service.transitionLocked(value, StatusCompleted, "CUSTOMER", "Completion confirmed")
		return nil
	})
}

func (service *Service) NoShow(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, reason string) (Booking, bool, error) {
	reason = strings.TrimSpace(reason)
	if !validActor(actor) || !validIdempotencyKey(idempotencyKey) || !safeID(bookingID) || expectedRevision < 1 || len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	requestKey := "no-show\x00" + actorKey(actor) + "\x00" + idempotencyKey
	fingerprint := fmt.Sprintf("%s\x00%d\x00%s", bookingID, expectedRevision, reason)
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.bookingRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Booking{}, false, ErrIdempotencyConflict
		}
		return cloneBooking(replay.value), true, nil
	}
	value, exists := service.bookings[bookingID]
	if !exists || value.scope.TenantID != actor.TenantID || value.scope.Country != actor.Country {
		return Booking{}, false, ErrBookingNotFound
	}
	target := StatusProviderNoShow
	if providerOwns(actor, value) {
		target = StatusCustomerNoShow
	} else if !customerOwns(actor, value) {
		return Booking{}, false, ErrForbidden
	}
	if value.Status != StatusAccepted && value.Status != StatusProviderEnRoute && value.Status != StatusArrived && value.Status != StatusStartOTPRequired {
		return Booking{}, false, ErrInvalidTransition
	}
	if value.Revision != expectedRevision {
		return Booking{}, false, ErrRevisionConflict
	}
	service.transitionLocked(&value, target, roleLabel(actor), reason)
	service.bookings[value.ID] = cloneBooking(value)
	service.bookingRequests[requestKey] = bookingReplay{fingerprint: fingerprint, value: cloneBooking(value)}
	return cloneBooking(value), false, nil
}

func (service *Service) Dispute(actor Actor, idempotencyKey, bookingID string, expectedRevision int64, reason string) (Booking, bool, error) {
	reason = strings.TrimSpace(reason)
	if !validActor(actor) || !validIdempotencyKey(idempotencyKey) || !safeID(bookingID) || expectedRevision < 1 || len(reason) < 3 || len(reason) > 500 {
		return Booking{}, false, ErrInvalidRequest
	}
	requestKey := "booking-dispute\x00" + actorKey(actor) + "\x00" + idempotencyKey
	fingerprint := fmt.Sprintf("%s\x00%d\x00%s", bookingID, expectedRevision, reason)
	service.mu.Lock()
	defer service.mu.Unlock()
	if replay, exists := service.bookingRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Booking{}, false, ErrIdempotencyConflict
		}
		return cloneBooking(replay.value), true, nil
	}
	value, exists := service.bookings[bookingID]
	if !exists || (!customerOwns(actor, value) && !providerOwns(actor, value)) {
		return Booking{}, false, ErrForbidden
	}
	if value.Status != StatusCustomerNoShow && value.Status != StatusProviderNoShow && value.Status != StatusCompletedPendingConfirmation && value.Status != StatusCompleted {
		return Booking{}, false, ErrInvalidTransition
	}
	if value.Revision != expectedRevision {
		return Booking{}, false, ErrRevisionConflict
	}
	service.transitionLocked(&value, StatusDisputed, roleLabel(actor), reason)
	service.bookings[value.ID] = cloneBooking(value)
	service.bookingRequests[requestKey] = bookingReplay{fingerprint: fingerprint, value: cloneBooking(value)}
	return cloneBooking(value), false, nil
}

func (service *Service) Get(actor Actor, bookingID string) (Booking, error) {
	if !validActor(actor) || !safeID(bookingID) {
		return Booking{}, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	value, exists := service.bookings[bookingID]
	if !exists || (!customerOwns(actor, value) && !providerOwns(actor, value) && !hasRole(actor, "ADMIN")) {
		return Booking{}, ErrBookingNotFound
	}
	copy := cloneBooking(value)
	if !customerOwns(actor, value) {
		copy.StartOTP = ""
	}
	return copy, nil
}

func (service *Service) List(actor Actor) ([]Booking, error) {
	if !validActor(actor) {
		return nil, ErrInvalidRequest
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	result := []Booking{}
	for _, value := range service.bookings {
		if !customerOwns(actor, value) && !providerOwns(actor, value) && !hasRole(actor, "ADMIN") {
			continue
		}
		copy := cloneBooking(value)
		if !customerOwns(actor, value) {
			copy.StartOTP = ""
		}
		result = append(result, copy)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].CreatedAt.After(result[right].CreatedAt) })
	return result, nil
}

func (service *Service) mutateCustomer(scope Scope, idempotencyKey, bookingID string, expectedRevision int64, action string, mutation func(*Booking) error) (Booking, bool, error) {
	return service.mutateCustomerWithFingerprint(scope, idempotencyKey, bookingID, expectedRevision, action, bookingID, mutation)
}

func (service *Service) mutateCustomerWithFingerprint(scope Scope, idempotencyKey, bookingID string, expectedRevision int64, action, fingerprint string, mutation func(*Booking) error) (Booking, bool, error) {
	if !validScope(scope) || !validIdempotencyKey(idempotencyKey) || !safeID(bookingID) || expectedRevision < 1 {
		return Booking{}, false, ErrInvalidRequest
	}
	requestKey := action + "\x00" + scopeKey(scope) + "\x00" + idempotencyKey
	fingerprint = fmt.Sprintf("%d\x00%s", expectedRevision, fingerprint)
	service.mu.Lock()
	defer service.mu.Unlock()
	service.expireLocked()
	if replay, exists := service.bookingRequests[requestKey]; exists {
		if replay.fingerprint != fingerprint {
			return Booking{}, false, ErrIdempotencyConflict
		}
		return cloneBooking(replay.value), true, nil
	}
	value, exists := service.bookings[bookingID]
	if !exists || value.scope != scope {
		return Booking{}, false, ErrBookingNotFound
	}
	if value.Revision != expectedRevision {
		return Booking{}, false, ErrRevisionConflict
	}
	if err := mutation(&value); err != nil {
		return Booking{}, false, err
	}
	service.bookings[value.ID] = cloneBooking(value)
	service.bookingRequests[requestKey] = bookingReplay{fingerprint: fingerprint, value: cloneBooking(value)}
	return cloneBooking(value), false, nil
}

func (service *Service) transitionLocked(value *Booking, status Status, actor, reason string) {
	now := service.clock().UTC()
	value.Status = status
	value.Revision++
	value.UpdatedAt = now
	value.Timeline = append(value.Timeline, TimelineEvent{Status: status, Actor: actor, Reason: reason, CreatedAt: now})
	value.AllowedActions = customerActions(*value)
}

func (service *Service) expireLocked() {
	now := service.clock().UTC()
	for id, hold := range service.holds {
		if hold.Status == HoldActive && !hold.ExpiresAt.After(now) {
			hold.Status = HoldExpired
			hold.AllowedActions = nil
			service.holds[id] = hold
			service.releaseSlotLocked(hold.SlotID)
		}
	}
	for id, value := range service.bookings {
		if value.Status == StatusPendingPayment && !value.paymentExpiresAt.After(now) {
			paymentScope := payment.Scope{TenantID: value.scope.TenantID, Country: value.scope.Country, CustomerID: value.scope.CustomerID}
			paymentValue, err := service.payments.CancelUncaptured(paymentScope, value.Payment.ID)
			if err == nil {
				value.Payment = paymentValue
			}
			service.releaseSlotLocked(value.Slot.ID)
			service.transitionLocked(&value, StatusCancelled, "SYSTEM", "Payment window expired")
			service.bookings[id] = cloneBooking(value)
		}
	}
}

func (service *Service) slotLocked(slotID string) Slot {
	value := service.slots[slotID]
	value.Remaining = value.Capacity - service.reserved[slotID]
	if value.Remaining < 0 {
		value.Remaining = 0
	}
	value.AllowedActions = nil
	if value.Remaining > 0 && value.StartsAt.After(service.clock().UTC()) {
		value.AllowedActions = []string{"HOLD"}
	}
	return value
}

func (service *Service) releaseSlotLocked(slotID string) {
	if service.reserved[slotID] > 0 {
		service.reserved[slotID]--
	}
}

func validConfiguration(configuration Configuration) bool {
	if len(configuration.Offerings) == 0 || len(configuration.Slots) == 0 || len(configuration.Policies) == 0 {
		return false
	}
	offerings := map[string]Offering{}
	for _, value := range configuration.Offerings {
		if !safeID(value.ID) || !safeID(value.ProviderID) || !safeID(value.CategoryID) || strings.TrimSpace(value.ProviderName) == "" || strings.TrimSpace(value.Name) == "" || value.DurationMinutes < 15 || value.DurationMinutes > 24*60 || !validMoney(value.Price) || !validMoney(value.Advance) || value.Advance.Currency != value.Price.Currency || value.Advance.AmountMinor > value.Price.AmountMinor || (value.PaymentMode != PaymentFull && value.PaymentMode != PaymentAdvance) || len(value.ServicePostalCodes) == 0 || !safeID(value.CancellationPolicyRef) || !safeID(value.ReschedulePolicyRef) {
			return false
		}
		offerings[value.ID] = value
	}
	for _, value := range configuration.Slots {
		offering, exists := offerings[value.OfferingID]
		if !safeID(value.ID) || !exists || value.ProviderID != offering.ProviderID || !value.EndsAt.After(value.StartsAt) || value.Capacity < 1 || value.Capacity > 100 || value.BufferMinutes < 0 || value.BufferMinutes > 240 || value.TimeZone == "" || value.Price != offering.Price || value.Advance != offering.Advance || !safeID(value.PolicyVersion) || value.ProviderVersion < 1 {
			return false
		}
	}
	for _, value := range configuration.Policies {
		if len(value.Country) != 2 || !safeID(value.Version) || value.HoldTTL < time.Minute || value.HoldTTL > 30*time.Minute || value.CancellationCutoff < 0 || value.MaximumFreeReschedules < 0 || value.MaximumFreeReschedules > 10 || value.StartOTPValidity < time.Minute || value.CompletionConfirmWindow < time.Hour || value.WalletPointValueMinor < 1 {
			return false
		}
	}
	return true
}

func validScope(scope Scope) bool {
	return safeID(scope.TenantID) && len(scope.Country) == 2 && strings.ToUpper(scope.Country) == scope.Country && safeID(scope.CustomerID)
}

func validActor(actor Actor) bool {
	return safeID(actor.TenantID) && len(actor.Country) == 2 && safeID(actor.Subject) && len(actor.Roles) > 0
}

func validMoney(value Money) bool {
	return value.AmountMinor > 0 && len(value.Currency) == 3 && strings.ToUpper(value.Currency) == value.Currency
}

func validIdempotencyKey(value string) bool { return safeID(value) && len(value) >= 16 }

func safeID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func safePostalCode(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 3 && len(value) <= 12 && safeID(value)
}

func validBookingMethod(method payment.Method) bool {
	return method == payment.MethodRazorpay || method == payment.MethodPaystack || method == payment.MethodWallet
}

func paymentCaptured(status payment.Status) bool {
	return status == payment.StatusCaptured || status == payment.StatusReconciled
}

func validProviderTransition(current, target Status) bool {
	return (current == StatusRequested && (target == StatusAccepted || target == StatusDeclined)) ||
		(current == StatusAccepted && target == StatusProviderEnRoute) ||
		(current == StatusProviderEnRoute && target == StatusArrived) ||
		(current == StatusArrived && target == StatusStartOTPRequired) ||
		(current == StatusInProgress && target == StatusCompletionEvidenceRequired)
}

func customerActions(value Booking) []string {
	switch value.Status {
	case StatusPendingPayment:
		return []string{"CONFIRM_PAYMENT", "CANCEL"}
	case StatusRequested, StatusAccepted:
		return []string{"RESCHEDULE", "CANCEL", "CONTACT_PROVIDER"}
	case StatusProviderEnRoute, StatusArrived, StatusStartOTPRequired:
		return []string{"VIEW_START_OTP", "CONTACT_PROVIDER", "REPORT_PROVIDER_NO_SHOW"}
	case StatusInProgress, StatusCompletionEvidenceRequired:
		return []string{"VIEW_STATUS", "CONTACT_SUPPORT"}
	case StatusCompletedPendingConfirmation:
		return []string{"CONFIRM_COMPLETION", "DISPUTE"}
	case StatusCompleted:
		return []string{"RATE", "VIEW_RECEIPT", "DISPUTE"}
	case StatusCustomerNoShow, StatusProviderNoShow:
		return []string{"DISPUTE", "CONTACT_SUPPORT"}
	case StatusDisputed:
		return []string{"VIEW_DISPUTE", "CONTACT_SUPPORT"}
	case StatusCancelled, StatusDeclined:
		return []string{"VIEW_REFUND", "BOOK_AGAIN"}
	default:
		return []string{"VIEW_STATUS"}
	}
}

func providerOwns(actor Actor, value Booking) bool {
	return actor.TenantID == value.scope.TenantID && actor.Country == value.scope.Country &&
		((actor.Subject == value.Offering.ProviderID && (hasRole(actor, "VENDOR") || hasRole(actor, "SERVICE_VENDOR"))) || hasRole(actor, "ADMIN"))
}

func customerOwns(actor Actor, value Booking) bool {
	return actor.TenantID == value.scope.TenantID && actor.Country == value.scope.Country && actor.Subject == value.scope.CustomerID && hasRole(actor, "CUSTOMER")
}

func hasRole(actor Actor, role string) bool {
	for _, value := range actor.Roles {
		if strings.EqualFold(strings.TrimSpace(value), role) {
			return true
		}
	}
	return false
}

func roleLabel(actor Actor) string {
	if hasRole(actor, "CUSTOMER") {
		return "CUSTOMER"
	}
	if hasRole(actor, "ADMIN") {
		return "ADMIN"
	}
	return "PROVIDER"
}

func actorKey(actor Actor) string {
	return actor.TenantID + "\x00" + actor.Country + "\x00" + actor.Subject
}
func scopeKey(scope Scope) string {
	return scope.TenantID + "\x00" + scope.Country + "\x00" + scope.CustomerID
}

func deterministicID(prefix, source string) string {
	digest := sha256.Sum256([]byte(source))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

func deterministicOTP(bookingID string, issuedAt time.Time) string {
	digest := sha256.Sum256([]byte(bookingID + "\x00" + issuedAt.UTC().Format(time.RFC3339Nano)))
	value := int(digest[0])<<16 | int(digest[1])<<8 | int(digest[2])
	return fmt.Sprintf("%06d", value%1000000)
}

func containsFold(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func cloneOffering(value Offering) Offering {
	value.ServicePostalCodes = append([]string(nil), value.ServicePostalCodes...)
	return value
}

func cloneHold(value SlotHold) SlotHold {
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	return value
}

func cloneBooking(value Booking) Booking {
	value.Offering = cloneOffering(value.Offering)
	value.Slot.AllowedActions = append([]string(nil), value.Slot.AllowedActions...)
	value.Payment.AllowedActions = append([]string(nil), value.Payment.AllowedActions...)
	value.AllowedActions = append([]string(nil), value.AllowedActions...)
	value.Timeline = append([]TimelineEvent(nil), value.Timeline...)
	if value.CompletionEvidence != nil {
		copy := *value.CompletionEvidence
		value.CompletionEvidence = &copy
	}
	if value.walletDebit != nil {
		copy := *value.walletDebit
		value.walletDebit = &copy
	}
	return value
}
