package runs

import (
	"encoding/json"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/tferror"
)

// Error message bounds for stored run reports (pipeline_runs.stages, and from there
// requests.failure_detail): at most MaxErrorLines Terraform "Error:" lines, MaxErrorLen runes.
const (
	MaxErrorLines = 3
	MaxErrorLen   = 500
)

// RedactError is the server-side guard on a report's error block before it is stored: the
// message is reduced to its redacted Terraform "Error:" lines (or one redacted line when it
// has none) and capped, the class is kept when it is a known value, and every other field
// (a log excerpt, anything else) is dropped. A value that is not an error object becomes a
// redacted message. The pipeline already sends only a summary; this is defence in depth.
// Pure.
func RedactError(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return raw
	}
	var block struct {
		Class   string `json:"class"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil // neither an object nor a string: nothing worth storing
		}
		block.Message = s
	}
	out := map[string]string{"message": tferror.Clean(block.Message, MaxErrorLines, MaxErrorLen)}
	if block.Class == "transient" || block.Class == "permanent" {
		out["class"] = block.Class
	}
	b, _ := json.Marshal(out) // a map of strings always marshals
	return b
}
