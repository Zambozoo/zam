#include <cbor.h>
#include <math.h>
#include <stdio.h>

#include "codec.h"

// The CBOR natives, over libcbor. A value is a cbor_item_t* the zam side
// holds one reference to. Containers made by NewArray and NewMap grow;
// those parsed from bytes are fixed in size, though their members may be
// replaced. Encoding writes every container and string in definite form.

// The kinds the zam side numbers, in CBOR.Kind order.
enum {
    KIND_NULL,
    KIND_UNDEFINED,
    KIND_BOOL,
    KIND_INT,
    KIND_FLT,
    KIND_BYTES,
    KIND_STR,
    KIND_ARRAY,
    KIND_MAP,
    KIND_TAG,
    KIND_SIMPLE
};

static cbor_item_t *itemArgument(ZamThread *thread, uint64_t wordIndex) {
    cbor_item_t *item = codec_handle(thread, wordIndex);
    if (item == NULL)
        zam_native_panic(thread, "CBOR value was released");
    return item;
}

static void writeItem(ZamThread *thread, uint64_t wordIndex, cbor_item_t *item) {
    if (item == NULL)
        zam_native_panic(thread, "out of memory building CBOR");
    codec_write_word(thread, wordIndex, (ZamWord)(uintptr_t)item);
}

static int kindOf(const cbor_item_t *item) {
    switch (cbor_typeof(item)) {
    case CBOR_TYPE_UINT:
    case CBOR_TYPE_NEGINT: return KIND_INT;
    case CBOR_TYPE_BYTESTRING: return KIND_BYTES;
    case CBOR_TYPE_STRING: return KIND_STR;
    case CBOR_TYPE_ARRAY: return KIND_ARRAY;
    case CBOR_TYPE_MAP: return KIND_MAP;
    case CBOR_TYPE_TAG: return KIND_TAG;
    case CBOR_TYPE_FLOAT_CTRL:
        if (!cbor_float_ctrl_is_ctrl(item))
            return KIND_FLT;
        if (cbor_is_null(item))
            return KIND_NULL;
        if (cbor_is_undef(item))
            return KIND_UNDEFINED;
        if (cbor_is_bool(item))
            return KIND_BOOL;
        return KIND_SIMPLE;
    }
    return KIND_SIMPLE;
}

static cbor_item_t *requireKind(ZamThread *thread, int kind, const char *message) {
    cbor_item_t *item = itemArgument(thread, 0);
    if (kindOf(item) != kind)
        zam_native_panic(thread, message);
    return item;
}

// gather appends the bytes of a string or byte string, joining the chunks of
// an indefinite one.
static void gather(CodecBuffer *b, const cbor_item_t *item) {
    if (cbor_isa_string(item)) {
        if (cbor_string_is_definite(item)) {
            codec_buffer_append(b, cbor_string_handle(item), cbor_string_length(item));
            return;
        }
        cbor_item_t **chunks = cbor_string_chunks_handle(item);
        for (size_t i = 0; i < cbor_string_chunk_count(item); i++)
            gather(b, chunks[i]);
        return;
    }
    if (cbor_bytestring_is_definite(item)) {
        codec_buffer_append(b, cbor_bytestring_handle(item), cbor_bytestring_length(item));
        return;
    }
    cbor_item_t **chunks = cbor_bytestring_chunks_handle(item);
    for (size_t i = 0; i < cbor_bytestring_chunk_count(item); i++)
        gather(b, chunks[i]);
}

static void stdCborParse(ZamThread *thread) {
    ZamInt n = (ZamInt)zam_native_argument(thread, 1);
    const uint8_t *data = n == 0 ? (const uint8_t *)"" : zam_native_argument_pointer(thread, 0);
    struct cbor_load_result result;
    cbor_item_t *item = cbor_load(data, (size_t)n, &result);
    const char *problem = NULL;
    switch (result.error.code) {
    case CBOR_ERR_NONE: break;
    case CBOR_ERR_NOTENOUGHDATA: problem = "truncated CBOR"; break;
    case CBOR_ERR_NODATA: problem = "empty CBOR input"; break;
    case CBOR_ERR_MALFORMATED: problem = "malformed CBOR"; break;
    case CBOR_ERR_MEMERROR: problem = "out of memory decoding CBOR"; break;
    case CBOR_ERR_SYNTAXERROR: problem = "invalid CBOR syntax"; break;
    }
    if (problem == NULL && result.read != (size_t)n)
        problem = "trailing data after the CBOR item";
    if (problem != NULL) {
        if (item != NULL)
            cbor_decref(&item);
        char message[128];
        snprintf(message, sizeof message, "%s at offset %zu", problem,
                 result.error.code == CBOR_ERR_NONE ? result.read : result.error.position);
        codec_write_word(thread, 2, 0);
        codec_write_error(thread, 3, message);
    } else {
        writeItem(thread, 2, item);
        codec_write_error(thread, 3, NULL);
    }
    zam_native_return_void(thread);
}

static void stdCborKind(ZamThread *thread) {
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = kindOf(itemArgument(thread, 0));
    zam_native_return_void(thread);
}

static void stdCborBool(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_BOOL, "CBOR.AsBool of a value that is not a bool");
    codec_write_bool(thread, 1, cbor_get_bool(item));
    zam_native_return_void(thread);
}

static void stdCborInt(ZamThread *thread) {
    cbor_item_t *item = itemArgument(thread, 0);
    int kind = kindOf(item);
    ZamInt value;
    if (kind == KIND_FLT) {
        value = (ZamInt)cbor_float_get_float(item);
    } else if (kind == KIND_INT) {
        uint64_t magnitude = cbor_get_int(item);
        if (magnitude > INT64_MAX)
            zam_native_panic(thread, "CBOR.AsInt of an integer that does not fit an int");
        value = cbor_isa_uint(item) ? (ZamInt)magnitude : -1 - (ZamInt)magnitude;
    } else {
        zam_native_panic(thread, "CBOR.AsInt of a value that is not a number");
    }
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = value;
    zam_native_return_void(thread);
}

static void stdCborFlt(ZamThread *thread) {
    cbor_item_t *item = itemArgument(thread, 0);
    int kind = kindOf(item);
    double value;
    if (kind == KIND_FLT)
        value = cbor_float_get_float(item);
    else if (kind == KIND_INT)
        value = cbor_isa_uint(item) ? (double)cbor_get_int(item) : -1.0 - (double)cbor_get_int(item);
    else
        zam_native_panic(thread, "CBOR.AsFlt of a value that is not a number");
    *(double *)zam_native_argument_pointer(thread, 1) = value;
    zam_native_return_void(thread);
}

// stdCborBytes leaves the contents of a string or byte string in a buffer
// for STD_CODEC_TAKE.
static void stdCborBytes(ZamThread *thread) {
    cbor_item_t *item = itemArgument(thread, 0);
    int kind = kindOf(item);
    if (kind != KIND_STR && kind != KIND_BYTES)
        zam_native_panic(thread, "CBOR.AsBytes of a value that is not a string or byte string");
    CodecBuffer *b = calloc(1, sizeof *b);
    if (b == NULL)
        abort();
    gather(b, item);
    codec_write_word(thread, 1, (ZamWord)(uintptr_t)b);
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = (ZamInt)b->length;
    zam_native_return_void(thread);
}

static void stdCborStr(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_STR, "CBOR.AsStr of a value that is not a string");
    CodecBuffer b = {0};
    gather(&b, item);
    codec_write_string(thread, 1, b.bytes, b.length);
    free(b.bytes);
    zam_native_return_void(thread);
}

static void stdCborLen(ZamThread *thread) {
    cbor_item_t *item = itemArgument(thread, 0);
    ZamInt n;
    switch (kindOf(item)) {
    case KIND_ARRAY: n = (ZamInt)cbor_array_size(item); break;
    case KIND_MAP: n = (ZamInt)cbor_map_size(item); break;
    default: zam_native_panic(thread, "CBOR.Len of a value that is not an array or map");
    }
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = n;
    zam_native_return_void(thread);
}

static void stdCborAt(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_ARRAY, "CBOR.At of a value that is not an array");
    ZamInt i = (ZamInt)zam_native_argument(thread, 1);
    if (i < 0 || (size_t)i >= cbor_array_size(item))
        zam_native_panic(thread, "CBOR.At index out of range");
    writeItem(thread, 2, cbor_array_get(item, (size_t)i));
    zam_native_return_void(thread);
}

// stdCborPair answers the key or the value of map pair i.
static void stdCborPair(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_MAP, "CBOR.KeyAt of a value that is not a map");
    ZamInt i = (ZamInt)zam_native_argument(thread, 1);
    if (i < 0 || (size_t)i >= cbor_map_size(item))
        zam_native_panic(thread, "CBOR map index out of range");
    struct cbor_pair pair = cbor_map_handle(item)[i];
    writeItem(thread, 3, cbor_incref(zam_native_argument(thread, 2) != 0 ? pair.value : pair.key));
    zam_native_return_void(thread);
}

// keyIs reports whether a map key is the string key.
static bool keyIs(const cbor_item_t *k, const ZamString *key) {
    if (!cbor_isa_string(k))
        return false;
    CodecBuffer b = {0};
    gather(&b, k);
    bool same = b.length == key->length && memcmp(b.bytes, key->bytes, key->length) == 0;
    free(b.bytes);
    return same;
}

static ptrdiff_t findKey(const cbor_item_t *map, const ZamString *key) {
    struct cbor_pair *pairs = cbor_map_handle(map);
    for (size_t i = 0; i < cbor_map_size(map); i++)
        if (keyIs(pairs[i].key, key))
            return (ptrdiff_t)i;
    return -1;
}

static void stdCborGet(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_MAP, "CBOR.Get of a value that is not a map");
    ptrdiff_t at = findKey(item, codec_string(thread, 1));
    codec_write_word(thread, 2,
                     at < 0 ? 0 : (ZamWord)(uintptr_t)cbor_incref(cbor_map_handle(item)[at].value));
    codec_write_bool(thread, 3, at >= 0);
    zam_native_return_void(thread);
}

// stdCborKeys answers a map's string keys joined by NUL bytes, a key of any
// other kind reading as empty.
static void stdCborKeys(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_MAP, "CBOR.Keys of a value that is not a map");
    struct cbor_pair *pairs = cbor_map_handle(item);
    CodecBuffer b = {0};
    for (size_t i = 0; i < cbor_map_size(item); i++) {
        if (i > 0)
            codec_buffer_append(&b, "", 1);
        if (cbor_isa_string(pairs[i].key))
            gather(&b, pairs[i].key);
    }
    codec_write_string(thread, 1, b.bytes, b.length);
    free(b.bytes);
    zam_native_return_void(thread);
}

static void stdCborTag(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_TAG, "CBOR.Tag of a value that is not a tag");
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = (ZamInt)cbor_tag_value(item);
    writeItem(thread, 2, cbor_tag_item(item));
    zam_native_return_void(thread);
}

static void stdCborNewNull(ZamThread *thread) {
    writeItem(thread, 0, cbor_new_null());
    zam_native_return_void(thread);
}

static void stdCborNewBool(ZamThread *thread) {
    writeItem(thread, 1, cbor_build_bool(zam_native_argument(thread, 0) != 0));
    zam_native_return_void(thread);
}

// buildInt builds an integer at the smallest width that holds it, which is
// the width it is encoded at.
static cbor_item_t *buildInt(uint64_t magnitude, bool negative) {
    if (magnitude <= UINT8_MAX)
        return negative ? cbor_build_negint8((uint8_t)magnitude) : cbor_build_uint8((uint8_t)magnitude);
    if (magnitude <= UINT16_MAX)
        return negative ? cbor_build_negint16((uint16_t)magnitude) : cbor_build_uint16((uint16_t)magnitude);
    if (magnitude <= UINT32_MAX)
        return negative ? cbor_build_negint32((uint32_t)magnitude) : cbor_build_uint32((uint32_t)magnitude);
    return negative ? cbor_build_negint64(magnitude) : cbor_build_uint64(magnitude);
}

static void stdCborNewInt(ZamThread *thread) {
    ZamInt i = (ZamInt)zam_native_argument(thread, 0);
    writeItem(thread, 1, i >= 0 ? buildInt((uint64_t)i, false) : buildInt((uint64_t)(-1 - i), true));
    zam_native_return_void(thread);
}

static void stdCborNewFlt(ZamThread *thread) {
    uint64_t word = zam_native_argument(thread, 0);
    double value;
    memcpy(&value, &word, sizeof value);
    writeItem(thread, 1, cbor_build_float8(value));
    zam_native_return_void(thread);
}

static void stdCborNewStr(ZamThread *thread) {
    const ZamString *s = codec_string(thread, 0);
    writeItem(thread, 1, cbor_build_stringn(s->bytes, s->length));
    zam_native_return_void(thread);
}

static void stdCborNewBytes(ZamThread *thread) {
    ZamInt n = (ZamInt)zam_native_argument(thread, 1);
    const uint8_t *data = n == 0 ? (const uint8_t *)"" : zam_native_argument_pointer(thread, 0);
    writeItem(thread, 2, cbor_build_bytestring(data, (size_t)n));
    zam_native_return_void(thread);
}

static void stdCborNewArray(ZamThread *thread) {
    writeItem(thread, 0, cbor_new_indefinite_array());
    zam_native_return_void(thread);
}

static void stdCborNewMap(ZamThread *thread) {
    writeItem(thread, 0, cbor_new_indefinite_map());
    zam_native_return_void(thread);
}

static void stdCborNewTag(ZamThread *thread) {
    writeItem(thread, 2, cbor_build_tag((uint64_t)zam_native_argument(thread, 0),
                                        itemArgument(thread, 1)));
    zam_native_return_void(thread);
}

static void stdCborAppend(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_ARRAY, "CBOR.Append of a value that is not an array");
    if (!cbor_array_push(item, itemArgument(thread, 1)))
        zam_native_panic(thread, "CBOR.Append to a parsed array, which is fixed in size");
    zam_native_return_void(thread);
}

// stdCborSet replaces the value of a string key, or adds the pair.
static void stdCborSet(ZamThread *thread) {
    cbor_item_t *item = requireKind(thread, KIND_MAP, "CBOR.Set of a value that is not a map");
    const ZamString *key = codec_string(thread, 1);
    cbor_item_t *value = itemArgument(thread, 2);
    ptrdiff_t at = findKey(item, key);
    if (at >= 0) {
        struct cbor_pair *pair = &cbor_map_handle(item)[at];
        cbor_item_t *old = pair->value;
        pair->value = cbor_incref(value);
        cbor_decref(&old);
    } else {
        cbor_item_t *k = cbor_build_stringn(key->bytes, key->length);
        bool added = cbor_map_add(item, (struct cbor_pair){k, value});
        cbor_decref(&k);
        if (!added)
            zam_native_panic(thread, "CBOR.Set of a new key in a parsed map, which is fixed in size");
    }
    zam_native_return_void(thread);
}

// stdCborEncode leaves the definite encoding in a buffer for STD_CODEC_TAKE.
static void stdCborEncode(ZamThread *thread) {
    cbor_item_t *copy = cbor_copy_definite(itemArgument(thread, 0));
    if (copy == NULL)
        zam_native_panic(thread, "out of memory encoding CBOR");
    unsigned char *bytes = NULL;
    size_t capacity = 0;
    size_t length = cbor_serialize_alloc(copy, &bytes, &capacity);
    cbor_decref(&copy);
    if (bytes == NULL)
        zam_native_panic(thread, "out of memory encoding CBOR");
    CodecBuffer *b = malloc(sizeof *b);
    if (b == NULL)
        abort();
    *b = (CodecBuffer){(char *)bytes, length, capacity};
    codec_write_word(thread, 1, (ZamWord)(uintptr_t)b);
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = (ZamInt)length;
    zam_native_return_void(thread);
}

static void appendText(CodecBuffer *b, const char *text) {
    codec_buffer_append(b, text, strlen(text));
}

// diagnose writes RFC 8949 diagnostic notation.
static void diagnose(CodecBuffer *b, const cbor_item_t *item) {
    char text[64];
    switch (kindOf(item)) {
    case KIND_NULL: appendText(b, "null"); return;
    case KIND_UNDEFINED: appendText(b, "undefined"); return;
    case KIND_BOOL: appendText(b, cbor_get_bool(item) ? "true" : "false"); return;
    case KIND_SIMPLE:
        snprintf(text, sizeof text, "simple(%u)", cbor_ctrl_value(item));
        appendText(b, text);
        return;
    case KIND_INT:
        if (cbor_isa_uint(item))
            snprintf(text, sizeof text, "%llu", (unsigned long long)cbor_get_int(item));
        else if (cbor_get_int(item) == UINT64_MAX)
            snprintf(text, sizeof text, "-18446744073709551616");
        else
            snprintf(text, sizeof text, "-%llu", (unsigned long long)cbor_get_int(item) + 1);
        appendText(b, text);
        return;
    case KIND_FLT: {
        double value = cbor_float_get_float(item);
        if (isnan(value))
            appendText(b, "NaN");
        else if (isinf(value))
            appendText(b, value < 0 ? "-Infinity" : "Infinity");
        else {
            for (int digits = 1; digits <= 17; digits++) {
                snprintf(text, sizeof text, "%.*g", digits, value);
                if (strtod(text, NULL) == value)
                    break;
            }
            appendText(b, text);
            if (strpbrk(text, ".eE") == NULL)
                appendText(b, ".0");
        }
        return;
    }
    case KIND_BYTES: {
        CodecBuffer raw = {0};
        gather(&raw, item);
        appendText(b, "h'");
        for (size_t i = 0; i < raw.length; i++) {
            snprintf(text, sizeof text, "%02x", (unsigned char)raw.bytes[i]);
            appendText(b, text);
        }
        appendText(b, "'");
        free(raw.bytes);
        return;
    }
    case KIND_STR: {
        CodecBuffer raw = {0};
        gather(&raw, item);
        appendText(b, "\"");
        for (size_t i = 0; i < raw.length; i++) {
            unsigned char c = (unsigned char)raw.bytes[i];
            if (c == '"' || c == '\\') {
                snprintf(text, sizeof text, "\\%c", c);
                appendText(b, text);
            } else if (c < 0x20) {
                snprintf(text, sizeof text, "\\u%04x", c);
                appendText(b, text);
            } else {
                codec_buffer_append(b, &raw.bytes[i], 1);
            }
        }
        appendText(b, "\"");
        free(raw.bytes);
        return;
    }
    case KIND_ARRAY:
        appendText(b, "[");
        for (size_t i = 0; i < cbor_array_size(item); i++) {
            if (i > 0)
                appendText(b, ", ");
            diagnose(b, cbor_array_handle(item)[i]);
        }
        appendText(b, "]");
        return;
    case KIND_MAP:
        appendText(b, "{");
        for (size_t i = 0; i < cbor_map_size(item); i++) {
            if (i > 0)
                appendText(b, ", ");
            diagnose(b, cbor_map_handle(item)[i].key);
            appendText(b, ": ");
            diagnose(b, cbor_map_handle(item)[i].value);
        }
        appendText(b, "}");
        return;
    case KIND_TAG: {
        snprintf(text, sizeof text, "%llu(", (unsigned long long)cbor_tag_value(item));
        appendText(b, text);
        cbor_item_t *inner = cbor_tag_item(item);
        diagnose(b, inner);
        cbor_decref(&inner);
        appendText(b, ")");
        return;
    }
    }
}

static void stdCborDiagnose(ZamThread *thread) {
    CodecBuffer b = {0};
    diagnose(&b, itemArgument(thread, 0));
    codec_write_string(thread, 1, b.bytes, b.length);
    free(b.bytes);
    zam_native_return_void(thread);
}

// stdCborEqual compares two values by their definite encodings.
static void stdCborEqual(ZamThread *thread) {
    unsigned char *encoded[2] = {NULL, NULL};
    size_t length[2];
    for (int i = 0; i < 2; i++) {
        cbor_item_t *copy = cbor_copy_definite(itemArgument(thread, (uint64_t)i));
        size_t capacity;
        length[i] = cbor_serialize_alloc(copy, &encoded[i], &capacity);
        cbor_decref(&copy);
    }
    codec_write_bool(thread, 2,
                     length[0] == length[1] && memcmp(encoded[0], encoded[1], length[0]) == 0);
    free(encoded[0]);
    free(encoded[1]);
    zam_native_return_void(thread);
}

static void stdCborRelease(ZamThread *thread) {
    cbor_item_t *item = codec_handle(thread, 0);
    if (item != NULL)
        cbor_decref(&item);
    zam_native_return_void(thread);
}

const ZamNativeEntry zamCborNativeTable[] = {
    {"STD_CBOR_PARSE", stdCborParse},
    {"STD_CBOR_KIND", stdCborKind},
    {"STD_CBOR_BOOL", stdCborBool},
    {"STD_CBOR_INT", stdCborInt},
    {"STD_CBOR_FLT", stdCborFlt},
    {"STD_CBOR_BYTES", stdCborBytes},
    {"STD_CBOR_STR", stdCborStr},
    {"STD_CBOR_LEN", stdCborLen},
    {"STD_CBOR_AT", stdCborAt},
    {"STD_CBOR_PAIR", stdCborPair},
    {"STD_CBOR_GET", stdCborGet},
    {"STD_CBOR_KEYS", stdCborKeys},
    {"STD_CBOR_TAG", stdCborTag},
    {"STD_CBOR_NEW_NULL", stdCborNewNull},
    {"STD_CBOR_NEW_BOOL", stdCborNewBool},
    {"STD_CBOR_NEW_INT", stdCborNewInt},
    {"STD_CBOR_NEW_FLT", stdCborNewFlt},
    {"STD_CBOR_NEW_STR", stdCborNewStr},
    {"STD_CBOR_NEW_BYTES", stdCborNewBytes},
    {"STD_CBOR_NEW_ARRAY", stdCborNewArray},
    {"STD_CBOR_NEW_MAP", stdCborNewMap},
    {"STD_CBOR_NEW_TAG", stdCborNewTag},
    {"STD_CBOR_APPEND", stdCborAppend},
    {"STD_CBOR_SET", stdCborSet},
    {"STD_CBOR_ENCODE", stdCborEncode},
    {"STD_CBOR_DIAGNOSE", stdCborDiagnose},
    {"STD_CBOR_EQUAL", stdCborEqual},
    {"STD_CBOR_RELEASE", stdCborRelease},
};

const uint64_t zamCborNativeTableCount =
    sizeof(zamCborNativeTable) / sizeof(zamCborNativeTable[0]);
