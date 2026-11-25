package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/apperr"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/observability"
)

// maxBodyBytes bounds request bodies.
const maxBodyBytes = 1 << 20

// ErrorTypeBase prefixes problem "type" URIs (docs/03 §7).
const ErrorTypeBase = "https://iacp.acme.internal/errors/"

// Problem is an RFC 9457 problem details body with the platform extensions.
type Problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Code      string              `json:"code"`
	Detail    string              `json:"detail,omitempty"`
	Instance  string              `json:"instance,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
	Errors    []apperr.FieldError `json:"errors,omitempty"`
}

// List is the envelope for collection responses.
type List[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// problemFor maps an error to a Problem. Pure apart from reading err.
func problemFor(err error) Problem {
	e, ok := apperr.As(err)
	if !ok {
		return Problem{Status: http.StatusInternalServerError, Code: "internal", Title: "Internal server error"}
	}
	p := Problem{Code: e.Code, Detail: e.Message, Errors: e.Fields}
	switch e.Kind {
	case apperr.KindNotFound:
		p.Status, p.Title = http.StatusNotFound, "Resource not found"
	case apperr.KindInvalid:
		p.Status, p.Title = http.StatusUnprocessableEntity, "Request failed validation"
		if e.Code == "bad_request" {
			p.Status, p.Title = http.StatusBadRequest, "Malformed request"
		}
	case apperr.KindConflict:
		p.Status, p.Title = http.StatusConflict, "Request conflicts with current state"
	case apperr.KindUnauthenticated:
		p.Status, p.Title = http.StatusUnauthorized, "Authentication required"
	case apperr.KindForbidden:
		p.Status, p.Title = http.StatusForbidden, "Not allowed"
	default:
		p.Status, p.Title, p.Code, p.Detail = http.StatusInternalServerError, "Internal server error", "internal", ""
	}
	return p
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	p := problemFor(err)
	p.Type = ErrorTypeBase + p.Code
	p.Instance = r.URL.Path
	p.RequestID = RequestIDFrom(r.Context())
	if p.Status >= 500 {
		observability.Logger(r.Context()).ErrorContext(r.Context(), "request failed", "error", err)
	} else {
		observability.Logger(r.Context()).InfoContext(r.Context(), "request rejected", "code", p.Code, "error", err)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func badRequest(msg string) error {
	return &apperr.Error{Kind: apperr.KindInvalid, Code: "bad_request", Message: msg}
}

// readBody reads at most maxBodyBytes of the request body.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, badRequest("could not read request body")
	}
	if len(body) > maxBodyBytes {
		return nil, badRequest(fmt.Sprintf("request body exceeds %d bytes", maxBodyBytes))
	}
	return body, nil
}

// decodeStrict unmarshals body into v, rejecting unknown fields and trailing data.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: " + err.Error())
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return badRequest("invalid JSON body: unexpected trailing data")
	}
	return nil
}
