
## Compilation Steps
### Syntax
1. Tokenize -> AST
    - Tokenizes each file and constructs the AST. Follows import links to discover other files.
### Semantic
1. Resolve Reference Types
    - Links reference types to AST declarations.
1. Detect Generic Cycles
    * Detects recursive generic declarations.
1. Detect Inheritance Cycles
    * Detects recursive child-parent declaration relationships.
1. Detect Member Cycles
    * Detects recursive member field relationships.
1. Static Scope and Type Checking
    * Performs static analysis on variable  and label scopes as well as running type checking.
1. Generic Type Morphization
    * Iterates through all possible parent, member, and variable types to generate all reachable generic type declarations.
1. Generic Arg Type Enumeration
    * Iterates and numerates all reachable generic argument containing types for runtime dynamic dispatch.
1. Thread and Function Type Enumeration
    * Iterates and numerates all reachable thread and function types.
1. Collect Interfaces
    * Populates dispatch interface tables.
### Code Generation
1. Type Definition Enumeration
    * Enumerates and generates all exported type definitions stubs.
1. String Literal Enumeration
    * Enumerates and populates string literals.
1. Static Field Enumeration and Type Definition Resolution
    * Computes static field offsets for bytecode mapping and populates type definition type references.
1. Operation Generation
    * Generates a list of executable bytecode.
### Build
1. Build Executable
    * Bundles the executable bytecode and any provided native C code into a C program that is shelled out to `cc` to produce a single executable.