package codegen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// The one declared type these fixtures have, which is main's own function type.
// The primitives hold every id below reservedTypeIDCount, so the first
// declaration is that count and no lower.
var (
	mainFunctionDeclID = uint64(reservedTypeIDCount)
	mainFunctionShort  = mainFunctionDeclID << 16
)

// buildRunHarness compiles the harness that runs an image, which needs the whole
// runtime rather than only the readers: everything but main.c, since a harness
// brings its own entry point.
func buildRunHarness(t *testing.T) string {
	t.Helper()
	return buildHarness(t, "run_harness.c", "run",
		"run.c", "thread.c", "vm.c", "gc.c", "native.c", "opcode.c")
}

// valueEntryBytes is how wide the mark entry a markable value standing on the
// stack owns is: its kind and offset, then its type id.
const valueEntryBytes = 4 * wordByteSize

// pushI64 is the instruction a word is laid onto the stack by.
func pushI64(value uint64) operation { return newUnaryOp(OpPushI64, value) }

// dieOutput is what a successful zam_die writes to stderr: the message wrapped
// in the red + reset ANSI pair the C runtime adds. Tests that assert the exact
// string use this so a change to the color codes needs only this one update.
func dieOutput(msg string) string {
	return "\033[31m" + msg + "\033[0m"
}

// pushFloat is the same instruction told a float's bits, which is what a float
// on the operand stack is. There is no float push: a word is a word, and what
// makes it a float is the instruction that reads it.
func pushFloat(value float64) operation {
	return pushI64(math.Float64bits(value))
}

// pushTypeID is the three OpPushI64s a type id is laid onto the stack by, which
// is how the compiler pushes one wherever the id is known at compile time. The
// short id rides in the high half of the first word, and neither an array
// length nor a tail length is in play for a function type.
func pushTypeID(shortID uint64) []operation {
	return pushTailedTypeID(shortID, 0)
}

// pushArrayTypeID is the same three words with the middle one filled in. An
// array length rides in the id for the reason a tail length does: the definition
// carries the stride and only the id can say how many, so two arrays of the same
// element are different widths under the same declaration.
func pushArrayTypeID(shortID, arrayLength uint64) []operation {
	return []operation{pushI64(shortID << 32), pushI64(arrayLength), pushI64(0)}
}

// pushTailedTypeID is the same three words with the last one filled in. A tail
// length lives in the id rather than in the definition because the definition
// carries the stride and only the id can say how many: two instances of the same
// tailed type are different widths and are named by the same declaration.
func pushTailedTypeID(shortID, tailLength uint64) []operation {
	return []operation{
		pushI64(shortID << 32),
		pushI64(0),
		pushI64(tailLength),
	}
}

// The declared types these fixtures have, each laid out as the compiler lays
// one: the header at the head, a single member after it, and the member's
// recorded offset net of that header. They follow main's function type, and the
// struct precedes the classes because the buckets are numbered in that order.
//
// The nest class is the second class rather than a change to the first, because
// what the first one's width and member mean is asserted all over this file. It
// holds one member and that member is a class value, which makes it the one
// fixture where a header lies inside a header.
// The four function types after main's are the ones a subtype test can be asked
// about, since a signature is what a function stands below rather than any
// declaration. They come in two pairs, each pair differing in one position and
// in the descent below — one pair in the parameter and one in the return — so
// each says which direction that position is read in and neither could pass by
// the two ids simply matching.
var (
	fixtureParentParamDeclID  = mainFunctionDeclID + 1
	fixtureParentParamShort   = fixtureParentParamDeclID << 16
	fixtureChildParamDeclID   = mainFunctionDeclID + 2
	fixtureChildParamShort    = fixtureChildParamDeclID << 16
	fixtureParentReturnDeclID = mainFunctionDeclID + 3
	fixtureParentReturnShort  = fixtureParentReturnDeclID << 16
	fixtureChildReturnDeclID  = mainFunctionDeclID + 4
	fixtureChildReturnShort   = fixtureChildReturnDeclID << 16

	// The two pointers next, the pointer bucket being numbered after the function
	// types. A pointer is a declared type of its own and what it points at lives
	// in its record, so these are the only ids from which the runtime can be made
	// to take a step before answering — which is the whole of what they are here
	// for. One points at the abstract and one at the class below it, so the pair
	// asks a question about descent through two ids that share nothing.
	fixtureAbstractPointerDeclID = mainFunctionDeclID + 5
	fixtureAbstractPointerShort  = fixtureAbstractPointerDeclID << 16
	fixtureChildPointerDeclID    = mainFunctionDeclID + 6
	fixtureChildPointerShort     = fixtureChildPointerDeclID << 16

	// Three more pointers, for the two rules a pointer answers without asking
	// what it points at. The void one stands above every pointer; the other two
	// point at one tailed struct, one with a length and one without, which is the
	// only pair in the image whose pointees differ in nothing but that word.
	fixtureVoidPointerDeclID = mainFunctionDeclID + 7
	fixtureVoidPointerShort  = fixtureVoidPointerDeclID << 16
	fixtureSizedTailPtrDecl  = mainFunctionDeclID + 8
	fixtureSizedTailPtrShort = fixtureSizedTailPtrDecl << 16
	fixtureOpenTailPtrDecl   = mainFunctionDeclID + 9
	fixtureOpenTailPtrShort  = fixtureOpenTailPtrDecl << 16

	fixtureStructDeclID = mainFunctionDeclID + 10
	fixtureStructShort  = fixtureStructDeclID << 16
	fixtureClassDeclID  = mainFunctionDeclID + 11
	fixtureClassShort   = fixtureClassDeclID << 16
	fixtureNestDeclID   = mainFunctionDeclID + 12
	fixtureNestShort    = fixtureNestDeclID << 16
	fixtureRunDeclID    = mainFunctionDeclID + 13
	fixtureRunShort     = fixtureRunDeclID << 16
	fixtureNestRunDecl  = mainFunctionDeclID + 14
	fixtureNestRunShort = fixtureNestRunDecl << 16

	// The child class is the one class that stands below something. It is a fifth
	// class rather than a supertype set added to the first, because what the
	// first one's width and members mean is asserted all over this file.
	fixtureChildDeclID = mainFunctionDeclID + 15
	fixtureChildShort  = fixtureChildDeclID << 16

	// The generic class follows the plain ones, the generic class bucket being
	// numbered after the class bucket. Its short id needs the generic half a
	// concrete one leaves at zero, that half being which instantiation is meant.
	fixtureGenericClassDeclID = mainFunctionDeclID + 16

	// And the abstract last, its bucket being numbered after the generic class's.
	// It is what the child stands below and is otherwise as empty as a
	// declaration gets: nothing here is laid out or reached, only descended from.
	fixtureAbstractDeclID = mainFunctionDeclID + 17
	fixtureAbstractShort  = fixtureAbstractDeclID << 16

	// The interface after it, that bucket being numbered after the generic
	// abstract's. It is the other thing the child stands below and the only one
	// that numbers its methods without regard to what implements it.
	fixtureInterfaceDeclID = mainFunctionDeclID + 18
	fixtureInterfaceShort  = fixtureInterfaceDeclID << 16
)

// fixtureRunElements is how many elements the run class's one member holds.
// Three, so that a copy which moved only the first element and a copy which
// moved all of them differ in two places rather than one, and so that the
// element between them is there to catch a stride read off the wrong end.
const fixtureRunElements = 3

// fixtureGenericShort names one instantiation of the generic class fixture. The
// declaration is one id and the instantiations differ only in the low half,
// which is what a reader of an open type has to attend to: the enumeration it
// indexes is the instantiation's and not the declaration's.
func fixtureGenericShort(genericID uint64) uint64 {
	return fixtureGenericClassDeclID<<16 | genericID
}

// fixtureClassHeader is what the compiler reserves in front of an untailed
// class's members, asked of the compiler for the reason
// fixtureTailedStructHeader is: the fixture moves with the rule instead of
// restating it. It is also the distance a member at offset zero lies from the
// front of the instance holding it, which is what the back-distance in that
// member's own mark word has to come out as.
var fixtureClassHeader = instanceHeaderByteSizeOf(syntax.KindClass, false)

// fixtureTailedStructHeader is what the compiler reserves in front of a tailed
// struct's members, asked of the compiler rather than written down, so that the
// fixture below moves with the rule instead of restating it.
var fixtureTailedStructHeader = instanceHeaderByteSizeOf(syntax.KindStruct, true)

// fixtureTailedStruct is the one instance layout no kind describes. Every other
// header is read off the mark word an instance carries; a struct carries none,
// so the runtime works this one out from the definition — a nonzero
// instanceTailByteSize and nothing else — and adds a width it holds on its own
// side as ZAMTAILED_STRUCT_HEADER_BYTES.
//
// The first member's offset is zero and the fixed size is the header plus that
// member, so the two are consistent only if the reader adds back exactly the
// header the offset was measured net of. The tail is an int wide, which is what
// makes the definition say there is a tail at all.
//
// The second member is the tail, which is a member like any other: a record says
// where the elements begin and what one of them is. The run it is stays unstated:
// an unsized tail has no length to put in the record, its extent living in the
// instance's own tail length instead. That is what makes indexing one the
// element's own width added to the member's location rather than a load through
// it. A sized array member does state its length, which is fixtureRunClass.
func fixtureTailedStruct() *typeDefinitionStruct {
	intMember := func(offset uint64) memberDefinition {
		return memberDefinition{
			Offset: offset,
			Type: typeInstance{
				typeInstanceShort: typeInstanceShort{
					typeID: uint16(syntax.TypeKindInt),
				},
			},
		}
	}
	return &typeDefinitionStruct{
		instanceMembers: []memberDefinition{
			intMember(0),
			intMember(wordByteSize),
		},
		instanceByteSize:     fixtureTailedStructHeader + wordByteSize,
		instanceTailByteSize: wordByteSize,
	}
}

// fixtureClass is that layout written out. The member's offset is zero and its
// instance is three words wide, which is the whole of the claim OpPushMember has
// to resolve: the two are only consistent if the reader adds the header the
// offset was measured net of.
//
// The initializer is where the routine a fresh instance is filled in by lies, and
// zero for the images that have none. It is a parameter because the routine is
// laid down among the operations under test, so only the test knows the offset —
// and because an initializer that never runs and one that does not exist leave
// exactly the same instance behind, which is what makes it worth being able to
// build both.
func fixtureClass(initializer uint64) *typeDefinitionClass {
	return &typeDefinitionClass{
		typeDefinitionAbstract: typeDefinitionAbstract{
			initializerInstruction: initializer,
			instanceMembers: []memberDefinition{{
				Offset: 0,
				Type: typeInstance{
					typeInstanceShort: typeInstanceShort{
						typeID: uint16(syntax.TypeKindInt),
					},
				},
			}},
			instanceByteSize: 3 * wordByteSize,
			supertypeSet:     map[typeInstanceShort]struct{}{},
		},
		dispatchEntries: map[uint64]uint32{},
	}
}

// fixtureAbstract is the one declaration here that is only ever descended from.
// It is laid out nowhere and reached through nothing, so it carries no members
// and no width — what it is for is to be the type a subtype test names above.
//
// Its set holds itself and nothing else. Every type is its own supertype, and a
// set that held nothing at all would make an abstract stand below itself only by
// the shortID equality that runs ahead of the lookup, leaving the lookup itself
// unexercised in the one direction it is certain to be right about.
//
// The one method is where a virtual call is written and never where one lands.
// It has no body — offset zero being where the program begins and so never a
// routine — which is what an abstract method is, and it makes the record a
// dispatch must not read: zam_type_method dies rather than call a body that is
// not there, so a slot resolved against this declaration fails loudly instead
// of running something else. Its signature is the parent one, the pair with the
// child's below being what says which of the two records a closure was filled
// from.
func fixtureAbstract() *typeDefinitionAbstract {
	return &typeDefinitionAbstract{
		instanceMethods: []methodDefinition{{
			instruction: 0,
			typeListID:  fixtureParentParamDeclID,
		}},
		supertypeSet: map[typeInstanceShort]struct{}{
			{typeID: uint16(fixtureAbstractDeclID)}: {},
		},
	}
}

// fixtureInterface is the one declaration whose whole record is what stands
// above it. An interface is laid out nowhere and holds no methods of its own to
// read — the numbering its methods have belongs to it and means nothing to any
// implementor, which is why a call through one goes to the implementor's
// dispatch table instead of to anything here.
//
// Its set holds itself for the reason fixtureAbstract's does.
func fixtureInterface() *interfaceDefinition {
	return &interfaceDefinition{
		supertypeSet: map[typeInstanceShort]struct{}{
			{typeID: uint16(fixtureInterfaceDeclID)}: {},
		},
	}
}

// fixtureInterfaceMethodIndex is the slot the interface numbers its one method
// at, which the dispatch table below sends to a different slot on the class.
const fixtureInterfaceMethodIndex = 0

// fixtureChildClassInterfaceMethod is the slot that translation arrives at. It
// is deliberately not fixtureInterfaceMethodIndex: were the two the same, a
// dispatch that ignored the table entirely and used the operand as it stood
// would pass, and the table is the whole of what this fixture is here for.
const fixtureChildClassInterfaceMethod = 1

// fixtureChildClass is the one class that stands below something: it holds the
// abstract above in its set, which is the only route by which two unequal ids
// answer true.
//
// It is a fifth class rather than a set added to the first, because the first
// one's width and members are asserted all over this file and a subtype test
// wants a class whose layout nothing else depends on. Its own layout is the
// simplest there is — a header and one int — since nothing here reads a member
// out of it.
//
// Its first method is the override of the abstract's, and it sits at the same
// index for the reason flattenedInstanceFields gives: a base's methods are laid
// out ahead of the ones a declaration adds, and an override takes the slot it
// inherited rather than a new one. So the two records differ in what the slot
// holds and in nothing else, which is the whole of what a dispatch has to pick
// between. The body comes from the test because the test is what lays it down,
// and the signature is the child one against the abstract's parent one.
//
// Its second is the interface's, which is a different kind of thing: it sits at
// an index the interface never named and is reached only by the dispatch table
// translating one numbering into the other. Its signature is a third one again,
// so which of the three records a closure was filled from is readable off the
// closure alone.
func fixtureChildClass(method, interfaceMethod uint64) *typeDefinitionClass {
	return &typeDefinitionClass{
		typeDefinitionAbstract: typeDefinitionAbstract{
			instanceMethods: []methodDefinition{{
				instruction: method,
				typeListID:  fixtureChildParamDeclID,
			}, {
				instruction: interfaceMethod,
				typeListID:  fixtureChildReturnDeclID,
			}},
			instanceMembers: []memberDefinition{{
				Offset: 0,
				Type: typeInstance{
					typeInstanceShort: typeInstanceShort{
						typeID: uint16(syntax.TypeKindInt),
					},
				},
			}},
			instanceByteSize: fixtureClassHeader + wordByteSize,
			supertypeSet: map[typeInstanceShort]struct{}{
				{typeID: uint16(fixtureChildDeclID)}:     {},
				{typeID: uint16(fixtureAbstractDeclID)}:  {},
				{typeID: uint16(fixtureInterfaceDeclID)}: {},
			},
		},
		dispatchEntries: map[uint64]uint32{
			classOrAbstractInterfaceMethodKey{
				InterfaceType: typeInstanceShort{
					typeID: uint16(fixtureInterfaceDeclID)},
				methodIndex: fixtureInterfaceMethodIndex,
			}.packed(): fixtureChildClassInterfaceMethod,
		},
	}
}

// fixtureFunctionTypes are the signatures a subtype test can be asked about,
// main's own leading them.
//
// A function stands below another by its signature rather than by any
// declaration, so these are the one kind of fixture whose answer is worked out
// from the record instead of looked up in a set. They come in two pairs, each
// pair differing in exactly one position — one in the parameter and one in the
// return — and differing there between the child class and the abstract above
// it. So each pair says which direction its position is read in, and neither
// could come out right by the two ids simply matching.
//
// The return leads each list and the parameters follow, which is the order
// functionTypeList writes. A reader that took the list the other way round would
// have the two pairs swap answers rather than fail outright, which is what makes
// writing them as a pair each worth the second entry.
func fixtureFunctionTypes() []typeDefinition {
	class := func(declID uint64) typeInstance {
		return typeInstance{
			typeInstanceShort: typeInstanceShort{typeID: uint16(declID)}}
	}
	void := typeInstance{
		typeInstanceShort: typeInstanceShort{typeID: uint16(syntax.TypeKindVoid)}}
	signature := func(types ...typeInstance) typeDefinition {
		return &typeListInstance{typeIDs: types}
	}
	return []typeDefinition{
		// main's, which is a nullary void and so the one signature here with no
		// parameter at all — an arity mismatch to be tested against.
		signature(void),

		signature(void, class(fixtureAbstractDeclID)),
		signature(void, class(fixtureChildDeclID)),
		signature(class(fixtureAbstractDeclID)),
		signature(class(fixtureChildDeclID)),
	}
}

// fixturePointers are the five pointer types, in the order their ids are
// numbered: one at the abstract, one at the class below it, one at void, and a
// pair at the tailed struct differing only in whether the tail has a length.
//
// A pointer's record is the only place what it points at is written down, the id
// itself saying nothing beyond which pointer it is. So every question asked
// through one of these is a question the runtime has to take a step to answer,
// and a pointer read as the type it is rather than as its pointee comes out
// unrelated to everything.
func fixturePointers() []typeDefinition {
	at := func(declID, tailLength uint64) typeDefinition {
		return &pointerDefinition{pointee: typeInstance{
			typeInstanceShort: typeInstanceShort{typeID: uint16(declID)},
			tailedLength:      tailLength}}
	}
	return []typeDefinition{
		at(fixtureAbstractDeclID, 0), at(fixtureChildDeclID, 0),
		at(uint64(syntax.TypeKindVoid), 0),
		at(fixtureStructDeclID, fixturePointeeTailLength),
		at(fixtureStructDeclID, 0)}
}

// fixturePointeeTailLength is the length the sized tailed pointee carries. Any
// nonzero length would do: what the rule reads is that there is one, an unsized
// tail being written as no length at all.
const fixturePointeeTailLength = 2

// fixtureNestClass is a class whose one member is a class value laid out inside
// it, which is the layout every claim about an interior header is made against.
//
// The member's offset is zero, so the value it names begins exactly one header
// into the instance and its own mark word is that far back from its container's
// — the number the runtime has to work out and the number this fixture is here
// to make wrong if it works out a different one. The width is the outer header
// and the whole of the inner value, the inner value's own header included, since
// a class laid inline brings the word it is marked through with it.
//
// Nothing here says the member is markable. That is read off the member's type
// id and the bucket it falls in, which is the same route an instruction takes,
// so a fixture that named a struct here would be stamped differently by the
// runtime for the right reason rather than by a switch in the test.
func fixtureNestClass() *typeDefinitionClass {
	return &typeDefinitionClass{
		typeDefinitionAbstract: typeDefinitionAbstract{
			instanceMembers: []memberDefinition{{
				Offset: 0,
				Type: typeInstance{
					typeInstanceShort: typeInstanceShort{
						typeID: uint16(fixtureClassDeclID),
					},
				},
			}},
			instanceByteSize: fixtureClassHeader + 3*wordByteSize,
			supertypeSet:     map[typeInstanceShort]struct{}{},
		},
		dispatchEntries: map[uint64]uint32{},
	}
}

// fixtureNestRunClass is fixtureNestClass with a run where it has one value:
// a member that is three markable classes laid inline rather than one.
//
// It is fixtureRunClass's other half. That one asks what a member's length means
// for how wide a location is, which is a question about ints and would be the
// same for any element. This one asks what it means for the headers inside the
// member, which only a markable element has — and those are what a walk of the
// run reads, so an element left as the loader's zeroes is an element whose kind
// says string and whose container was never written down.
func fixtureNestRunClass() *typeDefinitionClass {
	return &typeDefinitionClass{
		typeDefinitionAbstract: typeDefinitionAbstract{
			instanceMembers: []memberDefinition{{
				Offset: 0,
				Type: typeInstance{
					typeInstanceShort: typeInstanceShort{
						typeID: uint16(fixtureClassDeclID),
					},
					arrayLength: fixtureRunElements,
				},
			}},
			instanceByteSize: fixtureClassHeader +
				fixtureRunElements*(fixtureClassHeader+wordByteSize),
			supertypeSet: map[typeInstanceShort]struct{}{},
		},
		dispatchEntries: map[uint64]uint32{},
	}
}

// fixtureRunClass is a class whose one member is a run of ints rather than one
// int, which is the only fixture whose member record carries an array length.
//
// It is here because that length is the whole of what a member says about how
// wide it is. Nothing else in the record distinguishes a run of three from the
// first of three, so a reader that dropped the length would resolve this member
// to a location one word wide and a load or a store through it would move one
// element and leave the other two — which compiles, runs, and is wrong.
func fixtureRunClass() *typeDefinitionClass {
	return &typeDefinitionClass{
		typeDefinitionAbstract: typeDefinitionAbstract{
			instanceMembers: []memberDefinition{{
				Offset: 0,
				Type: typeInstance{
					typeInstanceShort: typeInstanceShort{
						typeID: uint16(syntax.TypeKindInt),
					},
					arrayLength: fixtureRunElements,
				},
			}},
			instanceByteSize: fixtureClassHeader +
				fixtureRunElements*wordByteSize,
			supertypeSet: map[typeInstanceShort]struct{}{},
		},
		dispatchEntries: map[uint64]uint32{},
	}
}

// fixtureGenericClass is a template with two instantiations, which is the only
// shape that can be asked what an open type resolved to: the enumeration of open
// types is written once per instantiation, resolved through that
// instantiation's own bindings, and the position in it is all a body compiled
// for the whole template has to go on.
//
// Two instantiations rather than one, because what the generic half of a short
// id picks out is which entry to read — and one entry would be reached just as
// well by a reader that ignored the id and took the first. Two open types each,
// so the operand naming the position is discriminating too.
//
// The types the two were instantiated over are deliberately unalike in every
// word of an id: a bare primitive, a run of them, a class value and a tailed
// struct. An id whose words came back in the wrong order, or out of the wrong
// entry, is a different answer in each of the four.
// The one method is where the two halves of a generic declaration are told
// apart. Its body is compiled once and is held above the entries, so both
// instantiations run the same instructions; its signature is the instantiation's
// and is named by each entry separately, because C[int].f and C[string].f are
// different function types. The two ids below are different for exactly that
// reason — a signature read off the declaration could only be one of them.
func fixtureGenericClass(method uint64) *typeDefinitionGenericClass {
	entry := func(methodType uint64, generics ...typeInstance) genericClassMapEntry {
		return genericClassMapEntry{
			genericMapEntry: genericMapEntry{
				instanceByteSize:      fixtureClassHeader + wordByteSize,
				genericTypes:          typeListInstance{typeIDs: generics},
				instanceMethodTypeIDs: []uint64{methodType},
				supertypeSet:          map[typeInstanceShort]struct{}{},
			},
			dispatchEntries: map[uint64]uint32{},
		}
	}
	primitive := func(kind syntax.TypeKind) typeInstanceShort {
		return typeInstanceShort{typeID: uint16(kind)}
	}
	return &typeDefinitionGenericClass{
		instanceMethodInstructions: []uint64{method},
		concreteTypesSizeMap: []genericClassMapEntry{
			entry(
				fixtureParentParamDeclID,
				typeInstance{typeInstanceShort: primitive(syntax.TypeKindInt)},
				typeInstance{
					typeInstanceShort: typeInstanceShort{
						typeID: uint16(fixtureClassDeclID)},
				},
			),
			entry(
				fixtureChildReturnDeclID,
				typeInstance{
					typeInstanceShort: primitive(syntax.TypeKindChar),
					arrayLength:       4,
				},
				typeInstance{
					typeInstanceShort: typeInstanceShort{
						typeID: uint16(fixtureStructDeclID)},
					tailedLength: 2,
				},
			),
		},
	}
}

// The literals every image here carries.
//
// fixtureStringLiteral is two bytes, so the length word and the bytes after it
// hold different numbers and a read of the wrong one is visible. The other two
// are what an order and an inequality are read off: one the same width and
// differing in a byte, and one that begins with it and runs past — which is the
// pair the bytes cannot settle and the lengths can.
const (
	fixtureStringLiteral    = stringLiteral("hi")
	fixtureSameWidthLiteral = stringLiteral("ho")
	fixturePrefixedLiteral  = stringLiteral("hit")
)

// fixtureLiterals is those three in the order their slots are numbered, which is
// the order intern assigns them in and so the order they lie in the region.
func fixtureLiterals() []stringLiteral {
	return []stringLiteral{
		fixtureStringLiteral, fixtureSameWidthLiteral, fixturePrefixedLiteral}
}

// The slots of the static region, in the order the compiler lays them.
//
// A literal takes the lower slot because populateStringLiterals runs before the
// static fields are laid out and returns the offset they start at, so literals
// hold the slots that begin the region. That ordering is load-bearing rather than
// incidental: zam_vm_init copies the literal section over the head of the static
// region, so a literal's slot has to be one that begins there.
// The third slot holds a fixture class value, which is the one static whose
// storage the compiler has anything to say about: a markable value is read
// through a header, and a static gets no initializer to write one.
const (
	fixtureLiteralSlot = iota
	fixtureSameWidthLiteralSlot
	fixturePrefixedLiteralSlot
	mainStaticSlot
	fixtureInstanceSlot
)

// writeImage lays the operations into an image and writes it out.
//
// The function type fixtures, the class fixtures, the abstract, the generic
// class's two instantiations, one interned literal and one static slot are always
// there, whether the program under test reaches them or not. What varies between these
// tests is the operations and nothing else, which is what keeps a difference in
// behaviour attributable to the instructions rather than to the image around
// them.
//
// The exceptions are the two routines that lie among the operations rather than
// in the image around them: the fixture class's initializer, which fills a fresh
// instance, and the child class's one method. Both have to come from the test
// because the test is what lays the routine down, and both are variadic so that
// the images with neither — which is most of them — say nothing about either.
// Zero is what a routine that does not exist is recorded as, offset zero being
// where the program begins and so never a body.
func writeImage(t *testing.T, operations []operation, routines ...uint64) string {
	t.Helper()

	routineAt := func(index int) uint64 {
		if index >= len(routines) {
			return 0
		}
		return routines[index]
	}
	classInitializer, childMethod := routineAt(0), routineAt(1)
	childInterfaceMethod, genericMethod := routineAt(2), routineAt(3)

	// Each literal lies where the one before it ended, which is the arithmetic
	// intern does as it hands out the slots.
	literals := base.NewStaticOrderedMap[stringLiteral, uint64]()
	literalOffsets := make([]int, len(fixtureLiterals()))
	literalBytes := 0
	for slot, literal := range fixtureLiterals() {
		literals.Set(literal, uint64(slot))
		literalOffsets[slot] = literalBytes
		literalBytes += internedString(literal).ByteCount()
	}

	first := int(reservedTypeIDCount)
	cr := &codegenReader{
		gcTriggerRatio:   0.7,
		initialStackSize: 16,
		initialHeapSize:  1 << 20,

		functionTypeDefinitions: fixtureFunctionTypes(),
		pointerDefinitions:      fixturePointers(),
		structDefinitions:       []typeDefinition{fixtureTailedStruct()},
		classDefinitions: []typeDefinition{
			fixtureClass(classInitializer), fixtureNestClass(),
			fixtureRunClass(), fixtureNestRunClass(),
			fixtureChildClass(childMethod, childInterfaceMethod)},
		genericClassDefinitions: []typeDefinition{fixtureGenericClass(genericMethod)},
		abstractDefinitions:     []typeDefinition{fixtureAbstract()},
		interfaceDefinitions:    []typeDefinition{fixtureInterface()},
		counts: &counts{
			threadTypeDefinitionsStartIndex:       first,
			functionTypeDefinitionsStartIndex:     first,
			pointerDefinitionsStartIndex:          first + 5,
			structDefinitionsStartIndex:           first + 10,
			genericStructDefinitionsStartIndex:    first + 11,
			enumDefinitionsStartIndex:             first + 11,
			classDefinitionsStartIndex:            first + 11,
			genericClassDefinitionsStartIndex:     first + 16,
			abstractDefinitionsStartIndex:         first + 17,
			genericAbstractDefinitionsStartIndex:  first + 18,
			interfaceDefinitionsStartIndex:        first + 18,
			genericInterfaceDefinitionsStartIndex: first + 19,

			staticRegionByteSize: literalBytes + wordByteSize + 3*wordByteSize,
		},
		// The literals begin the region, main's slot follows them, and the
		// instance follows that, which is the arithmetic zam_vm_init's copy
		// depends on.
		staticInstanceMaps: append(
			append([]int{}, literalOffsets...),
			literalBytes, literalBytes+wordByteSize),
		stringLiterals: literals,
		natives:        base.NewStaticOrderedMap[string, uint64](),
		operations:     operations,
	}

	// The literals' bytes are the head of the static region, which is what the
	// interning pass lays down as it assigns the slots above.
	for slot, literal := range fixtureLiterals() {
		interned := internedString(literal)
		_, err := interned.Read(cr.staticImageAt(
			uint64(literalOffsets[slot]), uint64(interned.ByteCount())))
		require.NoError(t, err)
	}

	// The instance slot's header, written through the same two functions the
	// static field pass writes one through. What the pass does with them is held
	// to the front end in 2_staticfields_test.go; what this holds is the other
	// half — that the bytes those functions produce are the bytes the runtime
	// reads a header out of.
	instanceHeader := cr.staticImageAt(
		uint64(literalBytes+wordByteSize), 2*wordByteSize)
	binary.LittleEndian.PutUint64(instanceHeader,
		staticMarkWord(objectKindInstance, 0))
	binary.LittleEndian.PutUint64(instanceHeader[wordByteSize:],
		fixtureClassShort|1<<32)

	image := make([]byte, cr.ByteCount())
	n, err := cr.Read(image)
	require.NoError(t, err)
	require.Equal(t, len(image), n, "the image is not the size it said it was")

	path := filepath.Join(t.TempDir(), "run.zam")
	require.NoError(t, os.WriteFile(path, image, 0o600))
	return path
}

// runImage runs a program and splits what the harness says about it into lines.
func runImage(t *testing.T, harness, subject string, operations []operation,
	routines ...uint64) []string {
	t.Helper()

	run := exec.Command(harness, subject,
		writeImage(t, operations, routines...))
	out, err := run.Output()
	// What the interpreter died of goes to stderr, which Output keeps out of the
	// error: without this a panic and a bad image both read as an exit status.
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		err = fmt.Errorf("%w: %s", err, exit.Stderr)
	}
	require.NoError(t, err, "the program did not run to a stop")

	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// minimalMainOperations is the smallest program there is: the static
// initializer that builds main's closure into its static slot, and the body
// control falls into from it.
//
// It is written out here rather than compiled from source because what is under
// test is the runtime's reading of these operations. A fixture keeps the two
// apart — a change in how the front end emits a static method moves this and
// leaves the expectation alone, and a change in what the runtime does to these
// operations moves the expectation and leaves this alone.
func minimalMainOperations() (operations []operation, bodyOffset int) {
	// The body offset is not known until everything before the body has been
	// laid down, which is why the lambda is held onto and filled in after.
	lambda := newBinaryOp(OpNewLambda, 0, 0)

	operations = []operation{
		newBinaryOp(OpPushStatic, mainStaticSlot, mainFunctionDeclID)}
	// The location's type, then the closure's own, then the lambda; then the
	// value's type, and the store. The location is left standing by the store
	// and discarded a word at a time after it.
	operations = append(operations, pushTypeID(mainFunctionShort)...)
	operations = append(operations, pushTypeID(mainFunctionShort)...)
	operations = append(operations, lambda)
	operations = append(operations, pushTypeID(mainFunctionShort)...)
	operations = append(operations,
		OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord)

	bodyOffset = byteCount(operations)
	lambda.first = uint64(bodyOffset)

	// main's body. It is fallen into rather than called, which is why it ends
	// with OpExit and a status rather than with a return.
	return append(operations,
		OpPushScope, pushI64(0), OpExit), bodyOffset
}

// TestMinimalMainRunsCConformance holds the interpreter to what the smallest
// program means.
//
// Nothing about a run is written down to be compared against the way a
// definition's bytes are, so what is asserted is the state the run left: the
// program stopped the way it said to, the stacks hold what the instructions
// should have left them holding, and the static slot holds a closure whose
// header and body say what the operations asked for.
//
// The closure is the part worth reading back in full. It is the first thing the
// runtime ever allocates, and its header is the decision that a closure carries
// a type id at all — a shape nothing on the wire restates, so nothing but this
// would notice it changing.
func TestMinimalMainRunsCConformance(t *testing.T) {
	t.Parallel()
	operations, bodyOffset := minimalMainOperations()

	assert.Equal(t, []string{
		"result=EXIT",
		"exitStatus=0",
		// Nothing is left on either stack but the scope main's body opened,
		// which is never closed because OpExit is reached inside it. In
		// particular neither the closure's mark entry nor the static pointer's
		// is left: both words were spent, and an entry outliving the word it
		// names is a root the collector would start from after the program gave
		// it up.
		"operandCount=0",
		"markByteCount=16",
		// White, and not any other state, because nothing has collected yet.
		"closure.state=0",
		// ZAMOBJECT_FUNCTION_CLOSURE, which is the kind that says the first
		// entry is no receiver before anything reads the entry.
		"closure.kind=3",
		fmt.Sprintf("closure.shortID=%d", mainFunctionShort),
		"closure.elementCount=1",
		// The body is where the prologue stopped, which is the offset the
		// lambda was built with and the offset control fell into.
		fmt.Sprintf("closure.instruction=%d", bodyOffset),
		"closure.captureCount=0",
		// A void receiver, which is how a closure with none says so.
		fmt.Sprintf("closure.receiver.shortID=%d", uint64(syntax.TypeKindVoid)),
		"closure.receiver.arrayLength=0",
		"closure.receiver.tailLength=0",
	}, runImage(t, buildRunHarness(t), "closure", operations))
}

// branchOperations is a program that computes one of two values and exits with
// it, which is the only shape a branch can be checked in: a jump's whole effect
// is which instructions ran, and the value left behind is what says which did.
//
// The targets are measured rather than written down, so an instruction added to
// a limb moves them.
func branchOperations(condition uint64) []operation {
	taken := []operation{pushI64(42)}
	otherwise := []operation{pushI64(7)}

	// The false limb begins past the true one and the jump over it; the exit
	// begins past the false limb.
	test := []operation{pushI64(condition), newUnaryOp(OpJumpIfFalse, 0)}
	over := newUnaryOp(OpJump, 0)

	falseTarget := byteCount(test) + byteCount(taken) + over.ByteCount()
	test[1].(*unaryOperation).operand = uint64(falseTarget)
	over.operand = uint64(falseTarget + byteCount(otherwise))

	operations := append(test, taken...)
	operations = append(operations, over)
	operations = append(operations, otherwise...)
	return append(operations, OpExit)
}

// TestArithmeticCConformance runs one small program per instruction and reads
// the answer off the exit status.
//
// The status is a byte, which is the whole of the interface a program has to
// whoever started it, so every expectation here is a value that fits in one.
// That is a limit worth working within rather than around: what is being
// checked is that an instruction did the arithmetic it names, and a small
// number shows that as well as a large one.
//
// These instructions are told no type ids, which is not an omission. The width
// and the signedness are in the opcode — OpAddI64 is only ever emitted where
// both operands are already known to be integers — so there is nothing left for
// an id to say. The instructions told one are the ones that dispatch on it,
// like OpEq and OpStore.
func TestArithmeticCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// A word with only its top bit set, which is the value that tells the two
	// right shifts apart: one carries the sign bit down and the other does not.
	const signBit = uint64(1) << 63

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
	}{
		{"add", []operation{pushI64(40), pushI64(2), OpAddI64, OpExit}, 42},
		{"subtract", []operation{pushI64(50), pushI64(8), OpSubI64, OpExit}, 42},
		{"multiply", []operation{pushI64(6), pushI64(7), OpMulI64, OpExit}, 42},
		{"divide", []operation{pushI64(85), pushI64(2), OpDivI64, OpExit}, 42},
		{"modulus", []operation{pushI64(142), pushI64(100), OpModI64, OpExit}, 42},

		// A negative operand is the case that says the division is signed
		// rather than a division of the words: -85/2 is -42 unsigned nowhere.
		{"divide is signed", []operation{
			pushI64(85), OpArithmeticNegationI64, pushI64(2), OpDivI64,
			OpArithmeticNegationI64, OpExit}, 42},
		// C rounds a remainder toward zero, so a negative left operand gives a
		// negative remainder. -142 % 100 is -42 and not 58.
		{"modulus is signed", []operation{
			pushI64(142), OpArithmeticNegationI64, pushI64(100), OpModI64,
			OpArithmeticNegationI64, OpExit}, 42},
		// The one division C leaves undefined that is not by zero, and the one
		// a processor traps on. Reaching the exit at all is the assertion.
		{"the most negative over minus one", []operation{
			pushI64(signBit), pushI64(1), OpArithmeticNegationI64, OpDivI64,
			OpExit}, 0},

		{"negate", []operation{
			pushI64(42), OpArithmeticNegationI64, OpArithmeticNegationI64,
			OpExit}, 42},
		// -42 is 0xff...d6, so the byte the status is comes out 214.
		{"negate wraps", []operation{
			pushI64(42), OpArithmeticNegationI64, OpExit}, 214},
		{"bitwise not", []operation{
			pushI64(42), OpBitwiseNegationI64, OpExit}, 213},

		{"and", []operation{pushI64(0xff), pushI64(42), OpAndI64, OpExit}, 42},
		{"or", []operation{pushI64(40), pushI64(2), OpOrI64, OpExit}, 42},
		{"xor", []operation{pushI64(47), pushI64(5), OpXorI64, OpExit}, 42},

		{"shift left", []operation{
			pushI64(21), pushI64(1), OpShiftLeftI64, OpExit}, 42},
		// The two right shifts differ only here: the unarithmetic one brings
		// zeroes down from the top and the arithmetic one brings the sign.
		{"shift right does not carry the sign", []operation{
			pushI64(signBit), pushI64(57), OpShiftRightI64, OpExit}, 64},
		{"arithmetic shift right carries the sign", []operation{
			pushI64(signBit), pushI64(57), OpShiftArithmeticRightI64,
			OpExit}, 192},

		{"equal", []operation{
			pushI64(42), pushI64(42), OpEqualI64, OpExit}, 1},
		{"unequal", []operation{
			pushI64(42), pushI64(43), OpEqualI64, OpExit}, 0},
		{"is negative", []operation{
			pushI64(1), OpArithmeticNegationI64, OpIsNegativeI64, OpExit}, 1},
		{"zero is not negative", []operation{
			pushI64(0), OpIsNegativeI64, OpExit}, 0},
		{"is positive", []operation{pushI64(1), OpIsPositiveI64, OpExit}, 1},
		{"zero is not positive", []operation{
			pushI64(0), OpIsPositiveI64, OpExit}, 0},

		// The word is duplicated so the copy can be spent on the test and the
		// original kept, which is what a short-circuiting operator does with it.
		{"duplicate", []operation{
			pushI64(42), newUnaryOp(OpDupWord, 0), OpEqualI64, OpExit}, 1},
		// Two subtractions rather than one, so that both originals are spent
		// and the answer distinguishes the depths: reaching one word down gives
		// 5 - (3 - 5), and reaching none would have given 5 - (3 - 3).
		{"duplicate reaches past the top", []operation{
			pushI64(5), pushI64(3), newUnaryOp(OpDupWord, 1),
			OpSubI64, OpAddI64, OpExit}, 3},

		{"a taken branch", branchOperations(1), 42},
		{"an untaken branch", branchOperations(0), 7},

		{"float add", []operation{
			pushFloat(1.5), pushFloat(2.5), OpAddF64, OpCastF64ToI64,
			OpExit}, 4},
		{"float subtract", []operation{
			pushFloat(5.5), pushFloat(1.5), OpSubF64, OpCastF64ToI64,
			OpExit}, 4},
		{"float multiply", []operation{
			pushFloat(1.5), pushFloat(2.0), OpMulF64, OpCastF64ToI64,
			OpExit}, 3},
		{"float negate", []operation{
			pushFloat(-2.5), OpNegF64, OpCastF64ToI64, OpExit}, 2},
		{"float equal", []operation{
			pushFloat(2.5), pushFloat(2.5), OpEqualF64, OpExit}, 1},
		// A NaN is equal to nothing, itself included, which is the whole reason
		// the opcode is written as three conditions rather than one.
		{"a NaN equals itself no more than anything else", []operation{
			pushFloat(math.NaN()), pushFloat(math.NaN()), OpEqualF64,
			OpExit}, 0},
		// A float division by zero is an infinity rather than a fault, which is
		// what sets it apart from the integer one below.
		{"float divide by zero", []operation{
			pushFloat(1.0), pushFloat(0.0), OpDivF64, pushFloat(math.Inf(1)),
			OpEqualF64, OpExit}, 1},

		{"an integer becomes a float and comes back", []operation{
			pushI64(21), OpCastI64ToF64, pushFloat(2.0), OpMulF64,
			OpCastF64ToI64, OpExit}, 42},
		{"an unsigned integer past the signed range becomes a float", []operation{
			pushI64(1 << 63), OpCastU64ToF64, pushFloat(1 << 63),
			OpEqualF64, OpExit}, 1},
		{"an unsigned quotient reads the sign bit as magnitude", []operation{
			pushI64(1 << 63), pushI64(1 << 62), OpDivU64, OpExit}, 2},
		{"an unsigned remainder reads the sign bit as magnitude", []operation{
			pushI64(1<<63 + 5), pushI64(1 << 62), OpModU64, OpExit}, 5},
		{"a float past the signed range becomes an unsigned integer", []operation{
			pushFloat(1 << 63), OpCastF64ToU64, pushI64(1 << 63),
			OpEqualI64, OpExit}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// Every one of these takes exactly its operands and leaves
				// exactly its result, and none of them touches the mark stack:
				// a number owns nothing the collector could start from.
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// intShort names the int primitive. A primitive's declaration id is its kind
// directly, which is the whole reason the ids below reservedTypeIDCount point at
// no definition.
var intShort = uint64(syntax.TypeKindInt) << 16

// charShort names another one, which is only wanted where two primitives have to
// be told apart and neither is the one every other test reaches for.
var charShort = uint64(syntax.TypeKindChar) << 16

// voidShort names the primitive whose short id is zero, which is the value an
// unwritten word holds and so the one id that has to be told apart from nothing
// having been written at all.
var voidShort = uint64(syntax.TypeKindVoid) << 16

// stringShort names the one primitive that is not its own bits. It is wanted
// wherever what is under test is a value being held as an address, since it is
// the shortest such id there is: every other one names a declaration the image
// would have to carry.
var stringShort = uint64(syntax.TypeKindString) << 16

// staticIntLocation is the pair that a location is: the address of the slot, and
// the type id that says what lies at it. Neither instruction pushes both — the
// address is OpPushStatic's and the type is the caller's — which is why a
// location has to be built rather than fetched.
func staticIntLocation() []operation {
	return append(
		[]operation{newBinaryOp(OpPushStatic, mainStaticSlot, uint64(syntax.TypeKindInt))},
		pushTypeID(intShort)...)
}

// TestLocationCConformance covers the instructions that move a value between the
// stack and the one place these images can put one.
//
// A store's whole effect is on memory, which the stack does not show, so every
// one of these reads the slot back rather than trusting the write: the load is
// what makes the store observable, and a fresh location built from scratch is
// what makes the load a read of memory rather than of something left standing.
func TestLocationCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Spending a location, which is four words: the three the type id stands on
	// and the address beneath them.
	dropLocation := []operation{OpPopWord, OpPopWord, OpPopWord, OpPopWord}

	// Putting 42 in the slot and leaving nothing behind, which is the prologue
	// every case below that reads the slot needs.
	store := func() []operation {
		operations := append(staticIntLocation(), pushI64(42))
		operations = append(operations, pushTypeID(intShort)...)
		operations = append(operations, OpStore)
		return append(operations, dropLocation...)
	}

	// Reading the slot through a location built for the purpose.
	load := func() []operation {
		return append(staticIntLocation(), OpLoad)
	}

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
	}{
		// A default value is one word of zero for an int. Adding to it is what
		// says it was one word and not none: a load of nothing would leave
		// OpAddI64 an operand short, and two words would leave one standing.
		{"the default int is a single zero word", append(
			pushTypeID(intShort), OpDefaultValue, pushI64(42), OpAddI64,
			OpExit), 42},

		// The store leaves the location standing, so the load that follows it
		// reads the slot through the very pointer that was written through.
		{"a store leaves its location to be read back", append(
			func() []operation {
				operations := append(staticIntLocation(), pushI64(42))
				operations = append(operations, pushTypeID(intShort)...)
				return append(operations, OpStore)
			}(), OpLoad, OpExit), 42},

		// The location the store used is thrown away first, so what this reads
		// is the memory and not a word the store happened to leave behind.
		{"a stored value is in the slot afterwards", append(
			append(store(), load()...), OpExit), 42},

		// OpPop is told the type so that it knows how much to let go of. If it
		// let go of the wrong amount the count at the end would say so.
		{"a discard spends the whole value", append(
			append(append(append(store(), load()...),
				pushTypeID(intShort)...), OpPop),
			pushI64(7), OpExit), 7},

		{"equal ints are equal", append(
			append(append(append(store(), load()...),
				pushTypeID(intShort)...), pushI64(42)),
			append(pushTypeID(intShort), OpEq, OpExit)...), 1},
		{"unequal ints are not", append(
			append(append(append(store(), load()...),
				pushTypeID(intShort)...), pushI64(7)),
			append(pushTypeID(intShort), OpEq, OpExit)...), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				"operandCount=0",
				// The static pointer owns a mark entry while it stands, and
				// every one of these spends its locations, so nothing is left
				// for the collector to start from. An int never owned one.
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestPushStringLiteralCConformance holds a literal to being the address of its
// slot and nothing else.
//
// The instruction copies nothing and builds nothing, so the whole of what it means
// is a claim about where the word points: at the mark word a string begins with,
// with the length a word past that and the bytes a word past the length. intern in
// 1_stringliterals.go records a slot's offset as its mark word for exactly that
// reason, and reading all three back through the pointer is what says the two
// sides agree about the shape rather than only about the number.
func TestPushStringLiteralCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	literal := func() []operation {
		return []operation{newUnaryOp(OpPushStringLiteral, fixtureLiteralSlot)}
	}

	// The literal with a word offset added, read as an int. A literal is a value
	// and not a location, so the type words that make one are pushed here rather
	// than by the instruction — which is the difference from OpPushStatic, and is
	// why the two are separate opcodes over the same slot numbering.
	word := func(offset uint64) []operation {
		return append(
			append(literal(), pushI64(offset*wordByteSize), OpAddI64),
			append(pushTypeID(intShort), OpLoad)...)
	}

	// Whether the word at that offset is the one expected. The comparison is made
	// inside the program rather than read off the exit status because a status is
	// kept to its low eight bits, and the word the bytes lie in does not fit in
	// one.
	is := func(offset, want uint64) []operation {
		return append(
			append(append(word(offset), pushTypeID(intShort)...), pushI64(want)),
			append(pushTypeID(intShort), OpEq, OpExit)...)
	}

	// "hi" as the word the bytes lie in: the two characters at the head and the
	// padding that rounds the slot up to a word behind them. Reading it whole is
	// what says the bytes are there and in that order.
	const packedBytes = 'h' | 'i'<<8

	for _, test := range []struct {
		name       string
		operations []operation
	}{
		// The mark word, which is what makes the pointer a pointer to a string
		// rather than to its length. What is in it is the contained bit, which is
		// the literal saying it is not an allocation — so this reads back as the
		// number the compiler wrote and not as a zero.
		{"the pointer names the mark word", is(0, staticStringMarkWord)},

		// The length, one word in. A pointer aimed at the length instead would
		// have answered this one with the bytes.
		{"the length lies a word past it", is(1, uint64(len(fixtureStringLiteral)))},

		// And the bytes, one word past the length.
		{"the bytes lie a word past the length", is(2, packedBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				// True, the word being the one the layout says lies there.
				"exitStatus=1",
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}

	// The one claim that cannot be read off an exit status: a literal owns a
	// mark entry like every other string, which is what roots whatever a slot it
	// initialized later holds. This stops deliberately without spending the
	// pointer, so the entry is standing here to be counted.
	t.Run("a literal owns a mark entry", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=5",
			"operandCount=1",
			// The eight bytes an object entry occupies.
			"markByteCount=8",
		}, runImage(t, harness, "stop", append(literal(), pushI64(5), OpExit)))
	})
}

// TestCastPrimitiveToStringCConformance pins OpCastToString's primitive branch
// to the one shape every reader downstream expects: a readable string of what
// the word means. Nothing in the image carries a Str method for an int or a
// bool, so without a primitive case the fallback would format a 5 as the
// generic @{id}{address}{hash} form — which was the shape a Sprintf of
// "%d", 5 came back in before this branch was written.
//
// The exit status reads back the first byte of the result, which is enough to
// tell the primitive branch ran: a '5' is 0x35 and a '@' from the fallback is
// 0x40, so the two are told apart in the one byte an exit status fits.
func TestCastPrimitiveToStringCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// The result of OpCastToString is a str allocation whose bytes begin two
	// words past the pointer: a mark word and a length. Reading one byte at
	// offset 16 is what the harness compares against the expected first byte.
	// A full round-trip through the stack would be nicer, but this is enough
	// to catch the regression that motivated the branch.
	byteShort := uint64(syntax.TypeKindByte) << 16
	boolShort := uint64(syntax.TypeKindBool) << 16

	cast := func(value operation, shortID uint64) []operation {
		ops := append([]operation{value}, pushTypeID(shortID)...)
		return append(ops, OpCastToString)
	}
	firstByte := func(ops []operation) []operation {
		ops = append(ops, pushI64(16), OpAddI64)
		return append(ops, append(pushTypeID(byteShort), OpLoad, OpExit)...)
	}

	for _, test := range []struct {
		name string
		ops  []operation
		want uint64
	}{
		// A positive int reads as its leading digit — a 5 begins with '5'.
		// The alternative is '@' from the generic fallback.
		{"an int reads as its number",
			firstByte(cast(pushI64(5), intShort)), '5'},

		// A true bool reads as "true", so the first byte is 't'. 't' and
		// '@' are far apart, so the primitive branch and the fallback are
		// told apart for a type that is not a number.
		{"a bool reads as its word",
			firstByte(cast(pushI64(1), boolShort)), 't'},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := runImage(t, harness, "stop", test.ops)
			assert.Equal(t, "result=EXIT", out[0])
			assert.Equal(t, fmt.Sprintf("exitStatus=%d", test.want), out[1])
		})
	}
}

// TestBoxAnyCConformance covers the three any opcodes end-to-end: a value is
// boxed, the stored id is read back through OpAnyPayloadType, and the value is
// read back out through OpAnyUnbox. The runtime round-trip is what settles
// that the two allocations the compiler described really do go down together
// and that the id and the pointer end up where the readers look for them.
//
// A primitive is used rather than a class so that nothing but the ops is in
// play. A class would also exercise the inline header skip, which is written
// into OpBoxAny and OpAnyUnbox but has no runtime site to pin here yet — no
// class is on the stack of these fixtures.
func TestBoxAnyCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	for _, test := range []struct {
		name       string
		operations []operation
		want       uint64
	}{
		// The round-trip: 42 goes into an any as an int and comes out as the
		// same int. The exit status is what says the unboxed value is the
		// value that was boxed and not some zero word a stub would push.
		{"a boxed int unboxes to itself", append(
			append(append([]operation{pushI64(42)}, pushTypeID(intShort)...),
				OpBoxAny),
			append(pushTypeID(intShort), OpAnyUnbox, OpExit)...), 42},

		// OpAnyPayloadType reads the id the box carries, not the box's own
		// id (which is always any). The id below is the three plain words of
		// an int type id, so comparing it against a freshly pushed int id
		// and reading the one bool back is what says the stored id survived
		// the box.
		{"the stored id is the payload's id", append(
			append(append([]operation{pushI64(7)}, pushTypeID(intShort)...),
				OpBoxAny, OpAnyPayloadType),
			append(pushTypeID(intShort), OpIsSubtypeOf, OpExit)...), 1},

		// A char has a 4-byte width but stands on a full word on the stack,
		// so the box reads and writes 8 bytes through the whole trip. The
		// char comes out as the same word it went in, which is what says the
		// width was not rounded or truncated inside the box.
		{"a boxed char unboxes to itself", append(
			append(append([]operation{pushI64('x')}, pushTypeID(charShort)...),
				OpBoxAny),
			append(pushTypeID(charShort), OpAnyUnbox, OpExit)...), 'x'},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// The unboxed value is spent by OpExit's read of the exit
				// status, so nothing is left on either stack. The any's and
				// the payload's mark entries are gone with the pointer OpAnyUnbox
				// consumed.
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestOrderCConformance covers OpCmp, which is what every comparison but
// equality is made of.
//
// Each case reads the order the way emitComparison does — by which side of zero
// it fell on — rather than by the number itself, since the number is only ever
// asked that one question and a status is a byte a negative one would not
// survive. The spaceship cases are the exception, being the one place the
// program is handed the order as it stands.
func TestOrderCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	charShort := uint64(syntax.TypeKindChar) << 16
	floatShort := uint64(syntax.TypeKindFloat) << 16
	const signBit = uint64(1) << 63

	// A literal push, which is how a string gets onto the stack at all until
	// there is an allocator for one. What it leaves is the address of an interned
	// slot, so two pushes of the same slot are the same pointer and two pushes of
	// different slots never are — which is what lets the pointer shortcut and the
	// byte comparison be told apart.
	literal := func(slot uint64) operation {
		return newUnaryOp(OpPushStringLiteral, slot)
	}

	// A comparison as the compiler lays one: each operand down under its own
	// type id, since what an order comes to is the type's to say and the
	// instruction is told which type by nothing else.
	cmp := func(shortID uint64, left, right operation) []operation {
		operations := append([]operation{left}, pushTypeID(shortID)...)
		operations = append(operations, right)
		operations = append(operations, pushTypeID(shortID)...)
		return append(operations, OpCmp)
	}

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
	}{
		{"a smaller int is the lesser", append(
			cmp(intShort, pushI64(1), pushI64(2)),
			OpIsNegativeI64, OpExit), 1},
		{"a larger int is the greater", append(
			cmp(intShort, pushI64(2), pushI64(1)),
			OpIsPositiveI64, OpExit), 1},
		// Equal values are the case both readings have to agree on: neither the
		// lesser nor the greater, and an order of exactly zero.
		{"equal ints are neither", append(
			cmp(intShort, pushI64(2), pushI64(2)),
			OpIsNegativeI64, OpExit), 0},
		{"the order of equal ints is zero", append(
			cmp(intShort, pushI64(2), pushI64(2)), OpExit), 0},

		// An int is ordered as a signed number and not as the word it stands
		// on, which these two are what say. The negative is the greater of the
		// pair read as words.
		{"a negative int is below a positive one", append(
			cmp(intShort, pushI64(signBit), pushI64(1)),
			OpIsNegativeI64, OpExit), 1},
		// The pair furthest apart there is, which is where an order computed as
		// a subtraction would wrap into the wrong answer: the difference is not
		// a number an int can hold, but the comparison is still an easy one.
		{"the most negative is below the most positive", append(
			cmp(intShort, pushI64(signBit), pushI64(signBit-1)),
			OpIsNegativeI64, OpExit), 1},

		// A char has no sign to read, so the whole word is the value.
		{"a smaller char is the lesser", append(
			cmp(charShort, pushI64('a'), pushI64('b')),
			OpIsNegativeI64, OpExit), 1},

		{"a smaller float is the lesser", append(
			cmp(floatShort, pushFloat(1.5), pushFloat(2.5)),
			OpIsNegativeI64, OpExit), 1},
		{"a larger float is the greater", append(
			cmp(floatShort, pushFloat(2.5), pushFloat(1.5)),
			OpIsPositiveI64, OpExit), 1},
		// A NaN stands on neither side of anything, itself included, and the
		// order that says so is the same zero equal values give. Nothing is
		// checked for to make that so — it falls out of asking both comparisons
		// and having neither hold.
		{"a NaN is ordered against nothing", append(
			cmp(floatShort, pushFloat(math.NaN()), pushFloat(1.5)), OpExit), 0},

		// A string against itself, which is the one case settled without a read:
		// the same literal is interned once, so both sides are one address and a
		// string is neither above nor below itself.
		{"a string is not ordered against itself", append(
			cmp(stringShort, literal(fixtureLiteralSlot),
				literal(fixtureLiteralSlot)), OpExit), 0},

		// Two of the same width, differing in the second byte. These are two
		// addresses, so the answer comes from the bytes and from nowhere else.
		{"a string with a smaller byte is the lesser", append(
			cmp(stringShort, literal(fixtureLiteralSlot),
				literal(fixtureSameWidthLiteralSlot)),
			OpIsNegativeI64, OpExit), 1},
		{"a string with a larger byte is the greater", append(
			cmp(stringShort, literal(fixtureSameWidthLiteralSlot),
				literal(fixtureLiteralSlot)),
			OpIsPositiveI64, OpExit), 1},

		// And the pair the bytes cannot settle: one is the beginning of the
		// other, so the comparison over the shorter of them comes back equal and
		// the lengths are what say which way round it goes. A reader that
		// compared over the longer length would be reading past an allocation to
		// answer this.
		{"a string is below the one that begins with it", append(
			cmp(stringShort, literal(fixtureLiteralSlot),
				literal(fixturePrefixedLiteralSlot)),
			OpIsNegativeI64, OpExit), 1},
		{"and the one that begins with it is the greater", append(
			cmp(stringShort, literal(fixturePrefixedLiteralSlot),
				literal(fixtureLiteralSlot)),
			OpIsPositiveI64, OpExit), 1},

		// The order handed to the program as it stands, which is where a string
		// has to agree with every other type: exactly one, and not whatever
		// distance memcmp happened to report. 'o' and 'i' are six apart, so an
		// order passed through unnormalised would show up here as a six.
		{"the order of two strings is a sign and not a distance", append(
			cmp(stringShort, literal(fixtureSameWidthLiteralSlot),
				literal(fixtureLiteralSlot)), OpExit), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// An order spends both values and the ids between them, and
				// none of these types ever owned a mark entry — a string
				// included, every one here being a literal, which is the one
				// string on the stack the collector is told nothing about.
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestStringEqualityCConformance covers OpEq on strings, which is the one
// primitive whose equality is not the equality of the word it stands on.
//
// The one claim these cannot make is the interesting half of the rule — that two
// separate allocations holding the same bytes are equal. Every string these build
// is an interned literal, and interning is what makes equal bytes one slot, so an
// equal pair here is also the same address. The pair that says it is built out of
// OpConcatString instead and is in TestConcatStringCConformance. What is held
// here is the other half: that unequal bytes are answered by reading them, and
// that the address shortcut is a shortcut rather than the whole rule.
func TestStringEqualityCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// An equality as the compiler lays one, which is the shape an order is laid
	// in: each operand down under its own type id, the instruction being told
	// what it is comparing by nothing else.
	eq := func(left, right uint64) []operation {
		operations := append([]operation{newUnaryOp(OpPushStringLiteral, left)},
			pushTypeID(stringShort)...)
		operations = append(operations,
			newUnaryOp(OpPushStringLiteral, right))
		operations = append(operations, pushTypeID(stringShort)...)
		return append(operations, OpEq, OpExit)
	}

	for _, test := range []struct {
		name       string
		operations []operation
		want       uint64
	}{
		// The same slot twice, which is one address: equal without a byte being
		// read, and the case a program makes most often.
		{"a string equals itself", eq(fixtureLiteralSlot, fixtureLiteralSlot), 1},

		// Two of the same width differing in one byte, which is what says the
		// comparison reaches the bytes at all. Two addresses that are not equal
		// would answer this one yes if the shortcut were the whole rule.
		{"a string differing in a byte is not equal",
			eq(fixtureLiteralSlot, fixtureSameWidthLiteralSlot), 0},

		// And a string against the one that begins with it, which is what the
		// length settles. Compared over the shorter of the two the bytes agree,
		// so a reader that took the bytes and left the lengths alone would call
		// these equal.
		{"a string is not equal to the one that begins with it",
			eq(fixtureLiteralSlot, fixturePrefixedLiteralSlot), 0},
		{"nor the one that begins with it equal to it",
			eq(fixturePrefixedLiteralSlot, fixtureLiteralSlot), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// An equality spends both values and the ids between them, and a
				// literal is the one string on the stack that owes the collector
				// no entry.
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestConcatStringCConformance covers OpConcatString, which is the first
// instruction that builds a string rather than naming one the image already
// laid down.
//
// What it leaves has to be a string in the same shape a literal is, since
// nothing downstream asks which of the two it was handed: the mark word, then
// the length, then the bytes. So the result is read back through the pointer at
// those three offsets, the way TestPushStringLiteralCConformance reads a
// literal — and a concatenation long enough to cross a word is what says the
// right operand's bytes land behind the left's rather than over them.
func TestConcatStringCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	program := func(parts ...[]operation) []operation {
		var operations []operation
		for _, part := range parts {
			operations = append(operations, part...)
		}
		return operations
	}

	literal := func(slot uint64) []operation {
		return []operation{newUnaryOp(OpPushStringLiteral, slot)}
	}

	// The two operands down and the instruction over them, which is the whole
	// shape: neither carries a type id, a sum of two strings being the only way
	// stringBinaryOpcodes in 3_operation.go reaches this opcode.
	concat := func(left, right []operation) []operation {
		return program(left, right, []operation{OpConcatString})
	}

	// "hiho", which is two literals of the same width, and "hithithit", which is
	// nine bytes and so is the one that runs past the word its bytes start in.
	// The nested build is also the only way an operand with a mark entry of its
	// own is reached, every other string on the stack here being a literal.
	pair := concat(literal(fixtureLiteralSlot), literal(fixtureSameWidthLiteralSlot))
	triple := concat(
		concat(literal(fixturePrefixedLiteralSlot), literal(fixturePrefixedLiteralSlot)),
		literal(fixturePrefixedLiteralSlot))

	// A word of the result, reached by adding an offset to the pointer and
	// loading an int out of it. The load spends the pointer and the entry it
	// owned, which is why the programs below stop owing nothing.
	word := func(built []operation, offset uint64) []operation {
		return program(built, []operation{pushI64(offset * wordByteSize), OpAddI64},
			pushTypeID(intShort), []operation{OpLoad})
	}

	// And that word asked whether it is the one it should be, inside the program
	// rather than off the exit status: a status is kept to its low eight bits and
	// the word the bytes lie in does not fit in one.
	is := func(built []operation, offset, want uint64) []operation {
		return program(word(built, offset), pushTypeID(intShort),
			[]operation{pushI64(want)}, pushTypeID(intShort),
			[]operation{OpEq, OpExit})
	}

	const (
		packedHiho     = 'h' | 'i'<<8 | 'h'<<16 | 'o'<<24
		packedHithithi = 'h' | 'i'<<8 | 't'<<16 | 'h'<<24 |
			'i'<<32 | 't'<<40 | 'h'<<48 | 'i'<<56
	)

	for _, test := range []struct {
		name       string
		operations []operation
	}{
		// The mark word the allocator wrote, which reads as nothing at all: white,
		// a string, not contained, and the end of a sweep list it is the only
		// thing on. A wrong kind is the failure this catches — every other one is
		// a number here.
		{"the pointer names a mark word", is(pair, 0, 0)},

		// The length, which is the two lengths added and not either of them.
		{"the length is both lengths", is(pair, 1, uint64(
			len(fixtureStringLiteral)+len(fixtureSameWidthLiteral)))},
		{"and adds all three of a nested build", is(triple, 1, uint64(
			3*len(fixturePrefixedLiteral)))},

		// The bytes, in the order the operands stood in. Reversed, this word
		// would read "hohi".
		{"the left bytes lead and the right follow", is(pair, 2, packedHiho)},

		// The nine-byte build, which is the case a word-at-a-time copy would get
		// wrong: the right operand's bytes begin three past a word boundary and
		// end one past the next.
		{"the bytes run past the word they start in",
			is(triple, 2, packedHithithi)},
		{"and the allocation runs as far as they do", is(triple, 3, 't')},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				// True, the word being the one the layout says lies there.
				"exitStatus=1",
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}

	// What the exit status cannot carry: that the operands were spent and the
	// result was not. A literal owes no entry, so the nested build is what makes
	// the claim — its inner result is an allocation with an entry of its own, and
	// an outer concatenation that left it standing would be handing the collector
	// a root naming a word the program has given up.
	t.Run("the operands' entries go and the result's stands", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			// The result, still standing.
			"operandCount=1",
			// One object entry and not the two a kept operand would leave.
			"markByteCount=8",
		}, runImage(t, harness, "stop",
			program(triple, []operation{pushI64(0), OpExit})))
	})

	// The block itself, read from the side no instruction reaches. A string is
	// the one kind zam_vm_alloc writes no type header for, so the two fields a
	// header would occupy are the length and the bytes — which is what these last
	// two lines are, and what a header stamped over them would destroy.
	t.Run("the result is an allocation the collector owns", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			"operandCount=1",
			"markByteCount=8",
			// One block on the list, which is the whole claim that the string was
			// linked as it was made.
			"heap.count=1",
			// White, nothing having collected.
			"heap.state=0",
			// ZAMOBJECT_STRING, which is what says the allocator was told a
			// string rather than an instance.
			"heap.kind=0",
			fmt.Sprintf("heap.shortID=%d",
				len(fixtureStringLiteral)+len(fixtureSameWidthLiteral)),
			"heap.elementCount=0",
		}, runImage(t, harness, "heap",
			program(pair, []operation{pushI64(0), OpExit})))
	})

	// The half TestStringEqualityCConformance names and cannot build: two
	// allocations holding the same bytes. Interning makes equal literals one
	// slot, so until there was an allocator for a string every equal pair was
	// also the same address.
	t.Run("two strings built alike are two allocations", func(t *testing.T) {
		as := func(shortID uint64) []operation {
			return program(pair, pushTypeID(shortID), pair, pushTypeID(shortID),
				[]operation{OpEq, OpExit})
		}

		// As words, which is what says they are not the same string by address.
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			"operandCount=0",
			"markByteCount=0",
		}, runImage(t, harness, "stop", as(intShort)))

		// And as strings, which is the rule: equal bytes are an equal string
		// however each of them was built.
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=1",
			"operandCount=0",
			"markByteCount=0",
		}, runImage(t, harness, "stop", as(stringShort)))
	})
}

// mainFirstLocalSlot is the slot main's first local lies in. Main is fallen
// into rather than called, so the frame it runs on is laid by the run itself:
// the slot a frame keeps for its receiver, then main's two parameters. The
// runtime names that width ZAMMAIN_FRAME_WORDS, and a local numbered anywhere
// else here would read one of those three instead.
const mainFirstLocalSlot = 3

// frameIntLocation is the pair a location is, built over a frame slot rather
// than a static one: OpPushVariable leaves where the slot lies and the type id
// says what to read out of it.
//
// What separates this from [staticIntLocation] is that the word is not an
// address. A frame slot lies in the very stack the pushes above it may move, so
// what is pushed is how far into that stack it is — which means every
// instruction that reads a location has to resolve it rather than follow it.
func frameIntLocation(slot uint64) []operation {
	return append(
		[]operation{newUnaryOp(OpPushVariable, slot)},
		pushTypeID(intShort)...)
}

// callOperations lays a program that builds a closure over a routine, calls it,
// and goes on with whatever the call left.
//
// The routine's body goes past main's, main being fallen into at offset zero,
// and the offset it lands at is measured rather than written down so that an
// instruction added to the caller moves it. The closure is given main's own
// function type, that being the only one these images hold — nothing here reads
// a signature, so which one it is does not matter.
func callOperations(arguments []operation, frameWords uint64,
	after, body []operation) []operation {
	lambda := newBinaryOp(OpNewLambda, 0, 0)

	caller := append(pushTypeID(mainFunctionShort), lambda)
	caller = append(caller, arguments...)
	caller = append(caller, newUnaryOp(OpCallMethod, frameWords))
	caller = append(caller, after...)

	lambda.first = uint64(byteCount(caller))
	return append(caller, body...)
}

// TestFrameCConformance covers the instructions a frame is read and written
// through, and the calls and returns that open and close one.
//
// A frame slot cannot be checked the way a static slot can. Its address is not
// knowable ahead of the run — the stack it lies in moves as it grows — so what
// is asserted is that a value written to a slot comes back out of it, and that
// two slots are two: an offset misread by one word is a program that reads its
// neighbour, and nothing but a cross-check would notice.
func TestFrameCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	dropLocation := []operation{OpPopWord, OpPopWord, OpPopWord, OpPopWord}

	// A local, made the way a declaration makes one: a default value pushed into
	// the next free slot. The slot is only a slot because the words are there.
	local := func() []operation {
		return append(pushTypeID(intShort), OpDefaultValue)
	}

	// Putting a value in a frame slot and leaving nothing behind.
	store := func(slot, value uint64) []operation {
		operations := append(frameIntLocation(slot), pushI64(value))
		operations = append(operations, pushTypeID(intShort)...)
		operations = append(operations, OpStore)
		return append(operations, dropLocation...)
	}

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
		// standing is what the program deliberately leaves on the operand stack,
		// which for anything declaring a local is the local itself: a frame slot
		// exists for as long as its words do.
		standing int
	}{
		// The load is what makes the store observable, and the slot is reached
		// twice through locations built from scratch rather than through one the
		// store left, so what comes back is the frame and not a leftover word.
		{"a value written to a slot comes back out of it", append(
			append(append(local(), store(mainFirstLocalSlot, 42)...),
				frameIntLocation(mainFirstLocalSlot)...),
			OpLoad, OpExit), 42, 1},

		// Two slots written and only the first read back. If the offsets named
		// the same words the second store would have overwritten the first and
		// the answer would be 7.
		{"neighbouring slots are not the same slot", append(
			append(append(append(append(local(), local()...),
				store(mainFirstLocalSlot, 42)...),
				store(mainFirstLocalSlot+1, 7)...),
				frameIntLocation(mainFirstLocalSlot)...),
			OpLoad, OpExit), 42, 2},

		// OpAddressOf spends the type id and leaves the pointer, and
		// OpDereference is the load that follows a pointer rather than a
		// location the compiler laid down. Round-tripping a frame slot through
		// the pair is what says the pointer a program holds can name one.
		{"a slot's address dereferences back to it", append(
			append(append(local(), store(mainFirstLocalSlot, 42)...),
				frameIntLocation(mainFirstLocalSlot)...),
			append([]operation{OpAddressOf},
				append(pushTypeID(intShort), OpDereference, OpExit)...)...),
			42, 1},

		// The argument is read out of slot 1, the slot a frame keeps for its
		// receiver being slot 0, so a routine that read its own receiver as its
		// first parameter would come back with the closure pointer instead.
		{"a call passes its argument and the return brings a value back",
			callOperations([]operation{pushI64(21)}, 2,
				[]operation{OpExit},
				append(append(frameIntLocation(1), OpLoad),
					append([]operation{pushI64(2), OpMulI64},
						append(pushTypeID(intShort), OpReturn)...)...)),
			42, 0},

		// A void return leaves nothing, so what says the frame was let go of is
		// the status pushed after the call being the one that is read: a frame
		// left standing would put the closure word under it.
		{"a void return leaves the stack as the call found it",
			callOperations(nil, 1, []operation{pushI64(42), OpExit},
				[]operation{OpReturnVoid}),
			42, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				fmt.Sprintf("operandCount=%d", test.standing),
				// A frame slot's pointer owns no mark entry, the slot's own
				// contents having been given one when they were pushed, and an
				// int never owned one either. The closure the calls are made
				// through does, and both of them spend it.
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestStepCConformance covers the four instructions that move a value in place.
//
// A step is the one arithmetic told a location rather than a value, so the word
// it leaves is only half of what it did. The other half is in memory and is read
// back here through a location built afresh, the way a store is read back in
// TestLocationCConformance — except after a pre-step, where the value handed
// over was read out of the slot after the write and so is the memory already.
//
// How far a step goes and how much of the slot it lands in are both the type
// id's to answer, and neither is anything the compiler worked out: emitStep in
// 3_operation.go pushes the id and scales nothing. The char and the two pointers
// are what hold that — a char moves by one of itself and lands in four bytes,
// and a pointer moves by a whole element of a pointee it takes a step to reach.
func TestStepCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	floatShort := uint64(syntax.TypeKindFloat) << 16
	dropLocation := []operation{OpPopWord, OpPopWord, OpPopWord, OpPopWord}

	program := func(parts ...[]operation) []operation {
		var operations []operation
		for _, part := range parts {
			operations = append(operations, part...)
		}
		return operations
	}

	// A location over the slot, told what lies there. Every case below writes
	// the slot as an int and steps it as whatever this is handed: a slot holds a
	// word either way, and the id is the whole of the difference.
	location := func(shortID uint64) []operation {
		return append(
			[]operation{newUnaryOp(OpPushVariable, mainFirstLocalSlot)},
			pushTypeID(shortID)...)
	}

	// The local each step moves, put there the way a declaration and an
	// assignment put one there: a default value to make the slot, then a store
	// through a location over it.
	local := func(value uint64) []operation {
		operations := append(pushTypeID(intShort), OpDefaultValue)
		operations = append(operations, location(intShort)...)
		operations = append(operations, pushI64(value))
		operations = append(operations, pushTypeID(intShort)...)
		operations = append(operations, OpStore)
		return append(operations, dropLocation...)
	}

	// Stepping the slot and letting go of the value that leaves, so that what is
	// read afterwards is the memory and not the word the step handed back.
	step := func(shortID uint64, opcode operation) []operation {
		operations := append(location(shortID), opcode)
		operations = append(operations, pushTypeID(shortID)...)
		return append(operations, OpPop)
	}

	// The whole slot read back as an int, which is what catches a step that
	// moved bytes it was not told about: they are in the answer.
	readBack := func() []operation {
		return append(location(intShort), OpLoad)
	}

	// And that answer asked whether it is the one word it should be, since a
	// status is a byte and these are compared wider than one.
	slotEquals := func(value uint64) []operation {
		operations := append(readBack(), pushTypeID(intShort)...)
		operations = append(operations, pushI64(value))
		operations = append(operations, pushTypeID(intShort)...)
		return append(operations, OpEq)
	}

	// A char at the top of a word that holds something else underneath it. The
	// step is told four bytes lie there and the other four are the neighbour it
	// must not carry into — which on an instance is the member laid after it.
	const charNeighbour = uint64(1) << 32

	for _, test := range []struct {
		name       string
		operations []operation
		want       uint64
		// standing is the local the program declared, a frame slot existing for
		// as long as its words do.
		standing int
	}{
		// A pre-step's value is read out of the slot after the write, so the
		// word it leaves is the memory and there is nothing further to ask of
		// it. A post-step's is read before, which is why each of those is two
		// cases: what it handed over, and what it left behind.
		{"a pre-increment leaves the place as it now is", program(
			local(41), location(intShort),
			[]operation{OpIncPre, OpExit}), 42, 1},
		{"a pre-decrement leaves the place as it now is", program(
			local(43), location(intShort),
			[]operation{OpDecPre, OpExit}), 42, 1},

		{"a post-increment leaves the place as it was", program(
			local(41), location(intShort),
			[]operation{OpIncPost, OpExit}), 41, 1},
		{"a post-increment moved the place all the same", program(
			local(41), step(intShort, OpIncPost), readBack(),
			[]operation{OpExit}), 42, 1},
		{"a post-decrement leaves the place as it was", program(
			local(43), location(intShort),
			[]operation{OpDecPost, OpExit}), 43, 1},
		{"a post-decrement moved the place all the same", program(
			local(43), step(intShort, OpDecPost), readBack(),
			[]operation{OpExit}), 42, 1},

		// A char counts in one of itself, exactly as an int does.
		{"a char steps by one", program(
			local('a'), location(charShort),
			[]operation{OpIncPre, OpExit}), 'b', 1},

		// And lands in four bytes. A step that returned a whole word to the slot
		// would answer this one with the neighbour cleared, the arithmetic
		// itself having been right either way.
		{"a char step lands in four bytes and not eight", program(
			local(charNeighbour|'a'), step(charShort, OpIncPre),
			slotEquals(charNeighbour|'b'), []operation{OpExit}), 1, 1},

		// A float steps by one of what it is rather than by one of the word it
		// stands on, which is a different number in every bit. It is compared
		// with the float instruction because a float has no equality in
		// valuesEqual to reach.
		{"a float steps up by one", program(
			local(math.Float64bits(1.5)), location(floatShort),
			[]operation{OpIncPre}, []operation{pushFloat(2.5)},
			[]operation{OpEqualF64, OpExit}), 1, 1},
		{"a float steps down by one", program(
			local(math.Float64bits(2.5)), location(floatShort),
			[]operation{OpDecPre}, []operation{pushFloat(1.5)},
			[]operation{OpEqualF64, OpExit}), 1, 1},

		// A pointer steps by a whole element of what it points at, which is a
		// number only the pointee's definition holds: the pointer's own id says
		// which pointer it is and nothing more, so a step that moved it by one
		// or by a word would have had to read nothing to do it.
		{"a pointer steps by an element of its pointee", program(
			local(0), step(fixtureSizedTailPtrShort, OpIncPre), readBack(),
			[]operation{OpExit}),
			fixtureTailedStructHeader + wordByteSize +
				fixturePointeeTailLength*wordByteSize, 1},

		// The same pointee without the tail, which is the pair these two
		// fixtures are here for: one declaration, two widths, and the only
		// difference between the ids is the length the pointer record carries.
		// A stride read off the definition and not off the id would move both of
		// these the same distance.
		{"a pointer to an untailed pointee steps a shorter distance", program(
			local(0), step(fixtureOpenTailPtrShort, OpIncPre), readBack(),
			[]operation{OpExit}),
			fixtureTailedStructHeader + wordByteSize, 1},

		// And back down by the same element, the one place the pointer's
		// direction is written separately from the integer's.
		{"a pointer steps back down by an element", program(
			local(fixtureTailedStructHeader+wordByteSize+
				fixturePointeeTailLength*wordByteSize),
			step(fixtureSizedTailPtrShort, OpDecPre), readBack(),
			[]operation{OpExit}), 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				fmt.Sprintf("operandCount=%d", test.standing),
				// A frame location owns no entry and neither does an int, a
				// char or a float. A stepped pointer does own one, and the case
				// that leaves one standing spends it through OpPop.
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestStepStaticCConformance is the one claim a frame slot cannot make: that a
// step lets go of the mark entry its location owned.
//
// A frame location is never given one, so every case in TestStepCConformance
// would pass with that pop taken out. A static pointer is given one whether
// the collector will do anything with it or not, so a step that left it standing
// would leave the collector a root naming a word the program has given up —
// which is what markByteCount reads here.
func TestStepStaticCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	assert.Equal(t, []string{
		"result=EXIT",
		// The slot begins at zero: nothing is stamped into the static region
		// but the literals and the one instance header, so a step of it is a
		// step of the number that lies there.
		"exitStatus=1",
		"operandCount=0",
		"markByteCount=0",
	}, runImage(t, harness, "stop",
		append(staticIntLocation(), OpIncPre, OpExit)))
}

// TestInstanceHeaderCConformance holds OpPushMember to the layout the compiler
// recorded its offsets against.
//
// A member's offset on the wire is net of the header, so that it is the same
// number up and down an inheritance chain whatever header the instance in front
// of it turns out to carry, and the reader is the side that adds the difference.
// Nothing on the wire states that difference — the definition gives the offset
// and the width and never the gap between them — so this is the only place the
// two sides are held to the same number.
//
// Reading the member back is not enough on its own to say the gap was right. A
// store and a load through the same wrong offset agree with each other perfectly,
// and the instance initializer makes that worse rather than better: it writes
// each member's default through the very same offset, so an offset a word short
// overwrites the header with a zero and then reads that zero back as a member
// that was never written. The header words are therefore read back as ints in
// their own right, which is what makes a wrong gap visible.
func TestInstanceHeaderCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// A fresh instance standing in a frame slot, and the slot it stands in. Main
	// is fallen into, so what lies below it is the frame the run laid.
	const slot = mainFirstLocalSlot
	instance := append(pushTypeID(fixtureClassShort), OpDefaultValue)

	// The location of the instance's one member, reached the way a selection
	// reaches it: the slot under its own type, and the member's index.
	member := func() []operation {
		return append(
			append([]operation{newUnaryOp(OpPushVariable, slot)},
				pushTypeID(fixtureClassShort)...),
			newUnaryOp(OpPushMember, 0))
	}

	// A word of the instance read as an int, which is how a header word is
	// looked at from a program that has no way to name one.
	word := func(offset uint64) []operation {
		return append(frameIntLocation(slot+offset), OpLoad)
	}

	// Writing 7 through the member's offset, and dropping the location the store
	// leaves standing.
	store := append(append(member(), pushI64(7)),
		append(pushTypeID(intShort),
			OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord)...)

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
	}{
		// The read side, which says only that the two agree.
		{"a member comes back out of the instance", append(
			append(append(instance, store...), member()...),
			OpLoad, OpExit), 7},

		// The mark word, which OpDefaultValue fills because an instance standing
		// in a frame slot has no allocator to fill it: white, and
		// ZAMOBJECT_INSTANCE, and contained, which packs to 36. Contained is
		// the part that is about this instance rather than instances in general
		// — the storage is the stack's and not the collector's — and it is why
		// this is not the word zam_vm_alloc writes. A member offset added to
		// nothing would have put the 7 here.
		{"a fresh instance carries the mark word it is read through", append(
			append(instance, word(0)...), OpExit), 36},
		{"writing a member leaves the mark word alone", append(
			append(append(instance, store...), word(0)...), OpExit), 36},

		// The type id word after it, which is where an offset a word short would
		// have landed. Its low byte is what the status can carry, and the id is
		// a whole number of 256s, so what says the word survived is that byte
		// still being zero rather than the 7 written through the member.
		{"writing a member leaves the type id alone", append(
			append(append(instance, store...), word(1)...), OpExit), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// The instance itself is what each of these leaves standing: it
				// is a frame slot, and a frame slot exists for as long as its
				// words do, with the value entry naming it.
				"operandCount=3",
				fmt.Sprintf("markByteCount=%d", valueEntryBytes),
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestSizeOfCConformance covers the one instruction that asks a width and
// nothing else. What it answers is the stride from one value of the type to the
// next where such values lie inline, which is the same number OpStore and OpLoad
// measure a value by — so a wrong answer here is a wrong answer everywhere.
//
// It is only ever emitted where the width could not be had before the run: the
// stride of a pointer whose pointee is an open generic argument, which is
// emitPointerStride in 3_operation.go. Node[int]* and Node[BigType]* step
// different distances under the same instruction, and this is what tells them
// apart.
//
// The widths are asked of the fixtures rather than written down, since what the
// image says an instance is wide is exactly what the instruction has to come
// back with. The status is one byte, which every case here is inside.
func TestSizeOfCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// The fixture class as its own definition states it: a header and one member.
	const fixtureClassBytes = 3 * wordByteSize

	for _, test := range []struct {
		name       string
		operations []operation
		want       uint64
	}{
		// A primitive is its own bytes and reaches no definition at all, the ids
		// below reservedTypeIDCount pointing at none.
		{"a primitive", pushTypeID(intShort), wordByteSize},

		// A class value is its layout, header included. The header is part of
		// what a value of the type occupies wherever one lies, so a width that
		// left it out would have every array of them overlap.
		{"a class value carries its header", pushTypeID(fixtureClassShort),
			fixtureClassBytes},
		// And a class holding a class, which is the case that says the width is
		// read out of the definition rather than assumed: the inner value brings
		// its own header, so the two headers are both in the answer.
		{"a class holding a class", pushTypeID(fixtureNestShort),
			fixtureClassHeader + fixtureClassBytes},

		// An array is the element's width times how many, and the count is in the
		// id rather than the definition — two arrays of the same element are
		// different widths under one declaration.
		{"an array is folded into the answer",
			pushArrayTypeID(fixtureClassShort, 3), 3 * fixtureClassBytes},
		// A tail is the same statement about the other length the id carries. The
		// fixed part is the definition's and the elements behind it are the id's.
		{"a tail is folded into the answer",
			pushTailedTypeID(fixtureStructShort, 2),
			fixtureTailedStructHeader + wordByteSize + 2*wordByteSize},

		// Everything held as a reference is one word however wide the thing it
		// refers to is. A function type is the one this image carries, and its own
		// definition is a signature rather than a layout — so a width taken off
		// the definition would come back as something other than a word.
		{"a reference is a word", pushTypeID(mainFunctionShort), wordByteSize},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// The id goes and one word comes back. Neither is anything the
				// collector could start from: a type id is three plain words and
				// a width is a number.
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop",
				append(test.operations, OpSizeOf, OpExit)))
		})
	}
}

// TestPushGenericArgTypeCConformance covers the instruction that turns an open
// type back into a type. A template's body is compiled once however many times
// it is instantiated, so a type mentioning one of its parameters has no id to be
// written down; what is written is the parameter's position in the
// declaration's enumeration of open types, and each instantiation stores that
// enumeration resolved through its own bindings.
//
// So the instruction is a read with two coordinates, and both are under test: the
// generic half of the id below it says which instantiation's enumeration, and
// the operand says which position in it. Every pairing is asked, since an
// implementation that dropped either coordinate would still answer correctly for
// one of them.
//
// The id is pushed here rather than reached through the receiver. The compiler
// gets it from OpTypeOf over the receiver — see emitPushTypeID in
// 3_operation.go — and what is under test here is the read rather than how the
// id arrived, so this pushes the id that instruction would have left. What
// OpTypeOf leaves is held on its own in TestTypeOfCConformance.
func TestPushGenericArgTypeCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	for _, test := range []struct {
		name        string
		genericID   uint64
		index       uint64
		shortID     uint64
		arrayLength uint64
		tailLength  uint64
	}{
		// The first instantiation. A bare primitive, so every word but the first
		// is zero — which is the case an id read out of the wrong place is least
		// likely to be caught by, and the reason the other three are not like it.
		{name: "the first argument of the first instantiation",
			genericID: 0, index: 0, shortID: intShort},
		// A declared type in the same entry, reached by moving the operand alone.
		{name: "the second argument of the first instantiation",
			genericID: 0, index: 1, shortID: fixtureClassShort},

		// The second instantiation, reached by moving the generic half of the id
		// alone. An array, so the middle word is what says the read landed here.
		{name: "the first argument of the second instantiation",
			genericID: 1, index: 0, shortID: charShort, arrayLength: 4},
		// And a tail, which is the last word of the three.
		{name: "the second argument of the second instantiation",
			genericID: 1, index: 1, shortID: fixtureStructShort, tailLength: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations := append(
				pushTypeID(fixtureGenericShort(test.genericID)),
				newUnaryOp(OpPushGenericArgType, test.index))

			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=0",
				// The three words the answer is, which are the three the id
				// pushed under it was — an id is spent and an id is left.
				"operandCount=3",
				// No entry either side. A type id is three plain words wherever
				// it came from, so neither the one spent nor the one left is
				// something the collector could start from.
				"markByteCount=0",
				fmt.Sprintf("type.shortID=%d", test.shortID),
				fmt.Sprintf("type.arrayLength=%d", test.arrayLength),
				fmt.Sprintf("type.tailLength=%d", test.tailLength),
			}, runImage(t, harness, "type",
				append(operations, pushI64(0), OpExit)))
		})
	}
}

// TestIsSubtypeOfCConformance holds one type standing below another to being
// four different questions, and to the types alone saying which is asked.
//
// The instruction takes two ids and leaves a boolean, so nothing about how it got
// there is visible from a program: what is asserted is only the answer, and the
// discrimination has to come from asking each pairing both ways round. A rule
// that answered by comparing the ids, or by looking every pair up in a set, would
// be right about one of these groups and wrong about the rest — which is what
// makes the groups worth being separate rather than a list of true cases.
func TestIsSubtypeOfCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	for _, test := range []struct {
		name          string
		child, parent []operation
		want          int
	}{
		// A primitive stands in no line of descent, so the ids being the same is
		// the whole of it and two primitives are unrelated however alike they are
		// as words. Both are asked, since a rule that always said yes and a rule
		// that always said no each get one of them right.
		{"a primitive is its own supertype",
			pushTypeID(intShort), pushTypeID(intShort), 1},
		{"one primitive does not stand below another",
			pushTypeID(intShort), pushTypeID(charShort), 0},

		// A class does descend, and the set on its record is the only thing that
		// says so — the two ids share nothing, so nothing about the ids
		// themselves could produce this answer.
		{"a class stands below the abstract it names",
			pushTypeID(fixtureChildShort), pushTypeID(fixtureAbstractShort), 1},

		// The same pair the other way about, which says the set was read as the
		// child's and not as a relation between the two. It is also the case a
		// lookup that trusted the slot it landed in gets wrong: the abstract's set
		// holds one key and the class is not it, so the class hashes into that one
		// key's slot and only reading the slot back tells a miss from a hit.
		{"an abstract does not stand below the class below it",
			pushTypeID(fixtureAbstractShort), pushTypeID(fixtureChildShort), 0},

		// A class whose set was built empty, against the same abstract. An empty
		// set has no slots to land in at all, which is the other half of a miss
		// and the one a table read without checking its width would run off.
		{"a class stands below an abstract it does not name",
			pushTypeID(fixtureClassShort), pushTypeID(fixtureAbstractShort), 0},

		// And a declared type against a primitive, which is a set being asked
		// about a key no set anywhere holds.
		{"a class does not stand below a primitive",
			pushTypeID(fixtureChildShort), pushTypeID(intShort), 0},

		// And against void, whose short id is zero. That is the value an unwritten
		// slot holds, so this is the one key a set can be asked about where the
		// slot read-back has nothing to distinguish a hole from a hit.
		{"a class does not stand below void",
			pushTypeID(fixtureChildShort), pushTypeID(voidShort), 0},

		// A pointer descends by what it points at, which lives in its record and
		// nowhere in its id — so this is the one group where the two sides have to
		// be opened before either can be compared, and the pair of ids here is no
		// more alike than the pair above.
		{"a pointer stands below a pointer to what stands above it",
			pushTypeID(fixtureChildPointerShort),
			pushTypeID(fixtureAbstractPointerShort), 1},
		{"and not the other way about",
			pushTypeID(fixtureAbstractPointerShort),
			pushTypeID(fixtureChildPointerShort), 0},

		// One pointer against one plain type, both ways. A pointer is one word
		// and the value is however many, so neither may be read as the other —
		// and a rule that reduced a pointer to its pointee before comparing would
		// answer yes to both of these.
		{"a pointer does not stand below what it points at",
			pushTypeID(fixtureChildPointerShort),
			pushTypeID(fixtureChildShort), 0},
		{"and a value does not stand below a pointer to itself",
			pushTypeID(fixtureChildShort),
			pushTypeID(fixtureChildPointerShort), 0},

		// void* is the one pointer whose answer does not come from its pointee's.
		// It names no layout, so there is nothing a pointee could fail to be —
		// and the pointee here is a class that stands nowhere near void, which is
		// what says the answer came from the rule rather than from the descent.
		// Read the other way it says nothing, a void* being no kind of Child*.
		{"every pointer stands below a void pointer",
			pushTypeID(fixtureChildPointerShort),
			pushTypeID(fixtureVoidPointerShort), 1},
		{"and a void pointer stands below no other",
			pushTypeID(fixtureVoidPointerShort),
			pushTypeID(fixtureChildPointerShort), 0},

		// A plain value against it, which says the rule is asked after both sides
		// are known to be pointers and not before. Nothing is a void* but a
		// pointer, however little a void* promises about what it points at.
		{"a value does not stand below a void pointer",
			pushTypeID(fixtureChildShort),
			pushTypeID(fixtureVoidPointerShort), 0},

		// Buf~N* is a Buf~*. These two pointees differ in the tail word and in
		// nothing else, a tail length riding with the type id rather than sitting
		// in the layout, so the sized one is laid out exactly as the unsized one
		// it was written from. Not the other way about: an unsized tail is the one
		// with no length to give.
		{"a sized tail pointer stands below the unsized one",
			pushTypeID(fixtureSizedTailPtrShort),
			pushTypeID(fixtureOpenTailPtrShort), 1},
		{"and an unsized tail pointer does not stand below a sized one",
			pushTypeID(fixtureOpenTailPtrShort),
			pushTypeID(fixtureSizedTailPtrShort), 0},

		// The tail is dropped against a pointer at something else entirely, which
		// is the rule being about one type written two ways rather than about the
		// parent simply having no length. A reader that took the lengths and left
		// the ids alone would answer yes here.
		{"a sized tail pointer does not stand below a pointer at another type",
			pushTypeID(fixtureSizedTailPtrShort),
			pushTypeID(fixtureChildPointerShort), 0},

		// The same two pointees named directly, which is what confines the rule to
		// the pointer. Standing on the stack they are two widths and neither may
		// be read as the other, which is the tail-length guard the rule above is
		// deliberately reached past.
		{"a sized tail is not an unsized one as a value",
			pushTailedTypeID(fixtureStructShort, fixturePointeeTailLength),
			pushTypeID(fixtureStructShort), 0},

		// A function stands below another by its signature, and the two positions
		// of a signature are read in opposite directions. What a caller does with
		// a function is hand it arguments and take a result, so a stand-in may
		// accept more than was promised and must promise no less than it returns.
		//
		// Each direction is one pair of signatures differing in one position, and
		// each pair is asked both ways. A reader that took both positions the same
		// way round gets one pair right and the other exactly backwards.
		{"a function taking the abstract stands below one taking the class",
			pushTypeID(fixtureParentParamShort),
			pushTypeID(fixtureChildParamShort), 1},
		{"and one taking the class does not stand below one taking the abstract",
			pushTypeID(fixtureChildParamShort),
			pushTypeID(fixtureParentParamShort), 0},
		{"a function returning the class stands below one returning the abstract",
			pushTypeID(fixtureChildReturnShort),
			pushTypeID(fixtureParentReturnShort), 1},
		{"and one returning the abstract does not stand below one returning the class",
			pushTypeID(fixtureParentReturnShort),
			pushTypeID(fixtureChildReturnShort), 0},

		// Two signatures of different lengths, which a walk that stopped at the
		// shorter would call related: main's takes nothing and every other one
		// here takes one.
		{"signatures of different arity stand nowhere near each other",
			pushTypeID(mainFunctionShort),
			pushTypeID(fixtureParentParamShort), 0},

		// And a function against a class, which is the same both-or-neither the
		// pointers are held to.
		{"a function does not stand below a class",
			pushTypeID(fixtureParentParamShort),
			pushTypeID(fixtureChildShort), 0},

		// A run descends nowhere. It is copied and passed whole, so a run of the
		// class is a different width from a run of the abstract and could not be
		// read as it — which the elements' own relation, asserted true above, says
		// nothing about. A rule that held the lengths equal and then looked the
		// elements up would answer yes here.
		{"a run of a class does not stand below a run of what stands above it",
			pushArrayTypeID(fixtureChildShort, 3),
			pushArrayTypeID(fixtureAbstractShort, 3), 0},
		{"a run does stand below itself",
			pushArrayTypeID(fixtureChildShort, 3),
			pushArrayTypeID(fixtureChildShort, 3), 1},

		// And the same length on one side only, which is the case where the
		// elements are not merely related but identical.
		{"a run does not stand below its own element type",
			pushArrayTypeID(fixtureChildShort, 3),
			pushTypeID(fixtureChildShort), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var operations []operation
			operations = append(operations, test.child...)
			operations = append(operations, test.parent...)
			operations = append(operations, OpIsSubtypeOf, OpExit)

			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// Six words in and one out, and the one is what the exit spent.
				// An id left standing would be an operand the instruction failed
				// to take.
				"operandCount=0",
				// A type id is three plain words and a boolean is one, so
				// nothing here is anything the collector could start from.
				"markByteCount=0",
			}, runImage(t, harness, "stop", operations))
		})
	}
}

// TestTypeOfCConformance holds a value's own type to being read off the value
// rather than off the id the source named it by.
//
// The two can differ only where the value carries a word saying what it was made
// under, so a header is the whole of the answer wherever there is one. Which
// means the instruction has two jobs it cannot be told apart from a stub at: it
// has to find the header, and where there is none it has to leave the id alone —
// and a value with no header is exactly what a stub returns correctly.
//
// So the discriminating cases are the ones where a header says something the
// declared id does not, and they come in the two shapes a header is reached in:
// a pointer, where the word on the stack names the instance, and a value
// standing where it is, where the header is the first of the words about to be
// spent. Getting the second one's width wrong reads a member or reads off the
// end, and neither is the id below.
func TestTypeOfCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Parts concatenated into a fresh program. These share a prefix, and
	// appending onto a shared slice would have each one overwrite the tail of the
	// one before it.
	program := func(parts ...[]operation) []operation {
		var operations []operation
		for _, part := range parts {
			operations = append(operations, part...)
		}
		return operations
	}

	// One child instance on the heap, which is the only value here whose header
	// says something no id in the program does.
	newChild := program([]operation{pushI64(1), pushI64(0)},
		pushTypeID(fixtureChildShort), []operation{OpNew})

	for _, test := range []struct {
		name       string
		operations []operation
		shortID    uint64
	}{
		// A primitive has nowhere to keep a header and is never anything but what
		// it was written as, so the id goes through untouched. This is the case a
		// stub is right about, and it is here to hold the branch rather than to
		// discriminate.
		{"a primitive is the type it was named by",
			program([]operation{pushI64(7)}, pushTypeID(intShort)), intShort},

		// The pointer shape. The instance was made as the class and the id it is
		// read through is a pointer to the abstract above it, so the two words
		// differ and the header is the only place the answer is written down.
		{"a pointer answers with what the instance was made as",
			program(newChild, pushTypeID(fixtureAbstractPointerShort)),
			fixtureChildShort},

		// The value shape, the same instance loaded off the heap so that its
		// words — header first — are the ones standing. The id it is read through
		// is a different class of the same width, which is a thing no program
		// would write and is the point: it leaves the header as the only word
		// that could produce the answer, and it holds the read to starting at the
		// front of the value rather than at the top of the stack.
		{"a value standing on the stack answers with its own header",
			program(newChild, pushTypeID(fixtureChildShort),
				[]operation{OpLoad}, pushTypeID(fixtureClassShort)),
			fixtureChildShort},

		// The pointer shape again under the one id that is not a pointer. An
		// interface is laid out nowhere, so the word standing here names an
		// instance for the same reason a pointer's does — but it names it by
		// being an interface rather than by pointing at one, and nothing about
		// what the interface declares says so. Which is why this is a case of
		// its own: a runtime that asked what the id points at would find an
		// interface points at nothing and answer with the id it was given.
		{"an interface answers with what the instance was made as",
			program(newChild, pushTypeID(fixtureInterfaceShort)),
			fixtureChildShort},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=0",
				// The three words the answer is. Whatever the value under the id
				// was, it is gone: an id is spent and an id is left.
				"operandCount=3",
				// And nothing on the mark stack, though two of these spent a
				// value that owned an entry. A type id is three plain words.
				"markByteCount=0",
				fmt.Sprintf("type.shortID=%d", test.shortID),
				"type.arrayLength=0",
				"type.tailLength=0",
			}, runImage(t, harness, "type",
				program(test.operations,
					[]operation{OpTypeOf, pushI64(0), OpExit})))
		})
	}
}

// newFixtureInstance is the allocation of one untailed fixture class, laid down
// the way VisitExprNew lays one: how many instances, then how long each one's
// tail, then the type id over both. It leaves a pointer that owns a mark entry.
func newFixtureInstance() []operation {
	return append(
		append([]operation{pushI64(1), pushI64(0)},
			pushTypeID(fixtureClassShort)...),
		OpNew)
}

// TestNewCConformance holds an allocation to being the collector's.
//
// What OpNew leaves is a pointer, and a pointer is one word that looks like every
// other word — so nothing a program can read says whether the allocator filled in
// a header, and nothing at all says whether the block was threaded onto the sweep
// list. Both are read from the harness's side instead, the list especially: it is
// bookkeeping no instruction reaches, and an allocation that was handed out but
// never linked is one a sweep could never find to free.
func TestNewCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Allocating and then stopping without spending the pointer, so that the
	// allocation and the entry naming it are both still standing to be counted.
	t.Run("an allocation is linked and headed", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			// The pointer, and the one entry it owns.
			"operandCount=1",
			"markByteCount=8",
			// One block on the list, which is the whole claim that zam_vm_alloc
			// linked what it handed out.
			"heap.count=1",
			// White, nothing having collected.
			"heap.state=0",
			// ZAMOBJECT_INSTANCE, which is what says the allocator was told an
			// untailed class rather than a tailed one or a string.
			"heap.kind=1",
			fmt.Sprintf("heap.shortID=%d", fixtureClassShort),
			// One instance behind the pointer, which is new's first operand.
			"heap.elementCount=1",
		}, runImage(t, harness, "heap",
			append(newFixtureInstance(), pushI64(0), OpExit)))
	})

	// The other half of the same claim, and the one the user's rule turns on: an
	// instance laid out on the operand stack carries the identical header, mark
	// word and all, but is not an allocation. Its storage is the stack's to
	// reclaim, so linking it would hand the sweep a block to free that malloc
	// never returned.
	t.Run("a stack instance is not linked", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			// The instance's three words, standing in a frame slot, and the
			// value entry naming them.
			"operandCount=3",
			fmt.Sprintf("markByteCount=%d", valueEntryBytes),
			// Nothing on the list, though a markable instance exists.
			"heap.count=0",
		}, runImage(t, harness, "heap", append(
			append(pushTypeID(fixtureClassShort), OpDefaultValue),
			pushI64(0), OpExit)))
	})
}

// TestNewUnmanagedCConformance is the other side of that: an allocation of a
// type that carries no header at all.
//
// A struct, an enum and a plain primitive are the coder's own memory. None of
// them is ever marked and none of them is ever reached under another type's id,
// so none of them carries a word saying what it is — which makes the whole of
// the block the value, and the pointer handed back the one malloc returned.
// OpDelete already assumes exactly that, its free of anything unmarkable being
// a free of the address the program holds.
//
// So the claim is that nothing was reserved in front of the value. That cannot
// be read off the pointer, one address looking like another, so it is read as an
// offset instead: a word written at the pointer has to come back at the pointer,
// and a member has to lie where the layout says rather than a header further
// along. heap.count is the second half — the block is not linked onto the sweep
// list, which is what keeps the collector out of memory it did not hand out and
// is the direct contrast with TestNewCConformance's count of one.
//
// A tailed struct is the one exception, and a narrow one: it keeps its tail
// length in a word in front of its members, that length living in the type id
// the allocation was named through and nowhere in the block. So it is written per
// element, every element having a tail of its own, and the run is what tells a
// per-element write from a write over the front of the block.
func TestNewUnmanagedCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	const tailLength = 2
	const elementCount = 2
	const member = 9

	// Parts concatenated into a fresh program. Several of these share a prefix,
	// and appending onto a shared slice would have each one overwrite the tail
	// of the one before it.
	program := func(parts ...[]operation) []operation {
		var operations []operation
		for _, part := range parts {
			operations = append(operations, part...)
		}
		return operations
	}

	// The shape of the allocation goes down as new's operands rather than into
	// the type id — how many instances, then how long each one's tail — which is
	// the route VisitExprNew takes and leaves the id the plain declared type.
	newBlock := func(shortID, count, tail uint64) []operation {
		return program([]operation{pushI64(count), pushI64(tail)},
			pushTypeID(shortID), []operation{OpNew})
	}

	// A word read at whatever address is on top of the stack. Nothing turns the
	// pointer into a location because it is one already, the frame form being
	// the one with the top bit set. Int is what a word is called when what is
	// being looked at is a header no program can name.
	read := func() []operation {
		return program(pushTypeID(intShort), []operation{OpLoad})
	}

	// One element's width, asked of the type id rather than written down. The
	// tail length rides in the id, so the id is the only thing that knows the
	// stride, and this is the id route where the allocation above took the
	// operand one.
	stride := func() []operation {
		return program(pushTailedTypeID(fixtureStructShort, tailLength),
			[]operation{OpSizeOf})
	}

	// The first member written through a copy of the pointer, so that the
	// pointer itself is still standing to be read at afterwards.
	writeMember := program(
		[]operation{newUnaryOp(OpDupWord, 0)},
		pushTailedTypeID(fixtureStructShort, tailLength),
		[]operation{newUnaryOp(OpPushMember, 0), pushI64(member)},
		pushTypeID(intShort),
		[]operation{OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord})

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
	}{
		// A primitive is the smallest form of the claim: one word, nothing in
		// front of it, and a fresh block reading as the zero a fresh value of
		// any of these is — none of them names a routine to fill one in.
		{"a fresh primitive block is a zeroed word",
			program(newBlock(intShort, 1, 0), read(), []operation{OpExit}), 0},

		// And the same word written and read at the same address, which is what
		// a header the allocation reserved and the program did not know about
		// would break in one direction or the other.
		{"a word written at the pointer comes back at the pointer",
			program(newBlock(intShort, 1, 0),
				[]operation{newUnaryOp(OpDupWord, 0)}, pushTypeID(intShort),
				[]operation{pushI64(member)}, pushTypeID(intShort),
				[]operation{OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord},
				read(), []operation{OpExit}),
			member},

		// The one word a struct does carry. Nothing else could have written it:
		// how long the tail is, is in the id the allocation was named through,
		// so unless it is put there at birth it is lost when that id leaves.
		{"a tailed struct's front word holds its tail length",
			program(newBlock(fixtureStructShort, 1, tailLength), read(),
				[]operation{OpExit}),
			tailLength},

		// A run of them, read one element along. A length written over the
		// front of the block rather than per element would leave this zero,
		// and a stride of anything but the element's own width would land in
		// the wrong word of the wrong element.
		{"every element of a run holds its own tail length",
			program(newBlock(fixtureStructShort, elementCount, tailLength),
				stride(), []operation{OpAddI64}, read(), []operation{OpExit}),
			tailLength},

		// The member, read at the offset this test works out for itself rather
		// than through the selection that wrote it — a store and a load through
		// the same wrong offset agree perfectly.
		{"the member lies one word past the pointer",
			program(newBlock(fixtureStructShort, 1, tailLength), writeMember,
				[]operation{pushI64(wordByteSize), OpAddI64}, read(),
				[]operation{OpExit}),
			member},

		// And the length word left standing, which is where a member resolved
		// against the pointer instead of against the members would have put it.
		{"writing the member leaves the length word alone",
			program(newBlock(fixtureStructShort, 1, tailLength), writeMember,
				read(), []operation{OpExit}),
			tailLength},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// The read spent the pointer and the entry naming it, so what a
				// program leaves is nothing either way.
				"operandCount=0",
				"markByteCount=0",
				// Nothing on the sweep list, though a pointer to live memory was
				// handed out. That is the whole of the second claim: this block
				// is freed by the program or not at all.
				"heap.count=0",
			}, runImage(t, harness, "heap", test.operations))
		})
	}
}

// TestNewInitializerCConformance holds OpNew to filling in what it allocates.
//
// A fresh instance is filled in wherever one is made, so an allocation runs the
// same routine OpDefaultValue runs and differs only in where the instance lies.
// The two are told apart by reading a member back: an initializer that never ran
// and one that does not exist leave the same zeroed block behind, so the same
// program is run against both images and what separates them is the member.
//
// The receiver is the part actually under test. An initializer body reaches the
// instance through frame slot zero — see receiverInFrame, which compiles the
// receiver as that slot rather than as OpPushReceiver — and OpNew has to put a
// heap address there where OpDefaultValue puts a frame location. Both are
// locations, the frame form being the one with the top bit set, so a body written
// for one works over the other only if OpNew laid the slot the same way.
func TestNewInitializerCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Allocate, then select the one member and read it. The member is the only
	// thing the two images disagree about.
	body := append(
		append(newFixtureInstance(), pushTypeID(fixtureClassShort)...),
		newUnaryOp(OpPushMember, 0), OpLoad, OpExit)

	// The initializer, laid down after the body so that the body's length is
	// where it begins. It writes 7 through the member's own offset and returns
	// with nothing, which is what a routine entered as a one-word frame rather
	// than called leaves behind.
	//
	// The receiver is reached in two steps because slot zero holds the address of
	// the instance rather than the instance. OpPushVariable answers with a
	// location naming the slot, so the load is what turns that into the address a
	// member selection resolves against — which is the same two steps
	// OpPushReceiver describes for a receiver that lives in a closure. The load
	// is told int because a word is a word and the fixture carries no pointer
	// definition to name; what is being read is one word either way.
	initializer := uint64(byteCount(body))
	receiver := append([]operation{newUnaryOp(OpPushVariable, 0)},
		append(pushTypeID(intShort), OpLoad)...)

	program := append(body,
		append(
			append(receiver, pushTypeID(fixtureClassShort)...),
			append([]operation{newUnaryOp(OpPushMember, 0), pushI64(7)},
				append(pushTypeID(intShort), OpStore,
					OpPopWord, OpPopWord, OpPopWord, OpPopWord,
					OpReturnVoid)...)...)...)

	for _, test := range []struct {
		name string
		// The offset the class's initializer is recorded at, and zero for the
		// image that has none.
		initializer uint64
		want        int
	}{
		// Zeroed, which is all an allocation is without a routine to fill it.
		{"an allocation with no initializer is left zeroed", 0, 0},
		// And the value the routine wrote, which it could only have written
		// through a receiver OpNew put where the body looks for one.
		{"an allocation runs its initializer", initializer, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// Nothing left over either way. The member selection spent the
				// pointer and the entry naming it, and the initializer's own
				// frame went back with its return.
				"operandCount=0",
				"markByteCount=0",
			}, runImage(t, harness, "stop", program, test.initializer))
		})
	}
}

// TestArrayInitializerCConformance holds a fresh run to being filled in element
// by element rather than only at its front.
//
// The routine is entered once and has to fill in however many elements there
// are, which is the whole reason its frame is two words. An entry is left rather
// than run — the frame is laid, the body is jumped to, and the return lands past
// the instruction that laid it — so the instruction cannot come back for the next
// element. So slot 0 is a cursor the body walks and slot 1 is how many turns are
// left, and the repetition belongs to the compiled routine.
//
// What is under test is the two words arriving and surviving use: that slot 1
// holds the run's length and not one, that the frame was laid wide enough for
// the body to have a second slot at all, and that a frame location can be added
// to and stored back and still name a place a member resolves against.
//
// The loop writes the count itself rather than a constant, so the elements come
// out counting down. A body that ran the right number of times over a cursor
// that never moved would leave the last count in the front element and zeros
// behind it; one that moved the cursor but read the count as one would leave a
// single element written. Reading each element separately tells all three apart.
func TestArrayInitializerCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	const elementCount = fixtureRunElements
	const cursorSlot, remainingSlot = 0, 1
	stride := fixtureClassHeader + wordByteSize

	// The two slots read and written as plain words, which is what the compiler
	// does with them: a cursor is address-shaped and a count is a count, and
	// neither is a value the collector should be told of.
	load := func(slot uint64) []operation {
		return append([]operation{newUnaryOp(OpPushVariable, slot)},
			append(pushTypeID(intShort), OpLoad)...)
	}
	store := func(slot uint64, value []operation) []operation {
		ops := append([]operation{newUnaryOp(OpPushVariable, slot)},
			pushTypeID(intShort)...)
		ops = append(ops, value...)
		ops = append(ops, pushTypeID(intShort)...)
		return append(ops, OpStore,
			OpPopWord, OpPopWord, OpPopWord, OpPopWord)
	}

	// The member of whatever element the cursor names, given the count that is
	// about to be spent. The cursor is loaded rather than pushed as a location
	// because the slot holds where the element is, not the element.
	write := append(load(cursorSlot), pushTypeID(fixtureClassShort)...)
	write = append(write, newUnaryOp(OpPushMember, 0))
	write = append(write, load(remainingSlot)...)
	write = append(write, pushTypeID(intShort)...)
	write = append(write, OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord)

	// The step. The cursor moves by one element's width, asked of the type id
	// rather than written down, and the count comes down by one.
	step := store(cursorSlot, append(load(cursorSlot),
		append(pushTypeID(fixtureClassShort), OpSizeOf, OpAddI64)...))
	step = append(step, store(remainingSlot,
		append(load(remainingSlot), pushI64(1), OpSubI64))...)

	for index := range uint64(elementCount) {
		t.Run(fmt.Sprintf("element %d", index), func(t *testing.T) {
			// The run on the stack, then the one member of the element under
			// test read back out of it. The member lies one header into its own
			// element and the element lies its index of strides into the run,
			// which is the arithmetic the initializer's cursor had to walk. The
			// run itself begins past the frame main is fallen into on.
			slot := mainFirstLocalSlot +
				(index*stride+fixtureClassHeader)/wordByteSize
			body := append(
				pushArrayTypeID(fixtureClassShort, elementCount),
				OpDefaultValue)
			body = append(body, frameIntLocation(slot)...)
			body = append(body, OpLoad, OpExit)

			// The condition ahead of the body, which is the shape the compiler
			// emits and the shape that makes a count of zero mean no turns.
			// Both targets are absolute offsets, the body being what the
			// routine is laid down after.
			initializer := uint64(byteCount(body))
			done := newUnaryOp(OpJumpIfFalse, 0)
			again := newUnaryOp(OpJump, initializer)

			loop := append(load(remainingSlot), OpIsPositiveI64, done)
			loop = append(loop, write...)
			loop = append(loop, step...)
			loop = append(loop, again)
			done.operand = initializer + uint64(byteCount(loop))

			assert.Equal(t, []string{
				"result=EXIT",
				// Counting down from the length, so the front element holds
				// the length itself and the last one holds one.
				fmt.Sprintf("exitStatus=%d", elementCount-index),
				// The run stands where it was laid. The routine's frame went
				// back with its return, both of its words with it.
				fmt.Sprintf("operandCount=%d",
					elementCount*stride/wordByteSize),
				// The elements are values rather than references, so the run
				// owns one value entry naming it, and neither of the routine's
				// two words may own one: a frame location is not an address the
				// collector can follow.
				fmt.Sprintf("markByteCount=%d", valueEntryBytes),
			}, runImage(t, harness, "stop",
				append(append(body, loop...), OpReturnVoid), initializer))
		})
	}
}

// TestFreeCConformance holds a free of something the collector owns to being a
// statement about the next sweep rather than a release.
//
// The list is singly linked, so unlinking a block means walking to whatever points
// at it — and the sweep makes that walk anyway. So the block stays on the list and
// is only said to be dead, and what says the free happened at all is the state:
// dead rather than white, because white is what a fresh allocation already is and
// because a stale reference reaching a white block would bring it back.
//
// The type id here is the class's own rather than a pointer to it. OpFree reads
// the pointee off the id and marks the block dead regardless; the pointer form
// would be the same path arrived at through pointeeOf. The dispatch to
// Object.Delete is OpDelete's and is not what this test pins.
func TestFreeCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	assert.Equal(t, []string{
		"result=EXIT",
		"exitStatus=0",
		// The free took the pointer and the entry that named it, which it has
		// to: an entry outliving its word is a root the collector would start
		// from after the program gave it up.
		"operandCount=0",
		"markByteCount=0",
		// Still linked, the freeing being the sweep's to do.
		"heap.count=1",
		// ZAMMARK_STATE_DEAD, and not the white it was allocated in.
		"heap.state=3",
		// The rest of the header is untouched: a free says the block is
		// finished with and not that it was something else.
		"heap.kind=1",
		fmt.Sprintf("heap.shortID=%d", fixtureClassShort),
		"heap.elementCount=1",
	}, runImage(t, harness, "heap", append(
		append(newFixtureInstance(), pushTypeID(fixtureClassShort)...),
		OpFree, pushI64(0), OpExit)))
}

// TestFreeArrayCConformance holds a free of a run to being the same free as a
// free of one value.
//
// A run is one allocation however long it is — OpNew takes the whole of it from
// a single call at a single stride — so the pointer names the front of a block
// either way and the length in the id changes nothing about what is released.
// What the two branches pick between is which allocator the block came from,
// and that is the element's question rather than the run's.
//
// Both branches are covered because they release in ways that look nothing
// alike and only one of them leaves anything to read: a collected run is left
// on the sweep list and said to be dead, and an uncollected one is gone. The
// second is asserted by the heap being empty, which is the only evidence a
// raw free leaves behind.
func TestFreeArrayCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	const elements = 3

	// The allocation and the free of it, under the same id both times. OpFree
	// does not dispatch — the mark-dead branch is what the class elements reach
	// and the raw-free branch is what the primitive elements reach.
	freeRun := func(element uint64) []operation {
		return append(append([]operation{pushI64(1), pushI64(0)},
			append(pushArrayTypeID(element, elements), OpNew)...),
			append(pushArrayTypeID(element, elements),
				OpFree, pushI64(0), OpExit)...)
	}

	// The run the collector owns. Its elements are laid inline and the first
	// one's header is the block's, so what the free reaches is the same header
	// a single instance would have put there.
	t.Run("a run of instances is one block the sweep takes", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			"operandCount=0",
			"markByteCount=0",
			"heap.count=1",
			"heap.state=3",
			"heap.kind=1",
			fmt.Sprintf("heap.shortID=%d", fixtureChildShort),
			fmt.Sprintf("heap.elementCount=%d", elements),
		}, runImage(t, harness, "heap", freeRun(fixtureChildShort)))
	})

	// And the run that is the program's own. A primitive carries no header, so
	// there is nothing to mark and the raw free is the whole of it.
	t.Run("a run of primitives is freed whole", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			"operandCount=0",
			"markByteCount=0",
			"heap.count=0",
		}, runImage(t, harness, "heap", freeRun(intShort)))
	})
}

// newFixtureNest is the allocation of one nest class, laid down the way
// VisitExprNew lays one. What it leaves is a pointer to an instance with another
// instance inside it, which is the only allocation these fixtures have that
// holds a header below its own.
func newFixtureNest() []operation {
	return append(
		append([]operation{pushI64(1), pushI64(0)},
			pushTypeID(fixtureNestShort)...),
		OpNew)
}

// TestInlineMemberHeaderCConformance holds a class value laid out inside another
// to knowing what it lies inside.
//
// A markable value carries the same header everywhere, so an inline member has a
// mark word of its own, and what that word has to say is the thing no type can:
// this is not an allocation, and the thing holding it begins this far back. Only
// the second half is a number, and it is a number nothing on the wire states —
// the definition gives the member's offset net of the header, so the distance is
// that offset plus a header width the reader supplies. This is the only place the
// two sides are held to the same sum.
//
// It is asserted from the harness rather than from inside the program because a
// program cannot read it. A header is reached through a value rather than pushed,
// and the exit status is one byte, which the low byte of an interior mark word
// fits into by coincidence — an interior word and a stack word agree on it — so
// even a program that could read one could not tell them apart.
//
// The store is the case worth having beside the fresh value. A store is a copy,
// and the value being copied stood on the operand stack and says so, so a store
// that wrote the whole width would replace what the member's word says about its
// container with what the stack's word says about nothing.
func TestInlineMemberHeaderCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// The outer value standing in a frame slot, which is where a fresh one goes.
	const slot = mainFirstLocalSlot
	nest := append(pushTypeID(fixtureNestShort), OpDefaultValue)

	// Writing a fresh inner value over the member: the member's location, then a
	// value of the member's own type, then the store, then the location the store
	// leaves standing. The value written is itself a fresh markable instance, so
	// what it carries is the stack's answer and not the member's.
	store := append(
		append([]operation{newUnaryOp(OpPushVariable, slot)},
			append(pushTypeID(fixtureNestShort),
				newUnaryOp(OpPushMember, 0))...),
		append(append(pushTypeID(fixtureClassShort), OpDefaultValue),
			append(pushTypeID(fixtureClassShort), OpStore,
				OpPopWord, OpPopWord, OpPopWord, OpPopWord)...)...)

	for _, test := range []struct {
		name       string
		operations []operation
	}{
		{"a fresh value stamps the member it holds",
			append(nest, pushI64(0), OpExit)},
		{"a store into the member leaves the member's own word alone",
			append(append(nest, store...), pushI64(0), OpExit)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=0",
				// The outer value's five words, standing in a frame slot with
				// the value entry naming them. The store spends everything it
				// pushed.
				"operandCount=5",
				fmt.Sprintf("markByteCount=%d", valueEntryBytes),

				// The outer value is the root, and a root on the stack is the
				// one markable value that is contained by nothing: its storage
				// is the stack's, so the upvalue is neither a sweep-list link
				// nor a distance.
				"outer.kind=1",
				"outer.contained=1",
				"outer.upvalue=0",

				// The inner value, which says the same thing about not being an
				// allocation and a different thing about what holds it.
				"inner.kind=1",
				"inner.contained=1",
				fmt.Sprintf("inner.upvalue=%d", fixtureClassHeader),
				// Its own type and not its container's, which is what says the
				// stamp was worked out from the member's record rather than
				// copied down from the value it lies in.
				fmt.Sprintf("inner.shortID=%d", fixtureClassShort),

				// And the claim the distance exists to support.
				"inner.containerIsOuter=1",
			}, runImage(t, harness, "inner", test.operations))
		})
	}
}

// TestStaticInstanceHeaderCConformance holds a markable value in the static
// region to carrying the header it is read through.
//
// This is the one markable value nothing at run time stamps. A fresh value is
// stamped by the instruction that made it and an allocation by the allocator, but
// a static's storage exists before the first instruction and there is no
// initializer ordering pass to write anything into it — so the header is the
// compiler's to write, and the loader's only job is to copy it there intact.
//
// Left to the zeroes the region is otherwise given, the mark word would read as
// ZAMOBJECT_STRING, whose header is one word where a class reserved two. That is
// the failure the store below catches and the reason it is worth a case of its
// own: it does not crash and the program cannot see it, the write and the read
// being wrong by the same amount, so the member has to be read back from the
// outside at the offset the layout put it.
func TestStaticInstanceHeaderCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// The member's location, built the way any location is: the slot's address,
	// the type id saying what lies at it, and the member picked out of that type.
	member := append(
		[]operation{newBinaryOp(OpPushStatic, fixtureInstanceSlot,
			fixtureClassDeclID)},
		append(pushTypeID(fixtureClassShort), newUnaryOp(OpPushMember, 0))...)

	store := append(append(member, pushI64(42)),
		append(pushTypeID(intShort), OpStore,
			OpPopWord, OpPopWord, OpPopWord, OpPopWord)...)

	for _, test := range []struct {
		name       string
		operations []operation
		want       uint64
	}{
		// The header on its own, which is the loader's copy and nothing else:
		// this program never reaches the slot.
		{"the region arrives holding the header the compiler wrote",
			[]operation{pushI64(0), OpExit}, 0},
		// And the header being used. A slot stamped as a string resolves this
		// member one word early, so the 42 would land in the object header and
		// this read would come back zero.
		{"a member is resolved through it", append(store, pushI64(0), OpExit), 42},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=0",
				"operandCount=0",
				"markByteCount=0",

				"static.kind=1",
				"static.contained=1",
				"static.upvalue=0",
				fmt.Sprintf("static.shortID=%d", fixtureClassShort),
				"static.elementCount=1",

				fmt.Sprintf("static.member=%d", test.want),
			}, runImage(t, harness, "static", test.operations))
		})
	}
}

// TestLoadRestampsRootCConformance holds a copy of an allocation to not claiming
// to be one.
//
// A load is a memcpy of the value's whole width, header and all, so the copy
// arrives holding whatever the original's mark word said — and for an allocation
// that is the sweep-list link, with the contained bit clear. Left alone, the copy
// on the stack would be a second value saying it is that block: deletable, and a
// free of memory the copy does not own.
//
// The mark word is read back as an int, which is the one interior word a program
// can reach. What separates the two answers is the contained bit, and it lies in
// the low byte along with the kind, so a status of one byte is enough here where
// it is not enough for a back-distance.
func TestLoadRestampsRootCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// The pointer OpNew leaves, and the slot above it the copy lands in.
	const pointerSlot = mainFirstLocalSlot
	const copySlot = pointerSlot + 1

	// The pointer read out of its slot, then the whole instance read through it.
	// The pointer is loaded as an int because a word is a word and these fixtures
	// carry no pointer definition to name — see TestNewInitializerCConformance,
	// which reaches a receiver the same way.
	dereference := append(
		append([]operation{newUnaryOp(OpPushVariable, pointerSlot)},
			append(pushTypeID(intShort), OpLoad)...),
		append(pushTypeID(fixtureNestShort), OpDereference)...)

	program := append(append(newFixtureNest(), dereference...),
		append(frameIntLocation(copySlot), OpLoad, OpExit)...)

	assert.Equal(t, []string{
		"result=EXIT",
		// White, ZAMOBJECT_INSTANCE and contained, which packs to 36 — the same
		// word OpDefaultValue writes, because a copy on the stack and a fresh
		// value on the stack are in the same place and there is nothing else for
		// either to say. The allocation's own word would have left a 4 here, the
		// contained bit being the whole difference.
		"exitStatus=36",
		// The pointer and the copy's five words. Nothing spends either: the
		// pointer stands in its slot and the copy stands in the next.
		"operandCount=6",
		// The pointer's own entry, which OpNew gave it and nothing here takes,
		// and the value entry the copy owns, a class standing on the stack.
		fmt.Sprintf("markByteCount=%d", wordByteSize+valueEntryBytes),
	}, runImage(t, harness, "stop", program))
}

// TestArrayElementHeaderCConformance holds a run of markable values behind one
// pointer to the shape the user settled on: the front element is the array.
//
// It carries how many there are, and it is what the allocator handed out, so the
// elements behind it are its interior and each says how far back it lies. That is
// the whole of the design, and it means the length is reachable from any element
// by one step to the front — which is the only way to reach it, a member record
// carrying no array length and an interior element carrying a count of one.
//
// The distance is to the front and not to the element before, which matters for
// nothing until an element is indexed and matters then: a walk to the container
// is one step from anywhere in the run rather than as many steps as the index.
//
// All three places a run can lie are run, because what the front element's mark
// word says is the one thing that differs between them. An allocation is
// contained by nothing and its upvalue is the sweep-list link; a run standing on
// the stack is contained and its upvalue is zero; a run laid inline as a member
// is contained by the class holding it and says how far back that lies. The
// elements behind the front are identical in all three, which is the point of
// the distance being to the front — the runtime stamps them from the definition
// without knowing which case it is in.
func TestArrayElementHeaderCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Three elements, which is the smallest run where a distance to the front and
	// a distance to the element before are different numbers.
	const elementCount = 3
	stride := fixtureClassHeader + wordByteSize

	// What each element past the front says. The distance is its own offset from
	// the front, so the last element's is twice the stride rather than one — a
	// stamp that walked element to element would put a single stride in both.
	elements := func() []string {
		var lines []string
		for index := uint64(1); index < elementCount; index++ {
			lines = append(lines,
				fmt.Sprintf("element.%d.contained=1", index),
				fmt.Sprintf("element.%d.upvalue=%d", index, index*stride),
				fmt.Sprintf("element.%d.elementCount=1", index),
				fmt.Sprintf("element.%d.containerIsFirst=1", index))
		}
		return lines
	}

	t.Run("a fresh array on the stack", func(t *testing.T) {
		assert.Equal(t, append([]string{
			"result=EXIT",
			"exitStatus=0",
			// Three elements of three words each, standing in a frame slot
			// with one value entry naming the run.
			fmt.Sprintf("operandCount=%d", elementCount*3),
			fmt.Sprintf("markByteCount=%d", valueEntryBytes),
			// The front element answers for the run.
			"array.count=3",
			// And is contained, its storage being the stack's rather than the
			// collector's.
			"array.contained=1",
		}, elements()...), runImage(t, harness, "array",
			append(append(pushArrayTypeID(fixtureClassShort, elementCount),
				OpDefaultValue), pushI64(0), OpExit)))
	})

	// The same run allocated, the count given as new's first operand. It is the
	// route the length was written into the type for the stack case, and the two
	// lay down the same flat run at the same stride — which is why OpNew takes
	// their product rather than treating them as different kinds of array.
	t.Run("a fresh array on the heap", func(t *testing.T) {
		assert.Equal(t, append([]string{
			"result=EXIT",
			"exitStatus=0",
			// The pointer, and the entry it owns.
			"operandCount=1",
			"markByteCount=8",
			"array.count=3",
			// Not contained, this being the one markable value that is nothing's
			// interior and whose upvalue is the sweep-list link.
			"array.contained=0",
		}, elements()...), runImage(t, harness, "heaparray",
			append(append([]operation{pushI64(elementCount), pushI64(0)},
				append(pushTypeID(fixtureClassShort), OpNew)...),
				pushI64(0), OpExit)))
	})

	// The same allocation packed out of values that already existed rather than
	// made from nothing, which is the one route where the headers being right is
	// not a matter of what was written but of what was overwritten: every element
	// arrived carrying a header about where it stood on the stack.
	t.Run("a run packed off the stack", func(t *testing.T) {
		var packed []operation
		for range elementCount {
			packed = append(packed,
				append(pushTypeID(fixtureClassShort), OpDefaultValue)...)
		}
		packed = append(packed, pushTypeID(fixtureClassShort)...)
		packed = append(packed, newUnaryOp(OpNewArray, elementCount))

		assert.Equal(t, append([]string{
			"result=EXIT",
			"exitStatus=0",
			"operandCount=1",
			"markByteCount=8",
			"array.count=3",
			// Not contained, for the reason the allocated run above is not: the
			// front element's word is the allocation's own, and the copy that
			// landed on it said the opposite.
			"array.contained=0",
		}, elements()...), runImage(t, harness, "heaparray",
			append(packed, pushI64(0), OpExit)))
	})

	// The third place the same run appears: as a member laid inline in a class
	// rather than as a value of its own. Nothing is pushed as an array here — the
	// type id names one class — so the length comes from the member record and
	// from nowhere else, which is what makes this the case the record's length is
	// under test in.
	t.Run("a run laid inline as a member", func(t *testing.T) {
		assert.Equal(t, append([]string{
			"result=EXIT",
			"exitStatus=0",
			// The container's own header and the whole run behind it.
			fmt.Sprintf("operandCount=%d",
				fixtureClassHeader/wordByteSize+elementCount*3),
			fmt.Sprintf("markByteCount=%d", valueEntryBytes),
			// The front of the run lies one header into the container, the member
			// offsets being net of it.
			fmt.Sprintf("member.upvalue=%d", fixtureClassHeader),
			"member.containerIsOuter=1",
			"array.count=3",
			"array.contained=1",
		}, elements()...), runImage(t, harness, "memberarray",
			append(append(pushTypeID(fixtureNestRunShort), OpDefaultValue),
				pushI64(0), OpExit)))
	})
}

// TestNewArrayCConformance covers the move OpNewArray is, which is the one
// thing OpNew never does: the elements exist before the allocation does.
//
// That makes the strides the whole of it. An element stands on the operand
// stack on a whole number of words and lies in the allocation on exactly its own
// bytes, so what is laid down is not a copy of the run but a run of copies —
// and the two only ever disagree for a type narrower than a word, which is why a
// char is what the packing is read through. Anything else would pass on either
// stride.
//
// The count is an operand rather than a length in the type id, the elements
// having been counted by whoever pushed them. Zero of them is the case that
// actually happens most, every variadic call with nothing to pack compiling to
// it, and it is a null rather than an empty block: how many arrived is not
// passed with them, so there is nothing a callee may read through that pointer.
//
// What the elements' own headers end up saying is [TestArrayElementHeaderCConformance]'s
// claim and not this one. Here it is only that the values moved.
func TestNewArrayCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// The two widths this turns on, asked of the same routine the compiler lays
	// the heap stride out of rather than written down.
	charBytes, _ := primitiveByteInfo(syntax.TypeKindChar)

	program := func(parts ...[]operation) []operation {
		var operations []operation
		for _, part := range parts {
			operations = append(operations, part...)
		}
		return operations
	}

	// One element read out of the block, the pointer being a location already.
	// The offset is worked out here rather than through an index, there being no
	// instruction that indexes and nothing to cross-check it against if there
	// were.
	read := func(offset, shortID uint64) []operation {
		return program([]operation{pushI64(offset), OpAddI64},
			pushTypeID(shortID), []operation{OpLoad})
	}

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
		markBytes  int
	}{
		// The elements land in the order they were pushed, front of the run
		// being bottom of the stack. A copy that walked the stack downward
		// would answer 10 here.
		{"the elements land in the order they were pushed",
			program([]operation{pushI64(10), pushI64(20), pushI64(30)},
				pushTypeID(intShort),
				[]operation{newUnaryOp(OpNewArray, 3)},
				read(2*wordByteSize, intShort), []operation{OpExit}),
			30, 0},

		// The same three at a width the stack cannot hold them at. Copying the
		// run whole rather than element by element would leave the third char
		// eight bytes along and this read would land in the second's padding.
		{"a narrower element packs at its own width",
			program([]operation{pushI64('a'), pushI64('b'), pushI64('c')},
				pushTypeID(charShort),
				[]operation{newUnaryOp(OpNewArray, 3)},
				read(2*charBytes, charShort), []operation{OpExit}),
			'c', 0},

		// Nothing packed is a null, which exits as a zero like any other word.
		// A run of ints is the program's own memory, so its null owns no entry
		// any more than its address would have.
		{"an empty array is a null",
			program(pushTypeID(intShort),
				[]operation{newUnaryOp(OpNewArray, 0), OpExit}),
			0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				"operandCount=0",
				fmt.Sprintf("markByteCount=%d", test.markBytes),
				// A run of primitives is the program's own memory and is never
				// linked, which is the same answer TestNewUnmanagedCConformance
				// gets for the same types allocated from nothing.
				"heap.count=0",
			}, runImage(t, harness, "heap", test.operations))
		})
	}

	// The pointer owns no entry. A run of ints is the program's own memory,
	// which the collector never follows, so the address leading to it is not
	// one it has to visit.
	t.Run("a pointer to the program's own memory owns no entry", func(t *testing.T) {
		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=9",
			"operandCount=1",
			"markByteCount=0",
		}, runImage(t, harness, "mark",
			program([]operation{pushI64(10), pushI64(20)},
				pushTypeID(intShort),
				[]operation{newUnaryOp(OpNewArray, 2), pushI64(9), OpExit})))
	})

	// And a markable element, which is the case the copy has to carry more than
	// one word of. The member is written into the second instance while it
	// stands on the stack and read back out of the block afterwards, so what is
	// asserted is the value's own bytes moving rather than a fresh instance
	// being made where it used to be.
	t.Run("a markable element's value comes with it", func(t *testing.T) {
		const second = mainFirstLocalSlot + 3
		stride := fixtureClassHeader + wordByteSize

		instance := append(pushTypeID(fixtureClassShort), OpDefaultValue)

		store := program(
			[]operation{newUnaryOp(OpPushVariable,
				second+fixtureClassHeader/wordByteSize)},
			pushTypeID(intShort), []operation{pushI64(7)},
			pushTypeID(intShort),
			[]operation{OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord})

		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=7",
			"operandCount=0",
			"markByteCount=0",
			// The one block, headed for the whole run rather than for its front
			// element: two instances behind a pointer the allocator handed out.
			"heap.count=1",
			"heap.state=0",
			"heap.kind=1",
			fmt.Sprintf("heap.shortID=%d", fixtureClassShort),
			"heap.elementCount=2",
		}, runImage(t, harness, "heap",
			program(instance, instance, store,
				pushTypeID(fixtureClassShort),
				[]operation{newUnaryOp(OpNewArray, 2)},
				read(stride+fixtureClassHeader, intShort),
				[]operation{OpExit})))
	})
}

// TestVirtualMethodCConformance holds OpPushMethod to resolving its index
// against the class an instance actually is rather than against the type the
// source was able to write.
//
// The index is the same number on both records — a base's methods are laid out
// ahead of the ones a declaration adds and an override takes the slot it
// inherited, which is flattenedInstanceFields and renumberInstanceFields — so
// what a dispatch picks is never the slot and always the table. That is what
// makes the abstract's record the right control: its slot holds no body, offset
// zero being where the program begins, so a selection resolved against the type
// it was written as does not quietly call something else but dies.
//
// Both records are read for two words and the test asserts both. The
// instruction is read by running it. The signature is read off the closure's
// own header, which is where the method's typeListID lands, and the two fixtures
// are given different function types for no other reason — an instruction is
// one number and could be arrived at by luck in a way a pair cannot.
func TestVirtualMethodCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// ZAMOBJECT_REFERENCE_METHOD_CLOSURE, which a receiver held as an address
	// earns: what the entry holds is the one word a pointer is and the instance
	// lives elsewhere.
	const referenceMethodClosure = 4

	// A child instance on the heap, which is the only shape a virtually typed
	// receiver comes in. The header saying what it actually is lies in the
	// allocation, and a pointer is what the source had to be holding for the
	// declared type to be able to disagree with it in the first place.
	instance := append([]operation{pushI64(1), pushI64(0)},
		append(pushTypeID(fixtureChildShort), OpNew)...)

	// The selection, told which type the receiver was written as. That id is the
	// whole variable here: the same allocation is reached either way and only
	// what the runtime is allowed to assume about it changes.
	selectOn := func(pointer uint64) []operation {
		return append(append(slices.Clone(instance), pushTypeID(pointer)...),
			newUnaryOp(OpPushMethod, 0))
	}

	// Every program lays the child's body after itself and reports where it
	// begins, so the record a dispatch finds names a real routine whether the
	// program goes there or not. It exits with a number nothing else in the
	// image produces, which is what makes reaching it readable from outside.
	withBody := func(operations []operation) ([]operation, uint64) {
		return append(operations, pushI64(7), OpExit),
			uint64(byteCount(operations))
	}

	for _, test := range []struct {
		name    string
		pointer uint64
	}{
		// The claim. The receiver is written as a pointer to the abstract, whose
		// slot 0 has no body at all, so the body that runs is one only the
		// instance's own header could have led to.
		{"an abstract-typed receiver reaches the class's body",
			fixtureAbstractPointerShort},

		// And the control that says the resolution did not change what a
		// directly typed selection already did. Nothing is virtual here — the
		// declared type is the class — so this is the same body reached the way
		// it was always reached, and the two agreeing is what makes the first
		// case a dispatch rather than a second mechanism.
		{"a class-typed receiver reaches the same body",
			fixtureChildPointerShort},
	} {
		t.Run(test.name, func(t *testing.T) {
			program, body := withBody(
				append(selectOn(test.pointer), newUnaryOp(OpCallMethod, 1)))

			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=7",
				// The closure, standing in the slot the frame keeps for it.
				// OpCallMethod does not spend it: the operands already are the
				// new scope, so it stays where the body reads its receiver from.
				"operandCount=1",
				// That closure's entry, and the call's own — the return offset
				// and the frame base, which no return came back to take.
				"markByteCount=32",
			}, runImage(t, harness, "stop", program, 0, body))
		})
	}

	// The other word off the same record, read where a call cannot reach it. A
	// closure's header holds the function type of the method it was made from,
	// so this says which of the two records the instruction beside it came out
	// of rather than only that some body ran.
	t.Run("the closure carries the class's signature", func(t *testing.T) {
		program, body := withBody(append(
			selectOn(fixtureAbstractPointerShort), pushI64(0), OpExit))

		assert.Equal(t, []string{
			"result=EXIT",
			"exitStatus=0",
			// The closure, and the entry it owns. The receiver went into it.
			"operandCount=1",
			"markByteCount=8",
			// The instance and the closure, both the allocator's.
			"heap.count=2",
			"heap.state=0",
			fmt.Sprintf("heap.kind=%d", referenceMethodClosure),
			// The child's signature and not the abstract's, which is the whole
			// of the claim: the two records differ here and nowhere a closure
			// could have got this from by accident.
			fmt.Sprintf("heap.shortID=%d", fixtureChildParamShort),
			"heap.elementCount=1",
		}, runImage(t, harness, "heap", program, 0, body))
	})

	// A null named no instance, so there is no header to dispatch on and nothing
	// to fall back to — the type the receiver was written as is exactly the one
	// that cannot answer. This is the deref the selection asked for and it is
	// refused the way every other deref refuses one.
	t.Run("a null receiver is refused", func(t *testing.T) {
		program, body := withBody(append(
			append([]operation{OpPushNull},
				pushTypeID(fixtureAbstractPointerShort)...),
			newUnaryOp(OpPushMethod, 0), pushI64(0), OpExit))

		out, err := exec.Command(harness, "stop",
			writeImage(t, program, 0, body)).CombinedOutput()

		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, "a method was selected on a null")
		assert.Equal(t, 67, exit.ExitCode())
		assert.Equal(t, dieOutput("selected a method of a null pointer\n"), string(out))
	})
}

// TestGenericMethodCConformance holds a selection on an instantiation to taking
// its two halves from the two places a generic declaration keeps them.
//
// A generic is the one kind whose method record is split. The body is compiled
// once for the whole template and is held above the entries, since C[int].f and
// C[string].f run the same instructions; the signature is not, since they are
// different function types. So the instruction comes off the declaration and the
// type comes off the instantiation, and a reader that took both from one place
// would be right about one of them.
//
// Which is what the fixture is arranged to expose. One body and two entries
// naming two different function types, asked through the two short ids that
// differ in nothing but their generic half. Both cases run the same routine —
// that is the half held constant — and the closure's header is read for the half
// that is not.
func TestGenericMethodCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// ZAMOBJECT_VALUE_METHOD_CLOSURE. The receiver is a class value with no
	// home of its own, so the closure becomes its home, which is the shape a
	// selection on a value leaves rather than anything generic about this.
	const valueMethodClosure = 5

	// The receiver, as wide as the entries say an instance is and holding
	// nothing. It is never read: the instantiation is named by the type id
	// beneath the instruction, and a generic class is not virtually typed, so
	// nothing here is dispatched on what the words happen to be.
	receiver := make([]operation, 0,
		(fixtureClassHeader+wordByteSize)/wordByteSize)
	for word := 0; word < cap(receiver); word++ {
		receiver = append(receiver, pushI64(0))
	}

	selectOn := func(genericID uint64) []operation {
		return append(append(slices.Clone(receiver),
			pushTypeID(fixtureGenericShort(genericID))...),
			newUnaryOp(OpPushMethod, 0))
	}

	// The shared body, laid after each program and reporting where it begins.
	// It exits with a number nothing else in the image produces, which is what
	// makes reaching it readable from outside.
	withBody := func(operations []operation) ([]operation, uint64) {
		return append(operations, pushI64(11), OpExit),
			uint64(byteCount(operations))
	}

	for _, test := range []struct {
		name       string
		genericID  uint64
		methodType uint64
	}{
		{"the first instantiation names its own signature", 0,
			fixtureParentParamShort},
		{"and the second names a different one", 1,
			fixtureChildReturnShort},
	} {
		t.Run(test.name, func(t *testing.T) {
			program, body := withBody(
				append(selectOn(test.genericID), pushI64(0), OpExit))

			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=0",
				// The closure, and the entry it owns. The receiver's words went
				// into it and were taken off the stack.
				"operandCount=1",
				"markByteCount=8",
				"heap.count=1",
				"heap.state=0",
				fmt.Sprintf("heap.kind=%d", valueMethodClosure),
				// The claim: the signature is the one this instantiation's
				// entry names, and the other instantiation names the other.
				fmt.Sprintf("heap.shortID=%d", test.methodType),
				"heap.elementCount=1",
			}, runImage(t, harness, "heap", program, 0, 0, 0, body))
		})
	}

	// And the half that does not vary, which is what makes the pair above a
	// split record rather than two unrelated ones: the same body runs whichever
	// instantiation was selected on, because there is only one of it.
	t.Run("both instantiations run the one body", func(t *testing.T) {
		for _, genericID := range []uint64{0, 1} {
			program, body := withBody(
				append(selectOn(genericID), newUnaryOp(OpCallMethod, 1)))

			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=11",
				"operandCount=1",
				// The closure's entry, and the call's own — the return offset
				// and the frame base, which no return came back to take.
				"markByteCount=32",
			}, runImage(t, harness, "stop", program, 0, 0, 0, body),
				"instantiation %d", genericID)
		}
	})
}

// TestInterfaceMethodCConformance holds OpPushInterfaceMethod to translating
// its index through the dispatch table of the class the instance turns out to
// be, rather than reading it as a slot the way OpPushMethod does.
//
// This is the one selection where the number changes. An abstract's methods are
// laid out ahead of its subclass's and an override takes the slot it inherited,
// so a virtual dispatch picks the table and never the slot — but an interface
// numbers its methods its own way and nothing about a class was arranged to
// agree with it. The class's dispatch table is what says which of its own slots
// an interface method names on it, and that is a map rather than an identity.
//
// The fixture is built so that identity would be visibly wrong: the interface's
// method 0 translates to the class's method 1, and the class's method 0 holds a
// real body that exits differently. An implementation that used the operand
// unchanged therefore runs something and reports 7, rather than dying somewhere
// that could be mistaken for the table being consulted.
func TestInterfaceMethodCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// ZAMOBJECT_REFERENCE_METHOD_CLOSURE. An interface value is one word
	// naming an instance that lives elsewhere, which is the only form this
	// receiver comes in — unlike OpPushMethod, there is no value case to pick
	// between.
	const referenceMethodClosure = 4

	// An instance of the given class on the heap, which an interface-typed word
	// is always the address of. Which class is the variable: the child
	// implements the interface and the plain class does not.
	instance := func(class uint64) []operation {
		return append([]operation{pushI64(1), pushI64(0)},
			append(pushTypeID(class), OpNew)...)
	}

	// The selection, told the receiver is the interface. That id is read twice
	// over and for different things — it is half the dispatch key, and it is
	// also what the closure records its receiver as — which is why it comes up
	// the stack rather than sitting in an operand: inside a template the
	// instantiation is not known until the receiver says.
	selectOn := func(class, method uint64) []operation {
		return append(
			append(instance(class), pushTypeID(fixtureInterfaceShort)...),
			newUnaryOp(OpPushInterfaceMethod, method))
	}

	// Both of the child's bodies laid after the program, each exiting with a
	// number the other does not. Slot 0 is the abstract's override and slot 1 is
	// the interface's, and slot 0 being reachable is the point: the wrong answer
	// here is a program that runs, not one that dies.
	withBodies := func(operations []operation) ([]operation, uint64, uint64) {
		override := []operation{pushI64(7), OpExit}
		implementation := []operation{pushI64(9), OpExit}
		return append(append(operations, override...), implementation...),
			uint64(byteCount(operations)),
			uint64(byteCount(operations) + byteCount(override))
	}

	t.Run("the interface's index reaches the class's own slot", func(t *testing.T) {
		program, override, implementation := withBodies(append(
			selectOn(fixtureChildShort, fixtureInterfaceMethodIndex),
			newUnaryOp(OpCallMethod, 1)))

		assert.Equal(t, []string{
			"result=EXIT",
			// The interface's method and not the class's slot of the same
			// number, which is the whole of the claim.
			"exitStatus=9",
			// The closure, standing in the slot the frame keeps for it, exactly
			// as a virtual call leaves it.
			"operandCount=1",
			// Its entry, and the call's return offset and frame base.
			"markByteCount=32",
		}, runImage(t, harness, "stop", program, 0, override, implementation))
	})

	// The other word off the record the translation landed on. A closure's
	// header holds the function type of the method it was made from, and the
	// child's two methods are given different signatures for no other reason, so
	// this says which slot was read rather than only that some body ran.
	t.Run("the closure carries the translated method's signature",
		func(t *testing.T) {
			program, override, implementation := withBodies(append(
				selectOn(fixtureChildShort, fixtureInterfaceMethodIndex),
				pushI64(0), OpExit))

			assert.Equal(t, []string{
				"result=EXIT",
				"exitStatus=0",
				// The closure, and the entry it owns. The receiver went into it.
				"operandCount=1",
				"markByteCount=8",
				// The instance and the closure, both the allocator's.
				"heap.count=2",
				"heap.state=0",
				fmt.Sprintf("heap.kind=%d", referenceMethodClosure),
				// The signature of slot 1 and not of slot 0, which is the same
				// claim the exit status made, read where no body ran at all.
				fmt.Sprintf("heap.shortID=%d", fixtureChildReturnShort),
				"heap.elementCount=1",
			}, runImage(t, harness, "heap", program, 0, override,
				implementation))
		})

	// A class that implements nothing. Its dispatch table is built over no keys
	// at all, which is the one absence a perfect hash answers honestly — a table
	// with buckets sorts a key it never saw into some other key's slot — so this
	// is where the refusal can be held to a message rather than only to not
	// being reached.
	t.Run("a type implementing the interface is required", func(t *testing.T) {
		program, override, implementation := withBodies(append(
			selectOn(fixtureClassShort, fixtureInterfaceMethodIndex),
			pushI64(0), OpExit))

		out, err := exec.Command(harness, "stop",
			writeImage(t, program, 0, override, implementation)).CombinedOutput()

		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, "a method was found on a non-implementor")
		assert.Equal(t, 67, exit.ExitCode())
		assert.Equal(t, dieOutput(fmt.Sprintf(
			"type %d implements no method %d of interface %d\n",
			fixtureClassShort, fixtureInterfaceMethodIndex,
			fixtureInterfaceShort)), string(out))
	})

	// A null named no instance, so there is no header to read a class off and
	// the interface — which is laid out nowhere and holds no method bodies —
	// is exactly the type that cannot answer in its place.
	t.Run("a null receiver is refused", func(t *testing.T) {
		program, override, implementation := withBodies(append(
			append([]operation{OpPushNull},
				pushTypeID(fixtureInterfaceShort)...),
			newUnaryOp(OpPushInterfaceMethod, fixtureInterfaceMethodIndex),
			pushI64(0), OpExit))

		out, err := exec.Command(harness, "stop",
			writeImage(t, program, 0, override, implementation)).CombinedOutput()

		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, "a method was selected on a null")
		assert.Equal(t, 67, exit.ExitCode())
		assert.Equal(t, dieOutput("selected a method of a null pointer\n"), string(out))
	})
}

// TestTailedStructHeaderCConformance is TestInstanceHeaderCConformance for the
// one instance whose header no instance carries.
//
// Every other header is read off the mark word in front of the value, so an
// instance reached under a base's id still says what it actually is. A struct
// has no mark word — it is the coder's own memory and is never collected — so
// the runtime has to work this one out from the definition instead: a struct
// whose instanceTailByteSize is nonzero keeps a tail length in front of its
// members, and how wide that is, is a number neither side reads from the other.
// The compiler holds it as instanceHeaderByteSize and the runtime as
// ZAMTAILED_STRUCT_HEADER_BYTES.
//
// A read alone would not catch a disagreement, since a store and a load through
// the same wrong offset agree perfectly. So the member is written through
// OpPushMember and then read back twice: once through OpPushMember again, and
// once at a frame offset this test works out for itself. The second is what
// makes the gap visible — if the runtime added no header, the 7 would be in the
// header word instead of past it.
func TestTailedStructHeaderCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Two tail elements, so the instance is wider than its fixed part and the
	// tail length in the id is doing something. A fresh one stands in the first
	// slot main's frame leaves free.
	const slot = mainFirstLocalSlot
	const tailLength = 2
	structType := func() []operation {
		return pushTailedTypeID(fixtureStructShort, tailLength)
	}
	instance := append(structType(), OpDefaultValue)

	// The member's location, reached the way a selection reaches it, which is
	// the path that goes through the runtime's own idea of the header.
	member := func() []operation {
		return tailedStructMember(slot, tailLength, 0)
	}

	// A word of the instance read as an int, which is how the header is looked
	// at from a program with no way to name one.
	word := func(offset uint64) []operation {
		return append(frameIntLocation(slot+offset), OpLoad)
	}

	store := append(append(member(), pushI64(7)),
		append(pushTypeID(intShort),
			OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord)...)

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
	}{
		// The read side, which says only that the write and the read agree.
		{"a member comes back out of the instance", append(
			append(append(instance, store...), member()...),
			OpLoad, OpExit), 7},

		// The header word, which is where a member offset added to nothing would
		// have put the 7. It holds the tail length instead, written by
		// OpDefaultValue: how long a tail is lives in the type id the instance
		// was made through and nowhere in the instance, so unless it is put there
		// at birth it is lost as soon as that id leaves the stack.
		{"instantiating writes the tail length in front of the members", append(
			append(append(instance, store...), word(0)...), OpExit), tailLength},

		// And the member at the offset this test worked out, which is the same
		// word the two OpPushMembers above reached. The two agreeing is the
		// header widths agreeing.
		{"the member lies one word past the pointer", append(
			append(append(instance, store...), word(1)...), OpExit), 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// The instance is what each of these leaves standing: the header
				// word, its one member, and two tail elements.
				"operandCount=4",
				// A struct owns no mark entry, being the one declared type the
				// collector never looks at.
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestTailedStructRunHeaderCConformance is TestTailedStructHeaderCConformance
// asked of a run of them, which is where the header stops being one word and
// becomes a stride.
//
// The tail length is the one word of a struct that nothing can recover once the
// id it came in on leaves the stack, so a run of tailed structs needs it written
// as many times as there are elements. Two things can go wrong and only a run
// shows either: writing it once, which leaves every element but the first saying
// its tail is empty, and writing it at the fixed part's width, which walks the
// run at the wrong stride and puts the later headers inside the element before.
//
// Both are caught by reading words at offsets this test works out for itself,
// since a run has no member path to the element past the first — the one thing
// an index into it would be resolved by is the very stride under test.
func TestTailedStructRunHeaderCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Three elements of two tail words each. Three because a stride read off
	// the fixed part lands on the right word for the second element by luck —
	// a two-word header-and-member against a four-word element puts the second
	// write at offset 4 either way — and only the third tells them apart. Two
	// tail words for the same reason: a one-word tail would make the two
	// strides equal and the test vacuous.
	const slot = mainFirstLocalSlot
	const tailLength = 2
	const elements = 3
	const elementWords = 1 + 1 + tailLength

	run := append([]operation{
		pushI64(fixtureStructShort << 32),
		pushI64(elements),
		pushI64(tailLength)}, OpDefaultValue)

	word := func(offset uint64) []operation {
		return append(frameIntLocation(slot+offset), OpLoad)
	}

	for _, test := range []struct {
		name   string
		offset uint64
		want   int
	}{
		// The first element's header, which is the whole of what the single
		// case already held and is here as the control.
		{"the first element carries its tail length", 0, tailLength},

		// The last one's, which is the claim. Nothing but a per-element write
		// at the element's own width puts the length here.
		{"the last element carries its own", 2 * elementWords, tailLength},

		// And a word no header lies in: the first element's first tail slot,
		// which a stride taken from the fixed part would have written the
		// second header into. It is a fresh value's word like any other and
		// holds the zero OpDefaultValue left.
		{"a tail element is left as the zero it was", 2, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// The run, standing whole. A struct is laid out inline and an
				// array of them is one value, so what the load left is the only
				// thing above it.
				fmt.Sprintf("operandCount=%d", elements*elementWords),
				// A struct owns no mark entry and neither does a run of them.
				"markByteCount=0",
			}, runImage(t, harness, "stop", append(
				append(slices.Clone(run), word(test.offset)...), OpExit)))
		})
	}
}

// tailedStructMember is the location a member of a fresh tailed struct in a
// frame slot names, built the way a selection builds it: the slot, the type id
// the instance was made through, and the member's index.
func tailedStructMember(slot, tailLength, index uint64) []operation {
	return append(
		append([]operation{newUnaryOp(OpPushVariable, slot)},
			pushTailedTypeID(fixtureStructShort, tailLength)...),
		newUnaryOp(OpPushMember, index))
}

// TestTailIndexCConformance follows an index into a tail the whole way, which is
// the one member access that is not a load.
//
// A tail is laid inline at the end of the instance — the struct hack, and the
// reason a tailed type's width is in its id rather than in its definition — so
// the member names where the elements begin and not a word holding where they
// begin. Indexing it is therefore that location with the element's width added,
// which is emitted as OpAddressOf over the location: the sum needs where the run
// begins, so the type words above the location are let go of rather than loaded
// through. That is emitArrayBase in 3_operation.go, and it is the one place an
// array is read as an address instead of as the run it is.
//
// Two claims are made here and both need the write and the read to disagree
// about something to be caught. So an element is written through the member and
// then read back at a frame offset this test works out for itself: that the
// elements begin past the fixed members, and that stepping one element is one
// element wide and not one byte or one member.
func TestTailIndexCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	// Three elements, so there is an element on either side of the one written
	// and a step in the wrong direction lands in the instance rather than off
	// the end of it.
	const slot = mainFirstLocalSlot
	const tailLength = 3

	// The instance's words: the tail length, the one fixed member, then the
	// elements. What the test asserts is that the elements start at the third.
	const firstElementWord = 2

	instance := append(pushTailedTypeID(fixtureStructShort, tailLength),
		OpDefaultValue)

	// The tail member is the second, and its location is where element zero
	// lies. Adding the index scaled to bytes is the whole of the indexing, and
	// the type id above it is what makes the sum a location again.
	element := func(index uint64) []operation {
		return append(
			append(tailedStructMember(slot, tailLength, 1),
				OpAddressOf, pushI64(index*wordByteSize), OpAddI64),
			pushTypeID(intShort)...)
	}

	store := func(index, value uint64) []operation {
		return append(append(element(index), pushI64(value)),
			append(pushTypeID(intShort),
				OpStore, OpPopWord, OpPopWord, OpPopWord, OpPopWord)...)
	}

	word := func(offset uint64) []operation {
		return append(frameIntLocation(slot+offset), OpLoad)
	}

	written := append(instance, store(1, 9)...)

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
	}{
		// The read side, which says only that the two agree about where the
		// element is.
		{"an element comes back out of the tail", append(
			append(written, element(1)...), OpLoad, OpExit), 9},

		// Where the elements begin. A step that started at the pointer, or at
		// the first member, would have landed in one of these instead.
		{"the elements begin past the fixed members", append(
			append(written, word(firstElementWord+1)...), OpExit), 9},
		{"the header is not an element", append(
			append(written, word(0)...), OpExit), tailLength},
		{"the fixed member is not an element", append(
			append(written, word(1)...), OpExit), 0},

		// And the elements on either side, which are what an index scaled by
		// anything but the element's own width would have reached.
		{"element zero is left alone", append(
			append(written, word(firstElementWord)...), OpExit), 0},
		{"element two is left alone", append(
			append(written, word(firstElementWord+2)...), OpExit), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				// The header word, the one fixed member, and three elements.
				"operandCount=5",
				"markByteCount=0",
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// frameRunLocation is [frameIntLocation] over a run rather than over one int:
// the same slot, named by a type id whose array length says how many elements
// lie there. The length is the only difference between the two, which is what
// makes it the thing under test.
func frameRunLocation(slot, shortID, elementCount uint64) []operation {
	return append(
		[]operation{newUnaryOp(OpPushVariable, slot)},
		pushArrayTypeID(shortID, elementCount)...)
}

// TestArrayValueCConformance covers an array being a value: a run of elements
// that is loaded, stored, and stood on the stack whole rather than collapsing to
// a pointer at its front.
//
// This is the one property no other test would catch, because a run and its
// first element are the same address. Everything reads and writes at the right
// place either way; what differs is how much moves. So the assertion is always
// about the elements past the first — a copy that moved one word leaves those
// holding what they held before, and the exit status says which happened.
func TestArrayValueCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	const count = fixtureRunElements
	const source = mainFirstLocalSlot
	const destination = mainFirstLocalSlot + count

	// The four words a location is, left standing by a store.
	dropLocation := []operation{OpPopWord, OpPopWord, OpPopWord, OpPopWord}

	reserve := append(pushArrayTypeID(intShort, count), OpDefaultValue)

	storeWord := func(location []operation, value uint64) []operation {
		return append(append(location, pushI64(value)),
			append(pushTypeID(intShort), OpStore)...)
	}

	// The source run, filled one element at a time so that the three words hold
	// three different numbers and a copy landing at the wrong offset is visible.
	filled := append(reserve, reserve...)
	for index := range uint64(count) {
		filled = append(filled,
			append(storeWord(frameIntLocation(source+index), 10+index),
				dropLocation...)...)
	}

	// The copy itself: a location naming the whole destination run, the whole
	// source run loaded onto the stack above it, and one store.
	copied := append(filled, frameRunLocation(destination, intShort, count)...)
	copied = append(copied,
		append(frameRunLocation(source, intShort, count), OpLoad)...)
	copied = append(copied,
		append(pushArrayTypeID(intShort, count), OpStore)...)
	copied = append(copied, dropLocation...)

	// The same thing through a member record rather than a frame slot, which is
	// the other half of the claim: the length a record carries has to survive
	// OpPushMember for the member's location to name the run it is.
	//
	// The instance stands in the frame, so its own words are what the elements
	// are read back out of: the header, and then the run behind it.
	headerWords := fixtureClassHeader / wordByteSize
	memberWords := int(headerWords) + count
	memberFirstElement := mainFirstLocalSlot + headerWords

	stored := append(pushTypeID(fixtureRunShort), OpDefaultValue)
	stored = append(stored, newUnaryOp(OpPushVariable, mainFirstLocalSlot))
	stored = append(stored, append(pushTypeID(fixtureRunShort),
		newUnaryOp(OpPushMember, 0))...)
	stored = append(stored, pushI64(7), pushI64(8), pushI64(9))
	stored = append(stored,
		append(pushArrayTypeID(intShort, count), OpStore)...)
	stored = append(stored, dropLocation...)

	// Each program is a fresh slice rather than an append onto a shared one:
	// every case below extends the same prefix, and appending to it in place
	// would have each case overwrite the tail of the one before it.
	program := func(prefix []operation, tail ...operation) []operation {
		return append(slices.Clone(prefix), tail...)
	}

	read := func(slot uint64) []operation {
		return append(frameIntLocation(slot), OpLoad, OpExit)
	}

	for _, test := range []struct {
		name       string
		operations []operation
		want       int
		operands   int
		markBytes  int
	}{
		// The read side of the copy, element by element. The last two are the
		// assertion: a run that collapsed to its front would leave them zero.
		{"the first element crosses", program(copied,
			read(destination)...), 10, 2 * count, 0},
		{"the middle element crosses", program(copied,
			read(destination+1)...), 11, 2 * count, 0},
		{"the last element crosses", program(copied,
			read(destination+2)...), 12, 2 * count, 0},

		// And the source, which a store that read its words from the wrong end
		// of the stack would have disturbed.
		{"the source run is left alone", program(copied,
			read(source+2)...), 12, 2 * count, 0},

		// A member location says how wide the run is, so a store through it
		// moves all of it. The instance is the frame's, so its own words are
		// what the elements are read back out of: the header, then the run.
		{"a member location names the whole run", program(stored,
			read(memberFirstElement+2)...), 9, memberWords, valueEntryBytes},
		{"a member run is not one element wide", program(stored,
			read(memberFirstElement)...), 7, memberWords, valueEntryBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, []string{
				"result=EXIT",
				fmt.Sprintf("exitStatus=%d", test.want),
				fmt.Sprintf("operandCount=%d", test.operands),
				// The instance holding the run stands on the stack, which a
				// value entry names.
				fmt.Sprintf("markByteCount=%d", test.markBytes),
			}, runImage(t, harness, "stop", test.operations))
		})
	}
}

// TestArrayReferenceCConformance covers the entry a run of elements held as
// addresses owns.
//
// It is the one case where being an array changes what the collector is told
// rather than only how much moves. A single address owns an object entry; a run
// of them owns one array entry over the whole run, because the words arrive
// together — the type id above a location names the run, so no element is ever
// pushed on its own for an entry to be given to it as it lands.
//
// So the count alone would not say this worked. One entry is one entry whether
// it covers the run or only the address at its front, and the two differ solely
// in the kind byte and the two words that kind carries. That is what the mark
// subject reports and what is asserted here.
func TestArrayReferenceCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	const count = fixtureRunElements
	const front = mainFirstLocalSlot

	// The array entry, three words wide, and the object entry that is one.
	const objectEntryBytes, arrayEntryBytes = 8, 24
	const objectKind, arrayKind = 1, 2

	run := append(pushArrayTypeID(stringShort, count), OpDefaultValue)

	// Every program stops on a status of its own choosing rather than on
	// something read out of the run, the elements all being null and a null
	// being the same byte whichever of them was asked. What the run holds is
	// not the claim here; what owns it is.
	stop := []operation{pushI64(9), OpExit}

	// Each program is a fresh slice rather than an append onto the run above.
	// Several of these extend it, and appending to it in place would have each
	// one overwrite the tail of the one before it.
	program := func(prefix []operation, tail ...operation) []operation {
		return append(slices.Clone(prefix), tail...)
	}

	entry := func(operand, element, length uint64) []string {
		return []string{
			fmt.Sprintf("mark.kind=%d", arrayKind),
			fmt.Sprintf("mark.operand=%d", operand),
			fmt.Sprintf("mark.element=%d", element),
			fmt.Sprintf("mark.length=%d", length),
		}
	}

	for _, test := range []struct {
		name       string
		operations []operation
		operands   int
		markBytes  int
		entry      []string
	}{
		// The claim itself. Three null words standing, and one entry over them
		// that says which type they are and how many there are.
		{"a fresh run owns one entry over all of it",
			program(run, stop...), count, arrayEntryBytes,
			entry(front, stringShort, count)},

		// The control that says the array kind is not simply what every
		// reference gets. One address is one word and owes the narrower entry,
		// which carries no length because there is no run to walk.
		{"a single address still owns an object entry",
			program(append(pushTypeID(stringShort), OpDefaultValue), stop...),
			1, objectEntryBytes,
			[]string{
				fmt.Sprintf("mark.kind=%d", objectKind),
				fmt.Sprintf("mark.operand=%d", front),
			}},

		// And the control on the other side: a run of bits is a run the
		// collector has no business in, so being an array is not on its own
		// what earns an entry.
		{"a run of primitives owns nothing",
			program(append(pushArrayTypeID(intShort, count), OpDefaultValue),
				stop...),
			count, 0, nil},

		// A copy of the run is a second run in a second place, so it owes an
		// entry of its own naming where it now lies rather than where it was
		// read from. The top entry is the copy's, the source's lying beneath it.
		{"a loaded run owns an entry where the copy lies",
			program(run, append(
				append(frameRunLocation(front, stringShort, count), OpLoad),
				stop...)...),
			2 * count, 2 * arrayEntryBytes,
			entry(front+count, stringShort, count)},

		// Letting go of the run lets go of the entry. The words go from the top
		// one at a time and the entry names the front, so this is also what says
		// the front is the word it names: an entry over the last word would have
		// gone on the first pop and left nothing to find here.
		{"spending the whole run drops the entry",
			program(run, append(
				append(pushArrayTypeID(stringShort, count), OpPop),
				stop...)...),
			0, 0, nil},

		// The other half of that, which is the part a wrongly named entry would
		// pass: with part of the run still standing the entry has to still be
		// there, because what is left is reachable.
		{"a partly spent run keeps its entry",
			program(run, append([]operation{OpPopWord, OpPopWord},
				stop...)...),
			1, arrayEntryBytes, entry(front, stringShort, count)},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, append([]string{
				"result=EXIT",
				"exitStatus=9",
				fmt.Sprintf("operandCount=%d", test.operands),
				fmt.Sprintf("markByteCount=%d", test.markBytes),
			}, test.entry...),
				runImage(t, harness, "mark", test.operations))
		})
	}
}

// TestPopMarkCConformance covers the one instruction whose whole effect is on
// the mark stack.
//
// It cannot be read off an exit status the way the others can, because the word
// it leaves is the one it was given. What says it worked is the mark stack being
// a byte shorter than the pointer standing on the operand stack would otherwise
// make it, which is why this program deliberately stops without cleaning up.
func TestPopMarkCConformance(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{
		"result=EXIT",
		"exitStatus=5",
		// The static pointer, still standing and now owning nothing.
		"operandCount=1",
		// Zero rather than the eight bytes an object entry occupies, which is
		// what OpPushStatic left and what OpPopMark took.
		"markByteCount=0",
	}, runImage(t, buildRunHarness(t), "stop", []operation{
		newBinaryOp(OpPushStatic, mainStaticSlot, uint64(syntax.TypeKindInt)),
		OpPopMark, pushI64(5), OpExit,
	}))
}

// TestDivideByZeroCConformance covers the one arithmetic fault the runtime
// stops for.
//
// A shift past the width is left undefined and a float division by zero has an
// infinity to be, so this is the only case where the program is brought down.
// It exits with the panic status rather than with a status the program chose,
// which is what keeps a fault distinguishable from a program that meant to end
// with that number.
func TestDivideByZeroCConformance(t *testing.T) {
	t.Parallel()
	harness := buildRunHarness(t)

	for _, test := range []struct {
		name   string
		opcode Opcode
	}{
		{"divide", OpDivI64},
		{"modulus", OpModI64},
	} {
		t.Run(test.name, func(t *testing.T) {
			image := writeImage(t, []operation{
				pushI64(1), pushI64(0), test.opcode, OpExit,
			})

			out, err := exec.Command(harness, "stop", image).CombinedOutput()

			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "the program was not brought down")
			// ZAMEXIT_PANIC, which is the program's own fault rather than
			// ZAMEXIT_UNIMPLEMENTED, which would be the runtime's.
			assert.Equal(t, 67, exit.ExitCode())
			assert.Equal(t, dieOutput("divide by zero\n"), string(out))
		})
	}
}
