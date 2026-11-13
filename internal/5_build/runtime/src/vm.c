#include "vm.h"

#include "gc.h"

#include <time.h>
#include <unistd.h>

// resetThreshold grows the heap until what it holds no longer asks for a
// collection, and sets the threshold the next one is asked for at.
static void resetThreshold(ZamVM *vm) {
    uint64_t allocated = atomic_load(&vm->allocatedByteCount);
    if (vm->heapByteCount == 0)
        vm->heapByteCount = 1;
    while (allocated >=
           (uint64_t)((double)vm->heapByteCount * vm->gcTriggerRatio))
        vm->heapByteCount *= 2;
    atomic_store(&vm->nextCollectionByteCount,
                 (uint64_t)((double)vm->heapByteCount * vm->gcTriggerRatio));
}

// postImageLoad is the half of VM init that is the same whether the image
// came off disk or in off a byte array: the heap counters, the statics, the
// native table and the main thread are all set up from what the image carries,
// so zam_vm_init and zam_vm_init_from_bytes share them.
static void postImageLoad(ZamVM *vm) {
    zam_heap_init(&vm->heap, vm->image.maxHeapSize, vm->image.gcReturnAge);
    vm->gcTriggerRatio = vm->image.gcTriggerRatio;
    vm->heapByteCount = vm->image.initialHeapSize;
    vm->nurseryByteCount = vm->image.nurserySize;
    vm->flushByteCount = vm->nurseryByteCount / 16;
    if (vm->flushByteCount < 256)
        vm->flushByteCount = 256;
    if (vm->flushByteCount > (64 << 10))
        vm->flushByteCount = 64 << 10;
    const char *verify = getenv("ZAM_GC_VERIFY");
    vm->verify = verify != NULL && *verify != '\0';
    const char *stats = getenv("ZAM_GC_STATS");
    vm->stats = stats != NULL && *stats != '\0';
    vm->concurrent = vm->image.gcConcurrent;
    vm->remarkGrowth = vm->image.gcRemarkGrowth;
    vm->precleanPasses = vm->image.gcPrecleanPasses;
    long markerCount = (long)vm->image.gcMarkers;
    if (markerCount == 0) {
        markerCount = sysconf(_SC_NPROCESSORS_ONLN) / 2;
        if (markerCount > 8)
            markerCount = 8;
    }
    vm->markerCount = markerCount < 1 ? 1 : (uint32_t)markerCount;
    resetThreshold(vm);

    pthread_mutex_init(&vm->collectionLock, NULL);
    pthread_cond_init(&vm->collectionChanged, NULL);
    pthread_mutex_init(&vm->threadLock, NULL);
    pthread_mutex_init(&vm->markLock, NULL);
    pthread_cond_init(&vm->markChanged, NULL);

    // The static region is allocated at the size the image asked for and zeroed,
    // which is what a static that was never written reads as. What the compiler
    // had to say about the region is then laid over the head of it: the literals,
    // which are the first thing interned and so hold the slots that begin the
    // region, and the header of every static whose value is a markable one.
    //
    // Copied rather than read in place, because a static is written to. And only
    // over the head, because everything the compiler had to say lies at the front
    // and the zeroes behind it are already what the rest of the region means.
    vm->statics = zam_alloc(vm->image.staticRegionByteCount);
    memset(vm->statics, 0, vm->image.staticRegionByteCount);
    if (vm->image.staticImageByteCount > vm->image.staticRegionByteCount)
        zam_die(ZAMEXIT_IMAGE,
                "the image's %zu initial bytes of static region do not fit in "
                "the %llu byte static region it asked for\n",
                vm->image.staticImageByteCount,
                (unsigned long long)vm->image.staticRegionByteCount);
    memcpy(vm->statics, vm->image.staticImage, vm->image.staticImageByteCount);

    vm->markableDecls =
        zam_alloc(vm->image.typeDefinitionCount * sizeof *vm->markableDecls);
    vm->customMarkDecls =
        zam_alloc(vm->image.typeDefinitionCount * sizeof *vm->customMarkDecls);
    vm->customDeleteDecls =
        zam_alloc(vm->image.typeDefinitionCount * sizeof *vm->customDeleteDecls);
    for (uint64_t declID = 0; declID < vm->image.typeDefinitionCount; declID++) {
        atomic_init(&vm->markableDecls[declID], 0);
        atomic_init(&vm->customMarkDecls[declID], 0);
        atomic_init(&vm->customDeleteDecls[declID], 0);
    }

    zam_natives_resolve(vm);
    zam_thread_init(&vm->main, vm, vm->image.initialStackSize);
}

void zam_vm_init(ZamVM *vm, const char *imagePath) {
    *vm = (ZamVM){0};
    zam_image_open(&vm->image, imagePath);
    postImageLoad(vm);
}

void zam_vm_init_from_bytes(ZamVM *vm, const uint8_t *bytes, size_t byteCount) {
    *vm = (ZamVM){0};
    zam_image_load(&vm->image, bytes, byteCount);
    postImageLoad(vm);
}

// stopMarker ends the marker thread, abandoning a collection it has not yet
// stopped the program for: nothing is left to collect for.
static void stopMarker(ZamVM *vm) {
    if (vm->marker == NULL)
        return;
    pthread_mutex_lock(&vm->collectionLock);
    vm->markerQuit = true;
    pthread_cond_broadcast(&vm->collectionChanged);
    pthread_mutex_unlock(&vm->collectionLock);
    zam_gc_request_finish(vm);
    pthread_join(vm->markerThread, NULL);
    zam_thread_free(vm->marker);
    free(vm->marker);
    vm->marker = NULL;
}

void zam_vm_free(ZamVM *vm) {
    stopMarker(vm);
    zam_gc_stop_helpers(vm);

    // A spawned thread's stacks are its own and are freed here; the thread
    // itself is one of the allocations and is freed by the heap sweep below.
    // Freeing it here as well would double-free, the two walks reaching the
    // same block.
    for (ZamThread *thread = vm->main.next; thread != NULL;
         thread = thread->next)
        zam_thread_free(thread);
    zam_thread_free(&vm->main);

    // The heap goes with the VM, every block in it at once.
    zam_heap_destroy(&vm->heap);
    memset(vm->young, 0, sizeof vm->young);
    memset(vm->survivors, 0, sizeof vm->survivors);
    memset(vm->old, 0, sizeof vm->old);

    free(vm->natives);
    free(vm->statics);
    free(vm->markableDecls);
    free(vm->customMarkDecls);
    free(vm->customDeleteDecls);
    free(vm->shared);
    free(vm->youngSpans);
    free(vm->heldCards);
    free(vm->retrace);
    zam_image_close(&vm->image);
    pthread_mutex_destroy(&vm->collectionLock);
    pthread_cond_destroy(&vm->collectionChanged);
    pthread_mutex_destroy(&vm->threadLock);
    pthread_mutex_destroy(&vm->markLock);
    pthread_cond_destroy(&vm->markChanged);
}

// threadsRunning reports whether a thread other than main has yet to finish.
static bool threadsRunning(ZamVM *vm) {
    bool running = false;
    pthread_mutex_lock(&vm->collectionLock);
    pthread_mutex_lock(&vm->threadLock);
    for (ZamThread *thread = vm->main.next; thread != NULL && !running;
         thread = thread->next)
        running = thread->state != ZAMTHREAD_EXITED;
    pthread_mutex_unlock(&vm->threadLock);
    pthread_mutex_unlock(&vm->collectionLock);
    return running;
}

_Noreturn void zam_vm_exit(ZamVM *vm, ZamResult result) {
    uint8_t status = vm->main.exitStatus;
    if (!threadsRunning(vm))
        zam_vm_free(vm);

    // Only a program that exited says what to end with. Anything else stopped
    // in a way the program did not choose, and reporting its status as the
    // program's would be claiming it ran.
    if (result != ZAMRESULT_EXIT)
        zam_die(ZAMEXIT_PANIC, "the program stopped: %s\n",
                zam_result_name(result));
    exit(status);
}

uint8_t *zam_vm_static(ZamVM *vm, uint64_t slot) {
    return vm->statics + zam_image_static_offset(&vm->image, slot);
}

// flushPending adds what a thread has allocated to the VM's counts, and asks
// for a collection if they now call for one.
// While the marker runs, the remark is asked for once the heap has grown by its
// own size since marking began, and a minor collection otherwise.
static void flushPending(ZamVM *vm, ZamThread *thread) {
    uint64_t bytes = thread->pendingByteCount;
    thread->pendingByteCount = 0;
    uint64_t allocated = atomic_fetch_add(&vm->allocatedByteCount, bytes) + bytes;
    uint64_t young = atomic_fetch_add(&vm->youngByteCount, bytes) + bytes;
    if (atomic_load(&vm->marking)) {
        if (allocated >= vm->cycleBefore + (uint64_t)((double)vm->heapByteCount *
                                                     vm->remarkGrowth))
            zam_gc_request_finish(vm);
        else if (young >= vm->nurseryByteCount)
            atomic_store(&vm->collectionRequested, true);
        return;
    }
    if (allocated >= atomic_load(&vm->nextCollectionByteCount) ||
        young >= vm->nurseryByteCount)
        atomic_store(&vm->collectionRequested, true);
}

// gatherThread flushes a thread's counts and splices its young lists onto the
// VM's. What was allocated while the marker ran is grey, and is left
// unreached first, for the remark to mark as it marks anything else.
static void gatherThread(ZamVM *vm, ZamThread *thread, bool whiten) {
    flushPending(vm, thread);
    for (int list = 0; list < ZAMYOUNG_COUNT; list++) {
        if (whiten)
            for (void *object = thread->young[list].head; object != NULL;
                 object = zam_list_next(object))
                zam_mark_swap_state(object, ZAMMARK_STATE_GREY,
                                    ZAMMARK_STATE_WHITE);
        zam_list_splice(&vm->young[list], &thread->young[list]);
    }
}

// gatherYoung gathers every thread's young lists, the markers' among them,
// onto the VM's, which is what a collection works from, along with what a
// minor collection left fresh. Every other thread is stopped.
static void gatherYoung(ZamVM *vm, bool whiten) {
    pthread_mutex_lock(&vm->threadLock);
    for (ZamThread *thread = &vm->main; thread != NULL; thread = thread->next)
        gatherThread(vm, thread, whiten);
    pthread_mutex_unlock(&vm->threadLock);
    if (vm->marker != NULL)
        gatherThread(vm, vm->marker, whiten);
    for (uint32_t index = 0; index < vm->helperCount; index++)
        gatherThread(vm, vm->helpers[index], whiten);
    for (int list = 0; list < ZAMYOUNG_COUNT; list++) {
        zam_list_splice(&vm->fresh[list], &vm->freshSurvivors[list]);
        if (whiten)
            for (void *object = vm->fresh[list].head; object != NULL;
                 object = zam_list_next(object))
                zam_mark_swap_state(object, ZAMMARK_STATE_GREY,
                                    ZAMMARK_STATE_WHITE);
        zam_list_splice(&vm->young[list], &vm->fresh[list]);
    }
}

// gatherFresh gathers every thread's young lists onto the fresh ones, grey as
// they are, for a minor collection alongside the marker. Every other thread is
// stopped.
static void gatherFresh(ZamVM *vm) {
    pthread_mutex_lock(&vm->threadLock);
    for (ZamThread *thread = &vm->main; thread != NULL; thread = thread->next) {
        flushPending(vm, thread);
        for (int list = 0; list < ZAMYOUNG_COUNT; list++)
            zam_list_splice(&vm->fresh[list], &thread->young[list]);
    }
    pthread_mutex_unlock(&vm->threadLock);
}


void *zam_vm_alloc(ZamVM *vm, ZamThread *thread, ZamObjectKind kind,
                  ZamTypeShort shortID, uint64_t elementCount,
                   uint64_t objectBytes) {
    uint8_t *block = zam_heap_alloc(&vm->heap, &thread->heapCache, objectBytes);
    memset(block, 0, objectBytes);

    // The pointer is the allocation itself, the mark word being the word it
    // begins with however long the header turned out to be. Every accessor in
    // instance.h steps forward from here.
    void *object = block;

    // Linked as it is made, so that no allocation is ever live and unfindable.
    // The upvalue is the link itself, which is why a markable allocation needs
    // no side table to be swept.
    //
    // Contained is clear, and this is the only place in the runtime where it is:
    // an allocation is the one markable value that is nothing's interior and
    // whose storage is the collector's to release.
    //
    // The upvalue holds 56 bits, which is every address these platforms hand
    // out — the top byte of a pointer is not part of the address. One past that
    // would be truncated into a link to somewhere else, and this is the only
    // place the list could be silently corrupted, so it is checked here rather
    // than assumed everywhere it is walked.
    //
    // An allocation made while collecting is one the collection has not
    // looked for and must not free, so it starts already reached, on the list
    // the collection is about to sweep, or for a helper on its own until the
    // round of marking it was made in is over. Any other goes on the thread's
    // own.
    // One made while the marker runs starts grey, which the marker neither
    // traces, the allocation perhaps still being filled in, nor takes for
    // reached; the remark leaves it unreached before marking what the program
    // holds. That includes one a minor collection alongside the marker makes,
    // which goes on the thread's own list, outside what it sweeps.
    bool collecting = vm->collector != NULL;
    bool marking = atomic_load_explicit(&vm->marking, memory_order_relaxed);
    ZamMarkState state = marking      ? ZAMMARK_STATE_GREY
                         : collecting ? ZAMMARK_STATE_BLACK
                                      : ZAMMARK_STATE_WHITE;
    ZamYoungList which = zam_gc_deletes_custom(vm, kind, shortID)
                             ? ZAMYOUNG_DELETES
                             : ZAMYOUNG_PLAIN;
    ZamObjectList *list = thread == vm->collector && !marking
                              ? &vm->young[which]
                              : &thread->young[which];
    void *head = list->head;
    if ((uintptr_t)head >> ZAMUPVALUE_BITS != 0)
        zam_die(ZAMEXIT_PANIC,
                "an allocation at %p does not fit the mark word's %d bit "
                "link\n",
                head, ZAMUPVALUE_BITS);
    *zam_mark_word_of(object) = (ZamMarkWord){
        .state = state,
        .kind = kind,
        .contained = 0,
        .upvalue = (uintptr_t)head,
    };
    zam_list_push(list, object);
    if (kind != ZAMOBJECT_STRING)
        *zam_object_header_of(object) = (ZamObjectHeader){
            .shortID = shortID,
            .elementCount = elementCount,
        };
    zam_heap_set_object(&vm->heap, block);
    if (collecting)
        zam_gc_allocated(thread, object);

    // The counts are of what the allocator set aside rather than what was
    // asked for, which is what a sweep can give back.
    thread->pendingByteCount += zam_heap_size(&vm->heap, block);
    if (collecting || thread->pendingByteCount >= vm->flushByteCount)
        flushPending(vm, thread);
    return object;
}

void *zam_vm_alloc_raw(ZamThread *thread, uint64_t byteCount) {
    return zam_heap_alloc(&thread->vm->heap, &thread->heapCache, byteCount);
}

void zam_vm_free_raw(ZamThread *thread, void *block) {
    if (zam_heap_contains(&thread->vm->heap, block))
        zam_heap_free(&thread->vm->heap, &thread->heapCache, block);
    else
        free(block);
}

static double millisecondsSince(const struct timespec *start) {
    struct timespec end;
    clock_gettime(CLOCK_MONOTONIC, &end);
    return (double)(end.tv_sec - start->tv_sec) * 1e3 +
           (double)(end.tv_nsec - start->tv_nsec) / 1e6;
}

// report writes the line ZAM_GC_STATS asks for, of the collection just done.
static void report(ZamVM *vm, const char *kind, double pause, uint64_t before,
                   uint64_t young, const char *more) {
    fprintf(stderr,
            "gc %llu %s pause=%.3fms heap=%llu->%llu young=%llu "
            "survived=%llu promoted=%llu cards=%llu retraced=%llu "
            "helped=%llu finalized=%llu returned=%llu%s\n",
            (unsigned long long)vm->collections, kind, pause,
            (unsigned long long)before,
            (unsigned long long)atomic_load(&vm->allocatedByteCount),
            (unsigned long long)young,
            (unsigned long long)vm->current.survived,
            (unsigned long long)vm->current.promoted,
            (unsigned long long)vm->current.cards,
            (unsigned long long)vm->current.retraced,
            (unsigned long long)vm->current.helped,
            (unsigned long long)vm->current.finalized,
            (unsigned long long)vm->current.returned, more);
}

// everyOtherThreadStopped reports whether no thread but self is still running
// instructions. The caller holds collectionLock.
static bool everyOtherThreadStopped(ZamVM *vm, const ZamThread *self) {
    pthread_mutex_lock(&vm->threadLock);
    bool stopped = true;
    for (ZamThread *thread = &vm->main; thread != NULL; thread = thread->next)
        if (thread != self && thread->state == ZAMTHREAD_RUNNING) {
            stopped = false;
            break;
        }
    pthread_mutex_unlock(&vm->threadLock);
    return stopped;
}

// finishCycle is the remark of the concurrent collection under way, run by
// the marker with every other thread stopped. What it sets aside is swept
// once the program runs again.
static void finishCycle(ZamVM *vm) {
    struct timespec start;
    clock_gettime(CLOCK_MONOTONIC, &start);
    vm->markTime = millisecondsSince(&vm->cycleStart) - vm->initialPause;

    atomic_store(&vm->marking, false);
    gatherYoung(vm, true);
    vm->collector = vm->marker;
    zam_gc_remark(vm->marker);
    vm->collector = NULL;
    atomic_store(&vm->youngByteCount, 0);
    atomic_store(&vm->sweeping, true);
    vm->remarkPause = millisecondsSince(&start);
}

// sweepCycle sweeps what the remark set aside, alongside the program, and
// sets the threshold by what is left.
static void sweepCycle(ZamVM *vm) {
    struct timespec start;
    clock_gettime(CLOCK_MONOTONIC, &start);
    zam_gc_sweep_concurrently(vm->marker);
    resetThreshold(vm);

    vm->collections++;
    if (vm->stats) {
        char more[160];
        snprintf(more, sizeof more,
                 " initial=%.3fms mark=%.3fms remark=%.3fms sweep=%.3fms "
                 "precleaned=%llu",
                 vm->initialPause, vm->markTime, vm->remarkPause,
                 millisecondsSince(&start),
                 (unsigned long long)vm->current.precleaned);
        report(vm, "concurrent", vm->initialPause + vm->remarkPause,
               vm->cycleBefore, vm->cycleYoung, more);
    }
}

// markerMain is the marker's thread: it waits for a concurrent collection to
// begin, traces alongside the program, stops the program for the remark, and
// sweeps once the program runs again. A stop already under way is waited out
// first, being a collection the marker has no part in.
static void *markerMain(void *context) {
    ZamVM *vm = context;
    pthread_mutex_lock(&vm->collectionLock);
    for (;;) {
        while (!vm->markerQuit && !atomic_load(&vm->marking))
            pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);
        if (vm->markerQuit)
            break;

        pthread_mutex_unlock(&vm->collectionLock);
        zam_gc_mark_concurrently(vm->marker);
        pthread_mutex_lock(&vm->collectionLock);
        if (vm->markerQuit)
            break;

        while (atomic_load(&vm->stopRequested))
            pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);
        atomic_store(&vm->stopRequested, true);
        while (!everyOtherThreadStopped(vm, NULL))
            pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);

        pthread_mutex_unlock(&vm->collectionLock);
        finishCycle(vm);
        pthread_mutex_lock(&vm->collectionLock);

        // The remark was a major collection, which whatever asked for a
        // collection meanwhile is answered by, unless what asked was GC.Major,
        // which is owed one begun after it asked.
        atomic_store(&vm->collectionRequested, atomic_load(&vm->majorRequested));
        atomic_store(&vm->finishRequested, false);
        atomic_store(&vm->stopRequested, false);
        pthread_cond_broadcast(&vm->collectionChanged);

        pthread_mutex_unlock(&vm->collectionLock);
        sweepCycle(vm);
        pthread_mutex_lock(&vm->collectionLock);
        atomic_store(&vm->sweeping, false);
        pthread_cond_broadcast(&vm->collectionChanged);
    }
    pthread_mutex_unlock(&vm->collectionLock);
    return NULL;
}

// startCycle begins a concurrent major collection, with every other thread
// stopped: it marks what the program holds, and sets the marker going on the
// rest. The marker thread is made the first time one is needed.
static void startCycle(ZamVM *vm, ZamThread *collector) {
    clock_gettime(CLOCK_MONOTONIC, &vm->cycleStart);
    vm->cycleBefore = atomic_load(&vm->allocatedByteCount);
    vm->cycleYoung = atomic_load(&vm->youngByteCount);
    vm->current = (ZamCollectionStats){0};

    vm->collector = collector;
    zam_gc_begin(collector);
    vm->collector = NULL;
    atomic_store(&vm->youngByteCount, 0);

    if (vm->marker == NULL) {
        vm->marker = zam_alloc(sizeof *vm->marker);
        memset(vm->marker, 0, sizeof *vm->marker);
        zam_thread_init(vm->marker, vm, vm->image.initialStackSize);
        vm->marker->collects = true;
        if (pthread_create(&vm->markerThread, NULL, markerMain, vm) != 0)
            zam_die(ZAMEXIT_PANIC, "could not start the collector's marker\n");
    }
    vm->initialPause = millisecondsSince(&vm->cycleStart);
    // Last, so that the marker takes up the work only once it is all laid out.
    atomic_store(&vm->marking, true);
}

// collectFresh is a minor collection while the marker runs, of what was
// allocated since it began. Every marker is paused at a checkpoint, waiting
// for work, or between its tracing and its remark, for its length.
static void collectFresh(ZamVM *vm, ZamThread *collector) {
    zam_gc_pause_markers(vm);

    gatherFresh(vm);
    uint64_t before = atomic_load(&vm->allocatedByteCount);
    uint64_t young = atomic_load(&vm->youngByteCount);
    struct timespec start;
    clock_gettime(CLOCK_MONOTONIC, &start);
    ZamCollectionStats cycle = vm->current;
    vm->current = (ZamCollectionStats){0};

    vm->collector = collector;
    zam_gc_collect_fresh(collector);
    vm->collector = NULL;
    atomic_store(&vm->youngByteCount, 0);

    vm->collections++;
    if (vm->stats)
        report(vm, "minor", millisecondsSince(&start), before, young,
               " alongside=1");
    vm->current = cycle;

    zam_gc_resume_markers(vm);
}

// A collection is major once the heap has grown to its threshold, and minor
// otherwise: the young generation filling is what asked for it.
// One the heap's growth asked for is begun concurrently where that is on; one
// GC.Major asked for is finished before the program goes on.
void zam_vm_collect(ZamVM *vm, ZamThread *collector) {
    if (atomic_load(&vm->marking)) {
        collectFresh(vm, collector);
        return;
    }
    gatherYoung(vm, false);
    bool requested = atomic_exchange(&vm->majorRequested, false);
    bool major = requested || atomic_load(&vm->allocatedByteCount) >=
                                  atomic_load(&vm->nextCollectionByteCount);
    if (major && !requested && vm->concurrent) {
        startCycle(vm, collector);
        return;
    }
    uint64_t before = atomic_load(&vm->allocatedByteCount);
    uint64_t young = atomic_load(&vm->youngByteCount);
    struct timespec start;
    clock_gettime(CLOCK_MONOTONIC, &start);
    vm->current = (ZamCollectionStats){0};

    vm->collector = collector;
    major = zam_gc_collect(vm, collector, major);
    vm->collector = NULL;
    atomic_store(&vm->youngByteCount, 0);
    if (major)
        resetThreshold(vm);

    vm->collections++;
    if (vm->stats)
        report(vm, major ? "major" : "minor", millisecondsSince(&start), before,
               young, "");
}

// awaitResume holds a thread in state until no collection is stopping the
// world, and leaves it running. The caller holds collectionLock.
static void awaitResume(ZamVM *vm, ZamThread *thread, ZamThreadState state) {
    thread->state = state;
    pthread_cond_broadcast(&vm->collectionChanged);
    while (atomic_load(&vm->stopRequested))
        pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);
    thread->state = ZAMTHREAD_RUNNING;
}

// collect stops every other thread, collects, and resumes them. The caller
// holds collectionLock, which is let go of while collecting: everything that
// takes it waits on stopRequested in any case, and the collection may run a
// body that blocks.
static void collect(ZamVM *vm, ZamThread *self) {
    atomic_store(&vm->stopRequested, true);
    while (!everyOtherThreadStopped(vm, self))
        pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);

    pthread_mutex_unlock(&vm->collectionLock);
    zam_vm_collect(vm, self);
    pthread_mutex_lock(&vm->collectionLock);

    atomic_store(&vm->collectionRequested, false);
    atomic_store(&vm->stopRequested, false);
    pthread_cond_broadcast(&vm->collectionChanged);
}

// awaitCycle holds a thread parked until the concurrent collection under way
// has marked and swept. The caller holds collectionLock.
static void awaitCycle(ZamVM *vm, ZamThread *thread) {
    thread->state = ZAMTHREAD_PARKED;
    pthread_cond_broadcast(&vm->collectionChanged);
    while (atomic_load(&vm->marking) || atomic_load(&vm->sweeping) ||
           atomic_load(&vm->stopRequested))
        pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);
    thread->state = ZAMTHREAD_RUNNING;
}

// A minor collection asked for while the marker runs collects alongside it. A
// major one hurries it to its remark and waits for that and the sweep after,
// which answer it, as does any asked for while the marker sweeps.
void zam_vm_safepoint(ZamVM *vm, ZamThread *thread) {
    if (thread->gcBlockDepth != 0)
        return;
    if (!atomic_load(&vm->stopRequested) &&
        !atomic_load(&vm->collectionRequested))
        return;

    pthread_mutex_lock(&vm->collectionLock);
    for (;;) {
        if (atomic_load(&vm->stopRequested))
            awaitResume(vm, thread, ZAMTHREAD_PARKED);
        else if (!atomic_load(&vm->collectionRequested))
            break;
        else if (atomic_load(&vm->marking) &&
                 !atomic_load(&vm->majorRequested) &&
                 !atomic_load(&vm->finishRequested)) {
            collect(vm, thread);
            break;
        } else if (atomic_load(&vm->marking) || atomic_load(&vm->sweeping)) {
            if (atomic_load(&vm->marking))
                zam_gc_request_finish(vm);
            awaitCycle(vm, thread);
        } else {
            collect(vm, thread);
            break;
        }
    }
    pthread_mutex_unlock(&vm->collectionLock);
}

// The collecting thread is the one every other thread waits on, so a call it
// makes from inside a collection is never a safe region: there is no
// collection for it to let run.
void zam_vm_enter_safe(ZamThread *thread) {
    if (zam_gc_collecting(thread))
        return;
    if (thread->gcBlockDepth != 0)
        zam_die(ZAMEXIT_PANIC,
                "a blocking call was made while GC.Block is in effect\n");

    ZamVM *vm = thread->vm;
    pthread_mutex_lock(&vm->collectionLock);
    thread->state = ZAMTHREAD_SAFE;
    pthread_cond_broadcast(&vm->collectionChanged);
    pthread_mutex_unlock(&vm->collectionLock);
}

void zam_vm_leave_safe(ZamThread *thread) {
    ZamVM *vm = thread->vm;
    if (zam_gc_collecting(thread))
        return;
    pthread_mutex_lock(&vm->collectionLock);
    awaitResume(vm, thread, ZAMTHREAD_SAFE);
    pthread_mutex_unlock(&vm->collectionLock);
}

void zam_vm_thread_exited(ZamThread *thread) {
    ZamVM *vm = thread->vm;
    pthread_mutex_lock(&vm->collectionLock);
    thread->state = ZAMTHREAD_EXITED;
    pthread_cond_broadcast(&vm->collectionChanged);
    pthread_mutex_unlock(&vm->collectionLock);
}
