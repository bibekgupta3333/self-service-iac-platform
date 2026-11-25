package catalog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// fakeRepo is an in-memory Repository.
type fakeRepo struct {
	modules  map[string]Module
	versions []ModuleVersion
	events   []audit.Event
}

func newFakeRepo() *fakeRepo { return &fakeRepo{modules: map[string]Module{}} }

func (f *fakeRepo) ListModules(_ context.Context, flt ModuleFilter) ([]Module, error) {
	var out []Module
	for _, name := range fp.SortedKeys(f.modules) {
		m := f.modules[name]
		if (flt.Category == "" || m.Category == flt.Category) && (flt.Status == "" || m.Status == flt.Status) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeRepo) GetModule(_ context.Context, name string) (Module, error) {
	m, ok := f.modules[name]
	if !ok {
		return Module{}, apperr.NotFound("module %q not found", name)
	}
	return m, nil
}

func (f *fakeRepo) ListVersions(_ context.Context, id *uuid.UUID) ([]ModuleVersion, error) {
	var out []ModuleVersion
	for _, v := range f.versions {
		if id == nil || v.ModuleID == *id {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeRepo) GetVersion(_ context.Context, module string, sv SemVer) (ModuleVersion, error) {
	for _, v := range f.versions {
		if v.ModuleName == module && v.Version == sv {
			return v, nil
		}
	}
	return ModuleVersion{}, apperr.NotFound("no version")
}

func (f *fakeRepo) Index(_ context.Context, m Module, v ModuleVersion, ev audit.Event) (ModuleVersion, bool, error) {
	if existing, ok := f.modules[m.Name]; ok {
		m.ID = existing.ID
	} else {
		m.ID = uuid.New()
	}
	f.modules[m.Name] = m
	for _, cur := range f.versions {
		if cur.ModuleName == v.ModuleName && cur.Version == v.Version {
			if cur.GitSHA != v.GitSHA {
				return ModuleVersion{}, false, apperr.Conflict("version_immutable", "already indexed")
			}
			return cur, false, nil
		}
	}
	v.ID, v.ModuleID = uuid.New(), m.ID
	f.versions = append(f.versions, v)
	f.events = append(f.events, ev)
	return v, true, nil
}

var bot = identity.Principal{Login: "module-release.yml", Type: identity.ActorGitHubActions}

func newTestService(repo Repository) *Service {
	return NewService(repo, platform.FixedClock{T: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)}, "https://example.test/iacp.git")
}

func TestServiceIndexAndRead(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	svc := newTestService(repo)

	for _, ver := range []string{"1.2.0", "1.3.0", "1.10.0"} {
		cmd := validIndexCommand()
		cmd.Version, cmd.GitTag = ver, "modules/sqs/v"+ver
		if _, created, err := svc.Index(ctx, bot, cmd); err != nil || !created {
			t.Fatalf("index %s: created=%v err=%v", ver, created, err)
		}
	}
	if len(repo.events) != 3 || repo.events[0].Action != "module_version.indexed" ||
		repo.events[0].ActorType != "github_actions" || repo.events[0].SubjectID != "modules/sqs/v1.2.0" {
		t.Fatalf("audit events = %+v", repo.events)
	}

	// Re-index same commit: idempotent.
	if _, created, err := svc.Index(ctx, bot, validIndexCommand()); err != nil || created {
		t.Fatalf("re-index: created=%v err=%v", created, err)
	}
	// Same version, different commit: conflict.
	cmd := validIndexCommand()
	cmd.CommitSHA = strings.Repeat("ab", 20)
	if _, _, err := svc.Index(ctx, bot, cmd); !apperr.IsKind(err, apperr.KindConflict) {
		t.Fatalf("want conflict, got %v", err)
	}

	mods, err := svc.ListModules(ctx, ModuleFilter{})
	if err != nil || len(mods) != 1 || mods[0].Latest == nil || mods[0].Latest.Version.String() != "1.10.0" {
		t.Fatalf("ListModules = %+v, %v", mods, err)
	}
	versions, err := svc.ListVersions(ctx, "sqs")
	if err != nil || len(versions) != 3 || versions[0].Version.String() != "1.10.0" || versions[2].Version.String() != "1.2.0" {
		t.Fatalf("ListVersions = %+v, %v", versions, err)
	}
	v, err := svc.GetVersion(ctx, "sqs", "1.3.0")
	if err != nil || svc.SourceAddress(v) != "git::https://example.test/iacp.git//modules/sqs?ref=modules/sqs/v1.3.0" {
		t.Fatalf("GetVersion = %+v, %v", v, err)
	}
}

func TestServiceNotFound(t *testing.T) {
	svc := newTestService(newFakeRepo())
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"module":      func() error { _, err := svc.GetModule(ctx, "nope"); return err },
		"versions":    func() error { _, err := svc.ListVersions(ctx, "nope"); return err },
		"version":     func() error { _, err := svc.GetVersion(ctx, "nope", "1.0.0"); return err },
		"bad version": func() error { _, err := svc.GetVersion(ctx, "nope", "latest"); return err },
	} {
		if err := call(); !apperr.IsKind(err, apperr.KindNotFound) {
			t.Errorf("%s: want not found, got %v", name, err)
		}
	}
}

func TestServiceIndexRejectsInvalid(t *testing.T) {
	repo := newFakeRepo()
	cmd := validIndexCommand()
	cmd.CommitSHA = "HEAD"
	if _, _, err := newTestService(repo).Index(context.Background(), bot, cmd); !apperr.IsKind(err, apperr.KindInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
	if len(repo.versions) != 0 {
		t.Fatal("invalid command must not reach the repository")
	}
}
