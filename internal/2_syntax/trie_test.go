package syntax

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func ptr(s string) *string {
	return &s
}

func TestTrie_New(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		pairs map[string]string
		want  *string
	}{
		{
			name:  "empty",
			pairs: map[string]string{},
			want:  nil,
		},
		{
			name:  "one",
			pairs: map[string]string{"a": "b"},
		},
		{
			name:  "two",
			pairs: map[string]string{"a": "b", "c": "d"},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trie := NewTrie(tt.pairs)

			for k, v := range tt.pairs {
				rr := newRuneReader(io.NopCloser(strings.NewReader(k)))
				value, path, err := trie.MatchPrefix(rr)
				assert.NoError(t, err)
				assert.Equal(t, &v, value)
				assert.Equal(t, &k, path)
			}

		})
	}
}

func TestTrie_Put_MatchPrefix(t *testing.T) {
	t.Parallel()

	type expectations struct {
		key       string
		value     string
		wantValue *string
		wantPath  *string
		errFunc   assert.ErrorAssertionFunc
	}
	tests := []struct {
		name         string
		expectations []expectations
		rrFunc       func(key string) runeReader
	}{
		{
			name:         "empty",
			expectations: []expectations{},
			rrFunc: func(key string) runeReader {
				return newRuneReader(io.NopCloser(strings.NewReader(key)))
			},
		},
		{
			name: "one",
			expectations: []expectations{
				{
					key:       "a",
					value:     "b",
					wantValue: ptr("b"),
					wantPath:  ptr("a"),
					errFunc:   assert.NoError,
				},
			},
			rrFunc: func(key string) runeReader {
				return newRuneReader(io.NopCloser(strings.NewReader(key)))
			},
		},
		{
			name: "two",
			expectations: []expectations{
				{
					key:       "a",
					value:     "b",
					wantValue: ptr("b"),
					wantPath:  ptr("a"),
					errFunc:   assert.NoError,
				},
				{
					key:       "c",
					value:     "d",
					wantValue: ptr("d"),
					wantPath:  ptr("c"),
					errFunc:   assert.NoError,
				},
			},
			rrFunc: func(key string) runeReader {
				return newRuneReader(io.NopCloser(strings.NewReader(key)))
			},
		},
		{
			name: "unmatched rune stops walk",
			expectations: []expectations{
				{
					key:       "a",
					value:     "b",
					wantValue: nil,
					wantPath:  ptr(""),
					errFunc:   assert.NoError,
				},
			},
			rrFunc: func(key string) runeReader {
				m := &mock_RuneReader{}
				m.On("Peek", uint(0)).Return('x', nil)
				return m
			},
		},
		{
			name: "peek non-EOF error",
			expectations: []expectations{
				{
					key:       "a",
					value:     "b",
					wantValue: nil,
					wantPath:  nil,
					errFunc:   assert.Error,
				},
			},
			rrFunc: func(key string) runeReader {
				m := &mock_RuneReader{}
				m.On("Peek", uint(0)).Return(rune(0), assert.AnError)
				return m
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trie := NewTrie[string](nil)
			for _, expectation := range tt.expectations {
				trie.Put(expectation.key, expectation.value)
				rr := tt.rrFunc(expectation.key)
				value, path, err := trie.MatchPrefix(rr)
				expectation.errFunc(t, err)
				assert.Equal(t, expectation.wantValue, value)
				assert.Equal(t, expectation.wantPath, path)
			}
		})
	}
}
