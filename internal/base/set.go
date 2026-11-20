package base

import "iter"

type Set[T comparable] map[T]struct{}

type StaticOrderedSet[T comparable] StaticOrderedMap[T, struct{}]

func NewStaticOrderedSet[T comparable]() *StaticOrderedSet[T] {
	return &StaticOrderedSet[T]{
		indices: make(map[T]int),
		entries: make([]MapEntry[T, struct{}], 0),
	}
}

func (os *StaticOrderedSet[T]) All() iter.Seq2[int, T] {
	return func(yield func(int, T) bool) {
		for i, entry := range os.entries {
			if !yield(i, entry.Key) {
				return
			}
		}
	}
}

func (os *StaticOrderedSet[T]) Contains(key T) bool {
	_, ok := os.indices[key]
	return ok
}

func (os *StaticOrderedSet[T]) Last() T {
	return os.entries[len(os.entries)-1].Key
}

func (os *StaticOrderedSet[T]) Len() int {
	return len(os.entries)
}

func (os *StaticOrderedSet[T]) Values() iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, entry := range os.entries {
			if !yield(entry.Key) {
				return
			}
		}
	}
}
