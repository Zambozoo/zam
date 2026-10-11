package codegen

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
)

// mainMethod is the entry point a project has to have, so that what a fixture
// writes around it is the only thing it is asking about.
const mainMethod = `
    static fun void(uint, str...) main = (argc, argv) {
        return;
    }
`

// compileTypes runs a one-file project through the front end and the two passes
// that lay out its types, and answers the reader together with the type indices
// the operations pass would need.
//
// It is compiled from source rather than assembled from a hand-built AST because
// what a type says about itself is worked out from what was declared, and a
// declared type is what a parse and a resolution produce. A fixture stating the
// answer itself would be agreeing with the pass by construction.
func compileTypes(t *testing.T, body string) (
	*syntax.AST, *codegenReader, *syntax.TypeMap[int],
) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"),
		[]byte("name: \"statics\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(body), 0o600))

	ast, errs := syntax.NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 2},
		&base.FileProvider{}, nil)
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))

	reader := &codegenReader{}
	typeIndices := buildTypeDefinitionSkeletons(ast, reader)
	populateTypeDefinitions(ast, reader, typeIndices,
		populateStringLiterals(ast, reader))
	return ast, reader, typeIndices
}

// compileStatics lays out the statics of a one-file project and answers the
// reader holding what the static region begins as, together with the slot the
// first static took.
//
// The operations pass is not run. Nothing it emits reaches the region: a static's
// value is the program's business and only the headers in front of it are the
// compiler's.
func compileStatics(t *testing.T, body string) (*codegenReader, uint64) {
	t.Helper()

	_, reader, _ := compileTypes(t, body)

	// The literals hold the slots that begin the region, populateStringLiterals
	// running first, so the statics are numbered from where they end. The count
	// is asked rather than written down because a project interns names of its
	// own whether or not the fixture mentions any.
	return reader, uint64(reader.stringLiterals.Len())
}

// staticHeader is a stamped header read back the way the runtime reads one.
//
// The fields are picked out by hand from the widths ZamMarkWord and
// ZamObjectHeader declare rather than compared against staticMarkWord, which is
// what wrote them: a repack would agree with a wrong layout as readily as a
// right one.
type staticHeader struct {
	state, kind, contained, upvalue uint64
	shortID, elementCount           uint64
	tailLength                      uint64
}

func headerAt(t *testing.T, reader *codegenReader, offset uint64) staticHeader {
	t.Helper()

	image := reader.staticImage
	require.LessOrEqual(t, offset+2*wordByteSize, uint64(len(image)),
		"the static image stops before the header at %d", offset)

	mark := binary.LittleEndian.Uint64(image[offset:])
	object := binary.LittleEndian.Uint64(image[offset+wordByteSize:])
	header := staticHeader{
		state:        mark & 0x3,
		kind:         mark >> 2 & 0x7,
		contained:    mark >> 5 & 0x1,
		upvalue:      mark >> 8,
		shortID:      object & 0xffffffff,
		elementCount: object >> 32,
	}
	if header.kind == objectKindTailedInstance {
		header.tailLength = binary.LittleEndian.Uint64(image[offset+2*wordByteSize:])
	}
	return header
}

// slotOffset is where a static lies in the region, which is the number the
// runtime reaches it by and so the number a header has to have been written at.
func slotOffset(t *testing.T, reader *codegenReader, slot uint64) uint64 {
	t.Helper()
	require.Less(t, int(slot), len(reader.staticInstanceMaps))
	return uint64(reader.staticInstanceMaps[slot])
}

// classShortID is the id the runtime names the nth declared class by, which is
// where the class bucket begins plus how far into it the class was built. It is
// asked of the compiler's own numbering rather than written down, that numbering
// being what the object header is meant to record.
func classShortID(reader *codegenReader, index int) uint64 {
	return uint64(reader.counts.classDefinitionsStartIndex+index) << 16
}

// TestStaticSlotCarriesItsHeader covers the headers a markable static begins
// with, which is the one thing about a static the compiler has to say.
//
// Zeroes are not a header. A mark word of zeroes reads as ZAMOBJECT_STRING, so
// zam_object_header_bytes answers one word where the layout reserved two, and
// every member of the slot is then reached a word short of where it was laid —
// which does not crash and is not visible in the program that did it, the write
// and the read being wrong by the same amount.
func TestStaticSlotCarriesItsHeader(t *testing.T) {
	reader, firstStatic := compileStatics(t, `
namespace Main {
    static var Point origin;
`+mainMethod+`
    class Point {
        var int x;
    }
}
`)

	offset := slotOffset(t, reader, firstStatic)
	header := headerAt(t, reader, offset)

	// White, because the state is the collector's and the collector has not run.
	assert.Zero(t, header.state)
	assert.Equal(t, uint64(objectKindInstance), header.kind)
	// Contained with no distance, which is what says the slot is not an
	// allocation and lies inside nothing. Cleared, a sweep would free memory
	// malloc never returned; set with a distance, a reader would walk out of the
	// region looking for a container.
	assert.Equal(t, uint64(1), header.contained)
	assert.Zero(t, header.upvalue)

	assert.Equal(t, classShortID(reader, 0), header.shortID)
	assert.Equal(t, uint64(1), header.elementCount)

	// The image ends at the last byte the compiler had anything to say about.
	// What follows is main's own slot, holding a closure the program builds, and
	// the loader zeroes the region before laying this over the head of it.
	assert.Equal(t, int(offset+2*wordByteSize), len(reader.staticImage))
	assert.Greater(t, slotOffset(t, reader, firstStatic+1), offset)
}

// TestStaticSlotSaysNothingWhereThereIsNoHeader covers the other side of it. A
// header is the width a member offset is added to, so one written where the
// runtime does not expect one displaces every member of the slot just as surely
// as a missing one does.
func TestStaticSlotSaysNothingWhereThereIsNoHeader(t *testing.T) {
	reader, firstStatic := compileStatics(t, `
namespace Main {
    static var int count;
    static var Point* somewhere;
    static var Thing thing;
    static var Shape* shape;
`+mainMethod+`
    class Point {
        var int x;
    }

    struct Thing {
        var int y;
    }

    abstract Shape {
        var int z;
    }
}
`)

	// A primitive is its bytes; a pointer is one word naming something whose
	// header is its own, whether it names a class or an abstract; a struct is the
	// coder's own memory and is never collected. None of the four has a header,
	// so the region says nothing past the literals that begin it.
	//
	// An abstract is only ever written as a pointer — it is a reference to the
	// implementor it names and semantic rejects it as a value — so a pointer is
	// the only way the case arises at all.
	assert.Equal(t, int(slotOffset(t, reader, firstStatic)), len(reader.staticImage))
}

// TestStaticArrayFrontAnswersForTheRun covers a slot holding more than one
// markable value: the front element is the array and carries how many there are,
// and each element behind it carries how far back the front lies. Reaching the
// length from an interior element is therefore a step to the front and a read
// there, which is what the distances have to make possible.
func TestStaticArrayFrontAnswersForTheRun(t *testing.T) {
	const elementCount = 3
	reader, firstStatic := compileStatics(t, `
namespace Main {
    static var Point[3] points;
`+mainMethod+`
    class Point {
        var int x;
    }
}
`)

	// Two words of header and one member, which is a stride wide enough that a
	// distance back to the front and a distance back one step are different
	// numbers at the last element.
	const stride = 2*wordByteSize + wordByteSize

	offset := slotOffset(t, reader, firstStatic)
	front := headerAt(t, reader, offset)
	assert.Equal(t, uint64(objectKindInstance), front.kind)
	assert.Equal(t, uint64(1), front.contained)
	assert.Zero(t, front.upvalue)
	assert.Equal(t, uint64(elementCount), front.elementCount)

	for index := uint64(1); index < elementCount; index++ {
		t.Run(fmt.Sprintf("element %d", index), func(t *testing.T) {
			element := headerAt(t, reader, offset+index*stride)
			assert.Equal(t, uint64(objectKindInstance), element.kind)
			assert.Equal(t, uint64(1), element.contained)
			assert.Equal(t, index*stride, element.upvalue)
			// One, and the front element's count is the array's. An element that
			// answered for the whole run would make the length depend on which
			// element was asked.
			assert.Equal(t, uint64(1), element.elementCount)
			assert.Equal(t, front.shortID, element.shortID)
		})
	}

	// The image ends at the last byte the compiler had anything to say about,
	// which is the last element's header rather than the end of its members: the
	// member word behind it is the zero the loader would have given it anyway.
	assert.Equal(t, int(offset+(elementCount-1)*stride+2*wordByteSize),
		len(reader.staticImage))
}

// TestStaticInlineMemberCarriesItsDistanceBack covers a static holding a class
// whose own member is a class. The inner value has a header of its own, and what
// it says is the whole of what makes an interior pointer resolvable: one step
// back of that many bytes lands on the mark word of the thing holding it.
func TestStaticInlineMemberCarriesItsDistanceBack(t *testing.T) {
	reader, firstStatic := compileStatics(t, `
namespace Main {
    static var Nest nest;
`+mainMethod+`
    class Nest {
        var Point inner;
    }

    class Point {
        var int x;
    }
}
`)

	offset := slotOffset(t, reader, firstStatic)
	outer := headerAt(t, reader, offset)
	assert.Equal(t, classShortID(reader, 0), outer.shortID)
	assert.Zero(t, outer.upvalue)

	// The member offsets the compiler records are net of the header, so the
	// member at offset zero begins where the header ends — and the container's
	// mark word being at the container's own start, that same number is how far
	// back the container lies.
	const back = 2 * wordByteSize
	inner := headerAt(t, reader, offset+back)
	assert.Equal(t, uint64(objectKindInstance), inner.kind)
	assert.Equal(t, uint64(1), inner.contained)
	assert.Equal(t, uint64(back), inner.upvalue)
	assert.Equal(t, uint64(1), inner.elementCount)
	assert.Equal(t, classShortID(reader, 1), inner.shortID)
}

// TestStaticInlineArrayMemberIsStampedThrough covers a member written as a run
// rather than as one value. The record carries how many elements lie there, and
// that length is the whole of what says so — a stamp that read the member as a
// single value would write the front element's header and leave the two behind
// it as the zeroes the loader gave them, which read as ZAMOBJECT_STRING.
//
// That is not a header that is merely wrong: it is one word where the layout
// reserved two, so a member of the second element resolves a word early and the
// collector walking the run reaches a container that was never written.
//
// The distances are the assertion. An element's is to the front of its own run
// and not to the class the run lies in, so they are the same numbers a run in a
// slot of its own gets — which is what lets one step out of any element land on
// the value that answers for the length.
func TestStaticInlineArrayMemberIsStampedThrough(t *testing.T) {
	const elementCount = 3
	reader, firstStatic := compileStatics(t, `
namespace Main {
    static var Nest nest;
`+mainMethod+`
    class Nest {
        var Point[3] inner;
    }

    class Point {
        var int x;
    }
}
`)

	// Two words of header and one member, which is a stride wide enough that a
	// distance back to the front and a distance back one step are different
	// numbers at the last element.
	const stride = 2*wordByteSize + wordByteSize
	// The member offsets recorded are net of the header, so the member at offset
	// zero begins where the container's header ends.
	const back = 2 * wordByteSize

	offset := slotOffset(t, reader, firstStatic)
	assert.Equal(t, classShortID(reader, 0), headerAt(t, reader, offset).shortID)

	front := headerAt(t, reader, offset+back)
	assert.Equal(t, uint64(objectKindInstance), front.kind)
	assert.Equal(t, uint64(1), front.contained)
	// The front of the run is contained by the class holding it, so its distance
	// is the member's offset — and it is the front that answers for how many
	// elements there are, the record's length having nowhere else to go.
	assert.Equal(t, uint64(back), front.upvalue)
	assert.Equal(t, uint64(elementCount), front.elementCount)
	assert.Equal(t, classShortID(reader, 1), front.shortID)

	for index := uint64(1); index < elementCount; index++ {
		t.Run(fmt.Sprintf("element %d", index), func(t *testing.T) {
			element := headerAt(t, reader, offset+back+index*stride)
			assert.Equal(t, uint64(objectKindInstance), element.kind)
			assert.Equal(t, uint64(1), element.contained)
			// To the front of the run rather than to the class, which is what
			// makes this the same number the run would carry anywhere else.
			assert.Equal(t, index*stride, element.upvalue)
			assert.Equal(t, uint64(1), element.elementCount)
			assert.Equal(t, front.shortID, element.shortID)
		})
	}

	assert.Equal(t,
		int(offset+back+(elementCount-1)*stride+2*wordByteSize),
		len(reader.staticImage))
}

// TestStaticTailedSlotCarriesItsTailLength covers the third header word, which
// is the one thing about a tailed instance the id it is reached through may not
// be able to say — a class reached under a supertype's id is reached under an id
// that cannot state a length only the concrete type has. A slot laid out for a
// tail and stamped as untailed would have its members read a word early.
func TestStaticTailedSlotCarriesItsTailLength(t *testing.T) {
	reader, firstStatic := compileStatics(t, `
namespace Main {
    static var Buffer~5 buffer;
`+mainMethod+`
    class Buffer~ {
        var int count;
        var int[~] elements;
    }
}
`)

	header := headerAt(t, reader, slotOffset(t, reader, firstStatic))
	assert.Equal(t, uint64(objectKindTailedInstance), header.kind)
	assert.Equal(t, uint64(1), header.contained)
	assert.Zero(t, header.upvalue)
	assert.Equal(t, uint64(1), header.elementCount)
	assert.Equal(t, uint64(5), header.tailLength)
}
