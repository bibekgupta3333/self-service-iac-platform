package httpapi

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
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

func (f *fakeFeed) setDeny(v bool) {
	f.mu.Lock()
	f.deny = v
	f.mu.Unlock()
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

// frameReader reads SSE frames one at a time, so a test can act between them instead of sleeping.
type frameReader struct{ sc *bufio.Scanner }

func newFrameReader(body io.Reader) *frameReader { return &frameReader{sc: bufio.NewScanner(body)} }

// next returns the next frame, or false when the stream ended.
func (r *frameReader) next() (frame, bool) {
	var (
		cur frame
		any bool
	)
	for r.sc.Scan() {
		line := r.sc.Text()
		switch {
		case line == "":
			if any {
				return cur, true
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
	return frame{}, false
}

// data returns the next non-comment frame and fails the test if the stream ended first.
func (r *frameReader) data(t *testing.T) frame {
	t.Helper()
	for {
		f, ok := r.next()
		if !ok {
			t.Fatal("the stream ended before the expected frame")
		}
		if f.Comment == "" {
			return f
		}
	}
}

// untilHeartbeat reads until an idle stream proves it is alive with a heartbeat comment.
func (r *frameReader) untilHeartbeat(t *testing.T) {
	t.Helper()
	for {
		f, ok := r.next()
		if !ok {
			t.Fatal("the stream ended before a heartbeat")
		}
		if f.Comment == "heartbeat" {
			return
		}
	}
}

// rest reads until the server closes the stream.
func (r *frameReader) rest() []frame {
	var out []frame
	for {
		f, ok := r.next()
		if !ok {
			return out
		}
		out = append(out, f)
	}
}

// readFrames parses the stream until it ends or max frames were read.
func readFrames(t *testing.T, body io.Reader, max int) []frame {
	t.Helper()
	var out []frame
	r := newFrameReader(body)
	for len(out) < max {
		f, ok := r.next()
		if !ok {
			break
		}
		out = append(out, f)
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
	fr := newFrameReader(resp.Body)
	frames := []frame{fr.data(t)} // the snapshot; the feed is only extended once the stream is open
	feed.add(requests.StatusPROpen)
	feed.add(requests.StatusValidating)
	frames = append(frames, fr.data(t), fr.data(t))
	feed.add(requests.StatusFailed)
	frames = append(frames, fr.data(t), fr.data(t))
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
	// A position at the head waits for the next change: once a heartbeat shows the stream is
	// open and idle, the change arrives and, being terminal, is followed by end.
	fr := newFrameReader(open(t, srv, path, "Last-Event-ID", "4").Body)
	fr.untilHeartbeat(t)
	feed.add(requests.StatusRejected)
	if f := fr.data(t); f.ID != "5" || f.Event != "status" {
		t.Fatalf("resume at the head: %+v", f)
	}
	if f := fr.data(t); f.Event != "end" {
		t.Fatalf("after the terminal change: %+v", f)
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
	feed := &fakeFeed{}
	feed.setDeny(true)
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
	feed.setDeny(false)
	fr := newFrameReader(open(t, srv, "/v1/requests/"+feedRequest.String()+"/events").Body)
	if f := fr.data(t); f.Event != "snapshot" {
		t.Fatalf("first frame = %+v", f)
	}
	feed.setDeny(true)
	for _, f := range fr.rest() { // returns when the server ends the stream
		if f.Event == "status" || f.Event == "end" {
			t.Fatalf("a revoked caller got %+v", f)
		}
	}
}

func TestEventStreamStopsPollingWhenTheClientLeaves(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	router := newTestRouter(nil, okPinger{}, NewRequestEventsHandler(feed, StreamOptions{Poll: 5 * time.Millisecond,
		Heartbeat: time.Hour, MaxLifetime: time.Hour}))
	exited := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
		close(exited)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/requests/"+feedRequest.String()+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if f := newFrameReader(resp.Body).data(t); f.Event != "snapshot" {
		t.Fatalf("first frame = %+v", f)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler kept running after the client left")
	}
	// Once the handler has returned nothing polls the feed any more.
	settled := feed.polls.Load()
	runtime.Gosched()
	if now := feed.polls.Load(); now != settled {
		t.Fatalf("polled after the handler returned: %d -> %d", settled, now)
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

// Each stream costs the database a few queries per poll, so a user and the process are capped.
func TestEventStreamCaps(t *testing.T) {
	path := "/v1/requests/" + feedRequest.String() + "/events"
	live := StreamOptions{Poll: 5 * time.Millisecond, Heartbeat: time.Hour, MaxLifetime: time.Hour}

	for name, opts := range map[string]StreamOptions{
		"per user": {Poll: live.Poll, Heartbeat: live.Heartbeat, MaxLifetime: live.MaxLifetime, MaxPerUser: 2, MaxTotal: 50},
		"global":   {Poll: live.Poll, Heartbeat: live.Heartbeat, MaxLifetime: live.MaxLifetime, MaxPerUser: 50, MaxTotal: 2},
	} {
		t.Run(name, func(t *testing.T) {
			feed := &fakeFeed{}
			feed.add(requests.StatusDraft)
			srv := eventsServer(t, feed, opts)
			first, second := open(t, srv, path), open(t, srv, path)
			for _, r := range []*http.Response{first, second} {
				if r.StatusCode != 200 || newFrameReader(r.Body).data(t).Event != "snapshot" {
					t.Fatalf("the first two streams must open: %d", r.StatusCode)
				}
			}
			refused := open(t, srv, path)
			body, _ := io.ReadAll(refused.Body)
			if refused.StatusCode != 429 || refused.Header.Get("Retry-After") == "" ||
				refused.Header.Get("Content-Type") != "application/problem+json" || !strings.Contains(string(body), `"code":"too_many_streams"`) {
				t.Fatalf("the third stream: %d %v %s", refused.StatusCode, refused.Header, body)
			}
			// Closing a stream frees its slot once the handler notices.
			first.Body.Close()
			deadline := time.Now().Add(5 * time.Second)
			for {
				r := open(t, srv, path)
				if r.StatusCode == 200 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the slot was not released: %d", r.StatusCode)
				}
				runtime.Gosched()
			}
		})
	}
}

func TestStreamSlots(t *testing.T) {
	s := &streamSlots{perUser: map[uuid.UUID]int{}, maxUser: 2, maxTotal: 3}
	a, b := uuid.New(), uuid.New()
	relA1, ok1 := s.acquire(a)
	_, ok2 := s.acquire(a)
	_, ok3 := s.acquire(a)
	relB, ok4 := s.acquire(b)
	_, ok5 := s.acquire(b) // total is 3 now
	if !ok1 || !ok2 || ok3 || !ok4 || ok5 {
		t.Fatalf("acquire results: %v %v %v %v %v", ok1, ok2, ok3, ok4, ok5)
	}
	relA1()
	relA1() // releasing twice must not free a slot twice
	if s.total != 2 || s.perUser[a] != 1 {
		t.Fatalf("after release: total %d, user a %d", s.total, s.perUser[a])
	}
	relB()
	if _, ok := s.perUser[b]; ok || s.total != 1 {
		t.Fatalf("a user with no streams must not stay in the map: %v total %d", s.perUser, s.total)
	}
}

// Closing Stop (the server's shutdown signal) ends every open stream at once, so
// http.Server.Shutdown does not wait out their lifetime.
func TestEventStreamsEndOnShutdown(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	stop := make(chan struct{})
	srv := eventsServer(t, feed, StreamOptions{Poll: 5 * time.Millisecond, Heartbeat: time.Hour, MaxLifetime: time.Hour, Stop: stop})
	path := "/v1/requests/" + feedRequest.String() + "/events"
	var readers []*frameReader
	for range 3 {
		fr := newFrameReader(open(t, srv, path).Body)
		if f := fr.data(t); f.Event != "snapshot" {
			t.Fatalf("first frame = %+v", f)
		}
		readers = append(readers, fr)
	}
	close(stop)
	done := make(chan struct{})
	go func() {
		for _, fr := range readers {
			fr.rest()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("open streams survived the shutdown signal")
	}
}

// The server bounds reads and writes (newServer: 15 s and 30 s). A stream must outlive the
// write bound because every write sets its own deadline. Here the bound is 200 ms and the
// stream is only judged alive once a heartbeat arrives well past three times that.
func TestEventStreamOutlivesTheServerWriteTimeout(t *testing.T) {
	const bound = 200 * time.Millisecond
	feed := &fakeFeed{}
	feed.add(requests.StatusDraft)
	router := newTestRouter(nil, okPinger{}, NewRequestEventsHandler(feed, StreamOptions{Poll: 10 * time.Millisecond,
		Heartbeat: 25 * time.Millisecond, MaxLifetime: time.Hour, WriteWindow: time.Second}))
	srv := httptest.NewUnstartedServer(router)
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = bound, bound
	srv.Start()
	t.Cleanup(srv.Close)

	start := time.Now()
	fr := newFrameReader(open(t, srv, "/v1/requests/"+feedRequest.String()+"/events").Body)
	for time.Since(start) < 3*bound {
		if _, ok := fr.next(); !ok {
			t.Fatalf("the server cut the stream after %s, inside the %s write bound", time.Since(start), bound)
		}
	}
	// One more heartbeat after the bound has long passed.
	fr.untilHeartbeat(t)
}
