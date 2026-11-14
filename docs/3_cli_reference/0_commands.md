# Commands
`zam <command> [flags] [arguments]`

| Command | Arguments | Meaning |
|---|---|---|
| `build` | `<input file>` | compile a program to a binary |
| `run` | `<input file> [program arguments]` | compile a program to a temporary binary and run it, passing on the arguments after its file |
| `test` | `[path...] [-- test arguments]` | compile the tests in test files and run each file's in a process of its own |
| `get` | `[repo@version...]` | add dependencies to the nearest project file, then fetch every dependency the project reaches |
| `clean` | | remove every fetched dependency |
| `lsp` | | serve the language server protocol over stdin and stdout |

`zam help` lists the commands, and `zam <command> -h` lists a command's flags.

## build, run and test
Flags come before the input file, or the paths `test` is given. Sizes take an optional `B`, `KB`, `MB` or `GB` suffix, and a bare number is a count of bytes.

### Output
| Flag | Default | Meaning |
|---|---|---|
| `-o`, `--output` | `out` | file to write; `build` only |
| `-E`, `--emit-image` | `false` | write the raw bytecode image instead of linking a binary; `build` only |
| `-k`, `--keep-build` | `false` | keep the temporary build directory and print its path |
| `--run` | | regular expression picking the tests to run by name; `test` only |
| `--parallel` | the number of processors | test files run at once; `test` only |
| `-n`, `--no-project` | `false` | ignore project files and compile local files as one project depending only on std |
| `-v`, `--verbose` | `false` | verbose output |
| `-t`, `--threads` | `1` | threads the compiler uses |

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
`zam clean` removes `$ZAMPATH/pkg`. Fetched dependencies are kept read-only, so this is the way to remove them; the next build or `zam get` fetches them again.

## Environment
`ZAMPATH` is read by the compiler. The rest are read by the program when it runs, for debugging the collector.

| Variable | Meaning |
|---|---|
| `ZAMPATH` | where fetched dependencies are kept, under `pkg/`; `~/zam` by default |
| `ZAM_GC_VERIFY` | check every collection against a full stop-the-world mark |
| `ZAM_GC_STATS` | write a line to stderr for every collection |
