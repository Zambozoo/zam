#include <openssl/evp.h>
#include <openssl/rand.h>

#include "codec.h"

// The WebSocket natives: the handshake's keys, and building and unmasking
// frames, the byte work of RFC 6455 the zam side leaves to C.

static const char wsGuid[] = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

static void randomBytes(ZamThread *thread, unsigned char *out, int n) {
    if (RAND_bytes(out, n) != 1)
        zam_native_panic(thread, "no random bytes for a WebSocket");
}

// stdWsKey makes a client's Sec-WebSocket-Key: 16 random bytes, in base64.
static void stdWsKey(ZamThread *thread) {
    unsigned char raw[16];
    char key[32];
    randomBytes(thread, raw, sizeof raw);
    int n = EVP_EncodeBlock((unsigned char *)key, raw, sizeof raw);
    codec_write_string(thread, 0, key, (size_t)n);
    zam_native_return_void(thread);
}

// stdWsAccept answers the Sec-WebSocket-Accept for a key: the base64 of the
// SHA-1 of the key and the protocol's GUID.
static void stdWsAccept(ZamThread *thread) {
    const ZamString *key = codec_string(thread, 0);
    unsigned char digest[EVP_MAX_MD_SIZE];
    unsigned int digestLength = 0;
    EVP_MD_CTX *ctx = EVP_MD_CTX_new();
    bool ok = ctx != NULL && EVP_DigestInit_ex(ctx, EVP_sha1(), NULL) == 1 &&
              EVP_DigestUpdate(ctx, key->bytes, key->length) == 1 &&
              EVP_DigestUpdate(ctx, wsGuid, sizeof wsGuid - 1) == 1 &&
              EVP_DigestFinal_ex(ctx, digest, &digestLength) == 1;
    EVP_MD_CTX_free(ctx);
    if (!ok)
        zam_native_panic(thread, "cannot hash a WebSocket key");
    char accept[64];
    int n = EVP_EncodeBlock((unsigned char *)accept, digest, (int)digestLength);
    codec_write_string(thread, 1, accept, (size_t)n);
    zam_native_return_void(thread);
}

// stdWsFrame writes one final frame of opcode carrying n payload bytes into
// out, which has room for n + 14, masking it with a random key when told
// to, and answers the frame's length.
static void stdWsFrame(ZamThread *thread) {
    ZamInt opcode = (ZamInt)zam_native_argument(thread, 0);
    bool mask = zam_native_argument(thread, 1) != 0;
    ZamInt n = (ZamInt)zam_native_argument(thread, 3);
    unsigned char *out = zam_native_argument_pointer(thread, 4);
    size_t at = 0;
    out[at++] = (unsigned char)(0x80 | (opcode & 0x0f));
    unsigned char maskBit = mask ? 0x80 : 0;
    if (n < 126)
        out[at++] = maskBit | (unsigned char)n;
    else if (n < 65536) {
        out[at++] = maskBit | 126;
        out[at++] = (unsigned char)(n >> 8);
        out[at++] = (unsigned char)n;
    } else {
        out[at++] = maskBit | 127;
        for (int shift = 56; shift >= 0; shift -= 8)
            out[at++] = (unsigned char)((uint64_t)n >> shift);
    }
    const unsigned char *payload = n > 0 ? zam_native_argument_pointer(thread, 2) : NULL;
    if (mask) {
        unsigned char key[4];
        randomBytes(thread, key, sizeof key);
        memcpy(out + at, key, sizeof key);
        at += sizeof key;
        for (ZamInt i = 0; i < n; i++)
            out[at + (size_t)i] = payload[i] ^ key[i & 3];
    } else if (n > 0)
        memcpy(out + at, payload, (size_t)n);
    *(ZamInt *)zam_native_argument_pointer(thread, 5) = (ZamInt)at + n;
    zam_native_return_void(thread);
}

// stdWsHeader reads a frame's head from the n bytes at b, answering (fin,
// opcode, headLength, payloadLength), headLength being 0 while b holds too
// little of it to tell.
static void stdWsHeader(ZamThread *thread) {
    const unsigned char *b = zam_native_argument_pointer(thread, 0);
    ZamInt n = (ZamInt)zam_native_argument(thread, 1);
    bool fin = false;
    ZamInt opcode = 0, head = 0, length = 0;
    if (n >= 2) {
        fin = (b[0] & 0x80) != 0;
        opcode = b[0] & 0x0f;
        uint64_t len = b[1] & 0x7f;
        ZamInt need = 2 + (len == 126 ? 2 : len == 127 ? 8 : 0) + ((b[1] & 0x80) ? 4 : 0);
        if (n >= need) {
            if (len == 126)
                len = ((uint64_t)b[2] << 8) | b[3];
            else if (len == 127) {
                len = 0;
                for (int i = 0; i < 8; i++)
                    len = (len << 8) | b[2 + i];
            }
            head = need;
            length = len > (uint64_t)INT64_MAX ? INT64_MAX : (ZamInt)len;
        }
    }
    codec_write_bool(thread, 2, fin);
    codec_write_word(thread, 3, (ZamWord)opcode);
    codec_write_word(thread, 4, (ZamWord)head);
    codec_write_word(thread, 5, (ZamWord)length);
    zam_native_return_void(thread);
}

// stdWsUnmask unmasks n payload bytes at b in place with the four-byte key
// at key.
static void stdWsUnmask(ZamThread *thread) {
    unsigned char *b = zam_native_argument_pointer(thread, 0);
    ZamInt n = (ZamInt)zam_native_argument(thread, 1);
    const unsigned char *key = zam_native_argument_pointer(thread, 2);
    unsigned char k[4];
    memcpy(k, key, sizeof k);
    for (ZamInt i = 0; i < n; i++)
        b[i] ^= k[i & 3];
    zam_native_return_void(thread);
}

const ZamNativeEntry zamWsNativeTable[] = {
    {"STD_WS_KEY", stdWsKey},
    {"STD_WS_ACCEPT", stdWsAccept},
    {"STD_WS_FRAME", stdWsFrame},
    {"STD_WS_HEADER", stdWsHeader},
    {"STD_WS_UNMASK", stdWsUnmask},
};

const uint64_t zamWsNativeTableCount = sizeof(zamWsNativeTable) / sizeof(zamWsNativeTable[0]);
