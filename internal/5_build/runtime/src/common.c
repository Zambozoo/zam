#include "common.h"

_Noreturn void zam_die(ZamExit status, const char *format, ...) {
    va_list args;
    va_start(args, format);
    fputs("\033[31m", stderr);
    vfprintf(stderr, format, args);
    fputs("\033[0m", stderr);
    va_end(args);
    exit(status);
}

void *zam_alloc(size_t byteCount) {
    void *pointer = malloc(byteCount);
    if (pointer == NULL)
        zam_die(ZAMEXIT_OOM, "out of memory allocating %zu bytes\n", byteCount);
    return pointer;
}

void *zam_realloc(void *pointer, size_t byteCount) {
    void *grown = realloc(pointer, byteCount);
    if (grown == NULL)
        zam_die(ZAMEXIT_OOM, "out of memory growing to %zu bytes\n", byteCount);
    return grown;
}
