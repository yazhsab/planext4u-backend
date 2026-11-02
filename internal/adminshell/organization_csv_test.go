package adminshell

import "testing"

func TestOrganizationExportNeutralizesLeadingWhitespaceFormulas(t *testing.T) {
	for _, value := range []string{"=1+1", "+SUM(1,2)", "-1+1", "@SUM(1,2)", "  +SUM(1,2)", "\u00a0=1+1", "\ufeff@SUM(1,2)", "\tname", "name\r\n=1+1", "\x01  -1"} {
		if actual := organizationCSVText(value); actual != "'"+value {
			t.Fatalf("formula not escaped: %q", value)
		}
	}
	for _, value := range []string{"", "North Chennai", "தமிழ்நாடு", "1 North", "  North"} {
		if actual := organizationCSVText(value); actual != value {
			t.Fatalf("ordinary name changed: %q", value)
		}
	}
}
