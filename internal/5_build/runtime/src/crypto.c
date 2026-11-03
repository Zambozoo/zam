#include <crypt.h>
#include <openssl/core_names.h>
#include <openssl/decoder.h>
#include <openssl/encoder.h>
#include <openssl/hpke.h>
#include <openssl/rsa.h>
#include <openssl/crypto.h>
#include <openssl/err.h>
#include <openssl/evp.h>
#include <openssl/kdf.h>
#include <openssl/params.h>
#include <openssl/rand.h>
#include <sodium.h>
#include <stdio.h>

#include "codec.h"
#include "crypto.h"

// The crypto natives: randomness, digests, MACs, key derivation and ciphers,
// over OpenSSL, with libsodium for the Salsa20 family and Argon2 strings and
// libxcrypt for bcrypt.

static const unsigned char emptyBytes[1];

ZamInt cryptoLength(ZamThread *thread, uint64_t wordIndex) {
    ZamInt n = (ZamInt)zam_native_argument(thread, wordIndex);
    return n < 0 ? 0 : n;
}

// cryptoBytes resolves the byte pointer at wordIndex, n bytes long, to
// something a library will read even when n is zero.
const unsigned char *cryptoBytes(ZamThread *thread, uint64_t wordIndex, ZamInt n) {
    if (n == 0)
        return emptyBytes;
    return zam_native_argument_pointer(thread, wordIndex);
}

// cryptoDigests are the OpenSSL names of Digest.Kind's values, in order.
static const char *const cryptoDigests[] = {
    "MD5", "SHA1", "SHA224", "SHA256", "SHA384", "SHA512", "SHA512-256",
    "SHA3-224", "SHA3-256", "SHA3-384", "SHA3-512", "SHAKE128", "SHAKE256",
    "BLAKE2B-512", "BLAKE2S-256",
};

// cryptoDigest names the Digest.Kind at wordIndex, or answers NULL for none.
static const char *cryptoDigest(ZamThread *thread, uint64_t wordIndex) {
    ZamInt kind = (ZamInt)zam_native_argument(thread, wordIndex);
    if (kind < 0 || kind >= (ZamInt)(sizeof cryptoDigests / sizeof cryptoDigests[0]))
        return NULL;
    return cryptoDigests[kind];
}

// cryptoFail writes what failed, with OpenSSL's reason when it has one, to
// the error slot at wordIndex.
void cryptoFail(ZamThread *thread, uint64_t wordIndex, const char *what) {
    char message[256];
    unsigned long code = ERR_get_error();
    const char *reason = code == 0 ? NULL : ERR_reason_error_string(code);
    ERR_clear_error();
    if (reason != NULL)
        snprintf(message, sizeof message, "%s: %s", what, reason);
    else
        snprintf(message, sizeof message, "%s", what);
    codec_write_error(thread, wordIndex, message);
}

static void cryptoSodium(ZamThread *thread) {
    if (sodium_init() < 0)
        zam_native_panic(thread, "cannot start libsodium");
}

// stdCryptoRand fills n bytes at b from the system's secure generator.
static void stdCryptoRand(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 1);
    if (n > 0 && RAND_bytes(zam_native_argument_pointer(thread, 0), (int)n) != 1)
        zam_native_panic(thread, "no secure random bytes");
    zam_native_return_void(thread);
}

// stdCryptoEqual compares n bytes at a and b in time independent of their
// contents.
static void stdCryptoEqual(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 2);
    bool equal = CRYPTO_memcmp(cryptoBytes(thread, 0, n), cryptoBytes(thread, 1, n), (size_t)n) == 0;
    codec_write_bool(thread, 3, equal);
    zam_native_return_void(thread);
}

// CryptoDigest is a running digest, with the length it finishes to, which
// only an extendable-output function lets the caller choose freely.
typedef struct CryptoDigest {
    EVP_MD_CTX *ctx;
    EVP_MD *md;
    size_t size;
    bool xof;
    bool sized;
} CryptoDigest;

static int cryptoDigestInit(CryptoDigest *d) {
    size_t size = d->size;
    OSSL_PARAM params[2] = {OSSL_PARAM_END, OSSL_PARAM_END};
    if (d->sized)
        params[0] = OSSL_PARAM_construct_size_t(OSSL_DIGEST_PARAM_SIZE, &size);
    return EVP_DigestInit_ex2(d->ctx, d->md, params);
}

static void cryptoDigestFree(CryptoDigest *d) {
    EVP_MD_CTX_free(d->ctx);
    EVP_MD_free(d->md);
    free(d);
}

// stdHashNew starts a digest of a Digest.Kind, size bytes long or of its
// default length for 0, answering (handle, size, blockSize, err).
static void stdHashNew(ZamThread *thread) {
    const char *name = cryptoDigest(thread, 0);
    ZamInt size = (ZamInt)zam_native_argument(thread, 1);
    codec_write_word(thread, 2, 0);
    CryptoDigest *d = calloc(1, sizeof *d);
    if (d == NULL)
        zam_native_panic(thread, "out of memory for a digest");
    d->md = name == NULL ? NULL : EVP_MD_fetch(NULL, name, NULL);
    d->ctx = EVP_MD_CTX_new();
    if (d->md == NULL || d->ctx == NULL) {
        cryptoDigestFree(d);
        cryptoFail(thread, 5, "unknown digest");
        zam_native_return_void(thread);
        return;
    }
    d->xof = (EVP_MD_get_flags(d->md) & EVP_MD_FLAG_XOF) != 0;
    d->size = (size_t)EVP_MD_get_size(d->md);
    if (size > 0) {
        d->size = (size_t)size;
        d->sized = !d->xof;
    } else if (d->xof) {
        cryptoDigestFree(d);
        codec_write_error(thread, 5, "an extendable-output digest needs a size");
        zam_native_return_void(thread);
        return;
    }
    if (cryptoDigestInit(d) != 1 || (d->sized && (size_t)EVP_MD_CTX_get_size_ex(d->ctx) != d->size)) {
        cryptoDigestFree(d);
        cryptoFail(thread, 5, "unsupported digest size");
        zam_native_return_void(thread);
        return;
    }
    codec_write_word(thread, 2, (ZamWord)(uintptr_t)d);
    codec_write_word(thread, 3, (ZamWord)d->size);
    codec_write_word(thread, 4, (ZamWord)EVP_MD_get_block_size(d->md));
    codec_write_error(thread, 5, NULL);
    zam_native_return_void(thread);
}

static void stdHashWrite(ZamThread *thread) {
    CryptoDigest *d = codec_handle(thread, 0);
    ZamInt n = cryptoLength(thread, 2);
    if (n > 0 && EVP_DigestUpdate(d->ctx, zam_native_argument_pointer(thread, 1), (size_t)n) != 1)
        zam_native_panic(thread, "cannot update a digest");
    zam_native_return_void(thread);
}

// stdHashSum writes the digest of everything written so far to out, leaving
// the digest running.
static void stdHashSum(ZamThread *thread) {
    CryptoDigest *d = codec_handle(thread, 0);
    unsigned char *out = zam_native_argument_pointer(thread, 1);
    EVP_MD_CTX *copy = EVP_MD_CTX_new();
    bool ok = copy != NULL && EVP_MD_CTX_copy_ex(copy, d->ctx) == 1;
    if (ok && d->xof)
        ok = EVP_DigestFinalXOF(copy, out, d->size) == 1;
    else if (ok)
        ok = EVP_DigestFinal_ex(copy, out, NULL) == 1;
    EVP_MD_CTX_free(copy);
    if (!ok)
        zam_native_panic(thread, "cannot finish a digest");
    zam_native_return_void(thread);
}

static void stdHashReset(ZamThread *thread) {
    if (cryptoDigestInit(codec_handle(thread, 0)) != 1)
        zam_native_panic(thread, "cannot reset a digest");
    zam_native_return_void(thread);
}

static void stdHashFree(ZamThread *thread) {
    CryptoDigest *d = codec_handle(thread, 0);
    if (d != NULL)
        cryptoDigestFree(d);
    zam_native_return_void(thread);
}

// cryptoMacNew starts the OpenSSL MAC named under the key at keyIndex, over
// digest for those that take one, answering (handle, size, blockSize, err)
// from outIndex on.
static void cryptoMacNew(ZamThread *thread, const char *name, const char *digest, uint64_t keyIndex,
                         uint64_t outIndex) {
    ZamInt keyLength = cryptoLength(thread, keyIndex + 1);
    const unsigned char *key = cryptoBytes(thread, keyIndex, keyLength);
    codec_write_word(thread, outIndex, 0);
    EVP_MAC *mac = EVP_MAC_fetch(NULL, name, NULL);
    EVP_MAC_CTX *ctx = mac == NULL ? NULL : EVP_MAC_CTX_new(mac);
    EVP_MAC_free(mac);
    OSSL_PARAM params[2] = {OSSL_PARAM_END, OSSL_PARAM_END};
    if (digest != NULL)
        params[0] = OSSL_PARAM_construct_utf8_string(OSSL_MAC_PARAM_DIGEST, (char *)digest, 0);
    if (ctx == NULL || EVP_MAC_init(ctx, key, (size_t)keyLength, params) != 1) {
        EVP_MAC_CTX_free(ctx);
        cryptoFail(thread, outIndex + 3, "cannot start a MAC");
        zam_native_return_void(thread);
        return;
    }
    codec_write_word(thread, outIndex, (ZamWord)(uintptr_t)ctx);
    codec_write_word(thread, outIndex + 1, (ZamWord)EVP_MAC_CTX_get_mac_size(ctx));
    codec_write_word(thread, outIndex + 2, (ZamWord)EVP_MAC_CTX_get_block_size(ctx));
    codec_write_error(thread, outIndex + 3, NULL);
    zam_native_return_void(thread);
}

// stdHmacNew starts an HMAC over a Digest.Kind, from (kind, key, keyLength),
// answering (handle, size, blockSize, err).
static void stdHmacNew(ZamThread *thread) {
    const char *digest = cryptoDigest(thread, 0);
    if (digest == NULL) {
        codec_write_word(thread, 3, 0);
        codec_write_error(thread, 6, "unknown digest");
        zam_native_return_void(thread);
        return;
    }
    cryptoMacNew(thread, "HMAC", digest, 1, 3);
}

// stdPoly1305New starts a Poly1305 authenticator, from (key, keyLength),
// answering (handle, size, blockSize, err).
static void stdPoly1305New(ZamThread *thread) {
    cryptoMacNew(thread, "POLY1305", NULL, 0, 2);
}

static void stdMacWrite(ZamThread *thread) {
    EVP_MAC_CTX *ctx = codec_handle(thread, 0);
    ZamInt n = cryptoLength(thread, 2);
    if (n > 0 && EVP_MAC_update(ctx, zam_native_argument_pointer(thread, 1), (size_t)n) != 1)
        zam_native_panic(thread, "cannot update a MAC");
    zam_native_return_void(thread);
}

// stdMacSum writes the MAC of everything written so far to out, leaving the
// MAC running.
static void stdMacSum(ZamThread *thread) {
    EVP_MAC_CTX *ctx = codec_handle(thread, 0);
    unsigned char *out = zam_native_argument_pointer(thread, 1);
    EVP_MAC_CTX *copy = EVP_MAC_CTX_dup(ctx);
    size_t size = EVP_MAC_CTX_get_mac_size(ctx);
    bool ok = copy != NULL && EVP_MAC_final(copy, out, &size, size) == 1;
    EVP_MAC_CTX_free(copy);
    if (!ok)
        zam_native_panic(thread, "cannot finish a MAC");
    zam_native_return_void(thread);
}

static void stdMacReset(ZamThread *thread) {
    if (EVP_MAC_init(codec_handle(thread, 0), NULL, 0, NULL) != 1)
        zam_native_panic(thread, "cannot reset a MAC");
    zam_native_return_void(thread);
}

static void stdMacFree(ZamThread *thread) {
    EVP_MAC_CTX_free(codec_handle(thread, 0));
    zam_native_return_void(thread);
}

// cryptoDerive runs the KDF named over params into the outLength bytes at
// the pointer at outIndex, writing an error to errIndex when it fails.
static void cryptoDerive(ZamThread *thread, const char *name, OSSL_PARAM *params,
                         uint64_t outIndex, ZamInt outLength, uint64_t errIndex) {
    EVP_KDF *kdf = EVP_KDF_fetch(NULL, name, NULL);
    EVP_KDF_CTX *ctx = kdf == NULL ? NULL : EVP_KDF_CTX_new(kdf);
    EVP_KDF_free(kdf);
    bool ok = ctx != NULL && outLength > 0 &&
              EVP_KDF_derive(ctx, zam_native_argument_pointer(thread, outIndex), (size_t)outLength, params) == 1;
    EVP_KDF_CTX_free(ctx);
    if (ok)
        codec_write_error(thread, errIndex, NULL);
    else
        cryptoFail(thread, errIndex, "cannot derive a key");
}

static OSSL_PARAM cryptoOctets(const char *key, const unsigned char *b, ZamInt n) {
    return OSSL_PARAM_construct_octet_string(key, (void *)b, (size_t)n);
}

// stdHkdf derives outLength bytes from (Digest.Kind, mode, key, salt, info), the
// mode being 0 to extract and expand, 2 to expand a pseudorandom key only.
static void stdHkdf(ZamThread *thread) {
    const char *digest = cryptoDigest(thread, 0);
    int mode = (int)(ZamInt)zam_native_argument(thread, 1);
    ZamInt keyLength = cryptoLength(thread, 3);
    ZamInt saltLength = cryptoLength(thread, 5);
    ZamInt infoLength = cryptoLength(thread, 7);
    ZamInt outLength = cryptoLength(thread, 9);
    OSSL_PARAM params[6];
    int p = 0;
    params[p++] = OSSL_PARAM_construct_utf8_string(OSSL_KDF_PARAM_DIGEST, (char *)(digest == NULL ? "" : digest), 0);
    params[p++] = OSSL_PARAM_construct_int(OSSL_KDF_PARAM_MODE, &mode);
    params[p++] = cryptoOctets(OSSL_KDF_PARAM_KEY, cryptoBytes(thread, 2, keyLength), keyLength);
    if (saltLength > 0)
        params[p++] = cryptoOctets(OSSL_KDF_PARAM_SALT, cryptoBytes(thread, 4, saltLength), saltLength);
    params[p++] = cryptoOctets(OSSL_KDF_PARAM_INFO, cryptoBytes(thread, 6, infoLength), infoLength);
    params[p] = OSSL_PARAM_construct_end();
    cryptoDerive(thread, "HKDF", params, 8, outLength, 10);
    zam_native_return_void(thread);
}

// stdPbkdf2 derives outLength bytes from (Digest.Kind, password, salt,
// iterations).
static void stdPbkdf2(ZamThread *thread) {
    const char *digest = cryptoDigest(thread, 0);
    ZamInt passwordLength = cryptoLength(thread, 2);
    ZamInt saltLength = cryptoLength(thread, 4);
    uint64_t iterations = zam_native_argument(thread, 5);
    ZamInt outLength = cryptoLength(thread, 7);
    OSSL_PARAM params[] = {
        OSSL_PARAM_construct_utf8_string(OSSL_KDF_PARAM_DIGEST, (char *)(digest == NULL ? "" : digest), 0),
        cryptoOctets(OSSL_KDF_PARAM_PASSWORD, cryptoBytes(thread, 1, passwordLength), passwordLength),
        cryptoOctets(OSSL_KDF_PARAM_SALT, cryptoBytes(thread, 3, saltLength), saltLength),
        OSSL_PARAM_construct_uint64(OSSL_KDF_PARAM_ITER, &iterations),
        OSSL_PARAM_construct_end(),
    };
    cryptoDerive(thread, "PBKDF2", params, 6, outLength, 8);
    zam_native_return_void(thread);
}

// stdScrypt derives outLength bytes from (password, salt, N, r, p).
static void stdScrypt(ZamThread *thread) {
    ZamInt passwordLength = cryptoLength(thread, 1);
    ZamInt saltLength = cryptoLength(thread, 3);
    uint64_t n = zam_native_argument(thread, 4);
    uint32_t r = (uint32_t)zam_native_argument(thread, 5);
    uint32_t p = (uint32_t)zam_native_argument(thread, 6);
    ZamInt outLength = cryptoLength(thread, 8);
    uint64_t maxMemory = UINT64_MAX;
    OSSL_PARAM params[] = {
        cryptoOctets(OSSL_KDF_PARAM_PASSWORD, cryptoBytes(thread, 0, passwordLength), passwordLength),
        cryptoOctets(OSSL_KDF_PARAM_SALT, cryptoBytes(thread, 2, saltLength), saltLength),
        OSSL_PARAM_construct_uint64(OSSL_KDF_PARAM_SCRYPT_N, &n),
        OSSL_PARAM_construct_uint32(OSSL_KDF_PARAM_SCRYPT_R, &r),
        OSSL_PARAM_construct_uint32(OSSL_KDF_PARAM_SCRYPT_P, &p),
        OSSL_PARAM_construct_uint64(OSSL_KDF_PARAM_SCRYPT_MAXMEM, &maxMemory),
        OSSL_PARAM_construct_end(),
    };
    cryptoDerive(thread, "SCRYPT", params, 7, outLength, 9);
    zam_native_return_void(thread);
}

// stdArgon2 derives outLength bytes with Argon2id from (password, salt,
// passes, memory in KiB, lanes).
static void stdArgon2(ZamThread *thread) {
    ZamInt passwordLength = cryptoLength(thread, 1);
    ZamInt saltLength = cryptoLength(thread, 3);
    uint32_t passes = (uint32_t)zam_native_argument(thread, 4);
    uint32_t memory = (uint32_t)zam_native_argument(thread, 5);
    uint32_t lanes = (uint32_t)zam_native_argument(thread, 6);
    ZamInt outLength = cryptoLength(thread, 8);
    OSSL_PARAM params[] = {
        cryptoOctets(OSSL_KDF_PARAM_PASSWORD, cryptoBytes(thread, 0, passwordLength), passwordLength),
        cryptoOctets(OSSL_KDF_PARAM_SALT, cryptoBytes(thread, 2, saltLength), saltLength),
        OSSL_PARAM_construct_uint32(OSSL_KDF_PARAM_ITER, &passes),
        OSSL_PARAM_construct_uint32(OSSL_KDF_PARAM_ARGON2_MEMCOST, &memory),
        OSSL_PARAM_construct_uint32(OSSL_KDF_PARAM_ARGON2_LANES, &lanes),
        OSSL_PARAM_construct_end(),
    };
    cryptoDerive(thread, "ARGON2ID", params, 7, outLength, 9);
    zam_native_return_void(thread);
}

// stdArgon2Hash hashes a password with Argon2id under a fresh salt into the
// PHC string form, from (password, passes, memory in KiB).
static void stdArgon2Hash(ZamThread *thread) {
    cryptoSodium(thread);
    ZamInt passwordLength = cryptoLength(thread, 1);
    uint64_t passes = zam_native_argument(thread, 2);
    uint64_t memory = zam_native_argument(thread, 3);
    char hash[crypto_pwhash_STRBYTES];
    if (memory > SIZE_MAX / 1024 ||
        crypto_pwhash_str_alg(hash, (const char *)cryptoBytes(thread, 0, passwordLength),
                              (unsigned long long)passwordLength, passes, (size_t)memory * 1024,
                              crypto_pwhash_ALG_ARGON2ID13) != 0) {
        codec_write_word(thread, 4, 0);
        codec_write_error(thread, 5, "cannot hash a password: out of range or out of memory");
        zam_native_return_void(thread);
        return;
    }
    codec_write_error(thread, 5, NULL);
    codec_write_string(thread, 4, hash, strlen(hash));
    zam_native_return_void(thread);
}

// stdArgon2Verify reports whether a password matches an Argon2 PHC string.
static void stdArgon2Verify(ZamThread *thread) {
    cryptoSodium(thread);
    char *hash = codec_c_string(thread, 0);
    ZamInt passwordLength = cryptoLength(thread, 2);
    bool ok = crypto_pwhash_str_verify(hash, (const char *)cryptoBytes(thread, 1, passwordLength),
                                       (unsigned long long)passwordLength) == 0;
    free(hash);
    codec_write_bool(thread, 3, ok);
    zam_native_return_void(thread);
}

// cryptoPassword copies a password into a NUL-terminated string for crypt,
// or answers NULL when it holds a NUL of its own.
static char *cryptoPassword(ZamThread *thread, uint64_t wordIndex, ZamInt n) {
    const unsigned char *b = cryptoBytes(thread, wordIndex, n);
    if (memchr(b, 0, (size_t)n) != NULL)
        return NULL;
    char *c = malloc((size_t)n + 1);
    if (c == NULL)
        zam_native_panic(thread, "out of memory copying a password");
    memcpy(c, b, (size_t)n);
    c[n] = '\0';
    return c;
}

// cryptoCrypt hashes password under setting, answering whether it could.
static bool cryptoCrypt(ZamThread *thread, const char *password, const char *setting, char *out, size_t outSize) {
    struct crypt_data *data = calloc(1, sizeof *data);
    if (data == NULL)
        zam_native_panic(thread, "out of memory for bcrypt");
    const char *hash = crypt_rn(password, setting, data, (int)sizeof *data);
    bool ok = hash != NULL && hash[0] != '*' && strlen(hash) < outSize;
    if (ok)
        strcpy(out, hash);
    OPENSSL_cleanse(data, sizeof *data);
    free(data);
    return ok;
}

// stdBcryptHash hashes a password with bcrypt at cost under a fresh salt.
static void stdBcryptHash(ZamThread *thread) {
    ZamInt passwordLength = cryptoLength(thread, 1);
    ZamInt cost = (ZamInt)zam_native_argument(thread, 2);
    codec_write_word(thread, 3, 0);
    if (passwordLength > 72) {
        codec_write_error(thread, 4, "a bcrypt password is at most 72 bytes");
        zam_native_return_void(thread);
        return;
    }
    char *password = cryptoPassword(thread, 0, passwordLength);
    if (password == NULL) {
        codec_write_error(thread, 4, "a bcrypt password cannot hold a NUL byte");
        zam_native_return_void(thread);
        return;
    }
    char setting[CRYPT_GENSALT_OUTPUT_SIZE];
    char hash[CRYPT_OUTPUT_SIZE];
    bool ok = cost >= 4 && cost <= 31 &&
              crypt_gensalt_rn("$2b$", (unsigned long)cost, NULL, 0, setting, sizeof setting) != NULL &&
              cryptoCrypt(thread, password, setting, hash, sizeof hash);
    OPENSSL_cleanse(password, (size_t)passwordLength);
    free(password);
    if (!ok) {
        codec_write_error(thread, 4, "cannot hash a password with bcrypt: the cost runs from 4 to 31");
        zam_native_return_void(thread);
        return;
    }
    codec_write_error(thread, 4, NULL);
    codec_write_string(thread, 3, hash, strlen(hash));
    zam_native_return_void(thread);
}

// stdBcryptVerify reports whether a password matches a bcrypt hash.
static void stdBcryptVerify(ZamThread *thread) {
    char *expected = codec_c_string(thread, 0);
    ZamInt passwordLength = cryptoLength(thread, 2);
    char *password = passwordLength > 72 ? NULL : cryptoPassword(thread, 1, passwordLength);
    char hash[CRYPT_OUTPUT_SIZE];
    bool ok = password != NULL && strncmp(expected, "$2", 2) == 0 &&
              cryptoCrypt(thread, password, expected, hash, sizeof hash) &&
              strlen(hash) == strlen(expected) && CRYPTO_memcmp(hash, expected, strlen(hash)) == 0;
    if (password != NULL) {
        OPENSSL_cleanse(password, (size_t)passwordLength);
        free(password);
    }
    free(expected);
    codec_write_bool(thread, 3, ok);
    zam_native_return_void(thread);
}

// CryptoAead is AEAD.Kind.
typedef enum CryptoAead {
    cryptoAesGcm,
    cryptoChaCha20Poly1305,
    cryptoXChaCha20Poly1305,
    cryptoXSalsa20Poly1305,
} CryptoAead;

// cryptoAeadCipher names the OpenSSL cipher for an AEAD, or NULL for those
// libsodium serves.
static const char *cryptoAeadCipher(ZamInt kind, ZamInt keyLength) {
    switch (kind) {
    case cryptoAesGcm:
        return keyLength == 16 ? "AES-128-GCM" : keyLength == 24 ? "AES-192-GCM" : "AES-256-GCM";
    case cryptoChaCha20Poly1305:
        return "ChaCha20-Poly1305";
    }
    return NULL;
}

// cryptoEvpAead seals or opens n bytes at in into out with an OpenSSL AEAD,
// the tag following the ciphertext.
static bool cryptoEvpAead(const char *cipherName, bool seal, const unsigned char *key,
                          const unsigned char *nonce, ZamInt nonceLength, const unsigned char *in,
                          ZamInt n, const unsigned char *aad, ZamInt aadLength, unsigned char *out) {
    const ZamInt tagLength = 16;
    ZamInt bodyLength = seal ? n : n - tagLength;
    EVP_CIPHER *cipher = EVP_CIPHER_fetch(NULL, cipherName, NULL);
    EVP_CIPHER_CTX *ctx = EVP_CIPHER_CTX_new();
    size_t ivLength = (size_t)nonceLength;
    OSSL_PARAM params[] = {
        OSSL_PARAM_construct_size_t(OSSL_CIPHER_PARAM_AEAD_IVLEN, &ivLength),
        OSSL_PARAM_construct_end(),
    };
    int written = 0;
    bool ok = cipher != NULL && ctx != NULL && bodyLength >= 0 &&
              EVP_CipherInit_ex2(ctx, cipher, NULL, NULL, seal, params) == 1 &&
              EVP_CipherInit_ex2(ctx, NULL, key, nonce, seal, NULL) == 1 &&
              (aadLength == 0 || EVP_CipherUpdate(ctx, NULL, &written, aad, (int)aadLength) == 1) &&
              (bodyLength == 0 || EVP_CipherUpdate(ctx, out, &written, in, (int)bodyLength) == 1);
    if (ok && seal)
        ok = EVP_CipherFinal_ex(ctx, out + bodyLength, &written) == 1 &&
             EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_GET_TAG, (int)tagLength, out + bodyLength) == 1;
    else if (ok)
        ok = EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_SET_TAG, (int)tagLength, (void *)(in + bodyLength)) == 1 &&
             EVP_CipherFinal_ex(ctx, out + bodyLength, &written) == 1;
    EVP_CIPHER_CTX_free(ctx);
    EVP_CIPHER_free(cipher);
    ERR_clear_error();
    return ok;
}

// cryptoSodiumAead seals or opens with an AEAD libsodium serves.
static bool cryptoSodiumAead(ZamInt kind, bool seal, const unsigned char *key,
                             const unsigned char *nonce, const unsigned char *in, ZamInt n,
                             const unsigned char *aad, ZamInt aadLength, unsigned char *out) {
    if (kind == cryptoXChaCha20Poly1305) {
        if (seal)
            return crypto_aead_xchacha20poly1305_ietf_encrypt(out, NULL, in, (unsigned long long)n, aad,
                                                              (unsigned long long)aadLength, NULL, nonce, key) == 0;
        return crypto_aead_xchacha20poly1305_ietf_decrypt(out, NULL, NULL, in, (unsigned long long)n, aad,
                                                          (unsigned long long)aadLength, nonce, key) == 0;
    }
    if (kind == cryptoXSalsa20Poly1305 && aadLength == 0) {
        if (seal)
            return crypto_secretbox_easy(out, in, (unsigned long long)n, nonce, key) == 0;
        return n >= crypto_secretbox_MACBYTES &&
               crypto_secretbox_open_easy(out, in, (unsigned long long)n, nonce, key) == 0;
    }
    return false;
}

// stdAead seals or opens n bytes with an AEAD.Kind, from (kind, seal, key,
// keyLength, nonce, nonceLength, in, n, aad, aadLength, out), answering
// whether it could: an open fails when the message was not authentic.
static void stdAead(ZamThread *thread) {
    ZamInt kind = (ZamInt)zam_native_argument(thread, 0);
    bool seal = zam_native_argument(thread, 1) != 0;
    ZamInt keyLength = cryptoLength(thread, 3);
    ZamInt nonceLength = cryptoLength(thread, 5);
    ZamInt n = cryptoLength(thread, 7);
    ZamInt aadLength = cryptoLength(thread, 9);
    const unsigned char *key = cryptoBytes(thread, 2, keyLength);
    const unsigned char *nonce = cryptoBytes(thread, 4, nonceLength);
    const unsigned char *in = cryptoBytes(thread, 6, n);
    const unsigned char *aad = cryptoBytes(thread, 8, aadLength);
    unsigned char *out = zam_native_argument_pointer(thread, 10);
    const char *cipherName = cryptoAeadCipher(kind, keyLength);
    bool ok;
    if (cipherName != NULL)
        ok = cryptoEvpAead(cipherName, seal, key, nonce, nonceLength, in, n, aad, aadLength, out);
    else {
        cryptoSodium(thread);
        ok = cryptoSodiumAead(kind, seal, key, nonce, in, n, aad, aadLength, out);
    }
    codec_write_bool(thread, 11, ok);
    zam_native_return_void(thread);
}

// CryptoAesMode is AES.Mode.
typedef enum CryptoAesMode {
    cryptoCbc,
    cryptoCtr,
} CryptoAesMode;

// stdAes runs n bytes through AES in an AES.Mode into out, which has room
// for n and a block more, from (mode, encrypt, key, keyLength, iv, in, n,
// out), answering (written, err).
static void stdAes(ZamThread *thread) {
    ZamInt mode = (ZamInt)zam_native_argument(thread, 0);
    bool encrypt = zam_native_argument(thread, 1) != 0;
    ZamInt keyLength = cryptoLength(thread, 3);
    ZamInt n = cryptoLength(thread, 6);
    const unsigned char *key = cryptoBytes(thread, 2, keyLength);
    const unsigned char *iv = zam_native_argument_pointer(thread, 4);
    const unsigned char *in = cryptoBytes(thread, 5, n);
    unsigned char *out = zam_native_argument_pointer(thread, 7);
    char name[32];
    snprintf(name, sizeof name, "AES-%d-%s", (int)keyLength * 8, mode == cryptoCbc ? "CBC" : "CTR");
    EVP_CIPHER *cipher = EVP_CIPHER_fetch(NULL, name, NULL);
    EVP_CIPHER_CTX *ctx = EVP_CIPHER_CTX_new();
    int written = 0, last = 0;
    bool ok = cipher != NULL && ctx != NULL && EVP_CIPHER_get_key_length(cipher) == keyLength &&
              EVP_CipherInit_ex2(ctx, cipher, key, iv, encrypt, NULL) == 1 &&
              (n == 0 || EVP_CipherUpdate(ctx, out, &written, in, (int)n) == 1) &&
              EVP_CipherFinal_ex(ctx, out + written, &last) == 1;
    EVP_CIPHER_CTX_free(ctx);
    EVP_CIPHER_free(cipher);
    codec_write_word(thread, 8, (ZamWord)(ok ? written + last : 0));
    if (ok)
        codec_write_error(thread, 9, NULL);
    else
        cryptoFail(thread, 9, encrypt ? "cannot encrypt" : "cannot decrypt");
    zam_native_return_void(thread);
}

// stdSalsa20 XORs n bytes at in with the Salsa20 keystream into out, from
// (key, nonce, nonceLength, counter, in, n, out): an 8-byte nonce picks
// Salsa20 and a 24-byte one XSalsa20.
static void stdSalsa20(ZamThread *thread) {
    cryptoSodium(thread);
    const unsigned char *key = zam_native_argument_pointer(thread, 0);
    const unsigned char *nonce = zam_native_argument_pointer(thread, 1);
    ZamInt nonceLength = cryptoLength(thread, 2);
    uint64_t counter = zam_native_argument(thread, 3);
    ZamInt n = cryptoLength(thread, 5);
    if (n > 0) {
        const unsigned char *in = zam_native_argument_pointer(thread, 4);
        unsigned char *out = zam_native_argument_pointer(thread, 6);
        if (nonceLength == crypto_stream_salsa20_NONCEBYTES)
            crypto_stream_salsa20_xor_ic(out, in, (unsigned long long)n, nonce, counter, key);
        else
            crypto_stream_xsalsa20_xor_ic(out, in, (unsigned long long)n, nonce, counter, key);
    }
    zam_native_return_void(thread);
}

// CryptoKeyKind is a Key.Kind: the OpenSSL algorithm a key is of, and its
// curve for the elliptic-curve kinds, by both of the curve's names.
typedef struct CryptoKeyKind {
    const char *name;
    const char *group;
    const char *groupAlias;
} CryptoKeyKind;

// cryptoKeyKinds are Key.Kind's values, in order.
static const CryptoKeyKind cryptoKeyKinds[] = {
    {"X25519", NULL, NULL},
    {"ED25519", NULL, NULL},
    {"EC", "P-256", "prime256v1"},
    {"EC", "P-384", "secp384r1"},
    {"EC", "P-521", "secp521r1"},
    {"RSA", NULL, NULL},
    {"ML-KEM-512", NULL, NULL},
    {"ML-KEM-768", NULL, NULL},
    {"ML-KEM-1024", NULL, NULL},
    {"ML-DSA-44", NULL, NULL},
    {"ML-DSA-65", NULL, NULL},
    {"ML-DSA-87", NULL, NULL},
};

static const ZamInt cryptoKeyRsa = 5;

static const CryptoKeyKind *cryptoKeyKind(ZamInt kind) {
    if (kind < 0 || kind >= (ZamInt)(sizeof cryptoKeyKinds / sizeof cryptoKeyKinds[0]))
        return NULL;
    return &cryptoKeyKinds[kind];
}

// cryptoKeyKindOf answers the Key.Kind of k, or -1 for one Key does not hold.
static ZamInt cryptoKeyKindOf(EVP_PKEY *k) {
    char group[64];
    for (ZamInt i = 0; i < (ZamInt)(sizeof cryptoKeyKinds / sizeof cryptoKeyKinds[0]); i++) {
        const CryptoKeyKind *kind = &cryptoKeyKinds[i];
        if (!EVP_PKEY_is_a(k, kind->name))
            continue;
        if (kind->group == NULL)
            return i;
        size_t length = 0;
        if (EVP_PKEY_get_group_name(k, group, sizeof group, &length) == 1 &&
            (strcmp(group, kind->group) == 0 || strcmp(group, kind->groupAlias) == 0))
            return i;
    }
    return -1;
}

// cryptoHand leaves n bytes copied from b in a buffer for STD_CODEC_TAKE,
// writing (handle, length) from wordIndex on.
void cryptoHand(ZamThread *thread, uint64_t wordIndex, const unsigned char *b, size_t n) {
    CodecBuffer *buffer = calloc(1, sizeof *buffer);
    if (buffer == NULL || (n > 0 && (buffer->bytes = malloc(n)) == NULL))
        zam_native_panic(thread, "out of memory for a key operation");
    if (n > 0)
        memcpy(buffer->bytes, b, n);
    buffer->length = buffer->capacity = n;
    codec_write_word(thread, wordIndex, (ZamWord)(uintptr_t)buffer);
    codec_write_word(thread, wordIndex + 1, (ZamWord)n);
}

// cryptoHandFail writes an empty result and what failed from wordIndex on.
void cryptoHandFail(ZamThread *thread, uint64_t wordIndex, const char *what) {
    codec_write_word(thread, wordIndex, 0);
    codec_write_word(thread, wordIndex + 1, 0);
    cryptoFail(thread, wordIndex + 2, what);
}

// cryptoHandResult hands n bytes at b on, or fails with what when ok is false,
// freeing b either way.
void cryptoHandResult(ZamThread *thread, uint64_t wordIndex, bool ok, unsigned char *b, size_t n,
                             const char *what) {
    if (ok) {
        cryptoHand(thread, wordIndex, b, n);
        codec_write_error(thread, wordIndex + 2, NULL);
    } else
        cryptoHandFail(thread, wordIndex, what);
    OPENSSL_clear_free(b, n);
}

static void cryptoKeyResult(ZamThread *thread, uint64_t wordIndex, EVP_PKEY *k, const char *what) {
    codec_write_word(thread, wordIndex, (ZamWord)(uintptr_t)k);
    if (k == NULL)
        cryptoFail(thread, wordIndex + 1, what);
    else
        codec_write_error(thread, wordIndex + 1, NULL);
}

// stdPkeyGenerate makes a key of a Key.Kind, of bits bits for RSA, answering
// (handle, err).
static void stdPkeyGenerate(ZamThread *thread) {
    const CryptoKeyKind *kind = cryptoKeyKind((ZamInt)zam_native_argument(thread, 0));
    ZamInt bits = (ZamInt)zam_native_argument(thread, 1);
    EVP_PKEY *k = NULL;
    EVP_PKEY_CTX *ctx = kind == NULL ? NULL : EVP_PKEY_CTX_new_from_name(NULL, kind->name, NULL);
    bool ok = ctx != NULL && EVP_PKEY_keygen_init(ctx) == 1 &&
              (kind->group == NULL || EVP_PKEY_CTX_set_group_name(ctx, kind->group) == 1) &&
              (kind != &cryptoKeyKinds[cryptoKeyRsa] || EVP_PKEY_CTX_set_rsa_keygen_bits(ctx, (int)bits) == 1) &&
              EVP_PKEY_generate(ctx, &k) == 1;
    EVP_PKEY_CTX_free(ctx);
    if (!ok) {
        EVP_PKEY_free(k);
        k = NULL;
    }
    cryptoKeyResult(thread, 2, k, "cannot generate a key");
    zam_native_return_void(thread);
}

EVP_PKEY *cryptoDecode(const unsigned char *b, size_t n, bool pem, int selection) {
    EVP_PKEY *k = NULL;
    OSSL_DECODER_CTX *ctx = OSSL_DECODER_CTX_new_for_pkey(&k, pem ? "PEM" : "DER", NULL, NULL, selection, NULL, NULL);
    if (ctx == NULL || OSSL_DECODER_from_data(ctx, &b, &n) != 1) {
        EVP_PKEY_free(k);
        k = NULL;
    }
    OSSL_DECODER_CTX_free(ctx);
    ERR_clear_error();
    return k;
}

// stdPkeyDecode reads a PKCS #8 private key or a SubjectPublicKeyInfo public
// key from (b, n, pem), answering (handle, kind, private, err).
static void stdPkeyDecode(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 1);
    const unsigned char *b = cryptoBytes(thread, 0, n);
    bool pem = zam_native_argument(thread, 2) != 0;
    bool private = true;
    EVP_PKEY *k = cryptoDecode(b, (size_t)n, pem, OSSL_KEYMGMT_SELECT_PRIVATE_KEY);
    if (k == NULL) {
        private = false;
        k = cryptoDecode(b, (size_t)n, pem, EVP_PKEY_PUBLIC_KEY);
    }
    ZamInt kind = k == NULL ? -1 : cryptoKeyKindOf(k);
    codec_write_word(thread, 3, 0);
    codec_write_word(thread, 4, (ZamWord)kind);
    codec_write_bool(thread, 5, private);
    if (k == NULL)
        codec_write_error(thread, 6, "cannot read a key");
    else if (kind < 0) {
        EVP_PKEY_free(k);
        codec_write_error(thread, 6, "unsupported key type");
    } else {
        codec_write_word(thread, 3, (ZamWord)(uintptr_t)k);
        codec_write_error(thread, 6, NULL);
    }
    zam_native_return_void(thread);
}

bool cryptoEncode(EVP_PKEY *k, bool private, bool pem, unsigned char **out, size_t *n) {
    OSSL_ENCODER_CTX *ctx = OSSL_ENCODER_CTX_new_for_pkey(
        k, private ? EVP_PKEY_KEYPAIR : EVP_PKEY_PUBLIC_KEY, pem ? "PEM" : "DER",
        private ? "PrivateKeyInfo" : "SubjectPublicKeyInfo", NULL);
    *out = NULL;
    *n = 0;
    bool ok = ctx != NULL && OSSL_ENCODER_to_data(ctx, out, n) == 1;
    OSSL_ENCODER_CTX_free(ctx);
    return ok;
}

// stdPkeyEncode writes a key as (h, private, pem), PKCS #8 for a private key
// and SubjectPublicKeyInfo for a public one, answering (buffer, length, err).
static void stdPkeyEncode(ZamThread *thread) {
    unsigned char *out;
    size_t n;
    bool ok = cryptoEncode(codec_handle(thread, 0), zam_native_argument(thread, 1) != 0,
                           zam_native_argument(thread, 2) != 0, &out, &n);
    cryptoHandResult(thread, 3, ok, out, n, "cannot write a key");
    zam_native_return_void(thread);
}

// stdPkeyPublic copies the public half of a key, answering (handle, err).
static void stdPkeyPublic(ZamThread *thread) {
    unsigned char *der;
    size_t n;
    EVP_PKEY *k = NULL;
    if (cryptoEncode(codec_handle(thread, 0), false, false, &der, &n))
        k = cryptoDecode(der, n, false, EVP_PKEY_PUBLIC_KEY);
    OPENSSL_free(der);
    cryptoKeyResult(thread, 1, k, "cannot copy a public key");
    zam_native_return_void(thread);
}

// stdPkeyFromRaw makes a key of a Key.Kind from its raw bytes, from (kind, b,
// n, private), answering (handle, err).
static void stdPkeyFromRaw(ZamThread *thread) {
    const CryptoKeyKind *kind = cryptoKeyKind((ZamInt)zam_native_argument(thread, 0));
    ZamInt n = cryptoLength(thread, 2);
    const unsigned char *b = cryptoBytes(thread, 1, n);
    bool private = zam_native_argument(thread, 3) != 0;
    EVP_PKEY *k = NULL;
    if (kind != NULL && kind->group == NULL)
        k = private ? EVP_PKEY_new_raw_private_key_ex(NULL, kind->name, NULL, b, (size_t)n)
                    : EVP_PKEY_new_raw_public_key_ex(NULL, kind->name, NULL, b, (size_t)n);
    cryptoKeyResult(thread, 4, k, "cannot make a key from these bytes");
    zam_native_return_void(thread);
}

// stdPkeyRaw writes a key's raw private or public bytes, answering (buffer,
// length, err).
static void stdPkeyRaw(ZamThread *thread) {
    EVP_PKEY *k = codec_handle(thread, 0);
    bool private = zam_native_argument(thread, 1) != 0;
    size_t n = 0;
    unsigned char *out = NULL;
    bool ok = (private ? EVP_PKEY_get_raw_private_key(k, NULL, &n) : EVP_PKEY_get_raw_public_key(k, NULL, &n)) == 1 &&
              (out = OPENSSL_malloc(n == 0 ? 1 : n)) != NULL &&
              (private ? EVP_PKEY_get_raw_private_key(k, out, &n) : EVP_PKEY_get_raw_public_key(k, out, &n)) == 1;
    cryptoHandResult(thread, 2, ok, out, n, "this key has no raw form");
    zam_native_return_void(thread);
}

static void stdPkeyFree(ZamThread *thread) {
    EVP_PKEY_free(codec_handle(thread, 0));
    zam_native_return_void(thread);
}

// stdPkeyDerive agrees a shared secret from (private key, peer's public key),
// answering (buffer, length, err).
static void stdPkeyDerive(ZamThread *thread) {
    EVP_PKEY_CTX *ctx = EVP_PKEY_CTX_new_from_pkey(NULL, codec_handle(thread, 0), NULL);
    size_t n = 0;
    unsigned char *out = NULL;
    bool ok = ctx != NULL && EVP_PKEY_derive_init(ctx) == 1 &&
              EVP_PKEY_derive_set_peer(ctx, codec_handle(thread, 1)) == 1 &&
              EVP_PKEY_derive(ctx, NULL, &n) == 1 && (out = OPENSSL_malloc(n)) != NULL &&
              EVP_PKEY_derive(ctx, out, &n) == 1;
    EVP_PKEY_CTX_free(ctx);
    cryptoHandResult(thread, 2, ok, out, n, "cannot derive a shared secret");
    zam_native_return_void(thread);
}

// cryptoSignInit starts signing or verifying with k over a Digest.Kind, or
// none for -1, padding RSA with PSS when told to.
static EVP_MD_CTX *cryptoSignInit(ZamThread *thread, EVP_PKEY *k, uint64_t digestIndex, bool pss, bool sign) {
    const char *digest = cryptoDigest(thread, digestIndex);
    EVP_MD_CTX *ctx = EVP_MD_CTX_new();
    EVP_PKEY_CTX *pctx = NULL;
    bool ok = ctx != NULL &&
              (sign ? EVP_DigestSignInit_ex(ctx, &pctx, digest, NULL, NULL, k, NULL)
                    : EVP_DigestVerifyInit_ex(ctx, &pctx, digest, NULL, NULL, k, NULL)) == 1 &&
              (!pss || (EVP_PKEY_CTX_set_rsa_padding(pctx, RSA_PKCS1_PSS_PADDING) == 1 &&
                        EVP_PKEY_CTX_set_rsa_pss_saltlen(pctx, RSA_PSS_SALTLEN_DIGEST) == 1));
    if (!ok) {
        EVP_MD_CTX_free(ctx);
        return NULL;
    }
    return ctx;
}

// stdPkeySign signs n bytes at b with (key, digest, pss, b, n), answering
// (buffer, length, err).
static void stdPkeySign(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 4);
    EVP_MD_CTX *ctx = cryptoSignInit(thread, codec_handle(thread, 0), 1, zam_native_argument(thread, 2) != 0, true);
    const unsigned char *b = cryptoBytes(thread, 3, n);
    size_t length = 0;
    unsigned char *out = NULL;
    bool ok = ctx != NULL && EVP_DigestSign(ctx, NULL, &length, b, (size_t)n) == 1 &&
              (out = OPENSSL_malloc(length)) != NULL && EVP_DigestSign(ctx, out, &length, b, (size_t)n) == 1;
    EVP_MD_CTX_free(ctx);
    cryptoHandResult(thread, 5, ok, out, length, "cannot sign");
    zam_native_return_void(thread);
}

// stdPkeyVerify reports whether (key, digest, pss, b, n, sig, sigLength)
// holds a valid signature of n bytes at b.
static void stdPkeyVerify(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 4);
    ZamInt sigLength = cryptoLength(thread, 6);
    EVP_MD_CTX *ctx = cryptoSignInit(thread, codec_handle(thread, 0), 1, zam_native_argument(thread, 2) != 0, false);
    bool ok = ctx != NULL && EVP_DigestVerify(ctx, cryptoBytes(thread, 5, sigLength), (size_t)sigLength,
                                              cryptoBytes(thread, 3, n), (size_t)n) == 1;
    EVP_MD_CTX_free(ctx);
    ERR_clear_error();
    codec_write_bool(thread, 7, ok);
    zam_native_return_void(thread);
}

// stdPkeyOaep encrypts or decrypts n bytes with RSA-OAEP, from (key, digest,
// decrypt, b, n), answering (buffer, length, err).
static void stdPkeyOaep(ZamThread *thread) {
    const char *digest = cryptoDigest(thread, 1);
    bool decrypt = zam_native_argument(thread, 2) != 0;
    ZamInt n = cryptoLength(thread, 4);
    const unsigned char *b = cryptoBytes(thread, 3, n);
    EVP_PKEY_CTX *ctx = EVP_PKEY_CTX_new_from_pkey(NULL, codec_handle(thread, 0), NULL);
    size_t length = 0;
    unsigned char *out = NULL;
    bool ok = ctx != NULL && digest != NULL &&
              (decrypt ? EVP_PKEY_decrypt_init(ctx) : EVP_PKEY_encrypt_init(ctx)) == 1 &&
              EVP_PKEY_CTX_set_rsa_padding(ctx, RSA_PKCS1_OAEP_PADDING) == 1 &&
              EVP_PKEY_CTX_set_rsa_oaep_md_name(ctx, digest, NULL) == 1 &&
              EVP_PKEY_CTX_set_rsa_mgf1_md_name(ctx, digest, NULL) == 1 &&
              (decrypt ? EVP_PKEY_decrypt(ctx, NULL, &length, b, (size_t)n)
                       : EVP_PKEY_encrypt(ctx, NULL, &length, b, (size_t)n)) == 1 &&
              (out = OPENSSL_malloc(length)) != NULL &&
              (decrypt ? EVP_PKEY_decrypt(ctx, out, &length, b, (size_t)n)
                       : EVP_PKEY_encrypt(ctx, out, &length, b, (size_t)n)) == 1;
    EVP_PKEY_CTX_free(ctx);
    cryptoHandResult(thread, 5, ok, out, length, decrypt ? "cannot decrypt" : "cannot encrypt");
    zam_native_return_void(thread);
}

// stdPkeyEncapsulate makes a shared secret and its ciphertext for a KEM's
// public key, answering (ctBuffer, ctLength, secretBuffer, secretLength, err).
static void stdPkeyEncapsulate(ZamThread *thread) {
    EVP_PKEY_CTX *ctx = EVP_PKEY_CTX_new_from_pkey(NULL, codec_handle(thread, 0), NULL);
    size_t ctLength = 0, secretLength = 0;
    unsigned char *ct = NULL, *secret = NULL;
    bool ok = ctx != NULL && EVP_PKEY_encapsulate_init(ctx, NULL) == 1 &&
              EVP_PKEY_encapsulate(ctx, NULL, &ctLength, NULL, &secretLength) == 1 &&
              (ct = OPENSSL_malloc(ctLength)) != NULL && (secret = OPENSSL_malloc(secretLength)) != NULL &&
              EVP_PKEY_encapsulate(ctx, ct, &ctLength, secret, &secretLength) == 1;
    EVP_PKEY_CTX_free(ctx);
    if (ok) {
        cryptoHand(thread, 1, ct, ctLength);
        cryptoHand(thread, 3, secret, secretLength);
        codec_write_error(thread, 5, NULL);
    } else {
        codec_write_word(thread, 1, 0);
        codec_write_word(thread, 2, 0);
        codec_write_word(thread, 3, 0);
        codec_write_word(thread, 4, 0);
        cryptoFail(thread, 5, "cannot encapsulate");
    }
    OPENSSL_free(ct);
    OPENSSL_clear_free(secret, secretLength);
    zam_native_return_void(thread);
}

// stdPkeyDecapsulate recovers the shared secret a ciphertext carries, from
// (private key, ct, n), answering (buffer, length, err).
static void stdPkeyDecapsulate(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 2);
    const unsigned char *ct = cryptoBytes(thread, 1, n);
    EVP_PKEY_CTX *ctx = EVP_PKEY_CTX_new_from_pkey(NULL, codec_handle(thread, 0), NULL);
    size_t length = 0;
    unsigned char *out = NULL;
    bool ok = ctx != NULL && EVP_PKEY_decapsulate_init(ctx, NULL) == 1 &&
              EVP_PKEY_decapsulate(ctx, NULL, &length, ct, (size_t)n) == 1 &&
              (out = OPENSSL_malloc(length)) != NULL && EVP_PKEY_decapsulate(ctx, out, &length, ct, (size_t)n) == 1;
    EVP_PKEY_CTX_free(ctx);
    cryptoHandResult(thread, 3, ok, out, length, "cannot decapsulate");
    zam_native_return_void(thread);
}

static OSSL_HPKE_SUITE cryptoHpkeSuite(ZamThread *thread) {
    OSSL_HPKE_SUITE suite = {
        (uint16_t)zam_native_argument(thread, 0),
        (uint16_t)zam_native_argument(thread, 1),
        (uint16_t)zam_native_argument(thread, 2),
    };
    return suite;
}

// stdHpkeSeal seals n bytes in HPKE's base mode to a recipient's public key,
// from (kem, kdf, aead, key, info, infoLength, aad, aadLength, b, n),
// answering (encBuffer, encLength, ctBuffer, ctLength, err).
static void stdHpkeSeal(ZamThread *thread) {
    OSSL_HPKE_SUITE suite = cryptoHpkeSuite(thread);
    ZamInt infoLength = cryptoLength(thread, 5);
    ZamInt aadLength = cryptoLength(thread, 7);
    ZamInt n = cryptoLength(thread, 9);
    const unsigned char *info = cryptoBytes(thread, 4, infoLength);
    const unsigned char *aad = cryptoBytes(thread, 6, aadLength);
    const unsigned char *b = cryptoBytes(thread, 8, n);
    unsigned char *pub = NULL;
    size_t pubLength = EVP_PKEY_get1_encoded_public_key(codec_handle(thread, 3), &pub);
    size_t encLength = OSSL_HPKE_get_public_encap_size(suite);
    size_t ctLength = OSSL_HPKE_get_ciphertext_size(suite, (size_t)n);
    unsigned char *enc = OPENSSL_malloc(encLength == 0 ? 1 : encLength);
    unsigned char *ct = OPENSSL_malloc(ctLength == 0 ? 1 : ctLength);
    OSSL_HPKE_CTX *ctx = OSSL_HPKE_CTX_new(OSSL_HPKE_MODE_BASE, suite, OSSL_HPKE_ROLE_SENDER, NULL, NULL);
    bool ok = pubLength > 0 && enc != NULL && ct != NULL && ctx != NULL && encLength > 0 && ctLength > 0 &&
              OSSL_HPKE_encap(ctx, enc, &encLength, pub, pubLength, info, (size_t)infoLength) == 1 &&
              OSSL_HPKE_seal(ctx, ct, &ctLength, aad, (size_t)aadLength, b, (size_t)n) == 1;
    OSSL_HPKE_CTX_free(ctx);
    OPENSSL_free(pub);
    if (ok) {
        cryptoHand(thread, 10, enc, encLength);
        cryptoHand(thread, 12, ct, ctLength);
        codec_write_error(thread, 14, NULL);
    } else {
        for (uint64_t i = 10; i < 14; i++)
            codec_write_word(thread, i, 0);
        cryptoFail(thread, 14, "cannot seal with HPKE");
    }
    OPENSSL_free(enc);
    OPENSSL_free(ct);
    zam_native_return_void(thread);
}

// stdHpkeOpen opens what stdHpkeSeal sealed, from (kem, kdf, aead, key, enc,
// encLength, info, infoLength, aad, aadLength, ct, ctLength), answering
// (buffer, length, err).
static void stdHpkeOpen(ZamThread *thread) {
    OSSL_HPKE_SUITE suite = cryptoHpkeSuite(thread);
    ZamInt encLength = cryptoLength(thread, 5);
    ZamInt infoLength = cryptoLength(thread, 7);
    ZamInt aadLength = cryptoLength(thread, 9);
    ZamInt ctLength = cryptoLength(thread, 11);
    const unsigned char *enc = cryptoBytes(thread, 4, encLength);
    const unsigned char *info = cryptoBytes(thread, 6, infoLength);
    const unsigned char *aad = cryptoBytes(thread, 8, aadLength);
    const unsigned char *ct = cryptoBytes(thread, 10, ctLength);
    size_t length = (size_t)ctLength;
    unsigned char *out = OPENSSL_malloc(length == 0 ? 1 : length);
    OSSL_HPKE_CTX *ctx = OSSL_HPKE_CTX_new(OSSL_HPKE_MODE_BASE, suite, OSSL_HPKE_ROLE_RECEIVER, NULL, NULL);
    bool ok = out != NULL && ctx != NULL &&
              OSSL_HPKE_decap(ctx, enc, (size_t)encLength, codec_handle(thread, 3), info, (size_t)infoLength) == 1 &&
              OSSL_HPKE_open(ctx, out, &length, aad, (size_t)aadLength, ct, (size_t)ctLength) == 1;
    OSSL_HPKE_CTX_free(ctx);
    cryptoHandResult(thread, 12, ok, out, ok ? length : (size_t)ctLength, "cannot open with HPKE");
    zam_native_return_void(thread);
}

const ZamNativeEntry zamCryptoNativeTable[] = {
    {"STD_CRYPTO_RAND", stdCryptoRand},
    {"STD_CRYPTO_EQUAL", stdCryptoEqual},
    {"STD_HASH_NEW", stdHashNew},
    {"STD_HASH_WRITE", stdHashWrite},
    {"STD_HASH_SUM", stdHashSum},
    {"STD_HASH_RESET", stdHashReset},
    {"STD_HASH_FREE", stdHashFree},
    {"STD_HMAC_NEW", stdHmacNew},
    {"STD_POLY1305_NEW", stdPoly1305New},
    {"STD_MAC_WRITE", stdMacWrite},
    {"STD_MAC_SUM", stdMacSum},
    {"STD_MAC_RESET", stdMacReset},
    {"STD_MAC_FREE", stdMacFree},
    {"STD_HKDF", stdHkdf},
    {"STD_PBKDF2", stdPbkdf2},
    {"STD_SCRYPT", stdScrypt},
    {"STD_ARGON2", stdArgon2},
    {"STD_ARGON2_HASH", stdArgon2Hash},
    {"STD_ARGON2_VERIFY", stdArgon2Verify},
    {"STD_BCRYPT_HASH", stdBcryptHash},
    {"STD_BCRYPT_VERIFY", stdBcryptVerify},
    {"STD_AEAD", stdAead},
    {"STD_AES", stdAes},
    {"STD_SALSA20", stdSalsa20},
    {"STD_PKEY_GENERATE", stdPkeyGenerate},
    {"STD_PKEY_DECODE", stdPkeyDecode},
    {"STD_PKEY_ENCODE", stdPkeyEncode},
    {"STD_PKEY_PUBLIC", stdPkeyPublic},
    {"STD_PKEY_FROM_RAW", stdPkeyFromRaw},
    {"STD_PKEY_RAW", stdPkeyRaw},
    {"STD_PKEY_FREE", stdPkeyFree},
    {"STD_PKEY_DERIVE", stdPkeyDerive},
    {"STD_PKEY_SIGN", stdPkeySign},
    {"STD_PKEY_VERIFY", stdPkeyVerify},
    {"STD_PKEY_OAEP", stdPkeyOaep},
    {"STD_PKEY_ENCAPSULATE", stdPkeyEncapsulate},
    {"STD_PKEY_DECAPSULATE", stdPkeyDecapsulate},
    {"STD_HPKE_SEAL", stdHpkeSeal},
    {"STD_HPKE_OPEN", stdHpkeOpen},
};

const uint64_t zamCryptoNativeTableCount = sizeof(zamCryptoNativeTable) / sizeof(zamCryptoNativeTable[0]);
