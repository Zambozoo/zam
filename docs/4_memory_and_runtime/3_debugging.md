## Debugging
`zam debug` serves the [Debug Adapter Protocol](https://microsoft.github.io/debug-adapter-protocol/) over stdin and stdout, which is how an editor runs a program under the debugger. It compiles the program it is asked to launch, runs it held before its first instruction while the editor sets its breakpoints, and then lets it go.

A launch takes:

| Attribute | Meaning |
|---|---|
| `program` | the program's entry file, or a test file to debug its tests |
| `args` | arguments the program is run with |
| `cwd` | directory the program is run in |
| `stopOnEntry` | stop where `main` begins |
| `console` | `integratedTerminal` to run the program in the editor's terminal, where it reads what is typed; `internalConsole`, the default, to write its output to the debug console with no input |
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
- A breakpoint stops the program before the first instruction of its line. A line with no code moves to the next line that has some.
- Step over runs to the next line of the same call, step in to the next line or into the next call made, and step out to the caller.
- A breakpoint may have a condition, a hit count, or a log message.
  - A condition is a path, true when its value is true, nonzero or not null, or a path compared with `==`, `!=`, `<`, `<=`, `>` or `>=` to another path or to a number, a quoted string, `true`, `false` or `null`. Numbers and strings are ordered; anything else is only equal or not. A condition that cannot be evaluated stops the program and says why.
  - A hit count is a number of hits, stopping on that one, or one after `==`, `>`, `>=`, `<`, `<=`, or `%` for every that many.
  - A log message is written, with each `{path}` in it replaced by its value, instead of stopping.
- Pause stops the program wherever it is.
- Restart ends the program and runs it again, built from the source as it is then, with every breakpoint placed in it again. Running without debugging runs it with nothing stopping it.
- Every stop stops every thread. A thread waiting in a blocking call, such as a read or a sleep, is shown where it called it, and stops before running anything once the call returns.
- Each frame shows its variables: its parameters, the variables declared so far that are still in scope, a lambda's captures, and `this`. A struct, class, tuple or fixed array expands into its members, and a pointer into what it points to, read as the class the instance actually is. A string shows up to its first 1 KiB. Inside a generic class's or abstract's methods, they are read as the instantiation the receiver is.
- A Globals scope beside each frame's variables shows the program's own static members, by declaration and name, as in `Main.counter`. The standard library's are left out.
- A posit shows as the fewest decimal digits that read back as it, or `NaR`.
- A bool, byte, char, int, uint, flt, pst or str can be set while the program is stopped. A pst is written as a quadruple-precision float, which is rounded to the nearest posit, or as `NaR`: an infinity or a NaN has no posit, and is refused. A byte or char may be written quoted, and a str is written quoted, with the escapes Go allows, or as `null`; the string is allocated on the thread whose variable it is.
- A failure stops the program where it failed, with its stack and variables there to look at: a runtime error such as a null dereference or a division by zero, an invalid memory access, or `System::Panic`. A failed check in a test, through `Test::Fail` or an `Assert`, is a failure too, though the test goes on once continued. What it failed with is shown as the exception. Continuing lets it end as it would have. The Failures exception breakpoint, on by default, turns this off.
- Watches, hovers and the debug console evaluate a variable's name, or a static's, as `Main::counter`, `Main.counter`, or alone inside its declaration, followed by any of `.member`, `.0` for a tuple's element, and `[index]`, as in `node.next.name` or `argV[1]`. Indexing a pointer or an array of unknown length reads the element that far past it.
- What the program writes to stdout and stderr is shown as output, and its stdin is empty, unless it runs in the editor's terminal. There it reads and writes as it would run on its own; it connects back to the debugger over a loopback port with a token only that launch was given.

### Limitations
- Expressions other than those paths, such as arithmetic or calls, cannot be evaluated, and pointers and other references but strings cannot be set.
- An enum's members and an array of unknown length show without their values.
- No collection runs while the program is stopped.
- A failure raised while the runtime holds a lock of its own, as one deep in a native might be, does not stop the program, which would keep every other thread from stopping. It says so, and fails as it would have.
- A binary built by `zam build` cannot be debugged. `zam debug` builds its own, with a check before every instruction that a program built otherwise does not pay for.
