package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

// A canonical order is the same however a set was filled, and the order it was
// filled in when none is asked for.
func TestOrderedTypes(t *testing.T) {
	intPointer := syntax.NewWrappedType(syntax.TypeKindPointer, syntax.TypeInt)
	strPointer := syntax.NewWrappedType(syntax.TypeKindPointer, syntax.TypeString)
	forward, backward := syntax.NewTypeSet(), syntax.NewTypeSet()
	forward.Add(strPointer)
	forward.Add(intPointer)
	backward.Add(intPointer)
	backward.Add(strPointer)

	assert.Equal(t, []syntax.Type{strPointer, intPointer}, orderedTypes(forward))

	t.Setenv("ZAM_CANONICAL_ORDER", "1")
	assert.Equal(t, orderedTypes(backward), orderedTypes(forward))
	assert.Equal(t, []syntax.Type{intPointer, strPointer}, orderedTypes(forward))
}

// An instantiation still holding an open argument, as a generic struct names
// itself inside its own body, is given no definition: only the ground ones are.
func TestOpenConcretesHaveNoDefinition(t *testing.T) {
	_, reader, _ := compileTypes(t, `namespace Main {
    struct Node[T] {
        var T value;
        fun Node[T]() Copy = () {
            var Node[T] copy;
            return copy;
        }
    }
    static var Node[int] ints;
    static var Node[str] strs;
`+mainMethod+`}
`)
	require.Len(t, reader.genericStructDefinitions, 2)
}
