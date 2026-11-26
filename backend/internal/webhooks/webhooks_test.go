package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/outbox"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func TestVerifySignature(t *testing.T) {
	secret, body := []byte("0123456789abcdef"), []byte(`{"zen":"ok"}`)
	good := Sign(secret, body)
	tests := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		want   error
	}{
		{"valid", secret, body, good, nil},
		{"no secret fails closed", nil, body, good, ErrNoSecret},
		{"missing header", secret, body, "", ErrBadSignature},
		{"sha1 header", secret, body, "sha1=abc", ErrBadSignature},
		{"not hex", secret, body, "sha256=zz", ErrBadSignature},
		{"short", secret, body, good[:20], ErrBadSignature},
		{"tampered body", secret, []byte(`{"zen":"no"}`), good, ErrBadSignature},
		{"other secret", []byte("fedcba9876543210"), body, good, ErrBadSignature},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := VerifySignature(tt.secret, tt.body, tt.header); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func prBody(action string, merged bool, ref string) []byte {
	b, _ := json.Marshal(map[string]any{"action": action, "number": 812, "before": "b0", "after": "a1",
		"pull_request": map[string]any{"merged": merged, "merge_commit_sha": "abc", "head": map[string]any{"ref": ref, "sha": "h1"}},
		"repository":   map[string]any{"full_name": "acme/infra"}})
	return b
}

func TestMap(t *testing.T) {
	run := []byte(`{"action":"completed","workflow_run":{"id":5,"path":".github/workflows/pr-validate.yml",
		"head_branch":"iacp/req-7k3p9x","head_sha":"s","conclusion":"success","html_url":"u"},"repository":{"full_name":"acme/infra"}}`)
	tests := []struct {
		name  string
		event string
		body  []byte
		want  *Command
		err   bool
	}{
		{"merged", "pull_request", prBody("closed", true, "iacp/req-7k3p9x"), &Command{Kind: KindTransition,
			RequestEvent: requests.EventPRMerged, MergeCommitSHA: "abc", Action: "closed", HeadSHA: "h1"}, false},
		{"closed unmerged", "pull_request", prBody("closed", false, "iacp/req-7k3p9x"), &Command{Kind: KindTransition,
			RequestEvent: requests.EventPRClosed, Action: "closed", HeadSHA: "h1"}, false},
		{"synchronize", "pull_request", prBody("synchronize", false, "iacp/req-7k3p9x"), &Command{Kind: KindTransition,
			RequestEvent: requests.EventNewCommit, Action: "synchronize", BeforeSHA: "b0", HeadSHA: "a1"}, false},
		{"opened is ignored", "pull_request", prBody("opened", false, "iacp/req-7k3p9x"), nil, false},
		{"foreign branch is ignored", "pull_request", prBody("closed", true, "feature/x"), nil, false},
		{"workflow run completed", "workflow_run", run, &Command{Kind: KindHint, Action: "completed",
			Workflow: ".github/workflows/pr-validate.yml", RunID: 5, Conclusion: "success", HeadSHA: "s", URL: "u"}, false},
		{"ping", "ping", []byte(`{"zen":"x"}`), nil, false},
		{"unknown event", "star", []byte(`not json`), nil, false},
		{"malformed known event", "pull_request", []byte(`{`), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Map(tt.event, "d-1", tt.body)
			if (err != nil) != tt.err || (tt.err && !errors.Is(err, ErrMalformedPayload)) {
				t.Fatalf("err = %v", err)
			}
			if tt.want == nil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			want := *tt.want
			want.DeliveryID, want.Event, want.Repository, want.RequestShortID = "d-1", tt.event, "acme/infra", "req-7k3p9x"
			if tt.event == "pull_request" {
				want.PRNumber = 812
			}
			if got == nil || *got != want {
				t.Fatalf("got %+v\nwant %+v", got, want)
			}
		})
	}
}

type memIngestRepo struct {
	seen map[uuid.UUID]bool
	jobs []outbox.Message
}

func (m *memIngestRepo) InTx(_ context.Context, fn func(Store) error) error { return fn(m) }

func (m *memIngestRepo) RecordDelivery(_ context.Context, id uuid.UUID, _ string, _ time.Time) (bool, error) {
	if m.seen[id] {
		return false, nil
	}
	m.seen[id] = true
	return true, nil
}

func (m *memIngestRepo) Enqueue(_ context.Context, msg outbox.Message) (bool, error) {
	m.jobs = append(m.jobs, msg)
	return true, nil
}

func TestIngestDedupesAndFiltersRepository(t *testing.T) {
	repo := &memIngestRepo{seen: map[uuid.UUID]bool{}}
	in := NewIngester(repo, platform.FixedClock{T: time.Unix(0, 0)}, "acme/infra")
	ctx := context.Background()
	d := Delivery{ID: uuid.New(), Event: "pull_request", Body: prBody("closed", false, "iacp/req-7k3p9x")}
	if res, err := in.Ingest(ctx, d); err != nil || !res.Enqueued || res.Duplicate {
		t.Fatalf("first: %+v, %v", res, err)
	}
	if res, err := in.Ingest(ctx, d); err != nil || res.Enqueued || !res.Duplicate {
		t.Fatalf("redelivery: %+v, %v", res, err)
	}
	// GitHub's canonical full_name may differ in case from the configured repository.
	mixed := NewIngester(repo, platform.FixedClock{}, "ACME/Infra")
	if res, err := mixed.Ingest(ctx, Delivery{ID: uuid.New(), Event: "pull_request", Body: d.Body}); err != nil || !res.Enqueued {
		t.Fatalf("case-insensitive repository: %+v, %v", res, err)
	}
	other := NewIngester(repo, platform.FixedClock{}, "acme/other")
	if res, err := other.Ingest(ctx, Delivery{ID: uuid.New(), Event: "pull_request", Body: d.Body}); err != nil || res.Enqueued {
		t.Fatalf("foreign repository: %+v, %v", res, err)
	}
	if res, err := in.Ingest(ctx, Delivery{ID: uuid.New(), Event: "ping", Body: []byte(`{}`)}); err != nil || res.Enqueued || res.Duplicate {
		t.Fatalf("ping: %+v, %v", res, err)
	}
	if len(repo.jobs) != 2 || repo.jobs[0].Topic != outbox.TopicGitHubEvent {
		t.Fatalf("jobs = %+v", repo.jobs)
	}
}

type fakeEvents struct {
	events []requests.GitHubEvent
	hints  []requests.WorkflowHint
	known  string // when set, pushes not starting here are stale
}

func (f *fakeEvents) ApplyGitHubEvent(_ context.Context, e requests.GitHubEvent) (requests.EventOutcome, error) {
	f.events = append(f.events, e)
	if f.known != "" && e.Event == requests.EventNewCommit && e.BeforeSHA != f.known {
		return requests.EventOutcome{Reason: "stale_head", KnownHead: f.known}, nil
	}
	return requests.EventOutcome{Applied: true, KnownHead: f.known}, nil
}

type fixedHead string

func (h fixedHead) PullRequestHead(context.Context, int) (string, error) { return string(h), nil }

func TestApplierResyncsAStalePush(t *testing.T) {
	ctx := context.Background()
	push, _ := json.Marshal(Command{Kind: KindTransition, RequestShortID: "req-7k3p9x", PRNumber: 812,
		RequestEvent: requests.EventNewCommit, BeforeSHA: "b", HeadSHA: "c", DeliveryID: "d"})
	tests := []struct {
		name       string
		head       PRHeads
		wantEvents int
		wantLast   requests.GitHubEvent
	}{
		{name: "real head moved: one push from the known head", head: fixedHead("c"), wantEvents: 2,
			wantLast: requests.GitHubEvent{BeforeSHA: "a", HeadSHA: "c"}},
		{name: "replay: the head did not move", head: fixedHead("a"), wantEvents: 1},
		{name: "no publisher: only audited", head: nil, wantEvents: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeEvents{known: "a"}
			if err := NewApplier(f, tt.head).Handle(ctx, outbox.Job{Payload: push}); err != nil {
				t.Fatal(err)
			}
			if len(f.events) != tt.wantEvents {
				t.Fatalf("events = %+v", f.events)
			}
			if tt.wantEvents == 2 {
				last := f.events[1]
				if last.BeforeSHA != tt.wantLast.BeforeSHA || last.HeadSHA != tt.wantLast.HeadSHA || last.Event != requests.EventNewCommit {
					t.Fatalf("resync event = %+v", last)
				}
			}
		})
	}
}

func (f *fakeEvents) RecordWorkflowHint(_ context.Context, h requests.WorkflowHint) (bool, error) {
	f.hints = append(f.hints, h)
	return true, nil
}

func TestApplier(t *testing.T) {
	f := &fakeEvents{}
	a := NewApplier(f, nil)
	ctx := context.Background()
	tr, _ := json.Marshal(Command{Kind: KindTransition, RequestShortID: "req-7k3p9x", PRNumber: 812,
		RequestEvent: requests.EventPRClosed, DeliveryID: "d"})
	hint, _ := json.Marshal(Command{Kind: KindHint, RequestShortID: "req-7k3p9x", RunID: 5})
	if err := a.Handle(ctx, outbox.Job{Payload: tr}); err != nil {
		t.Fatal(err)
	}
	if err := a.Handle(ctx, outbox.Job{Payload: hint}); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 1 || f.events[0].Event != requests.EventPRClosed || f.events[0].PRNumber != 812 || len(f.hints) != 1 {
		t.Fatalf("events %+v hints %+v", f.events, f.hints)
	}
	for _, bad := range []string{`{`, `{"kind":"other"}`} {
		if err := a.Handle(ctx, outbox.Job{Payload: json.RawMessage(bad)}); !errors.Is(err, outbox.ErrPermanent) {
			t.Fatalf("payload %s: err = %v", bad, err)
		}
	}
}
