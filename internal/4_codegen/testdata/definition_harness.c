// A harness that reads one type definition on stdin — exactly the bytes the
// compiler wrote for it, and nothing around them — casts it to the struct the
// runtime casts it to, and prints every field the runtime can reach.
//
// The runtime no longer parses a definition, it points at one, which means
// nothing on the read path can notice a disagreement about the layout. If the
// compiler writes a count where the runtime expects a scalar, every accessor
// after it reads a neighbouring field and answers plausibly. Printing the
// fields back out to the side that wrote them is the moment that is detectable.
//
// Alignment: the bytes are read into a malloc'd buffer, which is aligned for
// any type, so the casts below are as aligned as they are in a mapped image.

#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "definition.h"

static void put(const char *name, uint64_t value) {
    printf("%s=%" PRIu64 "\n", name, value);
}

static void putAt(const char *name, uint64_t index, const char *field,
                  uint64_t value) {
    printf("%s[%" PRIu64 "].%s=%" PRIu64 "\n", name, index, field, value);
}

// The supertype set and the dispatch table are perfect hashes, whose words say
// nothing readable on their own. What is worth printing is the answers they
// give, which is also the only way to exercise the lookup arithmetic against a
// table the compiler actually built.
static const ZamTypeShort probeTypes[] = {
    // The two the fixture puts in every supertype set.
    (9u << 16) | 0u,
    (7u << 16) | 2u,
    // One that is in no set, to show a miss is reported as one. A perfect hash
    // resolves an absent key to some present key's slot, so this line coming
    // back as a hit would mean the membership check is not checking.
    (11u << 16) | 4u,
};

// Nothing here prints the table's own seed, bucket count or slot count. Those
// are decided by the seed search the compiler happened to land on, so pinning
// them would be pinning the search rather than the layout. How wide the table
// is comes out in the wordCount every dump ends with, and whether it is read
// correctly comes out in the answers below.
static void putSupertypes(const char *name, const ZamPerfectHash *set) {
    for (size_t i = 0; i < sizeof(probeTypes) / sizeof(probeTypes[0]); i++)
        putAt(name, probeTypes[i], "contains",
              zam_supertype_set_contains(set, probeTypes[i]) ? 1 : 0);
}

static void putDispatch(const ZamPerfectHash *table) {
    uint32_t method;
    if (zam_dispatch_lookup(table, (9u << 16) | 0u, 0, &method))
        put("dispatch[589824,0]", method);
    if (zam_dispatch_lookup(table, (7u << 16) | 2u, 1, &method))
        put("dispatch[458754,1]", method);
}

static void putMethods(const ZamMethodDefinition *methods, uint64_t count) {
    for (uint64_t i = 0; i < count; i++) {
        putAt("method", i, "instruction", methods[i].instruction);
        putAt("method", i, "typeListID", methods[i].typeListID);
    }
}

static void putMembers(const ZamMemberDefinition *members, uint64_t count) {
    for (uint64_t i = 0; i < count; i++) {
       ZamTypeID type = zam_member_type(&members[i]);
        putAt("member", i, "offset", members[i].offset);
        putAt("member", i, "shortID", type.shortID);
        putAt("member", i, "arrayLength", type.arrayLength);
        putAt("member", i, "tailLength", type.tailLength);
    }
}

static void putTypeList(const char *name, uint64_t index,
                        const ZamTypeList *list) {
    printf("%s[%" PRIu64 "].count=%" PRIu64 "\n", name, index, list->count);
    for (uint64_t i = 0; i < list->count; i++) {
       ZamTypeID type = zam_type_list_at(list, i);
        printf("%s[%" PRIu64 "].type[%" PRIu64 "].shortID=%" PRIu32 "\n", name,
               index, i, type.shortID);
        printf("%s[%" PRIu64 "].type[%" PRIu64 "].arrayLength=%" PRIu64 "\n",
               name, index, i, type.arrayLength);
        printf("%s[%" PRIu64 "].type[%" PRIu64 "].tailLength=%" PRIu64 "\n",
               name, index, i, type.tailLength);
    }
}

// wordsBetween is how wide the definition turned out to be, run out through the
// accessors themselves. The compiler is asked the same question in Go, and the
// two answers have to agree: definitions are written back to back, so a width
// the runtime gets wrong is a definition it would start reading in the middle
// of the one before it.
static uint64_t wordsBetween(const void *start, const void *end) {
    return (uint64_t)((const ZamWord *)end - (const ZamWord *)start);
}

static const ZamWord *genericEntryDump(const ZamGenericMapEntry *e,
                                       bool isClass) {
    put("instanceByteSize", e->instanceByteSize);
    put("instanceTailByteSize", e->instanceTailByteSize);
    put("genericTypeCount", e->genericTypeCount);
    put("memberCount", e->memberCount);
    put("methodTypeCount", e->methodTypeCount);

    for (uint64_t i = 0; i < e->genericTypeCount; i++) {
       ZamTypeID type = zam_generic_entry_generic_type_at(e, i);
        putAt("genericType", i, "shortID", type.shortID);
        putAt("genericType", i, "arrayLength", type.arrayLength);
        putAt("genericType", i, "tailLength", type.tailLength);
    }

    putMembers(zam_generic_entry_members(e), e->memberCount);

    const ZamWord *methodTypeIDs = zam_generic_entry_method_type_ids(e);
    for (uint64_t i = 0; i < e->methodTypeCount; i++)
        putAt("methodType", i, "typeListID", methodTypeIDs[i]);

    putSupertypes("supertype", zam_generic_entry_supertypes(e));

    const ZamPerfectHash *dispatch = zam_generic_entry_dispatch(e);
    if (!isClass)
        return (const ZamWord *)dispatch;
    putDispatch(dispatch);
    return (const ZamWord *)zam_perfect_hash_next(dispatch);
}

static void structDump(const ZamStructDefinition *d) {
    put("methodCount", d->methodCount);
    put("memberCount", d->memberCount);
    put("instanceByteSize", d->instanceByteSize);
    put("instanceTailByteSize", d->instanceTailByteSize);
    put("initializerInstruction", d->initializerInstruction);
    putMethods(zam_struct_methods(d), d->methodCount);
    putMembers(zam_struct_members(d), d->memberCount);
    put("wordCount",
        wordsBetween(d, zam_struct_members(d) + d->memberCount));
}

static void enumDump(const ZamEnumDefinition *d) {
    put("methodCount", d->methodCount);
    put("memberCount", d->memberCount);
    put("instanceByteSize", d->instanceByteSize);
    put("initializerInstruction", d->initializerInstruction);
    put("valueCount", d->valueCount);
    put("valueSlot", d->valueSlot);
    putMethods(zam_enum_methods(d), d->methodCount);
    putMembers(zam_enum_members(d), d->memberCount);
    put("wordCount", wordsBetween(d, zam_enum_members(d) + d->memberCount));
}

static void abstractDump(const ZamAbstractDefinition *d) {
    put("methodCount", d->methodCount);
    put("memberCount", d->memberCount);
    put("instanceByteSize", d->instanceByteSize);
    put("instanceTailByteSize", d->instanceTailByteSize);
    put("initializerInstruction", d->initializerInstruction);
    putMethods(zam_abstract_methods(d), d->methodCount);
    putMembers(zam_abstract_members(d), d->memberCount);
    putSupertypes("supertype", zam_abstract_supertypes(d));
    put("wordCount",
        wordsBetween(d, zam_perfect_hash_next(zam_abstract_supertypes(d))));
}

static void classDump(const ZamClassDefinition *d) {
    put("methodCount", d->methodCount);
    put("memberCount", d->memberCount);
    put("instanceByteSize", d->instanceByteSize);
    put("instanceTailByteSize", d->instanceTailByteSize);
    put("initializerInstruction", d->initializerInstruction);
    putMethods(zam_class_methods(d), d->methodCount);
    putMembers(zam_class_members(d), d->memberCount);
    putSupertypes("supertype", zam_class_supertypes(d));
    putDispatch(zam_class_dispatch(d));
    put("wordCount",
        wordsBetween(d, zam_perfect_hash_next(zam_class_dispatch(d))));
}

static void interfaceDump(const ZamInterfaceDefinition *d) {
    putSupertypes("supertype", d);
    put("wordCount", zam_perfect_hash_word_count(d));
}

static void genericDump(const ZamGenericAbstractDefinition *d, bool isClass) {
    put("methodCount", d->methodCount);
    put("concreteCount", d->concreteCount);
    put("initializerInstruction", d->initializerInstruction);

    const ZamWord *instructions = zam_generic_abstract_method_instructions(d);
    for (uint64_t i = 0; i < d->methodCount; i++)
        putAt("methodInstruction", i, "at", instructions[i]);

    const ZamGenericMapEntry *entry = zam_generic_abstract_first_concrete(d);
    const ZamWord *end = (const ZamWord *)entry;
    for (uint64_t i = 0; i < d->concreteCount; i++) {
        put("concrete", i);
        end = genericEntryDump(entry, isClass);
        entry = (const ZamGenericMapEntry *)end;
    }

    put("wordCount", wordsBetween(d, end));
}

static void genericInterfaceDump(const ZamGenericInterfaceDefinition *d) {
    put("concreteCount", d->concreteCount);

    const ZamPerfectHash *set = zam_generic_interface_first_concrete(d);
    for (uint64_t i = 0; i < d->concreteCount; i++) {
        put("concrete", i);
        putSupertypes("supertype", set);
        set = zam_perfect_hash_next(set);
    }

    put("wordCount", wordsBetween(d, set));
}

// A pointer definition is a written type id and nothing else, so what is worth
// checking is that it decodes the way the operand-stack form does: the short id
// out of the high half of the first word, then the array length and the tail
// length as whole words behind it.
static void pointerDump(const ZamPointerDefinition *d) {
   ZamTypeID pointee = zam_pointer_pointee(d);
    put("pointee.shortID", pointee.shortID);
    put("pointee.arrayLength", pointee.arrayLength);
    put("pointee.tailLength", pointee.tailLength);
    put("wordCount", ZAMTYPE_ID_WORDS);
}

// A thread type's definition is the one type it yields. The bucket check is what
// zam_thread_type_result adds over this, and it is a question about the image
// rather than about the definition's bytes.
static void threadTypeDump(const ZamWord *words) {
   ZamTypeID result = zam_type_id_read(words);
    put("result.shortID", result.shortID);
    put("result.arrayLength", result.arrayLength);
    put("result.tailLength", result.tailLength);
    put("wordCount", ZAMTYPE_ID_WORDS);
}

static void typeListDump(const ZamTypeList *list) {
    putTypeList("list", 0, list);
    put("wordCount", wordsBetween(list, zam_type_list_next(list)));
}

// The generic map entry is also read on its own, since it is the one piece that
// is a definition's body rather than a definition and so is never reached
// through a bucket.
static void entryDump(const ZamGenericMapEntry *e, bool isClass) {
    const ZamWord *end = genericEntryDump(e, isClass);
    put("wordCount", wordsBetween(e, end));
}

int main(int argc, char **argv) {
    if (argc != 2) {
        fprintf(stderr, "usage: %s <kind>\n", argv[0]);
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

    const char *kind = argv[1];
    if (strcmp(kind, "struct") == 0)
        structDump((const ZamStructDefinition *)bytes);
    else if (strcmp(kind, "enum") == 0)
        enumDump((const ZamEnumDefinition *)bytes);
    else if (strcmp(kind, "abstract") == 0)
        abstractDump((const ZamAbstractDefinition *)bytes);
    else if (strcmp(kind, "class") == 0)
        classDump((const ZamClassDefinition *)bytes);
    else if (strcmp(kind, "interface") == 0)
        interfaceDump((const ZamInterfaceDefinition *)bytes);
    else if (strcmp(kind, "generic-abstract") == 0)
        genericDump((const ZamGenericAbstractDefinition *)bytes, false);
    else if (strcmp(kind, "generic-class") == 0)
        genericDump((const ZamGenericAbstractDefinition *)bytes, true);
    else if (strcmp(kind, "generic-interface") == 0)
        genericInterfaceDump(
            (const ZamGenericInterfaceDefinition *)bytes);
    else if (strcmp(kind, "generic-entry") == 0)
        entryDump((const ZamGenericMapEntry *)bytes, false);
    else if (strcmp(kind, "generic-class-entry") == 0)
        entryDump((const ZamGenericMapEntry *)bytes, true);
    else if (strcmp(kind, "type-list") == 0)
        typeListDump((const ZamTypeList *)bytes);
    else if (strcmp(kind, "pointer") == 0)
        pointerDump((const ZamPointerDefinition *)bytes);
    else if (strcmp(kind, "thread-type") == 0)
        threadTypeDump((const ZamWord *)bytes);
    else {
        fprintf(stderr, "unknown kind %s\n", kind);
        free(bytes);
        return 2;
    }

    free(bytes);
    return 0;
}
