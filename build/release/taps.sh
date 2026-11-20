#!/bin/bash
# Writes the Homebrew formula and the scoop manifest for a release of version,
# taking each archive's hash from the release's checksums.txt.
#
# usage: build/release/taps.sh <version> <checksums> <formula> <manifest>
set -euo pipefail

version=$1
checksums=$2
formula=$3
manifest=$4
number=${version#v}
releases=https://github.com/Zambozoo/zam/releases/download/$version

hash() {
    local archive="zam-$version-$1"
    local sum
    sum=$(awk -v archive="$archive" '$2 == archive { print $1 }' "$checksums")
    if [[ -z $sum ]]; then
        echo "no checksum for $archive in $checksums" >&2
        exit 1
    fi
    echo "$sum"
}

darwinAmd64=$(hash darwin-amd64.tar.gz)
darwinArm64=$(hash darwin-arm64.tar.gz)
linuxAmd64=$(hash linux-amd64.tar.gz)
linuxArm64=$(hash linux-arm64.tar.gz)
windowsAmd64=$(hash windows-amd64.zip)
windowsArm64=$(hash windows-arm64.zip)

cat >"$formula" <<EOF
class Zam < Formula
  desc "The Zam programming language"
  homepage "https://github.com/Zambozoo/zam"
  license "MIT"

  version "$number"

  os = OS.mac? ? "darwin" : "linux"
  arch = Hardware::CPU.arm? ? "arm64" : "amd64"

  url "https://github.com/Zambozoo/zam/releases/download/v#{version}/zam-v#{version}-#{os}-#{arch}.tar.gz"

  sha256 case "#{os}-#{arch}"
  when "darwin-amd64" then "$darwinAmd64"
  when "darwin-arm64" then "$darwinArm64"
  when "linux-amd64"  then "$linuxAmd64"
  when "linux-arm64"  then "$linuxArm64"
  end

  def install
    bin.install "zam"
  end

  def caveats
    <<~EOS
      With zig on the path, zam compiles its runtime for each target, and a
      project's native.files need it:
        brew install zig
    EOS
  end

  test do
    assert_match "zam", shell_output("#{bin}/zam --version")
  end
end
EOF

cat >"$manifest" <<EOF
{
    "version": "$number",
    "description": "The Zam programming language",
    "homepage": "https://github.com/Zambozoo/zam",
    "license": "MIT",
    "architecture": {
        "64bit": {
            "url": "$releases/zam-$version-windows-amd64.zip",
            "hash": "$windowsAmd64"
        },
        "arm64": {
            "url": "$releases/zam-$version-windows-arm64.zip",
            "hash": "$windowsArm64"
        }
    },
    "bin": "zam.exe",
    "suggest": {
        "zig": "main/zig"
    },
    "checkver": "github",
    "autoupdate": {
        "architecture": {
            "64bit": {
                "url": "https://github.com/Zambozoo/zam/releases/download/v\$version/zam-v\$version-windows-amd64.zip"
            },
            "arm64": {
                "url": "https://github.com/Zambozoo/zam/releases/download/v\$version/zam-v\$version-windows-arm64.zip"
            }
        },
        "hash": {
            "url": "https://github.com/Zambozoo/zam/releases/download/v\$version/checksums.txt"
        }
    }
}
EOF
