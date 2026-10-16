package codegen

import (
	"encoding/binary"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// runHarness asks the instance harness about one subject and splits what it
// says into lines.
func runHarness(t *testing.T, harness, subject, stdin string) []string {
	t.Helper()

	run := exec.Command(harness, subject)
	run.Stdin = strings.NewReader(stdin)
	out, err := run.Output()
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// TestPrimitiveLayoutCConformance holds the runtime's primitive widths to the
// compiler's.
//
// Nothing in the image states how wide an int or a char is, so unlike a member
// offset — which the runtime reads out of the definition and therefore cannot
// get wrong — this is two independent descriptions of the same thing:
// primitiveByteInfo here and the constants in instance.h there. A disagreement
// between them is silent, because a value read at the wrong width is still a
// value.
//
// The primitives are asked for by number rather than by name, which makes this
// a check on the ordering as much as on the widths.ZamPrimitive is declared to
// be the leading run of syntax.TypeKind, and a kind inserted among them would
// shift every primitive after it the way an opcode inserted mid-table shifts
// every opcode after it. Here that shows up either as a width that stopped
// matching or as a number the runtime says it has never heard of.
func TestPrimitiveLayoutCConformance(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "instance_harness.c", "instances")

	primitives := []syntax.TypeKind{
		syntax.TypeKindVoid,
		syntax.TypeKindBool,
		syntax.TypeKindChar,
		syntax.TypeKindByte,
		syntax.TypeKindInt,
		syntax.TypeKindFloat,
		syntax.TypeKindString,
		syntax.TypeKindType,
		syntax.TypeKindAny,
		syntax.TypeKindUint,
	}

	// The count is the ordering check stated directly: the runtime's
	// ZAMPRIMITIVE_COUNT is where declared types begin, so it has to be the
	// number of kinds above and they have to be the first ones.
	want := []string{fmt.Sprintf("primitiveCount=%d", len(primitives))}
	for id, kind := range primitives {
		require.Equal(t, id, int(kind),
			"ZamPrimitive is the leading run of syntax.TypeKind, so %s must be %d and is %d",
			kind, id, int(kind))

		size, align := primitiveByteInfo(kind)
		want = append(want,
			fmt.Sprintf("primitive[%d].bytes=%d", id, size),
			fmt.Sprintf("primitive[%d].align=%d", id, align))
	}

	assert.Equal(t, want, runHarness(t, harness, "primitives", ""))
}

// TestStringLayoutCConformance reads a string the way the runtime does out of
// the bytes the compiler interns it as.
//
// A string literal is not copied on the way in: it is written into the static
// region on load and pushed as the address of its string, so the compiler's
// interned form is directly the runtime's read form. That also means the
// padding matters as much as the contents — the width one side thinks a literal
// occupies is where the other side thinks the next slot begins.
//
// The mark word in front is the part no string operation reads. Every one of
// them would go on working if the compiler stopped writing the word, because
// what it says is not about the string: it is the contained bit, which is where
// a literal says it is not an allocation. A word left empty would say the
// opposite, and a delete of a literal would hand free a pointer into the static
// region. So the word is asserted here rather than left to the one instruction
// that reads it.
func TestStringLayoutCConformance(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "instance_harness.c", "instances")

	for _, literal := range []string{
		"",
		"hi",
		// Seven bytes needs a pad word, eight exactly fills one. Both sides
		// round up, so the pair catches a rule that rounded the wrong way as
		// well as one that stopped rounding.
		"abcdefg",
		"abcdefgh",
		// A zero byte in the middle, which is the case a length prefix exists
		// for and a terminator would lose.
		"a\x00b",
	} {
		t.Run(fmt.Sprintf("%q", literal), func(t *testing.T) {
			s := internedString(literal)
			p := make([]byte, s.ByteCount())
			n, err := s.Read(p)
			require.NoError(t, err)
			require.Equal(t, len(p), n)

			// The word in front is the mark word: the contained bit and
			// nothing else, a literal being white and a string and inside
			// nothing.
			markWord := make([]byte, wordByteSize)
			binary.LittleEndian.PutUint64(markWord, staticStringMarkWord)
			require.Equal(t, markWord, p[:wordByteSize])

			want := []string{
				// The slot is the string. A string begins with its own mark
				// word, so nothing is stepped over to reach the length.
				"stringOffset=0",
				fmt.Sprintf("length=%d", len(literal)),
			}
			for i := 0; i < len(literal); i++ {
				want = append(want, fmt.Sprintf("byte[%d]=%d", i, literal[i]))
			}
			want = append(want,
				fmt.Sprintf("byteCount=%d", s.ByteCount()),
				fmt.Sprintf("given=%d", len(p)))

			assert.Equal(t, want, runHarness(t, harness, "string", string(p)))
		})
	}
}

// TestObjectHeaderInvariant pins the one thing the headers exist to make true.
// Nothing here is on the wire — the headers are the runtime's own and the
// compiler never writes one — so this is not a conformance check but the
// layout's own claim, asserted where it will be noticed.
//
// The claim: an allocation has one of three header shapes, and the word the
// pointer names is the mark word in all three. That is what lets the collector
// read what an allocation is before it knows how much header to expect, and it
// is why each longer shape puts its extra word after rather than before. Every
// step forward after the first is licensed by what the step before it read — the
// kind says whether a type id follows, and the definition found by that type id
// says whether a tail length does.
//
// Reading all three shapes back through the same accessor is the direct test of
// it, which is why the string case is here despite carrying nothing to read.
func TestObjectHeaderInvariant(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "instance_harness.c", "instances")

	assert.Equal(t, []string{
		"markBytes=8",
		"typedBytes=16",
		"tailedBytes=24",
		// A string pays one word and everything else two. The kind is in the
		// mark word rather than being read off a type id precisely so that a
		// string can be understood without one, which is what lets it be the
		// kind that goes without.
		"stringHeaderBytes=8",
		"functionClosureHeaderBytes=16",
		"referenceMethodClosureHeaderBytes=16",
		"valueMethodClosureHeaderBytes=16",
		"objectHeaderBytes=16",
		"tailedHeaderBytes=24",
		// The one shape no kind answers for, a tailed struct having no mark word
		// for a kind to sit in. What holds this to the compiler is
		// TestInstanceHeaderByteSizeCConformance; here it is only the number.
		"tailedStructHeaderBytes=8",
		// The one-word discriminant every enum value carries. See
		// TestInstanceHeaderByteSizeCConformance for the compiler/runtime pin.
		"enumHeaderBytes=8",
		"string.state=1",
		"string.kind=0",
		"string.upvalue=4276993775",
		// The same accessor, at the head of a two word header, still lands on
		// the mark word.
		"typed.kind=1",
		"typed.state=2",
		"typed.shortID=589827",
		"typed.elementCount=5",
		// And again from a three word header. The kind is what said there was a
		// third word to read, so it is the tailed one here and not the plain one
		// above.
		"tailed.kind=2",
		"tailed.shortID=458754",
		"tailed.elementCount=4",
		"tailed.tailLength=12",
		// The upvalue has the rest of the word to itself: 2^56-1 comes back
		// whole, and the kind beside it is undisturbed.
		"wide.kind=6",
		"wide.upvalue=72057594037927935",
	}, runHarness(t, harness, "header", ""))
}

// TestInstanceHeaderByteSizeCConformance holds instanceHeaderByteSize to the
// widths the runtime actually steps over.
//
// This is the pair TestObjectHeaderInvariant does not check. That one says the
// runtime's three shapes are self-consistent; this one says the compiler agrees
// about how wide they are. Nothing on the wire states it: a member's offset is
// recorded net of the header — see layoutOf — and the reader adds the difference
// back, so the gap exists only as the same number written down on both sides.
// Getting it wrong reads every member of every instance one word off.
//
// The tailed struct is the case this exists for. Every other shape is reached by
// a program the other conformance tests already run, but a struct's header is
// asked for by definition rather than by kind — a struct has no mark word to
// hold one — so nothing but this compares the two claims.
func TestInstanceHeaderByteSizeCConformance(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "instance_harness.c", "instances")

	runtimeBytes := map[string]uint64{}
	for _, line := range runHarness(t, harness, "header", "") {
		name, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "the harness said %q, which is not a pair", line)
		parsed, err := strconv.ParseUint(value, 10, 64)
		require.NoError(t, err)
		runtimeBytes[name] = parsed
	}

	for _, test := range []struct {
		name   string
		kind   syntax.FieldKind
		tailed bool
		// reported is what the harness calls the same width, or empty where the
		// runtime adds nothing and so states no number to be held to.
		reported string
	}{
		{"class", syntax.KindClass, false, "objectHeaderBytes"},
		{"tailed class", syntax.KindClass, true, "tailedHeaderBytes"},
		// An abstract is never laid out anywhere and is laid out all the same: a
		// class flattens its base's members ahead of its own, so an abstract that
		// measured its header differently would move every member of every class
		// under it.
		{"abstract", syntax.KindAbstract, false, "objectHeaderBytes"},
		{"tailed abstract", syntax.KindAbstract, true, "tailedHeaderBytes"},
		{"tailed struct", syntax.KindStruct, true, "tailedStructHeaderBytes"},
		{"enum", syntax.KindEnum, false, "enumHeaderBytes"},

		// The one that keeps none. There is no runtime number to compare, the
		// reader adding nothing at all, so what is asserted is the compiler's
		// side alone: a header here is one the runtime would never step over.
		{"struct", syntax.KindStruct, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiler := instanceHeaderByteSizeOf(test.kind, test.tailed)
			if test.reported == "" {
				assert.Zero(t, compiler)
				return
			}

			reported, ok := runtimeBytes[test.reported]
			require.True(t, ok, "the harness never mentioned %s", test.reported)
			assert.Equal(t, reported, compiler)
		})
	}
}

// TestClosureWalkInvariant is the closure's counterpart to the header one, and
// is a layout claim rather than a conformance check for the same reason: no
// image states a closure's shape, so there is no second description to hold this
// one to.
//
// The claim: a closure can be walked with nothing outside it. Every value in one
// is written under its own type id, so the collector reaches each entry only
// through the width the entry before it gave back — a chain in which one wrong
// width strands everything after it. The walk ending exactly where the writing
// ended is what says no link in it was wrong.
//
// The receiver is written as the first entry rather than as a field of its own,
// which is why the harness loop has no case for it. That was the point of
// storing a type in front of every value instead of only in front of the
// captures.
func TestClosureWalkInvariant(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "instance_harness.c", "instances")

	assert.Equal(t, []string{
		// A closure is its allocation: the typed header is its own first field,
		// so the pointer the program holds names the closure and the mark word
		// alike.
		"closureOffset=0",
		// The two header words, two head words, then three entries of a type id
		// and one word each.
		"closureWords=16",
		"instruction=64",
		"captureCount=2",
		// The receiver, read by the same accessors as the captures below it.
		"entry[0].shortID=589827",
		"entry[0].value=4369",
		fmt.Sprintf("entry[1].shortID=%d", uint32(syntax.TypeKindInt)<<16),
		"entry[1].value=7",
		fmt.Sprintf("entry[2].shortID=%d", uint32(syntax.TypeKindString)<<16),
		"entry[2].value=8738",
		// Where the walk finished, which has to be where the writing did.
		"walkedWords=16",
	}, runHarness(t, harness, "closure", ""))
}
