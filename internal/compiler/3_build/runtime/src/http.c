#include <llhttp.h>
#include <stdio.h>

#include "codec.h"

// The HTTP/1.1 parser natives, over llhttp. The zam side reads the
// connection and feeds what it reads here; a parser keeps whatever lies past
// the message it is on, so pipelined messages are read in turn and the bytes
// after an upgrade are handed back. A message's head is limited to
// maxHeadBytes; its body is handed over as it arrives.

#define maxHeadBytes ((size_t)1 << 20)

enum {
    stateHead = 1,
    stateDone = 2,
    stateEnd = 4,
};

typedef struct HttpConn {
    llhttp_t parser;
    llhttp_settings_t settings;
    CodecBuffer method, target, headers, field, value, body, in;
    size_t inAt;
    size_t headSeen;
    ZamInt length;
    bool head;
    bool done;
    bool tooLarge;
} HttpConn;

static HttpConn *connOf(llhttp_t *parser) {
    return parser->data;
}

static int grow(llhttp_t *parser, CodecBuffer *b, const char *at, size_t length) {
    HttpConn *c = connOf(parser);
    c->headSeen += length;
    if (c->headSeen > maxHeadBytes) {
        c->tooLarge = true;
        return -1;
    }
    codec_buffer_append(b, at, length);
    return 0;
}

static int onMethod(llhttp_t *p, const char *at, size_t n) { return grow(p, &connOf(p)->method, at, n); }
static int onUrl(llhttp_t *p, const char *at, size_t n) { return grow(p, &connOf(p)->target, at, n); }
static int onStatus(llhttp_t *p, const char *at, size_t n) { return grow(p, &connOf(p)->target, at, n); }
static int onField(llhttp_t *p, const char *at, size_t n) { return grow(p, &connOf(p)->field, at, n); }
static int onValue(llhttp_t *p, const char *at, size_t n) { return grow(p, &connOf(p)->value, at, n); }

static int onBody(llhttp_t *p, const char *at, size_t n) {
    codec_buffer_append(&connOf(p)->body, at, n);
    return 0;
}

// onValueComplete files a finished header as its name and value, each ended
// by a NUL byte.
static int onValueComplete(llhttp_t *p) {
    HttpConn *c = connOf(p);
    codec_buffer_append(&c->headers, c->field.bytes == NULL ? "" : c->field.bytes, c->field.length);
    codec_buffer_append(&c->headers, "", 1);
    codec_buffer_append(&c->headers, c->value.bytes == NULL ? "" : c->value.bytes, c->value.length);
    codec_buffer_append(&c->headers, "", 1);
    c->field.length = 0;
    c->value.length = 0;
    return 0;
}

// onHeadersComplete notes the length of the body to come: as its
// Content-Length says, none for a request saying neither that nor chunked,
// and otherwise -1, unknown.
static int onHeadersComplete(llhttp_t *p) {
    HttpConn *c = connOf(p);
    c->head = true;
    if (p->flags & F_CHUNKED)
        c->length = -1;
    else if (p->flags & F_CONTENT_LENGTH)
        c->length = (ZamInt)p->content_length;
    else if (p->type == HTTP_REQUEST || p->status_code / 100 == 1 || p->status_code == 204 ||
             p->status_code == 304)
        c->length = 0;
    else
        c->length = -1;
    return 0;
}

// onMessageComplete pauses the parser, so that what follows the message is
// left for the next.
static int onMessageComplete(llhttp_t *p) {
    connOf(p)->done = true;
    return HPE_PAUSED;
}

// stdHttpConnNew makes a parser of requests, or of responses when told to.
static void stdHttpConnNew(ZamThread *thread) {
    bool response = zam_native_argument(thread, 0) != 0;
    HttpConn *c = calloc(1, sizeof *c);
    if (c == NULL)
        abort();
    llhttp_settings_init(&c->settings);
    c->settings.on_method = onMethod;
    c->settings.on_url = onUrl;
    c->settings.on_status = onStatus;
    c->settings.on_header_field = onField;
    c->settings.on_header_value = onValue;
    c->settings.on_header_value_complete = onValueComplete;
    c->settings.on_headers_complete = onHeadersComplete;
    c->settings.on_body = onBody;
    c->settings.on_message_complete = onMessageComplete;
    llhttp_init(&c->parser, response ? HTTP_RESPONSE : HTTP_REQUEST, &c->settings);
    c->parser.data = c;
    codec_write_word(thread, 1, (ZamWord)(uintptr_t)c);
    zam_native_return_void(thread);
}

static void stdHttpConnFree(ZamThread *thread) {
    HttpConn *c = codec_handle(thread, 0);
    if (c != NULL) {
        free(c->method.bytes);
        free(c->target.bytes);
        free(c->headers.bytes);
        free(c->field.bytes);
        free(c->value.bytes);
        free(c->body.bytes);
        free(c->in.bytes);
        free(c);
    }
    zam_native_return_void(thread);
}

// stdHttpNext starts on the next message, once the last is done.
static void stdHttpNext(ZamThread *thread) {
    HttpConn *c = codec_handle(thread, 0);
    c->method.length = c->target.length = c->headers.length = 0;
    c->field.length = c->value.length = c->body.length = 0;
    c->headSeen = 0;
    c->length = 0;
    c->head = c->done = c->tooLarge = false;
    zam_native_return_void(thread);
}

// parse runs the parser over the unparsed input, stopping at the end of a
// message, and answers an error message or NULL.
static const char *parse(HttpConn *c) {
    if (c->done || c->inAt >= c->in.length)
        return NULL;
    const char *data = c->in.bytes + c->inAt;
    size_t length = c->in.length - c->inAt;
    llhttp_errno_t result = llhttp_execute(&c->parser, data, length);
    if (result == HPE_PAUSED || result == HPE_PAUSED_UPGRADE) {
        c->done = true;
        c->inAt += (size_t)(llhttp_get_error_pos(&c->parser) - data);
        llhttp_resume(&c->parser);
        return NULL;
    }
    if (result != HPE_OK)
        return c->tooLarge ? "message head too large" : llhttp_get_error_reason(&c->parser);
    c->in.length = 0;
    c->inAt = 0;
    return NULL;
}

// stdHttpFeed parses n bytes the connection gave, or what is left over for
// n of zero, or the end of the connection for n below zero. It answers the
// state reached: stateHead once the head is read, stateDone once the whole
// message is, and stateEnd when the connection ended before a message
// began.
static void stdHttpFeed(ZamThread *thread) {
    HttpConn *c = codec_handle(thread, 0);
    ZamInt n = (ZamInt)zam_native_argument(thread, 2);
    const char *err = NULL;
    int state = 0;
    if (n > 0) {
        if (c->inAt > 0 && c->inAt == c->in.length) {
            c->in.length = 0;
            c->inAt = 0;
        }
        codec_buffer_append(&c->in, zam_native_argument_pointer(thread, 1), (size_t)n);
        err = parse(c);
    } else if (n == 0)
        err = parse(c);
    else if (!c->done) {
        bool begun = c->headSeen > 0 || c->head;
        llhttp_errno_t result = llhttp_finish(&c->parser);
        if (result == HPE_PAUSED) {
            c->done = true;
            llhttp_resume(&c->parser);
        } else if (!begun)
            state |= stateEnd;
        else if (!c->done)
            err = "connection closed in the middle of a message";
    }
    if (c->head || c->done)
        state |= stateHead;
    if (c->done)
        state |= stateDone;
    codec_write_word(thread, 3, (ZamWord)state);
    codec_write_error(thread, 4, err);
    zam_native_return_void(thread);
}

// stdHttpHead answers a read head: (method, target, headers, status,
// version, length, keepAlive, upgrade). The version is ten times the major
// version plus the minor, and the length is the body's, or -1 when it comes
// in chunks or runs to the end of the connection.
static void stdHttpHead(ZamThread *thread) {
    HttpConn *c = codec_handle(thread, 0);
    llhttp_t *p = &c->parser;
    codec_write_string(thread, 1, c->method.bytes == NULL ? "" : c->method.bytes, c->method.length);
    codec_write_string(thread, 2, c->target.bytes == NULL ? "" : c->target.bytes, c->target.length);
    codec_write_string(thread, 3, c->headers.bytes == NULL ? "" : c->headers.bytes,
                       c->headers.length == 0 ? 0 : c->headers.length - 1);
    codec_write_word(thread, 4, (ZamWord)llhttp_get_status_code(p));
    codec_write_word(thread, 5, (ZamWord)(llhttp_get_http_major(p) * 10 + llhttp_get_http_minor(p)));
    codec_write_word(thread, 6, (ZamWord)c->length);
    codec_write_bool(thread, 7, llhttp_should_keep_alive(p));
    codec_write_bool(thread, 8, llhttp_get_upgrade(p));
    zam_native_return_void(thread);
}

// stdHttpBody moves up to max body bytes parsed so far into out.
static void stdHttpBody(ZamThread *thread) {
    HttpConn *c = codec_handle(thread, 0);
    ZamInt max = (ZamInt)zam_native_argument(thread, 2);
    size_t n = c->body.length;
    if (max < 0)
        max = 0;
    if (n > (size_t)max)
        n = (size_t)max;
    if (n > 0) {
        memcpy(zam_native_argument_pointer(thread, 1), c->body.bytes, n);
        memmove(c->body.bytes, c->body.bytes + n, c->body.length - n);
        c->body.length -= n;
    }
    *(ZamInt *)zam_native_argument_pointer(thread, 3) = (ZamInt)n;
    zam_native_return_void(thread);
}

// stdHttpRest takes the bytes read past the message, as a str.
static void stdHttpRest(ZamThread *thread) {
    HttpConn *c = codec_handle(thread, 0);
    size_t n = c->in.length - c->inAt;
    codec_write_string(thread, 1, n == 0 ? "" : c->in.bytes + c->inAt, n);
    c->in.length = 0;
    c->inAt = 0;
    zam_native_return_void(thread);
}

const ZamNativeEntry zamHttpNativeTable[] = {
    {"STD_HTTP_CONN_NEW", stdHttpConnNew},
    {"STD_HTTP_CONN_FREE", stdHttpConnFree},
    {"STD_HTTP_NEXT", stdHttpNext},
    {"STD_HTTP_FEED", stdHttpFeed},
    {"STD_HTTP_HEAD", stdHttpHead},
    {"STD_HTTP_BODY", stdHttpBody},
    {"STD_HTTP_REST", stdHttpRest},
};

const uint64_t zamHttpNativeTableCount = sizeof(zamHttpNativeTable) / sizeof(zamHttpNativeTable[0]);
