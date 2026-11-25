package inputschema

import (
	"encoding/json"
	"testing"
)

const sqsSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["name"],
  "additionalProperties": false,
  "properties": {
    "name": { "type": "string", "pattern": "^[a-z][a-z0-9-]{2,70}$" },
    "visibility_timeout_seconds": { "type": "integer", "minimum": 0, "maximum": 43200 },
    "fifo": { "type": "boolean" }
  }
}`

func TestValidate(t *testing.T) {
	s, err := Compile(json.RawMessage(sqsSchema))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input string
		want  []Violation
	}{
		{name: "valid", input: `{"name":"orders-dev","visibility_timeout_seconds":60}`},
		{name: "missing required", input: `{}`, want: []Violation{{Pointer: "", Code: "required"}}},
		{
			name:  "maximum and unknown",
			input: `{"name":"orders-dev","visibility_timeout_seconds":99999,"colour":"red"}`,
			want:  []Violation{{Pointer: "", Code: "additionalProperties"}, {Pointer: "/visibility_timeout_seconds", Code: "maximum"}},
		},
		{name: "wrong type", input: `{"name":"orders-dev","fifo":"yes"}`, want: []Violation{{Pointer: "/fifo", Code: "type"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst, err := Decode(json.RawMessage(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			got := s.Validate(inst)
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i].Pointer != tt.want[i].Pointer || got[i].Code != tt.want[i].Code || got[i].Message == "" {
					t.Errorf("violation %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCompileRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":      `{`,
		"not an object": `[]`,
		"bad keyword":   `{"type": 12}`,
		"remote ref":    `{"$ref": "https://example.com/schema.json"}`,
		"file ref":      `{"$ref": "file:///etc/passwd"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(json.RawMessage(raw)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestHugeNumbersDoNotPanic(t *testing.T) {
	for _, raw := range []string{
		`{"name":"orders-dev","visibility_timeout_seconds":1e9999999}`,
		`{"name":"orders-dev","visibility_timeout_seconds":-1e9999999}`,
		`{"name":"orders-dev","nested":[{"x":1e400}]}`,
	} {
		if _, err := Decode(json.RawMessage(raw)); err == nil {
			t.Errorf("Decode(%s) accepted an out-of-range number", raw)
		}
	}
	if _, err := Decode(json.RawMessage(`{"big":9007199254740993,"small":1e-300}`)); err != nil {
		t.Errorf("in-range numbers rejected: %v", err)
	}
	// Even if a huge number reaches Validate directly, the library panic is contained.
	num, err := Compile(json.RawMessage(`{"type":"object","properties":{"n":{"type":"number","minimum":0,"maximum":10}}}`))
	if err != nil {
		t.Fatal(err)
	}
	inst := map[string]any{"n": json.Number("1e9999999")}
	func() {
		defer func() {
			if recover() == nil {
				t.Log("library no longer panics on huge numbers; the guard is now belt-and-braces")
			}
		}()
		_ = num.s.Validate(inst)
	}()
	got := num.Validate(inst)
	if len(got) != 1 || got[0].Code != "invalid" {
		t.Fatalf("got %+v, want one invalid violation", got)
	}
}

func TestPointer(t *testing.T) {
	if got := Pointer([]string{"a/b", "c~d", "0"}); got != "/a~1b/c~0d/0" {
		t.Fatalf("Pointer = %q", got)
	}
}
