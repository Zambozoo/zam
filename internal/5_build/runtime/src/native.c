#include "zam/native.h"

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <unistd.h>

#include "definition.h"
#include "gc.h"
#include "thread.h"
#include "vm.h"

// STD_MARK is Object's own Mark: while collecting, everything the receiver's
// members hold is reachable. STD_MARK_OVERRIDE is Objs.Mark: the receiver is
// reachable, and nothing it holds is traced.
static void stdMark(ZamThread *thread) {
    zam_gc_mark_members(thread, zam_native_argument(thread, 0));
    zam_native_return_void(thread);
}

static void stdMarkOverride(ZamThread *thread) {
    zam_gc_mark_only(thread, zam_native_argument(thread, 0));
    zam_native_return_void(thread);
}

// STD_DELETE marks the receiver's allocation dead for the next sweep. It is
// the body OpDelete used to run inline, now split out so a user's own Delete
// can decide whether to run it, delay it, or do something else first.
//
// Null is treated as a programming error even though OpDelete has already
// panicked on null before dispatching: an override could call this native with
// a different pointer, and the invariant has to hold wherever it is reached.
//
// The contained-bit check catches a delete of a value the allocator never
// handed out — a string literal in the static region, a member laid inline
// inside another object — which would otherwise free into memory nothing on
// this side owns. Only an interior pointer sets the upvalue to anything, so
// the message distinguishes the two for whoever reads it.
//
// STD_DELETE_OVERRIDE names the same entry, matching STD_MARK/STD_MARK_OVERRIDE.
static void stdDelete(ZamThread *thread) {
    void *pointer = (void *)(uintptr_t)zam_native_argument(thread, 0);
    if (pointer == NULL)
        zam_native_panic(thread, "STD_DELETE: null receiver");

   ZamMarkWord *mark = zam_mark_word_of(pointer);
    if (mark->contained)
        zam_native_panic(thread,
                         mark->upvalue != 0
                             ? "deleted a member of another object"
                             : "deleted a value with no allocation");
    mark->state = ZAMMARK_STATE_DEAD;
    zam_native_return_void(thread);
}

// The io.File natives, named by io/File.zam. A File wraps an OS-level handle,
// and each of these is the handle-level operation it was named for: open the
// file at a path, write or read bytes through its fd, close it on delete.
//
// Every one of them takes the fd as a direct int* rather than reaching through
// a File* and a header offset — the compiler is what knows where fd sits in
// the instance, and &this.fd is what the zam-side body emits. This file stays
// ignorant of File's layout, and a field reorder on the zam side cannot
// silently misread anything here.
//
// A syscall failure writes a strerror(errno)-based message through the errOut
// slot and returns normally. The wrapper method is what turns that string into
// a StrErr instance; the native stays out of the type system beyond what the
// SDK already exposes. On the success paths the error word is left null, which
// the zam side treats as "no error."

// errorMessage copies the C library's error description into a fresh zam
// string whose address the caller can store wherever a string value belongs.
// The returned word is a tracked allocation and is held alive by whichever
// operand slot it is written into.
static ZamWord errorMessage(ZamThread *thread, int err) {
    const char *msg = strerror(err);
    return (ZamWord)zam_native_new_string(thread, msg, strlen(msg));
}

static void stdFileInit(ZamThread *thread) {
    const ZamString *path =
        zam_string_of((void *)(uintptr_t)zam_native_argument(thread, 0));
    int mode = (int)zam_native_argument(thread, 1);
   ZamInt *fdOut = (ZamInt *)zam_native_argument_pointer(thread, 2);
   ZamWord *errOut = (ZamWord *)zam_native_argument_pointer(thread, 3);

    // The path is length-counted bytes without a terminator, which is what
    // open takes. The copy is small, bounded by the string's length, and freed
    // before the result is handed back. Allocation failure cannot be reported
    // through the Err channel — the Err path itself allocates — so this is
    // the one case left to the panic.
    char *cpath = malloc(path->length + 1);
    if (cpath == NULL)
        zam_native_panic(thread, "out of memory for path");
    memcpy(cpath, path->bytes, path->length);
    cpath[path->length] = '\0';

    // The permission mode 0644 is only consulted when the open mode asks to
    // create the file — O_CREAT, O_TMPFILE — and is ignored otherwise, so
    // passing it unconditionally is safe. The caller does not control it yet;
    // a permissions parameter would be a straightforward follow-up.
    zam_native_block_begin(thread);
    int fd = open(cpath, mode, 0644);
    int openErrno = errno;
    zam_native_block_end(thread);
    free(cpath);
    if (fd < 0) {
        *errOut = errorMessage(thread, openErrno);
        zam_native_return_void(thread);
        return;
    }

    *fdOut = fd;
    *errOut = 0;
    zam_native_return_void(thread);
}

// stdFileFlag answers the OS's value for one of the POSIX open flags, picked
// by the code the caller passed. The codes are stable in order; the zam side's
// File.ReadOnly()..File.Append() each pass a specific one, and the mapping is
// written down in exactly one place — here. The flag values are not portable
// across OSes (Linux's O_CREAT is 0100, macOS's is 0x0200), so hard-coding
// them on the zam side would be wrong; this native is how the zam code reaches
// the right numbers at run time.
static void stdFileFlag(ZamThread *thread) {
    int code = (int)zam_native_argument(thread, 0);
   ZamInt *out = (ZamInt *)zam_native_argument_pointer(thread, 1);

    switch (code) {
    case 0: *out = O_RDONLY; break;
    case 1: *out = O_WRONLY; break;
    case 2: *out = O_RDWR; break;
    case 3: *out = O_CREAT; break;
    case 4: *out = O_EXCL; break;
    case 5: *out = O_TRUNC; break;
    case 6: *out = O_APPEND; break;
    default:
        zam_native_panic(thread, "STD_FILE_FLAG: unknown flag code");
    }
    zam_native_return_void(thread);
}

static void stdFileWrite(ZamThread *thread) {
   ZamInt *fdPtr = (ZamInt *)zam_native_argument_pointer(thread, 0);
    const void *buffer = zam_native_argument_pointer(thread, 1);
    size_t count = (size_t)zam_native_argument(thread, 2);
   ZamInt *nOut = (ZamInt *)zam_native_argument_pointer(thread, 3);
   ZamWord *errOut = (ZamWord *)zam_native_argument_pointer(thread, 4);

    int fd = (int)*fdPtr;
    zam_native_block_begin(thread);
    ssize_t written = write(fd, buffer, count);
    int writeErrno = errno;
    zam_native_block_end(thread);
    if (written < 0) {
        *nOut = 0;
        *errOut = errorMessage(thread, writeErrno);
    } else {
        *nOut = (ZamInt)written;
        *errOut = 0;
    }

    zam_native_return_void(thread);
}

// stdFileClose releases the OS handle sitting at the pointer and reports a
// close() failure through the errOut slot the same way the read and write
// natives do. No guard on the fd itself: closing stdin, double-closing, or
// closing a slot that was never opened is a program bug and whatever the OS
// does with it is undefined — the native stays out of that lane. The caller
// passes the fd field directly, which keeps this file ignorant of where
// File puts it.
static void stdFileClose(ZamThread *thread) {
   ZamInt *fdPtr = (ZamInt *)zam_native_argument_pointer(thread, 0);
   ZamWord *errOut = (ZamWord *)zam_native_argument_pointer(thread, 1);
    int fd = (int)*fdPtr;
    zam_native_block_begin(thread);
    int closed = close(fd);
    int closeErrno = errno;
    zam_native_block_end(thread);
    if (closed < 0)
        *errOut = errorMessage(thread, closeErrno);
    else
        *errOut = 0;
    zam_native_return_void(thread);
}

static void stdFileRead(ZamThread *thread) {
   ZamInt *fdPtr = (ZamInt *)zam_native_argument_pointer(thread, 0);
    void *buffer = zam_native_argument_pointer(thread, 1);
    size_t count = (size_t)zam_native_argument(thread, 2);
   ZamInt *nOut = (ZamInt *)zam_native_argument_pointer(thread, 3);
   ZamWord *errOut = (ZamWord *)zam_native_argument_pointer(thread, 4);

    int fd = (int)*fdPtr;
    zam_native_block_begin(thread);
    ssize_t bytesRead = read(fd, buffer, count);
    int readErrno = errno;
    zam_native_block_end(thread);
    if (bytesRead < 0) {
        *nOut = 0;
        *errOut = errorMessage(thread, readErrno);
    } else {
        *nOut = (ZamInt)bytesRead;
        *errOut = 0;
    }

    zam_native_return_void(thread);
}

// The std.Strs natives. FromBytes allocates a fresh string and copies, and
// ByteLen just reads the length word. ToBytes and Chars have no native: both
// are a plain pointer cast on the zam side (s{byte*} / s{char*}) over the
// string's own bytes, so no code has to run per call. The UTF-8-aware
// natives (Chars's companion ToChars, and CharLen) are stubs — the shape is
// in Strs.zam so programs can be written against them, but reaching one
// panics loudly.
static void stdStrsFromBytes(ZamThread *thread) {
    const char *bytes =
        (const char *)zam_native_argument_pointer(thread, 0);
    uint64_t length = (uint64_t)zam_native_argument(thread, 1);
   ZamWord *strOut = (ZamWord *)zam_native_argument_pointer(thread, 2);
    *strOut = zam_native_new_string(thread, bytes, length);
    zam_native_return_void(thread);
}

static void stdStrsByteLen(ZamThread *thread) {
    const ZamString *s =
        zam_string_of((void *)(uintptr_t)zam_native_argument(thread, 0));
   ZamInt *nOut = (ZamInt *)zam_native_argument_pointer(thread, 1);
    *nOut = (ZamInt)s->length;
    zam_native_return_void(thread);
}

static void stdStrsToChars(ZamThread *thread) {
    (void)thread;
    ZAMTODO("STD_STRS_TO_CHARS: UTF-8 encode a code-point run");
}

static void stdStrsCharLen(ZamThread *thread) {
    (void)thread;
    ZAMTODO("STD_STRS_CHAR_LEN: UTF-8 walk counting code points");
}

// stdArrsCopy moves a run of A elements from src to dst. The element type
// rode across as its three-word id so the width is read off the image here:
// a template is not one width, and baking a size for one instantiation would
// be wrong for every other. The length is a uint, so there is no sign to
// guard against. memmove rather than memcpy, so an overlapping range inside
// the same run copies without the caller having to split it.
static void stdArrsCopy(ZamThread *thread) {
   ZamWord typeWords[ZAMTYPE_ID_WORDS] = {
        zam_native_argument(thread, 0),
        zam_native_argument(thread, 1),
        zam_native_argument(thread, 2),
    };
   ZamTypeID type = zam_type_id_read(typeWords);
   ZamUint len = (ZamUint)zam_native_argument(thread, 3);
    const void *src = zam_native_argument_pointer(thread, 4);
    void *dst = zam_native_argument_pointer(thread, 5);

    uint64_t stride = zam_type_bytes(&thread->vm->image, type);
    memmove(dst, src, (size_t)len * stride);
    zam_native_return_void(thread);
}

// stdGcBlock and stdGcUnblock freeze and unfreeze collection for the calling
// thread. They nest: a collection waits until every GC.Block a thread has
// made is matched by a GC.Unblock and the thread reaches its next safepoint.
static void stdGcBlock(ZamThread *thread) {
    thread->gcBlockDepth++;
    zam_native_return_void(thread);
}

static void stdGcUnblock(ZamThread *thread) {
    if (thread->gcBlockDepth == 0)
        zam_native_panic(thread, "GC.Unblock without a matching GC.Block");
    thread->gcBlockDepth--;
    zam_native_return_void(thread);
}

// stdSystemExit ends the process with the given status, bypassing the
// interpreter's own return path: no frames are unwound, no destructors run,
// no mark sweep runs. The caller asked for the program to stop and this is
// what stops it. The native does not return; zam_native_return_void is
// unreachable past exit() and omitting it does not matter.
static void stdSystemExit(ZamThread *thread) {
    (void)thread;
    int code = (int)zam_native_argument(thread, 0);
    exit(code);
}

// stdSystemProcessID reports getpid(), the id Signal.Send hands to kill().
static void stdSystemProcessID(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    *out = (ZamWord)getpid();
    zam_native_return_void(thread);
}

// stdSystemThreadID reports pthread_self() as the raw bits of the pthread_t,
// which is the form stdSignalThreadSend copies back out of the word.
static void stdSystemThreadID(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    pthread_t tid = pthread_self();
    uint64_t threadID = 0;
    memcpy(&threadID, &tid, sizeof tid < sizeof threadID
                                ? sizeof tid
                                : sizeof threadID);
    *out = (ZamWord)threadID;
    zam_native_return_void(thread);
}

// stdSignalRaise sends the signal to this process. raise() returns 0 on
// success and non-zero on failure; the native swallows the result since the
// zam signature returns void. Failure here means the OS rejected the signal
// number — a program bug the user wrote the wrong code for — rather than
// anything the caller could recover from.
static void stdSignalRaise(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    (void)raise(sig);
    zam_native_return_void(thread);
}

// stdSignalSend sends a signal to another process. The zam signature takes
// (sig, pid) in that order to match Signal.Send; kill() takes (pid, sig).
// Any OS error is swallowed, as with Raise.
static void stdSignalSend(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    pid_t pid = (pid_t)zam_native_argument(thread, 1);
    (void)kill(pid, sig);
    zam_native_return_void(thread);
}

// stdSignalInit maps a Signal enum ordinal to the OS-level signal number for
// the running platform. The index set is this native's contract with
// System.Signal — adding a signal to the enum means adding a case here, and
// a mismatch would hand the kernel a number the enum name does not stand
// for. Keep the switch in step with the signalNumber(N) calls in System.zam.
static void stdSignalInit(ZamThread *thread) {
    int idx = (int)zam_native_argument(thread, 0);
    ZamInt *out = (ZamInt *)zam_native_argument_pointer(thread, 1);
    int sig;
    switch (idx) {
    case 0:  sig = SIGHUP;  break;
    case 1:  sig = SIGINT;  break;
    case 2:  sig = SIGQUIT; break;
    case 3:  sig = SIGILL;  break;
    case 4:  sig = SIGABRT; break;
    case 5:  sig = SIGFPE;  break;
    case 6:  sig = SIGKILL; break;
    case 7:  sig = SIGSEGV; break;
    case 8:  sig = SIGPIPE; break;
    case 9:  sig = SIGALRM; break;
    case 10: sig = SIGTERM; break;
    case 11: sig = SIGSTOP; break;
    case 12: sig = SIGCONT; break;
    case 13: sig = SIGCHLD; break;
    case 14: sig = SIGUSR1; break;
    case 15: sig = SIGUSR2; break;
    default:
        zam_native_panic(thread, "STD_SIGNAL_INIT: unknown signal index");
    }
    *out = (ZamInt)sig;
    zam_native_return_void(thread);
}

// stdSignalThreadSend sends a signal to another thread of this process. The
// zam signature takes (sig, threadID) in that order to match Signal.ThreadSend;
// pthread_kill takes (pthread_t, int). The thread id crosses the ABI as the
// uint word the caller handed in, which is the raw bits of the OS-level
// pthread_t — the only way the two sides agree on which thread is meant.
static void stdSignalThreadSend(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    uint64_t threadID = zam_native_argument(thread, 1);
    pthread_t tid;
    memcpy(&tid, &threadID, sizeof tid < sizeof threadID
                                 ? sizeof tid
                                 : sizeof threadID);
    (void)pthread_kill(tid, sig);
    zam_native_return_void(thread);
}

// setOneSignal builds a sigset_t holding just sig. Returns false if either
// the empty-set call or the add fails, which on every platform means sig is
// out of range for a sigset.
static bool setOneSignal(int sig, sigset_t *out) {
    if (sigemptyset(out) != 0)
        return false;
    if (sigaddset(out, sig) != 0)
        return false;
    return true;
}

// stdSignalBlock and stdSignalUnblock change the process-wide signal mask so
// a signal is held pending (or released) across every thread. Fire-and-
// forget on the zam side; an invalid signal number panics rather than being
// reported back silently, since it is a program bug the caller cannot
// recover from.
static void stdSignalBlock(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    sigset_t set;
    if (!setOneSignal(sig, &set) ||
        sigprocmask(SIG_BLOCK, &set, NULL) != 0)
        zam_native_panic(thread, "STD_SIGNAL_BLOCK: invalid signal number");
    zam_native_return_void(thread);
}

static void stdSignalUnblock(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    sigset_t set;
    if (!setOneSignal(sig, &set) ||
        sigprocmask(SIG_UNBLOCK, &set, NULL) != 0)
        zam_native_panic(thread, "STD_SIGNAL_UNBLOCK: invalid signal number");
    zam_native_return_void(thread);
}

// stdSignalThreadBlock and stdSignalThreadUnblock are the per-thread form,
// touching only the calling thread's mask. pthread_sigmask has the same
// shape as sigprocmask but is thread-local; the C handler respects whichever
// mask the OS consults, which on a per-thread signal is this one.
static void stdSignalThreadBlock(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    sigset_t set;
    if (!setOneSignal(sig, &set) ||
        pthread_sigmask(SIG_BLOCK, &set, NULL) != 0)
        zam_native_panic(thread,
                         "STD_SIGNAL_THREAD_BLOCK: invalid signal number");
    zam_native_return_void(thread);
}

static void stdSignalThreadUnblock(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    sigset_t set;
    if (!setOneSignal(sig, &set) ||
        pthread_sigmask(SIG_UNBLOCK, &set, NULL) != 0)
        zam_native_panic(thread,
                         "STD_SIGNAL_THREAD_UNBLOCK: invalid signal number");
    zam_native_return_void(thread);
}

// stdSignalIsBlocked and stdSignalIsThreadBlocked read the current mask
// without changing it — passing NULL as the set to {sig,pthread}_sigmask is
// how POSIX spells "just hand me the current mask" — and report whether sig
// is a member.
static void stdSignalIsBlocked(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 1);
    sigset_t current;
    if (sigprocmask(0, NULL, &current) != 0)
        zam_native_panic(thread, "STD_SIGNAL_IS_BLOCKED: sigprocmask failed");
    int member = sigismember(&current, sig);
    if (member < 0)
        zam_native_panic(thread,
                         "STD_SIGNAL_IS_BLOCKED: invalid signal number");
    *out = member != 0;
    zam_native_return_void(thread);
}

static void stdSignalIsThreadBlocked(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 1);
    sigset_t current;
    if (pthread_sigmask(0, NULL, &current) != 0)
        zam_native_panic(thread,
                         "STD_SIGNAL_IS_THREAD_BLOCKED: pthread_sigmask failed");
    int member = sigismember(&current, sig);
    if (member < 0)
        zam_native_panic(thread,
                         "STD_SIGNAL_IS_THREAD_BLOCKED: invalid signal number");
    *out = member != 0;
    zam_native_return_void(thread);
}

// Install the zam closure as the handler for sig on the calling thread. The
// native stores the closure in the thread's own handler table and asks the
// OS to deliver future occurrences through the shared C handler. The C
// handler itself only ORs a bit into the current thread's pending mask —
// running the closure directly from signal context would re-enter the
// interpreter from a place that cannot safely call anything that is not
// async-signal-safe.
static void stdSignalHandle(ZamThread *thread) {
    uint64_t sig = zam_native_argument(thread, 0);
    ZamWord closure = zam_native_argument(thread, 1);
    if (sig >= ZAM_SIGNAL_MAX)
        zam_native_panic(thread,
                         "STD_SIGNAL_HANDLE: signal number out of range");

    zam_thread_install_signal_slot(thread, (int)sig,
                                   (void *)(uintptr_t)closure);
    zam_native_return_void(thread);
}

const ZamNativeEntry zamNativeTable[] = {
    {"STD_MARK", stdMark},
    {"STD_MARK_OVERRIDE", stdMarkOverride},
    {"STD_DELETE", stdDelete},
    {"STD_DELETE_OVERRIDE", stdDelete},
    {"STD_FILE_INIT", stdFileInit},
    {"STD_FILE_FLAG", stdFileFlag},
    {"STD_FILE_WRITE", stdFileWrite},
    {"STD_FILE_READ", stdFileRead},
    {"STD_FILE_CLOSE", stdFileClose},
    {"STD_STRS_FROM_BYTES", stdStrsFromBytes},
    {"STD_STRS_TO_CHARS", stdStrsToChars},
    {"STD_STRS_CHAR_LEN", stdStrsCharLen},
    {"STD_STRS_BYTE_LEN", stdStrsByteLen},
    {"STD_ARRS_COPY", stdArrsCopy},
    {"STD_GC_BLOCK", stdGcBlock},
    {"STD_GC_UNBLOCK", stdGcUnblock},
    {"STD_SYSTEM_EXIT", stdSystemExit},
    {"STD_SYSTEM_PROCESS_ID", stdSystemProcessID},
    {"STD_SYSTEM_THREAD_ID", stdSystemThreadID},
    {"STD_SIGNAL_INIT", stdSignalInit},
    {"STD_SIGNAL_RAISE", stdSignalRaise},
    {"STD_SIGNAL_SEND", stdSignalSend},
    {"STD_SIGNAL_THREAD_SEND", stdSignalThreadSend},
    {"STD_SIGNAL_HANDLE", stdSignalHandle},
    {"STD_SIGNAL_BLOCK", stdSignalBlock},
    {"STD_SIGNAL_UNBLOCK", stdSignalUnblock},
    {"STD_SIGNAL_THREAD_BLOCK", stdSignalThreadBlock},
    {"STD_SIGNAL_THREAD_UNBLOCK", stdSignalThreadUnblock},
    {"STD_SIGNAL_IS_BLOCKED", stdSignalIsBlocked},
    {"STD_SIGNAL_IS_THREAD_BLOCKED", stdSignalIsThreadBlocked},
};

const uint64_t zamNativeTableCount =
    sizeof(zamNativeTable) / sizeof(zamNativeTable[0]);

// The project table is defined by the wrapper C file the build driver
// generates: one entry per name reached from the project's .zam sources,
// each binding the name to a C function the project itself ships. A project
// with no natives still contributes a zero-length table, so these symbols
// are always defined and the runtime need not conditionally link against
// anything.
extern const ZamNativeEntry zamProjectNativeTable[];
extern const uint64_t zamProjectNativeTableCount;

// matchNative searches a table for a name of the given length and returns the
// bound function, or NULL if the table has no such entry. Names are stored as
// NUL-terminated C strings in the table and as length-counted bytes in the
// image, so the compare is bounded by both to catch a prefix match that would
// otherwise bind the wrong function.
static ZamNativeFn matchNative(const ZamNativeEntry *table, uint64_t count,
                               const char *name, uint64_t nameByteCount) {
    for (uint64_t i = 0; i < count; i++) {
        const char *candidate = table[i].name;
        if (strlen(candidate) == nameByteCount &&
            memcmp(candidate, name, nameByteCount) == 0)
            return table[i].fn;
    }
    return NULL;
}

void zam_natives_resolve(ZamVM *vm) {
    vm->nativeCount = vm->image.nativeCount;
    if (vm->nativeCount == 0) {
        vm->natives = NULL;
        return;
    }
    vm->natives = zam_alloc(vm->nativeCount * sizeof(ZamNativeFn));

    // Resolution is by name and happens once, here, rather than at each call:
    // the image names a slot, and what the slot holds is settled before the
    // first instruction runs. A name with nothing to bind to is the end of the
    // run, since the program will certainly call it and there is nothing to
    // call.
    for (uint64_t slot = 0; slot < vm->nativeCount; slot++) {
        uint64_t nameByteCount;
        const char *name = zam_image_native_name(&vm->image, slot, &nameByteCount);

        // The standard library's table is searched first, so a name that
        // appears in both is bound to the runtime's own entry. A project is
        // free to ship a C function whose name begins with STD_ by accident,
        // but it may not shadow one the standard library already answers to.
       ZamNativeFn fn = matchNative(zamNativeTable, zamNativeTableCount, name,
                                     nameByteCount);
        if (fn == NULL)
            fn = matchNative(zamProjectNativeTable,
                             zamProjectNativeTableCount, name, nameByteCount);
        vm->natives[slot] = fn;

        if (vm->natives[slot] == NULL)
            zam_die(ZAMEXIT_IMAGE,
                    "the image names native `%.*s`, which this runtime was not "
                    "built with\n",
                    (int)nameByteCount, name);
    }
}

// A native's frame begins at the function entry OpCallNative pushed. The base
// slot holds the native address — the one word the opcode read to find this
// function — and every slot above it is an argument. Argument indexing counts
// from the first word above the base, so the native asks about its arguments in
// source order with no accounting for the callee's own slot.
uint64_t zam_native_argument_words(ZamThread *thread) {
    uint64_t base = zam_thread_frame_base(thread);
    uint64_t top = thread->operandCount;
    if (top <= base + 1)
        return 0;
    return top - base - 1;
}

uint64_t zam_native_argument(ZamThread *thread, uint64_t wordIndex) {
    uint64_t base = zam_thread_frame_base(thread);
    uint64_t slot = base + 1 + wordIndex;
    if (slot >= thread->operandCount)
        zam_die(ZAMEXIT_PANIC,
                "native read argument word %llu, which is past its %llu-word frame\n",
                (unsigned long long)wordIndex,
                (unsigned long long)(thread->operandCount - base - 1));
    return thread->operands[slot];
}

void *zam_native_argument_pointer(ZamThread *thread, uint64_t wordIndex) {
    return zam_thread_location_address(
        thread, zam_native_argument(thread, wordIndex));
}

// zam_native_return pops the native's frame and lays the result where the
// caller reads one. The pop drops every mark entry naming a slot in the frame —
// which includes the entries the caller pushed for its arguments — so the
// result's own mark entries are added back here, from the bitmask the caller
// passed. The entries name the result at its new location, at the base.
void zam_native_return(ZamThread *thread, const uint64_t *words,
                       uint64_t wordCount, uint64_t markPointerBits) {
   ZamMarkFunction frame = zam_thread_pop_function(thread);
    uint64_t base = frame.operandPop;
    for (uint64_t i = 0; i < wordCount; i++) {
        zam_thread_push(thread, words[i]);
        if ((markPointerBits >> i) & 1)
            zam_thread_push_mark(thread, base + i);
    }
}

void zam_native_return_void(ZamThread *thread) {
    zam_thread_pop_function(thread);
}

void zam_native_block_begin(ZamThread *thread) { zam_vm_enter_safe(thread); }

void zam_native_block_end(ZamThread *thread) { zam_vm_leave_safe(thread); }

_Noreturn void zam_native_panic(ZamThread *thread, const char *message) {
    (void)thread;
    zam_die(ZAMEXIT_PANIC, "panic in native: %s\n", message);
}

uint64_t zam_native_new_string(ZamThread *thread, const char *bytes,
                               uint64_t length) {
    // Mirrors newString in run.c, with the caller's bytes copied in. The size
    // passed to zam_vm_alloc is the mark word plus the string's own payload,
    // which zam_string_bytes rounds up to a word so the next slot after it
    // begins where the layout expects.
   ZamString *string = zam_string_of(
        zam_vm_alloc(thread->vm, thread, ZAMOBJECT_STRING, 0, 0,
                     sizeof(ZamMarkWord) + zam_string_bytes(length)));
    string->length = length;
    if (length != 0)
        memcpy(string->bytes, bytes, length);
    return (uint64_t)(uintptr_t)string;
}
