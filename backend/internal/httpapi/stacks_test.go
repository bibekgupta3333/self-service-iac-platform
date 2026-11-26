package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/stacks"
)

type fakeStacks struct {
	queries []stacks.Query
	callers []identity.Principal
}

var stackAt = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

func sampleStack() stacks.Stack {
	return stacks.Stack{ID: lockStack, Team: "payments", Environment: "development", Name: "orders",
		Path: "environments/development/payments/orders", Region: "us-east-1", Status: requests.StackActive,
		Outputs: json.RawMessage(`{"queue_url":"http://q"}`), CreatedAt: stackAt, UpdatedAt: stackAt,
		LatestRequest: &stacks.RequestRef{ID: lockHolder, ShortID: "req-7k3p9x", Kind: requests.KindUpdate,
			Status: requests.StatusApplied, CreatedAt: stackAt}}
}

func (f *fakeStacks) List(_ context.Context, by identity.Principal, q stacks.Query) ([]stacks.Stack, *requests.Cursor, error) {
	f.queries, f.callers = append(f.queries, q), append(f.callers, by)
	if q.Team == "nope" {
		return nil, nil, apperr.Invalid("bad_request", "team must be a team slug")
	}
	return []stacks.Stack{sampleStack()}, &requests.Cursor{CreatedAt: stackAt, ID: lockStack}, nil
}

func (f *fakeStacks) Get(_ context.Context, _ identity.Principal, id uuid.UUID) (stacks.Stack, error) {
	if id != lockStack {
		return stacks.Stack{}, apperr.NotFound("stack %s not found", id)
	}
	s := sampleStack()
	s.LatestRequest, s.Outputs = nil, nil
	return s, nil
}

func TestStackHandlers(t *testing.T) {
	svc := &fakeStacks{}
	h := newTestRouter(nil, okPinger{}, NewStackHandler(svc))

	rec := do(t, h, "GET", "/v1/stacks?team=payments&env=development&status=active&limit=20", "")
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Data []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			LatestRequest struct {
				ShortID string `json:"short_id"`
				Kind    string `json:"kind"`
			} `json:"latest_request"`
			Links struct {
				Self          string `json:"self"`
				LatestRequest string `json:"latest_request"`
			} `json:"links"`
			Outputs map[string]string `json:"outputs"`
		} `json:"data"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	d := list.Data[0]
	if len(list.Data) != 1 || d.Status != "active" || d.LatestRequest.ShortID != "req-7k3p9x" || d.Links.Self != "/v1/stacks/"+lockStack.String() ||
		d.Links.LatestRequest != "/v1/requests/"+lockHolder.String() || d.Outputs["queue_url"] != "http://q" || list.NextCursor == nil {
		t.Fatalf("list = %+v", list)
	}
	q := svc.queries[0]
	if q.Team != "payments" || q.Environment != "development" || q.Status != requests.StackActive || q.Limit != 20 || svc.callers[0] != devPrincipal {
		t.Fatalf("query = %+v by %+v", q, svc.callers[0])
	}

	// The cursor from one page opens the next.
	do(t, h, "GET", "/v1/stacks?cursor="+*list.NextCursor, "")
	if c := svc.queries[1].After; c == nil || c.ID != lockStack || !c.CreatedAt.Equal(stackAt) {
		t.Fatalf("cursor not decoded: %+v", svc.queries[1])
	}

	for url, want := range map[string]int{
		"/v1/stacks?limit=0":             400,
		"/v1/stacks?limit=abc":           400,
		"/v1/stacks?cursor=!!notbase64":          400,
		"/v1/stacks?team=nope":           400,
		"/v1/stacks/not-a-uuid":          404,
		"/v1/stacks/" + uuid.NewString(): 404,
	} {
		if rec := do(t, h, "GET", url, ""); rec.Code != want {
			t.Errorf("%s = %d, want %d: %s", url, rec.Code, want, rec.Body)
		}
	}

	rec = do(t, h, "GET", "/v1/stacks/"+lockStack.String(), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"latest_request":null`) || !strings.Contains(rec.Body.String(), `"outputs":{}`) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	// Stacks are never created or destroyed directly.
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if rec := do(t, h, m, "/v1/stacks/"+lockStack.String(), ""); rec.Code != 405 {
			t.Errorf("%s /v1/stacks/{id} = %d, want 405", m, rec.Code)
		}
	}
}
