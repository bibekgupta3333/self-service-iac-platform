package httpapi

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

var feedRequest = uuid.MustParse("0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90")

// fakeFeed serves a request whose status changes are appended by the test.
type fakeFeed struct {
	mu       sync.Mutex
	status   requests.Status
	events   []requests.StatusEvent // seq 1..n
	polls    atomic.Int64
	deny     bool
	failPoll bool
}

func (f *fakeFeed) add(status requests.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	from := f.status
	f.status = status
	f.events = append(f.events, requests.StatusEvent{Seq: int64(len(f.events) + 1), At: stackAt, Status: status, From: from})
}

func (f *fakeFeed) StatusSnapshot(_ context.Context, _ identity.Principal, id uuid.UUID) (requests.StatusSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny || id != feedRequest {
		return requests.StatusSnapshot{}, apperr.NotFound("request %s not found", id)
	}
	return requests.StatusSnapshot{Seq: int64(len(f.events)), Request: &requests.Request{ID: id, Kind: requests.KindUpdate,
		Status: f.status, UpdatedAt: stackAt}}, nil
}

func (f *fakeFeed) StatusEventsAfter(_ context.Context, _ identity.Principal, id uuid.UUID, after int64, limit int) ([]requests.StatusEvent, error) {
	f.polls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny || id != feedRequest {
		return nil, apperr.NotFound("request %s not found", id)
	}
	if f.failPoll {
		return nil, io.ErrUnexpectedEOF
	}
	var out []requests.StatusEvent
	for _, e := range f.events {
		if e.Seq > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

var fastStream = StreamOptions{Poll: 10 * time.Millisecond, Heartbeat: 40 * time.Millisecond, MaxLifetime: 2 * time.Second,
	WriteWindow: time.Second}

func eventsServer(t *testing.T, feed RequestEventService, opts StreamOptions) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newTestRouter(nil, okPinger{}, NewRequestEventsHandler(feed, opts)))
	t.Cleanup(srv.Close)
	return srv
}

// frame is one parsed SSE message; comment frames have only Comment set.
type frame struct{ ID, Event, Data, Comment string }

// readFrames parses the stream until it ends or max frames were read.
func readFrames(t *testing.T, body io.Reader, max int) []frame {
	t.Helper()
	var (
		out []frame
		cur frame
		any bool
	)
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if any {
				out, cur, any = append(out, cur), frame{}, false
				if len(out) >= max {
					return out
				}
			}
		case strings.HasPrefix(line, ": "):
			cur.Comment, any = line[2:], true
		case strings.HasPrefix(line, "id: "):
			cur.ID, any = line[4:], true
		case strings.HasPrefix(line, "event: "):
			cur.Event, any = line[7:], true
		case strings.HasPrefix(line, "data: "):
			cur.Data, any = line[6:], true
		}
	}
	return out
}

func open(t *testing.T, srv *httptest.Server, path string, headers ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestEventStreamSnapshotThenChanges(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	srv := eventsServer(t, feed, fastStream)
	path := "/v1/requests/" + feedRequest.String() + "/events"

	resp := open(t, srv, path)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
		resp.Header.Get("Cache-Control") == "" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers = %d %v", resp.StatusCode, resp.Header)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		feed.add(requests.StatusPROpen)
		feed.add(requests.StatusValidating)
		time.Sleep(60 * time.Millisecond)
		feed.add(requests.StatusFailed)
	}()
	var frames []frame
	for _, f := range readFrames(t, resp.Body, 100) {
		if f.Comment == "" {
			frames = append(frames, f)
		}
	}
	want := []struct{ id, event, status string }{
		{"1", "snapshot", "draft"}, {"2", "status", "pr_open"}, {"3", "status", "validating"}, {"4", "status", "failed"}, {"", "end", "failed"},
	}
	if len(frames) != len(want) {
		t.Fatalf("frames = %+v", frames)
	}
	for i, w := range want {
		if frames[i].ID != w.id || frames[i].Event != w.event || !strings.Contains(frames[i].Data, `"status":"`+w.status+`"`) {
			t.Errorf("frame %d = %+v, want %+v", i, frames[i], w)
		}
	}
	if !strings.Contains(frames[1].Data, `"from":"draft"`) || !strings.Contains(frames[1].Data, feedRequest.String()) {
		t.Errorf("status data = %s", frames[1].Data)
	}
}

func TestEventStreamResumesFromLastEventID(t *testing.T) {
	feed := &fakeFeed{}
	for _, s := range []requests.Status{requests.StatusDraft, requests.StatusPROpen, requests.StatusValidating, requests.StatusPlanReady} {
		feed.add(s)
	}
	srv := eventsServer(t, feed, fastStream)
	path := "/v1/requests/" + feedRequest.String() + "/events"

	frames := readFrames(t, open(t, srv, path, "Last-Event-ID", "2").Body, 2)
	if len(frames) != 2 || frames[0].ID != "3" || frames[0].Event != "status" || frames[1].ID != "4" {
		t.Fatalf("resume after 2 = %+v (no snapshot, only newer events)", frames)
	}
	// A position at the head waits for the next change.
	go func() { time.Sleep(50 * time.Millisecond); feed.add(requests.StatusRejected) }()
	got := readFrames(t, open(t, srv, path, "Last-Event-ID", "4").Body, 1000)
	var events []frame
	for _, f := range got {
		if f.Comment == "" {
			events = append(events, f)
		}
	}
	if len(events) == 0 || events[0].ID != "5" || events[0].Event != "status" || events[len(events)-1].Event != "end" {
		t.Fatalf("resume at the head = %+v", got)
	}
	// A malformed position is no position: the stream starts with a snapshot.
	for _, bad := range []string{"abc", "-3", "1.5", ""} {
		if got := readFrames(t, open(t, srv, path, "Last-Event-ID", bad).Body, 1); len(got) != 1 || got[0].Event != "snapshot" {
			t.Errorf("Last-Event-ID %q: %+v", bad, got)
		}
	}
}

func TestEventStreamOfATerminalRequestEndsAtOnce(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	feed.add(requests.StatusApplied)
	srv := eventsServer(t, feed, fastStream)
	frames := readFrames(t, open(t, srv, "/v1/requests/"+feedRequest.String()+"/events").Body, 100)
	if len(frames) != 2 || frames[0].Event != "snapshot" || frames[1].Event != "end" {
		t.Fatalf("frames = %+v", frames)
	}
}

// A browser reconnects with the id of the terminal frame it already saw: it must be told
// the stream is over at once, not left on heartbeats for the whole lifetime.
func TestEventStreamResumeAtTheTerminalSeqEndsAtOnce(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	feed.add(requests.StatusRejected)
	srv := eventsServer(t, feed, StreamOptions{Poll: time.Hour, Heartbeat: time.Hour, MaxLifetime: time.Hour})
	path := "/v1/requests/" + feedRequest.String() + "/events"

	for _, last := range []string{"2", "9"} { // the terminal seq, and one past it
		frames := readFrames(t, open(t, srv, path, "Last-Event-ID", last).Body, 100) // returns when the server closes
		if len(frames) != 1 || frames[0].Event != "end" || !strings.Contains(frames[0].Data, `"status":"rejected"`) {
			t.Fatalf("Last-Event-ID %s: frames = %+v", last, frames)
		}
	}
	// Resuming before the terminal seq still delivers the terminal change, then end.
	frames := readFrames(t, open(t, srv, path, "Last-Event-ID", "1").Body, 100)
	if len(frames) != 2 || frames[0].ID != "2" || frames[0].Event != "status" || frames[1].Event != "end" {
		t.Fatalf("Last-Event-ID 1: frames = %+v", frames)
	}
}

func TestEventStreamHeartbeatAndLifetime(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	opts := StreamOptions{Poll: 10 * time.Millisecond, Heartbeat: 30 * time.Millisecond, MaxLifetime: 250 * time.Millisecond}
	srv := eventsServer(t, feed, opts)

	start := time.Now()
	frames := readFrames(t, open(t, srv, "/v1/requests/"+feedRequest.String()+"/events").Body, 1000)
	elapsed := time.Since(start)
	beats := 0
	for _, f := range frames {
		if f.Comment == "heartbeat" {
			beats++
		}
	}
	if beats < 3 {
		t.Fatalf("an idle stream must send heartbeat comments, got %d in %+v", beats, frames)
	}
	if elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("the stream must end at MaxLifetime, took %s", elapsed)
	}
	if frames[0].Event != "snapshot" {
		t.Fatalf("frames[0] = %+v", frames[0])
	}
	for _, f := range frames {
		if f.Event == "end" {
			t.Fatal("hitting the lifetime is not an end: the client reconnects")
		}
	}
}

func TestEventStreamAuthorization(t *testing.T) {
	feed := &fakeFeed{deny: true}
	feed.add(requests.StatusDraft)
	srv := eventsServer(t, feed, fastStream)
	for _, header := range [][]string{nil, {"Last-Event-ID", "1"}} {
		resp := open(t, srv, "/v1/requests/"+feedRequest.String()+"/events", header...)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 404 || resp.Header.Get("Content-Type") != "application/problem+json" || strings.Contains(string(body), "event:") {
			t.Fatalf("denied caller (%v): %d %s %s", header, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}
	if resp := open(t, srv, "/v1/requests/not-a-uuid/events"); resp.StatusCode != 404 {
		t.Fatalf("bad id: %d", resp.StatusCode)
	}
	// Access is re-checked while the stream is open: when it is revoked the stream ends.
	feed.deny = false
	resp := open(t, srv, "/v1/requests/"+feedRequest.String()+"/events")
	go func() { time.Sleep(50 * time.Millisecond); feed.mu.Lock(); feed.deny = true; feed.mu.Unlock() }()
	frames := readFrames(t, resp.Body, 1000)
	for _, f := range frames {
		if f.Event == "status" || f.Event == "end" {
			t.Fatalf("a revoked caller got %+v", f)
		}
	}
}

func TestEventStreamStopsPollingWhenTheClientLeaves(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	srv := eventsServer(t, feed, fastStream)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/requests/"+feedRequest.String()+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFrames(t, resp.Body, 1); len(got) != 1 {
		t.Fatalf("frames = %+v", got)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
	settled := feed.polls.Load()
	time.Sleep(150 * time.Millisecond)
	if now := feed.polls.Load(); now != settled {
		t.Fatalf("the handler kept polling after the client left: %d -> %d", settled, now)
	}
}

func TestEventStreamEndsOnFeedErrors(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	srv := eventsServer(t, feed, fastStream)
	resp := open(t, srv, "/v1/requests/"+feedRequest.String()+"/events")
	feed.mu.Lock()
	feed.failPoll = true
	feed.mu.Unlock()
	frames := readFrames(t, resp.Body, 1000) // returns when the server closes the stream
	if len(frames) == 0 || frames[0].Event != "snapshot" {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestParseLastEventID(t *testing.T) {
	for v, want := range map[string]struct {
		n  int64
		ok bool
	}{"0": {0, true}, "42": {42, true}, "": {}, "-1": {}, "x": {}, "9223372036854775808": {}, " 5": {}} {
		if n, ok := ParseLastEventID(v); n != want.n || ok != want.ok {
			t.Errorf("ParseLastEventID(%q) = %d, %v", v, n, ok)
		}
	}
}
