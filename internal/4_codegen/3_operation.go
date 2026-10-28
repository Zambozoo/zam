package codegen

import (
	"encoding/binary"
	"io"
	"math"
	"slices"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

const (
	// OpCode comments are written in the following format:
	// (Byte Code Values) | Prestate {Mark Stack} [Op Stack] <VM State>  -> Poststate {Mark Stack} [Operation Stack] <VM State>

	// OpDupWord duplicates the word at the given offset.
	//   (code, offset) | [value] -> [value,value]
	OpDupWord Opcode = iota

	// OpPushI64 pushes its single uint64 onto the op stack.
	//   (code, value) | -> [value]
	OpPushI64
	// OpIsNegativeI64 pops an i64 operand and pushes the result of its negative comparison.
	//   (code) | [value] -> [value < 0]
	OpIsNegativeI64
	// OpIsPositiveI64 pops an i64 operand and pushes the result of its positive comparison.
	//   (code) | [value] -> [value > 0]
	OpIsPositiveI64

	// OpAddI64 pops two i64 operands and pushes their sum.
	//   (code) | [left, right] -> [left + right]
	OpAddI64
	// OpSubI64 pops two i64 operands and pushes their difference.
	//   (code) | [left, right] -> [left - right]
	OpSubI64
	// OpMulI64 pops two i64 operands and pushes their product.
	//   (code) | [left, right] -> [left * right]
	OpMulI64
	// OpDivI64 pops two i64 operands and pushes their quotient.
	//   (code) | [left, right] -> [left / right]
	OpDivI64
	// OpModI64 pops two i64 operands and pushes their remainder.
	//   (code) | [left, right] -> [left % right]
	OpModI64
	// OpDivU64 pops two u64 operands and pushes their quotient.
	//   (code) | [left, right] -> [left / right]
	OpDivU64
	// OpModU64 pops two u64 operands and pushes their remainder.
	//   (code) | [left, right] -> [left % right]
	OpModU64

	// OpNegI64 pops an i64 operand and pushes its negation.
	//   (code) | [value] -> [-value]
	OpArithmeticNegationI64
	// OpNotI64 pops an i64 operand and pushes its bitwise negation.
	//   (code) | [value] -> [~value]
	OpBitwiseNegationI64

	// OpAndI64 pops two i64 operands and pushes their bitwise AND.
	//   (code) | [left, right] -> [left & right]
	OpAndI64
	// OpOrI64 pops two i64 operands and pushes their bitwise OR.
	//   (code) | [left, right] -> [left | right]
	OpOrI64
	// OpXorI64 pops two i64 operands and pushes their bitwise XOR.
	//   (code) | [left, right] -> [left ^ right]
	OpXorI64

	// OpEqualI64 pops two i64 operands and pushes the result of their equality
	// comparison. A written comparison is [OpEq] instead, which is told the
	// types; this is the word compared as a word, which is what asking whether a
	// value is zero comes to.
	//   (code) | [left, right] -> [left == right]
	OpEqualI64

	// OpCastToString pops a value and the type id above it and pushes what
	// that value reads as. Every kind of value reads as a string its own way, so
	// they all go through this one instruction and the type id is what says
	// which way: a number reads as what its bits count, a char as the character
	// it numbers, a bool as true or false, a string as itself, a type as the
	// name it was declared under, a declaration as its Str says, and anything
	// else as an & followed by the address it lives at. The type also says how
	// wide the value is, a type being the three words of a packed id where every
	// other value is one.
	//
	// Choosing here rather than where the cast is written is what lets a cast
	// inside a template compile at all: one body serves every instantiation, so
	// nothing there can know what a generic argument will be bound to. It is the
	// same reason a hash or a comparison is told a type id and left to choose.
	//   (code) | [value,typeID] {markEntry?} -> [value.String()] {markEntry}
	OpCastToString
	// OpCastI64ToF64 pops an i64 operand and pushes its float64 representation.
	//   (code) | [value] -> [f64(value)]
	OpCastI64ToF64
	// OpCastF64ToI64 pops an f64 operand and pushes its int64 representation.
	//   (code) | [value] -> [int64(value)]
	OpCastF64ToI64
	// OpCastU64ToF64 pops a u64 operand and pushes its float64 representation.
	//   (code) | [value] -> [f64(value)]
	OpCastU64ToF64
	// OpCastF64ToU64 pops an f64 operand and pushes its uint64 representation.
	//   (code) | [value] -> [uint64(value)]
	OpCastF64ToU64

	// OpAddF64 pops two f64 operands and pushes their sum.
	//   (code) | [left, right] -> [left + right]
	OpAddF64
	// OpSubF64 pops two f64 operands and pushes their difference.
	OpSubF64
	// OpMulF64 pops two f64 operands and pushes their product.
	//   (code) | [left, right] -> [left * right]
	OpMulF64
	// OpDivF64 pops two f64 operands and pushes their quotient.
	//   (code) | [left, right] -> [left / right]
	OpDivF64

	// OpNegF64 pops an f64 operand and pushes its negation.
	//   (code) | [value] -> [-value]
	OpNegF64

	// OpEqualF64 pops two f64 operands and pushes the result of their equality
	// comparison, which is what asking whether a float is zero comes to: a
	// negative zero is zero under it, and a NaN is not.
	//   (code) | [left, right] -> [left != NaN && right != NaN && left == right]
	OpEqualF64

	// OpShiftLeftI64 pops two i64 operands and pushes their bitwise left shift.
	//   (code) | [left, right] -> [left << right]
	OpShiftLeftI64
	// OpShiftRightI64 pops two i64 operands and pushes their bitwise right shift.
	//   (code) | [left, right] -> [left >> right]
	OpShiftRightI64
	// OpShiftArithmeticRightI64 pops two i64 operands and pushes their arithmetic right shift.
	//   (code) | [left, right] -> [left >>> right]
	OpShiftArithmeticRightI64

	// OpPushGenericType pushes a generic argument type onto the operand stack from a generic type.
	//   (code, genericIndex) | [genericTypeID] -> [genericArgTypeID]
	OpPushGenericArgType

	// OpReturn leaves a function scope with one value to show for it. The frame
	// the call was made on lies beneath that value — the closure in the slot a
	// frame keeps for its receiver, the arguments above it, and the callee's own
	// variables above those — and all of it goes, along with every mark stack
	// entry those words owned. The value takes the receiver slot, which is where
	// the caller began pushing, so a call collapses to what it evaluated to.
	//
	// The type id is the return type the method was declared with rather than
	// the type of the expression returned, since the caller reads the words left
	// behind as the former. It is what says how many words those are, a value
	// laid out inline being wider than one, and whether the value owes a mark
	// stack entry in the slot it lands in.
	//
	// Where the frame begins is the function scope's to say and no instruction
	// can name it, which is also why the lexical scopes a return sits inside are
	// unwound here rather than closed one at a time before it.
	//   (code) | <funScope:i> [functionScopeValues,value,valueTypeID] {functionScopeEntries?,valueMarkEntry?} -> <funScope:i.prev> [value] {valueMarkEntry?}
	OpReturn
	// OpReturnVoid leaves a function scope with nothing to show for it,
	// discarding the frame as OpReturn does. A call that runs to no value is
	// written for what it did rather than for what it is, so the caller is left
	// with the stack it had before it built the frame.
	//   (code) | <funScope:i> [functionScopeValues] {functionScopeEntries?} -> <funScope:i.prev>
	OpReturnVoid
	// OpExit ends the run with the status on top of the stack. Nothing is
	// unwound and nothing is closed: a return makes a caller's stack out of a
	// callee's, and there is no caller here to make one for. Whatever the
	// process held goes when the process does.
	//
	// A status is a byte to whoever started the program, so what survives is the
	// low eight bits of the word. The word is a number and owns nothing on the
	// mark stack, so the collector is never asked about it.
	//
	// This is how the main method ends rather than how any method ends: it is
	// the one body reached by control running into it instead of by a call, so
	// OpReturnVoid there would give the stack back to nobody.
	//   (code) | [status] ->
	OpExit

	// OpJumpIfFalse jumps to the target offset if the value is false.
	//   (code, targetOffset) | <instruction:i> [value] -> <instruction:value ? i : offset>
	OpJumpIfFalse
	// OpJump jumps to the target offset.
	//   (code, targetOffset) | <instruction:i> -> <instruction:offset>
	OpJump

	// OpPop pops a value from the operand stack.
	//   (code) | [value,valueTypeID] {markEntry?} ->
	OpPop
	// OpPopWord discards the top operand word and the mark stack entry that word
	// owns, if it has one. It is told no type because it needs none: whether the
	// word is a collected reference was settled when it was pushed, and the mark
	// stack already says so.
	//   (code) | [value] {markEntry?} ->
	OpPopWord
	// OpPopMark discards the mark stack entry the top operand word owns, if it
	// has one, and leaves the word itself. It is told no type for the same
	// reason OpPopWord is not: whether the word is a collected reference was
	// settled when it was pushed, and the mark stack already says so.
	//   (code) | [value] {markEntry?} -> [value]
	OpPopMark
	// OpMark spends a value and the id standing over it and, while a collection
	// runs the Mark it is written in, says the value is reachable: a reference
	// is queued to be traced and a value laid out inline is walked where it
	// lies. Outside a collection it only spends the value. It says nothing about
	// the mark stack, which is the distinction worth drawing against the two
	// above it: those are about whether a word on the operand stack is a root
	// the collector must start from, and this is about walking what a value
	// already reaches.
	//
	// The type id is what says how to walk it, the same way it says how to write
	// a value out in OpCastToString: every type reaches what it reaches its own
	// way, so they all go through this one instruction rather than the source
	// choosing. That is also what lets a mark written in a template compile, one
	// body serving every instantiation of a generic argument no instantiation
	// has bound yet.
	//   (code) | [value,typeID] {markEntry?} ->
	OpMark

	// OpPushScope pushes a new scope onto the operand stack.
	//   (code) | <variableScope:s> -> <variableScope:t> {t}
	OpPushScope
	// OpPopScope pops a scope from the operand stack.
	//   (code) | <variableScope:s> {scopeValues,s} [scopeValues] -> <variableScope:s.prev>
	OpPopScope

	// OpPushMember selects a member out of the given object. The object's type is
	// spent and the member's stands in its place, the index alone saying which
	// member is meant.
	//
	// What comes back is what went in. Handed the object's address, it leaves the
	// member's address, which is a location like any other and so loadable and
	// storable through — a member lies in the instance, so naming one is
	// arithmetic on where the instance is, and a store through it reaches the
	// object the program named rather than a copy of it. Handed the object's
	// words, it leaves the member's words, which are a value and can only be
	// read.
	//
	// Nothing here decides which of those it was given. The compiler does, when
	// it emits the object, and it hands over an address whenever the object has
	// one: a pointer, an abstract or an interface, or a value the program gave a
	// home to. Only a value with no home arrives as words, and a member of one is
	// not an assignment target, so the two forms never have to be told apart
	// after the fact.
	//
	// The member's type is not pushed by whoever selects it, the way a local's or
	// a static's is, because here it is not theirs to know: which type the index
	// lands on is the object's runtime type to answer, so that a subclass reached
	// through a base type reads its own.
	//
	// This is the one location whose pointer owns a mark entry, and it owns one
	// because the object word is spent making it: what was a reference to the
	// allocation is now a pointer into the middle of it, and nothing else is left
	// holding the allocation up. The entry is an interior one, saying how far
	// back the object lies, and is owed only where the object's word owned one.
	//   (code, memberIndex) | [objAddr,objTypeID] {objMarkEntry?} -> [memberPointer,memberTypeID] {memberInteriorMarkEntry?}
	OpPushMember
	// OpPushMemberOfValue selects a member out of an inline value with no home,
	// which stands on the stack as its words rather than as an address. The value
	// is spent and the member's value is left in its place.
	//   (code, memberIndex) | [objWords...,objTypeID] {objMarkEntries?} -> [memberWords...] {memberMarkEntries?}
	OpPushMemberOfValue

	// OpLoadTailMember is OpPushMember followed by OpLoad for the tail member
	// of a tailed struct, folded into one instruction so the length is still
	// reachable at the load. A tail is declared `var char[~] data`, one record
	// for every length it is instantiated at, so the record carries no length
	// of its own — the length lives in the enclosing, baked into its type id
	// when the value is sized and into the instance's one-word header when it
	// is reached through a pointer.
	//
	// The read taken off that length is what makes the whole run arrive on the
	// stack rather than one element; the type pushed over it has the length
	// stamped in, so a comparison or an order of it reads what it is. The
	// lvalue and base-address paths — store, index — go through OpPushMember
	// and see one element, which is the length the record carries.
	//   (code, memberIndex) | [obj,objTypeID] {objMarkEntry?} -> [tailWords...,memberTypeID]
	OpLoadTailMember

	// OpPushMethod pushes a method of the given object as a closure. The closure
	// holds the receiver, and the receiver is the object as it was pushed: a value
	// is captured entire, however many words its layout spans, and a pointer is
	// captured as the one word it is. The type id above the object is captured
	// with it, since it is the only thing that says which of the two was pushed
	// and how much of the stack the receiver was.
	//
	// That type is also what the index is resolved against, and it is resolved
	// through a pointer as readily as against a value: a method is reached on the
	// instance either way, which is what lets a call be written on the pointers
	// every subtype relation is written between.
	//
	// The object owes a mark entry when what was pushed is a reference — a pointer
	// receiver, or a value holding one among its words. A value of no such words
	// owes none.
	//   (code, methodIndex) | [obj...,objTypeID] {objMarkEntry?} -> [methodPointer] {markableEntry}
	OpPushMethod
	// OpCallMethod calls the closure lying beneath its arguments. The closure is
	// not consumed: it stays in the slot a frame keeps for its receiver and the
	// arguments follow it, so the operands already are the new scope. The frame
	// width is what says where that scope begins, and it is counted in words
	// rather than in arguments because an argument laid out inline is wider than
	// one.
	//
	// What a closure closes over is laid out past the arguments on entry, which
	// is where the body was compiled to read it. The caller counts none of it,
	// knowing nothing of it: the captures are the closure's own, and how many
	// there are is what OpNewLambda built it with.
	//
	// A native is called the same way. The frame is built as it is for anything
	// else, and what runs is C rather than operations, which reads its arguments
	// out of that frame and leaves through it.
	//   (code, frameWords) | <oldFunScope:i> [closure,args...] {closureMarkEntry,argMarkEntries?} -> <newFunScope:closure.instruction> {newFunScope, ..., oldFunScope}
	OpCallMethod
	// OpCallMethodOpen is OpCallMethod for a frame holding arguments whose width
	// an instantiation decides: their type ids stand above the frame, and their
	// widths are added to the fixed words the first operand counts.
	//   (code, fixedWords, openCount) | [closure,args...,typeID...] -> as OpCallMethod
	OpCallMethodOpen

	// OpEq pops two values, each with its own type id above it, and pushes whether they
	// are equal. What equality comes to is the type's to say, which is what it
	// is told for: a word counted as the number it is, a word read as an f64, a
	// string read as its characters, and a declaration asked by its own Eq —
	// though a value is never unequal to itself, so being the same word answers
	// it first.
	//   (code) | [left,leftTypeID,right,rightTypeID] {leftMarkEntry?,rightMarkEntry?} -> [left == right || left.Eq(right)]
	OpEq
	// OpCmp pops two values, each with its own type id above it, and pushes how they are
	// ordered: negative where the left is the lesser, positive where it is the
	// greater, and zero where neither is. Order is the type's to say for the
	// same reason equality is, and a declaration says it by its own Cmp.
	//
	// A string is ordered by its bytes as far as both run, the shorter standing
	// below the one it is the beginning of. What is pushed is a sign and not a
	// distance — exactly -1, 0 or 1, as every other type gives — so that the
	// number a spaceship hands the program means the same whatever it compared.
	//
	// These two are the whole of comparison. Every operator that compares asks
	// one of them and reads its answer, an order being asked which side of zero
	// it fell on.
	//   (code) | [left,leftTypeID,right,rightTypeID] {leftMarkEntry?,rightMarkEntry?} -> [(left > right) - (left < right)]
	OpCmp

	// OpNew allocates arraySize instances of the given type, each carrying
	// tailLength tail elements, and pushes a reference to the allocation.
	//
	// Each of the arraySize instances is then initialized in turn, by running
	// the routine the type definition names in initializerInstruction over it.
	// The initialization belongs here rather than at the place the allocation
	// was written because arraySize is not the compiler's to know: it is an
	// expression, so the same instruction stands for one instance or a thousand,
	// and only the runtime can count them. A type with nothing to initialize
	// names no routine and is left as it was allocated.
	//
	// The reference owns a mark entry where the instances are classes, which
	// are the collector's; any other run is the program's own memory.
	//   (code) | [arraySize,tailLength,typeID] -> [value] {markEntry?}
	OpNew
	// OpNewRun is OpNew for new with a count: an array, which is the program's
	// memory whatever its elements are. An array of class instances is laid out
	// as OpNew lays one but kept off the collector's heap, every element lying
	// inside nothing, and free releases it.
	//   (code) | [arraySize,tailLength,typeID] -> [value] {markEntry?}
	OpNewRun
	// OpNewArray allocates an array of the elements lying beneath its type id and
	// pushes a reference to it. The elements are already in order, so the
	// instruction is told only how many there are; the type id is what the
	// allocation is sized and marked by.
	//
	// How many is counted in elements and not in words, which part company as
	// soon as an element is a class, struct or enum: those lie on the stack
	// inline and are as wide as their layout. The type id already says how wide,
	// so the count and the type id together are what say where the elements
	// begin. OpNewThread is told words instead because a frame is a run of mixed
	// types that no one width describes, which is the reason the distinction is
	// worth drawing rather than a disagreement about it.
	//   (code, elementCount) | [elements...,typeID] {elementMarkEntries?} -> [array] {markEntry?}
	OpNewArray

	// OpNewThread allocates a thread of the given type, moves the frame lying
	// beneath its type id into that allocation, and pushes a reference to it. The
	// frame is what a call builds — the closure in the slot a frame keeps for its
	// receiver, and the arguments above it — so the frame width, in words, is
	// what says where it begins.
	//   (code, frameWords) | <threadList:i> [closure,args...,typeID] {closureMarkEntry,argMarkEntries?} -> <threadList:i,j> [thread] {markEntry}
	OpNewThread
	// OpNewThreadOpen is OpNewThread for a frame holding arguments whose width
	// an instantiation decides: their type ids stand above the thread's.
	//   (code, fixedWords, openCount) | [closure,args...,typeID,typeID...] -> as OpNewThread
	OpNewThreadOpen
	// OpWaitThreadVoid waits for a thread to complete and returns void.
	//   (code) | <threadList:i,...> [thread] -> <threadList:...>
	OpWaitThreadVoid
	// OpWaitThread waits for a thread to complete and returns its result. The
	// type id is the result's, which is what says how many words to read.
	//   (code) | <threadList:i,...> [thread,resultTypeID] {threadMarkEntry} -> <threadList:...> [value] {valueMarkEntry?}
	OpWaitThread

	// OpStore stores a value at the pointer beneath it and leaves that location
	// standing, so a chain of assignments reads as one and a statement that only
	// stores discards a location rather than a bare word.
	//
	// Two type ids are in play and they are not the same one: the value's says
	// how many words to take off the stack and whether they own mark entries,
	// while the location's says how much lies at the address and is what the
	// location goes on being. A store that narrows or widens — a subclass written
	// through a base-typed location — is the case that keeps them apart.
	// 	 (code) | [pointer,typeID,value,valueTypeID] {pointerMarkEntry,valueMarkEntry?} -> [pointer,typeID] {pointerMarkEntry?}
	OpStore

	// OpLoad reads a location, replacing it with the value that lies at its
	// pointer. The type id is what the read is measured by: how many words come
	// off the address, and whether what lands on the stack is a reference the
	// collector has to visit. That is the whole of what a location's type is
	// for, here and in OpStore, and it is why a location carries one at all.
	//   (opcode) | [pointer,typeID] {pointerMarkEntry?} -> [value] {valueMarkEntry?}
	OpLoad

	// OpPushStatic pushes a pointer to a static slot. The type id is the type of
	// what lies in the slot, not of the word pushed: the word is the slot's
	// address, and what is pushed is half of the location a load reads through
	// or a store writes through, the type words above it being pushed after.
	// The address owes no mark stack entry: a static is a root of its own.
	//   (code, staticIndex, typeID) | -> [staticPointer]
	OpPushStatic

	// OpPushGenericStaticSlot is OpPushStatic for a generic static method that
	// was called with open type arguments. The compiler cannot name the slot
	// as a constant — which slot answers depends on what the enclosing
	// generic's parameters are bound to at run time — so the type arguments
	// are pushed first, in source order, and the dispatch chooses the slot
	// among the method's concretes by matching them.
	//
	// The operand is the method's own id in the generic static method
	// registry; the registry says how many type-argument words to pop (3 per
	// argument) and holds the (typeArgs → slot, functionType) rows the search
	// walks. The result is both the slot pointer and the concrete function
	// type id, so the caller treats it as a single-step replacement for
	// OpPushStatic + emitPushTypeID.
	//   (code, genericMethodID) | [typeArg0,...,typeArgN] {} -> [slot,fnType]
	OpPushGenericStaticSlot

	// OpPushStringLiteral pushes an interned string literal, which is a value and
	// not a location: a string is a pointer to its length-prefixed bytes, and the
	// bytes lie in the static region, so the slot's address is already the
	// string. Nothing is loaded through what this leaves.
	//
	// It is a separate opcode from OpPushStatic for that reason. The two push the
	// same word out of the same slot numbering, but a static slot's type id
	// describes what the address points at while a literal's would describe the
	// address itself, and the id is what mark stack ownership is decided by. One
	// opcode reading its operand both ways is one a runtime cannot implement.
	//
	// The string owns a mark stack entry like every other string. The collector
	// skips static-region addresses, but a local a literal initializes may later
	// hold an allocated string, and the entry is what roots it.
	//   (code, staticIndex) | -> [string] {markEntry}
	OpPushStringLiteral

	// OpDelete runs the Delete override on the receiver under its type id. A
	// class, abstract, or interface pointer dispatches through the object's
	// own slot — the default being std.Obj.Delete, which marks the allocation
	// dead — and every other receiver falls through unchanged. Any value is a
	// valid receiver; releasing memory is OpFree's, so a non-pointer here only
	// pays the dispatch cost or the pop of its own words. The receiver's words
	// and the mark entries they owned go with the delete either way.
	//   (code) | [value,valueTypeID] {valueMarkEntries?} ->
	OpDelete

	// OpFree releases what the pointer under its type id addresses. A pointee
	// the collector owns — a class instance, a string, a closure, a thread —
	// is marked dead so the next sweep reclaims it; anything else is raw-freed
	// as the program's own allocation. The pointer and its mark entry go with
	// the release.
	//   (code) | [pointer,pointerTypeID] {pointerMarkEntry?} ->
	OpFree

	// OpHash pops a value with its type id above it and pushes its hash. What a hash
	// comes to is the type's to say, for the same reason equality is and told
	// the same way: values that are equal hash alike, and only the type knows
	// which values those are. A word hashes as the number it is, a string over
	// the characters it holds, and a declaration by its own Hash.
	//   (code) | [value,typeID] {markEntry?} -> [value.Hash()]
	OpHash

	// OpPushInterfaceMethod pushes an interface method of the given object as a
	// closure. The index is the interface's own, which means nothing to whatever
	// implements it, so the type id above the object names the interface to
	// translate that index out of and the object says whose table to translate
	// it through. The interface is pushed rather than named by an immediate
	// because a method body is emitted once for a whole template: inside
	// ArrList[E], the id of a List[E] is not known until the receiver says which
	// instantiation is executing, which is what [emitPushTypeID] reads off it.
	//
	// The object always owes a mark entry, which is what sets this apart from
	// OpPushMethod: an interface is not laid out anywhere, so whatever is reached
	// through one is a pointer to the class implementing it, and that pointer is
	// always a reference the collector has to visit. There is no value form of an
	// interface receiver for the entry to be owed by.
	//   (code, methodIndex) | [obj,interfaceTypeID] {objMarkEntry} -> [methodPointer] {markableEntry}
	OpPushInterfaceMethod

	// OpPushNull pushes the null of a type, which owns a mark entry where a
	// value of the type would.
	//   (code) | [type] -> [null] {markableEntry?}
	OpPushNull
	// The step operations move an lvalue by one of whatever it counts in, which
	// the type id above it is what says: an integer steps by one, a float by
	// one, and a pointer by one element of what it points at, which is that
	// pointee's stride in bytes rather than a single byte. The id is the bound
	// one even inside a template, since an open generic argument is resolved
	// against the receiver before it is pushed, so Node[int]* and Node[BigType]*
	// arrive here already telling the step how far it goes.
	//
	// The entry the location owned goes with the location, and what stands in
	// its place is the stepped value's own: a pointer moved by one element is
	// still a reference the collector has to visit, and an int never was one.
	//
	// OpIncPre increments a pre-state value.
	//   (code) | [lvalue,typeID] {lvalueMarkEntry?} -> [newValue:=lvalue+1] {valueMarkEntry?}
	OpIncPre
	// OpDecPre decrements a pre-state value.
	//   (code) | [lvalue,typeID] {lvalueMarkEntry?} -> [newValue:=lvalue-1] {valueMarkEntry?}
	OpDecPre
	// OpIncPost increments a post-state value.
	//   (code) | [lvalue,typeID] {lvalueMarkEntry?} -> [oldValue; (lvalue:=lvalue+1)] {valueMarkEntry?}
	OpIncPost
	// OpDecPost decrements a post-state value.
	//   (code) | [lvalue,typeID] {lvalueMarkEntry?} -> [oldValue; (lvalue:=lvalue-1)] {valueMarkEntry?}
	OpDecPost

	// OpTypeOf spends a value and the id standing over it, and leaves the whole
	// three-word id the value actually carries. That is read off the entry — the
	// type it was allocated as — which is why it can differ from the id that went
	// down with it: that one is only what the source was able to see the value as,
	// and settling a claim about what the value really is means asking the value.
	//   (code) | [value,typeID] {markEntry?} -> [value.type]
	OpTypeOf

	// OpSizeOf pushes how many bytes a value of the type its id names occupies
	// where such values are laid out inline, which is the stride from one to the
	// next. What is stored as a reference is a word wide however wide the thing
	// it refers to is.
	//
	// The size is asked for at runtime only when it cannot be had before it:
	// a template's instantiations are not all the same width, so the width of an
	// open generic argument is settled by the id that stands in for it here.
	//   (code) | [typeID] -> [size]
	OpSizeOf

	// OpConcatString concatenates two strings.
	//   (code) | [left,right] {leftMarkEntry,rightMarkEntry} -> [left + right] {valueMarkEntry}
	OpConcatString
	// OpNative pushes the native the index names, which is a slot in the native
	// table the image carries: the table holds names, and what stands in the slot
	// at run time is the C function the interpreter was built with under that
	// name. The interpreter is compiled against the project's own C files, so a
	// name that answers to nothing is a link error rather than anything this
	// compiler could have reported.
	//
	// The word is an address and no more, so it owns nothing on the mark stack —
	// there is no allocation behind it to visit — and what it takes and gives
	// back is not the compiler's to know. The source says that by casting the
	// address to a signature, and calling it is then a call like any other.
	//   (code, nativeIndex) | -> [native]
	OpNative

	// OpIsSubtypeOf answers whether a value of the type beneath may be read as the
	// type above it. Both operands are whole type ids, three words each, and what
	// is left in their place is the one boolean.
	//
	// For most types the answer is that the two ids are the same. A primitive, a
	// thread and an enum stand in no line of descent — nothing can declare itself
	// below them — so a value of one is a value of exactly its own type, and
	// comparing the three words settles it without any record being read.
	//
	// A class, an abstract or an interface is the case that descends, and so the
	// only case a table is needed for. The answer is one lookup of the parent's
	// short id in the table the child's record carries — itself, each declaration
	// up its base chain, and each interface it implements.
	//
	// The table is a perfect hash keyed by short id, and every key is stored as
	// its own value. It has to be, because a perfect hash records nothing about
	// which keys were built into it: an id that was not lands in some other id's
	// slot and would otherwise be answered yes. Reading the slot back and
	// comparing it against the id asked with is what makes a miss a miss.
	//
	// A pointer and a function descend too, and neither by any declaration: what
	// stands above them is worked out from their records rather than looked up.
	// Both are both-or-neither — a Child* may be read as a Parent* and is no kind
	// of Parent, the pointer being one word wherever the value is however many —
	// and once both sides are one, a pointer asks the same question of its pointee
	// and a function asks it of every position of its signature. The two positions
	// of a signature are read in opposite directions, because what a caller does
	// with a function is hand it arguments and take a result: a stand-in may
	// accept more than was promised and must promise no less than it returns.
	//
	// A run descends nowhere. It is copied and passed whole, so a run of one type
	// is a different width from a run of another and could not be read as it
	// however the elements are related: a Circle[5] is not a Shape[5] by way of a
	// Circle being a Shape, and a table of short ids could not say so if it were.
	// A tail length is a width in the same way and the same follows.
	//
	// Two pointers answer before their pointees are asked. A void* stands above
	// every pointer, naming no layout and so promising nothing a pointee could
	// fail to be; and a Buf~N* is a Buf~*, a tail length riding with the type id
	// rather than sitting in the layout, so a sized tail is laid out exactly as
	// the unsized one it was written from. Neither reads back the other way, and
	// both are pointer rules only: standing on the stack a Buf~N and a Buf~ are
	// two widths, which is the tail-length rule above and is not reached past
	// except through the one word a pointer is.
	//   (code) | [childTypeID,parentTypeID] -> [childTypeID <: parentTypeID]
	OpIsSubtypeOf

	// OpAddressOf pushes the address of what the pair beneath it names. A
	// location's pointer is already that address, so the pair collapses to it; a
	// temporary has none until the runtime puts the value somewhere.
	//   (code) | [value,typeID] {markEntry?} -> [&address of value] {markEntry?}
	OpAddressOf

	// OpNewLambda allocates a closure over the frame it was written in and
	// pushes it. Beneath the closure's own type id lie the captures, each as the
	// location it is held at under the type of what is held there, and the
	// allocation takes a copy of every one: a closure outlives the frame it was
	// written in, so what it closes over cannot be left there. Each type id is
	// what says how wide that copy is and whether it is a reference the
	// collector has to visit, which is also what lets a lambda in a template
	// close over a value whose width no instantiation has settled.
	//
	// The receiver is no operand of this and is not among the captures: a
	// closure carries one of its own, and the one this closure carries is the
	// one the frame it is built in carries, so a lambda written in an instance
	// method sees the receiver that method sees and a lambda written in a static
	// method sees none. That is what keeps the receiver a single word whatever
	// its declaration's width, the way a frame's own receiver slot is.
	//
	// The body is named by an offset rather than fallen into, since what follows
	// this instruction is the rest of whatever expression the lambda was written
	// in the middle of. A call finds the captures laid past the arguments, which
	// is where the body was compiled to read them.
	//   (code, instructionOffset, captureCount) | [(capturePointer,captureTypeID)...,typeID] {capturePointerMarkEntries?} -> [closure] {markEntry}
	OpNewLambda

	// OpPushVariable pushes a pointer to the frame slot at the given offset. The
	// type that makes it a location is pushed after it by whoever wanted the
	// location, this instruction being told none: what lies in the slot is not
	// its question, and the only instructions that ask are the load or store
	// that spends the pair.
	//
	// The pointer owns no mark entry. It addresses a frame slot rather than an
	// allocation, and the slot's own contents were given their entry when they
	// were pushed. A thread is no exception: its frame is moved into an
	// allocation, but the allocation is held by the thread list for as long as
	// the thread runs, so a pointer into it keeps nothing alive that was not
	// already.
	//   (code,offset) | -> [variablePointer]
	OpPushVariable
	// OpPushVariableOpen pushes a pointer to a frame slot that lies past slots
	// whose width an instantiation decides. The first operand counts the words of
	// the fixed-width slots before it, and the second how many type ids stand on
	// the stack, one for each open-width slot before it, whose widths are added.
	//   (code, fixedWords, openCount) | [typeID...] -> [frameLocation]
	OpPushVariableOpen

	// OpDereference dereferences a pointer.
	//   (code) | [pointer,typeID] {pointerMarkEntry?} -> [value] {valueMarkEntry?}
	OpDereference

	// OpDefaultValue pushes a default value for the given type. A number starts
	// at zero and anything held as a reference starts at null, neither of which
	// is any type's to say differently.
	//
	// A class, struct or enum lies inline and is as wide as its layout, so what
	// is pushed is that many words, and what stands in them is what the type
	// says stands in a fresh instance: the routine named by initializerInstruction
	// is run over the value before it is left on the stack. That is what carries
	// initialization inward without any site having to reach for it — a member
	// written with no expression of its own is emitted as this, so a member of a
	// declared type is filled in by running that type's own routine — and it is
	// what fills in an array, whose length the type id carries: the same routine
	// runs over each element in turn.
	//
	// The recursion terminates because a type cannot contain itself inline;
	// semantic analysis rejects the cycle before codegen sees it.
	//   (code) | [typeID] -> [defaultValue]
	OpDefaultValue

	// OpPushReceiver pushes a pointer to the word holding the receiver's
	// address, which is what `this` names. That word lies in the closure the
	// call was made through, and slot 0 of the frame is where the closure is —
	// so this is one step the compiler cannot take on its own, the step's length
	// being the closure's kind's to say and the kind not being known until the
	// call is standing.
	//
	// A reference method closure keeps the address as its first entry. A value
	// method closure keeps the receiver itself there and the address of that copy
	// as its second, so the step is one entry longer. Either way what is pushed
	// is a pointer to a word holding an address, which is exactly what a pointer
	// to a frame slot was when the receiver lived in one — so `this` goes on
	// being a location that loads to the receiver, and nothing downstream of it
	// changes.
	//
	// The pointer owns no mark entry, for the reason OpPushVariable's does not:
	// it addresses the inside of the closure, and the closure is held for the
	// whole of the call by frame slot 0, which has an entry of its own. An entry
	// here would be a second name for something already rooted, and a collector
	// following it would be following a pointer into the middle of an
	// allocation.
	//   (code) | -> [receiverPointer]
	OpPushReceiver

	// OpCallNative calls a native function sitting at the base of its own frame.
	// Everything else about the frame is shaped like OpCallMethod's: the callee
	// — the native address OpNative pushed — stands at the base, and the arguments
	// pile on top of it. The runtime pops the frame around the call via the SDK
	// return helpers rather than through OpReturn, since a native runs in C and
	// never comes back through the dispatch loop.
	//   (code, frameWords) | [native,arg0,...] -> [result?]
	OpCallNative

	// OpPushReceiverType pushes the three words of the receiver's actual type
	// id, read out of the mark header the receiver points at. This is how
	// [operationVisitor.emitPushTypeID] resolves an open generic argument: a body
	// is emitted once per declaration and the id a generic argument stands for is
	// known only at run time, from the instance whichever concrete called in with.
	//
	// The operand says where the receiver sits in the frame. A frame slot — the
	// one case that comes up in an initializer, where `this` is slot 0 of the
	// frame rather than a word in the closure — reads the slot directly. The
	// sentinel [ReceiverSlotClosure] reads it the same way OpPushReceiver does,
	// through the closure sitting in slot 0 of the frame.
	//
	// The slot's word is a pointer or a frame location — the two shapes a
	// receiver pointer arrives in — and is resolved through
	// zam_thread_location_address, which handles both. The header the pointer
	// lands at is the one word that answers for the instance under whatever
	// supertype it was named by, and so is the one that resolves a generic
	// argument its template left open.
	//
	// Three words are pushed, as every type id is, and none owns a mark entry: a
	// type id is three plain words wherever it comes from.
	//   (code, slot) | -> [typeID]
	OpPushReceiverType

	// OpBoxAny packs a value and its three-word type id into a fresh any object,
	// so that a slot typed any can hold a value of any width. The type id above
	// the value is what says how wide the value is and how it is to be walked,
	// which is the one piece the box has to carry so that whatever reads the box
	// back can tell what came out.
	//
	// The runtime allocates two heap objects: a payload box, mark + shortID (read
	// from the type id) + the value's words, and an any box, mark + any shortID
	// + the type id's three words + a pointer to the payload. The pointer in the
	// any object is what the collector follows; the typeID beside it is plain
	// data, which is what lets the walker's rule stay a single always-follow
	// rather than a branch on the stored id.
	//
	// The value and its id drop their mark entries into the one entry the any
	// pointer owns, as every operation that collapses a (value, typeID) pair
	// does: the pair's reachability is now expressed by the one pointer.
	//   (code) | [value,typeID] {valueMarkEntry?} -> [any*] {anyMarkEntry}
	OpBoxAny

	// OpAnyPayloadType reads the type id a box carries. The id lies at a fixed
	// offset inside the any object — past the mark word and the any shortID —
	// and is the three words the pointer landed on plus one.
	//
	// This is the one opcode OpTypeOf cannot stand in for. OpTypeOf asks the
	// header the value is laid out as, which for an any is the any shortID
	// itself and so always answers "any." The payload type that was actually
	// boxed is what lets @v on an any resolve to what the runtime found, which
	// is what makes @v <: @{T} the safe typeof-check.
	//
	// The any's mark entry goes with it: three plain words are left in its place
	// and the pointer is gone.
	//   (code) | [any*] {anyMarkEntry} -> [payloadTypeID]
	OpAnyPayloadType

	// OpAnyUnbox reads the value out of a box as the type above it, aborting if
	// the box does not actually hold a value of that type. The target id decides
	// how many words to copy out of the payload — the same id that would have
	// decided how many to copy in on the way back — and the result stands on the
	// stack as a value of that type would stand anywhere else.
	//
	// The check lives in the opcode rather than around it because a mismatch
	// cannot return a value: a primitive has no null to answer with and copying
	// the wrong number of words would corrupt the stack. A user that wants the
	// question without the trap writes @v <: @{T}, which answers true or false
	// without touching the payload.
	//
	// The any's mark entry drops with the pointer, and if the unboxed value is
	// one the collector tracks, the pair the value left on the stack owns a new
	// entry of its own — the usual rule for a value and the id above it.
	//   (code) | [any*,targetTypeID] {anyMarkEntry} -> [value] {valueMarkEntry?}
	OpAnyUnbox

	// OpEnumValue pushes a pointer to the static slot holding the primitive a
	// value of a primitive-backed enum is backed by. The primitives lie back to
	// back from the slot the definition the type id names gives, and the value's
	// discriminant is the byte offset of its own from there; nothing about the
	// value but its first word is read, and the rest of it is spent. What is
	// left is half of a location, as with [OpPushStatic], so a load through it
	// reads the primitive.
	//   (code) | [enumWords...,enumTypeID] {enumMarkEntries?} -> [staticPointer]
	OpEnumValue

	// OpMarkAs reads the word beneath its type id as that type, which is what a
	// cast of one word to another is: the word is left as it stands, and the
	// mark stack entry it owned is traded for the one a word of the new type
	// owns. Whether a type is one the collector visits may be known only once a
	// template is instantiated, which is why the runtime and not the compiler
	// settles it.
	//   (code) | [value,typeID] {markEntry?} -> [value] {markEntry?}
	OpMarkAs
)

// ReceiverSlotClosure is the OpPushReceiverType operand that reads `this`
// through the closure in frame slot 0 rather than out of a frame slot. It is
// the one value a frame offset will never take, since a slot number counts
// words into the frame and never fills a whole word.
const ReceiverSlotClosure uint64 = math.MaxUint64

type (
	// operation is an interface for all operations.
	operation interface {
		byteCountable
	}

	// Opcode is an operation code enum.
	Opcode uint8

	// unaryOperation is an operation with a single immediate operand.
	unaryOperation struct {
		// opcode is the opcode of the unary operation.
		opcode Opcode
		// operand is the immediate operand of the unary operation.
		operand uint64
	}

	// binaryOperation is an operation with two immediate operands.
	binaryOperation struct {
		// opcode is the opcode of the binary operation.
		opcode Opcode
		// first is the first immediate operand.
		first uint64
		// second is the second immediate operand.
		second uint64
	}

	// jumpOperation is an operation that jumps to a target offset.
	jumpOperation struct {
		// opcode is the opcode of the jump operation.
		opcode Opcode
		// target is the target offset of the jump operation.
		target int
	}

	// capturedVariable is a variable a lambda closes over, named as its body
	// names it and typed as the frame it was written in declared it.
	capturedVariable struct {
		name string
		typ  syntax.Type
	}

	// binding is what a name in scope stands for. Every name is resolved the
	// same way — innermost scope outward — but what it resolves to is what says
	// where the value lies, and so how naming it is pushed.
	binding interface {
		// bindingType is the type of the value the name stands for, which is the
		// type id pushed under the location it emits as.
		bindingType() syntax.Type
	}

	// localBinding is a declared variable, a parameter or a capture: a slot in
	// the frame being emitted. The type decides which push opcode reads the slot
	// and how many slots it spans.
	localBinding struct {
		offset uint64
		typ    syntax.Type
		// open is the types of the open-width slots ahead of this one, whose
		// widths the runtime adds to offset.
		open []syntax.Type
	}

	// staticBinding is a static of the declaration the name was written in or of
	// one that declaration is nested inside. It lies in the static region rather
	// than in any frame, so it is pushed the way a qualified selection of it is.
	staticBinding struct {
		field syntax.FieldIndexable
		typ   syntax.Type
	}

	// instanceBinding is a field of the declaration the name was written in,
	// reached through the receiver: writing the name alone is writing it on
	// this. decl is what the field's index is resolved against, an index
	// counting the fields of a static type rather than of an instance.
	instanceBinding struct {
		decl  syntax.DeclTypeable
		field syntax.FieldTypeable
	}

	// variableScope maps the names visible in one lexical scope to what they
	// stand for. baseOffset is the slot the scope started at, so leaving the
	// scope releases the slots it declared.
	variableScope struct {
		outer      *variableScope
		variables  map[string]binding
		baseOffset uint64
		baseOpen   int
	}

	// loopContext is one enclosing loop's unpatched jumps, and the number of
	// lexical scopes that were open around it. A jump out of the loop closes
	// every scope down to that depth before it branches.
	loopContext struct {
		// label is the name a label statement gave the loop, empty when none did.
		label string
		// scopeDepth is the number of scopes open around the loop.
		scopeDepth int
		// continues are the branches to the loop's step.
		continues []*jumpOperation
		// breaks are the branches past the end of the loop.
		breaks []*jumpOperation
	}

	// operationVisitor walks every declaration after the static-field pass and
	// emits each method body's operations into the operations region. The main
	// method is emitted first so execution begins at operations offset 0; it is
	// guarded during the walk so its body is not emitted a second time.
	operationVisitor struct {
		typeResolver
		ast    *syntax.AST
		reader *codegenReader

		// mainMethod is the entry method, emitted before the walk and skipped
		// when the walk reaches it again.
		mainMethod *syntax.FieldMethod

		// scopes are the lexical scopes of the method being emitted, innermost
		// last. The outermost of them are the declarations the method was
		// written in, which is what lets a field be named without qualification.
		scopes base.Stack[*variableScope]
		// currentDecl is the declaration whose fields are in scope. A lambda body
		// is emitted as a frame of its own and so builds its scopes over again,
		// but it is written in the same declaration, and this is what says which.
		currentDecl syntax.Decl
		// loops are the loops enclosing the statement being emitted, innermost
		// last. A break or continue is patched through one of these.
		loops base.Stack[*loopContext]
		// pendingLabel is the label a label statement left for the loop it
		// introduces to claim, since that loop may be nested in the block its
		// initializer was hoisted into rather than being the labelled statement.
		pendingLabel string
		// returnType is what the method being emitted was declared to give
		// back. A return lies anywhere in the body, however deep, and this is
		// the only thing that reaches it from there.
		returnType syntax.Type
		// inMain says the body being emitted is the main method's own, which is
		// what makes a return out of it a return out of the program. A lambda
		// written inside main is a frame like any other and clears this for the
		// length of its body, since returning from one goes back to whatever
		// called it rather than ending the run.
		inMain bool
		// receiverInFrame says slot 0 of the frame holds the receiver itself
		// rather than the closure the call was made through. That is true only of
		// an initializer routine, which is entered by OpDefaultValue rather than
		// by a call and so has no closure to reach through. Every other body gets
		// its receiver out of the closure, which is what OpPushReceiver is for.
		receiverInFrame bool
		// nextVariableOffset is the stack slot the next declared variable takes,
		// past the open-width slots in openSlots.
		nextVariableOffset uint64
		// openSlots is the types of the frame's slots whose width depends on an
		// instantiation, in the order they were declared.
		openSlots []syntax.Type
		// structMethodInstructions is where each instance method of each
		// generic struct concrete begins, keyed by the concrete's type index.
		structMethodInstructions map[int][]uint64
		// byteOffset is where the next emitted operation lands in the
		// operations region, which is what a jump targets.
		byteOffset int
		// mainClosure is the closure built for the main method, held until the
		// body it names has been emitted. Every other static method's body is
		// emitted where its closure is built, but the main method's is what
		// control runs into once the statics are initialized, so it stays where
		// it is and its closure is told where that turned out to be.
		mainClosure *binaryOperation
		// methodBindings is the substitution a generic static method's body
		// is being compiled under. Each concrete instantiation has its own
		// body, and every type the body pushes an id for is read through
		// this map so the method's T is a ground type at every emit site. A
		// non-generic body carries no bindings and the map stays nil.
		methodBindings map[syntax.GenericArgKey]syntax.Type
	}
)

// thisVariableName is the name bound to an instance method's receiver. It is a
// keyword, so no declared variable can shadow it.
const thisVariableName = "this"

// remainingVariableName is bound to the second slot of an initializer's frame,
// which is how many elements are left to fill in. It is not a name a program can
// write, so nothing shadows it and it shadows nothing.
const remainingVariableName = "~remaining"

// A bare Opcode is an operation with no immediate operands.
func (o Opcode) ByteCount() int { return 1 }

// Read implements the [byteCountable] interface.
func (o Opcode) Read(buf []byte) (int, error) {
	if len(buf) < 1 {
		return 0, io.ErrShortBuffer
	}
	buf[0] = byte(o)
	return 1, nil
}

func newUnaryOp(opcode Opcode, operand uint64) *unaryOperation {
	return &unaryOperation{
		opcode:  opcode,
		operand: operand,
	}
}

// ByteCount implements the [byteCountable] interface.
func (o *unaryOperation) ByteCount() int {
	return 1 + wordByteSize
}

// Read implements the [byteCountable] interface.
func (o *unaryOperation) Read(buf []byte) (int, error) {
	if len(buf) < o.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	buf[0] = byte(o.opcode)
	binary.LittleEndian.PutUint64(buf[1:], o.operand)
	return o.ByteCount(), nil
}

func newBinaryOp(opcode Opcode, first, second uint64) *binaryOperation {
	return &binaryOperation{
		opcode: opcode,
		first:  first,
		second: second,
	}
}

// ByteCount implements the [byteCountable] interface.
func (o *binaryOperation) ByteCount() int {
	return 1 + 2*wordByteSize
}

// Read implements the [byteCountable] interface.
func (o *binaryOperation) Read(buf []byte) (int, error) {
	if len(buf) < o.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	buf[0] = byte(o.opcode)
	binary.LittleEndian.PutUint64(buf[1:], o.first)
	binary.LittleEndian.PutUint64(buf[1+wordByteSize:], o.second)
	return o.ByteCount(), nil
}

// newJump builds a branch operation with an unset target.
func newJump(opcode Opcode) *jumpOperation {
	return &jumpOperation{
		opcode: opcode,
	}
}

// ByteCount implements the [byteCountable] interface.
func (j *jumpOperation) ByteCount() int {
	return 1 + wordByteSize
}

// Read implements the [byteCountable] interface.
func (j *jumpOperation) Read(buf []byte) (int, error) {
	if len(buf) < j.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	buf[0] = byte(j.opcode)
	binary.LittleEndian.PutUint64(buf[1:], uint64(j.target))
	return j.ByteCount(), nil
}

// emit appends an operation to the operations region.
func (v *operationVisitor) emit(op operation) {
	v.reader.operations = append(v.reader.operations, op)
	v.byteOffset += op.ByteCount()
}

// emitJump emits a branch to an instruction that has not been emitted yet. The
// returned operation is patched once its target is known.
func (v *operationVisitor) emitJump(opcode Opcode) *jumpOperation {
	jump := newJump(opcode)
	v.emit(jump)
	return jump
}

// emitJumpTo emits a branch to an instruction that has already been emitted,
// which is what a loop's back edge is.
func (v *operationVisitor) emitJumpTo(opcode Opcode, target int) {
	jump := newJump(opcode)
	jump.target = target
	v.emit(jump)
}

// patch points jumps at the next operation to be emitted.
func (v *operationVisitor) patch(jumps ...*jumpOperation) {
	for _, jump := range jumps {
		jump.target = v.byteOffset
	}
}

// enterScope opens a lexical scope in the compiler and at runtime both, so that
// the values the scope owns are discarded when it closes. A method's own scope
// is not one of these: the call establishes the function scope, and the return
// unwinds it.
func (v *operationVisitor) enterScope() {
	v.emit(OpPushScope)
	v.pushScope()
}

// exitScope closes the innermost lexical scope opened by [enterScope].
func (v *operationVisitor) exitScope() {
	v.popScope()
	v.emit(OpPopScope)
}

// pushScope enters a lexical scope. Its variables start at the current slot.
func (v *operationVisitor) pushScope() {
	v.pushScopeOf(map[string]binding{})
}

// pushScopeOf enters a lexical scope holding names that are already bound,
// which is what a declaration's fields and a frame's parameters both are.
func (v *operationVisitor) pushScopeOf(bindings map[string]binding) {
	v.scopes.Push(&variableScope{
		outer:      v.scopes.Peek(),
		variables:  bindings,
		baseOffset: v.nextVariableOffset,
		baseOpen:   len(v.openSlots),
	})
}

// popScope leaves a lexical scope, releasing the slots it declared.
func (v *operationVisitor) popScope() {
	scope, _ := v.scopes.Pop()
	v.nextVariableOffset = scope.baseOffset
	v.openSlots = v.openSlots[:scope.baseOpen]
}

// declareVariable binds name to the next free stack slot in the innermost scope,
// and reserves as many slots as the variable's type is wide: a struct, class or
// enum lies on the stack inline, so it spans more than the one slot a reference
// takes and the next variable begins past all of them.
//
// A name of [syntax.BlankName] reserves its slot but is left out of the lookup
// map: the static pass has already refused to let any expression name it, so
// nothing will look it up, and leaving it out is what lets two `_` in one
// scope share the scope without colliding on the one name.
func (v *operationVisitor) declareVariable(name string, typ syntax.Type) uint64 {
	typ = v.groundType(typ)
	offset := v.nextVariableOffset
	if name != syntax.BlankName {
		open := v.openSlots[:len(v.openSlots):len(v.openSlots)]
		v.scopes.Peek().variables[name] = localBinding{offset: offset, typ: typ, open: open}
	}
	v.reserveSlot(typ)
	return offset
}

// reserveSlot takes room in the frame for a slot of the given type: words of
// its own where its width is fixed, or a place in openSlots where an
// instantiation decides it.
func (v *operationVisitor) reserveSlot(typ syntax.Type) {
	if openWidth(typ) {
		v.openSlots = append(v.openSlots, typ)
		return
	}
	v.nextVariableOffset += stackWords(typ)
}

// groundType applies the bindings of the body being emitted, which is every
// binding a generic struct's or generic static method's body has.
func (v *operationVisitor) groundType(t syntax.Type) syntax.Type {
	if v.methodBindings != nil {
		return t.Map(v.methodBindings)
	}
	return t
}

// openWidth reports whether a value of t is as wide as an instantiation
// decides: a generic argument, or a type laid out inline that mentions one. A
// reference is one word whatever it refers to.
func openWidth(t syntax.Type) bool {
	t = syntax.StripConst(t)
	if !t.ContainsGenericArg() {
		return false
	}
	switch tt := t.(type) {
	case *syntax.TypeGenericArg:
		return true
	case *syntax.TypeArray:
		return true
	case *syntax.TypeWrapped, *syntax.TypeFunction:
		return false
	case syntax.TypeResolvable:
		return laidOutInline(tt)
	}
	return false
}

// emitPushSlot pushes the location of a frame slot, adding the widths of the
// open-width slots ahead of it when there are any.
func (v *operationVisitor) emitPushSlot(b localBinding) {
	if len(b.open) == 0 {
		v.emit(newUnaryOp(OpPushVariable, b.offset))
		return
	}
	for _, t := range b.open {
		v.emitPushTypeID(t)
	}
	v.emit(newBinaryOp(OpPushVariableOpen, b.offset, uint64(len(b.open))))
}

// lookupBinding resolves name to what it stands for, searching innermost outward
// so an inner declaration shadows an outer one of the same name — which is what
// puts a local ahead of a field, and a nested declaration's static ahead of the
// enclosing declaration's. Static analysis rejects unresolvable names, so a miss
// is a compiler bug.
func (v *operationVisitor) lookupBinding(name string) binding {
	for scope := v.scopes.Peek(); scope != nil; scope = scope.outer {
		if b, ok := scope.variables[name]; ok {
			return b
		}
	}
	panic("unreachable")
}

// lookupLocal resolves a name that can only be a frame slot: the receiver, or a
// capture, which static analysis records only for names it resolved to one.
func (v *operationVisitor) lookupLocal(name string) localBinding {
	local, ok := v.findLocal(name)
	if !ok {
		panic("unreachable")
	}
	return local
}

// findLocal resolves name to its slot if the frame holds one for it, which is
// what asking after a receiver comes to: a static method has none.
func (v *operationVisitor) findLocal(name string) (localBinding, bool) {
	for scope := v.scopes.Peek(); scope != nil; scope = scope.outer {
		if b, ok := scope.variables[name]; ok {
			local, isLocal := b.(localBinding)
			return local, isLocal
		}
	}
	return localBinding{}, false
}

// pushDeclScopes puts the fields nameable without qualification in scope: the
// statics of the declaration and of every declaration it is nested inside,
// outermost first so an inner one shadows an outer, and the declaration's own
// instance fields above those when there is a receiver to reach them through.
//
// Only the innermost declaration contributes instance fields. An enclosing
// declaration's instance is not the one the frame was entered on, so there would
// be nothing to reach one of its fields through.
//
// The returned count is how many scopes to leave again.
func (v *operationVisitor) pushDeclScopes(d syntax.Decl, hasReceiver bool) int {
	v.currentDecl = d

	var nesting []syntax.Decl
	for outer := d; outer != nil; outer = outer.OuterDecl() {
		nesting = append(nesting, outer)
	}
	slices.Reverse(nesting)

	for _, outer := range nesting {
		v.pushScopeOf(declStaticScope(outer))
	}
	if !hasReceiver {
		return len(nesting)
	}
	v.pushScopeOf(declInstanceScope(d))
	return len(nesting) + 1
}

// popScopes leaves the scopes [operationVisitor.pushDeclScopes] opened.
func (v *operationVisitor) popScopes(count int) {
	for range count {
		v.popScope()
	}
}

// declStaticScope binds the names a declaration's statics are written under. A
// nested declaration is a static field too, but it names a type rather than a
// value and lies in no slot, so it is not among them.
func declStaticScope(d syntax.Decl) map[string]binding {
	scope := map[string]binding{}
	for name, field := range d.StaticFields() {
		indexable, ok := field.(syntax.FieldIndexable)
		if !ok {
			continue
		}
		if t, ok := staticSlotType(indexable); ok {
			scope[name] = staticBinding{field: indexable, typ: t}
		}
	}
	return scope
}

// declInstanceScope binds the names a declaration's instance fields are written
// under, inherited ones among them, since a field reached through the receiver
// is reached the same way however far up it was declared.
func declInstanceScope(d syntax.Decl) map[string]binding {
	scope := map[string]binding{}
	declTypeable, ok := d.(syntax.DeclTypeable)
	if !ok {
		return scope
	}
	for _, entry := range d.AllInstanceFields().All() {
		field, ok := d.GetInstanceField(entry.Key)
		if !ok {
			continue
		}
		scope[entry.Key] = instanceBinding{decl: declTypeable, field: field}
	}
	return scope
}

// declareParameters binds a frame's parameter names to the slots the caller
// pushed its arguments into. A parameter is as wide as it is, since one lying on
// the stack inline spans every slot its layout covers, so an unnamed one still
// takes the room it was given.
func (v *operationVisitor) declareParameters(names []string, types []syntax.Type) {
	for i, t := range types {
		t = syntax.ParameterBindingType(t)
		if i >= len(names) {
			v.reserveSlot(v.groundType(t))
			continue
		}
		v.declareVariable(names[i], t)
	}
}

// lookupLoop resolves a jump's label to the loop it names, searching innermost
// outward. An empty label names the innermost enclosing loop, which is the loop
// a bare break or continue is written inside of. Static analysis rejects a label
// that names no enclosing loop, so a miss is a compiler bug.
func (v *operationVisitor) lookupLoop(label string) *loopContext {
	for loop := range v.loops.All() {
		if label == "" || loop.label == label {
			return loop
		}
	}
	panic("unreachable")
}

func (b localBinding) bindingType() syntax.Type    { return b.typ }
func (b staticBinding) bindingType() syntax.Type   { return b.typ }
func (b instanceBinding) bindingType() syntax.Type { return b.field.FieldType() }

// selection is how the field an instanceBinding names is picked out of the
// receiver, which is how an explicit selection off an object picks it out too:
// by index, against the declaration the name was written in.
func (b instanceBinding) selection() operation {
	if method, ok := b.field.(*syntax.FieldMethod); ok {
		return newUnaryOp(methodOpcode(b.decl), uint64(*method.Index()))
	}
	return newUnaryOp(OpPushMember, uint64(*b.field.Index()))
}

// emitPushLocal pushes a pointer to the named frame slot, over the variable's
// type, which is what a load or a store through it is measured by — including
// when that type is an open generic argument, whose id [emitPushTypeID] reads
// off the receiver.
func (v *operationVisitor) emitPushLocal(name string) {
	local := v.lookupLocal(name)
	v.emitPushSlot(local)
	v.emitPushTypeID(local.typ)
}

// emitPushReceiver emits `this` as the location it is. In a body reached by a
// call, the receiver is not a frame slot: slot 0 holds the closure the call was
// made through, and the word naming the receiver lies inside that closure, at a
// depth only the closure's kind knows. So the step is left to OpPushReceiver.
//
// An initializer routine is the exception, and [receiverInFrame] is what says
// so: it is entered with the instance in slot 0 and no closure at all, so the
// slot is the receiver and a plain push of it is right.
//
// Either way what comes back is a location loading to the receiver's address,
// over the receiver's own type, so every reader of `this` sees what it always
// saw.
func (v *operationVisitor) emitPushReceiver() {
	if v.receiverInFrame {
		v.emitPushLocal(thisVariableName)
		return
	}
	v.emit(OpPushReceiver)
	v.emitPushTypeID(v.lookupLocal(thisVariableName).typ)
}

// emitPushName emits an unqualified name as what it stands for. A local is its
// slot and a static is its slot in the static region, both of which are already
// locations; a field is selected out of the receiver, which is what leaving the
// receiver unwritten means.
func (v *operationVisitor) emitPushName(name string) {
	switch b := v.lookupBinding(name).(type) {
	case localBinding:
		v.emitPushSlot(b)
		v.emitPushTypeID(b.typ)
	case staticBinding:
		v.emitPushStatic(b.field, b.typ)
		v.emitPushTypeID(b.typ)
	case instanceBinding:
		v.emitPushReceiver()
		v.emit(OpLoad)
		v.emitPushTypeID(b.decl.DeclType())
		v.emit(b.selection())
	}
}

// genericTypeIndex returns an open type's position in its declaration's
// enumeration of open types. Every concrete instantiation stores that
// enumeration resolved through its own bindings, so the position is how the
// runtime recovers the type from the concrete it is executing as.
func genericTypeIndex(t syntax.Type) uint64 {
	d := t.OuterDecl().(syntax.DeclGeneric)
	return uint64(d.GenericTypes().Index(t))
}

// stringLiteralSlot returns the static slot of an interned string literal. Every
// literal is interned by the string literal pass, so a miss is a compiler bug.
func (v *operationVisitor) stringLiteralSlot(s string) uint64 {
	slot, index := v.reader.stringLiterals.Get(stringLiteral(s))
	if index < 0 {
		// Slot 0 is a literal like any other, so a miss that returned it would
		// be emitted as a working push of the wrong string rather than as
		// anything a reader of the image could tell was wrong.
		panic("unreachable")
	}
	return slot
}

// visitDecl dispatches each of d's static fields, nested declarations and
// instance methods back through the visitor so their bodies are emitted. The
// nested declarations recurse into VisitDecl* for their own emit.
func (v *operationVisitor) visitDecl(d syntax.Decl) error {
	for _, f := range d.StaticFields() {
		if err := f.Accept(v); err != nil {
			return err
		}
	}
	for child := range d.Decls() {
		if err := child.Accept(v); err != nil {
			return err
		}
	}
	for _, m := range d.InstanceMethods() {
		if err := m.Accept(v); err != nil {
			return err
		}
	}
	return nil
}

// VisitDeclStruct emits a struct's bodies. A generic struct's instance methods
// are emitted once per concrete, under that concrete's bindings: a struct
// carries no header a shared body could read its instantiation off, so every
// size and type id in the body has to be settled where it is emitted.
func (v *operationVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	if d.Generics() == nil {
		return v.visitDecl(d)
	}
	for _, f := range d.StaticFields() {
		if err := f.Accept(v); err != nil {
			return err
		}
	}
	for child := range d.Decls() {
		if err := child.Accept(v); err != nil {
			return err
		}
	}
	if v.structMethodInstructions == nil {
		v.structMethodInstructions = map[int][]uint64{}
	}
	for _, entry := range d.Concretes().All() {
		concrete, ok := entry.Value.(*syntax.DeclConcreteStruct)
		if !ok {
			continue
		}
		tg, ok := entry.Key.(*syntax.TypeGeneric)
		if !ok || tg.ContainsGenericArg() {
			continue
		}
		bindings := make(map[syntax.GenericArgKey]syntax.Type, d.Generics().Len())
		i := 0
		for _, g := range d.Generics().All() {
			bindings[g.Key] = tg.ParameterTypes[i]
			i++
		}

		cells := make([]uint64, instanceFieldCount[*syntax.FieldMethod](d))
		outer := v.methodBindings
		v.methodBindings = bindings
		for _, m := range d.InstanceMethods() {
			if err := v.emitMethodBody(m, m.FieldType().Map(bindings), &cells[*m.Index()]); err != nil {
				v.methodBindings = outer
				return err
			}
		}
		v.methodBindings = outer
		v.structMethodInstructions[*concrete.TypeIndex()] = cells
	}
	return nil
}
func (v *operationVisitor) VisitDeclEnum(d *syntax.DeclEnum) error           { return v.visitDecl(d) }
func (v *operationVisitor) VisitDeclClass(d *syntax.DeclClass) error         { return v.visitDecl(d) }
func (v *operationVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error   { return v.visitDecl(d) }
func (v *operationVisitor) VisitDeclInterface(d *syntax.DeclInterface) error { return v.visitDecl(d) }
func (v *operationVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error { return v.visitDecl(d) }

// VisitFieldMember emits nothing where a static member is declared, and nothing
// at all for an instance member. A static is initialized before the program
// runs rather than where it was written, which
// [operationVisitor.emitStaticInitializers] has done before this walk begins; an
// instance member is initialized by whatever builds the instance.
func (v *operationVisitor) VisitFieldMember(f *syntax.FieldMember) error { return nil }

// VisitFieldEnum emits nothing where an enum value is declared, for the reason a
// static member emits nothing: the value was built before the program ran.
func (v *operationVisitor) VisitFieldEnum(f *syntax.FieldEnum) error { return nil }

// emitStaticInitializers gives every static in the program the value it starts
// the run with, file by file in path order and, within a file, in the order the
// declarations were written. A static exists for the length of the run and is
// written once, before anything can name it.
//
// Which file goes first is not chosen so much as fixed: there is no dependency
// analysis here, so a static that reads one declared in another file sees an
// initialized value or a zero depending on which file came first. Sorting is
// what keeps that from being a different answer each run.
//
// Being emitted first is what makes it run first: the program begins at the
// first operation, so these lie ahead of the main method's own body and control
// falls out of the last of them into it. They leave the stack as they found it,
// which is the frame the main method was entered on.
//
// Every static method in the program is built before any other static is
// initialized. Building one reads nothing, so it can go first, and going first
// is what lets any initializer call any static method wherever it was written —
// an enum's values most of all, which are always written ahead of the enum's
// own methods.
func (v *operationVisitor) emitStaticInitializers() error {
	for _, methods := range []bool{true, false} {
		for _, entry := range v.ast.CodeFiles.All() {
			if err := v.emitDeclStaticInitializers(entry.Value.Decl, methods); err != nil {
				return err
			}
		}
	}
	return nil
}

// emitDeclStaticInitializers initializes either the static methods or the other
// statics a declaration holds and those the declarations nested inside it hold,
// in the order they were written. A nested declaration is a static field of the
// one it lies in, which is what makes the walk a single loop.
func (v *operationVisitor) emitDeclStaticInitializers(d syntax.Decl, methods bool) error {
	for _, f := range d.StaticFields() {
		switch f := f.(type) {
		case *syntax.FieldMethod:
			if !methods {
				continue
			}
			if err := v.emitStaticMethod(f); err != nil {
				return err
			}
		case *syntax.FieldMember:
			if methods {
				continue
			}
			if err := v.emitStaticMember(f); err != nil {
				return err
			}
		case *syntax.FieldEnum:
			if methods {
				continue
			}
			if err := v.emitEnumValue(f); err != nil {
				return err
			}
		}
	}
	for child := range d.Decls() {
		if err := v.emitDeclStaticInitializers(child, methods); err != nil {
			return err
		}
	}
	return nil
}

// emitStaticMethod builds a static method's closure into the slot the method was
// laid out in, so that naming the method later is reading that slot rather than
// building anything, the same as naming any other static.
//
// The closure captures nothing and carries no receiver, a static method having
// none, so it is the method's body and its type and no more. The body is emitted
// here with a jump around it, as a lambda's is, which is what makes the offset
// the closure is built with known as soon as the body begins; the walk that
// follows leaves static methods alone rather than emitting them a second time.
//
// A generic method is monomorphized: one closure and one body are built per
// concrete instantiation, each in its own slot and each compiled under the
// bindings that make the method's own T a ground type at every push site.
func (v *operationVisitor) emitStaticMethod(f *syntax.FieldMethod) error {
	if f.Generics() != nil {
		for _, concrete := range f.Concretes() {
			if err := v.emitStaticMethodConcrete(f, concrete); err != nil {
				return err
			}
		}
		return nil
	}
	return v.emitStaticMethodBody(f, f.FieldType(), *f.Index(), f.Instruction())
}

// emitStaticMethodConcrete builds the closure and body for one instantiation of
// a generic static method. The method's bindings are set for the duration of
// the body walk so emitPushTypeID resolves the method's T to the instantiation's
// ground type at every push.
func (v *operationVisitor) emitStaticMethodConcrete(f *syntax.FieldMethod, concrete *syntax.MethodConcrete) error {
	outer := v.methodBindings
	v.methodBindings = concrete.Bindings()
	defer func() { v.methodBindings = outer }()
	return v.emitStaticMethodBody(f, concrete.FieldType(), *concrete.Index(), concrete.Instruction())
}

// emitStaticMethodBody is the shared tail of the static method emit: push the
// slot location, build an empty closure placeholder, and jump the body it will
// come to name once the body has been emitted and the closure's offset filled
// in. The main method's closure is handed back to the driver, which patches it
// after the main body has been laid down between the statics and the walk.
func (v *operationVisitor) emitStaticMethodBody(f *syntax.FieldMethod, methodType syntax.Type, slot int, instruction *uint64) error {
	v.emitPushStaticSlot(slot, methodType)
	v.emitPushTypeID(methodType)
	v.emitPushTypeID(methodType)
	closure := newBinaryOp(OpNewLambda, 0, 0)
	v.emit(closure)
	v.emitPushTypeID(methodType)
	v.emit(OpStore)
	v.emitPopLocation()

	// The main method is the one body control runs into rather than one it is
	// sent to, so it is left where it lies and its closure is told where that
	// is once it has been emitted.
	if f == v.mainMethod {
		v.mainClosure = closure
		return nil
	}

	over := v.emitJump(OpJump)
	closure.first = uint64(v.byteOffset)
	if err := v.emitMethodBody(f, methodType, instruction); err != nil {
		return err
	}
	v.patch(over)
	return nil
}

// emitStaticMember gives one static member its starting value, stored into the
// slot the member was laid out in. The slot the store hands back is dropped,
// since the value is already there and nothing here is written for anything to
// read.
//
// A member written with an expression is that expression. One written with none
// is what anything written with none is, and it is created the same way, so that
// a static begins the run holding what a variable holds where it is declared.
func (v *operationVisitor) emitStaticMember(f *syntax.FieldMember) error {
	memberType := f.FieldType()
	defer v.popScopes(v.pushDeclScopes(f.OuterDecl(), false))

	v.emitPushStatic(f, memberType)
	v.emitPushTypeID(memberType)
	if f.Expr == nil {
		v.emitDefaultValue(memberType)
	} else if err := v.emitValueAs(f.Expr, memberType); err != nil {
		return err
	}

	v.emitPushTypeID(memberType)
	v.emit(OpStore)
	v.emitPopLocation()
	return nil
}

// emitEnumValue builds one enum value in the slot that value was laid out in. An
// enum value is an instance lying in its slot rather than a reference to one
// somewhere else, and it is created the way anything is: from what was written
// for it, and where nothing was written, from its type.
//
// After the default value is in place, the value's discriminant is written into
// the one word at the head of the slot (see [instanceHeaderByteSizeOf] for
// KindEnum and [enumDiscriminant] for what it holds). The
// runtime's equality instruction reads this word to tell BigEndian from
// LittleEndian — without it, two member-less enum values would be bitwise
// identical and compare equal — and [OpEnumValue] reads it to find the slot a
// primitive-backed value's primitive is held in, which is set here as well.
func (v *operationVisitor) emitEnumValue(f *syntax.FieldEnum) error {
	enum := f.OuterDecl().(*syntax.DeclEnum)
	enumType := enum.DeclType()
	defer v.popScopes(v.pushDeclScopes(enum, false))

	v.emitPushStatic(f, enumType)
	v.emitPushTypeID(enumType)
	v.emitDefaultValue(enumType)
	v.emitPushTypeID(enumType)
	v.emit(OpStore)
	v.emitPopLocation()

	// The slot is reinterpreted as a uint for this store so the discriminant
	// lands at offset zero, where equalValues and OpEnumValue read it.
	v.emitPushStaticSlot(*f.Index(), syntax.TypeUint)
	v.emitPushTypeID(syntax.TypeUint)
	v.emitPushWords(enumDiscriminant(f))
	v.emitPushTypeID(syntax.TypeUint)
	v.emit(OpStore)
	v.emitPopLocation()

	if f.Value != nil {
		primitive := enum.Primitive()
		v.emitPushStaticSlot(*f.ValueIndex(), primitive)
		v.emitPushTypeID(primitive)
		if err := v.emitValueAs(f.Value, primitive); err != nil {
			return err
		}
		v.emitPushTypeID(primitive)
		v.emit(OpStore)
		v.emitPopLocation()
		return nil
	}
	if f.Init == nil {
		return nil
	}
	return v.emitEnumInitializer(f, enum, enumType)
}

// emitEnumInitializer runs what an enum value names, for its effects alone. The
// initializer is an instance method called on the slot the value already lies
// in, so it sets the value up by assigning through its receiver; whatever it
// returns is discarded, which is what lets one be written to return anything at
// all.
func (v *operationVisitor) emitEnumInitializer(f *syntax.FieldEnum, enum *syntax.DeclEnum, enumType syntax.Type) error {
	method, ok := enum.GetInstanceMethod(*f.Init)
	if !ok {
		panic("unreachable")
	}

	v.emitPushStatic(f, enumType)
	v.emitPushTypeID(syntax.NewWrappedType(syntax.TypeKindPointer, enumType))
	v.emit(newUnaryOp(OpPushMethod, uint64(*method.Index())))

	ft := method.FieldType().(*syntax.TypeFunction)
	frameWords := uint64(1)
	for i, parameter := range ft.ParameterTypes {
		if err := v.emitValueAs(f.Parameters[i], parameter); err != nil {
			return err
		}
		frameWords += stackWords(parameter)
	}

	v.emit(newUnaryOp(OpCallMethod, frameWords))
	if ft.ReturnType.Kind() != syntax.TypeKindVoid {
		v.emitPushTypeID(ft.ReturnType)
		v.emit(OpPop)
	}
	return nil
}

// emitPushStatic pushes a pointer to where a static field lies. Every static
// holds a slot of its own in the static region, laid out wide enough for the
// value it holds, so that pointer is what a store writes through and what a
// method is called on.
func (v *operationVisitor) emitPushStatic(f syntax.FieldIndexable, t syntax.Type) {
	v.emitPushStaticSlot(*f.Index(), t)
}

// emitPushStaticSlot is the primitive beneath emitPushStatic: push a pointer
// to a static region slot by its laid-out slot index, under the type the slot
// is read as. A concrete method instantiation names its slot directly rather
// than through a FieldIndexable, so this is what it emits through.
func (v *operationVisitor) emitPushStaticSlot(slot int, t syntax.Type) {
	typeID := uint64(v.typeInstanceOf(t).typeID)
	v.emit(newBinaryOp(OpPushStatic, uint64(slot), typeID))
}

// emitPushGenericStaticMethod pushes the slot of a generic static method whose
// type arguments are not all ground at compile time: the call site writes a
// reference to the method under a binding that is itself open, so which of
// the method's concretes answers is a question for run time.
//
// The call site's type arguments are pushed first in source order — each may
// contain a class-level T, which emitPushTypeID resolves through the
// receiver — and OpPushGenericStaticSlot pops them and searches the method's
// registry entry. The result stands as if OpPushStatic plus a push of the
// method's function type had emitted it, which is what makes the sequence a
// drop-in replacement.
func (v *operationVisitor) emitPushGenericStaticMethod(method *syntax.FieldMethod, typeArgs []syntax.Type) {
	for _, ta := range typeArgs {
		v.emitPushTypeID(ta)
	}
	v.emit(newUnaryOp(OpPushGenericStaticSlot, uint64(v.genericStaticMethodID(method))))
}

// genericStaticMethodID returns the id a generic static method is addressed by
// in the runtime dispatch registry, assigning one the first time the method is
// seen. Every reference to the same method lands at the same id, so the two
// call sites in different generic contexts share the one registry entry.
//
// The entry is populated here from the method's concretes rather than from a
// separate pass. By the time an operation names a generic method, every
// concrete the program can reach has been materialized — the generic-decl
// closure pass runs before this one — so the list is final.
func (v *operationVisitor) genericStaticMethodID(method *syntax.FieldMethod) uint16 {
	if id, ok := v.reader.genericStaticMethodIDs[method]; ok {
		return id
	}

	id := uint16(len(v.reader.genericStaticMethods))
	v.reader.genericStaticMethodIDs[method] = id

	entry := genericStaticMethodEntry{
		arity: uint64(method.Generics().Len()),
	}
	for _, concrete := range method.Concretes() {
		row := genericStaticMethodConcrete{
			typeArgs: make([]typeInstance, 0, len(concrete.TypeArgs())),
			slot:     uint64(*concrete.Index()),
			fnType:   v.typeInstanceOf(concrete.FieldType()),
		}
		for _, ta := range concrete.TypeArgs() {
			row.typeArgs = append(row.typeArgs, v.typeInstanceOf(ta))
		}
		entry.concretes = append(entry.concretes, row)
	}
	v.reader.genericStaticMethods = append(v.reader.genericStaticMethods, entry)
	return id
}

// emitPopLocation discards a location: the pointer and the three words of the
// type standing over it. It is what a store written for its effect leaves
// behind, the value being already where it was written to.
func (v *operationVisitor) emitPopLocation() {
	for range locationWords {
		v.emit(OpPopWord)
	}
}

// emitLoadWord reads a frame slot as a plain word, and emitStoreWord writes one
// back. Both name the slot as an int whatever it actually holds, which is the
// point of them: an initializer's cursor is address-shaped and its count is a
// count, and neither is a value the collector should be told of. Naming the slot
// as the pointer it is would have the load push a mark entry over a frame
// location, which is the one word that must never own one.
//
// They are only for these two slots. Nothing a program writes goes through here
// — a declared variable is read and written as the type it was declared with.
func (v *operationVisitor) emitLoadWord(slot uint64) {
	v.emit(newUnaryOp(OpPushVariable, slot))
	v.emitPushTypeID(syntax.TypeInt)
	v.emit(OpLoad)
}

// emitStoreWord writes what value leaves on the stack into the slot. The store
// leaves the location standing, as every store does, so the four words it is are
// dropped after.
func (v *operationVisitor) emitStoreWord(slot uint64, value func()) {
	v.emit(newUnaryOp(OpPushVariable, slot))
	v.emitPushTypeID(syntax.TypeInt)
	value()
	v.emitPushTypeID(syntax.TypeInt)
	v.emit(OpStore)
	v.emitPopLocation()
}

// emitDefaultValue creates a value of the given type out of nothing but the
// type, which is what a declaration written with no expression comes to. The
// type is the whole of what such a value needs to be created from — how wide it
// is, what lies within it, and whether the collector is told of it are all the
// type's to say — so the three words of the type id are the value, and every
// declaration written with nothing is created alike whatever its type.
func (v *operationVisitor) emitDefaultValue(t syntax.Type) {
	v.emitPushTypeID(t)
	v.emit(OpDefaultValue)
}

// VisitFieldMethod emits the method's body. A static method is skipped here
// because it was emitted where its closure was built, before anything could name
// it, and the main method because it is emitted between the two so that
// execution runs into it once the statics are initialized.
func (v *operationVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	if f.Modifiers().Has(syntax.ModifierStatic) {
		return nil
	}
	return v.emitMethod(f)
}

// emitMethod emits the operations for a single method body. Slot 0 of a frame
// holds the closure the call was made through and the parameters follow it, so
// the body's variables start after them. An instance method names that slot as
// its receiver; a static method never names it, but the slot is there all the
// same, so that one calling convention serves both.
//
// The receiver slot is one word whatever the declaration's own width, because a
// closure is a reference and a closure is what the caller pushes there.
//
// A body that runs off its end returns all the same, since the frame it was
// called on has to be unwound however control leaves it. Only a method owing no
// value can run off its end — static analysis rejects a path that leaves a
// method owing one without giving it — so the end falls into a return of
// nothing.
//
// Where the body turned out to lie is recorded on the field, which is what the
// method tables are filled from once every body has been emitted. A method
// declared without one is left naming offset zero, since an abstract or
// interface method is dispatched to through the record of the class the instance
// actually is and never through the record that declared it.
func (v *operationVisitor) emitMethod(f *syntax.FieldMethod) error {
	return v.emitMethodBody(f, f.FieldType(), f.Instruction())
}

// emitMethodBody emits the operations for a method body under a specific
// signature and records where the body ends up in the given instruction cell.
// A non-generic method emits its own signature and writes its own instruction
// field; a generic method's concrete instantiation emits the signature its
// bindings resolve to and writes the concrete's instruction cell, so the
// method tables name the right offset for each monomorphization.
func (v *operationVisitor) emitMethodBody(f *syntax.FieldMethod, methodType syntax.Type, instruction *uint64) error {
	if f.Body == nil {
		return nil
	}
	*instruction = uint64(v.byteOffset)

	ft := methodType.(*syntax.TypeFunction)
	v.returnType = ft.ReturnType
	v.inMain = f == v.mainMethod
	v.receiverInFrame = false
	v.nextVariableOffset = 0
	v.openSlots = nil

	instance := !f.Modifiers().Has(syntax.ModifierStatic)
	defer v.popScopes(v.pushDeclScopes(f.OuterDecl(), instance))

	v.pushScope()
	defer v.popScope()

	if instance {
		v.declareVariable(thisVariableName, receiverType(f.OuterDecl()))
	}
	v.nextVariableOffset = 1
	v.declareParameters(f.Parameters, ft.ParameterTypes)

	if err := f.Body.Accept(v); err != nil {
		return err
	}

	if ft.ReturnType.Kind() == syntax.TypeKindVoid {
		v.emitReturnVoid()
	}
	return nil
}

// emitInstanceInitializers gives every declared type a routine that fills a
// fresh instance of it in, and records on the type's own definition where that
// routine begins. What an instance starts out holding is the type's to say, the
// way its width and its members' offsets already are, so an instance is
// initialized from its type id rather than at the place it was created. That is
// what carries the initialization to the places a value is made without a
// creation site of its own: every element of an array is the same type, so the
// same routine runs over each of them in turn, and a member of a declared type
// is created by running that type's routine in turn.
//
// The routines are emitted last, past every method body, where nothing runs into
// them. A routine is reached by being called and is left by returning.
func (v *operationVisitor) emitInstanceInitializers() error {
	for _, entry := range v.ast.CodeFiles.All() {
		if err := v.emitDeclInstanceInitializers(entry.Value.Decl); err != nil {
			return err
		}
	}
	return nil
}

// emitDeclInstanceInitializers emits the initializer of a declaration and of the
// declarations nested inside it. A type whose instances have no members to fill
// in gets no routine at all and is left naming offset 0, which is where the
// program begins and so is never an initializer — that is what lets the runtime
// read 0 as nothing to run rather than having to call a routine that returns as
// soon as it is entered.
func (v *operationVisitor) emitDeclInstanceInitializers(d syntax.Decl) error {
	switch d.Kind() {
	case syntax.KindStruct, syntax.KindClass, syntax.KindEnum, syntax.KindAbstract:
		if instanceFieldCount[*syntax.FieldMember](d) > 0 {
			// A generic struct is monomorphized at the type-definition layer:
			// each concrete carries its own instance byte size and its own
			// definition, so its initializer must be its own too. A struct
			// value has no mark header for OpPushReceiverType to read, which
			// is how a shared template body would resolve an open generic
			// against the receiver — the walk returns a garbage shortID
			// (usually zero, i.e. void) and the push dies. Giving each
			// concrete its own body under the concrete's bindings keeps every
			// emitPushTypeID in the walk a compile-time constant.
			if gs, ok := d.(*syntax.DeclStruct); ok && gs.Generics() != nil {
				if err := v.emitStructConcreteInitializers(gs); err != nil {
					return err
				}
				break
			}
			offset := uint64(v.byteOffset)
			if err := v.emitInstanceInitializer(d); err != nil {
				return err
			}
			v.recordInitializer(d, offset)
		}
	}

	for child := range d.Decls() {
		if err := v.emitDeclInstanceInitializers(child); err != nil {
			return err
		}
	}
	return nil
}

// emitStructConcreteInitializers emits one initializer body per concrete of a
// generic struct, each walked under that concrete's bindings so every type
// push in the body resolves to a ground id at compile time. The per-concrete
// instruction offset is written straight into the concrete's type definition,
// since no two concretes share a body.
func (v *operationVisitor) emitStructConcreteInitializers(d *syntax.DeclStruct) error {
	r := v.reader
	for _, entry := range d.Concretes().All() {
		concrete, ok := entry.Value.(*syntax.DeclConcreteStruct)
		if !ok {
			continue
		}
		tg, ok := entry.Key.(*syntax.TypeGeneric)
		if !ok {
			continue
		}
		// TypeGeneric.Decl() creates a concrete entry for every TypeGeneric it is
		// called on, including open ones; those entries name the template itself
		// and have no body of their own to emit, so skip them.
		if tg.ContainsGenericArg() {
			continue
		}
		bindings := make(map[syntax.GenericArgKey]syntax.Type, d.Generics().Len())
		i := 0
		for _, g := range d.Generics().All() {
			bindings[g.Key] = tg.ParameterTypes[i]
			i++
		}

		outer := v.methodBindings
		v.methodBindings = bindings
		offset := uint64(v.byteOffset)
		err := v.emitInstanceInitializer(d)
		v.methodBindings = outer
		if err != nil {
			return err
		}

		r.genericStructDefinitions[*concrete.TypeIndex()].(*typeDefinitionStruct).initializerInstruction = offset
	}
	return nil
}

// emitInstanceInitializer emits one type's initializer: every member an instance
// has, in the order it is laid out in, given what was written for it. Inherited
// members are laid out ahead of declared ones, so an abstract parent's members
// are filled in before the class's own without the routine having to reach for
// the parent's — the order is already the answer.
//
// A member written with no expression is created from its type, the way anything
// written with none is, which is what carries the initialization inward: a
// member of a declared type is made by running that type's own routine.
//
// The frame is a method's frame, since that is the one calling convention there
// is, except in what its first two slots hold: this routine is entered by
// OpDefaultValue or OpNew rather than by a call, so there is no closure to put
// in slot 0 and the slot holds the instance itself. That is the one body whose
// receiver really is the slot, which is what
// [operationVisitor.receiverInFrame] says.
//
// Slot 1 is how many instances are to be filled in, and the routine is a loop
// over them. That is where the count has to be: a fresh value may be a run of
// elements and every one of them is filled in alike, but the routine is entered
// by laying a frame and jumping, so the instruction that entered it is left
// rather than run and could not come back to lay a second frame. A loop is
// therefore the one place the repetition can live, and a loop is compiled.
//
// The body below is written as if there were only ever one element, because slot
// 0 is a cursor the loop advances rather than a fixed address — so `this` means
// the element being filled in, whichever turn it is.
func (v *operationVisitor) emitInstanceInitializer(d syntax.Decl) error {
	declType := d.(syntax.DeclTypeable).DeclType()

	v.returnType = syntax.TypeVoid
	v.inMain = false
	v.receiverInFrame = true
	v.nextVariableOffset = 0
	v.openSlots = nil
	defer v.popScopes(v.pushDeclScopes(d, true))

	v.pushScope()
	defer v.popScope()

	cursor := v.declareVariable(thisVariableName, receiverType(d))
	remaining := v.declareVariable(remainingVariableName, syntax.TypeInt)

	// The condition ahead of the body rather than behind it, which is the shape
	// every other loop here has and which makes a count of zero mean no turns
	// instead of one.
	top := v.byteOffset
	v.emitLoadWord(remaining)
	v.emit(OpIsPositiveI64)
	done := v.emitJump(OpJumpIfFalse)

	for _, m := range instanceFields[*syntax.FieldMember](d) {
		memberType := m.FieldType()

		// The member's location, reached the way a field selection off the
		// receiver reaches it: the instance under its own type id, which is the
		// pair the member's index is resolved against. What OpPushMember leaves
		// is that member's own location, which is the pair the store spends.
		v.emitPushReceiver()
		v.emit(OpLoad)
		v.emitPushTypeID(declType)
		v.emit(newUnaryOp(OpPushMember, uint64(*m.Index())))

		if m.Expr == nil {
			v.emitDefaultValue(memberType)
		} else if err := v.emitValueAs(m.Expr, memberType); err != nil {
			return err
		}

		v.emitPushTypeID(memberType)
		v.emit(OpStore)
		v.emitPopLocation()
	}

	// The step. The cursor moves by one element's width, which is asked of the
	// type id rather than written down here: an instantiation of a template is as
	// wide as its arguments make it, and a constant baked in now would be the
	// wrong number for every instantiation but one.
	v.emitStoreWord(cursor, func() {
		v.emitLoadWord(cursor)
		v.emitPushTypeID(declType)
		v.emit(OpSizeOf)
		v.emit(OpAddI64)
	})
	v.emitStoreWord(remaining, func() {
		v.emitLoadWord(remaining)
		v.emitPushWords(1)
		v.emit(OpSubI64)
	})
	v.emitJumpTo(OpJump, top)

	v.patch(done)
	v.emit(OpReturnVoid)
	return nil
}

// recordInitializer points a declaration's type definition at its initializer.
// A generic class or abstract is one definition holding every instantiation, and
// a generic struct is one definition apiece, but the routine is emitted once for
// the template either way, so every concrete of a struct names the same offset.
func (v *operationVisitor) recordInitializer(d syntax.Decl, offset uint64) {
	r := v.reader
	switch d := d.(type) {
	case *syntax.DeclStruct:
		if d.Generics() != nil {
			for _, entry := range d.Concretes().All() {
				r.genericStructDefinitions[*entry.Value.TypeIndex()].(*typeDefinitionStruct).initializerInstruction = offset
			}
			return
		}
		r.structDefinitions[*d.TypeIndex()].(*typeDefinitionStruct).initializerInstruction = offset
	case *syntax.DeclEnum:
		r.enumDefinitions[*d.TypeIndex()].(*typeDefinitionEnum).initializerInstruction = offset
	case *syntax.DeclClass:
		if d.Generics() != nil {
			r.genericClassDefinitions[*d.TypeIndex()].(*typeDefinitionGenericClass).initializerInstruction = offset
			return
		}
		r.classDefinitions[*d.TypeIndex()].(*typeDefinitionClass).initializerInstruction = offset
	case *syntax.DeclAbstract:
		if d.Generics() != nil {
			r.genericAbstractDefinitions[*d.TypeIndex()].(*typeDefinitionGenericAbstract).initializerInstruction = offset
			return
		}
		r.abstractDefinitions[*d.TypeIndex()].(*typeDefinitionAbstract).initializerInstruction = offset
	}
}

// recordMethodInstructions points every declared type's method table at the
// bodies those methods were emitted as. It runs once every body has been
// emitted, since none of the offsets is knowable before then, and it is the
// method half of what fillMethods did for the types: that pass could resolve a
// method's signature where it stood, but not where its body would land.
//
// It walks the declarations rather than the bodies because a table holds an
// entry for every method an instance has rather than for every method the
// declaration wrote. An inherited method is in the subclass's table as well as
// in the base's, and it is the same field in both, so filling each table from
// what an instance of that type actually has reaches it in each of them.
func (v *operationVisitor) recordMethodInstructions() {
	for _, entry := range v.ast.CodeFiles.All() {
		v.recordDeclMethodInstructions(entry.Value.Decl)
	}
}

// recordDeclMethodInstructions fills one declaration's table and those of the
// declarations nested inside it. A generic class or abstract is one definition
// holding every instantiation, whose method bodies are shared. A generic
// struct is one definition apiece, each naming the bodies emitted for it.
func (v *operationVisitor) recordDeclMethodInstructions(d syntax.Decl) {
	r := v.reader
	switch d := d.(type) {
	case *syntax.DeclStruct:
		if d.Generics() != nil {
			for _, entry := range d.Concretes().All() {
				index := *entry.Value.TypeIndex()
				def := r.genericStructDefinitions[index].(*typeDefinitionStruct)
				for i, instruction := range v.structMethodInstructions[index] {
					def.instanceMethods[i].instruction = instruction
				}
			}
			break
		}
		fillMethodDefinitionInstructions(r.structDefinitions[*d.TypeIndex()].(*typeDefinitionStruct).instanceMethods, d)
	case *syntax.DeclEnum:
		fillMethodDefinitionInstructions(r.enumDefinitions[*d.TypeIndex()].(*typeDefinitionEnum).instanceMethods, d)
	case *syntax.DeclClass:
		if d.Generics() != nil {
			fillMethodInstructions(r.genericClassDefinitions[*d.TypeIndex()].(*typeDefinitionGenericClass).instanceMethodInstructions, d)
			break
		}
		fillMethodDefinitionInstructions(r.classDefinitions[*d.TypeIndex()].(*typeDefinitionClass).instanceMethods, d)
	case *syntax.DeclAbstract:
		if d.Generics() != nil {
			fillMethodInstructions(r.genericAbstractDefinitions[*d.TypeIndex()].(*typeDefinitionGenericAbstract).instanceMethodInstructions, d)
			break
		}
		fillMethodDefinitionInstructions(r.abstractDefinitions[*d.TypeIndex()].(*typeDefinitionAbstract).instanceMethods, d)
	}

	for child := range d.Decls() {
		v.recordDeclMethodInstructions(child)
	}
}

// fillMethodDefinitionInstructions writes each instance method's body offset
// into the table slot its index names, alongside the signature fillMethods put
// there earlier.
func fillMethodDefinitionInstructions(dst []methodDefinition, d syntax.Decl) {
	for _, m := range instanceFields[*syntax.FieldMethod](d) {
		dst[*m.Index()].instruction = *m.Instruction()
	}
}

// fillMethodInstructions is the same for a generic declaration, whose table is
// offsets alone. One instantiation's method body is every instantiation's, an
// index meaning the same thing in each, so the offsets are held once for the
// definition rather than once per concrete.
func fillMethodInstructions(dst []uint64, d syntax.Decl) {
	for _, m := range instanceFields[*syntax.FieldMethod](d) {
		dst[*m.Index()] = *m.Instruction()
	}
}

// The statement visitor walks a method body's control flow and dispatches every
// embedded expression to the expression visitor.

func (v *operationVisitor) VisitStmtBlock(s *syntax.StmtBlock) error {
	v.enterScope()
	defer v.exitScope()
	for _, stmt := range s.Stmts {
		if err := stmt.Accept(v); err != nil {
			return err
		}
	}
	return nil
}

// VisitStmtExpr emits an expression evaluated for its effect rather than its
// value, so the value it leaves behind is discarded. The discard is told the
// value's type because that is what says whether the word it drops is a
// reference the mark stack is holding — including when the type is a generic
// argument the template has not bound, which [emitPushTypeID] reads off the
// receiver at runtime. A void expression leaves nothing to discard. The other
// kinds this visitor serves consume their operand themselves: a return hands it
// to the caller, a delete frees what it addresses, and mark is left to its own
// emission. Only a return may have no operand at all, being the one statement
// that means something without one.
func (v *operationVisitor) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Kind() == syntax.StmtKindReturn {
		return v.emitReturn(s.Expr)
	}
	if s.Expr == nil {
		return nil
	}
	if s.Kind() == syntax.StmtKindDelete {
		return v.emitDelete(s.Expr)
	}
	if s.Kind() == syntax.StmtKindFree {
		return v.emitFree(s.Expr)
	}
	if s.Kind() == syntax.StmtKindMark {
		return v.emitMark(s.Expr)
	}

	t := s.Expr.ResolvedType()
	if s.Kind() != syntax.StmtKindExpr || t.Kind() == syntax.TypeKindVoid {
		return s.Expr.Accept(v)
	}

	if err := v.emitValue(s.Expr); err != nil {
		return err
	}

	v.emitPushTypeID(t)
	v.emit(OpPop)
	return nil
}

// emitReturn leaves a method, and what it leaves behind is what the call it was
// reached by evaluates to. The value is described by the method's return type
// rather than by its own: the caller reads those words back as the type the
// method was declared with, so that is the type which has to describe them.
// Pushing an id rather than counting the words here is what lets a body emitted
// once for a whole template return a value whose width no instantiation has
// settled yet.
//
// A method owing no value returns none, and whatever expression its return was
// written with is emitted for what it does. Nothing has to be emitted to close
// the scopes the return lies inside of, since the frame they belong to is
// unwound whole.
func (v *operationVisitor) emitReturn(value syntax.Expr) error {
	if v.returnType.Kind() == syntax.TypeKindVoid {
		if value != nil {
			if err := value.Accept(v); err != nil {
				return err
			}
		}

		v.emitReturnVoid()
		return nil
	}

	if err := v.emitValueAs(value, v.returnType); err != nil {
		return err
	}

	v.emitPushTypeID(v.returnType)
	v.emit(OpReturn)
	return nil
}

// emitReturnVoid leaves a body that owes no value. Leaving the main method is
// leaving the program, since it is the one body control ran into rather than was
// sent to: there is no frame under it to hand the stack back to, and the
// instruction after its last is the next method's body, which a fall-through
// would enter on main's frame.
//
// The status is pushed here rather than taken from the program. The main method
// is declared to give nothing back, so reaching the end of it is all a run says
// about itself, and zero is what that says.
func (v *operationVisitor) emitReturnVoid() {
	if !v.inMain {
		v.emit(OpReturnVoid)
		return
	}

	v.emit(newUnaryOp(OpPushI64, 0))
	v.emit(OpExit)
}

// emitDelete runs the Delete override on what a pointer addresses. The pointer
// goes down under its own type id because the dispatch is a question only the
// type can answer: a class, abstract, or interface pointer reaches its own
// Delete slot, and everything else is a receiver no override reaches and so
// does nothing. Memory release is not here, which is emitFree's.
func (v *operationVisitor) emitDelete(pointer syntax.Expr) error {
	if err := v.emitValue(pointer); err != nil {
		return err
	}

	v.emitPushTypeID(pointer.ResolvedType())
	v.emit(OpDelete)
	return nil
}

// emitFree releases what a pointer addresses. Like emitDelete the pointer is
// told under its own type id, since whether the pointee is collector-owned —
// which has to be marked dead for the next sweep — or program-owned — which is
// raw-freed — is the type's to say and not the statement's.
func (v *operationVisitor) emitFree(pointer syntax.Expr) error {
	if err := v.emitValue(pointer); err != nil {
		return err
	}

	v.emitPushTypeID(pointer.ResolvedType())
	v.emit(OpFree)
	return nil
}

// emitMark hands a value to the collector to be traced. The value goes down
// under its own type id for the reason a delete does: what a value reaches is
// the type's to say, not the statement's, and only the id can say it for a type
// an instantiation has yet to bind. Nothing is left behind, a mark being a
// statement about the value rather than a value of its own.
func (v *operationVisitor) emitMark(object syntax.Expr) error {
	if err := v.emitValue(object); err != nil {
		return err
	}

	v.emitPushTypeID(object.ResolvedType())
	v.emit(OpMark)
	return nil
}

// VisitStmtVariable declares a variable, which is the slots it holds and the
// value that goes in them. The value is what was written for it, and where
// nothing was written it is created from its type, the way a static declared
// with nothing is: a declaration means the same thing wherever it is written.
func (v *operationVisitor) VisitStmtVariable(s *syntax.StmtVariable) error {
	v.declareVariable(s.Name(), s.Type)
	if s.Expr == nil {
		v.emitDefaultValue(s.Type)
		return nil
	}
	return v.emitValueAs(s.Expr, s.Type)
}

// visitScoped visits a statement in its own scope, so that a body which is a
// bare declaration rather than a block does not leak its variable outward. A
// block is a scope already, and is visited as it stands.
func (v *operationVisitor) visitScoped(s syntax.Stmt) error {
	if s.Kind() == syntax.StmtKindBlock {
		return s.Accept(v)
	}
	v.enterScope()
	defer v.exitScope()
	return s.Accept(v)
}

func (v *operationVisitor) VisitStmtIf(s *syntax.StmtIf) error {
	if err := v.emitValue(s.Condition); err != nil {
		return err
	}
	otherwise := v.emitJump(OpJumpIfFalse)

	if err := v.visitScoped(s.Then); err != nil {
		return err
	}
	if s.Else == nil {
		v.patch(otherwise)
		return nil
	}

	end := v.emitJump(OpJump)
	v.patch(otherwise)
	if err := v.visitScoped(s.Else); err != nil {
		return err
	}

	v.patch(end)
	return nil
}

// VisitStmtFor emits a loop as a test at the top, the body, and then the step
// the next iteration starts from. Any label left pending names this loop for the
// jumps written inside it: a continue lands on the step, and a break lands past
// the else clause, which is what makes a loop that was broken out of skip it.
func (v *operationVisitor) VisitStmtFor(s *syntax.StmtFor) error {
	loop := &loopContext{label: v.pendingLabel, scopeDepth: v.scopes.Len()}
	v.pendingLabel = ""

	exhausted, err := v.emitLoop(s, loop)
	if err != nil {
		return err
	}

	// The else is emitted with the loop already left, so a jump written there
	// reaches the loop enclosing this one — the same loop it would reach written
	// just after the whole statement. That is also what keeps the continues
	// patched above complete: nothing can be added to them after they are
	// resolved, since nothing inside the else can still name this loop.
	v.patch(exhausted)
	if s.Else != nil {
		if err := v.visitScoped(s.Else); err != nil {
			return err
		}
	}

	v.patch(loop.breaks...)
	return nil
}

// emitLoop emits the loop proper — the condition, the body, the step and the
// back edge — for as long as the loop is the one a jump inside it names. The
// branch it returns is the one taken when the condition fails, which lands on
// whatever follows the loop.
func (v *operationVisitor) emitLoop(s *syntax.StmtFor, loop *loopContext) (*jumpOperation, error) {
	v.loops.Push(loop)
	defer v.loops.Pop()

	condition := v.byteOffset
	if err := v.emitValue(s.Condition); err != nil {
		return nil, err
	}
	exhausted := v.emitJump(OpJumpIfFalse)

	if err := v.visitScoped(s.Body); err != nil {
		return nil, err
	}

	v.patch(loop.continues...)
	if s.Step != nil {
		if err := s.Step.Accept(v); err != nil {
			return nil, err
		}
	}
	v.emitJumpTo(OpJump, condition)

	return exhausted, nil
}

// VisitStmtLabel names the loop it introduces. The loop may be nested in the
// block its initializer was hoisted into rather than being the labelled
// statement itself, so the label is left for the loop to claim.
func (v *operationVisitor) VisitStmtLabel(s *syntax.StmtLabel) error {
	v.pendingLabel = s.Label
	return s.LoopStmt.Accept(v)
}

// VisitStmtLoopJump leaves a loop, or leaves the rest of one iteration of it.
// Every scope between the jump and the loop it names is closed first, so the
// values those scopes own are discarded whichever way control leaves them. Where
// the branch lands is not known until the loop finishes emitting, so the loop
// holds it until then.
func (v *operationVisitor) VisitStmtLoopJump(s *syntax.StmtLoopJump) error {
	loop := v.lookupLoop(s.Label)
	for depth := v.scopes.Len(); depth > loop.scopeDepth; depth-- {
		v.emit(OpPopScope)
	}

	jump := v.emitJump(OpJump)
	if s.Kind() == syntax.StmtKindContinue {
		loop.continues = append(loop.continues, jump)
	} else {
		loop.breaks = append(loop.breaks, jump)
	}
	return nil
}

// The expression visitor emits each expression as either the location it names
// or the value it computes. [operationVisitor.addressable] settles which of the
// two a method leaves, and [operationVisitor.emitValue] is what loads through a
// location where the value was what was wanted.

func (v *operationVisitor) VisitExprNAry(e *syntax.ExprNAry) error {
	switch e.Kind() {
	case syntax.ExprKindTernary:
		return v.emitTernary(e)
	case syntax.ExprKindCall:
		return v.emitCall(e)
	case syntax.ExprKindTypeof:
		return v.emitTypeof(e)
	case syntax.ExprKindHash:
		return v.emitHash(e)

	case syntax.ExprKindBinaryNegate, syntax.ExprKindArithmeticNegate,
		syntax.ExprKindLogicalNegate:
		return v.emitNegation(e)
	case syntax.ExprKindAddressOf:
		return v.emitAddressOf(e)
	case syntax.ExprKindDereference:
		return v.emitDereference(e)

	case syntax.ExprKindPreIncrement, syntax.ExprKindPreDecrement,
		syntax.ExprKindPostIncrement, syntax.ExprKindPostDecrement:
		return v.emitStep(e)

	case syntax.ExprKindWait:
		return v.emitWait(e)
	case syntax.ExprKindAsync:
		return v.emitAsync(e)

	case syntax.ExprKindAssign:
		return v.emitAssign(e)
	case syntax.ExprKindNullCoalesce:
		return v.emitNullCoalesce(e)

	case syntax.ExprKindLogicalOR, syntax.ExprKindLogicalAND:
		return v.emitLogical(e)

	case syntax.ExprKindEqual, syntax.ExprKindNotEqual,
		syntax.ExprKindGreaterThan, syntax.ExprKindLessThan,
		syntax.ExprKindGreaterThanOrEqual, syntax.ExprKindLessThanOrEqual,
		syntax.ExprKindSpaceship:
		return v.emitComparison(e)

	case syntax.ExprKindSubtypeOf:
		return v.emitSubtypeOf(e)

	case syntax.ExprKindAdd, syntax.ExprKindSubtract,
		syntax.ExprKindMultiply, syntax.ExprKindDivide, syntax.ExprKindModulus,
		syntax.ExprKindBinaryOR, syntax.ExprKindBinaryAND, syntax.ExprKindBinaryXOR,
		syntax.ExprKindBinaryShiftLeft, syntax.ExprKindBinaryShiftRight,
		syntax.ExprKindBinaryShiftRightRight:
		return v.emitBinary(e)

	case syntax.ExprKindSpread:
		panic("spread is handled by emitCallFrame; reaching the generic visitor means the static pass let one through outside a call")

	default:
		panic("unreachable")
	}
}

// addressable reports whether an expression emits as the location it names —
// a pointer to where its value lives, under the type of that value — rather than
// as the value itself. Reading one of these is a load through the location,
// which is what separates an lvalue from the value it holds. An assignment is
// one of them too: it leaves the location it wrote into.
//
// Selecting a member names where in the object that member lies, but selecting
// an instance method names nothing: such a method is closed over the object
// rather than kept in it, so the closure is a value that was nowhere until it
// was built. Every static lies in a slot, a static method's holding the closure
// built over its body before the run, so selecting one names that slot; naming a
// type with no static after it is the type's id, which is a value and lies
// nowhere.
//
// An identifier is whichever of those its binding makes it, an instance method
// named without a receiver being no more a location than one named with it.
func (v *operationVisitor) addressable(e syntax.Expr) bool {
	switch e.Kind() {
	case syntax.ExprKindIdentifier:
		b, isInstance := v.lookupBinding(e.Start().Value.(string)).(instanceBinding)
		if !isInstance {
			return true
		}
		_, isMethod := b.field.(*syntax.FieldMethod)
		return !isMethod
	case syntax.ExprKindDereference,
		syntax.ExprKindThis, syntax.ExprKindSuper, syntax.ExprKindAssign:
		return true
	case syntax.ExprKindField:
		selection := e.(*syntax.ExprField)
		_, field := selectedField(selection)
		if _, isMember := field.(*syntax.FieldMember); !isMember {
			return false
		}
		return v.hasHome(selection.Expr)
	case syntax.ExprKindType:
		typeExpr := e.(*syntax.ExprType)
		if typeExpr.Field == "" {
			return false
		}
		_, isSlot := staticSlotType(selectedStatic(typeExpr))
		return isSlot
	}
	return false
}

// hasHome reports whether the object a member is selected out of lies somewhere
// the selection can point into. Where the object is reached through an address
// it always does, the address being the home; otherwise the object is the
// instance itself, and it lies somewhere only as far as the expression that
// produced it named somewhere.
//
// This is the same question [emitMemberSelection] answers when it chooses which
// form to hand the selection, and it must stay the same question: what that
// choice pushes is what the selection leaves behind.
func (v *operationVisitor) hasHome(object syntax.Expr) bool {
	return syntax.ReachedThroughAddress(object.ResolvedType()) || v.addressable(object)
}

// selectedField resolves what a field selection names: the declaration the
// object's static type resolves to, and that declaration's instance field the
// selection picks out.
func selectedField(e *syntax.ExprField) (syntax.DeclTypeable, syntax.FieldTypeable) {
	resolvable, ok := syntax.SelectionTarget(e.Expr.ResolvedType())
	if !ok {
		return nil, nil
	}
	field, _ := resolvable.Decl().GetInstanceField(e.Field)
	return resolvable.Decl(), field
}

// receiverType is what `this` is bound as: a pointer to the declaration,
// whatever that declaration's own width is. It is a pointer because what the
// closure holds is an address, either of the caller's own instance or of the
// copy the closure took of a homeless one, and one compiled body serves both.
func receiverType(d syntax.Decl) syntax.Type {
	return syntax.NewWrappedType(syntax.TypeKindPointer, d.(syntax.DeclTypeable).DeclType())
}

// loadOpcode reads through a location. A dereference reaches through a pointer
// the program computed, while every other location is a slot the compiler laid
// out and handed over as a pointer. An assignment leaves whichever of the two
// its target was, so it is read through the same way that target is.
func loadOpcode(e syntax.Expr) Opcode {
	if e.Kind() == syntax.ExprKindAssign {
		return loadOpcode(e.(*syntax.ExprNAry).Exprs[0])
	}
	if e.Kind() == syntax.ExprKindDereference {
		return OpDereference
	}
	return OpLoad
}

// emitValue emits an expression as the value it denotes. An addressable
// expression emits as its location, so its value is a load through it;
// everything else already produces a value and is emitted as it stands.
//
// An array is no exception. Its value is the run it is, laid on the stack as
// wide as the elements are — which is what stackWords already reserves for one
// in a frame and what the runtime already measures a type id carrying a length
// by. The one place a run is stepped over rather than taken is a sum, and that
// is [operationVisitor.emitArrayBase].
func (v *operationVisitor) emitValue(e syntax.Expr) error {
	if err := e.Accept(v); err != nil {
		return err
	}
	if !v.addressable(e) {
		return nil
	}
	v.emit(loadOpcode(e))
	return nil
}

// emitArrayBase leaves where an array begins, which is what a sum over one adds
// its offset to. The location an array emits as is already that address, so the
// type words above it are simply let go of.
//
// This is the one place an array is read as an address rather than as the run it
// is. checkAdditive in 3_semantic/4_static.go says the same thing from the other
// side: it gives array + int the type of a pointer to the element, and it
// requires the array to be an lvalue, which is what makes the location this
// needs one the operand always has.
func (v *operationVisitor) emitArrayBase(e syntax.Expr) error {
	if err := e.Accept(v); err != nil {
		return err
	}
	if !v.addressable(e) {
		panic("unreachable")
	}

	v.emit(OpAddressOf)
	return nil
}

// spreadInner returns the inner expression of a spread, or nil when e is not
// a spread. The static pass has already said the spread stands alone and that
// its inner is a pointer or array of the right element type, so what is left
// at codegen is handing a pointer to the callee rather than packing a run.
func spreadInner(e syntax.Expr) syntax.Expr {
	nary, ok := e.(*syntax.ExprNAry)
	if !ok || nary.Kind() != syntax.ExprKindSpread {
		return nil
	}
	return nary.Exprs[0]
}

// emitVariadicPayload lays down what a variadic parameter reads at runtime: a
// single pointer word to the elements. Written out one argument at a time the
// elements are packed into a fresh run; forwarded whole with a trailing spread
// the inner's pointer is handed on, an array first decayed to its first-element
// address the same way a sum over one does.
func (v *operationVisitor) emitVariadicPayload(packed []syntax.Expr, element syntax.Type) error {
	if len(packed) == 1 {
		if inner := spreadInner(packed[0]); inner != nil {
			innerType := syntax.StripConst(inner.ResolvedType())
			switch innerType.Kind() {
			case syntax.TypeKindPointer:
				return v.emitValue(inner)
			case syntax.TypeKindArray:
				return v.emitArrayBase(inner)
			default:
				panic("unreachable; semantic requires a pointer or array")
			}
		}
	}
	for _, argument := range packed {
		if err := v.emitValueAs(argument, element); err != nil {
			return err
		}
	}
	v.emitPushTypeID(element)
	v.emit(newUnaryOp(OpNewArray, uint64(len(packed))))
	return nil
}

// variadicElement returns what one argument of a function's trailing variadic
// parameter is, or nil when the function has no such parameter.
func variadicElement(parameters []syntax.Type) syntax.Type {
	if len(parameters) == 0 {
		return nil
	}
	element, _ := syntax.VariadicElement(parameters[len(parameters)-1])
	return element
}

// emitValueAs emits a value as the type the slot it lands in was declared
// with, which is what makes an upcast into that slot stand for itself. The
// static pass has already said the value's type stands below the target's, so
// the only question left is whether anything has to be done to the value: for
// a pointer upcast or for the same type on both sides, nothing — the existing
// cast-visitor fall-through for an upcast is a no-op in [emitWordCast]. For
// an any slot, the box the slot expects has to be built, which is what the
// cast visitor's to=any branch is for; without this an int flowing into an
// any slot would be laid down as its one word of integer data where a
// one-word pointer to a box was supposed to go.
//
// A synthetic ExprCast is what the conversion is written as, because the cast
// visitor already settles every case the subtype rules permit — box to any,
// upcast through a pointer, descend into a numeric widening — and reaching it
// through the written cast path is what keeps an implicit conversion the same
// conversion the program could have written for itself.
//
// Every site a value flows into a declared slot uses this: a call argument,
// a variable initializer, an assignment, a return, and a member initializer
// in a static or instance layout. The slot's own type is what gets pushed as
// the value's id after the cast runs.
func (v *operationVisitor) emitValueAs(e syntax.Expr, target syntax.Type) error {
	if e.ResolvedType().Equals(target) {
		return v.emitValue(e)
	}
	return v.VisitExprCast(&syntax.ExprCast{
		Expr: e,
		Type: target,
	})
}

// emitCallFrame emits what a call's operands spell out, which is already the
// frame the callee runs in. The first operand is a closure, and a closure is the
// receiver the callee reads out of slot 0, so it is pushed first and then left
// alone and the arguments pile on top of it. A variadic parameter is one slot
// like every other, so the arguments feeding it are packed into an array first,
// which is what keeps the frame as wide as the callee was compiled to expect.
//
// The width returned is in words rather than in arguments, because an argument
// is not a word: a struct, class or enum lies on the stack inline and spans as
// many slots as its layout does. Words are what say where the frame begins.
func (v *operationVisitor) emitCallFrame(e *syntax.ExprNAry) (callFrame, error) {
	callee := e.Exprs[0]
	if err := v.emitValue(callee); err != nil {
		return callFrame{}, err
	}

	parameters := callee.ResolvedType().(*syntax.TypeFunction).ParameterTypes
	element := variadicElement(parameters)
	fixed := len(parameters)
	if element != nil {
		fixed--
	}

	arguments := e.Exprs[1:]
	for i, argument := range arguments[:fixed] {
		if err := v.emitValueAs(argument, parameters[i]); err != nil {
			return callFrame{}, err
		}
	}

	if element != nil {
		packed := arguments[fixed:]
		if err := v.emitVariadicPayload(packed, element); err != nil {
			return callFrame{}, err
		}
	}

	return v.frameShape(parameters)
}

// frameShape is how a call frame over the given parameters is measured: the
// words of the closure and the fixed-width arguments, and the types of the
// arguments whose width an instantiation decides.
func (v *operationVisitor) frameShape(parameters []syntax.Type) (callFrame, error) {
	frame := callFrame{words: 1}
	for _, parameter := range parameters {
		parameter = v.groundType(parameter)
		if openWidth(parameter) {
			frame.open = append(frame.open, parameter)
			continue
		}
		frame.words += stackWords(parameter)
	}
	return frame, nil
}

// callFrame is the measure [operationVisitor.frameShape] gives a call frame.
type callFrame struct {
	words uint64
	open  []syntax.Type
}

// emitFrameCall emits a call over a frame of the given shape, pushing the type
// ids of its open-width arguments for the open opcode where it has any.
func (v *operationVisitor) emitFrameCall(fixed, open Opcode, frame callFrame) {
	if len(frame.open) == 0 {
		v.emit(newUnaryOp(fixed, frame.words))
		return
	}
	for _, t := range frame.open {
		v.emitPushTypeID(t)
	}
	v.emit(newBinaryOp(open, frame.words, uint64(len(frame.open))))
}

// emitCall runs a call on the stack it was written on. A call through a native
// is one the compiler can see from the AST — the callee is an [syntax.ExprNative]
// behind any number of signature casts — and gets its own opcode: OpCallMethod
// reads a closure out of slot 0, which a bare native address is not. Everything
// above the base is the same shape either way.
func (v *operationVisitor) emitCall(e *syntax.ExprNAry) error {
	frame, err := v.emitCallFrame(e)
	if err != nil {
		return err
	}

	if isNativeCallee(e.Exprs[0]) {
		// A native's signature is C's, which has no width an instantiation
		// decides.
		if len(frame.open) != 0 {
			panic("unreachable")
		}
		v.emit(newUnaryOp(OpCallNative, frame.words))
		return nil
	}
	v.emitFrameCall(OpCallMethod, OpCallMethodOpen, frame)
	return nil
}

// isNativeCallee reports whether a call's callee is an [syntax.ExprNative], seen
// through any signature casts the source put over it. The native expression is
// always written and immediately cast — nothing else types as a function
// address — so a native stored in a variable and called later is not what this
// detects, nor is it a shape the compiler supports.
func isNativeCallee(e syntax.Expr) bool {
	for {
		switch n := e.(type) {
		case *syntax.ExprCast:
			e = n.Expr
		case *syntax.ExprNative:
			return true
		default:
			return false
		}
	}
}

// emitAsync runs a call on a thread of its own. The operands are the frame a
// call builds either way, and what differs is only where that frame is put: the
// thread's type id goes over it, since the frame moves into the allocation
// rather than staying on the stack the call was written on. The call is left
// unemitted as a call, so its own visit never happens.
func (v *operationVisitor) emitAsync(e *syntax.ExprNAry) error {
	frame, err := v.emitCallFrame(e.Exprs[0].(*syntax.ExprNAry))
	if err != nil {
		return err
	}

	v.emitPushTypeID(e.ResolvedType())
	v.emitFrameCall(OpNewThread, OpNewThreadOpen, frame)
	return nil
}

// emitWait joins a thread and evaluates to what it ran. The thread is emitted
// over its own type id, which is what the runtime reads the result's type out
// of. A thread that ran to no value has nothing to hand back, and is joined by
// the instruction that expects none.
func (v *operationVisitor) emitWait(e *syntax.ExprNAry) error {
	thread := e.Exprs[0]
	if e.ResolvedType().Kind() == syntax.TypeKindVoid {
		if err := v.emitValue(thread); err != nil {
			return err
		}

		v.emit(OpWaitThreadVoid)
		return nil
	}

	if err := v.emitValue(thread); err != nil {
		return err
	}

	v.emitPushTypeID(e.ResolvedType())
	v.emit(OpWaitThread)
	return nil
}

// emitAddressOf takes the address of what an expression names. A location is
// already that address, so the operand is emitted without the load that reading
// it would need, and the pair collapses to the pointer it already holds.
//
// Only a location reaches here: static analysis requires & to be written on an
// lvalue, so the temporary OpAddressOf is otherwise willing to give a home to is
// not something this operator can be handed. Method selection still spills one —
// a receiver has to be an address whether or not the source named a place for it
// — but that is a home the source never asked for and never has to name again.
func (v *operationVisitor) emitAddressOf(e *syntax.ExprNAry) error {
	operand := e.Exprs[0]
	if err := operand.Accept(v); err != nil {
		return err
	}
	if !v.addressable(operand) {
		panic("unreachable")
	}

	v.emit(OpAddressOf)
	return nil
}

// emitDereference names the location a pointer reaches. The pointer's value is
// that location, so a dereference emits as the same pair every other location
// emits as, and reading through it is left to whoever wants the value — which is
// what makes *p assignable and addressable like any other lvalue.
func (v *operationVisitor) emitDereference(e *syntax.ExprNAry) error {
	if err := v.emitValue(e.Exprs[0]); err != nil {
		return err
	}
	v.emitPushTypeID(e.ResolvedType())
	return nil
}

// emitTypeof replaces a value with its type. An abstract or an interface only
// names the value at runtime, so the runtime is what answers; an any carries
// the id of what was boxed into it and so answers by that id rather than by
// the static any id; anything else is already the type it was written as, and
// the value is discarded for the id the compiler knows — read off the receiver
// when the id is a generic argument the template has not bound yet.
func (v *operationVisitor) emitTypeof(e *syntax.ExprNAry) error {
	operand := e.Exprs[0]
	t := operand.ResolvedType()

	if t.Kind() == syntax.TypeKindAny {
		if err := v.emitValue(operand); err != nil {
			return err
		}
		v.emit(OpAnyPayloadType)
		return nil
	}

	if err := v.emitValue(operand); err != nil {
		return err
	}

	v.emitPushTypeID(t)
	if virtuallyTyped(t) {
		v.emit(OpTypeOf)
		return nil
	}

	v.emit(OpPop)
	v.emitPushTypeID(t)
	return nil
}

// virtuallyTyped reports whether a value's type is only what it was written as,
// with the value being some declaration below it in reality. An abstract or an
// interface always is: it is a reference to one of its implementors. A class is
// exactly what it says when it is laid out inline, since there is no room in it
// for anything more, but a pointer to one reaches a subclass: a Foo* that holds
// a Bar is a Bar*.
func virtuallyTyped(t syntax.Type) bool {
	var pointer bool
	for {
		wrapped, ok := t.(*syntax.TypeWrapped)
		if !ok || wrapped.Kind() != syntax.TypeKindPointer {
			break
		}
		pointer = true
		t = wrapped.Type
	}

	resolvable, ok := t.(syntax.TypeResolvable)
	if !ok {
		return false
	}
	switch resolvable.Decl().Kind() {
	case syntax.KindAbstract, syntax.KindInterface:
		return true
	case syntax.KindClass:
		return pointer
	}
	return false
}

// emitHash replaces a value with its hash. The value goes down under its type
// id because a hash is the type's to say, as an equality is, and for the same
// reason: values that compare equal have to hash alike, and which values those
// are is known to the type and to nothing else. Neither the instruction nor
// this pass has to know which type it will be, so a hash taken in a template of
// a type no instantiation has bound yet emits what a hash of a known type does.
func (v *operationVisitor) emitHash(e *syntax.ExprNAry) error {
	operand := e.Exprs[0]

	if err := v.emitValue(operand); err != nil {
		return err
	}

	v.emitPushTypeID(operand.ResolvedType())
	v.emit(OpHash)
	return nil
}

// emitTernary runs one of two branches. The condition chooses, and whichever
// branch runs is the one that leaves the value behind, so both arms meet at the
// same operand stack.
func (v *operationVisitor) emitTernary(e *syntax.ExprNAry) error {
	if err := v.emitValue(e.Exprs[0]); err != nil {
		return err
	}
	otherwise := v.emitJump(OpJumpIfFalse)

	if err := v.emitValue(e.Exprs[1]); err != nil {
		return err
	}
	end := v.emitJump(OpJump)

	v.patch(otherwise)
	if err := v.emitValue(e.Exprs[2]); err != nil {
		return err
	}
	v.patch(end)
	return nil
}

// emitNegation flips the sign, the bits, or the truth of a single operand. A
// char or byte needs no instruction of its own: both ride in a word, so negating
// the word negates the value and a trailing narrow clips the result back to its
// own width, since the two's-complement or bitwise flip of a narrow value sets
// bits above it.
func (v *operationVisitor) emitNegation(e *syntax.ExprNAry) error {
	if err := v.emitValue(e.Exprs[0]); err != nil {
		return err
	}

	switch t := e.Exprs[0].ResolvedType().Kind(); {
	case e.Kind() == syntax.ExprKindArithmeticNegate && t == syntax.TypeKindFloat:
		v.emit(OpNegF64)

	// Logical negation asks whether the operand is zero, which is what a
	// boolean false already is.
	case e.Kind() == syntax.ExprKindLogicalNegate:
		v.emitPushWords(0)
		v.emit(OpEqualI64)

	case e.Kind() == syntax.ExprKindBinaryNegate && t == syntax.TypeKindBool:
		v.emitBooleanFlip()

	case e.Kind() == syntax.ExprKindBinaryNegate:
		v.emit(OpBitwiseNegationI64)
		v.emitNarrow(e.ResolvedType())

	default:
		v.emit(OpArithmeticNegationI64)
		v.emitNarrow(e.ResolvedType())
	}
	return nil
}

// emitComparison compares two values. There are only two questions to ask of a
// pair — whether they are equal, and how they are ordered — so each operator
// asks one of them and reads the answer: equality as it stands or turned around,
// and an order by which side of zero it fell on. Each operand goes down over
// its own type id, since what equality and order come to is the type's to say
// and neither instruction is told which type by anything else.
func (v *operationVisitor) emitComparison(e *syntax.ExprNAry) error {
	if len(e.Exprs) != 2 {
		panic("unreachable")
	}

	// An any pair has no shape OpEq or OpCmp knows for itself; the pair is
	// put into one the two instructions already settle. An equality lifts
	// the non-any side to any so the pair reaches the runtime as box-vs-box
	// and the any-equality rule answers. An order hashes each side instead
	// — a hash is a single word of int and ints already have an order, so
	// OpCmp settles the pair without a new any-order branch of its own.
	// Equal values hash alike, so the order agrees with equality on the
	// pair it says is equal; two values with the same hash and different
	// payloads order as zero they are not, which is the one place the
	// agreement loosens and the price of a hash standing for a value.
	kind := e.Kind()
	leftType := e.Exprs[0].ResolvedType()
	rightType := e.Exprs[1].ResolvedType()
	equality := kind == syntax.ExprKindEqual || kind == syntax.ExprKindNotEqual
	leftAny := leftType.Kind() == syntax.TypeKindAny
	rightAny := rightType.Kind() == syntax.TypeKindAny
	anyPair := leftAny || rightAny

	if anyPair && !equality {
		if err := v.emitHashedComparisonOperand(e.Exprs[0]); err != nil {
			return err
		}
		if err := v.emitHashedComparisonOperand(e.Exprs[1]); err != nil {
			return err
		}
	} else {
		if equality && anyPair {
			if !leftAny {
				leftType = syntax.TypeAny
			} else if !rightAny {
				rightType = syntax.TypeAny
			}
		}
		if err := v.emitComparisonOperand(e.Exprs[0], leftType); err != nil {
			return err
		}
		if err := v.emitComparisonOperand(e.Exprs[1], rightType); err != nil {
			return err
		}
	}

	if kind == syntax.ExprKindEqual || kind == syntax.ExprKindNotEqual {
		v.emit(OpEq)
		if kind == syntax.ExprKindNotEqual {
			v.emitBooleanFlip()
		}
		return nil
	}

	v.emit(OpCmp)

	// An order is a number, so which comparison it answers is which side of zero
	// it lies on. Neither of the inclusive comparisons has a side of its own:
	// each is the strict comparison on the other side, turned around.
	switch kind {
	case syntax.ExprKindSpaceship:
		// The order itself is what a spaceship asks for.
	case syntax.ExprKindLessThan:
		v.emit(OpIsNegativeI64)
	case syntax.ExprKindGreaterThan:
		v.emit(OpIsPositiveI64)
	case syntax.ExprKindLessThanOrEqual:
		v.emit(OpIsPositiveI64)
		v.emitBooleanFlip()
	case syntax.ExprKindGreaterThanOrEqual:
		v.emit(OpIsNegativeI64)
		v.emitBooleanFlip()
	}
	return nil
}

// emitHashedComparisonOperand leaves one operand of an any order as a word
// of int above its own id, the shape OpCmp settles for two ints. The value
// is reduced to its hash by OpHash — an any's hash reads the box and
// delegates to the payload under its stored id, which is what makes the
// order read the value the box holds and not the pointer that names it.
// Hashing a non-any side is the plain hash of the value, since boxing it
// first would allocate for a word a hash would read right past.
func (v *operationVisitor) emitHashedComparisonOperand(operand syntax.Expr) error {
	if err := v.emitValue(operand); err != nil {
		return err
	}
	v.emitPushTypeID(operand.ResolvedType())
	v.emit(OpHash)
	v.emitPushTypeID(syntax.TypeInt)
	return nil
}

// emitComparisonOperand leaves one operand for OpEq or OpCmp: its value, as
// the type the pair settled on, with that type's id above it. A field
// selection of a tail member is the one shape it has to open up, because the
// type the record has is unsized and OpEq would read one element where the
// whole run is what the question is about. OpLoadTailMember is the one
// instruction that reaches the length, so the enclosing is laid out as
// emitMemberSelection would and OpLoadTailMember stands in for its
// OpPushMember — and the value and the type above it are both the sized run
// then, which is what the comparison asks after. A tail member is never
// compared against an any, so the lift the caller does for an any pair
// never changes the type here.
func (v *operationVisitor) emitComparisonOperand(operand syntax.Expr, as syntax.Type) error {
	if field, ok := operand.(*syntax.ExprField); ok {
		if _, member := selectedField(field); member != nil {
			if arr, isArr := member.FieldType().(*syntax.TypeArray); isArr && arr.Size == nil {
				return v.emitMemberSelection(field.Expr, field.Expr.ResolvedType(),
					newUnaryOp(OpLoadTailMember, uint64(*member.(*syntax.FieldMember).Index())))
			}
		}
	}

	if err := v.emitValueAs(operand, as); err != nil {
		return err
	}
	v.emitPushTypeID(as)
	return nil
}

// emitSubtypeOf asks whether the type on the left stands below the type on the
// right. Both operands are already ids, so nothing is pushed over them the way a
// comparison pushes a type over each value: an id is what the instruction takes,
// and what an operand of type type leaves is exactly that.
func (v *operationVisitor) emitSubtypeOf(e *syntax.ExprNAry) error {
	if len(e.Exprs) != 2 {
		panic("unreachable")
	}

	for _, operand := range e.Exprs {
		if err := v.emitValue(operand); err != nil {
			return err
		}
	}

	v.emit(OpIsSubtypeOf)
	return nil
}

// emitBooleanFlip turns the answer on top of the stack around. A boolean is one
// bit wide, so flipping that bit is the whole of it; flipping every bit of the
// word would leave one that is neither true nor false.
func (v *operationVisitor) emitBooleanFlip() {
	v.emitPushWords(1)
	v.emit(OpXorI64)
}

// The instruction each step operator compiles to. Prefix and postfix differ only
// in which value they leave behind.
var stepOpcodes = map[syntax.ExprKind]Opcode{
	syntax.ExprKindPreIncrement:  OpIncPre,
	syntax.ExprKindPreDecrement:  OpDecPre,
	syntax.ExprKindPostIncrement: OpIncPost,
	syntax.ExprKindPostDecrement: OpDecPost,
}

// emitStep steps an lvalue by one. The operand is emitted as the pointer to the
// location it names, under its type, which is the shape the instruction reads
// and is what tells the runtime whether it is stepping an integer, a float or a
// pointer — including when the type is a generic argument the template has not
// bound, in which case the id comes off the receiver at runtime.
//
// Nothing scales the step here the way [operationVisitor.emitPointerStride]
// scales an offset, because nothing has to: the type id is already an operand,
// so the runtime reads the pointee's stride off it. That is also the only way
// the generic case can work, since the width a template steps by is not settled
// until the id says which instantiation is running.
func (v *operationVisitor) emitStep(e *syntax.ExprNAry) error {
	if err := e.Exprs[0].Accept(v); err != nil {
		return err
	}
	v.emit(stepOpcodes[e.Kind()])
	return nil
}

// emitAssign writes a value into the location its target names. The target
// emits as that location — a pointer over the type of what lives there — and the
// store leaves it standing, so an assignment names a location just as its target
// does and reading one is a load through it.
//
// The value is coerced to the location's own type, which is what makes an
// implicit upcast at an assignment stand for itself: an int assigned into an
// any slot is boxed, a derived pointer stored through a base slot stays the
// pointer it was. The id pushed above the value is the location's type too,
// since that is what the value has become after the cast — the same shape the
// slot was laid out for.
func (v *operationVisitor) emitAssign(e *syntax.ExprNAry) error {
	if err := e.Exprs[0].Accept(v); err != nil {
		return err
	}
	target := e.Exprs[0].ResolvedType()
	if err := v.emitValueAs(e.Exprs[1], target); err != nil {
		return err
	}

	v.emitPushTypeID(target)
	v.emit(OpStore)
	return nil
}

// emitLogical folds a short circuiting chain: each operand but the last decides
// whether the rest of the chain runs at all, and the chain evaluates to the last
// operand it reached, or to the constant that ended it early. Only
// [OpJumpIfFalse] branches, so an OR inverts each test to ask the same question
// of it, a boolean being one bit wide.
func (v *operationVisitor) emitLogical(e *syntax.ExprNAry) error {
	isOr := e.Kind() == syntax.ExprKindLogicalOR

	var shortCircuits []*jumpOperation
	for _, operand := range e.Exprs[:len(e.Exprs)-1] {
		if err := v.emitValue(operand); err != nil {
			return err
		}
		if isOr {
			v.emitBooleanFlip()
		}
		shortCircuits = append(shortCircuits, v.emitJump(OpJumpIfFalse))
	}
	if err := v.emitValue(e.Exprs[len(e.Exprs)-1]); err != nil {
		return err
	}

	end := v.emitJump(OpJump)
	v.patch(shortCircuits...)
	v.emitPushWords(boolWord(isOr))
	v.patch(end)
	return nil
}

// emitNullCoalesce folds a chain to the first operand that is not null. Null is
// the zero word [OpJumpIfFalse] already tests for, so each operand is duplicated
// and the copy spent on the test: a null operand is discarded and the chain goes
// on, and any other one is what the chain evaluates to. The copy outlives no
// allocation, so it needs no mark stack entry of its own. The last operand is the
// answer whether it is null or not, which is what makes the chain total.
func (v *operationVisitor) emitNullCoalesce(e *syntax.ExprNAry) error {
	var ends []*jumpOperation
	for _, operand := range e.Exprs[:len(e.Exprs)-1] {
		if err := v.emitValue(operand); err != nil {
			return err
		}
		v.emit(newUnaryOp(OpDupWord, 0))
		next := v.emitJump(OpJumpIfFalse)
		ends = append(ends, v.emitJump(OpJump))

		v.patch(next)
		v.emit(OpPopWord)
	}
	if err := v.emitValue(e.Exprs[len(e.Exprs)-1]); err != nil {
		return err
	}

	v.patch(ends...)
	return nil
}

// boolWord is the operand stack word a boolean holds.
func boolWord(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// emitBinary folds a binary chain left to right: a + b + c pushes a and b,
// combines them, then pushes c and combines again. The expression's own type
// picks the instruction, which is the same for every step of the chain. A
// narrow numeric result is clipped back to its own width after each step: the
// operand stack carries a word, so an add or a shift can leave bits above the
// result's width that only an explicit mask takes off.
func (v *operationVisitor) emitBinary(e *syntax.ExprNAry) error {
	t := e.ResolvedType()
	opcode := binaryOpcode(e.Kind(), t)

	if err := v.emitOperand(e.Exprs[0], t); err != nil {
		return err
	}
	for _, operand := range e.Exprs[1:] {
		if err := v.emitOperand(operand, t); err != nil {
			return err
		}
		v.emit(opcode)
		v.emitNarrow(t)
	}
	return nil
}

// emitNarrow clips the word on top of the stack to the width of t. Only a byte
// or a char needs it, each occupying fewer bits than the operand stack carries;
// everything else is already its own width and the switch falls through.
func (v *operationVisitor) emitNarrow(t syntax.Type) {
	switch syntax.StripConst(t).Kind() {
	case syntax.TypeKindChar:
		v.emitPushWords(math.MaxUint32)
		v.emit(OpAndI64)
	case syntax.TypeKindByte:
		v.emitPushWords(math.MaxUint8)
		v.emit(OpAndI64)
	}
}

// emitOperand emits one operand of a binary chain evaluating to t, leaving it as
// the instruction expects it: a char concatenated onto a string is widened to
// one, and an integer offsetting a pointer is scaled to the bytes it means,
// since it counts elements while the pointer counts bytes.
func (v *operationVisitor) emitOperand(operand syntax.Expr, t syntax.Type) error {
	t = syntax.StripConst(t)
	operandType := syntax.StripConst(operand.ResolvedType())

	// Widening a char to a string is what a cast of it to one already writes
	// out, so the cast is emitted in place of the operand rather than after it.
	if t.Kind() == syntax.TypeKindString && operandType.Kind() == syntax.TypeKindChar {
		return v.emitStringCast(operand, operandType)
	}

	// An array contributes where its elements begin rather than the elements
	// themselves, the sum being a step over them. Taking the run here would put
	// every element on the stack for the offset to be added to the last of them.
	if operandType.Kind() == syntax.TypeKindArray {
		return v.emitArrayBase(operand)
	}

	if err := v.emitValue(operand); err != nil {
		return err
	}

	// An integer or uint offsetting a pointer is scaled to the bytes it means,
	// since it counts elements while the pointer counts bytes. The two integer
	// widths behave identically here: the stride is a positive constant, so
	// multiplying by a signed or unsigned word gives the same byte offset.
	if t.Kind() == syntax.TypeKindPointer &&
		(operandType.Kind() == syntax.TypeKindInt ||
			operandType.Kind() == syntax.TypeKindUint) {
		v.emitPointerStride(t.(*syntax.TypeWrapped).Type)
	}
	return nil
}

// emitPointerStride multiplies the offset on top of the stack by the width of
// the type a pointer steps over, turning a count of elements into the bytes it
// means. A pointee one byte wide needs no multiply, since the two counts agree.
//
// A pointee whose width is settled here is a constant. One that is or contains
// an open generic argument has no width until the template is instantiated —
// Node[int] and Node[BigType] are not the same size, so the same p + 5 steps a
// different distance in each — so the width is read off the type id at runtime,
// the same way the id itself is read off the receiver.
func (v *operationVisitor) emitPointerStride(pointee syntax.Type) {
	if pointee.ContainsGenericArg() {
		v.emitPushTypeID(pointee)
		v.emit(OpSizeOf)
		v.emit(OpMulI64)
		return
	}

	if stride, _ := fieldByteInfo(pointee); stride != 1 {
		v.emitPushWords(stride)
		v.emit(OpMulI64)
	}
}

// The operators each operand type supports. Integers do the whole set, a uint
// dividing unsigned; floats
// do the arithmetic apart from modulus, which steps in whole units, and none of
// the bitwise work; a boolean is only ever one bit wide, so it has the bitwise
// operators and nothing else; strings only concatenate; a pointer only steps
// forward and back.
var (
	integerBinaryOpcodes = map[syntax.ExprKind]Opcode{
		syntax.ExprKindAdd:                   OpAddI64,
		syntax.ExprKindSubtract:              OpSubI64,
		syntax.ExprKindMultiply:              OpMulI64,
		syntax.ExprKindDivide:                OpDivI64,
		syntax.ExprKindModulus:               OpModI64,
		syntax.ExprKindBinaryOR:              OpOrI64,
		syntax.ExprKindBinaryAND:             OpAndI64,
		syntax.ExprKindBinaryXOR:             OpXorI64,
		syntax.ExprKindBinaryShiftLeft:       OpShiftLeftI64,
		syntax.ExprKindBinaryShiftRight:      OpShiftRightI64,
		syntax.ExprKindBinaryShiftRightRight: OpShiftArithmeticRightI64,
	}
	floatBinaryOpcodes = map[syntax.ExprKind]Opcode{
		syntax.ExprKindAdd:      OpAddF64,
		syntax.ExprKindSubtract: OpSubF64,
		syntax.ExprKindMultiply: OpMulF64,
		syntax.ExprKindDivide:   OpDivF64,
	}
	booleanBinaryOpcodes = map[syntax.ExprKind]Opcode{
		syntax.ExprKindBinaryOR:  OpOrI64,
		syntax.ExprKindBinaryAND: OpAndI64,
		syntax.ExprKindBinaryXOR: OpXorI64,
	}
	unsignedBinaryOpcodes = map[syntax.ExprKind]Opcode{
		syntax.ExprKindDivide:  OpDivU64,
		syntax.ExprKindModulus: OpModU64,
	}
	stringBinaryOpcodes = map[syntax.ExprKind]Opcode{
		syntax.ExprKindAdd: OpConcatString,
	}
	pointerBinaryOpcodes = map[syntax.ExprKind]Opcode{
		syntax.ExprKindAdd:      OpAddI64,
		syntax.ExprKindSubtract: OpSubI64,
	}
)

// binaryOpcode picks the instruction a binary operator compiles to for operands
// of type t. A char is an integer on the operand stack.
func binaryOpcode(kind syntax.ExprKind, t syntax.Type) Opcode {
	t = syntax.StripConst(t)
	opcodes := integerBinaryOpcodes
	switch t.Kind() {
	case syntax.TypeKindFloat:
		opcodes = floatBinaryOpcodes
	case syntax.TypeKindBool:
		opcodes = booleanBinaryOpcodes
	case syntax.TypeKindString:
		opcodes = stringBinaryOpcodes
	case syntax.TypeKindPointer:
		opcodes = pointerBinaryOpcodes
	case syntax.TypeKindUint:
		if opcode, ok := unsignedBinaryOpcodes[kind]; ok {
			return opcode
		}
	}

	if opcode, ok := opcodes[kind]; ok {
		return opcode
	}
	panic("unreachable")
}

// VisitExprField selects an instance field of an object. A member emits as the
// location it lives at rather than as the member itself, which is what makes a
// field assignable as well as readable; [operationVisitor.emitValue] is what
// loads through it. A method emits as the closure a call is made through, which
// is the receiver the callee reads out of slot 0.
//
// Either is named by its index rather than by the offset or the body it resolves
// to, because neither of those is a property of the expression: the object's
// static type says which field is meant, but a subclass reached through a base
// type lays that same member out wherever its own layout puts it, and overrides
// that same method with a body of its own.
//
// Selecting through a type names a static instead, which [operationVisitor.VisitExprType] emits.
func (v *operationVisitor) VisitExprField(e *syntax.ExprField) error {
	object := e.Expr
	objectType := object.ResolvedType()

	decl, field := selectedField(e)
	if method, ok := field.(*syntax.FieldMethod); ok {
		return v.emitMethodSelection(object, objectType, newUnaryOp(methodOpcode(decl), uint64(*method.Index())))
	}
	if member, ok := field.(*syntax.FieldMember); ok {
		return v.emitMemberSelection(object, objectType, newUnaryOp(OpPushMember, uint64(*member.Index())))
	}
	panic("unreachable")
}

// emitMemberSelection hands OpPushMember the object under its own type, and what
// the selection leaves behind follows what was handed to it: an object that
// arrived as an address leaves the member's location, and an object that arrived
// as words leaves the member's words. The compiler chooses between them here, so
// the runtime never has to — the rule there is only that a member of an address
// is an address.
//
// Three of the four ways an object can arrive are addresses. A pointer is one
// already, so it is loaded for its value and the type pushed over it is what it
// points at rather than the pointer itself. An abstract or an interface is one
// too, being a single word naming an instance that lies elsewhere, so its value
// and the type over it are a location without anything being done to them. An
// inline value that lies somewhere is emitted as where it lies.
//
// The fourth is an inline value no expression gave a home to. It has no address
// to hand over, so its words are what is pushed and OpPushMemberOfValue leaves
// the member's words in their place. Nothing is lost by that: a member of a temporary can only be read,
// because [operationVisitor.hasHome] refuses it as an assignment target for
// exactly the reason it is emitted this way.
func (v *operationVisitor) emitMemberSelection(object syntax.Expr, objectType syntax.Type, selection operation) error {
	if pointee, isPointer := pointeeType(objectType); isPointer {
		if err := v.emitValue(object); err != nil {
			return err
		}
		v.emitPushTypeID(pointee)
	} else if laidOutInline(objectType) && v.addressable(object) {
		if err := object.Accept(v); err != nil {
			return err
		}
	} else {
		if err := v.emitValue(object); err != nil {
			return err
		}
		v.emitPushTypeID(objectType)
		if unary, ok := selection.(*unaryOperation); ok && unary.opcode == OpPushMember && laidOutInline(objectType) {
			selection = newUnaryOp(OpPushMemberOfValue, unary.operand)
		}
	}

	v.emit(selection)
	return nil
}

// pointeeType is what a pointer addresses, and whether the type is a pointer at
// all.
func pointeeType(t syntax.Type) (syntax.Type, bool) {
	w, ok := syntax.StripConst(t).(*syntax.TypeWrapped)
	if !ok || w.Kind() != syntax.TypeKindPointer {
		return nil, false
	}
	return w.Type, true
}

// laidOutInline reports whether a value of the type is the instance itself
// rather than a one-word reference to one lying elsewhere. A class, struct or
// enum is the instance, so where it lies is its address; an abstract or an
// interface is never laid out anywhere, so a value of one is already an address
// and has none of its own worth taking.
func laidOutInline(t syntax.Type) bool {
	resolvable, ok := syntax.StripConst(t).(syntax.TypeResolvable)
	if !ok {
		return false
	}
	switch resolvable.Decl().Kind() {
	case syntax.KindClass, syntax.KindStruct, syntax.KindEnum:
		return true
	}
	return false
}

// emitMethodSelection closes a method over its receiver. The object is emitted
// under its own type id, which is the pair the runtime resolves the method's
// index against: the index counts the methods the static type has, and the type
// says which method table that count lands in.
//
// A receiver that lies somewhere goes over as the one word its address is, which
// is what the frame slot holds and what the body reads. Something already reached
// through an address is that word already; an inline value with a home is pushed
// as the address of where it lives.
//
// A homeless value has no address to push. It goes over as itself, under its own
// type id, and the closure becomes where it lives — see OpPushMethod, which
// copies the words into the closure, and OpCallMethod, which puts their address
// in the slot. That is why nothing is spilled to a temporary here: a temporary
// would need an owner, and the closure is already one.
//
// So the type over the object says which of the two the receiver is, and it is
// the only thing that does. The method's index resolves through either.
func (v *operationVisitor) emitMethodSelection(object syntax.Expr, objectType syntax.Type, selection operation) error {
	if syntax.ReachedThroughAddress(objectType) {
		if err := v.emitValue(object); err != nil {
			return err
		}
		v.emitPushTypeID(objectType)
		v.emit(selection)
		return nil
	}

	if err := object.Accept(v); err != nil {
		return err
	}

	// The same question [operationVisitor.hasHome] asks, the address case having
	// been answered above: what is left is an inline value, and whether it lies
	// anywhere is whether the expression that produced it named anywhere.
	if !v.addressable(object) {
		v.emitPushTypeID(objectType)
		v.emit(selection)
		return nil
	}

	v.emit(OpAddressOf)
	v.emitPushTypeID(syntax.NewWrappedType(syntax.TypeKindPointer, objectType))
	v.emit(selection)
	return nil
}

// methodOpcode is how a method of the given declaration is reached. An interface
// numbers its methods its own way and says nothing about what implements them,
// so its index is translated through the table the implementor carries; every
// other declaration numbers a method where its own table holds it, which an
// override lands on too.
func methodOpcode(d syntax.DeclTypeable) Opcode {
	if d.Kind() == syntax.KindInterface {
		return OpPushInterfaceMethod
	}
	return OpPushMethod
}

// VisitExprCast reads a value as the type it is written as. There are three
// things a cast can be. A string target is built from the value, since a string
// is not the operand word at all. A target the value's own type stands in some
// line of descent from is the same word read as that type, and downward that is
// a claim the runtime has to settle. Everything else is a numeric primitive,
// each of which is a single operand word: an int is the whole word, a char is the
// low 32 bits of it, a bool is that word being 1 or 0, and a float is the word
// read as an f64 instead of counted. So a cast among those is whatever makes the
// word the operand left stand for a value of the target.
func (v *operationVisitor) VisitExprCast(e *syntax.ExprCast) error {
	from, to := syntax.StripConst(e.Expr.ResolvedType()), syntax.StripConst(e.Type)

	if enum, ok := syntax.EnumBackingCast(from, to); ok {
		primitive := enum.Primitive()
		if err := v.emitValue(e.Expr); err != nil {
			return err
		}
		v.emitPushTypeID(from)
		v.emit(OpEnumValue)
		v.emitPushTypeID(primitive)
		v.emit(OpLoad)
		v.emitWordCast(primitive, to)
		return nil
	}
	if to.Kind() == syntax.TypeKindString {
		return v.emitStringCast(e.Expr, from)
	}
	// A null is the null of the type it is read as, which is what says whether
	// the slot it lands in owns a mark entry. An any slot is held as a pointer
	// to a heap box, so a null any is the zero word too, not a fresh box
	// standing for a null payload: allocating one would make null{any} compare
	// unequal to an uninitialized any, which no reader would expect.
	if e.Expr.ResolvedType() == syntax.TypeNull {
		v.emitPushNull(to)
		return nil
	}
	// The any branches each want the other kind to be anything but any, so
	// that casting an any to an any does not round-trip through a redundant
	// box or an unbox that would have no payload type to settle itself with.
	if to.Kind() == syntax.TypeKindAny && from.Kind() != syntax.TypeKindAny {
		return v.emitBoxAny(e.Expr, from)
	}
	if from.Kind() == syntax.TypeKindAny && to.Kind() != syntax.TypeKindAny {
		return v.emitUnboxAny(e.Expr, to)
	}
	if isDowncast(from, to) {
		return v.emitDowncast(e.Expr, from, to)
	}
	if from.Kind() == syntax.TypeKindString && isStringBytesPointer(to) {
		return v.emitStringBytesCast(e.Expr)
	}

	if err := v.emitValue(e.Expr); err != nil {
		return err
	}
	v.emitWordCast(from, to)
	return nil
}

// emitBoxAny packs a value and its type id into a fresh any object. The id
// above the value is what the runtime reads to settle the box's size and the
// walker's rule, so the pair is what [OpBoxAny] takes and the single pointer
// to the box is what it leaves.
//
// An any upcast is this and nothing else, since every type is a subtype of
// any — the plain upcast no-op in [emitWordCast] would silently leave the
// value standing where the any slot expected a pointer to a box.
func (v *operationVisitor) emitBoxAny(e syntax.Expr, from syntax.Type) error {
	if err := v.emitValue(e); err != nil {
		return err
	}
	v.emitPushTypeID(from)
	v.emit(OpBoxAny)
	return nil
}

// emitUnboxAny reads the value out of a box as the type it was written as. The
// check lives inside [OpAnyUnbox] rather than around it: a mismatched target
// cannot be answered with a null for a primitive, and the opcode traps instead
// so the shape of the output is settled before the unbox runs.
//
// A user that wants the question without the trap writes @v <: @{T}, which
// reads the stored id through [OpAnyPayloadType] without touching the payload.
func (v *operationVisitor) emitUnboxAny(e syntax.Expr, to syntax.Type) error {
	if err := v.emitValue(e); err != nil {
		return err
	}
	v.emitPushTypeID(to)
	v.emit(OpAnyUnbox)
	return nil
}

// isStringBytesPointer reports whether t is a pointer the string cast resolves
// to the string's bytes field: byte* or char*. A void* goes through the plain
// word cast instead, promising nothing of what lies at the address — including
// that it is the bytes rather than the mark word in front of them.
func isStringBytesPointer(t syntax.Type) bool {
	w, ok := syntax.StripConst(t).(*syntax.TypeWrapped)
	if !ok || w.Kind() != syntax.TypeKindPointer {
		return false
	}
	k := syntax.StripConst(w.Type).Kind()
	return k == syntax.TypeKindByte || k == syntax.TypeKindChar
}

// stringBytesOffset is how far past the string pointer its bytes begin: the
// mark word and the length word. It matches ZamString's layout in
// internal/5_build/runtime/src/instance.h and is what [emitStringBytesCast] shifts a string by.
const stringBytesOffset = 2 * wordByteSize

// emitStringBytesCast turns the string value on top of the stack into a pointer
// to its bytes[]. The string pointer names the mark word; its bytes lie two
// words past that, so the cast is a plain pointer add — no copy, no allocation,
// and no change to the underlying memory. The resulting pointer stays NUL
// terminated because every string carries a trailing NUL — see
// [[string-nul-terminator]] — so a C API that reads a char* reads this pointer
// as the string's content and nothing else.
//
// The mark entry stays on the result slot: the collector sees a pointer whose
// pointee has no header to follow, which is what it would see for any byte* or
// char*. The caller is responsible for keeping the string alive by holding a
// reference to it elsewhere — an interior pointer to a swept allocation would
// dangle, as it does in C.
func (v *operationVisitor) emitStringBytesCast(e syntax.Expr) error {
	if err := v.emitValue(e); err != nil {
		return err
	}
	v.emitPushWords(stringBytesOffset)
	v.emit(OpAddI64)
	return nil
}

// emitWordCast makes the word the operand left stand for a value of the target
// type.
func (v *operationVisitor) emitWordCast(from, to syntax.Type) {
	from, to = syntax.StripConst(from), syntax.StripConst(to)
	fk, tk := from.Kind(), to.Kind()

	// A word read as another type is marked as that type: the entry it owned
	// goes with the type that owned it, and the new type says whether it owns
	// one. A number and a void* own none, saying nothing of what lies at the
	// word, so a word read out of one of them is never taken into the
	// collector's sight: a native's address is a void*, and what it is cast to
	// is a C function rather than a closure.
	if !numeric(fk) && !isVoidPointer(from) && !from.Equals(to) {
		v.emitPushTypeID(to)
		v.emit(OpMarkAs)
	}

	// A value read as a type it already has is left alone: an upcast is the same
	// object seen as less of itself, which is nothing done to the value.
	if fk == tk || from.IsSubtypeOf(to) {
		return
	}

	// A bool is whether the value is there at all, which is the comparison
	// against zero that logical negation already is, answered the other way
	// around. An address answers that question the same way a number does, its
	// entry having just been dropped: the comparison is told nothing of the mark
	// stack, so a word it consumes must own nothing there.
	if tk == syntax.TypeKindBool {
		v.emitPushWords(0)
		v.emit(zeroComparison(fk))
		v.emitBooleanFlip()
		return
	}

	// An address is a whole word and so is an int, so either read as the other —
	// or an address read as one the target says nothing more about — leaves the
	// word as it stands. Any other pairing of an address with a number is not
	// the same word, and has no instruction that makes it one.
	if !numeric(fk) || !numeric(tk) {
		if (numeric(fk) && !isWholeWord(fk)) || (numeric(tk) && !isWholeWord(tk)) {
			panic("unreachable")
		}
		return
	}

	// A float is the one of these whose word is not the number itself, so
	// entering it is a conversion rather than a reinterpretation. A char and a
	// bool are small whole numbers, which is what an int already is, so all
	// three enter it the same way.
	if tk == syntax.TypeKindFloat {
		if fk == syntax.TypeKindUint {
			v.emit(OpCastU64ToF64)
		} else {
			v.emit(OpCastI64ToF64)
		}
		return
	}

	// Leaving a float drops the fraction, which lands on an int. A char is that
	// int narrowed to the low 32 bits a char is wide, a byte to the low 8, which
	// are the same narrowings an int is cast to those by; a bool already lies
	// inside both widths, so it has nothing to lose. Widening to an int is
	// nothing at all.
	if fk == syntax.TypeKindFloat {
		if tk == syntax.TypeKindUint {
			v.emit(OpCastF64ToU64)
		} else {
			v.emit(OpCastF64ToI64)
		}
	}
	if fk != syntax.TypeKindBool {
		v.emitNarrow(to)
	}
}

// emitStringCast writes a value out as a string. What that comes to depends on
// what the value is — a primitive converts, a declaration says for itself by its
// Str, and anything else reads as the address it lives at — so the type id goes
// down over the value and the runtime is left to choose. Nothing here has to
// know which it will be, which is what lets a cast written in a template, of a
// type no instantiation has bound yet, emit the same instruction as a cast of a
// type that is known.
func (v *operationVisitor) emitStringCast(object syntax.Expr, from syntax.Type) error {
	if err := v.emitValue(object); err != nil {
		return err
	}

	v.emitPushTypeID(from)
	v.emit(OpCastToString)
	return nil
}

// emitDowncast reads a value as a type below its own. Every other cast says what
// to do to the value, but this one only claims what the value already is, and
// nothing but the value can settle that: the runtime carries the type it was
// allocated as, which is what the claim is tested against. A value that really
// is of the target type is already exactly the word the cast asks for, so a
// claim that holds costs nothing beyond the test; one that fails has no value to
// answer with, so it answers null.
func (v *operationVisitor) emitDowncast(object syntax.Expr, from, to syntax.Type) error {
	if err := v.emitValue(object); err != nil {
		return err
	}

	// The test spends a copy of the value, which outlives no allocation and so
	// needs no mark stack entry of its own. Copying it before the type id goes
	// down is what leaves the value itself beneath the test, for the test to
	// keep or to discard.
	v.emit(newUnaryOp(OpDupWord, 0))
	v.emitPushTypeID(from)
	v.emit(OpTypeOf)
	v.emitPushTypeID(to)
	v.emit(OpIsSubtypeOf)

	failed := v.emitJump(OpJumpIfFalse)
	held := v.emitJump(OpJump)

	v.patch(failed)
	v.emit(OpPopWord)
	v.emitPushNull(to)

	v.patch(held)
	return nil
}

// isDowncast reports whether a cast claims what a value is rather than saying
// what to do to it. Reading a value as its own type, or as one above it, holds
// for every value of the source type, so there is nothing to settle; reading it
// as one below holds only for those values that are of that type. A void pointer
// claims nothing about what it addresses, so a cast to or from one is the word
// read as the other type and no claim at all.
func isDowncast(from, to syntax.Type) bool {
	from, to = syntax.StripConst(from), syntax.StripConst(to)
	if isVoidPointer(from) || isVoidPointer(to) {
		return false
	}
	return !from.IsSubtypeOf(to) && to.IsSubtypeOf(from)
}

// isVoidPointer reports whether t is the void* type, which is any address at
// all: it says nothing of what lies at it.
func isVoidPointer(t syntax.Type) bool {
	w, ok := syntax.StripConst(t).(*syntax.TypeWrapped)
	return ok && w.Kind() == syntax.TypeKindPointer && w.Type.Kind() == syntax.TypeKindVoid
}

// typeIDWords is how many operand words a runtime type id occupies, which is
// what [typeDefinitionLong.packed] spells out.
const typeIDWords = 3

// locationWords is how wide a location is: the pointer and the type id standing
// over it.
const locationWords = 1 + typeIDWords

// numeric reports whether a type is one of the primitives that is a single
// operand word standing for a number — an i64 int, a u64 uint, a u32 char, a u8
// byte, a bool that is 1 or 0, or an f64 float. Casting among these is
// arithmetic on that word.
func numeric(k syntax.TypeKind) bool {
	switch k {
	case syntax.TypeKindInt, syntax.TypeKindUint, syntax.TypeKindChar, syntax.TypeKindByte, syntax.TypeKindBool, syntax.TypeKindFloat:
		return true
	}
	return false
}

// isWholeWord reports whether a numeric kind is an integer as wide as an
// address, which is what lets the two be read as each other.
func isWholeWord(k syntax.TypeKind) bool {
	return k == syntax.TypeKindInt || k == syntax.TypeKindUint
}

// zeroComparison is how a value of the given kind is compared against zero. The
// zero word is +0.0 as well as 0, but a float has to be compared as one: under
// its own comparison a negative zero is zero, and a NaN is not.
func zeroComparison(k syntax.TypeKind) Opcode {
	if k == syntax.TypeKindFloat {
		return OpEqualF64
	}
	return OpEqualI64
}

// VisitExprNew allocates instances of the named type. The shape of the
// allocation goes down first — how many instances to make and how many tail
// elements each carries, both defaulting to a single untailed instance when the
// source omits them — and the type id lands on top of it, where every type id
// lands.
func (v *operationVisitor) VisitExprNew(e *syntax.ExprNew) error {
	if err := v.emitOrDefault(e.Size, 1); err != nil {
		return err
	}
	if err := v.emitOrDefault(e.Tail, 0); err != nil {
		return err
	}

	v.emitPushTypeID(e.Type)
	if e.Size != nil {
		v.emit(OpNewRun)
	} else {
		v.emit(OpNew)
	}
	return nil
}

// emitOrDefault emits an optional operand expression, falling back to a literal
// when the source omitted it.
func (v *operationVisitor) emitOrDefault(e syntax.Expr, fallback uint64) error {
	if e == nil {
		v.emit(newUnaryOp(OpPushI64, fallback))
		return nil
	}
	return v.emitValue(e)
}

// emitPushNull pushes the null of t. A null read as no type in particular, as
// one compared against is, is the null of void and owns no mark entry.
func (v *operationVisitor) emitPushNull(t syntax.Type) {
	v.emitPushTypeID(t)
	v.emit(OpPushNull)
}

// emitPushTypeID pushes the three words of t's runtime type id. An open type has
// no id until its template is instantiated, so it is read out of the receiver
// instead: the receiver's own type id indexes the enumeration of open types that
// every concrete resolved through its bindings.
//
// A generic static method's body is compiled per concrete instantiation. The
// method's T is a ground type at that point, so the bindings in effect are
// applied first and the ground result goes down the non-open path. Only an
// enclosing decl's T stays open, which the receiver branch handles.
func (v *operationVisitor) emitPushTypeID(t syntax.Type) {
	if v.methodBindings != nil {
		t = t.Map(v.methodBindings)
	}
	if !t.ContainsGenericArg() {
		v.emitPushWords(v.typeInstanceOf(t).packed())
		return
	}

	// The receiver's id is read off the mark header the receiver pointer lands
	// at, through OpPushReceiverType — the instance's shortID is what every
	// concrete resolves its open arguments through, and that shortID lies in the
	// header. OpTypeOf is not what does this: it is told a declared type and
	// finds the header by that type's shape, which cannot be the declaration's
	// own open template here (the stack holds a pointer, not an inline value) and
	// cannot be the pointer either (the open template pointer is interned
	// nowhere). The receiver-type opcode sidesteps that question entirely.
	//
	// The open type is indexed in the enumeration of the receiver's own
	// declaration — the one OpPushReceiverType reads a shortID of — rather than
	// the type's own [syntax.Type.OuterDecl]. The two may differ when a type
	// written in one declaration (e.g. a nested struct's method signature) is
	// consumed by another (e.g. a containing class's instance method), and only
	// the current receiver's enumeration is what the runtime will use.
	slot := ReceiverSlotClosure
	if v.receiverInFrame {
		slot = v.lookupLocal(thisVariableName).offset
	}
	v.emit(newUnaryOp(OpPushReceiverType, slot))
	v.emit(newUnaryOp(OpPushGenericArgType, v.genericTypeIndexInReceiver(t)))
}

// genericTypeIndexInReceiver returns t's position in the current receiver
// declaration's open-type enumeration. OpPushReceiverType reads the receiver's
// shortID, and the runtime indexes that declaration's own enumeration — not the
// enumeration of whatever declaration t was syntactically parsed in.
func (v *operationVisitor) genericTypeIndexInReceiver(t syntax.Type) uint64 {
	if d, ok := v.currentDecl.(syntax.DeclGeneric); ok && d.GenericTypes() != nil {
		if i := d.GenericTypes().Index(t); i >= 0 {
			return uint64(i)
		}
	}
	return genericTypeIndex(t)
}

// emitPushWords pushes each word as an i64 literal.
func (v *operationVisitor) emitPushWords(words ...uint64) {
	for _, word := range words {
		v.emit(newUnaryOp(OpPushI64, word))
	}
}

// VisitExprLambda builds a closure and steps over the body it closes. The
// captures are pushed as the locations they are held at, beneath the closure's
// own type, and the allocation copies out of them, since the frame they lie in
// is gone by the time the closure is called.
//
// The body is emitted where it was written, with a jump around it, so that the
// offset the closure is built with is known as soon as the body begins. What
// follows the body is the rest of the expression the lambda was written in, and
// that is where the jump lands, with the closure standing as the value.
func (v *operationVisitor) VisitExprLambda(e *syntax.ExprLambda) error {
	captures := v.lambdaCaptures(e)
	receiver, hasReceiver := v.findLocal(thisVariableName)

	for _, capture := range captures {
		v.emitPushLocal(capture.name)
	}
	v.emitPushTypeID(e.Type)

	lambda := newBinaryOp(OpNewLambda, 0, uint64(len(captures)))
	v.emit(lambda)
	over := v.emitJump(OpJump)

	lambda.first = uint64(v.byteOffset)
	if !hasReceiver {
		receiver.typ = nil
	}
	if err := v.emitLambdaBody(e, captures, receiver.typ); err != nil {
		return err
	}

	v.patch(over)
	return nil
}

// lambdaCaptures is what a lambda closes over by name: what its body reaches out
// of itself for, which static analysis recorded as it resolved those names. The
// receiver is not among them. A closure carries one already — it is what the
// closure a method is selected as carries — so a lambda inherits the receiver of
// the frame it was written in, and the body reads it out of the slot every frame
// keeps for it rather than out of a capture of its own.
func (v *operationVisitor) lambdaCaptures(e *syntax.ExprLambda) []capturedVariable {
	captures := make([]capturedVariable, 0, len(e.Captures))
	for _, name := range e.Captures {
		captures = append(captures, capturedVariable{name: name, typ: v.lookupLocal(name).typ})
	}
	return captures
}

// emitLambdaBody emits a lambda's body as the frame it will run in rather than
// the one it was written in, so everything that belongs to a frame is set aside
// and given back: the slots, the loops a jump could name, and what a return owes
// and where it goes back to. Slot 0 holds the closure the call was made
// through, the parameters follow it, and the captures follow those, which is
// where a call lays them.
//
// The receiver is reached through that closure, as a method body reaches its
// own, and is there whenever the frame the lambda was written in had one —
// which is what lets a generic argument in the body be read off the receiver,
// as it is anywhere else.
func (v *operationVisitor) emitLambdaBody(e *syntax.ExprLambda, captures []capturedVariable, receiver syntax.Type) error {
	outerScopes, outerOffset, outerOpen := v.scopes, v.nextVariableOffset, v.openSlots
	outerLoops, outerLabel := v.loops, v.pendingLabel
	outerReturnType, outerInMain := v.returnType, v.inMain
	outerReceiverInFrame := v.receiverInFrame
	defer func() {
		v.scopes, v.nextVariableOffset, v.openSlots = outerScopes, outerOffset, outerOpen
		v.loops, v.pendingLabel = outerLoops, outerLabel
		v.returnType, v.inMain = outerReturnType, outerInMain
		v.receiverInFrame = outerReceiverInFrame
	}()

	v.scopes = base.Stack[*variableScope]{}
	v.loops = base.Stack[*loopContext]{}
	v.pendingLabel = ""
	v.returnType = e.Type.ReturnType
	v.inMain = false
	v.receiverInFrame = false

	v.nextVariableOffset = 0
	v.openSlots = nil
	defer v.popScopes(v.pushDeclScopes(v.currentDecl, receiver != nil))

	v.pushScope()
	defer v.popScope()

	if receiver != nil {
		v.declareVariable(thisVariableName, receiver)
	}
	v.nextVariableOffset = 1
	v.declareParameters(e.Parameters, e.Type.ParameterTypes)
	for _, capture := range captures {
		v.declareVariable(capture.name, capture.typ)
	}

	if err := e.Body.Accept(v); err != nil {
		return err
	}

	if e.Type.ReturnType.Kind() == syntax.TypeKindVoid {
		v.emit(OpReturnVoid)
	}
	return nil
}

// VisitExprNative pushes a native function. Nothing is checked of the name: the
// implementation is C, and whether one answering to that name exists is settled
// when the program and the natives are linked.
func (v *operationVisitor) VisitExprNative(e *syntax.ExprNative) error {
	v.emit(newUnaryOp(OpNative, v.nativeIndex(e.ID)))
	return nil
}

// nativeIndex is the slot a native holds in the table the image carries,
// assigning it one the first time the name is seen. The names are C symbols and
// share that flat namespace, so two places naming the same native mean the same
// entry. Slots are handed out in the order the names are met and the table is
// written in that same order, which is what makes an operand and an entry agree.
func (v *operationVisitor) nativeIndex(name string) uint64 {
	if index, at := v.reader.natives.Get(name); at >= 0 {
		return index
	}

	index := uint64(v.reader.natives.Len())
	v.reader.natives.Set(name, index)
	return index
}

// VisitExprSymbol pushes a single-token operand. Literals push their value,
// while the symbols that denote a location — identifiers and the receiver —
// push a pointer, leaving it to the consumer to load through it.
func (v *operationVisitor) VisitExprSymbol(e *syntax.ExprSymbol) error {
	start := e.Start()
	switch e.Kind() {
	case syntax.ExprKindLiteralString:
		v.emit(newUnaryOp(OpPushStringLiteral, v.stringLiteralSlot(start.Value.(string))))
	case syntax.ExprKindLiteralChar:
		v.emit(newUnaryOp(OpPushI64, uint64(start.Value.(rune))))
	case syntax.ExprKindLiteralInteger, syntax.ExprKindLiteralUint:
		v.emit(newUnaryOp(OpPushI64, start.Value.(uint64)))
	case syntax.ExprKindLiteralFloat:
		v.emit(newUnaryOp(OpPushI64, math.Float64bits(start.Value.(float64))))
	case syntax.ExprKindLiteralBoolean:
		var value uint64
		if start.Kind == syntax.TTTrue {
			value = 1
		}
		v.emit(newUnaryOp(OpPushI64, value))
	case syntax.ExprKindLiteralNull:
		v.emitPushNull(syntax.TypeVoid)

	// A base subobject shares its address with the object, so super pushes the
	// receiver too; the static type of the expression selects the members.
	case syntax.ExprKindThis, syntax.ExprKindSuper:
		v.emitPushReceiver()

	// An identifier names whatever is in scope under it, which is a slot in the
	// frame, a slot in the static region, or a field of the receiver.
	case syntax.ExprKindIdentifier:
		v.emitPushName(start.Value.(string))

	default:
		panic("unreachable")
	}
	return nil
}

// VisitExprType names a type as a value, which is the type's own id. An open
// generic argument has no id until the template is instantiated, so it is read
// off the receiver instead, which [operationVisitor.emitPushTypeID] handles.
//
// Naming a static of that type names the slot the static lies in rather than the
// type, and emits as the location that slot is, which is what makes a static
// assignable as well as readable. The slot is the one the static initializers
// wrote before the program ran, so a read here reaches a value that is already
// there.
func (v *operationVisitor) VisitExprType(e *syntax.ExprType) error {
	if e.Field == "" {
		v.emitPushTypeID(e.Type)
		return nil
	}

	if concrete := e.Concrete; concrete != nil {
		fieldType := concrete.FieldType()
		v.emitPushStaticSlot(*concrete.Index(), fieldType)
		v.emitPushTypeID(fieldType)
		return nil
	}

	field := selectedStatic(e)

	// A generic static method named with type arguments the compile-time pass
	// did not resolve to one concrete falls into runtime dispatch: the
	// arguments contain an open generic — a class-level T whose binding comes
	// from the receiver — so the slot has to be chosen while that binding is
	// in hand. The register-and-emit helper handles the whole sequence and
	// leaves the stack in the same shape OpPushStatic + emitPushTypeID would.
	if method, ok := field.(*syntax.FieldMethod); ok && method.Generics() != nil && len(e.TypeArgs) > 0 {
		v.emitPushGenericStaticMethod(method, e.TypeArgs)
		return nil
	}

	fieldType, ok := staticSlotType(field)
	if !ok {
		panic("unreachable")
	}

	v.emitPushStatic(field, fieldType)
	v.emitPushTypeID(fieldType)
	return nil
}

// staticSlotType is the type of a static that lies in a slot of its own, and
// reports whether it is one at all. Every static is: each was written into its
// slot before the program ran, so naming one is reading that slot and it emits
// as the location that slot is.
//
// An enum value holds an instance of the enum, which is the only type it could
// hold and so the one it answers with. A static method holds the closure built
// over its body, which is a value of the method's own function type, so it is
// read like anything else and calling it is a call through what was read.
func staticSlotType(f syntax.FieldIndexable) (syntax.Type, bool) {
	switch f := f.(type) {
	case *syntax.FieldMember:
		return f.FieldType(), true
	case *syntax.FieldEnum:
		return f.Type(), true
	case *syntax.FieldMethod:
		return f.FieldType(), true
	}
	return nil, false
}

// selectedStatic resolves what naming a static picks out: the static field of
// the declaration the named type resolves to. Static analysis rejects a
// selection that names nothing, so a miss is a compiler bug.
func selectedStatic(e *syntax.ExprType) syntax.FieldIndexable {
	resolvable, ok := e.Type.(syntax.TypeResolvable)
	if !ok {
		panic("unreachable")
	}
	field, ok := resolvable.Decl().GetStaticField(e.Field)
	if !ok {
		panic("unreachable")
	}
	return field
}

// populateOperations emits the program's operations. What initializes the
// statics is emitted first, so that it runs before anything that could name one,
// and the main method follows it: execution begins at operations offset 0 and
// runs into the main body from there. Every other method body is emitted during
// the subsequent walk, with the main method guarded to avoid emitting it twice.
//
// The per-type instance initializers come last, past everything control can run
// into, since each is reached only by being called. They are emitted after the
// statics although a static of a declared type is one of the things that runs
// them: a routine is named by the offset recorded on its type definition rather
// than by an immediate at the site, so nothing has to know it yet.
func populateOperations(ast *syntax.AST, reader *codegenReader, typeIndices *syntax.TypeMap[int]) error {
	mainMethod, _ := ast.MainCodeDecl.GetStaticMethod(base.MainMethodName)
	reader.natives = base.NewStaticOrderedMap[string, uint64]()
	visitor := &operationVisitor{
		typeResolver: typeResolver{typeIndices: typeIndices, counts: reader.counts},
		ast:          ast,
		reader:       reader,
		mainMethod:   mainMethod,
	}

	if err := visitor.emitStaticInitializers(); err != nil {
		return err
	}
	// The main body begins where the statics leave off, which is what its
	// closure was built waiting to be told.
	if visitor.mainClosure != nil {
		visitor.mainClosure.first = uint64(visitor.byteOffset)
	}
	if err := visitor.emitMethod(mainMethod); err != nil {
		return err
	}

	for _, entry := range ast.CodeFiles.All() {
		if err := entry.Value.Decl.Accept(visitor); err != nil {
			return err
		}
	}

	if err := visitor.emitInstanceInitializers(); err != nil {
		return err
	}

	// Every body has been emitted by here, which is the first point any of the
	// method tables can be filled in: a table entry is where a body turned out
	// to lie, and until the body is emitted that is not yet a thing to know.
	visitor.recordMethodInstructions()
	return nil
}
