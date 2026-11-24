#include "sockets.h"
#include "zam/native.h"

#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

#if defined(_WIN32)
#include <direct.h>
#include <io.h>
#include <windows.h>
#include <bcrypt.h>
#else
#include <sys/random.h>
#endif

#include "definition.h"
#include "gc.h"
#include "posit.h"
#include "run.h"
#include "thread.h"
#include "vm.h"
#include "zamrt.h"


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
// Object's own Delete, so a user's own Delete can decide whether to run it,
// delay it, or do something else first.
//
// Null is treated as a programming error even though delete has already
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

   ZamMarkWord mark = zam_mark_load(pointer);
    if (mark.contained && pointer == thread->finalizing) {
        zam_native_return_void(thread);
        return;
    }
    if (mark.contained)
        zam_native_panic(thread,
                         mark.upvalue != 0
                             ? "deleted a member of another object"
                             : "deleted a value with no allocation");
    zam_mark_kill(pointer);
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

// socketErrorMessage is errorMessage for a socket error as zam_socket_error
// answers it.
static ZamWord socketErrorMessage(ZamThread *thread, int err) {
    const char *msg = zam_socket_strerror(err);
    return (ZamWord)zam_native_new_string(thread, msg, strlen(msg));
}

// stringArgument is the zam string at wordIndex, panicking when it is null.
static const ZamString *stringArgument(ZamThread *thread, uint64_t wordIndex) {
    ZamWord word = zam_native_argument(thread, wordIndex);
    if (word == 0)
        zam_native_panic(thread, "handed a native a null string");
    return zam_string_of((void *)(uintptr_t)word);
}

static void stdFileInit(ZamThread *thread) {
    const ZamString *path = stringArgument(thread, 0);
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
#if defined(_WIN32)
    // Windows opens a file to translate line endings unless told otherwise.
    mode |= O_BINARY;
#endif
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
    const ZamString *s = stringArgument(thread, wordIndex);
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
#if defined(_WIN32)
    int result = _commit(fd);
#else
    int result = fsync(fd);
#endif
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

#if defined(_WIN32)
// windowsErrno is the errno that says what a Windows error code does.
static int windowsErrno(DWORD error) {
    switch (error) {
    case ERROR_FILE_NOT_FOUND:
    case ERROR_PATH_NOT_FOUND:
        return ENOENT;
    case ERROR_ACCESS_DENIED:
    case ERROR_SHARING_VIOLATION:
        return EACCES;
    case ERROR_ALREADY_EXISTS:
    case ERROR_FILE_EXISTS:
        return EEXIST;
    case ERROR_NOT_SAME_DEVICE:
        return EXDEV;
    case ERROR_DIR_NOT_EMPTY:
        return ENOTEMPTY;
    default:
        return EIO;
    }
}
#endif

static void stdFsRemove(ZamThread *thread) {
    char *path = cString(thread, 0);
    zam_native_block_begin(thread);
#if defined(_WIN32)
    // Windows' remove takes only files; POSIX's takes empty directories too.
    struct _stat st;
    int result = _stat(path, &st) == 0 && (st.st_mode & _S_IFDIR) ? _rmdir(path) : remove(path);
#else
    int result = remove(path);
#endif
    int err = errno;
    zam_native_block_end(thread);
    free(path);
    finishErr(thread, 1, result, err);
}

static void stdFsRename(ZamThread *thread) {
    char *from = cString(thread, 0);
    char *to = cString(thread, 1);
    zam_native_block_begin(thread);
#if defined(_WIN32)
    // Windows' rename refuses an existing destination, which POSIX's replaces.
    int result = 0;
    if (!MoveFileExA(from, to, MOVEFILE_REPLACE_EXISTING)) {
        result = -1;
        errno = windowsErrno(GetLastError());
    }
#else
    int result = rename(from, to);
#endif
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
#if defined(_WIN32)
    // A Windows directory has no permission bits to set.
    (void)mode;
    int result = _mkdir(path);
#else
    int result = mkdir(path, mode);
#endif
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
        if (count > 0)
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

#if defined(_WIN32)
#define PATH_SEPARATOR "\\"
#else
#define PATH_SEPARATOR "/"
#endif

static void stdFsSeparator(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    *out = (ZamWord)zam_native_new_string(thread, PATH_SEPARATOR, 1);
    zam_native_return_void(thread);
}

// stdFsTempDir answers the directory for temporary files without a trailing
// separator: on Windows the one GetTempPath names, elsewhere $TMPDIR, or /tmp
// when it is unset.
static void stdFsTempDir(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
#if defined(_WIN32)
    char path[MAX_PATH + 1];
    DWORD got = GetTempPathA(sizeof path, path);
    const char *dir = got > 0 && got < sizeof path ? path : "C:\\Windows\\Temp";
#else
    const char *dir = getenv("TMPDIR");
    if (dir == NULL || dir[0] == '\0')
        dir = "/tmp";
#endif
    size_t length = strlen(dir);
    while (length > 1 && (dir[length - 1] == '/' || dir[length - 1] == PATH_SEPARATOR[0]) &&
           dir[length - 2] != ':')
        length--;
    *out = (ZamWord)zam_native_new_string(thread, dir, length);
    zam_native_return_void(thread);
}

static void stdPipeInit(ZamThread *thread) {
    ZamInt *readOut = (ZamInt *)zam_native_argument_pointer(thread, 0);
    ZamInt *writeOut = (ZamInt *)zam_native_argument_pointer(thread, 1);
    int fds[2];
#if defined(_WIN32)
    int result = _pipe(fds, 64 * 1024, O_BINARY);
#else
    int result = pipe(fds);
#endif
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
   ZamUint *nOut = (ZamUint *)zam_native_argument_pointer(thread, 3);
   ZamWord *errOut = (ZamWord *)zam_native_argument_pointer(thread, 4);

    int fd = (int)*fdPtr;
    zam_native_block_begin(thread);
    ssize_t written;
    int writeErrno;
    if (zam_fd_is_socket(fd)) {
        written = send(zam_fd_socket(fd), buffer, (int)count, 0);
        writeErrno = zam_socket_error();
    } else {
        written = write(fd, buffer, count);
        writeErrno = errno;
    }
    zam_native_block_end(thread);
    if (written < 0) {
        *nOut = 0;
        *errOut = zam_fd_is_socket(fd) ? socketErrorMessage(thread, writeErrno) : errorMessage(thread, writeErrno);
    } else {
        *nOut = (ZamUint)written;
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
    bool socket = zam_fd_is_socket(fd);
    int closed = socket ? zam_socket_close(fd) : close(fd);
    int closeErrno = socket ? zam_socket_error() : errno;
    zam_native_block_end(thread);
    if (closed < 0)
        *errOut = socket ? socketErrorMessage(thread, closeErrno) : errorMessage(thread, closeErrno);
    else
        *errOut = 0;
    zam_native_return_void(thread);
}

static void stdFileRead(ZamThread *thread) {
   ZamInt *fdPtr = (ZamInt *)zam_native_argument_pointer(thread, 0);
    void *buffer = zam_native_argument_pointer(thread, 1);
    size_t count = (size_t)zam_native_argument(thread, 2);
   ZamUint *nOut = (ZamUint *)zam_native_argument_pointer(thread, 3);
   ZamWord *errOut = (ZamWord *)zam_native_argument_pointer(thread, 4);

    int fd = (int)*fdPtr;
    zam_native_block_begin(thread);
    ssize_t bytesRead;
    int readErrno;
    if (zam_fd_is_socket(fd)) {
        bytesRead = recv(zam_fd_socket(fd), buffer, (int)count, 0);
        readErrno = zam_socket_error();
    } else {
        bytesRead = read(fd, buffer, count);
        readErrno = errno;
    }
    zam_native_block_end(thread);
    if (bytesRead < 0) {
        *nOut = 0;
        *errOut = zam_fd_is_socket(fd) ? socketErrorMessage(thread, readErrno) : errorMessage(thread, readErrno);
    } else {
        *nOut = (ZamUint)bytesRead;
        *errOut = 0;
    }

    zam_native_return_void(thread);
}

// The std.Strs natives. FromBytes allocates a fresh string and copies, and
// ByteLen just reads the length word; the rest of Strs is zam.
static void stdStrsFromBytes(ZamThread *thread) {
    const char *bytes =
        (const char *)zam_native_argument_pointer(thread, 0);
    ZamUint length = (ZamUint)zam_native_argument(thread, 1);
   ZamWord *strOut = (ZamWord *)zam_native_argument_pointer(thread, 2);
    if (bytes == NULL && length > 0)
        zam_native_panic(thread, "Strs.FromBytes: null bytes");
    *strOut = zam_native_new_string(thread, bytes, length);
    zam_native_return_void(thread);
}

static void stdStrsByteLen(ZamThread *thread) {
    const ZamString *s = stringArgument(thread, 0);
   ZamUint *nOut = (ZamUint *)zam_native_argument_pointer(thread, 1);
    *nOut = s->length;
    zam_native_return_void(thread);
}

// stdStrsParseFlt reads the whole of a str as a decimal or special float,
// leaving ok false, and the float zero, when any of it is not one or the value
// does not fit.
static void stdStrsParseFlt(ZamThread *thread) {
    const ZamString *s = stringArgument(thread, 0);
    double *out = (double *)zam_native_argument_pointer(thread, 1);
    bool *ok = (bool *)zam_native_argument_pointer(thread, 2);
    *out = 0;
    *ok = false;
    if (s->length == 0 || strlen(s->bytes) != s->length ||
        (unsigned char)s->bytes[0] <= ' ') {
        zam_native_return_void(thread);
        return;
    }
    char *end = NULL;
    errno = 0;
    double value = strtod(s->bytes, &end);
    if (end == s->bytes + s->length && errno != ERANGE) {
        *out = value;
        *ok = true;
    }
    zam_native_return_void(thread);
}

// stdArrsCopy moves a run of A elements from src to dst. The element type
// rode across as its three-word id so the width is read off the image here:
// a template is not one width, and baking a size for one instantiation would
// be wrong for every other. A length with its sign bit set was a negative int
// cast to uint. memmove rather than memcpy, so an overlapping range inside
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
    uint64_t bytes;
    if (len > INT64_MAX)
        zam_native_panic(thread, "Arrs.Copy: negative length");
    if (__builtin_mul_overflow(len, stride, &bytes))
        zam_native_panic(thread, "Arrs.Copy: length too large");
    if (bytes > 0 && (src == NULL || dst == NULL))
        zam_native_panic(thread, "Arrs.Copy: null run");
    memmove(dst, src, bytes);
    zam_native_write_barrier(thread, dst, bytes);
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

// stdGcMinor and stdGcMajor ask for a collection of that kind, which the
// calling thread runs at its next safepoint, unless another thread reaches one first. A minor collection asked for is
// major all the same if the heap has already grown to its threshold.
static void stdGcMinor(ZamThread *thread) {
    atomic_store(&thread->vm->collectionRequested, true);
    zam_native_return_void(thread);
}

static void stdGcMajor(ZamThread *thread) {
    atomic_store(&thread->vm->majorRequested, true);
    atomic_store(&thread->vm->collectionRequested, true);
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
// program's own return path: no frames are unwound, no destructors run,
// no mark sweep runs. The caller asked for the program to stop and this is
// what stops it. The native does not return; zam_native_return_void is
// unreachable past exit() and omitting it does not matter.
static void stdSystemExit(ZamThread *thread) {
    (void)thread;
    int code = (int)zam_native_argument(thread, 0);
    exit(code);
}

// stdSystemTrace writes where the calling thread is to stderr, from the frame
// that called the method calling this native.
static void stdSystemTrace(ZamThread *thread) {
    fflush(stderr);
    zam_thread_trace(thread, 1, fileno(stderr));
    zam_native_return_void(thread);
}

// stdSystemFailed is called as the calling thread fails with a message, as a
// panic or a failed check does. It does nothing; it is where a debugger breaks
// to stop on failures.
static void stdSystemFailed(ZamThread *thread) {
    zam_native_return_void(thread);
}

// stdSystemPanic raises a panic saying its message, unwinding the calling
// thread to the defers it is inside of.
static void stdSystemPanic(ZamThread *thread) {
    ZamWord word = zam_native_argument(thread, 0);
    const ZamString *s = word == 0 ? NULL : zam_string_of((void *)(uintptr_t)word);
    zamrt_raise(thread, (uint8_t)-1, false, s == NULL ? "" : s->bytes, s == NULL ? 0 : s->length, 0);
}

// stdSystemRecover stops the panic the method calling the method calling this
// native is running its defers for, and answers its message, or leaves null
// when there is none.
static void stdSystemRecover(ZamThread *thread) {
    const ZamFrame *frames = thread->frames;
    ZamPanicking *panic = frames == NULL ? NULL : zamrt_recover(thread, frames->caller);
    if (panic != NULL) {
        ZamWord message = zam_native_new_string(thread, panic->message, panic->length);
        *(ZamWord *)zam_native_argument_pointer(thread, 0) = message;
        free(panic->message);
        free(panic);
    }
    zam_native_return_void(thread);
}

// stdSystemCaller reports the file and line of the frame skip frames out from
// the one that called the method calling this native, and whether there is
// one.
static void stdSystemCaller(ZamThread *thread) {
    uint64_t skip = zam_native_argument(thread, 0) + 1;
    const char *file = NULL;
    uint32_t line = 0;
    bool found = zam_thread_compiled_caller(thread, skip, &file, &line);
    if (found) {
        ZamWord string = (ZamWord)zam_native_new_string(thread, file, strlen(file));
        *(ZamWord *)zam_native_argument_pointer(thread, 1) = string;
        *(ZamWord *)zam_native_argument_pointer(thread, 2) = line;
    }
    *(bool *)zam_native_argument_pointer(thread, 3) = found;
    zam_native_return_void(thread);
}

// stdSystemProcessID reports getpid(), the id Signal.Send hands to kill().
static void stdSystemProcessID(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    *out = (ZamWord)getpid();
    zam_native_return_void(thread);
}

// stdSystemProcessorCount reports how many processors the system has online.
static void stdSystemProcessorCount(ZamThread *thread) {
    ZamWord *out = (ZamWord *)zam_native_argument_pointer(thread, 0);
    *out = (ZamWord)zam_processor_count();
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

// entropy fills buffer with byteCount random bytes from the system, answering
// whether it could.
static bool entropy(void *buffer, size_t byteCount) {
#if defined(_WIN32)
    return BCryptGenRandom(NULL, buffer, (ULONG)byteCount, BCRYPT_USE_SYSTEM_PREFERRED_RNG) == 0;
#else
    return getentropy(buffer, byteCount) == 0;
#endif
}

static uint64_t randNext(ZamThread *thread) {
    uint64_t *s = randState;
    if (!randSeeded) {
        if (!entropy(s, sizeof randState))
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

// stdRandPst writes a uniformly random pst in [0, 1): 64 random bits read as a
// fraction, rounded to the nearest pst below 1.
static void stdRandPst(ZamThread *thread) {
    ZamPosit *out = (ZamPosit *)zam_native_argument_pointer(thread, 0);
    ZamPosit posit = zam_posit_from_fraction(randNext(thread));
    *out = posit == ZAMPOSIT_ONE ? ZAMPOSIT_ONE - 1 : posit;
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
#if defined(_WIN32)
static void stdSignalSend(ZamThread *thread) {
    zam_native_panic(thread, "sending a signal to another process is not supported on Windows");
}
#else
static void stdSignalSend(ZamThread *thread) {
    int sig = (int)zam_native_argument(thread, 0);
    pid_t pid = (pid_t)zam_native_argument(thread, 1);
    (void)kill(pid, sig);
    zam_native_return_void(thread);
}
#endif

// stdSignalInit maps a Signal enum ordinal to the OS-level signal number for
// the running platform. The index set is this native's contract with
// System.Signal — adding a signal to the enum means adding a case here, and
// a mismatch would hand the kernel a number the enum name does not stand
// for. Keep the switch in step with the signalNumber(N) calls in System.zam.
static void stdSignalInit(ZamThread *thread) {
    int idx = (int)zam_native_argument(thread, 0);
    ZamInt *out = (ZamInt *)zam_native_argument_pointer(thread, 1);
    int sig;
#if defined(_WIN32)
    // Windows has six of the signals; the rest are -1, which is never raised
    // or handled.
    switch (idx) {
    case 1:  sig = SIGINT;  break;
    case 3:  sig = SIGILL;  break;
    case 4:  sig = SIGABRT; break;
    case 5:  sig = SIGFPE;  break;
    case 7:  sig = SIGSEGV; break;
    case 10: sig = SIGTERM; break;
    default:
        if (idx < 0 || idx > 15)
            zam_native_panic(thread, "STD_SIGNAL_INIT: unknown signal index");
        sig = -1;
    }
#else
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
#endif
    *out = (ZamInt)sig;
    zam_native_return_void(thread);
}

#if defined(_WIN32)
// Windows has no signal masks and no signals between threads: each of these
// natives says so.
static void stdSignalUnsupported(ZamThread *thread) {
    zam_native_panic(thread, "signal masks and signals between threads are not supported on Windows");
}
#define stdSignalThreadSend stdSignalUnsupported
#define stdSignalBlock stdSignalUnsupported
#define stdSignalUnblock stdSignalUnsupported
#define stdSignalThreadBlock stdSignalUnsupported
#define stdSignalThreadUnblock stdSignalUnsupported
#define stdSignalIsBlocked stdSignalUnsupported
#define stdSignalIsThreadBlocked stdSignalUnsupported
#else
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
#endif

// Install the zam closure as the handler for sig on the calling thread. The
// native stores the closure in the thread's own handler table and asks the
// OS to deliver future occurrences through the shared C handler. The C
// handler itself only ORs a bit into the current thread's pending mask —
// running the closure directly from signal context would re-enter compiled
// code from a place that cannot safely call anything that is not
// async-signal-safe.
static void stdSignalHandle(ZamThread *thread) {
    uint64_t sig = zam_native_argument(thread, 0);
    ZamWord closure = zam_native_argument(thread, 1);
#if defined(_WIN32)
    // A signal Windows does not have never arrives, so there is nothing to
    // handle.
    if ((ZamInt)sig == -1) {
        zam_native_return_void(thread);
        return;
    }
#endif
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

// The posit math natives, each writing its result through the out pointer
// that follows the arguments.

#define POSIT_UNARY(name, fn)                                                 \
    static void name(ZamThread *thread) {                                     \
        ZamPosit *out = (ZamPosit *)zam_native_argument_pointer(thread, 1);   \
        *out = fn(zam_native_argument(thread, 0));                            \
        zam_native_return_void(thread);                                       \
    }

#define POSIT_BINARY(name, fn)                                                \
    static void name(ZamThread *thread) {                                     \
        ZamPosit *out = (ZamPosit *)zam_native_argument_pointer(thread, 2);   \
        *out = fn(zam_native_argument(thread, 0),                             \
                  zam_native_argument(thread, 1));                            \
        zam_native_return_void(thread);                                       \
    }

POSIT_UNARY(stdPstSqrt, zam_posit_sqrt)
POSIT_UNARY(stdPstCbrt, zam_posit_cbrt)
POSIT_UNARY(stdPstExp, zam_posit_exp)
POSIT_UNARY(stdPstExp2, zam_posit_exp2)
POSIT_UNARY(stdPstExpm1, zam_posit_expm1)
POSIT_UNARY(stdPstLog, zam_posit_log)
POSIT_UNARY(stdPstLog2, zam_posit_log2)
POSIT_UNARY(stdPstLog10, zam_posit_log10)
POSIT_UNARY(stdPstLog1p, zam_posit_log1p)
POSIT_UNARY(stdPstFloor, zam_posit_floor)
POSIT_UNARY(stdPstCeil, zam_posit_ceil)
POSIT_UNARY(stdPstRound, zam_posit_round)
POSIT_UNARY(stdPstTrunc, zam_posit_trunc)
POSIT_UNARY(stdPstSin, zam_posit_sin)
POSIT_UNARY(stdPstCos, zam_posit_cos)
POSIT_UNARY(stdPstTan, zam_posit_tan)
POSIT_UNARY(stdPstAsin, zam_posit_asin)
POSIT_UNARY(stdPstAcos, zam_posit_acos)
POSIT_UNARY(stdPstAtan, zam_posit_atan)
POSIT_UNARY(stdPstSinh, zam_posit_sinh)
POSIT_UNARY(stdPstCosh, zam_posit_cosh)
POSIT_UNARY(stdPstTanh, zam_posit_tanh)
POSIT_UNARY(stdPstAsinh, zam_posit_asinh)
POSIT_UNARY(stdPstAcosh, zam_posit_acosh)
POSIT_UNARY(stdPstAtanh, zam_posit_atanh)
POSIT_BINARY(stdPstPow, zam_posit_pow)
POSIT_BINARY(stdPstMod, zam_posit_mod)
POSIT_BINARY(stdPstRemainder, zam_posit_remainder)
POSIT_BINARY(stdPstHypot, zam_posit_hypot)
POSIT_BINARY(stdPstAtan2, zam_posit_atan2)

static void stdPstFma(ZamThread *thread) {
    ZamPosit *out = (ZamPosit *)zam_native_argument_pointer(thread, 3);
    *out = zam_posit_fma(zam_native_argument(thread, 0),
                         zam_native_argument(thread, 1),
                         zam_native_argument(thread, 2));
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
    {"STD_FS_SEPARATOR", stdFsSeparator},
    {"STD_FS_TEMP_DIR", stdFsTempDir},
    {"STD_PIPE_INIT", stdPipeInit},
    {"STD_STRS_FROM_BYTES", stdStrsFromBytes},
    {"STD_STRS_BYTE_LEN", stdStrsByteLen},
    {"STD_STRS_PARSE_FLT", stdStrsParseFlt},
    {"STD_ARRS_COPY", stdArrsCopy},
    {"STD_ARRS_MARK_ALL", stdArrsMarkAll},
    {"STD_GC_BLOCK", stdGcBlock},
    {"STD_GC_UNBLOCK", stdGcUnblock},
    {"STD_GC_MARKABLE", stdGcMarkable},
    {"STD_GC_MINOR", stdGcMinor},
    {"STD_GC_MAJOR", stdGcMajor},
    {"STD_SYSTEM_EXIT", stdSystemExit},
    {"STD_SYSTEM_TRACE", stdSystemTrace},
    {"STD_SYSTEM_FAILED", stdSystemFailed},
    {"STD_SYSTEM_PANIC", stdSystemPanic},
    {"STD_SYSTEM_RECOVER", stdSystemRecover},
    {"STD_SYSTEM_CALLER", stdSystemCaller},
    {"STD_SYSTEM_PROCESS_ID", stdSystemProcessID},
    {"STD_SYSTEM_PROCESSOR_COUNT", stdSystemProcessorCount},
    {"STD_SYSTEM_THREAD_ID", stdSystemThreadID},
    {"STD_RAND_UINT", stdRandUint},
    {"STD_RAND_FLT", stdRandFlt},
    {"STD_RAND_PST", stdRandPst},
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
    {"STD_PST_SQRT", stdPstSqrt},
    {"STD_PST_CBRT", stdPstCbrt},
    {"STD_PST_EXP", stdPstExp},
    {"STD_PST_EXP2", stdPstExp2},
    {"STD_PST_EXPM1", stdPstExpm1},
    {"STD_PST_LOG", stdPstLog},
    {"STD_PST_LOG2", stdPstLog2},
    {"STD_PST_LOG10", stdPstLog10},
    {"STD_PST_LOG1P", stdPstLog1p},
    {"STD_PST_FLOOR", stdPstFloor},
    {"STD_PST_CEIL", stdPstCeil},
    {"STD_PST_ROUND", stdPstRound},
    {"STD_PST_TRUNC", stdPstTrunc},
    {"STD_PST_SIN", stdPstSin},
    {"STD_PST_COS", stdPstCos},
    {"STD_PST_TAN", stdPstTan},
    {"STD_PST_ASIN", stdPstAsin},
    {"STD_PST_ACOS", stdPstAcos},
    {"STD_PST_ATAN", stdPstAtan},
    {"STD_PST_SINH", stdPstSinh},
    {"STD_PST_COSH", stdPstCosh},
    {"STD_PST_TANH", stdPstTanh},
    {"STD_PST_ASINH", stdPstAsinh},
    {"STD_PST_ACOSH", stdPstAcosh},
    {"STD_PST_ATANH", stdPstAtanh},
    {"STD_PST_POW", stdPstPow},
    {"STD_PST_MOD", stdPstMod},
    {"STD_PST_REMAINDER", stdPstRemainder},
    {"STD_PST_HYPOT", stdPstHypot},
    {"STD_PST_ATAN2", stdPstAtan2},
    {"STD_PST_FMA", stdPstFma},
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
// table of its own. Which of those tables a program can call into is the
// link's to say: a compiled program names only those it calls, and
// stdtables.c names every one for the conformance harnesses.
extern const ZamNativeTables zamStdNativeTables[];
extern const uint64_t zamStdNativeTableCount;

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
    // program starts. A name with nothing to bind to is the end of the
    // run, since the program will certainly call it and there is nothing to
    // call.
    for (uint64_t slot = 0; slot < vm->nativeCount; slot++) {
        uint64_t nameByteCount;
        const char *name = zam_image_native_name(&vm->image, slot, &nameByteCount);

        // The standard library's table is searched first, so a name that
        // appears in both is bound to the runtime's own entry. A project is
        // free to ship a C function whose name begins with STD_ by accident,
        // but it may not shadow one the standard library already answers to.
       ZamNativeFn fn = matchNative(zamNativeTable, zamNativeTableCount, name, nameByteCount);
        for (uint64_t t = 0; fn == NULL && t < zamStdNativeTableCount; t++)
            fn = matchNative(zamStdNativeTables[t].table, *zamStdNativeTables[t].count, name,
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

// A native's frame begins at the function entry zamrt_call_native pushed. The
// base slot holds the native's address, and every slot above it is an argument. Argument indexing counts
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
    void *at = zam_thread_location_address(
        thread, zam_native_argument(thread, wordIndex));
    if (!zam_heap_contains(&thread->vm->heap, at))
        return at;
    zam_heap_dirty(&thread->vm->heap, at, ZAMHEAP_CARD_BYTES);
    if (thread->dirtiedCount == thread->dirtiedCapacity) {
        thread->dirtiedCapacity =
            thread->dirtiedCapacity == 0 ? 8 : thread->dirtiedCapacity * 2;
        thread->dirtied = zam_realloc(
            thread->dirtied, thread->dirtiedCapacity * sizeof *thread->dirtied);
    }
    thread->dirtied[thread->dirtiedCount++] = at;
    return at;
}

void zam_native_write_barrier(ZamThread *thread, const void *at,
                              uint64_t byteCount) {
    zam_heap_dirty(&thread->vm->heap, at, byteCount);
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
    // The thread runs compiled code again once it lands, which a collection
    // has to wait for.
    if (atomic_load_explicit(&thread->state, memory_order_relaxed) == ZAMTHREAD_SAFE)
        zam_vm_leave_safe(thread);
    zamrt_raise(thread, ZAMEXIT_PANIC, true, message, strlen(message), 0);
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
