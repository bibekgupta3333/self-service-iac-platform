package requests

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/inputschema"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tfname"
)

// CreateInput is the body of POST /v1/requests (docs/03 §6.4). A create names its stack
// by team, environment and stack; an update or destroy names an existing stack by StackID.
type CreateInput struct {
	Kind        Kind       `json:"kind"`
	StackID     *uuid.UUID `json:"stack_id,omitempty"`
	Team        string     `json:"team,omitempty"`
	Environment string     `json:"environment,omitempty"`
	Stack       string     `json:"stack,omitempty"`
	Region      string     `json:"region,omitempty"`
	Ephemeral   *Ephemeral `json:"ephemeral,omitempty"`
	// ConfirmStackName must repeat the stack's name on a destroy.
	ConfirmStackName string `json:"confirm_stack_name,omitempty"`
	// AllowRemovals lets an update drop resources of the stack's current spec. Without it
	// such an update is refused, so a form built from an older spec cannot silently delete
	// what a newer request added.
	AllowRemovals bool            `json:"allow_removals,omitempty"`
	Justification string          `json:"justification,omitempty"`
	Resources     []ResourceInput `json:"resources"`
	DependsOn     []string        `json:"depends_on,omitempty"`
}

// Ephemeral requests a stack that is destroyed after TTLHours (non-production only). Zero
// means the configured default.
type Ephemeral struct {
	TTLHours int `json:"ttl_hours"`
}

// ResourceInput is one module instance in a request.
type ResourceInput struct {
	Name    string          `json:"name"`
	Module  string          `json:"module"`
	Version string          `json:"version"`
	Inputs  json.RawMessage `json:"inputs"`
}

var (
	slugRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}$`)
	stackNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
)

// MaxResources bounds the number of module instances per stack.
const MaxResources = 50

// ValidateShape checks everything that does not need the catalog or the database. Pure.
func ValidateShape(in CreateInput) []apperr.FieldError {
	var errs []apperr.FieldError
	add := func(ptr, code, msg string) {
		errs = append(errs, apperr.FieldError{Pointer: ptr, Code: code, Message: msg})
	}

	switch {
	case !in.Kind.Valid():
		add("/kind", "enum", "must be one of create, update, upgrade, destroy, reconcile, rollback")
	case !in.Kind.Supported():
		add("/kind", "unsupported_kind", fmt.Sprintf("kind %q is not supported yet; supported kinds are create, update and destroy", in.Kind))
	}
	if in.Kind == KindUpdate || in.Kind == KindDestroy {
		validateExistingStackTarget(in, add)
	} else {
		validateNewStackTarget(in, add)
	}
	if len(in.Justification) > 2000 {
		add("/justification", "maxLength", "must be at most 2000 characters")
	}
	if in.Kind == KindDestroy && strings.TrimSpace(in.Justification) == "" {
		add("/justification", "required", "a destroy request needs a justification")
	}
	validateResourceShapes(in, add)
	return errs
}

// fieldAdder records one field error.
type fieldAdder func(ptr, code, msg string)

// validateNewStackTarget checks how a create names the stack it makes. Pure.
func validateNewStackTarget(in CreateInput, add fieldAdder) {
	if !slugRe.MatchString(in.Team) {
		add("/team", "pattern", "must be a team slug (^[a-z][a-z0-9-]{1,38}$)")
	}
	if in.Environment == "" {
		add("/environment", "required", "is required")
	}
	if !stackNameRe.MatchString(in.Stack) {
		add("/stack", "pattern", "must match ^[a-z][a-z0-9-]{1,40}$")
	}
	// 0 asks for the configured default TTL; the bounds are checked against the policy and
	// the environment (ttl.Policy.Resolve).
	if in.Ephemeral != nil && in.Ephemeral.TTLHours < 0 {
		add("/ephemeral/ttl_hours", "minimum", "must not be negative")
	}
	if len(in.DependsOn) > 0 {
		add("/depends_on", "unsupported", "cross-stack dependencies are not supported yet")
	}
	if in.StackID != nil {
		add("/stack_id", "not_allowed", "a create names its stack by team, environment and stack")
	}
	if in.ConfirmStackName != "" {
		add("/confirm_stack_name", "not_allowed", "only a destroy confirms the stack name")
	}
	if in.AllowRemovals {
		add("/allow_removals", "not_allowed", "only an update can remove resources")
	}
}

// validateExistingStackTarget checks how an update or destroy names its stack: by
// stack_id alone, since team, environment, name and region are fixed once it exists. Pure.
func validateExistingStackTarget(in CreateInput, add fieldAdder) {
	if in.StackID == nil || *in.StackID == uuid.Nil {
		add("/stack_id", "required", fmt.Sprintf("a %s request names its stack by stack_id", in.Kind))
	}
	notAllowed := func(ptr string, set bool) {
		if set {
			add(ptr, "not_allowed", fmt.Sprintf("a %s request identifies the stack by stack_id; leave this out", in.Kind))
		}
	}
	notAllowed("/team", in.Team != "")
	notAllowed("/environment", in.Environment != "")
	notAllowed("/stack", in.Stack != "")
	notAllowed("/region", in.Region != "")
	notAllowed("/ephemeral", in.Ephemeral != nil)
	if len(in.DependsOn) > 0 {
		add("/depends_on", "unsupported", "cross-stack dependencies are not supported yet")
	}
	switch in.Kind {
	case KindDestroy:
		if in.AllowRemovals {
			add("/allow_removals", "not_allowed", "only an update can remove resources")
		}
		if !stackNameRe.MatchString(in.ConfirmStackName) {
			add("/confirm_stack_name", "required", "repeat the stack name (^[a-z][a-z0-9-]{1,40}$) to confirm the destroy")
		}
		if len(in.Resources) > 0 {
			add("/resources", "not_allowed", "a destroy removes every resource of the stack; leave resources out")
		}
	default:
		if in.ConfirmStackName != "" {
			add("/confirm_stack_name", "not_allowed", "only a destroy confirms the stack name")
		}
	}
}

// validateResourceShapes checks the resource list of a create or update. A destroy has
// none, and that is checked with the target. Pure.
func validateResourceShapes(in CreateInput, add fieldAdder) {
	if in.Kind == KindDestroy {
		return
	}
	switch {
	case len(in.Resources) == 0:
		add("/resources", "minItems", "at least one resource is required")
	case len(in.Resources) > MaxResources:
		add("/resources", "maxItems", fmt.Sprintf("at most %d resources are allowed", MaxResources))
	}
	for i, r := range in.Resources {
		ptr := fmt.Sprintf("/resources/%d", i)
		if !tfname.IsIdentifier(r.Name) {
			add(ptr+"/name", "pattern", "must be an HCL identifier (^[a-z][a-z0-9_]{0,62}$)")
		}
		if r.Module == "" {
			add(ptr+"/module", "required", "is required")
		}
		if _, err := catalog.ParseSemVer(r.Version); err != nil {
			add(ptr+"/version", "pattern", "must be an exact semantic version such as 1.2.0")
		}
	}
	for _, d := range fp.Duplicates(fp.Map(in.Resources, func(r ResourceInput) string { return r.Name })) {
		add("/resources", "duplicate", fmt.Sprintf("resource name %q is used more than once", d))
	}
}

// ResolvedResource pairs a resource with the catalog version it pins.
type ResolvedResource struct {
	Input   ResourceInput
	Version catalog.ModuleVersion
	Schema  *inputschema.Schema
}

// ValidateResources checks each resource against its module version: usability,
// input schema, and intra-stack references ({"$ref": "<instance>.<output>"}). Pure.
func ValidateResources(resources []ResolvedResource) []apperr.FieldError {
	byName := make(map[string]catalog.ModuleVersion, len(resources))
	for _, r := range resources {
		byName[r.Input.Name] = r.Version
	}
	var errs []apperr.FieldError
	for i, r := range resources {
		ptr := fmt.Sprintf("/resources/%d", i)
		if !r.Version.Usable() {
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/version", Code: "module_version_unavailable",
				Message: fmt.Sprintf("%s@%s is yanked", r.Input.Module, r.Input.Version)})
			continue
		}
		raw := r.Input.Inputs
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		doc, err := inputschema.Decode(raw)
		if err != nil {
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/inputs", Code: "type", Message: "must be a JSON object: " + err.Error()})
			continue
		}
		if obj, ok := doc.(map[string]any); ok {
			// The generator renders these names as module arguments; reject what it would reject.
			for _, name := range fp.SortedKeys(obj) {
				if err := tfname.ValidateInputName(name); err != nil {
					errs = append(errs, apperr.FieldError{Pointer: ptr + "/inputs" + inputschema.Pointer([]string{name}),
						Code: "invalid_input_name", Message: err.Error()})
				}
			}
		}
		masked, refs := maskRefs(doc, "")
		for _, ref := range refs {
			if msg := checkRef(ref, r.Input.Name, byName); msg != "" {
				errs = append(errs, apperr.FieldError{Pointer: ptr + "/inputs" + ref.pointer, Code: "invalid_ref", Message: msg})
			}
		}
		for _, v := range r.Schema.Validate(masked) {
			if underRef(v.Pointer, refs) {
				continue // a reference stands in for a value only known at apply time
			}
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/inputs" + v.Pointer, Code: v.Code, Message: v.Message})
		}
	}
	return errs
}

type inputRef struct {
	pointer string // JSON pointer within the inputs object
	ref     tfname.Ref
	err     error // set when the {"$ref"} object is malformed
}

// refPlaceholder replaces references during schema validation.
const refPlaceholder = "${iacp:ref}"

// maskRefs returns a copy of v with every {"$ref": "..."} object replaced by a placeholder
// string, plus the location of each reference. Pure.
func maskRefs(v any, ptr string) (any, []inputRef) {
	switch t := v.(type) {
	case map[string]any:
		if ref, isRef, err := tfname.ParseRef(t); isRef {
			return refPlaceholder, []inputRef{{pointer: ptr, ref: ref, err: err}}
		}
		out := make(map[string]any, len(t))
		var refs []inputRef
		for _, k := range fp.SortedKeys(t) {
			child, rs := maskRefs(t[k], ptr+inputschema.Pointer([]string{k}))
			out[k], refs = child, append(refs, rs...)
		}
		return out, refs
	case []any:
		out := make([]any, len(t))
		var refs []inputRef
		for i, e := range t {
			child, rs := maskRefs(e, ptr+"/"+strconv.Itoa(i))
			out[i], refs = child, append(refs, rs...)
		}
		return out, refs
	default:
		return v, nil
	}
}

func checkRef(r inputRef, self string, byName map[string]catalog.ModuleVersion) string {
	if r.err != nil {
		return r.err.Error()
	}
	target, inst, out := r.ref.String(), r.ref.Resource, r.ref.Output
	if inst == self {
		return fmt.Sprintf("resource %q cannot reference itself", self)
	}
	v, ok := byName[inst]
	if !ok {
		return fmt.Sprintf("reference %q: no resource named %q in this request", target, inst)
	}
	if !v.HasOutput(out) {
		return fmt.Sprintf("reference %q: %s@%s has no output %q (outputs: %s)",
			target, v.ModuleName, v.Version, out, strings.Join(v.OutputNames(), ", "))
	}
	return ""
}

func underRef(ptr string, refs []inputRef) bool {
	return slices.ContainsFunc(refs, func(r inputRef) bool {
		return ptr == r.pointer || strings.HasPrefix(ptr, r.pointer+"/")
	})
}

// NormalizedSpec is the canonical StackSpec stored in requests.spec.
type NormalizedSpec struct {
	Team        string          `json:"team"`
	Environment string          `json:"environment"`
	Stack       string          `json:"stack"`
	Region      string          `json:"region"`
	Ephemeral   *Ephemeral      `json:"ephemeral,omitempty"`
	Resources   []ResourceInput `json:"resources"`
	DependsOn   []string        `json:"depends_on"`
}

// Normalize returns the spec to store, with resources sorted by name. Pure.
func Normalize(in CreateInput, region string) NormalizedSpec {
	res := slices.Clone(in.Resources)
	slices.SortFunc(res, func(a, b ResourceInput) int { return strings.Compare(a.Name, b.Name) })
	for i := range res {
		if len(res[i].Inputs) == 0 {
			res[i].Inputs = json.RawMessage(`{}`)
		}
	}
	deps := in.DependsOn
	if deps == nil {
		deps = []string{}
	}
	return NormalizedSpec{Team: in.Team, Environment: in.Environment, Stack: in.Stack, Region: region,
		Ephemeral: in.Ephemeral, Resources: res, DependsOn: deps}
}

// NormalizeForStack returns the spec to store for an update or destroy of an existing
// stack: the stack's own identity and region (the request cannot change them), the new
// resource list for an update, and no resources for a destroy. Pure.
func NormalizeForStack(in CreateInput, t StackTarget) NormalizedSpec {
	n := Normalize(in, t.Region)
	n.Team, n.Environment, n.Stack, n.Ephemeral = t.TeamSlug, t.Environment, t.Name, nil
	if in.Kind == KindDestroy {
		n.Resources = []ResourceInput{}
	}
	return n
}

// RemovedResources returns the instance names of prev that next no longer lists, sorted.
// An update is a full replacement of the resource list, so these are destroyed by it. Pure.
func RemovedResources(prev NormalizedSpec, next []ResourceInput) []string {
	kept := make(map[string]bool, len(next))
	for _, r := range next {
		kept[r.Name] = true
	}
	var gone []string
	for _, r := range prev.Resources {
		if !kept[r.Name] {
			gone = append(gone, r.Name)
		}
	}
	slices.Sort(gone)
	return gone
}

// RequestHash fingerprints an idempotent POST: sha256(method + path + body).
func RequestHash(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte(path))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// ValidateIdempotencyKey checks the Idempotency-Key header value.
func ValidateIdempotencyKey(key string) error {
	if len(key) < 8 || len(key) > 128 {
		return &apperr.Error{Kind: apperr.KindInvalid, Code: "bad_request",
			Message: "Idempotency-Key header is required and must be 8-128 characters"}
	}
	return nil
}

// Cursor is a keyset position in the request list (created_at DESC, id DESC).
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// Encode returns the opaque cursor string.
func (c Cursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

// DecodeCursor parses an opaque cursor.
func DecodeCursor(s string) (Cursor, error) {
	invalid := &apperr.Error{Kind: apperr.KindInvalid, Code: "bad_request", Message: "invalid cursor"}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, invalid
	}
	ts, id, ok := strings.Cut(string(b), "|")
	if !ok {
		return Cursor{}, invalid
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return Cursor{}, invalid
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return Cursor{}, invalid
	}
	return Cursor{CreatedAt: t, ID: u}, nil
}

// StackPaths returns the repository path and state key for a stack (docs/03 stacks table).
func StackPaths(env, team, stack string) (path, stateKey string) {
	return fmt.Sprintf("environments/%s/%s/%s", env, team, stack), fmt.Sprintf("%s/%s/%s/terraform.tfstate", env, team, stack)
}
