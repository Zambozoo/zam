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
`import` (ALIAS `=`)? (PATH | PACKAGE_NAME `(` PATH `)`) `;`
```

Imports can then be referenced by either their package and file name.
```
import std("data/list/ArrList.zam");
// ...
std.ArrList::New(0);
```

With aliasing, they can be referenced by their alias instead of their package name.
This is to avoid any import ambiguity, which would lead to a compilation error.
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
Glob imports cannot be aliased.
```
import alias = "some/glob/path/*"; // <-- Error!
```

## Root Declarations
The root declaration name must match the code file name.
A root declaration is implicitly restricted to its package.
Root delcarations can explicitly be labeled `public` to be accessible outside of their package.
Declarations are discussed in more detail in [1_declarations.md](/docs/2_language_guide/1_declarations.md).
