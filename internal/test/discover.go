// Package test compiles the tests in test files and runs them.
package test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

const (
	// recursiveSuffix ends a target naming every directory beneath it too.
	recursiveSuffix = "/..."
	// rootSuffix ends the name of a test file's root declaration.
	rootSuffix = "Test"
)

// File is a test file and the tests found in it.
type File struct {
	// Path is the test file.
	Path base.Path
	// Decl is the name of its root declaration.
	Decl string
	// Tests are the names of its test methods, in the order they were written.
	Tests []string
}

// Resolve answers the test files targets name: test files, the test files in
// directories, and those in every directory beneath a directory written with
// a trailing /..., in the order targets name them and once each.
func Resolve(files *base.FileProvider, targets []string) ([]base.Path, error) {
	seen := make(base.Set[base.Path])
	var paths []base.Path
	add := func(found ...base.Path) {
		for _, path := range found {
			if _, ok := seen[path]; !ok {
				seen[path] = struct{}{}
				paths = append(paths, path)
			}
		}
	}

	for _, target := range targets {
		dir, recursive := strings.CutSuffix(filepath.ToSlash(target), recursiveSuffix)
		if dir == "" {
			dir = "."
		}
		path, err := base.ParsePath(dir)
		if err != nil {
			return nil, err
		}

		info, err := os.Stat(string(path))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("%s does not exist", target)
		case err != nil:
			return nil, err
		case info.IsDir():
			found, err := files.ListTestFiles(path, recursive)
			if err != nil {
				return nil, err
			}
			add(found...)
		case recursive || !strings.HasSuffix(string(path), base.TestFileSuffix):
			return nil, fmt.Errorf("%s is not a directory or a %s file", target, base.TestFileSuffix)
		default:
			add(path)
		}
	}

	return paths, nil
}

// Discover answers the tests of each of paths in ast: the public static methods
// of its root declaration that have a main method's type, and whose names,
// qualified by the declaration's, pattern matches. A file with none is left
// out. The root declaration of X.test.zam must be public and named XTest, so
// that a runner in any package can call its tests.
func Discover(ast *syntax.AST, paths []base.Path, pattern *regexp.Regexp) ([]File, error) {
	mainType := syntax.MainMethodType()

	var files []File
	for _, path := range paths {
		codeFile, i := ast.CodeFiles.Get(path)
		if i < 0 {
			return nil, fmt.Errorf("%s was not parsed", path)
		}

		decl := codeFile.Decl
		want := strings.TrimSuffix(path.Base(), base.TestFileSuffix) + rootSuffix
		if decl.Name() != want {
			return nil, base.NewError(fmt.Errorf("the root declaration of a test file %s must be named %s, not %s",
				path.Base(), want, decl.Name()), codeFile.Location(decl.Start()))
		}
		if !decl.Modifiers().Has(syntax.ModifierPublic) {
			return nil, base.NewError(fmt.Errorf("the root declaration of a test file %s must be public", path.Base()),
				codeFile.Location(decl.Start()))
		}

		file := File{Path: path, Decl: decl.Name()}
		for name, field := range decl.StaticFields() {
			method, ok := field.(*syntax.FieldMethod)
			if !ok || !method.Modifiers().Has(syntax.ModifierPublic) || !method.FieldType().Equals(mainType) {
				continue
			}
			if pattern.MatchString(decl.Name() + "." + name) {
				file.Tests = append(file.Tests, name)
			}
		}
		if len(file.Tests) > 0 {
			files = append(files, file)
		}
	}
	return files, nil
}
