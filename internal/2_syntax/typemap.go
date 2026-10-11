package syntax

import (
	"iter"

	base "github.com/zambozoo/zam/internal/0_base"
)

type (
	// typeMapEntry is an entry in the type map.
	typeMapEntry[T any] struct {
		// key is the key of the entry.
		key Type
		// value is the value of the entry.
		value T
		// index is the entry's position in insertion order. Entries are never
		// removed, so it stays stable for the life of the map.
		index int
	}

	// TypeMap is a map of types.
	TypeMap[T any] struct {
		// buckets is the buckets of the type map.
		buckets *base.StaticOrderedMap[uint64, []typeMapEntry[T]]
		// len is the length of the type map.
		len int
	}

	TypeSet TypeMap[struct{}]
)

// NewTypeSet creates a new type set.
func NewTypeSet() *TypeSet {
	return &TypeSet{
		buckets: base.NewStaticOrderedMap[uint64, []typeMapEntry[struct{}]](),
	}
}

// NewTypeMap creates a new type map.
func NewTypeMap[T any]() *TypeMap[T] {
	return &TypeMap[T]{
		buckets: base.NewStaticOrderedMap[uint64, []typeMapEntry[T]](),
	}
}

// Get retrieves the value for the given key and its insertion index, or the zero
// value and -1 if the key is absent.
func (m *TypeMap[T]) Get(key Type) (T, int) {
	hash := key.Hash()

	bucket, _ := m.buckets.Get(hash)
	for _, entry := range bucket {
		if key.Equals(entry.key) {
			return entry.value, entry.index
		}
	}

	var zero T
	return zero, -1
}

// Set inserts key, or updates its value if an equal key is already present.
// Returns true if the key was inserted, false if it was updated.
func (m *TypeMap[T]) Set(key Type, value T) bool {
	hash := key.Hash()

	bucket, _ := m.buckets.Get(hash)
	for i, entry := range bucket {
		if key.Equals(entry.key) {
			bucket[i].value = value
			return false
		}
	}

	m.buckets.Set(hash, append(bucket, typeMapEntry[T]{key: key, value: value, index: m.len}))
	m.len++
	return true
}

// All iterates through the type map keys and values deterministically, yielding
// each entry's insertion index. Entries are grouped by hash bucket, so the
// indices are not ascending.
func (m *TypeMap[T]) All() iter.Seq2[int, base.MapEntry[Type, T]] {
	return func(yield func(int, base.MapEntry[Type, T]) bool) {
		for _, entry := range m.buckets.All() {
			for _, entry := range entry.Value {
				typeMapEntry := base.MapEntry[Type, T]{
					Key:   entry.key,
					Value: entry.value,
				}
				if !yield(entry.index, typeMapEntry) {
					return
				}
			}
		}
	}
}

// Len returns the number of entries in the map.
func (m *TypeMap[T]) Len() int {
	return m.len
}

func (m *TypeSet) Add(key Type) {
	(*TypeMap[struct{}])(m).Set(key, struct{}{})
}

func (m *TypeSet) Contains(key Type) bool {
	return m.Index(key) >= 0
}

// Index returns the insertion index of key, or -1 if the key is absent.
func (m *TypeSet) Index(key Type) int {
	_, index := (*TypeMap[struct{}])(m).Get(key)
	return index
}

func (m *TypeSet) Values() iter.Seq[Type] {
	return func(yield func(Type) bool) {
		for _, entry := range (*TypeMap[struct{}])(m).All() {
			if !yield(entry.Key) {
				return
			}
		}
	}
}

func (m *TypeSet) Len() int {
	return (*TypeMap[struct{}])(m).Len()
}
