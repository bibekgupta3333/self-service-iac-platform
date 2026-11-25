package generator

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tfname"
)

var hclIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// valueTokens converts a decoded JSON value into HCL expression tokens.
// Strings are escaped by hclwrite (including "${" template sequences), so user input
// can never inject Terraform expressions; only {"$ref"} objects become references.
func valueTokens(v any) (hclwrite.Tokens, error) {
	if ref, isRef, err := tfname.ParseRef(v); isRef {
		if err != nil {
			return nil, err
		}
		return moduleRef(ref.Resource, ref.Output), nil
	}
	switch t := v.(type) {
	case nil:
		return hclwrite.TokensForValue(cty.NullVal(cty.DynamicPseudoType)), nil
	case bool:
		return hclwrite.TokensForValue(cty.BoolVal(t)), nil
	case string:
		return hclwrite.TokensForValue(cty.StringVal(t)), nil
	case json.Number:
		f, _, err := big.ParseFloat(t.String(), 10, 512, big.ToNearestEven)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", t, err)
		}
		return hclwrite.TokensForValue(cty.NumberVal(f)), nil
	case []any:
		elems := make([]hclwrite.Tokens, 0, len(t))
		for _, e := range t {
			et, err := valueTokens(e)
			if err != nil {
				return nil, err
			}
			elems = append(elems, et)
		}
		return hclwrite.TokensForTuple(elems), nil
	case map[string]any:
		attrs := make([]hclwrite.ObjectAttrTokens, 0, len(t))
		for _, k := range fp.SortedKeys(t) {
			vt, err := valueTokens(t[k])
			if err != nil {
				return nil, err
			}
			attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: keyTokens(k), Value: vt})
		}
		return hclwrite.TokensForObject(attrs), nil
	}
	return nil, fmt.Errorf("unsupported input value of type %T", v)
}

// keyTokens renders an object key bare when it is an identifier, quoted otherwise.
func keyTokens(k string) hclwrite.Tokens {
	if hclIdentRe.MatchString(k) {
		return hclwrite.TokensForIdentifier(k)
	}
	return hclwrite.TokensForValue(cty.StringVal(k))
}

func moduleRef(inst, output string) hclwrite.Tokens {
	return hclwrite.TokensForTraversal(hcl.Traversal{
		hcl.TraverseRoot{Name: "module"},
		hcl.TraverseAttr{Name: inst},
		hcl.TraverseAttr{Name: output},
	})
}

// stringMap renders a map of string keys and values, sorted by key.
func stringMap(m map[string]string) hclwrite.Tokens {
	attrs := make([]hclwrite.ObjectAttrTokens, 0, len(m))
	for _, k := range fp.SortedKeys(m) {
		attrs = append(attrs, hclwrite.ObjectAttrTokens{Name: keyTokens(k), Value: hclwrite.TokensForValue(cty.StringVal(m[k]))})
	}
	return hclwrite.TokensForObject(attrs)
}

// Attr is one attribute of a generated block. Value is a Go string, bool, int,
// []string, or map[string]string.
type Attr struct {
	Name  string
	Value any
}

func setAttr(body *hclwrite.Body, a Attr) error {
	switch v := a.Value.(type) {
	case string:
		body.SetAttributeValue(a.Name, cty.StringVal(v))
	case bool:
		body.SetAttributeValue(a.Name, cty.BoolVal(v))
	case int:
		body.SetAttributeValue(a.Name, cty.NumberIntVal(int64(v)))
	case []string:
		if len(v) == 0 {
			body.SetAttributeValue(a.Name, cty.ListValEmpty(cty.String))
		} else {
			body.SetAttributeValue(a.Name, cty.ListVal(fp.Map(v, cty.StringVal)))
		}
	case map[string]string:
		body.SetAttributeRaw(a.Name, stringMap(v))
	default:
		return fmt.Errorf("attribute %s: unsupported type %T", a.Name, a.Value)
	}
	return nil
}

// finish prefixes the header and formats like `terraform fmt`.
func finish(f *hclwrite.File, comments ...string) []byte {
	var b []byte
	b = append(b, Header...)
	b = append(b, '\n')
	for _, c := range comments {
		b = append(b, "# "+c+"\n"...)
	}
	if len(comments) > 0 {
		b = append(b, '\n')
	}
	b = append(b, f.Bytes()...)
	return hclwrite.Format(b)
}
