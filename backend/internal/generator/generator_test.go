package generator

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

const repoURL = "https://github.com/bibekgupta3333/self-service-iac-platform.git"

// orderEventsSpec is the S3 + SQS + Lambda example from docs/01 §5.2.
func orderEventsSpec() StackSpec {
	return StackSpec{
		Environment: "development", Team: "payments", Stack: "order-events", Region: "us-east-1",
		CostCenter: "cc-4102", RequestID: "req-7k3p9x", FencingToken: 48213,
		TerraformVersion: "~> 1.13", AWSProviderVersion: "~> 5.80",
		Resources: []Resource{
			{
				Name: "order_processor", Module: "lambda", Version: catalog.SemVer{Major: 2, Patch: 1},
				GitSHA: "8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c",
				Inputs: json.RawMessage(`{
					"name": "payments-order-processor-dev", "runtime": "provided.al2023", "handler": "bootstrap",
					"memory_size": 256, "timeout": 30,
					"sqs_event_source": {"queue_arn": {"$ref": "orders_queue.queue_arn"}, "batch_size": 10},
					"s3_write_bucket_arns": [{"$ref": "events_bucket.bucket_arn"}],
					"environment_variables": {
						"QUEUE_URL": {"$ref": "orders_queue.queue_url"},
						"BUCKET_NAME": {"$ref": "events_bucket.bucket_name"}
					}
				}`),
				Outputs: []catalog.Output{{Name: "function_name"}, {Name: "function_arn"}, {Name: "role_arn"}},
			},
			{
				Name: "events_bucket", Module: "s3", Version: catalog.SemVer{Major: 1, Minor: 2},
				GitSHA: "4e1c9a7b2d8f0e3a5c6b7d8e9f0a1b2c3d4e5f60",
				Inputs: json.RawMessage(`{"name": "payments-order-events-dev", "versioning_enabled": true, "force_destroy": false,
					"lifecycle_rules": [{"id": "expire-raw", "prefix": "raw/", "expiration_days": 30}]}`),
				Outputs: []catalog.Output{{Name: "bucket_name"}, {Name: "bucket_arn"}},
			},
			{
				Name: "orders_queue", Module: "sqs", Version: catalog.SemVer{Major: 1, Minor: 3},
				GitSHA: "9c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d",
				Inputs: json.RawMessage(`{"name": "payments-order-events-dev", "visibility_timeout_seconds": 60,
					"message_retention_seconds": 345600, "dlq_max_receive_count": 5, "description": "uses ${var.x} literally",
					"extra_tags": {"a-b": 1.5, "with space": null, "Owner": "team"}}`),
				Outputs: []catalog.Output{{Name: "queue_url"}, {Name: "queue_arn"}, {Name: "dlq_arn"}, {Name: "kms_key_arn", Sensitive: true}},
			},
		},
	}
}

func TestGolden(t *testing.T) {
	tests := []struct {
		name string
		gen  *Generator
		spec StackSpec
	}{
		{name: "localstack-local-backend", gen: New(repoURL, LocalBackend{}, LocalStackProvider{Endpoint: "http://localhost:4566"}), spec: orderEventsSpec()},
		{name: "aws-s3-backend", gen: New(repoURL,
			S3Backend{Bucket: "iacp-tfstate-111111111111", Region: "us-east-1", KMSKeyID: "arn:aws:kms:us-east-1:111111111111:alias/iacp-tfstate"},
			AWSProvider{AllowedAccountIDs: []string{"222222222222"}}), spec: orderEventsSpec()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := tt.gen.Render(tt.spec)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{FileBackend, FileStackJSON, FileMain, FileOutputs, FileProviders, FileVariables, FileVersions}
			if !slices.Equal(files.Names(), want) {
				t.Fatalf("files = %v, want %v", files.Names(), want)
			}
			dir := filepath.Join("testdata", "golden", tt.name)
			for _, name := range files.Names() {
				path := filepath.Join(dir, name)
				if *update {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, files[name], 0o644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				golden, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden (run go test ./internal/generator -update): %v", err)
				}
				if !bytes.Equal(golden, files[name]) {
					t.Errorf("%s differs from golden file %s:\n--- got ---\n%s\n--- want ---\n%s", name, path, files[name], golden)
				}
			}
		})
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	g := New(repoURL, LocalBackend{}, LocalStackProvider{Endpoint: "http://localhost:4566"})
	a, err := g.Render(orderEventsSpec())
	if err != nil {
		t.Fatal(err)
	}
	shuffled := orderEventsSpec()
	slices.Reverse(shuffled.Resources)
	for i := 0; i < 20; i++ { // map iteration order differs between runs
		b, err := g.Render(shuffled)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range a.Names() {
			if !bytes.Equal(a[name], b[name]) {
				t.Fatalf("%s is not deterministic", name)
			}
		}
	}
}

func TestEveryFileHasHeaderAndNoTimestamps(t *testing.T) {
	files, err := New(repoURL, LocalBackend{}, LocalStackProvider{Endpoint: "http://localhost:4566"}).Render(orderEventsSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files.Names() {
		if strings.HasSuffix(name, ".tf") && !bytes.HasPrefix(files[name], []byte(Header+"\n")) {
			t.Errorf("%s does not start with the generated-file header", name)
		}
		if bytes.Contains(files[name], []byte("2026-")) {
			t.Errorf("%s contains a timestamp", name)
		}
	}
	main := string(files[FileMain])
	if !strings.Contains(main, `"uses $${var.x} literally"`) {
		t.Errorf("template sequences in user strings must be escaped:\n%s", main)
	}
	if strings.Contains(main, "?ref=main") || !strings.Contains(main, "?ref=modules/sqs/v1.3.0") {
		t.Errorf("module sources must be pinned to release tags")
	}
}

// TestTerraformFmtClean runs `terraform fmt -check` on every rendered .tf file.
func TestTerraformFmtClean(t *testing.T) {
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH")
	}
	gens := []*Generator{
		New(repoURL, LocalBackend{}, LocalStackProvider{Endpoint: "http://localhost:4566"}),
		New(repoURL, S3Backend{Bucket: "b", Region: "us-east-1", KMSKeyID: "k"}, AWSProvider{AllowedAccountIDs: []string{"222222222222"}}),
	}
	for _, g := range gens {
		files, err := g.Render(orderEventsSpec())
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		for _, name := range files.Names() {
			if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, err := exec.Command(tf, "fmt", "-check", "-diff", dir).CombinedOutput()
		if err != nil {
			t.Fatalf("terraform fmt -check failed: %v\n%s", err, out)
		}
	}
}

// TestTflintClean lints rendered roots with the repository's .tflint.hcl, the same
// check CI runs over environments/ and the golden files.
func TestTflintClean(t *testing.T) {
	tflint, err := exec.LookPath("tflint")
	if err != nil {
		t.Skip("tflint not on PATH")
	}
	cfg, err := filepath.Abs("../../../.tflint.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Skip("repository .tflint.hcl not found")
	}
	if out, err := exec.Command(tflint, "--init", "--config", cfg).CombinedOutput(); err != nil {
		t.Skipf("tflint plugins unavailable (offline?): %v\n%s", err, out)
	}
	files, err := New(repoURL, LocalBackend{}, LocalStackProvider{Endpoint: "http://localhost:4566"}).Render(orderEventsSpec())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range files.Names() {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(tflint, "--config", cfg, "--format", "compact")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tflint found issues in rendered files: %v\n%s", err, out)
	}
}

func TestRenderRejectsInvalidSpecs(t *testing.T) {
	g := New(repoURL, LocalBackend{}, LocalStackProvider{Endpoint: "http://localhost:4566"})
	tests := []struct {
		name   string
		mutate func(*StackSpec)
		want   string
	}{
		{name: "identity", mutate: func(s *StackSpec) { s.Team = "Payments" }, want: "invalid stack identity"},
		{name: "unpinned sha", mutate: func(s *StackSpec) { s.Resources[0].GitSHA = "main" }, want: "commit SHA"},
		{name: "duplicate", mutate: func(s *StackSpec) { s.Resources[1].Name = s.Resources[0].Name }, want: "duplicate"},
		{name: "unknown ref target", mutate: func(s *StackSpec) {
			s.Resources[1].Inputs = json.RawMessage(`{"x": {"$ref": "nope.queue_arn"}}`)
		}, want: "unknown resource"},
		{name: "unknown output", mutate: func(s *StackSpec) {
			s.Resources[1].Inputs = json.RawMessage(`{"x": {"$ref": "orders_queue.nope"}}`)
		}, want: "no output"},
		{name: "self ref", mutate: func(s *StackSpec) {
			s.Resources[1].Inputs = json.RawMessage(`{"x": {"$ref": "events_bucket.bucket_arn"}}`)
		}, want: "itself"},
		{name: "malformed ref", mutate: func(s *StackSpec) {
			s.Resources[1].Inputs = json.RawMessage(`{"x": {"$ref": "module.x.y"}}`)
		}, want: "malformed"},
		{name: "reserved input", mutate: func(s *StackSpec) { s.Resources[1].Inputs = json.RawMessage(`{"source": "evil"}`) }, want: "reserved"},
		{name: "bad input name", mutate: func(s *StackSpec) { s.Resources[1].Inputs = json.RawMessage(`{"Bad-Name": 1}`) }, want: "identifier"},
		{name: "not an object", mutate: func(s *StackSpec) { s.Resources[1].Inputs = json.RawMessage(`[1]`) }, want: "JSON object"},
		{name: "no versions", mutate: func(s *StackSpec) { s.TerraformVersion = "" }, want: "version constraints"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := orderEventsSpec()
			tt.mutate(&spec)
			if _, err := g.Render(spec); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestStateKey(t *testing.T) {
	id := orderEventsSpec().Identity()
	if id.StateKey() != "development/payments/order-events/terraform.tfstate" {
		t.Fatalf("StateKey = %s", id.StateKey())
	}
}
