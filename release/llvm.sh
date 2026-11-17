#!/bin/bash
# Builds LLVM and lld as static libraries for the machine it runs on, for zam
# to link into itself: with the system's clang for macOS 13 and later, with Zig
# for musl on Linux, so that zam needs no C++ library where it runs, and with
# Zig for mingw on Windows, as $ZAM_TARGET names the target there.
#
# usage: release/llvm.sh <version> <prefix>
set -euo pipefail

version=$1
prefix=$2
work=${LLVM_WORK:-$(mktemp -d)}
# Windows programs read paths as Windows writes them, not as Git Bash does.
case $(uname -s) in
MINGW* | MSYS*)
    mkdir -p "$prefix"
    prefix=$(cygpath -m "$prefix")
    work=$(cygpath -m "$work")
    ;;
esac

git clone --quiet --depth 1 --branch "llvmorg-$version" https://github.com/llvm/llvm-project.git "$work/src"

args=(
    -G Ninja -S "$work/src/llvm" -B "$work/build"
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$prefix"
    -DLLVM_ENABLE_PROJECTS=lld -DLLVM_TARGETS_TO_BUILD="AArch64;X86"
    -DBUILD_SHARED_LIBS=OFF -DLLVM_BUILD_LLVM_DYLIB=OFF -DLLVM_LINK_LLVM_DYLIB=OFF -DLLVM_ENABLE_PIC=ON
    -DLLVM_ENABLE_ZLIB=OFF -DLLVM_ENABLE_ZSTD=OFF -DLLVM_ENABLE_LIBXML2=OFF -DLLVM_ENABLE_Z3_SOLVER=OFF
    -DLLVM_ENABLE_LIBEDIT=OFF -DLLVM_ENABLE_LIBPFM=OFF -DLLVM_ENABLE_CURL=OFF -DLLVM_ENABLE_HTTPLIB=OFF
    -DLLVM_INCLUDE_TESTS=OFF -DLLVM_INCLUDE_BENCHMARKS=OFF -DLLVM_INCLUDE_EXAMPLES=OFF -DLLVM_INCLUDE_DOCS=OFF
    -DLLVM_BUILD_TOOLS=OFF -DLLD_BUILD_TOOLS=OFF
)
case $(uname -s) in
Darwin)
    args+=(-DCMAKE_OSX_DEPLOYMENT_TARGET=13.0)
    ;;
Linux)
    target=$(uname -m)-linux-musl
    tools=$work/tools
    mkdir -p "$tools"
    for tool in cc c++; do
        printf '#!/bin/sh\nexec zig %s -target %s "$@"\n' "$tool" "$target" >"$tools/$tool"
    done
    printf '#!/bin/sh\nexec zig ar "$@"\n' >"$tools/ar"
    printf '#!/bin/sh\nexec zig ranlib "$@"\n' >"$tools/ranlib"
    chmod +x "$tools"/*
    args+=(
        -DCMAKE_C_COMPILER="$tools/cc" -DCMAKE_CXX_COMPILER="$tools/c++"
        -DCMAKE_AR="$tools/ar" -DCMAKE_RANLIB="$tools/ranlib" -DLLVM_HOST_TRIPLE="$target"
    )
    ;;
MINGW* | MSYS*)
    target=${ZAM_TARGET:?set ZAM_TARGET to the Windows target, as x86_64-windows-gnu}
    tools=$work/tools
    mkdir -p "$tools"
    # CMake runs a compiler on Windows only as a program or a batch file.
    for tool in cc c++; do
        printf '@zig %s -target %s %%*\r\n' "$tool" "$target" >"$tools/$tool.cmd"
    done
    printf '@zig ar %%*\r\n' >"$tools/ar.cmd"
    printf '@zig ranlib %%*\r\n' >"$tools/ranlib.cmd"
    args+=(
        -DCMAKE_C_COMPILER="$tools/cc.cmd" -DCMAKE_CXX_COMPILER="$tools/c++.cmd"
        -DCMAKE_AR="$tools/ar.cmd" -DCMAKE_RANLIB="$tools/ranlib.cmd"
        -DLLVM_HOST_TRIPLE="${target%%-*}-w64-windows-gnu"
    )
    ;;
*)
    echo "unsupported system $(uname -s)" >&2
    exit 2
    ;;
esac

cmake "${args[@]}"
cmake --build "$work/build"
cmake --install "$work/build"
