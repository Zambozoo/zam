# Modifiers
Modifiers are keywords added to definitions to change their meaning, behavior, or visibility scope.

## Access Modifiers
Access modifiers restrict where a definition can be used.
Only one may be used at a time.
They come in four kinds:
* public, meaning it can be used anywhere.
* private, meaning it can only be used within the same file.
* protected, meaning it can only be used within the same file or within an implementing definition.
Only fields can be protected.
* package-private, which is implicit and requires no keyword, meaning it can only be used within the same package.

## Other Field Modifiers
There are two other kinds of modifiers applicable only to fields.
They are mutually exclusive.
* static, meaning it is tied to the definition itself, not any instance of the type.
By definition, all fields in namespaces are implicitly static.
Fields in other declarations are implicitly non-static, meaning they are tied to any instances of the declaration.
Static fields are initialized before `main` runs, file by file in path order.
Within a file, they are initialized in the order they are written, whichever declaration holds them, so an initializer sees the statics written above it.
Static methods can be named from any initializer.
* virtual, which is only applicable to methods, and specifies that it must be overridden.
This is used when implementing interfaces and extending abstracts.