package fulfillment

import (
	"errors"
	"testing"
	"time"
)

func TestLiveLocationRequiresCurrentOrderPartyAndEndsWithDelivery(t *testing.T) {
	service, now := fulfillmentFixture(t)
	riderID := "rider-private-location"
	service.locations[riderID] = &RiderLocation{RiderID: riderID, ExpiresAt: now.Add(time.Minute), tenantID: "tenant-synthetic-001", country: "IN"}
	service.tasks["private-order"] = &DeliveryTask{ID: "private-order", AssignedRiderID: riderID, Status: "ASSIGNED", tenantID: "tenant-synthetic-001", country: "IN"}
	service.participants["private-order"] = taskParticipants{customerID: "customer-related", counterpartyID: "restaurant-related"}
	for _, actor := range []Actor{fulfillmentActor("customer-related", "CUSTOMER", false), fulfillmentActor("restaurant-related", "RESTAURANT_VENDOR", false), fulfillmentActor("dispatcher", "DISPATCH", true)} {
		if _, err := service.Location(actor, riderID); err != nil {
			t.Fatalf("related tracking=%v", err)
		}
	}
	for _, subject := range []string{"customer-unrelated", riderID} {
		if _, err := service.Location(fulfillmentActor(subject, "CUSTOMER", false), riderID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unrelated=%v", err)
		}
	}
	service.tasks["private-order"].Status = "DELIVERED"
	if _, err := service.Location(fulfillmentActor("customer-related", "CUSTOMER", false), riderID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("completed tracking=%v", err)
	}
}
