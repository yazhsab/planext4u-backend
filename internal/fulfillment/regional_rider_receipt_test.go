package fulfillment

import "testing"

func TestRegionalRiderReviewReceiptExcludesPrivateFields(t *testing.T) {
	value := RiderProfile{ID: "application", RiderID: "rider", Revision: 3, Status: RiderSuspended,
		FullName: "Private rider", PhoneMasked: "******1234", VehicleNumber: "PRIVATE",
		Documents:     []RiderDocument{{Kind: "IDENTITY", AssetID: "private-asset"}},
		BankReference: "private-bank", BankStatus: "PENDING", Zones: []string{"private-zone"}}
	regional := regionalRiderReviewReceipt(Actor{Roles: []string{"REGIONAL_ADMIN"}}, value)
	if regional.ID != value.ID || regional.RiderID != value.RiderID || regional.Revision != value.Revision || regional.Status != value.Status ||
		regional.FullName != "" || regional.PhoneMasked != "" || regional.VehicleNumber != "" || len(regional.Documents) != 0 || regional.BankReference != "" || regional.BankStatus != "" || len(regional.Zones) != 0 {
		t.Fatalf("regional response disclosed private application fields: %+v", regional)
	}
	country := regionalRiderReviewReceipt(Actor{Roles: []string{"OPS_ADMIN"}}, value)
	if country.BankReference != value.BankReference || len(country.Documents) != 1 {
		t.Fatal("country reviewer lost authorized detail")
	}
}
