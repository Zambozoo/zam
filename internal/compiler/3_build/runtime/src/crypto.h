#pragma once

// Helpers the crypto and PKI natives share: reading byte runs out of the
// frame, handing variable-length results back for STD_CODEC_TAKE, and moving
// keys in and out of DER and PEM.

#include <openssl/evp.h>

#include "codec.h"

// cryptoLength reads the length at wordIndex, a negative one as zero.
ZamInt cryptoLength(ZamThread *thread, uint64_t wordIndex);

// cryptoBytes resolves the byte pointer at wordIndex, n bytes long, to
// something a library will read even when n is zero.
const unsigned char *cryptoBytes(ZamThread *thread, uint64_t wordIndex, ZamInt n);

// cryptoFail writes what failed, with OpenSSL's reason when it has one, to the
// error slot at wordIndex.
void cryptoFail(ZamThread *thread, uint64_t wordIndex, const char *what);

// cryptoHand leaves n bytes copied from b in a buffer for STD_CODEC_TAKE,
// writing (handle, length) from wordIndex on.
void cryptoHand(ZamThread *thread, uint64_t wordIndex, const unsigned char *b, size_t n);

// cryptoHandFail writes an empty result and what failed from wordIndex on.
void cryptoHandFail(ZamThread *thread, uint64_t wordIndex, const char *what);

// cryptoHandResult hands n bytes at b on as (handle, length, err) from
// wordIndex, or fails with what when ok is false, freeing b either way.
void cryptoHandResult(ZamThread *thread, uint64_t wordIndex, bool ok, unsigned char *b, size_t n,
                      const char *what);

// cryptoDecode reads a key of the selection from n bytes of PEM or DER, or
// answers NULL.
EVP_PKEY *cryptoDecode(const unsigned char *b, size_t n, bool pem, int selection);

// cryptoEncode writes a private key as PKCS #8 or a public one as
// SubjectPublicKeyInfo, in PEM or DER, into *out for OPENSSL_free.
bool cryptoEncode(EVP_PKEY *k, bool private, bool pem, unsigned char **out, size_t *n);
