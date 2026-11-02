package organization

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMasterValidationAndHierarchy(t *testing.T) {
	base := MasterChange{Master: Master{ID: uuid.NewString(), Kind: "OCCUPATION", Code: "TEACHER", NameEN: "Teacher", NameTA: "ஆசிரியர்", Active: true}, Reason: "Approved initial master"}
	if !validMaster(base, "IN") {
		t.Fatal("valid bilingual occupation rejected")
	}
	for _, change := range []func(*MasterChange){
		func(v *MasterChange) { v.Kind = "UNKNOWN" }, func(v *MasterChange) { v.Code = "bad code" }, func(v *MasterChange) { v.Revision = -1 },
		func(v *MasterChange) { v.NameEN = " Teacher" }, func(v *MasterChange) { v.NameTA = "" }, func(v *MasterChange) { v.NameEN = "Name\tControl" },
		func(v *MasterChange) { v.Reason = "short" }, func(v *MasterChange) { v.NameEN = string([]byte{0xff}) }, func(v *MasterChange) { v.NameTA = strings.Repeat("அ", 129) },
		func(v *MasterChange) { v.ParentID = uuid.NewString() }, func(v *MasterChange) { v.ID = "not-uuid" },
	} {
		value := base
		change(&value)
		if validMaster(value, "IN") {
			t.Fatalf("accepted invalid master: %+v", value)
		}
	}
	for kind, parent := range map[string]string{"COUNTRY": "", "STATE": "COUNTRY", "DISTRICT": "STATE", "CITY": "DISTRICT", "AREA": "CITY", "OCCUPATION": "", "REASON": ""} {
		got, ok := masterParent(kind)
		if !ok || got != parent {
			t.Fatalf("invalid hierarchy %s", kind)
		}
		value := base
		value.Kind = kind
		if parent != "" {
			value.ParentID = uuid.NewString()
		}
		if kind == "COUNTRY" {
			value.Code = "IN"
		}
		if !validMaster(value, "IN") {
			t.Fatalf("valid %s rejected", kind)
		}
		if kind == "COUNTRY" && validMaster(value, "US") {
			t.Fatal("cross-country root accepted")
		}
	}
}
