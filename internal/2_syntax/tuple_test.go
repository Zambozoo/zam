package syntax

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lexKinds(t *testing.T, src string) []TokenKind {
	t.Helper()
	tokens, err := Lex(io.NopCloser(strings.NewReader(src)))
	require.NoError(t, err)
	kinds := make([]TokenKind, len(tokens))
	for i, tok := range tokens {
		kinds[i] = tok.Kind
	}
	return kinds
}

func TestLexerReadsDigitsAfterAPeriodAsAnIndex(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		[]TokenKind{TTIdentifier, TTPeriod, TTIntLiteral, TTPeriod, TTIntLiteral},
		lexKinds(t, "t.0.1"))
	assert.Equal(t, []TokenKind{TTFloatLiteral}, lexKinds(t, "0.1"))
}

func TestTypeTupleEquality(t *testing.T) {
	t.Parallel()

	a := NewTypeTuple(nil, []Type{TypeInt, TypeString})
	b := NewTypeTuple(nil, []Type{TypeInt, TypeString})
	c := NewTypeTuple(nil, []Type{TypeString, TypeInt})

	assert.True(t, a.Equals(b))
	assert.Equal(t, a.Hash(), b.Hash())
	assert.False(t, a.Equals(c))
	assert.Equal(t, "(int, str)", a.String())
}

func TestTypeTupleIsSubtypeOnlyOfItselfAndAny(t *testing.T) {
	t.Parallel()

	tuple := NewTypeTuple(nil, []Type{TypeInt, TypeNull})
	pointers := NewTypeTuple(nil, []Type{TypeInt, NewWrappedType(TypeKindPointer, TypeInt)})

	assert.True(t, tuple.IsSubtypeOf(tuple))
	assert.True(t, tuple.IsSubtypeOf(TypeAny))
	assert.False(t, tuple.IsSubtypeOf(pointers))
	assert.False(t, pointers.IsSubtypeOf(tuple))
}

func TestTupleElement(t *testing.T) {
	t.Parallel()

	tuple := NewTypeTuple(nil, []Type{TypeInt, TypeString})

	element, ok := TupleElement(tuple, "1")
	assert.True(t, ok)
	assert.Equal(t, TypeString, element)

	for _, index := range []string{"2", "-1", "01", "x"} {
		_, ok := TupleElement(tuple, index)
		assert.False(t, ok, index)
	}
	_, ok = TupleElement(TypeInt, "0")
	assert.False(t, ok)
}
