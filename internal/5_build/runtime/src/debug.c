#include "debug.h"

#include "gc.h"
#include "run.h"
#include "zam/native.h"

#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <poll.h>
#include <signal.h>
#include <stdarg.h>
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

// ZAMDEBUG_FAILURE_BYTES is the most of a failure's message kept.
#define ZAMDEBUG_FAILURE_BYTES 1024

// ZAMDEBUG_FAILURE_NANOSECONDS is how long a failing thread sleeps between
// looks at whether it has been let go.
#define ZAMDEBUG_FAILURE_NANOSECONDS 1000000

// ZAMDEBUG_LOCK_TRIES is how many times, a failure's pause apart, a failing
// thread looks for a lock of the runtime's to come free.
#define ZAMDEBUG_LOCK_TRIES 200

// ZAMDEBUG_POLL_MILLISECONDS is how often the debugger looks to see whether
// every thread has stopped, there being no one event that says so.
#define ZAMDEBUG_POLL_MILLISECONDS 10

#ifndef MSG_NOSIGNAL
#define MSG_NOSIGNAL 0
#endif

struct ZamDebugger {
    ZamVM *vm;
    int fd;

    // active is cleared once the debugger has gone, after which no thread
    // stops.
    atomic_bool active;

    // breakpoints holds a bit for each byte of the operations section, set
    // where an instruction a thread stops before begins.
    _Atomic uint8_t *breakpoints;
    size_t breakpointByteCount;

    // lock guards what follows, and every thread's step. resumed is broadcast
    // each time the program is let go.
    pthread_mutex_t lock;
    pthread_cond_t resumed;

    // pausing is set from when a stop is asked for until the program is let
    // go, and is read without the lock before every instruction. generation
    // counts the times the program has been let go, which is what a held
    // thread waits on.
    atomic_bool pausing;
    _Atomic uint64_t generation;

    // stopped, reason and at are the thread that stopped the program, why,
    // and the instruction it stopped before.
    uint64_t stopped;
    const char *reason;
    uint64_t at;

    // announced is whether the debugger has been told of the stop. Only the
    // debugger's own thread reads or writes it.
    bool announced;

    // stopOnFailure is whether a failing thread stops the program. failing
    // is taken by the first thread to fail, which then fills in the failure
    // and sets failed. Only atomics are used, a failure being raised in a
    // signal handler as often as not. failureAnnounced is whether the
    // debugger has been told, and is the debugger's own thread's.
    atomic_bool stopOnFailure;
    atomic_bool failing;
    atomic_bool failed;
    _Atomic uint64_t failedThread;
    _Atomic uint64_t failedAt;
    char failure[ZAMDEBUG_FAILURE_BYTES];
    bool failureAnnounced;

    // input is what has been read and not yet acted on.
    char *input;
    size_t inputLength;
    size_t inputCapacity;
};

// ZamText is a reply being built.
typedef struct ZamText {
    char *data;
    size_t length;
    size_t capacity;
} ZamText;

static void textAppend(ZamText *text, const char *format, ...) {
    for (;;) {
        va_list arguments;
        va_start(arguments, format);
        size_t room = text->capacity - text->length;
        int n = vsnprintf(text->data == NULL ? NULL : text->data + text->length,
                          room, format, arguments);
        va_end(arguments);
        if (n < 0)
            return;
        if ((size_t)n < room) {
            text->length += (size_t)n;
            return;
        }
        text->capacity = (text->capacity + (size_t)n + 1) * 2;
        text->data = zam_realloc(text->data, text->capacity);
    }
}

// sendText writes text and a newline to the debugger, dropping it if the
// debugger has gone: that is noticed when the next read finds nothing.
static void sendText(ZamDebugger *d, ZamText *text) {
    textAppend(text, "\n");
    for (size_t sent = 0; sent < text->length;) {
        ssize_t n = send(d->fd, text->data + sent, text->length - sent,
                         MSG_NOSIGNAL);
        if (n < 0 && errno == EINTR)
            continue;
        if (n <= 0)
            break;
        sent += (size_t)n;
    }
    free(text->data);
}

// depthOf orders a thread's calls by how deep they are, deeper being greater:
// an entry is the offset its mark lies at, and marks are pushed as calls are
// made.
static uint64_t depthOf(uint64_t entry) {
    return entry == ZAMMARK_NONE ? 0 : entry + 1;
}

static bool breakpointAt(ZamDebugger *d, uint64_t offset) {
    if (offset / 8 >= d->breakpointByteCount)
        return false;
    uint8_t bits = atomic_load_explicit(&d->breakpoints[offset / 8],
                                        memory_order_relaxed);
    return (bits >> (offset % 8)) & 1;
}

// stepDone reports whether thread, about to run the instruction it stands at,
// has finished the step it is taking. An instruction the image places on no
// line ends no step.
static bool stepDone(const ZamThread *thread) {
    const char *file;
    uint32_t line;
    if (!zam_image_locate(&thread->vm->image, thread->instructionOffset, &file,
                          &line))
        return false;
    uint64_t depth = depthOf(thread->functionEntry);
    uint64_t start = thread->debugStepDepth;
    bool moved = line != thread->debugStepLine || file != thread->debugStepFile;
    switch (thread->debugStep) {
    case ZAMDEBUG_STEP_IN:
        return depth != start || moved;
    case ZAMDEBUG_STEP_OVER:
        return depth < start || (depth == start && moved);
    case ZAMDEBUG_STEP_OUT:
        return depth < start;
    default:
        return false;
    }
}

// park holds thread until the program is let go. The caller holds the lock.
static void park(ZamDebugger *d, ZamThread *thread, bool boundary) {
    uint64_t generation = atomic_load(&d->generation);
    thread->debugBoundary = boundary;
    atomic_store(&thread->debugParked, true);
    while (atomic_load(&d->generation) == generation)
        pthread_cond_wait(&d->resumed, &d->lock);
    atomic_store(&thread->debugParked, false);
}

void zam_debug_check(ZamThread *thread) {
    ZamDebugger *d = thread->vm->debugger;
    if (!atomic_load_explicit(&d->active, memory_order_relaxed))
        return;

    // A thread let go at a breakpoint runs the instruction it stopped at
    // rather than stopping there again.
    bool skip = thread->debugSkip;
    thread->debugSkip = false;
    const char *reason = NULL;
    if (!atomic_load_explicit(&d->pausing, memory_order_acquire)) {
        if (!skip && breakpointAt(d, thread->instructionOffset))
            reason = "breakpoint";
        else if (thread->debugStep != ZAMDEBUG_STEP_NONE && stepDone(thread))
            reason = "step";
        else
            return;
    }

    pthread_mutex_lock(&d->lock);
    if (!atomic_load(&d->pausing)) {
        if (reason == NULL) {
            pthread_mutex_unlock(&d->lock);
            return;
        }
        d->stopped = thread->id;
        d->reason = reason;
        d->at = thread->instructionOffset;
        atomic_store(&d->pausing, true);
    }
    park(d, thread, true);
    thread->debugSkip = true;
    pthread_mutex_unlock(&d->lock);
}

void zam_debug_hold(ZamThread *thread) {
    ZamDebugger *d = thread->vm->debugger;
    if (!atomic_load(&d->active) || !atomic_load(&d->pausing))
        return;
    pthread_mutex_lock(&d->lock);
    if (atomic_load(&d->pausing))
        park(d, thread, false);
    pthread_mutex_unlock(&d->lock);
}

// runtimeUnlocked reports whether every lock of the runtime's own comes free
// within a moment, which one held by another thread does. One that does not
// is held by the caller, and a thread held while holding it would keep every
// other from stopping.
static bool runtimeUnlocked(ZamVM *vm) {
    pthread_mutex_t *locks[] = {&vm->threadLock, &vm->collectionLock,
                                &vm->markLock, &vm->heap.lock};
    struct timespec pause = {.tv_nsec = ZAMDEBUG_FAILURE_NANOSECONDS};
    for (size_t i = 0; i < sizeof locks / sizeof *locks; i++) {
        int tries = 0;
        while (pthread_mutex_trylock(locks[i]) != 0) {
            if (++tries == ZAMDEBUG_LOCK_TRIES)
                return false;
            nanosleep(&pause, NULL);
        }
        pthread_mutex_unlock(locks[i]);
    }
    return true;
}

void zam_debug_fail(ZamThread *thread, const char *message) {
    if (thread == NULL || thread->collects || zam_gc_collecting(thread))
        return;
    ZamDebugger *d = thread->vm->debugger;
    if (d == NULL || !atomic_load(&d->active) ||
        !atomic_load(&d->stopOnFailure))
        return;
    if (!runtimeUnlocked(thread->vm)) {
        static const char notice[] =
            "the debugger cannot stop the program here, the runtime being in "
            "the middle of something of its own\n";
        (void)!write(STDERR_FILENO, notice, sizeof notice - 1);
        return;
    }

    uint64_t generation = atomic_load(&d->generation);
    bool first = false;
    if (atomic_compare_exchange_strong(&d->failing, &first, true)) {
        size_t length = 0;
        while (message[length] != '\0' && length < sizeof d->failure - 1) {
            d->failure[length] = message[length];
            length++;
        }
        d->failure[length] = '\0';
        atomic_store(&d->failedThread, thread->id);
        atomic_store(&d->failedAt, thread->instructionOffset);
        atomic_store(&d->failed, true);
    }
    atomic_store(&d->pausing, true);

    thread->debugBoundary = false;
    atomic_store(&thread->debugParked, true);
    struct timespec pause = {.tv_nsec = ZAMDEBUG_FAILURE_NANOSECONDS};
    while (atomic_load(&d->generation) == generation)
        nanosleep(&pause, NULL);
    atomic_store(&thread->debugParked, false);
}

// holdDying is what zam_die runs before ending the process.
static void holdDying(const char *message) {
    zam_debug_fail(zam_thread_current(), message);
}

// still reports whether thread's stacks are not changing: it is held by the
// debugger, waiting out a collection, or not running instructions at all.
static bool still(ZamThread *thread) {
    return atomic_load(&thread->debugParked) ||
           atomic_load(&thread->awaitingStop) ||
           atomic_load(&thread->state) != ZAMTHREAD_RUNNING;
}

// exact reports whether thread stands between instructions, so that where it
// stands is the start of the next one rather than just past the one it is in.
static bool exact(ZamThread *thread) {
    return (atomic_load(&thread->debugParked) && thread->debugBoundary) ||
           atomic_load(&thread->awaitingStop) ||
           atomic_load(&thread->state) == ZAMTHREAD_PARKED;
}

static bool everyThreadStill(ZamVM *vm) {
    bool stopped = true;
    pthread_mutex_lock(&vm->threadLock);
    for (ZamThread *thread = &vm->main; thread != NULL && stopped;
         thread = thread->next)
        stopped = still(thread);
    pthread_mutex_unlock(&vm->threadLock);
    return stopped;
}

static ZamThread *findThread(ZamVM *vm, uint64_t id) {
    pthread_mutex_lock(&vm->threadLock);
    ZamThread *thread = &vm->main;
    while (thread != NULL && thread->id != id)
        thread = thread->next;
    pthread_mutex_unlock(&vm->threadLock);
    if (thread == NULL || atomic_load(&thread->state) == ZAMTHREAD_EXITED)
        return NULL;
    return thread;
}

// resume lets the program go, with stepping, if it is not NULL, taking step.
static void resume(ZamDebugger *d, ZamThread *stepping, ZamDebugStep step) {
    ZamVM *vm = d->vm;
    pthread_mutex_lock(&d->lock);
    pthread_mutex_lock(&vm->threadLock);
    for (ZamThread *thread = &vm->main; thread != NULL; thread = thread->next)
        thread->debugStep = ZAMDEBUG_STEP_NONE;
    pthread_mutex_unlock(&vm->threadLock);

    if (stepping != NULL) {
        uint64_t at = stepping->instructionOffset;
        if (!exact(stepping) && at > 0)
            at--;
        stepping->debugStepFile = NULL;
        stepping->debugStepLine = 0;
        zam_image_locate(&vm->image, at, &stepping->debugStepFile,
                         &stepping->debugStepLine);
        stepping->debugStepDepth = depthOf(stepping->functionEntry);
        stepping->debugStep = step;
    }

    d->announced = false;
    d->failureAnnounced = false;
    atomic_store(&d->failed, false);
    atomic_store(&d->failing, false);
    atomic_store(&d->pausing, false);
    atomic_fetch_add(&d->generation, 1);
    pthread_cond_broadcast(&d->resumed);
    pthread_mutex_unlock(&d->lock);
}

static void setBreakpoints(ZamDebugger *d, char **words) {
    for (size_t i = 0; i < d->breakpointByteCount; i++)
        atomic_store_explicit(&d->breakpoints[i], 0, memory_order_relaxed);
    for (; *words != NULL; words++) {
        uint64_t offset = strtoull(*words, NULL, 10);
        if (offset / 8 < d->breakpointByteCount)
            atomic_fetch_or_explicit(&d->breakpoints[offset / 8],
                                     (uint8_t)(1u << (offset % 8)),
                                     memory_order_relaxed);
    }
}

static void sendThreads(ZamDebugger *d) {
    ZamVM *vm = d->vm;
    ZamText text = {0};
    textAppend(&text, "threads");
    pthread_mutex_lock(&vm->threadLock);
    for (ZamThread *thread = &vm->main; thread != NULL; thread = thread->next)
        if (atomic_load(&thread->state) != ZAMTHREAD_EXITED)
            textAppend(&text, " %" PRIu64, thread->id);
    pthread_mutex_unlock(&vm->threadLock);
    sendText(d, &text);
}

// ZAMDEBUG_READ_BYTES is the most a read answers with.
#define ZAMDEBUG_READ_BYTES (64 << 10)

// ZamDebugFrame is where one of a thread's frames stands: the instruction,
// the operand its slots begin at, and the function entry the call made.
typedef struct ZamDebugFrame {
    uint64_t offset;
    uint64_t base;
    uint64_t entry;
} ZamDebugFrame;

static uint64_t baseOf(const ZamThread *thread, uint64_t entryOffset) {
    ZamMarkFunction entry;
    if (!zam_thread_function_entry(thread, entryOffset, &entry))
        return 0;
    return entry.operandPop;
}

// sendStack answers where each of a thread's frames stands, as the
// instruction, the frame's base and its entry. A native's frame stands where
// its caller does, and is the caller's. It is read only while the program is
// stopped, there being nothing to read of a thread whose stacks are changing.
static void sendStack(ZamDebugger *d, uint64_t id) {
    ZamText text = {0};
    textAppend(&text, "stack %" PRIu64, id);
    ZamThread *thread = findThread(d->vm, id);
    if (thread == NULL || !d->announced) {
        sendText(d, &text);
        return;
    }

    ZamDebugFrame *frames = NULL;
    size_t count = 0, capacity = 0;
    uint64_t offset = thread->instructionOffset;
    uint64_t entryOffset = thread->functionEntry;
    for (;;) {
        if (offset != 0 && count > 0 && frames[count - 1].offset == offset) {
            frames[count - 1].entry = entryOffset;
        } else if (offset != 0) {
            if (count == capacity) {
                capacity = capacity == 0 ? 16 : capacity * 2;
                frames = zam_realloc(frames, capacity * sizeof *frames);
            }
            frames[count++] = (ZamDebugFrame){.offset = offset, .entry = entryOffset};
        }

        ZamMarkFunction entry;
        if (!zam_thread_function_entry(thread, entryOffset, &entry))
            break;
        // A spawned thread's outermost frame was laid by the spawn, and names
        // nowhere to come back to.
        if (entry.previous == ZAMMARK_NONE && thread != &d->vm->main)
            break;
        offset = entry.returnInstruction;
        entryOffset = entry.previous;
    }

    for (size_t i = 0; i < count; i++) {
        uint64_t at = frames[i].offset;
        if (i > 0 || !exact(thread))
            at--;
        textAppend(&text, " %" PRIu64 " %" PRIu64 " %" PRIu64, at,
                   baseOf(thread, frames[i].entry), frames[i].entry);
    }
    free(frames);
    sendText(d, &text);
}

// sendWords answers count of a thread's operand words from index on.
static void sendWords(ZamDebugger *d, uint64_t id, uint64_t index,
                      uint64_t count) {
    ZamText text = {0};
    textAppend(&text, "words");
    ZamThread *thread = findThread(d->vm, id);
    if (thread != NULL && d->announced && index <= thread->operandCount &&
        count <= thread->operandCount - index &&
        count <= ZAMDEBUG_READ_BYTES / ZAMWORD_BYTES)
        for (uint64_t i = 0; i < count; i++)
            textAppend(&text, " %" PRIu64, (uint64_t)thread->operands[index + i]);
    sendText(d, &text);
}

// readable answers where count bytes at address lie, or NULL if they do not
// all lie in the heap, the static region, or the thread's operand stack, which
// an address with the frame location bit set is an offset into.
static const uint8_t *readable(const ZamThread *thread, uint64_t address,
                               uint64_t count) {
    const ZamVM *vm = thread->vm;
    if (count == 0 || count > ZAMDEBUG_READ_BYTES)
        return NULL;
    if (address & ZAMFRAME_LOCATION_BIT) {
        uint64_t offset = address & ~ZAMFRAME_LOCATION_BIT;
        uint64_t size = thread->operandCount * ZAMWORD_BYTES;
        if (offset > size || count > size - offset)
            return NULL;
        return (const uint8_t *)thread->operands + offset;
    }
    const uint8_t *at = (const uint8_t *)(uintptr_t)address;
    if (zam_heap_contains(&vm->heap, at) &&
        zam_heap_contains(&vm->heap, at + count - 1))
        return at;
    uint64_t statics = (uint64_t)(uintptr_t)vm->statics;
    uint64_t size = vm->image.staticRegionByteCount;
    if (address >= statics && address - statics <= size &&
        count <= size - (address - statics))
        return at;
    return NULL;
}

// sendRead answers count bytes at address, in hexadecimal.
static void sendRead(ZamDebugger *d, uint64_t id, uint64_t address,
                     uint64_t count) {
    ZamText text = {0};
    textAppend(&text, "read ");
    ZamThread *thread = findThread(d->vm, id);
    const uint8_t *at = thread != NULL && d->announced
                            ? readable(thread, address, count)
                            : NULL;
    for (uint64_t i = 0; at != NULL && i < count; i++)
        textAppend(&text, "%02x", at[i]);
    sendText(d, &text);
}

// hexValue answers the value of a hexadecimal digit, or -1.
static int hexValue(char c) {
    if (c >= '0' && c <= '9')
        return c - '0';
    if (c >= 'a' && c <= 'f')
        return c - 'a' + 10;
    if (c >= 'A' && c <= 'F')
        return c - 'A' + 10;
    return -1;
}

// sendWrite writes the bytes hex spells at address, where read would read
// them, answering whether it could.
static void sendWrite(ZamDebugger *d, uint64_t id, uint64_t address,
                      const char *hex) {
    ZamText text = {0};
    size_t length = strlen(hex);
    ZamThread *thread = findThread(d->vm, id);
    uint8_t *at = thread != NULL && d->announced && length % 2 == 0
                      ? (uint8_t *)readable(thread, address, length / 2)
                      : NULL;
    for (size_t i = 0; at != NULL && i < length; i++)
        if (hexValue(hex[i]) < 0)
            at = NULL;
    for (size_t i = 0; at != NULL && i < length / 2; i++)
        at[i] = (uint8_t)(hexValue(hex[2 * i]) << 4 | hexValue(hex[2 * i + 1]));
    textAppend(&text, "write %d", at != NULL);
    sendText(d, &text);
}

// sendString stores at address a fresh string of the bytes hex spells,
// allocated as the thread allocates, answering where the string lies, or 0 if
// it could not. The store is marked for the collector as a program's is.
static void sendString(ZamDebugger *d, uint64_t id, uint64_t address,
                       const char *hex) {
    ZamText text = {0};
    size_t length = strlen(hex);
    ZamThread *thread = findThread(d->vm, id);
    uint8_t *at = thread != NULL && d->announced && length % 2 == 0 &&
                          address % ZAMWORD_BYTES == 0
                      ? (uint8_t *)readable(thread, address, ZAMWORD_BYTES)
                      : NULL;
    char *bytes = at != NULL ? zam_alloc(length / 2 + 1) : NULL;
    for (size_t i = 0; bytes != NULL && i < length / 2; i++) {
        int high = hexValue(hex[2 * i]), low = hexValue(hex[2 * i + 1]);
        if (high < 0 || low < 0) {
            free(bytes);
            bytes = NULL;
        } else {
            bytes[i] = (char)(high << 4 | low);
        }
    }

    uint64_t string = 0;
    if (bytes != NULL) {
        // A thread coming back from a blocking call waits on the lock, and so
        // keeps off the allocation state being used on its behalf.
        pthread_mutex_lock(&d->lock);
        string = zam_native_new_string(thread, bytes, length / 2);
        memcpy(at, &string, sizeof string);
        zam_heap_dirty(&d->vm->heap, at, ZAMWORD_BYTES);
        pthread_mutex_unlock(&d->lock);
        free(bytes);
    }
    textAppend(&text, "string %" PRIu64, string);
    sendText(d, &text);
}

// sendReceiver answers the receiver of the call a function entry of the
// thread's opened, or 0. The word naming it may lie anywhere a closure may,
// the stack the runtime calls from included, so it is read here rather than
// by the debugger.
static void sendReceiver(ZamDebugger *d, uint64_t id, uint64_t entry) {
    ZamText text = {0};
    ZamThread *thread = findThread(d->vm, id);
    uint64_t location = thread != NULL && d->announced
                            ? (uint64_t)zam_frame_receiver(thread, entry)
                            : 0;
    uint64_t receiver = 0;
    if (location & ZAMFRAME_LOCATION_BIT) {
        const uint8_t *at = readable(thread, location, ZAMWORD_BYTES);
        if (at != NULL)
            memcpy(&receiver, at, sizeof receiver);
    } else if (location != 0) {
        receiver = *(const ZamWord *)(uintptr_t)location;
    }
    textAppend(&text, "receiver %" PRIu64, receiver);
    sendText(d, &text);
}

// sendInstance answers the instantiation of the generic declaration template
// that the instance at address is, or descends from, or 0.
static void sendInstance(ZamDebugger *d, uint64_t id, uint64_t address,
                         uint64_t templateDecl) {
    ZamText text = {0};
    ZamThread *thread = findThread(d->vm, id);
    const uint8_t *at = thread != NULL && d->announced
                            ? readable(thread, address, 2 * ZAMWORD_BYTES)
                            : NULL;
    ZamTypeShort instance = 0;
    if (at != NULL && templateDecl <= UINT16_MAX) {
        ZamTypeID type = {.shortID = zam_object_header_of((void *)at)->shortID};
        instance = ZAMTYPE_DECL(type.shortID) == templateDecl
                       ? type.shortID
                       : zam_type_template_ancestor(&d->vm->image, type,
                                                    (uint16_t)templateDecl);
    }
    textAppend(&text, "instance %" PRIu32, instance);
    sendText(d, &text);
}

static void pauseProgram(ZamDebugger *d) {
    pthread_mutex_lock(&d->lock);
    if (!atomic_load(&d->pausing)) {
        d->stopped = d->vm->main.id;
        d->reason = "pause";
        d->at = d->vm->main.instructionOffset;
        atomic_store(&d->pausing, true);
    }
    pthread_mutex_unlock(&d->lock);
}

static void command(ZamDebugger *d, char *line) {
    char *words[64];
    size_t count = 0;
    char *save = NULL;
    char **all = words;
    size_t capacity = sizeof words / sizeof *words;
    for (char *word = strtok_r(line, " \r", &save); word != NULL;
         word = strtok_r(NULL, " \r", &save)) {
        if (count + 1 == capacity) {
            capacity *= 2;
            char **grown = zam_alloc(capacity * sizeof *grown);
            memcpy(grown, all, count * sizeof *grown);
            if (all != words)
                free(all);
            all = grown;
        }
        all[count++] = word;
    }
    all[count] = NULL;

    if (count == 0) {
    } else if (strcmp(all[0], "break") == 0) {
        setBreakpoints(d, all + 1);
    } else if (strcmp(all[0], "statics") == 0) {
        ZamText text = {0};
        textAppend(&text, "statics %" PRIu64, (uint64_t)(uintptr_t)d->vm->statics);
        sendText(d, &text);
    } else if (strcmp(all[0], "failures") == 0 && count == 2) {
        atomic_store(&d->stopOnFailure, strcmp(all[1], "1") == 0);
    } else if (strcmp(all[0], "continue") == 0) {
        resume(d, NULL, ZAMDEBUG_STEP_NONE);
    } else if (strcmp(all[0], "pause") == 0) {
        pauseProgram(d);
    } else if (strcmp(all[0], "threads") == 0) {
        sendThreads(d);
    } else if (strcmp(all[0], "stack") == 0 && count == 2) {
        sendStack(d, strtoull(all[1], NULL, 10));
    } else if (strcmp(all[0], "words") == 0 && count == 4) {
        sendWords(d, strtoull(all[1], NULL, 10), strtoull(all[2], NULL, 10),
                  strtoull(all[3], NULL, 10));
    } else if (strcmp(all[0], "read") == 0 && count == 4) {
        sendRead(d, strtoull(all[1], NULL, 10), strtoull(all[2], NULL, 10),
                 strtoull(all[3], NULL, 10));
    } else if (strcmp(all[0], "string") == 0 && (count == 3 || count == 4)) {
        sendString(d, strtoull(all[1], NULL, 10), strtoull(all[2], NULL, 10),
                   count == 4 ? all[3] : "");
    } else if (strcmp(all[0], "write") == 0 && count == 4) {
        sendWrite(d, strtoull(all[1], NULL, 10), strtoull(all[2], NULL, 10),
                  all[3]);
    } else if (strcmp(all[0], "instance") == 0 && count == 4) {
        sendInstance(d, strtoull(all[1], NULL, 10), strtoull(all[2], NULL, 10),
                     strtoull(all[3], NULL, 10));
    } else if (strcmp(all[0], "receiver") == 0 && count == 3) {
        sendReceiver(d, strtoull(all[1], NULL, 10), strtoull(all[2], NULL, 10));
    } else if (strcmp(all[0], "step") == 0 && count == 3) {
        ZamDebugStep step = strcmp(all[2], "in") == 0     ? ZAMDEBUG_STEP_IN
                            : strcmp(all[2], "over") == 0 ? ZAMDEBUG_STEP_OVER
                            : strcmp(all[2], "out") == 0  ? ZAMDEBUG_STEP_OUT
                                                          : ZAMDEBUG_STEP_NONE;
        ZamThread *thread = findThread(d->vm, strtoull(all[1], NULL, 10));
        if (thread != NULL && d->announced && step != ZAMDEBUG_STEP_NONE)
            resume(d, thread, step);
    }
    if (all != words)
        free(all);
}

// announce tells the debugger the program has stopped, once every thread has.
static void announce(ZamDebugger *d) {
    if (atomic_load(&d->failed) && !d->failureAnnounced &&
        everyThreadStill(d->vm)) {
        d->failureAnnounced = true;
        d->announced = true;
        ZamText text = {0};
        textAppend(&text, "stopped %" PRIu64 " exception %" PRIu64 " ",
                   atomic_load(&d->failedThread), atomic_load(&d->failedAt));
        for (const char *c = d->failure; *c != '\0'; c++)
            textAppend(&text, "%02x", (unsigned char)*c);
        sendText(d, &text);
        return;
    }
    if (d->announced || !atomic_load(&d->pausing) || !everyThreadStill(d->vm))
        return;
    pthread_mutex_lock(&d->lock);
    uint64_t stopped = d->stopped;
    const char *reason = d->reason;
    uint64_t at = d->at;
    pthread_mutex_unlock(&d->lock);
    d->announced = true;
    ZamText text = {0};
    textAppend(&text, "stopped %" PRIu64 " %s %" PRIu64, stopped, reason, at);
    sendText(d, &text);
}

// detach lets the program run on as though it had never had a debugger.
static void detach(ZamDebugger *d) {
    pthread_mutex_lock(&d->lock);
    atomic_store(&d->active, false);
    atomic_store(&d->pausing, false);
    atomic_fetch_add(&d->generation, 1);
    pthread_cond_broadcast(&d->resumed);
    pthread_mutex_unlock(&d->lock);
    close(d->fd);
}

// readCommands acts on every whole line the debugger has sent, answering
// false once it has gone.
static bool readCommands(ZamDebugger *d) {
    if (d->inputCapacity - d->inputLength < 4096) {
        d->inputCapacity = d->inputCapacity * 2 + 4096;
        d->input = zam_realloc(d->input, d->inputCapacity);
    }
    ssize_t n = read(d->fd, d->input + d->inputLength,
                     d->inputCapacity - d->inputLength);
    if (n < 0)
        return errno == EINTR || errno == EAGAIN;
    if (n == 0)
        return false;
    d->inputLength += (size_t)n;

    char *start = d->input;
    char *end = d->input + d->inputLength;
    for (char *newline; (newline = memchr(start, '\n', (size_t)(end - start)));
         start = newline + 1) {
        *newline = '\0';
        command(d, start);
    }
    d->inputLength = (size_t)(end - start);
    memmove(d->input, start, d->inputLength);
    return true;
}

static void *debuggerMain(void *argument) {
    ZamDebugger *d = argument;
    for (;;) {
        struct pollfd fd = {.fd = d->fd, .events = POLLIN};
        int ready = poll(&fd, 1, ZAMDEBUG_POLL_MILLISECONDS);
        if (ready < 0 && errno != EINTR)
            break;
        if (ready > 0 && !readCommands(d))
            break;
        announce(d);
    }
    detach(d);
    return NULL;
}

// connectTo connects to the debugger listening on the loopback port, and
// says who is connecting: the token the debugger handed out, and the role.
// It answers the descriptor, or -1.
static int connectTo(const char *port, const char *token, const char *role) {
    struct sockaddr_in address = {
        .sin_family = AF_INET,
        .sin_port = htons((uint16_t)atoi(port)),
        .sin_addr = {.s_addr = htonl(INADDR_LOOPBACK)},
    };
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0)
        return -1;
    if (connect(fd, (struct sockaddr *)&address, sizeof address) != 0) {
        close(fd);
        return -1;
    }
    dprintf(fd, "hello %s %s\n", token, role);
    return fd;
}

// supervise forks, returning in the child. The parent waits for the child,
// telling the debugger on port its pid and how it ended, and ends as it did.
static void supervise(const char *port, const char *token) {
    pid_t child = fork();
    if (child < 0)
        zam_die(ZAMEXIT_PANIC, "failed to fork the program to debug\n");
    if (child == 0)
        return;

    // An interrupt typed at the terminal is the program's to answer, and the
    // parent stays to say how it did.
    signal(SIGINT, SIG_IGN);
    signal(SIGQUIT, SIG_IGN);
    signal(SIGPIPE, SIG_IGN);
    int fd = connectTo(port, token, "status");
    if (fd >= 0)
        dprintf(fd, "pid %d\n", (int)child);
    int status = 0;
    while (waitpid(child, &status, 0) < 0 && errno == EINTR)
        ;
    int code = WIFEXITED(status)     ? WEXITSTATUS(status)
               : WIFSIGNALED(status) ? 128 + WTERMSIG(status)
                                     : ZAMEXIT_PANIC;
    if (fd >= 0) {
        dprintf(fd, "exited %d\n", code);
        close(fd);
    }
    _exit(code);
}

// environment answers a copy of the variable called name, which it unsets,
// as what the program starts is not under the debugger, or "" if unset.
static void environment(const char *name, char *value, size_t size) {
    const char *set = getenv(name);
    value[0] = '\0';
    if (set != NULL && strlen(set) < size)
        strcpy(value, set);
    unsetenv(name);
}

void zam_debug_connect(ZamVM *vm) {
    char port[16], statusPort[16], token[128];
    environment("ZAM_DEBUG_PORT", port, sizeof port);
    environment("ZAM_DEBUG_STATUS_PORT", statusPort, sizeof statusPort);
    environment("ZAM_DEBUG_TOKEN", token, sizeof token);
    if (*statusPort != '\0')
        supervise(statusPort, token);

    const char *fd = getenv("ZAM_DEBUG_FD");
    if (fd != NULL && *fd != '\0') {
        zam_debug_start(vm, atoi(fd));
        return;
    }
    if (*port != '\0') {
        int connected = connectTo(port, token, "program");
        if (connected < 0)
            zam_die(ZAMEXIT_PANIC,
                    "cannot connect to the debugger on port %s\n", port);
        zam_debug_start(vm, connected);
    }
}

void zam_debug_start(ZamVM *vm, int fd) {
    ZamDebugger *d = zam_alloc(sizeof *d);
    *d = (ZamDebugger){.vm = vm, .fd = fd};
    d->breakpointByteCount = (vm->image.operationsByteCount + 7) / 8;
    d->breakpoints = zam_alloc(d->breakpointByteCount + 1);
    memset((void *)d->breakpoints, 0, d->breakpointByteCount + 1);
    pthread_mutex_init(&d->lock, NULL);
    pthread_cond_init(&d->resumed, NULL);

    // The program is held before its first instruction, and the debugger,
    // which asked for that, needs no telling of it.
    atomic_init(&d->active, true);
    atomic_init(&d->pausing, true);
    d->stopped = vm->main.id;
    d->reason = "pause";
    d->announced = true;

    // What the program starts is not under the debugger.
    fcntl(fd, F_SETFD, FD_CLOEXEC);
    unsetenv("ZAM_DEBUG_FD");
#ifdef SO_NOSIGPIPE
    int one = 1;
    setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
#endif
    vm->debugger = d;
    zam_set_die_hold(holdDying);

    // The debugger's thread takes no signals, which are the program's.
    sigset_t all, previous;
    sigfillset(&all);
    pthread_sigmask(SIG_BLOCK, &all, &previous);
    pthread_t thread;
    if (pthread_create(&thread, NULL, debuggerMain, d) != 0)
        zam_die(ZAMEXIT_PANIC, "failed to start the debugger's thread\n");
    pthread_detach(thread);
    pthread_sigmask(SIG_SETMASK, &previous, NULL);
}
