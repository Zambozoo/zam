## Debugging
`zam debug` serves the [Debug Adapter Protocol](https://microsoft.github.io/debug-adapter-protocol/) over stdin and stdout, which is how an editor runs a program under the debugger. It compiles the program it is asked to launch to native code with debug information, unoptimized, and debugs it through [lldb-dap](https://lldb.llvm.org/resources/lldbdap.html), which ships with the Xcode Command Line Tools and with LLVM. `$ZAM_LLDB_DAP` names a different one.

A launch takes:

| Attribute | Meaning |
|---|---|
| `program` | the program's entry file, or a test file to debug its tests |
| `args` | arguments the program is run with |
| `cwd` | directory the program is run in |
| `stopOnEntry` | stop where `main` begins |
| `console` | `integratedTerminal` to run the program in the editor's terminal, where it reads what is typed; `internalConsole`, the default, to write its output to the debug console |
| `test` | for a test file, a regular expression picking the tests to run by name, as `zam test --run` does |

The VS Code extension in `editors/vscode` starts `zam debug` for a launch configuration of type `zam`:
```json
{
    "type": "zam",
    "request": "launch",
    "name": "Debug Zam program",
    "program": "${file}"
}
```

### What it does
Everything lldb does, it does: breakpoints by line, with conditions, hit counts and log messages; stepping; pausing; threads; watches and the debug console, which evaluate lldb's expressions. Functions are named as zam names them, as in `Main.main` or `Main.main.lambda1`, and so are the frames of a backtrace.

Variables show as zam values:
- A primitive shows its value, an enum its value's name.
- A class, abstract, struct or tuple expands into its members, a tuple's named `0`, `1` and so on; a pointer to one expands into what it points to.
- A `str` shows its text, up to its first 256 bytes.
- An `any` shows the type of what it holds and expands into it.
- A function shows the function it runs and how many values it captured.

A frame in the standard library shows the library's source.

### Limitations
- A failure ends the program as it would have, without stopping it first. A breakpoint on `zamrt_panic`, or on `stdSystemFailed` for `System::Panic` and failed checks, stops it there instead.
- Restart runs the program as it was built when it was launched, not as the source is then.
- `int` and `uint` show as `long` and `unsigned long`, which is how lldb names their kinds of integer.
- Expressions are lldb's, which reads a variable's members as `box.tag` but knows nothing of zam's statics, as `Main::counter`.
- A binary built by `zam build` carries no debug information.
