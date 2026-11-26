package catalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/inputschema"
)

// IndexCommand registers a released module version. Until the GitHub App client
// lands (WBS 4.8) the release workflow sends the parsed module.yaml inline; later
// the backend fetches it at CommitSHA itself and this payload shrinks to the
// {module, version, git_tag, commit_sha} tuple from docs/03.
type IndexCommand struct {
	Module           string          `json:"module"`
	Version          string          `json:"version"`
	GitTag           string          `json:"git_tag"`
	CommitSHA        string          `json:"commit_sha"`
	DisplayName      string          `json:"display_name"`
	Description      string          `json:"description"`
	Category         string          `json:"category"`
	Owners           []string        `json:"owners"`
	InputsSchema     json.RawMessage `json:"inputs_schema"`
	Outputs          []Output        `json:"outputs"`
	CostHints        json.RawMessage `json:"cost_hints,omitempty"`
	TFVersionRange   string          `json:"tf_version_range"`
	AWSProviderRange string          `json:"aws_provider_range"`
	Changelog        string          `json:"changelog_md,omitempty"`
	ReleasedBy       string          `json:"released_by"`
}

var (
	moduleNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	gitSHARe     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	outputNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	categories   = []string{"storage", "messaging", "compute", "data", "network", "observability"}
)

// ValidateIndex checks cmd and builds the module and version to store. Pure.
func ValidateIndex(cmd IndexCommand) (Module, ModuleVersion, error) {
	var errs []apperr.FieldError
	add := func(ptr, code, msg string) {
		errs = append(errs, apperr.FieldError{Pointer: ptr, Code: code, Message: msg})
	}
	required := func(ptr, v string) {
		if strings.TrimSpace(v) == "" {
			add(ptr, "required", "is required")
		}
	}

	if !moduleNameRe.MatchString(cmd.Module) {
		add("/module", "pattern", "must match ^[a-z][a-z0-9-]{1,40}$")
	}
	ver, err := ParseSemVer(cmd.Version)
	if err != nil {
		add("/version", "pattern", err.Error())
	}
	if err == nil && cmd.GitTag != GitTag(cmd.Module, ver) {
		add("/git_tag", "mismatch", fmt.Sprintf("must be %q", GitTag(cmd.Module, ver)))
	}
	if !gitSHARe.MatchString(cmd.CommitSHA) {
		add("/commit_sha", "pattern", "must be a 40-character lowercase hex commit SHA")
	}
	required("/display_name", cmd.DisplayName)
	required("/description", cmd.Description)
	required("/tf_version_range", cmd.TFVersionRange)
	required("/aws_provider_range", cmd.AWSProviderRange)
	required("/released_by", cmd.ReleasedBy)
	if !slices.Contains(categories, cmd.Category) {
		add("/category", "enum", "must be one of "+strings.Join(categories, ", "))
	}
	if len(cmd.Owners) == 0 {
		add("/owners", "required", "at least one owning team is required")
	}
	if len(cmd.InputsSchema) == 0 {
		add("/inputs_schema", "required", "is required")
	} else if _, err := inputschema.Compile(cmd.InputsSchema); err != nil {
		add("/inputs_schema", "invalid_schema", err.Error())
	}
	for i, o := range cmd.Outputs {
		if !outputNameRe.MatchString(o.Name) {
			add(fmt.Sprintf("/outputs/%d/name", i), "pattern", "must be a Terraform identifier")
		}
	}
	for _, d := range fp.Duplicates(fp.Map(cmd.Outputs, func(o Output) string { return o.Name })) {
		add("/outputs", "duplicate", fmt.Sprintf("output %q is declared more than once", d))
	}
	if len(errs) > 0 {
		return Module{}, ModuleVersion{}, apperr.Invalid("validation_failed", "module release is invalid", errs...)
	}

	manifest, err := json.Marshal(cmd)
	if err != nil {
		return Module{}, ModuleVersion{}, fmt.Errorf("encode manifest: %w", err)
	}
	outputs := cmd.Outputs
	if outputs == nil {
		outputs = []Output{}
	}
	m := Module{
		Name: cmd.Module, DisplayName: cmd.DisplayName, Description: cmd.Description, Category: cmd.Category,
		Owners: cmd.Owners, RepoPath: "modules/" + cmd.Module, Status: StatusActive,
	}
	v := ModuleVersion{
		ModuleName: cmd.Module, Version: ver, GitTag: cmd.GitTag, GitSHA: cmd.CommitSHA, Manifest: manifest,
		InputsSchema: cmd.InputsSchema, Outputs: outputs, CostHints: cmd.CostHints, TFVersionRange: cmd.TFVersionRange,
		AWSProviderRange: cmd.AWSProviderRange, Changelog: cmd.Changelog, Status: StatusActive, ReleasedBy: cmd.ReleasedBy,
	}
	return m, v, nil
}
