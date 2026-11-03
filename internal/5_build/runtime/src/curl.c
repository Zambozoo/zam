#include <curl/curl.h>
#include <pthread.h>
#include <stdio.h>
#include <strings.h>

#include "codec.h"

// The URL and HTTP client natives, over libcurl. A transfer runs on a multi
// handle of its own, stepped by the zam side, which feeds it the request
// body and takes the response body as each is needed. It copies what it
// needs out of the zam heap, since it runs in a safe region.

static pthread_once_t curlOnce = PTHREAD_ONCE_INIT;

static void curlInit(void) {
    curl_global_init(CURL_GLOBAL_DEFAULT);
}

static CURL *newEasy(ZamThread *thread) {
    pthread_once(&curlOnce, curlInit);
    CURL *curl = curl_easy_init();
    if (curl == NULL)
        zam_native_panic(thread, "out of memory starting a transfer");
    curl_easy_setopt(curl, CURLOPT_NOSIGNAL, 1L);
    return curl;
}

static void writeUrlError(ZamThread *thread, uint64_t wordIndex, CURLUcode code) {
    codec_write_error(thread, wordIndex, code == CURLUE_OK ? NULL : curl_url_strerror(code));
}

// getPart writes a part of a URL as written, or empty when it has none.
static void getPart(ZamThread *thread, CURLU *url, CURLUPart part, uint64_t wordIndex) {
    char *value = NULL;
    if (curl_url_get(url, part, &value, 0) == CURLUE_OK && value != NULL) {
        codec_write_string(thread, wordIndex, value, strlen(value));
        curl_free(value);
    } else
        codec_write_string(thread, wordIndex, "", 0);
}

// stdUrlParse splits an absolute URL into its parts, each as written.
static void stdUrlParse(ZamThread *thread) {
    pthread_once(&curlOnce, curlInit);
    char *text = codec_c_string(thread, 0);
    CURLU *url = curl_url();
    CURLUcode code = curl_url_set(url, CURLUPART_URL, text, CURLU_NON_SUPPORT_SCHEME);
    free(text);
    getPart(thread, url, CURLUPART_SCHEME, 1);
    getPart(thread, url, CURLUPART_USER, 2);
    getPart(thread, url, CURLUPART_PASSWORD, 3);
    getPart(thread, url, CURLUPART_HOST, 4);
    char *port = NULL;
    ZamInt portNumber = 0;
    if (curl_url_get(url, CURLUPART_PORT, &port, 0) == CURLUE_OK && port != NULL) {
        portNumber = atoi(port);
        curl_free(port);
    }
    *(ZamInt *)zam_native_argument_pointer(thread, 5) = portNumber;
    getPart(thread, url, CURLUPART_PATH, 6);
    getPart(thread, url, CURLUPART_QUERY, 7);
    getPart(thread, url, CURLUPART_FRAGMENT, 8);
    curl_url_cleanup(url);
    writeUrlError(thread, 9, code);
    zam_native_return_void(thread);
}

static CURLUcode setPart(ZamThread *thread, CURLU *url, CURLUPart part, uint64_t wordIndex,
                         unsigned int flags) {
    char *value = codec_c_string(thread, wordIndex);
    CURLUcode code = value[0] == '\0' ? CURLUE_OK : curl_url_set(url, part, value, flags);
    free(value);
    return code;
}

// stdUrlBuild joins parts, each already escaped, into a URL.
static void stdUrlBuild(ZamThread *thread) {
    pthread_once(&curlOnce, curlInit);
    CURLU *url = curl_url();
    CURLUcode code = setPart(thread, url, CURLUPART_SCHEME, 0, CURLU_NON_SUPPORT_SCHEME);
    if (code == CURLUE_OK)
        code = setPart(thread, url, CURLUPART_USER, 1, 0);
    if (code == CURLUE_OK)
        code = setPart(thread, url, CURLUPART_PASSWORD, 2, 0);
    if (code == CURLUE_OK)
        code = setPart(thread, url, CURLUPART_HOST, 3, 0);
    ZamInt port = (ZamInt)zam_native_argument(thread, 4);
    if (code == CURLUE_OK && port > 0) {
        char text[24];
        snprintf(text, sizeof text, "%lld", (long long)port);
        code = curl_url_set(url, CURLUPART_PORT, text, 0);
    }
    if (code == CURLUE_OK)
        code = setPart(thread, url, CURLUPART_PATH, 5, 0);
    if (code == CURLUE_OK)
        code = setPart(thread, url, CURLUPART_QUERY, 6, 0);
    if (code == CURLUE_OK)
        code = setPart(thread, url, CURLUPART_FRAGMENT, 7, 0);
    char *text = NULL;
    if (code == CURLUE_OK)
        code = curl_url_get(url, CURLUPART_URL, &text, 0);
    if (code == CURLUE_OK)
        codec_write_string(thread, 8, text, strlen(text));
    else
        codec_write_string(thread, 8, "", 0);
    curl_free(text);
    curl_url_cleanup(url);
    writeUrlError(thread, 9, code);
    zam_native_return_void(thread);
}

// stdUrlResolve resolves a reference, absolute or relative, against a base.
static void stdUrlResolve(ZamThread *thread) {
    pthread_once(&curlOnce, curlInit);
    char *base = codec_c_string(thread, 0);
    char *ref = codec_c_string(thread, 1);
    CURLU *url = curl_url();
    CURLUcode code = curl_url_set(url, CURLUPART_URL, base, CURLU_NON_SUPPORT_SCHEME);
    if (code == CURLUE_OK)
        code = curl_url_set(url, CURLUPART_URL, ref, CURLU_NON_SUPPORT_SCHEME);
    char *text = NULL;
    if (code == CURLUE_OK)
        code = curl_url_get(url, CURLUPART_URL, &text, 0);
    free(base);
    free(ref);
    if (code == CURLUE_OK)
        codec_write_string(thread, 2, text, strlen(text));
    else
        codec_write_string(thread, 2, "", 0);
    curl_free(text);
    curl_url_cleanup(url);
    writeUrlError(thread, 3, code);
    zam_native_return_void(thread);
}

static void stdUrlEscape(ZamThread *thread) {
    pthread_once(&curlOnce, curlInit);
    const ZamString *s = codec_string(thread, 0);
    char *escaped = curl_easy_escape(NULL, s->bytes, (int)s->length);
    if (escaped == NULL)
        zam_native_panic(thread, "out of memory escaping");
    codec_write_string(thread, 1, escaped, strlen(escaped));
    curl_free(escaped);
    zam_native_return_void(thread);
}

static void stdUrlUnescape(ZamThread *thread) {
    pthread_once(&curlOnce, curlInit);
    const ZamString *s = codec_string(thread, 0);
    int length = 0;
    char *raw = curl_easy_unescape(NULL, s->bytes, (int)s->length, &length);
    if (raw == NULL)
        zam_native_panic(thread, "out of memory unescaping");
    codec_write_string(thread, 1, raw, (size_t)length);
    curl_free(raw);
    zam_native_return_void(thread);
}

// headerList turns NUL-separated "Name: value" lines into a curl list.
static struct curl_slist *headerList(const ZamString *joined) {
    struct curl_slist *list = NULL;
    size_t start = 0;
    for (size_t i = 0; i <= joined->length; i++) {
        if (i < joined->length && joined->bytes[i] != '\0')
            continue;
        if (i > start) {
            char *line = malloc(i - start + 1);
            if (line == NULL)
                abort();
            memcpy(line, joined->bytes + start, i - start);
            line[i - start] = '\0';
            list = curl_slist_append(list, line);
            free(line);
        }
        start = i + 1;
    }
    return list;
}

// The most a transfer holds of a response body not yet read; past it, the
// transfer pauses until the body is read.
#define maxPendingBody ((size_t)1 << 20)

enum {
    stepUpload = 1,
    stepHead = 2,
    stepBody = 4,
    stepDone = 8,
};

// Transfer is one request in flight. Its body is sent from upload, which
// the zam side fills each time the transfer asks for more, and its response
// body collects in body until the zam side takes it.
typedef struct Transfer {
    CURL *curl;
    CURLM *multi;
    struct curl_slist *headerList;
    char *method;
    char *url;
    char errorText[CURL_ERROR_SIZE];
    CodecBuffer headers, body, upload;
    size_t uploadAt;
    bool uploadEnded;
    bool wantsUpload;
    bool bodyPaused;
    bool sawLocation;
    bool follow;
    bool head;
    bool done;
    CURLcode result;
} Transfer;

static size_t receiveBody(char *data, size_t size, size_t count, void *client) {
    Transfer *t = client;
    if (t->body.length >= maxPendingBody) {
        t->bodyPaused = true;
        return CURL_WRITEFUNC_PAUSE;
    }
    codec_buffer_append(&t->body, data, size * count);
    return size * count;
}

static size_t sendBody(char *data, size_t size, size_t count, void *client) {
    Transfer *t = client;
    size_t pending = t->upload.length - t->uploadAt;
    if (pending == 0) {
        if (t->uploadEnded)
            return 0;
        t->wantsUpload = true;
        return CURL_READFUNC_PAUSE;
    }
    size_t n = size * count;
    if (n > pending)
        n = pending;
    memcpy(data, t->upload.bytes + t->uploadAt, n);
    t->uploadAt += n;
    if (t->uploadAt == t->upload.length) {
        t->upload.length = 0;
        t->uploadAt = 0;
    }
    return n;
}

// receiveHeader keeps the headers of the final response, as NUL-separated
// names and values. Each status line starts over, and the blank line ending
// a head marks the head read unless it is interim or a redirect to follow.
static size_t receiveHeader(char *data, size_t size, size_t count, void *client) {
    Transfer *t = client;
    size_t n = size * count;
    if (t->head)
        return n;
    if (n >= 5 && memcmp(data, "HTTP/", 5) == 0) {
        t->headers.length = 0;
        t->sawLocation = false;
        return n;
    }
    if (n <= 2 && (n == 0 || data[0] == '\r' || data[0] == '\n')) {
        long status = 0;
        curl_easy_getinfo(t->curl, CURLINFO_RESPONSE_CODE, &status);
        bool redirect = status == 301 || status == 302 || status == 303 || status == 307 || status == 308;
        if (status / 100 != 1 && !(t->follow && redirect && t->sawLocation))
            t->head = true;
        return n;
    }
    char *colon = memchr(data, ':', n);
    if (colon == NULL)
        return n;
    size_t nameLength = (size_t)(colon - data);
    char *value = colon + 1;
    char *end = data + n;
    while (value < end && (*value == ' ' || *value == '\t'))
        value++;
    while (end > value && (end[-1] == '\r' || end[-1] == '\n' || end[-1] == ' '))
        end--;
    if (nameLength == 8 && strncasecmp(data, "location", 8) == 0)
        t->sawLocation = true;
    codec_buffer_append(&t->headers, data, nameLength);
    codec_buffer_append(&t->headers, "", 1);
    codec_buffer_append(&t->headers, value, (size_t)(end - value));
    codec_buffer_append(&t->headers, "", 1);
    return n;
}

static void freeTransfer(Transfer *t) {
    if (t == NULL)
        return;
    curl_multi_remove_handle(t->multi, t->curl);
    curl_easy_cleanup(t->curl);
    curl_multi_cleanup(t->multi);
    curl_slist_free_all(t->headerList);
    free(t->method);
    free(t->url);
    free(t->headers.bytes);
    free(t->body.bytes);
    free(t->upload.bytes);
    free(t);
}

// stdHttpOpen starts a request: (method, url, headers, timeoutMs, follow,
// bodyLength), the body length being -2 for none and -1 for one of unknown
// length, sent in chunks. Nothing is sent until the transfer is stepped.
static void stdHttpOpen(ZamThread *thread) {
    Transfer *t = calloc(1, sizeof *t);
    if (t == NULL)
        abort();
    t->curl = newEasy(thread);
    t->multi = curl_multi_init();
    if (t->multi == NULL)
        zam_native_panic(thread, "out of memory starting a transfer");
    t->method = codec_c_string(thread, 0);
    t->url = codec_c_string(thread, 1);
    t->headerList = curl_slist_append(headerList(codec_string(thread, 2)), "Expect:");
    ZamInt timeout = (ZamInt)zam_native_argument(thread, 3);
    t->follow = zam_native_argument(thread, 4) != 0;
    ZamInt bodyLength = (ZamInt)zam_native_argument(thread, 5);

    CURL *curl = t->curl;
    curl_easy_setopt(curl, CURLOPT_URL, t->url);
    curl_easy_setopt(curl, CURLOPT_HTTPHEADER, t->headerList);
    curl_easy_setopt(curl, CURLOPT_ACCEPT_ENCODING, "");
    curl_easy_setopt(curl, CURLOPT_ERRORBUFFER, t->errorText);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, receiveBody);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, t);
    curl_easy_setopt(curl, CURLOPT_HEADERFUNCTION, receiveHeader);
    curl_easy_setopt(curl, CURLOPT_HEADERDATA, t);
    if (timeout > 0)
        curl_easy_setopt(curl, CURLOPT_TIMEOUT_MS, (long)timeout);
    if (t->follow) {
        curl_easy_setopt(curl, CURLOPT_FOLLOWLOCATION, 1L);
        curl_easy_setopt(curl, CURLOPT_MAXREDIRS, 10L);
    }
    if (bodyLength >= -1) {
        curl_easy_setopt(curl, CURLOPT_UPLOAD, 1L);
        curl_easy_setopt(curl, CURLOPT_READFUNCTION, sendBody);
        curl_easy_setopt(curl, CURLOPT_READDATA, t);
        curl_easy_setopt(curl, CURLOPT_INFILESIZE_LARGE, (curl_off_t)bodyLength);
        curl_easy_setopt(curl, CURLOPT_CUSTOMREQUEST, t->method);
    } else if (strcmp(t->method, "POST") == 0) {
        curl_easy_setopt(curl, CURLOPT_POSTFIELDS, "");
        curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE_LARGE, (curl_off_t)0);
    } else if (strcmp(t->method, "HEAD") == 0)
        curl_easy_setopt(curl, CURLOPT_NOBODY, 1L);
    else if (strcmp(t->method, "GET") != 0)
        curl_easy_setopt(curl, CURLOPT_CUSTOMREQUEST, t->method);
    curl_multi_add_handle(t->multi, curl);
    codec_write_word(thread, 6, (ZamWord)(uintptr_t)t);
    zam_native_return_void(thread);
}

// stepState answers what a transfer has for the zam side.
static int stepState(Transfer *t) {
    int state = 0;
    if (t->wantsUpload && !t->done)
        state |= stepUpload;
    if (t->head || t->done)
        state |= stepHead;
    if (t->body.length > 0)
        state |= stepBody;
    if (t->done)
        state |= stepDone;
    return state;
}

// stdHttpStep runs a transfer until it wants more of the request body, has
// read the response head, has response body to take, or is done. It answers
// which of those hold, and the transfer's error once it is done.
static void stdHttpStep(ZamThread *thread) {
    Transfer *t = codec_handle(thread, 0);
    bool wantHead = !t->head;
    zam_native_block_begin(thread);
    while (!t->done) {
        int running = 0;
        curl_multi_perform(t->multi, &running);
        CURLMsg *msg;
        int left = 0;
        while ((msg = curl_multi_info_read(t->multi, &left)) != NULL) {
            if (msg->msg == CURLMSG_DONE && msg->easy_handle == t->curl) {
                t->done = true;
                t->result = msg->data.result;
            }
        }
        if (t->done || t->wantsUpload || (wantHead && t->head) || (!wantHead && t->body.length > 0))
            break;
        curl_multi_poll(t->multi, NULL, 0, 1000, NULL);
    }
    zam_native_block_end(thread);
    codec_write_word(thread, 1, (ZamWord)stepState(t));
    if (t->done && t->result != CURLE_OK)
        codec_write_error(thread, 2, t->errorText[0] != '\0' ? t->errorText : curl_easy_strerror(t->result));
    else
        codec_write_error(thread, 2, NULL);
    zam_native_return_void(thread);
}

// stdHttpUpload hands a transfer n more bytes of the request body, or the end
// of it for n of zero.
static void stdHttpUpload(ZamThread *thread) {
    Transfer *t = codec_handle(thread, 0);
    ZamInt n = (ZamInt)zam_native_argument(thread, 2);
    if (n > 0)
        codec_buffer_append(&t->upload, zam_native_argument_pointer(thread, 1), (size_t)n);
    else
        t->uploadEnded = true;
    if (t->wantsUpload) {
        t->wantsUpload = false;
        curl_easy_pause(t->curl, t->bodyPaused ? CURLPAUSE_RECV : CURLPAUSE_CONT);
    }
    zam_native_return_void(thread);
}

// stdHttpResponseHead answers the final response's (status, headers).
static void stdHttpResponseHead(ZamThread *thread) {
    Transfer *t = codec_handle(thread, 0);
    long status = 0;
    curl_easy_getinfo(t->curl, CURLINFO_RESPONSE_CODE, &status);
    *(ZamInt *)zam_native_argument_pointer(thread, 1) = status;
    codec_write_string(thread, 2, t->headers.bytes == NULL ? "" : t->headers.bytes,
                       t->headers.length == 0 ? 0 : t->headers.length - 1);
    zam_native_return_void(thread);
}

// stdHttpTake moves up to max bytes of the response body into out, letting a
// transfer paused on a full body go on.
static void stdHttpTake(ZamThread *thread) {
    Transfer *t = codec_handle(thread, 0);
    ZamInt max = (ZamInt)zam_native_argument(thread, 2);
    size_t n = t->body.length;
    if (max < 0)
        max = 0;
    if (n > (size_t)max)
        n = (size_t)max;
    if (n > 0) {
        memcpy(zam_native_argument_pointer(thread, 1), t->body.bytes, n);
        memmove(t->body.bytes, t->body.bytes + n, t->body.length - n);
        t->body.length -= n;
    }
    if (t->bodyPaused && t->body.length < maxPendingBody) {
        t->bodyPaused = false;
        curl_easy_pause(t->curl, t->wantsUpload ? CURLPAUSE_SEND : CURLPAUSE_CONT);
    }
    *(ZamInt *)zam_native_argument_pointer(thread, 3) = (ZamInt)n;
    zam_native_return_void(thread);
}

static void stdHttpFree(ZamThread *thread) {
    freeTransfer(codec_handle(thread, 0));
    zam_native_return_void(thread);
}

const ZamNativeEntry zamCurlNativeTable[] = {
    {"STD_URL_PARSE", stdUrlParse},
    {"STD_URL_BUILD", stdUrlBuild},
    {"STD_URL_RESOLVE", stdUrlResolve},
    {"STD_URL_ESCAPE", stdUrlEscape},
    {"STD_URL_UNESCAPE", stdUrlUnescape},
    {"STD_HTTP_OPEN", stdHttpOpen},
    {"STD_HTTP_STEP", stdHttpStep},
    {"STD_HTTP_UPLOAD", stdHttpUpload},
    {"STD_HTTP_RESPONSE_HEAD", stdHttpResponseHead},
    {"STD_HTTP_TAKE", stdHttpTake},
    {"STD_HTTP_FREE", stdHttpFree},
};

const uint64_t zamCurlNativeTableCount = sizeof(zamCurlNativeTable) / sizeof(zamCurlNativeTable[0]);
