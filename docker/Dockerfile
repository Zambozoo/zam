# Builds and tests zam on Linux, and builds target packs: Go, LLVM and lld 23
# from apt.llvm.org, the C libraries the codec natives wrap, CMake, Zig, and
# Wine to run what is built for Windows on the image's architecture.
FROM golang:1.25-trixie

ARG LLVM_VERSION=23
ARG ZIG_VERSION=0.17.1
# The SHA-256 of the Zig tarball for the image's architecture, as listed at
# https://ziglang.org/download/. ZIG_SHA256 overrides the one known here.
ARG ZIG_SHA256
ARG ZIG_SHA256_ARM64=a1546d5646eb184437918d6635ca6b762a0e889eac09cef720ee7a1ed3fe7795
ARG ZIG_SHA256_AMD64=3581863e3cbcfd0fce0efc682ce7e9e93da96668a977791d356a26d2a7e9b17a
ARG TARGETARCH

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg xz-utils \
    && install -d /etc/apt/keyrings \
    && curl -fsSL https://apt.llvm.org/llvm-snapshot.gpg.key -o /etc/apt/keyrings/llvm.asc \
    && echo "deb [signed-by=/etc/apt/keyrings/llvm.asc] http://apt.llvm.org/trixie/ llvm-toolchain-trixie-${LLVM_VERSION} main" \
        > /etc/apt/sources.list.d/llvm.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        llvm-${LLVM_VERSION}-dev liblld-${LLVM_VERSION}-dev lld-${LLVM_VERSION} lldb-${LLVM_VERSION} \
        build-essential pkg-config cmake wine \
        libjson-c-dev libyaml-dev libxml2-dev libbson-dev libcbor-dev \
        zlib1g-dev liblz4-dev libzstd-dev libbrotli-dev \
        libssl-dev libcurl4-openssl-dev libllhttp-dev libsodium-dev libarchive-dev \
    && rm -rf /var/lib/apt/lists/*

RUN case "${TARGETARCH}" in amd64) arch=x86_64 sum=${ZIG_SHA256:-$ZIG_SHA256_AMD64} ;; arm64) arch=aarch64 sum=${ZIG_SHA256:-$ZIG_SHA256_ARM64} ;; *) exit 1 ;; esac \
    && test -n "${sum}" || { echo "pass --build-arg ZIG_SHA256=<sha256 of the ${TARGETARCH} tarball>"; exit 1; } \
    && curl -fsSL "https://ziglang.org/download/${ZIG_VERSION}/zig-${arch}-linux-${ZIG_VERSION}.tar.xz" -o /tmp/zig.tar.xz \
    && echo "${sum}  /tmp/zig.tar.xz" | sha256sum -c - \
    && mkdir /opt/zig \
    && tar -xJf /tmp/zig.tar.xz -C /opt/zig --strip-components=1 \
    && rm /tmp/zig.tar.xz

ENV PATH="/opt/zig:/usr/lib/llvm-${LLVM_VERSION}/bin:${PATH}" \
    CGO_ENABLED=1 \
    WINEDEBUG=-all \
    WINEDLLOVERRIDES="mscoree,mshtml=" \
    XDG_RUNTIME_DIR=/run/user/0

# Debian's Wine 10.0 marks its Linux-side ntdll.so for BTI, though the entry
# points of its system call and Unix call dispatchers, which Windows code
# branches to indirectly, have no landing pad. Where the CPU enforces BTI,
# every such call faults, and handling the fault calls again, so Wine never
# starts. Without the marking the library runs unenforced.
RUN if [ "${TARGETARCH}" = arm64 ]; then \
        objcopy --remove-section .note.gnu.property /usr/lib/aarch64-linux-gnu/wine/aarch64-unix/ntdll.so; \
    fi

# Wine sets up its prefix here rather than on a test's first run, without the
# Mono and Gecko installs it would otherwise offer, and finds the session
# directory it complains of the lack of.
RUN install -d -m 700 /run/user/0 && wineboot --init && wineserver -w

WORKDIR /src
