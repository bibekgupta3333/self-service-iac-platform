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
