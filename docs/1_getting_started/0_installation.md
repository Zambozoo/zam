# Installation
## Mac and Linux
Zam is available through homebrew.
```
brew tap zambozoo/zam
brew install zam
zam -h
```

## Windows
Zam is available through scoop.
```
scoop bucket add zambozoo https://github.com/Zambozoo/scoop-zam
scoop install zam
zam -h
```

## Binary
Zam can be built locally through this repository as well.

Make sure you have the following installed:
* `go`
* `task`
* `cc`, for the cgo bindings to LLVM
* LLVM 23 and lld: `brew install llvm lld` on macOS, or `llvm-23-dev` and `liblld-23-dev` from [apt.llvm.org](https://apt.llvm.org) on Debian and Ubuntu
* `zig`, to compile the runtime and the tests' C harnesses

Then fetch and build.
```
git clone https://github.com/zambozoo/zam
task build
./zam -h
```

The tests link against the target packs in `bin/packs`, which `task packs` builds, or those `$ZAM_PACKS` names.

The Linux image in `docker/` has all of these. `task linux:image` builds it, and `task test:linux` runs the tests in it.
