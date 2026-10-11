package build

import "embed"

// runtimeFS is every C source and header the build driver stitches together
// to produce a per-project binary: the runtime's own sources, the zam/*.h
// headers a project's native C writes against, and the runtime's private
// src/*.h headers each .c file includes. main.c is embedded alongside them
// and is filtered out at use time, since the build driver supplies its own
// main from the generated wrapper.
//
// The embed lives in this package because //go:embed patterns are relative to
// the Go file's directory and may not contain `..`. Keeping the embed next to
// its one consumer also means the FS does not have to leak out of the build
// package.
//
//go:embed runtime/src/*.c runtime/src/*.h runtime/include/zam/*.h
var runtimeFS embed.FS
