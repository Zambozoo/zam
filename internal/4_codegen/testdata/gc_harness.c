// A harness that steps the collector through a concurrent cycle by hand.
//
// Run as a program, where a minor collection lands against the marker is up to
// how the threads are scheduled. Here the marker is held paused for as long as
// a case needs, so each collection lands exactly where the case puts it.

#include <inttypes.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <string.h>

#include "definition.h"
#include "gc.h"
#include "image.h"
#include "instance.h"
#include "run.h"
#include "vm.h"
#include "zamrt.h"

// The project's own natives, of which the harness has none.
const ZamNativeEntry zamProjectNativeTable[] = {{NULL, NULL}};
const uint64_t zamProjectNativeTableCount = 0;

static void put(const char *name, uint64_t value) {
    printf("%s=%" PRIu64 "\n", name, value);
}

// rootedSlot answers the static the image roots first, and the type of what
// its pointer points at.
static ZamWord *rootedSlot(ZamVM *vm, ZamTypeID *pointee) {
    ZamTypeID type;
    uint64_t slot = zam_image_static_root(&vm->image, 0, &type);
    *pointee = zam_pointer_pointee(zam_pointer_definition(&vm->image, ZAMTYPE_DECL(type.shortID)));
    return (ZamWord *)(void *)zam_vm_static(vm, slot);
}

// isOld reports whether object is on one of the old lists.
static bool isOld(ZamVM *vm, const void *object) {
    for (int list = 0; list < ZAMOLD_COUNT; list++)
        for (void *at = vm->old[list].head; at != NULL; at = zam_list_next(at))
            if (at == object)
                return true;
    return false;
}

// awaitCycle parks the program until the marker has remarked and swept.
static void awaitCycle(ZamVM *vm) {
    zam_vm_enter_safe(&vm->main);
    pthread_mutex_lock(&vm->collectionLock);
    while (atomic_load(&vm->marking) || atomic_load(&vm->sweeping))
        pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);
    pthread_mutex_unlock(&vm->collectionLock);
    zam_vm_leave_safe(&vm->main);
}

// handOver allocates a box while the marker is held, keeps it through one
// minor collection alongside the marker and promotes it at a second, and lets
// the marker finish. A box nothing holds is allocated beside it.
static void handOver(ZamVM *vm) {
    ZamThread *main = &vm->main;
    ZamTypeID box;
    ZamWord *held = rootedSlot(vm, &box);

    zam_gc_pause_markers(vm);
    atomic_store(&vm->nextCollectionByteCount, 0);
    zam_vm_collect(vm, main);
    put("marking", atomic_load(&vm->marking));

    void *kept = zam_thread_new_instance(main, box);
    *held = (ZamWord)(uintptr_t)kept;
    zam_thread_new_instance(main, box);

    zam_vm_collect(vm, main);
    put("first.fresh", zam_mark_state_of(kept) == ZAMMARK_STATE_GREY);
    put("first.old", isOld(vm, kept));
    zam_vm_collect(vm, main);
    put("second.old", isOld(vm, kept));
    put("second.marking", atomic_load(&vm->marking));

    zam_gc_resume_markers(vm);
    awaitCycle(vm);
    put("finished.old", isOld(vm, kept));
    put("finished.held", *held == (ZamWord)(uintptr_t)kept);
}

// isListed reports whether object is on any list the collector keeps of what
// it has not freed.
static bool isListed(ZamVM *vm, const void *object) {
    ZamObjectList *lists[] = {vm->young, vm->survivors, vm->fresh, vm->freshSurvivors};
    for (size_t at = 0; at < sizeof lists / sizeof lists[0]; at++)
        for (int list = 0; list < ZAMYOUNG_COUNT; list++)
            for (void *it = lists[at][list].head; it != NULL; it = zam_list_next(it))
                if (it == object)
                    return true;
    return isOld(vm, object);
}

// frameRoots holds one box in a frame's slots and another behind a slot that
// points at it, leaves a third held by nothing, and collects; then leaves the
// frame and collects again.
static void frameRoots(ZamVM *vm) {
    ZamThread *main = &vm->main;
    ZamTypeID box;
    rootedSlot(vm, &box);
    ZamTypeID boxes;
    zam_image_static_root(&vm->image, 0, &boxes);

    void *inlined = zam_thread_new_instance(main, box);
    void *indirect = zam_thread_new_instance(main, box);
    void *loose = zam_thread_new_instance(main, box);
    ZamWord held = (ZamWord)(uintptr_t)indirect;

    const ZamFrameRoot roots[] = {
        {.kind = ZAMFRAME_ROOT_INLINE, .slot = 0, .type = boxes},
        {.kind = ZAMFRAME_ROOT_INDIRECT, .slot = 1, .typeSlot = 2},
    };
    const ZamFrameInfo info = {.functionID = 1, .slotCount = 5, .rootCount = 2, .roots = roots};
    _Alignas(ZamFrame) uint8_t bytes[sizeof(ZamFrame) + 5 * sizeof(ZamWord)];
    ZamFrame *frame = (ZamFrame *)(void *)bytes;
    zamrt_frame_enter(main, frame, &info);
    frame->slots[0] = (ZamWord)(uintptr_t)inlined;
    frame->slots[1] = (ZamWord)(uintptr_t)&held;
    zam_type_id_write(&frame->slots[2], boxes);

    atomic_store(&vm->nextCollectionByteCount, UINT64_MAX);
    zam_vm_collect(vm, main);
    put("held.inline", isListed(vm, inlined));
    put("held.indirect", isListed(vm, indirect));
    put("held.loose", isListed(vm, loose));

    zamrt_frame_leave(main, frame);
    zam_vm_collect(vm, main);
    put("left.inline", isListed(vm, inlined));
    put("left.indirect", isListed(vm, indirect));
}

int main(int argc, char **argv) {
    if (argc != 3) {
        fprintf(stderr, "usage: gc_harness <case> <image>\n");
        return 2;
    }

    ZamVM vm;
    zam_vm_init(&vm, argv[2]);
    vm.stats = true;
    if (strcmp(argv[1], "handOver") == 0)
        handOver(&vm);
    else if (strcmp(argv[1], "frameRoots") == 0)
        frameRoots(&vm);
    else {
        fprintf(stderr, "unknown case %s\n", argv[1]);
        return 2;
    }
    fflush(stdout);
    zam_vm_free(&vm);
    return 0;
}
