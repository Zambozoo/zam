# Statements

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

Deferred statements do not run when the program ends by `System::Exit` or a panic.
