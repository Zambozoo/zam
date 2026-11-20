#include "zam/native.h"

#include "definition.h"
#include "gc.h"
#include "posit.h"
#include "run.h"
#include "thread.h"
#include "vm.h"
#include "zamrt.h"


// How many operand words a value of the type occupies. The stack counts in
// words and a type is measured in bytes, so anything narrower than a word — a
// bool, a char — still stands on one, and a void stands on none.
static uint64_t valueWordsOf(const ZamImage *image, ZamTypeID type) {
    return zam_align_up(zam_type_bytes(image, type), ZAMWORD_BYTES) /
           ZAMWORD_BYTES;
}

// Whether a value of the type stands on the stack as an address rather than as
// the value itself. That is what decides whether the word pushed for it owns a
// mark entry, and it is the second of the two things the type id above a
// location is read for.
//
// An array is laid out inline however its elements are held, so a run of them is
// not one address and is not answered here.
static bool isReferenceType(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    if (type.arrayLength != 0)
        return false;

    switch (zam_image_bucket_of(image, declID)) {
    // A string is the one primitive held as an allocation. Every other one is
    // its own bits, which is what makes it a primitive.
    case ZAMBUCKET_PRIMITIVE:
        return (ZamPrimitive)declID == ZAMPRIMITIVE_STRING;

    case ZAMBUCKET_POINTER:
    case ZAMBUCKET_THREAD_TYPE:
    case ZAMBUCKET_FUNCTION_TYPE:
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
        return true;

    // A class, struct, enum or tuple lies inline where it stands, so what is on
    // the stack is the value and there is no address to follow.
    case ZAMBUCKET_TUPLE:
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
    case ZAMBUCKET_ENUM:
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_GENERIC_CLASS:
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_GENERIC_ABSTRACT:
        return false;
    }

    zam_die(ZAMEXIT_IMAGE, "type id %u falls in no bucket\n", declID);
}

// Whether the type lies inline as a class or an abstract, which is what
// carries a mark word wherever it lies.
static bool isClassBucket(const ZamImage *image, uint16_t declID) {
    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_GENERIC_CLASS:
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_GENERIC_ABSTRACT:
        return true;
    default:
        return false;
    }
}

// Whether the one word a value of the type is names something with a mark word
// the collector owns: a string, a closure, a thread, an any box, or an instance
// reached through an interface or a pointer to a class. A pointer to anything
// else is the program's own memory and is never followed.
bool zam_type_is_markable_reference(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    if (type.arrayLength != 0)
        return false;

    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_PRIMITIVE:
        return (ZamPrimitive)declID == ZAMPRIMITIVE_STRING ||
               (ZamPrimitive)declID == ZAMPRIMITIVE_ANY;
    case ZAMBUCKET_THREAD_TYPE:
    case ZAMBUCKET_FUNCTION_TYPE:
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
        return true;
    case ZAMBUCKET_POINTER: {
        uint16_t pointee = ZAMTYPE_DECL(
            zam_pointer_pointee(zam_pointer_definition(image, declID)).shortID);
        switch (zam_image_bucket_of(image, pointee)) {
        case ZAMBUCKET_INTERFACE:
        case ZAMBUCKET_GENERIC_INTERFACE:
            return true;
        default:
            return isClassBucket(image, pointee);
        }
    }
    default:
        return false;
    }
}

// Whether a value of the type, wherever it lies, holds anything the collector
// must visit: a markable reference, a class held by value, or a struct, enum or
// run with either inside it. The answer for a declaration is worked out once
// and kept, a struct's being the answer of its members.
bool zam_type_contains_markable(ZamVM *vm, ZamTypeID type) {
    const ZamImage *image = &vm->image;
    if (type.arrayLength != 0) {
        type.arrayLength = 0;
        return zam_type_contains_markable(vm, type);
    }
    if (zam_type_is_markable_reference(image, type))
        return true;

    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_GENERIC_CLASS:
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_GENERIC_ABSTRACT:
        return true;
    case ZAMBUCKET_TUPLE:
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
    case ZAMBUCKET_ENUM:
        break;
    default:
        return false;
    }

    uint8_t known = atomic_load_explicit(&vm->markableDecls[declID],
                                         memory_order_relaxed);
    if (known != 0)
        return known == 2;

    bool contains = false;
    uint64_t count = zam_type_member_count(image, type.shortID);
    for (uint64_t index = 0; index < count && !contains; index++) {
        ZamMemberDefinition member = zam_type_member(image, type.shortID, index);
        contains = zam_type_contains_markable(vm, zam_member_type(&member));
    }
    atomic_store_explicit(&vm->markableDecls[declID], contains ? 2 : 1,
                          memory_order_relaxed);
    return contains;
}

// Which primitive the type is, or ZAMPRIMITIVE_COUNT where it is not one. An
// array of primitives is not one: what stands on the stack is the run.
static ZamPrimitive primitiveOf(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    if (type.arrayLength != 0 ||
        zam_image_bucket_of(image, declID) != ZAMBUCKET_PRIMITIVE)
        return ZAMPRIMITIVE_COUNT;
    return (ZamPrimitive)declID;
}

// What a pointer type points at, and the type itself where it is not one. A
// method or a member is reached on the instance whether the program named it
// through a pointer or held the instance directly, so what resolves an index
// against a type steps through this rather than each deciding what a pointer
// means.
static ZamTypeID pointeeOf(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    if (type.arrayLength != 0 ||
        zam_image_bucket_of(image, declID) != ZAMBUCKET_POINTER)
        return type;
    return zam_pointer_pointee(zam_pointer_definition(image, declID));
}

// Whether the type is a pointer to an enum or struct, which is what a string
// cast asks Str of rather than writing out as an address: see
// ZAMVALUE_SLOT_STR.
static bool namesValueWithStr(const ZamImage *image, ZamTypeID type) {
   ZamTypeID pointee = pointeeOf(image, type);
    if (pointee.shortID == type.shortID || pointee.arrayLength != 0)
        return false;
    switch (zam_image_bucket_of(image, ZAMTYPE_DECL(pointee.shortID))) {
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
    case ZAMBUCKET_ENUM:
        return true;
    default:
        return false;
    }
}

// A float stands on the stack as its bits, so reading one back is a
// reinterpretation and not a conversion. memcpy is how that is written without
// punning through a cast or a union, and every compiler this is built with
// turns it into the register move it is.
static ZamFloat floatOf(ZamWord word) {
   ZamFloat value;
    memcpy(&value, &word, sizeof value);
    return value;
}

// Discarding a word and the mark entry it owns, which is what spending a value
// off the stack is made of. The pop comes first so that
// its underflow check runs, and the entry is then looked for at the index the
// pop just freed.
static void popWord(ZamThread *thread) {
    zam_thread_pop(thread);
    zam_thread_pop_mark(thread, thread->operandCount);
}

// Spending a whole value, however many words it stands on. A discard is this
// and the tail of a store is this: the two differ in what they did with the
// value first and not in how they let go of it.
static void popValue(ZamThread *thread, const ZamImage *image, ZamTypeID type) {
    for (uint64_t word = valueWordsOf(image, type); word > 0; word--)
        popWord(thread);
}

// Declared ahead of its neighbours because a load has to know it: it sits with
// the other questions asked of a type id, which are grouped together below.
static bool isMarkableInstance(const ZamImage *image, ZamTypeID type);

// Declared ahead of itself for the same reason, a comparison of two values
// needing it. It is defined beside valueHeaderBytes rather than here because
// that is where both of the things it asks are already in scope.
static uint64_t inlineValueHeaderBytes(const ZamImage *image, ZamTypeID type);
static uint64_t boxedHeaderBytes(const ZamImage *image, ZamTypeID type);

// Declared ahead of itself because a comparison of two values has to descend
// into a member that answers for itself, which this says a member does. It
// sits with the other questions asked of a type id rather than here.
static bool answersThroughObject(const ZamImage *image, ZamTypeID type);

// Declared ahead of themselves for the overrides that run through them.
static void *instanceAt(const ZamThread *thread, const ZamImage *image,
                        ZamTypeID type, const ZamWord *value);
static bool callCompiled(ZamThread *thread, void *instance, uint64_t slot,
                         ZamWord argument, ZamWord *answer);
static bool callCompiledAs(ZamThread *thread, void *receiver, ZamTypeShort shortID,
                           uint64_t slot, ZamWord argument, ZamWord *answer);

// Declared ahead of itself for the spawned thread's join.
static void joinThread(ZamThread *thread, ZamThread *target);

// The mark entry the words of a value own, the value standing at the top of the
// stack. Whatever pushed it decided how the words got there and this decides
// what the collector is told of them, so every push of a whole value of a type
// the program named says it the same way.
//
// Only what the collector must visit is named. A markable reference gets an
// entry of its own, and a pointer into the program's own memory gets none.
//
// A run of markable references gets one entry over the run rather than one for
// each element. One is all it could get: the type id above a location names the
// run, so the words arrive together rather than an element at a time for each
// to be marked as it lands. The element type and the count are what the entry
// carries instead, which is enough to walk it. Any other value laid out inline
// that holds something markable gets one entry naming it and its type.
//
// The entry names the front word, and that is also what makes letting go of it
// right. Words go one at a time from the top, so only the last of them frees
// the index the entry named — the run stays reachable until none of it is left.
static void markPushedValue(ZamThread *thread, const ZamImage *image,
                           ZamTypeID type) {
    if (zam_type_is_markable_reference(image, type)) {
        zam_thread_push_mark(thread, thread->operandCount - 1);
        return;
    }

    uint64_t front = thread->operandCount - valueWordsOf(image, type);
   ZamTypeID element = type;
    element.arrayLength = 0;
    if (type.arrayLength != 0 && zam_type_is_markable_reference(image, element)) {
        zam_thread_push_array_mark(thread, type, front);
        return;
    }
    if (zam_type_contains_markable(thread->vm, type))
        zam_thread_push_value_mark(thread, type, front);
}

// Pushing a value that lies at a location, which is the whole of what a load
// leaves. The words go on zeroed and are then written over, so that a type
// narrower than a word — a bool, a char — leaves the rest of its word clear
// rather than holding whatever the address happened to be followed by.
//
// The location is carried in rather than an address, because a frame location
// names a slot in the very stack the pushes below may have moved: an address
// worked out before them would name where the slot used to be.
static void pushValueFrom(ZamThread *thread, const ZamImage *image,
                         ZamTypeID type, ZamWord location) {
    uint64_t words = valueWordsOf(image, type);
    for (uint64_t word = 0; word < words; word++)
        zam_thread_push(thread, 0);

    uint8_t *value = (uint8_t *)(thread->operands + thread->operandCount - words);
    memcpy(value, zam_thread_location_address(thread, location),
           zam_type_bytes(image, type));

    // A markable value copied here is a value in a new place, and the mark word
    // is the part of it that was about the old one. Its kind and the object
    // header behind it come over as they stand — those say what the value is,
    // which the copy has not changed — and the rest is written afresh for where
    // it now lies: on the operand stack, so contained and inside nothing.
    //
    // What is being thrown away is the source's answer, and either answer would
    // be wrong here. An allocation's word holds the sweep-list link, and a copy
    // of it would be a second value claiming to be that block — deletable, and
    // freeing memory the copy does not own. An interior member's word holds how
    // far back its container lies, and a copy of that on the stack would point
    // below itself at whatever the frame happens to hold.
    //
    // Only the root. Every header inside the value is a distance to the thing
    // enclosing it, and the copy moved both together, so all of them are still
    // right — which is the whole reason the distance is relative.
    if (isMarkableInstance(image, type)) {
       ZamMarkWord *mark = zam_mark_word_of(value);
        mark->state = ZAMMARK_STATE_WHITE;
        mark->contained = 1;
        mark->upvalue = 0;
    }

    markPushedValue(thread, image, type);
}

// The same place said as a location rather than as an address, which is what
// something that may push while it reads has to be given instead: a frame
// location is a byte offset from the bottom of the stack, so pushes above it
// leave it alone and a realloc beneath it does not move it.
static ZamWord locationAt(const ZamThread *thread, uint64_t above,
                          uint64_t words) {
    if (above + words > thread->operandCount)
        zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
    return zam_frame_location((thread->operandCount - above - words) *
                              ZAMWORD_BYTES);
}

// Whether the type is one laid out where it stands, so that the bytes are the
// value: a struct, an enum, and a class or abstract held by value rather than
// reached through an address.
//
// The buckets are named rather than this being everything that is not a
// reference, which would fold in a float and a type id. A float is not its bits
// for any of these questions — IEEE has -0.0 equal to 0.0 and a NaN equal to
// nothing — and what it should be instead is still open, so it has to reach a
// stub rather than quietly become a run of bytes.
static bool isInlineValue(const ZamImage *image, ZamTypeID type) {
    if (type.arrayLength != 0)
        return false;
    switch (zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID))) {
    case ZAMBUCKET_TUPLE:
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
    case ZAMBUCKET_ENUM:
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_GENERIC_CLASS:
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_GENERIC_ABSTRACT:
        return true;
    default:
        return false;
    }
}

// The run of bytes an inline value is, which is everything past whatever header
// its type carries. This is what the four type-directed questions a value
// answers for itself are all answered over, and they have to be answered over
// the same run: a hash that covered bytes an equality did not would put equal
// values in different buckets.
//
// Which bytes is what a header makes worth saying. A class or an abstract begins
// with the mark word, and its state, contained and upvalue fields say where the
// value lies rather than what it is — pushValueFrom rewrites all three on every
// copy, so two equal values routinely differ there. A tailed struct begins with
// a length its type id already states.
//
// The bytes and not the words: a value narrower than what it stands on has a
// tail its own type says nothing about, and covering that would make the answer
// depend on whatever the value was pushed over. Padding between members is not
// covered by that and is left as it lies, being written by whatever produced the
// instance.
typedef struct ZamValueBytes {
    const uint8_t *bytes;
    uint64_t length;
} ZamValueBytes;

static ZamValueBytes inlineValueBytes(const ZamImage *image, ZamTypeID type,
                                      const ZamWord *value) {
    uint64_t header = inlineValueHeaderBytes(image, type);
    return (ZamValueBytes){.bytes = (const uint8_t *)value + header,
                           .length = zam_type_bytes(image, type) - header};
}

// Whether the two inline values are of one type, which is what makes a run of
// bytes comparable with another: the length is the type's and so is what each
// byte in it means.
static bool sameInlineType(ZamTypeID leftType, ZamTypeID rightType) {
    return leftType.shortID == rightType.shortID &&
           leftType.tailLength == rightType.tailLength;
}

// FNV-1a over the bytes, which is what a value with no Hash of its own hashes
// to. Nothing outside a single run ever sees one, so which function it is
// matters only in that the same bytes give the same answer.
static ZamWord fnv1a(const uint8_t *bytes, uint64_t length) {
   ZamWord hash = 14695981039346656037ULL;
    for (uint64_t at = 0; at < length; at++) {
        hash ^= bytes[at];
        hash *= 1099511628211ULL;
    }
    return hash;
}

// An allocation of the given length with its bytes still to be written, which is
// the shape a string is held in whether it was built here or interned into the
// static region: the mark word and then the length and the bytes, so the header
// is added back onto what zam_string_bytes counts.
//
// The allocator writes that word — its kind, its state, and the upvalue, which
// on an allocation is the sweep list link and is what makes the block findable
// without a side table. The two words a type id would go in are none of a
// string's business, a string being the one kind whose kind settles its whole
// shape.
//
// Nothing is pushed, the two callers spending their operands at different points
// against the one they leave the result on.
static ZamString *newString(ZamThread *thread, uint64_t length) {
   ZamString *string = zam_string_of(
        zam_vm_alloc(thread->vm, thread, ZAMOBJECT_STRING, 0, 0,
                     sizeof(ZamMarkWord) + zam_string_bytes(length)));
    string->length = length;
    return string;
}

// The one word a value is, read out of wherever it lies.
//
// The read is the type's own width and not a whole word, because a value
// narrower than a word lies in only its own bytes: a bool member is one byte
// with the next member behind it, and eight bytes of it would be seven bytes of
// something else. On the stack the rest of the word is clear — pushValueFrom
// zeroes a value's words before writing them — so the narrow read is the same
// number there, which is what lets one reader serve both.
//
// Only for the types that stand in one word, which is every reference and every
// primitive but the type id. Settling that is the caller's, the switch that
// chose this over typeIDAt being where the width is known.
static ZamWord wordOfValue(const ZamThread *thread, const ZamImage *image,
                          ZamTypeID type, ZamWord location) {
   ZamWord word = 0;
    memcpy(&word, zam_thread_location_address(thread, location),
           zam_type_bytes(image, type));
    return word;
}

static ZamTypeID typeIDAt(const ZamThread *thread, ZamWord location) {
   ZamWord words[ZAMTYPE_ID_WORDS];
    memcpy(words, zam_thread_location_address(thread, location), sizeof words);
    return zam_type_id_read(words);
}

// Whether two type ids name one type, which is what a value of the type
// primitive is compared by. All three fields and not the words they lie in: only
// these are written, and what the rest of the words hold is nothing the language
// gave a meaning to.
static bool sameTypeID(ZamTypeID left, ZamTypeID right) {
    return left.shortID == right.shortID &&
           left.arrayLength == right.arrayLength &&
           left.tailLength == right.tailLength;
}

// Whether the type is a function, which is the one reference whose equality is
// more than the word: see equalValues.
static bool isFunctionType(const ZamImage *image, ZamTypeID type) {
    return type.arrayLength == 0 &&
           zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID)) ==
               ZAMBUCKET_FUNCTION_TYPE;
}

// Where a closure's captures begin, the receiver's entries stepped over. A
// function closure writes a void first entry and a value method closure writes
// a second entry holding the address of the value in the first, which is the
// one place the three kinds differ in shape.
static const ZamWord *closureCaptures(const ZamImage *image,
                                      const ZamClosure *closure) {
    const ZamWord *entry = zam_closure_first(closure);
    entry = zam_closure_entry_next(
        entry, valueWordsOf(image, zam_closure_entry_type(entry)));
    if (zam_mark_kind_of((void *)closure) ==
        ZAMOBJECT_VALUE_METHOD_CLOSURE)
        entry = zam_closure_entry_next(
            entry, valueWordsOf(image, zam_closure_entry_type(entry)));
    return entry;
}

// Whether the two values lying at the two locations are equal, split out
// because how the question is settled is the type's to say.
//
// Locations and not addresses, which is what lets the walk descend. A value
// reached here may be compared by running its own Eq, running a body pushes, and
// the operand stack grows by realloc — so an address worked out before a descent
// would name where the value used to be. A member or an element is the location
// plus an offset under either form, the tag riding along untouched, so one sum
// serves a value on the stack and a value in an allocation alike.
//
// Member by member and never as a run of bytes, which three things turn on.
// A float is not its bits — IEEE has two zeros that are one number and a NaN
// that is equal to nothing, not even to a copy of itself. A member that answers
// through an Eq of its own has to reach it. And the padding between members
// belongs to whatever wrote the instance rather than to the value, so two
// instances with equal members and unequal padding are equal and a memcmp of
// them is not.
static bool equalValues(ZamThread *thread, ZamTypeID leftType,
                        ZamWord leftLocation, ZamTypeID rightType,
                        ZamWord rightLocation);

// Calling the compiled Eq of the instance the left pointer names on the one
// the right names. This is how a walk asks a value that answers for itself.
static bool dispatchEqual(ZamThread *thread, const ZamImage *image,
                          ZamTypeID leftType, ZamWord left, ZamWord right) {
    ZamWord answer = 0;
    if (!callCompiled(thread, instanceAt(thread, image, leftType, &left),
                      ZAMOBJECT_SLOT_EQ, right, &answer))
        zam_die(ZAMEXIT_PANIC, "asked a method of a null pointer\n");
    return answer != 0;
}

// Two closures, which are equal when they are the same body closed over equal
// values.
//
// The entry alone is not it. One lambda is written once and compiled once, so
// two closures made in two turns of a loop share an entry and closed over
// different values, and they are not the same function. Nor are the
// captures alone: two bodies may capture nothing and still be two functions.
//
// Each capture is compared under its own type, which the closure carries: an
// entry is a type id and then the value, so a walk of one is as typed as a walk
// of an instance's members. The two are walked in lockstep, their counts already
// agreeing, and a capture's location is an address rather than a frame location
// — the closure is an allocation, which the collector never moves.
static bool equalClosures(ZamThread *thread, const ZamImage *image,
                          ZamWord left, ZamWord right) {
    const ZamClosure *l = zam_closure_of((void *)(uintptr_t)left);
    const ZamClosure *r = zam_closure_of((void *)(uintptr_t)right);

    if (l->entry != r->entry || l->captureCount != r->captureCount)
        return false;

    const ZamWord *leftEntry = closureCaptures(image, l);
    const ZamWord *rightEntry = closureCaptures(image, r);
    for (uint64_t at = 0; at < l->captureCount; at++) {
       ZamTypeID leftCapture = zam_closure_entry_type(leftEntry);
       ZamTypeID rightCapture = zam_closure_entry_type(rightEntry);

        if (!equalValues(thread, leftCapture,
                         (ZamWord)(uintptr_t)zam_closure_entry_value(leftEntry),
                         rightCapture,
                         (ZamWord)(uintptr_t)zam_closure_entry_value(rightEntry)))
            return false;

        leftEntry = zam_closure_entry_next(leftEntry,
                                           valueWordsOf(image, leftCapture));
        rightEntry = zam_closure_entry_next(rightEntry,
                                            valueWordsOf(image, rightCapture));
    }

    return true;
}

static bool equalValues(ZamThread *thread, ZamTypeID leftType,
                        ZamWord leftLocation, ZamTypeID rightType,
                        ZamWord rightLocation) {
    const ZamImage *image = &thread->vm->image;

    // A run is its length and then its elements, the length asked first so that
    // two of unequal length are settled without a read.
    //
    // Nothing makes them unequal today: a comparison is type-checked between
    // types that are equal, and two array types are equal only at one size. So
    // this is to a run what sameInlineType is to a value — the guard that says
    // the walk below is comparing elements that mean the same thing — and it is
    // written as the comparison it is rather than as an assertion, against the
    // day an unsized run becomes comparable.
    if (leftType.arrayLength != 0 || rightType.arrayLength != 0) {
        if (leftType.arrayLength != rightType.arrayLength)
            return false;

       ZamTypeID leftElement = leftType;
       ZamTypeID rightElement = rightType;
        leftElement.arrayLength = 0;
        rightElement.arrayLength = 0;
        uint64_t leftStride = zam_type_bytes(image, leftElement);
        uint64_t rightStride = zam_type_bytes(image, rightElement);

        for (uint64_t at = 0; at < leftType.arrayLength; at++) {
            if (!equalValues(thread, leftElement, leftLocation + at * leftStride,
                             rightElement, rightLocation + at * rightStride))
                return false;
        }

        return true;
    }

    // Two any values compare by what they hold rather than by which box
    // they were put in. Box identity is the one shortcut — the same
    // allocation holds one payload, so the walk is skipped — and a null
    // either side is handled before anything is read, a null any being the
    // zero word and so a question about the pointer alone.
    //
    // Past the shortcuts the stored id and payload pointer come out of each
    // box, and the pair is settled the same way every other pair is: same
    // id walks the payload under that id, two reference ids let dispatchEqual
    // call .Eq across the pair, and everything else is unequal. Recursing
    // into equalValues is what does all three, since those are the branches
    // it already answers for a pair that reaches it with the right ids.
    //
    // The payload box stores the body alone — the inline header the value
    // carried on the stack was stripped by boxing and is put back by
    // unboxing — so the location given to equalValues has to be far
    // enough behind the stored body that its header+offset walk lands on
    // the first member. typedHeader - inlineHeader is that distance: the
    // box's own object header stands in for the stripped inline header.
    if (ZAMTYPE_DECL(leftType.shortID) == ZAMPRIMITIVE_ANY &&
        ZAMTYPE_DECL(rightType.shortID) == ZAMPRIMITIVE_ANY) {
       ZamWord leftBox =
            wordOfValue(thread, image, leftType, leftLocation);
       ZamWord rightBox =
            wordOfValue(thread, image, rightType, rightLocation);
        if (leftBox == rightBox)
            return true;
        if (leftBox == 0 || rightBox == 0) {
            return false;
        }

        uint64_t typedHeader =
            zam_object_header_bytes(ZAMOBJECT_INSTANCE);
        const ZamWord *leftBody =
            (const ZamWord *)((const uint8_t *)(uintptr_t)leftBox +
                              typedHeader);
        const ZamWord *rightBody =
            (const ZamWord *)((const uint8_t *)(uintptr_t)rightBox +
                              typedHeader);
       ZamTypeID leftStored = zam_type_id_read(leftBody);
       ZamTypeID rightStored = zam_type_id_read(rightBody);

        bool bothRef = answersThroughObject(image, leftStored) &&
                       answersThroughObject(image, rightStored);
        if (!sameTypeID(leftStored, rightStored) && !bothRef)
            return false;

        uint8_t *leftPayload = (uint8_t *)(uintptr_t)leftBody[3];
        uint8_t *rightPayload = (uint8_t *)(uintptr_t)rightBody[3];
       ZamWord leftPayloadLoc = (ZamWord)(uintptr_t)(
            leftPayload + typedHeader -
            boxedHeaderBytes(image, leftStored));
       ZamWord rightPayloadLoc = (ZamWord)(uintptr_t)(
            rightPayload + typedHeader -
            boxedHeaderBytes(image, rightStored));
        return equalValues(thread, leftStored, leftPayloadLoc, rightStored,
                           rightPayloadLoc);
    }

    // One any and one null is a question about the box pointer alone: the
    // null any is the zero word, and a non-null any has a box it names. The
    // semantic layer refuses every other mixed pairing.
    if (ZAMTYPE_DECL(leftType.shortID) == ZAMPRIMITIVE_ANY ||
        ZAMTYPE_DECL(rightType.shortID) == ZAMPRIMITIVE_ANY) {
        return wordOfValue(thread, image, leftType, leftLocation) ==
                 wordOfValue(thread, image, rightType, rightLocation);
    }

   ZamPrimitive kind = primitiveOf(image, leftType);
    if (kind == primitiveOf(image, rightType)) {
        switch (kind) {
        case ZAMPRIMITIVE_BOOL:
        case ZAMPRIMITIVE_CHAR:
        case ZAMPRIMITIVE_BYTE:
        case ZAMPRIMITIVE_INT:
        case ZAMPRIMITIVE_UINT:
        case ZAMPRIMITIVE_POSIT:
            return wordOfValue(thread, image, leftType, leftLocation) ==
                     wordOfValue(thread, image, rightType, rightLocation);

        // Asked as the number and not as the bits, which is the whole reason a
        // float could not be left to a run of bytes: 0.0 and -0.0 are two
        // spellings of one number, and a NaN is no number and so equal to
        // nothing — not even to a copy of itself, which byte comparison would
        // call equal.
        case ZAMPRIMITIVE_FLOAT:
            return floatOf(wordOfValue(thread, image, leftType, leftLocation)) ==
                floatOf(wordOfValue(thread, image, rightType, rightLocation));

        // A string lies as the address of its allocation, and two of them are
        // the same string when their bytes are, however each was built. The
        // addresses are asked first all the same: a literal is interned once and
        // named as often as it is written, so the comparison a program makes
        // most often is settled without a read.
        //
        // The length is asked before the bytes rather than as part of them. Two
        // strings of unequal length are already unequal, and nothing about an
        // allocation says how far past the shorter one it is safe to read.
        case ZAMPRIMITIVE_STRING: {
           ZamWord left = wordOfValue(thread, image, leftType, leftLocation);
           ZamWord right = wordOfValue(thread, image, rightType, rightLocation);
            if (left == right)
                return true;
            if (left == 0 || right == 0) {
                return false;
            }

            const ZamString *l = zam_string_of((void *)(uintptr_t)left);
            const ZamString *r = zam_string_of((void *)(uintptr_t)right);
            return l->length == r->length &&
                     memcmp(l->bytes, r->bytes, l->length) == 0;
        }

        case ZAMPRIMITIVE_TYPE:
            return sameTypeID(typeIDAt(thread, leftLocation),
                                typeIDAt(thread, rightLocation));

        default:
            break;
        }
    }

    // A class, abstract or interface pointer answers through an Eq of its own,
    // which is a call and not a comparison of the words. Three things are
    // settled before the call is made, being three a call could not answer.
    //
    // Two pointers at one instance are equal whatever that instance's Eq would
    // say. A null is not an instance and has nothing to answer with, so a pair
    // with one null in it is unequal — and a pair of two is the identity above,
    // which is what makes the whole of it the rule a reader would write: equal
    // when both are null, or when neither is and the left one's Eq says so.
    //
    // And a pair this descent has already been through is where the graph led
    // back to itself. Answering it equal is what reflect.DeepEqual does and the
    // same approximation — see zam_thread_visit — and it is what makes a cyclic
    // value terminate.
    if (answersThroughObject(image, leftType) &&
        answersThroughObject(image, rightType)) {
       ZamWord left = wordOfValue(thread, image, leftType, leftLocation);
       ZamWord right = wordOfValue(thread, image, rightType, rightLocation);

        if (left == right)
            return true;
        if (left == 0 || right == 0) {
            return false;
        }
        if (zam_thread_visit(thread, left, right, leftType.shortID))
            return true;

        return dispatchEqual(thread, image, leftType, left, right);
    }

    if (isFunctionType(image, leftType) && isFunctionType(image, rightType)) {
       ZamWord left = wordOfValue(thread, image, leftType, leftLocation);
       ZamWord right = wordOfValue(thread, image, rightType, rightLocation);

        // The same two a dispatching pair settles, for the same reasons: one
        // closure is the same function as itself, and a null closed over
        // nothing and has no body to be the body of. No pair is recorded — a
        // capture is held by value, so a closure cannot be reached from inside
        // itself and a walk of one cannot come back.
        if (left == right)
            return true;
        if (left == 0 || right == 0) {
            return false;
        }

        return equalClosures(thread, image, left, right);
    }

    // A reference that is neither of those is the word it holds and nothing
    // more, so the word is the whole of what equality can mean for it: a pointer
    // to a struct or an enum, a void*, a thread. A string is a reference too and
    // is answered above, by its bytes rather than by where they lie.
    if (isReferenceType(image, leftType) && isReferenceType(image, rightType)) {
        return wordOfValue(thread, image, leftType, leftLocation) ==
                 wordOfValue(thread, image, rightType, rightLocation);
    }

    // Everything left lies inline and is its members: a struct, an enum, and a
    // class or abstract held by value, which has no Eq to answer through
    // because Object's takes an Object* and a value has no address to be one.
    //
    // The offsets the compiler records are net of whatever header the value
    // carries, so the header is added back first. The type's own width and not
    // the value's, which is what memberHeaderBytes draws the line at and holds
    // here for the reason valueHeaderBytes gives: a value laid out inline is
    // never polymorphic, polymorphism here being reached through a pointer.
    //
    // Two values of one declaration at different tail lengths are unequal
    // without reading a byte. The compiler refuses the pair, so this is only
    // ever reached when something upstream of here let one slip past, but
    // answering it rather than panicking is both cheap and the obvious truth: a
    // run of four is not a run of five, however the elements line up at the
    // front.
    if (isInlineValue(image, leftType) && isInlineValue(image, rightType) &&
        leftType.shortID == rightType.shortID) {
        if (leftType.tailLength != rightType.tailLength)
            return false;

        // An enum carries a one-word discriminant at the head of every
        // value, written per enum constant by emitEnumValue. Two enum
        // values of the same enum type are equal iff their discriminants
        // match — user members are tied to the discriminant, so walking
        // them would answer the same question with more work.
        if (zam_image_bucket_of(image, ZAMTYPE_DECL(leftType.shortID)) ==
            ZAMBUCKET_ENUM) {
            uint64_t leftTag, rightTag;
            memcpy(&leftTag, zam_thread_location_address(thread, leftLocation),
                   sizeof leftTag);
            memcpy(&rightTag, zam_thread_location_address(thread, rightLocation),
                   sizeof rightTag);
            return leftTag == rightTag;
        }

        uint64_t leftHeader = inlineValueHeaderBytes(image, leftType);
        uint64_t rightHeader = inlineValueHeaderBytes(image, rightType);

        uint64_t count = zam_type_member_count(image, leftType.shortID);
        for (uint64_t index = 0; index < count; index++) {
            ZamMemberDefinition member =
                zam_type_member(image, leftType.shortID, index);
           ZamTypeID memberType = zam_member_type(&member);

            // The tail is a member like the rest and is the last of them, but
            // it is the one whose record carries no length: `var char[~] data`
            // is one declaration standing for every length it is instantiated
            // at, so how many elements lie there is the container's type id to
            // say. Left as the record has it the member would be walked as a
            // single element and the rest of the run would go unasked.
            //
            // The two agree on that length, the compiler refusing a comparison
            // of two tailed types that differ in it, which is why one of them
            // can be read for both.
            if (leftType.tailLength != 0 && index + 1 == count)
                memberType.arrayLength = leftType.tailLength;

            if (!equalValues(thread, memberType, leftLocation + leftHeader + member.offset,
                             memberType, rightLocation + rightHeader + member.offset))
                return false;
        }

        return true;
    }

    zam_die(ZAMEXIT_PANIC,
            "unreachable, logging: equality of left %u/%llu/%llu "
            "and right %u/%llu/%llu\n",
            leftType.shortID, (unsigned long long)leftType.arrayLength,
            (unsigned long long)leftType.tailLength, rightType.shortID,
            (unsigned long long)rightType.arrayLength,
            (unsigned long long)rightType.tailLength);
}

// How the two values standing beneath the top of the stack are ordered, split
// out for the reason equality is: what an order comes to is the type's to say.
// valuesOrderAt is how the value of leftType at left orders against the value
// of rightType at right, for every type but one that answers through an object
// or is a tuple.
static ZamInt valuesOrderAt(const ZamImage *image, ZamTypeID leftType,
                            const ZamWord *left, ZamTypeID rightType,
                            const ZamWord *right) {
    uint64_t rightWords = valueWordsOf(image, rightType);
    uint64_t leftWords = valueWordsOf(image, leftType);

   ZamPrimitive kind = primitiveOf(image, leftType);
    if (kind == primitiveOf(image, rightType)) {
        switch (kind) {
        // Each order is asked as both strict comparisons rather than as a
        // subtraction, which is what keeps a pair far apart from wrapping into
        // the wrong answer.
        case ZAMPRIMITIVE_INT:
        case ZAMPRIMITIVE_POSIT: {
           ZamInt l = (ZamInt)*left, r = (ZamInt)*right;
            return (l > r) - (l < r);
        }

        // A bool, a char, a byte and a uint are their own bits widened to a
        // word by whoever pushed them, and none has a sign to read, so the
        // word is the value.
        case ZAMPRIMITIVE_BOOL:
        case ZAMPRIMITIVE_CHAR:
        case ZAMPRIMITIVE_BYTE:
        case ZAMPRIMITIVE_UINT:
            return (*left > *right) - (*left < *right);

        // Asking both sides is also what makes a NaN unordered: neither
        // comparison holds, so it is neither the lesser nor the greater and the
        // answer is the zero that says so.
        case ZAMPRIMITIVE_FLOAT: {
           ZamFloat l = floatOf(*left), r = floatOf(*right);
            return (l > r) - (l < r);
        }

        // A type id is three fields and is ordered field by field, the short
        // id first because that is what the whole shape begins with. It is the
        // layout's order and not one meaningful to the program, but it is a
        // total order and it agrees with sameTypeID on the pair it calls
        // equal, which is what the two owe each other.
        case ZAMPRIMITIVE_TYPE: {
           ZamTypeID l = zam_type_id_read(left);
           ZamTypeID r = zam_type_id_read(right);
            if (l.shortID != r.shortID)
                return l.shortID < r.shortID ? -1 : 1;
            if (l.arrayLength != r.arrayLength)
                return l.arrayLength < r.arrayLength ? -1 : 1;
            if (l.tailLength != r.tailLength)
                return l.tailLength < r.tailLength ? -1 : 1;
            return 0;
        }

        // A string is ordered by its bytes, the address being asked first as it
        // is in equality: a string is its own beginning and stands neither above
        // nor below itself.
        //
        // Only as far as both run can the bytes say anything, so the comparison
        // is made over the shorter length and the lengths settle what it leaves
        // equal — which puts a string below the one it is the beginning of.
        // memcmp is asked only which side of zero it fell on, its magnitude
        // being no part of what it promises.
        case ZAMPRIMITIVE_STRING: {
            if (*left == *right)
                return 0;
            if (*left == 0 || *right == 0)
                return *left == 0 ? -1 : 1;
            const ZamString *l = zam_string_of((void *)(uintptr_t)*left);
            const ZamString *r = zam_string_of((void *)(uintptr_t)*right);
            uint64_t shared = l->length < r->length ? l->length : r->length;
            int order = memcmp(l->bytes, r->bytes, shared);
            if (order != 0)
                return order < 0 ? -1 : 1;
            return (l->length > r->length) - (l->length < r->length);
        }

        default:
            break;
        }
    }

    // A reference that does not answer through a Cmp of its own is its address,
    // and an order of two addresses is an order of the words. Unsigned, an
    // address having no sign to read, and asked as both strict comparisons for
    // the reason every order here is.
    if (isReferenceType(image, leftType) && isReferenceType(image, rightType))
        return (*left > *right) - (*left < *right);

    // A value laid out inline is ordered by the same bytes its equality is
    // answered by. What that makes the more significant member is whichever one
    // lies nearer the front, which is the layout's order and not one the
    // declaration chose — but it is a total order, and it agrees with
    // valuesEqual on the pair it calls equal, which is what the two of them owe
    // each other.
    //
    // memcmp is asked only which side of zero it fell on, its magnitude being no
    // part of what it promises.
    if (isInlineValue(image, leftType) && isInlineValue(image, rightType) &&
        sameInlineType(leftType, rightType)) {
       ZamValueBytes l = inlineValueBytes(image, leftType, left);
       ZamValueBytes r = inlineValueBytes(image, rightType, right);
        int order = memcmp(l.bytes, r.bytes, l.length);
        return (order > 0) - (order < 0);
    }

    // Anything else falls to here because no case above knew what to say of it,
    // which is what makes it a value of a non-comparable type: a run, and a pair
    // of types the semantic pass does not refuse but no branch above picked up.
    //
    // The order is read off a hash of each. Equal values hash alike and so
    // order as zero, which is the agreement with equality that would be lost by
    // asking where the values stood — a stack slot's address is the push's and
    // not the value's, and a copy and its original would order unequal with it.
    // Two values that hash alike without being equal order as zero they are
    // not, which is the price of the hash being a single word; collisions are
    // rare and the order is still a total one, which is as much as a pair the
    // type ordered nothing by was going to give.
    //
    // FNV-1a over the operand words, which is the same hash a hash-of-bytes
    // elsewhere in this file uses and is cheap. The whole value and not past
    // whatever header it begins with: an inline value is not reached here, so
    // what is being hashed is a run of elements and the elements are the bytes.
   ZamWord leftHash = fnv1a((const uint8_t *)left, leftWords * ZAMWORD_BYTES);
   ZamWord rightHash = fnv1a((const uint8_t *)right, rightWords * ZAMWORD_BYTES);
    return (leftHash > rightHash) - (leftHash < rightHash);
}

// Whether an instance of the type carries the header it is marked through. A
// class and an abstract descend from std.Object and are collected, so an
// instance of one begins with the mark word wherever it is copied to; a struct
// and an enum are the coder's own memory and begin with their first member.
static bool isMarkableInstance(const ZamImage *image, ZamTypeID type) {
    switch (zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID))) {
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_GENERIC_CLASS:
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_GENERIC_ABSTRACT:
        return true;
    default:
        return false;
    }
}

// Whether what lies at the address is an allocation the collector owns. This is
// the question a delete asks, and it is not the same one isMarkableInstance
// answers: every kind zam_vm_alloc hands out is threaded onto the sweep list, so
// a string and a closure and a thread are as much the collector's as an instance
// is, however little any of them looks like one.
//
// A struct, an enum, a plain primitive and a pointer are what is left. None of
// them was ever handed out by the allocator and so none is linked, which makes
// the memory the program's own to release.
static bool isCollectedAllocation(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_GENERIC_CLASS:
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_GENERIC_ABSTRACT:
    // An instance reached under an interface is still an instance, the interface
    // naming only how it was written down and never how it was allocated.
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
    case ZAMBUCKET_FUNCTION_TYPE:
    case ZAMBUCKET_THREAD_TYPE:
        return true;
    // A string is the one primitive with an allocation behind it.
    case ZAMBUCKET_PRIMITIVE:
        return (ZamPrimitive)declID == ZAMPRIMITIVE_STRING;
    default:
        return false;
    }
}

// Whether the one word a reference is names an instance carrying a header.
//
// A pointer answers through its pointee. An interface answers by being one: it
// is laid out nowhere, so what is reached through one is always the class
// implementing it, and a class is an instance like any other however it was
// written down.
//
// That second case is why this is not isMarkableInstance asked of the pointee.
// Being a markable instance is a claim about a value lying somewhere — it is
// what says a header is among the words and what an allocation lays one out
// by — and an interface value lies nowhere at all. The two questions
// only look alike because a pointer answers both the same way.
//
// Both forms an interface arrives in go through the one switch, pointeeOf
// leaving anything that is not a pointer alone. An interface is written
// Greeter* in the source, so a variable of one is named by a pointer to it,
// while the bare id is what a method body reads off its receiver — and the word
// standing here is the same address under either.
static bool referenceNamesInstance(const ZamImage *image, ZamTypeID type) {
   ZamTypeID named = pointeeOf(image, type);
    switch (zam_image_bucket_of(image, ZAMTYPE_DECL(named.shortID))) {
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
        return true;
    default:
        return isMarkableInstance(image, named);
    }
}

// Whether the type answers equality, order, hashing and its string form through
// a method of its own rather than by having its words read. Exactly the types that descend
// from std.Object and are reached through an address do: a class or abstract
// pointer, and an interface.
//
// A class held by value is deliberately not one of these. Object's methods take
// an Object* and a value has no address to be one: the semantic pass refuses
// v1.Eq(v2) for that reason, so the runtime must not synthesize the call it
// rejects. What a value answers by instead is its bytes, in valuesEqual.
static bool answersThroughObject(const ZamImage *image, ZamTypeID type) {
    return isReferenceType(image, type) && referenceNamesInstance(image, type);
}

// instanceAt is the instance the value of type at value is or points at, or
// NULL for a null pointer or a value that is no instance.
static void *instanceAt(const ZamThread *thread, const ZamImage *image,
                        ZamTypeID type, const ZamWord *value) {
    if (answersThroughObject(image, type))
        return *value == 0 ? NULL : zam_thread_location_address(thread, *value);
    if (isMarkableInstance(image, type) && type.arrayLength == 0)
        return (void *)value;
    return NULL;
}

// callCompiled runs the slot'th method of instance compiled, with argument
// after the receiver for a method that takes one, and reports whether the
// program has it compiled. What the method answers, a word, is left in answer.
static bool callCompiled(ZamThread *thread, void *instance, uint64_t slot,
                         ZamWord argument, ZamWord *answer) {
    if (instance == NULL)
        return false;
    return callCompiledAs(thread, instance, zam_instance_type(instance).shortID, slot,
                          argument, answer);
}

// callCompiledAs is callCompiled for a receiver of the type shortID, which an
// enum or struct, having no header, cannot say of itself.
static bool callCompiledAs(ZamThread *thread, void *receiver, ZamTypeShort shortID,
                           uint64_t slot, ZamWord argument, ZamWord *answer) {
    if (receiver == NULL || thread->vm->compiledClasses == NULL)
        return false;
    ZamEntry entry = zam_vm_compiled_method(thread->vm, shortID, slot);
    if (entry == NULL)
        return false;
    ZamWord args[2] = {(ZamWord)(uintptr_t)receiver, argument};
    ZamWord result = 0;
    // A Mark or Delete runs for the collector or the allocator, which a panic
    // may not unwind; an Eq, Cmp, Hash or Str leaves nothing a landing does
    // not put back.
    bool unwinds = slot != ZAMOBJECT_SLOT_MARK && slot != ZAMOBJECT_SLOT_DELETE && !zam_gc_collecting(thread);
    ZamHandler *handlers = unwinds ? thread->handlers : zamrt_handlers_hide(thread);
    entry(thread, NULL, args, &result);
    zamrt_handlers_show(thread, handlers);
    if (answer != NULL)
        *answer = result;
    return true;
}

// The set of types a declaration stands below — itself, each declaration up its
// base chain, and each interface it implements — or NULL where the type is one
// nothing can descend from and so has no set to read.
//
// The generic forms are asked of the instantiation rather than the declaration.
// A method implementing an interface only once T is bound makes C[int] stand
// below what C[string] does not, so the set belongs to the concrete and the low
// half of the short id is what says which concrete is meant.
static const ZamPerfectHash *supertypesOf(const ZamImage *image,
                                         ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    uint16_t genericID = ZAMTYPE_GENERIC(type.shortID);

    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_ABSTRACT:
        return zam_abstract_supertypes(zam_abstract_definition(image, declID));
    case ZAMBUCKET_CLASS:
        return zam_class_supertypes(zam_class_definition(image, declID));

    // An interface is laid out nowhere and answers for nothing but what stands
    // above it, so its whole definition is the set.
    case ZAMBUCKET_INTERFACE:
        return zam_interface_definition(image, declID);

    case ZAMBUCKET_GENERIC_ABSTRACT:
        return zam_generic_entry_supertypes(zam_generic_abstract_concrete(
            zam_generic_abstract_definition(image, declID), genericID));
    case ZAMBUCKET_GENERIC_CLASS:
        return zam_generic_entry_supertypes(zam_generic_class_concrete(
            zam_generic_class_definition(image, declID), genericID));
    case ZAMBUCKET_GENERIC_INTERFACE:
        return zam_generic_interface_concrete(
            zam_generic_interface_definition(image, declID), genericID);

    default:
        return NULL;
    }
}

// The table saying which of a type's own methods an interface method names on
// it, or NULL where the type carries none.
//
// Only a class does. An interface method is reached on the value that
// implements it, and nothing is ever an abstract — an abstract is descended
// from and never allocated — so an abstract is never the far end of a dispatch
// and its record carries no table to be the far end with.
//
// The generic form is asked of the instantiation for the reason supertypesOf
// is: a method implementing an interface only once T is bound makes C[int]
// implement what C[string] does not, so the table belongs to the concrete.
static const ZamPerfectHash *dispatchOf(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    uint16_t genericID = ZAMTYPE_GENERIC(type.shortID);

    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_CLASS:
        return zam_class_dispatch(zam_class_definition(image, declID));
    case ZAMBUCKET_GENERIC_CLASS:
        return zam_generic_entry_dispatch(zam_generic_class_concrete(
            zam_generic_class_definition(image, declID), genericID));
    default:
        return NULL;
    }
}

static bool typeIsSubtypeOf(const ZamImage *image, ZamTypeID child,
                           ZamTypeID parent) {
    if (child.shortID == parent.shortID &&
        child.arrayLength == parent.arrayLength &&
        child.tailLength == parent.tailLength)
        return true;

    // A run descends nowhere. It is copied and passed whole, so a run of one
    // type is a different width from a run of another and could not be read as
    // it however the elements are related — which leaves the ids being the same
    // as the only way one of them is the other, and that was just asked.
    if (child.arrayLength != 0 || parent.arrayLength != 0)
        return false;

    // A tail length is a width in the same way and the same follows: the type it
    // hangs off is laid out with it, so two lengths are two layouts.
    if (child.tailLength != parent.tailLength)
        return false;

   ZamTypeBucket childBucket =
        zam_image_bucket_of(image, ZAMTYPE_DECL(child.shortID));
   ZamTypeBucket parentBucket =
        zam_image_bucket_of(image, ZAMTYPE_DECL(parent.shortID));

    // A pointer is a declared type of its own, and what stands above one is
    // another pointer and nothing else: a Child* may be read as a Parent* and
    // is no kind of Parent, the pointer being one word wherever the value is
    // however many. So both sides are asked and one pointer against one plain
    // type is a no before either record is opened.
    if (childBucket == ZAMBUCKET_POINTER || parentBucket == ZAMBUCKET_POINTER) {
        if (childBucket != ZAMBUCKET_POINTER || parentBucket != ZAMBUCKET_POINTER)
            return false;

       ZamTypeID childPointee = zam_pointer_pointee(
            zam_pointer_definition(image, ZAMTYPE_DECL(child.shortID)));
       ZamTypeID parentPointee = zam_pointer_pointee(
            zam_pointer_definition(image, ZAMTYPE_DECL(parent.shortID)));

        // void* stands above every pointer. It names no layout, so there is
        // nothing a pointee could fail to be — which makes it the one pointer
        // whose answer does not come from its pointee's. Read the other way it
        // says nothing: a void* is no kind of Child*.
        if (parentPointee.shortID == ZAMTYPE_SHORT(ZAMPRIMITIVE_VOID, 0) &&
            parentPointee.arrayLength == 0 && parentPointee.tailLength == 0)
            return true;

        // Buf~N* is a Buf~*. A tail length rides with the type id rather than
        // sitting in the layout, so a sized tail is laid out exactly as the
        // unsized one it was written from and the pointer is one word either
        // way. That is why the rule lives here rather than in the tail-length
        // guard above: as values the two are two widths, and only through a
        // pointer does the difference stop mattering. Not the other way about —
        // an unsized tail is the one with no length to give.
        if (childPointee.shortID == parentPointee.shortID &&
            childPointee.arrayLength == parentPointee.arrayLength &&
            childPointee.tailLength != 0 && parentPointee.tailLength == 0)
            return true;

        return typeIsSubtypeOf(image, childPointee, parentPointee);
    }

    // A signature is not a declaration anything descends from either, so a
    // function type is the other kind read rather than looked up. Both sides
    // must be one for the same reason: a function is nothing else's subtype and
    // nothing else is a function's.
    if (childBucket == ZAMBUCKET_FUNCTION_TYPE ||
        parentBucket == ZAMBUCKET_FUNCTION_TYPE) {
        if (childBucket != ZAMBUCKET_FUNCTION_TYPE ||
            parentBucket != ZAMBUCKET_FUNCTION_TYPE)
            return false;

        const ZamTypeList *childSignature =
            zam_function_type_signature(image, ZAMTYPE_DECL(child.shortID));
        const ZamTypeList *parentSignature =
            zam_function_type_signature(image, ZAMTYPE_DECL(parent.shortID));
        if (childSignature->count != parentSignature->count)
            return false;

        // The return leads the list and the parameters follow it, which is the
        // order functionTypeList writes. So index zero is the one position
        // read in the direction it is written and every other is read
        // backwards: what a caller does with a function is hand it arguments
        // and take a result, so a stand-in may accept more than was promised
        // and must promise no less than it returns.
        if (!typeIsSubtypeOf(image, zam_type_list_at(childSignature, 0),
                             zam_type_list_at(parentSignature, 0)))
            return false;
        for (uint64_t index = 1; index < childSignature->count; index++)
            if (!typeIsSubtypeOf(image, zam_type_list_at(parentSignature, index),
                                 zam_type_list_at(childSignature, index)))
                return false;
        return true;
    }

    // What is left descends or does not. The set holds every key as its own
    // value, so a parent that was never built into it lands in some other key's
    // slot and is caught by reading the slot back — which is what makes a miss
    // a miss and is why the lookup below can be trusted.
    const ZamPerfectHash *supertypes = supertypesOf(image, child);
    return supertypes != NULL &&
           zam_supertype_set_contains(supertypes, parent.shortID);
}

bool zam_type_is_subtype(const ZamImage *image, ZamTypeID child, ZamTypeID parent) {
    return typeIsSubtypeOf(image, child, parent);
}

// Whether an instance of the type keeps a tail length in the one word ahead of
// its members, which is the only header a struct ever has.
//
// Only a struct is asked. A class is asked its own mark word instead, because a
// class reached under a base's id may be a tailed child of an untailed base and
// the id in hand would say the wrong thing. A struct has no base at all, so the
// id in hand is the whole truth about it and its definition can be read
// directly.
static bool isTailedStruct(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
        break;
    default:
        return false;
    }

    // A generic struct is written as a plain struct definition, one per
    // concrete, so it is read as one — the same reading elementBytes gives it.
    const ZamStructDefinition *d =
        (const ZamStructDefinition *)zam_image_type_definition(image, declID);
    return d->instanceTailByteSize != 0;
}

// The header a fresh markable value is given, the elements behind it, and the
// members any of them holds. These are three halves of one statement about a
// value that lies inside something else — contained, and this far back — and
// they call each other in a ring because a member of a class may be a class, an
// array of one, or an array of classes with arrays of their own.
//
// The root's header is never written here, and that is the whole division of
// labour: the root is the one value whose answer depends on where it came from.
// zam_vm_alloc writes an allocation's, which is not contained and whose upvalue
// is the sweep-list link; a value laid out on a stack has one written that is
// contained and inside nothing. Everything below the root has the same answer
// wherever the root came from, which is why it is worked out from the definition
// alone.
static void stampArrayTail(const ZamImage *image, uint8_t *first,
                          ZamTypeID element, uint64_t elementCount);

// stampContained writes one interior run's headers. The distance is the caller's
// to work out, because only the caller knows what the run lies in — a member
// knows its offset within its container and an array element its stride from the
// front, and neither is recoverable from the value itself.
//
// The count is the front element's alone. A run laid inline is answered for by
// the value at its front, so that is where the number goes and the elements
// behind it are stamped as one apiece by stampArrayTail.
static void stampContained(const ZamImage *image, uint8_t *value,
                          ZamTypeID type, uint64_t backDistance,
                           uint64_t elementCount) {
    // A value laid out inline can only be tailed by a length its own type states,
    // an unsized tail having no width to lay anything after it, so the id is what
    // says which of the two kinds this is.
    bool tailed = type.tailLength != 0;
    *zam_mark_word_of(value) = (ZamMarkWord){
        .state = ZAMMARK_STATE_WHITE,
        .kind = tailed ? ZAMOBJECT_TAILED_INSTANCE : ZAMOBJECT_INSTANCE,
        .contained = 1,
        .upvalue = backDistance,
    };
    *zam_object_header_of(value) = (ZamObjectHeader){
        .shortID = type.shortID,
        .elementCount = elementCount,
    };
    if (tailed)
        ((ZamWord *)value)[2] = type.tailLength;

    stampArrayTail(image, value, type, elementCount);
}

static void stampInlineMembers(const ZamImage *image, uint8_t *value,
                              ZamTypeID type) {
    // Read off the value rather than worked out from the id, for the reason
    // memberHeaderBytes does the same: the member offsets are net of whatever
    // header the value actually turned out to carry.
    uint64_t headerBytes =
        zam_object_header_bytes(zam_mark_kind_of(value));

    uint64_t count = zam_type_member_count(image, type.shortID);
    for (uint64_t index = 0; index < count; index++) {
        ZamMemberDefinition member =
            zam_type_member(image, type.shortID, index);
       ZamTypeID memberType = zam_member_type(&member);
        // The length the record carries is how many elements lie at the member,
        // and it is set aside rather than dropped: the type below names one of
        // them, which is what a header is written from, and the count is what
        // the front element then answers for the run with.
        uint64_t elementCount =
            memberType.arrayLength == 0 ? 1 : memberType.arrayLength;
        memberType.arrayLength = 0;

        // Only a markable value has a header to write. A struct and an enum
        // carry none, a primitive is its bytes, and everything held as a
        // reference is one word naming something with a header of its own.
        if (!isMarkableInstance(image, memberType))
            continue;

        // The container's mark word is at the container's own start, so how far
        // back it lies from the member is exactly the member's offset within it.
        uint64_t offset = headerBytes + member.offset;
        stampContained(image, value + offset, memberType, offset, elementCount);
    }
}

// stampArrayTail is every element of a run but the one at the front.
//
// The front element is the array: it is what a pointer to the run names, what
// the allocator handed out where the run is an allocation, and what carries the
// count of how many followed. So the elements behind it are contained by it,
// each one its own stride further back, and reaching the length from any of them
// is a step to the front. That holds whether the run is an allocation, a fresh
// value on the stack, or a member laid inline — an element's distance is to the
// front of its own run and never to whatever the run lies in, which is what
// makes it the same number wherever the run happens to be.
//
// The front element's own members still need stamping, which is what it shares
// with a single value and why that is done here rather than left to the caller.
static void stampArrayTail(const ZamImage *image, uint8_t *first,
                          ZamTypeID element, uint64_t elementCount) {
    stampInlineMembers(image, first, element);

    uint64_t stride = zam_type_bytes(image, element);
    for (uint64_t index = 1; index < elementCount; index++)
        stampContained(image, first + index * stride, element, index * stride,
                       1);
}

// The same width worked out from the type alone, which is what a copy of a whole
// value has to step over at both ends.
//
// The type and not the value, which is the difference from memberHeaderBytes and
// is deliberate. A copy has two headers in play and they have to agree, or the
// members land at one offset and are read at another; and one of the two ends is
// storage a copy is about to overwrite, so asking it what it is means trusting a
// header the copy may be the first thing to write. A value laid out inline is
// never polymorphic — polymorphism here is reached through a pointer — so for
// every location the compiler lays down the type is the whole truth.
static uint64_t valueHeaderBytes(const ZamImage *image, ZamTypeID type) {
    if (!isMarkableInstance(image, type))
        return 0;
    return zam_object_header_bytes(
        type.tailLength != 0 ? ZAMOBJECT_TAILED_INSTANCE : ZAMOBJECT_INSTANCE);
}

// How far into a value laid out inline its own bytes begin, which is what a
// comparison of two of them steps over.
//
// Every header there is, where valueHeaderBytes answers only for the markable
// ones. A copy wants the mark word's width because the word is part of what it
// is copying; a comparison wants past it because the word is not part of what
// the value is. A tailed struct carries no mark word and a length instead, and
// that is a header by the same reading — the length is the type id's to state,
// so two values compared under one type id already agree on it.
// boxedHeaderBytes is how much of the front of a value of type an any's box
// leaves behind: the header an inline value carries, but not an enum's
// discriminant, which is the value itself.
static uint64_t boxedHeaderBytes(const ZamImage *image, ZamTypeID type) {
    if (zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID)) == ZAMBUCKET_ENUM)
        return 0;
    return inlineValueHeaderBytes(image, type);
}

static uint64_t inlineValueHeaderBytes(const ZamImage *image, ZamTypeID type) {
    if (isMarkableInstance(image, type))
        return valueHeaderBytes(image, type);
    if (isTailedStruct(image, type))
        return ZAMTAILED_STRUCT_HEADER_BYTES;
    // An enum carries a one-word discriminant at the head of every value,
    // written by the compiler's emitEnumValue, and user members live past
    // it. Equality below reads this word directly rather than walking any
    // further.
    if (zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID)) == ZAMBUCKET_ENUM)
        return ZAMWORD_BYTES;
    return 0;
}

static void opCmp(ZamThread *thread);
static void opCastToString(ZamThread *thread);
static void opHash(ZamThread *thread);
static ZamInt valueCmp(ZamThread *thread, ZamTypeID leftType, ZamWord left,
                       ZamTypeID rightType, ZamWord right);
static ZamWord valueHash(ZamThread *thread, ZamTypeID type, ZamWord location);
static ZamString *valueStr(ZamThread *thread, ZamTypeID type, ZamWord location);

// Whether the type is a tuple, which answers equality, order, hashing and its
// string form element by element rather than by its bytes.
static bool isTupleType(const ZamImage *image, ZamTypeID type) {
    return type.arrayLength == 0 &&
           zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID)) ==
               ZAMBUCKET_TUPLE;
}

// Pushing the element of a tuple lying at the location, with its type id over
// it, which is the pair a type-directed operation is handed.
static ZamTypeID pushTupleElement(ZamThread *thread, const ZamImage *image,
                                  ZamTypeID tuple, ZamWord location,
                                  uint64_t index) {
    ZamMemberDefinition member = zam_type_member(image, tuple.shortID, index);
    ZamTypeID type = zam_member_type(&member);
    pushValueFrom(thread, image, type, location + member.offset);
    zam_thread_push_type(thread, type);
    return type;
}

// The word a type-directed operation left on top of the stack, spent.
static ZamWord popAnswer(ZamThread *thread) {
    ZamWord answer = zam_thread_peek(thread, 0);
    popWord(thread);
    return answer;
}

// How two tuples of one type are ordered: by their first elements that are not
// ordered alike, the way two strings are ordered by their first bytes that
// differ. Each pair of elements is ordered as it would be on its own.
static ZamInt tupleOrder(ZamThread *thread, const ZamImage *image,
                         ZamTypeID type, ZamWord left, ZamWord right) {
    uint64_t count = zam_type_member_count(image, type.shortID);
    for (uint64_t index = 0; index < count; index++) {
        pushTupleElement(thread, image, type, left, index);
        pushTupleElement(thread, image, type, right, index);
        opCmp(thread);
        ZamInt order = (ZamInt)popAnswer(thread);
        if (order != 0)
            return order;
    }
    return 0;
}

// A tuple's hash, folded out of the hash of each element in turn. Equal tuples
// have equal elements, and equal elements hash alike.
static ZamWord tupleHash(ZamThread *thread, const ZamImage *image,
                         ZamTypeID type, ZamWord location) {
    ZamWord hash = 14695981039346656037ULL;
    uint64_t count = zam_type_member_count(image, type.shortID);
    for (uint64_t index = 0; index < count; index++) {
        pushTupleElement(thread, image, type, location, index);
        opHash(thread);
        hash ^= popAnswer(thread);
        hash *= 1099511628211ULL;
    }
    return hash;
}

// A tuple written out as its elements are, between parentheses and apart by
// commas. Every element's string is left standing on the stack until the whole
// is built, which is what keeps each one reachable while the result is
// allocated. A null string reads as null.
static ZamString *tupleString(ZamThread *thread, const ZamImage *image,
                              ZamTypeID type, ZamWord location) {
    static const char separator[] = ", ";
    static const char null[] = "null";

    uint64_t count = zam_type_member_count(image, type.shortID);
    uint64_t length = 2 + (count - 1) * (sizeof separator - 1);
    for (uint64_t index = 0; index < count; index++) {
        pushTupleElement(thread, image, type, location, index);
        opCastToString(thread);
        ZamWord element = zam_thread_peek(thread, 0);
        length += element == 0
                      ? sizeof null - 1
                      : zam_string_of((void *)(uintptr_t)element)->length;
    }

    ZamString *string = newString(thread, length);
    uint8_t *at = (uint8_t *)string->bytes;
    *at++ = '(';
    for (uint64_t index = 0; index < count; index++) {
        if (index > 0) {
            memcpy(at, separator, sizeof separator - 1);
            at += sizeof separator - 1;
        }
        ZamWord element = zam_thread_peek(thread, count - 1 - index);
        if (element == 0) {
            memcpy(at, null, sizeof null - 1);
            at += sizeof null - 1;
            continue;
        }
        const ZamString *s = zam_string_of((void *)(uintptr_t)element);
        memcpy(at, s->bytes, s->length);
        at += s->length;
    }
    *at = ')';

    for (uint64_t index = 0; index < count; index++)
        popWord(thread);
    return string;
}

static void opCmp(ZamThread *thread) {
    const ZamImage *image = &thread->vm->image;
    ZamTypeID rightType = zam_thread_pop_type(thread);
    uint64_t rightWords = valueWordsOf(image, rightType);
    ZamTypeID leftType = zam_thread_peek_type(thread, rightWords);
    uint64_t leftWords = valueWordsOf(image, leftType);

    ZamInt order = valueCmp(thread, leftType,
                            locationAt(thread, rightWords + ZAMTYPE_ID_WORDS, leftWords),
                            rightType, locationAt(thread, 0, rightWords));

    popValue(thread, image, rightType);
    zam_thread_pop_type(thread);
    popValue(thread, image, leftType);
    zam_thread_push(thread, (ZamWord)order);
}

static void opCastToString(ZamThread *thread) {
    const ZamImage *image = &thread->vm->image;
    ZamTypeID type = zam_thread_pop_type(thread);

    ZamString *string = valueStr(thread, type, locationAt(thread, 0, valueWordsOf(image, type)));

    popValue(thread, image, type);
    zam_thread_push(thread, (ZamWord)(uintptr_t)string);
    zam_thread_push_mark(thread, thread->operandCount - 1);
}

static void opHash(ZamThread *thread) {
    const ZamImage *image = &thread->vm->image;
    ZamTypeID type = zam_thread_pop_type(thread);

    ZamWord hash = valueHash(thread, type, locationAt(thread, 0, valueWordsOf(image, type)));

    popValue(thread, image, type);
    zam_thread_push(thread, hash);
}

// callObjectMethod runs the slot'th compiled method of the value of type at
// receiver, with argument after the receiver for a method that takes one, and
// answers what it returned.
static ZamWord callObjectMethod(ZamThread *thread, const ZamImage *image,
                                ZamTypeID type, ZamWord receiver, uint64_t slot,
                                ZamWord argument) {
    ZamWord answer = 0;
    const ZamWord *value = zam_thread_location_address(thread, receiver);
    bool compiled;
    if (namesValueWithStr(image, type)) {
        void *pointee = *value == 0 ? NULL : zam_thread_location_address(thread, *value);
        compiled = callCompiledAs(thread, pointee, pointeeOf(image, type).shortID, slot,
                                  argument, &answer);
    } else {
        compiled = callCompiled(thread, instanceAt(thread, image, type, value), slot,
                                argument, &answer);
    }
    if (!compiled)
        zam_die(ZAMEXIT_PANIC, "asked a method of a null pointer\n");
    return answer;
}

// wordAt is the one word at location.
static ZamWord wordAt(ZamThread *thread, ZamWord location) {
    return *(const ZamWord *)zam_thread_location_address(thread, location);
}

static bool valueEq(ZamThread *thread, ZamTypeID leftType, ZamWord left,
                    ZamTypeID rightType, ZamWord right) {
    const ZamImage *image = &thread->vm->image;
    if (answersThroughObject(image, leftType) &&
        answersThroughObject(image, rightType)) {
        ZamWord l = wordAt(thread, left);
        ZamWord r = wordAt(thread, right);
        if (l == r)
            return true;
        if (l == 0 || r == 0) {
            return false;
        }
        return dispatchEqual(thread, image, leftType, l, r);
    }

    zam_thread_push_visited(thread);
    bool equal = equalValues(thread, leftType, left, rightType, right);
    zam_thread_pop_visited(thread);
    return equal;
}

static ZamInt valueCmp(ZamThread *thread, ZamTypeID leftType, ZamWord left,
                       ZamTypeID rightType, ZamWord right) {
    const ZamImage *image = &thread->vm->image;
    if (answersThroughObject(image, leftType) &&
        answersThroughObject(image, rightType)) {
        ZamWord l = wordAt(thread, left);
        ZamWord r = wordAt(thread, right);
        if (l == r) {
            return 0;
        }
        if (l == 0 || r == 0) {
            return (l > r) - (l < r);
        }
        return (ZamInt)callObjectMethod(thread, image, leftType, left, ZAMOBJECT_SLOT_CMP, r);
    }
    if (isTupleType(image, leftType) && isTupleType(image, rightType) &&
        sameTypeID(leftType, rightType))
        return tupleOrder(thread, image, leftType, left, right);

    return valuesOrderAt(image, leftType, zam_thread_location_address(thread, left),
                           rightType, zam_thread_location_address(thread, right));
}

// anyBody is the body of the any at anyBox, past its header.
static const ZamWord *anyBody(const void *anyBox) {
    return (const ZamWord *)((const uint8_t *)anyBox +
                             zam_object_header_bytes(ZAMOBJECT_INSTANCE));
}

static ZamWord valueHash(ZamThread *thread, ZamTypeID type, ZamWord location) {
    const ZamImage *image = &thread->vm->image;
    uint64_t typedHeader = zam_object_header_bytes(ZAMOBJECT_INSTANCE);

    if (ZAMTYPE_DECL(type.shortID) == ZAMPRIMITIVE_ANY) {
        const void *anyBox = (const void *)(uintptr_t)wordAt(thread, location);
        if (anyBox == NULL)
            zam_die(ZAMEXIT_PANIC, "hash of a null any\n");
        const ZamWord *body = anyBody(anyBox);
        ZamTypeID stored = zam_type_id_read(body);
        const uint8_t *payloadBox = (const uint8_t *)(uintptr_t)body[3];
        if (payloadBox == NULL)
            zam_die(ZAMEXIT_PANIC, "hash of an any with no payload\n");

        if (isTupleType(image, stored))
            return tupleHash(thread, image, stored,
                             (ZamWord)(uintptr_t)(payloadBox + typedHeader));
        if (answersThroughObject(image, stored))
            return callObjectMethod(thread, image, stored,
                                    (ZamWord)(uintptr_t)(payloadBox + typedHeader),
                                    ZAMOBJECT_SLOT_HASH, 0);
        uint64_t byteCount = zam_type_bytes(image, stored) - boxedHeaderBytes(image, stored);
        return fnv1a(payloadBox + typedHeader, byteCount);
    }

    if (isTupleType(image, type))
        return tupleHash(thread, image, type, location);

    if (answersThroughObject(image, type))
        return callObjectMethod(thread, image, type, location, ZAMOBJECT_SLOT_HASH, 0);

    if (primitiveOf(image, type) == ZAMPRIMITIVE_STRING) {
        ZamWord word = wordAt(thread, location);
        if (word == 0)
            return 0;
        const ZamString *s = zam_string_of((void *)(uintptr_t)word);
        return fnv1a((const uint8_t *)s->bytes, s->length);
    }

    const uint8_t *bytes = zam_thread_location_address(thread, location);
    uint64_t byteCount = zam_type_bytes(image, type);
    if (isInlineValue(image, type)) {
        uint64_t header = inlineValueHeaderBytes(image, type);
        bytes += header;
        byteCount -= header;
    }
    return fnv1a(bytes, byteCount);
}

// primitiveText writes the text of a primitive word to text, answering its
// length, or zero for a primitive with no text of its own.
static int primitiveText(ZamPrimitive primitive, ZamWord word, char text[64]) {
    switch (primitive) {
    case ZAMPRIMITIVE_BOOL:
        return snprintf(text, 64, word ? "true" : "false");
    case ZAMPRIMITIVE_CHAR: {
        uint32_t cp = (uint32_t)word;
        if (cp < 0x80) {
            text[0] = (char)cp;
            return 1;
        }
        if (cp < 0x800) {
            text[0] = (char)(0xC0 | (cp >> 6));
            text[1] = (char)(0x80 | (cp & 0x3F));
            return 2;
        }
        if (cp < 0x10000) {
            text[0] = (char)(0xE0 | (cp >> 12));
            text[1] = (char)(0x80 | ((cp >> 6) & 0x3F));
            text[2] = (char)(0x80 | (cp & 0x3F));
            return 3;
        }
        text[0] = (char)(0xF0 | (cp >> 18));
        text[1] = (char)(0x80 | ((cp >> 12) & 0x3F));
        text[2] = (char)(0x80 | ((cp >> 6) & 0x3F));
        text[3] = (char)(0x80 | (cp & 0x3F));
        return 4;
    }
    case ZAMPRIMITIVE_BYTE:
        return snprintf(text, 64, "%u", (unsigned)(uint8_t)word);
    case ZAMPRIMITIVE_INT:
        return snprintf(text, 64, "%lld", (long long)(ZamInt)word);
    case ZAMPRIMITIVE_UINT:
        return snprintf(text, 64, "%llu", (unsigned long long)(ZamUint)word);
    case ZAMPRIMITIVE_FLOAT:
        return snprintf(text, 64, "%g", floatOf(word));
    case ZAMPRIMITIVE_POSIT:
        return (ZamPosit)word == ZAMPOSIT_NAR
                   ? snprintf(text, 64, "NaR")
                   : snprintf(text, 64, "%g", zam_posit_to_double((ZamPosit)word));
    default:
        return 0;
    }
}

// framePayload lays the payload of type in payloadBox out at out as a frame
// holds a value of type.
static void framePayload(const ZamImage *image, ZamTypeID type,
                         const void *payloadBox, void *out) {
    uint64_t typedHeader = zam_object_header_bytes(ZAMOBJECT_INSTANCE);
    uint64_t inlineHeader = boxedHeaderBytes(image, type);
    uint8_t *destination = out;
    if (isMarkableInstance(image, type)) {
        *zam_mark_word_of(destination) = (ZamMarkWord){
            .state = ZAMMARK_STATE_WHITE,
            .kind = ZAMOBJECT_INSTANCE,
            .contained = 1,
            .upvalue = 0,
        };
        *zam_object_header_of(destination) = (ZamObjectHeader){
            .shortID = type.shortID,
            .elementCount = 1,
        };
    }
    memcpy(destination + inlineHeader, (const uint8_t *)payloadBox + typedHeader,
           valueWordsOf(image, type) * ZAMWORD_BYTES - inlineHeader);
}

// unboxedForStr lays the value an any holds out as it would stand in a frame,
// in memory the caller frees.
static ZamWord *unboxedForStr(const ZamImage *image, const void *anyBox,
                              ZamTypeID *type) {
    const ZamWord *body = anyBody(anyBox);
    ZamTypeID payloadType = zam_type_id_read(body);
    const void *payloadBox = (const void *)(uintptr_t)body[3];
    if (payloadBox == NULL)
        zam_die(ZAMEXIT_PANIC, "string cast of an any with no payload\n");

    uint64_t payloadWords = valueWordsOf(image, payloadType);
    ZamWord *value = calloc(payloadWords == 0 ? 1 : payloadWords, ZAMWORD_BYTES);
    if (value == NULL)
        zam_die(ZAMEXIT_OOM, "out of memory unboxing an any\n");
    framePayload(image, payloadType, payloadBox, value);
    *type = payloadType;
    return value;
}

static ZamString *valueStrOf(ZamThread *thread, ZamTypeID type, ZamWord location) {
    const ZamImage *image = &thread->vm->image;
    if (isReferenceType(image, type) && wordAt(thread, location) == 0)
        return NULL;

    if (isTupleType(image, type))
        return tupleString(thread, image, type, location);

    if (answersThroughObject(image, type) || namesValueWithStr(image, type)) {
        uint64_t slot = answersThroughObject(image, type) ? ZAMOBJECT_SLOT_STR
                                                          : ZAMVALUE_SLOT_STR;
        return (ZamString *)(uintptr_t)callObjectMethod(thread, image, type, location, slot, 0);
    }

    // An enum value backed by a primitive is written out as its backing value,
    // read at its own width, the values lying side by side.
    if (type.arrayLength == 0 &&
        zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID)) == ZAMBUCKET_ENUM) {
        const ZamEnumDefinition *d = zam_enum_definition(image, ZAMTYPE_DECL(type.shortID));
        if (d->valueCount != 0) {
            ZamTypeID backing = {.shortID = (ZamTypeShort)d->valueType};
            ZamWord value = 0;
            memcpy(&value, zam_vm_static(thread->vm, d->valueSlot) + wordAt(thread, location),
                   zam_type_bytes(image, backing));
            return valueStrOf(thread, backing, (ZamWord)(uintptr_t)&value);
        }
    }

    ZamPrimitive primitive = primitiveOf(image, type);
    if (primitive == ZAMPRIMITIVE_STRING) {
        return (ZamString *)(uintptr_t)wordAt(thread, location);
    }
    if (primitive != ZAMPRIMITIVE_COUNT) {
        char text[64];
        int length = primitiveText(primitive, wordAt(thread, location), text);
        if (length > 0) {
            ZamString *string = newString(thread, (uint64_t)length);
            memcpy(string->bytes, text, (size_t)length);
            return string;
        }
    }

    const uint8_t *bytes = zam_thread_location_address(thread, location);
    uint64_t byteCount = zam_type_bytes(image, type);
    if (isInlineValue(image, type)) {
        uint64_t header = inlineValueHeaderBytes(image, type);
        bytes += header;
        byteCount -= header;
    }
    char text[128];
    int length = snprintf(
        text, sizeof text, "@{%u.%llu.%llu}{%p}{0x%016llx}",
        (unsigned)type.shortID, (unsigned long long)type.arrayLength,
        (unsigned long long)type.tailLength, (const void *)bytes,
        (unsigned long long)fnv1a(bytes, byteCount));
    ZamString *string = newString(thread, (uint64_t)length);
    memcpy(string->bytes, text, (size_t)length);
    return string;
}

static ZamString *valueStr(ZamThread *thread, ZamTypeID type, ZamWord location) {
    const ZamImage *image = &thread->vm->image;
    if (ZAMTYPE_DECL(type.shortID) != ZAMPRIMITIVE_ANY)
        return valueStrOf(thread, type, location);

    ZamWord *unboxed = NULL;
    while (ZAMTYPE_DECL(type.shortID) == ZAMPRIMITIVE_ANY) {
        const void *anyBox = (const void *)(uintptr_t)wordAt(thread, location);
        if (anyBox == NULL) {
            free(unboxed);
            return NULL;
        }
        ZamWord *next = unboxedForStr(image, anyBox, &type);
        free(unboxed);
        unboxed = next;
        location = (ZamWord)(uintptr_t)unboxed;
    }
    ZamString *string = valueStrOf(thread, type, location);
    free(unboxed);
    return string;
}

void *zam_any_box(ZamThread *thread, ZamTypeID payloadType, const void *value) {
    const ZamImage *image = &thread->vm->image;
    uint64_t inlineHeader = boxedHeaderBytes(image, payloadType);
    uint64_t bodyBytes = valueWordsOf(image, payloadType) * ZAMWORD_BYTES - inlineHeader;
    uint64_t typedHeader = zam_object_header_bytes(ZAMOBJECT_INSTANCE);
    void *payloadBox = zam_vm_alloc(thread->vm, thread, ZAMOBJECT_INSTANCE,
                                    payloadType.shortID, 1, typedHeader + bodyBytes);
    memcpy((uint8_t *)payloadBox + typedHeader, (const uint8_t *)value + inlineHeader,
           bodyBytes);
    void *anyBox = zam_vm_alloc(thread->vm, thread, ZAMOBJECT_ANY,
                                (ZamTypeShort)ZAMPRIMITIVE_ANY << 16, 1,
                                typedHeader + 4 * ZAMWORD_BYTES);
    ZamWord *body = (ZamWord *)(void *)((uint8_t *)anyBox + typedHeader);
    zam_type_id_write(body, payloadType);
    body[3] = (ZamWord)(uintptr_t)payloadBox;
    return anyBox;
}

ZamTypeID zam_any_type(const void *anyBox) {
    if (anyBox == NULL)
        zam_die(ZAMEXIT_PANIC, "payload type of a null any\n");
    return zam_type_id_read(anyBody(anyBox));
}

void zam_any_unbox(ZamThread *thread, const void *anyBox, ZamTypeID target, void *out) {
    const ZamImage *image = &thread->vm->image;
    if (anyBox == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_ANY);
    const ZamWord *body = anyBody(anyBox);
    if (!typeIsSubtypeOf(image, zam_type_id_read(body), target))
        zamrt_panic(thread, ZAMPANIC_ANY_TYPE);
    const void *payloadBox = (const void *)(uintptr_t)body[3];
    if (payloadBox == NULL)
        zam_die(ZAMEXIT_PANIC, "unbox of an any with no payload\n");
    framePayload(image, target, payloadBox, out);
}

bool zam_value_eq(ZamThread *thread, ZamTypeID leftType, const void *left,
                  ZamTypeID rightType, const void *right) {
    return valueEq(thread, leftType, (ZamWord)(uintptr_t)left, rightType,
                   (ZamWord)(uintptr_t)right);
}

ZamInt zam_value_cmp(ZamThread *thread, ZamTypeID leftType, const void *left,
                     ZamTypeID rightType, const void *right) {
    return valueCmp(thread, leftType, (ZamWord)(uintptr_t)left, rightType,
                    (ZamWord)(uintptr_t)right);
}

ZamWord zam_value_hash(ZamThread *thread, ZamTypeID type, const void *value) {
    return valueHash(thread, type, (ZamWord)(uintptr_t)value);
}

ZamString *zam_value_str(ZamThread *thread, ZamTypeID type, const void *value) {
    return valueStr(thread, type, (ZamWord)(uintptr_t)value);
}

bool zam_type_is_class(const ZamImage *image, ZamTypeShort shortID) {
    return isClassBucket(image, ZAMTYPE_DECL(shortID));
}

uint64_t zam_value_header_bytes(const ZamImage *image, ZamTypeID type) {
    return inlineValueHeaderBytes(image, type);
}

uint64_t zam_value_words(const ZamImage *image, ZamTypeID type) {
    return valueWordsOf(image, type);
}

void zam_type_stamp(const ZamImage *image, void *object, ZamTypeID element,
                    uint64_t elementCount, uint64_t backDistance) {
    uint8_t *at = (uint8_t *)object;
    if (isMarkableInstance(image, element)) {
        stampContained(image, at, element, backDistance, elementCount);
        return;
    }
    if (!isTailedStruct(image, element))
        return;
    uint64_t elementBytes = zam_type_bytes(image, element);
    for (uint64_t index = 0; index < elementCount; index++)
        *(ZamWord *)(at + index * elementBytes) = element.tailLength;
}

void zam_value_free(ZamThread *thread, ZamTypeID type, ZamWord pointer) {
    const ZamImage *image = &thread->vm->image;
    if (pointer == 0)
        return;
    ZamTypeID pointee = pointeeOf(image, type);
    if (isMarkableInstance(image, pointee)) {
        ZamMarkWord mark = zam_mark_load((void *)(uintptr_t)pointer);
        if (mark.contained) {
            if (mark.upvalue != 0)
                zamrt_panic(thread, ZAMPANIC_FREE_INTERIOR);
            zam_vm_free_raw(thread, (void *)(uintptr_t)pointer);
            return;
        }
    }
    if (isCollectedAllocation(image, pointee)) {
        bool typeIsPointer = type.arrayLength == 0 &&
                             zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID)) == ZAMBUCKET_POINTER;
        if (typeIsPointer && !isMarkableInstance(image, pointee)) {
            zam_vm_free_raw(thread, (void *)(uintptr_t)pointer);
            return;
        }
        void *object = (void *)(uintptr_t)pointer;
        ZamHeap *heap = &thread->vm->heap;
        if (zam_heap_object_of(heap, object) != object) {
            bool start = zam_heap_contains(heap, object) && zam_heap_block_of(heap, object) == object;
            zamrt_panic(thread, start ? ZAMPANIC_DOUBLE_FREE : ZAMPANIC_FREE_INTERIOR);
        }
        zam_mark_kill((void *)(uintptr_t)pointer);
        return;
    }
    zam_vm_free_raw(thread, (void *)(uintptr_t)pointer);
}

void *zam_string_concat(ZamThread *thread, const void *left, const void *right) {
    const ZamString *l = zam_string_of((void *)left);
    const ZamString *r = zam_string_of((void *)right);
    ZamString *result = newString(thread, l->length + r->length);
    memcpy(result->bytes, l->bytes, l->length);
    memcpy(result->bytes + l->length, r->bytes, r->length);
    return result;
}

ZamTypeID zam_instance_type(const void *instance) {
    void *object = (void *)instance;
    return (ZamTypeID){
        .shortID = zam_object_header_of(object)->shortID,
        .tailLength = zam_mark_kind_of(object) == ZAMOBJECT_TAILED_INSTANCE
                          ? zam_object_tail_length_of(object)
                          : 0,
    };
}

uint32_t zam_interface_method_index(const ZamImage *image, ZamTypeShort concrete,
                                    ZamTypeShort interfaceType, uint32_t methodIndex) {
    const ZamPerfectHash *dispatch = dispatchOf(image, (ZamTypeID){.shortID = concrete});
    uint32_t index = 0;
    if (dispatch == NULL || !zam_dispatch_lookup(dispatch, interfaceType, methodIndex, &index))
        zam_die(ZAMEXIT_PANIC, "type %u implements no method %u of interface %u\n",
                concrete, methodIndex, interfaceType);
    return index;
}

void zam_value_stamp(ZamThread *thread, ZamTypeID type, void *value) {
    const ZamImage *image = &thread->vm->image;
    ZamTypeID element = type;
    element.arrayLength = 0;
    uint64_t elementCount = type.arrayLength == 0 ? 1 : type.arrayLength;
    if (isMarkableInstance(image, element)) {
        stampContained(image, value, element, 0, elementCount);
    } else if (isTailedStruct(image, element)) {
        uint64_t elementBytes = zam_type_bytes(image, element);
        for (uint64_t index = 0; index < elementCount; index++)
            *(ZamWord *)((uint8_t *)value + index * elementBytes) = element.tailLength;
    }
}

void zam_value_delete(ZamThread *thread, ZamTypeID type, const void *value) {
    const ZamImage *image = &thread->vm->image;
    if (!answersThroughObject(image, type))
        return;
    void *instance = instanceAt(thread, image, type, value);
    if (instance == NULL)
        return;
    zam_thread_call_method(thread, (ZamWord)(uintptr_t)instance, ZAMOBJECT_SLOT_DELETE);
}

void zam_value_mark(ZamThread *thread, ZamTypeID type, const void *value) {
    if (zam_gc_collecting(thread))
        zam_gc_mark_value(thread, (ZamWord)(uintptr_t)value, type);
}

void *zam_thread_allocate(ZamThread *thread, ZamTypeID type, uint64_t elementCount, bool manual) {
    const ZamImage *image = &thread->vm->image;
    ZamTypeID element = type;
    element.arrayLength = 0;
    bool markable = isMarkableInstance(image, type);
    uint64_t elementBytes = zam_type_bytes(image, element);
    // A count is a word an int may have written, so one with its sign bit set
    // was negative.
    if (elementCount > INT64_MAX)
        zamrt_panic(thread, ZAMPANIC_NEGATIVE_COUNT);
    uint64_t objectBytes;
    if (__builtin_mul_overflow(elementCount, elementBytes, &objectBytes) ||
        (markable && !manual && elementCount > UINT32_MAX))
        zamrt_panic(thread, ZAMPANIC_COUNT_OVERFLOW);
    void *object;

    if (markable && manual) {
        object = zam_vm_alloc_raw(thread, objectBytes);
        memset(object, 0, objectBytes);
        for (uint64_t index = 0; index < elementCount; index++)
            stampContained(image, (uint8_t *)object + index * elementBytes,
                           element, 0, 1);
    } else if (markable) {
        // The tail length is the one header word the allocator is not told,
        // since it is the caller that knows which of the two places it came
        // from. Everything else the header holds is the kind and the id.
        bool tailed = type.tailLength != 0;
        object = zam_vm_alloc(
            thread->vm, thread,
            tailed ? ZAMOBJECT_TAILED_INSTANCE : ZAMOBJECT_INSTANCE,
            type.shortID, elementCount, objectBytes);
        if (tailed)
            ((ZamWord *)object)[2] = type.tailLength;

        // What lies inside the allocation gets its headers here, the
        // allocator having written only the one at the front. An element
        // past the first is contained by the first, and a markable member of
        // any of them is contained by the element holding it.
        stampArrayTail(image, object, element, elementCount);
    } else if (isCollectedAllocation(image, type)) {
        // A run of reference slots — string, function or thread. Each slot
        // is one word (the pointer) and no element has a header, so this
        // is a raw block of memory the user manages with delete, the way
        // a struct run is: ss[i] is ss + i*word with nothing in front of
        // the first slot. The slots' pointees stay collected on their own
        // — a reference stored into a slot still has a live reference
        // somewhere else for as long as the program means to keep it, and
        // if it does not the collector will reach it from that other
        // reference or not at all.
        //
        // So a run of reference slots is not itself a root the collector
        // walks: an array that outlives the frame it was declared in is
        // what a standard library array list is for, which stores the
        // slots behind a kind the collector does know about.
        object = zam_vm_alloc_raw(thread, objectBytes);
        memset(object, 0, objectBytes);
    } else {
        // A struct, an enum, a plain primitive or a pointer. The whole of
        // the block is the value — none of these carries a word saying what
        // it is — so the pointer handed back is the one the heap returned,
        // and freeing it frees that same address.
        //
        // It is not linked onto the sweep list, which is what makes this
        // memory the program's to release rather than the collector's. The
        // two byte counts a collection is triggered by are left alone for
        // the same reason: a collection would find nothing here it could
        // reclaim, so counting these bytes would only bring one on sooner
        // for no gain.
        object = zam_vm_alloc_raw(thread, objectBytes);
        memset(object, 0, objectBytes);

        // The one header a struct has, and it is written per element rather
        // than once over the run, a tail belonging to an instance and every
        // instance here having one of its own.
        //
        // Nothing else could write it. A struct is never marked and is never
        // reached under another type's id, so it carries neither of the
        // words that would say what it is, and how long its tail is lives in
        // the type id the allocation was named through and nowhere in the
        // block. A fresh struct standing on a stack carries the same word.
        if (isTailedStruct(image, element))
            for (uint64_t index = 0; index < elementCount; index++)
                *(ZamWord *)((uint8_t *)object + index * elementBytes) =
                    element.tailLength;
    }
    return object;
}

void *zam_thread_new_instance(ZamThread *thread, ZamTypeID element) {
    const ZamImage *image = &thread->vm->image;
    bool tailed = element.tailLength != 0;
    void *object = zam_vm_alloc(
        thread->vm, thread,
        tailed ? ZAMOBJECT_TAILED_INSTANCE : ZAMOBJECT_INSTANCE,
        element.shortID, 1, zam_type_bytes(image, element));
    if (tailed)
        ((ZamWord *)object)[2] = element.tailLength;
    stampArrayTail(image, object, element, 1);
    return object;
}

void zam_thread_initialize(ZamThread *thread, void *object,
                           ZamTypeID element, uint64_t elementCount) {
    const ZamImage *image = &thread->vm->image;
    if (primitiveOf(image, element) != ZAMPRIMITIVE_COUNT ||
        isReferenceType(image, element))
        return;
    const ZamCompiledClass *compiled = zam_vm_compiled_class(thread->vm, element.shortID);
    if (compiled == NULL || compiled->initializer == NULL)
        return;

    uint64_t stride = zam_type_bytes(image, element);
    for (uint64_t i = 0; i < elementCount; i++) {
        ZamWord at = (ZamWord)(uintptr_t)((uint8_t *)object + i * stride);
        compiled->initializer(thread, NULL, &at, NULL);
    }
}

void zam_thread_call_method(ZamThread *thread, ZamWord receiver, uint64_t slot) {
    callCompiled(thread, zam_thread_location_address(thread, receiver), slot, 0, NULL);
}

void zam_thread_push_value(ZamThread *thread, ZamTypeID type, const ZamWord *words) {
    const ZamImage *image = &thread->vm->image;
    uint64_t count = valueWordsOf(image, type);
    for (uint64_t i = 0; i < count; i++)
        zam_thread_push(thread, words[i]);
    markPushedValue(thread, image, type);
}

void zam_thread_join(ZamThread *thread, ZamThread *target) {
    joinThread(thread, target);
}

// joinThread waits for target to finish. The wait may be long, so it is a safe
// region: a collection need not wait for it to end.
static void joinThread(ZamThread *thread, ZamThread *target) {
    pthread_t systemThread = target->systemThread;
    zam_vm_enter_safe(thread);
    pthread_join(systemThread, NULL);
    zam_vm_leave_safe(thread);
    target->joined = true;
}

// callCompiledFunction is zam_native_call in a natively compiled program,
// where a function value's body is a compiled entry. The entry roots its
// arguments in its own frame before anything can collect.
static uint64_t callCompiledFunction(ZamThread *thread, ZamWord function, const ZamTypeList *signature,
                                     const ZamWord *args, uint64_t argWords, ZamWord *result,
                                     uint64_t resultCapacity) {
    const ZamImage *image = &thread->vm->image;
    ZamTypeID returned = zam_type_list_at(signature, 0);
    uint64_t words = returned.shortID == ZAMTYPE_SHORT(ZAMPRIMITIVE_VOID, 0) &&
                             returned.arrayLength == 0
                         ? 0
                         : valueWordsOf(image, returned);
    if (words > resultCapacity)
        zam_die(ZAMEXIT_PANIC, "zam_native_call given room for %llu result words of %llu\n",
                (unsigned long long)resultCapacity, (unsigned long long)words);

    ZamClosure *closure = zam_closure_of((void *)(uintptr_t)function);
    ZamWord *copy = zam_alloc((argWords + words + 1) * sizeof(ZamWord));
    memcpy(copy, args, argWords * sizeof(ZamWord));
    ZamWord *answer = copy + argWords;
    ZamHandler *handlers = zamrt_handlers_hide(thread);
    ((ZamEntry)(uintptr_t)closure->entry)(thread, closure, copy, answer);
    zamrt_handlers_show(thread, handlers);
    memcpy(result, answer, words * sizeof(ZamWord));
    free(copy);
    return words;
}

uint64_t zam_native_call(ZamThread *thread, ZamWord function, const ZamWord *args,
                         uint64_t argWords, ZamWord *result, uint64_t resultCapacity) {
    if (thread->state == ZAMTHREAD_SAFE)
        zam_die(ZAMEXIT_PANIC, "zam_native_call between zam_native_block_begin and _end\n");
    if (function == 0)
        zam_die(ZAMEXIT_PANIC, "called a null function\n");
    const ZamImage *image = &thread->vm->image;
    const ZamTypeList *signature = zam_function_type_signature(
        image, ZAMTYPE_DECL(zam_object_header_of((void *)(uintptr_t)function)->shortID));

    uint64_t expected = 0;
    for (uint64_t i = 1; i < signature->count; i++)
        expected += valueWordsOf(image, zam_type_list_at(signature, i));
    if (expected != argWords)
        zam_die(ZAMEXIT_PANIC,
                "zam_native_call passed %llu argument words to a function taking %llu\n",
                (unsigned long long)argWords, (unsigned long long)expected);

    return callCompiledFunction(thread, function, signature, args, argWords, result,
                                resultCapacity);
}
