#include "gc.h"

#include "definition.h"
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
        const ZamMemberDefinition *member =
            zam_type_member(image, type.shortID, index);
       ZamTypeID memberType = zam_member_type(member);
        if (type.tailLength != 0 && index + 1 == count)
            memberType.arrayLength = type.tailLength;
        markValue(collector, location + header + member->offset, memberType);
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

// markRoots marks everything a thread or a static holds. The thread list is
// read before anything is marked, a Mark body being able to start a thread.
static void markRoots(ZamThread *collector) {
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

    drain(collector);
}

// finalize runs Delete on every unreachable instance that nothing else
// unreachable reaches. What such an instance reaches is kept for one more
// collection rather than finalized alongside it, so that an instance deleting
// what it owns never deletes what the collector has already finalized.
// Whatever it deletes is freed with it.
static void finalize(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    const ZamImage *image = &vm->image;

    uint64_t count = 0;
    uint64_t capacity = 0;
    void **unreachable = NULL;
    for (void *object = atomic_load(&vm->allocations); object != NULL;
         object = (void *)(uintptr_t)zam_mark_word_of(object)->upvalue) {
       ZamMarkWord *mark = zam_mark_word_of(object);
        if (mark->state != ZAMMARK_STATE_WHITE ||
            (mark->kind != ZAMOBJECT_INSTANCE &&
             mark->kind != ZAMOBJECT_TAILED_INSTANCE) ||
            !zam_type_is_class(image, zam_object_header_of(object)->shortID))
            continue;
        if (count == capacity) {
            capacity = capacity == 0 ? 64 : capacity * 2;
            unreachable = zam_realloc(unreachable, capacity * sizeof *unreachable);
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
        if (zam_mark_word_of(unreachable[index])->state == ZAMMARK_STATE_WHITE)
            call(collector, (ZamWord)(uintptr_t)unreachable[index],
                 ZAMOBJECT_SLOT_DELETE);
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

// sweep frees every allocation left unreachable or deleted, and leaves every
// other one unreached for the next collection.
static void sweep(ZamVM *vm) {
    uint64_t freed = 0;
    void *head = NULL;
   ZamMarkWord *tail = NULL;
    for (void *object = atomic_load(&vm->allocations); object != NULL;) {
       ZamMarkWord *mark = zam_mark_word_of(object);
        void *next = (void *)(uintptr_t)mark->upvalue;

        bool live = mark->state == ZAMMARK_STATE_BLACK ||
                    mark->state == ZAMMARK_STATE_GREY ||
                    (mark->kind == ZAMOBJECT_THREAD &&
                     ((ZamThread *)object)->state != ZAMTHREAD_EXITED);
        if (live) {
            mark->state = ZAMMARK_STATE_WHITE;
            mark->upvalue = 0;
            if (tail == NULL)
                head = object;
            else
                tail->upvalue = (uintptr_t)object;
            tail = mark;
        } else {
            if (mark->kind == ZAMOBJECT_THREAD)
                release(vm, (ZamThread *)object);
            freed += zam_alloc_size(object);
            free(object);
        }
        object = next;
    }

    atomic_store(&vm->allocations, head);
    atomic_fetch_sub(&vm->allocatedByteCount, freed);
}

void zam_gc_collect(ZamVM *vm, ZamThread *collector) {
    markRoots(collector);
    finalize(collector);
    sweep(vm);
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
        const ZamMemberDefinition *member =
            zam_type_member(image, shortID, index);
       ZamTypeID memberType = zam_member_type(member);
        if (tailLength != 0 && index + 1 == count)
            memberType.arrayLength = tailLength;
        markValue(thread, instance + header + member->offset, memberType);
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
