#pragma once

#include "common.h"
#include "image.h"
#include "type.h"

// A type definition is not parsed, it is pointed at. The compiler writes each
// one head first — every fixed-width field it has, counts and scalars alike —
// and only then the runs those counts measure, so the bytes in the image are
// already the layout of a C struct and reading a definition is a cast.
//
// Each kind below is therefore that kind's wire layout exactly: its head as
// named fields, then a flexible member holding the runs. What the head cannot
// give a name to is the runs themselves, since where the second one starts
// depends on how long the first is; those are reached by the accessors, which
// are arithmetic on the head rather than a walk through the data.
//
// The maps lie inline in the definition that owns them, not off a pointer, so a
// small supertype set is in the same cache line as the words that led to it.
//
// The flexible member is written [] rather than [0]: the zero-length form is a
// GNU extension the build rejects under -Wpedantic -Werror. Nothing here is ever
// embedded in another struct by value, which is the one thing [] would forbid.
//
// There is a definition kind per bucket rather than one struct with the union of
// their fields. Definitions are not self-describing: what a run of words means
// is decided entirely by the bucket its type id fell in, so the reader has to be
// chosen by bucket in any case, and a kind that carried a field it never has
// would be answering a question about itself with a zero.

//ZamPerfectHash is a built two-level FKS table, as the compiler wrote it: a
// seed, then one word per bucket holding its block's start in the high half and
// the block's own seed in the low half, then one word per slot. The buckets run
// one past the last, so every bucket's width is the distance to the next.
//
// The buckets and the slots are one member rather than two because that is what
// they are on the wire, one run with the counts above saying where to cut it.
typedef struct ZamPerfectHash {
    uint64_t seed;
    uint64_t slotCount;
    // bucketCount is one more than the number of keys the table was built over.
    uint64_t bucketCount;
   ZamWord bucketsAndSlots[];
} ZamPerfectHash;

_Static_assert(sizeof(ZamPerfectHash) == 3 * ZAMWORD_BYTES,
               "perfect hash head no longer matches the wire");

#define ZAMPERFECT_HASH_HEAD_WORDS 3

static inline const ZamWord *zam_perfect_hash_buckets(const ZamPerfectHash *t) {
    return t->bucketsAndSlots;
}

static inline const ZamWord *zam_perfect_hash_slots(const ZamPerfectHash *t) {
    return t->bucketsAndSlots + t->bucketCount;
}

// zam_perfect_hash_word_count is how far past a table the next thing lies. An
// empty table is still its three head words, which is the shape the compiler
// writes rather than nothing at all.
static inline uint64_t zam_perfect_hash_word_count(const ZamPerfectHash *t) {
    return ZAMPERFECT_HASH_HEAD_WORDS + t->bucketCount + t->slotCount;
}

static inline const ZamPerfectHash *
zam_perfect_hash_next(const ZamPerfectHash *t) {
    return (const ZamPerfectHash *)((const ZamWord *)t +
                                    zam_perfect_hash_word_count(t));
}

// zam_perfect_hash_lookup resolves a key, and is false when the table can be
// seen to hold no answer — it was built over no keys, or the key landed in a
// bucket no key was sorted into.
//
// A true return does not mean the key was built in. The table records nothing
// about which keys it holds, so a key that was never added lands in some other
// key's slot and reads that one's value. Only ask this about a key already known
// to be present, which for dispatch the type checker has proven; anything else
// wants zam_supertype_set_contains, which checks the answer.
bool zam_perfect_hash_lookup(const ZamPerfectHash *table, uint64_t key,
                             uint32_t *value);

// zam_supertype_set_contains asks the membership question the raw lookup cannot
// answer. Every key in a supertype set is stored as its own value, so a type is
// in the set exactly when looking it up gives it back.
bool zam_supertype_set_contains(const ZamPerfectHash *set, ZamTypeShort type);

// zam_dispatch_lookup says which of a class's methods an interface method names.
// The key is the interface's short id in the high half and the method index in
// the low, which is the one word the compiler hashed.
bool zam_dispatch_lookup(const ZamPerfectHash *table, ZamTypeShort interfaceType,
                         uint32_t interfaceMethodIndex, uint32_t *methodIndex);

// A method is where its body begins and the id of the function type naming its
// signature.
typedef struct ZamMethodDefinition {
    uint64_t instruction;
    uint64_t typeListID;
} ZamMethodDefinition;

// A member is where it lies within an instance and what lies there. The type is
// held as the words the wire carries rather than as an ZamTypeID, because that
// is what lets an array of these be the image's own bytes: typeWord holds the
// short id in its high half, exactly as everywhere else.
typedef struct ZamMemberDefinition {
    uint64_t offset;
    uint64_t typeWord;
    uint64_t arrayLength;
    uint64_t tailLength;
} ZamMemberDefinition;

_Static_assert(sizeof(ZamMethodDefinition) == 2 * ZAMWORD_BYTES,
               "method definition no longer matches the wire");
_Static_assert(sizeof(ZamMemberDefinition) == ZAMWORD_BYTES +
                                                  ZAMTYPE_ID_WORDS *
                                                      ZAMWORD_BYTES,
               "member definition no longer matches the wire");

#define ZAMMETHOD_WORDS 2
#define ZAMMEMBER_WORDS (1 + ZAMTYPE_ID_WORDS)

static inline ZamTypeID zam_member_type(const ZamMemberDefinition *member) {
    return zam_type_id_read(&member->typeWord);
}

//ZamTypeList is a counted run of type ids, which is how a signature and a set
// of generic arguments are both written.
typedef struct ZamTypeList {
    uint64_t count;
   ZamWord types[];
} ZamTypeList;

_Static_assert(sizeof(ZamTypeList) == ZAMWORD_BYTES,
               "type list head no longer matches the wire");

static inline ZamTypeID zam_type_list_at(const ZamTypeList *list,
                                         uint64_t index) {
    return zam_type_id_read(&list->types[index * ZAMTYPE_ID_WORDS]);
}

static inline const ZamTypeList *zam_type_list_next(const ZamTypeList *list) {
    return (const ZamTypeList *)((const ZamWord *)list + 1 +
                                 list->count * ZAMTYPE_ID_WORDS);
}

// Every definition below that describes an instance carries an
// initializerInstruction: the routine that fills a fresh instance in, which is
// entered as a method is. Zero names no routine, since offset zero is where the
// program begins and so is never an initializer.

// A struct is its methods, its members, how much room an instance takes, and the
// routine that fills a fresh one in.
typedef struct ZamStructDefinition {
    uint64_t methodCount;
    uint64_t memberCount;
    uint64_t instanceByteSize;
    uint64_t instanceTailByteSize;
    uint64_t initializerInstruction;
   ZamWord body[];
} ZamStructDefinition;

static inline const ZamMethodDefinition *
zam_struct_methods(const ZamStructDefinition *d) {
    return (const ZamMethodDefinition *)d->body;
}

static inline const ZamMemberDefinition *
zam_struct_members(const ZamStructDefinition *d) {
    return (const ZamMemberDefinition *)(d->body +
                                         d->methodCount * ZAMMETHOD_WORDS);
}

// An enum is a struct that carries no tail: its instances are the values named
// in the declaration and none of them is variable-length.
typedef struct ZamEnumDefinition {
    uint64_t methodCount;
    uint64_t memberCount;
    uint64_t instanceByteSize;
    uint64_t initializerInstruction;
   ZamWord body[];
} ZamEnumDefinition;

static inline const ZamMethodDefinition *
zam_enum_methods(const ZamEnumDefinition *d) {
    return (const ZamMethodDefinition *)d->body;
}

static inline const ZamMemberDefinition *
zam_enum_members(const ZamEnumDefinition *d) {
    return (const ZamMemberDefinition *)(d->body +
                                         d->methodCount * ZAMMETHOD_WORDS);
}

// An abstract is laid out nowhere, so no instance is ever one and nothing
// dispatches through it — which is why it has no dispatch table. It carries a
// supertype set all the same, because a subtype test can be asked about an
// abstract rather than through one.
typedef struct ZamAbstractDefinition {
    uint64_t methodCount;
    uint64_t memberCount;
    uint64_t instanceByteSize;
    uint64_t instanceTailByteSize;
    uint64_t initializerInstruction;
   ZamWord body[];
} ZamAbstractDefinition;

static inline const ZamMethodDefinition *
zam_abstract_methods(const ZamAbstractDefinition *d) {
    return (const ZamMethodDefinition *)d->body;
}

static inline const ZamMemberDefinition *
zam_abstract_members(const ZamAbstractDefinition *d) {
    return (const ZamMemberDefinition *)(d->body +
                                         d->methodCount * ZAMMETHOD_WORDS);
}

static inline const ZamPerfectHash *
zam_abstract_supertypes(const ZamAbstractDefinition *d) {
    return (const ZamPerfectHash *)(d->body +
                                    d->methodCount * ZAMMETHOD_WORDS +
                                    d->memberCount * ZAMMEMBER_WORDS);
}

// A class is an abstract that also answers which of its methods an interface
// method names, which is the one question an abstract is never asked. Its head
// is an abstract's, so the two share every accessor but the last.
typedef struct ZamClassDefinition {
    uint64_t methodCount;
    uint64_t memberCount;
    uint64_t instanceByteSize;
    uint64_t instanceTailByteSize;
    uint64_t initializerInstruction;
   ZamWord body[];
} ZamClassDefinition;

_Static_assert(sizeof(ZamClassDefinition) == sizeof(ZamAbstractDefinition),
               "a class head is an abstract head");

static inline const ZamAbstractDefinition *
zam_class_as_abstract(const ZamClassDefinition *d) {
    return (const ZamAbstractDefinition *)d;
}

static inline const ZamMethodDefinition *
zam_class_methods(const ZamClassDefinition *d) {
    return zam_abstract_methods(zam_class_as_abstract(d));
}

static inline const ZamMemberDefinition *
zam_class_members(const ZamClassDefinition *d) {
    return zam_abstract_members(zam_class_as_abstract(d));
}

static inline const ZamPerfectHash *
zam_class_supertypes(const ZamClassDefinition *d) {
    return zam_abstract_supertypes(zam_class_as_abstract(d));
}

// The dispatch table follows the supertype set, whose width is in its own head,
// so this is the one field here reached through another rather than from the
// head alone.
static inline const ZamPerfectHash *
zam_class_dispatch(const ZamClassDefinition *d) {
    return zam_perfect_hash_next(zam_class_supertypes(d));
}

// An interface is laid out nowhere and dispatched to through the record of
// whatever class an instance turns out to be, so all it answers for is what
// stands above it — which is to say its definition is a supertype set and
// nothing else, and is read as one.
typedef ZamPerfectHash ZamInterfaceDefinition;

// A generic map entry is everything about one instantiation that its declaration
// cannot say. The supertypes are the instantiation's own, since a method
// implementing an interface only once T is bound makes C[int] implement what
// C[string] does not.
//
// The supertype set comes last of the runs. Every other run here is fixed-width,
// so the set is the one thing whose end has to be read rather than computed, and
// putting it last is what lets a class entry's dispatch table begin directly
// after it.
typedef struct ZamGenericMapEntry {
    uint64_t instanceByteSize;
    uint64_t instanceTailByteSize;
    uint64_t genericTypeCount;
    uint64_t memberCount;
    uint64_t methodTypeCount;
   ZamWord body[];
} ZamGenericMapEntry;

// The generic arguments this instantiation was instantiated over.
static inline const ZamWord *
zam_generic_entry_generic_types(const ZamGenericMapEntry *e) {
    return e->body;
}

static inline ZamTypeID
zam_generic_entry_generic_type_at(const ZamGenericMapEntry *e, uint64_t index) {
    return zam_type_id_read(&e->body[index * ZAMTYPE_ID_WORDS]);
}

static inline const ZamMemberDefinition *
zam_generic_entry_members(const ZamGenericMapEntry *e) {
    return (const ZamMemberDefinition *)(e->body +
                                         e->genericTypeCount *
                                             ZAMTYPE_ID_WORDS);
}

// This instantiation's signatures: the id of the function type each method has
// once T is bound, one word each, rather than the signature spelled out. C[int].f
// and C[string].f are different signatures, but each is a ground function type
// the program uses and so is already a definition with an id of its own — which
// is what lets a method of an instantiation name its type the same way a method
// of a plain declaration does.
static inline const ZamWord *
zam_generic_entry_method_type_ids(const ZamGenericMapEntry *e) {
    return e->body + e->genericTypeCount * ZAMTYPE_ID_WORDS +
           e->memberCount * ZAMMEMBER_WORDS;
}

static inline const ZamPerfectHash *
zam_generic_entry_supertypes(const ZamGenericMapEntry *e) {
    return (const ZamPerfectHash *)(zam_generic_entry_method_type_ids(e) +
                                    e->methodTypeCount);
}

// zam_generic_entry_next is the entry after this one. A class's entries each
// carry a dispatch table after them and an abstract's do not, so which follows
// is not something the entry itself can say.
const ZamGenericMapEntry *zam_generic_entry_next(const ZamGenericMapEntry *entry,
                                                 bool isClass);

static inline const ZamPerfectHash *
zam_generic_entry_dispatch(const ZamGenericMapEntry *e) {
    return zam_perfect_hash_next(zam_generic_entry_supertypes(e));
}

// A generic class or abstract holds one entry per instantiation, and above them
// the method bodies the instantiations share: a method is compiled once however
// many times its declaration is instantiated, because it reaches members by
// index and an index means the same thing in every instantiation.
//
// The initializer is in the head rather than behind the entries. It is one word
// whatever the instantiations are, and leaving it at the end would mean walking
// every one of them to reach it.
typedef struct ZamGenericAbstractDefinition {
    uint64_t methodCount;
    uint64_t concreteCount;
    uint64_t initializerInstruction;
   ZamWord body[];
} ZamGenericAbstractDefinition;

typedef struct ZamGenericClassDefinition {
    uint64_t methodCount;
    uint64_t concreteCount;
    uint64_t initializerInstruction;
   ZamWord body[];
} ZamGenericClassDefinition;

_Static_assert(sizeof(ZamGenericClassDefinition) ==
                   sizeof(ZamGenericAbstractDefinition),
               "a generic class head is a generic abstract head");

static inline const ZamWord *
zam_generic_abstract_method_instructions(const ZamGenericAbstractDefinition *d) {
    return d->body;
}

static inline const ZamGenericMapEntry *
zam_generic_abstract_first_concrete(const ZamGenericAbstractDefinition *d) {
    return (const ZamGenericMapEntry *)(d->body + d->methodCount);
}

static inline const ZamWord *
zam_generic_class_method_instructions(const ZamGenericClassDefinition *d) {
    return d->body;
}

static inline const ZamGenericMapEntry *
zam_generic_class_first_concrete(const ZamGenericClassDefinition *d) {
    return (const ZamGenericMapEntry *)(d->body + d->methodCount);
}

// A generic interface holds one supertype set per instantiation. I[int] and
// I[string] are separate types and what stands above them is not the same
// question, so one set between them would answer for neither.
typedef struct ZamGenericInterfaceDefinition {
    uint64_t concreteCount;
   ZamWord body[];
} ZamGenericInterfaceDefinition;

static inline const ZamPerfectHash *
zam_generic_interface_first_concrete(const ZamGenericInterfaceDefinition *d) {
    return (const ZamPerfectHash *)d->body;
}

// A pointer definition names what a pointer points at, and that is the whole of
// it — so it is a written type id and nothing more, the same three words an
// instruction is handed on the operand stack. A pointer value at runtime is one
// word like any address; this is the static record of the pointer *type*.
//
// A pointer is a type of its own rather than a depth counted alongside an array
// length, which is what keeps the two apart: E[5]* points at an E carrying an
// array length of 5, while E*[5] is an array of 5 whose element is the pointer.
typedef ZamTypeIDWords ZamPointerDefinition;

static inline ZamTypeID zam_pointer_pointee(const ZamPointerDefinition *d) {
    return zam_type_id_read(&d->typeWord);
}

// Each reader hands back a pointer into the image and dies unless the id falls
// in the bucket the reader is for. Nothing on the wire says which kind a
// definition is, so reading one as the wrong kind would not fail, it would
// answer wrongly — and that is a question about the call rather than about the
// image, which is why it is still asked.
//
// The image itself is trusted. A reader does not check that the definition it
// points at lies inside the file, because the only thing that writes one of
// these is the compiler in this repo, and it is held to the layout by the
// conformance test in internal/4_codegen rather than by a check on every read.
const ZamStructDefinition *zam_struct_definition(const ZamImage *image,
                                                 uint16_t declID);
const ZamEnumDefinition *zam_enum_definition(const ZamImage *image,
                                             uint16_t declID);
const ZamAbstractDefinition *zam_abstract_definition(const ZamImage *image,
                                                     uint16_t declID);
const ZamClassDefinition *zam_class_definition(const ZamImage *image,
                                               uint16_t declID);
const ZamInterfaceDefinition *zam_interface_definition(const ZamImage *image,
                                                       uint16_t declID);
const ZamGenericAbstractDefinition *
zam_generic_abstract_definition(const ZamImage *image, uint16_t declID);
const ZamGenericClassDefinition *
zam_generic_class_definition(const ZamImage *image, uint16_t declID);
const ZamGenericInterfaceDefinition *
zam_generic_interface_definition(const ZamImage *image, uint16_t declID);
const ZamPointerDefinition *zam_pointer_definition(const ZamImage *image,
                                                   uint16_t declID);

// A thread type's definition is the one type it yields and a function type's is
// its signature, so neither has a definition struct of its own: the type id and
// the type list are already what the runtime reads.
ZamTypeID zam_thread_type_result(const ZamImage *image, uint16_t declID);
const ZamTypeList *zam_function_type_signature(const ZamImage *image,
                                               uint16_t declID);

// The instantiation a generic id names, reached by walking the entries before
// it. They are variable-width and unindexed, so there is no other way to one.
const ZamGenericMapEntry *
zam_generic_abstract_concrete(const ZamGenericAbstractDefinition *definition,
                              uint16_t genericID);
const ZamGenericMapEntry *
zam_generic_class_concrete(const ZamGenericClassDefinition *definition,
                           uint16_t genericID);
const ZamPerfectHash *
zam_generic_interface_concrete(const ZamGenericInterfaceDefinition *definition,
                               uint16_t genericID);

// zam_type_member and zam_type_method reach one field of a declared type by the
// index the compiler numbered it with. Both are given a short id
// rather than a bucket, because which of the seven definition kinds holds the
// table is the id's to say and no caller of these wants to know.
//
// Members and methods are numbered separately and each run is flattened over the
// base chain, base first, so an index means the same thing in a subtype as in
// the type it was declared on. That is what lets a method be selected by index
// through a base-typed reference and reach the override.
// A generic declaration holds bare instruction words rather than method
// definitions, its signatures being the instantiation's and held on the map
// entry, so the signature comes back as the id of no function type.
const ZamMemberDefinition *zam_type_member(const ZamImage *image,
                                          ZamTypeShort shortID,
                                           uint64_t index);
ZamMethodDefinition zam_type_method(const ZamImage *image, ZamTypeShort shortID,
                                    uint64_t index);

// The slots std.Object's own methods occupy in any object's flattened method
// table. Everything that can carry a method descends from Object and its
// methods are laid out first, so these indices are known without a lookup and
// are what the type-directed instructions select through.
//
// These mirror the const block in internal/0_base/base.go by hand, the way the
// opcodes in opcode.h do, and the numbering is really the order std.Object
// declares its methods in — which TestObjectMethodSlots in 2_decl_test.go holds
// both sides to. Nothing here fails if the declarations are reordered: the call
// would simply reach a different method, so the Go-side guard is the only guard.
#define ZAMOBJECT_SLOT_EQ 0
#define ZAMOBJECT_SLOT_CMP 1
#define ZAMOBJECT_SLOT_HASH 2
#define ZAMOBJECT_SLOT_STR 3
#define ZAMOBJECT_SLOT_MARK 4
#define ZAMOBJECT_SLOT_DELETE 5

// zam_type_member_count is how many members zam_type_member will answer for,
// which is what a walk of all of them needs and what reaching one by index does
// not. The flattened run is what is counted, base members included, so this is
// the whole of what an instance holds and not only what its own declaration
// wrote.
uint64_t zam_type_member_count(const ZamImage *image, ZamTypeShort shortID);

// zam_type_initializer is the routine a fresh instance of the type is filled in
// by, and zero where the type names none — offset zero being where the program
// begins and so never a routine. A type that names none is left as it was
// allocated, which for everything held inline means zeros.
uint64_t zam_type_initializer(const ZamImage *image, ZamTypeShort shortID);

// zam_type_generic_arg is what one of an instantiation's open types resolved to.
// A template's body is compiled once however many times it is instantiated, so
// an open type has no id of its own to be written down; what is written is its
// position in the declaration's enumeration of open types, and every
// instantiation stores that enumeration resolved through its own bindings. This
// is the read that turns the position back into a type.
//
// The id has to name a generic class or a generic abstract, those being the two
// kinds whose record carries the enumeration. A generic struct is written as a
// plain struct per instantiation and so has no open types left to resolve, and a
// generic interface's record is a supertype set and nothing else.
ZamTypeID zam_type_generic_arg(const ZamImage *image, ZamTypeShort shortID,
                               uint64_t index);

// zam_definition_byte_count is how much room the definition occupies, measured
// through the same accessors that read it. Definitions are laid out back to
// back, so this is what lets a whole image be checked: each one has to end
// exactly where the next begins, and a field read at the wrong width shows up as
// a definition that ends in the wrong place rather than as a value that is
// quietly wrong.
uint64_t zam_definition_byte_count(const ZamImage *image, uint16_t declID);

// zam_type_bytes is how much room a value of the type occupies where such values
// lie inline, which is the stride from one to the next and so what an instruction
// told a type id has to move. It is OpSizeOf's answer, and it is what OpStore and
// OpLoad measure a value by.
//
// Anything held as a reference is one word however wide what it refers to is: a
// pointer, a thread and a function are addresses, and an interface is reached
// only through one. Everything else is laid out inline and the image says how
// wide, which is why this is here rather than in instance.h — it is a definition
// being read, not a shape the runtime decided.
uint64_t zam_type_bytes(const ZamImage *image, ZamTypeID type);
