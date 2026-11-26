package requests

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestReplay(t *testing.T) {
	repo := newMemRepo()
	svc := newSvc(repo, fakeVersions{})
	ctx := context.Background()

	// An unused key: nothing found, and nothing reserved (Create may still use it).
	if r, found, err := svc.Replay(ctx, dev, "key-replay-01", "hash-a"); err != nil || found || r != nil {
		t.Fatalf("unused key: %v %v %v", r, found, err)
	}
	cmd := createCmd(validInput(), "key-replay-01")
	created, err := svc.Create(ctx, dev, cmd)
	if err != nil || created.Replayed {
		t.Fatalf("create after a probe: %+v, %v", created, err)
	}

	r, found, err := svc.Replay(ctx, dev, "key-replay-01", cmd.RequestHash)
	if err != nil || !found || r.ID != created.Request.ID {
		t.Fatalf("replay: %v %v %v", r, found, err)
	}
	if _, _, err := svc.Replay(ctx, dev, "key-replay-01", "another-body"); !hasCode(err, "idempotency_key_reuse") {
		t.Fatalf("a different body must be idempotency_key_reuse, got %v", err)
	}
	if _, _, err := svc.Replay(ctx, dev, "short", "x"); !hasCode(err, "bad_request") {
		t.Fatalf("a malformed key must be refused, got %v", err)
	}
}

// RollbackOf is set by the platform only: a request body cannot carry it, and the stored
// spec records it.
func TestRollbackOfIsNotPartOfTheRequestBody(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"kind":"update","rollback_of":"req-abc123"}`)))
	dec.DisallowUnknownFields()
	var in CreateInput
	if err := dec.Decode(&in); err == nil {
		t.Fatal("rollback_of must be an unknown field of POST /v1/requests")
	}
	in = validInput()
	in.RollbackOf = "req-abc123"
	spec, err := json.Marshal(Normalize(in, "us-east-1"))
	if err != nil || !bytes.Contains(spec, []byte(`"rollback_of":"req-abc123"`)) {
		t.Fatalf("spec %s, %v", spec, err)
	}
	in.RollbackOf = ""
	if spec, _ := json.Marshal(Normalize(in, "us-east-1")); bytes.Contains(spec, []byte("rollback_of")) {
		t.Fatalf("an ordinary spec has no rollback_of: %s", spec)
	}
	p := createdPayload(&Request{ShortID: "req-x"}, StackRef{}, CreateInput{Kind: KindUpdate, RollbackOf: "req-abc123"})
	if p["rollback_of"] != "req-abc123" {
		t.Fatalf("audit payload %v", p)
	}
}
