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

A project file specifies a project name, its version and dependencies.
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

### Dependencies
A dependency is another project, distributed as source in a git repository.
It is added with `zam get`, run from inside the project:
```bash
zam get github.com/someone/list@v1.2.0
```
Each one is listed under the package name it is imported by, which is the last element of its repository unless `--as` names another:
```yaml
dependencies:
  list:
    repo: github.com/someone/list@v1.2.0
  json:
    repo: gitlab.com/group/sub/json@3f9c2ab1d0e4c5b6a7f8091a2b3c4d5e6f708192
```

`repo` is the repository's path followed by `@` and a version: a tag, a branch, or a full commit hash.
It is fetched from `https://<path>.git`, so any git host works, and a private repository uses git's own credentials.
Its files are then imported by package name:
```
import std("data/list/ArrList.zam");
import list("List.zam");
```

The first build to use a version fetches it and keeps it under the directory the `ZAMPATH` environment variable names, `~/zam` by default:
```
$ZAMPATH/pkg/github.com/someone/list@v1.2.0.tar.zst
$ZAMPATH/pkg/github.com/someone/list@v1.2.0/
```
The archive is the version as it was fetched. The directory beside it is that archive extracted, read-only, and is what is compiled; a missing one is extracted again.
Later builds use what is kept rather than fetching again. `zam clean` removes everything kept.

`zam get` and the compiler record a hash of each dependency's files in the project file the first time they fetch it:
```yaml
dependencies:
  list:
    repo: github.com/someone/list@v1.2.0
    hash: h1:2Yf3k0...
```
Every build after checks the kept archive against it and fails if they differ, as they would if the tag were moved or the archive changed.
Commit the hash with the project file.
A dependency's own project file is never written, so its dependencies are checked only if its author recorded their hashes.

A dependency's files can import each other, but nothing outside the directory it was extracted to.
Two dependencies may ask for different versions of the same repository; each gets the version it asked for, and their declarations are distinct.

## Exported Binary
Like C, the Zam compiler exports a binary.
`zam run` builds it to a temporary location and runs it, which is what we did when we first ran our hello world code.
`zam build` keeps it instead, and the `-o` flag says where.

To build the program to the `helloworld` binary, run the following from just outside the project directory:
```bash
zam build -o helloworld helloworld/Main.zam
```

Then, run the binary with:
```
./helloworld
```