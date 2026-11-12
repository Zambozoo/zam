#include "gc.h"

#include "definition.h"
#include "opcode.h"
#include "run.h"

bool zam_gc_collecting(const ZamThread *thread) {
    return thread->vm->collector == thread;
}

// allocationOf is the allocation a word points into, or NULL where it points
// into nothing the collector owns: a frame location, the static region, or a
// value standing inside nothing. An interior value is stepped out of to the
// allocation that holds it.
static void *allocationOf(ZamVM *vm, ZamWord word) {
    if (word == 0 || zam_is_frame_location(word))
        return NULL;

    uint8_t *at = (uint8_t *)(uintptr_t)word;
    if (at >= vm->statics && at < vm->statics + vm->image.staticRegionByteCount)
        return NULL;

    for (;;) {
       ZamMarkWord *mark = zam_mark_word_of(at);
        if (!mark->contained)
            return at;
        if (mark->upvalue == 0)
            return NULL;
        at -= mark->upvalue;
    }
}

static void pushGray(ZamVM *vm, void *object) {
    if (vm->grayCount == vm->grayCapacity) {
        vm->grayCapacity = vm->grayCapacity == 0 ? 64 : vm->grayCapacity * 2;
        vm->gray = zam_realloc(vm->gray, vm->grayCapacity * sizeof *vm->gray);
    }
    vm->gray[vm->grayCount++] = object;
}

// reach says the allocation a word points into is reachable, queueing it to be
// traced the first time it is said.
static void reach(ZamVM *vm, ZamWord word) {
    void *object = allocationOf(vm, word);
    if (object == NULL)
        return;
   ZamMarkWord *mark = zam_mark_word_of(object);
    if (mark->state != ZAMMARK_STATE_WHITE)
        return;
    mark->state = ZAMMARK_STATE_BLACK;
    pushGray(vm, object);
}

// call runs one of Object's methods on the instance at a location. What a
// collection runs has nowhere to hand a failure back to, so a body that ends
// the program ends it here.
static void call(ZamThread *collector, ZamWord receiver, uint64_t slot) {
   ZamResult result = zam_thread_call_method(collector, receiver, slot);
    if (result == ZAMRESULT_EXIT)
        exit(collector->exitStatus);
    if (result != ZAMRESULT_CONTINUE)
        zam_die(ZAMEXIT_PANIC,
                "a Mark or Delete run by the collector did not return\n");
}

// markValue says the value of the type at a location is reachable. The location
// is an address, or a frame location on the collector's own stack, which a
// Mark body run from here may move.
static void markValue(ZamThread *collector, ZamWord location, ZamTypeID type) {
   ZamVM *vm = collector->vm;
    const ZamImage *image = &vm->image;

    if (type.arrayLength != 0) {
       ZamTypeID element = type;
        element.arrayLength = 0;
        if (!zam_type_contains_markable(vm, element))
            return;
        uint64_t stride = zam_type_bytes(image, element);
        for (uint64_t index = 0; index < type.arrayLength; index++)
            markValue(collector, location + index * stride, element);
        return;
    }

    if (zam_type_is_markable_reference(image, type)) {
       ZamWord word;
        memcpy(&word, zam_thread_location_address(collector, location),
               sizeof word);
        reach(vm, word);
        return;
    }
    if (!zam_type_contains_markable(vm, type))
        return;

    if (zam_type_is_class(image, type.shortID)) {
        call(collector, location, ZAMOBJECT_SLOT_MARK);
        return;
    }

    // A struct or an enum, which has no Mark of its own and is its members.
    uint64_t header = zam_value_header_bytes(image, type);
    uint64_t count = zam_type_member_count(image, type.shortID);
    for (uint64_t index = 0; index < count; index++) {
        ZamMemberDefinition member =
            zam_type_member(image, type.shortID, index);
       ZamTypeID memberType = zam_member_type(&member);
        if (type.tailLength != 0 && index + 1 == count)
            memberType.arrayLength = type.tailLength;
        markValue(collector, location + header + member.offset, memberType);
    }
}

// trace visits what an allocation holds. An instance answers through its own
// Mark, once for each instance in the run; a closure holds its receiver and its
// captures; an any box holds its payload, which nothing else reaches; and a
// string or a thread holds nothing the collector does not reach by other means.
static void trace(ZamThread *collector, void *object) {
   ZamVM *vm = collector->vm;
    const ZamImage *image = &vm->image;
   ZamMarkWord *mark = zam_mark_word_of(object);

    switch ((ZamObjectKind)mark->kind) {
    case ZAMOBJECT_INSTANCE:
    case ZAMOBJECT_TAILED_INSTANCE: {
        // An any's payload is an instance of whatever it boxed, which is not
        // always a class, and is traced through its box.
        const ZamObjectHeader *header = zam_object_header_of(object);
        if (!zam_type_is_class(image, header->shortID))
            return;

       ZamTypeID element = {.shortID = header->shortID};
        if (mark->kind == ZAMOBJECT_TAILED_INSTANCE)
            element.tailLength = zam_object_tail_length_of(object);
        uint64_t stride = zam_type_bytes(image, element);
        uint64_t count = header->elementCount;
        for (uint64_t index = 0; index < count; index++)
            call(collector,
                 (ZamWord)(uintptr_t)((uint8_t *)object + index * stride),
                 ZAMOBJECT_SLOT_MARK);
        return;
    }

    case ZAMOBJECT_FUNCTION_CLOSURE:
    case ZAMOBJECT_REFERENCE_METHOD_CLOSURE:
    case ZAMOBJECT_VALUE_METHOD_CLOSURE: {
        const ZamClosure *closure = zam_closure_of(object);
        uint64_t entries = 1 + closure->captureCount;
        if (mark->kind == ZAMOBJECT_VALUE_METHOD_CLOSURE)
            entries++;

        const ZamWord *entry = zam_closure_first(closure);
        for (uint64_t index = 0; index < entries; index++) {
           ZamTypeID type = zam_closure_entry_type(entry);
            markValue(collector,
                      (ZamWord)(uintptr_t)zam_closure_entry_value(entry), type);
            entry = zam_closure_entry_next(entry, zam_value_words(image, type));
        }
        return;
    }

    case ZAMOBJECT_ANY: {
        uint64_t typedHeader = zam_object_header_bytes(ZAMOBJECT_INSTANCE);
        const ZamWord *body =
            (const ZamWord *)((const uint8_t *)object + typedHeader);
        uint8_t *payload = (uint8_t *)(uintptr_t)body[3];
        if (payload == NULL)
            return;

        // The payload carries the body of the value it boxed and none of that
        // value's own header, the box's standing in for it.
       ZamTypeID stored = zam_type_id_read(body);
        zam_mark_word_of(payload)->state = ZAMMARK_STATE_BLACK;
        markValue(collector,
                  (ZamWord)(uintptr_t)(payload + typedHeader -
                                       zam_value_header_bytes(image, stored)),
                  stored);
        return;
    }

    case ZAMOBJECT_STRING:
    case ZAMOBJECT_THREAD:
    case ZAMOBJECT_KIND_COUNT:
        return;
    }
}

static void drain(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    while (vm->grayCount != 0)
        trace(collector, vm->gray[--vm->grayCount]);
}

// operandLocation names an operand word of a thread's stack. The collector's
// own stack moves as the Mark bodies it runs push, so a word of it is named by
// its frame location; every other thread is stopped, so its words stay put.
static ZamWord operandLocation(const ZamThread *collector, ZamThread *thread,
                               uint64_t operand) {
    if (thread == collector)
        return zam_frame_location(operand * ZAMWORD_BYTES);
    return (ZamWord)(uintptr_t)(thread->operands + operand);
}

// markStack marks what a thread's mark entries name. The entries are read
// afresh each step, since a Mark body run from here may grow the stack they
// lie on; it leaves the entries beneath it as it found them.
static void markStack(ZamThread *collector, ZamThread *thread) {
   ZamVM *vm = collector->vm;
    uint64_t end = thread->markByteCount;
    for (uint64_t at = 0; at < end;) {
       ZamMarkKind kind = zam_mark_kind_at(thread, at);
        switch (kind) {
        case ZAMMARK_OBJECT: {
           ZamMarkObject entry;
            memcpy(&entry, thread->marks + at, sizeof entry);
            reach(vm, thread->operands[entry.operand]);
            break;
        }
        case ZAMMARK_ARRAY: {
           ZamMarkArray entry;
            memcpy(&entry, thread->marks + at, sizeof entry);
           ZamTypeID run = {
                .shortID = (ZamTypeShort)entry.element,
                .arrayLength = entry.length,
            };
            if (!entry.indirect)
                markValue(collector,
                          operandLocation(collector, thread, entry.operand),
                          run);
            else if (thread->operands[entry.operand] != 0)
                markValue(collector, thread->operands[entry.operand], run);
            break;
        }
        case ZAMMARK_VALUE: {
           ZamMarkValue entry;
            memcpy(&entry, thread->marks + at, sizeof entry);
            markValue(collector,
                      operandLocation(collector, thread, entry.operand),
                      zam_type_id_read((const ZamWord *)&entry.type));
            break;
        }
        case ZAMMARK_INTERIOR: {
           ZamMarkInterior entry;
            memcpy(&entry, thread->marks + at, sizeof entry);
            reach(vm, thread->operands[entry.operand] - entry.distance);
            break;
        }
        case ZAMMARK_FUNCTION:
        case ZAMMARK_SCOPE:
            break;
        }
        at += zam_mark_entry_bytes(kind);
    }
}

static int compareSpans(const void *left, const void *right) {
    uintptr_t a = *(const uintptr_t *)left;
    uintptr_t b = *(const uintptr_t *)right;
    return (a > b) - (a < b);
}

// youngLists is every list of the young generation: the newest allocations
// and the survivors, of both kinds.
static ZamObjectList *youngLists(ZamVM *vm, uint64_t index) {
    return index < ZAMYOUNG_COUNT ? &vm->young[index]
                                  : &vm->survivors[index - ZAMYOUNG_COUNT];
}

#define YOUNG_LIST_COUNT (2 * ZAMYOUNG_COUNT)

// collectYoungSpans lays the young generation out by address, so a word can be
// asked which young allocation, if any, it points into.
static void collectYoungSpans(ZamVM *vm) {
    vm->youngSpanCount = 0;
    for (uint64_t list = 0; list < YOUNG_LIST_COUNT; list++)
        for (void *object = youngLists(vm, list)->head; object != NULL;
             object = zam_list_next(object)) {
            if (vm->youngSpanCount == vm->youngSpanCapacity) {
                vm->youngSpanCapacity =
                    vm->youngSpanCapacity == 0 ? 64 : vm->youngSpanCapacity * 2;
                vm->youngSpans = zam_realloc(
                    vm->youngSpans,
                    vm->youngSpanCapacity * 2 * sizeof *vm->youngSpans);
            }
            uintptr_t *span = vm->youngSpans + vm->youngSpanCount * 2;
            span[0] = (uintptr_t)object;
            span[1] = span[0] + zam_heap_size(&vm->heap, object);
            vm->youngSpanCount++;
        }
    qsort(vm->youngSpans, vm->youngSpanCount, 2 * sizeof *vm->youngSpans,
          compareSpans);
}

// youngAllocationOf is the young allocation a word points into, or NULL.
static void *youngAllocationOf(const ZamVM *vm, ZamWord word) {
    uint64_t low = 0;
    uint64_t high = vm->youngSpanCount;
    while (low < high) {
        uint64_t middle = low + (high - low) / 2;
        const uintptr_t *span = vm->youngSpans + middle * 2;
        if (word < span[0])
            high = middle;
        else if (word >= span[1])
            low = middle + 1;
        else
            return (void *)span[0];
    }
    return NULL;
}

// markCards reaches every young allocation a word on a run of dirty cards
// points into, and keeps each card that held one on heldCards. The words are
// read without knowing what they are, so a number that happens to look like a
// young address keeps that allocation one collection longer, and nothing
// worse.
// It reads freed blocks as readily as live ones, which a sanitized build is
// told is deliberate.
#if defined(__has_feature)
#if __has_feature(address_sanitizer)
__attribute__((no_sanitize("address")))
#endif
#endif
static void markCards(void *context, const uint8_t *start, const uint8_t *end) {
    ZamVM *vm = context;
    vm->current.cards += (uint64_t)(end - start) / ZAMHEAP_CARD_BYTES;
    for (const uint8_t *at = start; at < end; at += ZAMWORD_BYTES) {
        ZamWord word = *(const ZamWord *)at;
        if (!zam_heap_contains(&vm->heap, (const void *)(uintptr_t)word))
            continue;
        void *object = youngAllocationOf(vm, word);
        if (object == NULL)
            continue;
        reach(vm, (ZamWord)(uintptr_t)object);

        uint8_t *card = (uint8_t *)((uintptr_t)at & ~(uintptr_t)(ZAMHEAP_CARD_BYTES - 1));
        if (vm->heldCardCount != 0 && vm->heldCards[vm->heldCardCount - 1] == card)
            continue;
        if (vm->heldCardCount == vm->heldCardCapacity) {
            vm->heldCardCapacity =
                vm->heldCardCapacity == 0 ? 64 : vm->heldCardCapacity * 2;
            vm->heldCards = zam_realloc(
                vm->heldCards, vm->heldCardCapacity * sizeof *vm->heldCards);
        }
        vm->heldCards[vm->heldCardCount++] = card;
    }
}

// isDefaultBody reports whether a method body is Object's own: a call to the
// one native it names and nothing else. A body that does anything more, or
// anything else, is the program's own.
static bool isDefaultBody(const ZamImage *image, uint64_t instruction,
                          const char *name) {
    uint64_t natives = 0;
    for (uint64_t at = instruction; at < image->operationsByteCount;) {
       ZamOpcode opcode = (ZamOpcode)image->operations[at];
        if (opcode >= ZAMOPCODE_COUNT)
            return false;
        switch (opcode) {
        case OpReturnVoid:
        case OpReturn:
            return natives == 1;
        case OpNative: {
            ZamWord slot;
            memcpy(&slot, image->operations + at + 1, sizeof slot);
            uint64_t length;
            const char *native = zam_image_native_name(image, slot, &length);
            if (length != strlen(name) || memcmp(native, name, length) != 0)
                return false;
            natives++;
            break;
        }
        case OpPushScope:
        case OpPopScope:
        case OpPushVariable:
        case OpPushReceiver:
        case OpPushReceiverType:
        case OpPushGenericArgType:
        case OpPushI64:
        case OpLoad:
        case OpCallNative:
        case OpPop:
        case OpPopWord:
        case OpPopMark:
        case OpMarkAs:
            break;
        default:
            return false;
        }
        at += 1 + (uint64_t)zam_opcode_operands(opcode) * ZAMWORD_BYTES;
    }
    return false;
}

// marksCustom reports whether marking a value of the type runs a Mark of the
// program's own: a class that overrides Mark, or a value holding one inline.
// What a reference names is an allocation of its own and answers for itself.
static bool marksCustom(ZamVM *vm, ZamTypeID type) {
    const ZamImage *image = &vm->image;
    type.arrayLength = 0;
    uint16_t declID = ZAMTYPE_DECL(type.shortID);
    bool isClass = false;
    switch (zam_image_bucket_of(image, declID)) {
    case ZAMBUCKET_CLASS:
    case ZAMBUCKET_GENERIC_CLASS:
        isClass = true;
        break;
    case ZAMBUCKET_TUPLE:
    case ZAMBUCKET_STRUCT:
    case ZAMBUCKET_GENERIC_STRUCT:
    case ZAMBUCKET_ENUM:
        break;
    default:
        return false;
    }

    uint8_t known = atomic_load_explicit(&vm->customMarkDecls[declID],
                                         memory_order_relaxed);
    if (known != 0)
        return known == 2;

    bool custom = isClass &&
                  zam_type_method_count(image, type.shortID) >
                      ZAMOBJECT_SLOT_MARK &&
                  !isDefaultBody(image,
                                 zam_type_method(image, type.shortID,
                                                 ZAMOBJECT_SLOT_MARK)
                                     .instruction,
                                 "STD_MARK");
    uint64_t count = zam_type_member_count(image, type.shortID);
    for (uint64_t index = 0; index < count && !custom; index++) {
        ZamMemberDefinition member = zam_type_member(image, type.shortID, index);
        custom = marksCustom(vm, zam_member_type(&member));
    }
    atomic_store_explicit(&vm->customMarkDecls[declID], custom ? 2 : 1,
                          memory_order_relaxed);
    return custom;
}

static bool deletesCustom(ZamVM *vm, ZamTypeShort shortID);

bool zam_gc_deletes_custom(ZamVM *vm, ZamObjectKind kind, ZamTypeShort shortID) {
    return (kind == ZAMOBJECT_INSTANCE || kind == ZAMOBJECT_TAILED_INSTANCE) &&
           zam_type_is_class(&vm->image, shortID) && deletesCustom(vm, shortID);
}

// deletesCustom reports whether a class's Delete is the program's own. One
// that is Object's only marks the instance dead, which an unreachable instance
// already is.
static bool deletesCustom(ZamVM *vm, ZamTypeShort shortID) {
    uint16_t declID = ZAMTYPE_DECL(shortID);
    uint8_t known = atomic_load_explicit(&vm->customDeleteDecls[declID],
                                         memory_order_relaxed);
    if (known != 0)
        return known == 2;
    bool custom =
        zam_type_method_count(&vm->image, shortID) > ZAMOBJECT_SLOT_DELETE &&
        !isDefaultBody(
        &vm->image,
        zam_type_method(&vm->image, shortID, ZAMOBJECT_SLOT_DELETE).instruction,
        "STD_DELETE");
    atomic_store_explicit(&vm->customDeleteDecls[declID], custom ? 2 : 1,
                          memory_order_relaxed);
    return custom;
}

// tracesCustom reports whether tracing an allocation runs a Mark of the
// program's own, which decides the old list it is promoted onto.
static bool tracesCustom(ZamVM *vm, void *object) {
    const ZamImage *image = &vm->image;
   ZamMarkWord *mark = zam_mark_word_of(object);
    switch ((ZamObjectKind)mark->kind) {
    case ZAMOBJECT_INSTANCE:
    case ZAMOBJECT_TAILED_INSTANCE: {
       ZamTypeShort shortID = zam_object_header_of(object)->shortID;
        return zam_type_is_class(image, shortID) &&
               marksCustom(vm, (ZamTypeID){.shortID = shortID});
    }
    case ZAMOBJECT_FUNCTION_CLOSURE:
    case ZAMOBJECT_REFERENCE_METHOD_CLOSURE:
    case ZAMOBJECT_VALUE_METHOD_CLOSURE: {
        const ZamClosure *closure = zam_closure_of(object);
        uint64_t entries = 1 + closure->captureCount;
        if (mark->kind == ZAMOBJECT_VALUE_METHOD_CLOSURE)
            entries++;
        const ZamWord *entry = zam_closure_first(closure);
        for (uint64_t index = 0; index < entries; index++) {
           ZamTypeID type = zam_closure_entry_type(entry);
            if (marksCustom(vm, type))
                return true;
            entry = zam_closure_entry_next(entry, zam_value_words(image, type));
        }
        return false;
    }
    case ZAMOBJECT_ANY: {
        const ZamWord *body =
            (const ZamWord *)((const uint8_t *)object +
                              zam_object_header_bytes(ZAMOBJECT_INSTANCE));
        return body[3] != 0 && marksCustom(vm, zam_type_id_read(body));
    }
    case ZAMOBJECT_STRING:
    case ZAMOBJECT_THREAD:
    case ZAMOBJECT_KIND_COUNT:
        return false;
    }
    return false;
}

// retraceCustom runs every old Mark of the program's own again, against what
// memory holds now. Nothing short of running it can say what it depends on.
static void retraceCustom(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    const ZamOldList lists[] = {ZAMOLD_MARKS, ZAMOLD_BOTH};
    for (size_t list = 0; list < sizeof lists / sizeof *lists; list++)
        for (void *object = vm->old[lists[list]].head; object != NULL;
             object = zam_list_next(object))
            if (zam_mark_word_of(object)->state != ZAMMARK_STATE_DEAD) {
                trace(collector, object);
                vm->current.retraced++;
            }
}

// markRoots marks everything a thread or a static holds, and in a minor
// collection everything a dirty card holds and every old Mark of the
// program's own reaches. The thread list is read before
// anything is marked, a Mark body being able to start a thread.
static void markRoots(ZamThread *collector, bool major) {
   ZamVM *vm = collector->vm;

    pthread_mutex_lock(&vm->threadLock);
    uint64_t threadCount = 0;
    for (ZamThread *thread = &vm->main; thread != NULL; thread = thread->next)
        threadCount++;
   ZamThread **threads = zam_alloc(threadCount * sizeof *threads);
    uint64_t index = 0;
    for (ZamThread *thread = &vm->main; thread != NULL; thread = thread->next)
        threads[index++] = thread;
    pthread_mutex_unlock(&vm->threadLock);

    for (index = 0; index < threadCount; index++) {
       ZamThread *thread = threads[index];
        markStack(collector, thread);
        for (int sig = 0; sig < ZAM_SIGNAL_MAX; sig++)
            reach(vm, (ZamWord)(uintptr_t)thread->signalHandlers[sig]);

        // A thread still running is alive whatever refers to it.
        if (thread != &vm->main && thread->state != ZAMTHREAD_EXITED)
            reach(vm, (ZamWord)(uintptr_t)thread);
    }
    free(threads);

    for (index = 0; index < vm->image.staticRootCount; index++) {
       ZamTypeID type;
        uint64_t slot = zam_image_static_root(&vm->image, index, &type);
        markValue(collector, (ZamWord)(uintptr_t)zam_vm_static(vm, slot), type);
    }

    if (!major) {
        zam_heap_dirty_cards(&vm->heap, markCards, vm);
        retraceCustom(collector);
    }

    drain(collector);
}

// finalize runs Delete on every unreachable instance with a Delete of the
// program's own that nothing else unreachable reaches; one with Object's is
// simply freed. What such an instance reaches is kept for one more
// collection rather than finalized alongside it, so that an instance deleting
// what it owns never deletes what the collector has already finalized.
// Whatever it deletes is freed with it.
static void finalize(ZamThread *collector, bool major) {
   ZamVM *vm = collector->vm;

    uint64_t count = 0;
    uint64_t capacity = 0;
    void **unreachable = NULL;
    ZamObjectList *lists[] = {
        &vm->young[ZAMYOUNG_DELETES], &vm->survivors[ZAMYOUNG_DELETES],
        major ? &vm->old[ZAMOLD_DELETES] : NULL,
        major ? &vm->old[ZAMOLD_BOTH] : NULL};
    for (size_t list = 0; list < sizeof lists / sizeof *lists; list++)
        for (void *object = lists[list] != NULL ? lists[list]->head : NULL;
             object != NULL; object = zam_list_next(object)) {
            if (zam_mark_word_of(object)->state != ZAMMARK_STATE_WHITE)
                continue;
            if (count == capacity) {
                capacity = capacity == 0 ? 64 : capacity * 2;
                unreachable =
                    zam_realloc(unreachable, capacity * sizeof *unreachable);
            }
            unreachable[count++] = object;
        }

    // Grey stands for an instance being traced from, so that it does not reach
    // itself. One left grey after its trace was reached from nothing else
    // traced so far, and one reached later turns black all the same.
    for (uint64_t index = 0; index < count; index++) {
       ZamMarkWord *mark = zam_mark_word_of(unreachable[index]);
        if (mark->state != ZAMMARK_STATE_WHITE)
            continue;
        mark->state = ZAMMARK_STATE_GREY;
        trace(collector, unreachable[index]);
        drain(collector);
        if (mark->state == ZAMMARK_STATE_GREY)
            mark->state = ZAMMARK_STATE_WHITE;
    }

    for (uint64_t index = 0; index < count; index++)
        if (zam_mark_word_of(unreachable[index])->state == ZAMMARK_STATE_WHITE) {
            call(collector, (ZamWord)(uintptr_t)unreachable[index],
                 ZAMOBJECT_SLOT_DELETE);
            vm->current.finalized++;
        }
    free(unreachable);
}

// release forgets a finished thread: it leaves the VM's list, its system
// thread is reaped if no join did so, and its stacks are freed.
static void release(ZamVM *vm, ZamThread *thread) {
    pthread_mutex_lock(&vm->threadLock);
    for (ZamThread *previous = &vm->main; previous->next != NULL;
         previous = previous->next)
        if (previous->next == thread) {
            previous->next = thread->next;
            break;
        }
    pthread_mutex_unlock(&vm->threadLock);

    if (!thread->joined)
        pthread_join(thread->systemThread, NULL);
    zam_thread_free(thread);
}

// SweepFate is where an allocation that outlives a sweep goes next.
typedef enum SweepFate {
    // A first survival of a minor collection: onto the survivors, white, to be
    // traced again by the next.
    SWEEP_SURVIVE,
    // A second: promoted, and its cards dirtied, since what it holds may still
    // be young.
    SWEEP_PROMOTE,
    // A major collection's: promoted, nothing being young once it is done.
    SWEEP_KEEP,
} SweepFate;

// sweepList frees every allocation on a list left unreachable or deleted, and
// sends every other one where its fate says. An old allocation is left black
// until the next major collection whitens it, which is what keeps a minor
// collection from tracing into it.
static void sweepList(ZamThread *collector, void *object, bool deletes,
                      SweepFate fate) {
   ZamVM *vm = collector->vm;
    uint64_t freed = 0;
    while (object != NULL) {
       ZamMarkWord *mark = zam_mark_word_of(object);
        void *next = zam_list_next(object);

        bool live = mark->state == ZAMMARK_STATE_BLACK ||
                    mark->state == ZAMMARK_STATE_GREY ||
                    (mark->kind == ZAMOBJECT_THREAD &&
                     ((ZamThread *)object)->state != ZAMTHREAD_EXITED);
        if (!live) {
            if (mark->kind == ZAMOBJECT_THREAD)
                release(vm, (ZamThread *)object);
            freed += zam_heap_size(&vm->heap, object);
            zam_heap_free_stopped(&vm->heap, object);
        } else if (fate == SWEEP_SURVIVE) {
            mark->state = ZAMMARK_STATE_WHITE;
            zam_list_push(&vm->survivors[deletes ? ZAMYOUNG_DELETES
                                                 : ZAMYOUNG_PLAIN],
                          object);
            vm->current.survived += zam_heap_size(&vm->heap, object);
        } else {
            mark->state = ZAMMARK_STATE_BLACK;
            ZamOldList list = (tracesCustom(vm, object) ? ZAMOLD_MARKS : 0) |
                              (deletes ? ZAMOLD_DELETES : 0);
            zam_list_push(&vm->old[list], object);
            if (fate == SWEEP_PROMOTE) {
                uint64_t bytes = zam_heap_size(&vm->heap, object);
                zam_heap_dirty(&vm->heap, object, bytes);
                vm->current.promoted += bytes;
            }
        }
        object = next;
    }
    atomic_fetch_sub(&vm->allocatedByteCount, freed);
}

// sweep frees what the collection left unreached. A minor collection promotes
// its survivors' survivors and keeps the rest of the young generation as
// survivors, then dirties again every card that held a young address. A major
// collection promotes everything it keeps.
static void sweep(ZamThread *collector, bool major) {
   ZamVM *vm = collector->vm;
    ZamObjectList young[ZAMYOUNG_COUNT];
    ZamObjectList survivors[ZAMYOUNG_COUNT];
    memcpy(young, vm->young, sizeof young);
    memcpy(survivors, vm->survivors, sizeof survivors);
    memset(vm->young, 0, sizeof vm->young);
    memset(vm->survivors, 0, sizeof vm->survivors);

    if (major) {
        ZamObjectList old[ZAMOLD_COUNT];
        memcpy(old, vm->old, sizeof old);
        memset(vm->old, 0, sizeof vm->old);
        for (int list = 0; list < ZAMOLD_COUNT; list++)
            sweepList(collector, old[list].head,
                      (list & ZAMOLD_DELETES) != 0, SWEEP_KEEP);
    }
    for (int list = 0; list < ZAMYOUNG_COUNT; list++) {
        bool deletes = list == ZAMYOUNG_DELETES;
        sweepList(collector, survivors[list].head, deletes,
                  major ? SWEEP_KEEP : SWEEP_PROMOTE);
        sweepList(collector, young[list].head, deletes,
                  major ? SWEEP_KEEP : SWEEP_SURVIVE);
    }

    if (!major)
        for (uint64_t index = 0; index < vm->heldCardCount; index++)
            zam_heap_dirty(&vm->heap, vm->heldCards[index], 1);
}

// whiten leaves every allocation on a list unreached, other than one deleted.
static void whiten(void *object) {
    for (; object != NULL; object = zam_list_next(object)) {
       ZamMarkWord *mark = zam_mark_word_of(object);
        if (mark->state != ZAMMARK_STATE_DEAD)
            mark->state = ZAMMARK_STATE_WHITE;
    }
}

// condemnYoung marks as a minor collection would and answers every young
// allocation it would free, then leaves both generations unreached for a
// major collection to mark afresh.
static void **condemnYoung(ZamThread *collector, uint64_t *count) {
   ZamVM *vm = collector->vm;
    collectYoungSpans(vm);
    markRoots(collector, false);

    uint64_t capacity = 0;
    void **condemned = NULL;
    *count = 0;
    for (uint64_t list = 0; list < YOUNG_LIST_COUNT; list++)
        for (void *object = youngLists(vm, list)->head; object != NULL;
             object = zam_list_next(object)) {
            if (zam_mark_word_of(object)->state != ZAMMARK_STATE_WHITE)
                continue;
            if (*count == capacity) {
                capacity = capacity == 0 ? 64 : capacity * 2;
                condemned = zam_realloc(condemned, capacity * sizeof *condemned);
            }
            condemned[(*count)++] = object;
        }

    for (uint64_t list = 0; list < YOUNG_LIST_COUNT; list++)
        whiten(youngLists(vm, list)->head);
    return condemned;
}

bool zam_gc_collect(ZamVM *vm, ZamThread *collector, bool major) {
    vm->heldCardCount = 0;
    uint64_t condemnedCount = 0;
    void **condemned = NULL;
    if (vm->verify) {
        condemned = condemnYoung(collector, &condemnedCount);
        major = true;
    }

    if (major) {
        for (int list = 0; list < ZAMOLD_COUNT; list++)
            whiten(vm->old[list].head);
    } else
        collectYoungSpans(vm);
    markRoots(collector, major);

    for (uint64_t index = 0; index < condemnedCount; index++) {
       ZamMarkWord *mark = zam_mark_word_of(condemned[index]);
        if (mark->state != ZAMMARK_STATE_WHITE)
            zam_die(ZAMEXIT_PANIC,
                    "a minor collection would have freed the reachable "
                    "allocation at %p, of kind %u\n",
                    condemned[index], (unsigned)mark->kind);
    }
    free(condemned);

    finalize(collector, major);
    sweep(collector, major);
    // A minor collection cleaned the cards it scanned. One a Mark or Delete
    // dirtied since is left for the next to scan, which costs no more than
    // the scan.
    if (major)
        zam_heap_clear_cards(&vm->heap);
    vm->current.returned = zam_heap_scavenge(&vm->heap);
    return major;
}

void zam_gc_mark_value(ZamThread *thread, ZamWord location, ZamTypeID type) {
    if (zam_gc_collecting(thread))
        markValue(thread, location, type);
}

void zam_gc_mark_members(ZamThread *thread, ZamWord instance) {
    if (!zam_gc_collecting(thread))
        return;

    const ZamImage *image = &thread->vm->image;
    const uint8_t *at = zam_thread_location_address(thread, instance);
   ZamObjectKind kind = (ZamObjectKind)zam_mark_word_of((void *)at)->kind;
   ZamTypeShort shortID = zam_object_header_of((void *)at)->shortID;
    uint64_t tailLength =
        kind == ZAMOBJECT_TAILED_INSTANCE ? zam_object_tail_length_of(at) : 0;
    uint64_t header = zam_object_header_bytes(kind);

    uint64_t count = zam_type_member_count(image, shortID);
    for (uint64_t index = 0; index < count; index++) {
        ZamMemberDefinition member =
            zam_type_member(image, shortID, index);
       ZamTypeID memberType = zam_member_type(&member);
        if (tailLength != 0 && index + 1 == count)
            memberType.arrayLength = tailLength;
        markValue(thread, instance + header + member.offset, memberType);
    }
}

void zam_gc_mark_only(ZamThread *thread, ZamWord instance) {
    if (!zam_gc_collecting(thread))
        return;
    void *object = allocationOf(thread->vm, instance);
    if (object != NULL &&
        zam_mark_word_of(object)->state == ZAMMARK_STATE_WHITE)
        zam_mark_word_of(object)->state = ZAMMARK_STATE_BLACK;
}
