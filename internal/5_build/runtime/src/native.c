#include "zam/native.h"

#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <pthread.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>
#include <sys/random.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

#include "definition.h"
#include "gc.h"
#include "run.h"
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

// cString copies the zam string at wordIndex into a NUL-terminated C string the
// caller frees, which is what every path-taking call wants.
static char *cString(ZamThread *thread, uint64_t wordIndex) {
    const ZamString *s =
        zam_string_of((void *)(uintptr_t)zam_native_argument(thread, wordIndex));
    char *c = malloc(s->length + 1);
    if (c == NULL)
        zam_native_panic(thread, "out of memory for a path");
    memcpy(c, s->bytes, s->length);
    c[s->length] = '\0';
    return c;
}

// finishErr writes the error slot at errIndex from the result of a call that
// answers -1 and errno on failure, and returns from the native.
static void finishErr(ZamThread *thread, uint64_t errIndex, int result,
                      int err) {
    ZamWord *errOut = (ZamWord *)zam_native_argument_pointer(thread, errIndex);
    *errOut = result < 0 ? errorMessage(thread, err) : 0;
    zam_native_return_void(thread);
}

static void stdFileSeek(ZamThread *thread) {
    int fd = (int)*(ZamInt *)zam_native_argument_pointer(thread, 0);
    off_t offset = (off_t)(ZamInt)zam_native_argument(thread, 1);
    int whence = (int)zam_native_argument(thread, 2);
    ZamInt *posOut = (ZamInt *)zam_native_argument_pointer(thread, 3);
    off_t pos = lseek(fd, offset, whence);
    int err = errno;
    *posOut = pos < 0 ? 0 : (ZamInt)pos;
    finishErr(thread, 4, pos < 0 ? -1 : 0, err);
}

static void stdFileSize(ZamThread *thread) {
    int fd = (int)*(ZamInt *)zam_native_argument_pointer(thread, 0);
    ZamInt *sizeOut = (ZamInt *)zam_native_argument_pointer(thread, 1);
    struct stat st;
    int result = fstat(fd, &st);
    int err = errno;
    *sizeOut = result < 0 ? 0 : (ZamInt)st.st_size;
    finishErr(thread, 2, result, err);
}

static void stdFileSync(ZamThread *thread) {
    int fd = (int)*(ZamInt *)zam_native_argument_pointer(thread, 0);
    zam_native_block_begin(thread);
    int result = fsync(fd);
    int err = errno;
    zam_native_block_end(thread);
    finishErr(thread, 1, result, err);
}

// stdFsStat reports the size, permission bits, kind and modification time in
// seconds since the epoch of what lies at a path, following symbolic links.
static void stdFsStat(ZamThread *thread) {
    char *path = cString(thread, 0);
    ZamInt *sizeOut = (ZamInt *)zam_native_argument_pointer(thread, 1);
    ZamInt *modeOut = (ZamInt *)zam_native_argument_pointer(thread, 2);
    bool *dirOut = (bool *)zam_native_argument_pointer(thread, 3);
    ZamInt *timeOut = (ZamInt *)zam_native_argument_pointer(thread, 4);
    struct stat st;
    zam_native_block_begin(thread);
    int result = stat(path, &st);
    int err = errno;
    zam_native_block_end(thread);
    free(path);
    if (result == 0) {
        *sizeOut = (ZamInt)st.st_size;
        *modeOut = (ZamInt)(st.st_mode & 07777);
        *dirOut = S_ISDIR(st.st_mode);
        *timeOut = (ZamInt)st.st_mtime;
    }
    finishErr(thread, 5, result, err);
}

static void stdFsRemove(ZamThread *thread) {
    char *path = cString(thread, 0);
    zam_native_block_begin(thread);
    int result = remove(path);
    int err = errno;
    zam_native_block_end(thread);
    free(path);
    finishErr(thread, 1, result, err);
}

static void stdFsRename(ZamThread *thread) {
    char *from = cString(thread, 0);
    char *to = cString(thread, 1);
    zam_native_block_begin(thread);
    int result = rename(from, to);
    int err = errno;
    zam_native_block_end(thread);
    free(from);
    free(to);
    finishErr(thread, 2, result, err);
}

static void stdFsMakeDir(ZamThread *thread) {
    char *path = cString(thread, 0);
    mode_t mode = (mode_t)zam_native_argument(thread, 1);
    zam_native_block_begin(thread);
    int result = mkdir(path, mode);
    int err = errno;
    zam_native_block_end(thread);
    free(path);
    finishErr(thread, 2, result, err);
}

static void stdFsChmod(ZamThread *thread) {
    char *path = cString(thread, 0);
    mode_t mode = (mode_t)zam_native_argument(thread, 1);
    int result = chmod(path, mode);
    int err = errno;
    free(path);
    finishErr(thread, 2, result, err);
}

static void stdFsChdir(ZamThread *thread) {
    char *path = cString(thread, 0);
    int result = chdir(path);
    int err = errno;
    free(path);
    finishErr(thread, 1, result, err);
}

static int compareNames(const void *a, const void *b) {
    return strcmp(*(char *const *)a, *(char *const *)b);
}

// stdFsReadDir lists a directory's entries but . and .., sorted bytewise and
// joined by NUL bytes into one string, which the zam side splits.
static void stdFsReadDir(ZamThread *thread) {
    char *path = cString(thread, 0);
    ZamWord *namesOut = (ZamWord *)zam_native_argument_pointer(thread, 1);
    uint64_t count = 0, capacity = 16, bytes = 0;
    char **names = zam_alloc(capacity * sizeof *names);

    zam_native_block_begin(thread);
    DIR *dir = opendir(path);
    int err = errno;
    if (dir != NULL) {
        for (struct dirent *entry = readdir(dir); entry != NULL;
             entry = readdir(dir)) {
            if (strcmp(entry->d_name, ".") == 0 ||
                strcmp(entry->d_name, "..") == 0)
                continue;
            if (count == capacity) {
                capacity *= 2;
                char **grown = realloc(names, capacity * sizeof *names);
                if (grown == NULL)
                    zam_native_panic(thread, "out of memory listing a directory");
                names = grown;
            }
            names[count] = strdup(entry->d_name);
            bytes += strlen(entry->d_name) + 1;
            count++;
        }
        closedir(dir);
    }
    zam_native_block_end(thread);
    free(path);

    if (dir != NULL) {
        qsort(names, count, sizeof *names, compareNames);
        char *joined = zam_alloc(bytes + 1);
        uint64_t at = 0;
        for (uint64_t i = 0; i < count; i++) {
            if (i > 0)
                joined[at++] = '\0';
            size_t length = strlen(names[i]);
            memcpy(joined + at, names[i], length);
            at += length;
            free(names[i]);
        }
        *namesOut = (ZamWord)zam_native_new_string(thread, joined, at);
        free(joined);
    }
    free(names);
    finishErr(thread, 2, dir == NULL ? -1 : 0, err);
}

static void stdFsCwd(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    char *cwd = getcwd(NULL, 0);
    int err = errno;
    if (cwd != NULL) {
        *out = (ZamWord)zam_native_new_string(thread, cwd, strlen(cwd));
        free(cwd);
    }
    finishErr(thread, 1, cwd == NULL ? -1 : 0, err);
}

// stdFsTempDir answers $TMPDIR, or /tmp when it is unset, without a trailing
// slash.
static void stdFsTempDir(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    const char *dir = getenv("TMPDIR");
    if (dir == NULL || dir[0] == '\0')
        dir = "/tmp";
    size_t length = strlen(dir);
    while (length > 1 && dir[length - 1] == '/')
        length--;
    *out = (ZamWord)zam_native_new_string(thread, dir, length);
    zam_native_return_void(thread);
}

static void stdPipeInit(ZamThread *thread) {
    ZamInt *readOut = (ZamInt *)zam_native_argument_pointer(thread, 0);
    ZamInt *writeOut = (ZamInt *)zam_native_argument_pointer(thread, 1);
    int fds[2];
    int result = pipe(fds);
    int err = errno;
    if (result == 0) {
        *readOut = fds[0];
        *writeOut = fds[1];
    }
    finishErr(thread, 2, result, err);
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

// stdStrsToChars encodes a run of code points as a fresh UTF-8 str. A code
// point past U+10FFFF or in the surrogate range has no encoding and panics.
static void stdStrsToChars(ZamThread *thread) {
    const uint32_t *chars =
        (const uint32_t *)zam_native_argument_pointer(thread, 0);
    ZamInt count = (ZamInt)zam_native_argument(thread, 1);
   ZamWord *strOut = (ZamWord *)zam_native_argument_pointer(thread, 2);
    if (count < 0)
        zam_native_panic(thread, "STD_STRS_TO_CHARS: negative length");

    char *bytes = zam_alloc(count == 0 ? 1 : (size_t)count * 4);
    uint64_t length = 0;
    for (ZamInt i = 0; i < count; i++) {
        uint32_t c = chars[i];
        if (c > 0x10FFFF || (c >= 0xD800 && c <= 0xDFFF)) {
            free(bytes);
            zam_native_panic(thread, "STD_STRS_TO_CHARS: invalid code point");
        }
        if (c < 0x80) {
            bytes[length++] = (char)c;
        } else if (c < 0x800) {
            bytes[length++] = (char)(0xC0 | (c >> 6));
            bytes[length++] = (char)(0x80 | (c & 0x3F));
        } else if (c < 0x10000) {
            bytes[length++] = (char)(0xE0 | (c >> 12));
            bytes[length++] = (char)(0x80 | ((c >> 6) & 0x3F));
            bytes[length++] = (char)(0x80 | (c & 0x3F));
        } else {
            bytes[length++] = (char)(0xF0 | (c >> 18));
            bytes[length++] = (char)(0x80 | ((c >> 12) & 0x3F));
            bytes[length++] = (char)(0x80 | ((c >> 6) & 0x3F));
            bytes[length++] = (char)(0x80 | (c & 0x3F));
        }
    }
    *strOut = zam_native_new_string(thread, bytes, length);
    free(bytes);
    zam_native_return_void(thread);
}

// stdStrsCharLen counts the code points of a str by its lead bytes, every byte
// that is not a continuation byte beginning one.
static void stdStrsCharLen(ZamThread *thread) {
    const ZamString *s =
        zam_string_of((void *)(uintptr_t)zam_native_argument(thread, 0));
   ZamInt *nOut = (ZamInt *)zam_native_argument_pointer(thread, 1);
    ZamInt n = 0;
    for (uint64_t i = 0; i < s->length; i++)
        if (((unsigned char)s->bytes[i] & 0xC0) != 0x80)
            n++;
    *nOut = n;
    zam_native_return_void(thread);
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

// stdArrsMarkAll marks a run of len A elements while collecting, walking it
// in C as the one value of type A[len] it is. A run whose elements hold
// nothing to mark costs nothing.
static void stdArrsMarkAll(ZamThread *thread) {
   ZamWord typeWords[ZAMTYPE_ID_WORDS] = {
        zam_native_argument(thread, 0),
        zam_native_argument(thread, 1),
        zam_native_argument(thread, 2),
    };
   ZamTypeID type = zam_type_id_read(typeWords);
   ZamUint len = (ZamUint)zam_native_argument(thread, 3);
    // Resolve through zam_native_argument_pointer the same way Arrs.Copy does:
    // the run may arrive as a frame location rather than a raw address.
    void *run = zam_native_argument_pointer(thread, 4);
    if (len != 0 && run != NULL) {
        type.arrayLength = len;
        zam_gc_mark_value(thread, (ZamWord)(uintptr_t)run, type);
    }
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

// stdGcMarkable reports whether a value of a type holds anything the
// collector must visit: the test that decides whether a value gets a mark
// entry, so a Mark override can skip walking what holds nothing to mark.
static void stdGcMarkable(ZamThread *thread) {
    ZamWord words[ZAMTYPE_ID_WORDS];
    for (uint64_t i = 0; i < ZAMTYPE_ID_WORDS; i++)
        words[i] = zam_native_argument(thread, i);
    bool *out = (bool *)zam_native_argument_pointer(thread, ZAMTYPE_ID_WORDS);
    *out = zam_type_contains_markable(thread->vm, zam_type_id_read(words));
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

// randState is the calling thread's xoshiro256** state, seeded from the OS on
// the thread's first draw so that no draw takes a lock.
static _Thread_local uint64_t randState[4];
static _Thread_local bool randSeeded;

static uint64_t randRotl(uint64_t x, int k) {
    return (x << k) | (x >> (64 - k));
}

static uint64_t randNext(ZamThread *thread) {
    uint64_t *s = randState;
    if (!randSeeded) {
        if (getentropy(s, sizeof randState) != 0)
            zam_native_panic(thread, "STD_RAND: no entropy available");
        if ((s[0] | s[1] | s[2] | s[3]) == 0)
            s[0] = 1;
        randSeeded = true;
    }
    uint64_t result = randRotl(s[1] * 5, 7) * 9;
    uint64_t t = s[1] << 17;
    s[2] ^= s[0];
    s[3] ^= s[1];
    s[1] ^= s[2];
    s[0] ^= s[3];
    s[2] ^= t;
    s[3] = randRotl(s[3], 45);
    return result;
}

// stdRandUint writes a uniformly random uint.
static void stdRandUint(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    *out = (ZamWord)randNext(thread);
    zam_native_return_void(thread);
}

// stdRandFlt writes a uniformly random flt in [0, 1), one of the 2^53 evenly
// spaced values a double holds there.
static void stdRandFlt(ZamThread *thread) {
    double *out = (double *)zam_native_argument_pointer(thread, 0);
    *out = (double)(randNext(thread) >> 11) * 0x1.0p-53;
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
    bool *out = (bool *)zam_native_argument_pointer(thread, 1);
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
    bool *out = (bool *)zam_native_argument_pointer(thread, 1);
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

// The sync natives. A Mutex, RWMutex or Cond holds its pthread object as a
// handle word: the address of a malloc'd lock, which is never moved and never
// on the heap the collector walks. Every lock is tried first, and only one that
// is contended is waited on from a safe region, so an uncontended lock costs
// nothing beyond the pthread call.

// syncHandle reads the handle argument at wordIndex, which a deleted lock
// leaves zero.
static void *syncHandle(ZamThread *thread, uint64_t wordIndex,
                        const char *name) {
    void *handle = (void *)(uintptr_t)zam_native_argument(thread, wordIndex);
    if (handle == NULL)
        zam_native_panic(thread, name);
    return handle;
}

// stdMutexInit writes the handle of a new error-checking mutex, so that a
// thread locking a mutex it holds, or unlocking one it does not, panics rather
// than deadlocking or corrupting it.
static void stdMutexInit(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    pthread_mutex_t *mutex = zam_alloc(sizeof *mutex);
    pthread_mutexattr_t attr;
    pthread_mutexattr_init(&attr);
    pthread_mutexattr_settype(&attr, PTHREAD_MUTEX_ERRORCHECK);
    if (pthread_mutex_init(mutex, &attr) != 0)
        zam_native_panic(thread, "STD_MUTEX_INIT: pthread_mutex_init failed");
    pthread_mutexattr_destroy(&attr);
    *out = (ZamWord)(uintptr_t)mutex;
    zam_native_return_void(thread);
}

static void stdMutexLock(ZamThread *thread) {
    pthread_mutex_t *mutex =
        syncHandle(thread, 0, "Mutex.Lock: the mutex was deleted");
    int err = pthread_mutex_trylock(mutex);
    if (err == EBUSY) {
        zam_native_block_begin(thread);
        err = pthread_mutex_lock(mutex);
        zam_native_block_end(thread);
    }
    if (err == EDEADLK)
        zam_native_panic(thread, "Mutex.Lock: the mutex is already held by "
                                 "this thread");
    if (err != 0)
        zam_native_panic(thread, "Mutex.Lock: pthread_mutex_lock failed");
    zam_native_return_void(thread);
}

static void stdMutexTryLock(ZamThread *thread) {
    pthread_mutex_t *mutex =
        syncHandle(thread, 0, "Mutex.TryLock: the mutex was deleted");
    bool *out = (bool *)zam_native_argument_pointer(thread, 1);
    int err = pthread_mutex_trylock(mutex);
    if (err != 0 && err != EBUSY)
        zam_native_panic(thread, "Mutex.TryLock: pthread_mutex_trylock failed");
    *out = err == 0;
    zam_native_return_void(thread);
}

static void stdMutexUnlock(ZamThread *thread) {
    pthread_mutex_t *mutex =
        syncHandle(thread, 0, "Mutex.Unlock: the mutex was deleted");
    if (pthread_mutex_unlock(mutex) != 0)
        zam_native_panic(thread, "Mutex.Unlock: the mutex is not held by this "
                                 "thread");
    zam_native_return_void(thread);
}

static void stdMutexDelete(ZamThread *thread) {
    pthread_mutex_t *mutex = (void *)(uintptr_t)zam_native_argument(thread, 0);
    if (mutex != NULL) {
        pthread_mutex_destroy(mutex);
        free(mutex);
    }
    zam_native_return_void(thread);
}

static void stdRWMutexInit(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    pthread_rwlock_t *lock = zam_alloc(sizeof *lock);
    if (pthread_rwlock_init(lock, NULL) != 0)
        zam_native_panic(thread, "STD_RWMUTEX_INIT: pthread_rwlock_init failed");
    *out = (ZamWord)(uintptr_t)lock;
    zam_native_return_void(thread);
}

// rwLock takes lock for reading or writing, waiting from a safe region only if
// it is contended.
static void rwLock(ZamThread *thread, bool write) {
    pthread_rwlock_t *lock =
        syncHandle(thread, 0, "RWMutex: the mutex was deleted");
    int err = write ? pthread_rwlock_trywrlock(lock)
                    : pthread_rwlock_tryrdlock(lock);
    if (err == EBUSY) {
        zam_native_block_begin(thread);
        err = write ? pthread_rwlock_wrlock(lock) : pthread_rwlock_rdlock(lock);
        zam_native_block_end(thread);
    }
    if (err == EDEADLK)
        zam_native_panic(thread, "RWMutex: the mutex is already held by this "
                                 "thread");
    if (err != 0)
        zam_native_panic(thread, "RWMutex: locking failed");
    zam_native_return_void(thread);
}

static void stdRWMutexRLock(ZamThread *thread) { rwLock(thread, false); }

static void stdRWMutexLock(ZamThread *thread) { rwLock(thread, true); }

static void stdRWMutexUnlock(ZamThread *thread) {
    pthread_rwlock_t *lock =
        syncHandle(thread, 0, "RWMutex.Unlock: the mutex was deleted");
    if (pthread_rwlock_unlock(lock) != 0)
        zam_native_panic(thread, "RWMutex: the mutex is not held by this "
                                 "thread");
    zam_native_return_void(thread);
}

static void stdRWMutexDelete(ZamThread *thread) {
    pthread_rwlock_t *lock = (void *)(uintptr_t)zam_native_argument(thread, 0);
    if (lock != NULL) {
        pthread_rwlock_destroy(lock);
        free(lock);
    }
    zam_native_return_void(thread);
}

static void stdCondInit(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    pthread_cond_t *cond = zam_alloc(sizeof *cond);
    if (pthread_cond_init(cond, NULL) != 0)
        zam_native_panic(thread, "STD_COND_INIT: pthread_cond_init failed");
    *out = (ZamWord)(uintptr_t)cond;
    zam_native_return_void(thread);
}

// stdCondWait releases the mutex, waits from a safe region for a signal, and
// takes the mutex back before leaving it.
static void stdCondWait(ZamThread *thread) {
    pthread_cond_t *cond =
        syncHandle(thread, 0, "Cond.Wait: the condition was deleted");
    pthread_mutex_t *mutex =
        syncHandle(thread, 1, "Cond.Wait: the mutex was deleted");
    zam_native_block_begin(thread);
    int err = pthread_cond_wait(cond, mutex);
    zam_native_block_end(thread);
    if (err != 0)
        zam_native_panic(thread, "Cond.Wait: the mutex is not held by this "
                                 "thread");
    zam_native_return_void(thread);
}

static void stdCondSignal(ZamThread *thread) {
    pthread_cond_signal(
        syncHandle(thread, 0, "Cond.Signal: the condition was deleted"));
    zam_native_return_void(thread);
}

static void stdCondBroadcast(ZamThread *thread) {
    pthread_cond_broadcast(
        syncHandle(thread, 0, "Cond.Broadcast: the condition was deleted"));
    zam_native_return_void(thread);
}

static void stdCondDelete(ZamThread *thread) {
    pthread_cond_t *cond = (void *)(uintptr_t)zam_native_argument(thread, 0);
    if (cond != NULL) {
        pthread_cond_destroy(cond);
        free(cond);
    }
    zam_native_return_void(thread);
}

// The math natives. A flt crosses the ABI as the bits of a double in one word,
// and each result is written through the out pointer that follows the
// arguments.

static double fltArgument(ZamThread *thread, uint64_t wordIndex) {
    uint64_t word = zam_native_argument(thread, wordIndex);
    double value;
    memcpy(&value, &word, sizeof value);
    return value;
}

#define MATH_UNARY(name, fn)                                                  \
    static void name(ZamThread *thread) {                                     \
        double *out = (double *)zam_native_argument_pointer(thread, 1);       \
        *out = fn(fltArgument(thread, 0));                                    \
        zam_native_return_void(thread);                                       \
    }

#define MATH_BINARY(name, fn)                                                 \
    static void name(ZamThread *thread) {                                     \
        double *out = (double *)zam_native_argument_pointer(thread, 2);       \
        *out = fn(fltArgument(thread, 0), fltArgument(thread, 1));            \
        zam_native_return_void(thread);                                       \
    }

MATH_UNARY(stdMathSqrt, sqrt)
MATH_UNARY(stdMathCbrt, cbrt)
MATH_UNARY(stdMathExp, exp)
MATH_UNARY(stdMathExp2, exp2)
MATH_UNARY(stdMathExpm1, expm1)
MATH_UNARY(stdMathLog, log)
MATH_UNARY(stdMathLog2, log2)
MATH_UNARY(stdMathLog10, log10)
MATH_UNARY(stdMathLog1p, log1p)
MATH_UNARY(stdMathFloor, floor)
MATH_UNARY(stdMathCeil, ceil)
MATH_UNARY(stdMathRound, round)
MATH_UNARY(stdMathTrunc, trunc)
MATH_UNARY(stdMathSin, sin)
MATH_UNARY(stdMathCos, cos)
MATH_UNARY(stdMathTan, tan)
MATH_UNARY(stdMathAsin, asin)
MATH_UNARY(stdMathAcos, acos)
MATH_UNARY(stdMathAtan, atan)
MATH_UNARY(stdMathSinh, sinh)
MATH_UNARY(stdMathCosh, cosh)
MATH_UNARY(stdMathTanh, tanh)
MATH_UNARY(stdMathAsinh, asinh)
MATH_UNARY(stdMathAcosh, acosh)
MATH_UNARY(stdMathAtanh, atanh)
MATH_BINARY(stdMathPow, pow)
MATH_BINARY(stdMathMod, fmod)
MATH_BINARY(stdMathRemainder, remainder)
MATH_BINARY(stdMathHypot, hypot)
MATH_BINARY(stdMathAtan2, atan2)

static void stdMathFma(ZamThread *thread) {
    double *out = (double *)zam_native_argument_pointer(thread, 3);
    *out = fma(fltArgument(thread, 0), fltArgument(thread, 1),
               fltArgument(thread, 2));
    zam_native_return_void(thread);
}

// stdBitsCopy writes its argument word through the out pointer unchanged,
// which reinterprets a flt as its bits or bits as a flt depending on the
// signature it was called under.
static void stdBitsCopy(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 1);
    *out = zam_native_argument(thread, 0);
    zam_native_return_void(thread);
}

static void stdBitsOnes(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 1);
    *out = (ZamWord)__builtin_popcountll(zam_native_argument(thread, 0));
    zam_native_return_void(thread);
}

static void stdBitsLeadingZeros(ZamThread *thread) {
    uint64_t x = zam_native_argument(thread, 0);
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 1);
    *out = x == 0 ? 64 : (ZamWord)__builtin_clzll(x);
    zam_native_return_void(thread);
}

static void stdBitsTrailingZeros(ZamThread *thread) {
    uint64_t x = zam_native_argument(thread, 0);
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 1);
    *out = x == 0 ? 64 : (ZamWord)__builtin_ctzll(x);
    zam_native_return_void(thread);
}

static void stdBitsReverseBytes(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 1);
    *out = (ZamWord)__builtin_bswap64(zam_native_argument(thread, 0));
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
    {"STD_FILE_SEEK", stdFileSeek},
    {"STD_FILE_SIZE", stdFileSize},
    {"STD_FILE_SYNC", stdFileSync},
    {"STD_FS_STAT", stdFsStat},
    {"STD_FS_REMOVE", stdFsRemove},
    {"STD_FS_RENAME", stdFsRename},
    {"STD_FS_MAKE_DIR", stdFsMakeDir},
    {"STD_FS_CHMOD", stdFsChmod},
    {"STD_FS_CHDIR", stdFsChdir},
    {"STD_FS_READ_DIR", stdFsReadDir},
    {"STD_FS_CWD", stdFsCwd},
    {"STD_FS_TEMP_DIR", stdFsTempDir},
    {"STD_PIPE_INIT", stdPipeInit},
    {"STD_STRS_FROM_BYTES", stdStrsFromBytes},
    {"STD_STRS_TO_CHARS", stdStrsToChars},
    {"STD_STRS_CHAR_LEN", stdStrsCharLen},
    {"STD_STRS_BYTE_LEN", stdStrsByteLen},
    {"STD_ARRS_COPY", stdArrsCopy},
    {"STD_ARRS_MARK_ALL", stdArrsMarkAll},
    {"STD_GC_BLOCK", stdGcBlock},
    {"STD_GC_UNBLOCK", stdGcUnblock},
    {"STD_GC_MARKABLE", stdGcMarkable},
    {"STD_SYSTEM_EXIT", stdSystemExit},
    {"STD_SYSTEM_PROCESS_ID", stdSystemProcessID},
    {"STD_SYSTEM_THREAD_ID", stdSystemThreadID},
    {"STD_RAND_UINT", stdRandUint},
    {"STD_RAND_FLT", stdRandFlt},
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
    {"STD_MUTEX_INIT", stdMutexInit},
    {"STD_MUTEX_LOCK", stdMutexLock},
    {"STD_MUTEX_TRY_LOCK", stdMutexTryLock},
    {"STD_MUTEX_UNLOCK", stdMutexUnlock},
    {"STD_MUTEX_DELETE", stdMutexDelete},
    {"STD_RWMUTEX_INIT", stdRWMutexInit},
    {"STD_RWMUTEX_RLOCK", stdRWMutexRLock},
    {"STD_RWMUTEX_LOCK", stdRWMutexLock},
    {"STD_RWMUTEX_UNLOCK", stdRWMutexUnlock},
    {"STD_RWMUTEX_DELETE", stdRWMutexDelete},
    {"STD_COND_INIT", stdCondInit},
    {"STD_COND_WAIT", stdCondWait},
    {"STD_COND_SIGNAL", stdCondSignal},
    {"STD_COND_BROADCAST", stdCondBroadcast},
    {"STD_COND_DELETE", stdCondDelete},
    {"STD_MATH_SQRT", stdMathSqrt},
    {"STD_MATH_CBRT", stdMathCbrt},
    {"STD_MATH_EXP", stdMathExp},
    {"STD_MATH_EXP2", stdMathExp2},
    {"STD_MATH_EXPM1", stdMathExpm1},
    {"STD_MATH_LOG", stdMathLog},
    {"STD_MATH_LOG2", stdMathLog2},
    {"STD_MATH_LOG10", stdMathLog10},
    {"STD_MATH_LOG1P", stdMathLog1p},
    {"STD_MATH_FLOOR", stdMathFloor},
    {"STD_MATH_CEIL", stdMathCeil},
    {"STD_MATH_ROUND", stdMathRound},
    {"STD_MATH_TRUNC", stdMathTrunc},
    {"STD_MATH_SIN", stdMathSin},
    {"STD_MATH_COS", stdMathCos},
    {"STD_MATH_TAN", stdMathTan},
    {"STD_MATH_ASIN", stdMathAsin},
    {"STD_MATH_ACOS", stdMathAcos},
    {"STD_MATH_ATAN", stdMathAtan},
    {"STD_MATH_SINH", stdMathSinh},
    {"STD_MATH_COSH", stdMathCosh},
    {"STD_MATH_TANH", stdMathTanh},
    {"STD_MATH_ASINH", stdMathAsinh},
    {"STD_MATH_ACOSH", stdMathAcosh},
    {"STD_MATH_ATANH", stdMathAtanh},
    {"STD_MATH_POW", stdMathPow},
    {"STD_MATH_MOD", stdMathMod},
    {"STD_MATH_REMAINDER", stdMathRemainder},
    {"STD_MATH_HYPOT", stdMathHypot},
    {"STD_MATH_ATAN2", stdMathAtan2},
    {"STD_MATH_FMA", stdMathFma},
    {"STD_BITS_FROM_FLT", stdBitsCopy},
    {"STD_BITS_TO_FLT", stdBitsCopy},
    {"STD_BITS_ONES", stdBitsOnes},
    {"STD_BITS_LEADING_ZEROS", stdBitsLeadingZeros},
    {"STD_BITS_TRAILING_ZEROS", stdBitsTrailingZeros},
    {"STD_BITS_REVERSE_BYTES", stdBitsReverseBytes},
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

// The codec natives live beside the libraries they wrap, each file with a
// table of its own.
extern const ZamNativeEntry zamJsonNativeTable[];
extern const uint64_t zamJsonNativeTableCount;
extern const ZamNativeEntry zamYamlNativeTable[];
extern const uint64_t zamYamlNativeTableCount;
extern const ZamNativeEntry zamXmlNativeTable[];
extern const uint64_t zamXmlNativeTableCount;
extern const ZamNativeEntry zamBsonNativeTable[];
extern const uint64_t zamBsonNativeTableCount;
extern const ZamNativeEntry zamCompressNativeTable[];
extern const uint64_t zamCompressNativeTableCount;
extern const ZamNativeEntry zamCborNativeTable[];
extern const uint64_t zamCborNativeTableCount;
extern const ZamNativeEntry zamArchiveNativeTable[];
extern const uint64_t zamArchiveNativeTableCount;
extern const ZamNativeEntry zamNetNativeTable[];
extern const uint64_t zamNetNativeTableCount;
extern const ZamNativeEntry zamTlsNativeTable[];
extern const uint64_t zamTlsNativeTableCount;
extern const ZamNativeEntry zamCurlNativeTable[];
extern const uint64_t zamCurlNativeTableCount;
extern const ZamNativeEntry zamHttpNativeTable[];
extern const uint64_t zamHttpNativeTableCount;
extern const ZamNativeEntry zamWsNativeTable[];
extern const uint64_t zamWsNativeTableCount;
extern const ZamNativeEntry zamCryptoNativeTable[];
extern const uint64_t zamCryptoNativeTableCount;
extern const ZamNativeEntry zamPkiNativeTable[];
extern const uint64_t zamPkiNativeTableCount;
extern const ZamNativeEntry zamTimeNativeTable[];
extern const uint64_t zamTimeNativeTableCount;

// stdTables are the standard library's tables, searched in order.
static const struct {
    const ZamNativeEntry *table;
    const uint64_t *count;
} stdTables[] = {
    {zamNativeTable, &zamNativeTableCount},
    {zamJsonNativeTable, &zamJsonNativeTableCount},
    {zamYamlNativeTable, &zamYamlNativeTableCount},
    {zamXmlNativeTable, &zamXmlNativeTableCount},
    {zamBsonNativeTable, &zamBsonNativeTableCount},
    {zamCompressNativeTable, &zamCompressNativeTableCount},
    {zamCborNativeTable, &zamCborNativeTableCount},
    {zamArchiveNativeTable, &zamArchiveNativeTableCount},
    {zamNetNativeTable, &zamNetNativeTableCount},
    {zamTlsNativeTable, &zamTlsNativeTableCount},
    {zamCurlNativeTable, &zamCurlNativeTableCount},
    {zamHttpNativeTable, &zamHttpNativeTableCount},
    {zamWsNativeTable, &zamWsNativeTableCount},
    {zamCryptoNativeTable, &zamCryptoNativeTableCount},
    {zamPkiNativeTable, &zamPkiNativeTableCount},
    {zamTimeNativeTable, &zamTimeNativeTableCount},
};

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
       ZamNativeFn fn = NULL;
        for (uint64_t t = 0; fn == NULL && t < sizeof stdTables / sizeof stdTables[0]; t++)
            fn = matchNative(stdTables[t].table, *stdTables[t].count, name,
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
