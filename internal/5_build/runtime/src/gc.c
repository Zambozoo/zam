#include "gc.h"

#include "definition.h"
#include "opcode.h"
#include "run.h"
#include "zamrt.h"

#include <sched.h>

bool zam_gc_collecting(const ZamThread *thread) {
    return thread->vm->collector == thread || thread->collects;
}

// allocationOf is the allocation a word points into, or NULL where it points
// into nothing the collector owns: a frame location, the static region, the
// program's own memory, or nothing at all. An interior value is stepped out of
// to the allocation that holds it. The heap answers rather than the word's
// mark word, so that a word the marker read while the program was changing it
// is never trusted to point at one.
static void *allocationOf(ZamVM *vm, ZamWord word) {
    if (word == 0 || zam_is_frame_location(word))
        return NULL;
    return zam_heap_object_of(&vm->heap, (const void *)(uintptr_t)word);
}

// reserveGray makes room on a thread's gray stack for count more.
static void reserveGray(ZamThread *thread, uint64_t count) {
    if (thread->grayCount + count <= thread->grayCapacity)
        return;
    uint64_t capacity = thread->grayCapacity == 0 ? 64 : thread->grayCapacity;
    while (capacity < thread->grayCount + count)
        capacity *= 2;
    thread->gray = zam_realloc(thread->gray, capacity * sizeof *thread->gray);
    thread->grayCapacity = capacity;
}

static void pushGray(ZamThread *thread, void *object) {
    reserveGray(thread, 1);
    thread->gray[thread->grayCount++] = object;
}

static void *youngAllocationOf(const ZamVM *vm, ZamWord word);

// reach says the allocation a word points into is reachable, queueing it on
// the thread to be traced the first time it is said. A minor collection
// alongside the marker reaches only what it collects, leaving everything else
// to the markers.
static void reach(ZamThread *thread, ZamWord word) {
    ZamVM *vm = thread->vm;
    void *object = vm->freshOnly ? youngAllocationOf(vm, word)
                                 : allocationOf(vm, word);
    if (object == NULL ||
        !zam_mark_swap_state(object, ZAMMARK_STATE_WHITE, ZAMMARK_STATE_BLACK))
        return;
    pushGray(thread, object);
}

void zam_gc_allocated(ZamThread *thread, void *object) {
    if (thread->vm->traceAllocations)
        pushGray(thread, object);
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
        reach(collector, word);
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
        zam_mark_swap_state(payload, ZAMMARK_STATE_WHITE, ZAMMARK_STATE_BLACK);
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
    while (collector->grayCount != 0)
        trace(collector, collector->gray[--collector->grayCount]);
}

// SHARE_CHUNK is the most work a marker hands over or takes up at once.
#define SHARE_CHUNK 64

// giveWork moves the top count of a marker's gray stack onto the shared one,
// and takeWork the top count of the shared one onto a marker's. The caller
// holds markLock.
static void giveWork(ZamVM *vm, ZamThread *marker, uint64_t count) {
    if (count == 0)
        return;
    if (vm->sharedCount + count > vm->sharedCapacity) {
        uint64_t capacity = vm->sharedCapacity == 0 ? 64 : vm->sharedCapacity;
        while (capacity < vm->sharedCount + count)
            capacity *= 2;
        vm->shared = zam_realloc(vm->shared, capacity * sizeof *vm->shared);
        vm->sharedCapacity = capacity;
    }
    marker->grayCount -= count;
    memcpy(vm->shared + vm->sharedCount, marker->gray + marker->grayCount,
           count * sizeof *vm->shared);
    vm->sharedCount += count;
}

static void takeWork(ZamVM *vm, ZamThread *marker, uint64_t count) {
    if (count == 0)
        return;
    reserveGray(marker, count);
    vm->sharedCount -= count;
    memcpy(marker->gray + marker->grayCount, vm->shared + vm->sharedCount,
           count * sizeof *vm->shared);
    marker->grayCount += count;
}

// checkpoint is where a marker stops, between one piece of its work and the
// next, for a minor collection that asked it to.
static void checkpoint(ZamVM *vm) {
    if (!atomic_load(&vm->markerPause))
        return;
    pthread_mutex_lock(&vm->markLock);
    vm->markersWorking--;
    pthread_cond_broadcast(&vm->markChanged);
    while (atomic_load(&vm->markerPause))
        pthread_cond_wait(&vm->markChanged, &vm->markLock);
    vm->markersWorking++;
    pthread_mutex_unlock(&vm->markLock);
}

void zam_gc_pause_markers(ZamVM *vm) {
    pthread_mutex_lock(&vm->markLock);
    atomic_fetch_add(&vm->markerPause, 1);
    while (vm->markersWorking != 0)
        pthread_cond_wait(&vm->markChanged, &vm->markLock);
    pthread_mutex_unlock(&vm->markLock);
}

void zam_gc_resume_markers(ZamVM *vm) {
    pthread_mutex_lock(&vm->markLock);
    if (atomic_fetch_sub(&vm->markerPause, 1) == 1)
        pthread_cond_broadcast(&vm->markChanged);
    pthread_mutex_unlock(&vm->markLock);
}

void zam_gc_request_finish(ZamVM *vm) {
    if (atomic_exchange(&vm->finishRequested, true))
        return;
    pthread_mutex_lock(&vm->markLock);
    pthread_cond_broadcast(&vm->markChanged);
    pthread_mutex_unlock(&vm->markLock);
}

static bool tracesCustom(ZamVM *vm, void *object);

static void pushRetrace(ZamVM *vm, void *object) {
    if (vm->retraceCount == vm->retraceCapacity) {
        vm->retraceCapacity =
            vm->retraceCapacity == 0 ? 64 : vm->retraceCapacity * 2;
        vm->retrace =
            zam_realloc(vm->retrace, vm->retraceCapacity * sizeof *vm->retrace);
    }
    vm->retrace[vm->retraceCount++] = object;
}

// traceAlongside traces an allocation as a marker does alongside the program.
// A Mark of the program's own may read anything at all, which the program may
// be changing, so every allocation with one reached is left for the remark to
// run its Mark, once, with the program stopped. Object's own reads only what
// the allocation's type says it holds. An any box's payload and the type it
// was boxed under are written apart, so a box the program is filling is
// traced at the remark too.
static void traceAlongside(ZamThread *marker, void *object) {
   ZamVM *vm = marker->vm;
    if (tracesCustom(vm, object))
        return;
    if (zam_mark_word_of(object)->kind == ZAMOBJECT_ANY) {
        pthread_mutex_lock(&vm->markLock);
        pushRetrace(vm, object);
        pthread_mutex_unlock(&vm->markLock);
    } else
        trace(marker, object);
    // Verification widens every window the program has to change what the
    // marker is looking at.
    if (vm->verify)
        sched_yield();
}

// drainShared traces until no marker taking part in the round has work left,
// handing half of its own over whenever another is waiting for some, and
// taking up what is handed over once its own runs out. It answers how many
// allocations it traced.
//
// Alongside the program a marker stops at its checkpoints, and counts as
// stopped while it waits for work, when it touches nothing. It gives up once
// the remark is asked for, leaving what it has on the shared stack.
static uint64_t drainShared(ZamThread *marker, bool alongside) {
   ZamVM *vm = marker->vm;
    uint64_t traced = 0;
    for (;;) {
        while (marker->grayCount != 0 &&
               !(alongside && atomic_load(&vm->finishRequested))) {
            if (alongside)
                checkpoint(vm);
            void *object = marker->gray[--marker->grayCount];
            if (alongside)
                traceAlongside(marker, object);
            else
                trace(marker, object);
            traced++;
            if (marker->grayCount > 1 &&
                atomic_load_explicit(&vm->markersIdle, memory_order_relaxed) !=
                    0) {
                uint64_t count = marker->grayCount / 2;
                pthread_mutex_lock(&vm->markLock);
                giveWork(vm, marker,
                         count < SHARE_CHUNK ? count : SHARE_CHUNK);
                pthread_cond_broadcast(&vm->markChanged);
                pthread_mutex_unlock(&vm->markLock);
            }
        }

        pthread_mutex_lock(&vm->markLock);
        giveWork(vm, marker, marker->grayCount);
        vm->markersActive--;
        atomic_fetch_add(&vm->markersIdle, 1);
        if (alongside)
            vm->markersWorking--;
        pthread_cond_broadcast(&vm->markChanged);
        for (;;) {
            bool finishing = alongside && atomic_load(&vm->finishRequested);
            bool over = vm->sharedCount == 0 && vm->markersActive == 0;
            bool paused = alongside && atomic_load(&vm->markerPause);
            if (!paused && (finishing || over || vm->sharedCount != 0))
                break;
            pthread_cond_wait(&vm->markChanged, &vm->markLock);
        }
        atomic_fetch_sub(&vm->markersIdle, 1);
        if (alongside)
            vm->markersWorking++;
        if (vm->sharedCount == 0 ||
            (alongside && atomic_load(&vm->finishRequested))) {
            pthread_mutex_unlock(&vm->markLock);
            return traced;
        }
        vm->markersActive++;
        takeWork(vm, marker,
                 vm->sharedCount < SHARE_CHUNK ? vm->sharedCount : SHARE_CHUNK);
        pthread_mutex_unlock(&vm->markLock);
    }
}

// helperMain is a helper's thread: it takes part in every round of marking
// until the VM is done with it.
static void *helperMain(void *context) {
   ZamThread *helper = context;
   ZamVM *vm = helper->vm;
    uint64_t seen = 0;
    pthread_mutex_lock(&vm->markLock);
    for (;;) {
        while (!vm->helpersQuit && vm->markRound == seen)
            pthread_cond_wait(&vm->markChanged, &vm->markLock);
        if (vm->helpersQuit)
            break;
        seen = vm->markRound;
        bool alongside = vm->markRoundAlongside;
        pthread_mutex_unlock(&vm->markLock);
        uint64_t traced = drainShared(helper, alongside);
        pthread_mutex_lock(&vm->markLock);
        vm->current.helped += traced;
        if (alongside)
            vm->markersWorking--;
        vm->helpersDone++;
        pthread_cond_broadcast(&vm->markChanged);
    }
    pthread_mutex_unlock(&vm->markLock);
    return NULL;
}

// startHelpers makes the helpers the first time a collection marks in
// parallel. Each runs Mark bodies on a stack of its own.
static void startHelpers(ZamVM *vm) {
    if (vm->helpers != NULL || vm->markerCount <= 1)
        return;
    uint32_t count = vm->markerCount - 1;
    vm->helpers = zam_alloc(count * sizeof *vm->helpers);
    vm->helperThreads = zam_alloc(count * sizeof *vm->helperThreads);
    for (uint32_t index = 0; index < count; index++) {
       ZamThread *helper = zam_alloc(sizeof *helper);
        memset(helper, 0, sizeof *helper);
        zam_thread_init(helper, vm, vm->image.initialStackSize);
        helper->collects = true;
        vm->helpers[index] = helper;
        if (pthread_create(&vm->helperThreads[index], NULL, helperMain,
                           helper) != 0)
            zam_die(ZAMEXIT_PANIC, "could not start the collector's helpers\n");
    }
    vm->helperCount = count;
}

void zam_gc_stop_helpers(ZamVM *vm) {
    pthread_mutex_lock(&vm->markLock);
    vm->helpersQuit = true;
    pthread_cond_broadcast(&vm->markChanged);
    pthread_mutex_unlock(&vm->markLock);
    for (uint32_t index = 0; index < vm->helperCount; index++) {
        pthread_join(vm->helperThreads[index], NULL);
        zam_thread_free(vm->helpers[index]);
        free(vm->helpers[index]);
    }
    free(vm->helpers);
    free(vm->helperThreads);
    vm->helpers = NULL;
    vm->helperThreads = NULL;
    vm->helperCount = 0;
}

// shareAll moves the whole of a thread's gray stack onto the shared one, for
// the markers to take up.
static void shareAll(ZamThread *thread) {
   ZamVM *vm = thread->vm;
    pthread_mutex_lock(&vm->markLock);
    giveWork(vm, thread, thread->grayCount);
    pthread_cond_broadcast(&vm->markChanged);
    pthread_mutex_unlock(&vm->markLock);
}

// RECRUIT_AFTER is how many allocations the coordinator traces alone before it
// sets the helpers going on what is left, so that a mark too small to repay
// waking them never does.
#define RECRUIT_AFTER 1024

// drainParallel is drain shared with every helper, the coordinator taking
// part as one more marker. What a helper allocates with the program stopped is
// black, and is put with the collector's own allocations once the round is
// over, to be swept with them.
static void drainParallel(ZamThread *coordinator, bool alongside) {
   ZamVM *vm = coordinator->vm;
    pthread_mutex_lock(&vm->markLock);
    takeWork(vm, coordinator, vm->sharedCount);
    pthread_mutex_unlock(&vm->markLock);
    for (uint64_t traced = 0;
         coordinator->grayCount != 0 &&
         !(alongside && atomic_load(&vm->finishRequested)) &&
         (vm->markerCount <= 1 || traced < RECRUIT_AFTER ||
          coordinator->grayCount < 2);
         traced++) {
        if (alongside)
            checkpoint(vm);
        void *object = coordinator->gray[--coordinator->grayCount];
        if (alongside)
            traceAlongside(coordinator, object);
        else
            trace(coordinator, object);
    }
    if (coordinator->grayCount == 0 ||
        (alongside && atomic_load(&vm->finishRequested))) {
        shareAll(coordinator);
        return;
    }

    startHelpers(vm);
    pthread_mutex_lock(&vm->markLock);
    vm->markersActive = 1 + vm->helperCount;
    vm->helpersDone = 0;
    if (alongside)
        vm->markersWorking += vm->helperCount;
    vm->markRoundAlongside = alongside;
    vm->markRound++;
    pthread_cond_broadcast(&vm->markChanged);
    pthread_mutex_unlock(&vm->markLock);

    drainShared(coordinator, alongside);

    // A helper waits out a minor collection before it is done, so the
    // coordinator counts as stopped while it waits for them.
    pthread_mutex_lock(&vm->markLock);
    if (alongside) {
        vm->markersWorking--;
        pthread_cond_broadcast(&vm->markChanged);
    }
    while (vm->helpersDone < vm->helperCount ||
           (alongside && atomic_load(&vm->markerPause)))
        pthread_cond_wait(&vm->markChanged, &vm->markLock);
    if (alongside)
        vm->markersWorking++;
    pthread_mutex_unlock(&vm->markLock);

    if (!alongside)
        for (uint32_t index = 0; index < vm->helperCount; index++)
            for (int list = 0; list < ZAMYOUNG_COUNT; list++)
                zam_list_splice(&vm->young[list],
                                &vm->helpers[index]->young[list]);
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
    uint64_t end = thread->markByteCount;
    for (uint64_t at = 0; at < end;) {
       ZamMarkKind kind = zam_mark_kind_at(thread, at);
        switch (kind) {
        case ZAMMARK_OBJECT: {
           ZamMarkObject entry;
            memcpy(&entry, thread->marks + at, sizeof entry);
            reach(collector, thread->operands[entry.operand]);
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
            reach(collector, thread->operands[entry.operand] - entry.distance);
            break;
        }
        case ZAMMARK_FUNCTION:
        case ZAMMARK_SCOPE:
        case ZAMMARK_PACK:
            break;
        }
        at += zam_mark_entry_bytes(kind);
    }
    for (uint64_t index = 0; index < thread->heldCount; index++)
        reach(collector, thread->held[index]);
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

// addSpans adds every allocation on a list to the spans.
static void addSpans(ZamVM *vm, const ZamObjectList *list) {
    for (void *object = list->head; object != NULL;
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
}

// collectYoungSpans lays the young generation out by address, so a word can be
// asked which young allocation, if any, it points into.
static void collectYoungSpans(ZamVM *vm) {
    vm->youngSpanCount = 0;
    for (uint64_t list = 0; list < YOUNG_LIST_COUNT; list++)
        addSpans(vm, youngLists(vm, list));
    if (vm->youngSpanCount > 0)
        qsort(vm->youngSpans, vm->youngSpanCount, 2 * sizeof *vm->youngSpans,
              compareSpans);
}

// collectFreshSpans is collectYoungSpans over the fresh lists alone.
static void collectFreshSpans(ZamVM *vm) {
    vm->youngSpanCount = 0;
    for (uint64_t list = 0; list < ZAMYOUNG_COUNT; list++) {
        addSpans(vm, &vm->fresh[list]);
        addSpans(vm, &vm->freshSurvivors[list]);
    }
    if (vm->youngSpanCount > 0)
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
    ZamThread *collector = context;
    ZamVM *vm = collector->vm;
    vm->current.cards += (uint64_t)(end - start) / ZAMHEAP_CARD_BYTES;
    for (const uint8_t *at = start; at < end; at += ZAMWORD_BYTES) {
        ZamWord word = *(const ZamWord *)at;
        if (!zam_heap_contains(&vm->heap, (const void *)(uintptr_t)word))
            continue;
        void *object = youngAllocationOf(vm, word);
        if (object == NULL)
            continue;
        reach(collector, (ZamWord)(uintptr_t)object);

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

// markHeld marks everything a thread or a static holds, without tracing it.
// The thread list is read before anything is marked, a Mark body being able to
// start a thread.
// markFrames marks what the compiled frames thread is running hold.
static void markFrames(ZamThread *collector, const ZamThread *thread) {
    const ZamFrame *frame = atomic_load_explicit(
        (_Atomic(ZamFrame *) *)&thread->frames, memory_order_acquire);
    for (; frame != NULL; frame = frame->caller) {
        const ZamFrameInfo *info = frame->info;
        for (uint64_t index = 0; index < info->rootCount; index++) {
            const ZamFrameRoot *root = &info->roots[index];
            if (root->kind == ZAMFRAME_ROOT_INLINE) {
                // An instance's slot holds nothing until it is first written,
                // and has no header to be marked through until then.
                void *at = (void *)&frame->slots[root->slot];
                if (zam_type_is_class(&collector->vm->image, root->type.shortID) &&
                    zam_object_header_of(at)->shortID == 0)
                    continue;
                markValue(collector, (ZamWord)(uintptr_t)at, root->type);
                continue;
            }
            ZamWord address = frame->slots[root->slot];
            if (address != 0)
                markValue(collector, address,
                          zam_type_id_read(&frame->slots[root->typeSlot]));
        }
    }
}

static void markHeld(ZamThread *collector) {
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
        markFrames(collector, thread);
        for (int sig = 0; sig < ZAM_SIGNAL_MAX; sig++)
            reach(collector, (ZamWord)(uintptr_t)thread->signalHandlers[sig]);

        // A thread still running is alive whatever refers to it.
        if (thread != &vm->main && thread->state != ZAMTHREAD_EXITED)
            reach(collector, (ZamWord)(uintptr_t)thread);
    }
    free(threads);

    for (index = 0; index < vm->image.staticRootCount; index++) {
       ZamTypeID type;
        uint64_t slot = zam_image_static_root(&vm->image, index, &type);
        markValue(collector, (ZamWord)(uintptr_t)zam_vm_static(vm, slot), type);
    }
}

// markRoots marks everything a thread or a static holds, and in a minor
// collection everything a dirty card holds and every old Mark of the
// program's own reaches.
static void markRoots(ZamThread *collector, bool major) {
   ZamVM *vm = collector->vm;
    markHeld(collector);
    if (!major) {
        zam_heap_dirty_cards(&vm->heap, markCards, collector);
        retraceCustom(collector);
    }

    // A minor collection alongside the marker has its helpers paused
    // mid-round, and one of its own is too short to share.
    if (major && !atomic_load(&vm->marking))
        drainParallel(collector, false);
    else
        drain(collector);
}

// finalize runs Delete on every unreachable instance with a Delete of the
// program's own that nothing else unreachable reaches; one with Object's is
// simply freed. What such an instance reaches is kept for one more
// collection rather than finalized alongside it, so that an instance deleting
// what it owns never deletes what the collector has already finalized.
// Whatever it deletes is freed with it.
static void finalizeLists(ZamThread *collector, ZamObjectList *const *lists,
                          size_t listCount) {
   ZamVM *vm = collector->vm;

    uint64_t count = 0;
    uint64_t capacity = 0;
    void **unreachable = NULL;
    for (size_t list = 0; list < listCount; list++)
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

    vm->traceAllocations = false;
    for (uint64_t index = 0; index < count; index++)
        if (zam_mark_word_of(unreachable[index])->state == ZAMMARK_STATE_WHITE) {
            call(collector, (ZamWord)(uintptr_t)unreachable[index],
                 ZAMOBJECT_SLOT_DELETE);
            vm->current.finalized++;
        }
    free(unreachable);
}

static void finalize(ZamThread *collector, bool major) {
   ZamVM *vm = collector->vm;
    ZamObjectList *const lists[] = {
        &vm->young[ZAMYOUNG_DELETES], &vm->survivors[ZAMYOUNG_DELETES],
        major ? &vm->old[ZAMOLD_DELETES] : NULL,
        major ? &vm->old[ZAMOLD_BOTH] : NULL};
    finalizeLists(collector, lists, sizeof lists / sizeof *lists);
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
    // A first survival of a minor collection alongside the marker: onto the
    // fresh survivors, grey, for the remark to mark as it marks anything
    // allocated meanwhile.
    SWEEP_FRESH,
    // A second: promoted as SWEEP_PROMOTE is, and queued for the marker to
    // trace, since what it holds may be reachable from nothing else.
    SWEEP_HAND_OVER,
} SweepFate;

// sweepList frees every allocation on a list left unreachable or deleted, and
// sends every other one where its fate says. An old allocation is left black
// until the next major collection whitens it, which is what keeps a minor
// collection from tracing into it.
//
// A sweep alongside the program frees through the heap's lock and keeps what
// survives without disturbing a delete the program makes meanwhile. Only a
// major collection's sweep runs alongside, so every survivor is kept.
static void sweepList(ZamThread *collector, void *object, bool deletes,
                      SweepFate fate, bool alongside) {
   ZamVM *vm = collector->vm;
    uint64_t freed = 0;
    while (object != NULL) {
       ZamMarkWord *mark = zam_mark_word_of(object);
        void *next = zam_list_next(object);

        ZamMarkState state = zam_mark_state_of(object);
        bool live = state == ZAMMARK_STATE_BLACK ||
                    state == ZAMMARK_STATE_GREY ||
                    (mark->kind == ZAMOBJECT_THREAD &&
                     ((ZamThread *)object)->state != ZAMTHREAD_EXITED);
        if (!live) {
            if (mark->kind == ZAMOBJECT_THREAD)
                release(vm, (ZamThread *)object);
            freed += zam_heap_size(&vm->heap, object);
            if (alongside)
                zam_heap_free(&vm->heap, &collector->heapCache, object);
            else
                zam_heap_free_stopped(&vm->heap, object);
        } else if (alongside) {
            zam_mark_swap_state(object, ZAMMARK_STATE_GREY, ZAMMARK_STATE_BLACK);
            ZamOldList list = (tracesCustom(vm, object) ? ZAMOLD_MARKS : 0) |
                              (deletes ? ZAMOLD_DELETES : 0);
            zam_list_push_shared(&vm->old[list], object);
        } else if (fate == SWEEP_FRESH) {
            mark->state = ZAMMARK_STATE_GREY;
            zam_list_push(&vm->freshSurvivors[deletes ? ZAMYOUNG_DELETES
                                                      : ZAMYOUNG_PLAIN],
                          object);
            vm->current.survived += zam_heap_size(&vm->heap, object);
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
            if (fate != SWEEP_KEEP) {
                uint64_t bytes = zam_heap_size(&vm->heap, object);
                zam_heap_dirty(&vm->heap, object, bytes);
                vm->current.promoted += bytes;
            }
            if (fate == SWEEP_HAND_OVER)
                pushGray(collector, object);
        }
        object = next;
    }
    atomic_fetch_sub(&vm->allocatedByteCount, freed);
}

// detach takes every list a collection is to sweep off the VM, onto the
// unswept lists, leaving the VM's lists empty for what survives.
static void detach(ZamVM *vm, bool major) {
    memcpy(vm->unsweptYoung, vm->young, sizeof vm->young);
    memcpy(vm->unsweptSurvivors, vm->survivors, sizeof vm->survivors);
    memset(vm->young, 0, sizeof vm->young);
    memset(vm->survivors, 0, sizeof vm->survivors);
    memset(vm->unsweptOld, 0, sizeof vm->unsweptOld);
    if (major) {
        memcpy(vm->unsweptOld, vm->old, sizeof vm->old);
        memset(vm->old, 0, sizeof vm->old);
    }
}

// sweepDetached sweeps the unswept lists. A minor collection promotes its
// survivors' survivors and keeps the rest of the young generation as
// survivors. A major collection promotes everything it keeps.
static void sweepDetached(ZamThread *collector, bool major, bool alongside) {
   ZamVM *vm = collector->vm;
    for (int list = 0; list < ZAMOLD_COUNT; list++)
        sweepList(collector, vm->unsweptOld[list].head,
                  (list & ZAMOLD_DELETES) != 0, SWEEP_KEEP, alongside);
    for (int list = 0; list < ZAMYOUNG_COUNT; list++) {
        bool deletes = list == ZAMYOUNG_DELETES;
        sweepList(collector, vm->unsweptSurvivors[list].head, deletes,
                  major ? SWEEP_KEEP : SWEEP_PROMOTE, alongside);
        sweepList(collector, vm->unsweptYoung[list].head, deletes,
                  major ? SWEEP_KEEP : SWEEP_SURVIVE, alongside);
    }
    memset(vm->unsweptOld, 0, sizeof vm->unsweptOld);
    memset(vm->unsweptSurvivors, 0, sizeof vm->unsweptSurvivors);
    memset(vm->unsweptYoung, 0, sizeof vm->unsweptYoung);
}

// sweep frees what the collection left unreached, with every thread
// stopped. A minor collection then dirties again every card that held a young
// address.
static void sweep(ZamThread *collector, bool major) {
   ZamVM *vm = collector->vm;
    detach(vm, major);
    sweepDetached(collector, major, false);
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

// condemn answers every unreached allocation on the first count lists of the
// young generation and then of the old, and leaves all of them unreached for a
// collection to mark afresh.
static void **condemn(ZamVM *vm, uint64_t lists, uint64_t *count) {
    uint64_t capacity = 0;
    void **condemned = NULL;
    *count = 0;
    for (uint64_t list = 0; list < lists; list++) {
        ZamObjectList *at = list < YOUNG_LIST_COUNT
                                ? youngLists(vm, list)
                                : &vm->old[list - YOUNG_LIST_COUNT];
        for (void *object = at->head; object != NULL;
             object = zam_list_next(object)) {
            if (zam_mark_word_of(object)->state != ZAMMARK_STATE_WHITE)
                continue;
            if (*count == capacity) {
                capacity = capacity == 0 ? 64 : capacity * 2;
                condemned = zam_realloc(condemned, capacity * sizeof *condemned);
            }
            condemned[(*count)++] = object;
        }
        whiten(at->head);
    }
    return condemned;
}

// acquit dies if a collection marking in full reached anything condemned.
static void acquit(void **condemned, uint64_t count, const char *by) {
    for (uint64_t index = 0; index < count; index++) {
       ZamMarkWord *mark = zam_mark_word_of(condemned[index]);
        if (mark->state != ZAMMARK_STATE_WHITE)
            zam_die(ZAMEXIT_PANIC,
                    "%s would have freed the reachable allocation at %p, of "
                    "kind %u\n",
                    by, condemned[index], (unsigned)mark->kind);
    }
    free(condemned);
}

// reclaim finalizes and sweeps what marking left unreached, and gives back to
// the system what has stayed free.
static void reclaim(ZamThread *collector, bool major) {
   ZamVM *vm = collector->vm;
    finalize(collector, major);
    sweep(collector, major);
    // A minor collection cleaned the cards it scanned. One a Mark or Delete
    // dirtied since is left for the next to scan, which costs no more than
    // the scan.
    if (major)
        zam_heap_clear_cards(&vm->heap);
    vm->current.returned = zam_heap_scavenge(&vm->heap);
}

bool zam_gc_collect(ZamVM *vm, ZamThread *collector, bool major) {
    vm->traceAllocations = true;
    vm->heldCardCount = 0;
    uint64_t condemnedCount = 0;
    void **condemned = NULL;
    if (vm->verify) {
        collectYoungSpans(vm);
        markRoots(collector, false);
        condemned = condemn(vm, YOUNG_LIST_COUNT, &condemnedCount);
        major = true;
    }

    if (major) {
        for (int list = 0; list < ZAMOLD_COUNT; list++)
            whiten(vm->old[list].head);
    } else
        collectYoungSpans(vm);
    markRoots(collector, major);
    acquit(condemned, condemnedCount, "a minor collection");
    reclaim(collector, major);
    return major;
}

void zam_gc_begin(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    vm->traceAllocations = true;
    for (int list = 0; list < ZAMOLD_COUNT; list++)
        whiten(vm->old[list].head);
    zam_heap_clear_cards(&vm->heap);
    vm->retraceCount = 0;
    markHeld(collector);
    vm->traceAllocations = false;
    shareAll(collector);
}

// precleanCard reaches every allocation a word on a card the marker cleaned
// points into, the words being read as markCards reads them while the program
// may be writing them. A card pointing into an allocation made while the
// marker runs is dirtied again: the remark leaves that allocation unreached,
// and only the card says what holds it.
#if defined(__has_feature)
#if __has_feature(address_sanitizer) || __has_feature(thread_sanitizer)
__attribute__((no_sanitize("address", "thread")))
#endif
#endif
static void precleanCard(void *context, const uint8_t *start,
                         const uint8_t *end) {
   ZamThread *marker = context;
   ZamVM *vm = marker->vm;
    bool holdsNew = false;
    for (const uint8_t *at = start; at < end; at += ZAMWORD_BYTES) {
        void *object = allocationOf(vm, *(const volatile ZamWord *)at);
        if (object == NULL)
            continue;
        if (zam_mark_state_of(object) == ZAMMARK_STATE_GREY)
            holdsNew = true;
        else if (zam_mark_swap_state(object, ZAMMARK_STATE_WHITE,
                                     ZAMMARK_STATE_BLACK))
            pushGray(marker, object);
    }
    if (holdsNew)
        zam_heap_dirty(&vm->heap, start, (uint64_t)(end - start));
    checkpoint(vm);
}

// zam_gc_mark_concurrently also scans, alongside the program, the cards it
// dirtied while the markers traced, so that the remark scans only those
// dirtied since. The marker counts as working from when no minor collection
// is under way until it is done.
void zam_gc_mark_concurrently(ZamThread *marker) {
   ZamVM *vm = marker->vm;
    pthread_mutex_lock(&vm->markLock);
    while (atomic_load(&vm->markerPause))
        pthread_cond_wait(&vm->markChanged, &vm->markLock);
    vm->markersWorking++;
    pthread_mutex_unlock(&vm->markLock);

    drainParallel(marker, true);
    for (uint64_t pass = 0;
         pass < vm->precleanPasses && !atomic_load(&vm->finishRequested);
         pass++) {
        uint64_t cleaned =
            zam_heap_preclean_cards(&vm->heap, precleanCard, marker);
        vm->current.precleaned += cleaned;
        if (cleaned == 0)
            break;
        drainParallel(marker, true);
    }

    pthread_mutex_lock(&vm->markLock);
    vm->markersWorking--;
    pthread_cond_broadcast(&vm->markChanged);
    pthread_mutex_unlock(&vm->markLock);
}

// markEveryCard reaches every allocation a word on a run of dirty cards points
// into, the words being read as markCards reads them.
#if defined(__has_feature)
#if __has_feature(address_sanitizer)
__attribute__((no_sanitize("address")))
#endif
#endif
static void markEveryCard(void *context, const uint8_t *start,
                          const uint8_t *end) {
   ZamThread *collector = context;
    collector->vm->current.cards +=
        (uint64_t)(end - start) / ZAMHEAP_CARD_BYTES;
    for (const uint8_t *at = start; at < end; at += ZAMWORD_BYTES)
        reach(collector, *(const ZamWord *)at);
}

// queueReachedCustom queues the Mark of the program's own of every allocation
// reached so far to run against what memory holds then, along with what the
// markers left untraced. What is reached from here on is queued as it is
// reached, so each Mark runs once.
static void queueReachedCustom(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    pthread_mutex_lock(&vm->markLock);
    takeWork(vm, collector, vm->sharedCount);
    pthread_mutex_unlock(&vm->markLock);
    uint64_t kept = 0;
    for (uint64_t index = 0; index < collector->grayCount; index++)
        if (!tracesCustom(vm, collector->gray[index]))
            collector->gray[kept++] = collector->gray[index];
    collector->grayCount = kept;

    uint64_t count = 0;
    uint64_t capacity = 0;
    void **reached = NULL;
    for (uint64_t list = 0; list < YOUNG_LIST_COUNT + ZAMOLD_COUNT; list++) {
        bool old = list >= YOUNG_LIST_COUNT;
        if (old && ((list - YOUNG_LIST_COUNT) & ZAMOLD_MARKS) == 0)
            continue;
        ZamObjectList *at =
            old ? &vm->old[list - YOUNG_LIST_COUNT] : youngLists(vm, list);
        for (void *object = at->head; object != NULL;
             object = zam_list_next(object)) {
            if (zam_mark_word_of(object)->state != ZAMMARK_STATE_BLACK ||
                (!old && !tracesCustom(vm, object)))
                continue;
            if (count == capacity) {
                capacity = capacity == 0 ? 64 : capacity * 2;
                reached = zam_realloc(reached, capacity * sizeof *reached);
            }
            reached[count++] = object;
        }
    }
    for (uint64_t index = 0; index < count; index++)
        pushGray(collector, reached[index]);
    vm->current.retraced += count;
    free(reached);
}

void zam_gc_remark(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    vm->traceAllocations = true;
    queueReachedCustom(collector);
    for (uint64_t index = 0; index < vm->retraceCount; index++)
        pushGray(collector, vm->retrace[index]);
    vm->retraceCount = 0;

    markHeld(collector);
    zam_heap_dirty_cards(&vm->heap, markEveryCard, collector);
    drainParallel(collector, false);

    if (vm->verify) {
        uint64_t condemnedCount;
        void **condemned =
            condemn(vm, YOUNG_LIST_COUNT + ZAMOLD_COUNT, &condemnedCount);
        markRoots(collector, true);
        acquit(condemned, condemnedCount, "a concurrent collection");
    }
    finalize(collector, true);
    detach(vm, true);
    // Cleaned now rather than after the sweep, the program storing into the
    // heap again from here on and the next minor collection needing to see it.
    zam_heap_clear_cards(&vm->heap);
}

// verifyFresh dies if a collection marking in full would reach a fresh
// allocation the minor collection alongside the marker left unreached. Every
// allocation that is not fresh is given back the state the marker left it in.
static void verifyFresh(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    uint64_t condemnedCount = 0;
    uint64_t capacity = 0;
    void **condemned = NULL;
    for (int list = 0; list < 2 * ZAMYOUNG_COUNT; list++)
        for (void *object = list < ZAMYOUNG_COUNT
                                ? vm->fresh[list].head
                                : vm->freshSurvivors[list - ZAMYOUNG_COUNT].head;
             object != NULL; object = zam_list_next(object)) {
            if (zam_mark_word_of(object)->state != ZAMMARK_STATE_WHITE)
                continue;
            if (condemnedCount == capacity) {
                capacity = capacity == 0 ? 64 : capacity * 2;
                condemned = zam_realloc(condemned, capacity * sizeof *condemned);
            }
            condemned[condemnedCount++] = object;
        }

    uint64_t savedCount = 0;
    uint64_t savedCapacity = 0;
    void **saved = NULL;
    uint8_t *states = NULL;
    for (uint64_t list = 0; list < YOUNG_LIST_COUNT + ZAMOLD_COUNT; list++) {
        ZamObjectList *at = list < YOUNG_LIST_COUNT
                                ? youngLists(vm, list)
                                : &vm->old[list - YOUNG_LIST_COUNT];
        for (void *object = at->head; object != NULL;
             object = zam_list_next(object)) {
            if (savedCount == savedCapacity) {
                savedCapacity = savedCapacity == 0 ? 64 : savedCapacity * 2;
                saved = zam_realloc(saved, savedCapacity * sizeof *saved);
                states = zam_realloc(states, savedCapacity * sizeof *states);
            }
            saved[savedCount] = object;
            states[savedCount++] = (uint8_t)zam_mark_word_of(object)->state;
        }
        whiten(at->head);
    }

    vm->freshOnly = false;
    markRoots(collector, true);
    vm->freshOnly = true;
    acquit(condemned, condemnedCount, "a minor collection alongside the marker");

    for (uint64_t index = 0; index < savedCount; index++)
        zam_mark_word_of(saved[index])->state = states[index];
    free(saved);
    free(states);
}

// What the program holds of a fresh allocation is on its stacks and statics,
// on a card dirtied since marking began, which precleaning leaves dirty, or
// where a Mark of the program's own decodes it, of any allocation that is not
// fresh. The cards are scanned without cleaning them, the remark needing them
// still, and what was promoted is handed to the markers.
void zam_gc_collect_fresh(ZamThread *collector) {
   ZamVM *vm = collector->vm;
    vm->traceAllocations = true;
    vm->heldCardCount = 0;
    for (int list = 0; list < ZAMYOUNG_COUNT; list++) {
        whiten(vm->fresh[list].head);
        whiten(vm->freshSurvivors[list].head);
    }
    collectFreshSpans(vm);
    vm->freshOnly = true;

    markHeld(collector);
    zam_heap_scan_cards(&vm->heap, markCards, collector);
    retraceCustom(collector);
    for (uint64_t list = 0; list < YOUNG_LIST_COUNT; list++)
        for (void *object = youngLists(vm, list)->head; object != NULL;
             object = zam_list_next(object))
            if (zam_mark_word_of(object)->state != ZAMMARK_STATE_DEAD &&
                tracesCustom(vm, object)) {
                trace(collector, object);
                vm->current.retraced++;
            }
    drain(collector);
    if (vm->verify)
        verifyFresh(collector);

    ZamObjectList *const deletes[] = {&vm->fresh[ZAMYOUNG_DELETES],
                                      &vm->freshSurvivors[ZAMYOUNG_DELETES]};
    finalizeLists(collector, deletes, 2);
    ZamObjectList unswept[ZAMYOUNG_COUNT];
    ZamObjectList unsweptSurvivors[ZAMYOUNG_COUNT];
    memcpy(unswept, vm->fresh, sizeof unswept);
    memcpy(unsweptSurvivors, vm->freshSurvivors, sizeof unsweptSurvivors);
    memset(vm->fresh, 0, sizeof vm->fresh);
    memset(vm->freshSurvivors, 0, sizeof vm->freshSurvivors);
    for (int list = 0; list < ZAMYOUNG_COUNT; list++) {
        bool deletes = list == ZAMYOUNG_DELETES;
        sweepList(collector, unsweptSurvivors[list].head, deletes,
                  SWEEP_HAND_OVER, false);
        sweepList(collector, unswept[list].head, deletes, SWEEP_FRESH, false);
    }

    vm->freshOnly = false;
    vm->traceAllocations = false;
    shareAll(collector);
}

void zam_gc_sweep_concurrently(ZamThread *marker) {
   ZamVM *vm = marker->vm;
    sweepDetached(marker, true, true);
    vm->current.returned = zam_heap_scavenge(&vm->heap);
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
    if (object != NULL)
        zam_mark_swap_state(object, ZAMMARK_STATE_WHITE, ZAMMARK_STATE_BLACK);
}
