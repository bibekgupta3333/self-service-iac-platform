package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/oauth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

type flowClock struct{ t time.Time }

func (c *flowClock) Now() time.Time { return c.t }

type flowStore struct{ rows map[string]session.Record }

func (s *flowStore) Insert(_ context.Context, r session.Record) error {
	r.Login = "priya"
	s.rows[string(r.IDHash)] = r
	return nil
}
func (s *flowStore) Get(_ context.Context, h []byte) (session.Record, error) {
	r, ok := s.rows[string(h)]
	if !ok {
		return session.Record{}, session.ErrNotFound
	}
	return r, nil
}
func (s *flowStore) Touch(_ context.Context, h []byte, now time.Time) error {
	r := s.rows[string(h)]
	r.LastSeenAt = now
	s.rows[string(h)] = r
	return nil
}
func (s *flowStore) Revoke(_ context.Context, h []byte, now time.Time) error {
	if r, ok := s.rows[string(h)]; ok {
		r.RevokedAt = &now
		s.rows[string(h)] = r
	}
	return nil
}
func (s *flowStore) RevokeUser(context.Context, uuid.UUID, time.Time) error { return nil }
func (s *flowStore) PurgeExpired(context.Context, time.Time, time.Duration) (int64, error) {
	return 0, nil
}

type flowLogin struct {
	sessions *session.Service
	user     uuid.UUID
	begun    []string
	fail     error
}

func (f *flowLogin) Begin(_ context.Context, returnTo string) (string, string, error) {
	f.begun = append(f.begun, returnTo)
	state, _ := oauth.NewToken(strings.NewReader(strings.Repeat("s", 32)))
	return "https://github.com/login/oauth/authorize?state=" + state, state, nil
}

func (f *flowLogin) Complete(ctx context.Context, code, qs, cs string, _ oauth.Meta) (oauth.Result, error) {
	if f.fail != nil {
		return oauth.Result{}, f.fail
	}
	if !oauth.StatesMatch(qs, cs) || code == "" {
		return oauth.Result{}, oauth.ErrInvalidState
	}
	id, err := f.sessions.Issue(ctx, f.user)
	return oauth.Result{SessionID: id, ReturnTo: "/requests", User: oauth.SyncResult{UserID: f.user, Login: "priya"}}, err
}

func (f *flowLogin) RedirectPolicy() oauth.RedirectPolicy { return oauth.RedirectPolicy{Default: "/"} }

type flowProfiles struct{ user uuid.UUID }

func (f flowProfiles) Profile(context.Context, uuid.UUID) (identity.Profile, error) {
	return identity.Profile{ID: f.user, Login: "priya", DisplayName: "Priya S.",
		Teams: []identity.TeamRole{{Slug: "payments", Role: rbac.RoleDeveloper}}}, nil
}

type flowRoles struct{ role rbac.Role }

func (f flowRoles) HighestRole(context.Context, uuid.UUID) (rbac.Role, error) { return f.role, nil }

type flowAudit struct{ events []audit.Event }

func (f *flowAudit) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

type flow struct {
	h     http.Handler
	sess  *session.Service
	store *flowStore
	clock *flowClock
	login *flowLogin
	audit *flowAudit
	reqs  *fakeRequests
	user  uuid.UUID
}

func newFlow(t *testing.T, secure bool, role rbac.Role) *flow {
	t.Helper()
	f := &flow{store: &flowStore{rows: map[string]session.Record{}}, clock: &flowClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)},
		audit: &flowAudit{}, user: uuid.New(), reqs: &fakeRequests{byHash: map[string]*requests.Request{}}}
	f.sess = session.NewService(f.store, session.Policy{AbsoluteTTL: 8 * time.Hour, IdleTTL: 30 * time.Minute, TouchInterval: time.Minute}, f.clock)
	f.login = &flowLogin{sessions: f.sess, user: f.user}
	ah := &AuthHandler{Login: f.login, Sessions: f.sess, Profiles: flowProfiles{f.user}, Audit: f.audit, Secure: secure, Mode: "github"}
	f.h = NewRouter(Deps{
		Logger:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth:           NewSessionAuthenticator(f.sess),
		Ready:          okPinger{},
		Catalog:        NewCatalogHandler(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}),
		Public:         []RouteRegistrar{ah, NewRequestHandler(f.reqs), NewAuditHandler(&fakeVerifier{})},
		PublicHandlers: []RouteRegistrar{ah.Public()},
		Policies:       DefaultPolicies(),
		Roles:          flowRoles{role},
		DenialAudit:    f.audit,
	})
	return f
}

func cookieOf(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
