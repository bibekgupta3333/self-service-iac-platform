package runs

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

type storedError struct {
	Class   string   `json:"class"`
	Message string   `json:"message"`
	Signals []string `json:"signals"`
}

func redacted(t *testing.T, raw json.RawMessage) (storedError, string) {
	t.Helper()
	got := RedactError(raw)
	var out storedError
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("redacted %s: %v", got, err)
	}
	return out, string(got)
}

func TestRedactError(t *testing.T) {
	boxed := "╷\n│ Error: creating DB (orders): api error InvalidParameterValue: password=hunter2 rejected\n│\n" +
		"│   master_password = \"s3cr3t-detail\" # ThrottlingException\n╵"
	raw, _ := json.Marshal(map[string]any{"class": "permanent", "message": boxed, "log_excerpt": "token=leaky",
		"signals": []string{"Rate exceeded", "HunterTwoPasswordError"}})
	out, got := redacted(t, raw)
	for _, leak := range []string{"hunter2", "s3cr3t-detail", "leaky", "log_excerpt", "│", "HunterTwoPasswordError"} {
		if strings.Contains(got, leak) {
			t.Errorf("stored error leaks %q: %s", leak, got)
		}
	}
	// Reported signals are filtered to the vocabulary; the message's own are added; a
	// signal only in a detail line is not.
	if out.Class != "permanent" || out.Message != "Error: creating DB (orders)" ||
		!slices.Equal(out.Signals, []string{"Rate exceeded", "InvalidParameterValue"}) {
		t.Fatalf("redacted %s", got)
	}

	// What the pipeline sends (a summary and its signals) is stored unchanged.
	sent, _ := json.Marshal(map[string]any{"class": "permanent",
		"message": "Error: creating SQS Queue (orders)\nError: creating SNS Topic (t)", "signals": []string{"ThrottlingException", "StatusCode: 503"}})
	out, _ = redacted(t, sent)
	if out.Message != "Error: creating SQS Queue (orders)\nError: creating SNS Topic (t)" ||
		!slices.Equal(out.Signals, []string{"ThrottlingException", "StatusCode: 503"}) {
		t.Fatalf("second pass changed the pipeline's excerpt: %+v", out)
	}

	long, _ := json.Marshal(map[string]string{"message": "Error: " + strings.Repeat("x", 2000), "class": "bogus"})
	out, _ = redacted(t, long)
	if len([]rune(out.Message)) != MaxErrorLen || out.Class != "" || out.Signals != nil {
		t.Fatalf("cap/class: %d %q %v", len([]rune(out.Message)), out.Class, out.Signals)
	}
	out, _ = redacted(t, json.RawMessage(`"plain token=abc"`))
	if out.Message != "plain token= <redacted>" {
		t.Fatalf("string error: %+v", out)
	}
	if RedactError(json.RawMessage(`42`)) != nil || RedactError(nil) != nil {
		t.Fatal("non-error values are dropped")
	}
}
