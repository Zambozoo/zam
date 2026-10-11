package codegen

import (
	"encoding/binary"
	"io"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	base "github.com/zambozoo/zam/internal/0_base"
)

func TestReadInt(t *testing.T) {
	t.Run("a word is written little-endian", func(t *testing.T) {
		p := make([]byte, wordByteSize)

		n, err := readInt(p, 1)

		assert.NoError(t, err)
		assert.Equal(t, wordByteSize, n)
		// The runtime reads the file directly, so the low byte comes first.
		assert.Equal(t, []byte{1, 0, 0, 0, 0, 0, 0, 0}, p)
	})

	t.Run("a buffer too short for a word is refused", func(t *testing.T) {
		n, err := readInt(make([]byte, wordByteSize-1), 1)

		assert.ErrorIs(t, err, io.ErrShortBuffer)
		assert.Zero(t, n)
	})
}

func TestReadFloat64(t *testing.T) {
	p := make([]byte, wordByteSize)

	n, err := readFloat64(p, 2.5)

	assert.NoError(t, err)
	assert.Equal(t, wordByteSize, n)
	assert.Equal(t, math.Float64bits(2.5), binary.LittleEndian.Uint64(p))
}

// TestReadAllWords covers the header, the type definition map and the static
// instance map, which are all written through this one function.
func TestReadAllWords(t *testing.T) {
	values := []int{1, 2, 3}

	t.Run("each value lands in a word of its own", func(t *testing.T) {
		p := make([]byte, len(values)*wordByteSize)

		n, err := readAllWords(p, values...)

		assert.NoError(t, err)
		assert.Equal(t, len(p), n)
		for i, value := range values {
			assert.Equal(t, uint64(value),
				binary.LittleEndian.Uint64(p[i*wordByteSize:]),
				"value %d was not written at its own offset", i)
		}
	})

	t.Run("a buffer one word short is refused", func(t *testing.T) {
		// The bounds are checked a value at a time rather than once for the
		// first word, so a buffer that holds some of the values but not all of
		// them is caught rather than quietly truncating the map.
		n, err := readAllWords(make([]byte, len(values)*wordByteSize-1), values...)

		assert.ErrorIs(t, err, io.ErrShortBuffer)
		assert.Zero(t, n)
	})

	t.Run("no values write nothing", func(t *testing.T) {
		n, err := readAllWords[int](nil)

		assert.NoError(t, err)
		assert.Zero(t, n)
	})
}

// newTestReader builds a reader holding one of each section, with definitions in
// two buckets so the walk that lays them out has to cross from one to the next.
func newTestReader() *codegenReader {
	// The start indices are written out by hand rather than computed, so that a
	// walk that starts numbering somewhere else has something to disagree with.
	first := int(reservedTypeIDCount)
	cr := &codegenReader{
		counts: &counts{
			threadTypeDefinitionsStartIndex:       first,
			functionTypeDefinitionsStartIndex:     first,
			pointerDefinitionsStartIndex:          first,
			structDefinitionsStartIndex:           first + 2,
			genericStructDefinitionsStartIndex:    first + 3,
			enumDefinitionsStartIndex:             first + 3,
			classDefinitionsStartIndex:            first + 3,
			genericClassDefinitionsStartIndex:     first + 3,
			abstractDefinitionsStartIndex:         first + 3,
			genericAbstractDefinitionsStartIndex:  first + 3,
			interfaceDefinitionsStartIndex:        first + 3,
			genericInterfaceDefinitionsStartIndex: first + 3,
		},
		pointerDefinitions: []typeDefinition{&pointerDefinition{}, &pointerDefinition{}},
		structDefinitions:  []typeDefinition{&typeDefinitionStruct{}},
		staticInstanceMaps: []int{0, wordByteSize},
		stringLiterals:     base.NewStaticOrderedMap[stringLiteral, uint64](),
		natives:            base.NewStaticOrderedMap[string, uint64](),
		operations:         []operation{OpPopWord, newUnaryOp(OpPushStatic, 1)},
	}
	cr.stringLiterals.Set(stringLiteral("hi"), 0)
	// The literal's bytes are the head of the static region, so the section is
	// built through the same span writer the interning pass writes into.
	interned := internedString("hi")
	if _, err := interned.Read(cr.staticImageAt(0, uint64(interned.ByteCount()))); err != nil {
		panic(err)
	}
	cr.natives.Set("A", 0)
	cr.natives.Set("B", 1)
	return cr
}

// headerByteSize is asked of the counts rather than written down, so that a
// field added to the header does not quietly leave the expectation behind.
func headerByteSize(t *testing.T) int {
	t.Helper()
	n, err := (&counts{}).Read(make([]byte, 1024))
	assert.NoError(t, err)
	return n
}

// TestImageOpensWithMagicAndVersion covers the two words a reader has to take
// before it can trust that anything after them means what it expects. The magic
// is read as bytes rather than as a word because identifying the format is what
// happens before the byte order can be assumed.
func TestImageOpensWithMagicAndVersion(t *testing.T) {
	p := make([]byte, 1024)
	_, err := newTestReader().Read(p)
	assert.NoError(t, err)

	assert.Equal(t, magic, string(p[:wordByteSize]))

	version := binary.LittleEndian.Uint64(p[wordByteSize:])
	assert.Equal(t, uint64(versionMajor), version>>32&0xffff)
	assert.Equal(t, uint64(versionMinor), version>>16&0xffff)
	assert.Equal(t, uint64(versionPatch), version&0xffff)
}

// TestCalculateByteCounts covers the offsets the header advertises. They are
// measured from the end of the header, and they have to name the byte the
// section actually begins at, since the runtime seeks against them and nothing
// in the file says where a section really landed.
func TestCalculateByteCounts(t *testing.T) {
	cr := newTestReader()
	bc, typeDefinitionMap := cr.calculateByteCounts()

	// The map runs from type id zero, so the reserved primitive ids are part of
	// its length even though they name no definition.
	mapWordCount := int(reservedTypeIDCount) + 3
	definitionsStart := 3*wordByteSize + mapWordCount*wordByteSize
	// Two pointer definitions of three words, then an empty struct: two empty
	// counted lists and the three trailing sizes.
	definitionsEnd := definitionsStart + 2*3*wordByteSize + 5*wordByteSize

	t.Run("a section is offset by the bytes before it", func(t *testing.T) {
		// A definition counts the bytes it writes, so counting it in words and
		// scaling would put every section 8 times too far out.
		assert.Equal(t, definitionsEnd, bc.staticInstanceMapsByteOffset)
		assert.Equal(t, definitionsEnd+2*wordByteSize, bc.staticImageByteOffset)
		// "hi" is a mark word, then a length word and two bytes padded out to a
		// whole word. The mark word is what a literal has that a native's name,
		// counted below, does not.
		literalsEnd := definitionsEnd + 2*wordByteSize + 3*wordByteSize
		assert.Equal(t, literalsEnd, bc.nativesByteOffset)
		// The table opens with how many names follow, and each is laid out as a
		// literal is: a length word and its bytes, padded out to a whole word.
		nativesEnd := literalsEnd + wordByteSize + 2*2*wordByteSize
		assert.Equal(t, nativesEnd, bc.genericStaticMethodsByteOffset)
		// The generic static method registry opens with the entry count — zero
		// here, since the fixture names no generics — so the whole section is
		// that one word.
		assert.Equal(t, nativesEnd+wordByteSize, bc.operationsByteOffset)
	})

	t.Run("the map is indexed by type id", func(t *testing.T) {
		assert.Len(t, typeDefinitionMap, bc.typeDefinitionCount)

		// Nothing is serialized for the primitives, so the ids below the first
		// definition name no offset.
		for id := range int(reservedTypeIDCount) {
			assert.Zero(t, typeDefinitionMap[id], "primitive id %d names a definition", id)
		}

		// Definitions run in bucket order, so the pointers hold the first ids and
		// the struct the one after them, each offset by the widths before it.
		first := int(reservedTypeIDCount)
		assert.Equal(t, definitionsStart, typeDefinitionMap[first])
		assert.Equal(t, definitionsStart+3*wordByteSize, typeDefinitionMap[first+1])
		assert.Equal(t, definitionsStart+2*3*wordByteSize, typeDefinitionMap[first+2])
	})

	t.Run("the ids the header advertises are the ids the map is indexed by", func(t *testing.T) {
		// The header's start indices and the offset map are filled by two
		// separate walks. A bucket counted one way in one walk and another way
		// in the other leaves the header naming ids the map never filled, so
		// the indices the compiler computes are checked against the ones
		// written out by hand rather than against another walk of the buckets.
		fresh := newTestReader()
		expected := fresh.counts.words()

		(&typeDefinitionsInstantiatorVisitor{reader: fresh}).computeStartIndices()

		assert.Equal(t, expected, fresh.counts.words())
		assert.Equal(t, bc.typeDefinitionCount,
			bc.genericInterfaceDefinitionsStartIndex+len(cr.genericInterfaceDefinitions))
	})

	t.Run("each section begins where it was said to", func(t *testing.T) {
		p := make([]byte, 1024)
		n, err := cr.Read(p)
		assert.NoError(t, err)

		// Offsets are measured from the end of the header, so the header is
		// added back to reach the byte the section actually begins at.
		headerEnd := headerByteSize(t)
		assert.Equal(t, headerEnd+bc.operationsByteOffset+OpPopWord.ByteCount()+
			newUnaryOp(OpPushStatic, 1).ByteCount(), n)

		// The literal region opens with its first literal's mark word, and the
		// length a word past it, which is the offset the slot map records. The
		// native table opens with how many names it holds and the operations
		// region with the first opcode.
		literalsStart := headerEnd + bc.staticImageByteOffset
		assert.Equal(t, uint64(staticStringMarkWord),
			binary.LittleEndian.Uint64(p[literalsStart:]))
		assert.Equal(t, uint64(len("hi")),
			binary.LittleEndian.Uint64(p[literalsStart+wordByteSize:]))
		assert.Equal(t, uint64(2),
			binary.LittleEndian.Uint64(p[headerEnd+bc.nativesByteOffset:]))
		assert.Equal(t, byte(OpPopWord), p[headerEnd+bc.operationsByteOffset])
	})
}

// TestNativeTableIsWrittenInSlotOrder covers the one thing an OpNative operand
// rests on: the nth name in the table is the native the nth slot was handed out
// for. Slots are assigned while operations are emitted and the table is written
// while the image is laid out, so an order held in only one of the two walks
// leaves an operand naming a different C function than the one compiled.
func TestNativeTableIsWrittenInSlotOrder(t *testing.T) {
	cr := &codegenReader{natives: base.NewStaticOrderedMap[string, uint64]()}
	v := &operationVisitor{reader: cr}

	assert.Equal(t, uint64(0), v.nativeIndex("STD_MARK"))
	assert.Equal(t, uint64(1), v.nativeIndex("STD_ALLOC"))
	// A C symbol is one entry however many places name it, so meeting a name
	// again is not a second slot.
	assert.Equal(t, uint64(0), v.nativeIndex("STD_MARK"))

	assert.Equal(t, []stringLiteral{"STD_MARK", "STD_ALLOC"}, cr.nativeNames())
}
