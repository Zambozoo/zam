# Commands
`zam <command> [flags] [arguments]`

| Command | Arguments | Meaning |
|---|---|---|
| `build` | `<input file>` | compile a program to a binary |
| `run` | `<input file> [program arguments]` | compile a program to a temporary binary and run it, passing on the arguments after its file |
| `test` | `[path...] [-- test arguments]` | compile the tests in test files and run each file's in a process of its own |
| `get` | `[repo@version...]` | add dependencies to the nearest project file, then fetch every dependency the project reaches |
| `clean` | | remove every fetched dependency and cached target pack and runtime |
| `lsp` | | serve the language server protocol over stdin and stdout |
| `debug` | | serve the debug adapter protocol over stdin and stdout; see [Debugging](../4_memory_and_runtime/3_debugging.md) |
| `version` | | print the version of zam; `zam --version` does the same |

`zam help` lists the commands, and `zam <command> -h` lists a command's flags.

## build, run and test
Flags come before the input file, or the paths `test` is given. Sizes take an optional `B`, `KB`, `MB` or `GB` suffix, and a bare number is a count of bytes.

### Output
| Flag | Default | Meaning |
|---|---|---|
| `-o`, `--output` | `out` | file to write; `build` only |
| `-O`, `--optimize` | `2` | how far to optimize: `0`, `1`, `2`, `3`, or `s` to favor size |
| `--target` | the host | platform to compile for; `build` only. See [Native code](#native-code) |
| `--run` | | regular expression picking the tests to run by name; `test` only |
| `--parallel` | the number of processors | test files run at once; `test` only |
| `--coverprofile` | | file to write the statements the tests run to, as LCOV; `test` only |
| `-n`, `--no-project` | `false` | ignore project files and compile local files as one project depending only on std |
| `-v`, `--verbose` | `false` | verbose output |
| `-t`, `--threads` | `1` | threads the compiler uses |

### Native code
A program is compiled to native code through LLVM. What zam cannot compile to native code yet is compiled to a trap that ends the program when reached.

A build links the program statically against the runtime and the C libraries the standard library wraps, taken from a target pack: one directory per target holding those libraries, their headers and licenses, and the C library to link against. A binary for Linux runs on any Linux of its architecture; one for macOS runs on macOS 13 or later.

| Target | Platform |
|---|---|
| `aarch64-linux-musl` | Linux on arm64, statically linked against musl |
| `x86_64-linux-musl` | Linux on x86-64, statically linked against musl |
| `aarch64-macos` | macOS on Apple silicon |
| `x86_64-macos` | macOS on Intel |
| `aarch64-windows-gnu` | Windows 10 and later on arm64 |
| `x86_64-windows-gnu` | Windows 10 and later on x86-64 |

Any target is built from any machine zam runs on. `run` and `test` build for the machine they run on. An executable for Windows is named with `.exe`, which is added to `-o` when it is missing.

A target's pack is downloaded from the release of the running version of zam the first time it is needed, checked against the release's checksums, and kept in zam's cache directory, `$ZAM_CACHE_DIR` or the user's cache directory, for every build after. `$ZAM_PACKS` names a directory of packs to use instead, one directory per target, which is how a build works offline and how a development build of zam, which has no release, finds them. `task packs` builds them from source into `bin/packs` in this repository.

With `zig` on the path, the runtime is compiled for the target against its pack the first time it is needed, and kept in zam's cache. Without it, the pack's own prebuilt runtime is used. A project's `native.files` are compiled with `zig cc` for the target, so they need `zig`.

A binary carries no debug information; `zam debug` builds its own.

On Windows, a program behaves as it does elsewhere but where Windows has no equivalent:
- Of the signals, only `SIGINT`, `SIGILL`, `SIGABRT`, `SIGFPE`, `SIGSEGV` and `SIGTERM` exist. Handling or raising any other does nothing, and sending a signal to another process or thread, or masking one, ends the program saying so.
- A file has only a read-only flag, so `FS::Chmod` sets or clears that, and a mode reads back as `0444` or `0666`.
- `FS::Join` joins with a backslash, `FS::Separator`, and `FS::TempDir` is the directory Windows names for temporary files.
- Unix sockets need Windows 10 version 1803 or later.
- TLS and HTTPS trust the certificate authorities in the Windows certificate store.

### Memory
These are baked into the program and take effect every time it runs.

| Flag | Default | Valid | Meaning |
|---|---|---|---|
| `-s`, `--stack-size` | `512KB` | positive | stack a thread starts with; stacks grow as needed |
| `-m`, `--heap-size` | `16MB` | up to `-M` | heap the program starts with |
| `-N`, `--nursery-size` | `8MB` | up to `-M` | bytes allocated since the last collection that trigger a minor collection |
| `-M`, `--max-heap-size` | `64GB` | positive | most memory the heap may ever hold, reserved up front as address space |
| `-g`, `--gc-ratio` | `0.75` | (0, 1] | fraction of the heap in use that triggers a major collection |

### Garbage Collection
Also baked into the program. See [Garbage Collection](../4_memory_and_runtime/1_garbage_collection.md) for what each one tunes.

| Flag | Default | Valid | Meaning |
|---|---|---|---|
| `--gc-markers` | `0` | 0–64 | threads that mark during a major collection; `0` is half the processors, up to 8 |
| `--gc-concurrent` | `true` | bool | mark a major collection the heap's growth asks for alongside the program |
| `--gc-remark-growth` | `1` | > 0 | fraction of its size the heap may grow by while marking alongside the program before the remark is forced |
| `--gc-preclean-passes` | `2` | 0–16 | passes over the cards dirtied while marking alongside the program, before the remark |
| `--gc-return-age` | `3` | ≥ 1 | collections a run of free pages waits before its memory is given back to the system |

## test
`zam test` takes test files, directories holding them, and directories written with a trailing `/...` to include every directory beneath them; the current directory by default.
See [Testing](../1_getting_started/3_testing.md).

## get
`zam get repo@version` adds the dependency to the project file nearest the current directory, under the last element of its repository, with the hash of what was fetched. A dependency already of that name has its version and hash replaced.
Every dependency the project reaches is then fetched and checked, and any hash the project file lacks is recorded.
`zam get` alone does only the second part, which is how a project's dependencies are fetched ahead of a build.

| Flag | Meaning |
|---|---|
| `--as` | package name to add the one dependency under, for a repository whose last element is not a name an import can use |

## clean
`zam clean` removes `$ZAMPATH/pkg`, and the `packs` and `runtime` directories of zam's cache directory: the target packs it downloaded and the runtimes it compiled. Fetched dependencies and packs are kept read-only, so this is the way to remove them; the next build or `zam get` fetches them again.

## Environment
`ZAMPATH` is read by the compiler. The rest are read by the program when it runs, for debugging the collector.

| Variable | Meaning |
|---|---|
| `ZAMPATH` | where fetched dependencies are kept, under `pkg/`; `~/zam` by default |
| `ZAM_GC_VERIFY` | check every collection against a full stop-the-world mark |
| `ZAM_GC_STATS` | write a line to stderr for every collection |
