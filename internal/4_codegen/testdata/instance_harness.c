// A harness over the parts of an instance the runtime lays out itself.
//
// Most of an instance's shape comes off the image: a member sits where
//ZamMemberDefinition says it sits, so the two sides cannot disagree about it.
// What is left is the shape nothing on the wire states — how wide a primitive
// is, how a string is written, and how much header sits in front of a class —
// and that is exactly the part where a disagreement is silent. A member read
// one byte too narrow still reads.

#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "instance.h"

static void put(const char *name, uint64_t value) {
    printf("%s=%" PRIu64 "\n", name, value);
}

// The primitives are asked for by number, which is the point: ZamPrimitive is
// the leading run of syntax.TypeKind and nothing checks that but this. A kind
// inserted on the Go side shifts every primitive after it, and the shift shows
// up here as either a width that stopped matching or a number this runtime says
// it has never heard of.
static void primitivesDump(void) {
    put("primitiveCount", ZAMPRIMITIVE_COUNT);
    for (uint64_t i = 0; i < ZAMPRIMITIVE_COUNT; i++) {
        printf("primitive[%" PRIu64 "].bytes=%" PRIu64 "\n", i,
               zam_primitive_bytes((ZamPrimitive)i));
        printf("primitive[%" PRIu64 "].align=%" PRIu64 "\n", i,
               zam_primitive_align((ZamPrimitive)i));
    }
}

// The bytes on stdin are a whole static slot, exactly as the compiler interns a
// literal into the static region: a mark word and then the string. Nothing is
// copied on the way in — a literal is pushed as the address of its slot — so
// the shape read here is the shape written there, and the two sides have to
// agree on the width or the slot after it starts in the wrong place.
static void stringDump(const unsigned char *slot, size_t length) {
    // What the program holds is the slot, and the slot is the string: a string
    // begins with the mark word every markable value begins with, so reading one
    // is reading the pointer and no arithmetic. The length the compiler wrote
    // has to be the field the struct names, or a literal is the one string whose
    // bytes are somewhere else.
    const ZamString *string = zam_string_of((void *)slot);
    put("stringOffset", (uint64_t)((const unsigned char *)string - slot));

    put("length", string->length);
    // A byte at a time rather than as text, so a literal holding a zero byte is
    // reported as the string it is instead of being cut short at it. That a
    // string may hold one is the reason it is length-prefixed and not
    // terminated, so it is the case most worth covering.
    for (uint64_t i = 0; i < string->length; i++)
        printf("byte[%" PRIu64 "]=%u\n", i, (unsigned char)string->bytes[i]);
    // The slot's width worked out the way the runtime works one out: the header
    // a string gets, plus the string. Neither half is stated on the wire, so
    // this is the composition being checked as much as either part.
    put("byteCount", zam_object_header_bytes(ZAMOBJECT_STRING) +
                         zam_string_bytes(string->length));
    // What was actually handed over, so a padding rule that drifted shows up as
    // the two disagreeing rather than as both being wrong together.
    put("given", (uint64_t)length);
}

// The headers are the runtime's own and no part of them is on the wire, so what
// is worth asserting is not a number but the invariant the layout exists for:
// whichever of the three shapes an allocation has, the word the pointer names is
// the mark word. That is what lets the runtime learn what an allocation is
// before it knows how much header to expect, and each step forward after it is
// licensed by what the step before read.
static void headerDump(void) {
    put("markBytes", sizeof(ZamMarkWord));
    put("typedBytes", sizeof(ZamTypedObjectHeader));
    put("tailedBytes", sizeof(ZamTailedObjectHeader));
    put("stringHeaderBytes", zam_object_header_bytes(ZAMOBJECT_STRING));
    // All three kinds a closure is pay the same header. What the kind sorts out
    // is the first entry of the body, which begins in the same place whichever
    // of the three it turned out to be.
    put("functionClosureHeaderBytes",
        zam_object_header_bytes(ZAMOBJECT_FUNCTION_CLOSURE));
    put("referenceMethodClosureHeaderBytes",
        zam_object_header_bytes(ZAMOBJECT_REFERENCE_METHOD_CLOSURE));
    put("valueMethodClosureHeaderBytes",
        zam_object_header_bytes(ZAMOBJECT_VALUE_METHOD_CLOSURE));
    put("objectHeaderBytes",
        zam_object_header_bytes(ZAMOBJECT_INSTANCE));
    put("tailedHeaderBytes",
        zam_object_header_bytes(ZAMOBJECT_TAILED_INSTANCE));
    // The one header no kind answers for, a tailed struct carrying no mark word
    // to hold a kind in. It is reported here so the compiler's own rule can be
    // held to it beside the four that are.
    put("tailedStructHeaderBytes", ZAMTAILED_STRUCT_HEADER_BYTES);

   ZamWord buffer[8];
    memset(buffer, 0, sizeof(buffer));

    // A string: the mark word and nothing else. That one word is the whole of
    // its header, and there is no type id after it to read.
   ZamMarkWord *string = (ZamMarkWord *)buffer;
    string->state = ZAMMARK_STATE_GREY;
    string->kind = ZAMOBJECT_STRING;
    string->upvalue = 0xFEEDBEEF;
    put("string.state", zam_mark_word_of(buffer)->state);
    put("string.kind", zam_mark_word_of(buffer)->kind);
    put("string.upvalue", zam_mark_word_of(buffer)->upvalue);

    // An object: the mark word, then the type id, then the members.
    memset(buffer, 0, sizeof(buffer));
   ZamTypedObjectHeader *typed = (ZamTypedObjectHeader *)buffer;
    typed->type.shortID = (9u << 16) | 3u;
    typed->type.elementCount = 5;
    typed->mark.state = ZAMMARK_STATE_BLACK;
    typed->mark.kind = ZAMOBJECT_INSTANCE;
    put("typed.kind", zam_mark_word_of(buffer)->kind);
    put("typed.state", zam_mark_word_of(buffer)->state);
    put("typed.shortID", zam_object_header_of(buffer)->shortID);
    put("typed.elementCount", zam_object_header_of(buffer)->elementCount);

    // A tailed object: mark word, type id, length, then the members. Every step
    // forward has to land on the same field it did above.
    memset(buffer, 0, sizeof(buffer));
   ZamTailedObjectHeader *tailed = (ZamTailedObjectHeader *)buffer;
    tailed->tailLength = 12;
    tailed->head.type.shortID = (7u << 16) | 2u;
    tailed->head.type.elementCount = 4;
    tailed->head.mark.state = ZAMMARK_STATE_WHITE;
    tailed->head.mark.kind = ZAMOBJECT_TAILED_INSTANCE;
    put("tailed.kind", zam_mark_word_of(buffer)->kind);
    put("tailed.shortID", zam_object_header_of(buffer)->shortID);
    put("tailed.elementCount", zam_object_header_of(buffer)->elementCount);
    put("tailed.tailLength", zam_object_tail_length_of(buffer));

    // The upvalue is the whole of the rest of the word, so the largest one that
    // fits has to come back as itself rather than truncated into the kind.
    memset(buffer, 0, sizeof(buffer));
   ZamMarkWord *wide = (ZamMarkWord *)buffer;
    wide->kind = ZAMOBJECT_THREAD;
    wide->upvalue = ((uint64_t)1 << ZAMUPVALUE_BITS) - 1;
    put("wide.kind", zam_mark_word_of(buffer)->kind);
    put("wide.upvalue", zam_mark_word_of(buffer)->upvalue);
}

// writeEntry lays one closure entry down the way OpNewLambda would: the type id
// in the form the operand stack carries it, with the short id in the high half
// of the first word, then the value's words beneath it.
static ZamWord *writeEntry(ZamWord *at, ZamTypeShort shortID,
                           const ZamWord *value, uint64_t valueWords) {
    at[0] = (ZamWord)shortID << 32;
    at[1] = 0;
    at[2] = 0;
    at += ZAMTYPE_ID_WORDS;
    for (uint64_t i = 0; i < valueWords; i++)
        at[i] = value[i];
    return at + valueWords;
}

// entryWords is how wide the value under a type id is. A collector reads this
// off the type's definition; this harness has no image, so a primitive comes
// off zam_primitive_bytes and the one declared type here stands for a pointer
// receiver, which is a word however large the thing it points at is.
static uint64_t entryWords(ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    if (declID >= ZAMPRIMITIVE_COUNT)
        return 1;
    uint64_t bytes = zam_primitive_bytes((ZamPrimitive)declID);
    return zam_align_up(bytes, ZAMWORD_BYTES) / ZAMWORD_BYTES;
}

// A closure is written and then walked the way the collector walks one. Nothing
// here is on the wire either — no image states a closure's shape — so what is
// being asserted is that the encoding is traversable at all: every entry is
// found only through the one before it, so an entry whose type did not say how
// wide its value was would strand every entry after it.
//
// The receiver is entry zero and is read by the same three accessors as the
// captures, which is the point of writing it in the same form. The loop below
// has no case for it.
static void closureDump(void) {
   ZamWord buffer[32];
    memset(buffer, 0, sizeof(buffer));

    // Written where a closure actually lies, which is the allocation itself: the
    // typed header is the closure's own first field, so the accessor steps
    // nothing and the words counted below count it.
   ZamClosure *closure = zam_closure_of(buffer);
    put("closureOffset", (uint64_t)((ZamWord *)closure - buffer));
    closure->instruction = 64;
    closure->captureCount = 2;

    // A pointer receiver of some class, then an int capture and a string
    // capture. The string is the one that matters to a collector: it is a word
    // that owns what it points at, and only its type says so.
    const ZamWord receiver[] = {0x1111};
    const ZamWord capturedInt[] = {7};
    const ZamWord capturedString[] = {0x2222};
   ZamWord *at = closure->body;
    at = writeEntry(at, ZAMTYPE_SHORT(9, 3), receiver, 1);
    at = writeEntry(at, ZAMTYPE_SHORT(ZAMPRIMITIVE_INT, 0), capturedInt, 1);
    at = writeEntry(at, ZAMTYPE_SHORT(ZAMPRIMITIVE_STRING, 0), capturedString,
                    1);
    put("closureWords", (uint64_t)(at - (ZamWord *)closure));

    put("instruction", closure->instruction);
    put("captureCount", closure->captureCount);

    const ZamWord *entry = zam_closure_first(closure);
    for (uint64_t i = 0; i <= closure->captureCount; i++) {
       ZamTypeID type = zam_closure_entry_type(entry);
        printf("entry[%" PRIu64 "].shortID=%" PRIu32 "\n", i, type.shortID);
        printf("entry[%" PRIu64 "].value=%" PRIu64 "\n", i,
               zam_closure_entry_value(entry)[0]);
        entry = zam_closure_entry_next(entry, entryWords(type));
    }

    // The walk has to end exactly where writing ended. Anywhere else and the
    // widths the types gave back are not the widths the values were written at.
    put("walkedWords", (uint64_t)(entry - (ZamWord *)closure));
}

int main(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "usage: %s <what>\n", argv[0]);
        return 2;
    }

    const char *what = argv[1];
    if (strcmp(what, "primitives") == 0) {
        primitivesDump();
        return 0;
    }
    if (strcmp(what, "header") == 0) {
        headerDump();
        return 0;
    }
    if (strcmp(what, "closure") == 0) {
        closureDump();
        return 0;
    }
    if (strcmp(what, "string") != 0) {
        fprintf(stderr, "unknown subject %s\n", what);
        return 2;
    }

    size_t capacity = 1 << 16;
    size_t length = 0;
    unsigned char *bytes = malloc(capacity);
    if (bytes == NULL)
        return 2;

    size_t got;
    while ((got = fread(bytes + length, 1, capacity - length, stdin)) > 0) {
        length += got;
        if (length == capacity) {
            capacity *= 2;
            unsigned char *grown = realloc(bytes, capacity);
            if (grown == NULL) {
                free(bytes);
                return 2;
            }
            bytes = grown;
        }
    }

    stringDump(bytes, length);
    free(bytes);
    return 0;
}
