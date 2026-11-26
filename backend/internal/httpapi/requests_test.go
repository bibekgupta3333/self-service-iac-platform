package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// fakeRequests records calls and replays by request hash.
type fakeRequests struct {
	byHash  map[string]*requests.Request
	cmds    []requests.CreateCommand
	callers []identity.Principal
	filters []requests.ListFilter
}

func (f *fakeRequests) Create(_ context.Context, by identity.Principal, cmd requests.CreateCommand) (requests.CreateResult, error) {
	f.cmds, f.callers = append(f.cmds, cmd), append(f.callers, by)
	if cmd.Input.Stack == "bad" {
		return requests.CreateResult{}, apperr.Invalid("validation_failed", "request is invalid",
			apperr.FieldError{Pointer: "/stack", Code: "pattern", Message: "bad"})
	}
	if r, ok := f.byHash[cmd.RequestHash]; ok {
		return requests.CreateResult{Request: r, Replayed: true}, nil
	}
	r := requests.NewDraft(uuid.New(), "req-abc123", requests.StackRef{Path: "environments/development/payments/" + cmd.Input.Stack},
		cmd.Input.Kind, json.RawMessage(`{}`), "", by.UserID, time.Date(2026, 11, 24, 10, 2, 11, 0, time.UTC))
	f.byHash[cmd.RequestHash] = r
	return requests.CreateResult{Request: r}, nil
}

func (f *fakeRequests) Get(_ context.Context, id uuid.UUID) (*requests.Request, error) {
	for _, r := range f.byHash {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, apperr.NotFound("request %s not found", id)
}

func (f *fakeRequests) List(_ context.Context, flt requests.ListFilter) ([]*requests.Request, *requests.Cursor, error) {
	f.filters = append(f.filters, flt)
	var out []*requests.Request
	for _, r := range f.byHash {
		out = append(out, r)
	}
	return out, &requests.Cursor{CreatedAt: time.Unix(0, 0).UTC(), ID: uuid.Nil}, nil
}

func TestRequestHandlers(t *testing.T) {
	svc := &fakeRequests{byHash: map[string]*requests.Request{}}
	h := newTestRouter(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}, okPinger{}, NewRequestHandler(svc, allowAllAccess{}))
	body := `{"kind":"create","team":"payments","environment":"development","stack":"order-events",
		"resources":[{"name":"orders_queue","module":"sqs","version":"1.3.0","inputs":{"name":"orders"}}]}`
	const key = "6a1f0c7e-2b9d-4d3e-a1c2-9f8e7d6c5b4a"

	// Missing key.
	if rec := do(t, h, "POST", "/v1/requests", body); rec.Code != 400 {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body)
	}

	rec := do(t, h, "POST", "/v1/requests", body, "Idempotency-Key", key, "X-Request-Id", "corr-42")
	if rec.Code != 202 || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("create: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created["id"].(string)
	if created["status"] != "draft" || created["short_id"] != "req-abc123" || rec.Header().Get("Location") != "/v1/requests/"+id ||
		created["links"].(map[string]any)["self"] != "/v1/requests/"+id {
		t.Fatalf("create body = %v", created)
	}
	cmd := svc.cmds[0]
	if cmd.IdempotencyKey != key || cmd.CorrelationID != "corr-42" || svc.callers[0] != devPrincipal ||
		cmd.RequestHash != requests.RequestHash("POST", "/v1/requests", []byte(body)) || cmd.Input.Team != "payments" {
		t.Fatalf("command not plumbed: %+v", cmd)
	}

	rec = do(t, h, "POST", "/v1/requests", body, "Idempotency-Key", key)
	if rec.Code != 202 || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", rec.Code, rec.Header())
	}

	rec = do(t, h, "POST", "/v1/requests", `{"kind":"create","stack":"bad"}`, "Idempotency-Key", key+"-2")
	if rec.Code != 422 {
		t.Fatalf("invalid: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", "/v1/requests", `{"kind":"create","stak":"x"}`, "Idempotency-Key", key+"-3"); rec.Code != 400 {
		t.Fatalf("unknown field: %d", rec.Code)
	}

	rec = do(t, h, "GET", "/v1/requests/"+id, "")
	if rec.Code != 200 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["spec"] == nil || got["allowed_events"] == nil {
		t.Fatalf("get body = %v", got)
	}
	for _, path := range []string{"/v1/requests/not-a-uuid", "/v1/requests/" + uuid.NewString()} {
		if rec := do(t, h, "GET", path, ""); rec.Code != 404 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
	}

	rec = do(t, h, "GET", "/v1/requests?team=payments&status=draft&kind=create&limit=10&mine=true", "")
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var list map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list["data"].([]any)) != 1 || list["next_cursor"] == nil {
		t.Fatalf("list body = %v", list)
	}
	f := svc.filters[0]
	if f.Team != "payments" || f.Status != requests.StatusDraft || f.Kind != requests.KindCreate || f.Limit != 10 ||
		f.CreatedBy == nil || *f.CreatedBy != devPrincipal.UserID {
		t.Fatalf("filter = %+v", f)
	}
	rec = do(t, h, "GET", "/v1/requests?cursor="+list["next_cursor"].(string), "")
	if rec.Code != 200 || svc.filters[1].After == nil {
		t.Fatalf("cursor not decoded: %d %+v", rec.Code, svc.filters[1])
	}
	for _, q := range []string{"limit=0", "limit=abc", "cursor=%21%21"} {
		if rec := do(t, h, "GET", "/v1/requests?"+q, ""); rec.Code != 400 {
			t.Errorf("GET ?%s = %d, want 400", q, rec.Code)
		}
	}
}
