package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// RequestEventService is the status feed consumed by RequestEventsHandler (requests.Service).
// Both calls authorize the caller for the request.
type RequestEventService interface {
	StatusSnapshot(ctx context.Context, by identity.Principal, id uuid.UUID) (requests.StatusSnapshot, error)
	StatusEventsAfter(ctx context.Context, by identity.Principal, id uuid.UUID, after int64, limit int) ([]requests.StatusEvent, error)
}

// StreamOptions times an event stream.
type StreamOptions struct {
	Poll        time.Duration // how often the feed is read
	Heartbeat   time.Duration // idle interval between comment lines that keep proxies from closing the stream
	MaxLifetime time.Duration // the stream ends after this long; clients resume with Last-Event-ID
	WriteWindow time.Duration // deadline for one write, replacing the server-wide WriteTimeout
	MaxPerUser  int           // concurrent streams one user may hold (default 5)
	MaxTotal    int           // concurrent streams in the process (default 500)
	// Stop, when closed, ends every open stream at once. The server passes its shutdown
	// signal, so http.Server.Shutdown does not wait out streams of up to MaxLifetime.
	Stop <-chan struct{}
}

// DefaultStreamOptions are the production timings.
func DefaultStreamOptions() StreamOptions {
	return StreamOptions{Poll: 2 * time.Second, Heartbeat: 15 * time.Second, MaxLifetime: 5 * time.Minute,
		WriteWindow: 10 * time.Second, MaxPerUser: 5, MaxTotal: 500}
}

// feedBatch bounds how many events one poll reads.
const feedBatch = 100

// RequestEventsHandler serves GET /v1/requests/{id}/events as Server-Sent Events.
//
// The feed is the request's status changes in the audit log, so no change is lost between
// polls and every event id (the audit seq) is a resume point: a reconnecting EventSource
// sends it back in Last-Event-ID. A first connection starts with a "snapshot" event of the
// current status. The stream ends with an "end" event once the request is terminal, or
// without one when MaxLifetime passes, and the client should reconnect only in that case.
type RequestEventsHandler struct {
	svc   RequestEventService
	opts  StreamOptions
	slots *streamSlots
}

// streamSlots counts open streams per user and in total. Each stream costs the database a
// few queries every poll, so both are capped; the caller answers 429 when a slot is refused.
type streamSlots struct {
	mu       sync.Mutex
	perUser  map[uuid.UUID]int
	total    int
	maxUser  int
	maxTotal int
}

// acquire takes a slot for user and returns its release function; ok is false when the
// user or the process is at its cap.
func (s *streamSlots) acquire(user uuid.UUID) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total >= s.maxTotal || s.perUser[user] >= s.maxUser {
		return nil, false
	}
	s.total++
	s.perUser[user]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.total--
			if s.perUser[user]--; s.perUser[user] <= 0 {
				delete(s.perUser, user)
			}
		})
	}, true
}

// NewRequestEventsHandler returns a RequestEventsHandler; zero option fields take the defaults.
func NewRequestEventsHandler(svc RequestEventService, opts StreamOptions) *RequestEventsHandler {
	d := DefaultStreamOptions()
	if opts.Poll <= 0 {
		opts.Poll = d.Poll
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = d.Heartbeat
	}
	if opts.MaxLifetime <= 0 {
		opts.MaxLifetime = d.MaxLifetime
	}
	if opts.WriteWindow <= 0 {
		opts.WriteWindow = d.WriteWindow
	}
	if opts.MaxPerUser <= 0 {
		opts.MaxPerUser = d.MaxPerUser
	}
	if opts.MaxTotal <= 0 {
		opts.MaxTotal = d.MaxTotal
	}
	return &RequestEventsHandler{svc: svc, opts: opts,
		slots: &streamSlots{perUser: map[uuid.UUID]int{}, maxUser: opts.MaxPerUser, maxTotal: opts.MaxTotal}}
}

// Routes implements RouteRegistrar.
func (h *RequestEventsHandler) Routes(r chi.Router) { r.Get("/requests/{id}/events", h.stream) }

type snapshotEvent struct {
	RequestID uuid.UUID       `json:"request_id"`
	Status    requests.Status `json:"status"`
	Kind      requests.Kind   `json:"kind"`
	UpdatedAt time.Time       `json:"updated_at"`
	Seq       int64           `json:"seq"`
}

type statusEvent struct {
	RequestID  uuid.UUID       `json:"request_id"`
	Seq        int64           `json:"seq"`
	OccurredAt time.Time       `json:"occurred_at"`
	Status     requests.Status `json:"status"`
	From       requests.Status `json:"from,omitempty"`
	Event      requests.Event  `json:"event,omitempty"`
}

type endEvent struct {
	RequestID uuid.UUID       `json:"request_id"`
	Status    requests.Status `json:"status"`
}

// writeProblem answers with a problem+json body for a status that has no apperr kind.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	p := Problem{Type: ErrorTypeBase + code, Title: title, Status: status, Code: code, Detail: detail,
		Instance: r.URL.Path, RequestID: RequestIDFrom(r.Context())}
	observability.Logger(r.Context()).InfoContext(r.Context(), "request rejected", "code", code)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// ParseLastEventID reads a Last-Event-ID header: a non-negative audit seq. Anything else
// (empty, malformed, negative) means "no position". Pure.
func ParseLastEventID(v string) (int64, bool) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// sseWriter writes SSE frames, extending the write deadline for each one so the stream
// outlives the server's WriteTimeout, and flushing after every frame.
type sseWriter struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	window time.Duration
}

func (s sseWriter) frame(id int64, event string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", event, err)
	}
	var msg string
	if id > 0 {
		msg = fmt.Sprintf("id: %d\n", id)
	}
	return s.write(fmt.Sprintf("%sevent: %s\ndata: %s\n\n", msg, event, body))
}

func (s sseWriter) comment(text string) error { return s.write(": " + text + "\n\n") }

func (s sseWriter) write(frame string) error {
	if err := s.rc.SetWriteDeadline(time.Now().Add(s.window)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := fmt.Fprint(s.w, frame); err != nil {
		return err
	}
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

func (h *RequestEventsHandler) stream(w http.ResponseWriter, r *http.Request) {
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, apperr.NotFound("request %q not found", chi.URLParam(r, "id")))
		return
	}
	ctx := r.Context()

	release, ok := h.slots.acquire(p.UserID)
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeProblem(w, r, http.StatusTooManyRequests, "too_many_streams", "Too many open streams",
			"close an open request stream before opening another")
		return
	}
	defer release()

	// Everything that can fail with a problem response happens before the first byte.
	var (
		after    int64
		snapshot *requests.StatusSnapshot
		backlog  []requests.StatusEvent
	)
	var endNow *requests.Status // set when a resumed stream has nothing left to say: the request is already terminal
	if last, ok := ParseLastEventID(r.Header.Get("Last-Event-ID")); ok {
		after = last
		backlog, err = h.svc.StatusEventsAfter(ctx, p, id, after, feedBatch)
		if err == nil && len(backlog) == 0 {
			// A client that reconnects after the terminal frame (or whose connection dropped
			// before `end`) must still be told the stream is over, not wait for heartbeats.
			var snap requests.StatusSnapshot
			if snap, err = h.svc.StatusSnapshot(ctx, p, id); err == nil && snap.Request.Status.IsTerminal() {
				// The terminal change may have committed after the first read: replay it if so.
				if backlog, err = h.svc.StatusEventsAfter(ctx, p, id, after, feedBatch); err == nil && len(backlog) == 0 {
					endNow = &snap.Request.Status
				}
			}
		}
	} else {
		var snap requests.StatusSnapshot
		snap, err = h.svc.StatusSnapshot(ctx, p, id)
		snapshot, after = &snap, snap.Seq
	}
	if err != nil {
		writeError(w, r, err)
		return
	}

	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream; charset=utf-8")
	hd.Set("Cache-Control", "no-cache, no-transform")
	hd.Set("X-Accel-Buffering", "no") // reverse proxies must not buffer the stream
	w.WriteHeader(http.StatusOK)
	out := sseWriter{w: w, rc: http.NewResponseController(w), window: h.opts.WriteWindow}
	log := observability.Logger(ctx)

	if snapshot != nil {
		rq := snapshot.Request
		if out.frame(snapshot.Seq, "snapshot", snapshotEvent{RequestID: rq.ID, Status: rq.Status, Kind: rq.Kind,
			UpdatedAt: rq.UpdatedAt.UTC(), Seq: snapshot.Seq}) != nil {
			return
		}
		if rq.Status.IsTerminal() {
			_ = out.frame(0, "end", endEvent{RequestID: rq.ID, Status: rq.Status})
			return
		}
	}

	if endNow != nil {
		_ = out.frame(0, "end", endEvent{RequestID: id, Status: *endNow})
		return
	}

	deadline := time.NewTimer(h.opts.MaxLifetime)
	poll := time.NewTicker(h.opts.Poll)
	beat := time.NewTimer(h.opts.Heartbeat)
	defer func() { deadline.Stop(); poll.Stop(); beat.Stop() }()

	// emit sends events and reports whether the stream is over (terminal status or write error).
	emit := func(events []requests.StatusEvent) (done bool) {
		for _, e := range events {
			if out.frame(e.Seq, "status", statusEvent{RequestID: id, Seq: e.Seq, OccurredAt: e.At.UTC(), Status: e.Status,
				From: e.From, Event: e.Event}) != nil {
				return true
			}
			after = e.Seq
			beat.Reset(h.opts.Heartbeat)
			if e.Status.IsTerminal() {
				_ = out.frame(0, "end", endEvent{RequestID: id, Status: e.Status})
				return true
			}
		}
		return false
	}
	if emit(backlog) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.opts.Stop:
			return // server shutdown: clients reconnect with Last-Event-ID to the next instance
		case <-deadline.C:
			return
		case <-beat.C:
			if out.comment("heartbeat") != nil {
				return
			}
			beat.Reset(h.opts.Heartbeat)
		case <-poll.C:
			events, err := h.svc.StatusEventsAfter(ctx, p, id, after, feedBatch)
			if err != nil {
				if ctx.Err() == nil {
					log.WarnContext(ctx, "status stream ended", "request", id, "error", err)
				}
				return // the client reconnects with Last-Event-ID and meets the error as a response
			}
			if emit(events) {
				return
			}
		}
	}
}
