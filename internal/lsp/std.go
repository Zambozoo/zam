package lsp

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/std"
)

// stdRootOf returns the root of the standard library's source when a file on
// disk is part of a copy of it, such as the compiler's own. The copy is
// recognized by its project file, the nearest one above the file, being the
// one compiled in.
func stdRootOf(files *base.FileProvider, path base.Path) (string, bool) {
	if base.IsStdPath(path) {
		return "", false
	}
	projectFile, err := files.ProjectFilePath(path)
	if err != nil {
		return "", false
	}
	got, err := os.ReadFile(string(projectFile))
	if err != nil {
		return "", false
	}
	want, err := std.FS.ReadFile(filepath.Base(string(projectFile)))
	if err != nil || !bytes.Equal(got, want) {
		return "", false
	}
	return filepath.Dir(string(projectFile)), true
}

// stdPathOf returns the standard library path of a file under root.
func stdPathOf(root string, path base.Path) (base.Path, bool) {
	rel, err := filepath.Rel(root, string(path))
	if err != nil {
		return "", false
	}
	return base.StdPath(filepath.ToSlash(rel)), true
}
