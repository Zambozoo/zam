#include <json-c/json.h>
#include <stdio.h>

#include "codec.h"

// The JSON natives, over json-c. A value crosses as a handle word: a
// json_object* the zam side holds one reference to, or zero for JSON null,
// which is how json-c spells null too. Every handle handed out is a reference
// of its own, released by STD_JSON_RELEASE.

// The kinds the zam side numbers, in JSON.Kind order.
enum { KIND_NULL, KIND_BOOL, KIND_INT, KIND_FLT, KIND_STR, KIND_ARRAY, KIND_OBJECT };

static json_object *handle(ZamThread *thread, uint64_t wordIndex) {
    return codec_handle(thread, wordIndex);
}

static void writeHandle(ZamThread *thread, uint64_t wordIndex, json_object *o) {
    codec_write_word(thread, wordIndex, (ZamWord)(uintptr_t)o);
}

static int kindOf(json_object *o) {
    switch (json_object_get_type(o)) {
    case json_type_null: return KIND_NULL;
    case json_type_boolean: return KIND_BOOL;
    case json_type_int: return KIND_INT;
    case json_type_double: return KIND_FLT;
    case json_type_string: return KIND_STR;
    case json_type_array: return KIND_ARRAY;
    case json_type_object: return KIND_OBJECT;
    }
    return KIND_NULL;
}

static json_object *requireKind(ZamThread *thread, uint64_t wordIndex, int kind,
                                const char *message) {
    json_object *o = handle(thread, wordIndex);
    if (kindOf(o) != kind)
        zam_native_panic(thread, message);
    return o;
}

// stdJsonParse parses a whole document, which may be surrounded by
// whitespace and nothing else.
static void stdJsonParse(ZamThread *thread) {
    const ZamString *s = codec_string(thread, 0);
    char *text = malloc(s->length + 1);
    if (text == NULL)
        zam_native_panic(thread, "out of memory parsing JSON");
    memcpy(text, s->bytes, s->length);
    text[s->length] = '\0';

    json_tokener *tok = json_tokener_new();
    json_object *o = json_tokener_parse_ex(tok, text, (int)s->length + 1);
    enum json_tokener_error err = json_tokener_get_error(tok);
    size_t end = json_tokener_get_parse_end(tok);
    json_tokener_free(tok);

    if (err == json_tokener_success)
        for (size_t i = end; i < s->length; i++)
            if (strchr(" \t\r\n", text[i]) == NULL) {
                err = json_tokener_error_parse_unexpected;
                end = i;
                break;
            }
    free(text);

    if (err != json_tokener_success) {
        json_object_put(o);
        char message[160];
        snprintf(message, sizeof message, "%s at offset %zu",
                 json_tokener_error_desc(err), end);
        writeHandle(thread, 1, NULL);
        codec_write_error(thread, 2, message);
    } else {
        writeHandle(thread, 1, o);
        codec_write_error(thread, 2, NULL);
    }
    zam_native_return_void(thread);
}

static void stdJsonKind(ZamThread *thread) {
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = kindOf(handle(thread, 0));
    zam_native_return_void(thread);
}

static void stdJsonBool(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_BOOL, "JSON.Bool of a value that is not a bool");
    *(bool *)zam_native_argument_pointer(thread, 1) = json_object_get_boolean(o) != 0;
    zam_native_return_void(thread);
}

// stdJsonInt reads an int, or a flt truncated toward zero.
static void stdJsonInt(ZamThread *thread) {
    json_object *o = handle(thread, 0);
    int kind = kindOf(o);
    if (kind != KIND_INT && kind != KIND_FLT)
        zam_native_panic(thread, "JSON.Int of a value that is not a number");
    *(ZamInt *)zam_native_argument_pointer(thread, 1) =
        kind == KIND_INT ? json_object_get_int64(o) : (ZamInt)json_object_get_double(o);
    zam_native_return_void(thread);
}

static void stdJsonFlt(ZamThread *thread) {
    json_object *o = handle(thread, 0);
    int kind = kindOf(o);
    if (kind != KIND_INT && kind != KIND_FLT)
        zam_native_panic(thread, "JSON.Flt of a value that is not a number");
    *(double *)zam_native_argument_pointer(thread, 1) =
        kind == KIND_INT ? (double)json_object_get_int64(o) : json_object_get_double(o);
    zam_native_return_void(thread);
}

static void stdJsonStr(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_STR, "JSON.Str of a value that is not a string");
    codec_write_string(thread, 1, json_object_get_string(o), (size_t)json_object_get_string_len(o));
    zam_native_return_void(thread);
}

// stdJsonLen counts an array's elements or an object's members.
static void stdJsonLen(ZamThread *thread) {
    json_object *o = handle(thread, 0);
    ZamInt n;
    switch (kindOf(o)) {
    case KIND_ARRAY: n = (ZamInt)json_object_array_length(o); break;
    case KIND_OBJECT: n = json_object_object_length(o); break;
    default: zam_native_panic(thread, "JSON.Len of a value that is not an array or object");
    }
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = n;
    zam_native_return_void(thread);
}

static void stdJsonAt(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_ARRAY, "JSON.At of a value that is not an array");
    ZamInt i = (ZamInt)zam_native_argument(thread, 1);
    if (i < 0 || (size_t)i >= json_object_array_length(o))
        zam_native_panic(thread, "JSON.At index out of range");
    writeHandle(thread, 2, json_object_get(json_object_array_get_idx(o, (size_t)i)));
    zam_native_return_void(thread);
}

static void stdJsonGet(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_OBJECT, "JSON.Get of a value that is not an object");
    char *key = codec_c_string(thread, 1);
    json_object *member = NULL;
    json_bool found = json_object_object_get_ex(o, key, &member);
    free(key);
    writeHandle(thread, 2, found ? json_object_get(member) : NULL);
    *(bool *)zam_native_argument_pointer(thread, 3) = found != 0;
    zam_native_return_void(thread);
}

// stdJsonKeys answers an object's keys in insertion order, joined by NUL
// bytes into one string the zam side splits.
static void stdJsonKeys(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_OBJECT, "JSON.Keys of a value that is not an object");
    size_t bytes = 0;
    struct json_object_iterator end = json_object_iter_end(o);
    for (struct json_object_iterator it = json_object_iter_begin(o);
         !json_object_iter_equal(&it, &end); json_object_iter_next(&it))
        bytes += strlen(json_object_iter_peek_name(&it)) + 1;
    char *joined = malloc(bytes + 1);
    if (joined == NULL)
        zam_native_panic(thread, "out of memory listing JSON keys");
    size_t at = 0;
    for (struct json_object_iterator it = json_object_iter_begin(o);
         !json_object_iter_equal(&it, &end); json_object_iter_next(&it)) {
        const char *key = json_object_iter_peek_name(&it);
        if (at > 0)
            joined[at++] = '\0';
        size_t length = strlen(key);
        memcpy(joined + at, key, length);
        at += length;
    }
    codec_write_string(thread, 1, joined, at);
    free(joined);
    zam_native_return_void(thread);
}

static void stdJsonNewBool(ZamThread *thread) {
    writeHandle(thread, 1, json_object_new_boolean(zam_native_argument(thread, 0) != 0));
    zam_native_return_void(thread);
}

static void stdJsonNewInt(ZamThread *thread) {
    writeHandle(thread, 1, json_object_new_int64((int64_t)zam_native_argument(thread, 0)));
    zam_native_return_void(thread);
}

static void stdJsonNewFlt(ZamThread *thread) {
    uint64_t word = zam_native_argument(thread, 0);
    double value;
    memcpy(&value, &word, sizeof value);
    writeHandle(thread, 1, json_object_new_double(value));
    zam_native_return_void(thread);
}

static void stdJsonNewStr(ZamThread *thread) {
    const ZamString *s = codec_string(thread, 0);
    writeHandle(thread, 1, json_object_new_string_len((const char *)s->bytes, (int)s->length));
    zam_native_return_void(thread);
}

static void stdJsonNewArray(ZamThread *thread) {
    writeHandle(thread, 0, json_object_new_array());
    zam_native_return_void(thread);
}

static void stdJsonNewObject(ZamThread *thread) {
    writeHandle(thread, 0, json_object_new_object());
    zam_native_return_void(thread);
}

// stdJsonSet adds or replaces a member. The object takes a reference of its
// own to the value, so the zam handle keeps the one it holds.
static void stdJsonSet(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_OBJECT, "JSON.Set of a value that is not an object");
    char *key = codec_c_string(thread, 1);
    json_object_object_add(o, key, json_object_get(handle(thread, 2)));
    free(key);
    zam_native_return_void(thread);
}

static void stdJsonRemove(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_OBJECT, "JSON.Remove of a value that is not an object");
    char *key = codec_c_string(thread, 1);
    json_object_object_del(o, key);
    free(key);
    zam_native_return_void(thread);
}

static void stdJsonAppend(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_ARRAY, "JSON.Append of a value that is not an array");
    json_object_array_add(o, json_object_get(handle(thread, 1)));
    zam_native_return_void(thread);
}

static void stdJsonSetAt(ZamThread *thread) {
    json_object *o = requireKind(thread, 0, KIND_ARRAY, "JSON.SetAt of a value that is not an array");
    ZamInt i = (ZamInt)zam_native_argument(thread, 1);
    if (i < 0 || (size_t)i >= json_object_array_length(o))
        zam_native_panic(thread, "JSON.SetAt index out of range");
    json_object_array_put_idx(o, (size_t)i, json_object_get(handle(thread, 2)));
    zam_native_return_void(thread);
}

static void stdJsonEncode(ZamThread *thread) {
    int flags = JSON_C_TO_STRING_NOSLASHESCAPE |
                (zam_native_argument(thread, 1) != 0 ? JSON_C_TO_STRING_PRETTY | JSON_C_TO_STRING_SPACED
                                                     : JSON_C_TO_STRING_PLAIN);
    size_t length;
    const char *text = json_object_to_json_string_length(handle(thread, 0), flags, &length);
    codec_write_string(thread, 2, text, length);
    zam_native_return_void(thread);
}

static void stdJsonEqual(ZamThread *thread) {
    *(bool *)zam_native_argument_pointer(thread, 2) =
        json_object_equal(handle(thread, 0), handle(thread, 1)) != 0;
    zam_native_return_void(thread);
}

static void stdJsonRelease(ZamThread *thread) {
    json_object_put(handle(thread, 0));
    zam_native_return_void(thread);
}

const ZamNativeEntry zamJsonNativeTable[] = {
    {"STD_JSON_PARSE", stdJsonParse},
    {"STD_JSON_KIND", stdJsonKind},
    {"STD_JSON_BOOL", stdJsonBool},
    {"STD_JSON_INT", stdJsonInt},
    {"STD_JSON_FLT", stdJsonFlt},
    {"STD_JSON_STR", stdJsonStr},
    {"STD_JSON_LEN", stdJsonLen},
    {"STD_JSON_AT", stdJsonAt},
    {"STD_JSON_GET", stdJsonGet},
    {"STD_JSON_KEYS", stdJsonKeys},
    {"STD_JSON_NEW_BOOL", stdJsonNewBool},
    {"STD_JSON_NEW_INT", stdJsonNewInt},
    {"STD_JSON_NEW_FLT", stdJsonNewFlt},
    {"STD_JSON_NEW_STR", stdJsonNewStr},
    {"STD_JSON_NEW_ARRAY", stdJsonNewArray},
    {"STD_JSON_NEW_OBJECT", stdJsonNewObject},
    {"STD_JSON_SET", stdJsonSet},
    {"STD_JSON_REMOVE", stdJsonRemove},
    {"STD_JSON_APPEND", stdJsonAppend},
    {"STD_JSON_SET_AT", stdJsonSetAt},
    {"STD_JSON_ENCODE", stdJsonEncode},
    {"STD_JSON_EQUAL", stdJsonEqual},
    {"STD_JSON_RELEASE", stdJsonRelease},
};

const uint64_t zamJsonNativeTableCount =
    sizeof(zamJsonNativeTable) / sizeof(zamJsonNativeTable[0]);
