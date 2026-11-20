#!/bin/bash
# Builds the target pack for each target named, with Zig as the C compiler,
# into bin/packs/<target>: static archives and headers of the C libraries the
# std natives wrap, the zam runtime, compiler-rt, the C library to link against
# and every library's license.
#
# usage: docker/pack.sh <target>... [--runtime-only]
# targets: x86_64-linux-musl aarch64-linux-musl x86_64-macos aarch64-macos
#          x86_64-windows-gnu aarch64-windows-gnu
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
lock=$repo/docker/sources.lock
cache=${ZAM_PACK_CACHE:-$HOME/.cache/zam-packs}
parallel=$(nproc)

runtimeOnly=false
targets=()
for arg; do
    case $arg in
    --runtime-only) runtimeOnly=true ;;
    *) targets+=("$arg") ;;
    esac
done
((${#targets[@]})) || { echo "usage: $0 <target>... [--runtime-only]" >&2; exit 2; }

zigLib=$(zig env | sed -n 's/.*lib_dir[" =:]*"\([^"]*\)".*/\1/p')

# fetch extracts the locked source of name into dir, recording its SHA-256
# in the lock the first time it is fetched.
fetch() {
    local name=$1 dir=$2 version url sum
    read -r version url sum < <(awk -v n="$name" '$1 == n { print $2, $3, $4 }' "$lock") || true
    [[ -n ${url:-} ]] || { echo "$name is not in $lock" >&2; exit 1; }
    local archive=$cache/sources/$name-$version.tar.${url##*.tar.}
    mkdir -p "$cache/sources"
    if [[ ! -f $archive ]]; then
        curl -fsSL "$url" -o "$archive.tmp"
        mv "$archive.tmp" "$archive"
    fi
    local actual
    actual=$(sha256sum "$archive" | cut -d' ' -f1)
    if [[ $sum == - ]]; then
        sed -i "s|^\($name[[:space:]].*[[:space:]]\)-$|\1$actual|" "$lock"
        echo "recorded $name $version $actual"
    elif [[ $sum != "$actual" ]]; then
        echo "$name $version: expected SHA-256 $sum, got $actual" >&2
        exit 1
    fi
    rm -rf "$dir"
    mkdir -p "$dir"
    tar -xf "$archive" -C "$dir" --strip-components=1
}

# built reports whether the locked version of name is already in the pack,
# which a deleted pack forgets along with it.
built() {
    [[ -f $pack/.stamps/$1-$(awk -v n="$1" '$1 == n { print $2 }' "$lock") ]]
}

# stamp records that the locked version of name is in the pack.
stamp() {
    mkdir -p "$pack/.stamps"
    touch "$pack/.stamps/$1-$(awk -v n="$1" '$1 == n { print $2 }' "$lock")"
}

# licenses copies the license files of the source in dir into the pack.
licenses() {
    local name=$1 dir=$2
    mkdir -p "$pack/licenses/$name"
    find "$dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'COPYING*' -o -iname 'COPYRIGHT*' -o -iname 'NOTICE*' \) \
        -exec cp {} "$pack/licenses/$name/" \;
}

# cmakeBuild configures, builds and installs the CMake project in dir into the
# pack with the target's toolchain.
cmakeBuild() {
    local dir=$1
    shift
    cmake -S "$dir" -B "$dir/build" -DCMAKE_TOOLCHAIN_FILE="$tools/toolchain.cmake" \
        -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$pack" -DCMAKE_INSTALL_LIBDIR=lib \
        -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DBUILD_SHARED_LIBS=OFF -DBUILD_TESTING=OFF "$@" >"$dir/configure.log" 2>&1
    cmake --build "$dir/build" -j "$parallel" >"$dir/build.log" 2>&1
    cmake --install "$dir/build" >"$dir/install.log" 2>&1
}

# configureBuild runs the autotools project in dir into the pack, making
# $MAKE_TARGETS, else everything, and installing $INSTALL_TARGETS, else all.
configureBuild() {
    local dir=$1
    shift
    (cd "$dir" && ./configure --host="$host" --prefix="$pack" --libdir="$pack/lib" \
        --disable-shared --enable-static --with-pic \
        CC="$tools/cc" AR="$tools/ar" RANLIB="$tools/ranlib" "$@" >configure.log 2>&1 &&
        make -j "$parallel" ${MAKE_TARGETS:-} >build.log 2>&1 &&
        make ${INSTALL_TARGETS:-install} >install.log 2>&1)
}

# tools writes the compiler wrappers and CMake toolchain for the target.
writeTools() {
    mkdir -p "$tools"
    for tool in cc c++; do
        cat >"$tools/$tool" <<EOF
#!/bin/bash
# Drops -arch, which build systems pass for macOS and the target already says,
# -m64, which OpenSSL's mingw64 passes and an Arm target refuses, and LTO,
# which would leave Zig's LLVM bitcode in the archives in place of machine
# code.
args=()
while ((\$#)); do
    case \$1 in
    -arch) shift 2; continue ;;
    -flto | -flto=* | -ffat-lto-objects | -fno-fat-lto-objects) shift; continue ;;
    -m64) [[ $arch == aarch64 ]] && { shift; continue; } ;;
    esac
    args+=("\$1"); shift
done
exec zig $tool -target $zigTarget $frameworkFlags "\${args[@]}"
EOF
    done
    # Windows resources, which some libraries compile in, go through LLVM's
    # windres, with Zig to preprocess them.
    printf '#!/bin/sh\nexec llvm-windres --target=%s-w64-mingw32 --preprocessor=%s --preprocessor-arg=-E --preprocessor-arg=-xc --preprocessor-arg=-DRC_INVOKED "$@"\n' \
        "$arch" "$tools/cc" >"$tools/windres"
    printf '#!/bin/sh\nexec zig ar "$@"\n' >"$tools/ar"
    printf '#!/bin/sh\nexec zig ranlib "$@"\n' >"$tools/ranlib"
    chmod +x "$tools"/*
    cat >"$tools/toolchain.cmake" <<EOF
set(CMAKE_SYSTEM_NAME $cmakeSystem)
set(CMAKE_SYSTEM_PROCESSOR $arch)
set(CMAKE_C_COMPILER $tools/cc)
set(CMAKE_CXX_COMPILER $tools/c++)
set(CMAKE_AR $tools/ar)
set(CMAKE_RANLIB $tools/ranlib)
set(CMAKE_RC_COMPILER $tools/windres)
set(CMAKE_OSX_SYSROOT "")
set(CMAKE_INSTALL_NAME_TOOL /bin/true)
set(CMAKE_FRAMEWORK_PATH $pack/frameworks)
set(CMAKE_FIND_ROOT_PATH $pack)
set(CMAKE_PREFIX_PATH $pack)
set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)
set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)
EOF
}

buildLibraries() {
    local src=$work/src

    if [[ $os == linux ]]; then
        if ! built musl; then
            fetch musl "$src/musl"
            (cd "$src/musl" && ./configure --target="$host" --prefix="$pack/libc" --disable-shared \
                CC="$tools/cc" AR="$tools/ar" RANLIB="$tools/ranlib" >configure.log 2>&1 &&
                make -j "$parallel" >build.log 2>&1 && make install >install.log 2>&1)
            licenses musl "$src/musl"
            stamp musl
        fi
    elif [[ $os == macos ]]; then
        cp "$zigLib/libc/darwin/libSystem.tbd" "$pack/lib/"
        mkdir -p "$pack/frameworks"
        cp -R "$repo/docker/darwin/." "$pack/frameworks/"
    fi

    if ! built zlib; then
        fetch zlib "$src/zlib"
        if [[ $os == windows ]]; then
            # zlib's configure does not cross-compile for mingw; its own
            # makefile for it builds the static library alone.
            make -C "$src/zlib" -f win32/Makefile.gcc -j "$parallel" libz.a \
                CC="$tools/cc" AR="$tools/ar" RANLIB="$tools/ranlib" CFLAGS="-O2" >"$src/zlib/build.log" 2>&1
            cp "$src/zlib/libz.a" "$pack/lib/"
            cp "$src/zlib/zlib.h" "$src/zlib/zconf.h" "$pack/include/"
        else
            (cd "$src/zlib" && CC="$tools/cc" AR="$tools/ar" RANLIB="$tools/ranlib" CFLAGS="-O2 -fPIC" \
                ./configure --static --prefix="$pack" --libdir="$pack/lib" >configure.log 2>&1 &&
                make -j "$parallel" >build.log 2>&1 && make install >install.log 2>&1)
        fi
        licenses zlib "$src/zlib"
        stamp zlib
    fi

    if ! built zstd; then
        fetch zstd "$src/zstd"
        make -C "$src/zstd/lib" -j "$parallel" install-static install-includes install-pc \
            CC="$tools/cc" AR="$tools/ar" CFLAGS="-O2 -fPIC" PREFIX="$pack" LIBDIR="$pack/lib" >"$src/zstd/build.log" 2>&1
        licenses zstd "$src/zstd"
        stamp zstd
    fi

    if ! built lz4; then
        fetch lz4 "$src/lz4"
        # lz4's makefile compiles its sources in one command, which Zig cannot
        # do for Windows; the library is these four files.
        (cd "$src/lz4/lib" && for source in lz4 lz4frame lz4hc xxhash; do
            "$tools/cc" -O2 -fPIC -c "$source.c" -o "$source.o" || exit 1
        done >build.log 2>&1 &&
            "$tools/ar" rcs "$pack/lib/liblz4.a" lz4.o lz4frame.o lz4hc.o xxhash.o &&
            cp lz4.h lz4frame.h lz4hc.h "$pack/include/")
        licenses lz4 "$src/lz4"
        stamp lz4
    fi

    if ! built brotli; then
        fetch brotli "$src/brotli"
        cmakeBuild "$src/brotli" -DBROTLI_DISABLE_TESTS=ON
        licenses brotli "$src/brotli"
        stamp brotli
    fi

    if ! built openssl; then
        fetch openssl "$src/openssl"
        (cd "$src/openssl" && ./Configure "$opensslTarget" $opensslOptions no-shared no-tests no-docs no-module \
            --prefix="$pack" --libdir=lib --openssldir=/etc/ssl \
            CC="$tools/cc" AR="$tools/ar" RANLIB="$tools/ranlib" >configure.log 2>&1 &&
            make -j "$parallel" build_libs >build.log 2>&1 && make install_dev >install.log 2>&1)
        licenses openssl "$src/openssl"
        stamp openssl
    fi

    if ! built json-c; then
        fetch json-c "$src/json-c"
        cmakeBuild "$src/json-c" -DBUILD_STATIC_LIBS=ON -DDISABLE_WERROR=ON -DBUILD_APPS=OFF
        licenses json-c "$src/json-c"
        stamp json-c
    fi

    if ! built libyaml; then
        fetch libyaml "$src/libyaml"
        configureBuild "$src/libyaml"
        licenses libyaml "$src/libyaml"
        stamp libyaml
    fi

    if ! built libxml2; then
        fetch libxml2 "$src/libxml2"
        cmakeBuild "$src/libxml2" -DLIBXML2_WITH_PYTHON=OFF -DLIBXML2_WITH_LZMA=OFF -DLIBXML2_WITH_ZLIB=OFF \
            -DLIBXML2_WITH_ICONV=OFF -DLIBXML2_WITH_ICU=OFF -DLIBXML2_WITH_HTTP=OFF \
            -DLIBXML2_WITH_PROGRAMS=OFF -DLIBXML2_WITH_TESTS=OFF
        licenses libxml2 "$src/libxml2"
        stamp libxml2
    fi

    if ! built libbson; then
        fetch libbson "$src/libbson"
        cmakeBuild "$src/libbson" -DENABLE_MONGOC=OFF -DENABLE_SHARED=OFF -DENABLE_STATIC=ON \
            -DENABLE_TESTS=OFF -DENABLE_EXAMPLES=OFF -DENABLE_UNINSTALL=OFF
        licenses libbson "$src/libbson"
        stamp libbson
    fi

    if ! built libcbor; then
        fetch libcbor "$src/libcbor"
        cmakeBuild "$src/libcbor" -DWITH_EXAMPLES=OFF -DWITH_TESTS=OFF
        licenses libcbor "$src/libcbor"
        stamp libcbor
    fi

    if ! built curl; then
        fetch curl "$src/curl"
        cmakeBuild "$src/curl" -DBUILD_STATIC_LIBS=ON -DBUILD_CURL_EXE=OFF -DBUILD_LIBCURL_DOCS=OFF \
            -DBUILD_MISC_DOCS=OFF -DENABLE_CURL_MANUAL=OFF -DCURL_USE_OPENSSL=ON -DCURL_USE_LIBPSL=OFF \
            -DCURL_USE_LIBSSH2=OFF -DUSE_LIBIDN2=OFF -DUSE_NGHTTP2=OFF -DCURL_BROTLI=OFF -DCURL_ZSTD=OFF \
            -DCURL_ZLIB=ON -DCURL_DISABLE_LDAP=ON -DENABLE_ARES=OFF \
            -DCURL_CA_BUNDLE=none -DCURL_CA_PATH=none -DCURL_CA_FALLBACK=ON
        licenses curl "$src/curl"
        stamp curl
    fi

    if ! built llhttp; then
        fetch llhttp "$src/llhttp"
        cmakeBuild "$src/llhttp" -DBUILD_STATIC_LIBS=ON
        licenses llhttp "$src/llhttp"
        stamp llhttp
    fi

    if ! built libsodium; then
        fetch libsodium "$src/libsodium"
        configureBuild "$src/libsodium"
        licenses libsodium "$src/libsodium"
        stamp libsodium
    fi

    if ! built libarchive; then
        fetch libarchive "$src/libarchive"
        # Only the library: its command-line tools' helpers need what macOS
        # declares only in its SDK.
        MAKE_TARGETS=libarchive.la \
            INSTALL_TARGETS="install-libLTLIBRARIES install-includeHEADERS install-pkgconfigDATA" \
            configureBuild "$src/libarchive" CPPFLAGS="-I$pack/include" LDFLAGS="-L$pack/lib" \
            --with-zlib --with-zstd --with-lz4 --without-bz2lib --without-lzma --without-lzo2 \
            --without-libb2 --without-openssl --without-nettle --without-mbedtls --without-cng \
            --without-xml2 --without-expat --without-iconv --disable-acl --disable-xattr \
            --disable-bsdtar --disable-bsdcpio --disable-bsdcat --disable-bsdunzip
        licenses libarchive "$src/libarchive"
        stamp libarchive
    fi

    # A Windows pack takes compiler-rt with the rest of what Zig links.
    if [[ $os == windows ]]; then
        windowsLibraries
    else
        zig build-lib "$zigLib/compiler_rt.zig" -target "$zigTarget" -O ReleaseFast -fPIC -static \
            --name compiler_rt -femit-bin="$pack/lib/libcompiler_rt.a" --cache-dir "$work/zig-cache"
    fi

    ls "$pack/.stamps" | sed 's/-\([^-]*\)$/ \1/' >"$pack/sources.txt"
}

# windowsSystemLibraries are the Windows DLLs the runtime and the pack's
# libraries call, whose import libraries a Windows pack holds.
windowsSystemLibraries=(kernel32 ntdll advapi32 user32 shell32 ws2_32 crypt32 bcrypt gdi32 secur32 iphlpapi)

# windowsLibraries puts in the pack what Zig links a Windows program with: the
# mingw-w64 C runtime and its start-up object, winpthreads, Zig's libc and the
# import libraries of windowsSystemLibraries, as Zig builds them, and writes the
# flags it links with to link.txt. A probe program linked with --verbose-link
# names them.
windowsLibraries() {
    local probe=$work/probe
    rm -rf "$probe"
    mkdir -p "$probe"
    printf 'int main(void) { return 0; }\n' >"$probe/probe.c"
    local libs=()
    for library in "${windowsSystemLibraries[@]}"; do
        libs+=(-l"$library")
    done
    local link
    link=$(cd "$probe" && zig build-exe -target "$zigTarget" -O ReleaseFast -lc "${libs[@]}" probe.c \
        --name probe --cache-dir "$work/zig-cache" --verbose-link 2>&1 | grep -E '^([^ ]*/)?lld-link ' | tail -1)
    [[ -n $link ]] || { echo "zig did not show how it links for $zigTarget" >&2; exit 1; }

    : >"$pack/link.txt"
    for argument in $link; do
        case $argument in
        lld-link | */probe.obj | -OUT:* | -IMPLIB:* | -PDB* | -DEBUG* | -ERRORLIMIT:* | -NOLOGO) ;;
        -*) echo "$argument" >>"$pack/link.txt" ;;
        *.lib | *.obj) cp "$argument" "$pack/lib/" ;;
        esac
    done
}

# buildRuntime compiles the zam runtime against the pack's headers into
# lib/libzamrt.a.
buildRuntime() {
    local runtime=$repo/internal/5_build/runtime objects=$work/runtime
    rm -rf "$objects"
    mkdir -p "$objects"
    local includes=(-I"$runtime/src" -I"$runtime/include" -I"$pack/include" -I"$pack/include/libxml2")
    for dir in "$pack"/include/bson-*; do
        includes+=(-I"$dir")
    done
    local pids=()
    for source in "$runtime"/src/*.c; do
        "$tools/cc" -c -std=gnu11 -O3 -fPIC -DCURL_STATICLIB -DBSON_STATIC -DLIBXML_STATIC "${includes[@]}" \
            -o "$objects/$(basename "$source" .c).o" "$source" &
        pids+=($!)
    done
    for pid in "${pids[@]}"; do
        wait "$pid"
    done
    rm -f "$pack/lib/libzamrt.a"
    "$tools/ar" rcs "$pack/lib/libzamrt.a" "$objects"/*.o
}

# A failed step's log is shown, its output having gone there.
trap 'status=$?; if ((status)) && [[ -n ${work:-} ]]; then
    log=$(ls -t "$work"/src/*/*.log 2>/dev/null | head -1)
    [[ -z $log ]] || { echo "--- $log" >&2; tail -n 60 "$log" >&2; }
fi' EXIT

# verify fails unless every object in the pack's archives is one for the
# target, as llvm-readobj names its format.
verify() {
    local bad=
    for archive in "$pack"/lib/*.a "$pack"/libc/lib/*.a; do
        [[ -f $archive ]] || continue
        bad+=$(llvm-readobj --file-headers "$archive" 2>&1 |
            sed -n 's/^Format: //p; s/^.*error: /error: /p' | sort -u | grep -vxF "$objectFormat" |
            sed "s|^|$(basename "$archive"): |" || true)
    done
    if [[ -n $bad ]]; then
        echo "objects in $pack not built for $target ($objectFormat):" >&2
        echo "$bad" >&2
        exit 1
    fi
}

for target in "${targets[@]}"; do
    arch=${target%%-*}
    case $target in
    x86_64-linux-musl | aarch64-linux-musl)
        os=linux zigTarget=$target host=$target cmakeSystem=Linux opensslTarget=linux-$arch opensslOptions= frameworkFlags=
        objectFormat=$([[ $arch == aarch64 ]] && echo elf64-littleaarch64 || echo elf64-x86-64)
        ;;
    x86_64-macos | aarch64-macos)
        os=macos zigTarget=$target.13.0 host=$arch-apple-darwin cmakeSystem=Darwin
        frameworkFlags="-F$repo/bin/packs/$target/frameworks"
        objectFormat=$([[ $arch == aarch64 ]] && echo "Mach-O arm64" || echo "Mach-O 64-bit x86-64")
        opensslTarget=darwin64-${arch/aarch64/arm64} opensslOptions=
        ;;
    x86_64-windows-gnu | aarch64-windows-gnu)
        os=windows zigTarget=$target host=$arch-w64-mingw32 cmakeSystem=Windows frameworkFlags=
        objectFormat=$([[ $arch == aarch64 ]] && echo COFF-ARM64 || echo COFF-x86-64)
        # OpenSSL has no Arm mingw configuration; mingw64's fits it but for its
        # x86 assembly.
        opensslTarget=mingw64 opensslOptions=$([[ $arch == aarch64 ]] && echo no-asm || true)
        ;;
    *)
        echo "unknown target $target" >&2
        exit 2
        ;;
    esac
    pack=$repo/bin/packs/$target
    work=$cache/work/$target
    tools=$work/tools
    echo "== $target"
    mkdir -p "$work" "$pack/lib" "$pack/include"
    writeTools
    if ! $runtimeOnly; then
        buildLibraries
    fi
    buildRuntime
    rm -rf "$pack/bin" "$pack/share" "$pack/lib/cmake" "$pack"/lib/*.la
    verify
    echo "built $pack ($(du -sh "$pack" | cut -f1))"
done
