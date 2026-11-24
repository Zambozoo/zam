# Code Files
## Syntax
A syntactically Zam code file contains a list of optional imports followed by the root declaration.
```
IMPORT* DECL
```

## Imports
Imports are scopes defined in project files.
The `std` package is reserved for Zam's standard library.
They may be aliased to prevent naming collisions.
Every import contains a path from the project file directory to the imported location.
Import paths ending in `*` include all code files in specified directory.
If no valid code file is referenced by a path, a compile error occurs.
Imports are defined as follows:
```
IMPORT      = `import` (IMPORT_SPEC | `{` IMPORT_SPEC* `}`)
IMPORT_SPEC = (ALIAS `=`)? (PATH | PACKAGE_NAME `(` PATH `)`) `;`
```

Imports can then be referenced by either their package and file name.
```
import std("data/list/ArrList.zam");
// ...
std.ArrList::New(0);
```

With aliasing, they can be referenced by their alias instead of their package name.
Two imports in one scope that are reached by the same name are a compilation error, including those a glob expands into.
Aliasing one of them resolves the collision.
```
import alias = std("data/list/ArrList.zam");
// ...
alias.ArrList::New(0);
```

Local files can be imported as well by eliding the package name in the import.
```
import "some/local/File.zam";
// ...
File::New();
```

Local imports can also be aliased.
```
import alias = "some/local/File.zam";
// ...
alias.File::New();
```
An alias on a glob names every file it matches, each reached through the alias by its own name.
```
import alias = "some/glob/path/*";
// ...
alias.File::New();
```

Several imports can be grouped under one `import`.
```
import {
    std("data/list/ArrList.zam");
    alias = "some/local/File.zam";
}
```

## Scoped Imports
An import may also be written among the statements of a block.
It is in scope from where it is written to the end of that block, and is not visible to anything written before it.
```
fun void() Run = () {
    import std("data/list/ArrList.zam");
    var ArrList[str]* list = ArrList::[str]New(0);
}
```

A name is resolved innermost outward: the imports of each enclosing block, then each enclosing declaration, then the file's imports.
A scoped import therefore shadows a declaration or file import of the same name.
Within one block, scoped imports follow the same duplicate and aliasing rules as a file's imports.
A grouped import in a block comes into scope at its closing `}`.

## Root Declarations
The root declaration name must match the code file name.
A root declaration is implicitly restricted to its package.
Root delcarations can explicitly be labeled `public` to be accessible outside of their package.
Declarations are discussed in more detail in [1_declarations.md](/docs/2_language_guide/1_declarations.md).
