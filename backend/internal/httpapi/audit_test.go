package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/audit"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
)

type fakeVerifier struct{ from, to int64 }

func (f *fakeVerifier) Verify(_ context.Context, from, to int64) (audit.VerifyResult, error) {
	f.from, f.to = from, to
	bad := int64(7)
	return audit.VerifyResult{Valid: false, Checked: 6, FirstBadSeq: &bad}, nil
}

func TestAuditVerify(t *testing.T) {
	v := &fakeVerifier{}
	h := newTestRouter(&fakeCatalog{versions: []catalog.ModuleVersion{sqsVersion()}}, okPinger{}, NewAuditHandler(v))

	rec := do(t, h, "GET", "/v1/audit/verify", "")
	if rec.Code != 200 || v.from != 1 || v.to != audit.MaxVerifyRange ||
		!strings.Contains(rec.Body.String(), `"valid":false`) || !strings.Contains(rec.Body.String(), `"first_bad_seq":7`) {
		t.Fatalf("got %d %s (from=%d to=%d)", rec.Code, rec.Body, v.from, v.to)
	}
	if rec := do(t, h, "GET", "/v1/audit/verify?from_seq=5&to_seq=9", ""); rec.Code != 200 || v.from != 5 || v.to != 9 {
		t.Fatalf("range not passed: %d from=%d to=%d", rec.Code, v.from, v.to)
	}
	for _, q := range []string{"from_seq=x", "to_seq=-1", "from_seq=9&to_seq=5", "from_seq=1&to_seq=10001"} {
		if rec := do(t, h, "GET", "/v1/audit/verify?"+q, ""); rec.Code != 400 {
			t.Errorf("?%s = %d, want 400", q, rec.Code)
		}
	}
}
