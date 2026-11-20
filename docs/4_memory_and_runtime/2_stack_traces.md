## Stack Traces
A program that fails says where it was. The runtime's own failures, such as dereferencing a null pointer or dividing by zero, and `System::Panic` both write their message, once nothing [recovers](/docs/2_language_guide/4_statements.md#panic-and-recover) them, followed by a line for each call the failing thread was inside of, innermost first:
```
dereferenced a null pointer
	at Main.Inner (/p/Main.zam:9)
	at Main.Outer (/p/Main.zam:13)
	at Main.main (/p/Main.zam:20)
```
A function is named by the declarations it lies in and its own name. A lambda is named after the function it was written in, with `.lambda` on the end, and the code that sets up statics is `<static init>`. A file is named by the path it was compiled from, or `std://` for the standard library.

A read or write through a pointer that is not null but points nowhere, such as far past the end of an array, panics too. Once nothing recovers it, it says the address it touched and writes the same trace, then ends the process by the signal it raised:
```
invalid memory access at 0x10000000000
	at Main.Read (/p/Main.zam:3)
	at Main.main (/p/Main.zam:8)
```

A thread started by `async` is traced back to the call it was started on. A trace longer than 64 calls says how many more there were instead of writing them.

`System::Trace()` writes the calling thread's trace to stderr without stopping it, and `System::Caller(skip)` answers the file and line `skip` calls out from the method calling it.

### How
Every function keeps a frame the runtime can walk, linked to its caller's, which names the function and holds the line it last reached. The compiler writes that line as each statement begins, so a frame's line is the statement it is in. The function's name and file come from a table the compiler emits beside the program.

The runtime walks a thread's frames from the innermost outward and writes a line for each.

### Limitations
* A line is a statement's: an expression spread over several lines is placed at the line the statement begins on.
* An access past the end of an array that lands in memory the process owns is not caught at all.
* A program that handles `SIGSEGV` or `SIGBUS` itself through `Signal` replaces the trace.
* An invalid memory access whose result is never used may be optimized away, and so never caught.
* After recovering an invalid memory access, a number the failing function stored into the heap just before it may be lost.
