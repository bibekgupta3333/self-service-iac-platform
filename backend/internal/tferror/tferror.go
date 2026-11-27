// Package tferror reduces Terraform failure output to short, redacted text that is safe to
// store and to classify. It is shared by drift reports (drift.SummarizeError) and apply
// failures (run reports, recovery), so a tightening of the rules benefits both. It mirrors
// tools/stack_pipeline/tf_error.py. Pure.
//
// Terraform prints a diagnostic as a box (the lines are prefixed with the box characters
// U+2502, U+2577 and U+2575) whose first line is "Error: <summary>". The lines after it
// carry source snippets and provider detail, which can hold attribute values and request
// bodies, so only "Error:" lines are kept, and each is cut after the resource identifier
// (at the first ": " after the prefix), because what follows is the provider's own message,
// which can echo input. Credential-shaped text is replaced as defence in depth and control
// characters become spaces.
//
// What recovery needs to classify a failure (an AWS error code, an HTTP status, a known
// Terraform phrase) is carried separately by Signals: tokens matched against a fixed
// vocabulary, never free text.
package tferror

import (
	"regexp"
	"slices"
	"strings"
)

var (
	secretRe = regexp.MustCompile(`(?i)((?:secret|token|password|passwd|authorization|api[_-]?key|access[_-]?key)[A-Za-z_-]*\s*[=:]\s*)(?:bearer\s+)?\S+|AKIA[0-9A-Z]{16}|bearer\s+\S+`)
	errorRe  = regexp.MustCompile(`^Error:\s+\S`)
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

// Summary joins the first maxLines "Error:" lines with " | ", capped at limit runes; with no
// "Error:" line it returns fallback, never other lines of the output.
func Summary(text string, maxLines, limit int, fallback string) string {
	lines := ErrorLines(text, maxLines)
	if len(lines) == 0 {
		return fallback
	}
	return Cap(strings.Join(lines, " | "), limit)
}

// signalRes match the parts of a failure that classify it. Each match is reduced to a
// fixed-shape token: an AWS error code name, an HTTP status, or a phrase from a fixed list.
var (
	apiErrorRe   = regexp.MustCompile(`\bapi error ([A-Z][A-Za-z0-9]{2,63})\b`)
	codeColonRe  = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]{2,63}(?:Exception|Exceeded|Error|Failure|Denied|Unavailable|AlreadyExists|Throttling|Capacity))\b`)
	statusCodeRe = regexp.MustCompile(`(?i)\bstatus ?code:? ?([1-5][0-9]{2})\b`)
	phrases      = []string{
		"Error acquiring the state lock", "Error locking state", "i/o timeout", "context deadline exceeded",
		"TLS handshake timeout", "connection reset by peer", "connection refused", "no such host",
		"Rate exceeded", "SlowDown", "Throttling", "role cannot be assumed",
		"role defined for the function cannot be assumed", "runner has received a shutdown signal",
		"lost communication with the server", "plan_mismatch", "differs from the approved plan",
		"Invalid value for variable", "Unsupported argument", "Missing required argument",
		"Reference to undeclared", "Unsupported attribute", "Invalid reference", "already exists",
		"is not authorized to perform", "explicit deny",
	}
)

// MaxSignals bounds how many tokens Signals returns.
const MaxSignals = 8

// Signals returns the classification tokens found in the "Error:" lines of text (never in
// the source and detail lines below them), in a stable order and without duplicates: "api error <Code>", "StatusCode: <nnn>", AWS-style error code names and the
// fixed phrases above. They carry no input values: a code name is one identifier, a status
// is three digits and a phrase is fixed text, so they are safe to store next to a summary.
func Signals(text string) []string {
	var out []string
	add := func(s string) {
		if len(out) < MaxSignals && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	text = strings.Join(fullErrorLines(text, 16), "\n")
	for _, m := range apiErrorRe.FindAllStringSubmatch(text, -1) {
		add("api error " + m[1])
	}
	for _, m := range statusCodeRe.FindAllStringSubmatch(text, -1) {
		add("StatusCode: " + m[1])
	}
	for _, m := range codeColonRe.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	lower := strings.ToLower(text)
	for _, p := range phrases {
		if strings.Contains(lower, strings.ToLower(p)) {
			add(p)
		}
	}
	return out
}

// Excerpt is what an apply failure stores and classifies: Summary of up to maxLines
// "Error:" lines, plus the Signals in brackets, capped at limit runes; fallback (with any
// signals) when there is no "Error:" line.
func Excerpt(text string, maxLines, limit int, fallback string) string {
	s := Summary(text, maxLines, limit, fallback)
	if sig := Signals(text); len(sig) > 0 {
		s += " [" + strings.Join(sig, "; ") + "]"
	}
	return Cap(s, limit)
}

// Clean is the server-side guard for an error message a pipeline reported. Terraform output
// (text with "Error:" lines) becomes its Excerpt. Any other text is kept as one redacted line
// only when it is short single-line platform text; otherwise a fixed message replaces it.
// The result is capped at limit runes.
func Clean(text string, maxLines, limit int) string {
	if len(ErrorLines(text, 1)) > 0 {
		return Excerpt(text, maxLines, limit, "")
	}
	flat := strings.Join(strings.Fields(Redact(text)), " ")
	if !strings.Contains(strings.TrimSpace(text), "\n") && len(flat) <= 200 {
		return Cap(flat, limit)
	}
	return Cap("pipeline stage failed; see the workflow log", limit)
}
