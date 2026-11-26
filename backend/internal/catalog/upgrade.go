package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
)

// Change is the SemVer class of a move between two versions (docs/06 §1).
type Change string

// Change classes, smallest first.
const (
	ChangeNone  Change = "none"
	ChangePatch Change = "patch"
	ChangeMinor Change = "minor"
	ChangeMajor Change = "major"
)

// Breaking reports whether the change may break consumers (a major).
func (c Change) Breaking() bool { return c == ChangeMajor }

// Classify returns the SemVer class of moving from from to to. A move that only changes
// the pre-release tag counts as a patch; equal versions are ChangeNone. Direction is not
// considered: callers check that to is newer. Pure.
func Classify(from, to SemVer) Change {
	switch {
	case from.Major != to.Major:
		return ChangeMajor
	case from.Minor != to.Minor:
		return ChangeMinor
	case from.Patch != to.Patch || from.Pre != to.Pre:
		return ChangePatch
	}
	return ChangeNone
}

// MajorsCrossed is how many major boundaries an upgrade from from to to crosses. Pure.
func MajorsCrossed(from, to SemVer) int { return max(0, to.Major-from.Major) }

// MaxMajorsPerUpgrade is how many majors one upgrade request may cross (docs/06 §1: one
// major at a time, so every major's migration notes are read and applied in turn).
const MaxMajorsPerUpgrade = 1

// CheckUpgrade decides whether one upgrade request may move a module from from to to:
// the target must be newer, and the request may cross at most one major. Pure.
func CheckUpgrade(module string, from, to SemVer) error {
	switch {
	case to.Compare(from) <= 0:
		return apperr.Invalid("not_an_upgrade",
			fmt.Sprintf("%s %s is not newer than the pinned %s; use an update request to pin an older version", module, to, from))
	case MajorsCrossed(from, to) > MaxMajorsPerUpgrade:
		return apperr.Invalid("major_jump_too_large",
			fmt.Sprintf("%s %s -> %s crosses %d majors; upgrade one major at a time (next: %d.x)",
				module, from, to, MajorsCrossed(from, to), from.Major+1))
	}
	return nil
}

// UpgradeStep is one released version on an upgrade path.
type UpgradeStep struct {
	Version ModuleVersion
	// Change is the class of the move from the previous step (or from the pinned version).
	Change Change
}

// Notes returns the upgrade notes of the step's version.
func (s UpgradeStep) Notes() string { return s.Version.Notes() }

// UpgradeTargets are the newest versions reachable per change class (docs/06 §1).
type UpgradeTargets struct {
	Patch *SemVer // newest X.Y.z of the pinned X.Y
	Minor *SemVer // newest X.y.z of the pinned X
	Major *SemVer // newest overall (the path's end)
}

// UpgradePath is the ordered list of versions between a pinned version and a target.
type UpgradePath struct {
	Module string
	From   SemVer
	To     SemVer
	// Steps are the usable versions after From up to and including To, oldest first.
	Steps []UpgradeStep
	// Change is the class of the whole move From -> To.
	Change  Change
	Targets UpgradeTargets
	// Next is the newest version one upgrade request may pin (one major at a time); nil
	// when From is already the target.
	Next *SemVer
}

// UpToDate reports whether there is nothing to upgrade to.
func (p UpgradePath) UpToDate() bool { return len(p.Steps) == 0 }

// MajorsCrossed is how many majors the whole path crosses.
func (p UpgradePath) MajorsCrossed() int { return MajorsCrossed(p.From, p.To) }

// BuildUpgradePath returns the path of module from from to to through versions (any
// order, any status). Steps are the versions newer than from and not newer than to that
// are usable (not yanked) and released (no pre-release, except to itself). to must be
// usable and not older than from. Pure.
func BuildUpgradePath(module string, from, to SemVer, versions []ModuleVersion) (UpgradePath, error) {
	if to.Compare(from) < 0 {
		return UpgradePath{}, apperr.Invalid("not_an_upgrade", fmt.Sprintf("%s %s is older than %s", module, to, from))
	}
	inRange := func(v ModuleVersion) bool {
		return v.Usable() && v.Version.Compare(from) > 0 && v.Version.Compare(to) <= 0 &&
			(v.Version.Pre == "" || v.Version == to)
	}
	sorted := SortVersionsDesc(fp.Filter(versions, inRange))
	slices.Reverse(sorted)

	p := UpgradePath{Module: module, From: from, To: to, Change: Classify(from, to)}
	prev := from
	for _, v := range sorted {
		p.Steps = append(p.Steps, UpgradeStep{Version: v, Change: Classify(prev, v.Version)})
		prev = v.Version
	}
	for _, s := range p.Steps {
		v := s.Version.Version
		if v.Major == from.Major && v.Minor == from.Minor {
			p.Targets.Patch = &v
		}
		if v.Major == from.Major {
			p.Targets.Minor = &v
		}
		if v.Major-from.Major <= MaxMajorsPerUpgrade {
			p.Next = &v
		}
		p.Targets.Major = &v
	}
	return p, nil
}

// migrationHeadingRe matches the CHANGELOG subsection that carries upgrade instructions:
// "### Migration" (docs/04 §5) or "### Upgrade notes", in any letter case.
var migrationHeadingRe = regexp.MustCompile(`(?im)^###[ \t]+(migration|upgrade notes?)\b.*$`)

// subsectionEndRe matches the next heading of the same or a higher level.
var subsectionEndRe = regexp.MustCompile(`(?m)^#{1,3}[ \t]`)

// MigrationNotes extracts the "### Migration" (or "### Upgrade notes") subsection of a
// version's CHANGELOG section, without its heading; "" when there is none. Pure.
func MigrationNotes(changelog string) string {
	loc := migrationHeadingRe.FindStringIndex(changelog)
	if loc == nil {
		return ""
	}
	body := changelog[loc[1]:]
	if end := subsectionEndRe.FindStringIndex(body); end != nil {
		body = body[:end[0]]
	}
	return strings.TrimSpace(body)
}

// Notes returns the version's upgrade notes: the indexed upgrade_notes, or else the
// migration subsection of its changelog (versions indexed before upgrade_notes existed).
func (v ModuleVersion) Notes() string {
	if strings.TrimSpace(v.UpgradeNotes) != "" {
		return v.UpgradeNotes
	}
	return MigrationNotes(v.Changelog)
}
