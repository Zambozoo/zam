# Compiler Flags
`zam [flags] <input file> [program arguments]`

Flags come before the input file. Sizes take an optional `B`, `KB`, `MB` or `GB` suffix, and a bare number is a count of bytes.

## Output
| Flag | Default | Meaning |
|---|---|---|
| `-o`, `--output` | `out` | file to write |
| `-E`, `--emit-image` | `false` | write the raw bytecode image instead of linking a binary |
| `-r`, `--run` | `false` | build to a temporary binary and run it, passing on the arguments after the input file; not with `-E` |
| `-k`, `--keep-build` | `false` | keep the temporary build directory and print its path |
| `-n`, `--no-project` | `false` | ignore project files and compile local files as one project depending only on std |
| `-v`, `--verbose` | `false` | verbose output |
| `-t`, `--threads` | `1` | threads the compiler uses |

## Memory
These are baked into the program and take effect every time it runs.

| Flag | Default | Valid | Meaning |
|---|---|---|---|
| `-s`, `--stack-size` | `512KB` | positive | stack a thread starts with; stacks grow as needed |
| `-m`, `--heap-size` | `16MB` | up to `-M` | heap the program starts with |
| `-N`, `--nursery-size` | `8MB` | up to `-M` | bytes allocated since the last collection that trigger a minor collection |
| `-M`, `--max-heap-size` | `64GB` | positive | most memory the heap may ever hold, reserved up front as address space |
| `-g`, `--gc-ratio` | `0.75` | (0, 1] | fraction of the heap in use that triggers a major collection |

## Garbage Collection
Also baked into the program. See [Garbage Collection](../4_memory_and_runtime/1_garbage_collection.md) for what each one tunes.

| Flag | Default | Valid | Meaning |
|---|---|---|---|
| `--gc-markers` | `0` | 0–64 | threads that mark during a major collection; `0` is half the processors, up to 8 |
| `--gc-concurrent` | `true` | bool | mark a major collection the heap's growth asks for alongside the program |
| `--gc-remark-growth` | `1` | > 0 | fraction of its size the heap may grow by while marking alongside the program before the remark is forced |
| `--gc-preclean-passes` | `2` | 0–16 | passes over the cards dirtied while marking alongside the program, before the remark |
| `--gc-return-age` | `3` | ≥ 1 | collections a run of free pages waits before its memory is given back to the system |

## Environment
These are read by the program when it runs, for debugging the collector.

| Variable | Meaning |
|---|---|
| `ZAM_GC_VERIFY` | check every collection against a full stop-the-world mark |
| `ZAM_GC_STATS` | write a line to stderr for every collection |
