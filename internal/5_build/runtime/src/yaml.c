#include <errno.h>
#include <math.h>
#include <stdio.h>
#include <yaml.h>

#include "codec.h"

// The YAML natives, over libyaml's document API. A value is a document and a
// node index within it. A document is shared by every value in it and counts
// them, being freed with the last. Node zero stands for an empty document,
// read as null.

// The kinds the zam side numbers, in YAML.Kind order.
enum { KIND_NULL, KIND_BOOL, KIND_INT, KIND_FLT, KIND_STR, KIND_SEQ, KIND_MAP };

typedef struct YamlDoc {
    int64_t refs;
    yaml_document_t doc;
} YamlDoc;

static YamlDoc *docArgument(ZamThread *thread, uint64_t wordIndex) {
    YamlDoc *d = codec_handle(thread, wordIndex);
    if (d == NULL)
        zam_native_panic(thread, "YAML value was released");
    return d;
}

static yaml_node_t *nodeOf(YamlDoc *d, int64_t index) {
    return index == 0 ? NULL : yaml_document_get_node(&d->doc, (int)index);
}

static bool textIs(const char *text, size_t n, const char *const *options) {
    for (; *options != NULL; options++)
        if (strlen(*options) == n && memcmp(text, *options, n) == 0)
            return true;
    return false;
}

static const char *const nullTexts[] = {"", "~", "null", "Null", "NULL", NULL};
static const char *const trueTexts[] = {"true", "True", "TRUE", NULL};
static const char *const falseTexts[] = {"false", "False", "FALSE", NULL};

// parseInt reads a core schema integer: decimal, 0o octal or 0x hex.
static bool parseInt(const char *text, size_t n, int64_t *out) {
    char buffer[72];
    if (n == 0 || n >= sizeof buffer)
        return false;
    memcpy(buffer, text, n);
    buffer[n] = '\0';
    const char *digits = buffer;
    int base = 10;
    bool negative = false;
    if (*digits == '+' || *digits == '-') {
        negative = *digits == '-';
        digits++;
    }
    if (digits[0] == '0' && (digits[1] == 'x' || digits[1] == 'o') && base == 10 &&
        digits == buffer) {
        base = digits[1] == 'x' ? 16 : 8;
        digits += 2;
    }
    if (*digits == '\0')
        return false;
    for (const char *c = digits; *c != '\0'; c++) {
        int v = *c >= '0' && *c <= '9' ? *c - '0'
                : *c >= 'a' && *c <= 'f' ? *c - 'a' + 10
                : *c >= 'A' && *c <= 'F' ? *c - 'A' + 10
                                          : 99;
        if (v >= base)
            return false;
    }
    errno = 0;
    char *end;
    long long value = strtoll(digits, &end, base);
    if (errno != 0 || *end != '\0')
        return false;
    *out = negative ? -value : value;
    return true;
}

// parseFlt reads a core schema float, including .inf and .nan.
static bool parseFlt(const char *text, size_t n, double *out) {
    char buffer[128];
    if (n == 0 || n >= sizeof buffer)
        return false;
    memcpy(buffer, text, n);
    buffer[n] = '\0';
    const char *body = buffer;
    double sign = 1;
    if (*body == '+' || *body == '-') {
        sign = *body == '-' ? -1 : 1;
        body++;
    }
    static const char *const infTexts[] = {".inf", ".Inf", ".INF", NULL};
    static const char *const nanTexts[] = {".nan", ".NaN", ".NAN", NULL};
    if (textIs(body, strlen(body), infTexts)) {
        *out = sign * INFINITY;
        return true;
    }
    if (body == buffer && textIs(body, strlen(body), nanTexts)) {
        *out = NAN;
        return true;
    }
    bool digit = false;
    for (const char *c = body; *c != '\0'; c++) {
        if (*c >= '0' && *c <= '9')
            digit = true;
        else if (*c != '.' && *c != 'e' && *c != 'E' && *c != '+' && *c != '-')
            return false;
    }
    if (!digit)
        return false;
    char *end;
    double value = strtod(buffer, &end);
    if (*end != '\0')
        return false;
    *out = value;
    return true;
}

// scalarKind resolves a scalar under the core schema. Only a plain scalar is
// resolved: a quoted or block one is always a string.
static int scalarKind(const yaml_node_t *n) {
    if (n->data.scalar.style != YAML_PLAIN_SCALAR_STYLE)
        return KIND_STR;
    const char *text = (const char *)n->data.scalar.value;
    size_t length = n->data.scalar.length;
    int64_t i;
    double f;
    if (textIs(text, length, nullTexts))
        return KIND_NULL;
    if (textIs(text, length, trueTexts) || textIs(text, length, falseTexts))
        return KIND_BOOL;
    if (parseInt(text, length, &i))
        return KIND_INT;
    if (parseFlt(text, length, &f))
        return KIND_FLT;
    return KIND_STR;
}

static int kindOf(const yaml_node_t *n) {
    if (n == NULL)
        return KIND_NULL;
    switch (n->type) {
    case YAML_SCALAR_NODE: return scalarKind(n);
    case YAML_SEQUENCE_NODE: return KIND_SEQ;
    case YAML_MAPPING_NODE: return KIND_MAP;
    default: return KIND_NULL;
    }
}

static yaml_node_t *requireScalar(ZamThread *thread, const char *message) {
    yaml_node_t *n = nodeOf(docArgument(thread, 0), (int64_t)zam_native_argument(thread, 1));
    if (n == NULL || n->type != YAML_SCALAR_NODE)
        zam_native_panic(thread, message);
    return n;
}

static yaml_node_t *requireType(ZamThread *thread, yaml_node_type_t type, const char *message) {
    yaml_node_t *n = nodeOf(docArgument(thread, 0), (int64_t)zam_native_argument(thread, 1));
    if (n == NULL || n->type != type)
        zam_native_panic(thread, message);
    return n;
}

static YamlDoc *newDoc(void) {
    YamlDoc *d = calloc(1, sizeof *d);
    if (d == NULL)
        abort();
    yaml_document_initialize(&d->doc, NULL, NULL, NULL, 1, 1);
    return d;
}

// YamlList holds the documents of a parsed stream until the zam side has
// taken each.
typedef struct YamlList {
    size_t count;
    YamlDoc **docs;
} YamlList;

// stdYamlParse loads every document in a stream.
static void stdYamlParse(ZamThread *thread) {
    const ZamString *s = codec_string(thread, 0);
    yaml_parser_t parser;
    yaml_parser_initialize(&parser);
    yaml_parser_set_input_string(&parser, (const unsigned char *)s->bytes, s->length);

    YamlList *list = calloc(1, sizeof *list);
    size_t capacity = 0;
    char message[256];
    bool failed = false;
    for (;;) {
        YamlDoc *d = calloc(1, sizeof *d);
        if (d == NULL || list == NULL)
            abort();
        if (!yaml_parser_load(&parser, &d->doc)) {
            snprintf(message, sizeof message, "%s at line %zu, column %zu",
                     parser.problem != NULL ? parser.problem : "invalid YAML",
                     parser.problem_mark.line + 1, parser.problem_mark.column + 1);
            free(d);
            failed = true;
            break;
        }
        if (yaml_document_get_root_node(&d->doc) == NULL) {
            yaml_document_delete(&d->doc);
            free(d);
            break;
        }
        if (list->count == capacity) {
            capacity = capacity == 0 ? 4 : capacity * 2;
            YamlDoc **grown = realloc(list->docs, capacity * sizeof *grown);
            if (grown == NULL)
                abort();
            list->docs = grown;
        }
        list->docs[list->count++] = d;
    }
    yaml_parser_delete(&parser);

    if (failed) {
        for (size_t i = 0; i < list->count; i++) {
            yaml_document_delete(&list->docs[i]->doc);
            free(list->docs[i]);
        }
        free(list->docs);
        free(list);
        codec_write_word(thread, 1, 0);
        codec_write_word(thread, 2, 0);
        codec_write_error(thread, 3, message);
    } else {
        *(ZamInt *)zam_native_argument_pointer(thread, 2) = (ZamInt)list->count;
        if (list->count == 0) {
            free(list->docs);
            free(list);
            list = NULL;
        }
        codec_write_word(thread, 1, (ZamWord)(uintptr_t)list);
        codec_write_error(thread, 3, NULL);
    }
    zam_native_return_void(thread);
}

// stdYamlTake hands over document i of a parsed stream, and frees the list
// once the last has been taken.
static void stdYamlTake(ZamThread *thread) {
    YamlList *list = codec_handle(thread, 0);
    ZamInt i = (ZamInt)zam_native_argument(thread, 1);
    codec_write_word(thread, 2, (ZamWord)(uintptr_t)list->docs[i]);
    if ((size_t)i + 1 == list->count) {
        free(list->docs);
        free(list);
    }
    zam_native_return_void(thread);
}

static void stdYamlRetain(ZamThread *thread) {
    docArgument(thread, 0)->refs++;
    zam_native_return_void(thread);
}

static void stdYamlRelease(ZamThread *thread) {
    YamlDoc *d = codec_handle(thread, 0);
    if (d != NULL && --d->refs == 0) {
        yaml_document_delete(&d->doc);
        free(d);
    }
    zam_native_return_void(thread);
}

static void stdYamlKind(ZamThread *thread) {
    yaml_node_t *n = nodeOf(docArgument(thread, 0), (int64_t)zam_native_argument(thread, 1));
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = kindOf(n);
    zam_native_return_void(thread);
}

static void stdYamlScalar(ZamThread *thread) {
    yaml_node_t *n = requireScalar(thread, "YAML.AsStr of a value that is not a scalar");
    codec_write_string(thread, 2, (const char *)n->data.scalar.value, n->data.scalar.length);
    zam_native_return_void(thread);
}

static void stdYamlBool(ZamThread *thread) {
    yaml_node_t *n = requireScalar(thread, "YAML.AsBool of a value that is not a bool");
    if (kindOf(n) != KIND_BOOL)
        zam_native_panic(thread, "YAML.AsBool of a value that is not a bool");
    codec_write_bool(thread, 2, textIs((const char *)n->data.scalar.value,
                                       n->data.scalar.length, trueTexts));
    zam_native_return_void(thread);
}

static void stdYamlInt(ZamThread *thread) {
    yaml_node_t *n = requireScalar(thread, "YAML.AsInt of a value that is not a number");
    int64_t i;
    double f;
    const char *text = (const char *)n->data.scalar.value;
    if (kindOf(n) == KIND_INT && parseInt(text, n->data.scalar.length, &i))
        *(ZamInt *)zam_native_argument_pointer(thread, 2) = i;
    else if (kindOf(n) == KIND_FLT && parseFlt(text, n->data.scalar.length, &f))
        *(ZamInt *)zam_native_argument_pointer(thread, 2) = (ZamInt)f;
    else
        zam_native_panic(thread, "YAML.AsInt of a value that is not a number");
    zam_native_return_void(thread);
}

static void stdYamlFlt(ZamThread *thread) {
    yaml_node_t *n = requireScalar(thread, "YAML.AsFlt of a value that is not a number");
    int64_t i;
    double f;
    const char *text = (const char *)n->data.scalar.value;
    if (kindOf(n) == KIND_INT && parseInt(text, n->data.scalar.length, &i))
        f = (double)i;
    else if (kindOf(n) != KIND_FLT || !parseFlt(text, n->data.scalar.length, &f))
        zam_native_panic(thread, "YAML.AsFlt of a value that is not a number");
    *(double *)zam_native_argument_pointer(thread, 2) = f;
    zam_native_return_void(thread);
}

static void stdYamlLen(ZamThread *thread) {
    yaml_node_t *n = nodeOf(docArgument(thread, 0), (int64_t)zam_native_argument(thread, 1));
    ZamInt length;
    if (n != NULL && n->type == YAML_SEQUENCE_NODE)
        length = n->data.sequence.items.top - n->data.sequence.items.start;
    else if (n != NULL && n->type == YAML_MAPPING_NODE)
        length = n->data.mapping.pairs.top - n->data.mapping.pairs.start;
    else
        zam_native_panic(thread, "YAML.Len of a value that is not a sequence or mapping");
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = length;
    zam_native_return_void(thread);
}

static void stdYamlAt(ZamThread *thread) {
    yaml_node_t *n = requireType(thread, YAML_SEQUENCE_NODE, "YAML.At of a value that is not a sequence");
    ZamInt i = (ZamInt)zam_native_argument(thread, 2);
    if (i < 0 || i >= n->data.sequence.items.top - n->data.sequence.items.start)
        zam_native_panic(thread, "YAML.At index out of range");
    *(ZamInt *)zam_native_argument_pointer(thread, 3) = n->data.sequence.items.start[i];
    zam_native_return_void(thread);
}

// findKey returns the index of the pair whose scalar key is key, or -1.
static ptrdiff_t findKey(YamlDoc *d, yaml_node_t *map, const char *key, size_t length) {
    for (yaml_node_pair_t *pair = map->data.mapping.pairs.start;
         pair < map->data.mapping.pairs.top; pair++) {
        yaml_node_t *k = yaml_document_get_node(&d->doc, pair->key);
        if (k != NULL && k->type == YAML_SCALAR_NODE && k->data.scalar.length == length &&
            memcmp(k->data.scalar.value, key, length) == 0)
            return pair - map->data.mapping.pairs.start;
    }
    return -1;
}

static void stdYamlGet(ZamThread *thread) {
    YamlDoc *d = docArgument(thread, 0);
    yaml_node_t *n = requireType(thread, YAML_MAPPING_NODE, "YAML.Get of a value that is not a mapping");
    const ZamString *key = codec_string(thread, 2);
    ptrdiff_t at = findKey(d, n, key->bytes, key->length);
    *(ZamInt *)zam_native_argument_pointer(thread, 3) =
        at < 0 ? 0 : n->data.mapping.pairs.start[at].value;
    codec_write_bool(thread, 4, at >= 0);
    zam_native_return_void(thread);
}

// stdYamlKeys answers a mapping's scalar keys joined by NUL bytes; a key that
// is not a scalar reads as empty.
static void stdYamlKeys(ZamThread *thread) {
    YamlDoc *d = docArgument(thread, 0);
    yaml_node_t *n = requireType(thread, YAML_MAPPING_NODE, "YAML.Keys of a value that is not a mapping");
    CodecBuffer b = {0};
    for (yaml_node_pair_t *pair = n->data.mapping.pairs.start;
         pair < n->data.mapping.pairs.top; pair++) {
        if (pair > n->data.mapping.pairs.start)
            codec_buffer_append(&b, "", 1);
        yaml_node_t *k = yaml_document_get_node(&d->doc, pair->key);
        if (k != NULL && k->type == YAML_SCALAR_NODE)
            codec_buffer_append(&b, k->data.scalar.value, k->data.scalar.length);
    }
    codec_write_string(thread, 2, b.bytes, b.length);
    free(b.bytes);
    zam_native_return_void(thread);
}

// addScalar adds a scalar, quoting a string that would otherwise read back
// as something else.
static int addScalar(yaml_document_t *doc, const char *text, size_t length, bool string) {
    yaml_scalar_style_t style = string ? YAML_ANY_SCALAR_STYLE : YAML_PLAIN_SCALAR_STYLE;
    if (string) {
        yaml_node_t probe = {0};
        probe.type = YAML_SCALAR_NODE;
        probe.data.scalar.value = (yaml_char_t *)text;
        probe.data.scalar.length = length;
        probe.data.scalar.style = YAML_PLAIN_SCALAR_STYLE;
        if (scalarKind(&probe) != KIND_STR)
            style = YAML_DOUBLE_QUOTED_SCALAR_STYLE;
    }
    return yaml_document_add_scalar(doc, NULL, (yaml_char_t *)text, (int)length, style);
}

// copyNode copies the subtree at index in src into dst and returns its index
// there. Every node is fetched again after an add, which may move the nodes
// of dst, src among them when the two are one.
static int copyNode(ZamThread *thread, yaml_document_t *dst, yaml_document_t *src,
                    int index, int depth) {
    if (depth > 10000)
        zam_native_panic(thread, "YAML value nests too deeply to copy");
    if (index == 0)
        return yaml_document_add_scalar(dst, NULL, (yaml_char_t *)"null", 4, YAML_PLAIN_SCALAR_STYLE);
    yaml_node_t *n = yaml_document_get_node(src, index);
    switch (n->type) {
    case YAML_SCALAR_NODE: {
        size_t length = n->data.scalar.length;
        char *text = malloc(length + 1);
        if (text == NULL)
            abort();
        memcpy(text, n->data.scalar.value, length);
        yaml_scalar_style_t style = n->data.scalar.style;
        int out = yaml_document_add_scalar(dst, NULL, (yaml_char_t *)text, (int)length, style);
        free(text);
        return out;
    }
    case YAML_SEQUENCE_NODE: {
        int out = yaml_document_add_sequence(dst, NULL, n->data.sequence.style);
        for (ptrdiff_t i = 0;; i++) {
            n = yaml_document_get_node(src, index);
            if (i >= n->data.sequence.items.top - n->data.sequence.items.start)
                break;
            int item = copyNode(thread, dst, src, n->data.sequence.items.start[i], depth + 1);
            yaml_document_append_sequence_item(dst, out, item);
        }
        return out;
    }
    case YAML_MAPPING_NODE: {
        int out = yaml_document_add_mapping(dst, NULL, n->data.mapping.style);
        for (ptrdiff_t i = 0;; i++) {
            n = yaml_document_get_node(src, index);
            if (i >= n->data.mapping.pairs.top - n->data.mapping.pairs.start)
                break;
            yaml_node_pair_t pair = n->data.mapping.pairs.start[i];
            int key = copyNode(thread, dst, src, pair.key, depth + 1);
            int value = copyNode(thread, dst, src, pair.value, depth + 1);
            yaml_document_append_mapping_pair(dst, out, key, value);
        }
        return out;
    }
    default:
        return yaml_document_add_scalar(dst, NULL, (yaml_char_t *)"null", 4, YAML_PLAIN_SCALAR_STYLE);
    }
}

// stdYamlNewScalar starts a document holding one scalar.
static void stdYamlNewScalar(ZamThread *thread) {
    const ZamString *s = codec_string(thread, 0);
    YamlDoc *d = newDoc();
    int node = addScalar(&d->doc, s->bytes, s->length, zam_native_argument(thread, 1) != 0);
    codec_write_word(thread, 2, (ZamWord)(uintptr_t)d);
    *(ZamInt *)zam_native_argument_pointer(thread, 3) = node;
    zam_native_return_void(thread);
}

// stdYamlNewFlt starts a document holding one float, written with enough
// digits to read back exactly.
static void stdYamlNewFlt(ZamThread *thread) {
    uint64_t word = zam_native_argument(thread, 0);
    double value;
    memcpy(&value, &word, sizeof value);
    char text[40];
    if (isnan(value))
        snprintf(text, sizeof text, ".nan");
    else if (isinf(value))
        snprintf(text, sizeof text, value < 0 ? "-.inf" : ".inf");
    else {
        for (int digits = 1; digits <= 17; digits++) {
            snprintf(text, sizeof text, "%.*g", digits, value);
            if (strtod(text, NULL) == value)
                break;
        }
        if (strpbrk(text, ".eE") == NULL)
            strcat(text, ".0");
    }
    YamlDoc *d = newDoc();
    int node = yaml_document_add_scalar(&d->doc, NULL, (yaml_char_t *)text, (int)strlen(text),
                                        YAML_PLAIN_SCALAR_STYLE);
    codec_write_word(thread, 1, (ZamWord)(uintptr_t)d);
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = node;
    zam_native_return_void(thread);
}

static void stdYamlNewCollection(ZamThread *thread) {
    YamlDoc *d = newDoc();
    int node = zam_native_argument(thread, 0) != 0
                   ? yaml_document_add_mapping(&d->doc, NULL, YAML_ANY_MAPPING_STYLE)
                   : yaml_document_add_sequence(&d->doc, NULL, YAML_ANY_SEQUENCE_STYLE);
    codec_write_word(thread, 1, (ZamWord)(uintptr_t)d);
    *(ZamInt *)zam_native_argument_pointer(thread, 2) = node;
    zam_native_return_void(thread);
}

// stdYamlSet sets a mapping's member to a copy of a value.
static void stdYamlSet(ZamThread *thread) {
    YamlDoc *d = docArgument(thread, 0);
    int map = (int)zam_native_argument(thread, 1);
    requireType(thread, YAML_MAPPING_NODE, "YAML.Set of a value that is not a mapping");
    const ZamString *key = codec_string(thread, 2);
    YamlDoc *src = docArgument(thread, 3);
    int value = copyNode(thread, &d->doc, &src->doc, (int)zam_native_argument(thread, 4), 0);
    yaml_node_t *n = yaml_document_get_node(&d->doc, map);
    ptrdiff_t at = findKey(d, n, key->bytes, key->length);
    if (at >= 0) {
        n->data.mapping.pairs.start[at].value = value;
    } else {
        int k = addScalar(&d->doc, key->bytes, key->length, true);
        yaml_document_append_mapping_pair(&d->doc, map, k, value);
    }
    zam_native_return_void(thread);
}

static void stdYamlRemove(ZamThread *thread) {
    YamlDoc *d = docArgument(thread, 0);
    yaml_node_t *n = requireType(thread, YAML_MAPPING_NODE, "YAML.Remove of a value that is not a mapping");
    const ZamString *key = codec_string(thread, 2);
    ptrdiff_t at = findKey(d, n, key->bytes, key->length);
    if (at >= 0) {
        yaml_node_pair_t *pairs = n->data.mapping.pairs.start;
        ptrdiff_t count = n->data.mapping.pairs.top - pairs;
        memmove(pairs + at, pairs + at + 1, (size_t)(count - at - 1) * sizeof *pairs);
        n->data.mapping.pairs.top--;
    }
    zam_native_return_void(thread);
}

static void stdYamlAppend(ZamThread *thread) {
    YamlDoc *d = docArgument(thread, 0);
    int seq = (int)zam_native_argument(thread, 1);
    requireType(thread, YAML_SEQUENCE_NODE, "YAML.Append of a value that is not a sequence");
    YamlDoc *src = docArgument(thread, 2);
    int item = copyNode(thread, &d->doc, &src->doc, (int)zam_native_argument(thread, 3), 0);
    yaml_document_append_sequence_item(&d->doc, seq, item);
    zam_native_return_void(thread);
}

static int writeOutput(void *data, unsigned char *buffer, size_t size) {
    codec_buffer_append(data, buffer, size);
    return 1;
}

// stdYamlEncode emits the subtree at a node as a document of its own. The
// emitter consumes what it is handed, so it is handed a copy.
static void stdYamlEncode(ZamThread *thread) {
    YamlDoc *d = docArgument(thread, 0);
    YamlDoc *copy = newDoc();
    copyNode(thread, &copy->doc, &d->doc, (int)zam_native_argument(thread, 1), 0);

    CodecBuffer b = {0};
    yaml_emitter_t emitter;
    yaml_emitter_initialize(&emitter);
    yaml_emitter_set_output(&emitter, writeOutput, &b);
    yaml_emitter_set_unicode(&emitter, 1);
    yaml_emitter_open(&emitter);
    yaml_emitter_dump(&emitter, &copy->doc);
    yaml_emitter_close(&emitter);
    yaml_emitter_delete(&emitter);
    free(copy);

    codec_write_string(thread, 2, b.bytes, b.length);
    free(b.bytes);
    zam_native_return_void(thread);
}

const ZamNativeEntry zamYamlNativeTable[] = {
    {"STD_YAML_PARSE", stdYamlParse},
    {"STD_YAML_TAKE", stdYamlTake},
    {"STD_YAML_RETAIN", stdYamlRetain},
    {"STD_YAML_RELEASE", stdYamlRelease},
    {"STD_YAML_KIND", stdYamlKind},
    {"STD_YAML_SCALAR", stdYamlScalar},
    {"STD_YAML_BOOL", stdYamlBool},
    {"STD_YAML_INT", stdYamlInt},
    {"STD_YAML_FLT", stdYamlFlt},
    {"STD_YAML_LEN", stdYamlLen},
    {"STD_YAML_AT", stdYamlAt},
    {"STD_YAML_GET", stdYamlGet},
    {"STD_YAML_KEYS", stdYamlKeys},
    {"STD_YAML_NEW_SCALAR", stdYamlNewScalar},
    {"STD_YAML_NEW_FLT", stdYamlNewFlt},
    {"STD_YAML_NEW_COLLECTION", stdYamlNewCollection},
    {"STD_YAML_SET", stdYamlSet},
    {"STD_YAML_REMOVE", stdYamlRemove},
    {"STD_YAML_APPEND", stdYamlAppend},
    {"STD_YAML_ENCODE", stdYamlEncode},
};

const uint64_t zamYamlNativeTableCount =
    sizeof(zamYamlNativeTable) / sizeof(zamYamlNativeTable[0]);
