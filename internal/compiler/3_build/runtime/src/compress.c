#include <brotli/decode.h>
#include <brotli/encode.h>
#include <lz4frame.h>
#include <stdio.h>
#include <zlib.h>
#include <zstd.h>

#include "codec.h"

// The compression natives, over zlib, liblz4, libzstd and libbrotli. Each
// works on a whole run of bytes at once. The output is left in a CodecBuffer
// the zam side copies out with STD_CODEC_TAKE, having learned its length.
//
// Every native takes (byte* in, int n, ...) and writes (uint buffer, int
// length, str err) through the last three arguments.

static void finish(ZamThread *thread, uint64_t outIndex, CodecBuffer *b, const char *err) {
    if (err != NULL) {
        free(b->bytes);
        codec_write_word(thread, outIndex, 0);
        *(ZamInt *)zam_native_argument_pointer(thread, outIndex + 1) = 0;
        codec_write_error(thread, outIndex + 2, err);
    } else {
        CodecBuffer *kept = malloc(sizeof *kept);
        if (kept == NULL)
            abort();
        *kept = *b;
        codec_write_word(thread, outIndex, (ZamWord)(uintptr_t)kept);
        *(ZamInt *)zam_native_argument_pointer(thread, outIndex + 1) = (ZamInt)b->length;
        codec_write_error(thread, outIndex + 2, NULL);
    }
    zam_native_return_void(thread);
}

static const uint8_t *input(ZamThread *thread, size_t *n) {
    ZamInt length = (ZamInt)zam_native_argument(thread, 1);
    if (length < 0)
        zam_native_panic(thread, "negative input length");
    *n = (size_t)length;
    return length == 0 ? (const uint8_t *)"" : zam_native_argument_pointer(thread, 0);
}

static void reserve(CodecBuffer *b, size_t more) {
    if (b->length + more <= b->capacity)
        return;
    size_t capacity = b->capacity == 0 ? 4096 : b->capacity;
    while (capacity < b->length + more)
        capacity *= 2;
    char *grown = realloc(b->bytes, capacity);
    if (grown == NULL)
        abort();
    b->bytes = grown;
    b->capacity = capacity;
}

// stdCodecTake copies a buffer's bytes out and frees it.
static void stdCodecTake(ZamThread *thread) {
    CodecBuffer *b = codec_handle(thread, 0);
    if (b->length > 0)
        memcpy(zam_native_argument_pointer(thread, 1), b->bytes, b->length);
    free(b->bytes);
    free(b);
    zam_native_return_void(thread);
}

// zlibWindow is the windowBits that selects the gzip or zlib wrapper.
static int zlibWindow(bool gzip) {
    return gzip ? 15 + 16 : 15;
}

static void deflateRun(ZamThread *thread, bool gzip) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    int level = (int)(ZamInt)zam_native_argument(thread, 2);
    if (level < -1 || level > 9)
        zam_native_panic(thread, "zlib level must be -1 through 9");
    z_stream z = {0};
    if (deflateInit2(&z, level, Z_DEFLATED, zlibWindow(gzip), 8, Z_DEFAULT_STRATEGY) != Z_OK)
        zam_native_panic(thread, "deflateInit2 failed");
    CodecBuffer b = {0};
    reserve(&b, deflateBound(&z, (uLong)n));
    z.next_in = (Bytef *)in;
    z.avail_in = (uInt)n;
    z.next_out = (Bytef *)b.bytes;
    z.avail_out = (uInt)b.capacity;
    int result = deflate(&z, Z_FINISH);
    b.length = z.total_out;
    deflateEnd(&z);
    finish(thread, 3, &b, result == Z_STREAM_END ? NULL : "deflate did not finish");
}

static void inflateRun(ZamThread *thread, bool gzip) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    z_stream z = {0};
    if (inflateInit2(&z, zlibWindow(gzip)) != Z_OK)
        zam_native_panic(thread, "inflateInit2 failed");
    CodecBuffer b = {0};
    z.next_in = (Bytef *)in;
    z.avail_in = (uInt)n;
    int result;
    do {
        reserve(&b, n * 2 + 4096);
        z.next_out = (Bytef *)b.bytes + b.length;
        z.avail_out = (uInt)(b.capacity - b.length);
        result = inflate(&z, Z_NO_FLUSH);
        b.length = z.total_out;
    } while (result == Z_OK);
    const char *err = NULL;
    if (result != Z_STREAM_END)
        err = z.msg != NULL ? z.msg : "truncated or invalid compressed data";
    else if (z.avail_in != 0)
        err = "trailing data after the compressed stream";
    char message[128];
    if (err != NULL) {
        snprintf(message, sizeof message, "%s", err);
        err = message;
    }
    inflateEnd(&z);
    finish(thread, 2, &b, err);
}

static void stdGzipCompress(ZamThread *thread) { deflateRun(thread, true); }
static void stdGzipDecompress(ZamThread *thread) { inflateRun(thread, true); }
static void stdZlibCompress(ZamThread *thread) { deflateRun(thread, false); }
static void stdZlibDecompress(ZamThread *thread) { inflateRun(thread, false); }

static void stdLz4Compress(ZamThread *thread) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    int level = (int)(ZamInt)zam_native_argument(thread, 2);
    if (level < 0 || level > LZ4F_compressionLevel_max())
        zam_native_panic(thread, "LZ4 level must be 0 through 12");
    LZ4F_preferences_t prefs;
    memset(&prefs, 0, sizeof prefs);
    prefs.compressionLevel = level;
    prefs.frameInfo.contentSize = n;
    CodecBuffer b = {0};
    reserve(&b, LZ4F_compressFrameBound(n, &prefs));
    size_t written = LZ4F_compressFrame(b.bytes, b.capacity, in, n, &prefs);
    if (LZ4F_isError(written)) {
        finish(thread, 3, &b, LZ4F_getErrorName(written));
        return;
    }
    b.length = written;
    finish(thread, 3, &b, NULL);
}

static void stdLz4Decompress(ZamThread *thread) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    LZ4F_dctx *ctx;
    if (LZ4F_isError(LZ4F_createDecompressionContext(&ctx, LZ4F_VERSION)))
        zam_native_panic(thread, "LZ4F_createDecompressionContext failed");
    CodecBuffer b = {0};
    size_t at = 0;
    const char *err = NULL;
    for (;;) {
        reserve(&b, 65536);
        size_t outSize = b.capacity - b.length;
        size_t inSize = n - at;
        size_t hint = LZ4F_decompress(ctx, b.bytes + b.length, &outSize, in + at, &inSize, NULL);
        if (LZ4F_isError(hint)) {
            err = LZ4F_getErrorName(hint);
            break;
        }
        b.length += outSize;
        at += inSize;
        if (hint == 0)
            break;
        if (inSize == 0 && outSize == 0) {
            err = "truncated LZ4 frame";
            break;
        }
    }
    if (err == NULL && at != n)
        err = "trailing data after the LZ4 frame";
    LZ4F_freeDecompressionContext(ctx);
    finish(thread, 2, &b, err);
}

static void stdZstdCompress(ZamThread *thread) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    int level = (int)(ZamInt)zam_native_argument(thread, 2);
    if (level < ZSTD_minCLevel() || level > ZSTD_maxCLevel())
        zam_native_panic(thread, "zstd level is out of range");
    CodecBuffer b = {0};
    reserve(&b, ZSTD_compressBound(n));
    size_t written = ZSTD_compress(b.bytes, b.capacity, in, n, level);
    if (ZSTD_isError(written)) {
        finish(thread, 3, &b, ZSTD_getErrorName(written));
        return;
    }
    b.length = written;
    finish(thread, 3, &b, NULL);
}

static void stdZstdDecompress(ZamThread *thread) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    ZSTD_DStream *ds = ZSTD_createDStream();
    ZSTD_initDStream(ds);
    CodecBuffer b = {0};
    ZSTD_inBuffer source = {in, n, 0};
    size_t remaining = 1;
    const char *err = NULL;
    while (remaining != 0) {
        reserve(&b, ZSTD_DStreamOutSize());
        ZSTD_outBuffer sink = {b.bytes + b.length, b.capacity - b.length, 0};
        size_t before = source.pos;
        remaining = ZSTD_decompressStream(ds, &sink, &source);
        if (ZSTD_isError(remaining)) {
            err = ZSTD_getErrorName(remaining);
            break;
        }
        b.length += sink.pos;
        if (remaining != 0 && source.pos == n && sink.pos == 0 && source.pos == before) {
            err = "truncated zstd frame";
            break;
        }
        if (remaining == 0 && source.pos < n) {
            ZSTD_initDStream(ds);
            remaining = 1;
        }
    }
    if (err == NULL && n == 0)
        err = "empty zstd input";
    ZSTD_freeDStream(ds);
    finish(thread, 2, &b, err);
}

static void stdBrotliCompress(ZamThread *thread) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    int quality = (int)(ZamInt)zam_native_argument(thread, 2);
    if (quality < BROTLI_MIN_QUALITY || quality > BROTLI_MAX_QUALITY)
        zam_native_panic(thread, "Brotli quality must be 0 through 11");
    CodecBuffer b = {0};
    size_t size = BrotliEncoderMaxCompressedSize(n);
    reserve(&b, size == 0 ? n + 1024 : size);
    size = b.capacity;
    if (!BrotliEncoderCompress(quality, BROTLI_DEFAULT_WINDOW, BROTLI_MODE_GENERIC, n, in,
                               &size, (uint8_t *)b.bytes)) {
        finish(thread, 3, &b, "Brotli compression failed");
        return;
    }
    b.length = size;
    finish(thread, 3, &b, NULL);
}

static void stdBrotliDecompress(ZamThread *thread) {
    size_t n;
    const uint8_t *in = input(thread, &n);
    BrotliDecoderState *state = BrotliDecoderCreateInstance(NULL, NULL, NULL);
    if (state == NULL)
        zam_native_panic(thread, "BrotliDecoderCreateInstance failed");
    CodecBuffer b = {0};
    size_t availableIn = n;
    const uint8_t *nextIn = in;
    BrotliDecoderResult result;
    do {
        reserve(&b, 65536);
        size_t availableOut = b.capacity - b.length;
        uint8_t *nextOut = (uint8_t *)b.bytes + b.length;
        result = BrotliDecoderDecompressStream(state, &availableIn, &nextIn, &availableOut,
                                               &nextOut, NULL);
        b.length = (size_t)((char *)nextOut - b.bytes);
    } while (result == BROTLI_DECODER_RESULT_NEEDS_MORE_OUTPUT);
    const char *err = NULL;
    if (result == BROTLI_DECODER_RESULT_ERROR)
        err = BrotliDecoderErrorString(BrotliDecoderGetErrorCode(state));
    else if (result == BROTLI_DECODER_RESULT_NEEDS_MORE_INPUT)
        err = "truncated Brotli stream";
    else if (availableIn != 0)
        err = "trailing data after the Brotli stream";
    BrotliDecoderDestroyInstance(state);
    finish(thread, 2, &b, err);
}

const ZamNativeEntry zamCompressNativeTable[] = {
    {"STD_CODEC_TAKE", stdCodecTake},
    {"STD_GZIP_COMPRESS", stdGzipCompress},
    {"STD_GZIP_DECOMPRESS", stdGzipDecompress},
    {"STD_ZLIB_COMPRESS", stdZlibCompress},
    {"STD_ZLIB_DECOMPRESS", stdZlibDecompress},
    {"STD_LZ4_COMPRESS", stdLz4Compress},
    {"STD_LZ4_DECOMPRESS", stdLz4Decompress},
    {"STD_ZSTD_COMPRESS", stdZstdCompress},
    {"STD_ZSTD_DECOMPRESS", stdZstdDecompress},
    {"STD_BROTLI_COMPRESS", stdBrotliCompress},
    {"STD_BROTLI_DECOMPRESS", stdBrotliDecompress},
};

const uint64_t zamCompressNativeTableCount =
    sizeof(zamCompressNativeTable) / sizeof(zamCompressNativeTable[0]);
