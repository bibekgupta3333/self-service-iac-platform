package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/locks"
)

var (
	lockStack  = uuid.MustParse("b2c1f0a9-3e4d-4c5b-8a7f-1e2d3c4b5a69")
	lockHolder = uuid.MustParse("0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90")
	lockWaiter = uuid.MustParse("1e8f4b63-7d22-4a1f-8ac7-6a3f4d9e2ba1")
	lockAt     = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
)

type fakeLocks struct {
	acquired []locks.AcquireCommand
	released []locks.TokenCommand
}

func heldLock() locks.Lock {
	return locks.Lock{StackID: lockStack, RequestID: lockHolder, Holder: "request:req-7k3p9x", FencingToken: 48214,
		AcquiredAt: lockAt, ExpiresAt: lockAt.Add(locks.DefaultLease)}
}

func (f *fakeLocks) Acquire(_ context.Context, cmd locks.AcquireCommand) (locks.Result, error) {
	f.acquired = append(f.acquired, cmd)
	if cmd.RequestID == lockHolder {
		return locks.Result{Outcome: locks.OutcomeGranted, Lock: heldLock()}, nil
	}
	behind := lockHolder
	return locks.Result{Outcome: locks.OutcomeQueued, Lock: heldLock(), QueuedBehind: &behind}, nil
}

func (f *fakeLocks) Heartbeat(_ context.Context, cmd locks.TokenCommand) (locks.Lock, error) {
	if cmd.FencingToken != 48214 {
		return locks.Lock{}, apperr.Conflict("stale_fencing_token", "fencing token is not current")
	}
	return heldLock(), nil
}

func (f *fakeLocks) Release(_ context.Context, cmd locks.TokenCommand) (locks.ReleaseResult, error) {
	if cmd.FencingToken != 48214 {
		return locks.ReleaseResult{}, apperr.Conflict("stale_fencing_token", "fencing token is not current")
	}
	f.released = append(f.released, cmd)
	promoted := heldLock()
	promoted.RequestID, promoted.FencingToken = lockWaiter, 48215
	return locks.ReleaseResult{Released: heldLock(), Promoted: &promoted}, nil
}

func (f *fakeLocks) Get(_ context.Context, stackID uuid.UUID) (locks.Lock, error) {
	if stackID != lockStack {
		return locks.Lock{}, apperr.NotFound("stack %s is not locked", stackID)
	}
	return heldLock(), nil
}

func TestLockHandlers(t *testing.T) {
	fl := &fakeLocks{}
	h := NewRouter(Deps{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal: NewTokenAuthenticator(testInternalToken, releaseBot), Ready: okPinger{},
		Catalog: NewCatalogHandler(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}), InternalHandlers: []RouteRegistrar{NewLockHandler(fl)},
	})
	auth := []string{"Authorization", "Bearer " + testInternalToken, "X-Request-Id", "corr-lock"}
	base := "/v1/internal/stacks/" + lockStack.String() + "/lock"

	tests := []struct {
		name, method, path, body string
		noAuth                   bool
		want                     int
		check                    func(t *testing.T, b map[string]any)
	}{
		{name: "no token", method: "POST", path: base, body: `{"request_id":"` + lockHolder.String() + `"}`, noAuth: true, want: 401},
		{name: "granted", method: "POST", path: base, body: `{"request_id":"` + lockHolder.String() + `"}`, want: 200,
			check: func(t *testing.T, b map[string]any) {
				cur := b["current"].(map[string]any)
				if b["lock"] != "granted" || b["queued_behind"] != nil || cur["fencing_token"] != float64(48214) ||
					cur["holder"] != "request:req-7k3p9x" || cur["expires_at"] != "2026-12-01T10:00:00Z" {
					t.Fatalf("body = %v", b)
				}
			}},
		{name: "queued", method: "POST", path: base, body: `{"request_id":"` + lockWaiter.String() + `"}`, want: 202,
			check: func(t *testing.T, b map[string]any) {
				if b["lock"] != "queued" || b["queued_behind"] != lockHolder.String() {
					t.Fatalf("body = %v", b)
				}
			}},
		{name: "missing request id", method: "POST", path: base, body: `{}`, want: 422},
		{name: "unknown field", method: "POST", path: base, body: `{"request_id":"` + lockHolder.String() + `","x":1}`, want: 400},
		{name: "bad stack id", method: "POST", path: "/v1/internal/stacks/nope/lock", body: `{}`, want: 404},
		{name: "get", method: "GET", path: base, want: 200,
			check: func(t *testing.T, b map[string]any) {
				if b["request_id"] != lockHolder.String() || b["stack_id"] != lockStack.String() {
					t.Fatalf("body = %v", b)
				}
			}},
		{name: "get unlocked", method: "GET", path: "/v1/internal/stacks/" + uuid.NewString() + "/lock", want: 404},
		{name: "heartbeat", method: "POST", path: base + "/heartbeat", body: `{"request_id":"` + lockHolder.String() + `","fencing_token":48214}`, want: 200},
		{name: "heartbeat stale", method: "POST", path: base + "/heartbeat", body: `{"request_id":"` + lockHolder.String() + `","fencing_token":1}`, want: 409,
			check: func(t *testing.T, b map[string]any) {
				if b["code"] != "stale_fencing_token" {
					t.Fatalf("body = %v", b)
				}
			}},
		{name: "heartbeat without token", method: "POST", path: base + "/heartbeat", body: `{"request_id":"` + lockHolder.String() + `"}`, want: 422},
		{name: "release promotes", method: "DELETE", path: base + "?request_id=" + lockHolder.String() + "&fencing_token=48214", want: 200,
			check: func(t *testing.T, b map[string]any) {
				p := b["promoted"].(map[string]any)
				if b["released"].(map[string]any)["fencing_token"] != float64(48214) || p["request_id"] != lockWaiter.String() || p["fencing_token"] != float64(48215) {
					t.Fatalf("body = %v", b)
				}
			}},
		{name: "release stale", method: "DELETE", path: base + "?request_id=" + lockHolder.String() + "&fencing_token=3", want: 409},
		{name: "release bad token", method: "DELETE", path: base + "?request_id=" + lockHolder.String() + "&fencing_token=x", want: 400},
		{name: "release missing request", method: "DELETE", path: base + "?fencing_token=48214", want: 422},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := auth
			if tt.noAuth {
				headers = nil
			}
			rec := do(t, h, tt.method, tt.path, tt.body, headers...)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body)
			}
			if tt.check != nil {
				var b map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
					t.Fatal(err)
				}
				tt.check(t, b)
			}
		})
	}
	if len(fl.acquired) != 2 || fl.acquired[0].By != releaseBot || fl.acquired[0].CorrelationID != "corr-lock" || fl.acquired[0].StackID != lockStack {
		t.Fatalf("acquire commands = %+v", fl.acquired)
	}
	if len(fl.released) != 1 || fl.released[0].RequestID != lockHolder {
		t.Fatalf("release commands = %+v", fl.released)
	}
	// Lock routes are machine-only: the dev user authenticator does not open them.
	if rec := do(t, h, http.MethodGet, base, ""); rec.Code != 401 {
		t.Fatalf("lock route without internal token = %d", rec.Code)
	}
}
