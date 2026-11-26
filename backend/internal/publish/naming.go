package publish

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"strings"
)

// BranchPrefix starts every branch the platform writes (docs/03 §6.4: iacp/req-7k3p9x).
const BranchPrefix = "iacp/"

// shortIDRe matches request short ids (req- plus lower-case base32, see platform.RandomIDs).
var shortIDRe = regexp.MustCompile(`^req-[0-9a-z]{4,32}$`)

// BranchName returns the branch for a request short id. Pure.
func BranchName(shortID string) (string, error) {
	if !shortIDRe.MatchString(shortID) {
		return "", fmt.Errorf("request short id %q cannot name a branch", shortID)
	}
	return BranchPrefix + shortID, nil
}

// ParseBranch returns the request short id behind a platform branch, accepting both
// "iacp/req-…" and "refs/heads/iacp/req-…". ok is false for any other ref. Pure.
func ParseBranch(ref string) (shortID string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimPrefix(ref, "refs/heads/"), BranchPrefix)
	if !found || !shortIDRe.MatchString(rest) {
		return "", false
	}
	return rest, true
}

// ValidatePath rejects repository paths that are absolute, contain "." or ".." segments,
// empty segments, backslashes, NUL, a ".git" segment, or a segment starting with "-"
// (which a CLI could read as a flag). Pure.
func ValidatePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return fmt.Errorf("%w: %q", ErrInvalidPath, p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "", seg == ".", seg == "..", strings.EqualFold(seg, ".git"), strings.HasPrefix(seg, "-"):
			return fmt.Errorf("%w: %q", ErrInvalidPath, p)
		}
	}
	return nil
}

// StackFiles places rendered files under the stack root (e.g. environments/dev/team/stack),
// validating every resulting path. Pure.
func StackFiles(root string, files map[string][]byte) (map[string][]byte, error) {
	if err := ValidatePath(root); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(files))
	for name, body := range maps.All(files) {
		p := root + "/" + name
		if ValidatePath(name) != nil || ValidatePath(p) != nil || path.Clean(p) != p {
			return nil, fmt.Errorf("%w: %q under %q", ErrInvalidPath, name, root)
		}
		out[p] = body
	}
	return out, nil
}
