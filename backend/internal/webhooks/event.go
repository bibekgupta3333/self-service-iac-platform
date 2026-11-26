// Package webhooks ingests GitHub App webhooks (docs/03 §6.10).
//
// The HTTP handler verifies the signature (VerifySignature), maps the event to a domain
// command (Map), and the Ingester records the delivery id and enqueues the command in one
// transaction, so a redelivery is acknowledged without a second job. The Applier is the
// outbox handler that applies commands to requests idempotently. Signature checks and
// event mapping are pure functions.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// Errors returned by VerifySignature.
var (
	ErrNoSecret         = errors.New("webhook secret is not configured")
	ErrBadSignature     = errors.New("invalid webhook signature")
	ErrMalformedPayload = errors.New("malformed webhook payload")
)

// Sign returns the X-Hub-Signature-256 value for body. Pure.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature checks X-Hub-Signature-256 against an HMAC-SHA256 of body with a
// constant-time compare. An empty secret fails closed. Pure.
func VerifySignature(secret, body []byte, header string) error {
	if len(secret) == 0 {
		return ErrNoSecret
	}
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return ErrBadSignature
	}
	sig, err := hex.DecodeString(got)
	if err != nil || len(sig) != sha256.Size {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return ErrBadSignature
	}
	return nil
}

// CommandKind says what the worker does with a command.
type CommandKind string

// Command kinds.
const (
	KindTransition CommandKind = "transition" // fire a request event
	KindHint       CommandKind = "hint"       // record a workflow run against the request
)

// Command is a webhook mapped to a domain action on one request (the outbox payload).
type Command struct {
	Kind           CommandKind    `json:"kind"`
	DeliveryID     string         `json:"delivery_id"`
	Event          string         `json:"event"`  // X-GitHub-Event
	Action         string         `json:"action"` // payload action
	Repository     string         `json:"repository"`
	RequestShortID string         `json:"request_short_id"`
	PRNumber       int            `json:"pr_number,omitempty"`
	RequestEvent   requests.Event `json:"request_event,omitempty"`
	MergeCommitSHA string         `json:"merge_commit_sha,omitempty"`
	Workflow       string         `json:"workflow,omitempty"`
	RunID          int64          `json:"run_id,omitempty"`
	Conclusion     string         `json:"conclusion,omitempty"`
	HeadSHA        string         `json:"head_sha,omitempty"`
	URL            string         `json:"url,omitempty"`
}

type repoField struct {
	FullName string `json:"full_name"`
}

type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Head           struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository repoField `json:"repository"`
}

type workflowRunPayload struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		ID         int64  `json:"id"`
		Path       string `json:"path"`
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
		Conclusion string `json:"conclusion"`
		HTMLURL    string `json:"html_url"`
	} `json:"workflow_run"`
	Repository repoField `json:"repository"`
}

// Map turns a verified webhook into a command, or nil when the event needs no action
// (unknown events, other actions, branches the platform did not create). Mapping:
//
//	pull_request closed, merged     → pr_merged (approved → applying)
//	pull_request closed, not merged → pr_closed (→ rejected, lock released)
//	pull_request synchronize        → new_commit
//	workflow_run completed          → hint (audited, no transition)
//
// Pure.
func Map(event, deliveryID string, body []byte) (*Command, error) {
	switch event {
	case "pull_request":
		var p pullRequestPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMalformedPayload, err)
		}
		shortID, ok := publish.ParseBranch(p.PullRequest.Head.Ref)
		if !ok || p.Number <= 0 {
			return nil, nil
		}
		c := &Command{Kind: KindTransition, DeliveryID: deliveryID, Event: event, Action: p.Action,
			Repository: p.Repository.FullName, RequestShortID: shortID, PRNumber: p.Number}
		switch {
		case p.Action == "closed" && p.PullRequest.Merged:
			c.RequestEvent, c.MergeCommitSHA = requests.EventPRMerged, p.PullRequest.MergeCommitSHA
		case p.Action == "closed":
			c.RequestEvent = requests.EventPRClosed
		case p.Action == "synchronize":
			c.RequestEvent = requests.EventNewCommit
		default:
			return nil, nil
		}
		return c, nil
	case "workflow_run":
		var p workflowRunPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrMalformedPayload, err)
		}
		shortID, ok := publish.ParseBranch(p.WorkflowRun.HeadBranch)
		if p.Action != "completed" || !ok {
			return nil, nil
		}
		r := p.WorkflowRun
		return &Command{Kind: KindHint, DeliveryID: deliveryID, Event: event, Action: p.Action,
			Repository: p.Repository.FullName, RequestShortID: shortID, Workflow: r.Path, RunID: r.ID,
			Conclusion: r.Conclusion, HeadSHA: r.HeadSHA, URL: r.HTMLURL}, nil
	}
	return nil, nil
}
