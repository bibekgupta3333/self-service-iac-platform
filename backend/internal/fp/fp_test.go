package fp

import (
	"slices"
	"strconv"
	"testing"
)

func TestMapFilterReduce(t *testing.T) {
	in := []int{1, 2, 3, 4}
	if got := Map(in, strconv.Itoa); !slices.Equal(got, []string{"1", "2", "3", "4"}) {
		t.Fatalf("Map = %v", got)
	}
	if got := Filter(in, func(v int) bool { return v%2 == 0 }); !slices.Equal(got, []int{2, 4}) {
		t.Fatalf("Filter = %v", got)
	}
	if got := Reduce(in, 0, func(a, v int) int { return a + v }); got != 10 {
		t.Fatalf("Reduce = %d", got)
	}
}

func TestSortedKeys(t *testing.T) {
	got := SortedKeys(map[string]int{"b": 1, "a": 2, "c": 3})
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("SortedKeys = %v", got)
	}
}

func TestDuplicates(t *testing.T) {
	tests := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"a", "b"}, nil},
		{[]string{"a", "b", "a", "a", "b"}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		if got := Duplicates(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("Duplicates(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
