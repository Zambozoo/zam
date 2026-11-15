#include "common.h"

#if defined(__APPLE__)
#include <malloc/malloc.h>
#else
#include <malloc.h>
#endif

// ZAMDIE_HELD_BYTES is the most of a message zam_die hands its hold.
#define ZAMDIE_HELD_BYTES 1024

// dieTrace is what zam_die writes after its message, if anything, and dieHold
// what it runs before ending the process.
static void (*dieTrace)(int fd);
static void (*dieHold)(const char *message);

void zam_set_die_trace(void (*trace)(int fd)) { dieTrace = trace; }

void zam_set_die_hold(void (*hold)(const char *message)) { dieHold = hold; }

_Noreturn void zam_die(ZamExit status, const char *format, ...) {
    va_list args, held;
    va_start(args, format);
    va_copy(held, args);
    fputs("\033[31m", stderr);
    vfprintf(stderr, format, args);
    fputs("\033[0m", stderr);
    va_end(args);
    fflush(stderr);
    if (dieTrace != NULL)
        dieTrace(fileno(stderr));
    if (dieHold != NULL) {
        char message[ZAMDIE_HELD_BYTES];
        vsnprintf(message, sizeof message, format, held);
        dieHold(message);
    }
    va_end(held);
    exit(status);
}

void *zam_alloc(size_t byteCount) {
    void *pointer = malloc(byteCount);
    if (pointer == NULL)
        zam_die(ZAMEXIT_OOM, "out of memory allocating %zu bytes\n", byteCount);
    return pointer;
}

size_t zam_alloc_size(void *pointer) {
#if defined(__APPLE__)
    return malloc_size(pointer);
#else
    return malloc_usable_size(pointer);
#endif
}

void *zam_realloc(void *pointer, size_t byteCount) {
    void *grown = realloc(pointer, byteCount);
    if (grown == NULL)
        zam_die(ZAMEXIT_OOM, "out of memory growing to %zu bytes\n", byteCount);
    return grown;
}
