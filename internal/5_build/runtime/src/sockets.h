#pragma once

// Sockets as the zam side holds them: a descriptor in a File. On Windows a
// socket is no C runtime descriptor, so its handle is held with
// ZAM_SOCKET_TAG set, which no descriptor the C runtime hands out has, and the
// file natives read, write and close it as a socket. Windows handles fit in
// 32 bits, and a socket's in fewer than the tag's.

#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#if defined(_WIN32)
#include <winsock2.h>
#include <ws2tcpip.h>
#include <afunix.h>
#include <string.h>

#define ZAM_SOCKET_TAG 0x40000000

#define ZAM_SOCKET_EINTR WSAEINTR
#define ZAM_SOCKET_EAGAIN WSAEWOULDBLOCK
#define ZAM_SOCKET_ECONNABORTED WSAECONNABORTED
#define ZAM_SOCKET_ECONNREFUSED WSAECONNREFUSED
#define ZAM_SOCKET_ENOTCONN WSAENOTCONN
#define ZAM_SOCKET_EINVAL WSAEINVAL

typedef SOCKET ZamSocket;

static inline bool zam_fd_is_socket(int fd) { return fd >= 0 && (fd & ZAM_SOCKET_TAG) != 0; }

static inline ZamSocket zam_fd_socket(int fd) { return (ZamSocket)(unsigned)(fd & ~ZAM_SOCKET_TAG); }

// zam_socket_fd is the descriptor the zam side holds for s, -1 for none.
static inline int zam_socket_fd(ZamSocket s) {
    if (s == INVALID_SOCKET)
        return -1;
    if ((uint64_t)s >= ZAM_SOCKET_TAG) {
        closesocket(s);
        WSASetLastError(WSAEMFILE);
        return -1;
    }
    return (int)s | ZAM_SOCKET_TAG;
}

static inline int zam_socket_close(int fd) { return closesocket(zam_fd_socket(fd)); }

static inline int zam_socket_error(void) { return WSAGetLastError(); }

static inline int zam_socket_poll(struct pollfd *fds, unsigned count, int ms) {
    return WSAPoll(fds, count, ms);
}

// zam_socket_strerror says what a socket error is, in a buffer of the calling
// thread's own.
static inline const char *zam_socket_strerror(int err) {
    static _Thread_local char text[256];
    DWORD length = FormatMessageA(FORMAT_MESSAGE_FROM_SYSTEM | FORMAT_MESSAGE_IGNORE_INSERTS, NULL,
                                  (DWORD)err, 0, text, sizeof text, NULL);
    while (length > 0 && (text[length - 1] == '\n' || text[length - 1] == '\r' || text[length - 1] == '.'))
        text[--length] = '\0';
    if (length == 0)
        snprintf(text, sizeof text, "socket error %d", err);
    return text;
}
#else
#include <arpa/inet.h>
#include <errno.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#define ZAM_SOCKET_EINTR EINTR
#define ZAM_SOCKET_EAGAIN EAGAIN
#define ZAM_SOCKET_ECONNABORTED ECONNABORTED
#define ZAM_SOCKET_ECONNREFUSED ECONNREFUSED
#define ZAM_SOCKET_ENOTCONN ENOTCONN
#define ZAM_SOCKET_EINVAL EINVAL

typedef int ZamSocket;

static inline bool zam_fd_is_socket(int fd) {
    (void)fd;
    return false;
}

static inline ZamSocket zam_fd_socket(int fd) { return fd; }

static inline int zam_socket_fd(ZamSocket s) { return s; }

static inline int zam_socket_close(int fd) { return close(fd); }

static inline int zam_socket_error(void) { return errno; }

static inline int zam_socket_poll(struct pollfd *fds, unsigned count, int ms) {
    return poll(fds, count, ms);
}

static inline const char *zam_socket_strerror(int err) { return strerror(err); }
#endif
