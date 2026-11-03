#include <openssl/bio.h>
#include <openssl/err.h>
#include <openssl/ocsp.h>
#include <openssl/pem.h>
#include <openssl/pkcs12.h>
#include <openssl/rand.h>
#include <openssl/x509.h>
#include <openssl/x509v3.h>
#include <stdio.h>
#include <time.h>

#include "crypto.h"

// The PKI natives: X.509 certificates and requests, PKCS #12 bundles and OCSP,
// over OpenSSL. Keys cross from zam as DER, which is what Key writes.

// pkiKey reads the key whose n bytes of DER start at wordIndex, private if
// they hold one and public otherwise.
static EVP_PKEY *pkiKey(ZamThread *thread, uint64_t wordIndex) {
    ZamInt n = cryptoLength(thread, wordIndex + 1);
    const unsigned char *b = cryptoBytes(thread, wordIndex, n);
    EVP_PKEY *k = cryptoDecode(b, (size_t)n, false, OSSL_KEYMGMT_SELECT_PRIVATE_KEY);
    return k != NULL ? k : cryptoDecode(b, (size_t)n, false, EVP_PKEY_PUBLIC_KEY);
}

// pkiDigest is the digest k signs over: none for the kinds that hash the
// message themselves, SHA-256 otherwise.
static const EVP_MD *pkiDigest(EVP_PKEY *k) {
    if (EVP_PKEY_is_a(k, "ED25519") || EVP_PKEY_is_a(k, "ED448") || EVP_PKEY_is_a(k, "ML-DSA-44") ||
        EVP_PKEY_is_a(k, "ML-DSA-65") || EVP_PKEY_is_a(k, "ML-DSA-87"))
        return NULL;
    return EVP_sha256();
}

// pkiHandles hands a run of certificates to zam as a malloc'd array of
// handles for STD_PKI_TAKE, writing (list, count) from wordIndex on. The
// certificates are owned by the array from then on.
static void pkiHandles(ZamThread *thread, uint64_t wordIndex, STACK_OF(X509) *certs) {
    int n = certs == NULL ? 0 : sk_X509_num(certs);
    ZamWord *list = malloc(sizeof *list * (size_t)(n == 0 ? 1 : n));
    if (list == NULL)
        zam_native_panic(thread, "out of memory for certificates");
    for (int i = 0; i < n; i++)
        list[i] = (ZamWord)(uintptr_t)sk_X509_value(certs, i);
    sk_X509_free(certs);
    codec_write_word(thread, wordIndex, (ZamWord)(uintptr_t)list);
    codec_write_word(thread, wordIndex + 1, (ZamWord)n);
}

// stdPkiTake copies a handle array into zam memory and frees it.
static void stdPkiTake(ZamThread *thread) {
    ZamWord *list = codec_handle(thread, 0);
    ZamInt n = cryptoLength(thread, 2);
    if (n > 0)
        memcpy(zam_native_argument_pointer(thread, 1), list, sizeof *list * (size_t)n);
    free(list);
    zam_native_return_void(thread);
}

// pkiCerts reads the n handles at wordIndex into a stack the caller frees
// with sk_X509_free, leaving the certificates with zam.
static STACK_OF(X509) *pkiCerts(ZamThread *thread, uint64_t wordIndex) {
    ZamInt n = cryptoLength(thread, wordIndex + 1);
    STACK_OF(X509) *certs = sk_X509_new_null();
    if (certs == NULL)
        zam_native_panic(thread, "out of memory for certificates");
    const ZamWord *handles = n > 0 ? zam_native_argument_pointer(thread, wordIndex) : NULL;
    for (ZamInt i = 0; i < n; i++)
        sk_X509_push(certs, (X509 *)(uintptr_t)handles[i]);
    return certs;
}

// stdX509Decode reads every certificate in n bytes of PEM, or the one in n
// bytes of DER, answering (list, count, err).
static void stdX509Decode(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 1);
    const unsigned char *b = cryptoBytes(thread, 0, n);
    bool pem = zam_native_argument(thread, 2) != 0;
    STACK_OF(X509) *certs = sk_X509_new_null();
    if (certs == NULL)
        zam_native_panic(thread, "out of memory for certificates");
    if (pem) {
        BIO *bio = BIO_new_mem_buf(b, (int)n);
        X509 *cert;
        while (bio != NULL && (cert = PEM_read_bio_X509(bio, NULL, NULL, NULL)) != NULL)
            sk_X509_push(certs, cert);
        BIO_free(bio);
    } else {
        X509 *cert = d2i_X509(NULL, &b, (long)n);
        if (cert != NULL)
            sk_X509_push(certs, cert);
    }
    ERR_clear_error();
    if (sk_X509_num(certs) == 0) {
        sk_X509_free(certs);
        codec_write_word(thread, 3, 0);
        codec_write_word(thread, 4, 0);
        codec_write_error(thread, 5, "no certificate to read");
    } else {
        pkiHandles(thread, 3, certs);
        codec_write_error(thread, 5, NULL);
    }
    zam_native_return_void(thread);
}

// stdX509Encode writes a certificate in PEM or DER, answering (buffer,
// length, err).
static void stdX509Encode(ZamThread *thread) {
    X509 *cert = codec_handle(thread, 0);
    bool pem = zam_native_argument(thread, 1) != 0;
    BIO *bio = BIO_new(BIO_s_mem());
    bool ok = bio != NULL && (pem ? PEM_write_bio_X509(bio, cert) : i2d_X509_bio(bio, cert)) == 1;
    char *data = NULL;
    long n = ok ? BIO_get_mem_data(bio, &data) : 0;
    if (ok) {
        cryptoHand(thread, 2, (const unsigned char *)data, (size_t)n);
        codec_write_error(thread, 4, NULL);
    } else
        cryptoHandFail(thread, 2, "cannot write a certificate");
    BIO_free(bio);
    zam_native_return_void(thread);
}

static void stdX509Free(ZamThread *thread) {
    X509_free(codec_handle(thread, 0));
    zam_native_return_void(thread);
}

static ZamInt pkiTime(const ASN1_TIME *t) {
    struct tm tm;
    if (t == NULL || ASN1_TIME_to_tm(t, &tm) != 1)
        return 0;
    return (ZamInt)timegm(&tm);
}

static void pkiWriteName(ZamThread *thread, uint64_t wordIndex, const X509_NAME *name) {
    BIO *bio = BIO_new(BIO_s_mem());
    char *data = NULL;
    long n = 0;
    if (bio != NULL && X509_NAME_print_ex(bio, name, 0, XN_FLAG_RFC2253) >= 0)
        n = BIO_get_mem_data(bio, &data);
    codec_write_string(thread, wordIndex, data == NULL ? "" : data, (size_t)n);
    BIO_free(bio);
}

// stdX509Info reads a certificate's fields, answering (subject, issuer,
// serial in hex, notBefore, notAfter, isCA, DNS names one per line, OCSP
// responder URL or "").
static void stdX509Info(ZamThread *thread) {
    X509 *cert = codec_handle(thread, 0);
    pkiWriteName(thread, 1, X509_get_subject_name(cert));
    pkiWriteName(thread, 2, X509_get_issuer_name(cert));

    BIGNUM *serial = ASN1_INTEGER_to_BN(X509_get0_serialNumber(cert), NULL);
    char *hex = serial == NULL ? NULL : BN_bn2hex(serial);
    codec_write_string(thread, 3, hex == NULL ? "" : hex, hex == NULL ? 0 : strlen(hex));
    OPENSSL_free(hex);
    BN_free(serial);

    codec_write_word(thread, 4, (ZamWord)pkiTime(X509_get0_notBefore(cert)));
    codec_write_word(thread, 5, (ZamWord)pkiTime(X509_get0_notAfter(cert)));
    codec_write_bool(thread, 6, X509_check_ca(cert) != 0);

    CodecBuffer names = {0};
    GENERAL_NAMES *sans = X509_get_ext_d2i(cert, NID_subject_alt_name, NULL, NULL);
    for (int i = 0; i < sk_GENERAL_NAME_num(sans); i++) {
        const GENERAL_NAME *name = sk_GENERAL_NAME_value(sans, i);
        if (name->type != GEN_DNS)
            continue;
        if (names.length > 0)
            codec_buffer_append(&names, "\n", 1);
        codec_buffer_append(&names, ASN1_STRING_get0_data(name->d.dNSName),
                            (size_t)ASN1_STRING_get_length(name->d.dNSName));
    }
    GENERAL_NAMES_free(sans);
    codec_write_string(thread, 7, names.bytes == NULL ? "" : names.bytes, names.length);
    free(names.bytes);

    STACK_OF(OPENSSL_STRING) *ocsp = X509_get1_ocsp(cert);
    const char *url = sk_OPENSSL_STRING_num(ocsp) > 0 ? sk_OPENSSL_STRING_value(ocsp, 0) : "";
    codec_write_string(thread, 8, url, strlen(url));
    X509_email_free(ocsp);
    zam_native_return_void(thread);
}

// stdX509PublicKey writes a certificate's public key as SubjectPublicKeyInfo
// DER, answering (buffer, length, err).
static void stdX509PublicKey(ZamThread *thread) {
    unsigned char *der = NULL;
    size_t n = 0;
    EVP_PKEY *k = X509_get0_pubkey(codec_handle(thread, 0));
    bool ok = k != NULL && cryptoEncode(k, false, false, &der, &n);
    cryptoHandResult(thread, 1, ok, der, n, "cannot read a certificate's key");
    zam_native_return_void(thread);
}

// stdX509Verify checks a certificate chains to one of the roots through the
// intermediates, holds for the host unless it is "", and is valid at the
// time given in seconds since the epoch, from (cert, roots,
// nRoots, intermediates, nIntermediates, host, at), answering err.
static void stdX509Verify(ZamThread *thread) {
    X509 *cert = codec_handle(thread, 0);
    STACK_OF(X509) *roots = pkiCerts(thread, 1);
    STACK_OF(X509) *intermediates = pkiCerts(thread, 3);
    char *host = codec_c_string(thread, 5);
    ZamInt at = (ZamInt)zam_native_argument(thread, 6);
    X509_STORE *store = X509_STORE_new();
    X509_STORE_CTX *ctx = X509_STORE_CTX_new();
    bool ok = store != NULL && ctx != NULL;
    for (int i = 0; ok && i < sk_X509_num(roots); i++)
        ok = X509_STORE_add_cert(store, sk_X509_value(roots, i)) == 1;
    ok = ok && X509_STORE_CTX_init(ctx, store, cert, intermediates) == 1;
    if (ok) {
        X509_VERIFY_PARAM *param = X509_STORE_CTX_get0_param(ctx);
        if (host[0] != '\0')
            X509_VERIFY_PARAM_set1_host(param, host, 0);
        X509_VERIFY_PARAM_set_time(param, (time_t)at);
    }
    bool verified = ok && X509_verify_cert(ctx) == 1;
    if (verified)
        codec_write_error(thread, 7, NULL);
    else if (ok)
        codec_write_error(thread, 7, X509_verify_cert_error_string(X509_STORE_CTX_get_error(ctx)));
    else
        cryptoFail(thread, 7, "cannot verify a certificate");
    ERR_clear_error();
    X509_STORE_CTX_free(ctx);
    X509_STORE_free(store);
    sk_X509_free(roots);
    sk_X509_free(intermediates);
    free(host);
    zam_native_return_void(thread);
}

// pkiAddExt adds the extension value names under nid, issued by issuer.
static bool pkiAddExt(X509 *cert, X509 *issuer, int nid, const char *value) {
    X509V3_CTX ctx;
    X509V3_set_ctx(&ctx, issuer, cert, NULL, NULL, 0);
    X509_EXTENSION *ext = X509V3_EXT_conf_nid(NULL, &ctx, nid, value);
    bool ok = ext != NULL && X509_add_ext(cert, ext, -1) == 1;
    X509_EXTENSION_free(ext);
    return ok;
}

// pkiSubjectAltNames writes "DNS:a,DNS:b" for names one per line, or NULL
// for none, for the caller to free.
static char *pkiSubjectAltNames(const char *lines) {
    if (lines[0] == '\0')
        return NULL;
    CodecBuffer b = {0};
    const char *at = lines;
    for (;;) {
        const char *end = strchr(at, '\n');
        size_t n = end == NULL ? strlen(at) : (size_t)(end - at);
        if (n > 0) {
            if (b.length > 0)
                codec_buffer_append(&b, ",", 1);
            codec_buffer_append(&b, "DNS:", 4);
            codec_buffer_append(&b, at, n);
        }
        if (end == NULL)
            break;
        at = end + 1;
    }
    codec_buffer_append(&b, "", 1);
    return b.bytes;
}

// pkiName makes the name of commonName and organization, leaving out either
// that is "", or answers NULL.
static X509_NAME *pkiName(const char *commonName, const char *organization) {
    X509_NAME *name = X509_NAME_new();
    bool ok = name != NULL &&
              (commonName[0] == '\0' || X509_NAME_add_entry_by_txt(name, "CN", MBSTRING_UTF8,
                                                                    (const unsigned char *)commonName, -1, -1, 0) == 1) &&
              (organization[0] == '\0' || X509_NAME_add_entry_by_txt(name, "O", MBSTRING_UTF8,
                                                                      (const unsigned char *)organization, -1, -1, 0) == 1);
    if (!ok) {
        X509_NAME_free(name);
        return NULL;
    }
    return name;
}

// stdX509Create issues a certificate for a subject's key from (subject key,
// subject length, issuer certificate or 0 for self-signed, issuer key,
// issuer key length, commonName, organization, DNS names one per line, OCSP
// URL, notBefore, notAfter, isCA), answering (handle, err), the times in
// seconds since the epoch.
static void stdX509Create(ZamThread *thread) {
    EVP_PKEY *subject = pkiKey(thread, 0);
    X509 *issuer = codec_handle(thread, 2);
    EVP_PKEY *signer = pkiKey(thread, 3);
    char *commonName = codec_c_string(thread, 5);
    char *organization = codec_c_string(thread, 6);
    char *dnsNames = codec_c_string(thread, 7);
    char *ocspURL = codec_c_string(thread, 8);
    ZamInt notBefore = (ZamInt)zam_native_argument(thread, 9);
    ZamInt notAfter = (ZamInt)zam_native_argument(thread, 10);
    bool isCA = zam_native_argument(thread, 11) != 0;

    X509 *cert = X509_new();
    unsigned char serial[16];
    BIGNUM *serialNumber = NULL;
    char *sans = pkiSubjectAltNames(dnsNames);
    char ocspValue[1100];
    snprintf(ocspValue, sizeof ocspValue, "OCSP;URI:%s", ocspURL);
    X509_NAME *name = pkiName(commonName, organization);
    bool ok = cert != NULL && subject != NULL && signer != NULL && name != NULL &&
              RAND_bytes(serial, sizeof serial) == 1;
    if (ok) {
        serial[0] &= 0x7f;
        serialNumber = BN_bin2bn(serial, sizeof serial, NULL);
        X509 *issuedBy = issuer == NULL ? cert : issuer;
        ok = X509_set_version(cert, X509_VERSION_3) == 1 && serialNumber != NULL &&
             BN_to_ASN1_INTEGER(serialNumber, X509_get_serialNumber(cert)) != NULL &&
             ASN1_TIME_set(X509_getm_notBefore(cert), (time_t)notBefore) != NULL &&
             ASN1_TIME_set(X509_getm_notAfter(cert), (time_t)notAfter) != NULL &&
             X509_set_pubkey(cert, subject) == 1 &&
             X509_set_subject_name(cert, name) == 1 &&
             X509_set_issuer_name(cert, X509_get_subject_name(issuedBy)) == 1 &&
             pkiAddExt(cert, issuedBy, NID_basic_constraints, isCA ? "critical,CA:TRUE" : "critical,CA:FALSE") &&
             pkiAddExt(cert, issuedBy, NID_key_usage,
                       isCA ? "critical,keyCertSign,cRLSign,digitalSignature" : "critical,digitalSignature,keyEncipherment") &&
             pkiAddExt(cert, issuedBy, NID_subject_key_identifier, "hash") &&
             (issuer == NULL || pkiAddExt(cert, issuedBy, NID_authority_key_identifier, "keyid:always")) &&
             (isCA || pkiAddExt(cert, issuedBy, NID_ext_key_usage, "serverAuth,clientAuth")) &&
             (sans == NULL || pkiAddExt(cert, issuedBy, NID_subject_alt_name, sans)) &&
             (ocspURL[0] == '\0' || pkiAddExt(cert, issuedBy, NID_info_access, ocspValue)) &&
             X509_sign(cert, signer, pkiDigest(signer)) > 0;
    }
    BN_free(serialNumber);
    X509_NAME_free(name);
    free(sans);
    free(commonName);
    free(organization);
    free(dnsNames);
    free(ocspURL);
    EVP_PKEY_free(subject);
    EVP_PKEY_free(signer);
    if (!ok) {
        X509_free(cert);
        codec_write_word(thread, 12, 0);
        cryptoFail(thread, 13, "cannot issue a certificate");
    } else {
        codec_write_word(thread, 12, (ZamWord)(uintptr_t)cert);
        codec_write_error(thread, 13, NULL);
    }
    zam_native_return_void(thread);
}

// stdX509Request makes a certificate signing request in DER from (private
// key, length, commonName, DNS names one per line), answering (buffer,
// length, err).
static void stdX509Request(ZamThread *thread) {
    EVP_PKEY *k = pkiKey(thread, 0);
    char *commonName = codec_c_string(thread, 2);
    char *dnsNames = codec_c_string(thread, 3);
    char *sans = pkiSubjectAltNames(dnsNames);
    X509_REQ *req = X509_REQ_new();
    X509_NAME *name = pkiName(commonName, "");
    STACK_OF(X509_EXTENSION) *exts = sk_X509_EXTENSION_new_null();
    X509_EXTENSION *ext = sans == NULL ? NULL : X509V3_EXT_conf_nid(NULL, NULL, NID_subject_alt_name, sans);
    bool ok = k != NULL && req != NULL && exts != NULL && (sans == NULL || ext != NULL) &&
              X509_REQ_set_version(req, X509_REQ_VERSION_1) == 1 &&
              name != NULL && X509_REQ_set_subject_name(req, name) == 1 && X509_REQ_set_pubkey(req, k) == 1 &&
              (ext == NULL || (sk_X509_EXTENSION_push(exts, ext) > 0 && X509_REQ_add_extensions(req, exts) == 1)) &&
              X509_REQ_sign(req, k, pkiDigest(k)) > 0;
    unsigned char *der = NULL;
    int n = ok ? i2d_X509_REQ(req, &der) : 0;
    cryptoHandResult(thread, 4, ok && n > 0, der, n > 0 ? (size_t)n : 0, "cannot make a certificate request");
    sk_X509_EXTENSION_pop_free(exts, X509_EXTENSION_free);
    X509_NAME_free(name);
    X509_REQ_free(req);
    EVP_PKEY_free(k);
    free(sans);
    free(commonName);
    free(dnsNames);
    zam_native_return_void(thread);
}

// stdPkcs12Parse opens a PKCS #12 bundle from (b, n, password), answering
// (key buffer, key length, certificate, chain list, chain count, err), the
// key being PKCS #8 DER.
static void stdPkcs12Parse(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 1);
    const unsigned char *b = cryptoBytes(thread, 0, n);
    char *password = codec_c_string(thread, 2);
    PKCS12 *p12 = d2i_PKCS12(NULL, &b, (long)n);
    EVP_PKEY *k = NULL;
    X509 *cert = NULL;
    STACK_OF(X509) *chain = NULL;
    unsigned char *der = NULL;
    size_t derLength = 0;
    bool ok = p12 != NULL && PKCS12_parse(p12, password, &k, &cert, &chain) == 1 &&
              (k == NULL || cryptoEncode(k, true, false, &der, &derLength));
    PKCS12_free(p12);
    EVP_PKEY_free(k);
    OPENSSL_clear_free(password, strlen(password));
    if (!ok) {
        X509_free(cert);
        sk_X509_pop_free(chain, X509_free);
        for (uint64_t i = 3; i < 8; i++)
            codec_write_word(thread, i, 0);
        cryptoFail(thread, 8, "cannot open a PKCS #12 bundle");
    } else {
        cryptoHand(thread, 3, der, derLength);
        codec_write_word(thread, 5, (ZamWord)(uintptr_t)cert);
        pkiHandles(thread, 6, chain);
        codec_write_error(thread, 8, NULL);
    }
    OPENSSL_clear_free(der, derLength);
    zam_native_return_void(thread);
}

// stdPkcs12Create bundles a key, its certificate and a chain under a
// password, from (key, length, certificate, chain, chain count, password,
// name), answering (buffer, length, err).
static void stdPkcs12Create(ZamThread *thread) {
    EVP_PKEY *k = pkiKey(thread, 0);
    X509 *cert = codec_handle(thread, 2);
    STACK_OF(X509) *chain = pkiCerts(thread, 3);
    char *password = codec_c_string(thread, 5);
    char *name = codec_c_string(thread, 6);
    PKCS12 *p12 = k == NULL ? NULL
                            : PKCS12_create_ex(password, name[0] == '\0' ? NULL : name, k, cert, chain, 0, 0, 0, 0,
                                               0, NULL, NULL);
    unsigned char *der = NULL;
    int n = p12 == NULL ? 0 : i2d_PKCS12(p12, &der);
    cryptoHandResult(thread, 7, n > 0, der, n > 0 ? (size_t)n : 0, "cannot make a PKCS #12 bundle");
    PKCS12_free(p12);
    sk_X509_free(chain);
    EVP_PKEY_free(k);
    OPENSSL_clear_free(password, strlen(password));
    free(name);
    zam_native_return_void(thread);
}

// stdOcspRequest asks after a certificate's status, from (certificate,
// issuer), answering (buffer, length, err) in DER.
static void stdOcspRequest(ZamThread *thread) {
    OCSP_CERTID *id = OCSP_cert_to_id(NULL, codec_handle(thread, 0), codec_handle(thread, 1));
    OCSP_REQUEST *req = OCSP_REQUEST_new();
    bool ok = id != NULL && req != NULL && OCSP_request_add0_id(req, id) != NULL;
    if (!ok)
        OCSP_CERTID_free(id);
    unsigned char *der = NULL;
    int n = ok ? i2d_OCSP_REQUEST(req, &der) : 0;
    cryptoHandResult(thread, 2, n > 0, der, n > 0 ? (size_t)n : 0, "cannot make an OCSP request");
    OCSP_REQUEST_free(req);
    zam_native_return_void(thread);
}

// stdOcspCheck reads an OCSP response about a certificate, signed by its
// issuer or a responder the issuer delegated to, from (certificate, issuer,
// b, n), answering (status, revokedAt, thisUpdate, nextUpdate,
// hasNextUpdate, err), the times in seconds since the epoch.
static void stdOcspCheck(ZamThread *thread) {
    X509 *cert = codec_handle(thread, 0);
    X509 *issuer = codec_handle(thread, 1);
    ZamInt n = cryptoLength(thread, 3);
    const unsigned char *b = cryptoBytes(thread, 2, n);
    OCSP_RESPONSE *resp = d2i_OCSP_RESPONSE(NULL, &b, (long)n);
    OCSP_BASICRESP *basic = NULL;
    OCSP_CERTID *id = OCSP_cert_to_id(NULL, cert, issuer);
    X509_STORE *store = X509_STORE_new();
    STACK_OF(X509) *certs = sk_X509_new_null();
    int status = V_OCSP_CERTSTATUS_UNKNOWN, reason = 0;
    ASN1_GENERALIZEDTIME *revoked = NULL, *thisUpdate = NULL, *nextUpdate = NULL;
    const char *failure = NULL;
    if (resp == NULL || OCSP_response_status(resp) != OCSP_RESPONSE_STATUS_SUCCESSFUL)
        failure = "the OCSP responder did not answer";
    else if ((basic = OCSP_response_get1_basic(resp)) == NULL || id == NULL || store == NULL || certs == NULL ||
             X509_STORE_add_cert(store, issuer) != 1 || sk_X509_push(certs, issuer) <= 0)
        failure = "cannot read an OCSP response";
    else {
        X509_STORE_set_flags(store, X509_V_FLAG_PARTIAL_CHAIN);
        if (OCSP_basic_verify(basic, certs, store, 0) != 1)
            failure = "the OCSP response is not signed by the issuer";
        else if (OCSP_resp_find_status(basic, id, &status, &reason, &revoked, &thisUpdate, &nextUpdate) != 1)
            failure = "the OCSP response does not cover the certificate";
        else if (OCSP_check_validity(thisUpdate, nextUpdate, 300, -1) != 1)
            failure = "the OCSP response is out of date";
    }
    codec_write_word(thread, 4, (ZamWord)(ZamInt)status);
    codec_write_word(thread, 5, (ZamWord)pkiTime(revoked));
    codec_write_word(thread, 6, (ZamWord)pkiTime(thisUpdate));
    codec_write_word(thread, 7, (ZamWord)pkiTime(nextUpdate));
    codec_write_bool(thread, 8, nextUpdate != NULL);
    codec_write_error(thread, 9, failure);
    ERR_clear_error();
    sk_X509_free(certs);
    X509_STORE_free(store);
    OCSP_CERTID_free(id);
    OCSP_BASICRESP_free(basic);
    OCSP_RESPONSE_free(resp);
    zam_native_return_void(thread);
}

// stdOcspRespond answers an OCSP request as the issuer, giving every
// certificate it asks after the one status, from (request, length, issuer,
// issuer key, key length, status, revokedAt, validFor seconds), answering
// (buffer, length, err) in DER.
static void stdOcspRespond(ZamThread *thread) {
    ZamInt n = cryptoLength(thread, 1);
    const unsigned char *b = cryptoBytes(thread, 0, n);
    X509 *issuer = codec_handle(thread, 2);
    EVP_PKEY *k = pkiKey(thread, 3);
    int status = (int)(ZamInt)zam_native_argument(thread, 5);
    ZamInt revokedAt = (ZamInt)zam_native_argument(thread, 6);
    ZamInt validFor = (ZamInt)zam_native_argument(thread, 7);
    OCSP_REQUEST *req = d2i_OCSP_REQUEST(NULL, &b, (long)n);
    OCSP_BASICRESP *basic = OCSP_BASICRESP_new();
    time_t now = time(NULL);
    ASN1_TIME *thisUpdate = ASN1_TIME_set(NULL, now);
    ASN1_TIME *nextUpdate = ASN1_TIME_set(NULL, now + (time_t)validFor);
    ASN1_TIME *revoked = status == V_OCSP_CERTSTATUS_REVOKED ? ASN1_TIME_set(NULL, (time_t)revokedAt) : NULL;
    bool ok = req != NULL && k != NULL && basic != NULL && thisUpdate != NULL && nextUpdate != NULL &&
              (status != V_OCSP_CERTSTATUS_REVOKED || revoked != NULL);
    for (int i = 0; ok && i < OCSP_request_onereq_count(req); i++) {
        OCSP_CERTID *id = OCSP_onereq_get0_id(OCSP_request_onereq_get0(req, i));
        ok = OCSP_basic_add1_status(basic, id, status, OCSP_REVOKED_STATUS_UNSPECIFIED, revoked, thisUpdate,
                                    nextUpdate) != NULL;
    }
    ok = ok && OCSP_copy_nonce(basic, req) > 0 && OCSP_basic_sign(basic, issuer, k, pkiDigest(k), NULL, 0) == 1;
    OCSP_RESPONSE *resp = ok ? OCSP_response_create(OCSP_RESPONSE_STATUS_SUCCESSFUL, basic) : NULL;
    unsigned char *der = NULL;
    int length = resp == NULL ? 0 : i2d_OCSP_RESPONSE(resp, &der);
    cryptoHandResult(thread, 8, length > 0, der, length > 0 ? (size_t)length : 0, "cannot answer an OCSP request");
    OCSP_RESPONSE_free(resp);
    ASN1_TIME_free(revoked);
    ASN1_TIME_free(nextUpdate);
    ASN1_TIME_free(thisUpdate);
    OCSP_BASICRESP_free(basic);
    OCSP_REQUEST_free(req);
    EVP_PKEY_free(k);
    zam_native_return_void(thread);
}

const ZamNativeEntry zamPkiNativeTable[] = {
    {"STD_PKI_TAKE", stdPkiTake},
    {"STD_X509_DECODE", stdX509Decode},
    {"STD_X509_ENCODE", stdX509Encode},
    {"STD_X509_FREE", stdX509Free},
    {"STD_X509_INFO", stdX509Info},
    {"STD_X509_PUBLIC_KEY", stdX509PublicKey},
    {"STD_X509_VERIFY", stdX509Verify},
    {"STD_X509_CREATE", stdX509Create},
    {"STD_X509_REQUEST", stdX509Request},
    {"STD_PKCS12_PARSE", stdPkcs12Parse},
    {"STD_PKCS12_CREATE", stdPkcs12Create},
    {"STD_OCSP_REQUEST", stdOcspRequest},
    {"STD_OCSP_CHECK", stdOcspCheck},
    {"STD_OCSP_RESPOND", stdOcspRespond},
};

const uint64_t zamPkiNativeTableCount = sizeof(zamPkiNativeTable) / sizeof(zamPkiNativeTable[0]);
