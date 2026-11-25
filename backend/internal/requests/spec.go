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
)

// CreateInput is the body of POST /v1/requests (docs/03 §6.4).
type CreateInput struct {
	Kind          Kind            `json:"kind"`
	Team          string          `json:"team"`
	Environment   string          `json:"environment"`
	Stack         string          `json:"stack"`
	Region        string          `json:"region,omitempty"`
	Ephemeral     *Ephemeral      `json:"ephemeral,omitempty"`
	Justification string          `json:"justification,omitempty"`
	Resources     []ResourceInput `json:"resources"`
	DependsOn     []string        `json:"depends_on,omitempty"`
}

// Ephemeral requests a stack that is destroyed after TTLHours.
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
	slugRe         = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}$`)
	stackNameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	instanceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	refRe          = regexp.MustCompile(`^([a-z][a-z0-9_]{0,62})\.([a-z][a-z0-9_]*)$`)
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
	case in.Kind != KindCreate:
		add("/kind", "unsupported_kind", fmt.Sprintf("kind %q is not supported yet; only create is implemented", in.Kind))
	}
	if !slugRe.MatchString(in.Team) {
		add("/team", "pattern", "must be a team slug (^[a-z][a-z0-9-]{1,38}$)")
	}
	if in.Environment == "" {
		add("/environment", "required", "is required")
	}
	if !stackNameRe.MatchString(in.Stack) {
		add("/stack", "pattern", "must match ^[a-z][a-z0-9-]{1,40}$")
	}
	if in.Ephemeral != nil && in.Ephemeral.TTLHours <= 0 {
		add("/ephemeral/ttl_hours", "minimum", "must be greater than 0")
	}
	if len(in.DependsOn) > 0 {
		add("/depends_on", "unsupported", "cross-stack dependencies are not supported yet")
	}
	if len(in.Justification) > 2000 {
		add("/justification", "maxLength", "must be at most 2000 characters")
	}
	switch {
	case len(in.Resources) == 0:
		add("/resources", "minItems", "at least one resource is required")
	case len(in.Resources) > MaxResources:
		add("/resources", "maxItems", fmt.Sprintf("at most %d resources are allowed", MaxResources))
	}
	for i, r := range in.Resources {
		ptr := fmt.Sprintf("/resources/%d", i)
		if !instanceNameRe.MatchString(r.Name) {
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
	return errs
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
			errs = append(errs, apperr.FieldError{Pointer: ptr + "/inputs", Code: "type", Message: "must be a JSON object"})
			continue
		}
		masked, refs := maskRefs(doc, "")
		for _, ref := range refs {
			if msg := checkRef(ref.target, r.Input.Name, byName); msg != "" {
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
	target  string // "<instance>.<output>"
}

// refPlaceholder replaces references during schema validation.
const refPlaceholder = "${iacp:ref}"

// maskRefs returns a copy of v with every {"$ref": "..."} object replaced by a placeholder
// string, plus the location of each reference. Pure.
func maskRefs(v any, ptr string) (any, []inputRef) {
	switch t := v.(type) {
	case map[string]any:
		if target, ok := AsRef(t); ok {
			return refPlaceholder, []inputRef{{pointer: ptr, target: target}}
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

// AsRef reports whether m is a reference object {"$ref": "<instance>.<output>"}.
func AsRef(m map[string]any) (string, bool) {
	if len(m) != 1 {
		return "", false
	}
	s, ok := m["$ref"].(string)
	return s, ok
}

func checkRef(target, self string, byName map[string]catalog.ModuleVersion) string {
	m := refRe.FindStringSubmatch(target)
	if m == nil {
		return fmt.Sprintf("reference %q must have the form <resource>.<output>", target)
	}
	inst, out := m[1], m[2]
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
