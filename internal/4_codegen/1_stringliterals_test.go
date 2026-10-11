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
func TestInternedSlotsNameTheirStrings(t *testing.T) {
	literals := []stringLiteral{"hi", "abcdefgh", ""}

	reader := &codegenReader{
		stringLiterals: base.NewStaticOrderedMap[stringLiteral, uint64](),
	}
	visitor := &stringLiteralVisitor{reader: reader}
	for _, literal := range literals {
		visitor.intern(literal)
		// Interning the same literal twice is interning it once: the visitor
		// reaches an inherited member's expression through every subclass that
		// has it, and each of those is the same literal and not another one.
		visitor.intern(literal)
	}

	require.Len(t, reader.staticInstanceMaps, len(literals))

	// The region as the image carries it, which is what the recorded offsets
	// are offsets into.
	region := make([]byte, visitor.offset)
	written := 0
	for _, literal := range literals {
		n, err := internedString(literal).Read(region[written:])
		require.NoError(t, err)
		written += n
	}
	// The running offset is what the static fields are laid out after, so a
	// literal region measured short would have the first static field sitting
	// on top of the last literal.
	assert.Equal(t, written, len(region))

	for i, literal := range literals {
		_, index := reader.stringLiterals.Get(literal)
		require.Equal(t, i, index, "%q was interned out of order", literal)

		// The slot begins with the mark word, and an offset that named the
		// string instead would read the length as a state and a kind, and the
		// contained bit as whatever the length's sixth bit happened to be.
		offset := reader.staticInstanceMaps[index]
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
