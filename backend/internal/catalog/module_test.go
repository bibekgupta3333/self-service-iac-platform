package catalog

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
)

func mustSemVer(t *testing.T, s string) SemVer {
	t.Helper()
	v, err := ParseSemVer(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseSemVer(t *testing.T) {
	tests := []struct {
		in      string
		want    SemVer
		wantErr bool
	}{
		{in: "1.2.3", want: SemVer{1, 2, 3, ""}},
		{in: "0.1.0-rc.1", want: SemVer{0, 1, 0, "rc.1"}},
		{in: "v1.2.3", wantErr: true},
		{in: "1.2", wantErr: true},
		{in: "01.2.3", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseSemVer(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseSemVer(%q) = %+v, %v", tt.in, got, err)
		}
		if err == nil && got.String() != tt.in {
			t.Errorf("String() = %q, want %q", got.String(), tt.in)
		}
	}
}

func TestSemVerCompare(t *testing.T) {
	ordered := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i := 0; i < len(ordered)-1; i++ {
		a, b := mustSemVer(t, ordered[i]), mustSemVer(t, ordered[i+1])
		if a.Compare(b) >= 0 || b.Compare(a) <= 0 {
			t.Errorf("expected %s < %s", a, b)
		}
	}
	if mustSemVer(t, "1.2.3").Compare(mustSemVer(t, "1.2.3")) != 0 {
		t.Error("equal versions should compare 0")
	}
}

func TestLatestByModule(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	mk := func(id uuid.UUID, v string, st Status) ModuleVersion {
		return ModuleVersion{ModuleID: id, Version: mustSemVer(t, v), Status: st}
	}
	got := LatestByModule([]ModuleVersion{
		mk(a, "1.0.0", StatusActive), mk(a, "1.2.0", StatusActive), mk(a, "2.0.0", StatusYanked),
		mk(a, "1.3.0-rc.1", StatusActive),
		mk(b, "0.1.0-rc.1", StatusActive), mk(b, "0.1.0-rc.2", StatusDeprecated),
		mk(c, "1.0.0", StatusYanked),
	})
	if got[a].Version.String() != "1.2.0" {
		t.Errorf("module a latest = %s, want 1.2.0 (yanked and pre-release skipped)", got[a].Version)
	}
	if got[b].Version.String() != "0.1.0-rc.2" {
		t.Errorf("module b latest = %s, want newest pre-release when no release exists", got[b].Version)
	}
	if _, ok := got[c]; ok {
		t.Error("module with only yanked versions should have no latest")
	}
}

func TestSortVersionsDescDoesNotMutate(t *testing.T) {
	in := []ModuleVersion{{Version: SemVer{1, 0, 0, ""}}, {Version: SemVer{2, 0, 0, ""}}}
	out := SortVersionsDesc(in)
	if in[0].Version.Major != 1 || out[0].Version.Major != 2 {
		t.Fatal("SortVersionsDesc must return a sorted copy")
	}
}

func TestModuleChanges(t *testing.T) {
	before := Module{DisplayName: "SQS", Description: "q", Category: "messaging", Owners: []string{"platform"}}
	if got := ModuleChanges(before, before); len(got) != 0 {
		t.Fatalf("no-op re-index reported changes: %v", got)
	}
	after := before
	after.Owners = []string{"platform", "payments"}
	after.Description = "queue"
	got := ModuleChanges(before, after)
	if len(got) != 2 || got["description"].To != "queue" {
		t.Fatalf("changes = %+v", got)
	}
	if o, ok := got["owners"]; !ok || !slices.Equal(o.To.([]string), after.Owners) {
		t.Fatalf("owner change missing: %+v", got)
	}
}

func TestSourceAddress(t *testing.T) {
	got := SourceAddress("https://github.com/acme/iacp.git", "sqs", SemVer{1, 3, 0, ""})
	want := "git::https://github.com/acme/iacp.git//modules/sqs?ref=modules/sqs/v1.3.0"
	if got != want {
		t.Fatalf("SourceAddress = %q, want %q", got, want)
	}
}

func validIndexCommand() IndexCommand {
	return IndexCommand{
		Module: "sqs", Version: "1.3.0", GitTag: "modules/sqs/v1.3.0",
		CommitSHA:   strings.Repeat("9c", 20),
		DisplayName: "SQS queue", Description: "Encrypted SQS queue with DLQ", Category: "messaging",
		Owners: []string{"platform"},
		InputsSchema: json.RawMessage(`{"type":"object","required":["name"],"additionalProperties":false,
			"properties":{"name":{"type":"string"},"visibility_timeout_seconds":{"type":"integer","maximum":43200}}}`),
		Outputs:        []Output{{Name: "queue_url"}, {Name: "queue_arn"}, {Name: "dlq_arn"}},
		TFVersionRange: ">= 1.10, < 2.0", AWSProviderRange: "~> 5.80", ReleasedBy: "release-bot",
	}
}

func TestValidateIndex(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*IndexCommand)
		pointers []string
	}{
		{name: "valid", mutate: func(*IndexCommand) {}},
		{name: "bad name", mutate: func(c *IndexCommand) { c.Module = "S3!" }, pointers: []string{"/git_tag", "/module"}},
		{name: "tag mismatch", mutate: func(c *IndexCommand) { c.GitTag = "modules/sqs/v1.2.0" }, pointers: []string{"/git_tag"}},
		{name: "bad version", mutate: func(c *IndexCommand) { c.Version = "1.3" }, pointers: []string{"/version"}},
		{name: "bad sha", mutate: func(c *IndexCommand) { c.CommitSHA = "main" }, pointers: []string{"/commit_sha"}},
		{name: "bad category", mutate: func(c *IndexCommand) { c.Category = "misc" }, pointers: []string{"/category"}},
		{name: "bad schema", mutate: func(c *IndexCommand) { c.InputsSchema = json.RawMessage(`{"type":5}`) }, pointers: []string{"/inputs_schema"}},
		{name: "duplicate outputs", mutate: func(c *IndexCommand) { c.Outputs = append(c.Outputs, Output{Name: "queue_url"}) }, pointers: []string{"/outputs"}},
		{name: "missing fields", mutate: func(c *IndexCommand) { c.DisplayName, c.Owners = "", nil }, pointers: []string{"/display_name", "/owners"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := validIndexCommand()
			tt.mutate(&cmd)
			m, v, err := ValidateIndex(cmd)
			if len(tt.pointers) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if m.RepoPath != "modules/sqs" || v.Version.String() != "1.3.0" || len(v.Manifest) == 0 {
					t.Fatalf("unexpected result: %+v %+v", m, v)
				}
				return
			}
			e, ok := apperr.As(err)
			if !ok || e.Kind != apperr.KindInvalid {
				t.Fatalf("want validation error, got %v", err)
			}
			got := fp.Map(e.Fields, func(f apperr.FieldError) string { return f.Pointer })
			slices.Sort(got)
			if !slices.Equal(got, tt.pointers) {
				t.Fatalf("pointers = %v, want %v", got, tt.pointers)
			}
		})
	}
}
