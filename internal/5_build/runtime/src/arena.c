#include "zam/native.h"

#include <stdlib.h>
#include <string.h>

#include "definition.h"
#include "gc.h"
#include "instance.h"
#include "run.h"
#include "thread.h"
#include "vm.h"

// The arena natives: a bump allocator over the tail of a std Arena, whose
// values are contained by the arena and live and die with it.
//
// Every allocation is led by a record saying how far the next one lies and
// what was laid down, which is what lets the arena walk its own values when it
// is marked and when it is deleted.

typedef struct ArenaRecord {
    // bytes is the record and its value together, rounded up to a word.
    uint64_t bytes;
    // type is the element type, its array length the element count.
    ZamWord type[ZAMTYPE_ID_WORDS];
} ArenaRecord;

static uint64_t roundToWord(uint64_t bytes) {
    return (bytes + ZAMWORD_BYTES - 1) / ZAMWORD_BYTES * ZAMWORD_BYTES;
}

// recordType is the element type a record holds and how many of it.
static ZamTypeID recordType(const ArenaRecord *record, uint64_t *elementCount) {
   ZamTypeID type = zam_type_id_read(record->type);
    *elementCount = type.arrayLength;
    type.arrayLength = 0;
    return type;
}

// arenaMemory is where the arena's memory starts, its tail being the last of
// its members, and how many bytes of it there are.
static uint8_t *arenaMemory(const ZamImage *image, uint8_t *arena,
                            uint64_t *capacity) {
   ZamTypeShort shortID = zam_object_header_of(arena)->shortID;
    uint64_t count = zam_type_member_count(image, shortID);
    ZamMemberDefinition memory = zam_type_member(image, shortID, count - 1);
    // An arena made with no memory is laid out untailed, with no length word.
   ZamObjectKind kind = zam_mark_kind_of(arena);
    *capacity = kind == ZAMOBJECT_TAILED_INSTANCE ? zam_object_tail_length_of(arena) : 0;
    return arena + zam_object_header_bytes(kind) + memory.offset;
}

// stdArenaMake allocates an arena of size bytes for the collector and writes it
// through out. A tail of a length known only at run time cannot be allocated
// by new without a count, which would make it the program's own memory.
//
// Arguments: type (three words, the arena's), size, out*.
static void stdArenaMake(ZamThread *thread) {
   ZamWord typeWords[ZAMTYPE_ID_WORDS] = {
        zam_native_argument(thread, 0),
        zam_native_argument(thread, 1),
        zam_native_argument(thread, 2),
    };
   ZamTypeID type = zam_type_id_read(typeWords);
    type.arrayLength = 0;
    type.tailLength = (ZamUint)zam_native_argument(thread, 3);

    void *arena = zam_thread_new_instance(thread, type);
    void **out = (void **)zam_native_argument_pointer(thread, 4);
    *out = arena;

   ZamResult ran = zam_thread_initialize(thread, arena, type, 1);
    if (ran == ZAMRESULT_EXIT)
        exit(thread->exitStatus);
    if (ran != ZAMRESULT_CONTINUE)
        zam_native_panic(thread, "Arena.New: an initializer did not return");
    zam_native_return_void(thread);
}

// stdArenaNew lays length values of a type in the arena, initialized as new
// would, and writes where they lie through out, or null for none or for no room.
//
// Arguments: arena, type (three words), length, used*, out*.
static void stdArenaNew(ZamThread *thread) {
    const ZamImage *image = &thread->vm->image;
    uint8_t *arena = (uint8_t *)zam_native_argument_pointer(thread, 0);
   ZamWord typeWords[ZAMTYPE_ID_WORDS] = {
        zam_native_argument(thread, 1),
        zam_native_argument(thread, 2),
        zam_native_argument(thread, 3),
    };
   ZamTypeID type = zam_type_id_read(typeWords);
   ZamUint length = (ZamUint)zam_native_argument(thread, 4);
    uint64_t *used = (uint64_t *)zam_native_argument_pointer(thread, 5);
    void **out = (void **)zam_native_argument_pointer(thread, 6);
    *out = NULL;

    if (arena == NULL)
        zam_native_panic(thread, "Arena.Alloc: null arena");

   ZamTypeID element = type;
    element.arrayLength = 0;
    uint64_t elementCount = length * (type.arrayLength == 0 ? 1 : type.arrayLength);
    if (elementCount == 0) {
        zam_native_return_void(thread);
        return;
    }

    uint64_t elementBytes = zam_type_bytes(image, element);
    if (elementBytes != 0 && elementCount > UINT64_MAX / elementBytes) {
        zam_native_return_void(thread);
        return;
    }
    uint64_t valueBytes = elementCount * elementBytes;
    if (valueBytes > UINT64_MAX - sizeof(ArenaRecord) - ZAMWORD_BYTES) {
        zam_native_return_void(thread);
        return;
    }
    uint64_t bytes = roundToWord(sizeof(ArenaRecord) + valueBytes);

    uint64_t capacity;
    uint8_t *start = arenaMemory(image, arena, &capacity);

    if (*used > capacity || bytes > capacity - *used) {
        zam_native_return_void(thread);
        return;
    }

    // The record and the headers are written before the space is claimed, so
    // that a collection the initializer brings on walks a well-formed arena.
    ArenaRecord *record = (ArenaRecord *)(start + *used);
    uint8_t *value = (uint8_t *)(record + 1);
    record->bytes = bytes;
   ZamTypeID recorded = element;
    recorded.arrayLength = elementCount;
    zam_type_id_write(record->type, recorded);
    zam_type_stamp(image, value, element, elementCount, (uint64_t)(value - arena));
    *used += bytes;
    *out = value;

   ZamResult ran = zam_thread_initialize(thread, value, element, elementCount);
    if (ran == ZAMRESULT_EXIT)
        exit(thread->exitStatus);
    if (ran != ZAMRESULT_CONTINUE)
        zam_native_panic(thread, "Arena.Alloc: an initializer did not return");
    zam_native_return_void(thread);
}

// stdArenaMark marks every value the arena holds while collecting.
//
// Arguments: arena, used.
static void stdArenaMark(ZamThread *thread) {
    if (!zam_gc_collecting(thread)) {
        zam_native_return_void(thread);
        return;
    }
    uint64_t capacity;
    const uint8_t *start = arenaMemory(
        &thread->vm->image, (uint8_t *)zam_native_argument_pointer(thread, 0), &capacity);
   ZamUint used = (ZamUint)zam_native_argument(thread, 1);

    for (uint64_t at = 0; at < used;) {
        const ArenaRecord *record = (const ArenaRecord *)(start + at);
        uint64_t elementCount;
       ZamTypeID type = recordType(record, &elementCount);
        if (zam_type_contains_markable(thread->vm, type)) {
            type.arrayLength = elementCount;
            zam_gc_mark_value(thread, (ZamWord)(uintptr_t)(record + 1), type);
        }
        at += record->bytes;
    }
    zam_native_return_void(thread);
}

// deleteAll runs Delete on the front of every run of class values in the used
// bytes of an arena's memory, as the collector does for an allocation of its
// own.
static void deleteAll(ZamThread *thread, const uint8_t *start, uint64_t used,
                      const char *failure) {
    const ZamImage *image = &thread->vm->image;
    for (uint64_t at = 0; at < used;) {
        const ArenaRecord *record = (const ArenaRecord *)(start + at);
        uint64_t elementCount;
       ZamTypeID type = recordType(record, &elementCount);
        uint64_t bytes = record->bytes;
        if (zam_type_is_class(image, type.shortID)) {
            void *value = (void *)(record + 1);
            const void *outer = thread->finalizing;
            thread->finalizing = value;
           ZamResult ran = zam_thread_call_method(thread, (ZamWord)(uintptr_t)value,
                                                   ZAMOBJECT_SLOT_DELETE);
            thread->finalizing = outer;
            if (ran == ZAMRESULT_EXIT)
                exit(thread->exitStatus);
            if (ran != ZAMRESULT_CONTINUE)
                zam_native_panic(thread, failure);
        }
        at += bytes;
    }
}

// stdArenaDelete deletes every class value the arena holds.
//
// Arguments: arena, used.
static void stdArenaDelete(ZamThread *thread) {
    uint64_t capacity;
    const uint8_t *start = arenaMemory(
        &thread->vm->image, (uint8_t *)zam_native_argument_pointer(thread, 0), &capacity);
    deleteAll(thread, start, (ZamUint)zam_native_argument(thread, 1),
              "Arena.Delete: a Delete did not return");
    zam_native_return_void(thread);
}

// stdArenaReset deletes every class value the arena holds, then empties it for
// reuse. A Delete that allocates in the arena has that allocation emptied too.
//
// Arguments: arena, used*.
static void stdArenaReset(ZamThread *thread) {
    uint8_t *arena = (uint8_t *)zam_native_argument_pointer(thread, 0);
    uint64_t capacity;
    uint8_t *start = arenaMemory(&thread->vm->image, arena, &capacity);
    uint64_t used = *(uint64_t *)zam_native_argument_pointer(thread, 1);
    deleteAll(thread, start, used, "Arena.Reset: a Delete did not return");

    // A Delete may have allocated, and may have moved the operand stack the
    // pointer was resolved against, so both are read again.
    uint64_t *usedOut = (uint64_t *)zam_native_argument_pointer(thread, 1);
    memset(start, 0, *usedOut);
    *usedOut = 0;
    zam_native_return_void(thread);
}

const ZamNativeEntry zamArenaNativeTable[] = {
    {"STD_ARENA_MAKE", stdArenaMake},
    {"STD_ARENA_NEW", stdArenaNew},
    {"STD_ARENA_MARK", stdArenaMark},
    {"STD_ARENA_DELETE", stdArenaDelete},
    {"STD_ARENA_RESET", stdArenaReset},
};

const uint64_t zamArenaNativeTableCount =
    sizeof(zamArenaNativeTable) / sizeof(zamArenaNativeTable[0]);
