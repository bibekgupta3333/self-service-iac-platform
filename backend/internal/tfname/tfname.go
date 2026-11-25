// Package tfname holds the pure naming rules shared by request validation and the
// config generator: Terraform identifiers, module input names and intra-stack
// references ({"$ref": "<resource>.<output>"}). Keeping them in one place means a
// request accepted at create time always renders.
package tfname

import (
	"fmt"
	"regexp"
	"slices"
)

var (
	identRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	refRe   = regexp.MustCompile(`^([a-z][a-z0-9_]{0,62})\.([a-z][a-z0-9_]{0,62})$`)
)

// ReservedInputs are module block arguments owned by Terraform itself.
var ReservedInputs = []string{"count", "depends_on", "for_each", "providers", "source", "version"}

// IsIdentifier reports whether s is a lowercase Terraform identifier (resource and input names).
func IsIdentifier(s string) bool { return identRe.MatchString(s) }

// ValidateInputName checks that name can be a module argument.
func ValidateInputName(name string) error {
	if !IsIdentifier(name) {
		return fmt.Errorf("input name %q is not an identifier (^[a-z][a-z0-9_]{0,62}$)", name)
	}
	if slices.Contains(ReservedInputs, name) {
		return fmt.Errorf("input name %q is reserved by Terraform", name)
	}
	return nil
}

// Ref is a reference to another resource's output in the same stack.
type Ref struct {
	Resource string
	Output   string
}

func (r Ref) String() string { return r.Resource + "." + r.Output }

// ParseRef inspects a decoded JSON value. isRef is true when v is an object whose only
// key is "$ref"; err is set when such an object is malformed.
func ParseRef(v any) (ref Ref, isRef bool, err error) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return Ref{}, false, nil
	}
	raw, ok := m["$ref"]
	if !ok {
		return Ref{}, false, nil
	}
	s, ok := raw.(string)
	if !ok {
		return Ref{}, true, fmt.Errorf("reference must be a string, got %T", raw)
	}
	parts := refRe.FindStringSubmatch(s)
	if parts == nil {
		return Ref{}, true, fmt.Errorf("reference %q must have the form <resource>.<output>", s)
	}
	return Ref{Resource: parts[1], Output: parts[2]}, true, nil
}
