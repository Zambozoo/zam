package syntax

import (
	"errors"
	"io"
	"unicode/utf8"
)

var (
	symbolTrie  = NewTrie(Symbols())
	keywordTrie = NewTrie(Keywords())
)

// runeReader reads a UTF-8 stream as runes with bounded peek and sequential advance.
type runeReader interface {
	Peek(i uint) (rune, error)
	Advance(n uint)
	Offset() uint
	Close()
}

type trieNode[T any] struct {
	children map[rune]*trieNode[T]
	value    *T // nil when this node is not a complete key
}

// Trie is a longest-prefix matcher for UTF-8 string keys with associated values.
type trie[T any] struct {
	root        *trieNode[T]
	maxKeyRunes int // longest key in runes; used to cap path slice capacity
}

// NewTrie builds a trie from a string→T map.
func NewTrie[T any](words map[string]T) *trie[T] {
	t := &trie[T]{
		root: &trieNode[T]{children: make(map[rune]*trieNode[T])},
	}
	for s, v := range words {
		t.Put(s, v)
	}
	return t
}

// Put inserts key with value, replacing any previous value at key.
func (t *trie[T]) Put(key string, value T) {
	if n := utf8.RuneCountInString(key); n > t.maxKeyRunes {
		t.maxKeyRunes = n
	}

	node := t.root
	for _, r := range key {
		if node.children[r] == nil {
			node.children[r] = &trieNode[T]{children: make(map[rune]*trieNode[T])}
		}
		node = node.children[r]
	}
	node.value = &value
}

// MatchPrefix tries to match a trie entry from rr's current position.
// When an unmatched rune or EOF is encountered, the walk is stopped and
// the maximal trie path is returned. rr is advanced through the entire walk.
// If the walk ends on a terminal node, it returns (value, &path, nil).
// If the walk ends on a non-terminal node, it returns (nil, &path, nil).
// If the first rune matches no key, path is empty and value is nil.
// Non-EOF Peek or Advance errors return (nil, nil, err).
func (t *trie[T]) MatchPrefix(rr runeReader) (*T, *string, error) {
	node := t.root
	pathRunes := make([]rune, 0, t.maxKeyRunes)
	for {
		r, err := rr.Peek(0)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, err
		}

		next := node.children[r]
		if next == nil {
			break
		}

		// rune already peeked, cannot fail
		rr.Advance(1) //nolint:errcheck
		pathRunes = append(pathRunes, r)
		node = next
	}

	path := string(pathRunes)
	return node.value, &path, nil
}
