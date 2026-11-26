package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/rbac"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/session"
)

// Logging out (or any revocation) must cut an open stream on its next poll, not after the
// stream's lifetime: the poll authorizes by membership, so the session needs its own check.
func TestEventStreamEndsWhenItsSessionIsRevoked(t *testing.T) {
	f := newFlow(t, false, rbac.RoleDeveloper)
	cookie := f.signIn(t)
	sessionAuth := f.deps.Auth.(*SessionAuthenticator)

	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	deps := f.deps
	deps.Public = append(append([]RouteRegistrar{}, deps.Public...), NewRequestEventsHandler(feed, StreamOptions{
		Poll: 5 * time.Millisecond, Heartbeat: time.Hour, MaxLifetime: time.Hour, Session: sessionAuth}))
	srv := httptest.NewServer(NewRouter(deps))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest("GET", srv.URL+"/v1/requests/"+feedRequest.String()+"/events", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	fr := newFrameReader(resp.Body)
	if first := fr.data(t); first.Event != "snapshot" {
		t.Fatalf("first frame = %+v", first)
	}

	// The user logs out in another tab; a status change follows, which the stream must not deliver.
	if err := f.sess.Revoke(context.Background(), cookie.Value); err != nil {
		t.Fatal(err)
	}
	feed.add(requests.StatusPROpen)
	done := make(chan []frame, 1)
	go func() { done <- fr.rest() }()
	select {
	case rest := <-done:
		for _, fm := range rest {
			if fm.Event == "status" || fm.Event == "end" {
				t.Fatalf("a revoked session still received %+v", fm)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its revoked session")
	}
}

// Alive checks the cookie against the store without sliding the idle window.
func TestSessionAuthenticatorAlive(t *testing.T) {
	f := newFlow(t, false, rbac.RoleDeveloper)
	c := f.signIn(t)
	a := f.deps.Auth.(*SessionAuthenticator)
	ctx := context.Background()
	withCookie := func(c *http.Cookie) *http.Request {
		r := httptest.NewRequest("GET", "/v1/requests/x/events", nil)
		if c != nil {
			r.AddCookie(c)
		}
		return r
	}

	if admin, err := a.Alive(ctx, withCookie(c)); err != nil || admin {
		t.Fatalf("a live session of a non-admin: admin=%v err=%v", admin, err)
	}
	f.setPlatformAdmin(c, true)
	if admin, err := a.Alive(ctx, withCookie(c)); err != nil || !admin {
		t.Fatalf("a live session of a platform admin: admin=%v err=%v", admin, err)
	}
	f.setPlatformAdmin(c, false)
	if _, err := a.Alive(ctx, withCookie(nil)); err == nil {
		t.Fatal("no cookie is not alive")
	}
	if _, err := a.Alive(ctx, withCookie(&http.Cookie{Name: c.Name, Value: "nope"})); err == nil {
		t.Fatal("an unknown session is not alive")
	}
	seen := f.store.rows[string(session.HashID(c.Value))].LastSeenAt
	f.clock.t = f.clock.t.Add(10 * time.Minute) // inside the idle window
	if _, err := a.Alive(ctx, withCookie(c)); err != nil {
		t.Fatal(err)
	}
	if got := f.store.rows[string(session.HashID(c.Value))].LastSeenAt; !got.Equal(seen) {
		t.Fatalf("Alive slid the idle window: %s -> %s", seen, got)
	}
	f.clock.t = f.clock.t.Add(2 * time.Hour) // past the idle timeout
	if _, err := a.Alive(ctx, withCookie(c)); err == nil {
		t.Fatal("an idle-expired session is not alive")
	}
}

// setPlatformAdmin flips the stored admin flag of the user behind a session cookie.
func (f *flow) setPlatformAdmin(c *http.Cookie, admin bool) {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	k := string(session.HashID(c.Value))
	r := f.store.rows[k]
	r.PlatformAdmin = admin
	f.store.rows[k] = r
}

// The poll authorizes with the principal the stream was opened with, so a changed admin
// flag (demotion keeps team data readable, promotion widens it) must end the stream.
func TestEventStreamEndsWhenThePlatformAdminFlagChanges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start bool
	}{{"demoted", true}, {"promoted", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlow(t, false, rbac.RoleDeveloper)
			cookie := f.signIn(t)
			f.setPlatformAdmin(cookie, tc.start)

			feed := &fakeFeed{}
			feed.add(requests.StatusDraft)
			deps := f.deps
			deps.Public = append(append([]RouteRegistrar{}, deps.Public...), NewRequestEventsHandler(feed, StreamOptions{
				Poll: 5 * time.Millisecond, Heartbeat: time.Hour, MaxLifetime: time.Hour, Session: f.deps.Auth.(*SessionAuthenticator)}))
			srv := httptest.NewServer(NewRouter(deps))
			t.Cleanup(srv.Close)

			req, _ := http.NewRequest("GET", srv.URL+"/v1/requests/"+feedRequest.String()+"/events", nil)
			req.AddCookie(cookie)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			fr := newFrameReader(resp.Body)
			if first := fr.data(t); first.Event != "snapshot" {
				t.Fatalf("first frame = %+v", first)
			}

			f.setPlatformAdmin(cookie, !tc.start)
			feed.add(requests.StatusPROpen)
			done := make(chan []frame, 1)
			go func() { done <- fr.rest() }()
			select {
			case rest := <-done:
				for _, fm := range rest {
					if fm.Event == "status" || fm.Event == "end" {
						t.Fatalf("a stream whose admin flag changed still received %+v", fm)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the stream outlived the change of its admin flag")
			}
		})
	}
}
