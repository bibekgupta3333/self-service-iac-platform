// Package tferror reduces Terraform failure output to short, redacted text that is safe to
// store and to classify. It is shared by drift reports (drift.SummarizeError) and apply
// failures (run reports, recovery), so a tightening of the rules benefits both. It mirrors
// tools/stack_pipeline/tf_error.py; testdata/vectors.json holds the cases both suites run.
// Pure.
//
// Terraform prints a diagnostic as a box (the lines are prefixed with the box characters
// U+2502, U+2577 and U+2575) whose first line is "Error: <summary>". The lines after it
// carry source snippets and provider detail, which can hold attribute values and request
// bodies, so only "Error:" lines are kept, and each is cut after the resource identifier
// (at the first ": " after the prefix), because what follows is the provider's own message,
// which can echo input. Credential-shaped text is replaced as defence in depth and control
// characters become spaces.
//
// What recovery needs to classify a failure travels separately, as signals: entries of the
// fixed Vocabulary (AWS error codes and Terraform phrases, each tagged with the class and
// reason recovery gives it) found in the "Error:" lines, plus "StatusCode: <nnn>". A signal
// is always one of those fixed strings, never text copied from the output.
package tferror

import (
	"regexp"
	"slices"
	"strings"
)

var (
	secretRe = regexp.MustCompile(`(?i)((?:secret|token|password|passwd|authorization|api[_-]?key|access[_-]?key)[A-Za-z_-]*\s*[=:]\s*)(?:bearer\s+)?\S+|AKIA[0-9A-Z]{16}|bearer\s+\S+`)
	errorRe  = regexp.MustCompile(`^Error:\s+\S`)
	statusRe = regexp.MustCompile(`(?i)\bstatus ?code:? ?([1-5][0-9]{2})\b`)
	signalRe = regexp.MustCompile(`^StatusCode: [1-5][0-9]{2}$`)
)

const boxChars = "│╷╵ \t"

// Redact replaces credential-shaped text (key=value secrets, including "Authorization:
// Bearer <token>", AWS access key ids, bearer tokens) with "<redacted>" and control
// characters with spaces.
func Redact(line string) string {
	line = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, line)
	return secretRe.ReplaceAllStringFunc(line, func(m string) string {
		if i := strings.IndexAny(m, "=:"); i >= 0 {
			return m[:i+1] + " <redacted>"
		}
		return "<redacted>"
	})
}

// CutAfterIdentifier keeps an "Error:" line up to the first ": " after the prefix
// ("Error: putting SSM Parameter (/app/db): ..." becomes "Error: putting SSM Parameter (/app/db)").
// It is idempotent.
func CutAfterIdentifier(line string) string {
	if !strings.HasPrefix(line, "Error:") {
		return line
	}
	if i := strings.Index(line[len("Error:"):], ": "); i >= 0 {
		return strings.TrimSpace(line[:len("Error:")+i])
	}
	return line
}

// ErrorLines returns up to max "Error:" lines of text (the first line of each diagnostic),
// box characters stripped, redacted and cut after the resource identifier, in order.
func ErrorLines(text string, max int) []string {
	full := fullErrorLines(text, max)
	for i, l := range full {
		full[i] = CutAfterIdentifier(l)
	}
	return full
}

// fullErrorLines is ErrorLines without the cut (the provider message is still there).
func fullErrorLines(text string, max int) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if len(out) >= max {
			break
		}
		line := strings.TrimSpace(strings.Trim(strings.TrimSpace(l), boxChars))
		if errorRe.MatchString(line) {
			out = append(out, Redact(line))
		}
	}
	return out
}

// Cap shortens s to at most limit runes, ending with "..." when cut.
func Cap(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return string(r[:limit-3]) + "..."
	}
	return s
}

// Summary returns the first maxLines "Error:" lines, one per line, capped at limit runes;
// with no "Error:" line it returns fallback, never other lines of the output. Summary of a
// summary is the same summary.
func Summary(text string, maxLines, limit int, fallback string) string {
	lines := ErrorLines(text, maxLines)
	if len(lines) == 0 {
		return fallback
	}
	return Cap(strings.Join(lines, "\n"), limit)
}

// MaxSignals bounds how many signals one failure carries.
const MaxSignals = 12

// Signals returns the Vocabulary entries found in the "Error:" lines of text (never in the
// source and detail lines below them), in Vocabulary order, then "StatusCode: <nnn>" for
// each HTTP status, without duplicates and at most MaxSignals. Matching is case-insensitive.
func Signals(text string) []string {
	text = strings.Join(fullErrorLines(text, 16), "\n")
	lower := strings.ToLower(text)
	var out []string
	add := func(s string) {
		if len(out) < MaxSignals && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	for _, v := range Vocabulary {
		if strings.Contains(lower, strings.ToLower(v.Text)) {
			add(v.Text)
		}
	}
	for _, m := range statusRe.FindAllStringSubmatch(text, -1) {
		add("StatusCode: " + m[1])
	}
	return out
}

// FilterSignals keeps the reported signals that are Vocabulary entries or HTTP statuses,
// without duplicates and at most MaxSignals: the server's guard on what a pipeline sent.
func FilterSignals(reported []string) []string {
	var out []string
	for _, s := range reported {
		if len(out) >= MaxSignals {
			break
		}
		if (IsVocabulary(s) || signalRe.MatchString(s)) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// IsVocabulary reports whether s is exactly one Vocabulary entry.
func IsVocabulary(s string) bool {
	return slices.ContainsFunc(Vocabulary, func(v Signal) bool { return v.Text == s })
}

// Clean is the server-side guard for an error message a pipeline reported. Terraform output
// (text with "Error:" lines) becomes its Summary (idempotent on a summary the pipeline
// already made). Any other text is kept as one redacted line only when it is short
// single-line platform text; otherwise a fixed message replaces it. Capped at limit runes.
func Clean(text string, maxLines, limit int) string {
	if lines := ErrorLines(text, maxLines); len(lines) > 0 {
		return Cap(strings.Join(lines, "\n"), limit)
	}
	flat := strings.Join(strings.Fields(Redact(text)), " ")
	if !strings.Contains(strings.TrimSpace(text), "\n") && len(flat) <= 200 {
		return Cap(flat, limit)
	}
	return Cap("pipeline stage failed; see the workflow log", limit)
}
