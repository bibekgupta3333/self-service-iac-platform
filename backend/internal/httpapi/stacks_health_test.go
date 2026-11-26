package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/stacks"
)

// oneStack serves a single stack for the health and lock tests.
type oneStack struct{ s stacks.Stack }

func (o oneStack) List(context.Context, identity.Principal, stacks.Query) ([]stacks.Stack, *requests.Cursor, error) {
	return []stacks.Stack{o.s}, nil, nil
}

func (o oneStack) Get(context.Context, identity.Principal, uuid.UUID) (stacks.Stack, error) {
	return o.s, nil
}

func TestStackShowsHealthAndTheLockThatHoldsIt(t *testing.T) {
	at := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	req := uuid.New()
	base := stacks.Stack{ID: uuid.New(), Status: requests.StackActive, CreatedAt: at, UpdatedAt: at}

	held := base
	held.Health = requests.HealthDegraded
	held.Lock = &stacks.LockInfo{RequestID: req, RequestShortID: "req-7k3p9x", Holder: "request:req-7k3p9x",
		AcquiredAt: at, ExpiresAt: at.Add(7 * 24 * time.Hour)}
	free := base // health unset (a row from before the column): reads as unknown, and no lock

	for name, tc := range map[string]struct {
		s      stacks.Stack
		health string
		lock   *map[string]any
	}{
		"held and degraded": {held, "degraded", &map[string]any{"request_id": req.String(), "request_short_id": "req-7k3p9x",
			"holder": "request:req-7k3p9x", "acquired_at": "2026-11-26T10:00:00Z", "expires_at": "2026-12-03T10:00:00Z"}},
		"free": {free, "unknown", nil},
	} {
		t.Run(name, func(t *testing.T) {
			h := newTestRouter(nil, okPinger{}, NewStackHandler(oneStack{tc.s}))
			rec := do(t, h, "GET", "/v1/stacks/"+tc.s.ID.String(), "")
			if rec.Code != 200 {
				t.Fatalf("get: %d %s", rec.Code, rec.Body)
			}
			var got struct {
				Health string         `json:"health"`
				Lock   map[string]any `json:"lock"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Health != tc.health {
				t.Fatalf("health = %q", got.Health)
			}
			if (tc.lock == nil) != (got.Lock == nil) {
				t.Fatalf("lock = %v", got.Lock)
			}
			if tc.lock != nil {
				for k, want := range *tc.lock {
					if got.Lock[k] != want {
						t.Errorf("lock.%s = %v, want %v", k, got.Lock[k], want)
					}
				}
				if _, leaked := got.Lock["fencing_token"]; leaked {
					t.Fatal("the fencing token must not be shown to users")
				}
			}
		})
	}
}
