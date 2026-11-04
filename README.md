# Zam Programming Language
![Zam Logo](assets/logo_banner.png)
Zam is an imperative, multi-pass language featuring imperative interfaces, advanced generics, user-defined GC hooks, and inline struct-hack types.

```
import std("io/File.zam");
namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr("Hello World!\n");
    }
}
```

## Getting Started
The best place to start is the [documentation](docs).

## TODO
Zam is far from complete.
* Validate package inclusion
* Implement a testing framework
* Implement debugging
* Export LLVM instead of bytecode
* Bootstrap the language