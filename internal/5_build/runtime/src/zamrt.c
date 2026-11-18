#include "zamrt.h"

#include <setjmp.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#include <windows.h>
#endif

#include <zam/native.h>

#include "common.h"
#include "gc.h"
#include "run.h"

// panicMessages is what each failed check reports.
static const char *const panicMessages[ZAMPANIC_COUNT] = {
    [ZAMPANIC_DIVIDE_BY_ZERO] = "divide by zero",
    [ZAMPANIC_NULL_MEMBER] = "selected a member of a null pointer",
    [ZAMPANIC_NULL_DEREFERENCE] = "dereferenced a null pointer",
    [ZAMPANIC_NULL_CALL] = "called a null function",
    [ZAMPANIC_NULL_RECEIVER] = "called a method of a null receiver",
    [ZAMPANIC_NULL_THREAD] = "waited on a null thread",
    [ZAMPANIC_ANY_TYPE] = "any holds a value of a type the unbox did not ask for",
    [ZAMPANIC_ENUM_UNBACKED] = "enum value has no backing value",
    [ZAMPANIC_FREE_INTERIOR] = "freed a pointer into the middle of what new allocated",
    [ZAMPANIC_NULL_ANY] = "used a null any",
    [ZAMPANIC_NULL_STRING] = "joined a null string",
    [ZAMPANIC_NEGATIVE_COUNT] = "allocated a negative number of elements",
    [ZAMPANIC_COUNT_OVERFLOW] = "allocated more elements than memory can hold",
    [ZAMPANIC_FREE_UNALLOCATED] = "freed memory new did not allocate",
    [ZAMPANIC_DOUBLE_FREE] = "freed memory that was already freed",
};

_Static_assert(sizeof(jmp_buf) <= sizeof(((ZamHandler *)0)->jump), "a jmp_buf outgrows a landing pad");
_Static_assert(sizeof(ZamHandler) <= ZAMHANDLER_WORDS * sizeof(ZamWord), "a landing pad outgrows its words");

// longJump returns to the setjmp compiled code filled jump with, without
// unwinding anything on the way: on Windows x86-64 that setjmp is _setjmp
// given no frame, and on Windows on Arm it is mingw's own.
static _Noreturn void longJump(ZamWord *jump) {
#if defined(_WIN32) && defined(__aarch64__)
    __mingw_longjmp((void *)jump, 1);
#elif defined(_WIN32)
    longjmp((void *)jump, 1);
#else
    _longjmp((void *)jump, 1);
#endif
}

void zamrt_handler_push(ZamThread *thread, ZamHandler *handler, ZamFrame *frame) {
    handler->outer = thread->handlers;
    handler->frame = frame;
    handler->operandCount = thread->operandCount;
    handler->markByteCount = thread->markByteCount;
    handler->functionEntry = thread->functionEntry;
    handler->scopeEntry = thread->scopeEntry;
    handler->packEntry = thread->packEntry;
    handler->heldCount = thread->heldCount;
    handler->visitedCount = thread->visitedCount;
    handler->dirtiedCount = thread->dirtiedCount;
    handler->instructionOffset = thread->instructionOffset;
    thread->handlers = handler;
}

// endPanic writes panic and where it was raised, and ends the program.
static _Noreturn void endPanic(ZamPanicking *panic) {
    if (panic->check)
        fputs("\033[31m", stderr);
    fwrite(panic->message, 1, panic->length, stderr);
    if (panic->check)
        fputs("\n\033[0m", stderr);
    fflush(stderr);
    zam_trace_write(&panic->trace, fileno(stderr));
    if (panic->fault != 0) {
#if defined(_WIN32)
        TerminateProcess(GetCurrentProcess(), panic->fault);
#else
        signal((int)panic->fault, SIG_DFL);
        raise((int)panic->fault);
#endif
    }
    exit(panic->status);
}

// land puts thread back as it stood in the function of handler and runs that
// function's defers for the newest panic, which any older one it was running
// them for gives way to.
static _Noreturn void land(ZamThread *thread, ZamHandler *handler) {
    ZamPanicking *panic = thread->panics;
    for (ZamPanicking **older = &panic->outer; *older != NULL;) {
        ZamPanicking *p = *older;
        if (p->handler != handler) {
            older = &p->outer;
            continue;
        }
        *older = p->outer;
        free(p->message);
        free(p);
    }
    panic->handler = handler;

    // The cards a native running here was handed are dirtied, as its return
    // would have dirtied them.
    for (uint64_t at = handler->dirtiedCount; at < thread->dirtiedCount; at++)
        zam_heap_dirty(&thread->vm->heap, thread->dirtied[at], ZAMHEAP_CARD_BYTES);
    thread->dirtiedCount = handler->dirtiedCount;
    thread->operandCount = handler->operandCount;
    thread->markByteCount = handler->markByteCount;
    thread->functionEntry = handler->functionEntry;
    thread->scopeEntry = handler->scopeEntry;
    thread->packEntry = handler->packEntry;
    thread->heldCount = handler->heldCount;
    thread->visitedCount = handler->visitedCount;
    thread->instructionOffset = handler->instructionOffset;
    thread->handlers = handler;
    atomic_store_explicit((_Atomic(ZamFrame *) *)&thread->frames, handler->frame, memory_order_release);
    longJump(handler->jump);
}

// raisePanic is zamrt_raise for a panic that ends the program by fault, when
// that is not 0.
static _Noreturn void raisePanic(ZamThread *thread, uint8_t status, bool check, uint32_t fault,
                                 const char *message, uint64_t length, uint64_t skip) {
    ZamPanicking *panic = malloc(sizeof(ZamPanicking));
    char *copy = malloc(length + 1);
    if (panic == NULL || copy == NULL)
        zam_die(ZAMEXIT_OOM, "out of memory raising a panic\n");
    memcpy(copy, message, length);
    copy[length] = '\0';
    *panic = (ZamPanicking){.outer = thread->panics,
                            .status = status,
                            .check = check,
                            .fault = fault,
                            .length = length,
                            .message = copy};
    zam_thread_capture(thread, skip, &panic->trace);
    if (thread->handlers == NULL)
        endPanic(panic);
    thread->panics = panic;
    land(thread, thread->handlers);
}

_Noreturn void zamrt_raise(ZamThread *thread, uint8_t status, bool check, const char *message,
                           uint64_t length, uint64_t skip) {
    raisePanic(thread, status, check, 0, message, length, skip);
}

_Noreturn void zamrt_fault(ZamThread *thread, uint32_t fault, uintptr_t address, bool bus) {
    char message[64];
    int length = snprintf(message, sizeof message, "%s at 0x%llx", bus ? "bus error" : "invalid memory access",
                          (unsigned long long)address);
    raisePanic(thread, ZAMEXIT_PANIC, true, fault, message, (uint64_t)length, 0);
}

bool zamrt_panicking(ZamThread *thread, ZamHandler *handler) {
    return thread->panics != NULL && thread->panics->handler == handler;
}

_Noreturn void zamrt_unwind(ZamThread *thread, ZamHandler *handler) {
    ZamHandler *outer = handler->outer;
    if (outer == NULL)
        endPanic(thread->panics);
    land(thread, outer);
}

ZamPanicking *zamrt_recover(ZamThread *thread, const ZamFrame *frame) {
    ZamPanicking *panic = thread->panics;
    if (panic == NULL || panic->handler == NULL || panic->handler->frame != frame)
        return NULL;
    thread->panics = panic->outer;
    return panic;
}

void zamrt_frame_push(ZamThread *thread, ZamFrame *frame, const ZamFrameInfo *info) {
    zamrt_frame_enter(thread, frame, info);
}

void zamrt_frame_pop(ZamThread *thread, ZamFrame *frame) {
    zamrt_frame_leave(thread, frame);
}

void zamrt_safepoint(ZamThread *thread) {
    zamrt_poll(thread);
}

void zamrt_poll_slow(ZamThread *thread) {
    // A Mark or Delete the collector runs is the collection, and does not
    // wait on it.
    if (zam_gc_collecting(thread))
        return;
    zam_vm_safepoint(thread->vm, thread);

    // A signal the C handler flagged runs its handler here, between
    // statements, as the interpreter runs it between instructions.
    for (int sig; (sig = zam_thread_take_pending_signal(thread)) >= 0;) {
        ZamClosure *handler = thread->signalHandlers[sig];
        if (handler != NULL) {
            ZamHandler *handlers = zamrt_handlers_hide(thread);
            ((ZamEntry)(uintptr_t)handler->instruction)(thread, handler, NULL, NULL);
            zamrt_handlers_show(thread, handlers);
        }
    }
}

_Noreturn void zamrt_panic(ZamThread *thread, ZamPanic panic) {
    const char *message = panic < ZAMPANIC_COUNT ? panicMessages[panic] : NULL;
    if (message == NULL)
        zam_die(ZAMEXIT_PANIC, "unknown panic %u\n", (unsigned)panic);
    zamrt_raise(thread, ZAMEXIT_PANIC, true, message, strlen(message), 0);
}

_Noreturn void zamrt_unsupported(ZamThread *thread, const char *what) {
    (void)thread;
    zam_die(ZAMEXIT_UNIMPLEMENTED, "reached code native compilation does not support yet: %s\n", what);
}

_Noreturn void zamrt_exit(ZamThread *thread, int64_t status) {
    thread->exitStatus = (uint8_t)status;
    // The thread runs nothing more, so a remark under way need not wait for
    // it to stop; the teardown below waits for the remark.
    zam_vm_thread_exited(thread);
    zam_vm_exit(thread->vm, ZAMRESULT_EXIT);
}

// startVM is the VM a compiled program runs on.
static ZamVM startVM;

_Noreturn void zamrt_start(int argc, const char *argv[], const uint8_t *image, size_t imageBytes,
                           ZamEntry main, const ZamCompiledClass *classes, uint64_t classCount,
                           const ZamCompiledFunction *functions, uint64_t functionCount) {
    ZamVM *vm = &startVM;
    zam_vm_init_from_bytes(vm, image, imageBytes);
    vm->compiledClasses = classes;
    vm->compiledClassCount = classCount;
    vm->compiledFunctions = functions;
    vm->compiledFunctionCount = functionCount;
    ZamThread *thread = &vm->main;
    zam_thread_bind_current(thread);

    // main's arguments are the program's own, after its name: a count and a
    // run of strings, which the frame here keeps rooted while main runs.
    uint64_t count = argc > 1 ? (uint64_t)(argc - 1) : 0;
    ZamWord *strings = NULL;
    if (count > 0) {
        strings = zam_vm_alloc_raw(thread, count * ZAMWORD_BYTES);
        for (uint64_t i = 0; i < count; i++)
            strings[i] = zam_native_new_string(thread, argv[i + 1], strlen(argv[i + 1]));
        zam_heap_dirty(&vm->heap, strings, count * ZAMWORD_BYTES);
    }

    const ZamFrameRoot roots[] = {{.kind = ZAMFRAME_ROOT_INDIRECT, .slot = 0, .typeSlot = 1}};
    const ZamFrameInfo info = {.slotCount = 1 + ZAMTYPE_ID_WORDS, .rootCount = 1, .roots = roots};
    _Alignas(ZamFrame) uint8_t frameBytes[sizeof(ZamFrame) + (1 + ZAMTYPE_ID_WORDS) * sizeof(ZamWord)];
    ZamFrame *frame = (ZamFrame *)(void *)frameBytes;
    zamrt_frame_enter(thread, frame, &info);
    frame->slots[0] = (ZamWord)(uintptr_t)strings;
    zam_type_id_write(&frame->slots[1], (ZamTypeID){
                                            .shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_STRING, 0),
                                            .arrayLength = count,
                                        });

    ZamWord args[2] = {count, (ZamWord)(uintptr_t)strings};
    ZamWord result[1] = {0};
    main(thread, NULL, args, result);
    zamrt_frame_leave(thread, frame);
    zamrt_exit(thread, 0);
}

// Spawn is what a spawned thread runs: the call, and the type of what it
// answers.
typedef struct Spawn {
    ZamThread *thread;
    ZamEntry entry;
    ZamFrame *frame;
    bool returns;
    ZamTypeID resultType;
} Spawn;

// spawnMain runs a spawned call to its end. Its result is left on the thread's
// operand stack, rooted there until the thread is waited on.
static void *spawnMain(void *arg) {
    Spawn spawn = *(Spawn *)arg;
    free(arg);
    ZamThread *thread = spawn.thread;
    zam_thread_bind_current(thread);
    ZamWord *result = NULL;
    if (spawn.returns) {
        result = calloc(zam_value_words(&thread->vm->image, spawn.resultType) + 1, sizeof(ZamWord));
        if (result == NULL)
            zam_die(ZAMEXIT_PANIC, "out of memory spawning a thread\n");
    }
    ZamClosure *self = (ZamClosure *)(uintptr_t)spawn.frame->slots[0];
    spawn.entry(thread, self, spawn.frame->slots + 1, result);
    if (spawn.returns) {
        zam_thread_push_value(thread, spawn.resultType, result);
        free(result);
    }
    zamrt_frame_leave(thread, spawn.frame);
    free(spawn.frame);
    zam_vm_thread_exited(thread);
    return NULL;
}

void *zamrt_spawn(ZamThread *thread, const ZamWord *threadType, ZamEntry entry, ZamClosure *self,
                  const ZamFrameInfo *info, const ZamWord *args, const ZamWord *resultType) {
    ZamVM *vm = thread->vm;
    ZamThread *spawned = zam_vm_alloc(vm, thread, ZAMOBJECT_THREAD, zam_type_id_read(threadType).shortID,
                                      1, sizeof(ZamThread));
    zam_thread_init(spawned, vm, vm->image.initialStackSize);

    // The call's frame is the new thread's before the collector can see the
    // thread, so nothing it holds goes unrooted in between.
    ZamFrame *frame = malloc(sizeof(ZamFrame) + info->slotCount * sizeof(ZamWord));
    Spawn *spawn = malloc(sizeof(Spawn));
    if (frame == NULL || spawn == NULL)
        zam_die(ZAMEXIT_PANIC, "out of memory spawning a thread\n");
    zamrt_frame_enter(spawned, frame, info);
    frame->slots[0] = (ZamWord)(uintptr_t)self;
    if (info->slotCount > 1)
        memcpy(frame->slots + 1, args, (info->slotCount - 1) * sizeof(ZamWord));
    *spawn = (Spawn){.thread = spawned, .entry = entry, .frame = frame, .returns = resultType != NULL};
    if (resultType != NULL)
        spawn->resultType = zam_type_id_read(resultType);

    pthread_mutex_lock(&vm->threadLock);
    spawned->next = vm->main.next;
    vm->main.next = spawned;
    pthread_mutex_unlock(&vm->threadLock);

    if (pthread_create(&spawned->systemThread, NULL, spawnMain, spawn) != 0)
        zam_die(ZAMEXIT_PANIC, "failed to spawn a system thread\n");
    return spawned;
}

void zamrt_wait(ZamThread *thread, void *target, const ZamWord *resultType, ZamWord *out) {
    if (target == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_THREAD);
    ZamThread *joined = target;
    zam_thread_join(thread, joined);
    if (resultType == NULL)
        return;
    uint64_t words = zam_value_words(&thread->vm->image, zam_type_id_read(resultType));
    if (words > joined->operandCount)
        zam_die(ZAMEXIT_PANIC, "joined thread returned %llu words, expected %llu\n",
                (unsigned long long)joined->operandCount, (unsigned long long)words);
    memcpy(out, joined->operands + joined->operandCount - words, words * sizeof(ZamWord));
}

void *zamrt_new(ZamThread *thread, ZamTypeID element, uint64_t count) {
    if (count == 0)
        return NULL;
    return zam_thread_allocate(thread, element, count, false);
}

void *zamrt_new_of(ZamThread *thread, const ZamWord *element, uint64_t count) {
    return zamrt_new(thread, zam_type_id_read(element), count);
}

void *zamrt_new_tailed(ZamThread *thread, const ZamWord *element, uint64_t count, uint64_t tail) {
    if (count == 0)
        return NULL;
    ZamTypeID type = zam_type_id_read(element);
    if (type.tailLength == 0)
        type.tailLength = tail;
    return zam_thread_allocate(thread, type, count, true);
}

void *zamrt_new_run(ZamThread *thread, const ZamWord *element, uint64_t count) {
    if (count == 0)
        return NULL;
    return zam_thread_allocate(thread, zam_type_id_read(element), count, true);
}

void zamrt_free(ZamThread *thread, const ZamWord *type, uint64_t pointer) {
    zam_value_free(thread, zam_type_id_read(type), pointer);
}

void zamrt_record_write(ZamThread *thread, const void *at, uint64_t byteCount) {
    zamrt_write_barrier(thread, at, byteCount);
}

void *zamrt_new_raw(ZamThread *thread, uint64_t byteCount) {
    return zam_vm_alloc_raw(thread, byteCount);
}

void zamrt_free_raw(ZamThread *thread, void *block) {
    zam_vm_free_raw(thread, block);
}

ZamString *zamrt_new_string(ZamThread *thread, const char *bytes, uint64_t length) {
    return (ZamString *)(uintptr_t)zam_native_new_string(thread, bytes, length);
}

bool zamrt_is_subtype(const ZamImage *image, ZamTypeID child, ZamTypeID parent) {
    return zam_type_is_subtype(image, child, parent);
}

uint64_t zamrt_size_of(const ZamImage *image, ZamTypeID type) {
    return zam_type_bytes(image, type);
}

ZamTypeID zamrt_generic_arg(const ZamImage *image, ZamTypeShort shortID, uint64_t index) {
    return zam_type_generic_arg(image, shortID, index);
}

// ran ends the program when an override it called ended it, and answers
// otherwise.
static void ran(ZamThread *thread, ZamResult result) {
    if (result == ZAMRESULT_CONTINUE)
        return;
    if (result == ZAMRESULT_EXIT)
        exit(thread->exitStatus);
    zam_die(ZAMEXIT_PANIC, "an override did not return: %s\n", zam_result_name(result));
}

bool zamrt_eq(ZamThread *thread, const ZamWord *aType, const void *a, const ZamWord *bType, const void *b) {
    bool equal = false;
    ran(thread, zam_value_eq(thread, zam_type_id_read(aType), a, zam_type_id_read(bType), b, &equal));
    return equal;
}

int64_t zamrt_cmp(ZamThread *thread, const ZamWord *aType, const void *a, const ZamWord *bType,
                  const void *b) {
    ZamInt order = 0;
    ran(thread, zam_value_cmp(thread, zam_type_id_read(aType), a, zam_type_id_read(bType), b, &order));
    return order;
}

uint64_t zamrt_hash(ZamThread *thread, const ZamWord *type, const void *value) {
    ZamWord hash = 0;
    ran(thread, zam_value_hash(thread, zam_type_id_read(type), value, &hash));
    return hash;
}

void zamrt_stamp(ZamThread *thread, const ZamWord *type, void *value) {
    zam_value_stamp(thread, zam_type_id_read(type), value);
}

void zamrt_mark(ZamThread *thread, const ZamWord *type, const void *value) {
    zam_value_mark(thread, zam_type_id_read(type), value);
}

void zamrt_delete(ZamThread *thread, const ZamWord *type, const void *value) {
    ran(thread, zam_value_delete(thread, zam_type_id_read(type), value));
}

ZamString *zamrt_str(ZamThread *thread, const ZamWord *type, const void *value) {
    ZamString *string = NULL;
    ran(thread, zam_value_str(thread, zam_type_id_read(type), value, &string));
    return string;
}

ZamString *zamrt_concat(ZamThread *thread, const ZamString *left, const ZamString *right) {
    if (left == NULL || right == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_STRING);
    return zam_string_concat(thread, left, right);
}

bool zamrt_subtype_of(ZamThread *thread, const ZamWord *child, const ZamWord *parent) {
    return zam_type_is_subtype(&thread->vm->image, zam_type_id_read(child), zam_type_id_read(parent));
}

void zamrt_type_of_words(ZamThread *thread, const void *instance, ZamWord *out) {
    zam_type_id_write(out, zamrt_type_of(thread, instance));
}

uint64_t zamrt_downcast(ZamThread *thread, uint64_t pointer, const ZamWord *target) {
    if (pointer == 0)
        return 0;
    ZamTypeID actual = zam_instance_type((const void *)(uintptr_t)pointer);
    return zam_type_is_subtype(&thread->vm->image, actual, zam_type_id_read(target)) ? pointer : 0;
}

void zamrt_any_type_words(ZamThread *thread, const void *any, ZamWord *out) {
    zam_type_id_write(out, zamrt_any_type(thread, any));
}

ZamTypeID zamrt_type_of(ZamThread *thread, const void *instance) {
    if (instance == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_RECEIVER);
    return zam_instance_type(instance);
}

ZamClosure *zamrt_method(ZamThread *thread, ZamTypeID receiverType, const void *receiver,
                         uint32_t methodIndex) {
    ZamMethodDefinition method = zam_virtual_method(thread, receiverType, receiver, methodIndex);
    return zam_closure_of(zam_method_closure(thread, receiverType, receiver, method));
}

ZamClosure *zamrt_interface_method(ZamThread *thread, ZamTypeID receiverType, const void *receiver,
                                   uint32_t methodIndex) {
    ZamMethodDefinition method = zam_interface_method(thread, receiverType, receiver, methodIndex);
    return zam_closure_of(zam_method_closure(thread, receiverType, receiver, method));
}

ZamClosure *zamrt_generic_static(ZamThread *thread, uint64_t registryIndex, const ZamWord *args) {
    const ZamWord *fnType;
    uint8_t *slot = zam_generic_static_slot(thread->vm, registryIndex, args, &fnType);
    return zam_closure_of((void *)(uintptr_t) * (const ZamWord *)(const void *)slot);
}

void *zamrt_any_box(ZamThread *thread, const ZamWord *type, const void *value) {
    return zam_any_box(thread, zam_type_id_read(type), value);
}

ZamTypeID zamrt_any_type(ZamThread *thread, const void *any) {
    if (any == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_ANY);
    return zam_any_type(any);
}

void zamrt_any_unbox(ZamThread *thread, const void *any, const ZamWord *want, void *out) {
    zam_any_unbox(thread, any, zam_type_id_read(want), out);
}

void zamrt_call_native(ZamThread *thread, uint64_t index, ZamWord *args, uint64_t argWords,
                       ZamWord *result, uint64_t resultWords) {
    ZamVM *vm = thread->vm;
    if (index >= vm->nativeCount)
        zam_die(ZAMEXIT_IMAGE, "native %llu is past the %llu-entry native table\n",
                (unsigned long long)index, (unsigned long long)vm->nativeCount);
    ZamNativeFn fn = vm->natives[index];
    if (fn == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_CALL);

    // A native reads its arguments off a frame on the operand stack, the
    // address it was called through first, and leaves what it returns there.
    uint64_t base = thread->operandCount;
    zam_thread_push(thread, (ZamWord)(uintptr_t)fn);
    for (uint64_t word = 0; word < argWords; word++)
        zam_thread_push(thread, args[word]);

    uint64_t enclosingEntry = thread->functionEntry;
    uint64_t dirtied = thread->dirtiedCount;
    zam_thread_push_function(thread, thread->instructionOffset, base);
    fn(thread);
    for (uint64_t at = dirtied; at < thread->dirtiedCount; at++)
        zam_heap_dirty(&vm->heap, thread->dirtied[at], ZAMHEAP_CARD_BYTES);
    thread->dirtiedCount = dirtied;
    if (thread->functionEntry != enclosingEntry)
        zam_die(ZAMEXIT_PANIC, "native returned without calling zam_native_return\n");

    for (uint64_t word = resultWords; word > 0; word--) {
        result[word - 1] = zam_thread_pop(thread);
        zam_thread_pop_mark(thread, thread->operandCount);
    }
    if (thread->operandCount != base)
        zam_die(ZAMEXIT_PANIC, "native returned %llu words where %llu were asked for\n",
                (unsigned long long)(thread->operandCount - base + resultWords),
                (unsigned long long)resultWords);

    // A native may ask for a collection before the caller's next instruction,
    // as the interpreter's step after it would run one.
    zamrt_poll(thread);
}

ZamClosure *zamrt_new_closure(ZamThread *thread, ZamObjectKind kind, ZamTypeShort functionType,
                              ZamEntry entry, uint64_t bodyBytes) {
    void *object = zam_vm_alloc(thread->vm, thread, kind, functionType, 1, sizeof(ZamClosure) + bodyBytes);
    ZamClosure *closure = zam_closure_of(object);
    closure->instruction = (uint64_t)(uintptr_t)entry;
    closure->captureCount = 0;
    return closure;
}

ZamClosure *zamrt_new_function_closure(ZamThread *thread, const ZamWord *functionType, ZamEntry entry,
                                       uint64_t captureCount, uint64_t captureWords) {
    ZamClosure *closure =
        zamrt_new_closure(thread, ZAMOBJECT_FUNCTION_CLOSURE, zam_type_id_read(functionType).shortID, entry,
                          (ZAMTYPE_ID_WORDS + captureWords) * ZAMWORD_BYTES);
    closure->captureCount = captureCount;
    zam_type_id_write(closure->body, (ZamTypeID){.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_VOID, 0)});
    return closure;
}

// boundEntry is the entry of a closure binding a method to its receiver: it
// calls the method with the receiver ahead of the arguments the closure was
// called with.
static void boundEntry(ZamThread *thread, ZamClosure *self, ZamWord *args, ZamWord *result) {
    const ZamImage *image = &thread->vm->image;
    const ZamWord *captures = zamrt_closure_captures(self);
    ZamWord receiver = captures[ZAMTYPE_ID_WORDS];
    ZamEntry method = (ZamEntry)(uintptr_t)captures[2 * ZAMTYPE_ID_WORDS + 1];
    const ZamTypeList *signature =
        zam_function_type_signature(image, ZAMTYPE_DECL(self->header.type.shortID));
    uint64_t words = 0;
    for (uint64_t i = 1; i < signature->count; i++)
        words += zam_value_words(image, zam_type_list_at(signature, i));
    ZamWord *bound = zam_alloc((words + 1) * sizeof(ZamWord));
    bound[0] = receiver;
    memcpy(bound + 1, args, words * sizeof(ZamWord));
    method(thread, NULL, bound, result);
    free(bound);
}

ZamClosure *zamrt_new_bound_closure(ZamThread *thread, const ZamWord *functionType,
                                    const ZamWord *receiverType, uint64_t receiver, ZamEntry method) {
    ZamClosure *closure = zamrt_new_function_closure(thread, functionType, boundEntry, 2,
                                                     2 * (ZAMTYPE_ID_WORDS + 1));
    ZamWord *captures = zamrt_closure_captures(closure);
    memcpy(captures, receiverType, ZAMTYPE_ID_WORDS * sizeof(ZamWord));
    captures[ZAMTYPE_ID_WORDS] = receiver;
    zam_type_id_write(captures + ZAMTYPE_ID_WORDS + 1,
                      (ZamTypeID){.shortID = ZAMTYPE_SHORT(ZAMPRIMITIVE_INT, 0)});
    captures[2 * ZAMTYPE_ID_WORDS + 1] = (ZamWord)(uintptr_t)method;
    zamrt_record_write(thread, captures, 2 * (ZAMTYPE_ID_WORDS + 1) * sizeof(ZamWord));
    return closure;
}

ZamWord *zamrt_closure_captures(ZamClosure *closure) {
    return closure->body + ZAMTYPE_ID_WORDS;
}

ZamEntry zamrt_closure_entry(ZamThread *thread, ZamClosure *closure) {
    if (closure == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_CALL);
    return (ZamEntry)(uintptr_t)closure->instruction;
}

// compiledMethod is the methodIndex'th compiled method of the class shortID.
static ZamEntry compiledMethod(ZamThread *thread, ZamTypeShort shortID, uint64_t methodIndex) {
    ZamEntry entry = zam_vm_compiled_method(thread->vm, shortID, methodIndex);
    if (entry == NULL)
        zam_die(ZAMEXIT_PANIC, "method %llu of type %u has no body\n", (unsigned long long)methodIndex,
                shortID);
    return entry;
}

ZamEntry zamrt_virtual_entry(ZamThread *thread, const void *receiver, uint64_t methodIndex) {
    if (receiver == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_RECEIVER);
    return compiledMethod(thread, zam_instance_type(receiver).shortID, methodIndex);
}

ZamEntry zamrt_interface_entry(ZamThread *thread, const void *receiver, ZamTypeShort interfaceType,
                               uint32_t methodIndex) {
    if (receiver == NULL)
        zamrt_panic(thread, ZAMPANIC_NULL_RECEIVER);
    ZamTypeShort concrete = zam_instance_type(receiver).shortID;
    uint32_t index = zam_interface_method_index(&thread->vm->image, concrete, interfaceType, methodIndex);
    return compiledMethod(thread, concrete, index);
}

uint8_t *zamrt_static(ZamThread *thread, uint64_t slot) {
    return zam_vm_static(thread->vm, slot);
}
