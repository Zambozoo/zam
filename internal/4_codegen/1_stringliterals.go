package codegen

import (
	"encoding/binary"
	"io"
)

type (
	// stringLiteral is a length-prefixed string as it is written anywhere in the
	// image: an 8-byte length followed by its bytes, padded up to a word
	// boundary. A native's name in the natives table is one of these and nothing
	// more, since nothing at run time holds a pointer to one.
	stringLiteral string

	// internedString is a string literal as it lies in the static region: a mark
	// word, which is what the program's pointer names, and then the
	// stringLiteral it stands in front of.
	//
	// The word is there because every markable value begins with one — see
	// ZamMarkWord in internal/5_build/runtime/src/instance.h — and a literal is pushed as its slot
	// address with nothing copied, so if a literal lacked the word it would be
	// the one string in the system that could not be asked what it was.
	//
	// The word is read, and by more than the collector: it is where a literal
	// says it is not an allocation, which is what stops a delete of one from
	// handing free a pointer into the static region. See staticStringMarkWord.
	internedString stringLiteral
)

// The widths of the mark word fields, mirroring ZAM_MARK_STATE_BITS,
// ZAM_OBJECT_KIND_BITS and the contained and reserved fields of ZamMarkWord in
// internal/5_build/runtime/src/instance.h. The upvalue holds what is left of the word.
const (
	markStateBits      = 2
	markObjectKindBits = 3
	markContainedBits  = 1
	markReservedBits   = 2
)

// The object kinds the compiler lays down, mirroring ZamObjectKind. These three
// are all it names: the rest are allocations the runtime makes for itself, and a
// kind it never writes is a kind it has no bytes to write.
const (
	objectKindString = iota
	objectKindInstance
	objectKindTailedInstance
)

// staticMarkWord is the mark word every markable value laid into the static
// region carries: white, of the given kind, contained, and that far back to what
// holds it.
//
// White because the state is the collector's and the collector has not run.
// Contained because the bit is what says the slot is not an allocation — see
// ZamMarkWord, where it decides both whether the upvalue is a sweep-list link
// and whether the block may be freed — and freeing a static would release memory
// malloc never returned. The distance is zero for a value the region holds
// directly, contained-with-no-distance being how the same bit covers a value
// inside something and a value inside nothing.
//
// This is the one place the compiler spells out the runtime's mark bits, which is
// why it is pinned from the C side rather than only asserted here.
func staticMarkWord(kind, backDistance uint64) uint64 {
	const containedShift = markStateBits + markObjectKindBits
	const upvalueShift = containedShift + markContainedBits + markReservedBits
	return kind<<markStateBits | 1<<containedShift | backDistance<<upvalueShift
}

// staticStringMarkWord is the mark word every interned literal carries, which is
// the general rule with nothing to say: ZAM_OBJECT_STRING is zero and nothing
// contains a literal, so the contained bit is the whole of it.
var staticStringMarkWord = staticMarkWord(objectKindString, 0)

// ByteCount implements the [byteCountable] interface. The +1 is the trailing
// NUL every string carries one byte past its last content byte, matching
// zam_string_bytes on the runtime side; both halves have to size the slot the
// same way, because the compiler lays this slot out and the runtime reads it
// directly without copying.
func (s stringLiteral) ByteCount() int {
	return int(alignUp(uint64(wordByteSize+len(s)+1), wordByteSize))
}

// Read implements the [byteCountable] interface.
func (s stringLiteral) Read(buf []byte) (int, error) {
	if len(buf) < s.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	binary.LittleEndian.PutUint64(buf, uint64(len(s)))
	copy(buf[wordByteSize:], s)
	for i := wordByteSize + len(s); i < s.ByteCount(); i++ {
		buf[i] = 0
	}
	return s.ByteCount(), nil
}

// ByteCount implements the [byteCountable] interface.
func (s internedString) ByteCount() int {
	return wordByteSize + stringLiteral(s).ByteCount()
}

// Read implements the [byteCountable] interface.
//
// The mark word says the one thing about a literal that has to be said: it is not
// an allocation. A literal lies in the static region, so freeing it would release
// memory malloc never returned and collecting it would drop a value that outlives
// every frame naming it — and the state bits cannot say either, every colour
// being a colour of something the collector owns.
func (s internedString) Read(buf []byte) (int, error) {
	if len(buf) < s.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	binary.LittleEndian.PutUint64(buf, staticStringMarkWord)
	if _, err := stringLiteral(s).Read(buf[wordByteSize:]); err != nil {
		return 0, err
	}
	return s.ByteCount(), nil
}

// internString answers the static slot holding literal's length-prefixed bytes,
// laying one out in the literal region the first time literal is asked for.
//
// The recorded offset is the slot's own, which is the mark word: that is where a
// pointer to any markable value points, and the length lies a word past it. A
// literal is therefore pushable as its offset with nothing copied.
func (cr *codegenReader) internString(literal stringLiteral) uint64 {
	if slot, index := cr.stringLiterals.Get(literal); index >= 0 {
		return slot
	}

	slot := uint64(len(cr.staticInstanceMaps))
	cr.stringLiterals.Set(literal, slot)
	cr.staticInstanceMaps = append(cr.staticInstanceMaps, len(cr.literalImage))

	interned := internedString(literal)
	at := len(cr.literalImage)
	cr.literalImage = append(cr.literalImage, make([]byte, interned.ByteCount())...)
	if _, err := interned.Read(cr.literalImage[at:]); err != nil {
		panic("unreachable")
	}
	return slot
}

// placeLiterals lays the literal region at the head of the static region and
// moves every static past it.
//
// Statics are laid out before any literal is known, but only the slot map and
// the static image hold where one lies: everything else names a static by its
// slot, and a stamped header measures only back to what holds it. Placing the
// literals first keeps the image from carrying the statics' zeroes ahead of
// them, since it stops at the last byte anything wrote.
func (cr *codegenReader) placeLiterals() {
	if len(cr.literalImage) == 0 {
		return
	}
	shift := alignUp(uint64(len(cr.literalImage)), max(cr.staticAlign, wordByteSize))

	literals := make(map[uint64]struct{}, cr.stringLiterals.Len())
	for _, entry := range cr.stringLiterals.All() {
		literals[entry.Value] = struct{}{}
	}
	for slot := range cr.staticInstanceMaps {
		if _, ok := literals[uint64(slot)]; !ok {
			cr.staticInstanceMaps[slot] += int(shift)
		}
	}

	image := cr.literalImage
	if len(cr.staticImage) > 0 {
		image = append(image, make([]byte, shift-uint64(len(image)))...)
		image = append(image, cr.staticImage...)
	}
	cr.staticImage = image
	cr.counts.staticRegionByteSize += int(shift)
	cr.literalImage = nil
}
