#pragma once

#include "common.h"
#include "type.h"

// The header, word by word, in the order counts.words() writes it. An index
// here is a word index into the file from byte zero.
//
// Every offset the header carries is measured from the end of the header rather
// than from the start of the file, so the first byte after this is offset zero.
//ZamImage.body is that byte, and every section pointer is found from it.
typedef enum ZamHeaderWord {
    ZAMHEADER_MAGIC = 0,
    ZAMHEADER_VERSION,
    ZAMHEADER_TYPE_DEFINITION_COUNT,

    // The start index of each type definition bucket. The buckets are laid out
    // in this order and a type id is an index into all of them run together, so
    // each bucket's start is where the one before it ended, and the bucket a
    // type falls in is found by comparing its id against these.
    ZAMHEADER_THREAD_TYPES_START,
    ZAMHEADER_FUNCTION_TYPES_START,
    ZAMHEADER_POINTERS_START,
    ZAMHEADER_STRUCTS_START,
    ZAMHEADER_GENERIC_STRUCTS_START,
    ZAMHEADER_ENUMS_START,
    ZAMHEADER_CLASSES_START,
    ZAMHEADER_GENERIC_CLASSES_START,
    ZAMHEADER_ABSTRACTS_START,
    ZAMHEADER_GENERIC_ABSTRACTS_START,
    ZAMHEADER_INTERFACES_START,
    ZAMHEADER_GENERIC_INTERFACES_START,

    ZAMHEADER_STATIC_SLOTS_OFFSET,
    ZAMHEADER_STATIC_REGION_BYTES,
    ZAMHEADER_STATIC_IMAGE_OFFSET,
    ZAMHEADER_NATIVES_OFFSET,
    ZAMHEADER_GENERIC_STATIC_METHODS_OFFSET,
    ZAMHEADER_STATIC_ROOTS_OFFSET,
    ZAMHEADER_OPERATIONS_OFFSET,

    ZAMHEADER_WORDS,
} ZamHeaderWord;

// The version the compiler stamps an image with, packed as one word so that a
// runtime can reject an image with a single comparison and still say which
// version it was handed. This runtime reads exactly one version: the format is
// still being decided, so there is nothing yet to be compatible with.
#define ZAMVERSION_MAJOR 0
#define ZAMVERSION_MINOR 0
#define ZAMVERSION_PATCH 0
#define ZAMVERSION_WORD                                                       \
    (((ZamWord)ZAMVERSION_MAJOR << 32) | ((ZamWord)ZAMVERSION_MINOR << 16) | \
     (ZamWord)ZAMVERSION_PATCH)

// The bucket a declared type id falls in. What a type definition means is
// entirely a matter of which bucket it came out of — the definitions are not
// self-describing and carry no tag — so this is read off the header's start
// indices before any definition is looked at.
typedef enum ZamTypeBucket {
    ZAMBUCKET_PRIMITIVE,
    ZAMBUCKET_THREAD_TYPE,
    ZAMBUCKET_FUNCTION_TYPE,
    ZAMBUCKET_POINTER,
    ZAMBUCKET_STRUCT,
    ZAMBUCKET_GENERIC_STRUCT,
    ZAMBUCKET_ENUM,
    ZAMBUCKET_CLASS,
    ZAMBUCKET_GENERIC_CLASS,
    ZAMBUCKET_ABSTRACT,
    ZAMBUCKET_GENERIC_ABSTRACT,
    ZAMBUCKET_INTERFACE,
    ZAMBUCKET_GENERIC_INTERFACE,
} ZamTypeBucket;

//ZamImage is a whole image file held in one allocation and read where it lies.
// Nothing is decoded on load beyond the header: the sections are word-aligned
// and little-endian, which is the layout a pointer into the buffer already has,
// so a definition is reached by arithmetic rather than by having been copied
// into a struct on the way in.
typedef struct ZamImage {
    // bytes is the whole file, read in place.
    uint8_t *bytes;
    size_t byteCount;

    // ownsBytes is true when the bytes above were allocated by the loader and
    // must be freed on close, as zam_image_open does. An image loaded from a
    // byte array baked into the binary (zam_image_load) does not own them.
    bool ownsBytes;

    // header is the first ZAMHEADER_WORDS words, and body is the byte after
    // them, which every offset in the header is measured from.
    const ZamWord *header;
    const uint8_t *body;

    // The three words of runtime configuration, which are the first thing in
    // the body. They are how the program asks to be run rather than anything
    // about the program, which is why they are copied out here and the sections
    // after them are not.
    double gcTriggerRatio;
    uint64_t initialStackSize;
    uint64_t initialHeapSize;

    // typeDefinitionMap gives the body-relative byte offset of each type's
    // definition, indexed by type id. Definitions are not all the same width,
    // so this is the only way to reach the nth one. The entries below
    // ZAMPRIMITIVE_COUNT are zero and are never read.
    const ZamWord *typeDefinitionMap;
    uint64_t typeDefinitionCount;

    // staticSlots gives the byte offset within the static region of each static
    // slot. One index space is shared by string literals, static members and
    // static methods, since a static is named on the wire by a slot index
    // however it was declared.
    const ZamWord *staticSlots;
    uint64_t staticSlotCount;

    // staticRegionByteCount is how much room the statics need between them. The
    // slot map says where each one begins but never how wide it is, so the end
    // of the last one is not something that could be worked out from the map.
    uint64_t staticRegionByteCount;

    // staticImage is what the static region begins as, laid out exactly as it
    // sits at the head of it. The loader copies it there rather than
    // interpreting it: a literal's slot address is already the string, and a
    // static holding a markable value already carries the header it is read
    // through.
    //
    // It is not the whole region. Everything the compiler had to say about the
    // region is at the front of it, so the image stops at the last such byte and
    // the rest of the region is the zeroes an unassigned static reads as.
    const uint8_t *staticImage;
    size_t staticImageByteCount;

    // natives is the table of C symbol names, opening with how many follow.
    // Each is laid out as a literal is. What stands in a slot at run time is
    // resolved against the table the interpreter was built with.
    const uint8_t *natives;
    uint64_t nativeCount;

    // genericStaticMethods is the runtime dispatch table for generic static
    // methods called with open-generic type arguments. It opens with the entry
    // count, then one entry per generic method the compiler needed a dispatch
    // for: the method's arity and the run of concretes, each written as its
    // type arguments (arity * typeInstance), its static slot, and the function
    // type id its body was emitted as. OpPushGenericStaticSlot walks it.
    const uint8_t *genericStaticMethods;
    uint64_t genericStaticMethodCount;

    // staticRoots is every static whose value may hold something the
    // collector must visit, opening with how many follow: each is its slot
    // and then the three words of its type id.
    const ZamWord *staticRoots;
    uint64_t staticRootCount;

    // operations is the program, and runs to the end of the file. Execution
    // begins at offset zero of it.
    const uint8_t *operations;
    size_t operationsByteCount;
} ZamImage;

// zam_image_open reads the file and validates the header. It dies rather than
// returning a failure: an image that cannot be read is the end of the run, and
// there is no caller with anything better to do about it.
void zam_image_open(ZamImage *image, const char *path);

// zam_image_load takes an image whose bytes already sit in memory — the usual
// case for a program that was compiled against the runtime, where the compiler
// baked the image into the binary as a static byte array. The bytes are read
// in place and are not freed on close, so the caller may hand in storage of
// any lifetime and need not keep a second pointer to free.
void zam_image_load(ZamImage *image, const uint8_t *bytes, size_t byteCount);

void zam_image_close(ZamImage *image);

// zam_image_type_definition is where the type's definition begins. What is
// there depends on which bucket the id falls in, which zam_image_bucket_of
// answers; a primitive has no definition and asking for one is a bug.
const uint8_t *zam_image_type_definition(const ZamImage *image,
                                         uint16_t declID);

// zam_image_bucket_of says which kind of definition a type id names.
ZamTypeBucket zam_image_bucket_of(const ZamImage *image, uint16_t declID);

// zam_image_static_offset is where the slot begins within the static region.
uint64_t zam_image_static_offset(const ZamImage *image, uint64_t slot);

// ZAMSTATIC_ROOT_WORDS is how wide one entry of the static root table is: the
// slot and then the type id.
#define ZAMSTATIC_ROOT_WORDS (1 + ZAMTYPE_ID_WORDS)

// zam_image_static_root is the slot of the nth static root, and the type of
// the value it holds.
uint64_t zam_image_static_root(const ZamImage *image, uint64_t index,
                               ZamTypeID *type);

// zam_image_native_name is the C symbol the slot names, as a pointer into the
// image and a length. It is not NUL-terminated: a name is written as a length
// and its bytes, the way a string literal is.
const char *zam_image_native_name(const ZamImage *image, uint64_t slot,
                                  uint64_t *byteCount);

// zam_image_generic_static_method_entry is the pointer to the id's entry in
// the generic static method registry, together with its arity and concrete
// count. The entry's concretes follow it as a flat run of (arity typeInstance
// words, slot word, 3 typeInstance words) each — the shape readers used to
// pick a slot by matching the type arguments on the stack walk through it.
const uint8_t *zam_image_generic_static_method_entry(const ZamImage *image,
                                                     uint64_t id,
                                                     uint64_t *arity,
                                                     uint64_t *concreteCount);
