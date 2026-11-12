# Projects
### Program Files
In addition to code files, Zam uses project files for dependency management.
We'll add a project file to our hello world project.
Place it here in the directory:

```bash
helloworld/
|-zam.yml
|-Main.zam
```

A project files specifies a project name, its version and dependencies.
It is always named `zam.yml` or `zam.yaml`.
A project file has a one-to-many relationship with code files.
Each code file belongs to the closest project file found in its parent directories.
Zam's convention is that the project file and main code file are in the project's root directory.

A program that needs no dependencies or natives can skip the project file with the `-n` flag.
Every code file is then compiled into an implicit project named after the main code file.
We did this when we first ran our hello world code.

For this first project file, we only need the name:
```yaml
# zam.yml
name: "helloworld"
```

## Exported Binary
Like C, the Zam compiler exports a binary.
When debugging, this step can be skipped with the `-r` flag.
We did this when we first ran our hello world code.
You can target a specific location with the `-o` flag.

To build the program to the `helloworld` binary, run the following from just outside the project directory:
```bash
zam -o helloworld helloworld/Main.zam
```

Then, run the binary with:
```
./helloworld
```