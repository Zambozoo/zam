#include "zam/native.h"

#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <unistd.h>

#include "definition.h"
#include "thread.h"
#include "vm.h"

// STD_MARK hands the receiver to the collector. Object.Mark names it, and
// Object.MarkObject names the same entry for the "bypass the user's override"
// path — the override is applied by the zam-side dispatch choosing to call
// the static directly, which spares a second native entry that would do the
// same work.
static void stdMark(ZamThread *thread) {
    (void)thread;
    ZAMTODO("STD_MARK: hand the receiver to the collector");
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
    int fd = open(cpath, mode, 0644);
    int openErrno = errno;
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

    ssize_t written = write((int)*fdPtr, buffer, count);
    if (written < 0) {
        *nOut = 0;
        *errOut = errorMessage(thread, errno);
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
    if (close((int)*fdPtr) < 0)
        *errOut = errorMessage(thread, errno);
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

    ssize_t bytesRead = read((int)*fdPtr, buffer, count);
    if (bytesRead < 0) {
        *nOut = 0;
        *errOut = errorMessage(thread, errno);
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

// stdGcLock and stdGcUnlock are placeholders for the GC pause pair the zam
// side already names. The collector itself has not been written, so there is
// nothing to pause or resume; the natives return cleanly so a program that
// calls GC.Lock()/GC.Unlock() around a critical section still runs.
static void stdGcLock(ZamThread *thread) {
    zam_native_return_void(thread);
}

static void stdGcUnlock(ZamThread *thread) {
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

const ZamNativeEntry zamNativeTable[] = {
    {"STD_MARK", stdMark},
    {"STD_MARK_OVERRIDE", stdMark},
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
    {"STD_GC_LOCK", stdGcLock},
    {"STD_GC_UNLOCK", stdGcUnlock},
    {"STD_SYSTEM_EXIT", stdSystemExit},
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
