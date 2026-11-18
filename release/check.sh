#!/bin/bash
# Checks an archive of zam as a user would meet it: zam downloads its packs,
# served from packs, checks and extracts them, and runs hello world on this
# machine. With --all it also builds hello world for every target and runs a
# test.
#
# usage: release/check.sh [--all] <version> <archive> <packs>
set -euo pipefail

all=false
if [[ ${1:-} == --all ]]; then
    all=true
    shift
fi
version=$1
archive=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
packs=$(cd "$3" && pwd)
cd "$(dirname "$0")/.."

# native answers path as this machine's programs take it, which on Windows is
# not as Git Bash writes it.
native() {
    if command -v cygpath >/dev/null; then
        cygpath -m "$1"
    else
        echo "$1"
    fi
}

work=$PWD/bin/check
# zam keeps the packs it extracts read-only.
[[ -d $work ]] && chmod -R u+w "$work"
rm -rf "$work"
mkdir -p "$work/releases/$version" "$work/bin" "$work/cache" "$work/smoke"

cp "$packs"/zam-pack-"$version"-*.tar.zst "$work/releases/$version"
(
    cd "$work/releases/$version"
    if command -v sha256sum >/dev/null; then
        sha256sum zam-pack-* >checksums.txt
    else
        shasum -a 256 zam-pack-* >checksums.txt
    fi
)

case $archive in
*.zip) (cd "$work/bin" && 7z x "$(native "$archive")" >/dev/null) ;;
*) tar -xzf "$archive" -C "$work/bin" ;;
esac
zam=$work/bin/zam
[[ -f $zam ]] || zam=$zam.exe

python=$(command -v python3 || command -v python)
port=8765
"$python" -m http.server "$port" --bind 127.0.0.1 --directory "$(native "$work/releases")" >/dev/null 2>&1 &
server=$!
trap 'kill "$server"' EXIT
for _ in $(seq 30); do
    curl -fs "http://127.0.0.1:$port/$version/checksums.txt" >/dev/null && break
    sleep 1
done

ZAM_CACHE_DIR=$(native "$work/cache")
export ZAM_RELEASES_URL=http://127.0.0.1:$port ZAM_CACHE_DIR
unset ZAM_PACKS

got=$("$zam" --version)
if [[ ${got%$'\r'} != "zam $version" ]]; then
    echo "zam --version says $got, not zam $version" >&2
    exit 1
fi

got=$("$zam" run --native docs/0_example/helloworld/Main.zam)
if [[ ${got%$'\r'} != "Hello World!" ]]; then
    echo "hello world wrote $got" >&2
    exit 1
fi

if ! $all; then
    echo "zam $version checks out"
    exit 0
fi

for target in aarch64-linux-musl x86_64-linux-musl aarch64-macos x86_64-macos aarch64-windows-gnu x86_64-windows-gnu; do
    echo "build $target"
    "$zam" build --native --target "$target" -o "$(native "$work/hello-$target")" docs/0_example/helloworld/Main.zam
done

cat >"$work/smoke/Smoke.test.zam" <<'EOF'
import std("test/Assert.zam");

public namespace SmokeTest {
    public fun void(uint, str...) Adds = (argC, argV) {
        Assert::[int]Equal(4, 2 + 2);
    }
}
EOF
"$zam" test --native -n "$(native "$work/smoke")"
echo "zam $version checks out"
