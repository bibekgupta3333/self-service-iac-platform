package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/runs"
)

type recordingObserver struct {
	seen []runs.Report
	err  error
}

func (o *recordingObserver) RunRecorded(_ context.Context, _ identity.Principal, r runs.Report) error {
	o.seen = append(o.seen, r)
	return o.err
}

func TestRunObserversSeeRecordedReports(t *testing.T) {
	obs := &recordingObserver{}
	h := NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal: NewTokenAuthenticator(testInternalToken, releaseBot), Ready: okPinger{},
		PipelineHandlers: []RouteRegistrar{NewRunHandler(&fakeRuns{}).WithObserver(obs)}})
	auth := []string{"Authorization", "Bearer " + testInternalToken}

	if rec := do(t, h, "POST", "/v1/runs", runReport("48213"), auth...); rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(obs.seen) != 1 || obs.seen[0].Stage != "summary" {
		t.Fatalf("observed %+v", obs.seen)
	}
	// A report the service refused is never observed.
	if rec := do(t, h, "POST", "/v1/runs", runReport("1"), auth...); rec.Code != 409 || len(obs.seen) != 1 {
		t.Fatalf("refused report: %d, observed %d", rec.Code, len(obs.seen))
	}
	// An observer failure asks the workflow to send the report again.
	obs.err = errors.New("database unavailable")
	rec := do(t, h, "POST", "/v1/runs", runReport("48213"), auth...)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"run_follow_up_failed"`) ||
		strings.Contains(rec.Body.String(), "database unavailable") {
		t.Fatalf("observer failure: %d %s", rec.Code, rec.Body)
	}
}
