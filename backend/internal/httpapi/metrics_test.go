package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
)

type idRoutes struct{}

func (idRoutes) Routes(r chi.Router) {
	r.Get("/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("bug") })
}

const metricsToken = "metrics-token-0123456789abcdef-0123"

func metricsRouter(m *observability.Metrics) http.Handler {
	return NewRouter(Deps{Policies: testPolicies(),
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Ready: okPinger{}, Catalog: NewCatalogHandler(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}),
		Public:  []RouteRegistrar{idRoutes{}},
		Metrics: m, MetricsAccess: NewMetricsAccess(metricsToken),
	})
}

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback scrape: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestMetricsUseRoutePatternsNotRawPaths(t *testing.T) {
	h := metricsRouter(observability.NewMetrics())
	for _, id := range []string{"a1", "b2", "c3"} {
		if rec := do(t, h, "GET", "/v1/things/"+id, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("GET /v1/things/%s = %d", id, rec.Code)
		}
	}
	do(t, h, "GET", "/no/such/path/1", "")
	do(t, h, "GET", "/no/such/path/2", "")
	do(t, h, "GET", "/v1/boom", "")
	body := scrape(t, h)

	for _, want := range []string{
		`iacp_http_requests_total{method="GET",route="/v1/things/{id}",status="204"} 3`,
		`iacp_http_requests_total{method="GET",route="unmatched",status="404"} 2`,
		`iacp_http_requests_total{method="GET",route="/v1/boom",status="500"} 1`,
		`iacp_http_request_duration_seconds_count{method="GET",route="/v1/things/{id}"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\n%s", want, body)
		}
	}
	for _, leak := range []string{"/v1/things/a1", "/no/such/path"} {
		if strings.Contains(body, leak) {
			t.Errorf("raw path %q leaked into a label", leak)
		}
	}
}

func TestMetricsBoundJunkMethods(t *testing.T) {
	h := metricsRouter(observability.NewMetrics())
	for i := range 50 {
		do(t, h, fmt.Sprintf("JUNK%d", i), "/healthz", "")
	}
	body := scrape(t, h)
	series := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "iacp_http_requests_total{") {
			series++
			if strings.Contains(line, "JUNK") {
				t.Errorf("raw method leaked into a label: %s", line)
			}
		}
	}
	if series > 2 { // OTHER plus at most one bucket per status; never one per method
		t.Fatalf("%d request series for 50 junk methods, want a bounded count:\n%s", series, body)
	}
	if !strings.Contains(body, `method="OTHER"`) {
		t.Fatal("junk methods must be recorded as OTHER")
	}
}

func TestMetricsIncludePoolGauges(t *testing.T) {
	m := observability.NewMetrics()
	m.RegisterPool(func() observability.PoolStats {
		return observability.PoolStats{Acquired: 2, Idle: 3, Total: 5, Max: 10}
	})
	body := scrape(t, metricsRouter(m))
	for _, want := range []string{
		`iacp_db_pool_connections{state="acquired"} 2`,
		`iacp_db_pool_connections{state="idle"} 3`,
		`iacp_db_pool_connections{state="total"} 5`,
		`iacp_db_pool_max_connections 10`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestMetricsAccess(t *testing.T) {
	h := metricsRouter(observability.NewMetrics())
	tests := []struct {
		name    string
		remote  string
		headers []string
		want    int
	}{
		{name: "loopback v4", remote: "127.0.0.1:4000", want: 200},
		{name: "loopback v6", remote: "[::1]:4000", want: 200},
		{name: "remote without token", remote: "10.0.3.7:4000", want: 403},
		{name: "remote with token", remote: "10.0.3.7:4000", headers: []string{"Authorization", "Bearer " + metricsToken}, want: 200},
		{name: "remote with wrong token", remote: "10.0.3.7:4000", headers: []string{"Authorization", "Bearer nope"}, want: 403},
		{name: "loopback with a wrong token is not rescued", remote: "127.0.0.1:4000", headers: []string{"Authorization", "Bearer nope"}, want: 403},
		{name: "relayed by a local proxy", remote: "127.0.0.1:4000", headers: []string{"X-Forwarded-For", "203.0.113.9"}, want: 403},
		{name: "forwarded header", remote: "127.0.0.1:4000", headers: []string{"Forwarded", "for=203.0.113.9"}, want: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/metrics", nil)
			req.RemoteAddr = tt.remote
			for i := 0; i+1 < len(tt.headers); i += 2 {
				req.Header.Set(tt.headers[i], tt.headers[i+1])
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("got %d want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.want == 403 && !strings.Contains(rec.Body.String(), "metrics_forbidden") {
				t.Fatalf("403 body = %s", rec.Body.String())
			}
		})
	}
}

func TestMetricsTokenDisabledWhenEmpty(t *testing.T) {
	a := NewMetricsAccess("")
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer x")
	if a.Allow(req) {
		t.Fatal("no token configured: bearer callers must be refused")
	}
	req.Header.Del("Authorization")
	req.RemoteAddr = "127.0.0.1:9"
	if !a.Allow(req) {
		t.Fatal("loopback must still be allowed without a token")
	}
}

func TestNoMetricsRouteWithoutMetrics(t *testing.T) {
	h := NewRouter(Deps{Policies: testPolicies(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth: DevAuthenticator{Principal: devPrincipal}, Ready: okPinger{}})
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestIsDirectLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:80": true, "127.8.9.1:80": true, "[::1]:80": true, "::1": true,
		"10.0.0.1:80": false, "[2001:db8::1]:80": false, "": false, "localhost:80": false,
	} {
		if got := IsDirectLoopback(addr, http.Header{}); got != want {
			t.Errorf("IsDirectLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
