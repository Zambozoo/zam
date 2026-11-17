// Natives that step a concurrent collection from a program, so that what the
// program changes lands exactly between the marker's tracing and its remark.

#include <pthread.h>
#include <stdatomic.h>
#include <stdbool.h>

#include "zam/native.h"
#include "vm.h"

// GC_STEP_TRACE() begins a concurrent collection with its remark held, and
// returns once the marker has traced all it will before the remark.
void GC_STEP_TRACE(ZamThread *thread) {
    ZamVM *vm = thread->vm;
    zam_vm_hold_remark(vm);
    atomic_store(&vm->nextCollectionByteCount, 0);
    zam_vm_collect(vm, thread);
    // As a collection run from a safepoint does, wake the marker to the
    // cycle begun.
    pthread_mutex_lock(&vm->collectionLock);
    pthread_cond_broadcast(&vm->collectionChanged);
    pthread_mutex_unlock(&vm->collectionLock);
    zam_vm_await_traced(vm);
    zam_native_return_void(thread);
}

// GC_STEP_FINISH() lets the remark run and waits for the collection to be
// swept.
void GC_STEP_FINISH(ZamThread *thread) {
    ZamVM *vm = thread->vm;
    zam_vm_release_remark(vm);
    zam_vm_enter_safe(thread);
    pthread_mutex_lock(&vm->collectionLock);
    while (atomic_load(&vm->marking) || atomic_load(&vm->sweeping))
        pthread_cond_wait(&vm->collectionChanged, &vm->collectionLock);
    pthread_mutex_unlock(&vm->collectionLock);
    zam_vm_leave_safe(thread);
    zam_native_return_void(thread);
}

// listedIn reports whether object is on one of count lists.
static bool listedIn(ZamObjectList *lists, int count, const void *object) {
    for (int list = 0; list < count; list++)
        for (void *at = lists[list].head; at != NULL; at = zam_list_next(at))
            if (at == object)
                return true;
    return false;
}

// GC_STEP_LISTED(void* object, bool* listed) reports whether the collector
// still keeps object, on any list of what it has not freed.
void GC_STEP_LISTED(ZamThread *thread) {
    ZamVM *vm = thread->vm;
    const void *object = (const void *)(uintptr_t)zam_native_argument(thread, 0);
    bool listed = listedIn(vm->young, ZAMYOUNG_COUNT, object) ||
                  listedIn(vm->survivors, ZAMYOUNG_COUNT, object) ||
                  listedIn(vm->fresh, ZAMYOUNG_COUNT, object) ||
                  listedIn(vm->freshSurvivors, ZAMYOUNG_COUNT, object) ||
                  listedIn(vm->old, ZAMOLD_COUNT, object);
    *(bool *)zam_native_argument_pointer(thread, 1) = listed;
    zam_native_return_void(thread);
}
