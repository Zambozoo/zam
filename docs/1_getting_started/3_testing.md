# Testing
Tests sit beside the code they test, in test files.
A test file is named after what it tests with `.test.zam` on the end, and its root declaration is a public namespace of the same name with `Test` on the end:
```zam
// list/ArrList.test.zam
import std("test/Assert.zam");
import "ArrList.zam";

public namespace ArrListTest {
    public fun void(uint, str...) AddLast = (argC, argV) {
        var ArrList[int]* l = ArrList::[int]New(0U);
        l.AddLast(1);
        Assert::[uint]Equal(1U, l.Len());
    }
}
```

Every public method of that namespace with `main`'s type is a test.
A test file is in the package of its directory, so it can use what that package keeps to itself.
Test files are compiled only by `zam test`.
Only a test file may import a test file, from its own project or another; a glob import in a test file takes in the test files it matches, and one anywhere else leaves them out.

## Running Tests
```bash
zam test                # the test files in the current directory
zam test list ./map/... # those in list, and in map and every directory beneath it
zam test --run 'ArrListTest\.A' -- extra args
```
`--run` picks tests by a regular expression matched against their names, written `ArrListTest.AddLast`.
The arguments after `--` are passed to every test as its `argC` and `argV`.

All the tests are compiled into one program, which then runs the tests of each file in a process of its own, `--parallel` files at a time.
The output of each file is written whole, in order, followed by a line saying how it went:
```
=== RUN   ArrListTest.AddLast
--- PASS: ArrListTest.AddLast
=== RUN   ArrListTest.Remove
    want 1, got 2
--- FAIL: ArrListTest.Remove
FAIL
FAIL list/ArrList.test.zam 12ms
```
`zam test` exits with status 1 if any test failed.

## Failing
`std/test/Assert.zam` checks a condition, and fails the test running if it does not hold:
* `True(bool, str)` and `False(bool, str)`.
* `Equal[T](want, got)` and `NotEqual[T](unwanted, got)`, by `==`, which compares objects with their `Eq`.
* `Null[T](T*)` and `NotNull[T](T*)`.
* `Fatal(str)`.

Each check reports whether it held, and the test goes on either way.
What a failure writes begins with the file and line in the test that led to it, as in `ArrList.test.zam:12: want 1, got 2`: the first call outside `std/test`.
`std/test/Test.zam` gives the same at a lower level: `Fail(str)` fails the test running and goes on, `Log(str)` writes beneath it, and `Name()` says which it is.

`Fatal` ends the test file's process, as does a panic or any fault that stops a program.
The rest of that file's tests do not run, and its line says how the process ended, but every other file still runs.
A panic or fault also writes a [stack trace](../4_memory_and_runtime/2_stack_traces.md), which names the test it happened in.

## Debugging Tests
A launch of a test file under [the debugger](../4_memory_and_runtime/3_debugging.md) runs its tests as `zam test` would, `test` picking them by name as `--run` does.
A failed check stops it there, with the test's frame among those it stopped in, and the test goes on once continued.
In an editor using the language server, links above each test, and above the test file's namespace, run or debug that test or every test in the file.
