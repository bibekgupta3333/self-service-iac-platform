// Package audit implements the append-only, hash-chained audit log.
//
// The chain logic (canonical encoding, hashing, verification) is pure and lives in
// this file. Writer and Verifier are the PostgreSQL adapters.
package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Actor types allowed by the audit_events.actor_type CHECK constraint.
const (
	ActorUser          = "user"
	ActorSystem        = "system"
	ActorGitHubActions = "github_actions"
	ActorGitHubWebhook = "github_webhook"
)

// HashSize is the length of a chain hash (sha256).
const HashSize = sha256.Size

// GenesisHash is the prev_hash of the first event in the chain.
var GenesisHash = make([]byte, HashSize)

// timeLayout fixes microsecond precision so the canonical form survives timestamptz.
const timeLayout = "2006-01-02T15:04:05.000000Z"

// Event is one audit fact before it is sealed into the chain.
type Event struct {
	ID            uuid.UUID
	OccurredAt    time.Time
	ActorType     string
	ActorID       string
	Action        string // e.g. request.created
	SubjectType   string // request | stack | module_version | ...
	SubjectID     string
	TeamID        *uuid.UUID
	CorrelationID string
	Payload       json.RawMessage
}

// Record is a sealed event as stored in audit_events.
type Record struct {
	Seq int64
	Event
	PrevHash []byte
	Hash     []byte
}

// canonicalEvent fixes field order (alphabetical) and representation.
type canonicalEvent struct {
	Action        string `json:"action"`
	ActorID       string `json:"actor_id"`
	ActorType     string `json:"actor_type"`
	CorrelationID string `json:"correlation_id"`
	ID            string `json:"id"`
	OccurredAt    string `json:"occurred_at"`
	Payload       any    `json:"payload"`
	SubjectID     string `json:"subject_id"`
	SubjectType   string `json:"subject_type"`
	TeamID        string `json:"team_id"`
}

// CanonicalJSON returns the deterministic encoding of e that is hashed into the chain.
//
// The payload is decoded and re-encoded, so key order, whitespace and number spelling
// (1e2 vs 100) do not matter: the same logical payload read back from jsonb hashes the same.
func CanonicalJSON(e Event) ([]byte, error) {
	var payload any = map[string]any{}
	if len(bytes.TrimSpace(e.Payload)) > 0 {
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			return nil, fmt.Errorf("canonical payload: %w", err)
		}
	}
	team := ""
	if e.TeamID != nil {
		team = e.TeamID.String()
	}
	c := canonicalEvent{
		Action:        e.Action,
		ActorID:       e.ActorID,
		ActorType:     e.ActorType,
		CorrelationID: e.CorrelationID,
		ID:            e.ID.String(),
		OccurredAt:    e.OccurredAt.UTC().Format(timeLayout),
		Payload:       payload,
		SubjectID:     e.SubjectID,
		SubjectType:   e.SubjectType,
		TeamID:        team,
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("canonical event: %w", err)
	}
	return b, nil
}

// ChainHash computes sha256(prevHash || CanonicalJSON(e)).
func ChainHash(prevHash []byte, e Event) ([]byte, error) {
	if len(prevHash) != HashSize {
		return nil, fmt.Errorf("prev hash: want %d bytes, got %d", HashSize, len(prevHash))
	}
	canon, err := CanonicalJSON(e)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(prevHash)
	h.Write(canon)
	return h.Sum(nil), nil
}

// Seal links e to prevHash and returns the record to store (Seq is assigned by the database).
func Seal(prevHash []byte, e Event) (Record, error) {
	hash, err := ChainHash(prevHash, e)
	if err != nil {
		return Record{}, err
	}
	return Record{Event: e, PrevHash: bytes.Clone(prevHash), Hash: hash}, nil
}

// VerifyResult reports the outcome of a chain verification.
type VerifyResult struct {
	Valid       bool   `json:"valid"`
	Checked     int    `json:"checked"`
	FirstBadSeq *int64 `json:"first_bad_seq"`
}

// VerifyChain checks that records (in seq order) form a valid chain starting at anchor,
// the hash of the record preceding the range (GenesisHash for the start of the log).
func VerifyChain(anchor []byte, records []Record) VerifyResult {
	prev := anchor
	for i, r := range records {
		want, err := ChainHash(prev, r.Event)
		if err != nil || !bytes.Equal(r.PrevHash, prev) || !bytes.Equal(r.Hash, want) {
			seq := r.Seq
			return VerifyResult{Valid: false, Checked: i, FirstBadSeq: &seq}
		}
		prev = r.Hash
	}
	return VerifyResult{Valid: true, Checked: len(records)}
}
