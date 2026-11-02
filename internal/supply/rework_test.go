package supply

import "testing"

func TestReworkRequiresDistinctCanonicalDocumentSet(t *testing.T) {
	a, b := "90000000-0000-4000-8000-000000000901", "90000000-0000-4000-8000-000000000902"
	for _, documents := range [][]Document{
		{{Kind: "OWNER_IDENTITY", AssetID: a}, {Kind: "IDENTITY_PROOF", AssetID: b}},
		{{Kind: "OWNER_IDENTITY", AssetID: a}, {Kind: "BUSINESS_REGISTRATION", AssetID: a}},
		{{Kind: "BUSINESS_REGISTRATION", AssetID: a}, {Kind: "BUSINESS_LICENSE", AssetID: b}},
		{{Kind: "OWNER_IDENTITY", AssetID: a}, {Kind: "UNREVIEWED_KIND", AssetID: b}},
	} {
		if validReworkDocuments(documents) {
			t.Fatalf("accepted incomplete/duplicate documents: %+v", documents)
		}
	}
	if !validReworkDocuments([]Document{{Kind: "OWNER_IDENTITY", AssetID: a}, {Kind: "BUSINESS_REGISTRATION", AssetID: b}}) || !validReworkDocuments([]Document{{Kind: "IDENTITY_PROOF", AssetID: a}, {Kind: "BUSINESS_LICENSE", AssetID: b}}) {
		t.Fatal("required canonical or bounded legacy set denied")
	}
}
