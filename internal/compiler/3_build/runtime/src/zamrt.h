#pragma once

// The runtime as compiled code calls it.
//
// Every compiled function, closure body, initializer and user override has one
// shape, ZamEntry: its arguments laid out as words the way a frame holds them,
// and its result written as words, so that whatever the runtime calls back into
// is called the same way. Each function keeps its locals in a ZamFrame linked
// onto its thread for as long as it runs, which is where the collector finds
// the roots it holds and a trace finds the line it is at.

#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>

#include "definition.h"
#include "heap.h"
#include "instance.h"
#include "posit.h"
#include "thread.h"
#include "vm.h"

// ZamFrameRootKind is how a frame holds a value the collector must visit.
typedef enum ZamFrameRootKind {
    // ZAMFRAME_ROOT_INLINE is a value laid out in the frame's slots, of the
    // root's type.
    ZAMFRAME_ROOT_INLINE,
    // ZAMFRAME_ROOT_INDIRECT is a slot holding the address of a value whose
    // width is only known at run time, of the type laid out in the slots at
    // typeSlot.
    ZAMFRAME_ROOT_INDIRECT,
} ZamFrameRootKind;

// ZamFrameRoot is one value a frame may hold that the collector must visit.
typedef struct ZamFrameRoot {
    uint32_t kind;
    // slot is the word the value, or its address, begins at.
    uint32_t slot;
    // typeSlot is the word an indirect root's type id begins at.
    uint32_t typeSlot;
    // type is an inline root's type.
    ZamTypeID type;
} ZamFrameRoot;

// ZamFrameInfo is what the compiler knows of every frame of one function.
typedef struct ZamFrameInfo {
    // functionID names the function in the compiled function table, from 1.
    uint64_t functionID;
    // slotCount is how many words the frame's slots span.
    uint64_t slotCount;
    uint64_t rootCount;
    const ZamFrameRoot *roots;
} ZamFrameInfo;

// ZamFrame is one running function's locals. A frame's slots are zeroed when
// it is entered, so a root not yet written holds nothing.
typedef struct ZamFrame {
    struct ZamFrame *caller;
    const ZamFrameInfo *info;
    // line is the source line of the statement the function is in.
    uint64_t line;
    ZamWord slots[];
} ZamFrame;

// ZAMHANDLER_JUMP_WORDS is room for any target's jmp_buf.
#define ZAMHANDLER_JUMP_WORDS 64

// ZAMHANDLER_WORDS is how many words compiled code sets aside for a ZamHandler.
#define ZAMHANDLER_WORDS 80

// ZamHandler is the landing pad of a running function that defers: where a
// panic unwinding the thread runs the function's defers. jump is filled by the
// setjmp of the function's entry, and the rest is the thread as it stands in
// the function's own code, which landing puts back.
typedef struct ZamHandler {
    _Alignas(16) ZamWord jump[ZAMHANDLER_JUMP_WORDS];
    struct ZamHandler *outer;
    ZamFrame *frame;
    uint64_t operandCount;
    uint64_t markByteCount;
    uint64_t functionEntry;
    uint64_t visitedCount;
    uint64_t dirtiedCount;
} ZamHandler;

// ZamPanicking is a panic unwinding a thread.
typedef struct ZamPanicking {
    struct ZamPanicking *outer;
    // handler is the landing pad running defers for the panic, NULL until it
    // lands.
    ZamHandler *handler;
    // status is what the program ends with if nothing recovers, and check
    // whether a failed check rather than the program raised it.
    uint8_t status;
    bool check;
    // fault is the signal, or on Windows the exception code, of the invalid
    // access that raised the panic, which the program ends by instead of
    // status; 0 for a panic that is not one.
    uint32_t fault;
    uint64_t length;
    char *message;
    ZamTrace trace;
} ZamPanicking;

// zamrt_frame_enter links frame onto thread as its innermost, with zeroed
// slots.
static inline void zamrt_frame_enter(ZamThread *thread, ZamFrame *frame, const ZamFrameInfo *info) {
    frame->info = info;
    frame->line = 0;
    for (uint64_t slot = 0; slot < info->slotCount; slot++)
        frame->slots[slot] = 0;
    frame->caller = thread->frames;
    atomic_store_explicit((_Atomic(ZamFrame *) *)&thread->frames, frame, memory_order_release);
}

// zamrt_frame_leave unlinks frame, which has to be thread's innermost, and
// the landing pad of its function if it has one.
static inline void zamrt_frame_leave(ZamThread *thread, ZamFrame *frame) {
    ZamHandler *handler = thread->handlers;
    if (handler != NULL && handler->frame == frame)
        thread->handlers = handler->outer;
    atomic_store_explicit((_Atomic(ZamFrame *) *)&thread->frames, frame->caller, memory_order_release);
}

// zamrt_handler_push links handler, of the function running in frame, onto
// thread as its innermost.
void zamrt_handler_push(ZamThread *thread, ZamHandler *handler, ZamFrame *frame);

// zamrt_raise unwinds thread for a panic saying the length bytes of message,
// to the innermost landing pad, or, with none, writes the message and where
// the thread was and ends the program with status. check says the message is
// a failed check's, written as the runtime writes its own errors. skip is how
// many of the innermost frames the trace leaves out.
_Noreturn void zamrt_raise(ZamThread *thread, uint8_t status, bool check, const char *message,
                           uint64_t length, uint64_t skip);

// zamrt_fault raises a panic for the invalid access thread faulted at,
// which ends the program by fault, its signal or exception code, if nothing
// recovers it. bus says the access was to memory that is mapped but not
// backed.
_Noreturn void zamrt_fault(ZamThread *thread, uint32_t fault, uintptr_t address, bool bus);

// zamrt_panicking reports whether thread is still unwinding for the panic that
// landed at handler, none of its function's defers having recovered it.
bool zamrt_panicking(ZamThread *thread, ZamHandler *handler);

// zamrt_unwind leaves the function of handler, the thread's innermost, whose
// defers have all run without recovering the panic, and unwinds to the next.
_Noreturn void zamrt_unwind(ZamThread *thread, ZamHandler *handler);

// zamrt_recover stops the panic the function running in frame is unwinding
// for, and answers it, or NULL when that function is unwinding for none.
ZamPanicking *zamrt_recover(ZamThread *thread, const ZamFrame *frame);

// zamrt_handlers_hide hides thread's landing pads from what the runtime is
// about to call compiled code for, so that a panic it raises ends the program
// rather than unwind through the runtime, and answers them for
// zamrt_handlers_show.
static inline ZamHandler *zamrt_handlers_hide(ZamThread *thread) {
    ZamHandler *handlers = thread->handlers;
    thread->handlers = NULL;
    return handlers;
}

// zamrt_handlers_show puts back the landing pads zamrt_handlers_hide hid.
static inline void zamrt_handlers_show(ZamThread *thread, ZamHandler *handlers) {
    thread->handlers = handlers;
}

// zamrt_frame_push and zamrt_frame_pop are zamrt_frame_enter and
// zamrt_frame_leave for code that cannot inline them.
void zamrt_frame_push(ZamThread *thread, ZamFrame *frame, const ZamFrameInfo *info);
void zamrt_frame_pop(ZamThread *thread, ZamFrame *frame);

// zamrt_poll_slow is zamrt_poll's answer to a collection asking for the thread
// or a signal delivered to it.
void zamrt_poll_slow(ZamThread *thread);

// zamrt_safepoint is zamrt_poll for code that cannot inline it.
void zamrt_safepoint(ZamThread *thread);

// zamrt_poll is a safepoint: the thread stops here for a collection that asks
// it to, and runs the handlers of the signals delivered to it.
static inline void zamrt_poll(ZamThread *thread) {
    ZamVM *vm = thread->vm;
    if (atomic_load_explicit(&vm->stopRequested, memory_order_relaxed) ||
        atomic_load_explicit(&vm->collectionRequested, memory_order_relaxed) ||
        thread->pendingSignalMask != 0)
        zamrt_poll_slow(thread);
}

// zamrt_write_barrier records that byteCount bytes at at were written, which a
// store of anything the collector visits into memory outside a frame owes.
static inline void zamrt_write_barrier(ZamThread *thread, const void *at, uint64_t byteCount) {
    zam_heap_dirty(&thread->vm->heap, at, byteCount);
}

// ZamPanic is a check compiled code found failed.
typedef enum ZamPanic {
    ZAMPANIC_DIVIDE_BY_ZERO,
    ZAMPANIC_NULL_MEMBER,
    ZAMPANIC_NULL_DEREFERENCE,
    ZAMPANIC_NULL_CALL,
    ZAMPANIC_NULL_RECEIVER,
    ZAMPANIC_NULL_THREAD,
    ZAMPANIC_ANY_TYPE,
    ZAMPANIC_ENUM_UNBACKED,
    ZAMPANIC_FREE_INTERIOR,
    ZAMPANIC_NULL_ANY,
    ZAMPANIC_NULL_STRING,
    ZAMPANIC_NEGATIVE_COUNT,
    ZAMPANIC_COUNT_OVERFLOW,
    ZAMPANIC_FREE_UNALLOCATED,
    ZAMPANIC_DOUBLE_FREE,
    ZAMPANIC_COUNT,
} ZamPanic;

// zamrt_panic raises a panic for a failed check.
_Noreturn void zamrt_panic(ZamThread *thread, ZamPanic panic);

// zamrt_unsupported ends the program on reaching code native compilation
// could not compile, saying what it was.
_Noreturn void zamrt_unsupported(ZamThread *thread, const char *what);

// zamrt_exit ends the program with status, kept to its low eight bits.
_Noreturn void zamrt_exit(ZamThread *thread, int64_t status);

// zamrt_start runs a compiled program: it loads the VM from the image of
// imageBytes at image, with classCount compiled classes sorted by short id
// and functionCount compiled functions named for traces by function id,
// calls main over the program's arguments as main takes them, a count and a
// run of strings, and ends the program once main returns. A program compiled
// for coverage passes the counterCount bytes its statements set as they run,
// which are written at exit to the file $ZAM_COVERAGE names, when it names
// one; a program ended by a fault writes none.
_Noreturn void zamrt_start(int argc, const char *argv[], const uint8_t *image, size_t imageBytes,
                           ZamEntry main, const ZamCompiledClass *classes, uint64_t classCount,
                           const ZamCompiledFunction *functions, uint64_t functionCount,
                           const uint8_t *counters, uint64_t counterCount);

// zamrt_new allocates count zeroed, stamped values of element. It runs no
// initializer; the compiled code calls the type's own.
void *zamrt_new(ZamThread *thread, ZamTypeID element, uint64_t count);

// zamrt_new_of is zamrt_new for an element named by its type id's words.
void *zamrt_new_of(ZamThread *thread, const ZamWord *element, uint64_t count);

// zamrt_record_write is zamrt_write_barrier for code that cannot inline it.
void zamrt_record_write(ZamThread *thread, const void *at, uint64_t byteCount);

// zamrt_new_run allocates count zeroed, stamped values of the element whose
// type id's words are element, in memory the program releases with free, or
// answers NULL for none.
void *zamrt_new_run(ZamThread *thread, const ZamWord *element, uint64_t count);

// zamrt_new_tailed is zamrt_new_run for a tailed element whose tail is tail
// long where its type does not say.
void *zamrt_new_tailed(ZamThread *thread, const ZamWord *element, uint64_t count, uint64_t tail);

// zamrt_free releases what pointer, of the pointer type whose id's words are
// type, points at, as free does.
void zamrt_free(ZamThread *thread, const ZamWord *type, uint64_t pointer);

// zamrt_new_raw allocates byteCount bytes the collector does not look inside,
// and zamrt_free_raw gives them back.
void *zamrt_new_raw(ZamThread *thread, uint64_t byteCount);
void zamrt_free_raw(ZamThread *thread, void *block);

// zamrt_new_string allocates a string of length bytes copied from bytes.
ZamString *zamrt_new_string(ZamThread *thread, const char *bytes, uint64_t length);

// zamrt_concat allocates the string left then right.
ZamString *zamrt_concat(ZamThread *thread, const ZamString *left, const ZamString *right);

// zamrt_new_closure allocates a closure over entry, of the function type
// functionType, with bodyBytes of receiver and captures for the caller to
// write.
ZamClosure *zamrt_new_closure(ZamThread *thread, ZamObjectKind kind, ZamTypeShort functionType,
                              ZamEntry entry, uint64_t bodyBytes);

// zamrt_new_function_closure allocates a closure with no receiver over entry,
// of the function type whose id is functionType's words, with room for
// captureCount captures spanning captureWords words, type ids included, which
// the caller writes from zamrt_closure_captures on.
ZamClosure *zamrt_new_function_closure(ZamThread *thread, const ZamWord *functionType, ZamEntry entry,
                                       uint64_t captureCount, uint64_t captureWords);

// zamrt_closure_captures is where closure's captures begin, past its receiver.
ZamWord *zamrt_closure_captures(ZamClosure *closure);

// zamrt_closure_entry is the compiled function closure calls, panicking on a
// null closure.
ZamEntry zamrt_closure_entry(ZamThread *thread, ZamClosure *closure);

// zamrt_eq, zamrt_cmp, zamrt_hash and zamrt_str are equality, order, hashing
// and the string form of values laid out as a frame lays them out, calling a
// type's own Eq, Cmp, Hash or Str where it has one. Each type is its type id's
// ZAMTYPE_ID_WORDS words.
bool zamrt_eq(ZamThread *thread, const ZamWord *aType, const void *a, const ZamWord *bType, const void *b);
int64_t zamrt_cmp(ZamThread *thread, const ZamWord *aType, const void *a, const ZamWord *bType,
                  const void *b);
uint64_t zamrt_hash(ZamThread *thread, const ZamWord *type, const void *value);
ZamString *zamrt_str(ZamThread *thread, const ZamWord *type, const void *value);

// zamrt_spawn starts a thread, of the thread type whose id's words are
// threadType, calling entry through self with args, which info lays out after
// a slot for self. resultType is the id's words of what it answers, or NULL
// for nothing. zamrt_wait joins target and writes what it answered to out.
void *zamrt_spawn(ZamThread *thread, const ZamWord *threadType, ZamEntry entry, ZamClosure *self,
                  const ZamFrameInfo *info, const ZamWord *args, const ZamWord *resultType);
void zamrt_wait(ZamThread *thread, void *target, const ZamWord *resultType, ZamWord *out);

// zamrt_stamp writes the headers a fresh value of the type whose id's words
// are type carries, laid out in a frame at value.
void zamrt_stamp(ZamThread *thread, const ZamWord *type, void *value);

// zamrt_new_bound_closure allocates the function value of method bound to
// receiver, of the pointer type whose id's words are receiverType: calling it
// calls method with receiver ahead of its arguments.
ZamClosure *zamrt_new_bound_closure(ZamThread *thread, const ZamWord *functionType,
                                    const ZamWord *receiverType, uint64_t receiver, ZamEntry method);

// zamrt_mark says the value of the type at value is reachable, while the
// thread collects; zamrt_delete runs the Delete of the instance it is or
// points at, if any.
void zamrt_mark(ZamThread *thread, const ZamWord *type, const void *value);
void zamrt_delete(ZamThread *thread, const ZamWord *type, const void *value);

// zamrt_is_subtype reports whether a value of child may stand where parent is
// asked for.
bool zamrt_is_subtype(const ZamImage *image, ZamTypeID child, ZamTypeID parent);

// zamrt_subtype_of reports whether the type whose id's words are child may
// stand where the one at parent is asked for.
bool zamrt_subtype_of(ZamThread *thread, const ZamWord *child, const ZamWord *parent);

// zamrt_type_of_words writes the type of the instance at instance, read off its
// header, to out as its id's words, and zamrt_any_type_words the type an any
// holds; both panic on null.
void zamrt_type_of_words(ZamThread *thread, const void *instance, ZamWord *out);

// zamrt_downcast is pointer when it is null or points at an instance of a
// subtype of the type whose id's words are target, and null otherwise.
uint64_t zamrt_downcast(ZamThread *thread, uint64_t pointer, const ZamWord *target);
void zamrt_any_type_words(ZamThread *thread, const void *any, ZamWord *out);

// zamrt_type_of is the type of the instance at instance, read off its header,
// panicking on a null instance.
ZamTypeID zamrt_type_of(ZamThread *thread, const void *instance);

// zamrt_size_of is how many bytes a value of type spans.
uint64_t zamrt_size_of(const ZamImage *image, ZamTypeID type);

// zamrt_generic_arg is the index'th type argument of the generic type shortID.
ZamTypeID zamrt_generic_arg(const ZamImage *image, ZamTypeShort shortID, uint64_t index);

// zamrt_virtual_entry is the methodIndex'th method of the class of the
// instance at receiver, panicking on a null receiver.
ZamEntry zamrt_virtual_entry(ZamThread *thread, const void *receiver, uint64_t methodIndex);

// zamrt_interface_entry is the method receiver's class implements for the
// methodIndex'th method of interfaceType, panicking on a null receiver or when
// it implements none.
ZamEntry zamrt_interface_entry(ZamThread *thread, const void *receiver, ZamTypeShort interfaceType,
                               uint32_t methodIndex);

// zamrt_any_box allocates an any holding a copy of the value of type at value,
// zamrt_any_type is the type an any holds, and zamrt_any_unbox copies what an
// any holds to out, panicking unless it is of a subtype of want.
void *zamrt_any_box(ZamThread *thread, const ZamWord *type, const void *value);
ZamTypeID zamrt_any_type(ZamThread *thread, const void *any);
void zamrt_any_unbox(ZamThread *thread, const void *any, const ZamWord *want, void *out);

// zamrt_call_native calls the native at index in the native table over
// argWords words of args, writing resultWords words of what it returns to
// result.
void zamrt_call_native(ZamThread *thread, uint64_t index, ZamWord *args, uint64_t argWords,
                       ZamWord *result, uint64_t resultWords);

// zamrt_static is the address of the static slot slot.
uint8_t *zamrt_static(ZamThread *thread, uint64_t slot);

// The posit arithmetic compiled code calls is posit.h's, unchanged.
