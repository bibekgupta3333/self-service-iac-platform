package requests

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tfname"
)

// UpgradeInput moves one resource of the stack to another version of its module.
type UpgradeInput struct {
	Resource  string `json:"resource"`
	ToVersion string `json:"to_version"`
}

// UpgradeChange records one resource's move. It is stored in the request spec (upgrades)
// and in the request.created audit payload, so reviewers see what the PR pins and why.
type UpgradeChange struct {
	Resource    string         `json:"resource"`
	Module      string         `json:"module"`
	FromVersion string         `json:"from_version"`
	ToVersion   string         `json:"to_version"`
	Change      catalog.Change `json:"change"`
}

// validateUpgradeShapes checks the upgrade list of an upgrade request, and that other kinds
// carry none. Pure.
func validateUpgradeShapes(in CreateInput, add fieldAdder) {
	if in.Kind != KindUpgrade {
		if len(in.Upgrades) > 0 {
			add("/upgrades", "not_allowed", "only an upgrade request lists upgrades")
		}
		if in.AcknowledgeBreaking {
			add("/acknowledge_breaking", "not_allowed", "only an upgrade request acknowledges a breaking change")
		}
		return
	}
	if len(in.Resources) > 0 {
		add("/resources", "not_allowed", "an upgrade keeps the stack's resources and inputs; list the versions in upgrades")
	}
	if in.AllowRemovals {
		add("/allow_removals", "not_allowed", "an upgrade removes no resources")
	}
	switch {
	case len(in.Upgrades) == 0:
		add("/upgrades", "minItems", "name at least one resource and the version to upgrade it to")
	case len(in.Upgrades) > MaxResources:
		add("/upgrades", "maxItems", fmt.Sprintf("at most %d upgrades are allowed", MaxResources))
	}
	for i, u := range in.Upgrades {
		ptr := fmt.Sprintf("/upgrades/%d", i)
		if !tfname.IsIdentifier(u.Resource) {
			add(ptr+"/resource", "pattern", "must be the name of a resource of the stack")
		}
		if _, err := catalog.ParseSemVer(u.ToVersion); err != nil {
			add(ptr+"/to_version", "pattern", "must be an exact semantic version such as 1.4.0")
		}
	}
	for _, d := range fp.Duplicates(fp.Map(in.Upgrades, func(u UpgradeInput) string { return u.Resource })) {
		add("/upgrades", "duplicate", fmt.Sprintf("resource %q is upgraded more than once", d))
	}
}

// PlanUpgrade applies upgrades to the stack's current spec: each named resource keeps its
// module and inputs and pins the new version. It returns the new resource list, the changes
// sorted by resource, and field errors for unknown resources, downgrades and jumps across
// more than one major (catalog.CheckUpgrade). Pure.
func PlanUpgrade(current NormalizedSpec, upgrades []UpgradeInput) ([]ResourceInput, []UpgradeChange, []apperr.FieldError) {
	next := slices.Clone(current.Resources)
	var (
		changes []UpgradeChange
		errs    []apperr.FieldError
	)
	for j, u := range upgrades {
		ptr := fmt.Sprintf("/upgrades/%d", j)
		i := slices.IndexFunc(next, func(r ResourceInput) bool { return r.Name == u.Resource })
		if i < 0 {
			names := strings.Join(fp.Map(current.Resources, func(r ResourceInput) string { return r.Name }), ", ")
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/resource", Code: "unknown_resource",
				Message: fmt.Sprintf("the stack has no resource %q (resources: %s)", u.Resource, names)})
			continue
		}
		r := next[i]
		from, err := catalog.ParseSemVer(r.Version)
		if err != nil {
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/resource", Code: "invalid_spec",
				Message: fmt.Sprintf("resource %q pins %q, which is not a version", r.Name, r.Version)})
			continue
		}
		to, err := catalog.ParseSemVer(u.ToVersion)
		if err != nil {
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/to_version", Code: "pattern", Message: err.Error()})
			continue
		}
		if err := catalog.CheckUpgrade(r.Module, from, to); err != nil {
			e, _ := apperr.As(err)
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/to_version", Code: e.Code, Message: e.Message})
			continue
		}
		next[i].Version = to.String()
		changes = append(changes, UpgradeChange{Resource: r.Name, Module: r.Module, FromVersion: from.String(),
			ToVersion: to.String(), Change: catalog.Classify(from, to)})
	}
	slices.SortFunc(changes, func(a, b UpgradeChange) int { return strings.Compare(a.Resource, b.Resource) })
	return next, changes, errs
}

// BreakingChanges returns the changes that cross a major. Pure.
func BreakingChanges(changes []UpgradeChange) []UpgradeChange {
	return fp.Filter(changes, func(c UpgradeChange) bool { return c.Change.Breaking() })
}

// upgradeFieldErrors rewrites field errors found while validating the upgraded resource list
// (pointers /resources/<i>/...) to the request the caller sent: an upgraded resource's errors
// point into its /upgrades entry (/version becomes /to_version), any other resource's go to
// /upgrades with the resource named. A non-upgraded resource pinned to a yanked version is not
// an error: a yanked pin keeps working until it is upgraded (docs/04 §7). Pure.
func upgradeFieldErrors(errs []apperr.FieldError, next []ResourceInput, upgrades []UpgradeInput) []apperr.FieldError {
	out := make([]apperr.FieldError, 0, len(errs))
	for _, e := range errs {
		i, rest, ok := splitResourcePointer(e.Pointer)
		if !ok || i >= len(next) {
			out = append(out, e)
			continue
		}
		name := next[i].Name
		j := slices.IndexFunc(upgrades, func(u UpgradeInput) bool { return u.Resource == name })
		switch {
		case j >= 0 && rest == "/version":
			e.Pointer = fmt.Sprintf("/upgrades/%d/to_version", j)
		case j >= 0:
			e.Pointer = fmt.Sprintf("/upgrades/%d%s", j, rest)
		case e.Code == "module_version_unavailable":
			continue
		default:
			e.Pointer, e.Message = "/upgrades", fmt.Sprintf("resource %q%s: %s", name, rest, e.Message)
		}
		out = append(out, e)
	}
	return out
}

// splitResourcePointer splits "/resources/<i><rest>" into i and rest. Pure.
func splitResourcePointer(ptr string) (int, string, bool) {
	tail, ok := strings.CutPrefix(ptr, "/resources/")
	if !ok {
		return 0, "", false
	}
	idx, rest, _ := strings.Cut(tail, "/")
	i, err := strconv.Atoi(idx)
	if err != nil || i < 0 {
		return 0, "", false
	}
	if rest != "" {
		rest = "/" + rest
	}
	return i, rest, true
}

// upgradeErrorCode names a refused upgrade by its field errors: upgrade_requires_input_changes
// when inputs no longer validate against the target version, otherwise the shared code when
// every error has the same one (module_version_unavailable, major_jump_too_large, ...), else
// validation_failed. Pure.
func upgradeErrorCode(errs []apperr.FieldError) string {
	if slices.ContainsFunc(errs, func(e apperr.FieldError) bool { return strings.Contains(e.Pointer, "/inputs") }) {
		return "upgrade_requires_input_changes"
	}
	codes := slices.Compact(slices.Sorted(slices.Values(fp.Map(errs, func(e apperr.FieldError) string { return e.Code }))))
	if len(codes) == 1 {
		return codes[0]
	}
	return "validation_failed"
}
