package codegen

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
)

// TestInternedSlotsNameTheirStrings covers the one thing about interning that
// has no reader to complain: a literal is pushed as the offset recorded here,
// and that offset has to name the slot — the mark word every markable value
// begins with — with the length a word past it.
//
// The mark word in front is what makes the slot and the string two different
// addresses. An offset a word long would have every use of the literal read the
// first byte of the string as the length, and nothing on the runtime side would
// report that — a length is a length. So the region is laid out here and read
// back at the offsets the compiler recorded, which is the only place the
// difference is visible.
//
// A static is laid out ahead of the literals, as the static layout pass does,
// and to an alignment wider than a word, so that moving it past them has to
// keep it aligned.
func TestInternedSlotsNameTheirStrings(t *testing.T) {
	literals := []stringLiteral{"hi", "abcdefgh", ""}
	static := []byte{1, 2, 3}
	const staticAlign = 4 * wordByteSize

	reader := &codegenReader{
		counts:             &counts{staticRegionByteSize: len(static)},
		stringLiterals:     base.NewStaticOrderedMap[stringLiteral, uint64](),
		staticInstanceMaps: []int{0},
		staticImage:        append([]byte(nil), static...),
		staticAlign:        staticAlign,
	}
	for i, literal := range literals {
		slot := reader.internString(literal)
		require.Equal(t, uint64(i+1), slot, "%q was interned out of order", literal)
		// Interning the same literal twice is interning it once: a literal in an
		// inherited member's initializer is emitted for every subclass that has
		// it, and each of those is the same literal and not another one.
		require.Equal(t, slot, reader.internString(literal))
	}
	reader.placeLiterals()

	require.Len(t, reader.staticInstanceMaps, len(literals)+1)
	region := reader.staticImage

	// The static follows the literals at its own alignment, its bytes moved with
	// it, and the region grows by as much as it moved.
	moved := reader.staticInstanceMaps[0]
	last := reader.staticInstanceMaps[len(literals)]
	assert.GreaterOrEqual(t, moved, last+internedString(literals[len(literals)-1]).ByteCount(),
		"the static was left under the literals")
	assert.Zero(t, moved%staticAlign, "the static lost its alignment")
	assert.Equal(t, static, region[moved:moved+len(static)])
	assert.Equal(t, moved+len(static), reader.counts.staticRegionByteSize)

	for i, literal := range literals {
		// The slot begins with the mark word, and an offset that named the
		// string instead would read the length as a state and a kind, and the
		// contained bit as whatever the length's sixth bit happened to be.
		offset := reader.staticInstanceMaps[i+1]
		assert.Zero(t, offset%wordByteSize, "%q's mark word is not word aligned", literal)
		assert.Equal(t, uint64(staticStringMarkWord),
			binary.LittleEndian.Uint64(region[offset:]),
			"the offset recorded for %q does not land on its mark word", literal)

		head := offset + wordByteSize
		assert.Equal(t, uint64(len(literal)),
			binary.LittleEndian.Uint64(region[head:]),
			"the word past %q's mark word is not its length", literal)
		assert.Equal(t, string(literal),
			string(region[head+wordByteSize:head+wordByteSize+len(literal)]))
	}
}
