// Package tferror reduces Terraform failure output to short, redacted text that is safe to
// store and to classify. It is shared by drift reports (drift.SummarizeError) and apply
// failures (runs reports, recovery), so a tightening of the rules benefits both. It mirrors
// tools/stack_pipeline/tf_error.py. Pure.
//
// Terraform prints a diagnostic as a box (the lines are prefixed with the box characters
// U+2502, U+2577 and U+2575) whose first line is "Error: <summary>". The lines after it
// carry source snippets and provider detail, which can hold attribute values and request
// bodies, so only "Error:" lines are ever kept. Credential-shaped text is replaced as
// defence in depth and control characters become spaces.
package tferror

import (
	"regexp"
	"strings"
)

var (
	secretRe = regexp.MustCompile(`(?i)((?:secret|token|password|passwd|authorization|api[_-]?key|access[_-]?key)[A-Za-z_-]*\s*[=:]\s*)(?:bearer\s+)?\S+|AKIA[0-9A-Z]{16}|bearer\s+\S+`)
	errorRe  = regexp.MustCompile(`^Error:\s+\S`)
)

const boxChars = "\u2502\u2577\u2575 \t"

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

// ErrorLines returns up to max "Error:" lines of text (the first line of each diagnostic),
// box characters stripped and redacted, in order.
func ErrorLines(text string, max int) []string {
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

// Clean is the server-side guard for an error message a pipeline reported: the Terraform
// summary when the text has "Error:" lines, otherwise the text as one redacted line. Either
// way it is capped at limit runes.
func Clean(text string, maxLines, limit int) string {
	if lines := ErrorLines(text, maxLines); len(lines) > 0 {
		return Cap(strings.Join(lines, " | "), limit)
	}
	return Cap(strings.Join(strings.Fields(Redact(text)), " "), limit)
}
