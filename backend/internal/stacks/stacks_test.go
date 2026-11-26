package stacks

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

func TestValidateQuery(t *testing.T) {
	tests := []struct {
		name  string
		in    Query
		ok    bool
		limit int
	}{
		{"empty gets the default page", Query{}, true, DefaultPageSize},
		{"filters", Query{Team: "payments", Environment: "production", Status: requests.StackActive, Limit: 10}, true, 10},
		{"limit is clamped", Query{Limit: 10_000}, true, MaxPageSize},
		{"negative limit is the default", Query{Limit: -3}, true, DefaultPageSize},
		{"bad team", Query{Team: "Payments;drop"}, false, 0},
		{"unknown environment", Query{Environment: "prod"}, false, 0},
		{"unknown status", Query{Status: "gone"}, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateQuery(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v", err)
			}
			if err != nil && !apperr.IsKind(err, apperr.KindInvalid) {
				t.Fatalf("want a validation error, got %v", err)
			}
			if tt.ok && got.Limit != tt.limit {
				t.Fatalf("limit = %d, want %d", got.Limit, tt.limit)
			}
		})
	}
}

type fakeRepo struct {
	rows    []Stack
	filters []Filter
}

func (f *fakeRepo) List(_ context.Context, flt Filter) ([]Stack, error) {
	f.filters = append(f.filters, flt)
	return f.rows[:min(flt.Limit, len(f.rows))], nil
}

func (f *fakeRepo) Get(_ context.Context, _, id uuid.UUID) (Stack, error) {
	for _, s := range f.rows {
		if s.ID == id {
			return s, nil
		}
	}
	return Stack{}, apperr.NotFound("stack %s not found", id)
}

var user = identity.Principal{UserID: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Type: identity.ActorUser}

func TestListPaginatesAndScopesToTheViewer(t *testing.T) {
	at := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	var rows []Stack
	for i := range 5 {
		rows = append(rows, Stack{ID: uuid.New(), Name: "s", CreatedAt: at.Add(-time.Duration(i) * time.Minute)})
	}
	repo := &fakeRepo{rows: rows}
	svc := NewService(repo)

	page, next, err := svc.List(context.Background(), user, Query{Limit: 2, Team: "payments"})
	if err != nil || len(page) != 2 || next == nil || next.ID != rows[1].ID || !next.CreatedAt.Equal(rows[1].CreatedAt) {
		t.Fatalf("page = %d, next = %+v, err = %v", len(page), next, err)
	}
	if f := repo.filters[0]; f.Viewer != user.UserID || f.Limit != 3 || f.Team != "payments" {
		t.Fatalf("the repository must get the viewer and limit+1: %+v", f)
	}
	if page, next, err = svc.List(context.Background(), user, Query{Limit: 5}); err != nil || len(page) != 5 || next != nil {
		t.Fatalf("last page: %d, %+v, %v", len(page), next, err)
	}
}

func TestOnlyUsersSeeStacks(t *testing.T) {
	svc := NewService(&fakeRepo{})
	machine := identity.Principal{Login: "apply.yml", Type: identity.ActorGitHubActions}
	if _, _, err := svc.List(context.Background(), machine, Query{}); !apperr.IsKind(err, apperr.KindUnauthenticated) {
		t.Fatalf("list: %v", err)
	}
	if _, err := svc.Get(context.Background(), machine, uuid.New()); !apperr.IsKind(err, apperr.KindUnauthenticated) {
		t.Fatalf("get: %v", err)
	}
	if _, err := svc.Get(context.Background(), user, uuid.New()); !apperr.IsKind(err, apperr.KindNotFound) {
		t.Fatalf("an unknown or hidden stack is not found: %v", err)
	}
}
