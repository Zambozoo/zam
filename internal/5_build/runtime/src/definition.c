#include "definition.h"

#include "instance.h"

// The arithmetic below mirrors internal/4_codegen/perfecthash.go. It is the one
// place the two sides have to agree bit for bit: a table resolved by different
// arithmetic than it was built with does not fail, it answers wrongly.

static uint32_t reduce(uint32_t x, uint32_t n) {
    return (uint32_t)(((uint64_t)x * (uint64_t)n) >> 32);
}

static uint64_t fksHash(uint64_t key, uint64_t seed) {
    uint64_t hash = key ^ seed;
    hash ^= hash >> 33;
    hash *= 0xff51afd7ed558ccdULL;
    hash ^= hash >> 33;
    hash *= 0xc4ceb9fe1a85ec53ULL;
    hash ^= hash >> 33;
    return hash;
}

static uint32_t fksIndex(uint64_t key, uint64_t seed, uint32_t n) {
    return reduce((uint32_t)(fksHash(key, seed) >> 32), n);
}

bool zam_perfect_hash_lookup(const ZamPerfectHash *table, uint64_t key,
                             uint32_t *value) {
    // A table built over no keys has no bucket to land in. The compiler still
    // writes its three head words, so this is the shape an empty set arrives
    // in rather than a malformed one.
    if (table->bucketCount == 0)
        return false;

    const ZamWord *buckets = zam_perfect_hash_buckets(table);
    uint32_t index =
        fksIndex(key, table->seed, (uint32_t)(table->bucketCount - 1));
   ZamWord bucket = buckets[index];
    uint32_t offset = (uint32_t)(bucket >> 32);
    uint32_t width = (uint32_t)(buckets[index + 1] >> 32) - offset;

    // A bucket no key was sorted into owns no slots, and reducing into a range
    // of zero would read the slot after its block rather than one of its own.
    if (width == 0)
        return false;

    const ZamWord *slots = zam_perfect_hash_slots(table);
    *value = (uint32_t)slots[offset + fksIndex(key, (uint32_t)bucket, width)];
    return true;
}

bool zam_supertype_set_contains(const ZamPerfectHash *set, ZamTypeShort type) {
    // Zero is void's id and no declaration's, so it stands in no set and can be
    // answered without looking. Answered here because it is the one key the
    // read-back below cannot speak for: a bucket is sized by the square of what
    // sorted into it and the slots it does not use are left as they were
    // allocated, so a hole reads back as zero and would agree with a zero key.
    if (type == 0)
        return false;

    uint32_t value;
    if (!zam_perfect_hash_lookup(set, (uint64_t)type, &value))
        return false;
    // Believe the answer only if what came back is what was asked about: a key
    // that was never built in lands in some other key's slot.
    return value == type;
}

bool zam_dispatch_lookup(const ZamPerfectHash *table, ZamTypeShort interfaceType,
                         uint32_t interfaceMethodIndex, uint32_t *methodIndex) {
    uint64_t key = ((uint64_t)interfaceType << 32) | interfaceMethodIndex;
    return zam_perfect_hash_lookup(table, key, methodIndex);
}

// Everything below finds where a definition ends, and every one of them gets
// there by running the kind's own accessors out to its last run. That is the
// point: this is not a second description of the layout that could drift from
// the first, it is the accessors themselves asked how far they reach. A field
// reached at the wrong width shows up here as a definition ending in the wrong
// place, which is what zam_definition_byte_count exists to catch.

static const ZamWord *structEnd(const ZamStructDefinition *d) {
    return (const ZamWord *)(zam_struct_members(d) + d->memberCount);
}

static const ZamWord *enumEnd(const ZamEnumDefinition *d) {
    return (const ZamWord *)(zam_enum_members(d) + d->memberCount);
}

static const ZamWord *abstractEnd(const ZamAbstractDefinition *d) {
    return (const ZamWord *)zam_perfect_hash_next(zam_abstract_supertypes(d));
}

static const ZamWord *classEnd(const ZamClassDefinition *d) {
    return (const ZamWord *)zam_perfect_hash_next(zam_class_dispatch(d));
}

// A generic map entry ends after its supertype set, or after the dispatch table
// that a class instantiation carries behind it. Which of the two it is cannot be
// read off the entry, so it is passed in.
static const ZamWord *genericEntryEnd(const ZamGenericMapEntry *e,
                                      bool isClass) {
    const ZamPerfectHash *dispatch = zam_generic_entry_dispatch(e);
    if (!isClass)
        return (const ZamWord *)dispatch;
    return (const ZamWord *)zam_perfect_hash_next(dispatch);
}

const ZamGenericMapEntry *
zam_generic_entry_next(const ZamGenericMapEntry *entry, bool isClass) {
    return (const ZamGenericMapEntry *)genericEntryEnd(entry, isClass);
}

static const ZamWord *genericEnd(const ZamGenericAbstractDefinition *d,
                                 bool isClass) {
    const ZamGenericMapEntry *entry = zam_generic_abstract_first_concrete(d);
    for (uint64_t i = 0; i < d->concreteCount; i++)
        entry = zam_generic_entry_next(entry, isClass);
    return (const ZamWord *)entry;
}

static const ZamWord *
genericInterfaceEnd(const ZamGenericInterfaceDefinition *d) {
    const ZamPerfectHash *set = zam_generic_interface_first_concrete(d);
    for (uint64_t i = 0; i < d->concreteCount; i++)
        set = zam_perfect_hash_next(set);
    return (const ZamWord *)set;
}

static uint64_t wordsBetween(const void *start, const ZamWord *end) {
    return (uint64_t)(end - (const ZamWord *)start);
}

// A definition is reached by bucket rather than by anything written beside it,
// so reading one as the wrong kind would not fail — it would answer wrongly.
// This is the one thing still checked on the way in, and it is not a question
// about the image: it is a question about the call, and the image's own bucket
// table already holds the answer.
static const void *definitionAt(const ZamImage *image, uint16_t declID,
                               ZamTypeBucket expected, const char *name) {
    if (zam_image_bucket_of(image, declID) != expected)
        zam_die(ZAMEXIT_IMAGE,
                "type %u was read as %s, which is not the bucket it is in\n",
                (unsigned)declID, name);
    return zam_image_type_definition(image, declID);
}

const ZamStructDefinition *zam_struct_definition(const ZamImage *image,
                                                 uint16_t declID) {
    // A generic struct is flattened at compile time into one definition per
    // instantiation, each with an id of its own, so the two buckets hold the
    // same shape and are read the same way.
   ZamTypeBucket bucket = zam_image_bucket_of(image, declID);
    if (bucket != ZAMBUCKET_STRUCT && bucket != ZAMBUCKET_GENERIC_STRUCT)
        zam_die(ZAMEXIT_IMAGE,
                "type %u was read as a struct, which is not the bucket it is "
                "in\n",
                (unsigned)declID);

    return (const ZamStructDefinition *)zam_image_type_definition(image,
                                                                  declID);
}

const ZamEnumDefinition *zam_enum_definition(const ZamImage *image,
                                             uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_ENUM, "an enum");
}

const ZamAbstractDefinition *zam_abstract_definition(const ZamImage *image,
                                                     uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_ABSTRACT, "an abstract");
}

const ZamClassDefinition *zam_class_definition(const ZamImage *image,
                                               uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_CLASS, "a class");
}

const ZamInterfaceDefinition *zam_interface_definition(const ZamImage *image,
                                                       uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_INTERFACE, "an interface");
}

const ZamGenericAbstractDefinition *
zam_generic_abstract_definition(const ZamImage *image, uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_GENERIC_ABSTRACT,
                        "a generic abstract");
}

const ZamGenericClassDefinition *
zam_generic_class_definition(const ZamImage *image, uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_GENERIC_CLASS,
                        "a generic class");
}

const ZamGenericInterfaceDefinition *
zam_generic_interface_definition(const ZamImage *image, uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_GENERIC_INTERFACE,
                        "a generic interface");
}

const ZamPointerDefinition *zam_pointer_definition(const ZamImage *image,
                                                   uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_POINTER, "a pointer");
}

ZamTypeID zam_thread_type_result(const ZamImage *image, uint16_t declID) {
    return zam_type_id_read(
        definitionAt(image, declID, ZAMBUCKET_THREAD_TYPE, "a thread type"));
}

const ZamTypeList *zam_function_type_signature(const ZamImage *image,
                                               uint16_t declID) {
    return definitionAt(image, declID, ZAMBUCKET_FUNCTION_TYPE,
                        "a function type");
}

const ZamGenericMapEntry *
zam_generic_abstract_concrete(const ZamGenericAbstractDefinition *definition,
                              uint16_t genericID) {
    const ZamGenericMapEntry *entry =
        zam_generic_abstract_first_concrete(definition);
    for (uint16_t i = 0; i < genericID; i++)
        entry = zam_generic_entry_next(entry, false);
    return entry;
}

const ZamGenericMapEntry *
zam_generic_class_concrete(const ZamGenericClassDefinition *definition,
                           uint16_t genericID) {
    const ZamGenericMapEntry *entry =
        zam_generic_class_first_concrete(definition);
    for (uint16_t i = 0; i < genericID; i++)
        entry = zam_generic_entry_next(entry, true);
    return entry;
}

const ZamPerfectHash *
zam_generic_interface_concrete(const ZamGenericInterfaceDefinition *definition,
                               uint16_t genericID) {
    const ZamPerfectHash *set =
        zam_generic_interface_first_concrete(definition);
    for (uint16_t i = 0; i < genericID; i++)
        set = zam_perfect_hash_next(set);
    return set;
}

// The member and method runs of whichever kind of definition the id names, with
// how long each is. Everything below reads a definition for its tables and
// nothing else, so the two are found once here rather than in each caller.
//
// A generic declaration is the one kind where the two runs come from different
// places. The members are the instantiation's, since List[int] and List[string]
// share a declaration and not a layout; the method bodies are the declaration's,
// because a method is compiled once however many times it is instantiated —
// which is exactly why the instructions are held above the entries rather than
// on each of them.
typedef struct ZamFieldTables {
    const ZamMemberDefinition *members;
    uint64_t memberCount;
    // methods is the run of definitions, or NULL where the kind holds bare
    // instruction words instead. Exactly one of the two is ever set.
    const ZamMethodDefinition *methods;
    const ZamWord *methodInstructions;
    // methodTypeIDs goes with methodInstructions and is set only alongside it,
    // the two being the halves a generic keeps apart: a body is compiled once
    // for the declaration and a signature is one per instantiation, so the run
    // they are read from is not the same run.
    const ZamWord *methodTypeIDs;
    uint64_t methodCount;
    // initializer is the routine a fresh instance is filled in by, or zero
    // where the type names none. It comes off the same read as the tables
    // because it lies in the same definition and is asked about a type by the
    // same short id.
    uint64_t initializer;
} ZamFieldTables;

static ZamFieldTables fieldTables(const ZamImage *image, ZamTypeShort shortID) {
    uint16_t declID = ZAMTYPE_DECL(shortID);
    uint16_t genericID = ZAMTYPE_GENERIC(shortID);

    switch (zam_image_bucket_of(image, declID)) {
    // A generic struct is written as a plain struct definition, one per
    // concrete, so it is read as one — the same reading elementBytes gives it.
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT: {
        const ZamStructDefinition *d =
            (const ZamStructDefinition *)zam_image_type_definition(image,
                                                                   declID);
        return (ZamFieldTables){
            .members = zam_struct_members(d),
            .memberCount = d->memberCount,
            .methods = zam_struct_methods(d),
            .methodCount = d->methodCount,
            .initializer = d->initializerInstruction,
        };
    }

    case ZAMBUCKET_ENUM: {
        const ZamEnumDefinition *d = zam_enum_definition(image, declID);
        return (ZamFieldTables){
            .members = zam_enum_members(d),
            .memberCount = d->memberCount,
            .methods = zam_enum_methods(d),
            .methodCount = d->methodCount,
            .initializer = d->initializerInstruction,
        };
    }

    case ZAMBUCKET_ABSTRACT: {
        const ZamAbstractDefinition *d = zam_abstract_definition(image, declID);
        return (ZamFieldTables){
            .members = zam_abstract_members(d),
            .memberCount = d->memberCount,
            .methods = zam_abstract_methods(d),
            .methodCount = d->methodCount,
            .initializer = d->initializerInstruction,
        };
    }

    case ZAMBUCKET_CLASS: {
        const ZamClassDefinition *d = zam_class_definition(image, declID);
        return (ZamFieldTables){
            .members = zam_class_members(d),
            .memberCount = d->memberCount,
            .methods = zam_class_methods(d),
            .methodCount = d->methodCount,
            .initializer = d->initializerInstruction,
        };
    }

    case ZAMBUCKET_GENERIC_ABSTRACT: {
        const ZamGenericAbstractDefinition *d =
            zam_generic_abstract_definition(image, declID);
        const ZamGenericMapEntry *e = zam_generic_abstract_concrete(d, genericID);
        return (ZamFieldTables){
            .members = zam_generic_entry_members(e),
            .memberCount = e->memberCount,
            .methodInstructions =
                zam_generic_abstract_method_instructions(d),
            .methodTypeIDs = zam_generic_entry_method_type_ids(e),
            .methodCount = d->methodCount,
            .initializer = d->initializerInstruction,
        };
    }

    case ZAMBUCKET_GENERIC_CLASS: {
        const ZamGenericClassDefinition *d =
            zam_generic_class_definition(image, declID);
        const ZamGenericMapEntry *e = zam_generic_class_concrete(d, genericID);
        return (ZamFieldTables){
            .members = zam_generic_entry_members(e),
            .memberCount = e->memberCount,
            .methodInstructions = zam_generic_class_method_instructions(d),
            .methodTypeIDs = zam_generic_entry_method_type_ids(e),
            .methodCount = d->methodCount,
            .initializer = d->initializerInstruction,
        };
    }

    // None of these describes an instance. A primitive has no definition at
    // all, an interface is a supertype set and is dispatched to through
    // whatever class the instance turns out to be, and the three held as
    // addresses say what lies at the far end rather than what it holds.
    case ZAMBUCKET_PRIMITIVE:
    case ZAMBUCKET_POINTER:
    case ZAMBUCKET_THREAD_TYPE:
    case ZAMBUCKET_FUNCTION_TYPE:
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
        break;
    }

    zam_die(ZAMEXIT_PANIC, "type id %u names no fields\n", declID);
}

const ZamMemberDefinition *zam_type_member(const ZamImage *image,
                                          ZamTypeShort shortID,
                                           uint64_t index) {
   ZamFieldTables tables = fieldTables(image, shortID);
    if (index >= tables.memberCount)
        zam_die(ZAMEXIT_IMAGE, "member %llu of a type holding %llu\n",
                (unsigned long long)index,
                (unsigned long long)tables.memberCount);
    return &tables.members[index];
}

uint64_t zam_type_member_count(const ZamImage *image, ZamTypeShort shortID) {
    return fieldTables(image, shortID).memberCount;
}

ZamMethodDefinition zam_type_method(const ZamImage *image, ZamTypeShort shortID,
                                    uint64_t index) {
   ZamFieldTables tables = fieldTables(image, shortID);
    if (index >= tables.methodCount)
        zam_die(ZAMEXIT_IMAGE, "method %llu of a type holding %llu\n",
                (unsigned long long)index,
                (unsigned long long)tables.methodCount);

   ZamMethodDefinition method =
        tables.methods != NULL
            ? tables.methods[index]
            : (ZamMethodDefinition){
                  .instruction = tables.methodInstructions[index],
                  .typeListID = tables.methodTypeIDs[index]};

    // Offset zero is where the program begins, so it is never a body and is
    // what a method declared without one is left naming. Reaching one means an
    // abstract or interface method was selected against the record that
    // declared it rather than against the class the instance actually is.
    if (method.instruction == 0)
        zam_die(ZAMEXIT_PANIC, "method %llu of type %u has no body\n",
                (unsigned long long)index, shortID);
    return method;
}

uint64_t zam_type_initializer(const ZamImage *image, ZamTypeShort shortID) {
    return fieldTables(image, shortID).initializer;
}

ZamTypeID zam_type_generic_arg(const ZamImage *image, ZamTypeShort shortID,
                               uint64_t index) {
    uint16_t declID = ZAMTYPE_DECL(shortID);
    uint16_t genericID = ZAMTYPE_GENERIC(shortID);

    const ZamGenericMapEntry *entry = NULL;
    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_GENERIC_ABSTRACT:
        entry = zam_generic_abstract_concrete(
            zam_generic_abstract_definition(image, declID), genericID);
        break;

    case ZAMBUCKET_GENERIC_CLASS:
        entry = zam_generic_class_concrete(
            zam_generic_class_definition(image, declID), genericID);
        break;

    // None of these enumerates open types. The first two resolve theirs before
    // anything is written — a generic struct is a struct per instantiation, and
    // a generic interface holds only what stands above it — and the rest are
    // not templates at all.
    case ZAMBUCKET_PRIMITIVE:
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
    case ZAMBUCKET_ENUM:
    case ZAMBUCKET_ABSTRACT:
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
    case ZAMBUCKET_POINTER:
    case ZAMBUCKET_THREAD_TYPE:
    case ZAMBUCKET_FUNCTION_TYPE:
        break;
    }

    if (entry == NULL)
        zam_die(ZAMEXIT_IMAGE, "type id %u enumerates no open types\n", declID);

    // A position past the end is the one wrong operand that would otherwise read
    // as a type: what lies behind the enumeration is the member run, whose first
    // word is an offset and reads as a short id of zero — void, which no open
    // type ever resolves to and which nothing downstream would reject.
    if (index >= entry->genericTypeCount)
        zam_die(ZAMEXIT_IMAGE, "open type %llu of a type enumerating %llu\n",
                (unsigned long long)index,
                (unsigned long long)entry->genericTypeCount);
    return zam_generic_entry_generic_type_at(entry, index);
}

uint64_t zam_definition_byte_count(const ZamImage *image, uint16_t declID) {
    const void *at = zam_image_type_definition(image, declID);

    uint64_t words;
    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_THREAD_TYPE:
        words = ZAMTYPE_ID_WORDS;
        break;
    case ZAMBUCKET_FUNCTION_TYPE:
        words = wordsBetween(at, (const ZamWord *)zam_type_list_next(at));
        break;
    case ZAMBUCKET_POINTER:
        words = ZAMTYPE_ID_WORDS;
        break;
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
        words = wordsBetween(at, structEnd(at));
        break;
    case ZAMBUCKET_ENUM:
        words = wordsBetween(at, enumEnd(at));
        break;
    case ZAMBUCKET_ABSTRACT:
        words = wordsBetween(at, abstractEnd(at));
        break;
    case ZAMBUCKET_CLASS:
        words = wordsBetween(at, classEnd(at));
        break;
    case ZAMBUCKET_INTERFACE:
        words = zam_perfect_hash_word_count(at);
        break;
    case ZAMBUCKET_GENERIC_INTERFACE:
        words = wordsBetween(at, genericInterfaceEnd(at));
        break;
    case ZAMBUCKET_GENERIC_ABSTRACT:
        words = wordsBetween(at, genericEnd(at, false));
        break;
    case ZAMBUCKET_GENERIC_CLASS:
        words = wordsBetween(at, genericEnd(at, true));
        break;
    case ZAMBUCKET_PRIMITIVE:
    default:
        zam_die(ZAMEXIT_IMAGE, "a primitive has no definition\n");
    }

    return words * ZAMWORD_BYTES;
}

// How wide one value of the type is, before any array length multiplies it.
//
// A tail length is added wherever a definition names a per-element tail size,
// because that is what a tail is: the type id carries the count and the
// definition carries the stride, and neither is any use without the other.
static uint64_t elementBytes(const ZamImage *image, ZamTypeID type) {
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    uint16_t genericID = ZAMTYPE_GENERIC(type.shortID);

    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_PRIMITIVE:
        // A primitive's declaration id is its kind, which is the whole reason
        // the definition map runs from zero and leaves these entries unread.
        return zam_primitive_bytes((ZamPrimitive)declID);

    // Every one of these is held as an address. A pointer and a thread and a
    // function are addresses outright, and an interface is reached only through
    // one, so what lies at the far end is as wide as it is and none of that is
    // paid for here.
    case ZAMBUCKET_POINTER:
    case ZAMBUCKET_THREAD_TYPE:
    case ZAMBUCKET_FUNCTION_TYPE:
    case ZAMBUCKET_INTERFACE:
    case ZAMBUCKET_GENERIC_INTERFACE:
        return ZAMWORD_BYTES;

    // A generic struct is written as a plain struct definition, so it is read
    // as one rather than through the bucket-checking reader, exactly as
    // zam_definition_byte_count reads it.
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT: {
        const ZamStructDefinition *d =
            (const ZamStructDefinition *)zam_image_type_definition(image,
                                                                   declID);
        return d->instanceByteSize + type.tailLength * d->instanceTailByteSize;
    }

    case ZAMBUCKET_ENUM:
        // An enum is the one declared type with no tail: its instances are the
        // values named in the declaration, and none of those is variable-length.
        return zam_enum_definition(image, declID)->instanceByteSize;

    case ZAMBUCKET_ABSTRACT: {
        const ZamAbstractDefinition *d = zam_abstract_definition(image, declID);
        return d->instanceByteSize + type.tailLength * d->instanceTailByteSize;
    }
    case ZAMBUCKET_CLASS: {
        const ZamClassDefinition *d = zam_class_definition(image, declID);
        return d->instanceByteSize + type.tailLength * d->instanceTailByteSize;
    }

    // An instantiation's width is its own. List[int] and List[BigStruct] share
    // a declaration and not a layout, which is why the size is on the map entry
    // rather than on the definition that holds the entries.
    case ZAMBUCKET_GENERIC_ABSTRACT: {
        const ZamGenericMapEntry *e = zam_generic_abstract_concrete(
            zam_generic_abstract_definition(image, declID), genericID);
        return e->instanceByteSize + type.tailLength * e->instanceTailByteSize;
    }
    case ZAMBUCKET_GENERIC_CLASS: {
        const ZamGenericMapEntry *e = zam_generic_class_concrete(
            zam_generic_class_definition(image, declID), genericID);
        return e->instanceByteSize + type.tailLength * e->instanceTailByteSize;
    }
    }

    zam_die(ZAMEXIT_IMAGE, "type id %u falls in no bucket\n", declID);
}

uint64_t zam_type_bytes(const ZamImage *image, ZamTypeID type) {
    uint64_t bytes = elementBytes(image, type);
    // A length of zero is not an empty array, it is a value that is not an array
    // at all: the compiler writes a length only where there is one to write.
    return type.arrayLength == 0 ? bytes : bytes * type.arrayLength;
}
