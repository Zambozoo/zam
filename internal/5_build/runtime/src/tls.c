#include <arpa/inet.h>
#include <openssl/err.h>
#include <openssl/ssl.h>
#include <openssl/x509v3.h>
#include <stdio.h>

#include "codec.h"

// The TLS natives, over OpenSSL. A context holds the settings a connection is
// made with, and a connection is an SSL over a socket the zam side still
// owns. The handshake, reads and writes run in a safe region.

// opensslError writes the most recent OpenSSL error, or fallback when there
// is none, to the error slot at wordIndex.
static void opensslError(ZamThread *thread, uint64_t wordIndex, const char *fallback) {
    unsigned long code = ERR_get_error();
    char message[256];
    if (code == 0)
        snprintf(message, sizeof message, "%s", fallback);
    else
        ERR_error_string_n(code, message, sizeof message);
    ERR_clear_error();
    codec_write_error(thread, wordIndex, message);
}

// stdTlsClientContext makes a client context trusting the PEM certificates
// in caFile, or the system roots for an empty path, and verifying the peer
// only when told to.
static void stdTlsClientContext(ZamThread *thread) {
    char *caFile = codec_c_string(thread, 0);
    bool verify = zam_native_argument(thread, 1) != 0;
    SSL_CTX *ctx = SSL_CTX_new(TLS_client_method());
    bool ok = ctx != NULL;
    if (ok) {
        SSL_CTX_set_min_proto_version(ctx, TLS1_2_VERSION);
        SSL_CTX_set_options(ctx, SSL_OP_IGNORE_UNEXPECTED_EOF);
        SSL_CTX_set_verify(ctx, verify ? SSL_VERIFY_PEER : SSL_VERIFY_NONE, NULL);
        ok = caFile[0] == '\0' ? SSL_CTX_set_default_verify_paths(ctx) == 1
                               : SSL_CTX_load_verify_locations(ctx, caFile, NULL) == 1;
    }
    free(caFile);
    if (!ok) {
        SSL_CTX_free(ctx);
        codec_write_word(thread, 2, 0);
        opensslError(thread, 3, "cannot make a TLS client context");
    } else {
        codec_write_word(thread, 2, (ZamWord)(uintptr_t)ctx);
        codec_write_error(thread, 3, NULL);
    }
    zam_native_return_void(thread);
}

// stdTlsServerContext makes a server context presenting the PEM certificate
// chain in certFile with the private key in keyFile.
static void stdTlsServerContext(ZamThread *thread) {
    char *certFile = codec_c_string(thread, 0);
    char *keyFile = codec_c_string(thread, 1);
    SSL_CTX *ctx = SSL_CTX_new(TLS_server_method());
    bool ok = ctx != NULL &&
              SSL_CTX_use_certificate_chain_file(ctx, certFile) == 1 &&
              SSL_CTX_use_PrivateKey_file(ctx, keyFile, SSL_FILETYPE_PEM) == 1 &&
              SSL_CTX_check_private_key(ctx) == 1;
    if (ok) {
        SSL_CTX_set_min_proto_version(ctx, TLS1_2_VERSION);
        SSL_CTX_set_options(ctx, SSL_OP_IGNORE_UNEXPECTED_EOF);
    }
    free(certFile);
    free(keyFile);
    if (!ok) {
        SSL_CTX_free(ctx);
        codec_write_word(thread, 2, 0);
        opensslError(thread, 3, "cannot make a TLS server context");
    } else {
        codec_write_word(thread, 2, (ZamWord)(uintptr_t)ctx);
        codec_write_error(thread, 3, NULL);
    }
    zam_native_return_void(thread);
}

static void stdTlsContextFree(ZamThread *thread) {
    SSL_CTX_free(codec_handle(thread, 0));
    zam_native_return_void(thread);
}

// stdTlsHandshake runs the client or server handshake over a connected
// socket. A client names the server it expects, which is sent for SNI and,
// when the context verifies, checked against the certificate.
static void stdTlsHandshake(ZamThread *thread) {
    SSL_CTX *ctx = codec_handle(thread, 0);
    int fd = (int)(ZamInt)zam_native_argument(thread, 1);
    char *serverName = codec_c_string(thread, 2);
    bool server = zam_native_argument(thread, 3) != 0;

    SSL *ssl = SSL_new(ctx);
    if (ssl == NULL || SSL_set_fd(ssl, fd) != 1) {
        free(serverName);
        SSL_free(ssl);
        codec_write_word(thread, 4, 0);
        opensslError(thread, 5, "cannot start TLS");
        zam_native_return_void(thread);
        return;
    }
    if (!server && serverName[0] != '\0') {
        X509_VERIFY_PARAM *param = SSL_get0_param(ssl);
        unsigned char ip[16];
        bool literal = inet_pton(AF_INET, serverName, ip) == 1 ||
                       inet_pton(AF_INET6, serverName, ip) == 1;
        if (literal) {
            X509_VERIFY_PARAM_set1_ip_asc(param, serverName);
        } else {
            SSL_set_tlsext_host_name(ssl, serverName);
            X509_VERIFY_PARAM_set1_host(param, serverName, 0);
        }
    }
    free(serverName);

    zam_native_block_begin(thread);
    int result = server ? SSL_accept(ssl) : SSL_connect(ssl);
    zam_native_block_end(thread);
    if (result != 1) {
        long verified = SSL_get_verify_result(ssl);
        if (verified != X509_V_OK) {
            ERR_clear_error();
            codec_write_error(thread, 5, X509_verify_cert_error_string(verified));
        } else
            opensslError(thread, 5, "TLS handshake failed");
        SSL_free(ssl);
        codec_write_word(thread, 4, 0);
    } else {
        codec_write_word(thread, 4, (ZamWord)(uintptr_t)ssl);
        codec_write_error(thread, 5, NULL);
    }
    zam_native_return_void(thread);
}

// transfer reads or writes up to n bytes, answering 0 at a clean end of the
// stream when reading.
static void transfer(ZamThread *thread, bool write) {
    SSL *ssl = codec_handle(thread, 0);
    if (ssl == NULL)
        zam_native_panic(thread, "TLS connection was closed");
    void *buffer = zam_native_argument_pointer(thread, 1);
    ZamInt n = (ZamInt)zam_native_argument(thread, 2);
    size_t done = 0;
    zam_native_block_begin(thread);
    int result = write ? SSL_write_ex(ssl, buffer, (size_t)n, &done)
                       : SSL_read_ex(ssl, buffer, (size_t)n, &done);
    int reason = result == 1 ? SSL_ERROR_NONE : SSL_get_error(ssl, result);
    zam_native_block_end(thread);
    *(ZamInt *)zam_native_argument_pointer(thread, 3) = (ZamInt)done;
    if (reason == SSL_ERROR_NONE || (!write && reason == SSL_ERROR_ZERO_RETURN))
        codec_write_error(thread, 4, NULL);
    else
        opensslError(thread, 4, write ? "TLS write failed" : "TLS read failed");
    zam_native_return_void(thread);
}

static void stdTlsRead(ZamThread *thread) { transfer(thread, false); }
static void stdTlsWrite(ZamThread *thread) { transfer(thread, true); }

// stdTlsClose sends close_notify without waiting for the peer's, and frees
// the connection; the socket under it is the caller's to close.
static void stdTlsClose(ZamThread *thread) {
    SSL *ssl = codec_handle(thread, 0);
    if (ssl != NULL) {
        SSL_shutdown(ssl);
        SSL_free(ssl);
        ERR_clear_error();
    }
    zam_native_return_void(thread);
}

const ZamNativeEntry zamTlsNativeTable[] = {
    {"STD_TLS_CLIENT_CONTEXT", stdTlsClientContext},
    {"STD_TLS_SERVER_CONTEXT", stdTlsServerContext},
    {"STD_TLS_CONTEXT_FREE", stdTlsContextFree},
    {"STD_TLS_HANDSHAKE", stdTlsHandshake},
    {"STD_TLS_READ", stdTlsRead},
    {"STD_TLS_WRITE", stdTlsWrite},
    {"STD_TLS_CLOSE", stdTlsClose},
};

const uint64_t zamTlsNativeTableCount = sizeof(zamTlsNativeTable) / sizeof(zamTlsNativeTable[0]);
