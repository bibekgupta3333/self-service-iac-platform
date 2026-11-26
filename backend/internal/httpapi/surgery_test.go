package httpapi

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/recovery"
)

type fakeSurgery struct {
	by  []identity.Principal
	dup bool
	err error
}

func (f *fakeSurgery) Record(_ context.Context, by identity.Principal, r recovery.SurgeryReport) (recovery.Recorded, error) {
	if f.err != nil {
		return recovery.Recorded{}, f.err
	}
	if errs := recovery.ValidateSurgeryReport(r); len(errs) > 0 {
		return recovery.Recorded{}, apperr.Invalid("validation_failed", "invalid", errs...)
	}
	f.by = append(f.by, by)
	return recovery.Recorded{Duplicate: f.dup}, nil
}

const surgeryBody = `{"stack_path":"environments/development/payments/orders","operation":"state rm",
 "args":["module.queue.aws_sqs_queue.this"],"ticket":"INC-1234","requested_by":"priya-s","dry_run":true,
 "outcome":"succeeded","exit_code":0,"github":{"run_id":11873399021,"run_attempt":2,"head_sha":"e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4"}}`

func TestStateSurgeryRoute(t *testing.T) {
	svc := &fakeSurgery{}
	h := NewRouter(Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Auth: DevAuthenticator{Principal: devPrincipal},
		Internal: AnyAuthenticator{NewOIDCAuthenticator(fakeTokenVerifier{}), NewTokenAuthenticator(testInternalToken, releaseBot)},
		Ready:    okPinger{}, InternalHandlers: []RouteRegistrar{NewSurgeryHandler(svc)}})
	cases := []struct {
		name, auth, body string
		want             int
		code             string
	}{
		{"state-surgery.yml on main", "Bearer surgery.jwt.token", surgeryBody, 201, ""},
		{"apply.yml may not report surgery", "Bearer applydispatch.jwt.token", surgeryBody, 403, "workflow_not_allowed"},
		{"pr-validate may not report surgery", "Bearer prvalidate.jwt.token", surgeryBody, 403, "workflow_not_allowed"},
		{"dev user is not a workflow", "", surgeryBody, 401, ""},
		{"unknown field", "Bearer surgery.jwt.token", `{"stack_path":"x","shell":"rm -rf /"}`, 400, ""},
		{"operation outside the allow-list", "Bearer surgery.jwt.token",
			strings.Replace(surgeryBody, `"state rm"`, `"state push"`, 1), 422, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hdr []string
			if tc.auth != "" {
				hdr = []string{"Authorization", tc.auth}
			}
			rec := do(t, h, "POST", "/v1/internal/state-surgery", tc.body, hdr...)
			if rec.Code != tc.want || (tc.code != "" && !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`)) {
				t.Fatalf("%d %s, want %d %s", rec.Code, rec.Body, tc.want, tc.code)
			}
		})
	}
	if len(svc.by) != 1 || svc.by[0].Workflow == nil || svc.by[0].Workflow.File() != "state-surgery.yml" {
		t.Fatalf("recorded by %+v", svc.by)
	}
	svc.dup = true
	if rec := do(t, h, "POST", "/v1/internal/state-surgery", surgeryBody, "Authorization", "Bearer surgery.jwt.token"); rec.Code != 200 {
		t.Fatalf("duplicate report = %d", rec.Code)
	}
}
