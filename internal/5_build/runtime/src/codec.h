#pragma once

// Helpers the codec natives share: reading strings and handles out of the
// frame, and writing results and errors through out pointers.

#include "zam/native.h"

#include <stdlib.h>
#include <string.h>

#include "instance.h"

static inline const ZamString *codec_string(ZamThread *thread, uint64_t wordIndex) {
    return zam_string_of((void *)(uintptr_t)zam_native_argument(thread, wordIndex));
}

// codec_c_string copies the zam string at wordIndex into a NUL-terminated
// string the caller frees.
static inline char *codec_c_string(ZamThread *thread, uint64_t wordIndex) {
    const ZamString *s = codec_string(thread, wordIndex);
    char *c = malloc(s->length + 1);
    if (c == NULL)
        zam_native_panic(thread, "out of memory copying a string");
    memcpy(c, s->bytes, s->length);
    c[s->length] = '\0';
    return c;
}

static inline void *codec_handle(ZamThread *thread, uint64_t wordIndex) {
    return (void *)(uintptr_t)zam_native_argument(thread, wordIndex);
}

static inline void codec_write_word(ZamThread *thread, uint64_t wordIndex, ZamWord word) {
    *(ZamWord *)zam_native_argument_pointer(thread, wordIndex) = word;
}

// codec_write_bool writes through the bool* at wordIndex, which points at
// one byte rather than a word.
static inline void codec_write_bool(ZamThread *thread, uint64_t wordIndex, bool value) {
    *(bool *)zam_native_argument_pointer(thread, wordIndex) = value;
}

static inline void codec_write_string(ZamThread *thread, uint64_t wordIndex,
                                      const char *bytes, size_t length) {
    codec_write_word(thread, wordIndex,
                     (ZamWord)zam_native_new_string(thread, bytes, length));
}

// codec_write_error writes message to the error slot, or null for none.
static inline void codec_write_error(ZamThread *thread, uint64_t wordIndex,
                                     const char *message) {
    if (message == NULL)
        codec_write_word(thread, wordIndex, 0);
    else
        codec_write_string(thread, wordIndex, message, strlen(message));
}

// CodecBuffer is a growable run of bytes for a library to write output into.
typedef struct CodecBuffer {
    char *bytes;
    size_t length;
    size_t capacity;
} CodecBuffer;

static inline void codec_buffer_append(CodecBuffer *b, const void *bytes, size_t length) {
    if (b->length + length > b->capacity) {
        size_t capacity = b->capacity == 0 ? 256 : b->capacity;
        while (capacity < b->length + length)
            capacity *= 2;
        char *grown = realloc(b->bytes, capacity);
        if (grown == NULL)
            abort();
        b->bytes = grown;
        b->capacity = capacity;
    }
    memcpy(b->bytes + b->length, bytes, length);
    b->length += length;
}
