# Hello World
## Project
Every Zam project is composed of two kinds of files, project files and code files.
This first project will have two files, with the following directory structure:

```bash
helloworld/
|-zam.yml
|-Main.zam
```

### Program Files
A project files specifies a project name, its version and dependencies.
It is always named `zam.yml` or `zam.yaml`.
A project file has a one-to-many relationship with code files.
Each code file belongs to the closest project file found in its parent directories.
Zam's convention is that the project file and main code file are in the project's root directory.

For this first project file, we only need the name:
```yaml
# zam.yml
name: "helloworld"
```

## Code Files
Code files contain the program structures and logic.
They always end in `.zam`, and the name of the file must match the name of the root declaration.
They contain imports and a root declaration, further discussed in the [language guide](../2_language_guide/).
Zam's convention is that they are `PascalCase`.

Let's step through the first code file.

It'll start with an import, as we need to print to `stdout`.
To access it, we'll import the standard library `std/io/local/File.zam` file.
```go
// Main.zam
import std("io/local/File.zam");
```

Next, we need the root declaration.
Since we don't need any objects, we can use a `namespace`, which is just a scoped group of `static` fields.
We name is `Main` to match the name of the code file, `Main.zam`.
```go
// Main.zam
import std("io/File.zam");

namespace Main {}
```

Now let's populate the `namespace`.
We'll add in the `main` function, which is our program's entry point, and always has the name `main` and type `void(uint, str...)`.
Functions are defined syntactically by `fun <TYPE> <NAME> = (<PARAMETER_IDS>) {<BODY>}`.
The `main` function must always be static, which is implicitly given by the `namespace` declaration.
We aren't using the parameters, so we've set them to be `_`, but they are special.
The `uint` parameter is `argC`, or the number of arguments passed into the cli command.
Meanwhile, the `str...` parameter is the variadic `argV`, or the actual array of elements passed in.
The `str...` type decays to `str*` inside a method.
```go
// Main.zam
import std("io/local/File.zam");

namespace Main {
    fun void(uint, str...) main = (_, _) {}
}
```

Lastly, let's print!
First, we'll statically reference `std/io/File.zam` with a static type expression, `type(File)`.
Then, we'll grab the `stdout` file from it, `type(File).StdOut`.
Next, we'll call its `WriteStr` method with the `"Hello World!"` string argument, `type(File).StdOut.WriteStr("Hello World!\n")`.
Lastly, we end the statement with a semicolon.
```go
// Main.zam
import std("io/local/File.zam");

namespace Main {
    fun void(uint, str...) main = (_, _) {
        type(File).StdOut.WriteStr("Hello World!\n");
    }
}
```

## Running
To build the program, run the following from just outside the project directory:
```bash
zam -o helloworld helloworld/Main.zam
./helloworld
```