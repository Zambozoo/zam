#include "vm.h"

// postImageLoad is the half of VM init that is the same whether the image
// came off disk or in off a byte array: the heap counters, the statics, the
// native table and the main thread are all set up from what the image carries,
// so zam_vm_init and zam_vm_init_from_bytes share them.
static void postImageLoad(ZamVM *vm) {
    vm->gcTriggerRatio = vm->image.gcTriggerRatio;
    vm->nextCollectionByteCount =
        (uint64_t)((double)vm->image.initialHeapSize * vm->gcTriggerRatio);

    pthread_mutex_init(&vm->collectionLock, NULL);
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
    for (void *object = vm->allocations; object != NULL;) {
        void *next = (void *)(uintptr_t)zam_mark_word_of(object)->upvalue;
        free(object);
        object = next;
    }
    vm->allocations = NULL;

    free(vm->natives);
    free(vm->statics);
    zam_image_close(&vm->image);
    pthread_mutex_destroy(&vm->collectionLock);
    pthread_mutex_destroy(&vm->threadLock);
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

    // The upvalue holds 56 bits, which is every address these platforms hand
    // out — the top byte of a pointer is not part of the address. One past that
    // would be truncated into a link to somewhere else, and this is the only
    // place the list could be silently corrupted, so it is checked here rather
    // than assumed everywhere it is walked.
    if ((uintptr_t)vm->allocations >> ZAMUPVALUE_BITS != 0)
        zam_die(ZAMEXIT_PANIC,
                "an allocation at %p does not fit the mark word's %d bit link\n",
                vm->allocations, ZAMUPVALUE_BITS);

    // Linked as it is made, so that no allocation is ever live and unfindable.
    // The upvalue is the link itself, which is why a markable allocation needs
    // no side table to be swept.
    //
    // Contained is clear, and this is the only place in the runtime where it is:
    // an allocation is the one markable value that is nothing's interior and
    // whose storage is the collector's to release.
    *zam_mark_word_of(object) = (ZamMarkWord){
        .state = ZAMMARK_STATE_WHITE,
        .kind = kind,
        .contained = 0,
        .upvalue = (uintptr_t)vm->allocations,
    };
    vm->allocations = object;
    if (kind != ZAMOBJECT_STRING)
        *zam_object_header_of(object) = (ZamObjectHeader){
            .shortID = shortID,
            .elementCount = elementCount,
        };

    // The thread's count is what a collection is triggered by and the VM's is
    // what the next threshold is set from, so both move and neither stands for
    // the other.
    thread->allocatedByteCount += objectBytes;
    vm->allocatedByteCount += objectBytes;
    if (vm->allocatedByteCount >= vm->nextCollectionByteCount)
        vm->collectionRequested = true;

    return object;
}

void zam_vm_collect(ZamVM *vm) {
    (void)vm;
    // The mark half walks every thread's mark stack, and every static that
    // holds a reference, and traces what each reaches. The sweep half walks
    // vm->allocations, frees what was not reached, unlinks it as it goes, and
    // resets the threshold from what survived.
    //
    // A block OpDelete left dead is the one the mark half must leave alone: the
    // program has said it is finished with whether or not anything still points
    // at it, so reaching one must not promote it and the sweep must free it
    // alongside the white ones. That is what makes an explicit delete a delete
    // rather than a hint.
    ZAMTODO("garbage collection");
}
