package runs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactError(t *testing.T) {
	boxed := "╷\n│ Error: creating DB: api error InvalidParameterValue: password=hunter2 rejected\n│\n" +
		"│   master_password = \"s3cr3t-detail\"\n╵"
	raw, _ := json.Marshal(map[string]string{"class": "permanent", "message": boxed, "log_excerpt": "token=leaky"})
	got := string(RedactError(raw))
	for _, leak := range []string{"hunter2", "s3cr3t-detail", "leaky", "log_excerpt", "│"} {
		if strings.Contains(got, leak) {
			t.Errorf("stored error leaks %q: %s", leak, got)
		}
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(got), &out); err != nil || out["class"] != "permanent" ||
		out["message"] != "Error: creating DB [api error InvalidParameterValue]" {
		t.Fatalf("redacted %s (%v)", got, err)
	}

	long, _ := json.Marshal(map[string]string{"message": "Error: " + strings.Repeat("x", 2000), "class": "bogus"})
	out = map[string]string{}
	_ = json.Unmarshal(RedactError(long), &out)
	if len([]rune(out["message"])) != MaxErrorLen || out["class"] != "" {
		t.Fatalf("cap/class: %d %q", len([]rune(out["message"])), out["class"])
	}
	out = map[string]string{}
	if err := json.Unmarshal(RedactError(json.RawMessage(`"plain token=abc"`)), &out); err != nil || out["message"] != "plain token= <redacted>" {
		t.Fatalf("string error: %v %v", out, err)
	}
	if RedactError(json.RawMessage(`42`)) != nil || RedactError(nil) != nil {
		t.Fatal("non-error values are dropped")
	}
}
