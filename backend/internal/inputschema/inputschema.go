// Package inputschema compiles module input schemas (JSON Schema 2020-12) and turns
// validation failures into flat, pointer-addressed violations. It performs no I/O:
// remote and file $refs are refused.
package inputschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Schema is a compiled input schema.
type Schema struct{ s *jsonschema.Schema }

// Violation is one failed constraint, located by a JSON pointer into the instance.
type Violation struct {
	Pointer string // RFC 6901, relative to the validated instance ("" = root)
	Code    string // failing keyword, e.g. "maximum", "required", "additionalProperties"
	Message string
}

type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not allowed", url)
}

const resourceURL = "urn:iacp:inputs-schema"

// Compile parses and compiles raw as a JSON Schema (2020-12 unless $schema says otherwise).
func Compile(raw json.RawMessage) (*Schema, error) {
	doc, err := Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	if _, ok := doc.(map[string]any); !ok {
		return nil, errors.New("schema must be a JSON object")
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(noLoader{})
	if err := c.AddResource(resourceURL, doc); err != nil {
		return nil, fmt.Errorf("add schema: %w", err)
	}
	s, err := c.Compile(resourceURL)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return &Schema{s: s}, nil
}

// Decode parses JSON into the value model the validator expects (json.Number for numbers).
// Numbers outside the float64 range (for example 1e9999999) are rejected: no module input
// needs them, and the validator library cannot compare them safely.
func Decode(raw json.RawMessage) (any, error) {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if ptr, ok := firstOutOfRange(v, ""); !ok {
		return nil, fmt.Errorf("number at %q is out of range", ptr)
	}
	return v, nil
}

// firstOutOfRange walks v and returns the pointer of the first number that does not fit
// in a float64. Pure.
func firstOutOfRange(v any, ptr string) (string, bool) {
	switch t := v.(type) {
	case json.Number:
		if _, err := strconv.ParseFloat(t.String(), 64); err != nil {
			return ptr, false
		}
	case map[string]any:
		for k, e := range t {
			if p, ok := firstOutOfRange(e, ptr+Pointer([]string{k})); !ok {
				return p, false
			}
		}
	case []any:
		for i, e := range t {
			if p, ok := firstOutOfRange(e, ptr+"/"+strconv.Itoa(i)); !ok {
				return p, false
			}
		}
	}
	return "", true
}

// Validate checks instance (as returned by Decode) and returns violations sorted by pointer.
// A panic inside the validator library becomes a single violation instead of a crash.
func (s *Schema) Validate(instance any) (out []Violation) {
	defer func() {
		if p := recover(); p != nil {
			out = []Violation{{Code: "invalid", Message: "input could not be validated"}}
		}
	}()
	err := s.s.Validate(instance)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Violation{{Code: "invalid", Message: err.Error()}}
	}
	p := message.NewPrinter(language.English)
	out = leaves(ve, p, nil)
	slices.SortFunc(out, func(a, b Violation) int {
		if c := strings.Compare(a.Pointer, b.Pointer); c != 0 {
			return c
		}
		return strings.Compare(a.Code, b.Code)
	})
	return slices.CompactFunc(out, func(a, b Violation) bool { return a == b })
}

func leaves(e *jsonschema.ValidationError, p *message.Printer, acc []Violation) []Violation {
	if len(e.Causes) == 0 {
		code := "invalid"
		if kp := e.ErrorKind.KeywordPath(); len(kp) > 0 {
			code = kp[0]
		}
		return append(acc, Violation{
			Pointer: Pointer(e.InstanceLocation),
			Code:    code,
			Message: e.ErrorKind.LocalizedString(p),
		})
	}
	for _, c := range e.Causes {
		acc = leaves(c, p, acc)
	}
	return acc
}

// Pointer encodes path segments as an RFC 6901 JSON pointer.
func Pointer(segments []string) string {
	var b strings.Builder
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(s))
	}
	return b.String()
}
