#include "zam/native.h"

#include "definition.h"
#include "gc.h"
#include "opcode.h"
#include "run.h"
#include "thread.h"
#include "vm.h"

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

    // A class, struct or enum lies inline where it stands, so what is on the
    // stack is the value and there is no address to follow.
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
    for (uint64_t index = 0; index < count && !contains; index++)
        contains = zam_type_contains_markable(
            vm, zam_member_type(zam_type_member(image, type.shortID, index)));
    atomic_store_explicit(&vm->markableDecls[declID], contains ? 2 : 1,
                          memory_order_relaxed);
    return contains;
}

// Which primitive the type is, or ZAMPRIMITIVE_COUNT where it is not one. An
// array of primitives is not one: what stands on the stack is the run, and no
// instruction told a single primitive means that.
static ZamPrimitive primitiveOf(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    if (type.arrayLength != 0 ||
        zam_image_bucket_of(image, declID) != ZAMBUCKET_PRIMITIVE)
        return ZAMPRIMITIVE_COUNT;
    return (ZamPrimitive)declID;
}

// What a pointer type points at, and the type itself where it is not one. A
// method or a member is reached on the instance whether the program named it
// through a pointer or held the instance directly, so the instructions that
// resolve an index against a type step through this rather than each deciding
// what a pointer means.
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

// Whether the type is one an instance is only ever reached through, so that the
// index standing over it names a method of whatever the instance turns out to
// be rather than of the type it was written as.
static bool isVirtuallyTyped(const ZamImage *image, ZamTypeID type) {
    switch (zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID))) {
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_GENERIC_ABSTRACT:
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
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

static ZamWord wordOfFloat(ZamFloat value) {
   ZamWord word;
    memcpy(&word, &value, sizeof word);
    return word;
}

// A comparison leaves a bool, which is one word holding one or zero — the same
// word OpJumpIfFalse tests, which is why asking whether a value is zero and
// asking whether to branch are the same question here.
static ZamWord boolWord(bool value) { return value ? 1 : 0; }

// The two operands of a binary instruction come off in the order they were
// pushed, so the right is the one on top.
//
// Every wrapping result is computed on the unsigned word and cast back rather
// than on the signed one. Signed overflow is undefined in C, and a sum that
// wraps is a thing this language means rather than a thing the runtime may
// reinterpret; the unsigned computation is that meaning written down.
static ZamWord integerBinary(ZamThread *thread, ZamOpcode opcode) {
   ZamWord rightWord = zam_thread_pop(thread);
   ZamWord leftWord = zam_thread_pop(thread);
   ZamInt left = (ZamInt)leftWord;
   ZamInt right = (ZamInt)rightWord;

    switch (opcode) {
    case OpAddI64:
        return leftWord + rightWord;
    case OpSubI64:
        return leftWord - rightWord;
    case OpMulI64:
        return leftWord * rightWord;

    case OpDivI64:
    case OpModI64:
        // A division by zero is the program's fault and is stopped for, rather
        // than being left to whatever the hardware does with it.
        if (right == 0)
            zam_die(ZAMEXIT_PANIC, "divide by zero\n");
        // The other division C leaves undefined, and the one a processor traps
        // on: the most negative integer has no positive counterpart, so its
        // quotient by -1 is itself and its remainder is nothing. That is what
        // the wrapping this language does everywhere else already says.
        if (left == INT64_MIN && right == -1)
            return opcode == OpDivI64 ? leftWord : 0;
        return opcode == OpDivI64 ? (ZamWord)(left / right)
                                  : (ZamWord)(left % right);

    case OpAndI64:
        return leftWord & rightWord;
    case OpOrI64:
        return leftWord | rightWord;
    case OpXorI64:
        return leftWord ^ rightWord;
    case OpEqualI64:
        return boolWord(leftWord == rightWord);

    // A shift of 64 or more is undefined, and deliberately not checked for: the
    // program asked a 64 bit word a question its width has no answer to, and
    // the runtime does not pay a branch on every shift to say so. The
    // unarithmetic shift is the unsigned one, which is what makes it the one
    // that does not carry the sign bit down.
    case OpShiftLeftI64:
        return leftWord << rightWord;
    case OpShiftRightI64:
        return leftWord >> rightWord;
    case OpShiftArithmeticRightI64:
        return (ZamWord)(left >> right);

    default:
        zam_die(ZAMEXIT_PANIC, "%s is not an integer instruction\n",
                zam_opcode_name(opcode));
    }
}

static ZamWord floatBinary(ZamThread *thread, ZamOpcode opcode) {
   ZamFloat right = floatOf(zam_thread_pop(thread));
   ZamFloat left = floatOf(zam_thread_pop(thread));

    switch (opcode) {
    case OpAddF64:
        return wordOfFloat(left + right);
    case OpSubF64:
        return wordOfFloat(left - right);
    case OpMulF64:
        return wordOfFloat(left * right);
    // A division by zero is a float's own business and not the runtime's: it
    // has an infinity to be, which is the answer rather than a fault. That is
    // the whole of why this is not the integer case.
    case OpDivF64:
        return wordOfFloat(left / right);
    // A NaN compares equal to nothing, itself included, which C already does —
    // so the three conditions the opcode is written as are the one comparison.
    case OpEqualF64:
        return boolWord(left == right);

    default:
        zam_die(ZAMEXIT_PANIC, "%s is not a float instruction\n",
                zam_opcode_name(opcode));
    }
}

// The word a step leaves, which is the one arithmetic whose size the type has
// to be asked for: an int and a char move by one of themselves, a float by one,
// and a pointer by a whole element of whatever it points at. Nothing scaled
// that before the instruction — emitStep in 3_operation.go leaves it here on
// purpose, a template's stride not being settled until the id in hand says
// which instantiation is running.
//
// The pointee is what says the type is a pointer at all. pointeeOf hands back
// the type itself where it is not one, and no type points at itself, so the two
// coming out different is exactly the case there is a stride to read.
static ZamWord steppedWord(const ZamImage *image, ZamTypeID type,
                          ZamOpcode opcode, ZamWord word) {
    bool up = opcode == OpIncPre || opcode == OpIncPost;

    switch (primitiveOf(image, type)) {
    // Computed on the unsigned word for the reason every other wrapping result
    // is, and narrowed to the destination's own width by the write rather than
    // here: what a char, a byte, an int or a uint counts in is one of itself,
    // which is the same one.
    case ZAMPRIMITIVE_INT:
    case ZAMPRIMITIVE_UINT:
    case ZAMPRIMITIVE_CHAR:
    case ZAMPRIMITIVE_BYTE:
        return up ? word + 1 : word - 1;

    case ZAMPRIMITIVE_FLOAT:
        return wordOfFloat(up ? floatOf(word) + 1 : floatOf(word) - 1);

    default:
        break;
    }

   ZamTypeID pointee = pointeeOf(image, type);
    if (pointee.shortID != type.shortID) {
        uint64_t stride = zam_type_bytes(image, pointee);
        return up ? word + stride : word - stride;
    }

    zam_die(ZAMEXIT_PANIC, "type %u counts in nothing a step could move it by\n",
            type.shortID);
}

// Discarding a word and the mark entry it owns, which is what OpPopWord is and
// what spending a value off the stack is made of. The pop comes first so that
// its underflow check runs, and the entry is then looked for at the index the
// pop just freed.
static void popWord(ZamThread *thread) {
    zam_thread_pop(thread);
    zam_thread_pop_mark(thread, thread->operandCount);
}

// offsetWord is a sum or difference of two words, one of which may be an
// address into an object: an array's base, or an element of one, stepped
// along. A sum keeps the object alive the way the address it was taken from
// did, so where either side reached an object the sum owns an interior entry,
// its distance moved by however far the sum moved from that side.
static void offsetWord(ZamThread *thread, ZamOpcode opcode) {
    if (thread->operandCount < 2)
        zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
    uint64_t rightAt = thread->operandCount - 1;
   ZamWord right = thread->operands[rightAt];
   ZamWord left = thread->operands[rightAt - 1];

    uint64_t rightDistance = 0;
    uint64_t leftDistance = 0;
    bool rightReaches =
        zam_thread_pop_object_mark(thread, rightAt, &rightDistance);
    bool leftReaches =
        zam_thread_pop_object_mark(thread, rightAt - 1, &leftDistance);

   ZamWord sum = integerBinary(thread, opcode);
    zam_thread_push(thread, sum);

   ZamWord base;
    uint64_t distance;
    if (leftReaches) {
        base = left;
        distance = leftDistance;
    } else if (rightReaches && opcode == OpAddI64) {
        base = right;
        distance = rightDistance;
    } else {
        return;
    }

    int64_t at = (int64_t)distance + (int64_t)(sum - base);
    if (sum != 0 && !zam_is_frame_location(sum) && at >= 0)
        zam_thread_push_interior_mark(thread, thread->operandCount - 1,
                                      (uint64_t)at);
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

// Declared ahead of themselves because a comparison of two values has to
// descend into a member that answers for itself: the first says a member does,
// and the second is how its Eq is reached. Both sit with the other questions
// asked of a type id rather than here.
static bool answersThroughObject(const ZamImage *image, ZamTypeID type);
static ZamResult enterObjectMethod(ZamThread *thread, const ZamImage *image,
                                  ZamTypeID receiverType, uint64_t slot,
                                   bool hasArgument, ZamWord argument,
                                  ZamTypeID argumentType);

// Declared ahead of itself because running that Eq is running instructions. It
// is the one call out of these helpers back into the loop they are called from.
static ZamResult step(ZamThread *thread);

// Declared ahead of itself for the reason step is: OpNewThread hands this to
// pthread_create, and its body is the one that drives the new thread's loop.
static void *threadMain(void *arg);

// Declared ahead of itself beside threadMain, the two being the halves of a
// spawned thread's life that the run loop does not see.
static void joinThread(ZamThread *thread, ZamThread *target);

// The mark entry the words of a value own, the value standing at the top of the
// stack. Whatever pushed it decided how the words got there and this decides
// what the collector is told of them, so the three instructions that leave a
// whole value of a type the program named say it the same way.
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

// Where a value begins, given how many words it stands on and how many words of
// other things stand above it. Reading a value wider than a word cannot go
// through zam_thread_peek, which is told a depth and hands back one word.
static const ZamWord *valueAt(const ZamThread *thread, uint64_t above,
                              uint64_t words) {
    if (above + words > thread->operandCount)
        zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
    return thread->operands + thread->operandCount - above - words;
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

// The closure standing for one method bound to the receiver on top of the
// stack, the receiver taken off and the closure left in its place.
//
// This is the whole of what a method selection builds and the whole of what the
// two selections share. Which method is asked is where they differ — an
// abstract's index is read off the class it turns out to be and an interface's
// has to be translated through one — and by here that is settled and what is
// left is the same closure either way.
//
// The receiver type is the one the source wrote and not the one the instance
// turns out to be. Its width and its reference-ness are what the words standing
// here actually are, and the entry is written to be read back by those.
static void pushMethodClosure(ZamThread *thread, const ZamImage *image,
                             ZamTypeID receiverType,
                             ZamMethodDefinition method) {
    // Which of the two method closures this is, which is the whole of what the
    // receiver's type id was pushed to settle: an address stands for a receiver
    // that lives somewhere else, and anything else is a receiver that lives here
    // and nowhere.
    uint64_t receiverWords = valueWordsOf(image, receiverType);
    bool homeless = !isReferenceType(image, receiverType);
   ZamObjectKind kind = homeless ? ZAMOBJECT_VALUE_METHOD_CLOSURE
                                  : ZAMOBJECT_REFERENCE_METHOD_CLOSURE;

    // A homeless receiver is written twice: once as the value, which the closure
    // is now the home of, and once as the address of that copy. The body reads
    // its receiver as one word holding an address whichever kind the call was
    // made through — emitMethod in 3_operation.go compiles the slot as a pointer
    // whatever the declaration's own width — so the word the frame wants has to
    // exist somewhere, and the only place that outlives the call is the closure
    // itself.
    //
    // The address entry is what makes the two kinds one shape from the second
    // entry on: a reference closure's captures begin after one entry and a value
    // closure's after two, and past that offset nothing reading a capture has to
    // know which it had.
    uint64_t entryWords = ZAMTYPE_ID_WORDS + receiverWords;
    if (homeless)
        entryWords += ZAMTYPE_ID_WORDS + 1;
    uint64_t bodyBytes = sizeof(ZamClosure) + entryWords * ZAMWORD_BYTES;
    void *object = zam_vm_alloc(thread->vm, thread, kind,
                                ZAMTYPE_SHORT(method.typeListID, 0), 1,
                                bodyBytes);
   ZamClosure *closure = zam_closure_of(object);
    closure->instruction = method.instruction;
    closure->captureCount = 0;
    zam_type_id_write(closure->body, receiverType);
    memcpy(closure->body + ZAMTYPE_ID_WORDS, valueAt(thread, 0, receiverWords),
           receiverWords * ZAMWORD_BYTES);

    // The address is written under a plain word's type rather than under a
    // pointer's, and that is deliberate on both counts a type id is read for.
    // Its width is the one word an address is, which is what keeps the walk to
    // the captures right. And it names a place inside this very allocation, so a
    // collector must not follow it as it would follow a pointer: what the
    // receiver's own members owe is owed through the value at the entry above,
    // which is traced where it lies.
    //
    // A pointer's own id could not be written here in any case. Pointer types
    // are collected from the ones the source writes — see collectTypes in
    // 3_semantic/7_collect_types.go — so a receiver the program never took the
    // address of has no pointer type in the image, and nothing at runtime maps a
    // type to a pointer to it.
    if (homeless) {
       ZamWord *address = closure->body + ZAMTYPE_ID_WORDS + receiverWords;
        zam_type_id_write(
            address, (ZamTypeID){.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_INT, 0)});
        address[ZAMTYPE_ID_WORDS] =
            (ZamWord)(uintptr_t)(closure->body + ZAMTYPE_ID_WORDS);
    }

    popValue(thread, image, receiverType);
    zam_thread_push(thread, (ZamWord)(uintptr_t)object);
    zam_thread_push_mark(thread, thread->operandCount - 1);
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

// Running a routine entered from inside an instruction through to its return,
// which is what a walk of two values needs and what a dispatching instruction
// does not.
//
// A dispatcher lays the frame, jumps, and is finished: the body's own result
// lands where the instruction's belongs, so there is nothing left for it to do.
// A walk has more members to visit once one member's Eq has answered, so it has
// to still be running when the body returns — and a jump would have left it.
//
// outerEntry is the innermost open call from before the frame was laid.
// zam_thread_push_function and zam_thread_pop_function are the only two things
// that move it and no one instruction does both, so the entry coming back to
// what it was is the entered body having returned and nothing else. An offset
// reused by a later call cannot be mistaken for it: the check runs after every
// step, so the loop leaves on the step that returned and never reaches a push.
//
// Nothing restores the program counter, there being nothing to restore.
// enterObjectMethod recorded the counter as the return instruction, which inside
// a case body is already the instruction after this one, and the returning step
// put it back — so what the counter holds when this leaves is what the walk's
// own caller was going to fall through to.
static ZamResult runEntered(ZamThread *thread, uint64_t outerEntry) {
    while (thread->functionEntry != outerEntry) {
       ZamResult result = step(thread);
        if (result != ZAMRESULT_CONTINUE)
            return result;
    }
    return ZAMRESULT_CONTINUE;
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
// one place the three kinds differ in shape — the same step OpPushReceiver
// makes, and for the same reason.
static const ZamWord *closureCaptures(const ZamImage *image,
                                      const ZamClosure *closure) {
    const ZamWord *entry = zam_closure_first(closure);
    entry = zam_closure_entry_next(
        entry, valueWordsOf(image, zam_closure_entry_type(entry)));
    if (zam_mark_word_of((void *)closure)->kind ==
        ZAMOBJECT_VALUE_METHOD_CLOSURE)
        entry = zam_closure_entry_next(
            entry, valueWordsOf(image, zam_closure_entry_type(entry)));
    return entry;
}

// Whether the two values lying at the two locations are equal, which is the
// whole of what OpEq answers and is split out because how the question is
// settled is the type's to say.
//
// Locations and not addresses, which is what lets the walk descend. A value
// reached here may be compared by running its own Eq, running a body pushes, and
// the operand stack grows by realloc — so an address worked out before a descent
// would name where the value used to be. A member or an element is the location
// plus an offset under either form, the tag riding along untouched, so one sum
// serves a value on the stack and a value in an allocation alike.
//
// The answer goes to equal and what comes back is what the interpreter should do
// next, because a descent runs instructions and a body can exit or block.
// Anything but ZAMRESULT_CONTINUE leaves equal untouched and is the caller's to
// pass on.
//
// Member by member and never as a run of bytes, which three things turn on.
// A float is not its bits — IEEE has two zeros that are one number and a NaN
// that is equal to nothing, not even to a copy of itself. A member that answers
// through an Eq of its own has to reach it. And the padding between members
// belongs to whatever wrote the instance rather than to the value, so two
// instances with equal members and unequal padding are equal and a memcmp of
// them is not.
static ZamResult equalValues(ZamThread *thread, ZamTypeID leftType,
                            ZamWord leftLocation, ZamTypeID rightType,
                            ZamWord rightLocation, bool *equal);

// Entering the Eq of the instance the left pointer names on the one the right
// names, and running it to its return. This is how a walk asks a value that
// answers for itself.
//
// The frame is the one enterObjectMethod lays and so the one a compiled call
// lays: the closure at the base with the argument above it, and the base is
// where the returned value lands. Here the base is the top of the stack rather
// than where the answer belongs — a walk partway through has no one place for
// one — so the bool is read off the top and spent, leaving the stack as deep as
// it was found.
static ZamResult dispatchEqual(ZamThread *thread, const ZamImage *image,
                              ZamTypeID leftType, ZamWord left,
                              ZamTypeID rightType, ZamWord right,
                               bool *equal) {
    uint64_t outerEntry = thread->functionEntry;

    zam_thread_push(thread, left);
    markPushedValue(thread, image, leftType);

   ZamResult result = enterObjectMethod(thread, image, leftType,
                                         ZAMOBJECT_SLOT_EQ, true, right,
                                         rightType);
    if (result == ZAMRESULT_CONTINUE)
        result = runEntered(thread, outerEntry);
    if (result != ZAMRESULT_CONTINUE)
        return result;

    *equal = zam_thread_peek(thread, 0) != 0;
    popWord(thread);
    return ZAMRESULT_CONTINUE;
}

// Two closures, which are equal when they are the same body closed over equal
// values.
//
// The instruction alone is not it. One lambda is written once and compiled once,
// so two closures made in two turns of a loop share an instruction and closed
// over different values, and they are not the same function. Nor are the
// captures alone: two bodies may capture nothing and still be two functions.
//
// Each capture is compared under its own type, which the closure carries: an
// entry is a type id and then the value, so a walk of one is as typed as a walk
// of an instance's members. The two are walked in lockstep, their counts already
// agreeing, and a capture's location is an address rather than a frame location
// — the closure is an allocation, which the collector never moves.
static ZamResult equalClosures(ZamThread *thread, const ZamImage *image,
                              ZamWord left, ZamWord right, bool *equal) {
    const ZamClosure *l = zam_closure_of((void *)(uintptr_t)left);
    const ZamClosure *r = zam_closure_of((void *)(uintptr_t)right);

    if (l->instruction != r->instruction || l->captureCount != r->captureCount) {
        *equal = false;
        return ZAMRESULT_CONTINUE;
    }

    const ZamWord *leftEntry = closureCaptures(image, l);
    const ZamWord *rightEntry = closureCaptures(image, r);
    for (uint64_t at = 0; at < l->captureCount; at++) {
       ZamTypeID leftCapture = zam_closure_entry_type(leftEntry);
       ZamTypeID rightCapture = zam_closure_entry_type(rightEntry);

       ZamResult result = equalValues(
            thread, leftCapture,
            (ZamWord)(uintptr_t)zam_closure_entry_value(leftEntry),
            rightCapture,
            (ZamWord)(uintptr_t)zam_closure_entry_value(rightEntry), equal);
        if (result != ZAMRESULT_CONTINUE || !*equal)
            return result;

        leftEntry = zam_closure_entry_next(leftEntry,
                                           valueWordsOf(image, leftCapture));
        rightEntry = zam_closure_entry_next(rightEntry,
                                            valueWordsOf(image, rightCapture));
    }

    *equal = true;
    return ZAMRESULT_CONTINUE;
}

static ZamResult equalValues(ZamThread *thread, ZamTypeID leftType,
                            ZamWord leftLocation, ZamTypeID rightType,
                            ZamWord rightLocation, bool *equal) {
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
        if (leftType.arrayLength != rightType.arrayLength) {
            *equal = false;
            return ZAMRESULT_CONTINUE;
        }

       ZamTypeID leftElement = leftType;
       ZamTypeID rightElement = rightType;
        leftElement.arrayLength = 0;
        rightElement.arrayLength = 0;
        uint64_t leftStride = zam_type_bytes(image, leftElement);
        uint64_t rightStride = zam_type_bytes(image, rightElement);

        for (uint64_t at = 0; at < leftType.arrayLength; at++) {
           ZamResult result = equalValues(
                thread, leftElement, leftLocation + at * leftStride,
                rightElement, rightLocation + at * rightStride, equal);
            if (result != ZAMRESULT_CONTINUE || !*equal)
                return result;
        }

        *equal = true;
        return ZAMRESULT_CONTINUE;
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
    // carried on the stack was stripped by OpBoxAny and will be put back by
    // OpAnyUnbox — so the location given to equalValues has to be far
    // enough behind the stored body that its header+offset walk lands on
    // the first member. typedHeader - inlineHeader is that distance: the
    // box's own object header stands in for the stripped inline header.
    if (ZAMTYPE_DECL(leftType.shortID) == ZAMPRIMITIVE_ANY &&
        ZAMTYPE_DECL(rightType.shortID) == ZAMPRIMITIVE_ANY) {
       ZamWord leftBox =
            wordOfValue(thread, image, leftType, leftLocation);
       ZamWord rightBox =
            wordOfValue(thread, image, rightType, rightLocation);
        if (leftBox == rightBox) {
            *equal = true;
            return ZAMRESULT_CONTINUE;
        }
        if (leftBox == 0 || rightBox == 0) {
            *equal = false;
            return ZAMRESULT_CONTINUE;
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
        if (!sameTypeID(leftStored, rightStored) && !bothRef) {
            *equal = false;
            return ZAMRESULT_CONTINUE;
        }

        uint8_t *leftPayload = (uint8_t *)(uintptr_t)leftBody[3];
        uint8_t *rightPayload = (uint8_t *)(uintptr_t)rightBody[3];
       ZamWord leftPayloadLoc = (ZamWord)(uintptr_t)(
            leftPayload + typedHeader -
            inlineValueHeaderBytes(image, leftStored));
       ZamWord rightPayloadLoc = (ZamWord)(uintptr_t)(
            rightPayload + typedHeader -
            inlineValueHeaderBytes(image, rightStored));
        return equalValues(thread, leftStored, leftPayloadLoc, rightStored,
                           rightPayloadLoc, equal);
    }

    // One any and one null is a question about the box pointer alone: the
    // null any is the zero word, and a non-null any has a box it names. The
    // semantic layer refuses every other mixed pairing.
    if (ZAMTYPE_DECL(leftType.shortID) == ZAMPRIMITIVE_ANY ||
        ZAMTYPE_DECL(rightType.shortID) == ZAMPRIMITIVE_ANY) {
        *equal = wordOfValue(thread, image, leftType, leftLocation) ==
                 wordOfValue(thread, image, rightType, rightLocation);
        return ZAMRESULT_CONTINUE;
    }

   ZamPrimitive kind = primitiveOf(image, leftType);
    if (kind == primitiveOf(image, rightType)) {
        switch (kind) {
        case ZAMPRIMITIVE_BOOL:
        case ZAMPRIMITIVE_CHAR:
        case ZAMPRIMITIVE_BYTE:
        case ZAMPRIMITIVE_INT:
        case ZAMPRIMITIVE_UINT:
            *equal = wordOfValue(thread, image, leftType, leftLocation) ==
                     wordOfValue(thread, image, rightType, rightLocation);
            return ZAMRESULT_CONTINUE;

        // Asked as the number and not as the bits, which is the whole reason a
        // float could not be left to a run of bytes: 0.0 and -0.0 are two
        // spellings of one number, and a NaN is no number and so equal to
        // nothing — not even to a copy of itself, which byte comparison would
        // call equal.
        case ZAMPRIMITIVE_FLOAT:
            *equal =
                floatOf(wordOfValue(thread, image, leftType, leftLocation)) ==
                floatOf(wordOfValue(thread, image, rightType, rightLocation));
            return ZAMRESULT_CONTINUE;

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
            if (left == right) {
                *equal = true;
                return ZAMRESULT_CONTINUE;
            }

            const ZamString *l = zam_string_of((void *)(uintptr_t)left);
            const ZamString *r = zam_string_of((void *)(uintptr_t)right);
            *equal = l->length == r->length &&
                     memcmp(l->bytes, r->bytes, l->length) == 0;
            return ZAMRESULT_CONTINUE;
        }

        case ZAMPRIMITIVE_TYPE:
            *equal = sameTypeID(typeIDAt(thread, leftLocation),
                                typeIDAt(thread, rightLocation));
            return ZAMRESULT_CONTINUE;

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

        if (left == right) {
            *equal = true;
            return ZAMRESULT_CONTINUE;
        }
        if (left == 0 || right == 0) {
            *equal = false;
            return ZAMRESULT_CONTINUE;
        }
        if (zam_thread_visit(thread, left, right, leftType.shortID)) {
            *equal = true;
            return ZAMRESULT_CONTINUE;
        }

        return dispatchEqual(thread, image, leftType, left, rightType, right,
                             equal);
    }

    if (isFunctionType(image, leftType) && isFunctionType(image, rightType)) {
       ZamWord left = wordOfValue(thread, image, leftType, leftLocation);
       ZamWord right = wordOfValue(thread, image, rightType, rightLocation);

        // The same two a dispatching pair settles, for the same reasons: one
        // closure is the same function as itself, and a null closed over
        // nothing and has no body to be the body of. No pair is recorded — a
        // capture is held by value, so a closure cannot be reached from inside
        // itself and a walk of one cannot come back.
        if (left == right) {
            *equal = true;
            return ZAMRESULT_CONTINUE;
        }
        if (left == 0 || right == 0) {
            *equal = false;
            return ZAMRESULT_CONTINUE;
        }

        return equalClosures(thread, image, left, right, equal);
    }

    // A reference that is neither of those is the word it holds and nothing
    // more, so the word is the whole of what equality can mean for it: a pointer
    // to a struct or an enum, a void*, a thread. A string is a reference too and
    // is answered above, by its bytes rather than by where they lie.
    if (isReferenceType(image, leftType) && isReferenceType(image, rightType)) {
        *equal = wordOfValue(thread, image, leftType, leftLocation) ==
                 wordOfValue(thread, image, rightType, rightLocation);
        return ZAMRESULT_CONTINUE;
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
        if (leftType.tailLength != rightType.tailLength) {
            *equal = false;
            return ZAMRESULT_CONTINUE;
        }

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
            *equal = leftTag == rightTag;
            return ZAMRESULT_CONTINUE;
        }

        uint64_t leftHeader = inlineValueHeaderBytes(image, leftType);
        uint64_t rightHeader = inlineValueHeaderBytes(image, rightType);

        uint64_t count = zam_type_member_count(image, leftType.shortID);
        for (uint64_t index = 0; index < count; index++) {
            const ZamMemberDefinition *member =
                zam_type_member(image, leftType.shortID, index);
           ZamTypeID memberType = zam_member_type(member);

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

           ZamResult result = equalValues(
                thread, memberType, leftLocation + leftHeader + member->offset,
                memberType, rightLocation + rightHeader + member->offset,
                equal);
            if (result != ZAMRESULT_CONTINUE || !*equal)
                return result;
        }

        *equal = true;
        return ZAMRESULT_CONTINUE;
    }

    zam_die(ZAMEXIT_PANIC,
            "unreachable, logging: equality of left %u/%llu/%llu "
            "and right %u/%llu/%llu\n",
            leftType.shortID, (unsigned long long)leftType.arrayLength,
            (unsigned long long)leftType.tailLength, rightType.shortID,
            (unsigned long long)rightType.arrayLength,
            (unsigned long long)rightType.tailLength);
}

// How the two values standing beneath the top of the stack are ordered, which
// is the whole of what OpCmp answers. It is split out for the reason equality
// is: what an order comes to is the type's to say.
static ZamInt valuesOrder(const ZamThread *thread, const ZamImage *image,
                         ZamTypeID leftType, ZamTypeID rightType) {
    uint64_t rightWords = valueWordsOf(image, rightType);
    uint64_t leftWords = valueWordsOf(image, leftType);
    const ZamWord *right = valueAt(thread, 0, rightWords);
    const ZamWord *left =
        valueAt(thread, rightWords + ZAMTYPE_ID_WORDS, leftWords);

   ZamPrimitive kind = primitiveOf(image, leftType);
    if (kind == primitiveOf(image, rightType)) {
        switch (kind) {
        // Each order is asked as both strict comparisons rather than as a
        // subtraction, which is what keeps a pair far apart from wrapping into
        // the wrong answer.
        case ZAMPRIMITIVE_INT: {
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
// what says a header is among the words and what OpNew and OpDefaultValue lay
// one out by — and an interface value lies nowhere at all. The two questions
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

// Whether the type answers the type-directed instructions through a method of
// its own rather than by having its words read. Exactly the types that descend
// from std.Object and are reached through an address do: a class or abstract
// pointer, and an interface.
//
// Those are the same two questions a header is read behind, which is why this is
// the condition instanceHeaderOnTop opens with — an instruction that dispatches
// has to find the instance's own table, and finding it is reading its header.
//
// A class held by value is deliberately not one of these. Object's methods take
// an Object* and a value has no address to be one: the semantic pass refuses
// v1.Eq(v2) for that reason, so the runtime must not synthesize the call it
// rejects. What a value answers by instead is its bytes, in valuesEqual.
static bool answersThroughObject(const ZamImage *image, ZamTypeID type) {
    return isReferenceType(image, type) && referenceNamesInstance(image, type);
}

// The header of the value standing on top of the stack, its type id already
// taken off, or NULL where there is none to read.
//
// A header is the only thing that says what a value actually is wherever the id
// it was named by cannot, so this is the one step every instruction that has to
// look past a declared type begins with. What it answers is an address and not a
// type, the two callers wanting different words out of it.
//
// Three things have no header. A type that carries none — a struct, an enum, a
// primitive — answers for itself and never lies about what it is. A run answers
// for one element where what stands here is the whole of it, which is why the
// inline case asks and the reference case does not have to: isReferenceType is
// already false for an array. And a null named no instance at all.
static void *instanceHeaderOnTop(const ZamThread *thread, const ZamImage *image,
                                ZamTypeID type) {
    // One word naming the instance, which is a pointer and also an interface —
    // an instance reached under an interface is still an instance and still
    // carries its own header. The word goes through
    // zam_thread_location_address because a pointer taken of a stack value is a
    // frame location rather than an address.
    if (answersThroughObject(image, type)) {
       ZamWord location = zam_thread_peek(thread, 0);
        return location == 0 ? NULL
                             : zam_thread_location_address(thread, location);
    }

    // The value standing where it is, its header the first of its words. This
    // is the one shape whose address is the stack's own.
    if (isMarkableInstance(image, type) && type.arrayLength == 0)
        return thread->operands + thread->operandCount -
               valueWordsOf(image, type);

    return NULL;
}

// Entering one of std.Object's methods on the receiver standing on top of the
// stack, its type id already taken off. This is how the type-directed
// instructions answer for a value that answers for itself.
//
// It is a jump and not a nested run of the interpreter, which is what the frame
// being the one a compiled call leaves buys: the closure sits at the base with
// the arguments above it, so the base is where the return value lands, and the
// base is exactly where the receiver stood and so where the instruction's own
// result belongs. The caller returns this and the loop carries on into the body
// — see OpCallMethod, which builds the same frame from operations instead.
//
// The return offset is the instruction after this one because step advances the
// program counter before dispatching, so what a case body reads is already
// where a fall-through would go.
static ZamResult enterObjectMethod(ZamThread *thread, const ZamImage *image,
                                  ZamTypeID receiverType, uint64_t slot,
                                   bool hasArgument, ZamWord argument,
                                  ZamTypeID argumentType) {
    // Which table the slot is read out of is the instance's to say; the slot
    // itself is not in question. Object's methods sit at the same index in
    // every object's flattened table — a base's methods are laid out first and
    // an override takes the slot it inherited, which is flattenedInstanceFields
    // and renumberInstanceFields — so a virtual receiver changes the table and
    // nothing else.
    //
    // An interface goes the same way here though OpPushInterfaceMethod exists
    // for it, and for the reason that instruction gives in reverse: a
    // translation is needed where the index is one the interface numbered
    // itself, and these indices are Object's. The concrete class's slot is
    // already known, so what is wanted is its table and not a lookup in it.
   ZamTypeID declared = pointeeOf(image, receiverType);
    if (isVirtuallyTyped(image, declared)) {
        void *instance = instanceHeaderOnTop(thread, image, receiverType);
        if (instance == NULL)
            zam_die(ZAMEXIT_PANIC, "asked a method of a null pointer\n");
        declared.shortID = zam_object_header_of(instance)->shortID;
    }

    pushMethodClosure(thread, image, receiverType,
                      zam_type_method(image, declared.shortID, slot));

    uint64_t frameWords = 1;
    if (hasArgument) {
        zam_thread_push(thread, argument);
        markPushedValue(thread, image, argumentType);
        frameWords++;
    }

    // Read after the pushes rather than before them: the operand stack grows by
    // realloc, so an address into it taken any earlier would name where the
    // base used to be. The closure itself is an allocation and does not move.
    uint64_t base = thread->operandCount - frameWords;
   ZamClosure *closure =
        zam_closure_of((void *)(uintptr_t)thread->operands[base]);

    zam_thread_push_function(thread, thread->instructionOffset, base);
    thread->instructionOffset = closure->instruction;
    return ZAMRESULT_CONTINUE;
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

// Whether a value of the child type may be read as the parent type.
//
// Four questions in one, and which is asked is decided by the types rather than
// by the caller. Most types stand in no line of descent and are their own only
// supertype, so the ids being the same settles it. A class, an abstract or an
// interface descends, and the set its record carries is what says how far. A
// pointer descends by what it points at, and a function by its signature: those
// two are the kinds whose answer is worked out rather than looked up, neither
// standing below any declaration. Two pointers answer before their pointees are
// asked at all, and both are noted where they are read.
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
// is the sweep-list link; OpDefaultValue writes a stack value's, which is
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
        zam_object_header_bytes(zam_mark_word_of(value)->kind);

    uint64_t count = zam_type_member_count(image, type.shortID);
    for (uint64_t index = 0; index < count; index++) {
        const ZamMemberDefinition *member =
            zam_type_member(image, type.shortID, index);
       ZamTypeID memberType = zam_member_type(member);
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
        uint64_t offset = headerBytes + member->offset;
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

// How far past the pointer an instance's members begin, which is what a member
// offset has to be added to.
//
// The offsets the compiler records are net of the header — see layoutOf in
// internal/4_codegen/2_staticfields.go — so that a member sits at the same
// number up and down an inheritance chain whatever the instance in front of it
// turned out to carry. The reader is the side that adds the difference, which is
// what this is.
//
// For a markable instance the width is read out of the instance rather than
// worked out from the type it was named through, that being the point of putting
// the kind in the mark word: an instance reached as an abstract still begins
// with whatever it actually is, tail and all.
static uint64_t memberHeaderBytes(const ZamThread *thread,
                                  const ZamImage *image, ZamTypeID objectType,
                                 ZamWord object) {
    if (!isMarkableInstance(image, objectType)) {
        if (isTailedStruct(image, objectType))
            return ZAMTAILED_STRUCT_HEADER_BYTES;
        // An enum carries a one-word discriminant at the head of every
        // value, so user members live past it. Keep this in step with
        // instanceHeaderByteSizeOf for KindEnum on the compiler side.
        if (zam_image_bucket_of(image, ZAMTYPE_DECL(objectType.shortID)) ==
            ZAMBUCKET_ENUM)
            return ZAMWORD_BYTES;
        return 0;
    }

    void *instance = zam_thread_location_address(thread, object);
    return zam_object_header_bytes(zam_mark_word_of(instance)->kind);
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

// An instruction is one opcode byte followed by however many immediate words
// the opcode carries. Reading one means bounds-checking the whole of it against
// the operations section, since a truncated instruction at the end of the
// program would otherwise be read as whatever lay past it.
typedef struct ZamInstruction {
   ZamOpcode opcode;
    // operands are the immediate words, kept as the bytes they are. An
    // instruction is one opcode byte and then its words, so an operand begins
    // one byte into a word and a pointer to one is aligned only by accident —
    // which makes ZamWord* the one type this cannot be. Forming a misaligned
    // pointer is undefined before it is ever read through, so the bytes stay
    // bytes and operandAt is what turns them into a word.
    const uint8_t *operands;
    // byteCount is how far the program counter moves to reach the next
    // instruction, which is what makes a fall-through a fall-through.
    uint64_t byteCount;
} ZamInstruction;

// The immediate word at a position, read out of bytes that do not lie where a
// word lies.
//
// memcpy is how an unaligned word is read in C without lying to the compiler
// about the alignment: it is defined for any address, and every target this is
// built for lowers eight bytes of it to the single load the cast used to be.
// What it is not is a second interpretation of the bytes — the image is
// little-endian and read in place, which the runtime already requires, so
// these are the same bytes in the same order the load took.
static ZamWord operandAt(ZamInstruction instruction, uint64_t index) {
   ZamWord word;
    memcpy(&word, instruction.operands + index * ZAMWORD_BYTES, sizeof word);
    return word;
}

static ZamInstruction fetch(ZamThread *thread) {
    const ZamImage *image = &thread->vm->image;
    uint64_t offset = thread->instructionOffset;

    if (offset >= image->operationsByteCount)
        zam_die(ZAMEXIT_PANIC,
                "ran past the end of the program: offset %llu of %zu\n",
                (unsigned long long)offset, image->operationsByteCount);

   ZamOpcode opcode = (ZamOpcode)image->operations[offset];
    if (opcode >= ZAMOPCODE_COUNT)
        zam_die(ZAMEXIT_PANIC, "byte %u at offset %llu is not an opcode\n",
                image->operations[offset], (unsigned long long)offset);

    uint64_t byteCount = 1 + (uint64_t)zam_opcode_operands(opcode) * ZAMWORD_BYTES;
    if (offset + byteCount > image->operationsByteCount)
        zam_die(ZAMEXIT_PANIC,
                "%s at offset %llu runs past the end of the program\n",
                zam_opcode_name(opcode), (unsigned long long)offset);

    return (ZamInstruction){
        .opcode = opcode,
        .operands = image->operations + offset + 1,
        .byteCount = byteCount,
    };
}

// step runs one instruction. The program counter is advanced before the
// instruction rather than after it, so that a jump can simply write it: an
// instruction that sets where to go next would otherwise have that overwritten
// by the advance.
static ZamResult step(ZamThread *thread) {
   ZamInstruction instruction = fetch(thread);
    thread->instructionOffset += instruction.byteCount;

    switch (instruction.opcode) {
    // OpExit is the one instruction implemented here, because it is the loop's
    // own ending rather than anything the program computes: it is what makes
    // the loop terminate, so the loop cannot be said to have a shape without
    // it. A status is a byte to whoever started the program, so what survives
    // is the low eight bits of the word.
    case OpExit:
        thread->exitStatus = (uint8_t)zam_thread_pop(thread);
        return ZAMRESULT_EXIT;

    // A literal is the one value that needs nothing but itself: the word is in
    // the instruction, and every type narrower than a word was widened to one
    // by whoever emitted it.
    case OpPushI64:
        zam_thread_push(thread, operandAt(instruction, 0));
        return ZAMRESULT_CONTINUE;

    // The offset is a depth from the top, so zero duplicates the topmost word.
    // The copy owns no mark entry: what this is for is spending a word twice,
    // once to test and once to keep, and the entry the original owns goes on
    // naming the original.
    case OpDupWord:
        zam_thread_push(thread,
                        zam_thread_peek(thread, operandAt(instruction, 0)));
        return ZAMRESULT_CONTINUE;

    case OpIsNegativeI64:
        zam_thread_push(thread,
                        boolWord((ZamInt)zam_thread_pop(thread) < 0));
        return ZAMRESULT_CONTINUE;

    case OpIsPositiveI64:
        zam_thread_push(thread,
                        boolWord((ZamInt)zam_thread_pop(thread) > 0));
        return ZAMRESULT_CONTINUE;

    case OpArithmeticNegationI64:
        zam_thread_push(thread, (ZamWord)0 - zam_thread_pop(thread));
        return ZAMRESULT_CONTINUE;

    case OpBitwiseNegationI64:
        zam_thread_push(thread, ~zam_thread_pop(thread));
        return ZAMRESULT_CONTINUE;

    case OpAddI64:
    case OpSubI64:
        offsetWord(thread, instruction.opcode);
        return ZAMRESULT_CONTINUE;

    case OpMulI64:
    case OpDivI64:
    case OpModI64:
    case OpAndI64:
    case OpOrI64:
    case OpXorI64:
    case OpEqualI64:
    case OpShiftLeftI64:
    case OpShiftRightI64:
    case OpShiftArithmeticRightI64:
        zam_thread_push(thread, integerBinary(thread, instruction.opcode));
        return ZAMRESULT_CONTINUE;

    case OpNegF64:
        zam_thread_push(thread,
                        wordOfFloat(-floatOf(zam_thread_pop(thread))));
        return ZAMRESULT_CONTINUE;

    case OpCastI64ToF64:
        zam_thread_push(
            thread, wordOfFloat((ZamFloat)(ZamInt)zam_thread_pop(thread)));
        return ZAMRESULT_CONTINUE;

    // A float too large for an integer, and a NaN, have no value to become.
    // What they become is left undefined rather than checked for, the way a
    // shift past the width is.
    case OpCastF64ToI64:
        zam_thread_push(thread,
                        (ZamWord)(ZamInt)floatOf(zam_thread_pop(thread)));
        return ZAMRESULT_CONTINUE;

    case OpAddF64:
    case OpSubF64:
    case OpMulF64:
    case OpDivF64:
    case OpEqualF64:
        zam_thread_push(thread, floatBinary(thread, instruction.opcode));
        return ZAMRESULT_CONTINUE;

    // The two values stand with an id over each, the left pair beneath the
    // right, so the right id comes off first and the left is then read at the
    // depth the right value leaves it.
    case OpEq: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID rightType = zam_thread_pop_type(thread);
        uint64_t rightWords = valueWordsOf(image, rightType);
       ZamTypeID leftType = zam_thread_peek_type(thread, rightWords);

        bool equal;
        if (answersThroughObject(image, leftType) &&
            answersThroughObject(image, rightType)) {
           ZamWord right = zam_thread_peek(thread, 0);
           ZamWord left = zam_thread_peek(thread, rightWords + ZAMTYPE_ID_WORDS);

            // Two settled before any call is made. The same instance is equal
            // to itself whatever its Eq would say, which is the case the
            // instruction's own documentation says answers first. And a null is
            // not an instance, so it has nothing to answer with — which is the
            // whole reason to settle it here rather than enter a body whose
            // `this` would be nothing.
            if (left == right)
                equal = true;
            else if (left == 0 || right == 0)
                equal = false;
            else {
                // The right value becomes the argument and the left the
                // receiver, so the only thing spent here is the id between
                // them. The frame is built over the receiver, which is where
                // this instruction's answer belongs, so the call leaves it
                // there itself.
                popValue(thread, image, rightType);
                zam_thread_pop_type(thread);
                return enterObjectMethod(thread, image, leftType,
                                         ZAMOBJECT_SLOT_EQ, true, right,
                                         rightType);
            }
        } else {
            // Everything else is walked, and the walk is bracketed by a set of
            // its own. The set is what makes a graph that leads back to itself
            // terminate, and opening it here rather than inside the walk is
            // what keeps it to one descent: a pair recorded by this comparison
            // says nothing about any other, and a hand-written Eq this walk
            // dispatches into is a body like any other and may compare whatever
            // it likes.
            //
            // The dispatching pair above opens none. It leaves this instruction
            // rather than descending, so there is nothing yet visited for a set
            // to hold and nothing left running to ask it.
            uint64_t leftWords = valueWordsOf(image, leftType);
           ZamWord rightLocation = locationAt(thread, 0, rightWords);
           ZamWord leftLocation =
                locationAt(thread, rightWords + ZAMTYPE_ID_WORDS, leftWords);

            zam_thread_push_visited(thread);
           ZamResult result = equalValues(thread, leftType, leftLocation,
                                           rightType, rightLocation, &equal);
            zam_thread_pop_visited(thread);

            // The operands are left standing where a walk did not finish, which
            // costs nothing: every result but CONTINUE ends the thread, so there
            // is no later instruction for the stack to be wrong for.
            if (result != ZAMRESULT_CONTINUE)
                return result;
        }

        // Both pairs are spent, the id between them along with them: an
        // equality answers a question about the values and keeps neither.
        popValue(thread, image, rightType);
        zam_thread_pop_type(thread);
        popValue(thread, image, leftType);

        zam_thread_push(thread, boolWord(equal));
        return ZAMRESULT_CONTINUE;
    }

    // The same shape as OpEq, and spends the same: an order answers a question
    // about the values and keeps neither of them.
    case OpCmp: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID rightType = zam_thread_pop_type(thread);
        uint64_t rightWords = valueWordsOf(image, rightType);
       ZamTypeID leftType = zam_thread_peek_type(thread, rightWords);

       ZamInt order;
        if (answersThroughObject(image, leftType) &&
            answersThroughObject(image, rightType)) {
           ZamWord right = zam_thread_peek(thread, 0);
           ZamWord left = zam_thread_peek(thread, rightWords + ZAMTYPE_ID_WORDS);

            // The same two an equality settles before calling anything, for the
            // same reasons: an instance does not come before or after itself,
            // and a null has no Cmp to ask. Where one of them is null the
            // addresses are all there is to order by, which puts the null first.
            if (left == right)
                order = 0;
            else if (left == 0 || right == 0)
                order = (left > right) - (left < right);
            else {
                popValue(thread, image, rightType);
                zam_thread_pop_type(thread);
                return enterObjectMethod(thread, image, leftType,
                                         ZAMOBJECT_SLOT_CMP, true, right,
                                         rightType);
            }
        } else {
            order = valuesOrder(thread, image, leftType, rightType);
        }

        popValue(thread, image, rightType);
        zam_thread_pop_type(thread);
        popValue(thread, image, leftType);

        zam_thread_push(thread, (ZamWord)order);
        return ZAMRESULT_CONTINUE;
    }

    // A width is asked of the definition and of nothing else, which is what
    // separates this from the type-directed instructions below it: there is no
    // value under the id to read a kind off and no branch on what the type turned
    // out to be, an array being folded into the answer by zam_type_bytes the same
    // way OpStore and OpLoad already have it fold one.
    //
    // Asked at all only where the width could not be had before the run — the
    // stride of a pointer whose pointee is an open generic argument, which is
    // emitPointerStride in 3_operation.go. A width settled at compile time is
    // pushed as the constant it is.
    case OpSizeOf: {
       ZamTypeID type = zam_thread_pop_type(thread);
        zam_thread_push(thread, zam_type_bytes(&thread->vm->image, type));
        return ZAMRESULT_CONTINUE;
    }

    // These are told a type id and left to choose what to do by it, which is
    // what lets one body serve every instantiation of a template. Each needs
    // the type records read before it can dispatch on anything.
    //
    // Where a declaration answers for itself the three go the same way: the id
    // is spent, the value beneath it is the receiver, and the frame is built
    // over the word the receiver stood on — which is where each of these
    // instructions leaves its own answer, so the return lands it there without
    // any of them having to place it. Str and Hash each return a value and a
    // Mark returns nothing, which is the difference between the two documented
    // stack effects and is all of it.
    case OpCastToString: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);

        // An any defers to the type its payload was boxed as: the string a
        // user wants is the string of what the box holds, not the address of
        // the box. The payload is read onto the stack as its real type and
        // the rest of this handler then runs over that value as it would
        // over any other. The loop handles a box of a box, which is a shape
        // the type system allows and nothing in the runtime should refuse to
        // walk down.
        while (ZAMTYPE_DECL(type.shortID) == ZAMPRIMITIVE_ANY) {
            void *anyBox = (void *)(uintptr_t)zam_thread_pop(thread);
            zam_thread_pop_mark(thread, thread->operandCount);
            if (anyBox == NULL)
                zam_die(ZAMEXIT_PANIC, "string cast of a null any\n");

            uint64_t typedHeader =
                zam_object_header_bytes(ZAMOBJECT_INSTANCE);
            const ZamWord *anyBody =
                (const ZamWord *)((const uint8_t *)anyBox + typedHeader);
           ZamTypeID payloadType = zam_type_id_read(anyBody);
            void *payloadBox = (void *)(uintptr_t)anyBody[3];
            if (payloadBox == NULL)
                zam_die(ZAMEXIT_PANIC,
                        "string cast of an any with no payload\n");

            uint64_t payloadWords = valueWordsOf(image, payloadType);
            uint64_t inlineHeader =
                inlineValueHeaderBytes(image, payloadType);
            uint64_t bodyBytes =
                payloadWords * ZAMWORD_BYTES - inlineHeader;
            for (uint64_t word = 0; word < payloadWords; word++)
                zam_thread_push(thread, 0);
            uint8_t *destination =
                (uint8_t *)(thread->operands + thread->operandCount -
                            payloadWords);
            if (isMarkableInstance(image, payloadType)) {
                *zam_mark_word_of(destination) = (ZamMarkWord){
                    .state = ZAMMARK_STATE_WHITE,
                    .kind = ZAMOBJECT_INSTANCE,
                    .contained = 1,
                    .upvalue = 0,
                };
                *zam_object_header_of(destination) = (ZamObjectHeader){
                    .shortID = payloadType.shortID,
                    .elementCount = 1,
                };
            }
            memcpy(destination + inlineHeader,
                   (const uint8_t *)payloadBox + typedHeader, bodyBytes);
            markPushedValue(thread, image, payloadType);
            type = payloadType;
        }

        if (answersThroughObject(image, type))
            return enterObjectMethod(thread, image, type, ZAMOBJECT_SLOT_STR,
                                     false, 0, type);

        // A pointer to an enum or struct is written out by the Str every one
        // of them has first among its methods. A null one names nothing to
        // ask, so it is written out as the address it is.
        if (namesValueWithStr(image, type) && zam_thread_peek(thread, 0) != 0)
            return enterObjectMethod(thread, image, type, ZAMVALUE_SLOT_STR,
                                     false, 0, type);

        // A primitive reads as what its word means — a number as the number,
        // a bool as true or false, a char as the character it numbers, a
        // string as itself. The alternatives were a per-type Str method the
        // image would have to carry an entry for, which a primitive has no
        // definition to hold, or the generic fallback below, which reads a
        // value's bytes as an unknown blob — right in no case.
       ZamPrimitive primitive = primitiveOf(image, type);
        if (primitive != ZAMPRIMITIVE_COUNT) {
            char text[64];
            int length = 0;
            switch (primitive) {
            case ZAMPRIMITIVE_VOID:
            case ZAMPRIMITIVE_TYPE:
            case ZAMPRIMITIVE_ANY:
            case ZAMPRIMITIVE_COUNT:
                // Void has nothing to read, type is three words that would
                // need the image to name a declaration, and any was already
                // peeled by the loop above. All three fall through to the
                // generic form below for lack of a shorter one.
                break;

            case ZAMPRIMITIVE_BOOL: {
               ZamWord word = zam_thread_pop(thread);
                length = snprintf(text, sizeof text,
                                  word ? "true" : "false");
                break;
            }
            case ZAMPRIMITIVE_CHAR: {
               ZamWord word = zam_thread_pop(thread);
                uint32_t cp = (uint32_t)word;
                if (cp < 0x80) {
                    text[0] = (char)cp;
                    length = 1;
                } else if (cp < 0x800) {
                    text[0] = (char)(0xC0 | (cp >> 6));
                    text[1] = (char)(0x80 | (cp & 0x3F));
                    length = 2;
                } else if (cp < 0x10000) {
                    text[0] = (char)(0xE0 | (cp >> 12));
                    text[1] = (char)(0x80 | ((cp >> 6) & 0x3F));
                    text[2] = (char)(0x80 | (cp & 0x3F));
                    length = 3;
                } else {
                    text[0] = (char)(0xF0 | (cp >> 18));
                    text[1] = (char)(0x80 | ((cp >> 12) & 0x3F));
                    text[2] = (char)(0x80 | ((cp >> 6) & 0x3F));
                    text[3] = (char)(0x80 | (cp & 0x3F));
                    length = 4;
                }
                break;
            }
            case ZAMPRIMITIVE_BYTE: {
               ZamWord word = zam_thread_pop(thread);
                length = snprintf(text, sizeof text, "%u",
                                  (unsigned)(uint8_t)word);
                break;
            }
            case ZAMPRIMITIVE_INT: {
               ZamInt value = (ZamInt)zam_thread_pop(thread);
                length = snprintf(text, sizeof text, "%lld",
                                  (long long)value);
                break;
            }
            case ZAMPRIMITIVE_UINT: {
               ZamUint value = (ZamUint)zam_thread_pop(thread);
                length = snprintf(text, sizeof text, "%llu",
                                  (unsigned long long)value);
                break;
            }
            case ZAMPRIMITIVE_FLOAT: {
               ZamFloat value = floatOf(zam_thread_pop(thread));
                length = snprintf(text, sizeof text, "%g", value);
                break;
            }
            case ZAMPRIMITIVE_STRING: {
                // A string is already a string — the pointer stands and its
                // mark entry with it, and nothing is allocated.
                return ZAMRESULT_CONTINUE;
            }
            }

            if (length > 0) {
               ZamString *string = newString(thread, (uint64_t)length);
                memcpy(string->bytes, text, (size_t)length);
                zam_thread_push(thread, (ZamWord)(uintptr_t)string);
                zam_thread_push_mark(thread, thread->operandCount - 1);
                return ZAMRESULT_CONTINUE;
            }
            // Otherwise fall through to the generic form.
        }

        // The type it was named by, where it stands, and the hash of the bytes
        // that are it. Nothing in the image names a declaration, so the id is
        // written as the three numbers it is rather than as the name it was
        // declared under; and a value laid out inline has no address of its
        // own, so the one written is the stack's. A reference is one word
        // naming an allocation elsewhere, so the words on the stack are a run
        // of one and the hash and the address are of that one word.
        //
        // An inline value's header is where it lies rather than what it holds,
        // and so is skipped, the same way inlineValueBytes skips it: the
        // address and the hash should name what the value is, not what the
        // storage happens to have put in front of it.
        //
        // Built before anything is allocated, the bytes being read through a
        // pointer into the operand stack.
        uint64_t words = valueWordsOf(image, type);
        const uint8_t *bytes = (const uint8_t *)valueAt(thread, 0, words);
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

        popValue(thread, image, type);
        zam_thread_push(thread, (ZamWord)(uintptr_t)string);
        zam_thread_push_mark(thread, thread->operandCount - 1);
        return ZAMRESULT_CONTINUE;
    }
    case OpHash: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);

        // An any delegates its hash to the payload it holds, so a boxed int
        // hashes as the int and a boxed Foo* hashes through Foo.Hash. The
        // alternative — hashing the box pointer the stack holds — would put
        // two anys with equal payloads in different buckets, which is the one
        // guarantee hash owes equality. The payload's own Hash is reached
        // through enterObjectMethod for a reference, and over the stored body
        // with fnv1a for everything else, which is the same two-branch shape
        // the main OpHash body is.
        if (ZAMTYPE_DECL(type.shortID) == ZAMPRIMITIVE_ANY) {
            void *anyBox = (void *)(uintptr_t)zam_thread_pop(thread);
            zam_thread_pop_mark(thread, thread->operandCount);
            if (anyBox == NULL)
                zam_die(ZAMEXIT_PANIC, "hash of a null any\n");

            uint64_t typedHeader =
                zam_object_header_bytes(ZAMOBJECT_INSTANCE);
            const ZamWord *anyBody =
                (const ZamWord *)((const uint8_t *)anyBox + typedHeader);
           ZamTypeID stored = zam_type_id_read(anyBody);
            void *payloadBox = (void *)(uintptr_t)anyBody[3];
            if (payloadBox == NULL)
                zam_die(ZAMEXIT_PANIC, "hash of an any with no payload\n");

            if (answersThroughObject(image, stored)) {
                // The payload box's body is the one-word pointer the stored
                // id names, read here and pushed back as a receiver so the
                // body's own Hash runs over it. markPushedValue puts the mark
                // entry back on the pointer, which the one popped off the any
                // owned.
               ZamWord payloadPtr = *(const ZamWord *)(
                    (const uint8_t *)payloadBox + typedHeader);
                zam_thread_push(thread, payloadPtr);
                markPushedValue(thread, image, stored);
                return enterObjectMethod(thread, image, stored,
                                         ZAMOBJECT_SLOT_HASH, false, 0,
                                         stored);
            }

            uint64_t byteCount =
                zam_type_bytes(image, stored) -
                inlineValueHeaderBytes(image, stored);
            const uint8_t *bytes =
                (const uint8_t *)payloadBox + typedHeader;
            zam_thread_push(thread, fnv1a(bytes, byteCount));
            return ZAMRESULT_CONTINUE;
        }

        if (answersThroughObject(image, type))
            return enterObjectMethod(thread, image, type, ZAMOBJECT_SLOT_HASH,
                                     false, 0, type);

        // Over the run valuesEqual compares, which is what makes equal values
        // hash alike: a byte covered by one and not the other would be a byte
        // that put two equal values in different buckets. A reference is one
        // word naming an allocation elsewhere, and that word is the whole of
        // what equality has for it, so it is also the whole of what the hash
        // reads.
        //
        // An inline value's header is where it lies rather than what it holds,
        // and so is skipped the same way inlineValueBytes skips it and
        // valuesEqual's walk steps past it, which is what keeps equal values
        // in the same bucket.
        uint64_t words = valueWordsOf(image, type);
        const uint8_t *bytes = (const uint8_t *)valueAt(thread, 0, words);
        uint64_t byteCount = zam_type_bytes(image, type);
        if (isInlineValue(image, type)) {
            uint64_t header = inlineValueHeaderBytes(image, type);
            bytes += header;
            byteCount -= header;
        }

       ZamWord hash = fnv1a(bytes, byteCount);
        popValue(thread, image, type);
        zam_thread_push(thread, hash);
        return ZAMRESULT_CONTINUE;
    }
    // mark(x) says x is reachable, and means something only while the thread
    // is collecting, which is when a Mark body runs. A reference is queued for
    // the collector to reach in its turn, and a value laid out inline is
    // walked where it stands. Outside a collection the value is only spent.
    case OpMark: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
        if (zam_gc_collecting(thread)) {
            uint64_t front = thread->operandCount - valueWordsOf(image, type);
            zam_gc_mark_value(thread, zam_frame_location(front * ZAMWORD_BYTES),
                              type);
        }
        popValue(thread, image, type);
        return ZAMRESULT_CONTINUE;
    }
    // What the value is, as against what the source was able to see it as. The
    // two can differ only where the value carries a word saying which type it
    // was made under, so that word is the whole of the answer wherever there is
    // one and the id in hand is the answer wherever there is not.
    case OpTypeOf: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID declared = zam_thread_pop_type(thread);
       ZamTypeID actual = declared;

        // A null had nothing to answer for itself and neither does anything
        // headerless, so both are left with the id they were named by. Read
        // before the value goes, the interior shape's header being on the very
        // words about to be spent.
        void *instance = instanceHeaderOnTop(thread, image, declared);
        if (instance != NULL) {
            actual.shortID = zam_object_header_of(instance)->shortID;
            actual.tailLength =
                zam_mark_word_of(instance)->kind == ZAMOBJECT_TAILED_INSTANCE
                    ? zam_object_tail_length_of(instance)
                    : 0;
        }

        popValue(thread, image, declared);
        zam_thread_push_type(thread, actual);
        return ZAMRESULT_CONTINUE;
    }

    // Whether a value of the type beneath may be read as the type above it. Both
    // operands are whole ids and what is left in their place is the one boolean,
    // so nothing here is spent that the answer is then read out of.
    case OpIsSubtypeOf: {
       ZamTypeID parent = zam_thread_pop_type(thread);
       ZamTypeID child = zam_thread_pop_type(thread);
        zam_thread_push(thread,
                        boolWord(typeIsSubtypeOf(&thread->vm->image, child,
                                                 parent)));
        return ZAMRESULT_CONTINUE;
    }

    // The program counter was advanced before the switch, so a branch is a
    // write and a fall-through is nothing. The target is not checked here: an
    // offset that names no instruction is caught by the fetch that follows,
    // which is where a target past the end of the program would land anyway.
    case OpJump:
        thread->instructionOffset = operandAt(instruction, 0);
        return ZAMRESULT_CONTINUE;

    case OpJumpIfFalse:
        if (zam_thread_pop(thread) == 0)
            thread->instructionOffset = operandAt(instruction, 0);
        return ZAMRESULT_CONTINUE;

    case OpPopWord:
        popWord(thread);
        return ZAMRESULT_CONTINUE;

    // A discard, told the type only so that it knows how much there is to let
    // go of. Every word of the value goes, and every mark entry those words
    // owned goes with them.
    case OpPop: {
        const ZamImage *image = &thread->vm->image;
        popValue(thread, image, zam_thread_pop_type(thread));
        return ZAMRESULT_CONTINUE;
    }

    // The entry goes and the word stays, which is what handing a reference to
    // something that will keep it reachable by other means comes to. The peek
    // is what catches an empty stack: without it an underflow would read as a
    // search for an entry that no word owns and find nothing to complain about.
    case OpPopMark:
        (void)zam_thread_peek(thread, 0);
        zam_thread_pop_mark(thread, thread->operandCount - 1);
        return ZAMRESULT_CONTINUE;

    case OpPushScope:
        zam_thread_push_scope(thread);
        return ZAMRESULT_CONTINUE;

    case OpPopScope:
        zam_thread_pop_scope(thread);
        return ZAMRESULT_CONTINUE;

    // The value is read where it stands, the frame is let go of, and the value
    // is then moved down into the slot the frame began at. Popping the frame
    // rewinds the count past the value but writes nothing, so the words are
    // still where they were and the move is what makes them the caller's.
    case OpReturn: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
        uint64_t words = valueWordsOf(image, type);
        if (words > thread->operandCount)
            zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
        uint64_t from = thread->operandCount - words;

       ZamMarkFunction frame = zam_thread_pop_function(thread);
        if (frame.operandPop > from)
            zam_die(ZAMEXIT_PANIC,
                    "returned a %llu word value from beneath its own frame\n",
                    (unsigned long long)words);

        memmove(thread->operands + frame.operandPop, thread->operands + from,
                words * ZAMWORD_BYTES);
        thread->operandCount = frame.operandPop + words;
        markPushedValue(thread, image, type);

        thread->instructionOffset = frame.returnInstruction;

        // The top-level frame of a spawned thread is pushed by OpNewThread and
        // its return instruction is unread, this leaving instead. The value
        // stays on the stack where a join can read it. Main is run without a
        // function entry, so its OpReturn panics in pop_function above, which
        // is the one place that invariant is checked.
        if (thread->functionEntry == ZAMMARK_NONE && thread != &thread->vm->main)
            return ZAMRESULT_RETURNED;
        return ZAMRESULT_CONTINUE;
    }

    case OpReturnVoid: {
       ZamMarkFunction frame = zam_thread_pop_function(thread);
        thread->instructionOffset = frame.returnInstruction;
        if (thread->functionEntry == ZAMMARK_NONE && thread != &thread->vm->main)
            return ZAMRESULT_RETURNED;
        return ZAMRESULT_CONTINUE;
    }

    // Only the form that arrives as an address is answered. The instruction is
    // documented as leaving whichever of the two it was handed, but the same
    // type id is pushed over an object either way, so which one it was is not
    // in the operands to read — and the words form is a value with no home,
    // which is the case a member of one is not an assignment target in.
    case OpPushMember: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID objectType = pointeeOf(image, zam_thread_pop_type(thread));
       ZamWord object = zam_thread_pop(thread);
        uint64_t distance = 0;
        bool reachesObject =
            zam_thread_pop_object_mark(thread, thread->operandCount, &distance);
        if (object == 0)
            zam_die(ZAMEXIT_PANIC, "selected a member of a null pointer\n");

        const ZamMemberDefinition *member =
            zam_type_member(image, objectType.shortID, operandAt(instruction, 0));

        // An offset is added to a frame location and to an address the same
        // way, the tag riding along untouched: what it says is only which of
        // the two the sum is still to be read as. The header the offset is net
        // of is added first, and is the instance's own rather than the type's.
       ZamWord location = object +
                           memberHeaderBytes(thread, image, objectType, object) +
                           member->offset;
        // A member of an object keeps the object alive for as long as its
        // location stands here, the way the pointer it was selected from did:
        // a store into it may allocate before it lands.
        zam_thread_push(thread, location);
        if (reachesObject && !zam_is_frame_location(location))
            zam_thread_push_interior_mark(thread, thread->operandCount - 1,
                                          distance + (location - object));

        // The array length the record carries is passed through, so an array
        // member's location is the whole run and a load or store through it moves
        // all of it. Indexing does not read the length and does not need it
        // cleared: a sum over an array is emitted as the location's address with
        // these words let go of, which is emitArrayBase in 3_operation.go.
        //
        // The tail member is the one record whose length is zero and is not the
        // whole run: a declaration like `var char[~] data` stands for every
        // instantiation at every length, and the length lives in the enclosing
        // and not in the record. For a value as a whole — comparison, hash,
        // rendering — the length matters, and OpLoadTailMember is what reaches
        // it. The lvalue and base-address paths that OpPushMember feeds —
        // store, index — see one element here all the same, which is the
        // length the record has, so they are unaffected.
        zam_thread_push_type(thread, zam_member_type(member));
        return ZAMRESULT_CONTINUE;
    }

    // OpLoadTailMember is a tail selection as a whole value: the sized run its
    // length names, not the one element the record has. It is OpPushMember and
    // OpLoad folded together — the length is the enclosing's to answer, and
    // keeping the enclosing until the load means the load has what the compiler
    // emitted after it cannot reach.
    //
    // The length comes from the enclosing in one of two shapes: baked into its
    // type id where the value is sized, or written into the one-word header a
    // tailed struct carries ahead of its members where the value is reached
    // through a pointer and the id has no length to say. Both forms land here as
    // one word for the enclosing and one type id above it, which is what makes
    // this one instruction and not two.
    case OpLoadTailMember: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID objectType = pointeeOf(image, zam_thread_pop_type(thread));
       ZamWord object = zam_thread_pop(thread);
        zam_thread_pop_mark(thread, thread->operandCount);
        if (object == 0)
            zam_die(ZAMEXIT_PANIC, "loaded a tail member of a null pointer\n");

        const ZamMemberDefinition *member =
            zam_type_member(image, objectType.shortID, operandAt(instruction, 0));

        uint64_t tailLength;
        if (objectType.tailLength != 0) {
            tailLength = objectType.tailLength;
        } else {
           ZamWord headerWord;
            memcpy(&headerWord, zam_thread_location_address(thread, object),
                   sizeof headerWord);
            tailLength = headerWord;
        }
       ZamWord location = object +
                           memberHeaderBytes(thread, image, objectType, object) +
                           member->offset;
       ZamTypeID valueType = zam_member_type(member);
        valueType.arrayLength = tailLength;

        pushValueFrom(thread, image, valueType, location);
        zam_thread_push_type(thread, valueType);
        return ZAMRESULT_CONTINUE;
    }

    // A method and the object it was selected on, made into a closure of no
    // captures: the object becomes the receiver entry, written under the type
    // id it was pushed with, which is the only thing that says whether it was a
    // pointer or a value and how wide the value was.
    case OpPushMethod: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID objectType = zam_thread_pop_type(thread);
       ZamTypeID declared = pointeeOf(image, objectType);

        // The index stands for a slot in the flattened table, and against an
        // abstract the slot to read is the one on the class the instance
        // actually is. The number does not change on the way down: a base's
        // methods are laid out ahead of the ones a declaration adds and an
        // override takes the slot it inherited rather than a new one — see
        // flattenedInstanceFields and renumberInstanceFields — so what differs
        // between the two is only which table the index is read out of.
        //
        // Which that is, is what the instance's header says and the one thing
        // that does. Nothing else here could: the receiver arrived under the
        // type the source wrote, and that type is the whole of what would
        // otherwise be known about it.
        //
        // An interface is virtually typed too and does not reach this. Its
        // numbering is its own and means nothing to whatever implements it, so
        // the index has to be translated through the implementor's dispatch
        // table instead — which is a different instruction, methodOpcode in
        // 3_operation.go sending one to OpPushInterfaceMethod.
        if (isVirtuallyTyped(image, declared)) {
            void *instance = instanceHeaderOnTop(thread, image, objectType);
            // The deref the selection asked for. An abstract is reached through
            // a pointer, so this is the same null every other deref answers the
            // same way, and the same one OpPushMember answers just above.
            if (instance == NULL)
                zam_die(ZAMEXIT_PANIC, "selected a method of a null pointer\n");

            // Only the id changes. A tail length belongs to the instance and
            // came down with the receiver, and an abstract-typed receiver is
            // never a run — the index above it names one method of one value.
            declared.shortID = zam_object_header_of(instance)->shortID;
        }

        pushMethodClosure(
            thread, image, objectType,
            zam_type_method(image, declared.shortID, operandAt(instruction, 0)));
        return ZAMRESULT_CONTINUE;
    }

    // The same selection through the one type that numbers its methods its own
    // way. An interface says nothing about what implements it, so unlike an
    // abstract's the index does not survive the step to the class: it names a
    // slot in the interface's own table and has to be translated into a slot in
    // the class's, which is what the class carries a dispatch table for.
    //
    // Which interface is asked comes up the stack rather than in an operand. A
    // method body is compiled once for a whole template, so inside ArrList[E]
    // the id of a List[E] is not known until the receiver says which
    // instantiation is running — see OpPushInterfaceMethod in 3_operation.go.
    case OpPushInterfaceMethod: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID receiverType = zam_thread_pop_type(thread);

        // The key is the interface's own id and the receiver is written as a
        // pointer to one, so the pointer is stepped through to reach the key
        // the dispatch table was built over. The receiver keeps the id it
        // arrived under, that being what says how wide the word is.
       ZamTypeID interfaceType = pointeeOf(image, receiverType);

        // There is no value form of this receiver, which is what makes the
        // whole instruction narrower than OpPushMethod: an interface is laid
        // out nowhere, so the one word standing here names the instance and the
        // instance's header names its class.
        void *instance = instanceHeaderOnTop(thread, image, receiverType);
        if (instance == NULL)
            zam_die(ZAMEXIT_PANIC, "selected a method of a null pointer\n");
       ZamTypeID concrete = {
            .shortID = zam_object_header_of(instance)->shortID};

        // The table is the class's and the key is the interface's, which is
        // what makes this a translation and not a lookup: one interface method
        // is a different slot on every class implementing it.
        const ZamPerfectHash *dispatch = dispatchOf(image, concrete);
        uint32_t index = 0;
        if (dispatch == NULL ||
            !zam_dispatch_lookup(dispatch, interfaceType.shortID,
                                 (uint32_t)operandAt(instruction, 0), &index))
            zam_die(ZAMEXIT_PANIC,
                    "type %u implements no method %llu of interface %u\n",
                    concrete.shortID,
                    (unsigned long long)operandAt(instruction, 0),
                    interfaceType.shortID);

        pushMethodClosure(thread, image, receiverType,
                          zam_type_method(image, concrete.shortID, index));
        return ZAMRESULT_CONTINUE;
    }

    // The frame is already standing: the closure sits at its base with the
    // arguments above it, so opening the call is a matter of saying where the
    // base is and going to the body.
    //
    // The slot is left holding the closure, whichever of the three kinds it is.
    // That is what roots it: a mark entry names an operand offset and not an
    // address — see ZamMarkObject in thread.h — so the entry OpPushMethod pushed
    // keeps the closure only for as long as the closure is the word in the slot.
    // A receiver written over it would leave the allocation the receiver lives
    // in named by nothing, which for a value method closure is the receiver's
    // own home.
    //
    // What the body wanted out of the slot it asks OpPushReceiver for instead,
    // that being the one step whose length the kind decides.
    case OpCallMethod: {
        uint64_t frameWords = operandAt(instruction, 0);
        if (frameWords == 0 || frameWords > thread->operandCount)
            zam_die(ZAMEXIT_PANIC,
                    "a frame of %llu words under a stack of %llu\n",
                    (unsigned long long)frameWords,
                    (unsigned long long)thread->operandCount);
        uint64_t base = thread->operandCount - frameWords;

        void *object = (void *)(uintptr_t)thread->operands[base];
        if (object == NULL)
            zam_die(ZAMEXIT_PANIC, "called a null function\n");
       ZamClosure *closure = zam_closure_of(object);

        zam_thread_push_function(thread, thread->instructionOffset, base);
        thread->instructionOffset = closure->instruction;

        // The closure's captures land past the arguments, where the body was
        // compiled to read them: slot 0 is the closure, the parameters follow,
        // and the captures follow those. They are copied by value, which is
        // what the closure already holds for each one — a reference is one
        // word pointing at an allocation elsewhere, and a value is held entire.
        if (closure->captureCount != 0) {
            const ZamImage *image = &thread->vm->image;
            const ZamWord *entry = closureCaptures(image, closure);
            for (uint64_t i = 0; i < closure->captureCount; i++) {
               ZamTypeID capType = zam_closure_entry_type(entry);
                const ZamWord *capValue = zam_closure_entry_value(entry);
                uint64_t capWords = valueWordsOf(image, capType);
                for (uint64_t w = 0; w < capWords; w++)
                    zam_thread_push(thread, capValue[w]);
                markPushedValue(thread, image, capType);
                entry = zam_closure_entry_next(entry, capWords);
            }
        }
        return ZAMRESULT_CONTINUE;
    }

    // A closure is its body's offset, its captures, and the receiver of the
    // frame it was written in. The type id beneath the captures names the
    // function type it satisfies and is what the typed header is filled from.
    case OpNewLambda: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
        uint64_t captureCount = operandAt(instruction, 1);

        // The captures lie beneath this instruction's own type id, each as a
        // (location, type) pair the compiler built — see VisitExprLambda in
        // 3_operation.go. They are read without popping first so the
        // allocation is sized from them, and popped once the body has been
        // filled in.
        //
        // Capture 0 was pushed first and sits lowest. Capture i's type id tops
        // out i * (ZAMTYPE_ID_WORDS + 1) words down from capture (i+1), so
        // the topmost capture — the last — is at depth zero and the first at
        // depth (captureCount - 1) * (ZAMTYPE_ID_WORDS + 1).
        uint64_t captureSlotWords = ZAMTYPE_ID_WORDS + 1;

        // A lambda inherits the receiver of the frame it was written in. In a
        // body entered through a method closure the receiver is reached by
        // reading the first entry of the closure in slot 0 — the same walk
        // OpPushReceiver does. In a function closure or outside any frame
        // there is no receiver to inherit, and the new closure is a function
        // closure too.
        void *enclosingClosure = NULL;
       ZamObjectKind enclosingKind = ZAMOBJECT_FUNCTION_CLOSURE;
        if (thread->functionEntry != ZAMMARK_NONE) {
            enclosingClosure = (void *)(uintptr_t)
                thread->operands[zam_thread_frame_base(thread)];
            if (enclosingClosure != NULL)
                enclosingKind = zam_mark_word_of(enclosingClosure)->kind;
        }

       ZamTypeID receiverType = {
            .shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_VOID, 0),
            .arrayLength = 0,
            .tailLength = 0,
        };
       ZamObjectKind newKind = ZAMOBJECT_FUNCTION_CLOSURE;
        uint64_t receiverEntryValueWords = 0;
        if (enclosingKind != ZAMOBJECT_FUNCTION_CLOSURE &&
            enclosingClosure != NULL) {
            // The receiver is one word — the pointer the enclosing closure
            // already resolved it to, whichever kind the enclosing was. A
            // reference closure holds it in the first entry; a value closure
            // holds it as the second entry's one word, pointing at the inline
            // value the first entry is. Either way one word is enough for the
            // new closure, which carries no inline value of its own.
            const ZamClosure *ec = zam_closure_of(enclosingClosure);
            receiverType = zam_closure_entry_type(zam_closure_first(ec));
            newKind = ZAMOBJECT_REFERENCE_METHOD_CLOSURE;
            receiverEntryValueWords = 1;
        }

        uint64_t bodyWords = ZAMTYPE_ID_WORDS + receiverEntryValueWords;
        for (uint64_t i = 0; i < captureCount; i++) {
            uint64_t topOffset = (captureCount - 1 - i) * captureSlotWords;
           ZamTypeID capType = zam_thread_peek_type(thread, topOffset);
            bodyWords += ZAMTYPE_ID_WORDS + valueWordsOf(image, capType);
        }

        uint64_t bodyBytes = sizeof(ZamClosure) + bodyWords * ZAMWORD_BYTES;
        void *object = zam_vm_alloc(thread->vm, thread, newKind, type.shortID,
                                    1, bodyBytes);
       ZamClosure *closure = zam_closure_of(object);
        closure->instruction = operandAt(instruction, 0);
        closure->captureCount = captureCount;

        // Receiver entry. A void receiver's three words are the type id alone
        // and are already zero from the allocation; they are written anyway so
        // the entry's shape is stated here rather than inferred from that.
       ZamWord *writeHead = closure->body;
        zam_type_id_write(writeHead, receiverType);
        writeHead += ZAMTYPE_ID_WORDS;
        if (receiverEntryValueWords == 1) {
            const ZamClosure *ec = zam_closure_of(enclosingClosure);
            const ZamWord *entry = zam_closure_first(ec);
            if (enclosingKind == ZAMOBJECT_VALUE_METHOD_CLOSURE)
                entry = zam_closure_entry_next(
                    entry, valueWordsOf(image, zam_closure_entry_type(entry)));
            *writeHead = *zam_closure_entry_value(entry);
            writeHead++;
        }

        // Captures. The pushed order and the closure order agree: capture 0
        // is both the first push and the first entry past the receiver.
        for (uint64_t i = 0; i < captureCount; i++) {
            uint64_t topOffset = (captureCount - 1 - i) * captureSlotWords;
           ZamTypeID capType = zam_thread_peek_type(thread, topOffset);
           ZamWord capLocation =
                zam_thread_peek(thread, topOffset + ZAMTYPE_ID_WORDS);
            uint64_t capWords = valueWordsOf(image, capType);

            zam_type_id_write(writeHead, capType);
            writeHead += ZAMTYPE_ID_WORDS;
            memcpy(writeHead,
                   zam_thread_location_address(thread, capLocation),
                   zam_type_bytes(image, capType));
            writeHead += capWords;
        }

        // Spend the captures: the type and the one-word location of each.
        for (uint64_t i = 0; i < captureCount; i++) {
            zam_thread_pop_type(thread);
            popWord(thread);
        }

        zam_thread_push(thread, (ZamWord)(uintptr_t)object);
        zam_thread_push_mark(thread, thread->operandCount - 1);
        return ZAMRESULT_CONTINUE;
    }

    // The one instruction that hands the program a pointer to something the
    // collector owns. The shape of the allocation goes down first — how many
    // instances, and how long each one's tail — and the type id lands on top of
    // it, which is the order VisitExprNew pushes them in.
    case OpNew: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
        uint64_t tailLength = zam_thread_pop(thread);
        uint64_t arraySize = zam_thread_pop(thread);

        // A tail length is written either into the type or as new's third
        // operand and never both — VisitExprNew in 3_semantic/4_static.go
        // rejects the pair, because the two could disagree and only one of them
        // would ever be allocated. So whichever is nonzero is the length, and an
        // untailed allocation has neither.
        if (type.tailLength == 0)
            type.tailLength = tailLength;

        // How many instances lie behind the pointer, which the two routes to an
        // array both end at: a length written into the type and a count given as
        // new's first operand lay down the same flat run of elements at the same
        // stride, so what is allocated is their product and nothing distinguishes
        // them afterwards.
       ZamTypeID element = type;
        element.arrayLength = 0;
        uint64_t elementCount =
            arraySize * (type.arrayLength == 0 ? 1 : type.arrayLength);

        // A zero-element allocation points at nothing, which is what the null
        // pointer is for. No block is reserved, no header is stamped, and no
        // initializer is entered — none of them has anywhere to go. The slot
        // is still a pointer slot, so the mark is pushed the way every other
        // reference-shaped result of OpNew does: a null is as walkable a
        // reference as any, and the collector no-ops on it.
        bool markable = isMarkableInstance(image, type);
        if (elementCount == 0) {
            zam_thread_push(thread, 0);
            if (markable)
                zam_thread_push_mark(thread, thread->operandCount - 1);
            return ZAMRESULT_CONTINUE;
        }

        uint64_t elementBytes = zam_type_bytes(image, element);
        uint64_t objectBytes = elementCount * elementBytes;
        void *object;

        if (markable) {
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
            object = zam_alloc(objectBytes);
            memset(object, 0, objectBytes);
        } else {
            // A struct, an enum, a plain primitive or a pointer. The whole of
            // the block is the value — none of these carries a word saying what
            // it is — so the pointer handed back is the one malloc returned, and
            // the free in OpDelete is a free of that same address.
            //
            // It is not linked onto the sweep list, which is what makes this
            // memory the program's to release rather than the collector's. The
            // two byte counts a collection is triggered by are left alone for
            // the same reason: a collection would find nothing here it could
            // reclaim, so counting these bytes would only bring one on sooner
            // for no gain.
            object = zam_alloc(objectBytes);
            memset(object, 0, objectBytes);

            // The one header a struct has, and it is written per element rather
            // than once over the run, a tail belonging to an instance and every
            // instance here having one of its own.
            //
            // Nothing else could write it. A struct is never marked and is never
            // reached under another type's id, so it carries neither of the
            // words that would say what it is, and how long its tail is lives in
            // the type id the allocation was named through and nowhere in the
            // block. This is the same word OpDefaultValue writes for a fresh
            // struct standing on the stack.
            if (isTailedStruct(image, element))
                for (uint64_t index = 0; index < elementCount; index++)
                    *(ZamWord *)((uint8_t *)object + index * elementBytes) =
                        element.tailLength;
        }

        zam_thread_push(thread, (ZamWord)(uintptr_t)object);
        if (markable)
            zam_thread_push_mark(thread, thread->operandCount - 1);

        // Only a type laid out inline can name a routine, and only that kind of
        // type has a definition to name one in: the ids a primitive and a
        // pointer are read under lie below reservedTypeIDCount and point at no
        // record at all. This is the same guard OpDefaultValue has, and it is
        // reached here now that those two allocate.
        if (primitiveOf(image, element) != ZAMPRIMITIVE_COUNT ||
            isReferenceType(image, element))
            return ZAMRESULT_CONTINUE;

        uint64_t initializer = zam_type_initializer(image, element.shortID);
        if (initializer == 0)
            return ZAMRESULT_CONTINUE;

        // The initializer is entered exactly as OpDefaultValue enters it, and
        // for the same reason: a fresh value is filled in wherever one is made,
        // so the two instructions run the same routine and differ only in where
        // the instance lies. A frame two words wide, jumped into rather than
        // called, with the return landing on the instruction after this one.
        //
        // Slot 0 is the receiver, and here it is the address itself rather than
        // a frame location: the instance is in the heap, so there is no slot for
        // a location to name. Either way the slot holds where the instance is,
        // which is what an initializer body reads through — see receiverInFrame
        // in 3_operation.go, which compiles the receiver as the slot rather than
        // as OpPushReceiver.
        //
        // Slot 1 is how many elements there are, and the routine is a loop over
        // them: an entry is left rather than run, so coming back for the next
        // element would be a second entry into this instruction. The count is
        // handed over instead and the loop is the compiler's, which is also what
        // lets it be a number only this run knows — new's first operand is the
        // program's own value and not something the type could have said.
        zam_thread_push(thread, (ZamWord)(uintptr_t)object);
        if (markable)
            zam_thread_push_mark(thread, thread->operandCount - 1);
        zam_thread_push(thread, elementCount);
        zam_thread_push_function(thread, thread->instructionOffset,
                                 thread->operandCount - 2);
        thread->instructionOffset = initializer;
        return ZAMRESULT_CONTINUE;
    }

    // The same run OpNew lays down, built out of values that already exist
    // rather than out of nothing: the same stride, the same triage on what an
    // element is, and the same headers. What it adds is the move — the elements
    // stand on the stack one to a slot and land in the allocation packed.
    case OpNewArray: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID element = zam_thread_pop_type(thread);
        uint64_t count = operandAt(instruction, 0);

        // A length written into the element type is a run inside each element,
        // so it multiplies rather than repeats and what is laid down is the
        // flat product — the same folding OpNew does to the two ways it can be
        // told a length, and for the same reason: nothing downstream can tell
        // the two runs apart once they are laid.
       ZamTypeID flat = element;
        flat.arrayLength = 0;
        uint64_t flatCount =
            count * (element.arrayLength == 0 ? 1 : element.arrayLength);

        // No arguments fed the variadic. An array of nothing is nothing to
        // point at, and null is the honest word for that: how many elements
        // arrived is not passed with them — see ParameterBindingType in
        // 2_syntax/6_type.go — so there is nothing a callee may read through
        // this pointer and no count an empty allocation would have carried.
        //
        // The entry goes on all the same, this being a reference like any
        // other. OpPushNull marks the same word for the same reason.
        if (count == 0) {
            zam_thread_push(thread, 0);
            if (isMarkableInstance(image, flat))
                zam_thread_push_mark(thread, thread->operandCount - 1);
            return ZAMRESULT_CONTINUE;
        }

        // The two strides. An element stands on the stack on a whole number of
        // words and lies in the allocation on exactly its own bytes, so the
        // narrower one is not a copy of a run but a run of copies.
        uint64_t elementWords = valueWordsOf(image, element);
        uint64_t elementBytes = zam_type_bytes(image, element);
        uint64_t objectBytes = count * elementBytes;

        bool markable = isMarkableInstance(image, flat);
        void *object;
        // The word the allocator writes is the one the copy below lands on, and
        // it cannot be written again afterwards: the sweep-list link in it is
        // the previous head of a list this block now heads, so nothing here
        // could work it out a second time. It is kept aside instead.
       ZamMarkWord allocated = {0};

        if (markable) {
            bool tailed = flat.tailLength != 0;
            object = zam_vm_alloc(
                thread->vm, thread,
                tailed ? ZAMOBJECT_TAILED_INSTANCE : ZAMOBJECT_INSTANCE,
                flat.shortID, flatCount, objectBytes);
            allocated = *zam_mark_word_of(object);
        } else {
            // Everything else is a flat run of words the collector does not
            // own, which is OpNew's answer for the same types and is not only
            // about structs and primitives: an element held as a reference — a
            // string, a pointer, an interface — is one address here, and what
            // it names carries its own header elsewhere. There is nothing in
            // this block for a collector to have a header of.
            //
            // Nothing is zeroed, the copy below covering every byte of it: the
            // elements lie at exactly their own width, so the run has no
            // padding between them and the last one ends the block.
            object = zam_alloc(objectBytes);
        }

        // Copied whole, headers and all, and then stamped afresh. A header says
        // where a value lies rather than what it holds, and these values have
        // just moved — the same statement pushValueFrom makes when it copies one
        // the other way, off a location and onto the stack.
        const uint8_t *source =
            (const uint8_t *)valueAt(thread, 0, count * elementWords);
        for (uint64_t index = 0; index < count; index++)
            memcpy((uint8_t *)object + index * elementBytes,
                   source + index * elementWords * ZAMWORD_BYTES,
                   elementBytes);

        if (markable) {
            // The front element is the array, so what stands at the front of it
            // is the allocation's own header and not the element's: the kind and
            // the link the allocator wrote, and a count of the whole run where
            // the element's copied header counted only itself. Everything behind
            // it is contained by it, which is what stampArrayTail says, and a
            // copied element's own members are stamped along with it.
            *zam_mark_word_of(object) = allocated;
            *zam_object_header_of(object) = (ZamObjectHeader){
                .shortID = flat.shortID,
                .elementCount = flatCount,
            };
            if (flat.tailLength != 0)
                ((ZamWord *)object)[2] = flat.tailLength;
            stampArrayTail(image, object, flat, flatCount);
        }

        // A tailed struct's length word is not written here, where OpNew writes
        // one: OpNew fills fresh memory and this copied a value that already
        // carries it.
        for (uint64_t index = 0; index < count; index++)
            popValue(thread, image, element);

        zam_thread_push(thread, (ZamWord)(uintptr_t)object);
        if (markable)
            zam_thread_push_mark(thread, thread->operandCount - 1);
        return ZAMRESULT_CONTINUE;
    }

    // OpDelete runs the Delete override on the receiver. An Object-dispatchable
    // type — a class, abstract, or interface pointer — reaches its own Delete
    // slot, where the default is std.Obj.Delete and marks the allocation for
    // the next sweep. Every other receiver names no override and so does
    // nothing here; releasing the memory is OpFree's. The receiver's words go
    // with the delete either way, which is why the fallthrough pops the whole
    // value rather than a single word — a struct is as valid a receiver as a
    // pointer to one.
    case OpDelete: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
        if (answersThroughObject(image, type)) {
            if (zam_thread_peek(thread, 0) == 0)
                return ZAMRESULT_CONTINUE;
            return enterObjectMethod(thread, image, type,
                                     ZAMOBJECT_SLOT_DELETE, false, 0, type);
        }

        popValue(thread, image, type);
        return ZAMRESULT_CONTINUE;
    }

    // OpFree releases what the pointer addresses. A pointee the collector owns
    // — a class instance, a string, a closure, a thread, a run of class
    // instances — gets marked dead so the next sweep reclaims it; handing one
    // of those to free() would double-manage memory the sweep list still points
    // at. Everything else is the program's own and is raw-freed.
    //
    // A run of reference slots is the one shape that does not fit either rule.
    // new(X, n) for X in string, function, thread or interface takes OpNew's
    // raw branch — the block is a flat run of pointer slots with no header in
    // front of them, since each slot's pointee stays collected on its own — and
    // the same pointeeOf that says a slot's type is collected would steer this
    // free into a mark-word write against the first slot. The pointer shape of
    // the type is what tells the two apart: a free of the primitive itself
    // (type = str) names the collected allocation the pointer-shaped value
    // addresses, and a free through a pointer to one (type = str*) names the
    // raw slot block.
    //
    // Nothing is checked. A pointer the allocator never handed out, or one
    // taken into the middle of a block, is the caller's own undefined
    // behavior, the same as a free of a bogus int* would be — this keeps the
    // one shape and the many shapes consistent and keeps the free path a
    // single mark-dead or a single malloc free.
    case OpFree: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
       ZamWord pointer = zam_thread_pop(thread);
        zam_thread_pop_mark(thread, thread->operandCount);
        if (pointer == 0)
            return ZAMRESULT_CONTINUE;

        // The pointee is what the collector sees: a run of class instances
        // asks about the class, since the block one of them lies in is the
        // block all of them do. A string reduces to itself — pointeeOf leaves
        // a non-pointer id alone — which is the id that says a string carries
        // a header behind the pointer.
       ZamTypeID pointee = pointeeOf(image, type);
        if (isCollectedAllocation(image, pointee)) {
            bool typeIsPointer =
                type.arrayLength == 0 &&
                zam_image_bucket_of(image, ZAMTYPE_DECL(type.shortID)) ==
                    ZAMBUCKET_POINTER;
            if (typeIsPointer && !isMarkableInstance(image, pointee)) {
                free((void *)(uintptr_t)pointer);
                return ZAMRESULT_CONTINUE;
            }

            zam_mark_word_of((void *)(uintptr_t)pointer)->state =
                ZAMMARK_STATE_DEAD;
            return ZAMRESULT_CONTINUE;
        }

        free((void *)(uintptr_t)pointer);
        return ZAMRESULT_CONTINUE;
    }

    // Zeros, wherever zero is the whole of what a fresh value holds: every
    // primitive starts at zero and everything held as an address starts at
    // null, and an array of either does so element for element. A declared
    // type is the case that is more than that, naming a routine to run over
    // the fresh value, and running one is a call.
    case OpDefaultValue: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);

        uint64_t words = valueWordsOf(image, type);
        uint64_t base = thread->operandCount;
        for (uint64_t word = 0; word < words; word++)
            zam_thread_push(thread, 0);
        markPushedValue(thread, image, type);

       ZamTypeID element = type;
        element.arrayLength = 0;

        // A markable instance carries the header it is collected through
        // wherever it lies, which since the header became part of the layout
        // includes the stack. So a fresh one is given the header a fresh
        // allocation is given: nothing else would fill it, an inline value
        // having no allocator, and the zeros left otherwise say the kind is a
        // string — which is what a member offset would then be resolved
        // against.
        if (isMarkableInstance(image, element)) {
            // Read after the pushes rather than before: growing the stack is
            // what moves it, so an address taken at the base would name where
            // the words used to be.
            void *instance = thread->operands + base;
            uint64_t elementCount =
                type.arrayLength == 0 ? 1 : type.arrayLength;

            // Contained, with no container, which is the one way this header
            // differs from the one zam_vm_alloc writes. The instance lies on the
            // operand stack: it is not an allocation, so the upvalue is not a
            // sweep-list link and the storage is the stack's to reclaim rather
            // than the collector's to free. Being markable and being the
            // collector's are not the same thing, and this bit is what tells
            // them apart.
            //
            // Nothing contains it either, the frame slot it stands in being no
            // object, so the distance is zero — which is the whole of what makes
            // this the same call a member of a class gets. Everything below the
            // root is stamped from the definition alone: the elements behind the
            // front one, each contained by it, and the markable members laid
            // inline in any of them.
            stampContained(image, instance, element, 0, elementCount);
        } else if (isTailedStruct(image, element)) {
            // The whole of a struct's header, and the one number in an instance
            // that nothing else could recover: a struct is never marked and is
            // never reached under another type's id, so it carries neither of the
            // words that would say what it is, and how long its tail is lives in
            // the id the instance was named through and nowhere in the instance.
            // Writing it here is what makes the length outlive that id.
            //
            // Written per element rather than once over the run, since a tail
            // belongs to an instance and every instance in a run has one of its
            // own. The stride is the element's own width and the tail is part of
            // it — the elements of a run lie back to back, so a stride taken
            // from the fixed part would write every header but the first inside
            // the element before it. This is the same loop OpNew writes over an
            // allocated run, against the stack rather than a block.
            uint64_t elementBytes = zam_type_bytes(image, element);
            uint64_t elementCount =
                type.arrayLength == 0 ? 1 : type.arrayLength;
            for (uint64_t index = 0; index < elementCount; index++)
                *(ZamWord *)((uint8_t *)(thread->operands + base) +
                             index * elementBytes) = element.tailLength;
        }

        // Only a type laid out inline can name a routine, and only that kind of
        // type has a definition to name one in.
        if (primitiveOf(image, element) != ZAMPRIMITIVE_COUNT ||
            isReferenceType(image, element))
            return ZAMRESULT_CONTINUE;

        uint64_t initializer = zam_type_initializer(image, element.shortID);
        if (initializer == 0)
            return ZAMRESULT_CONTINUE;

        // The routine is entered as a method is in every respect but its first
        // two slots: a frame two words wide, jumped into rather than called.
        // There is no closure to put in slot 0, so the instance goes there
        // directly, and since it is on the stack rather than in an allocation
        // what goes there is where it lies — which is exactly what a frame
        // location says.
        //
        // So an initializer body reads its receiver as the slot and not through
        // OpPushReceiver. The compiler knows which body it is emitting, which is
        // receiverInFrame in 3_operation.go.
        //
        // Slot 1 is how many elements lie there, and the routine loops over
        // them. A call is left rather than run — the frame is laid, the body is
        // jumped to, and the return lands on the instruction after this one — so
        // this instruction could not come back to lay a second frame, and the
        // repetition is the compiled routine's rather than the step's.
        zam_thread_push(thread, zam_frame_location(base * ZAMWORD_BYTES));
        zam_thread_push(thread, type.arrayLength == 0 ? 1 : type.arrayLength);
        zam_thread_push_function(thread, thread->instructionOffset,
                                 thread->operandCount - 2);
        thread->instructionOffset = initializer;
        return ZAMRESULT_CONTINUE;
    }

    // OpNewThread takes the frame a call would run against and moves it onto a
    // thread of its own. The frame is already assembled on this thread's stack
    // — closure, then arguments — so the move is a straight copy of its words
    // onto the new thread, with the mark entries that go with those words
    // copied across too, their operand offsets shifted to the new base of
    // zero. This thread lets go of them in turn.
    //
    // The new thread's body is entered by jumping to the closure's instruction
    // on top of a function frame of its own. The return instruction on that
    // frame is unread: a return from the top of a spawned thread leaves the
    // run loop with ZAMRESULT_RETURNED rather than continuing, which
    // OpReturn is what checks and this is what sets up.
    case OpNewThread: {
        uint64_t frameWords = operandAt(instruction, 0);
       ZamTypeID threadType = zam_thread_pop_type(thread);
        if (frameWords == 0 || frameWords > thread->operandCount)
            zam_die(ZAMEXIT_PANIC,
                    "a thread frame of %llu words under a stack of %llu\n",
                    (unsigned long long)frameWords,
                    (unsigned long long)thread->operandCount);
        uint64_t base = thread->operandCount - frameWords;

        void *closureObject = (void *)(uintptr_t)thread->operands[base];
        if (closureObject == NULL)
            zam_die(ZAMEXIT_PANIC, "spawned a null function\n");
       ZamClosure *closure = zam_closure_of(closureObject);
       ZamVM *vm = thread->vm;
        void *object = zam_vm_alloc(vm, thread, ZAMOBJECT_THREAD,
                                    threadType.shortID, 1, sizeof(ZamThread));
       ZamThread *newThread = (ZamThread *)object;
        zam_thread_init(newThread, vm, vm->image.initialStackSize);

        // The frame's words land as the new thread's first operands. The
        // closure is at index 0, which is also where a call reads its receiver
        // from, so the body enters on the same shape its compiled form expects.
        for (uint64_t i = 0; i < frameWords; i++)
            zam_thread_push(newThread, thread->operands[base + i]);

        // The frame's mark entries move with its words. They are the topmost
        // entries on this thread's mark stack above the innermost open call or
        // scope, because the frame was assembled by pushes and no entry below
        // the frame can lie above its words — mark entries sit in operand
        // order. So the first entry whose operand lies in the frame marks the
        // cut: everything from there up moves, with each operand shifted to the
        // new thread's base of zero.
        uint64_t floor = thread->functionEntry != ZAMMARK_NONE
                             ? thread->functionEntry + sizeof(ZamMarkFunction)
                             : 0;
        if (thread->scopeEntry != ZAMMARK_NONE &&
            thread->scopeEntry + sizeof(ZamMarkScope) > floor)
            floor = thread->scopeEntry + sizeof(ZamMarkScope);

        uint64_t cutoff = thread->markByteCount;
        for (uint64_t at = floor; at < thread->markByteCount;) {
           ZamMarkKind kind = zam_mark_kind_at(thread, at);
            uint64_t size = zam_mark_entry_bytes(kind);
            ZamMarkObject head;
            memcpy(&head, thread->marks + at, sizeof head);
            if (kind != ZAMMARK_OBJECT && kind != ZAMMARK_ARRAY &&
                kind != ZAMMARK_VALUE && kind != ZAMMARK_INTERIOR)
                zam_die(ZAMEXIT_PANIC,
                        "a %u-kind entry above the enclosing frame\n",
                        (unsigned)kind);
            if (head.operand >= base) {
                if (at < cutoff)
                    cutoff = at;
                uint64_t operand = head.operand - base;
                if (kind == ZAMMARK_OBJECT) {
                    zam_thread_push_mark(newThread, operand);
                } else if (kind == ZAMMARK_ARRAY) {
                   ZamMarkArray entry;
                    memcpy(&entry, thread->marks + at, sizeof entry);
                   ZamTypeID element = {
                        .shortID = (ZamTypeShort)entry.element,
                        .arrayLength = entry.length,
                        .tailLength = 0,
                    };
                    if (entry.indirect)
                        zam_thread_push_indirect_array_mark(newThread, element,
                                                            operand);
                    else
                        zam_thread_push_array_mark(newThread, element, operand);
                } else if (kind == ZAMMARK_VALUE) {
                   ZamMarkValue entry;
                    memcpy(&entry, thread->marks + at, sizeof entry);
                    zam_thread_push_value_mark(
                        newThread, zam_type_id_read((const ZamWord *)&entry.type),
                        operand);
                } else {
                   ZamMarkInterior entry;
                    memcpy(&entry, thread->marks + at, sizeof entry);
                    zam_thread_push_interior_mark(newThread, operand,
                                                  entry.distance);
                }
            }
            at += size;
        }
        thread->markByteCount = cutoff;
        thread->operandCount = base;

        // The sentinel frame the body returns past. operandPop zero puts the
        // returned value at the base of the stack, where a join reads it; the
        // return instruction is unread, OpReturn leaving the loop first.
        zam_thread_push_function(newThread, 0, 0);
        newThread->instructionOffset = closure->instruction;

        // The closure's captures land past the arguments on the new thread's
        // stack, where the body was compiled to read them: the same shape
        // OpCallMethod lays them in for a call on this thread.
        if (closure->captureCount != 0) {
            const ZamImage *image = &vm->image;
            const ZamWord *entry = closureCaptures(image, closure);
            for (uint64_t i = 0; i < closure->captureCount; i++) {
               ZamTypeID capType = zam_closure_entry_type(entry);
                const ZamWord *capValue = zam_closure_entry_value(entry);
                uint64_t capWords = valueWordsOf(image, capType);
                for (uint64_t w = 0; w < capWords; w++)
                    zam_thread_push(newThread, capValue[w]);
                markPushedValue(newThread, image, capType);
                entry = zam_closure_entry_next(entry, capWords);
            }
        }

        // On the VM's thread list so the collector can walk its stacks as
        // roots. The lock is the one that guards the list; it is not what
        // keeps the thread itself alive, which is the mark entry below.
        pthread_mutex_lock(&vm->threadLock);
        newThread->next = vm->main.next;
        vm->main.next = newThread;
        pthread_mutex_unlock(&vm->threadLock);

        zam_thread_push(thread, (ZamWord)(uintptr_t)newThread);
        zam_thread_push_mark(thread, thread->operandCount - 1);

        if (pthread_create(&newThread->systemThread, NULL, threadMain,
                           newThread) != 0)
            zam_die(ZAMEXIT_PANIC, "failed to spawn a system thread\n");

        return ZAMRESULT_CONTINUE;
    }

    // OpWaitThreadVoid joins a thread and spends it. The thread's result lands
    // on its own stack where OpReturn put it; a void body leaves no value, so
    // the join reads nothing and the thread pointer is all that is spent.
    case OpWaitThreadVoid: {
       ZamThread *target =
            (ZamThread *)(uintptr_t)zam_thread_peek(thread, 0);
        if (target == NULL)
            zam_die(ZAMEXIT_PANIC, "joined a null thread\n");
        joinThread(thread, target);
        popWord(thread);
        return ZAMRESULT_CONTINUE;
    }

    // OpWaitThread is the same, with the value the body returned read off the
    // joined thread's stack and pushed onto this one.
    case OpWaitThread: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID resultType = zam_thread_pop_type(thread);
       ZamThread *target =
            (ZamThread *)(uintptr_t)zam_thread_peek(thread, 0);
        if (target == NULL)
            zam_die(ZAMEXIT_PANIC, "joined a null thread\n");
        joinThread(thread, target);

        uint64_t words = valueWordsOf(image, resultType);
        if (words > target->operandCount)
            zam_die(ZAMEXIT_PANIC,
                    "joined thread returned %llu words, expected %llu\n",
                    (unsigned long long)target->operandCount,
                    (unsigned long long)words);

        popWord(thread);
        for (uint64_t i = 0; i < words; i++)
            zam_thread_push(thread, target->operands[i]);
        markPushedValue(thread, image, resultType);
        return ZAMRESULT_CONTINUE;
    }

    // The two type ids in play answer different questions and are read for
    // different things: the value's says how many words come off the stack,
    // and the location's says how much of them lands at the address. A subclass
    // written through a base-typed location is the case that keeps them apart —
    // what is written is the base's worth, and the rest of the value is spent
    // rather than stored.
    case OpStore: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID valueType = zam_thread_pop_type(thread);
        uint64_t valueWords = valueWordsOf(image, valueType);

        // Neither the location nor its pointer is spent: a store leaves the
        // location standing so a chain of assignments reads as one.
       ZamTypeID locationType = zam_thread_peek_type(thread, valueWords);
        uint8_t *at = zam_thread_location_address(
            thread, zam_thread_peek(thread, valueWords + ZAMTYPE_ID_WORDS));

        uint64_t locationBytes = zam_type_bytes(image, locationType);
        if (locationBytes > valueWords * ZAMWORD_BYTES)
            zam_die(ZAMEXIT_PANIC,
                    "storing %llu bytes through a location wider than the "
                    "%llu word value beneath it\n",
                    (unsigned long long)locationBytes,
                    (unsigned long long)valueWords);
        // The destination's header is the one part of it a store leaves alone.
        // What a header says is about where the value lies rather than about
        // what it holds, and a store moves the value and not the place: an
        // allocation's word carries the sweep-list link, and an interior slot's
        // carries how far back its container is, and the value coming off the
        // stack knows neither. Copying it over the destination's would replace a
        // true statement about that address with a true statement about the
        // stack — losing a block off the sweep list, or leaving a member
        // claiming to be contained by nothing.
        //
        // Stepping over the same width at both ends is what keeps the members
        // aligned, the offsets being net of the header on either side.
        uint64_t headerBytes = valueHeaderBytes(image, locationType);
        const uint8_t *value =
            (const uint8_t *)(thread->operands + thread->operandCount -
                              valueWords);
        memcpy(at + headerBytes, value + headerBytes,
               locationBytes - headerBytes);

        popValue(thread, image, valueType);
        return ZAMRESULT_CONTINUE;
    }

    // The mirror of OpStore, and told a type id for the same two reasons: how
    // much comes off the address, and whether what lands on the stack is a
    // reference the collector has to visit.
    //
    // OpDereference is the same instruction over a pointer the program worked
    // out rather than a location the compiler laid down, and the difference is
    // the whole of what separates them: a slot that exists by construction
    // cannot be null, and a pointer a program holds can be.
    case OpDereference:
    case OpLoad: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
       ZamWord location = zam_thread_pop(thread);
        if (instruction.opcode == OpDereference && location == 0)
            zam_die(ZAMEXIT_PANIC, "dereferenced a null pointer\n");

        // The pointer's entry goes with the pointer. What stands in its place
        // is the value's own, which pushValueFrom leaves if the value is one to
        // have one.
        zam_thread_pop_mark(thread, thread->operandCount);
        pushValueFrom(thread, image, type, location);
        return ZAMRESULT_CONTINUE;
    }

    // A location's pointer already is the address, so the pair collapses to it
    // by letting the type words go. The compiler emits this only over something
    // addressable, so there is never a temporary here to find a home for, and
    // the pointer's mark entry — if it owned one — is left as it stands.
    case OpAddressOf:
        (void)zam_thread_pop_type(thread);
        return ZAMRESULT_CONTINUE;

    // The slot's address, which is half of a location; the type words above it
    // are pushed by whoever wanted the location.
    //
    // The pointer is marked whether or not the collector will do anything with
    // it. An address inside the static region is not an allocation and is never
    // collected, so the entry is one the collector recognizes by range and
    // leaves alone — but a word that owns an entry and a word that does not are
    // spent differently, and having every pointer own one is what keeps that
    // from being a question asked at each of them. The type id operand is
    // therefore unread here: it would only have said whether to mark.
    case OpPushStatic:
        zam_thread_push(
            thread,
            (ZamWord)(uintptr_t)zam_vm_static(thread->vm,
                                              operandAt(instruction, 0)));
        return ZAMRESULT_CONTINUE;

    // OpPushGenericStaticSlot is OpPushStatic for a method that could not be
    // named by a constant at compile time: the type arguments are on the
    // stack and the slot has to be chosen by them. The compiler laid the
    // arguments in source order, so the first argument is deepest and the
    // last is on top.
    //
    // The entry's concretes are walked front to back, each matched word-for-
    // word against the arguments. A match ends the search and the result —
    // the slot pointer and the function type id the body was emitted as —
    // leaves the stack in the shape OpPushStatic + three id-word pushes
    // would have. Nothing having matched is a bug the program could not have
    // asked for, since every concrete the dispatch could reach is in the
    // registry by construction, so it ends the run rather than carries on.
    case OpPushGenericStaticSlot: {
        uint64_t id = operandAt(instruction, 0);
        uint64_t arity = 0;
        uint64_t concreteCount = 0;
        const uint8_t *concretes = zam_image_generic_static_method_entry(
            &thread->vm->image, id, &arity, &concreteCount);

        uint64_t argWords = arity * ZAMTYPE_ID_WORDS;
        if (thread->operandCount < argWords)
            zam_die(ZAMEXIT_PANIC,
                    "generic static method %llu wants %llu type-id words and "
                    "the stack holds only %llu\n",
                    (unsigned long long)id, (unsigned long long)argWords,
                    (unsigned long long)thread->operandCount);
        const ZamWord *args = thread->operands + thread->operandCount - argWords;

        size_t concreteBytes =
            (arity * ZAMTYPE_ID_WORDS + 1 + ZAMTYPE_ID_WORDS) * ZAMWORD_BYTES;
        const uint8_t *match = NULL;
        for (uint64_t i = 0; i < concreteCount; i++) {
            const uint8_t *entry = concretes + i * concreteBytes;
            if (memcmp(entry, args, argWords * ZAMWORD_BYTES) == 0) {
                match = entry;
                break;
            }
        }
        if (match == NULL)
            zam_die(ZAMEXIT_PANIC,
                    "generic static method %llu has no concrete for the type "
                    "arguments on the stack\n",
                    (unsigned long long)id);

        // Pop the arguments and their mark entries, which the compiler pushed
        // through emitPushTypeID — a push for every type id is three words
        // and the mark entries of each are threaded through as any other push
        // would be.
        for (uint64_t i = 0; i < argWords; i++)
            popWord(thread);

        uint64_t slot = ((const ZamWord *)(match + arity * ZAMTYPE_ID_WORDS *
                                                       ZAMWORD_BYTES))[0];
        const ZamWord *fnType =
            (const ZamWord *)(match
                              + (arity * ZAMTYPE_ID_WORDS + 1) * ZAMWORD_BYTES);

        zam_thread_push(thread, (ZamWord)(uintptr_t)zam_vm_static(thread->vm,
                                                                   slot));
        for (uint64_t i = 0; i < ZAMTYPE_ID_WORDS; i++)
            zam_thread_push(thread, fnType[i]);
        return ZAMRESULT_CONTINUE;
    }

    // Null is told the type of the slot it is headed for, which is what says
    // whether it owns a mark entry: a slot of a markable reference keeps its
    // entry for whatever is stored in it later, and a pointer into the
    // program's own memory never had one.
    case OpPushNull: {
       ZamTypeID type = zam_thread_pop_type(thread);
        zam_thread_push(thread, 0);
        markPushedValue(thread, &thread->vm->image, type);
        return ZAMRESULT_CONTINUE;
    }

    // A frame slot, named by how far into the stack it lies rather than by
    // where it is now: the offset is measured from the frame the innermost open
    // call laid, and the operand is the slot's place within it.
    //
    // The pointer owns no mark entry, the slot's own contents having been given
    // one when they were pushed.
    case OpPushVariable:
        zam_thread_push(thread,
                        zam_frame_location((zam_thread_frame_base(thread) +
                                            operandAt(instruction, 0)) *
                                           ZAMWORD_BYTES));
        return ZAMRESULT_CONTINUE;

    // `this`, which is a word in the closure rather than a word in the frame.
    // Slot 0 of the frame is the closure the call was made through, and the
    // word naming the receiver lies inside it — one entry in for a receiver
    // that had a home of its own, two for one whose home the closure became.
    //
    // What is pushed is the address of that word, not the address in it. A
    // location loads to what the word holds, so `this` loads to the receiver's
    // address exactly as it did when the frame slot held it, and every reader
    // of `this` is untouched.
    //
    // No mark entry: this addresses the inside of the closure, and slot 0 holds
    // the closure for the whole of the call under an entry of its own.
    case OpPushReceiver: {
        void *object =
            (void *)(uintptr_t)thread->operands[zam_thread_frame_base(thread)];
        if (object == NULL)
            zam_die(ZAMEXIT_PANIC, "a receiver of no closure\n");

        const ZamClosure *closure = zam_closure_of(object);
        const ZamWord *entry = zam_closure_first(closure);
        if (zam_mark_word_of(object)->kind ==
            ZAMOBJECT_VALUE_METHOD_CLOSURE) {
            const ZamImage *image = &thread->vm->image;
            entry = zam_closure_entry_next(
                entry, valueWordsOf(image, zam_closure_entry_type(entry)));
        }

        zam_thread_push(thread,
                        (ZamWord)(uintptr_t)zam_closure_entry_value(entry));
        return ZAMRESULT_CONTINUE;
    }

    // A literal is the address of its slot and no more. The static region holds
    // it in the shape a string is held in everywhere — a mark word, a length,
    // and the bytes — so there is nothing to copy and nothing to build, and
    // intern in 1_stringliterals.go records a slot's offset as its mark word for
    // exactly that reason.
    //
    // What is pushed is the string and not a location over it, so no type words
    // follow and nothing is loaded through it. See OpPushStatic, which pushes the
    // same word out of the same slot numbering and means the other thing by it.
    //
    // No mark entry, which is the one way a literal differs from every other
    // string on the stack: the static region is never collected and outlives
    // every frame that names one, so an entry would hand the collector an
    // allocation it does not own.
    case OpPushStringLiteral:
        zam_thread_push(thread, (ZamWord)(uintptr_t)zam_vm_static(
                                    thread->vm, operandAt(instruction, 0)));
        return ZAMRESULT_CONTINUE;

    // A type id in and a type id out, which is the last step of pushing a type
    // that is only known once a template has been instantiated: the id below is
    // the instantiation the body is executing as, and the operand is the position
    // the open type holds in the declaration's enumeration of them.
    //
    // The id is popped rather than read, so this composes: emitPushTypeID in
    // 3_operation.go leaves the receiver's own id here through OpTypeOf, and what
    // comes back stands in the same place a settled id would have been pushed.
    //
    // No mark entry either side. A type id is three plain words wherever it came
    // from, and the enumeration it is read out of is the image's own bytes.
    case OpPushGenericArgType: {
       ZamTypeID type = zam_thread_pop_type(thread);
        zam_thread_push_type(thread,
                             zam_type_generic_arg(&thread->vm->image,
                                                  type.shortID,
                                                  operandAt(instruction, 0)));
        return ZAMRESULT_CONTINUE;
    }

    // The four steps, which are the one arithmetic handed a location rather than
    // a value: what they move is where the value lies, and what they leave is a
    // read of that place. How far a step goes is steppedWord's to say.
    //
    // The location is measured at its own width and not at a word. A char is
    // four bytes and lies in four, so a step that returned eight to it would
    // write over whatever the instance holds after it. The arithmetic is done on
    // a word all the same, every type a step is told about standing on exactly
    // one, and the word starts at zero so that the bytes above a char's four are
    // clear rather than whatever the address happened to be followed by.
    case OpIncPre:
    case OpDecPre:
    case OpIncPost:
    case OpDecPost: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
       ZamWord location = zam_thread_pop(thread);

        // The location's entry comes off with the location, as it does in a
        // load, and what goes on in its place is the value's own — which
        // pushValueFrom pushes if the value is one to have one, a stepped
        // pointer being the case where it is. The two are separate entries over
        // the same index and not one entry kept: what an entry names is a word
        // of this stack, and the word standing there is no longer the one that
        // was named.
        zam_thread_pop_mark(thread, thread->operandCount);

        if (valueWordsOf(image, type) != 1)
            zam_die(ZAMEXIT_PANIC,
                    "stepping a value of type %u, which stands on %llu words\n",
                    type.shortID,
                    (unsigned long long)valueWordsOf(image, type));

        uint64_t bytes = zam_type_bytes(image, type);
       ZamWord word = 0;
        memcpy(&word, zam_thread_location_address(thread, location), bytes);
        word = steppedWord(image, type, instruction.opcode, word);

        // Which of the two values the program asked for is the whole of what
        // separates the post forms from the pre forms: one is the place as it
        // was and the other is the place as it now is. Both are read back out of
        // the location rather than pushed from the word in hand, so that what
        // lands on the stack is what lies there either way.
        //
        // The address is worked out again on the far side of the push. A frame
        // location names a slot in the very stack a push may have moved, which
        // is why pushValueFrom is handed the location rather than an address,
        // and an address taken before it would name where the slot used to be.
        bool before = instruction.opcode == OpIncPost ||
                      instruction.opcode == OpDecPost;
        if (before)
            pushValueFrom(thread, image, type, location);
        memcpy(zam_thread_location_address(thread, location), &word, bytes);
        if (!before)
            pushValueFrom(thread, image, type, location);
        return ZAMRESULT_CONTINUE;
    }

    // The first instruction to build a string rather than name one. Both
    // operands are strings by construction — stringBinaryOpcodes in
    // 3_operation.go reaches this opcode only for a sum of two of them — so
    // neither carries a type id and there is nothing here to branch on.
    //
    // The bytes are copied because neither operand can be extended in place:
    // each is either an interned literal, which is the image's own memory, or an
    // allocation something else may still hold. So a concatenation is a third
    // string, and two of the same pair built twice are two allocations that
    // compare equal — which is what valuesEqual already answers by the bytes.
    case OpConcatString: {
        // Read while both are still standing, so that the allocation below
        // happens with the two of them reachable from this stack. Nothing
        // collects mid-instruction today, the loop being the only place a
        // collection may happen, but spending them first would make this the
        // one place that relied on it.
        const ZamString *left =
            zam_string_of((void *)(uintptr_t)zam_thread_peek(thread, 1));
        const ZamString *right =
            zam_string_of((void *)(uintptr_t)zam_thread_peek(thread, 0));
        uint64_t length = left->length + right->length;

       ZamString *result = newString(thread, length);
        memcpy(result->bytes, left->bytes, left->length);
        memcpy(result->bytes + left->length, right->bytes, right->length);

        // Each operand goes with the entry it owned, if it owned one: a literal
        // is handed no entry, the static region outliving every frame that names
        // one, so the pop is asked rather than assumed. The result owns one
        // outright — it is an allocation, and the word standing here is the only
        // thing that refers to it.
        popWord(thread);
        popWord(thread);
        zam_thread_push(thread, (ZamWord)(uintptr_t)result);
        zam_thread_push_mark(thread, thread->operandCount - 1);
        return ZAMRESULT_CONTINUE;
    }

    // OpNative pushes a resolved native function pointer. The index is the slot
    // the compiler assigned the name, and zam_natives_resolve filled that slot
    // when the VM was built, so the word is the function the interpreter was
    // linked against. OpCallNative is what reads it back off the frame and
    // calls it; everything in between is as any other call built its frame.
    case OpNative: {
        uint64_t index = operandAt(instruction, 0);
        if (index >= thread->vm->nativeCount)
            zam_die(ZAMEXIT_IMAGE,
                    "OpNative index %llu is past the %llu-entry native table\n",
                    (unsigned long long)index,
                    (unsigned long long)thread->vm->nativeCount);
        zam_thread_push(thread, (ZamWord)(uintptr_t)thread->vm->natives[index]);
        return ZAMRESULT_CONTINUE;
    }

    // OpCallNative enters a C function sitting at the base of its own frame.
    // The frame was built by whatever pushed the native address and then its
    // arguments; the opcode's job is to turn that into a call. A function entry
    // is laid down so the SDK's return helpers can rewind the operand stack to
    // the base, and so the collector sees the frame the way it sees any other.
    //
    // The native runs in C and returns through the usual C mechanism, which is
    // not through the dispatch loop. One of zam_native_return_void or
    // zam_native_return must have run before it returned here, or the frame it
    // was handed is still open; that is a bug in the native, and the check
    // after the call is what catches it loudly rather than letting a stale
    // entry outlive its words.
    case OpCallNative: {
        uint64_t frameWords = operandAt(instruction, 0);
        if (frameWords == 0 || frameWords > thread->operandCount)
            zam_die(ZAMEXIT_PANIC,
                    "a native frame of %llu words under a stack of %llu\n",
                    (unsigned long long)frameWords,
                    (unsigned long long)thread->operandCount);
        uint64_t base = thread->operandCount - frameWords;

       ZamWord address = thread->operands[base];
        if (address == 0)
            zam_die(ZAMEXIT_PANIC, "called a null native\n");
       ZamNativeFn fn = (ZamNativeFn)(uintptr_t)address;

        uint64_t enclosingEntry = thread->functionEntry;
        zam_thread_push_function(thread, thread->instructionOffset, base);
        fn(thread);
        if (thread->functionEntry != enclosingEntry)
            zam_die(ZAMEXIT_PANIC,
                    "native returned without calling zam_native_return\n");
        return ZAMRESULT_CONTINUE;
    }

    // Pushes the receiver's actual type id, which is the one word a generic
    // body cannot know ahead of time: the body is emitted once per declaration
    // and the id a generic argument stands for is whichever concrete called in.
    // The header the receiver points at carries it.
    //
    // The operand says where the receiver sits. A frame slot — an initializer's
    // case, where slot 0 is `this` outright — reads the slot directly. The
    // sentinel reads it through the closure in slot 0, which is where a method
    // body's `this` lives and which is the same walk OpPushReceiver takes.
    //
    // Either way the word is a pointer or a frame location, resolved to a real
    // address through zam_thread_location_address. Three plain words are pushed
    // for the type id, none owning a mark entry.
    case OpPushReceiverType: {
        uint64_t slot = operandAt(instruction, 0);
        void *receiver;
        if (slot == UINT64_MAX) {
            void *closureObject = (void *)(uintptr_t)thread->operands
                                      [zam_thread_frame_base(thread)];
            if (closureObject == NULL)
                zam_die(ZAMEXIT_PANIC, "a receiver of no closure\n");

            const ZamClosure *closure = zam_closure_of(closureObject);
            const ZamWord *entry = zam_closure_first(closure);
            if (zam_mark_word_of(closureObject)->kind ==
                ZAMOBJECT_VALUE_METHOD_CLOSURE) {
                const ZamImage *image = &thread->vm->image;
                entry = zam_closure_entry_next(
                    entry, valueWordsOf(image, zam_closure_entry_type(entry)));
            }
            // The entry's value word holds the receiver's address — the
            // pointer the caller handed to the method — so the receiver is
            // reached by reading that word, not by taking the address of it.
            // Taking the address of the entry would reach the closure's own
            // bytes, whose header carries the closure's shortID rather than
            // the receiver's and so would read as a type that enumerates no
            // open generic arguments at all.
            //
            // The word may be a real pointer (a heap receiver) or a frame
            // location (a stack-allocated receiver's address survived the
            // call as the same tagged form it was captured in). Both go
            // through zam_thread_location_address — a real pointer is left
            // alone and a frame location is resolved to the stack byte it
            // names — so one branch answers for both.
            receiver = zam_thread_location_address(
                thread, *zam_closure_entry_value(entry));
        } else {
           ZamWord location =
                thread->operands[zam_thread_frame_base(thread) + slot];
            if (location == 0)
                zam_die(ZAMEXIT_PANIC,
                        "receiver type of a null pointer\n");
            receiver = zam_thread_location_address(thread, location);
        }

        if (receiver == NULL)
            zam_die(ZAMEXIT_PANIC, "receiver type of a null pointer\n");

       ZamMarkWord *mark = zam_mark_word_of(receiver);
       ZamTypeID actual = {
            .shortID = zam_object_header_of(receiver)->shortID,
            .arrayLength = 0,
            .tailLength = mark->kind == ZAMOBJECT_TAILED_INSTANCE
                              ? zam_object_tail_length_of(receiver)
                              : 0,
        };
        zam_thread_push_type(thread, actual);
        return ZAMRESULT_CONTINUE;
    }

    // The any trio. OpBoxAny lays a payload and its id into a fresh pair of
    // allocations and leaves the one pointer that reaches them both.
    // OpAnyPayloadType reads the stored id, and OpAnyUnbox reads the payload
    // out as a type the box is checked to actually hold.
    //
    // The payload box is a standard ZAMOBJECT_INSTANCE whose shortID is the
    // payload type's own, so a primitive payload reads as a one-word body
    // under a primitive id and a class payload reads as the usual mark + type
    // header followed by its members. The any box is ZAMOBJECT_ANY whose
    // four-word body is the three-word payload id and the payload pointer,
    // in that order. The collector is a stub (see zam_vm_collect in vm.c), so
    // the walker rule for ZAMOBJECT_ANY — mark word 5 as a pointer — is
    // described in the enum comment and will land with the collector.

    case OpBoxAny: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID payloadType = zam_thread_pop_type(thread);

        uint64_t payloadValueWords = valueWordsOf(image, payloadType);
        uint64_t inlineHeader = inlineValueHeaderBytes(image, payloadType);
        uint64_t bodyBytes =
            payloadValueWords * ZAMWORD_BYTES - inlineHeader;
        uint64_t typedHeader = zam_object_header_bytes(ZAMOBJECT_INSTANCE);

        // The payload box's body is the value's body — not its inline header
        // — because the fresh allocation gets its own mark and type header
        // written by zam_vm_alloc. Copying the stack value's header on top
        // would overwrite that with one whose contained bit and upvalue were
        // right for a container that no longer holds the value.
        void *payloadBox =
            zam_vm_alloc(thread->vm, thread, ZAMOBJECT_INSTANCE,
                         payloadType.shortID, 1, typedHeader + bodyBytes);
        const ZamWord *stackValue = valueAt(thread, 0, payloadValueWords);
        memcpy((uint8_t *)payloadBox + typedHeader,
               (const uint8_t *)stackValue + inlineHeader, bodyBytes);
        popValue(thread, image, payloadType);

        // The any box's body is the id and the payload pointer. The id is
        // laid down in the same wire form every id is written in, so an
        // OpAnyPayloadType or an OpAnyUnbox can read it with the same
        // zam_type_id_read every other reader uses.
        void *anyBox = zam_vm_alloc(thread->vm, thread, ZAMOBJECT_ANY,
                                    (ZamTypeShort)ZAMPRIMITIVE_ANY << 16, 1,
                                    typedHeader + 4 * ZAMWORD_BYTES);
       ZamWord *anyBody = (ZamWord *)((uint8_t *)anyBox + typedHeader);
        zam_type_id_write(anyBody, payloadType);
        anyBody[3] = (ZamWord)(uintptr_t)payloadBox;

        zam_thread_push(thread, (ZamWord)(uintptr_t)anyBox);
        zam_thread_push_mark(thread, thread->operandCount - 1);
        return ZAMRESULT_CONTINUE;
    }

    case OpAnyPayloadType: {
        void *anyBox = (void *)(uintptr_t)zam_thread_pop(thread);
        zam_thread_pop_mark(thread, thread->operandCount);
        if (anyBox == NULL)
            zam_die(ZAMEXIT_PANIC, "payload type of a null any\n");

        uint64_t typedHeader = zam_object_header_bytes(ZAMOBJECT_INSTANCE);
        const ZamWord *anyBody =
            (const ZamWord *)((const uint8_t *)anyBox + typedHeader);
        zam_thread_push_type(thread, zam_type_id_read(anyBody));
        return ZAMRESULT_CONTINUE;
    }

    case OpAnyUnbox: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID target = zam_thread_pop_type(thread);
        void *anyBox = (void *)(uintptr_t)zam_thread_pop(thread);
        zam_thread_pop_mark(thread, thread->operandCount);
        if (anyBox == NULL)
            zam_die(ZAMEXIT_PANIC, "unbox of a null any\n");

        uint64_t typedHeader = zam_object_header_bytes(ZAMOBJECT_INSTANCE);
        const ZamWord *anyBody =
            (const ZamWord *)((const uint8_t *)anyBox + typedHeader);
       ZamTypeID payloadType = zam_type_id_read(anyBody);
        if (!typeIsSubtypeOf(image, payloadType, target))
            zam_die(ZAMEXIT_PANIC, "any holds a value of a type the unbox "
                                     "did not ask for\n");

        void *payloadBox = (void *)(uintptr_t)anyBody[3];
        if (payloadBox == NULL)
            zam_die(ZAMEXIT_PANIC, "unbox of an any with no payload\n");

        // The unbox is told the target type and copies that many words out,
        // which is what makes a primitive drop to its one word, a class
        // instance arrive with its usual inline header, and a generic arg
        // read as whatever width the receiver resolved it to. The payload
        // was stored under the exact target width, since isSubtypeOf agreed
        // the two name the same layout.
        uint64_t targetValueWords = valueWordsOf(image, target);
        uint64_t inlineHeader = inlineValueHeaderBytes(image, target);
        uint64_t bodyBytes =
            targetValueWords * ZAMWORD_BYTES - inlineHeader;
        for (uint64_t word = 0; word < targetValueWords; word++)
            zam_thread_push(thread, 0);
        uint8_t *destination =
            (uint8_t *)(thread->operands + thread->operandCount -
                        targetValueWords);

        // A markable value lands with its own fresh header: the one in the
        // payload box carries a sweep-list link and a contained bit for the
        // heap, which is wrong for a value standing on the stack. Reading
        // out leaves the body alone and lets markPushedValue decide what
        // the stack entry owns, as every push of a markable value does.
        if (isMarkableInstance(image, target)) {
           ZamMarkWord *destMark = zam_mark_word_of(destination);
            *destMark = (ZamMarkWord){
                .state = ZAMMARK_STATE_WHITE,
                .kind = ZAMOBJECT_INSTANCE,
                .contained = 1,
                .upvalue = 0,
            };
            *zam_object_header_of(destination) = (ZamObjectHeader){
                .shortID = target.shortID,
                .elementCount = 1,
            };
        }
        memcpy(destination + inlineHeader,
               (const uint8_t *)payloadBox + typedHeader, bodyBytes);

        markPushedValue(thread, image, target);
        return ZAMRESULT_CONTINUE;
    }

    // The value's first word is its discriminant, the byte offset of its
    // primitive from the first value's, and the rest of it is spent unread.
    // The primitive's slot is pushed the way OpPushStatic pushes one.
    case OpEnumValue: {
        const ZamImage *image = &thread->vm->image;
       ZamTypeID type = zam_thread_pop_type(thread);
       ZamWord discriminant =
            zam_thread_peek(thread, valueWordsOf(image, type) - 1);
        popValue(thread, image, type);

        const ZamEnumDefinition *d =
            zam_enum_definition(image, ZAMTYPE_DECL(type.shortID));
        if (d->valueCount == 0)
            zam_die(ZAMEXIT_PANIC, "enum %u is not backed by a primitive\n",
                    type.shortID);

        zam_thread_push(
            thread,
            (ZamWord)(uintptr_t)(zam_vm_static(thread->vm, d->valueSlot) +
                                 discriminant));
        return ZAMRESULT_CONTINUE;
    }

    // A word read as another type owns what a word of that type owns. A cast
    // is otherwise nothing done to the word, and whether the type is one the
    // collector visits may be known only once a template is instantiated.
    case OpMarkAs: {
       ZamTypeID type = zam_thread_pop_type(thread);
        (void)zam_thread_peek(thread, 0);
        zam_thread_pop_mark(thread, thread->operandCount - 1);
        markPushedValue(thread, &thread->vm->image, type);
        return ZAMRESULT_CONTINUE;
    }

    case ZAMOPCODE_COUNT:
        break;
    }

    zam_die(ZAMEXIT_PANIC, "%s is not dispatched\n",
            zam_opcode_name(instruction.opcode));
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

ZamResult zam_thread_call_method(ZamThread *thread, ZamWord receiver,
                                 uint64_t slot) {
    const ZamImage *image = &thread->vm->image;
   ZamTypeShort shortID =
        zam_object_header_of(zam_thread_location_address(thread, receiver))
            ->shortID;
   ZamMethodDefinition method = zam_type_method(image, shortID, slot);

    // The closure the body reads its receiver through lies in this frame
    // rather than in the heap: nothing outlives the call to need it, and a
    // collection running one of these for every object it reaches would
    // otherwise leave as much garbage as it found. It is marked as standing
    // inside nothing, so nothing mistakes it for an allocation.
    ZamWord storage[sizeof(ZamClosure) / ZAMWORD_BYTES + ZAMTYPE_ID_WORDS + 1];
   ZamClosure *closure = (ZamClosure *)storage;
    *zam_mark_word_of(closure) = (ZamMarkWord){
        .state = ZAMMARK_STATE_WHITE,
        .kind = ZAMOBJECT_REFERENCE_METHOD_CLOSURE,
        .contained = 1,
    };
    closure->header.type = (ZamObjectHeader){
        .shortID = ZAMTYPE_SHORT(method.typeListID, 0),
        .elementCount = 1,
    };
    closure->instruction = method.instruction;
    closure->captureCount = 0;

    // The receiver is written under a plain word's type, being an address the
    // body reads and not a reference the closure owns.
    zam_type_id_write(
        closure->body, (ZamTypeID){.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_INT, 0)});
    closure->body[ZAMTYPE_ID_WORDS] = receiver;

    uint64_t outerEntry = thread->functionEntry;
    zam_thread_push(thread, (ZamWord)(uintptr_t)closure);
    zam_thread_push_function(thread, thread->instructionOffset,
                             thread->operandCount - 1);
    thread->instructionOffset = method.instruction;
    return runEntered(thread, outerEntry);
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

// What a spawned system thread runs. pthread wants a void* in and out, which
// is the whole of the shape; zam_thread_run is what the body does, which is
// everything else.
static void *threadMain(void *arg) {
    ZamThread *thread = (ZamThread *)arg;
    zam_thread_run(thread);
    zam_vm_thread_exited(thread);
    return NULL;
}

// dispatchSignalClosure synthesizes the frame a zam-side call would have built
// and jumps into the closure's body. Reached from pollSignalHandlers between
// instructions, so thread->instructionOffset is where control resumes once
// the closure's OpReturnVoid pops the frame.
//
// The frame is the one OpCallMethod expects: the closure address at slot 0,
// and any captures above it. For a Signal.Handle closure the parameter list
// is empty (the signature is void()), so there are no caller-pushed args to
// put between the two.
static void dispatchSignalClosure(ZamThread *thread, void *closurePtr) {
    const ZamImage *image = &thread->vm->image;
   ZamClosure *closure = zam_closure_of(closurePtr);

    zam_thread_push(thread, (ZamWord)(uintptr_t)closurePtr);
    zam_thread_push_mark(thread, thread->operandCount - 1);
    uint64_t base = thread->operandCount - 1;

    zam_thread_push_function(thread, thread->instructionOffset, base);
    thread->instructionOffset = closure->instruction;

    // Captures land past the (empty) argument list, which is where the body
    // was compiled to read them.
    if (closure->captureCount != 0) {
        const ZamWord *entry = closureCaptures(image, closure);
        for (uint64_t i = 0; i < closure->captureCount; i++) {
           ZamTypeID capType = zam_closure_entry_type(entry);
            const ZamWord *capValue = zam_closure_entry_value(entry);
            uint64_t capWords = valueWordsOf(image, capType);
            for (uint64_t w = 0; w < capWords; w++)
                zam_thread_push(thread, capValue[w]);
            markPushedValue(thread, image, capType);
            entry = zam_closure_entry_next(entry, capWords);
        }
    }
}

// pollSignalHandlers takes one pending signal, if there is one, and jumps
// into whatever closure Signal.Handle registered for it. Called between
// instructions so the dispatched closure runs on a frame whose return
// address is the instruction we were about to run next.
static void pollSignalHandlers(ZamThread *thread) {
    int sig = zam_thread_take_pending_signal(thread);
    if (sig < 0)
        return;
    void *closure = thread->signalHandlers[sig];
    if (closure == NULL)
        return;
    dispatchSignalClosure(thread, closure);
}

ZamResult zam_thread_run(ZamThread *thread) {
    // Bind this OS thread to the ZamThread so a signal reaching this thread
    // routes into its own pending mask. A thread that never binds would have
    // its signals dropped — which is the safe default rather than firing
    // whatever closures the previous OS thread had registered.
    zam_thread_bind_current(thread);
    for (;;) {
        zam_vm_safepoint(thread->vm, thread);
        pollSignalHandlers(thread);
        thread->result = step(thread);
        if (thread->result != ZAMRESULT_CONTINUE)
            return thread->result;
    }
}

ZamResult zam_vm_run(ZamVM *vm, int argC, const char *argV[]) {
   ZamThread *main = &vm->main;

    // Signals delivered to this OS thread route to the main ZamThread's own
    // pending mask. Must happen before Signal.Handle can be called.
    zam_thread_bind_current(main);

    // Main is entered on a frame like any other method, and being fallen into
    // rather than called there is no caller to have built it, so the words it
    // declares are laid here. No call is open over them, so
    // zam_thread_frame_base answers zero and the offsets are read straight off
    // the bottom of the stack.
    //
    // Slot 0 is the receiver — null, since main has none. Slot 1 is argC, the
    // variadic's count the body reads directly. Slot 2 is argV, a pointer at a
    // run of string words — one allocation per argument, the array itself a
    // flat run of pointers the way OpNewArray lays an array of a reference
    // type. An empty argv is a null slot, which matches OpNewArray's answer
    // when a variadic takes nothing. The run is the program's own memory, so
    // the strings in it are rooted by an entry naming the slot and the count,
    // which an empty one has no need of.
   ZamWord argvArray = 0;
    if (argC > 0) {
        argvArray = (ZamWord)(uintptr_t)zam_alloc((uint64_t)argC * ZAMWORD_BYTES);
       ZamWord *slots = (ZamWord *)(uintptr_t)argvArray;
        for (int i = 0; i < argC; i++)
            slots[i] = zam_native_new_string(main, argV[i], strlen(argV[i]));
    }
    zam_thread_push(main, 0);
    zam_thread_push(main, (ZamWord)(uint64_t)(unsigned)argC);
    zam_thread_push(main, argvArray);
    if (argC > 0)
        zam_thread_push_indirect_array_mark(
            main,
            (ZamTypeID){.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_STRING, 0),
                        .arrayLength = (uint64_t)argC},
            main->operandCount - 1);

    // Execution begins at operations offset zero, which is where the static
    // initializers were laid down. Control runs from them into the main
    // method's body, which is why main is entered by falling into it rather
    // than by being called, and why it ends with OpExit rather than a return.
    main->instructionOffset = 0;

    for (main->result = ZAMRESULT_CONTINUE;
         main->result == ZAMRESULT_CONTINUE;) {
        // A collection happens between instructions and never inside one: a
        // thread mid-instruction has operands on the stack that its mark stack
        // does not yet account for, so what the collector would see is not what
        // the program means.
        zam_vm_safepoint(vm, main);

        // Signals the C handler flagged while this thread was executing
        // dispatch here, between instructions, into whatever closure
        // Signal.Handle registered for them. The handler runs on the main
        // thread's own frame so its return lands right back where the signal
        // interrupted it.
        pollSignalHandlers(main);

        main->result = step(main);
    }

    zam_vm_thread_exited(main);
    return main->result;
}
