package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

type downStore struct{}

func (downStore) Claim(context.Context, string, time.Time) error {
	return errors.New("connection refused")
}

// jtiVerifier is fakeTokenVerifier plus a jti derived from the raw token and an exp,
// so presenting the same token twice is a replay.
type jtiVerifier struct{}

func (jtiVerifier) Verify(ctx context.Context, raw string) (auth.Claims, error) {
	c, err := fakeTokenVerifier{}.Verify(ctx, raw)
	if err != nil {
		return c, err
	}
	c.ID = "jti-" + raw
	c.ExpiresAt = jwt.NewNumericDate(time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC))
	return c, nil
}

func replayRouter(store auth.ReplayStore) http.Handler {
	return NewRouter(Deps{Policies: testPolicies(), Roles: noRoles{}, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Auth: DevAuthenticator{Principal: devPrincipal},
		Internal: AnyAuthenticator{NewOIDCAuthenticator(auth.NewReplayGuard(jtiVerifier{}, store, 0)),
			NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:            okPinger{},
		InternalHandlers: []RouteRegistrar{NewBudgetContextHandler(&fakeBudgetContext{})},
	})
}

const budgetURL = "/v1/internal/budget-context?stack_path=environments/development/payments/orders"

func TestReplayedTokenIs401(t *testing.T) {
	h := replayRouter(auth.NewMemoryReplayCache(10, platform.FixedClock{T: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}))
	if rec := do(t, h, "GET", budgetURL, "", "Authorization", "Bearer apply.jwt.token"); rec.Code != 200 {
		t.Fatalf("first use = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", budgetURL, "", "Authorization", "Bearer apply.jwt.token"); rec.Code != 401 {
		t.Fatalf("replay = %d %s, want 401", rec.Code, rec.Body)
	}
}

func TestReplayStoreOutageIs503(t *testing.T) {
	h := replayRouter(downStore{})
	rec := do(t, h, "GET", budgetURL, "", "Authorization", "Bearer apply.jwt.token")
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" ||
		!strings.Contains(rec.Body.String(), `"code":"replay_store_unavailable"`) {
		t.Fatalf("got %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Fatal("the store error must not leak to the client")
	}
	// The shared token does not depend on the replay store.
	if rec := do(t, h, "GET", budgetURL, "", "Authorization", "Bearer "+testInternalToken); rec.Code != 200 {
		t.Fatalf("shared token = %d", rec.Code)
	}
}
