# Sets the environment cgo links LLVM and lld into zam with, statically, from
# the build of release/llvm.sh at the prefix given: for macOS 13 and later,
# fully static against musl with Zig on Linux, and with Zig for mingw on
# Windows, as $ZAM_TARGET names the target there. $LLVM_LIBS names anything
# else the LLVM build needs. It leaves in zamLinkFlags what go build's -ldflags
# also needs, and is sourced by release/zam.sh and by CI jobs that test zam.
#
# usage: source release/cgo.sh <prefix>

zamPrefix=$1
# Windows programs read paths as Windows writes them, not as Git Bash does.
case $(uname -s) in
MINGW* | MSYS*) zamPrefix=$(cygpath -m "$zamPrefix") ;;
esac

zamLibs=("$zamPrefix"/lib/liblld{MachO,ELF,MinGW,COFF,Common}.a "$zamPrefix"/lib/libLLVM*.a)
export CGO_ENABLED=1 CGO_CPPFLAGS="-I$zamPrefix/include"
case $(uname -s) in
Darwin)
    export MACOSX_DEPLOYMENT_TARGET=13.0
    export CGO_LDFLAGS="${zamLibs[*]} ${LLVM_LIBS:-} -lc++"
    zamLinkFlags=
    ;;
Linux)
    zamTarget=$(uname -m)-linux-musl
    export CC="zig cc -target $zamTarget" CXX="zig c++ -target $zamTarget"
    export CGO_LDFLAGS="-Wl,--start-group ${zamLibs[*]} -Wl,--end-group ${LLVM_LIBS:-} -lc++"
    zamLinkFlags="-linkmode external -extldflags -static"
    ;;
MINGW* | MSYS*)
    zamTarget=${ZAM_TARGET:?set ZAM_TARGET to the Windows target, as x86_64-windows-gnu}
    export CC="zig cc -target $zamTarget" CXX="zig c++ -target $zamTarget"
    export CGO_LDFLAGS="-Wl,--start-group ${zamLibs[*]} -Wl,--end-group ${LLVM_LIBS:-} -lc++ \
-lpsapi -lshell32 -lole32 -luuid -ladvapi32 -lws2_32 -lntdll -lversion"
    zamLinkFlags="-linkmode external -extldflags -static"
    ;;
*)
    echo "unsupported system $(uname -s)" >&2
    return 2
    ;;
esac
