package base

import "iter"

type (
	MapEntry[K comparable, V any] struct {
		Key   K
		Value V
		Index int
	}

	// StaticOrderedMap is an insertion-ordered map that does not support deletion.
	// Because entries are never removed, each key's index is stable for the
	// lifetime of the map, so Get, Set, and At are O(1).
	StaticOrderedMap[K comparable, V any] struct {
		indices map[K]int
		entries []MapEntry[K, V]
	}
)

func NewStaticOrderedMap[K comparable, V any]() *StaticOrderedMap[K, V] {
	return &StaticOrderedMap[K, V]{
		indices: make(map[K]int),
	}
}

// Get returns the value stored for key and its insertion-order index, or the
// zero value and -1 if key is absent.
func (om *StaticOrderedMap[K, V]) Get(key K) (V, int) {
	i, ok := om.indices[key]
	if !ok {
		var v V
		return v, -1
	}
	return om.entries[i].Value, i
}

// Set inserts key and returns its index, or updates the value in place and
// returns the existing index if the key is already present.
func (om *StaticOrderedMap[K, V]) Set(key K, value V) int {
	if i, ok := om.indices[key]; ok {
		om.entries[i].Value = value
		return i
	}

	i := len(om.entries)
	om.indices[key] = i
	om.entries = append(om.entries, MapEntry[K, V]{Key: key, Value: value, Index: i})
	return i
}

func (om *StaticOrderedMap[K, V]) Len() int {
	return len(om.entries)
}

func (om *StaticOrderedMap[K, V]) Last() V {
	return om.entries[len(om.entries)-1].Value
}

// At returns the value at insertion-order position i, or the zero value if i
// is out of range.
func (om *StaticOrderedMap[K, V]) At(i int) V {
	return om.entries[i].Value
}

func (om *StaticOrderedMap[K, V]) All() iter.Seq2[int, MapEntry[K, V]] {
	return func(yield func(int, MapEntry[K, V]) bool) {
		for i, entry := range om.entries {
			if !yield(i, entry) {
				return
			}
		}
	}
}

func (om *StaticOrderedMap[K, V]) Values() iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, entry := range om.entries {
			if !yield(entry.Value) {
				return
			}
		}
	}
}
