package observability

import "testing"

func TestRouteLabel(t *testing.T) {
	if RouteLabel("") != UnmatchedRoute {
		t.Fatal("empty pattern must map to the shared unmatched label")
	}
	if RouteLabel("/v1/requests/{id}") != "/v1/requests/{id}" {
		t.Fatal("patterns pass through")
	}
}

func TestMethodLabel(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if MethodLabel(m) != m {
			t.Errorf("%s must pass through", m)
		}
	}
	for _, m := range []string{"", "get", "TRACE", "CONNECT", "PROPFIND", "JUNK42"} {
		if MethodLabel(m) != OtherMethod {
			t.Errorf("%q must map to OTHER", m)
		}
	}
}
