package oauth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

type fakeIdentity struct {
	user      identity.GitHubUser
	err       error
	gotCode   string
	gotVerify string
}

func (f *fakeIdentity) AuthorizeURL(state, challenge string) string {
	return "https://github.test/authorize?state=" + state + "&code_challenge=" + challenge
}

func (f *fakeIdentity) Authenticate(_ context.Context, code, verifier string) (identity.GitHubUser, error) {
	f.gotCode, f.gotVerify = code, verifier
	return f.user, f.err
}

type memStates struct{ rows map[string]StateRecord }

func (m *memStates) Save(_ context.Context, r StateRecord) error {
	m.rows[string(r.StateHash)] = r
	return nil
}

func (m *memStates) Consume(_ context.Context, h []byte, now time.Time) (StateRecord, error) {
	r, ok := m.rows[string(h)]
	if !ok || !now.Before(r.ExpiresAt) {
		return StateRecord{}, ErrInvalidState
	}
	delete(m.rows, string(h))
	return r, nil
}

func (m *memStates) Purge(context.Context, time.Time) (int64, error) { return 0, nil }

type fakeSyncer struct {
	res   SyncResult
	err   error
	calls int
}

func (f *fakeSyncer) SyncLogin(context.Context, identity.GitHubUser) (SyncResult, error) {
	f.calls++
	return f.res, f.err
}

type fakeSessions struct{ issued []uuid.UUID }

func (f *fakeSessions) Issue(_ context.Context, u uuid.UUID) (string, error) {
	f.issued = append(f.issued, u)
	return "session-for-" + u.String(), nil
}

type fakeAudit struct{ events []audit.Event }

func (f *fakeAudit) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

type rig struct {
	svc      *Service
	gh       *fakeIdentity
	states   *memStates
	syncer   *fakeSyncer
	sessions *fakeSessions
	audit    *fakeAudit
	clock    *mutClock
	user     uuid.UUID
}

type mutClock struct{ t time.Time }

func (c *mutClock) Now() time.Time { return c.t }

var _ platform.Clock = (*mutClock)(nil)

func newRig(t *testing.T) *rig {
	t.Helper()
	user := uuid.New()
	r := &rig{
		gh:       &fakeIdentity{user: identity.GitHubUser{ID: 7, Login: "priya", OrgMember: true}},
		states:   &memStates{rows: map[string]StateRecord{}},
		syncer:   &fakeSyncer{res: SyncResult{UserID: user, Login: "priya"}},
		sessions: &fakeSessions{},
		audit:    &fakeAudit{},
		clock:    &mutClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)},
		user:     user,
	}
	r.svc = NewService(r.gh, r.states, r.syncer, r.sessions, r.audit,
		RedirectPolicy{Origins: []string{"https://portal.example.test"}, Default: "/"}, r.clock)
	return r
}

func stateFromURL(t *testing.T, raw string) (state, challenge string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state"), u.Query().Get("code_challenge")
}

func TestBeginStoresVerifierAndPKCEChallenge(t *testing.T) {
	r := newRig(t)
	authURL, state, err := r.svc.Begin(context.Background(), "/requests")
	if err != nil {
		t.Fatal(err)
	}
	qs, challenge := stateFromURL(t, authURL)
	if qs != state || !ValidToken(state) {
		t.Fatalf("state in URL %q vs returned %q", qs, state)
	}
	rec, ok := r.states.rows[string(HashState(state))]
	if !ok {
		t.Fatal("the state must be stored (hashed)")
	}
	if Challenge(rec.Verifier) != challenge {
		t.Fatal("the URL carries the S256 challenge of the stored verifier")
	}
	if rec.ReturnTo != "/requests" || !rec.ExpiresAt.Equal(r.clock.t.Add(StateTTL)) {
		t.Fatalf("record = %+v", rec)
	}
	for k := range r.states.rows {
		if strings.Contains(k, state) {
			t.Fatal("the raw state must not be stored")
		}
	}
}

func TestBeginSanitisesReturnTo(t *testing.T) {
	r := newRig(t)
	for _, evil := range []string{"//evil.example.test", "https://evil.example.test/", "javascript:alert(1)"} {
		_, state, err := r.svc.Begin(context.Background(), evil)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.states.rows[string(HashState(state))].ReturnTo; got != "/" {
			t.Errorf("return_to %q was stored as %q", evil, got)
		}
	}
}

func TestCompleteHappyPath(t *testing.T) {
	r := newRig(t)
	_, state, _ := r.svc.Begin(context.Background(), "https://portal.example.test/catalog")
	verifier := r.states.rows[string(HashState(state))].Verifier

	res, err := r.svc.Complete(context.Background(), "the-code", state, state, Meta{IP: "203.0.113.9", CorrelationID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.gh.gotCode != "the-code" || r.gh.gotVerify != verifier {
		t.Fatalf("the code and the stored PKCE verifier must reach GitHub: %q %q", r.gh.gotCode, r.gh.gotVerify)
	}
	if res.ReturnTo != "https://portal.example.test/catalog" || res.User.UserID != r.user || res.SessionID != "session-for-"+r.user.String() {
		t.Fatalf("result = %+v", res)
	}
	if len(r.audit.events) != 1 {
		t.Fatalf("audit events = %+v", r.audit.events)
	}
	if e := r.audit.events[0]; e.Action != "auth.login" || e.ActorIP != "203.0.113.9" || e.CorrelationID != "req-1" || e.SubjectID != r.user.String() {
		t.Fatalf("login event = %+v", e)
	}
}

func TestCompleteStateIsSingleUse(t *testing.T) {
	r := newRig(t)
	_, state, _ := r.svc.Begin(context.Background(), "")
	if _, err := r.svc.Complete(context.Background(), "c", state, state, Meta{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Complete(context.Background(), "c", state, state, Meta{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("a replayed callback: %v", err)
	}
	if len(r.sessions.issued) != 1 {
		t.Fatalf("sessions issued = %d", len(r.sessions.issued))
	}
}

func TestCompleteRejectsExpiredState(t *testing.T) {
	r := newRig(t)
	_, state, _ := r.svc.Begin(context.Background(), "")
	r.clock.t = r.clock.t.Add(StateTTL)
	if _, err := r.svc.Complete(context.Background(), "c", state, state, Meta{}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expired state: %v", err)
	}
}

// Login CSRF: an attacker starts a login, then tricks the victim's browser into completing
// it. The victim's browser holds no (or a different) state cookie, so it must fail.
func TestCompleteRequiresTheStateCookieToMatch(t *testing.T) {
	r := newRig(t)
	_, attackerState, _ := r.svc.Begin(context.Background(), "")
	_, victimState, _ := r.svc.Begin(context.Background(), "")
	for name, cookie := range map[string]string{"no cookie": "", "another login's state": victimState, "garbage": "x"} {
		if _, err := r.svc.Complete(context.Background(), "c", attackerState, cookie, Meta{}); !errors.Is(err, ErrInvalidState) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(r.sessions.issued) != 0 || r.gh.gotCode != "" {
		t.Fatal("nothing may reach GitHub or the session store on a state mismatch")
	}
	// A rejected attempt must not burn the legitimate one.
	if _, err := r.svc.Complete(context.Background(), "c", attackerState, attackerState, Meta{}); err != nil {
		t.Fatalf("the genuine browser can still finish: %v", err)
	}
}

func TestCompleteRejectsBadInput(t *testing.T) {
	r := newRig(t)
	_, state, _ := r.svc.Begin(context.Background(), "")
	for _, code := range []string{"", strings.Repeat("c", 513)} {
		if _, err := r.svc.Complete(context.Background(), code, state, state, Meta{}); !errors.Is(err, ErrInvalidState) {
			t.Errorf("code len %d: %v", len(code), err)
		}
	}
}

func TestCompleteDeniesNonMembersAndAudits(t *testing.T) {
	r := newRig(t)
	r.gh.user = identity.GitHubUser{ID: 9, Login: "outsider", OrgMember: false}
	_, state, _ := r.svc.Begin(context.Background(), "")
	_, err := r.svc.Complete(context.Background(), "c", state, state, Meta{})
	if !errors.Is(err, ErrNotOrgMember) {
		t.Fatalf("err = %v", err)
	}
	if r.syncer.calls != 0 || len(r.sessions.issued) != 0 {
		t.Fatal("a non-member must not be synced or get a session")
	}
	if len(r.audit.events) != 1 || r.audit.events[0].Action != "auth.login_denied" || r.audit.events[0].ActorID != "outsider" {
		t.Fatalf("events = %+v", r.audit.events)
	}
}

func TestCompleteDisabledUser(t *testing.T) {
	r := newRig(t)
	r.syncer.err = ErrUserDisabled
	_, state, _ := r.svc.Begin(context.Background(), "")
	if _, err := r.svc.Complete(context.Background(), "c", state, state, Meta{}); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("err = %v", err)
	}
	if len(r.sessions.issued) != 0 || len(r.audit.events) != 1 || r.audit.events[0].Action != "auth.login_denied" {
		t.Fatalf("issued=%d events=%+v", len(r.sessions.issued), r.audit.events)
	}
}

func TestCompleteGitHubFailure(t *testing.T) {
	r := newRig(t)
	r.gh.err = ErrGitHub
	_, state, _ := r.svc.Begin(context.Background(), "")
	_, err := r.svc.Complete(context.Background(), "c", state, state, Meta{})
	if !errors.Is(err, ErrGitHubUnavailable) || LoginErrorCode(err) != "github_unavailable" {
		t.Fatalf("err = %v", err)
	}
	if len(r.sessions.issued) != 0 {
		t.Fatal("no session on failure")
	}
}

// Fixation: every successful login yields a new session, whatever the browser held before.
func TestEveryLoginGetsAFreshSession(t *testing.T) {
	r := newRig(t)
	seen := map[string]bool{}
	for range 3 {
		_, state, _ := r.svc.Begin(context.Background(), "")
		// session ids are random in the real issuer; the fake derives them from the user, so
		// assert the service always calls Issue instead of reusing anything.
		res, err := r.svc.Complete(context.Background(), "c", state, state, Meta{})
		if err != nil {
			t.Fatal(err)
		}
		seen[res.SessionID] = true
	}
	if len(r.sessions.issued) != 3 {
		t.Fatalf("Issue calls = %d, want 3", len(r.sessions.issued))
	}
}
