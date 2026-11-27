package tferror

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

// vectors is testdata/vectors.json, shared with tools/stack_pipeline/tests/test_tf_error.py.
type vectors struct {
	Redact []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"redact"`
	Summary []struct {
		Name     string   `json:"name"`
		In       string   `json:"in"`
		MaxLines int      `json:"max_lines"`
		Limit    int      `json:"limit"`
		Fallback string   `json:"fallback"`
		Summary  string   `json:"summary"`
		Signals  []string `json:"signals"`
		Never    []string `json:"never"`
	} `json:"summary"`
	FilterSignals []struct {
		In  []string `json:"in"`
		Out []string `json:"out"`
	} `json:"filter_signals"`
	Vocabulary []string `json:"vocabulary"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSharedVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Redact {
		if got := Redact(c.In); got != c.Out {
			t.Errorf("Redact(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
	for _, c := range v.Summary {
		t.Run(c.Name, func(t *testing.T) {
			got := Summary(c.In, c.MaxLines, c.Limit, c.Fallback)
			if got != c.Summary {
				t.Errorf("Summary = %q, want %q", got, c.Summary)
			}
			sig := Signals(c.In)
			if !slices.Equal(sig, c.Signals) && !(len(sig) == 0 && len(c.Signals) == 0) {
				t.Errorf("Signals = %q, want %q", sig, c.Signals)
			}
			joined := got + "\n" + strings.Join(sig, "\n")
			for _, leak := range c.Never {
				if strings.Contains(joined, leak) {
					t.Errorf("output leaks %q: %q", leak, joined)
				}
			}
			// The server's pass is idempotent on what the pipeline sent.
			if again := Clean(got, c.MaxLines, c.Limit); got != c.Fallback && again != got {
				t.Errorf("Clean(summary) = %q, want %q", again, got)
			}
		})
	}
	for _, c := range v.FilterSignals {
		if got := FilterSignals(c.In); !slices.Equal(got, c.Out) {
			t.Errorf("FilterSignals(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
	texts := make([]string, 0, len(Vocabulary))
	for _, s := range Vocabulary {
		texts = append(texts, s.Text)
	}
	if !slices.Equal(texts, v.Vocabulary) {
		t.Errorf("Vocabulary differs from testdata/vectors.json:\n got %q\nwant %q", texts, v.Vocabulary)
	}
}

func TestVocabularyIsWellFormed(t *testing.T) {
	classes := []string{"transient", "config", "infra"}
	seen := map[string]bool{}
	for _, s := range Vocabulary {
		if !slices.Contains(classes, s.Class) || s.Reason == "" || s.Text == "" || seen[s.Text] {
			t.Errorf("bad vocabulary entry %+v", s)
		}
		seen[s.Text] = true
		if !IsVocabulary(s.Text) {
			t.Errorf("IsVocabulary(%q) = false", s.Text)
		}
	}
	if IsVocabulary("throttlingexception") || IsVocabulary("HunterTwoPasswordError") {
		t.Fatal("IsVocabulary must match entries exactly")
	}
}

func TestSummaryCapAndOneLine(t *testing.T) {
	long := "Error: " + strings.Repeat("x", 900)
	if got := Summary(long, 3, 500, "f"); len([]rune(got)) != 500 || !strings.HasSuffix(got, "...") {
		t.Fatalf("cap: %d", len([]rune(got)))
	}
	two := "Error: a (1): x\nError: b (2): y"
	if got := Summary(two, 1, 500, "f"); got != "Error: a (1)" {
		t.Fatalf("one line: %q", got)
	}
}

func TestSignalsAreCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("Error: x (y): ")
	for _, s := range Vocabulary {
		b.WriteString(s.Text + " ")
	}
	if got := Signals(b.String()); len(got) != MaxSignals {
		t.Fatalf("%d signals", len(got))
	}
}

func TestClean(t *testing.T) {
	if got := Clean("apply.yml did not finish (failure) token=abc123\x07", 3, 500); got != "apply.yml did not finish (failure) token= <redacted>" {
		t.Fatalf("clean platform text: %q", got)
	}
	if got := Clean("line one\nsecret detail: xyz\n", 3, 500); got != "pipeline stage failed; see the workflow log" {
		t.Fatalf("multi-line non-terraform text must be replaced: %q", got)
	}
}
