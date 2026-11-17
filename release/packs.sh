#!/bin/bash
# Archives each target pack in bin/packs for a release of version into dir, as
# zam-pack-<version>-<target>.tar.zst, and writes the release notes naming
# what the packs bundle and under which licenses.
#
# usage: release/packs.sh <version> <dir>
set -euo pipefail

version=$1
dir=$2
cd "$(dirname "$0")/.."
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)

notes=$dir/notes.md
{
    echo "## Target packs"
    echo "Each \`zam-pack-$version-<target>.tar.zst\` holds static builds of these libraries for its target, with each library's license text under \`licenses/\`:"
    echo
    echo "| Library | Version | Targets |"
    echo "|---|---|---|"
} >"$notes"

# A pack archive holds only its files, named from the pack's root, as zam reads
# them.
for pack in bin/packs/*/; do
    target=$(basename "$pack")
    archive=$dir/zam-pack-$version-$target.tar.zst
    (cd "$pack" && find . -type f ! -path './.stamps/*' | sed 's|^\./||' | LC_ALL=C sort |
        tar --zstd -cf "$archive" -T -)
done

cat bin/packs/*/sources.txt | sort -u | while read -r name libraryVersion; do
    targets=$(grep -lx "$name $libraryVersion" bin/packs/*/sources.txt | xargs -n1 dirname | xargs -n1 basename | paste -sd, - | sed 's/,/, /g')
    echo "| $name | $libraryVersion | $targets |" >>"$notes"
done
