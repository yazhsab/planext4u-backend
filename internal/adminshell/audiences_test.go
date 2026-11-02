package adminshell

import "testing"

func TestOrganizationActorDerivesMFAAndSourceProofWithoutInventingAssurance(t *testing.T) {
	principal := fieldPrincipal()
	principal.AuthMethods = []string{"password"}
	actor := organizationActor(ResolvedSession{Principal: principal})
	if actor.MFA {
		t.Fatal("password-only principal became MFA verified")
	}
	if actor.OperatorSessionID != principal.SessionID || actor.OperatorRevision != principal.OperatorRevision || !actor.RequireLiveOperator {
		t.Fatal("source operator proof was not derived from the resolved principal")
	}
	principal.AuthMethods = []string{"password", "TOTP"}
	if !organizationActor(ResolvedSession{Principal: principal}).MFA {
		t.Fatal("actual MFA method rejected")
	}
}
