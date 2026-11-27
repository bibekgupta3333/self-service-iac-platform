package catalog

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
)

func sv(s string) SemVer {
	v, err := ParseSemVer(s)
	if err != nil {
		panic(err)
	}
	return v
}

func TestClassify(t *testing.T) {
	tests := []struct {
		from, to string
		want     Change
	}{
		{"1.2.0", "1.2.0", ChangeNone},
		{"1.2.0", "1.2.1", ChangePatch},
		{"1.2.0-rc.1", "1.2.0", ChangePatch},
		{"1.2.9", "1.3.0", ChangeMinor},
		{"1.2.0", "1.10.0", ChangeMinor},
		{"1.9.9", "2.0.0", ChangeMajor},
		{"1.0.0", "3.0.0", ChangeMajor},
	}
	for _, tt := range tests {
		if got := Classify(sv(tt.from), sv(tt.to)); got != tt.want {
			t.Errorf("Classify(%s, %s) = %s, want %s", tt.from, tt.to, got, tt.want)
		}
	}
	if !ChangeMajor.Breaking() || ChangeMinor.Breaking() || ChangePatch.Breaking() {
		t.Fatal("only a major is breaking")
	}
}

func TestCheckUpgrade(t *testing.T) {
	tests := []struct {
		from, to string
		wantCode string
	}{
		{"1.2.0", "1.2.1", ""},
		{"1.2.0", "1.9.0", ""},
		{"1.2.0", "2.0.0", ""},
		{"1.2.0", "2.5.1", ""},
		{"1.2.0", "3.0.0", "major_jump_too_large"},
		{"1.2.0", "1.2.0", "not_an_upgrade"},
		{"1.2.0", "1.1.9", "not_an_upgrade"},
		{"1.2.0", "1.2.0-rc.1", "not_an_upgrade"},
	}
	for _, tt := range tests {
		err := CheckUpgrade("s3", sv(tt.from), sv(tt.to))
		e, _ := apperr.As(err)
		switch {
		case tt.wantCode == "" && err != nil:
			t.Errorf("%s -> %s: unexpected %v", tt.from, tt.to, err)
		case tt.wantCode != "" && (e == nil || e.Code != tt.wantCode || e.Kind != apperr.KindInvalid):
			t.Errorf("%s -> %s: err = %v, want %s", tt.from, tt.to, err, tt.wantCode)
		}
	}
	err := CheckUpgrade("s3", sv("1.4.2"), sv("3.1.0"))
	if !strings.Contains(err.Error(), "next: 2.x") {
		t.Fatalf("the refusal must name the next major: %v", err)
	}
}

// docsVersions is the example from docs/06 §1, plus a yanked and a pre-release version.
func docsVersions() []ModuleVersion {
	mk := func(v string, status Status) ModuleVersion {
		return ModuleVersion{ModuleName: "s3", Version: sv(v), Status: status, GitTag: "modules/s3/v" + v}
	}
	return []ModuleVersion{
		mk("2.1.0", StatusActive), mk("1.2.0", StatusActive), mk("1.2.1", StatusActive), mk("1.3.0", StatusYanked),
		mk("1.4.2", StatusDeprecated), mk("2.0.0", StatusActive), mk("2.2.0-rc.1", StatusActive), mk("1.1.0", StatusActive),
	}
}

func steps(p UpgradePath) []string {
	return fp.Map(p.Steps, func(s UpgradeStep) string { return s.Version.Version.String() + ":" + string(s.Change) })
}

func ver(p *SemVer) string {
	if p == nil {
		return "-"
	}
	return p.String()
}

func TestBuildUpgradePath(t *testing.T) {
	tests := []struct {
		name, from, to          string
		steps                   []string
		change                  Change
		patch, minor, major, nx string
	}{
		{
			name: "docs example", from: "1.2.0", to: "2.1.0",
			steps:  []string{"1.2.1:patch", "1.4.2:minor", "2.0.0:major", "2.1.0:minor"},
			change: ChangeMajor, patch: "1.2.1", minor: "1.4.2", major: "2.1.0", nx: "2.1.0",
		},
		{
			name: "within the major", from: "1.2.0", to: "1.4.2",
			steps: []string{"1.2.1:patch", "1.4.2:minor"}, change: ChangeMinor, patch: "1.2.1", minor: "1.4.2", major: "1.4.2", nx: "1.4.2",
		},
		{
			name: "up to date", from: "2.1.0", to: "2.1.0", change: ChangeNone, patch: "-", minor: "-", major: "-", nx: "-",
		},
		{
			name: "explicit pre-release target", from: "2.1.0", to: "2.2.0-rc.1",
			steps: []string{"2.2.0-rc.1:minor"}, change: ChangeMinor, patch: "-", minor: "2.2.0-rc.1", major: "2.2.0-rc.1", nx: "2.2.0-rc.1",
		},
		{
			name: "from an old major, next stops one major up", from: "1.1.0", to: "2.1.0",
			steps:  []string{"1.2.0:minor", "1.2.1:patch", "1.4.2:minor", "2.0.0:major", "2.1.0:minor"},
			change: ChangeMajor, patch: "-", minor: "1.4.2", major: "2.1.0", nx: "2.1.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := BuildUpgradePath("s3", sv(tt.from), sv(tt.to), docsVersions())
			if err != nil {
				t.Fatal(err)
			}
			if got := steps(p); strings.Join(got, ",") != strings.Join(tt.steps, ",") {
				t.Fatalf("steps = %v, want %v", got, tt.steps)
			}
			if p.Change != tt.change || ver(p.Targets.Patch) != tt.patch || ver(p.Targets.Minor) != tt.minor ||
				ver(p.Targets.Major) != tt.major || ver(p.Next) != tt.nx || p.UpToDate() != (len(tt.steps) == 0) {
				t.Fatalf("path = change %s patch %s minor %s major %s next %s", p.Change, ver(p.Targets.Patch),
					ver(p.Targets.Minor), ver(p.Targets.Major), ver(p.Next))
			}
		})
	}

	// Across two majors, the next request may only go one major up.
	vs := append(docsVersions(), ModuleVersion{ModuleName: "s3", Version: sv("3.0.0"), Status: StatusActive})
	p, err := BuildUpgradePath("s3", sv("1.2.0"), sv("3.0.0"), vs)
	if err != nil || ver(p.Next) != "2.1.0" || p.MajorsCrossed() != 2 || ver(p.Targets.Major) != "3.0.0" {
		t.Fatalf("two majors: next %s crossed %d err %v", ver(p.Next), p.MajorsCrossed(), err)
	}
	if _, err := BuildUpgradePath("s3", sv("2.0.0"), sv("1.2.0"), vs); !apperr.IsKind(err, apperr.KindInvalid) {
		t.Fatalf("a downgrade is not a path: %v", err)
	}
}

func TestMigrationNotes(t *testing.T) {
	tests := []struct{ name, changelog, want string }{
		{name: "none", changelog: "### Added\n- x\n", want: ""},
		{name: "migration", changelog: "### Changed\n- y\n\n### Migration\n\nRename `a` to `b`.\n\n### Fixed\n- z\n", want: "Rename `a` to `b`."},
		{name: "upgrade notes heading, last subsection", changelog: "### Upgrade notes\n- step 1\n- step 2\n", want: "- step 1\n- step 2"},
		{name: "any case, stops at a level-2 heading", changelog: "### MIGRATION NOTES\ndo it\n## [1.0.0]\nold", want: "do it"},
		{name: "keeps deeper headings", changelog: "### Migration\n#### Step 1\nmove\n", want: "#### Step 1\nmove"},
	}
	for _, tt := range tests {
		if got := MigrationNotes(tt.changelog); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
	v := ModuleVersion{Changelog: "### Migration\nfrom changelog"}
	if v.Notes() != "from changelog" {
		t.Fatalf("fallback = %q", v.Notes())
	}
	v.UpgradeNotes = "indexed"
	if v.Notes() != "indexed" {
		t.Fatalf("indexed notes win: %q", v.Notes())
	}
}

func TestServiceUpgradePath(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	id := uuid.New()
	repo.modules["s3"] = Module{ID: id, Name: "s3"}
	for _, v := range docsVersions() {
		v.ModuleID = id
		repo.versions = append(repo.versions, v)
	}
	svc := newTestService(repo)

	p, err := svc.UpgradePath(ctx, "s3", "1.2.0", "")
	if err != nil || ver(p.Targets.Major) != "2.1.0" || len(p.Steps) != 4 {
		t.Fatalf("latest: %+v %v", p, err)
	}
	if p, err = svc.UpgradePath(ctx, "s3", "1.2.0", "1.4.2"); err != nil || p.To.String() != "1.4.2" {
		t.Fatalf("explicit: %+v %v", p, err)
	}
	// A yanked pin still gets its way out.
	if p, err = svc.UpgradePath(ctx, "s3", "1.3.0", "latest"); err != nil || steps(p)[0] != "1.4.2:minor" {
		t.Fatalf("from yanked: %v %v", steps(p), err)
	}
	if p, err = svc.UpgradePath(ctx, "s3", "2.1.0", "latest"); err != nil || !p.UpToDate() || p.To.String() != "2.1.0" {
		t.Fatalf("up to date: %+v %v", p, err)
	}
	for name, tc := range map[string]struct {
		module, from, to string
		kind             apperr.Kind
	}{
		"bad from":       {"s3", "v1", "", apperr.KindInvalid},
		"unknown from":   {"s3", "9.9.9", "", apperr.KindNotFound},
		"unknown module": {"nope", "1.0.0", "", apperr.KindNotFound},
		"bad to":         {"s3", "1.2.0", "newest", apperr.KindInvalid},
		"unknown to":     {"s3", "1.2.0", "1.5.0", apperr.KindNotFound},
		"yanked to":      {"s3", "1.2.0", "1.3.0", apperr.KindInvalid},
		"older to":       {"s3", "2.0.0", "1.2.0", apperr.KindInvalid},
	} {
		if _, err := svc.UpgradePath(ctx, tc.module, tc.from, tc.to); !apperr.IsKind(err, tc.kind) {
			t.Errorf("%s: err = %v, want kind %v", name, err, tc.kind)
		}
	}
}
