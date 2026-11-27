package auth

import "testing"

func TestClaimsIdentityCarriesActorAndEnvironment(t *testing.T) {
	wf, err := Claims{RunID: "7", RunAttempt: "2", Actor: "priya-s", Environment: "break-glass"}.Identity()
	if err != nil || wf.Actor != "priya-s" || wf.Environment != "break-glass" || wf.RunID != 7 || wf.RunAttempt != 2 {
		t.Fatalf("%+v, %v", wf, err)
	}
}
