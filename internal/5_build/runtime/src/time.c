#include <errno.h>
#include <pthread.h>
#include <time.h>

#include "codec.h"

// The time natives: reading the wall and monotonic clocks, sleeping, and
// timers a thread waits on until they fire or are stopped. Times cross as
// nanoseconds in an int.

static ZamInt timeRead(clockid_t clock) {
    struct timespec ts;
    clock_gettime(clock, &ts);
    return (ZamInt)ts.tv_sec * 1000000000 + ts.tv_nsec;
}

static struct timespec timeSpec(ZamInt ns) {
    struct timespec ts = {.tv_sec = (time_t)(ns / 1000000000), .tv_nsec = (long)(ns % 1000000000)};
    return ts;
}

// stdTimeNow answers (wall, monotonic): nanoseconds since the Unix epoch and
// since an arbitrary fixed point.
static void stdTimeNow(ZamThread *thread) {
    codec_write_word(thread, 0, (ZamWord)timeRead(CLOCK_REALTIME));
    codec_write_word(thread, 1, (ZamWord)timeRead(CLOCK_MONOTONIC));
    zam_native_return_void(thread);
}

// stdTimeSleep sleeps for ns nanoseconds, letting the collector run meanwhile.
static void stdTimeSleep(ZamThread *thread) {
    ZamInt ns = (ZamInt)zam_native_argument(thread, 0);
    struct timespec ts = timeSpec(ns > 0 ? ns : 0);
    zam_native_block_begin(thread);
    while (ns > 0 && nanosleep(&ts, &ts) != 0 && errno == EINTR)
        ;
    zam_native_block_end(thread);
    zam_native_return_void(thread);
}

// TimeTimer fires at a monotonic deadline unless stopped first, and again
// every period after it when it has one, as a ticker.
typedef struct TimeTimer {
    pthread_mutex_t lock;
    pthread_cond_t changed;
    ZamInt deadline;
    ZamInt period;
    bool stopped;
} TimeTimer;

// stdTimerNew starts a timer firing at the monotonic deadline and every
// period after it, or once for a period of 0, answering its handle.
static void stdTimerNew(ZamThread *thread) {
    TimeTimer *t = calloc(1, sizeof *t);
    if (t == NULL || pthread_mutex_init(&t->lock, NULL) != 0 || pthread_cond_init(&t->changed, NULL) != 0)
        zam_native_panic(thread, "cannot make a timer");
    t->deadline = (ZamInt)zam_native_argument(thread, 0);
    t->period = (ZamInt)zam_native_argument(thread, 1);
    codec_write_word(thread, 2, (ZamWord)(uintptr_t)t);
    zam_native_return_void(thread);
}

// stdTimerWait blocks until the timer fires or is stopped, answering whether
// it fired. A ticker's tick is taken by the one wait that sees it, which moves
// the deadline on to the next tick still to come, dropping any missed.
static void stdTimerWait(ZamThread *thread) {
    TimeTimer *t = codec_handle(thread, 0);
    bool fired = false;
    zam_native_block_begin(thread);
    pthread_mutex_lock(&t->lock);
    for (;;) {
        if (t->stopped)
            break;
        ZamInt left = t->deadline - timeRead(CLOCK_MONOTONIC);
        if (left <= 0) {
            if (t->period > 0)
                t->deadline += t->period * (-left / t->period + 1);
            fired = true;
            break;
        }
        // The condition waits against the wall clock, which may step; the
        // deadline is monotonic, so each wake measures again.
        struct timespec at = timeSpec(timeRead(CLOCK_REALTIME) + left);
        pthread_cond_timedwait(&t->changed, &t->lock, &at);
    }
    pthread_mutex_unlock(&t->lock);
    zam_native_block_end(thread);
    codec_write_bool(thread, 1, fired);
    zam_native_return_void(thread);
}

// stdTimerStop stops the timer, answering whether it was still to fire.
static void stdTimerStop(ZamThread *thread) {
    TimeTimer *t = codec_handle(thread, 0);
    pthread_mutex_lock(&t->lock);
    bool active = !t->stopped && (t->period > 0 || t->deadline > timeRead(CLOCK_MONOTONIC));
    t->stopped = true;
    pthread_cond_broadcast(&t->changed);
    pthread_mutex_unlock(&t->lock);
    codec_write_bool(thread, 1, active);
    zam_native_return_void(thread);
}

// stdTimerReset rearms the timer for a new monotonic deadline and period,
// answering whether it was still to fire.
static void stdTimerReset(ZamThread *thread) {
    TimeTimer *t = codec_handle(thread, 0);
    ZamInt deadline = (ZamInt)zam_native_argument(thread, 1);
    ZamInt period = (ZamInt)zam_native_argument(thread, 2);
    pthread_mutex_lock(&t->lock);
    bool active = !t->stopped && (t->period > 0 || t->deadline > timeRead(CLOCK_MONOTONIC));
    t->stopped = false;
    t->deadline = deadline;
    t->period = period;
    pthread_cond_broadcast(&t->changed);
    pthread_mutex_unlock(&t->lock);
    codec_write_bool(thread, 3, active);
    zam_native_return_void(thread);
}

static void stdTimerFree(ZamThread *thread) {
    TimeTimer *t = codec_handle(thread, 0);
    if (t != NULL) {
        pthread_cond_destroy(&t->changed);
        pthread_mutex_destroy(&t->lock);
        free(t);
    }
    zam_native_return_void(thread);
}

const ZamNativeEntry zamTimeNativeTable[] = {
    {"STD_TIME_NOW", stdTimeNow},
    {"STD_TIME_SLEEP", stdTimeSleep},
    {"STD_TIMER_NEW", stdTimerNew},
    {"STD_TIMER_WAIT", stdTimerWait},
    {"STD_TIMER_STOP", stdTimerStop},
    {"STD_TIMER_RESET", stdTimerReset},
    {"STD_TIMER_FREE", stdTimerFree},
};

const uint64_t zamTimeNativeTableCount = sizeof(zamTimeNativeTable) / sizeof(zamTimeNativeTable[0]);
