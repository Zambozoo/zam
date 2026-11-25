// The register names a signal handler reads its context by are GNU's.
#if defined(__linux__) && !defined(_GNU_SOURCE)
#define _GNU_SOURCE
#endif

#include "thread.h"

#include <errno.h>
#include <signal.h>
#include <unistd.h>

#if defined(_WIN32)
#include <windows.h>
#endif

#include "gc.h"
#include "vm.h"
#include "zamrt.h"


// currentZamThread records which ZamThread the OS thread is driving. The C
// signal handler reads this to find the per-thread pending mask it should
// OR into; the mask lives on the ZamThread so each line of execution
// dispatches only the handlers it registered.
//
// Set at the top of a thread's run via zam_thread_bind_current. A thread
// that has not yet bound sees a NULL here, in which case the signal is
// dropped — nothing has claimed it, and reaching through NULL would be a
// crash before any program observed anything.
static __thread ZamThread *currentZamThread;

static void zamSignalHandler(int sig) {
#if defined(_WIN32)
    // Windows resets a handler to the default once it has run.
    (void)signal(sig, zamSignalHandler);
#endif
   ZamThread *thread = currentZamThread;
    if (thread == NULL)
        return;
    if (sig < 0 || sig >= ZAM_SIGNAL_MAX)
        return;
    thread->pendingSignalMask |= (sig_atomic_t)1u << sig;
}

// stackReserve is how much of a thread's stack is kept back below where a
// stack overflow is raised, for the panic and what it runs to unwind, a
// quarter of the stack at most.
static const uintptr_t stackReserve = 128 * 1024;

// stackLimit answers the lowest a compiled frame may stand on the calling
// thread's stack, or zero where its bounds are unknown. A stack grows down on
// every target zam compiles for.
static uintptr_t stackLimit(void) {
    uintptr_t low = 0;
    uintptr_t size = 0;
#if defined(_WIN32)
    ULONG_PTR lowest = 0, highest = 0;
    GetCurrentThreadStackLimits(&lowest, &highest);
    low = (uintptr_t)lowest;
    size = (uintptr_t)(highest - lowest);
#elif defined(__APPLE__)
    pthread_t self = pthread_self();
    size = (uintptr_t)pthread_get_stacksize_np(self);
    low = (uintptr_t)pthread_get_stackaddr_np(self) - size;
#else
    pthread_attr_t attr;
    if (pthread_getattr_np(pthread_self(), &attr) != 0)
        return 0;
    void *address = NULL;
    size_t bytes = 0;
    int err = pthread_attr_getstack(&attr, &address, &bytes);
    pthread_attr_destroy(&attr);
    if (err != 0)
        return 0;
    low = (uintptr_t)address;
    size = (uintptr_t)bytes;
#endif
    if (low == 0 || size == 0)
        return 0;
    uintptr_t reserve = stackReserve < size / 4 ? stackReserve : size / 4;
    return low + reserve;
}

void zam_thread_bind_current(ZamThread *thread) {
    currentZamThread = thread;
    thread->stackLimit = stackLimit();
    thread->stackOverflowing = false;
}

ZamThread *zam_thread_current(void) { return currentZamThread; }

// TraceWriter gathers a trace a line at a time and writes it to fd, using
// nothing a signal handler may not.
typedef struct TraceWriter {
    int fd;
    size_t length;
    char data[256];
} TraceWriter;

static void traceFlush(TraceWriter *w) {
    size_t written = 0;
    while (written < w->length) {
        ssize_t n = write(w->fd, w->data + written, w->length - written);
        if (n < 0 && errno == EINTR)
            continue;
        if (n <= 0)
            break;
        written += (size_t)n;
    }
    w->length = 0;
}

static void traceText(TraceWriter *w, const char *text) {
    for (; *text != '\0'; text++) {
        if (w->length == sizeof w->data)
            traceFlush(w);
        w->data[w->length++] = *text;
    }
}

static void traceNumber(TraceWriter *w, uint64_t value, unsigned base) {
    char digits[24];
    size_t i = sizeof digits;
    digits[--i] = '\0';
    do {
        digits[--i] = "0123456789abcdef"[value % base];
        value /= base;
    } while (value > 0);
    traceText(w, digits + i);
}

// compiledFunction is what names the compiled function frame is running, or
// NULL for a frame that is no function of the program's: the one a thread
// starts with.
static const ZamCompiledFunction *compiledFunction(const ZamVM *vm, const ZamFrame *frame) {
    uint64_t id = frame->info->functionID;
    if (id == 0 || id > vm->compiledFunctionCount)
        return NULL;
    return &vm->compiledFunctions[id - 1];
}

bool zam_thread_compiled_caller(const ZamThread *thread, uint64_t skip, const char **file,
                                uint32_t *line) {
    const ZamFrame *frame = atomic_load_explicit((_Atomic(ZamFrame *) *)&thread->frames,
                                                 memory_order_acquire);
    for (; frame != NULL; frame = frame->caller) {
        const ZamCompiledFunction *function = compiledFunction(thread->vm, frame);
        if (function == NULL)
            continue;
        if (skip-- == 0) {
            *file = function->file;
            *line = (uint32_t)frame->line;
            return true;
        }
    }
    return false;
}

void zam_thread_capture(const ZamThread *thread, uint64_t skip, ZamTrace *trace) {
    trace->count = 0;
    trace->elided = 0;
    const ZamFrame *frame = atomic_load_explicit((_Atomic(ZamFrame *) *)&thread->frames,
                                                 memory_order_acquire);
    for (; frame != NULL; frame = frame->caller) {
        const ZamCompiledFunction *function = compiledFunction(thread->vm, frame);
        if (function == NULL)
            continue;
        if (skip > 0) {
            skip--;
            continue;
        }
        if (trace->count == ZAMTRACE_FRAMES) {
            trace->elided++;
            continue;
        }
        trace->frames[trace->count].function = function;
        trace->frames[trace->count].line = frame->line;
        trace->count++;
    }
}

void zam_trace_write(const ZamTrace *trace, int fd) {
    TraceWriter w = {.fd = fd};
    for (uint64_t i = 0; i < trace->count; i++) {
        traceText(&w, "\tat ");
        traceText(&w, trace->frames[i].function->name);
        traceText(&w, " (");
        traceText(&w, trace->frames[i].function->file);
        traceText(&w, ":");
        traceNumber(&w, trace->frames[i].line, 10);
        traceText(&w, ")\n");
        traceFlush(&w);
    }
    if (trace->elided > 0) {
        traceText(&w, "\t... ");
        traceNumber(&w, trace->elided, 10);
        traceText(&w, " more frames\n");
        traceFlush(&w);
    }
}

void zam_thread_trace(const ZamThread *thread, uint64_t skip, int fd) {
    ZamTrace trace;
    zam_thread_capture(thread, skip, &trace);
    zam_trace_write(&trace, fd);
}

void zam_thread_trace_current(int fd) {
    const ZamThread *thread = currentZamThread;
    if (thread != NULL)
        zam_thread_trace(thread, 0, fd);
}

// faultPanic is where a thread that made an invalid access a panic can be
// raised for is sent to, as though the faulting instruction called it.
static _Noreturn void faultPanic(void) {
    ZamThread *thread = currentZamThread;
    uint32_t fault = thread->fault;
    uintptr_t address = thread->faultAddress;
    bool bus = thread->faultBus;
    thread->faulting = false;
    zamrt_fault(thread, fault, address, bus);
}

// recoverable reports whether an invalid access thread made can be raised as
// a panic: one it is running compiled code or a native for, with defers to
// unwind to, rather than one made collecting, blocked or raising one already.
static bool recoverable(const ZamThread *thread) {
    return thread != NULL && !thread->faulting && thread->handlers != NULL &&
           atomic_load_explicit(&thread->state, memory_order_relaxed) == ZAMTHREAD_RUNNING &&
           !zam_gc_collecting(thread);
}

// faultAt records an invalid access thread made at address, for faultPanic.
static void faultAt(ZamThread *thread, uint32_t fault, uintptr_t address, bool bus) {
    thread->faulting = true;
    thread->fault = fault;
    thread->faultAddress = address;
    thread->faultBus = bus;
}

#if defined(_WIN32)
// faultVector sends a thread that made an invalid access a panic can be
// raised for to faultPanic, leaving every other exception to what handles it.
static LONG CALLBACK faultVector(EXCEPTION_POINTERS *exception) {
    const EXCEPTION_RECORD *record = exception->ExceptionRecord;
    bool access = record->ExceptionCode == EXCEPTION_ACCESS_VIOLATION ||
                  record->ExceptionCode == EXCEPTION_IN_PAGE_ERROR;
    ZamThread *thread = currentZamThread;
    if (!access || record->NumberParameters < 2 || !recoverable(thread))
        return EXCEPTION_CONTINUE_SEARCH;
    faultAt(thread, (uint32_t)record->ExceptionCode, (uintptr_t)record->ExceptionInformation[1],
            record->ExceptionCode == EXCEPTION_IN_PAGE_ERROR);
    CONTEXT *context = exception->ContextRecord;
#if defined(__aarch64__)
    context->Sp &= ~(DWORD64)15;
    context->Pc = (DWORD64)(uintptr_t)faultPanic;
#else
    // As a call leaves it: 8 past a multiple of 16.
    context->Rsp = (context->Rsp & ~(DWORD64)15) - 8;
    context->Rip = (DWORD64)(uintptr_t)faultPanic;
#endif
    return EXCEPTION_CONTINUE_EXECUTION;
}

// faultFilter says what was touched and where the thread touching it was, for
// a fault nothing handled, and ends the process with the fault's code, before
// Windows' own crash handling reports it again or waits on a dialog.
static LONG CALLBACK faultFilter(EXCEPTION_POINTERS *exception) {
    const EXCEPTION_RECORD *record = exception->ExceptionRecord;
    bool access = record->ExceptionCode == EXCEPTION_ACCESS_VIOLATION ||
                  record->ExceptionCode == EXCEPTION_IN_PAGE_ERROR;
    if (!access || record->NumberParameters < 2)
        return EXCEPTION_CONTINUE_SEARCH;
    TraceWriter w = {.fd = STDERR_FILENO};
    traceText(&w, "\033[31m");
    traceText(&w, record->ExceptionCode == EXCEPTION_IN_PAGE_ERROR ? "bus error" : "invalid memory access");
    traceText(&w, " at 0x");
    traceNumber(&w, (uint64_t)record->ExceptionInformation[1], 16);
    traceText(&w, "\n\033[0m");
    traceFlush(&w);
    zam_thread_trace_current(STDERR_FILENO);
    TerminateProcess(GetCurrentProcess(), record->ExceptionCode);
    return EXCEPTION_CONTINUE_SEARCH;
}

void zam_thread_install_fault_handler(void) {
    // Last, so that the heap commits reserved memory a touch faulted on first.
    (void)AddVectoredExceptionHandler(0, faultVector);
    SetUnhandledExceptionFilter(faultFilter);
}

void zam_thread_install_signal_slot(ZamThread *thread, int sig, void *closure) {
    if (sig < 0 || sig >= ZAM_SIGNAL_MAX)
        return;
    thread->signalHandlers[sig] = closure;
    (void)signal(sig, zamSignalHandler);
}
#else
// sendToFaultPanic has the interrupted context resume in faultPanic, below
// the red zone the code it interrupted may be using.
static void sendToFaultPanic(void *context) {
    ucontext_t *uc = context;
#if defined(__APPLE__) && defined(__aarch64__)
    __darwin_arm_thread_state64_set_sp(uc->uc_mcontext->__ss,
                                       (__darwin_arm_thread_state64_get_sp(uc->uc_mcontext->__ss) - 128) & ~(uint64_t)15);
    __darwin_arm_thread_state64_set_pc_fptr(uc->uc_mcontext->__ss, faultPanic);
#elif defined(__APPLE__)
    uc->uc_mcontext->__ss.__rsp = ((uc->uc_mcontext->__ss.__rsp - 128) & ~(uint64_t)15) - 8;
    uc->uc_mcontext->__ss.__rip = (uint64_t)(uintptr_t)faultPanic;
#elif defined(__aarch64__)
    uc->uc_mcontext.sp = (uc->uc_mcontext.sp - 128) & ~(uint64_t)15;
    uc->uc_mcontext.pc = (uint64_t)(uintptr_t)faultPanic;
#else
    // As a call leaves it: 8 past a multiple of 16.
    uc->uc_mcontext.gregs[REG_RSP] = (long long)(((uint64_t)uc->uc_mcontext.gregs[REG_RSP] - 128) & ~(uint64_t)15) - 8;
    uc->uc_mcontext.gregs[REG_RIP] = (long long)(uintptr_t)faultPanic;
#endif
}

// faultHandler sends a thread that made an invalid access a panic can be
// raised for to faultPanic. For any other, it says what was touched and where
// the thread touching it was, then returns to the instruction that faulted,
// with the handler reset, so it faults again and ends the process by the
// signal.
static void faultHandler(int sig, siginfo_t *info, void *context) {
    ZamThread *thread = currentZamThread;
    if (recoverable(thread)) {
        faultAt(thread, (uint32_t)sig, (uintptr_t)info->si_addr, sig == SIGBUS);
        sendToFaultPanic(context);
        return;
    }
    signal(sig, SIG_DFL);
    TraceWriter w = {.fd = STDERR_FILENO};
    traceText(&w, "\033[31m");
    traceText(&w, sig == SIGBUS ? "bus error" : "invalid memory access");
    traceText(&w, " at 0x");
    traceNumber(&w, (uint64_t)(uintptr_t)info->si_addr, 16);
    traceText(&w, "\n\033[0m");
    traceFlush(&w);
    zam_thread_trace_current(STDERR_FILENO);
}

void zam_thread_install_fault_handler(void) {
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_sigaction = faultHandler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_SIGINFO | SA_NODEFER;
    (void)sigaction(SIGSEGV, &sa, NULL);
    (void)sigaction(SIGBUS, &sa, NULL);
}

void zam_thread_install_signal_slot(ZamThread *thread, int sig, void *closure) {
    if (sig < 0 || sig >= ZAM_SIGNAL_MAX)
        return;
    thread->signalHandlers[sig] = closure;

    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = zamSignalHandler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_RESTART;
    (void)sigaction(sig, &sa, NULL);
}
#endif

int zam_thread_take_pending_signal(ZamThread *thread) {
    sig_atomic_t mask = thread->pendingSignalMask;
    if (mask == 0)
        return -1;
    for (int sig = 0; sig < ZAM_SIGNAL_MAX; sig++) {
        if (mask & ((sig_atomic_t)1u << sig)) {
            thread->pendingSignalMask &= ~((sig_atomic_t)1u << sig);
            return sig;
        }
    }
    return -1;
}

// The header is left alone. Main lives embedded in the ZamVM and has no header
// to care about; a spawned thread lives in an allocation whose first word is
// the mark word the collector finds it through, written by the allocator
// before this is called. Every other field is set here, with the ones not
// named relying on their zero value — the thread is zero when it reaches here
// in both cases, main because the whole VM was, and a spawned one because the
// allocator zeroes.
void zam_thread_init(ZamThread *thread, ZamVM *vm, uint64_t initialStackSize) {
    thread->vm = vm;
    thread->frames = NULL;
    thread->id = atomic_fetch_add(&vm->nextThreadId, 1) + 1;
    thread->operandCapacity = initialStackSize;
    thread->markByteCapacity = initialStackSize * ZAMWORD_BYTES;
    thread->functionEntry = ZAMMARK_NONE;
    thread->operands = zam_alloc(thread->operandCapacity * sizeof(ZamWord));
    thread->marks = zam_alloc(thread->markByteCapacity);
}

void zam_thread_free(ZamThread *thread) {
    zam_heap_release_cache(&thread->vm->heap, &thread->heapCache);
    free(thread->operands);
    free(thread->marks);
    free(thread->dirtied);
    free(thread->gray);

    // Every set the thread ever opened, not only the ones still open: a popped
    // set keeps its slots for the next comparison at that depth, so the
    // allocation outlives the comparison that asked for it.
    for (uint64_t at = 0; at < thread->visitedCapacity; at++)
        free(thread->visited[at].entries);
    free(thread->visited);

    // The struct itself is left as it was. A spawned thread lives in a heap
    // allocation that the VM's sweep is still to free, and the sweep walks the
    // allocation chain through the mark word — zeroing would snap that chain
    // at this block and strand everything behind it. Main is embedded in the
    // VM and so has no such chain to protect, but one of these two shapes is
    // the shared one.
}

// The stacks grow rather than being bounded, because how deep a program goes is
// not something the compiler wrote down: the initial size in the image is a
// starting point the program asked for and not a limit it agreed to.
#define ZAMDEFINE_GROW(name, field, countField, capacityField, elementType)    \
    static void name(ZamThread *thread, uint64_t needed) {                     \
        if (thread->countField + needed <= thread->capacityField)              \
            return;                                                            \
        while (thread->capacityField < thread->countField + needed)            \
            thread->capacityField *= 2;                                        \
        thread->field = zam_realloc(                                           \
            thread->field, thread->capacityField * sizeof(elementType));       \
    }

ZAMDEFINE_GROW(growOperands, operands, operandCount, operandCapacity, ZamWord)
ZAMDEFINE_GROW(growMarks, marks, markByteCount, markByteCapacity, uint8_t)

void zam_thread_push(ZamThread *thread, ZamWord word) {
    growOperands(thread, 1);
    thread->operands[thread->operandCount++] = word;
}

ZamWord zam_thread_pop(ZamThread *thread) {
    if (thread->operandCount == 0)
        zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
    return thread->operands[--thread->operandCount];
}

ZamWord zam_thread_peek(const ZamThread *thread, uint64_t depth) {
    if (depth >= thread->operandCount)
        zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
    return thread->operands[thread->operandCount - 1 - depth];
}

// A type id is written low word first, so pushing it leaves the packed short id
// deepest and the tail length on top. Popping reads them back in the order that
// undoes it.
void zam_thread_push_type(ZamThread *thread, ZamTypeID type) {
    growOperands(thread, ZAMTYPE_ID_WORDS);
    zam_thread_push(thread, (ZamWord)type.shortID << 32);
    zam_thread_push(thread, type.arrayLength);
    zam_thread_push(thread, type.tailLength);
}

ZamTypeID zam_thread_pop_type(ZamThread *thread) {
   ZamTypeID type;
    type.tailLength = zam_thread_pop(thread);
    type.arrayLength = zam_thread_pop(thread);
    type.shortID = (ZamTypeShort)(zam_thread_pop(thread) >> 32);
    return type;
}

ZamTypeID zam_thread_peek_type(const ZamThread *thread, uint64_t depth) {
    return (ZamTypeID){
        .tailLength = zam_thread_peek(thread, depth),
        .arrayLength = zam_thread_peek(thread, depth + 1),
        .shortID = (ZamTypeShort)(zam_thread_peek(thread, depth + 2) >> 32),
    };
}

static const uint64_t markEntryBytes[] = {
    [ZAMMARK_FUNCTION] = sizeof(ZamMarkFunction),
    [ZAMMARK_OBJECT] = sizeof(ZamMarkObject),
    [ZAMMARK_ARRAY] = sizeof(ZamMarkArray),
    [ZAMMARK_VALUE] = sizeof(ZamMarkValue),
};

uint64_t zam_mark_entry_bytes(ZamMarkKind kind) {
    if (kind >= ZAMMARK_COUNT)
        zam_die(ZAMEXIT_PANIC, "mark stack holds no kind %d\n", (int)kind);
    return markEntryBytes[kind];
}

ZamMarkKind zam_mark_kind_at(const ZamThread *thread, uint64_t offset) {
    if (offset >= thread->markByteCount)
        zam_die(ZAMEXIT_PANIC, "mark offset %llu is past the stack\n",
                (unsigned long long)offset);
    return (ZamMarkKind)thread->marks[offset];
}

// An entry is copied in and out whole. Every width is a multiple of a word and
// the buffer begins aligned, so nothing here is a misaligned access; memcpy is
// used rather than a cast because it says that without relying on it.
static uint64_t pushEntry(ZamThread *thread, const void *entry,
                          uint64_t byteCount) {
    uint64_t offset = thread->markByteCount;
    growMarks(thread, byteCount);
    memcpy(thread->marks + offset, entry, byteCount);
    thread->markByteCount = offset + byteCount;
    return offset;
}

static void readEntry(const ZamThread *thread, uint64_t offset, void *entry,
                      uint64_t byteCount) {
    if (offset + byteCount > thread->markByteCount)
        zam_die(ZAMEXIT_PANIC, "mark entry at %llu runs past the stack\n",
                (unsigned long long)offset);
    memcpy(entry, thread->marks + offset, byteCount);
}

void zam_thread_push_mark(ZamThread *thread, uint64_t operand) {
   ZamMarkObject entry = {.kind = ZAMMARK_OBJECT, .operand = operand};
    pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_push_array_mark(ZamThread *thread, ZamTypeID type,
                                uint64_t operand) {
    // The tail length goes no further than this: what is traversed is the run
    // of elements, which the element type and the count describe on their own.
   ZamMarkArray entry = {
        .kind = ZAMMARK_ARRAY,
        .operand = operand,
        .element = type.shortID,
        .length = type.arrayLength,
    };
    pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_push_value_mark(ZamThread *thread, ZamTypeID type,
                                uint64_t operand) {
   ZamMarkValue entry = {.kind = ZAMMARK_VALUE, .operand = operand};
    zam_type_id_write((ZamWord *)&entry.type, type);
    pushEntry(thread, &entry, sizeof entry);
}

// Where a search for the topmost entry may begin. Everything below the innermost
// open call belongs to an enclosing one, so the walk starts above its entry.
static uint64_t markSearchFloor(const ZamThread *thread) {
    return thread->functionEntry == ZAMMARK_NONE
               ? 0
               : thread->functionEntry + sizeof(ZamMarkFunction);
}

// popMark takes the topmost entry off if it names the operand word, and
// answers its kind, or ZAMMARK_FUNCTION where it took nothing.
static ZamMarkKind popMark(ZamThread *thread, uint64_t operand) {
    uint64_t last = thread->markByteCount;
    for (uint64_t at = markSearchFloor(thread); at < thread->markByteCount;) {
        last = at;
        at += zam_mark_entry_bytes(zam_mark_kind_at(thread, at));
    }
    if (last == thread->markByteCount)
        return ZAMMARK_FUNCTION;

    // Every kind that names a word puts the operand offset in the same place,
    // so which one it is does not have to be told apart to ask whether it
    // names this word.
   ZamMarkKind kind = zam_mark_kind_at(thread, last);
    if (kind == ZAMMARK_FUNCTION)
        return ZAMMARK_FUNCTION;

   ZamMarkObject entry;
    readEntry(thread, last, &entry, sizeof entry);
    if (entry.operand != operand)
        return ZAMMARK_FUNCTION;

    thread->markByteCount = last;
    return kind;
}

bool zam_thread_pop_mark(ZamThread *thread, uint64_t operand) {
    return popMark(thread, operand) != ZAMMARK_FUNCTION;
}

void *zam_thread_location_address(const ZamThread *thread, ZamWord location) {
    if (!zam_is_frame_location(location))
        return (void *)(uintptr_t)location;

    uint64_t byteOffset = location & ~ZAMFRAME_LOCATION_BIT;
    if (byteOffset >= thread->operandCount * ZAMWORD_BYTES)
        zam_die(ZAMEXIT_PANIC,
                "frame location at byte %llu is past the %llu word stack\n",
                (unsigned long long)byteOffset,
                (unsigned long long)thread->operandCount);
    return (uint8_t *)thread->operands + byteOffset;
}

uint64_t zam_thread_frame_base(const ZamThread *thread) {
    if (thread->functionEntry == ZAMMARK_NONE)
        return 0;

   ZamMarkFunction entry;
    readEntry(thread, thread->functionEntry, &entry, sizeof entry);
    return entry.operandPop;
}

void zam_thread_push_function(ZamThread *thread, uint64_t operandPop) {
   ZamMarkFunction entry = {
        .kind = ZAMMARK_FUNCTION,
        .previous = thread->functionEntry,
        .operandPop = operandPop,
    };
    thread->functionEntry = pushEntry(thread, &entry, sizeof entry);
}

ZamMarkFunction zam_thread_pop_function(ZamThread *thread) {
    uint64_t offset = thread->functionEntry;
    if (offset == ZAMMARK_NONE)
        zam_die(ZAMEXIT_PANIC, "returned with no call open\n");

   ZamMarkFunction entry;
    readEntry(thread, offset, &entry, sizeof entry);

    thread->markByteCount = offset;
    thread->operandCount = entry.operandPop;
    thread->functionEntry = entry.previous;

    // The closure and the arguments were pushed by the caller, so their entries
    // lie beneath this call's and survive the rewind above — but the words are
    // the frame's and have just gone, which would leave the entries naming slots
    // the program has given up. They are dropped here, and here rather than at
    // the call because entries are pushed in the order their words are: the
    // first one naming a word at or above the frame is where the frame's begin,
    // and everything from there up went with it.
    for (uint64_t at = markSearchFloor(thread); at < thread->markByteCount;) {
       ZamMarkKind kind = zam_mark_kind_at(thread, at);
        if (kind != ZAMMARK_FUNCTION) {
            // Every kind that names a word names it in the same place, so which
            // one it is does not have to be told apart to read the offset.
           ZamMarkObject named;
            readEntry(thread, at, &named, sizeof named);
            if (named.operand >= entry.operandPop) {
                thread->markByteCount = at;
                break;
            }
        }
        at += zam_mark_entry_bytes(kind);
    }
    return entry;
}

// ZAMVISITED_INITIAL_SLOTS is what a set is given the first time it is opened.
// A descent that meets no pair at all is the common one and allocates nothing,
// the slots being taken on the first record rather than on the push.
#define ZAMVISITED_INITIAL_SLOTS 16

// Where a pair's slot search begins. The three fields are mixed rather than
// added: two pointers an allocation apart and two a type id apart would
// otherwise land on one slot, and the probe is linear, so pairs that hash alike
// are pairs that walk over each other.
static uint64_t visitHash(ZamWord left, ZamWord right, ZamTypeShort type) {
    uint64_t hash = 14695981039346656037ULL;
    uint64_t words[] = {left, right, type};
    for (uint64_t at = 0; at < sizeof words / sizeof *words; at++) {
        hash = (hash ^ words[at]) * 1099511628211ULL;
        hash ^= hash >> 32;
    }
    return hash;
}

static bool visitEquals(const ZamVisit *entry, ZamWord left, ZamWord right,
                       ZamTypeShort type) {
    return entry->left == left && entry->right == right && entry->type == type;
}

// Rehashing into twice the slots, which open addressing needs before the probe
// has to walk most of the table to find a hole. The entries are re-placed rather
// than copied: where one lands is the slot count's to say.
static void growVisited(ZamVisitedSet *set) {
    uint64_t capacity = set->capacity == 0 ? ZAMVISITED_INITIAL_SLOTS
                                           : set->capacity * 2;
   ZamVisit *entries = zam_alloc(capacity * sizeof *entries);
    for (uint64_t at = 0; at < capacity; at++)
        entries[at] = (ZamVisit){0};

    for (uint64_t at = 0; at < set->capacity; at++) {
        const ZamVisit *entry = &set->entries[at];
        if (entry->left == 0)
            continue;
        uint64_t slot =
            visitHash(entry->left, entry->right, entry->type) & (capacity - 1);
        while (entries[slot].left != 0)
            slot = (slot + 1) & (capacity - 1);
        entries[slot] = *entry;
    }

    free(set->entries);
    set->entries = entries;
    set->capacity = capacity;
}

void zam_thread_push_visited(ZamThread *thread) {
    if (thread->visitedCount == thread->visitedCapacity) {
        thread->visitedCapacity =
            thread->visitedCapacity == 0 ? 4 : thread->visitedCapacity * 2;
        thread->visited = zam_realloc(
            thread->visited, thread->visitedCapacity * sizeof *thread->visited);
        for (uint64_t at = thread->visitedCount; at < thread->visitedCapacity;
             at++)
            thread->visited[at] = (ZamVisitedSet){0};
    }
    thread->visitedCount++;
}

void zam_thread_pop_visited(ZamThread *thread) {
    if (thread->visitedCount == 0)
        zam_die(ZAMEXIT_PANIC, "closed a comparison with none open\n");

    // The slots are kept and only emptied. They are the room the next
    // comparison at this depth will ask for, and a program comparing in a loop
    // is the one that asks most often.
   ZamVisitedSet *set = &thread->visited[--thread->visitedCount];
    for (uint64_t at = 0; at < set->capacity; at++)
        set->entries[at] = (ZamVisit){0};
    set->count = 0;
}

bool zam_thread_visit(ZamThread *thread, ZamWord left, ZamWord right,
                     ZamTypeShort type) {
    if (thread->visitedCount == 0)
        zam_die(ZAMEXIT_PANIC, "recorded a pair with no comparison open\n");
    if (left == 0 || right == 0)
        zam_die(ZAMEXIT_PANIC, "descended into a null\n");

   ZamVisitedSet *set = &thread->visited[thread->visitedCount - 1];

    // Grown at three quarters full rather than when full, which is where a
    // linear probe stops being short.
    if ((set->count + 1) * 4 > set->capacity * 3)
        growVisited(set);

    uint64_t slot = visitHash(left, right, type) & (set->capacity - 1);
    while (set->entries[slot].left != 0) {
        if (visitEquals(&set->entries[slot], left, right, type))
            return true;
        slot = (slot + 1) & (set->capacity - 1);
    }

    set->entries[slot] =
        (ZamVisit){.left = left, .right = right, .type = type};
    set->count++;
    return false;
}
