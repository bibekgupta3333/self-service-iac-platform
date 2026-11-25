package audit

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func sampleEvent(n int) Event {
	return Event{
		ID:          uuid.MustParse("00000000-0000-0000-0000-00000000000" + string(rune('0'+n))),
		OccurredAt:  time.Date(2026, 11, 24, 10, 0, n, 512000, time.UTC),
		ActorType:   ActorUser,
		ActorID:     "4b1f2a0e-8a71-4b0c-9c1e-2f1d5e6a7b80",
		Action:      "request.created",
		SubjectType: "request",
		SubjectID:   "req-7k3p9x",
		Payload:     json.RawMessage(`{"kind":"create","n":1}`),
	}
}

func TestCanonicalJSONIsIndependentOfPayloadSpelling(t *testing.T) {
	a := sampleEvent(1)
	b := a
	b.Payload = json.RawMessage(`{ "n" : 1.0e0, "kind": "create" }`)
	ca, err := CanonicalJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := CanonicalJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) {
		t.Fatalf("canonical forms differ:\n%s\n%s", ca, cb)
	}
	want := `{"action":"request.created","actor_id":"4b1f2a0e-8a71-4b0c-9c1e-2f1d5e6a7b80","actor_type":"user","correlation_id":"","id":"00000000-0000-0000-0000-000000000001","occurred_at":"2026-11-24T10:00:01.000512Z","payload":{"kind":"create","n":1},"subject_id":"req-7k3p9x","subject_type":"request","team_id":""}`
	if string(ca) != want {
		t.Fatalf("canonical =\n%s\nwant\n%s", ca, want)
	}
}

func TestChainHashKnownValue(t *testing.T) {
	h, err := ChainHash(GenesisHash, sampleEvent(1))
	if err != nil {
		t.Fatal(err)
	}
	// Pinned so any change to the canonical form is a deliberate, reviewed decision:
	// existing chains stop verifying if it changes.
	const want = "9de36f6763389ce108177d240c0e6dedfc11860dd85802fc5984729341bb2eaf"
	if got := hex.EncodeToString(h); got != want {
		t.Fatalf("hash = %s, want %s", got, want)
	}
}

func buildChain(t *testing.T, n int) []Record {
	t.Helper()
	prev := GenesisHash
	out := make([]Record, 0, n)
	for i := 1; i <= n; i++ {
		r, err := Seal(prev, sampleEvent(i))
		if err != nil {
			t.Fatal(err)
		}
		r.Seq = int64(i)
		out = append(out, r)
		prev = r.Hash
	}
	return out
}

func TestVerifyChain(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func([]Record)
		anchor  func([]Record) []byte
		valid   bool
		badSeq  int64
		checked int
	}{
		{name: "intact", valid: true, checked: 3},
		{name: "explicit genesis anchor", anchor: func([]Record) []byte { return GenesisHash }, valid: true, checked: 3},
		{name: "tampered payload", mutate: func(r []Record) { r[1].Payload = json.RawMessage(`{"kind":"destroy"}`) }, badSeq: 2, checked: 1},
		{name: "tampered action", mutate: func(r []Record) { r[2].Action = "request.approved" }, badSeq: 3, checked: 2},
		{name: "deleted middle", mutate: func(r []Record) { r[1] = r[2] }, badSeq: 3, checked: 1},
		{name: "wrong anchor", anchor: func(r []Record) []byte { return r[0].Hash }, badSeq: 1, checked: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := buildChain(t, 3)
			if tt.mutate != nil {
				tt.mutate(recs)
			}
			anchor := GenesisHash
			if tt.anchor != nil {
				anchor = tt.anchor(recs)
			}
			got := VerifyChain(anchor, recs)
			if got.Valid != tt.valid || got.Checked != tt.checked {
				t.Fatalf("got %+v", got)
			}
			if !tt.valid && (got.FirstBadSeq == nil || *got.FirstBadSeq != tt.badSeq) {
				t.Fatalf("first bad seq = %v, want %d", got.FirstBadSeq, tt.badSeq)
			}
		})
	}
	if got := VerifyChain(GenesisHash, nil); !got.Valid {
		t.Fatal("empty chain should be valid")
	}
}

func TestChainHashRejectsBadPrev(t *testing.T) {
	if _, err := ChainHash([]byte{1, 2}, sampleEvent(1)); err == nil {
		t.Fatal("expected error for short prev hash")
	}
}
