#!/bin/bash
# Builds zam for version against the LLVM at prefix, as build/release/zam.sh does,
# and archives it as a release publishes it for platform: a zip on Windows,
# which scoop installs, and a gzipped tarball elsewhere. Prints the archive.
#
# usage: build/release/dist.sh <prefix> <version> <platform>
set -euo pipefail

prefix=$1
version=$2
platform=$3
cd "$(dirname "$0")/../.."

case $(uname -s) in
MINGW* | MSYS*)
    ZAM_VERSION=$version build/release/zam.sh "$prefix" dist/zam.exe >&2
    archive=zam-$version-$platform.zip
    rm -f "$archive"
    (cd dist && 7z a "../$archive" zam.exe >/dev/null)
    ;;
*)
    ZAM_VERSION=$version build/release/zam.sh "$prefix" dist/zam >&2
    archive=zam-$version-$platform.tar.gz
    tar -czf "$archive" -C dist zam
    ;;
esac
echo "$archive"
