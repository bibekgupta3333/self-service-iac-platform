package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/db/dbtest"
)

// panicRoutes mounts handlers that panic, optionally inside a database transaction.
type panicRoutes struct{ txr *db.TxRunner }

func (p panicRoutes) Routes(r chi.Router) {
	r.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("handler bug") })
	r.Get("/panic-in-tx", func(_ http.ResponseWriter, req *http.Request) {
		_ = p.txr.InTx(req.Context(), func(pgx.Tx) error { panic("bug inside a transaction") })
	})
}

func TestPanicBecomesProblem500(t *testing.T) {
	var logs bytes.Buffer
	h := NewRouter(Deps{Policies: testPolicies(),
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Ready: okPinger{}, Catalog: NewCatalogHandler(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}),
		Public: []RouteRegistrar{panicRoutes{}},
	})
	rec := do(t, h, "GET", "/v1/panic", "")
	if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p["code"] != "internal" || p["detail"] != nil {
		t.Fatalf("problem = %v (%v)", p, err)
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"msg":"http request"`)) || !bytes.Contains(logs.Bytes(), []byte(`"status":500`)) {
		t.Fatalf("panicking request must still get an access log line with status 500:\n%s", logs.String())
	}
}

func TestPanicInsideTransactionReleasesConnection(t *testing.T) {
	pool := dbtest.NewDatabase(t)
	h := newTestRouter(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}, pool, panicRoutes{txr: db.NewTxRunner(pool)})

	for range 3 {
		if rec := do(t, h, "GET", "/v1/panic-in-tx", ""); rec.Code != 500 {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if n := pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("acquired connections = %d after panics, want 0", n)
	}
	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("readyz after panics = %d", rec.Code)
	}
}
