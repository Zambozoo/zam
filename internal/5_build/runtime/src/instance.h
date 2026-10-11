#pragma once

#include "common.h"
#include "type.h"

// An instance is a value as it actually lies in memory, which is a different
// question from the definition that describes it. definition.h reads the
// image's account of a type; this says what one of them is once it exists.
//
// Two kinds of thing are described here and they are worth keeping apart:
//
//   - Program types — class, struct, enum, and their generic forms — have no
//     shape of their own to state. Their members lie at the offsets the image
//     already carries in a ZamMemberDefinition, so the runtime never computes a
//     layout and there is nothing here to get wrong. What they do need is the
//     one thing the image cannot say, below: which of them carry a header.
//
//   - Primitives, the string and the closure are laid out by this file and
//     nowhere else. Their shape is the runtime's own decision and the compiler
//     agrees with it by construction rather than by being told, which is where
//     a disagreement would go unnoticed.
//
//   - A thread instance is a ZamThread, which thread.h lays out because it is
//     execution state and not a value. All this file says about one is what
//     sits in front of it, the same header a class or a closure gets.

// The primitive widths, mirroring primitiveByteInfo in 2_staticfields.go. This
// pair is hand-kept, like the opcode table: nothing in the image restates a
// primitive's width, so the two sides agreeing is an agreement between a Go
// switch and these constants and nothing else. TestPrimitiveLayoutCConformance
// is what holds them together.
//
// A bool is a byte and a char is four, so neither is a word — a member of one
// is packed at its own alignment inside an instance, and only the operand stack
// rounds everything up to a word.
#define ZAMBOOL_BYTES 1
#define ZAMBOOL_ALIGN 1
#define ZAMCHAR_BYTES 4
#define ZAMCHAR_ALIGN 4
#define ZAMBYTE_BYTES 1
#define ZAMBYTE_ALIGN 1
#define ZAMINT_BYTES 8
#define ZAMINT_ALIGN 8
#define ZAMUINT_BYTES 8
#define ZAMUINT_ALIGN 8
#define ZAMFLOAT_BYTES 8
#define ZAMFLOAT_ALIGN 8
// A string is the one-word pointer, not the bytes it addresses.
#define ZAMSTRING_BYTES 8
#define ZAMSTRING_ALIGN 8
// A value of type `type` is the three words a type id is written as, so a
// member holding one is as wide as ZamTypeIDWords and no narrower.
#define ZAMTYPE_BYTES (ZAMTYPE_ID_WORDS * ZAMWORD_BYTES)
#define ZAMTYPE_ALIGN 8
// An any value is the one-word pointer to the any box, not the box itself.
#define ZAMANY_BYTES 8
#define ZAMANY_ALIGN 8
// Void occupies nothing, and is here so that a table over the primitives has an
// entry for every one of them rather than a hole at the front.
#define ZAMVOID_BYTES 0
#define ZAMVOID_ALIGN 1

typedef bool ZamBool;
typedef uint32_t ZamChar;
typedef uint8_t ZamByte;
typedef int64_t ZamInt;
typedef uint64_t ZamUint;
typedef double ZamFloat;

_Static_assert(sizeof(ZamBool) == ZAMBOOL_BYTES, "bool is not its wire width");
_Static_assert(sizeof(ZamChar) == ZAMCHAR_BYTES, "char is not its wire width");
_Static_assert(sizeof(ZamByte) == ZAMBYTE_BYTES, "byte is not its wire width");
_Static_assert(sizeof(ZamInt) == ZAMINT_BYTES, "int is not its wire width");
_Static_assert(sizeof(ZamUint) == ZAMUINT_BYTES, "uint is not its wire width");
_Static_assert(sizeof(ZamFloat) == ZAMFLOAT_BYTES,
               "float is not its wire width");

// zam_primitive_bytes and zam_primitive_align answer for a primitive what a
// type definition answers for a declared type. They are switches rather than a
// table so that a primitive added to the enum without a width fails the build.
uint64_t zam_primitive_bytes(ZamPrimitive primitive);
uint64_t zam_primitive_align(ZamPrimitive primitive);

// zam_align_up is the rule every layout in the system is built on, and is here
// because the tail of an instance begins at one of these and the runtime is the
// side that finds it.
static inline uint64_t zam_align_up(uint64_t offset, uint64_t align) {
    if (align == 0)
        return offset;
    return (offset + align - 1) / align * align;
}

// What a markable allocation carries in front of it, and the pointer the
// program holds points past all of it.
//
// The word immediately beneath the pointer is always the mark word, whatever
// kind of thing it turns out to be. That is what makes a bare pointer enough:
// an ZAMMARK_OBJECT entry on the mark stack is an operand offset and says
// nothing about what it points at, so the thing pointed at has to be readable
// before anything is known about it. Reading the mark word is that first step,
// and its kind says how much more header is behind it.
//
// A struct or an enum has none of this. Both are values laid out inline whose
// type the compiler knows at every point they are touched, and neither is ever
// marked, so a header on one would be words paid on every instance to answer a
// question nobody asks.

// The mark states. Four of them is two bits, and there is no fifth waiting: the one
// this used to keep room for was "never collected", for a value the collector
// must reach and leave alone, and the contained bit below says that instead. A
// literal and a stack-resident instance are not allocations at all, which is a
// fact about the word rather than a colour it is painted.
#define ZAMMARK_STATE_BITS 2

typedef enum ZamMarkState {
    ZAMMARK_STATE_WHITE,
    ZAMMARK_STATE_GREY,
    ZAMMARK_STATE_BLACK,
    // Dead is what an explicit delete leaves, and is the one state the program
    // rather than the collector puts an allocation in. White would not do: a
    // fresh allocation is already white, so a delete that set it would say
    // nothing, and a stale reference reaching the block would turn it grey and
    // bring it back. A delete is the program asserting the block is finished
    // with whether or not anything still points at it, so the mark phase must
    // refuse to promote this state and the sweep must free and unlink it.
    ZAMMARK_STATE_DEAD,
} ZamMarkState;

// The kind of a markable allocation, which is the one thing about it knowable
// without reading anything else. It is here rather than being worked out from a
// type id because a string has no type definition to work it out from, and
// because the collector's first question of an allocation is which of these it
// is rather than which type it is.
#define ZAMOBJECT_KIND_BITS 3

typedef enum ZamObjectKind {
    // A string is the mark word and then a ZamString, and is the one kind that
    // carries no type id: nothing about a string varies, so there is no
    // definition to find and the kind has already said everything.
    ZAMOBJECT_STRING,
    // An object is a class allocation, and its type id is what its layout is
    // looked up through.
    //
    // A tailed one is its own kind because its header is a word longer, and
    // nothing else on hand says so. A tail length rides in the long type id
    // everywhere else, but a class reached polymorphically is reached under a
    // supertype's id and a supertype cannot state a length only the concrete
    // type has — nor even whether there is one, an untailed abstract being
    // allowed a tailed child. So the kind is asked, and the instance answers
    // for itself however it was named.
    ZAMOBJECT_INSTANCE,
    ZAMOBJECT_TAILED_INSTANCE,
    // A closure's type id names the function type it satisfies, and a thread's
    // the thread type whose definition says what a join gets back. Neither is
    // needed to walk the allocation — a closure states its own captures and a
    // thread is a ZamThread — but a kind cannot answer *which* function or
    // *which* thread, and both are asked that.
    //
    // A closure is three kinds rather than one because its first entry is three
    // different things and every reader of one has to know which. A function
    // closure has no receiver at all; a reference method closure's entry holds
    // the one word an address is, and what it names lives elsewhere; a value
    // method closure's entry holds the receiver itself, the closure being the
    // only home a homeless value got. The collector has to visit the second
    // and walk into the third, and OpCallMethod has to hand the frame a word
    // from one or the address of the other. Reading the kind is how both learn
    // which, without a type definition in hand.
    ZAMOBJECT_FUNCTION_CLOSURE,
    ZAMOBJECT_REFERENCE_METHOD_CLOSURE,
    ZAMOBJECT_VALUE_METHOD_CLOSURE,
    ZAMOBJECT_THREAD,
    // An any box carries a value whose type is not known at the slot and may
    // vary from one assignment to the next. The pointer the program holds
    // names the mark word; past it lie a shortID of ZAMOBJECT_ANY's own id,
    // the three words of the payload's type id, and a pointer to the payload
    // box the value lives in. The walker's rule is fixed by the kind — a
    // follow of word 5 as a pointer and nothing else — so the stored id is
    // plain data from the collector's point of view, read only by OpAnyUnbox
    // and OpAnyPayloadType when a program asks what the any holds.
    //
    // The payload is a markable allocation of its own, headered like any
    // other instance; its own kind is what the walker reads when it reaches
    // it through this one's payload word.
    ZAMOBJECT_ANY,

    ZAMOBJECT_KIND_COUNT,
} ZamObjectKind;

_Static_assert(ZAMOBJECT_KIND_COUNT <= (1 << ZAMOBJECT_KIND_BITS),
               "the object kinds no longer fit the mark word's kind field");

// The upvalue is the collector's own field in every markable value, and is a
// pointer's worth of room: 56 bits covers every address on the platforms this
// runs on, where the top byte of a pointer is not part of the address.
//
// It holds one of two things, and the contained bit is which. On an allocation
// it is the link to the next one on the sweep list. On a value that is not an
// allocation it is how far back its container lies, which is what makes an
// interior pointer resolvable to the block that owns it.
//
// A thread is the one allocation that does not need the link.ZamThread is
// already threaded onto the VM's chain by its own next field, so a second link
// through the mark word would be one kept in step with one that already exists.
// The field is still there — every mark word is one shape — and is simply unread
// on a thread.
#define ZAMUPVALUE_BITS 56

typedef struct ZamMarkWord {
    uint64_t state : ZAMMARK_STATE_BITS;
    uint64_t kind : ZAMOBJECT_KIND_BITS;

    // Whether this value lies inside something else rather than being an
    // allocation of its own, which is the question that decides what the upvalue
    // means and whether the block may be freed.
    //
    // A class is a value laid out inline, so a markable value turns up in three
    // places and only one of them is the allocator's. It can be a block zam_vm_alloc
    // handed out; it can be a member of one, laid inline inside it with a mark
    // word of its own; or it can stand on the operand stack or in the static
    // region, where the storage belongs to neither the heap nor another object.
    // All three carry the identical header, so nothing but this bit tells them
    // apart — and a sweep that guessed wrong would free memory malloc never
    // returned.
    //
    // Set, the upvalue is how many bytes back the immediate container's own mark
    // word lies, and zero means there is no container. Zero is free to mean that
    // because a container's header always precedes the member laid inside it, so
    // a real distance is never less than one header. That is what lets one bit
    // cover both the interior case and the stack-and-static case: the first has a
    // container to walk to, the second has none, and neither is a link.
    //
    // Backwards and relative, which is the property the whole layout rests on. A
    // distance from a value to the thing enclosing it does not change when both
    // move together, so the body of an instance can be copied with one memcpy and
    // every interior mark word inside it is still right about where its container
    // is. An absolute address would have had to be found and rewritten at every
    // depth on every copy, which is a walk of the definition on an assignment.
    //
    // The immediate container and not the outermost allocation, which matters for
    // the copy that is only part of a container: assigning one inline member from
    // a value standing elsewhere moves a nested value nearer to or further from
    // the allocation, but leaves its distance to its own enclosing value exactly
    // as it was. So the chain is what is walked to reach the allocation, each step
    // landing at a lower address than the one before and the last one clear.
    uint64_t contained : 1;

    // Two bits spare. The collector is the one thing likely to want them, and it
    // is not written yet.
    uint64_t reserved : 2;

    uint64_t upvalue : ZAMUPVALUE_BITS;
} ZamMarkWord;

_Static_assert(ZAMMARK_STATE_DEAD < (1 << ZAMMARK_STATE_BITS),
               "the mark states no longer fit the mark word's state field");

_Static_assert(sizeof(ZamMarkWord) == ZAMWORD_BYTES,
               "the mark word is meant to be one word");

// The type id behind the mark word, which every kind but a string carries. One
// word is enough for the generic forms too: the short id is the declaration id
// in its high half and the instantiation's generic id in its low, so
// LinkedList[int] and LinkedList[string] are already different values here and a
// generic class needs nothing a plain one does not.
typedef struct ZamObjectHeader {
    uint64_t shortID : 32;
    // elementCount is how many instances lie behind the pointer, since OpNew
    // allocates an array of them and the collector has to walk all of them
    // rather than the first. One for the ordinary single allocation, which is
    // what a closure and a thread always are.
    uint64_t elementCount : 32;
} ZamObjectHeader;

_Static_assert(sizeof(ZamObjectHeader) == ZAMWORD_BYTES,
               "the object header is meant to be one word");

// An object's header is the mark word and then the type id, the mark word
// leading because it is the word the pointer the program holds names.
typedef struct ZamTypedObjectHeader {
   ZamMarkWord mark;
   ZamObjectHeader type;
} ZamTypedObjectHeader;

// A tailed object carries its tail length as a third word, because that is the
// one thing about a tailed instance the type id may not be able to say.
// Everywhere else a tail length rides in the long type id and never in the
// instance — but a class reached polymorphically is reached under a supertype's
// id, and a supertype cannot state a length only the concrete type has. So the
// length goes where the dynamic type already went.
//
// The tail length is a whole word rather than a bitfield somewhere: a tail is
// an element count, and capping it at 2^32 to save a word on the one kind of
// allocation that is already the largest would be paying in the wrong place.
typedef struct ZamTailedObjectHeader {
   ZamTypedObjectHeader head;
    uint64_t tailLength;
} ZamTailedObjectHeader;

_Static_assert(sizeof(ZamTypedObjectHeader) == 2 * ZAMWORD_BYTES,
               "an object's header is meant to be two words");
_Static_assert(sizeof(ZamTailedObjectHeader) == 3 * ZAMWORD_BYTES,
               "a tailed object's header is meant to be three words");

// The whole point of the ordering, and the thing not to break: every header is
// the shorter one with another word after it, so the mark word sits at the
// pointer itself in all three shapes and zam_mark_word_of is right about every
// one of them. Each step forward is licensed by what the step before it read —
// the kind says whether there is a type id, and the definition found by that
// type id says whether there is a tail length.
//
// That the mark word leads is what lets an instance be reached by a pointer to
// the whole of it rather than to the middle of it. A class value is not only
// ever behind a pointer the allocator handed out — it is copied onto the operand
// stack, laid inline inside another class, and taken into a closure — and in
// every one of those places the value is its header and then its members, so a
// pointer to the value is a pointer the collector can read.
_Static_assert(offsetof(ZamTypedObjectHeader, mark) == 0,
               "the mark word must be the word the pointer names");
_Static_assert(offsetof(ZamTailedObjectHeader, head.mark) == 0,
               "the mark word must be the word the pointer names");

// zam_mark_word_of is the word every markable allocation begins with, and so is
// the pointer the program holds read as what it names. It is only ever asked of
// a pointer the runtime already knows to be one. It is not const: marking is the
// collector writing to exactly this word.
static inline ZamMarkWord *zam_mark_word_of(void *object) {
    return (ZamMarkWord *)object;
}

// zam_object_header_of is the word after that, and is there for every kind but
// ZAMOBJECT_STRING. A string is the one allocation whose kind settles its whole
// shape, so it is the one that stores no type id to be read here.
//
// It is not const, for the same reason zam_mark_word_of is not: the allocator
// fills this word in, and having it do so through the accessor is what keeps the
// step to it written down once.
static inline ZamObjectHeader *zam_object_header_of(void *object) {
    return (ZamObjectHeader *)((ZamWord *)object + 1);
}

// zam_object_tail_length_of is the word after that one, and is only there when
// the definition names a tail. Asking it of an untailed allocation reads the
// first word of the members, which is why the definition is consulted first
// rather than this being made to answer for both.
static inline uint64_t zam_object_tail_length_of(const void *object) {
    return ((const ZamWord *)object)[2];
}

// zam_object_header_bytes is how far past the pointer the members begin, which
// is what a reader of one has to add and what the allocator has to leave room
// for. The kind answers it alone — that is what having a kind per header shape
// buys, and why a tailed instance is a kind of its own rather than a flag
// somewhere the reader would have to find first.
//
// A tailed struct is not answerable here and is not meant to be. It keeps a tail
// length and no mark word, so there is no kind to ask; what says its header is
// there is its definition, which is where instanceHeaderByteSize in
// 2_staticfields.go puts the same claim on the other side.
static inline uint64_t zam_object_header_bytes(ZamObjectKind kind) {
    if (kind == ZAMOBJECT_STRING)
        return sizeof(ZamMarkWord);
    if (kind == ZAMOBJECT_TAILED_INSTANCE)
        return sizeof(ZamTailedObjectHeader);
    return sizeof(ZamTypedObjectHeader);
}

// zam_container_of is the value one step out: the thing this one lies inside, or
// NULL where it lies inside nothing. It is the upvalue read the way the contained
// bit says to read it, and it is subtraction rather than a fetch because the
// distance is backwards and relative.
//
// A contained value with no container is one standing on the operand stack or in
// the static region. That is storage belonging to no object, so there is nothing
// to step out to, which is a different answer from an allocation's — an
// allocation is not contained at all and its upvalue is a sweep-list link.
static inline void *zam_container_of(void *value) {
   ZamMarkWord *mark = zam_mark_word_of(value);
    if (!mark->contained || mark->upvalue == 0)
        return NULL;
    return (uint8_t *)value - mark->upvalue;
}

// ZAMTAILED_STRUCT_HEADER_BYTES is how far past the pointer a tailed struct's
// members begin. A struct is the coder's own memory and is never collected, so
// it carries no mark word; it has no base either, so it is never reached under
// another type's id and needs no type word to say what it actually is. The one
// thing left that nothing else can supply is the length of its tail, and that is
// the whole of its header. Every other struct and every enum has none at all.
#define ZAMTAILED_STRUCT_HEADER_BYTES ZAMWORD_BYTES

// A string is its header and then a length and its bytes. The header is only
// the mark word, a string being the one kind whose kind settles its whole shape,
// and it is the same shape a string literal is interned as in the static region
// — which is what lets a literal be pushed as the address of its slot with
// nothing copied.
//
// The compiler writes the mark word ahead of a literal the way an allocation
// carries one, so no operation has to ask which of the two it was handed. A
// literal's word is written empty and stays that way: the static region is never
// collected, so the word is there to make the shape uniform rather than to be
// read.
//
// The bytes are both length-prefixed and NUL-terminated. The length is still
// what every string operation walks by — a string can hold a zero byte and
// still be the string it was — so the terminator is never read on the zam
// side. It is laid down for the C side: a native handing a string to a
// NUL-terminated API (open, perror, inet_pton) can pass ZamString.bytes
// without copying it into a terminated buffer first. The terminator is one
// byte past `length`, zeroed by the allocator and preserved by every writer
// below.
typedef struct ZamString {
   ZamMarkWord mark;
    uint64_t length;
    char bytes[];
} ZamString;

_Static_assert(sizeof(ZamString) == 2 * ZAMWORD_BYTES,
               "the string head is meant to be a mark word and a length");
_Static_assert(offsetof(ZamString, mark) == 0,
               "the mark word must be the word the pointer names");

// zam_string_bytes is how much of the heap or the static region the length and
// the bytes occupy, rounded up so the slot after them starts on a word. This is
// stringLiteral.ByteCount in 1_stringliterals.go and has to stay it. The mark
// word is not counted, being the header rather than the string — internedString
// is what adds it on that side.
//
// The +1 is the trailing NUL — see ZamString's note above. It rides inside the
// align-up where it has room, and widens the slot by a whole word only when the
// payload already filled one.
static inline uint64_t zam_string_bytes(uint64_t length) {
    return zam_align_up(sizeof(ZamString) - sizeof(ZamMarkWord) + length + 1,
                        ZAMWORD_BYTES);
}

// zam_string_of is the string a pointer names, which is the pointer itself: the
// header a string carries is the first field of one.
static inline ZamString *zam_string_of(void *object) {
    return (ZamString *)object;
}

// A closure is a body and everything that body needs which is not an argument:
// the receiver it was built over and the values it closed over. It begins with
// its typed object header, and the short id there names the function type it
// satisfies. That the allocation is a closure at all is the mark word's kind;
// the short id is the further question of which function it is, which a caller
// asks and the kind cannot answer.
//
// Every value in one is written as its type id and then the value beneath it.
// The type is stored rather than inferred because what the collector has to
// visit is not derivable from the words themselves and no signature states it —
// a capture is not a parameter, so nothing outside the closure describes it.
//
// The receiver is written in exactly that form as the first of them, which is
// what makes walking a closure one loop rather than a special case followed by
// a loop. It is stored the way OpPushMethod pushed it: a pointer receiver as
// the single word it is, a value receiver entire and then a second entry
// holding the address of that copy, since the body reads a receiver as an
// address whichever kind it was called through. Which of the three the first
// entry is is the kind's to say — a function closure writes a void entry there
// and has no receiver — and the type id under it says only how wide it is.
typedef struct ZamClosure {
   ZamTypedObjectHeader header;
    // instruction is where the body begins in the operations section.
    uint64_t instruction;
    // captureCount is how many captures follow the receiver. The caller counts
    // none of them: captures are laid past the arguments on entry, which is
    // where the body was compiled to read them.
    uint64_t captureCount;
    // body is the receiver's entries and then captureCount more. Every entry is
    // variable-width, so the only way to the second is through the first —
    // which is what a collector walking a closure does anyway.
    //
    // The receiver is one entry, except in a value method closure, where it is
    // two: the value and then the address of it. So a capture walk begins one
    // entry further along there than it does in the other two kinds, and the
    // kind is what says which. See OpPushMethod for why the second is written.
   ZamWord body[];
} ZamClosure;

_Static_assert(sizeof(ZamClosure) == 4 * ZAMWORD_BYTES,
               "the closure head is meant to be a typed header and two words");
_Static_assert(offsetof(ZamClosure, header.mark) == 0,
               "the mark word must be the word the pointer names");

// zam_closure_of is the closure a pointer names, which is the pointer itself:
// the typed header every closure carries is the first field of one.
static inline ZamClosure *zam_closure_of(void *object) {
    return (ZamClosure *)object;
}

// zam_closure_first is the receiver's first entry, and everything after it is
// reached by zam_closure_entry_next in turn — the address entry a value method
// closure keeps, and then every capture.
static inline const ZamWord *zam_closure_first(const ZamClosure *closure) {
    return closure->body;
}

// zam_closure_entry_type is what the entry holds and zam_closure_entry_value is
// the words beneath it. A void type on the first entry is a closure with no
// receiver, and its words are none.
static inline ZamTypeID zam_closure_entry_type(const ZamWord *entry) {
    return zam_type_id_read(entry);
}

static inline const ZamWord *zam_closure_entry_value(const ZamWord *entry) {
    return entry + ZAMTYPE_ID_WORDS;
}

// zam_closure_entry_next is given the width the caller worked out from the
// entry's own type, since how wide a value is is the type definition's to say
// and this file is told no definitions.
static inline const ZamWord *zam_closure_entry_next(const ZamWord *entry,
                                                    uint64_t valueWords) {
    return entry + ZAMTYPE_ID_WORDS + valueWords;
}

// A thread instance is a ZamThread, which begins with its own object header
// the way every other entity does. thread.h lays it out because it is execution
// state rather than a value. The short id in the typed header names a thread
// type, and that is how a join learns what the result is. That the allocation
// is a thread at all is the mark word's kind.
//
//ZamThread is already what the user meant by a thread getting its own vm: it
// holds a line of execution's own state — its two stacks, where it is in the
// operations, what it has allocated since the last collection — and points at
// the ZamVM it shares the statics, the instructions and the type definitions
// with. Nothing about a spawned thread is a copy of the program.
//
// So there is no frame here and no result field. The frame OpNewThread builds
// is pushed onto the new thread's own operand stack, which is where a call
// already puts one; the value the body returns is left on that same stack,
// which is where returning already puts one; and whether it has finished is
//ZamThread.result, which the run loop already sets. A second account of any of
// the three would only be a thing to keep in step.
