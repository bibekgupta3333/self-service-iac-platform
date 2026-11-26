package requests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/ttl"
)

var ttlBot = identity.Principal{UserID: uuid.MustParse("99999999-9999-4999-8999-999999999999"), Login: ttl.Actor, Type: identity.ActorSystem}

// ephemeralStack creates an ephemeral stack for 72 h and returns its id; the stack is made
// active like a finished apply would.
func ephemeralStack(t *testing.T, repo *memRepo, svc *Service, in CreateInput) uuid.UUID {
	t.Helper()
	in.Ephemeral = &Ephemeral{TTLHours: 72}
	res, err := svc.Create(context.Background(), dev, createCmd(in, "key-ttl-seed-01"))
	if err != nil {
		t.Fatal(err)
	}
	repo.setStackStatus(res.Request.Stack.ID, StackActive)
	for _, r := range repo.state.requests {
		r.Status = StatusApplied // the create is finished, so only the destroy stays open
	}
	return res.Request.Stack.ID
}

func TestOpenTTLDestroy(t *testing.T) {
	ctx := context.Background()
	clock := &stepClock{t: t0}
	newTTLSvc := func() (*memRepo, *Service, uuid.UUID) {
		repo := newMemRepo()
		svc := NewService(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleDeveloper}, &fakeLocker{}, clock, &seqIDs{}).EnablePublishing()
		clock.t = t0
		return repo, svc, ephemeralStack(t, repo, svc, validInput())
	}
	cmd := func(id uuid.UUID) ttl.DestroyCommand {
		return ttl.DestroyCommand{StackID: id, By: ttlBot, CorrelationID: "corr-ttl"}
	}

	t.Run("opens a destroy request as the system actor once the stack expired", func(t *testing.T) {
		repo, svc, id := newTTLSvc()
		clock.t = t0.Add(72 * time.Hour)
		res, err := svc.OpenTTLDestroy(ctx, cmd(id))
		if err != nil || !res.Created || res.RequestID == uuid.Nil || !strings.HasPrefix(res.ShortID, "req-") {
			t.Fatalf("OpenTTLDestroy = %+v, %v", res, err)
		}
		var req *Request
		for _, r := range repo.state.requests {
			if r.ID == res.RequestID {
				req = r
			}
		}
		if req == nil || req.Kind != KindDestroy || req.Justification != "ttl expired" || req.CreatedBy != ttlBot.UserID ||
			req.Stack.ID != id || req.Status != StatusDraft {
			t.Fatalf("request = %+v", req)
		}
		var spec NormalizedSpec
		if err := json.Unmarshal(req.Spec, &spec); err != nil || spec.Ephemeral != nil || len(spec.Resources) != 0 {
			t.Fatalf("a destroy renders an empty spec: %s (%v)", req.Spec, err)
		}
		var actions []string
		for _, e := range repo.state.events {
			actions = append(actions, e.Action)
			if e.Action == "ttl.expired" && (e.ActorID != ttl.Actor || e.SubjectID != id.String() || e.CorrelationID != "corr-ttl" ||
				!strings.Contains(string(e.Payload), res.ShortID)) {
				t.Fatalf("ttl.expired audit = %+v", e)
			}
		}
		if !contains(actions, "ttl.expired") || !contains(actions, "request.created") || !contains(actions, "request.publish_enqueued") {
			t.Fatalf("audit actions = %v", actions)
		}
		if len(repo.state.jobs) == 0 {
			t.Fatal("the destroy request must be published like any other")
		}
	})
	t.Run("is idempotent while a destroy is open", func(t *testing.T) {
		repo, svc, id := newTTLSvc()
		clock.t = t0.Add(80 * time.Hour)
		if _, err := svc.OpenTTLDestroy(ctx, cmd(id)); err != nil {
			t.Fatal(err)
		}
		n := len(repo.state.requests)
		res, err := svc.OpenTTLDestroy(ctx, cmd(id))
		if err != nil || res.Created || len(repo.state.requests) != n {
			t.Fatalf("second call = %+v, %v (requests %d -> %d)", res, err, n, len(repo.state.requests))
		}
	})
	t.Run("refuses a stack that has not expired", func(t *testing.T) {
		repo, svc, id := newTTLSvc()
		clock.t = t0.Add(72*time.Hour - time.Second)
		_, err := svc.OpenTTLDestroy(ctx, cmd(id))
		if e, ok := apperr.As(err); !ok || e.Code != "ttl_not_due" {
			t.Fatalf("err = %v", err)
		}
		if len(repo.state.events) != 0 && repo.state.events[len(repo.state.events)-1].Action == "ttl.expired" {
			t.Fatal("nothing may be audited as expired")
		}
	})
	t.Run("refuses a permanent stack, however old", func(t *testing.T) {
		repo := newMemRepo()
		svc := NewService(repo, fakeVersions{}, fakeMembers{dev.UserID: RoleDeveloper}, &fakeLocker{}, clock, &seqIDs{})
		clock.t = t0
		res, err := svc.Create(ctx, dev, createCmd(validInput(), "key-ttl-perm-01"))
		if err != nil {
			t.Fatal(err)
		}
		repo.setStackStatus(res.Request.Stack.ID, StackActive)
		clock.t = t0.Add(10000 * time.Hour)
		_, err = svc.OpenTTLDestroy(ctx, cmd(res.Request.Stack.ID))
		if e, ok := apperr.As(err); !ok || e.Code != "not_ephemeral" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("refuses production even if the stack row claims to be ephemeral", func(t *testing.T) {
		repo, svc, id := newTTLSvc()
		clock.t = t0.Add(100 * time.Hour)
		repo.state.environmentOf = map[uuid.UUID]string{id: "production"}
		_, err := svc.OpenTTLDestroy(ctx, cmd(id))
		if e, ok := apperr.As(err); !ok || e.Code != "production_not_ephemeral" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a stack that is busy waits for the next run", func(t *testing.T) {
		repo, svc, id := newTTLSvc()
		repo.setStackStatus(id, StackUpdating)
		clock.t = t0.Add(100 * time.Hour)
		_, err := svc.OpenTTLDestroy(ctx, cmd(id))
		if e, ok := apperr.As(err); !ok || e.Code != "stack_busy" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unknown stack is not found", func(t *testing.T) {
		_, svc, _ := newTTLSvc()
		if _, err := svc.OpenTTLDestroy(ctx, cmd(uuid.New())); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func cloneStrMap(m map[uuid.UUID]string) map[uuid.UUID]string {
	out := make(map[uuid.UUID]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
