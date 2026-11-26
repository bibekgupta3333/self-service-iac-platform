package oauth

import (
	"context"
	"errors"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// If the login cannot be audited the user gets no usable session: it is revoked and the
// login fails.
func TestFailedLoginAuditRevokesTheSession(t *testing.T) {
	r := newRig(t)
	r.audit.fail = errors.New("audit store down")
	_, state, _ := r.svc.Begin(context.Background(), "")
	res, err := r.svc.Complete(context.Background(), "c", state, state, Meta{})
	if err == nil || res.SessionID != "" {
		t.Fatalf("login must fail without a session: %+v, %v", res, err)
	}
	if len(r.sessions.issued) != 1 || len(r.sessions.revoked) != 1 || r.sessions.revoked[0] != "session-for-"+r.user.String() {
		t.Fatalf("issued=%v revoked=%v: the new session must be revoked", r.sessions.issued, r.sessions.revoked)
	}
}

// A failing audit store must not turn a denial into a success, nor hide it.
func TestDeniedLoginStaysDeniedWhenAuditFails(t *testing.T) {
	r := newRig(t)
	r.audit.fail = errors.New("audit store down")
	r.gh.user = identity.GitHubUser{ID: 9, Login: "outsider", OrgMember: false}
	_, state, _ := r.svc.Begin(context.Background(), "")
	if _, err := r.svc.Complete(context.Background(), "c", state, state, Meta{}); !errors.Is(err, ErrNotOrgMember) {
		t.Fatalf("err = %v", err)
	}
	if len(r.sessions.issued) != 0 {
		t.Fatal("no session for a denied login")
	}
}
