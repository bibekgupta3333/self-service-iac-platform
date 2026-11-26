package identity

import (
	"testing"

	"github.com/google/uuid"
)

func TestVisibility(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	var zero Visibility
	if zero.Allows(a) {
		t.Fatal("the zero value must see nothing")
	}
	if all, ids := zero.SQLArgs(); all || ids == nil || len(ids) != 0 {
		t.Fatalf("zero SQLArgs = %v %v (ids must be a non-nil empty slice)", all, ids)
	}
	v := Visibility{TeamIDs: []uuid.UUID{a}}
	if !v.Allows(a) || v.Allows(b) {
		t.Fatal("team membership decides")
	}
	if !(Visibility{All: true}).Allows(b) {
		t.Fatal("All sees every team")
	}
}
