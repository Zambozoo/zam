# Statements

## Import
An `import` may be written as a statement in a block, scoping it to the rest of that block.
See [0_files_and_imports.md](/docs/2_language_guide/0_files_and_imports.md#scoped-imports).

## Defer
`defer` runs a statement when control leaves the block it is written in, however it leaves: by reaching the end, by `return`, or by a `break` or `continue` out of it.
```
var int* buffer = new(int, 64);
defer free(buffer);
if(Main::fill(buffer) == 0) {
    return; // buffer is freed here
}
Main::use(buffer);
// and here
```

Statements deferred in one block run in the reverse of the order their `defer`s were reached, and only those that were reached run. One in a loop's body runs at the end of every iteration.

A deferred statement reads its variables as they are when it runs, not as they were at the `defer`. To keep a value from then, copy it into a variable first. Names mean what they meant at the `defer`: a variable declared after it cannot be named in it, even one that shadows a name it uses.

A `return` works out its value before the deferred statements run, so they cannot change it except through a pointer.

A deferred statement cannot `return`, or `break` or `continue` out of itself. It cannot be a declaration or another `defer` either, though a block it runs may hold both, along with loops that it breaks out of.

Deferred statements do not run when the program ends by `System::Exit`. A panic runs them, as below.

## Panic and recover
`System::Panic` panics, as does what the runtime catches a program doing wrong: dividing by zero, using a null pointer, string, `any` or thread, unboxing an `any` as a type it does not hold, asking `new` for a negative or impossibly large count, freeing what `new` did not allocate, a pointer into the middle of what it did, or what was already freed, and reading or writing memory the program does not have. The thread unwinds: it runs the deferred statements of every block it is inside of, innermost first, out through the methods that called it. Once none are left, the program writes the panic's message and where it was raised, and ends.

`System::Recover`, called by a deferred statement a panic runs, stops the panic and answers its message. The rest of that block's deferred statements still run, and execution goes on past the block as though it had reached its end. A method whose outermost block recovers answers the zero value of its result.
```
var str failure;
{
    defer failure = System::Recover();
    Main::parse(input);
}
if(failure != null) {
    // parse panicked
}
```

Anywhere else `System::Recover` answers `null`, in a method a deferred statement calls as much as outside a panic. A panic raised by a deferred statement as it runs replaces the one it was running for.

A panic a native raises unwinds out of it like any other. A panic does not unwind out of what the runtime calls on a program's behalf to collect or deliver: a `Mark`, a `Delete`, a signal handler or a function a native calls back. It ends the program there. An `Eq`, `Cmp`, `Hash` or `Str` the runtime calls unwinds as any method does. An invalid memory access nothing recovers ends the process by the signal it raised, once the deferred statements have run. A call nested too deeply for its thread's stack panics with `stack overflow` before it runs, which recovers like any other panic.
