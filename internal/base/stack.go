package base

import (
	"iter"
	"slices"
)

// Stack is a generic LIFO stack. The zero value is an empty, ready-to-use stack.
type Stack[T any] struct {
	items []T
}

// Push adds v to the top of the stack.
func (s *Stack[T]) Push(v T) {
	s.items = append(s.items, v)
}

// Pop removes and returns the top of the stack, or the zero value of T and false if empty.
func (s *Stack[T]) Pop() (v T, ok bool) {
	if len(s.items) == 0 {
		return v, false
	}
	i := len(s.items) - 1
	v = s.items[i]
	s.items = s.items[:i]
	return v, true
}

// Peek returns the top of the stack without removing it, or the zero value of T and false if empty.
func (s *Stack[T]) Peek() (v T) {
	if len(s.items) == 0 {
		return v
	}
	return s.items[len(s.items)-1]
}

// All iterates the stack from the top down.
func (s *Stack[T]) All() iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, item := range slices.Backward(s.items) {
			if !yield(item) {
				return
			}
		}
	}
}

// Len returns the number of items on the stack.
func (s *Stack[T]) Len() int {
	return len(s.items)
}
