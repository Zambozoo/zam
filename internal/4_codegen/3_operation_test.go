package codegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compileOperations runs a one-file project all the way through the operations
// pass and answers the reader holding what was emitted.
func compileOperations(t *testing.T, body string) *codegenReader {
	t.Helper()

	ast, reader, typeIndices := compileTypes(t, body)
	require.NoError(t, populateOperations(ast, reader, typeIndices))
	return reader
}

// operationsFrom answers the operations laid down at a byte offset, which is how
// a routine is named: an initializer is recorded on its type as where it begins
// and the operations are one flat run, so the offset has to be walked to.
func operationsFrom(t *testing.T, reader *codegenReader, offset uint64) []operation {
	t.Helper()

	at := uint64(0)
	for index, op := range reader.operations {
		if at == offset {
			return reader.operations[index:]
		}
		require.Less(t, at, offset, "no operation begins at byte %d", offset)
		at += uint64(op.ByteCount())
	}
	require.FailNow(t, "the operations stop before byte %d", offset)
	return nil
}

// TestInstanceInitializerLoopsOverEveryElement holds the compiled initializer to
// being a loop rather than a single pass.
//
// A fresh value may be a run of elements and every one of them is filled in
// alike, but the routine is entered by laying a frame and jumping, so the
// instruction that entered it is left rather than run and cannot come back for
// the next element. The repetition is therefore the routine's, and this is what
// says the routine has it: slot 1 is how many are left, slot 0 is a cursor, and
// the body sits between a test at the top and a jump back at the bottom.
//
// What is asserted is the scaffolding and not the member stores between them.
// The stores are what every other initializer test already covers, and they are
// what moves when anything unrelated to the loop changes — whereas a swap of the
// two slots, a condition emitted behind the body, or a cursor that never moved
// would all leave the stores exactly as they are and compile clean.
func TestInstanceInitializerLoopsOverEveryElement(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    class Box {
        var int count = 7;
    }
}
`)

	require.Len(t, reader.classDefinitions, 1)
	class := reader.classDefinitions[0].(*typeDefinitionClass)
	top := class.initializerInstruction
	require.NotZero(t, top, "the class was given no initializer")

	routine := operationsFrom(t, reader, top)

	// The condition, at the top: how many are left, and a jump past everything
	// when none are. Ahead of the body rather than behind it, which is what
	// makes a count of zero mean no turns instead of one.
	head := append(loadWordOperations(remainingSlot), OpIsPositiveI64)
	assert.Equal(t, head, routine[:len(head)],
		"the routine does not open by testing how many are left")

	done, ok := routine[len(head)].(*jumpOperation)
	require.True(t, ok, "the test is not followed by a jump")
	assert.Equal(t, OpJumpIfFalse, done.opcode)

	// The step, at the bottom. The cursor moves by one element's width, asked of
	// the type id rather than written down here: an instantiation of a template
	// is as wide as its arguments make it, so a constant baked in would be the
	// wrong number for every instantiation but one.
	//
	// Both slots are read and written as ints whatever they hold. That is the
	// point of them: naming the cursor as the pointer it is would have the load
	// push a mark entry over a frame location, which is the one word that must
	// never own one.
	tail := storeWordOperations(cursorSlot,
		append(loadWordOperations(cursorSlot),
			append(pushTypeID(classShortID(reader, 0)),
				OpSizeOf, OpAddI64)...))
	tail = append(tail, storeWordOperations(remainingSlot,
		append(loadWordOperations(remainingSlot), pushI64(1), OpSubI64))...)
	tail = append(tail,
		&jumpOperation{opcode: OpJump, target: int(top)}, OpReturnVoid)

	assert.Equal(t, tail, routine[len(routine)-len(tail):],
		"the routine does not close by stepping both slots and going back")

	// The jump out lands on the return, which is the only instruction past the
	// jump back. Measured against the routine rather than written down, so an
	// instruction added to the body moves it.
	assert.Equal(t,
		int(top)+byteCount(routine)-OpReturnVoid.ByteCount(), done.target,
		"the exit does not land on the return")
}

// TestBlankVariablesCompile pins the one guarantee codegen makes about `_`
// beyond the static pass's refusal to resolve it: a method body that declares
// several blanks compiles cleanly rather than panicking when the second one
// is laid down. The scope map holds no `_` entry, so the two cannot step on
// one another, and the slots they reserve are what later `_` slots are laid
// after. Nothing is checked about the operations themselves — that is covered
// by every other declareVariable call — only that the whole run finished.
func TestBlankVariablesCompile(t *testing.T) {
	compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int _ = 1;
        var int _ = 2;
        var int x = 3;
        return;
    }
}
`)
}

// TestBlankParametersCompile pins the same guarantee for parameters, which
// reach declareVariable through declareParameters rather than
// VisitStmtVariable. Two `_` parameters would collide on the one scope entry
// if the blank were still written to the map.
func TestBlankParametersCompile(t *testing.T) {
	compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun int(int, int) f = (_, _) {
        return 0;
    }
}
`)
}

// Each any opcode has a cast or typeof shape it is the one answer to. The tests
// below assert that writing the shape in a program is enough to see the opcode
// land in the emitted operations, and that writing another shape beside it is
// not.
//
// The runtime side of these three is stubbed — the dispatch in run.c dies with
// the opcode's name — so what is being pinned here is that the compiler agrees
// with the design: a cast to any boxes, a cast from any unboxes, and @ on an
// any reads the stored id rather than the static one.

// contains reports whether a bare opcode was emitted anywhere in the operations
// region. The test does not care where, only that writing the cast was enough to
// reach the pass that chooses the opcode.
func containsOpcode(ops []operation, op Opcode) bool {
	for _, laid := range ops {
		if bare, ok := laid.(Opcode); ok && bare == op {
			return true
		}
	}
	return false
}

// TestCastToAnyEmitsOpBoxAny pins the one shape every value boxes through: a
// cast to any, which is the upcast that cannot be a no-op in [emitWordCast]
// because the slot the value lands in is not laid out as the value itself. The
// bare upcast fall-through would leave a primitive standing where a pointer
// was expected, so the cast visitor's to=any branch is the one thing that must
// run.
func TestCastToAnyEmitsOpBoxAny(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var any boxed = 42{any};
        return;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for a cast to any")
	assert.False(t, containsOpcode(reader.operations, OpAnyUnbox),
		"no cast from any was written, OpAnyUnbox should not appear")
}

// TestCastFromAnyEmitsOpAnyUnbox pins the mirror: a cast from any goes through
// OpAnyUnbox, which does its type check inside the opcode rather than around
// it. The compiler owes no jump around the unbox because the trap is on the
// runtime, which is also why no OpPushNull lies on the fall-through — the
// existing emitDowncast path is bypassed entirely for an any source.
func TestCastFromAnyEmitsOpAnyUnbox(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var any boxed = 42{any};
        var int unboxed = boxed{int};
        return;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpAnyUnbox),
		"OpAnyUnbox was not emitted for a cast from any")
}

// TestTypeofAnyEmitsOpAnyPayloadType pins the typeof shape @v on an any. The
// static any id would be the answer the fall-through emitTypeof gives, which
// is the one id that cannot settle anything: the whole reason the check is
// written is to find out what the box actually holds. The any branch is what
// reads the stored id instead.
func TestTypeofAnyEmitsOpAnyPayloadType(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun bool() f = () {
        var any boxed = 42{any};
        return @boxed <: @{int};
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpAnyPayloadType),
		"OpAnyPayloadType was not emitted for @v on an any")
}

// TestCastAnyToAnyIsNoOp pins the one case the any branches must not reach.
// Both branches gate on the other kind being something other than any, so a
// value already of type any that is written {any} compiles as a plain cast of
// a value to its own type — which is nothing done to the value and no boxing
// of a value that is already a box.
func TestCastAnyToAnyIsNoOp(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var any x = 42{any};
        var any y = x{any};
        return;
    }
}
`)
	// One box for 42{any}, and no second one for x{any}: an any already is a
	// box, and casting it to any does not round-trip through another.
	count := 0
	for _, op := range reader.operations {
		if bare, ok := op.(Opcode); ok && bare == OpBoxAny {
			count++
		}
	}
	assert.Equal(t, 1, count,
		"OpBoxAny should appear once (for the int cast), not twice")
}

// TestCallImplicitBoxToAny pins the one conversion a call site is on the hook
// for: a value handed to an any-typed parameter is boxed on the way in. The
// static pass accepted the pass because int stands below any, but the slot the
// callee reads expects a one-word pointer to a box and not a one-word integer
// — if the compiler laid the integer down, the callee would read it as the
// pointer and the first unbox would dereference a random address.
//
// The fixed parameter is the one case tested here: the variadic flows through
// the same emitValueAs and the same cast visitor, so one case holds the pair.
func TestCallImplicitBoxToAny(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void(any) take = (x) {
        return;
    }
    static fun void() call = () {
        type(Main).take(42);
        return;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for an int passed to an any parameter")
}

// TestCallVariadicBoxesEachArg pins the same rule for a variadic any: each
// element of the packed array is a box, not a word of the argument's own type.
// Three ints means three boxes, because the array OpNewArray lays down is a
// run of one-word any pointers and each slot needs the pointer OpBoxAny
// returns.
func TestCallVariadicBoxesEachArg(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void(any...) take = (xs) {
        return;
    }
    static fun void() call = () {
        type(Main).take(1, 2, 3);
        return;
    }
}
`)
	count := 0
	for _, op := range reader.operations {
		if bare, ok := op.(Opcode); ok && bare == OpBoxAny {
			count++
		}
	}
	assert.Equal(t, 3, count,
		"OpBoxAny should appear once per variadic any argument")
}

// TestVarInitBoxesImplicit pins the same rule for a variable initializer: a
// value flowing into an any slot is boxed on the way in, not laid down as its
// own type's one word. Without this the slot would hold the raw int and the
// first read of it would dereference an invented address.
func TestVarInitBoxesImplicit(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var any a = 5;
        return;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for an int flowing into an any variable slot")
}

// TestAssignBoxesImplicit pins the rule at an assignment: writing an int into
// an any slot that was declared earlier is the same conversion a var
// initializer makes, so the store sees a one-word box pointer and not a word
// of integer data.
func TestAssignBoxesImplicit(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var any a = null;
        a = 5;
        return;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for an int assigned to an any slot")
}

// TestReturnBoxesImplicit pins the rule at a return: the caller reads back
// the words a method returned as the type the method was declared with, so a
// method whose return is any must leave a box on the stack whatever the
// expression's own type was.
func TestReturnBoxesImplicit(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun any() f = () {
        return 5;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for an int returned as any")
}

// TestStaticMemberInitBoxesImplicit pins the rule at a static initializer: the
// slot the static lies in was laid out at the member's declared width, so an
// int flowing into an any static is a box like every other int into any.
func TestStaticMemberInitBoxesImplicit(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static var any held = 5;
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for an int flowing into an any static")
}

// TestOrderingAnyHashesEachSide pins the rule at ordering: an order of an
// any pair hashes each side and compares the two hashes as ints, which is
// the shape OpCmp already settles for a pair of primitives. OpHash is what
// does the reduction; the compiler's part is laying each operand down as its
// hash and leaving OpCmp to a comparison of two words.
func TestOrderingAnyHashesEachSide(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun bool() f = () {
        var any a = 5;
        var any b = 7;
        return a < b;
    }
}
`)
	count := 0
	for _, op := range reader.operations {
		if bare, ok := op.(Opcode); ok && bare == OpHash {
			count++
		}
	}
	assert.Equal(t, 2, count,
		"OpHash should appear once per operand of an any order, got %d", count)
	assert.True(t, containsOpcode(reader.operations, OpCmp),
		"OpCmp was not emitted for an any order")
}

// TestOrderingCrossAnyHashesBothSides pins the same for a mixed any order:
// the non-any side hashes directly rather than boxing first, since a box
// would allocate for a word a hash reads right past. Only two OpHash
// instructions sit under the order, which is the whole of what the order
// adds — the one OpBoxAny the program carries belongs to the initializer
// of the any variable, not to the comparison.
func TestOrderingCrossAnyHashesBothSides(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun bool() f = () {
        var any a = 5;
        return a < 10;
    }
}
`)
	hashes, boxes := 0, 0
	for _, op := range reader.operations {
		if bare, ok := op.(Opcode); ok {
			switch bare {
			case OpHash:
				hashes++
			case OpBoxAny:
				boxes++
			}
		}
	}
	assert.Equal(t, 2, hashes,
		"OpHash should appear once per operand of a cross-type any order, got %d", hashes)
	assert.Equal(t, 1, boxes,
		"only the var initializer should box; the order should not, got %d", boxes)
}

// TestEqualityBoxesImplicit pins the rule at equality: `a == 5` where `a`
// is any lifts the int side to any so the pair reaches the runtime as two
// anys and the any-equality rule settles it. Ordering uses a different
// shape — hash-vs-hash — so an any equality still boxes while an any
// ordering never does.
func TestEqualityBoxesImplicit(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun bool() f = () {
        var any a = 5;
        return a == 5;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for an int compared with an any")
}

// TestInstanceMemberInitBoxesImplicit pins the rule inside the per-type
// initializer that runs for every fresh value: an any member given an int
// expression lands a box in the member's slot, not the int's one word.
func TestInstanceMemberInitBoxesImplicit(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    class Box {
        var any held = 5;
    }
}
`)
	assert.True(t, containsOpcode(reader.operations, OpBoxAny),
		"OpBoxAny was not emitted for an int flowing into an any member")
}

// Const is a compile-time promise about a slot and has no runtime trace: a
// const static is laid out at its underlying type's width, and reading or
// adding through it compiles exactly as if the const had never been written.
// The whole test is that the compiler reaches the end without panicking —
// typeInstanceOf would have to peel the const for the layout to be chosen,
// and the operand helpers would have to peel it for the arithmetic opcode to
// be found, both of which the pass does on the way to the emitted operations.
func TestConstStaticCompilesAndIsRead(t *testing.T) {
	compileOperations(t, `
namespace Main {
`+mainMethod+`
    static var int$ limit = 10;
    static fun int() f = () {
        return limit + 1;
    }
}
`)
}

// A method taking a const parameter is called the same way one taking the
// base type is: the parameter is a slot the body cannot write to, and the
// caller gives it its one value by passing an argument. A non-const value
// stands in for the const parameter because the call site is the parameter's
// declaration.
func TestConstParameterCompiles(t *testing.T) {
	compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun int(int$) f = (n) {
        return n + 1;
    }
    static fun int() g = () {
        return f(5);
    }
}
`)
}

// countOpcodes returns how many times a bare opcode appears in the emitted
// run, which is what distinguishes "a call forwarded its payload" from "the
// payload was repacked".
func countOpcodes(ops []operation, op Opcode) int {
	count := 0
	for _, laid := range ops {
		if bare, ok := laid.(Opcode); ok && bare == op {
			count++
		}
	}
	return count
}

// countUnaryOp returns how many times a unary opcode appears, which is what
// catches an OpNewArray that would be hidden behind the length operand it
// takes as its immediate.
func countUnaryOp(ops []operation, op Opcode) int {
	count := 0
	for _, laid := range ops {
		if unary, ok := laid.(*unaryOperation); ok && unary.opcode == op {
			count++
		}
	}
	return count
}

// TestCallSpreadForwardsPointer pins the one shape a spread compiles to: the
// callee's variadic parameter is one word of pointer regardless of how the
// caller wrote it, so when a pointer is handed on with ... there is nothing
// to pack and OpNewArray is not emitted. Writing the arguments out one at a
// time would have landed one — the TestCallVariadicBoxesEachArg case covers
// that path — so the opcode being absent is what says the forward took.
func TestCallSpreadForwardsPointer(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void(int, int...) take = (len, xs) {
        return;
    }
    static fun void(int*) f = (p) {
        take(0, p...);
        return;
    }
}
`)
	assert.Zero(t, countUnaryOp(reader.operations, OpNewArray),
		"a spread of a pointer should not pack a fresh run with OpNewArray")
}

// TestCallSpreadForwardsAnotherVariadic covers the forwarding case: a function
// whose variadic parameter is bound as a pointer inside its body hands that
// pointer straight on to another variadic. The caller does not know how many
// elements lie behind the pointer, which is the same convention a direct call
// into a variadic already stands on.
func TestCallSpreadForwardsAnotherVariadic(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void(int, int...) take = (len, xs) {
        return;
    }
    static fun void(int, int...) forward = (len, xs) {
        take(len, xs...);
        return;
    }
}
`)
	assert.Zero(t, countUnaryOp(reader.operations, OpNewArray),
		"forwarding a variadic with ... should not pack a fresh run with OpNewArray")
}

// TestCallSpreadDecaysArrayToPointer pins the array case: a run of elements
// lies inline and is not a pointer, so what reaches the callee has to be the
// address of its first element. That is the same decay a sum over an array
// takes — emitArrayBase leaves OpAddressOf — and OpNewArray is still absent
// because the elements are not copied.
func TestCallSpreadDecaysArrayToPointer(t *testing.T) {
	reader := compileOperations(t, `
namespace Main {
`+mainMethod+`
    static fun void(int...) take = (xs) {
        return;
    }
    static fun void() f = () {
        var int[3] arr;
        take(arr...);
        return;
    }
}
`)
	assert.Zero(t, countUnaryOp(reader.operations, OpNewArray),
		"a spread of an array should decay to a pointer, not pack a fresh run")
	assert.NotZero(t, countOpcodes(reader.operations, OpAddressOf),
		"an array spread should decay to the array's address with OpAddressOf")
}

// The two slots an initializer's frame is, named here as the test's own
// statement of the convention rather than read off the pass under test.
const cursorSlot, remainingSlot = 0, 1

// loadWordOperations is the read of a frame slot as a plain word.
func loadWordOperations(slot uint64) []operation {
	return append([]operation{newUnaryOp(OpPushVariable, slot)},
		append(pushTypeID(intShort), OpLoad)...)
}

// storeWordOperations is the store of a plain word into a frame slot: the slot,
// what to read it as, the value, what the value is, and the four words the
// location standing after the store is dropped a word at a time.
func storeWordOperations(slot uint64, value []operation) []operation {
	ops := append([]operation{newUnaryOp(OpPushVariable, slot)},
		pushTypeID(intShort)...)
	ops = append(ops, value...)
	ops = append(ops, pushTypeID(intShort)...)
	return append(ops, OpStore,
		OpPopWord, OpPopWord, OpPopWord, OpPopWord)
}
