// Package fp holds small, pure, generic helpers for working with slices and maps.
package fp

import (
	"cmp"
	"slices"
)

// Map returns a new slice with f applied to every element of in.
func Map[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// Filter returns the elements of in for which keep returns true.
func Filter[T any](in []T, keep func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

// Reduce folds in into a single value, starting from init.
func Reduce[T, A any](in []T, init A, f func(A, T) A) A {
	acc := init
	for _, v := range in {
		acc = f(acc, v)
	}
	return acc
}

// SortedKeys returns the keys of m in ascending order, for deterministic iteration.
func SortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Duplicates returns the values that occur more than once in in, in first-seen order.
func Duplicates[T comparable](in []T) []T {
	seen := make(map[T]int, len(in))
	var dups []T
	for _, v := range in {
		seen[v]++
		if seen[v] == 2 {
			dups = append(dups, v)
		}
	}
	return dups
}
