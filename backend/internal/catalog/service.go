package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/platform"
)

// ModuleFilter narrows ListModules.
type ModuleFilter struct {
	Category string
	Status   Status
}

// Repository is the persistence port for the catalog.
type Repository interface {
	ListModules(ctx context.Context, f ModuleFilter) ([]Module, error)
	// GetModule returns an apperr NotFound error when name is unknown.
	GetModule(ctx context.Context, name string) (Module, error)
	// ListVersions returns versions of moduleID, or of every module when moduleID is nil.
	ListVersions(ctx context.Context, moduleID *uuid.UUID) ([]ModuleVersion, error)
	// GetVersion returns an apperr NotFound error when the version is unknown.
	GetVersion(ctx context.Context, module string, v SemVer) (ModuleVersion, error)
	// Index upserts m and inserts v atomically with ev. Re-indexing the same version at the
	// same commit is a no-op (created=false); a different commit is a version_immutable conflict.
	Index(ctx context.Context, m Module, v ModuleVersion, ev audit.Event) (stored ModuleVersion, created bool, err error)
}

// ModuleSummary is a module with its latest usable version (nil if none).
type ModuleSummary struct {
	Module Module
	Latest *ModuleVersion
}

// Service implements catalog use cases.
type Service struct {
	repo    Repository
	clock   platform.Clock
	repoURL string
}

// NewService returns a catalog Service. repoURL is the git URL used in module sources.
func NewService(repo Repository, clock platform.Clock, repoURL string) *Service {
	return &Service{repo: repo, clock: clock, repoURL: repoURL}
}

// ListModules returns modules matching f, each with its latest version.
func (s *Service) ListModules(ctx context.Context, f ModuleFilter) ([]ModuleSummary, error) {
	mods, err := s.repo.ListModules(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}
	versions, err := s.repo.ListVersions(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list versions: %w", err)
	}
	latest := LatestByModule(versions)
	out := make([]ModuleSummary, 0, len(mods))
	for _, m := range mods {
		sum := ModuleSummary{Module: m}
		if v, ok := latest[m.ID]; ok {
			sum.Latest = &v
		}
		out = append(out, sum)
	}
	return out, nil
}

// GetModule returns one module with its latest version.
func (s *Service) GetModule(ctx context.Context, name string) (ModuleSummary, error) {
	m, err := s.repo.GetModule(ctx, name)
	if err != nil {
		return ModuleSummary{}, fmt.Errorf("get module %q: %w", name, err)
	}
	versions, err := s.repo.ListVersions(ctx, &m.ID)
	if err != nil {
		return ModuleSummary{}, fmt.Errorf("list versions of %q: %w", name, err)
	}
	sum := ModuleSummary{Module: m}
	if v, ok := LatestByModule(versions)[m.ID]; ok {
		sum.Latest = &v
	}
	return sum, nil
}

// ListVersions returns all versions of a module, newest first.
func (s *Service) ListVersions(ctx context.Context, name string) ([]ModuleVersion, error) {
	m, err := s.repo.GetModule(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get module %q: %w", name, err)
	}
	versions, err := s.repo.ListVersions(ctx, &m.ID)
	if err != nil {
		return nil, fmt.Errorf("list versions of %q: %w", name, err)
	}
	return SortVersionsDesc(versions), nil
}

// GetVersion returns one version of a module.
func (s *Service) GetVersion(ctx context.Context, name, version string) (ModuleVersion, error) {
	sv, err := ParseSemVer(version)
	if err != nil {
		return ModuleVersion{}, apperr.NotFound("module %s has no version %q", name, version)
	}
	v, err := s.repo.GetVersion(ctx, name, sv)
	if err != nil {
		return ModuleVersion{}, fmt.Errorf("get %s@%s: %w", name, version, err)
	}
	return v, nil
}

// LatestTarget asks UpgradePath for the newest usable version.
const LatestTarget = "latest"

// UpgradePath returns the versions of module name between the pinned version from and to
// (a version, or "latest"/"" for the newest usable one), oldest first. from must be a
// version in the catalog (a yanked one too: that is when an upgrade is most needed).
func (s *Service) UpgradePath(ctx context.Context, name, from, to string) (UpgradePath, error) {
	fromV, err := ParseSemVer(from)
	if err != nil {
		return UpgradePath{}, apperr.Invalid("validation_failed", "from must be an exact semantic version such as 1.2.0",
			apperr.FieldError{Pointer: "/from", Code: "pattern", Message: err.Error()})
	}
	m, err := s.repo.GetModule(ctx, name)
	if err != nil {
		return UpgradePath{}, fmt.Errorf("get module %q: %w", name, err)
	}
	versions, err := s.repo.ListVersions(ctx, &m.ID)
	if err != nil {
		return UpgradePath{}, fmt.Errorf("list versions of %q: %w", name, err)
	}
	find := func(v SemVer) (ModuleVersion, bool) {
		i := slices.IndexFunc(versions, func(mv ModuleVersion) bool { return mv.Version == v })
		if i < 0 {
			return ModuleVersion{}, false
		}
		return versions[i], true
	}
	if _, ok := find(fromV); !ok {
		return UpgradePath{}, apperr.NotFound("module %s has no version %s", name, fromV)
	}
	toV := fromV
	switch to {
	case "", LatestTarget:
		if latest, ok := LatestByModule(versions)[m.ID]; ok && latest.Version.Compare(fromV) > 0 {
			toV = latest.Version
		}
	default:
		parsed, err := ParseSemVer(to)
		if err != nil {
			return UpgradePath{}, apperr.Invalid("validation_failed", "to must be latest or an exact semantic version",
				apperr.FieldError{Pointer: "/to", Code: "pattern", Message: err.Error()})
		}
		target, ok := find(parsed)
		if !ok {
			return UpgradePath{}, apperr.NotFound("module %s has no version %s", name, parsed)
		}
		if !target.Usable() {
			return UpgradePath{}, apperr.Invalid("module_version_unavailable", fmt.Sprintf("%s@%s is yanked", name, parsed))
		}
		toV = parsed
	}
	return BuildUpgradePath(name, fromV, toV, versions)
}

// Index registers a released module version and records module_version.indexed.
func (s *Service) Index(ctx context.Context, by identity.Principal, cmd IndexCommand) (ModuleVersion, bool, error) {
	m, v, err := ValidateIndex(cmd)
	if err != nil {
		return ModuleVersion{}, false, err
	}
	payload, err := json.Marshal(map[string]string{"git_tag": v.GitTag, "git_sha": v.GitSHA})
	if err != nil {
		return ModuleVersion{}, false, fmt.Errorf("encode audit payload: %w", err)
	}
	ev := audit.Event{
		OccurredAt:  s.clock.Now(),
		ActorType:   string(by.Type),
		ActorID:     by.AuditActorID(),
		Action:      "module_version.indexed",
		SubjectType: "module_version",
		SubjectID:   v.GitTag,
		Payload:     payload,
	}
	stored, created, err := s.repo.Index(ctx, m, v, ev)
	if err != nil {
		return ModuleVersion{}, false, fmt.Errorf("index %s: %w", v.GitTag, err)
	}
	return stored, created, nil
}

// SourceAddress returns the pinned Terraform source for v.
func (s *Service) SourceAddress(v ModuleVersion) string {
	return SourceAddress(s.repoURL, v.ModuleName, v.Version)
}
