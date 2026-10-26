#include "vm.h"

#include "gc.h"

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
    vm->gcTriggerRatio = vm->image.gcTriggerRatio;
    vm->heapByteCount = vm->image.initialHeapSize;
    resetThreshold(vm);

    pthread_mutex_init(&vm->collectionLock, NULL);
    pthread_cond_init(&vm->collectionChanged, NULL);
    pthread_mutex_init(&vm->threadLock, NULL);

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
    for (uint64_t declID = 0; declID < vm->image.typeDefinitionCount; declID++)
        atomic_init(&vm->markableDecls[declID], 0);

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

void zam_vm_free(ZamVM *vm) {
    // A spawned thread's stacks are its own and are freed here; the thread
    // itself is one of the allocations and is freed by the heap sweep below.
    // Freeing it here as well would double-free, the two walks reaching the
    // same block.
    for (ZamThread *thread = vm->main.next; thread != NULL;
         thread = thread->next)
        zam_thread_free(thread);
    zam_thread_free(&vm->main);

    // The heap goes with the VM. This is the sweep's own walk run once with
    // everything treated as garbage, which is what the end of the program makes
    // it, and the link is read before the block holding it is freed.
    for (void *object = atomic_load(&vm->allocations); object != NULL;) {
        void *next = (void *)(uintptr_t)zam_mark_word_of(object)->upvalue;
        free(object);
        object = next;
    }
    atomic_store(&vm->allocations, NULL);

    free(vm->natives);
    free(vm->statics);
    free(vm->markableDecls);
    free(vm->gray);
    zam_image_close(&vm->image);
    pthread_mutex_destroy(&vm->collectionLock);
    pthread_cond_destroy(&vm->collectionChanged);
    pthread_mutex_destroy(&vm->threadLock);
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

void *zam_vm_alloc(ZamVM *vm, ZamThread *thread, ZamObjectKind kind,
                  ZamTypeShort shortID, uint64_t elementCount,
                   uint64_t objectBytes) {
    uint8_t *block = zam_alloc(objectBytes);
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
    // looked for and must not free, so it starts already reached.
   ZamMarkState color =
        vm->collector != NULL ? ZAMMARK_STATE_BLACK : ZAMMARK_STATE_WHITE;
    void *head = atomic_load(&vm->allocations);
    do {
        if ((uintptr_t)head >> ZAMUPVALUE_BITS != 0)
            zam_die(ZAMEXIT_PANIC,
                    "an allocation at %p does not fit the mark word's %d bit "
                    "link\n",
                    head, ZAMUPVALUE_BITS);
        *zam_mark_word_of(object) = (ZamMarkWord){
            .state = color,
            .kind = kind,
            .contained = 0,
            .upvalue = (uintptr_t)head,
        };
    } while (!atomic_compare_exchange_weak(&vm->allocations, &head, object));
    if (kind != ZAMOBJECT_STRING)
        *zam_object_header_of(object) = (ZamObjectHeader){
            .shortID = shortID,
            .elementCount = elementCount,
        };

    // The thread's count is what a collection is triggered by and the VM's is
    // what the next threshold is set from, so both move and neither stands for
    // the other. Both count what the allocator set aside rather than what was
    // asked for, which is what a sweep can give back.
    uint64_t blockBytes = zam_alloc_size(block);
    thread->allocatedByteCount += blockBytes;
    uint64_t allocated =
        atomic_fetch_add(&vm->allocatedByteCount, blockBytes) + blockBytes;
    if (allocated >= atomic_load(&vm->nextCollectionByteCount))
        atomic_store(&vm->collectionRequested, true);

    return object;
}

void zam_vm_collect(ZamVM *vm, ZamThread *collector) {
    vm->collector = collector;
    zam_gc_collect(vm, collector);
    vm->collector = NULL;
    resetThreshold(vm);
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

void zam_vm_safepoint(ZamVM *vm, ZamThread *thread) {
    if (thread->gcBlockDepth != 0)
        return;
    if (!atomic_load(&vm->stopRequested) &&
        !atomic_load(&vm->collectionRequested))
        return;

    pthread_mutex_lock(&vm->collectionLock);
    if (atomic_load(&vm->stopRequested))
        awaitResume(vm, thread, ZAMTHREAD_PARKED);
    else if (atomic_load(&vm->collectionRequested))
        collect(vm, thread);
    pthread_mutex_unlock(&vm->collectionLock);
}

// The collecting thread is the one every other thread waits on, so a call it
// makes from inside a collection is never a safe region: there is no
// collection for it to let run.
void zam_vm_enter_safe(ZamThread *thread) {
    if (thread->vm->collector == thread)
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
    if (vm->collector == thread)
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
