package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// maxExponent bounds number exponents in audit payloads (payloads are server-built).
const maxExponent = 4000

// CanonicalPayload returns payload re-encoded with sorted keys and every number in
// canonical decimal form (see CanonicalNumber). An empty payload is "{}". Pure.
//
// The canonical form is a fixed point of PostgreSQL's jsonb number output, so the
// writer stores these bytes and the verifier gets the same bytes back from jsonb.
func CanonicalPayload(payload json.RawMessage) ([]byte, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return []byte("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical payload: %w", err)
	}
	if dec.More() {
		return nil, errors.New("canonical payload: trailing data")
	}
	cv, err := canonicalValue(v)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(cv) // encoding/json sorts map keys
	if err != nil {
		return nil, fmt.Errorf("canonical payload: %w", err)
	}
	return b, nil
}

func canonicalValue(v any) (any, error) {
	switch t := v.(type) {
	case json.Number:
		n, err := CanonicalNumber(t.String())
		if err != nil {
			return nil, err
		}
		return json.Number(n), nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			ce, err := canonicalValue(e)
			if err != nil {
				return nil, err
			}
			out[k] = ce
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			ce, err := canonicalValue(e)
			if err != nil {
				return nil, err
			}
			out[i] = ce
		}
		return out, nil
	}
	return v, nil
}

// CanonicalNumber rewrites a JSON number as a plain decimal with no exponent, no leading
// zeros and no trailing fractional zeros, keeping every significant digit:
// "1e2" -> "100", "2.50" -> "2.5", "-0" -> "0", "9007199254740993" is unchanged. Pure.
func CanonicalNumber(s string) (string, error) {
	invalid := fmt.Errorf("canonical number: invalid %q", s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	mant, expPart, hasExp := strings.Cut(strings.ToLower(s), "e")
	exp := 0
	if hasExp {
		e, err := strconv.Atoi(expPart)
		if err != nil {
			return "", invalid
		}
		if e > maxExponent || e < -maxExponent {
			return "", fmt.Errorf("canonical number: exponent of %q out of range", s)
		}
		exp = e
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	digits := intPart + frac
	if intPart == "" || strings.Trim(digits, "0123456789") != "" {
		return "", invalid
	}
	point := len(intPart) + exp // position of the decimal point within digits

	trimmed := strings.TrimLeft(digits, "0")
	point -= len(digits) - len(trimmed)
	digits = trimmed
	if digits == "" {
		return "0", nil
	}
	for len(digits) > max(point, 0) && strings.HasSuffix(digits, "0") {
		digits = digits[:len(digits)-1]
	}

	var out string
	switch {
	case point <= 0:
		out = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		out = digits + strings.Repeat("0", point-len(digits))
	default:
		out = digits[:point] + "." + digits[point:]
	}
	if neg {
		out = "-" + out
	}
	return out, nil
}

// CanonicalIP returns the canonical text of an IP address ("" stays ""). IPv4-mapped
// IPv6 addresses are unmapped so the stored inet value reads back identically. Pure.
func CanonicalIP(ip string) (string, error) {
	if ip == "" {
		return "", nil
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "", fmt.Errorf("canonical actor ip: %w", err)
	}
	return a.Unmap().String(), nil
}
