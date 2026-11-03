#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdio.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>
#include <unistd.h>

#include "codec.h"

// The socket natives. A connected socket is a plain file descriptor the zam
// side holds in a File; a listener is a small C struct, so that closing it
// from one thread stops an Accept waiting in another. Everything that may
// wait runs in a safe region.

#ifndef SO_NOSIGPIPE
static void ignoreSigpipe(void) {
    signal(SIGPIPE, SIG_IGN);
}
#endif

// noSigpipe keeps writing to a closed peer from raising SIGPIPE: per socket
// where the platform allows it, and process-wide otherwise.
static void noSigpipe(int fd) {
#ifdef SO_NOSIGPIPE
    int one = 1;
    setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
#else
    (void)fd;
    static pthread_once_t once = PTHREAD_ONCE_INIT;
    pthread_once(&once, ignoreSigpipe);
#endif
}

static void writeFd(ZamThread *thread, uint64_t wordIndex, int fd) {
    *(ZamInt *)zam_native_argument_pointer(thread, wordIndex) = fd;
}

static void writeErrno(ZamThread *thread, uint64_t wordIndex, int err) {
    codec_write_error(thread, wordIndex, err == 0 ? NULL : strerror(err));
}

// addrText writes the canonical text of an address and its port.
static void addrText(const struct sockaddr *sa, char *text, size_t size, int *port) {
    *port = 0;
    text[0] = '\0';
    if (sa->sa_family == AF_INET) {
        const struct sockaddr_in *in = (const struct sockaddr_in *)sa;
        inet_ntop(AF_INET, &in->sin_addr, text, (socklen_t)size);
        *port = ntohs(in->sin_port);
    } else if (sa->sa_family == AF_INET6) {
        const struct sockaddr_in6 *in = (const struct sockaddr_in6 *)sa;
        inet_ntop(AF_INET6, &in->sin6_addr, text, (socklen_t)size);
        *port = ntohs(in->sin6_port);
    } else if (sa->sa_family == AF_UNIX) {
        snprintf(text, size, "%s", ((const struct sockaddr_un *)sa)->sun_path);
    }
}

// stdIpParse reads an IPv4 or IPv6 address as its 16 bytes in two
// big-endian words, an IPv4 address in the leading four.
static void stdIpParse(ZamThread *thread) {
    char *text = codec_c_string(thread, 0);
    unsigned char raw[16] = {0};
    bool v6 = false;
    const char *err = NULL;
    if (inet_pton(AF_INET, text, raw) != 1) {
        if (inet_pton(AF_INET6, text, raw) == 1)
            v6 = true;
        else
            err = "not an IP address";
    }
    free(text);
    uint64_t hi = 0, lo = 0;
    for (int i = 0; i < 8; i++) {
        hi = hi << 8 | raw[i];
        lo = lo << 8 | raw[8 + i];
    }
    *(ZamWord *)zam_native_argument_pointer(thread, 1) = hi;
    *(ZamWord *)zam_native_argument_pointer(thread, 2) = lo;
    codec_write_bool(thread, 3, v6);
    codec_write_error(thread, 4, err);
    zam_native_return_void(thread);
}

// stdIpStr writes an address's canonical text from its two words.
static void stdIpStr(ZamThread *thread) {
    uint64_t hi = zam_native_argument(thread, 0);
    uint64_t lo = zam_native_argument(thread, 1);
    bool v6 = zam_native_argument(thread, 2) != 0;
    unsigned char raw[16];
    for (int i = 0; i < 8; i++) {
        raw[i] = (unsigned char)(hi >> (56 - 8 * i));
        raw[8 + i] = (unsigned char)(lo >> (56 - 8 * i));
    }
    char text[INET6_ADDRSTRLEN];
    inet_ntop(v6 ? AF_INET6 : AF_INET, raw, text, sizeof text);
    codec_write_string(thread, 3, text, strlen(text));
    zam_native_return_void(thread);
}

// stdIpLookup resolves a host name to its addresses, joined by NUL bytes
// without repeats, in the order the resolver gave them.
static void stdIpLookup(ZamThread *thread) {
    char *host = codec_c_string(thread, 0);
    struct addrinfo hints = {0};
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;
    struct addrinfo *results = NULL;
    zam_native_block_begin(thread);
    int status = getaddrinfo(host, NULL, &hints, &results);
    zam_native_block_end(thread);
    free(host);
    if (status != 0) {
        codec_write_word(thread, 1, 0);
        codec_write_error(thread, 2, gai_strerror(status));
        zam_native_return_void(thread);
        return;
    }
    CodecBuffer joined = {0};
    size_t count = 0;
    for (struct addrinfo *ai = results; ai != NULL; ai = ai->ai_next) {
        char text[INET6_ADDRSTRLEN];
        int port;
        addrText(ai->ai_addr, text, sizeof text, &port);
        bool seen = false;
        for (size_t at = 0; at < joined.length;) {
            size_t length = strlen(joined.bytes + at);
            if (strcmp(joined.bytes + at, text) == 0)
                seen = true;
            at += length + 1;
        }
        if (seen || text[0] == '\0')
            continue;
        codec_buffer_append(&joined, text, strlen(text) + 1);
        count++;
    }
    freeaddrinfo(results);
    size_t length = joined.length == 0 ? 0 : joined.length - 1;
    codec_write_string(thread, 1, joined.bytes == NULL ? "" : joined.bytes, length);
    codec_write_error(thread, 2, count == 0 ? "host has no addresses" : NULL);
    free(joined.bytes);
    zam_native_return_void(thread);
}

// resolve looks up host and port for a socket of the given type. An empty
// host means every local address, for a listener.
static int resolve(ZamThread *thread, const char *host, int port, int socktype,
                   struct addrinfo **results) {
    struct addrinfo hints = {0};
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = socktype;
    if (host[0] == '\0')
        hints.ai_flags = AI_PASSIVE;
    char service[16];
    snprintf(service, sizeof service, "%d", port);
    zam_native_block_begin(thread);
    int status = getaddrinfo(host[0] == '\0' ? NULL : host, service, &hints, results);
    zam_native_block_end(thread);
    return status;
}

// dial connects a socket of the given type to the first address of host
// that answers.
static void dial(ZamThread *thread, int socktype) {
    char *host = codec_c_string(thread, 0);
    int port = (int)(ZamInt)zam_native_argument(thread, 1);
    struct addrinfo *results = NULL;
    int status = resolve(thread, host, port, socktype, &results);
    free(host);
    if (status != 0) {
        writeFd(thread, 2, -1);
        codec_write_error(thread, 3, gai_strerror(status));
        zam_native_return_void(thread);
        return;
    }
    int fd = -1;
    int err = 0;
    zam_native_block_begin(thread);
    for (struct addrinfo *ai = results; ai != NULL && fd < 0; ai = ai->ai_next) {
        fd = socket(ai->ai_family, ai->ai_socktype, ai->ai_protocol);
        if (fd < 0) {
            err = errno;
            continue;
        }
        if (connect(fd, ai->ai_addr, ai->ai_addrlen) != 0) {
            err = errno;
            close(fd);
            fd = -1;
        }
    }
    zam_native_block_end(thread);
    freeaddrinfo(results);
    if (fd >= 0) {
        noSigpipe(fd);
        if (socktype == SOCK_STREAM) {
            int one = 1;
            setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
        }
        err = 0;
    }
    writeFd(thread, 2, fd);
    writeErrno(thread, 3, fd < 0 ? (err == 0 ? ECONNREFUSED : err) : 0);
    zam_native_return_void(thread);
}

static void stdTcpDial(ZamThread *thread) { dial(thread, SOCK_STREAM); }
static void stdUdpDial(ZamThread *thread) { dial(thread, SOCK_DGRAM); }

static void stdUnixDial(ZamThread *thread) {
    char *path = codec_c_string(thread, 0);
    struct sockaddr_un addr = {0};
    addr.sun_family = AF_UNIX;
    if (strlen(path) >= sizeof addr.sun_path) {
        free(path);
        writeFd(thread, 1, -1);
        codec_write_error(thread, 2, "socket path is too long");
        zam_native_return_void(thread);
        return;
    }
    strcpy(addr.sun_path, path);
    free(path);
    int fd = socket(AF_UNIX, SOCK_STREAM, 0);
    int err = fd < 0 ? errno : 0;
    if (fd >= 0) {
        zam_native_block_begin(thread);
        int result = connect(fd, (struct sockaddr *)&addr, sizeof addr);
        err = result == 0 ? 0 : errno;
        zam_native_block_end(thread);
        if (err != 0) {
            close(fd);
            fd = -1;
        } else
            noSigpipe(fd);
    }
    writeFd(thread, 1, fd);
    writeErrno(thread, 2, err);
    zam_native_return_void(thread);
}

typedef struct Listener {
    int fd;
    atomic_bool closed;
    char *path;
} Listener;

// stdNetListen opens a TCP listener on host and port, or a Unix listener on
// a path, which must not exist yet.
static void stdNetListen(ZamThread *thread) {
    bool unixPath = zam_native_argument(thread, 0) != 0;
    char *where = codec_c_string(thread, 1);
    int port = (int)(ZamInt)zam_native_argument(thread, 2);
    int fd = -1;
    int err = 0;
    const char *message = NULL;
    if (unixPath) {
        struct sockaddr_un addr = {0};
        addr.sun_family = AF_UNIX;
        if (strlen(where) >= sizeof addr.sun_path)
            message = "socket path is too long";
        else {
            strcpy(addr.sun_path, where);
            fd = socket(AF_UNIX, SOCK_STREAM, 0);
            if (fd < 0 || bind(fd, (struct sockaddr *)&addr, sizeof addr) != 0 ||
                listen(fd, 128) != 0)
                err = errno;
        }
    } else {
        struct addrinfo *results = NULL;
        int status = resolve(thread, where, port, SOCK_STREAM, &results);
        if (status != 0)
            message = gai_strerror(status);
        else {
            struct addrinfo *ai = results;
            fd = socket(ai->ai_family, ai->ai_socktype, ai->ai_protocol);
            int one = 1, zero = 0;
            if (fd >= 0) {
                setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
                if (ai->ai_family == AF_INET6)
                    setsockopt(fd, IPPROTO_IPV6, IPV6_V6ONLY, &zero, sizeof zero);
            }
            if (fd < 0 || bind(fd, ai->ai_addr, ai->ai_addrlen) != 0 || listen(fd, 128) != 0)
                err = errno;
            freeaddrinfo(results);
        }
    }
    if (message != NULL || err != 0) {
        if (fd >= 0)
            close(fd);
        free(where);
        codec_write_word(thread, 3, 0);
        codec_write_error(thread, 4, message != NULL ? message : strerror(err));
        zam_native_return_void(thread);
        return;
    }
    Listener *l = calloc(1, sizeof *l);
    if (l == NULL)
        abort();
    l->fd = fd;
    atomic_init(&l->closed, false);
    l->path = unixPath ? where : (free(where), NULL);
    codec_write_word(thread, 3, (ZamWord)(uintptr_t)l);
    codec_write_error(thread, 4, NULL);
    zam_native_return_void(thread);
}

// stdNetAccept waits for a connection, checking every tenth of a second
// whether the listener was closed in the meantime.
static void stdNetAccept(ZamThread *thread) {
    Listener *l = codec_handle(thread, 0);
    int fd = -1;
    int err = 0;
    zam_native_block_begin(thread);
    while (!atomic_load(&l->closed)) {
        struct pollfd p = {.fd = l->fd, .events = POLLIN};
        int ready = poll(&p, 1, 100);
        if (ready < 0 && errno != EINTR) {
            err = errno;
            break;
        }
        if (ready <= 0 || atomic_load(&l->closed))
            continue;
        fd = accept(l->fd, NULL, NULL);
        if (fd >= 0)
            break;
        if (errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR && errno != ECONNABORTED) {
            err = errno;
            break;
        }
    }
    zam_native_block_end(thread);
    if (fd >= 0) {
        noSigpipe(fd);
        int one = 1;
        setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof one);
    }
    writeFd(thread, 1, fd);
    if (fd < 0)
        codec_write_error(thread, 2, err != 0 ? strerror(err) : "listener closed");
    else
        codec_write_error(thread, 2, NULL);
    zam_native_return_void(thread);
}

// stdNetListenerClose stops the listener. A Unix listener removes its path.
static void stdNetListenerClose(ZamThread *thread) {
    Listener *l = codec_handle(thread, 0);
    bool wasClosed = atomic_exchange(&l->closed, true);
    if (!wasClosed) {
        close(l->fd);
        if (l->path != NULL)
            unlink(l->path);
    }
    codec_write_bool(thread, 1, !wasClosed);
    zam_native_return_void(thread);
}

static void stdNetListenerFree(ZamThread *thread) {
    Listener *l = codec_handle(thread, 0);
    if (l != NULL) {
        if (!atomic_exchange(&l->closed, true)) {
            close(l->fd);
            if (l->path != NULL)
                unlink(l->path);
        }
        free(l->path);
        free(l);
    }
    zam_native_return_void(thread);
}

static void writeAddr(ZamThread *thread, uint64_t first, const struct sockaddr *sa) {
    char text[INET6_ADDRSTRLEN > 108 ? INET6_ADDRSTRLEN : 108];
    int port;
    addrText(sa, text, sizeof text, &port);
    codec_write_string(thread, first, text, strlen(text));
    *(ZamInt *)zam_native_argument_pointer(thread, first + 1) = port;
}

static void stdNetListenerAddr(ZamThread *thread) {
    Listener *l = codec_handle(thread, 0);
    struct sockaddr_storage addr;
    socklen_t length = sizeof addr;
    if (getsockname(l->fd, (struct sockaddr *)&addr, &length) != 0) {
        int err = errno;
        codec_write_string(thread, 1, "", 0);
        *(ZamInt *)zam_native_argument_pointer(thread, 2) = 0;
        writeErrno(thread, 3, err);
    } else {
        writeAddr(thread, 1, (struct sockaddr *)&addr);
        codec_write_error(thread, 3, NULL);
    }
    zam_native_return_void(thread);
}

// stdSocketAddr answers the local or the remote address of a socket.
static void stdSocketAddr(ZamThread *thread) {
    int fd = (int)(ZamInt)zam_native_argument(thread, 0);
    bool remote = zam_native_argument(thread, 1) != 0;
    struct sockaddr_storage addr;
    socklen_t length = sizeof addr;
    int result = remote ? getpeername(fd, (struct sockaddr *)&addr, &length)
                        : getsockname(fd, (struct sockaddr *)&addr, &length);
    if (result != 0) {
        int err = errno;
        codec_write_string(thread, 2, "", 0);
        *(ZamInt *)zam_native_argument_pointer(thread, 3) = 0;
        writeErrno(thread, 4, err);
    } else {
        writeAddr(thread, 2, (struct sockaddr *)&addr);
        codec_write_error(thread, 4, NULL);
    }
    zam_native_return_void(thread);
}

// stdSocketTimeout bounds how long a read or a write waits, zero for ever.
static void stdSocketTimeout(ZamThread *thread) {
    int fd = (int)(ZamInt)zam_native_argument(thread, 0);
    bool write = zam_native_argument(thread, 1) != 0;
    ZamInt ms = (ZamInt)zam_native_argument(thread, 2);
    struct timeval tv = {.tv_sec = (time_t)(ms / 1000), .tv_usec = (suseconds_t)(ms % 1000) * 1000};
    int result = setsockopt(fd, SOL_SOCKET, write ? SO_SNDTIMEO : SO_RCVTIMEO, &tv, sizeof tv);
    writeErrno(thread, 3, result == 0 ? 0 : errno);
    zam_native_return_void(thread);
}

// stdSocketShutdown stops reading (0), writing (1) or both (2), which wakes
// a read waiting in another thread.
static void stdSocketShutdown(ZamThread *thread) {
    int fd = (int)(ZamInt)zam_native_argument(thread, 0);
    int how = (int)(ZamInt)zam_native_argument(thread, 1);
    int result = shutdown(fd, how == 0 ? SHUT_RD : how == 1 ? SHUT_WR : SHUT_RDWR);
    writeErrno(thread, 2, result == 0 || errno == ENOTCONN ? 0 : errno);
    zam_native_return_void(thread);
}

static void stdUdpBind(ZamThread *thread) {
    char *host = codec_c_string(thread, 0);
    int port = (int)(ZamInt)zam_native_argument(thread, 1);
    struct addrinfo *results = NULL;
    int status = resolve(thread, host, port, SOCK_DGRAM, &results);
    free(host);
    if (status != 0) {
        writeFd(thread, 2, -1);
        codec_write_error(thread, 3, gai_strerror(status));
        zam_native_return_void(thread);
        return;
    }
    struct addrinfo *ai = results;
    int fd = socket(ai->ai_family, ai->ai_socktype, ai->ai_protocol);
    int err = 0;
    if (fd >= 0 && ai->ai_family == AF_INET6) {
        int zero = 0;
        setsockopt(fd, IPPROTO_IPV6, IPV6_V6ONLY, &zero, sizeof zero);
    }
    if (fd < 0 || bind(fd, ai->ai_addr, ai->ai_addrlen) != 0) {
        err = errno;
        if (fd >= 0)
            close(fd);
        fd = -1;
    }
    freeaddrinfo(results);
    writeFd(thread, 2, fd);
    writeErrno(thread, 3, err);
    zam_native_return_void(thread);
}

// stdUdpSendTo sends one datagram to an address and port.
static void stdUdpSendTo(ZamThread *thread) {
    int fd = (int)(ZamInt)zam_native_argument(thread, 0);
    ZamInt n = (ZamInt)zam_native_argument(thread, 2);
    const void *data = n == 0 ? "" : zam_native_argument_pointer(thread, 1);
    char *ip = codec_c_string(thread, 3);
    int port = (int)(ZamInt)zam_native_argument(thread, 4);

    struct sockaddr_storage addr = {0};
    socklen_t length;
    struct sockaddr_in *in4 = (struct sockaddr_in *)&addr;
    struct sockaddr_in6 *in6 = (struct sockaddr_in6 *)&addr;
    struct sockaddr_storage local;
    socklen_t localLength = sizeof local;
    bool localV6 = getsockname(fd, (struct sockaddr *)&local, &localLength) == 0 &&
                   local.ss_family == AF_INET6;
    int err = 0;
    if (inet_pton(AF_INET, ip, &in4->sin_addr) == 1 && !localV6) {
        in4->sin_family = AF_INET;
        in4->sin_port = htons((uint16_t)port);
        length = sizeof *in4;
    } else if (inet_pton(AF_INET6, ip, &in6->sin6_addr) == 1) {
        in6->sin6_family = AF_INET6;
        in6->sin6_port = htons((uint16_t)port);
        length = sizeof *in6;
    } else {
        struct in_addr v4;
        if (inet_pton(AF_INET, ip, &v4) == 1) {
            in6->sin6_family = AF_INET6;
            in6->sin6_port = htons((uint16_t)port);
            in6->sin6_addr.s6_addr[10] = 0xff;
            in6->sin6_addr.s6_addr[11] = 0xff;
            memcpy(&in6->sin6_addr.s6_addr[12], &v4, 4);
            length = sizeof *in6;
        } else {
            err = EINVAL;
            length = 0;
        }
    }
    free(ip);
    ssize_t sent = -1;
    if (err == 0) {
        zam_native_block_begin(thread);
        sent = sendto(fd, data, (size_t)n, 0, (struct sockaddr *)&addr, length);
        err = sent < 0 ? errno : 0;
        zam_native_block_end(thread);
    }
    *(ZamInt *)zam_native_argument_pointer(thread, 5) = sent < 0 ? 0 : sent;
    writeErrno(thread, 6, err);
    zam_native_return_void(thread);
}

// stdUdpRecvFrom waits for one datagram and answers who sent it.
static void stdUdpRecvFrom(ZamThread *thread) {
    int fd = (int)(ZamInt)zam_native_argument(thread, 0);
    void *buffer = zam_native_argument_pointer(thread, 1);
    ZamInt n = (ZamInt)zam_native_argument(thread, 2);
    struct sockaddr_storage addr;
    socklen_t length = sizeof addr;
    zam_native_block_begin(thread);
    ssize_t got = recvfrom(fd, buffer, (size_t)n, 0, (struct sockaddr *)&addr, &length);
    int err = got < 0 ? errno : 0;
    zam_native_block_end(thread);
    *(ZamInt *)zam_native_argument_pointer(thread, 3) = got < 0 ? 0 : got;
    if (got < 0) {
        codec_write_string(thread, 4, "", 0);
        *(ZamInt *)zam_native_argument_pointer(thread, 5) = 0;
    } else {
        struct sockaddr_in6 *in6 = (struct sockaddr_in6 *)&addr;
        if (addr.ss_family == AF_INET6 && IN6_IS_ADDR_V4MAPPED(&in6->sin6_addr)) {
            struct sockaddr_in in4 = {0};
            in4.sin_family = AF_INET;
            in4.sin_port = in6->sin6_port;
            memcpy(&in4.sin_addr, &in6->sin6_addr.s6_addr[12], 4);
            writeAddr(thread, 4, (struct sockaddr *)&in4);
        } else
            writeAddr(thread, 4, (struct sockaddr *)&addr);
    }
    writeErrno(thread, 6, err);
    zam_native_return_void(thread);
}

const ZamNativeEntry zamNetNativeTable[] = {
    {"STD_IP_PARSE", stdIpParse},
    {"STD_IP_STR", stdIpStr},
    {"STD_IP_LOOKUP", stdIpLookup},
    {"STD_TCP_DIAL", stdTcpDial},
    {"STD_UDP_DIAL", stdUdpDial},
    {"STD_UNIX_DIAL", stdUnixDial},
    {"STD_NET_LISTEN", stdNetListen},
    {"STD_NET_ACCEPT", stdNetAccept},
    {"STD_NET_LISTENER_CLOSE", stdNetListenerClose},
    {"STD_NET_LISTENER_FREE", stdNetListenerFree},
    {"STD_NET_LISTENER_ADDR", stdNetListenerAddr},
    {"STD_SOCKET_ADDR", stdSocketAddr},
    {"STD_SOCKET_TIMEOUT", stdSocketTimeout},
    {"STD_SOCKET_SHUTDOWN", stdSocketShutdown},
    {"STD_UDP_BIND", stdUdpBind},
    {"STD_UDP_SEND_TO", stdUdpSendTo},
    {"STD_UDP_RECV_FROM", stdUdpRecvFrom},
};

const uint64_t zamNetNativeTableCount = sizeof(zamNetNativeTable) / sizeof(zamNetNativeTable[0]);
