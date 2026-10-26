#pragma once

#include <stdarg.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// ZAM_SIGNAL_MAX is the number of signals each thread can register a handler
// for. 32 covers every POSIX standard signal and the realtime range the
// runtime cares to expose.
#define ZAM_SIGNAL_MAX 32

// A word is the unit the image is written in and the unit the operand stack
// counts in. Every offset the image carries is a byte offset, but every one of
// them is a multiple of this, so nothing is ever read misaligned.
#define ZAMWORD_BYTES 8
typedef uint64_t ZamWord;

// The image is little-endian throughout, and is read in place rather than
// decoded word by word. That is only sound on a little-endian host; a
// big-endian port would have to byte-swap on the way in, so the assumption is
// asserted here rather than discovered as garbage offsets.
#if defined(__BYTE_ORDER__) && __BYTE_ORDER__ != __ORDER_LITTLE_ENDIAN__
#error "the Zamylene image format is little-endian; this host is not"
#endif

// Exit statuses the runtime itself ends with. A program's own status comes from
// OpExit and is whatever the program said, so these are deliberately kept to
// the low numbers a program is unlikely to mean anything by.
typedef enum ZamExit {
    ZAMEXIT_OK = 0,
    // ZAMEXIT_USAGE is a command line the runtime could not read.
    ZAMEXIT_USAGE = 64,
    // ZAMEXIT_IMAGE is an image that could not be opened, or that was opened
    // and turned out not to be one.
    ZAMEXIT_IMAGE = 65,
    // ZAMEXIT_OOM is an allocation the runtime could not make.
    ZAMEXIT_OOM = 66,
    // ZAMEXIT_PANIC is the program brought down by something it did.
    ZAMEXIT_PANIC = 67,
    // ZAMEXIT_UNIMPLEMENTED is a path this runtime has not been built out to
    // yet. It is distinct from a panic so that a stub being reached is never
    // mistaken for the program being at fault.
    ZAMEXIT_UNIMPLEMENTED = 70,
} ZamExit;

// zam_die writes the message and ends the process. Nothing is unwound on the
// way out: a runtime that has lost track of its own state cannot be trusted to
// run a destructor, and everything it holds is the process's anyway.
_Noreturn void zam_die(ZamExit status, const char *format, ...);

// ZAMTODO marks a path that is declared but not built. Every one of these is a
// decision still owed rather than an error, and reaching one stops the run
// loudly so that a half-built runtime never quietly produces a wrong answer.
#define ZAMTODO(what)                                                         \
    zam_die(ZAMEXIT_UNIMPLEMENTED, "%s:%d: not implemented: %s\n", __FILE__,   \
            __LINE__, (what))

void *zam_alloc(size_t byteCount);

// zam_alloc_size is how many bytes the allocator actually set aside for a
// block zam_alloc returned, which is what the heap is measured in.
size_t zam_alloc_size(void *pointer);
void *zam_realloc(void *pointer, size_t byteCount);
