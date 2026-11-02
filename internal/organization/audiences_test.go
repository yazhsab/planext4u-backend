package organization

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAudienceDefinitionRejectsAmbiguousAndUnboundedCriteria(t *testing.T) {
	valid := AudienceGroup{NodeID: uuid.NewString(), Name: "West delivery participants", RoleIDs: []string{uuid.NewString()}, Active: true, IncludeDescendants: true}
	if !validAudienceGroup(valid) {
		t.Fatal("valid create rejected")
	}
	update := valid
	update.ID = uuid.NewString()
	update.Revision = 1
	if !validAudienceGroup(update) {
		t.Fatal("valid revisioned update rejected")
	}
	cases := []AudienceGroup{}
	v := valid
	v.Name = " "
	cases = append(cases, v)
	v = valid
	v.Name = strings.Repeat("界", 129)
	cases = append(cases, v)
	v = valid
	v.NodeID = "not-a-node"
	cases = append(cases, v)
	v = valid
	v.RoleIDs = nil
	cases = append(cases, v)
	v = valid
	v.RoleIDs = []string{valid.RoleIDs[0], strings.ToUpper(valid.RoleIDs[0])}
	cases = append(cases, v)
	v = valid
	v.RoleIDs = []string{"not-a-role"}
	cases = append(cases, v)
	v = valid
	v.RoleIDs = make([]string, 33)
	for i := range v.RoleIDs {
		v.RoleIDs[i] = uuid.NewString()
	}
	cases = append(cases, v)
	v = valid
	v.Revision = 1
	cases = append(cases, v)
	v = update
	v.Revision = 0
	cases = append(cases, v)
	v = update
	v.Revision = -1
	cases = append(cases, v)
	for i, v := range cases {
		if validAudienceGroup(v) {
			t.Errorf("invalid criteria %d accepted", i)
		}
	}
}
