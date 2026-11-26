package requests

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/inputschema"
)

func pointers(errs []apperr.FieldError) []string {
	p := fp.Map(errs, func(e apperr.FieldError) string { return e.Pointer + ":" + e.Code })
	slices.Sort(p)
	return p
}

func validInput() CreateInput {
	return CreateInput{
		Kind: KindCreate, Team: "payments", Environment: "development", Stack: "order-events",
		Resources: []ResourceInput{
			{Name: "orders_queue", Module: "sqs", Version: "1.3.0", Inputs: json.RawMessage(`{"name":"payments-orders-dev"}`)},
			{Name: "order_processor", Module: "lambda", Version: "2.0.1",
				Inputs: json.RawMessage(`{"name":"payments-processor-dev","queue_arn":{"$ref":"orders_queue.queue_arn"},
					"env":{"QUEUE_URL":{"$ref":"orders_queue.queue_url"}}}`)},
		},
	}
}

func TestValidateShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateInput)
		want   []string
	}{
		{name: "valid", mutate: func(*CreateInput) {}},
		{name: "unsupported kind", mutate: func(c *CreateInput) { c.Kind = KindDestroy }, want: []string{"/kind:unsupported_kind"}},
		{name: "unknown kind", mutate: func(c *CreateInput) { c.Kind = "delete" }, want: []string{"/kind:enum"}},
		{name: "bad names", mutate: func(c *CreateInput) { c.Team, c.Stack = "Payments", "x" }, want: []string{"/stack:pattern", "/team:pattern"}},
		{name: "no resources", mutate: func(c *CreateInput) { c.Resources = nil }, want: []string{"/resources:minItems"}},
		{name: "duplicate resource", mutate: func(c *CreateInput) { c.Resources[1].Name = "orders_queue" }, want: []string{"/resources:duplicate"}},
		{name: "bad instance and version", mutate: func(c *CreateInput) { c.Resources[0].Name, c.Resources[0].Version = "Queue", "latest" },
			want: []string{"/resources/0/name:pattern", "/resources/0/version:pattern"}},
		{name: "bad ttl", mutate: func(c *CreateInput) { c.Ephemeral = &Ephemeral{TTLHours: 0} }, want: []string{"/ephemeral/ttl_hours:minimum"}},
		{name: "depends_on", mutate: func(c *CreateInput) { c.DependsOn = []string{"x"} }, want: []string{"/depends_on:unsupported"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.mutate(&in)
			if got := pointers(ValidateShape(in)); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func mustSchema(t *testing.T, raw string) *inputschema.Schema {
	t.Helper()
	s, err := inputschema.Compile(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func resolvedFixture(t *testing.T, in CreateInput) []ResolvedResource {
	t.Helper()
	sqs := catalog.ModuleVersion{ModuleName: "sqs", Version: catalog.SemVer{Major: 1, Minor: 3}, Status: "active",
		Outputs: []catalog.Output{{Name: "queue_arn"}, {Name: "queue_url"}}}
	lambda := catalog.ModuleVersion{ModuleName: "lambda", Version: catalog.SemVer{Major: 2, Patch: 1}, Status: "active",
		Outputs: []catalog.Output{{Name: "function_arn"}}}
	sqsSchema := mustSchema(t, `{"type":"object","required":["name"],"additionalProperties":false,
		"properties":{"name":{"type":"string","pattern":"^[a-z][a-z0-9-]{2,70}$"},"visibility_timeout_seconds":{"type":"integer","maximum":43200}}}`)
	lambdaSchema := mustSchema(t, `{"type":"object","required":["name","queue_arn"],"additionalProperties":false,
		"properties":{"name":{"type":"string"},"queue_arn":{"type":"string","pattern":"^arn:"},
		"env":{"type":"object","additionalProperties":{"type":"string"}}}}`)
	var out []ResolvedResource
	for _, r := range in.Resources {
		switch r.Module {
		case "sqs":
			out = append(out, ResolvedResource{Input: r, Version: sqs, Schema: sqsSchema})
		default:
			out = append(out, ResolvedResource{Input: r, Version: lambda, Schema: lambdaSchema})
		}
	}
	return out
}

func TestValidateResources(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateInput)
		yank   bool
		want   []string
	}{
		{name: "valid with refs (pattern ^arn: not applied to refs)", mutate: func(*CreateInput) {}},
		{name: "schema violations", mutate: func(c *CreateInput) {
			c.Resources[0].Inputs = json.RawMessage(`{"name":"X","visibility_timeout_seconds":99999,"colour":"red"}`)
		}, want: []string{"/resources/0/inputs/name:pattern", "/resources/0/inputs/visibility_timeout_seconds:maximum", "/resources/0/inputs:additionalProperties"}},
		{name: "missing inputs object", mutate: func(c *CreateInput) { c.Resources[0].Inputs = nil },
			want: []string{"/resources/0/inputs:required"}},
		{name: "unknown output", mutate: func(c *CreateInput) {
			c.Resources[1].Inputs = json.RawMessage(`{"name":"p","queue_arn":{"$ref":"orders_queue.dlq"}}`)
		}, want: []string{"/resources/1/inputs/queue_arn:invalid_ref"}},
		{name: "unknown instance", mutate: func(c *CreateInput) {
			c.Resources[1].Inputs = json.RawMessage(`{"name":"p","queue_arn":{"$ref":"nope.queue_arn"}}`)
		}, want: []string{"/resources/1/inputs/queue_arn:invalid_ref"}},
		{name: "self reference", mutate: func(c *CreateInput) {
			c.Resources[1].Inputs = json.RawMessage(`{"name":"p","queue_arn":{"$ref":"order_processor.function_arn"}}`)
		}, want: []string{"/resources/1/inputs/queue_arn:invalid_ref"}},
		{name: "malformed ref", mutate: func(c *CreateInput) {
			c.Resources[1].Inputs = json.RawMessage(`{"name":"p","queue_arn":{"$ref":"orders_queue"}}`)
		}, want: []string{"/resources/1/inputs/queue_arn:invalid_ref"}},
		{name: "not an object", mutate: func(c *CreateInput) { c.Resources[0].Inputs = json.RawMessage(`[1]`) },
			want: []string{"/resources/0/inputs:type"}},
		{name: "reserved and invalid input names are caught at create time", mutate: func(c *CreateInput) {
			// The lambda fixture schema has no additionalProperties limit on env, so only the name rules catch these.
			c.Resources[1].Inputs = json.RawMessage(`{"name":"p","queue_arn":{"$ref":"orders_queue.queue_arn"},"env":{}}`)
			c.Resources[0].Inputs = json.RawMessage(`{"name":"orders-dev","source":"evil","Bad-Name":1}`)
		}, want: []string{"/resources/0/inputs/Bad-Name:invalid_input_name", "/resources/0/inputs/source:invalid_input_name",
			"/resources/0/inputs:additionalProperties"}},
		{name: "non-string ref", mutate: func(c *CreateInput) {
			c.Resources[1].Inputs = json.RawMessage(`{"name":"p","queue_arn":{"$ref":42}}`)
		}, want: []string{"/resources/1/inputs/queue_arn:invalid_ref"}},
		{name: "huge number is rejected, not a panic", mutate: func(c *CreateInput) {
			c.Resources[0].Inputs = json.RawMessage(`{"name":"orders-dev","visibility_timeout_seconds":1e9999999}`)
		}, want: []string{"/resources/0/inputs:type"}},
		{name: "yanked", mutate: func(*CreateInput) {}, yank: true, want: []string{"/resources/0/version:module_version_unavailable"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.mutate(&in)
			res := resolvedFixture(t, in)
			if tt.yank {
				res[0].Version.Status = catalog.StatusYanked
			}
			if got := pointers(ValidateResources(res)); !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateEnvironment(t *testing.T) {
	maxTTL := 168
	env := Environment{Name: "development", AllowedRegions: []string{"us-east-1"}, AllowEphemeral: true, MaxTTLHours: &maxTTL}
	in := validInput()
	if errs := ValidateEnvironment(in, env, "us-east-1"); len(errs) != 0 {
		t.Fatalf("unexpected %v", errs)
	}
	in.Ephemeral = &Ephemeral{TTLHours: 500}
	got := pointers(ValidateEnvironment(in, env, "eu-west-1"))
	if !slices.Equal(got, []string{"/ephemeral/ttl_hours:maximum", "/region:region_not_allowed"}) {
		t.Fatalf("got %v", got)
	}
	env.AllowEphemeral = false
	if got := pointers(ValidateEnvironment(in, env, "us-east-1")); !slices.Equal(got, []string{"/ephemeral:ephemeral_not_allowed"}) {
		t.Fatalf("got %v", got)
	}
}

func TestNormalizeSortsResources(t *testing.T) {
	n := Normalize(validInput(), "us-east-1")
	if n.Resources[0].Name != "order_processor" || n.Region != "us-east-1" || n.DependsOn == nil {
		t.Fatalf("normalize = %+v", n)
	}
}

func TestRequestHash(t *testing.T) {
	a := RequestHash("POST", "/v1/requests", []byte(`{"a":1}`))
	if len(a) != 64 || a == RequestHash("POST", "/v1/requests", []byte(`{"a":2}`)) {
		t.Fatalf("hash = %s", a)
	}
	if a != RequestHash("POST", "/v1/requests", []byte(`{"a":1}`)) {
		t.Fatal("hash must be deterministic")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{CreatedAt: time.Date(2026, 11, 24, 10, 2, 11, 123456000, time.UTC), ID: uuid.New()}
	got, err := DecodeCursor(c.Encode())
	if err != nil || !got.CreatedAt.Equal(c.CreatedAt) || got.ID != c.ID {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	for _, bad := range []string{"!!", "bm9waXBl", strings.Repeat("A", 10)} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Errorf("DecodeCursor(%q) should fail", bad)
		}
	}
}

func TestStackPaths(t *testing.T) {
	p, k := StackPaths("development", "payments", "order-events")
	if p != "environments/development/payments/order-events" || k != "development/payments/order-events/terraform.tfstate" {
		t.Fatalf("paths = %s %s", p, k)
	}
}
