#include <bson/bson.h>

#include "codec.h"

// The BSON natives, over libbson. A value is a whole document the zam side
// owns alone: reading a subdocument copies it out, and appending a document
// copies it in. Members are reached by key, as BSON lays them out.

static bson_t *docArgument(ZamThread *thread, uint64_t wordIndex) {
    bson_t *b = codec_handle(thread, wordIndex);
    if (b == NULL)
        zam_native_panic(thread, "BSON value was released");
    return b;
}

static void writeDoc(ZamThread *thread, uint64_t wordIndex, bson_t *b) {
    codec_write_word(thread, wordIndex, (ZamWord)(uintptr_t)b);
}

// find positions it at the member named by the key at wordIndex, and
// reports whether there is one.
static bool find(ZamThread *thread, bson_iter_t *it, const bson_t *b, uint64_t wordIndex) {
    const ZamString *key = codec_string(thread, wordIndex);
    return bson_iter_init_find_w_len(it, b, key->bytes, (int)key->length);
}

static void requireFind(ZamThread *thread, bson_iter_t *it, const bson_t *b,
                        uint64_t wordIndex, const char *message) {
    if (!find(thread, it, b, wordIndex))
        zam_native_panic(thread, message);
}

static void stdBsonNew(ZamThread *thread) {
    writeDoc(thread, 0, bson_new());
    zam_native_return_void(thread);
}

static void stdBsonFromBytes(ZamThread *thread) {
    const uint8_t *data = zam_native_argument_pointer(thread, 0);
    ZamInt length = (ZamInt)zam_native_argument(thread, 1);
    bson_t *b = length < 5 ? NULL : bson_new_from_data(data, (size_t)length);
    bson_error_t error;
    if (b != NULL && !bson_validate_with_error(b, BSON_VALIDATE_UTF8, &error)) {
        bson_destroy(b);
        writeDoc(thread, 2, NULL);
        codec_write_error(thread, 3, error.message);
    } else if (b == NULL) {
        writeDoc(thread, 2, NULL);
        codec_write_error(thread, 3, "invalid BSON document length");
    } else {
        writeDoc(thread, 2, b);
        codec_write_error(thread, 3, NULL);
    }
    zam_native_return_void(thread);
}

// stdBsonFromJSON reads MongoDB extended JSON, canonical or relaxed.
static void stdBsonFromJSON(ZamThread *thread) {
    const ZamString *s = codec_string(thread, 0);
    bson_error_t error;
    bson_t *b = bson_new_from_json((const uint8_t *)s->bytes, (ssize_t)s->length, &error);
    writeDoc(thread, 1, b);
    codec_write_error(thread, 2, b == NULL ? error.message : NULL);
    zam_native_return_void(thread);
}

static void stdBsonToJSON(ZamThread *thread) {
    bson_t *b = docArgument(thread, 0);
    size_t length;
    char *text = zam_native_argument(thread, 1) != 0
                     ? bson_as_canonical_extended_json(b, &length)
                     : bson_as_relaxed_extended_json(b, &length);
    if (text == NULL)
        zam_native_panic(thread, "BSON document cannot be written as JSON");
    codec_write_string(thread, 2, text, length);
    bson_free(text);
    zam_native_return_void(thread);
}

static void stdBsonByteLen(ZamThread *thread) {
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = docArgument(thread, 0)->len;
    zam_native_return_void(thread);
}

static void stdBsonCopyBytes(ZamThread *thread) {
    bson_t *b = docArgument(thread, 0);
    memcpy(zam_native_argument_pointer(thread, 1), bson_get_data(b), b->len);
    zam_native_return_void(thread);
}

static void stdBsonCount(ZamThread *thread) {
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = bson_count_keys(docArgument(thread, 0));
    zam_native_return_void(thread);
}

// stdBsonKeys answers the keys in document order, joined by NUL bytes.
static void stdBsonKeys(ZamThread *thread) {
    bson_t *b = docArgument(thread, 0);
    CodecBuffer out = {0};
    bson_iter_t it;
    bool first = true;
    if (bson_iter_init(&it, b))
        while (bson_iter_next(&it)) {
            if (!first)
                codec_buffer_append(&out, "", 1);
            first = false;
            codec_buffer_append(&out, bson_iter_key(&it), bson_iter_key_len(&it));
        }
    codec_write_string(thread, 1, out.bytes, out.length);
    free(out.bytes);
    zam_native_return_void(thread);
}

// stdBsonType answers the BSON type byte of a member, zero when there is none.
static void stdBsonType(ZamThread *thread) {
    bson_iter_t it;
    bson_t *b = docArgument(thread, 0);
    *(ZamInt *)zam_native_argument_pointer(thread, 2) =
        find(thread, &it, b, 1) ? (ZamInt)bson_iter_type(&it) : 0;
    zam_native_return_void(thread);
}

static void stdBsonGetStr(ZamThread *thread) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetStr of a missing key");
    if (!BSON_ITER_HOLDS_UTF8(&it))
        zam_native_panic(thread, "BSON.GetStr of a member that is not a string");
    uint32_t length;
    const char *text = bson_iter_utf8(&it, &length);
    codec_write_string(thread, 2, text, length);
    zam_native_return_void(thread);
}

static bool isNumber(const bson_iter_t *it) {
    return BSON_ITER_HOLDS_INT32(it) || BSON_ITER_HOLDS_INT64(it) || BSON_ITER_HOLDS_DOUBLE(it);
}

// stdBsonGetInt reads any number, truncating a double toward zero.
static void stdBsonGetInt(ZamThread *thread) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetInt of a missing key");
    if (!isNumber(&it))
        zam_native_panic(thread, "BSON.GetInt of a member that is not a number");
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = bson_iter_as_int64(&it);
    zam_native_return_void(thread);
}

static void stdBsonGetFlt(ZamThread *thread) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetFlt of a missing key");
    if (!isNumber(&it))
        zam_native_panic(thread, "BSON.GetFlt of a member that is not a number");
    *(double *)zam_native_argument_pointer(thread, 2) = bson_iter_as_double(&it);
    zam_native_return_void(thread);
}

static void stdBsonGetBool(ZamThread *thread) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetBool of a missing key");
    if (!BSON_ITER_HOLDS_BOOL(&it))
        zam_native_panic(thread, "BSON.GetBool of a member that is not a bool");
    codec_write_bool(thread, 2, bson_iter_bool(&it));
    zam_native_return_void(thread);
}

// stdBsonGetDoc copies out a member that is a document or an array.
static void stdBsonGetDoc(ZamThread *thread) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetDoc of a missing key");
    uint32_t length;
    const uint8_t *data;
    if (BSON_ITER_HOLDS_DOCUMENT(&it))
        bson_iter_document(&it, &length, &data);
    else if (BSON_ITER_HOLDS_ARRAY(&it))
        bson_iter_array(&it, &length, &data);
    else
        zam_native_panic(thread, "BSON.GetDoc of a member that is not a document or array");
    writeDoc(thread, 2, bson_new_from_data(data, length));
    zam_native_return_void(thread);
}

static void stdBsonGetDateTime(ZamThread *thread) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetDateTime of a missing key");
    if (!BSON_ITER_HOLDS_DATE_TIME(&it))
        zam_native_panic(thread, "BSON.GetDateTime of a member that is not a date-time");
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = bson_iter_date_time(&it);
    zam_native_return_void(thread);
}

static void stdBsonGetObjectId(ZamThread *thread) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetObjectId of a missing key");
    if (!BSON_ITER_HOLDS_OID(&it))
        zam_native_panic(thread, "BSON.GetObjectId of a member that is not an ObjectId");
    char hex[25];
    bson_oid_to_string(bson_iter_oid(&it), hex);
    codec_write_string(thread, 2, hex, 24);
    zam_native_return_void(thread);
}

static const uint8_t *binaryOf(ZamThread *thread, uint32_t *length) {
    bson_iter_t it;
    requireFind(thread, &it, docArgument(thread, 0), 1, "BSON.GetBinary of a missing key");
    if (!BSON_ITER_HOLDS_BINARY(&it))
        zam_native_panic(thread, "BSON.GetBinary of a member that is not binary");
    bson_subtype_t subtype;
    const uint8_t *data;
    bson_iter_binary(&it, &subtype, length, &data);
    return data;
}

static void stdBsonBinaryLen(ZamThread *thread) {
    uint32_t length;
    binaryOf(thread, &length);
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = length;
    zam_native_return_void(thread);
}

static void stdBsonBinaryCopy(ZamThread *thread) {
    uint32_t length;
    const uint8_t *data = binaryOf(thread, &length);
    if (length > 0)
        memcpy(zam_native_argument_pointer(thread, 2), data, length);
    zam_native_return_void(thread);
}

// appendKey reads the key every append takes as its second argument.
static const ZamString *appendKey(ZamThread *thread) {
    return codec_string(thread, 1);
}

static void appended(ZamThread *thread, bool ok) {
    if (!ok)
        zam_native_panic(thread, "BSON document is too large to append to");
    zam_native_return_void(thread);
}

static void stdBsonAppendStr(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    const ZamString *s = codec_string(thread, 2);
    appended(thread, bson_append_utf8(docArgument(thread, 0), key->bytes, (int)key->length,
                                      s->bytes, (int)s->length));
}

static void stdBsonAppendInt(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    appended(thread, bson_append_int64(docArgument(thread, 0), key->bytes, (int)key->length,
                                       (int64_t)zam_native_argument(thread, 2)));
}

static void stdBsonAppendFlt(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    uint64_t word = zam_native_argument(thread, 2);
    double value;
    memcpy(&value, &word, sizeof value);
    appended(thread, bson_append_double(docArgument(thread, 0), key->bytes, (int)key->length, value));
}

static void stdBsonAppendBool(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    appended(thread, bson_append_bool(docArgument(thread, 0), key->bytes, (int)key->length,
                                      zam_native_argument(thread, 2) != 0));
}

static void stdBsonAppendNull(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    appended(thread, bson_append_null(docArgument(thread, 0), key->bytes, (int)key->length));
}

static void stdBsonAppendDoc(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    bson_t *b = docArgument(thread, 0);
    bson_t *other = docArgument(thread, 2);
    bool ok = zam_native_argument(thread, 3) != 0
                  ? bson_append_array(b, key->bytes, (int)key->length, other)
                  : bson_append_document(b, key->bytes, (int)key->length, other);
    appended(thread, ok);
}

static void stdBsonAppendDateTime(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    appended(thread, bson_append_date_time(docArgument(thread, 0), key->bytes, (int)key->length,
                                           (int64_t)zam_native_argument(thread, 2)));
}

static void stdBsonAppendObjectId(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    const ZamString *hex = codec_string(thread, 2);
    if (!bson_oid_is_valid(hex->bytes, hex->length))
        zam_native_panic(thread, "BSON ObjectId must be 24 hex digits");
    char text[25];
    memcpy(text, hex->bytes, 24);
    text[24] = '\0';
    bson_oid_t oid;
    bson_oid_init_from_string(&oid, text);
    appended(thread, bson_append_oid(docArgument(thread, 0), key->bytes, (int)key->length, &oid));
}

static void stdBsonAppendBinary(ZamThread *thread) {
    const ZamString *key = appendKey(thread);
    const uint8_t *data = zam_native_argument_pointer(thread, 2);
    ZamInt length = (ZamInt)zam_native_argument(thread, 3);
    appended(thread, bson_append_binary(docArgument(thread, 0), key->bytes, (int)key->length,
                                        BSON_SUBTYPE_BINARY, data, (uint32_t)length));
}

// stdBsonNewObjectId answers a fresh ObjectId as hex digits.
static void stdBsonNewObjectId(ZamThread *thread) {
    bson_oid_t oid;
    bson_oid_init(&oid, NULL);
    char hex[25];
    bson_oid_to_string(&oid, hex);
    codec_write_string(thread, 0, hex, 24);
    zam_native_return_void(thread);
}

static void stdBsonEqual(ZamThread *thread) {
    codec_write_bool(thread, 2, bson_equal(docArgument(thread, 0), docArgument(thread, 1)));
    zam_native_return_void(thread);
}

static void stdBsonRelease(ZamThread *thread) {
    bson_t *b = codec_handle(thread, 0);
    if (b != NULL)
        bson_destroy(b);
    zam_native_return_void(thread);
}

const ZamNativeEntry zamBsonNativeTable[] = {
    {"STD_BSON_NEW", stdBsonNew},
    {"STD_BSON_FROM_BYTES", stdBsonFromBytes},
    {"STD_BSON_FROM_JSON", stdBsonFromJSON},
    {"STD_BSON_TO_JSON", stdBsonToJSON},
    {"STD_BSON_BYTE_LEN", stdBsonByteLen},
    {"STD_BSON_COPY_BYTES", stdBsonCopyBytes},
    {"STD_BSON_COUNT", stdBsonCount},
    {"STD_BSON_KEYS", stdBsonKeys},
    {"STD_BSON_TYPE", stdBsonType},
    {"STD_BSON_GET_STR", stdBsonGetStr},
    {"STD_BSON_GET_INT", stdBsonGetInt},
    {"STD_BSON_GET_FLT", stdBsonGetFlt},
    {"STD_BSON_GET_BOOL", stdBsonGetBool},
    {"STD_BSON_GET_DOC", stdBsonGetDoc},
    {"STD_BSON_GET_DATE_TIME", stdBsonGetDateTime},
    {"STD_BSON_GET_OBJECT_ID", stdBsonGetObjectId},
    {"STD_BSON_BINARY_LEN", stdBsonBinaryLen},
    {"STD_BSON_BINARY_COPY", stdBsonBinaryCopy},
    {"STD_BSON_APPEND_STR", stdBsonAppendStr},
    {"STD_BSON_APPEND_INT", stdBsonAppendInt},
    {"STD_BSON_APPEND_FLT", stdBsonAppendFlt},
    {"STD_BSON_APPEND_BOOL", stdBsonAppendBool},
    {"STD_BSON_APPEND_NULL", stdBsonAppendNull},
    {"STD_BSON_APPEND_DOC", stdBsonAppendDoc},
    {"STD_BSON_APPEND_DATE_TIME", stdBsonAppendDateTime},
    {"STD_BSON_APPEND_OBJECT_ID", stdBsonAppendObjectId},
    {"STD_BSON_APPEND_BINARY", stdBsonAppendBinary},
    {"STD_BSON_NEW_OBJECT_ID", stdBsonNewObjectId},
    {"STD_BSON_EQUAL", stdBsonEqual},
    {"STD_BSON_RELEASE", stdBsonRelease},
};

const uint64_t zamBsonNativeTableCount =
    sizeof(zamBsonNativeTable) / sizeof(zamBsonNativeTable[0]);
