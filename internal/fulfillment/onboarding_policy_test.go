package fulfillment

import "testing"

func TestRiderRegistrationAcceptsCanonicalMobileBicycleWithoutInventedPhoneProof(t *testing.T) {
	request := RiderRegistrationRequest{FullName: "Li", VehicleType: "BICYCLE", VehicleNumber: "CYC1", Documents: []RiderDocument{{Kind: "IDENTITY", AssetID: "asset-identity-one"}}, BankReference: "bankref_synthetic1234", Zones: []string{"600001"}}
	if !validRiderRegistration(request) {
		t.Fatal("normal bicycle registration without a phone mask was rejected")
	}
	for _, mutate := range []func(*RiderRegistrationRequest){
		func(r *RiderRegistrationRequest) { r.PhoneMasked = "+919876543210" },
		func(r *RiderRegistrationRequest) { r.VehicleType = "UNKNOWN" },
		func(r *RiderRegistrationRequest) { r.Documents[0].Kind = "ARBITRARY" },
		func(r *RiderRegistrationRequest) { r.Zones = []string{"other-rider-zone"} },
		func(r *RiderRegistrationRequest) { r.BankReference = "client-claims-verified" },
	} {
		copy := request
		copy.Documents = append([]RiderDocument(nil), request.Documents...)
		mutate(&copy)
		if validRiderRegistration(copy) {
			t.Fatalf("invalid onboarding accepted: %+v", copy)
		}
	}
}

func TestMotorRiderRequiresIndependentCompleteKindsAndDistinctAssetsIncludingLegacySpelling(t *testing.T) {
	full := []RiderDocument{{Kind: "IDENTITY", AssetID: "asset-identity-one"}, {Kind: "DRIVER_LICENSE", AssetID: "asset-license-one"}, {Kind: "VEHICLE_REGISTRATION", AssetID: "asset-registration-one"}, {Kind: "INSURANCE", AssetID: "asset-insurance-one"}}
	for _, vehicle := range []string{"MOTORBIKE", "CAR", "VAN", "BIKE"} {
		if !validRiderDocumentSet(vehicle, full) {
			t.Fatal("complete canonical evidence rejected")
		}
		for i := range full {
			copy := append([]RiderDocument(nil), full[:i]...)
			copy = append(copy, full[i+1:]...)
			if validRiderDocumentSet(vehicle, copy) {
				t.Fatalf("%s accepted missing %s", vehicle, full[i].Kind)
			}
		}
	}
	duplicate := append([]RiderDocument(nil), full...)
	duplicate[3].AssetID = duplicate[0].AssetID
	if validRiderDocumentSet("MOTORBIKE", duplicate) {
		t.Fatal("one private photo was reused for two requirements")
	}
	duplicate = append([]RiderDocument(nil), full...)
	duplicate[3].Kind = "LICENSE"
	if validRiderDocumentSet("MOTORBIKE", duplicate) {
		t.Fatal("legacy license alias bypassed kind uniqueness")
	}
}
