#!/bin/bash
# Builds zam with LLVM and lld linked in statically from the build of
# build/release/llvm.sh at prefix, as build/release/cgo.sh sets cgo up for the system.
# $ZAM_VERSION is the version zam downloads its target packs for.
#
# usage: build/release/zam.sh <prefix> <output>
set -euo pipefail

cd "$(dirname "$0")/../.."
source build/release/cgo.sh "$1"

ldflags="-s -w $zamLinkFlags"
if [[ -n ${ZAM_VERSION:-} ]]; then
    ldflags+=" -X github.com/zambozoo/zam/internal/compiler/3_build.Version=$ZAM_VERSION"
fi
go build -tags zam_static -trimpath -ldflags "$ldflags" -o "$2" .
