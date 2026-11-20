package base

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every map in the compiler that is keyed by a type or a declaration reaches its
// bucket through these, so a hash that varies between calls on the same input is
// not a slower map but a map that never finds anything. It fails quietly, too:
// a lookup that misses looks exactly like a key that was never inserted.
func TestHashIsAFunctionOfItsInput(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Hash(1, 2, 3), Hash(1, 2, 3))
	assert.Equal(t, Hash(), Hash())
	assert.NotEqual(t, Hash(1, 2), Hash(2, 1))

	assert.Equal(t, FNV("a", "b"), FNV("a", "b"))
	assert.Equal(t, FNV(), FNV())
	assert.NotEqual(t, FNV("a", "b"), FNV("b", "a"))
}
