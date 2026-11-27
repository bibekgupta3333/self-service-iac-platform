// Package catalog manages the module registry: modules, their immutable released
// versions and the index operation called by module-release.yml.
package catalog

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
)

// Status is the lifecycle of a module or module version (enum module_status).
type Status string

// Module statuses.
const (
	StatusActive     Status = "active"
	StatusDeprecated Status = "deprecated"
	StatusYanked     Status = "yanked"
)

// Module is a catalog package such as "s3".
type Module struct {
	ID          uuid.UUID
	Name        string
	DisplayName string
	Description string
	Category    string
	Owners      []string
	RepoPath    string
	Status      Status
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Output describes one module output.
type Output struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Sensitive   bool   `json:"sensitive"`
}

// ModuleVersion is an immutable released version of a module.
type ModuleVersion struct {
	ID               uuid.UUID
	ModuleID         uuid.UUID
	ModuleName       string
	Version          SemVer
	GitTag           string
	GitSHA           string
	Manifest         json.RawMessage
	InputsSchema     json.RawMessage
	Outputs          []Output
	CostHints        json.RawMessage
	TFVersionRange   string
	AWSProviderRange string
	Changelog        string
	// UpgradeNotes are the migration instructions for moving to this version (the CHANGELOG
	// "### Migration" subsection, or module.yaml upgrade.notes); see Notes.
	UpgradeNotes    string
	Status          Status
	DeprecationNote string
	ReleasedBy      string
	ReleasedAt      time.Time
}

// Usable reports whether new requests may pin this version.
func (v ModuleVersion) Usable() bool { return v.Status != StatusYanked }

// HasOutput reports whether the version declares an output called name.
func (v ModuleVersion) HasOutput(name string) bool {
	return slices.ContainsFunc(v.Outputs, func(o Output) bool { return o.Name == name })
}

// OutputNames returns declared output names, sorted.
func (v ModuleVersion) OutputNames() []string {
	names := fp.Map(v.Outputs, func(o Output) string { return o.Name })
	slices.Sort(names)
	return names
}

// SemVer is a parsed semantic version (MAJOR.MINOR.PATCH[-PRERELEASE]).
type SemVer struct {
	Major, Minor, Patch int
	Pre                 string
}

var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?$`)

// ParseSemVer parses s (without a leading "v").
func ParseSemVer(s string) (SemVer, error) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return SemVer{}, fmt.Errorf("invalid semantic version %q", s)
	}
	atoi := func(x string) int { n, _ := strconv.Atoi(x); return n }
	return SemVer{Major: atoi(m[1]), Minor: atoi(m[2]), Patch: atoi(m[3]), Pre: m[4]}, nil
}

func (v SemVer) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders versions by SemVer precedence (pre-releases sort before the release).
func (v SemVer) Compare(o SemVer) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return comparePre(v.Pre, o.Pre)
}

func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		var c int
		switch {
		case aErr == nil && bErr == nil:
			c = cmp.Compare(an, bn)
		case aErr == nil:
			c = -1 // numeric identifiers have lower precedence
		case bErr == nil:
			c = 1
		default:
			c = strings.Compare(as[i], bs[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// SortVersionsDesc returns versions ordered newest first. It does not modify in.
func SortVersionsDesc(in []ModuleVersion) []ModuleVersion {
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b ModuleVersion) int { return b.Version.Compare(a.Version) })
	return out
}

// LatestByModule returns, per module id, the newest usable, non-prerelease version
// (falling back to the newest usable pre-release when no release exists).
func LatestByModule(versions []ModuleVersion) map[uuid.UUID]ModuleVersion {
	latest := map[uuid.UUID]ModuleVersion{}
	for _, v := range SortVersionsDesc(fp.Filter(versions, ModuleVersion.Usable)) {
		cur, seen := latest[v.ModuleID]
		if !seen || (cur.Version.Pre != "" && v.Version.Pre == "") {
			latest[v.ModuleID] = v
		}
	}
	return latest
}

// FieldChange is the before and after value of one changed module field.
type FieldChange struct {
	From any `json:"from"`
	To   any `json:"to"`
}

// ModuleChanges lists the descriptive fields that differ between before and after. Pure.
func ModuleChanges(before, after Module) map[string]FieldChange {
	out := map[string]FieldChange{}
	diff := func(field string, a, b any, equal bool) {
		if !equal {
			out[field] = FieldChange{From: a, To: b}
		}
	}
	diff("display_name", before.DisplayName, after.DisplayName, before.DisplayName == after.DisplayName)
	diff("description", before.Description, after.Description, before.Description == after.Description)
	diff("category", before.Category, after.Category, before.Category == after.Category)
	diff("owners", before.Owners, after.Owners, slices.Equal(before.Owners, after.Owners))
	return out
}

// GitTag returns the immutable tag for module name at version v.
func GitTag(name string, v SemVer) string { return fmt.Sprintf("modules/%s/v%s", name, v) }

// SourceAddress returns the pinned Terraform module source for name at version v.
func SourceAddress(repoURL, name string, v SemVer) string {
	return fmt.Sprintf("git::%s//modules/%s?ref=%s", repoURL, name, GitTag(name, v))
}
