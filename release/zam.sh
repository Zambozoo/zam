#!/bin/bash
# Builds zam with LLVM and lld linked in statically from the build of
# release/llvm.sh at prefix: for macOS 13 and later, and fully static against
# musl with Zig on Linux. $ZAM_VERSION is the version zam downloads its target
# packs for; $LLVM_LIBS names anything else the LLVM build needs.
#
# usage: release/zam.sh <prefix> <output>
set -euo pipefail

prefix=$1
output=$2
cd "$(dirname "$0")/.."

ldflags="-s -w"
if [[ -n ${ZAM_VERSION:-} ]]; then
    ldflags+=" -X github.com/zambozoo/zam/internal/5_build.Version=$ZAM_VERSION"
fi

libs=("$prefix"/lib/liblld{MachO,ELF,MinGW,COFF,Common}.a "$prefix"/lib/libLLVM*.a)
export CGO_ENABLED=1 CGO_CPPFLAGS="-I$prefix/include"
case $(uname -s) in
Darwin)
    export MACOSX_DEPLOYMENT_TARGET=13.0
    export CGO_LDFLAGS="${libs[*]} ${LLVM_LIBS:-} -lc++"
    go build -tags zam_static -trimpath -ldflags "$ldflags" -o "$output" ./cmd
    ;;
Linux)
    target=$(uname -m)-linux-musl
    export CC="zig cc -target $target" CXX="zig c++ -target $target"
    export CGO_LDFLAGS="-Wl,--start-group ${libs[*]} -Wl,--end-group ${LLVM_LIBS:-} -lc++"
    go build -tags zam_static -trimpath -ldflags "$ldflags -linkmode external -extldflags -static" -o "$output" ./cmd
    ;;
*)
    echo "unsupported system $(uname -s)" >&2
    exit 2
    ;;
esac
