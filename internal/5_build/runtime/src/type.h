#pragma once

#include "common.h"

// A type is named by a short id: which declaration, and which instantiation of
// it. LinkedList[int] and LinkedList[string] share a declaration id and differ
// in the generic id. A non-generic declaration's generic id is zero.
typedef uint32_t ZamTypeShort;

#define ZAMTYPE_DECL(shortID) ((uint16_t)((shortID) >> 16))
#define ZAMTYPE_GENERIC(shortID) ((uint16_t)(shortID))
#define ZAMTYPE_SHORT(declID, genericID)                                      \
    (((ZamTypeShort)(declID) << 16) | (ZamTypeShort)(genericID))

// A type id is the one way a type is named, wherever it is named: a member of a
// definition, an entry in a signature, a pointer's pointee, or three words
// standing on the operand stack above a value. The short id occupies the high
// half of the first word, which leaves the low half free for whatever a given
// table wants to pack beside it, then the array length and the tail length.
//
// One encoding rather than a narrow one for definitions and a wide one for the
// stack, so nothing walking a table has to know which of two widths the entry it
// is stepping over was written as.
typedef struct ZamTypeID {
   ZamTypeShort shortID;
    uint64_t arrayLength;
    uint64_t tailLength;
} ZamTypeID;

// ZAMTYPE_ID_WORDS is how much of the operand stack a type id occupies, which
// is what every instruction that pops one has to step back over.
#define ZAMTYPE_ID_WORDS 3

//ZamTypeIDWords is the same three words as they are written, rather than as
// they are used.ZamTypeID above cannot be laid over them: its shortID is a
// uint32_t at the front, while the wire puts the short id in the high half of
// the first word. This is the form anything that holds a type id in the image
// is cast to, and zam_type_id_read below is how it turns into a ZamTypeID.
typedef struct ZamTypeIDWords {
    uint64_t typeWord;
    uint64_t arrayLength;
    uint64_t tailLength;
} ZamTypeIDWords;

_Static_assert(sizeof(ZamTypeIDWords) == ZAMTYPE_ID_WORDS * ZAMWORD_BYTES,
               "type id no longer matches the wire");

// The primitive type ids. A primitive's id is its kind directly and points at
// no definition, which is why the type definition map runs from zero and leaves
// these entries unread rather than starting at the first declaration.
//
// These are the leading values of syntax.TypeKind in the compiler, in that
// order. They are written down rather than read out of the image because the
// image carries no definition for them to be read from — a primitive is a type
// every side already knows everything about.
typedef enum ZamPrimitive {
    ZAMPRIMITIVE_VOID = 0,
    ZAMPRIMITIVE_BOOL = 1,
    ZAMPRIMITIVE_CHAR = 2,
    ZAMPRIMITIVE_BYTE = 3,
    ZAMPRIMITIVE_INT = 4,
    ZAMPRIMITIVE_FLOAT = 5,
    ZAMPRIMITIVE_STRING = 6,
    ZAMPRIMITIVE_TYPE = 7,
    // An any value on the stack is one word — a pointer to the any box that
    // holds its type id and payload. The box itself is a markable heap
    // allocation of its own, so this width is only for a value in a slot or
    // on the operand stack, the same way ZAMPRIMITIVE_STRING is the pointer
    // and not the bytes it addresses.
    ZAMPRIMITIVE_ANY = 8,
    // An unsigned 64-bit integer, the same width as INT and sharing every
    // bit-identical arithmetic op with it. Comparison and ordering read the
    // word as unsigned; everything else treats the two the same.
    ZAMPRIMITIVE_UINT = 9,

    // ZAMPRIMITIVE_COUNT is where the declared types begin. Everything below
    // it is a primitive and points at no definition; everything from it up
    // indexes the type definition map.
    ZAMPRIMITIVE_COUNT,
} ZamPrimitive;

// zam_type_id_read decodes the three words a type id is written as, wherever
// they were read from.
static inline ZamTypeID zam_type_id_read(const ZamWord *words) {
    return (ZamTypeID){
        .shortID = (ZamTypeShort)(words[0] >> 32),
        .arrayLength = words[1],
        .tailLength = words[2],
    };
}

// zam_type_id_write lays a type id down in that same wire form, which is what a
// closure holding one of its own has to write rather than read.
static inline void zam_type_id_write(ZamWord *words, ZamTypeID type) {
    words[0] = (ZamWord)type.shortID << 32;
    words[1] = type.arrayLength;
    words[2] = type.tailLength;
}
