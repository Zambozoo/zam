package build_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
)

// syntaxDump is the binary src/syntax/dump compiles to, built once for the
// package.
var syntaxDump = sync.OnceValues(func() (string, error) {
	entry, err := filepath.Abs(filepath.Join("..", "..", "..", "src", "syntax", "dump", "Main.zam"))
	if err != nil {
		return "", err
	}
	return compileEntry(entry)
})

// compileEntry builds the program entry starts, in the project it belongs to.
func compileEntry(entry string) (string, error) {
	dir, err := os.MkdirTemp("", "zam-selfhost-")
	if err != nil {
		return "", err
	}
	out := testTarget().Executable(filepath.Join(dir, filepath.Base(filepath.Dir(entry))))
	files := &base.FileProvider{}
	cmd, err := cli.New([]string{"zam", "build", "-o", out, entry}, files)
	if err != nil {
		return "", err
	}
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(filepath.Join(dir, "packages")))
	if len(errs) > 0 {
		return "", fmt.Errorf("parsing %s: %v", entry, errs)
	}
	if errs := semantic.ValidateAST(ast); len(errs) > 0 {
		return "", fmt.Errorf("validating %s: %v", entry, errs)
	}
	object, err := codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: testTarget().Triple})
	if err != nil {
		return "", err
	}
	cache, err := nativeRuntimeCache()
	if err != nil {
		return "", err
	}
	return out, build.LinkObject(object.Bytes, build.Options{
		OutputPath:      out,
		Project:         ast.MainCodeDecl.File().Project,
		RuntimeCacheDir: cache,
		Target:          testTarget(),
	})
}

// TestSelfHostTokenKinds checks that the Zam token kinds are the Go ones, in
// the same order, with the same text and group.
func TestSelfHostTokenKinds(t *testing.T) {
	t.Parallel()
	binary, err := syntaxDump()
	require.NoError(t, err)

	var want strings.Builder
	for k := syntax.TTNone; k <= syntax.TTEOF; k++ {
		group := "other"
		if _, ok := syntax.Symbols()[k.String()]; ok {
			group = "symbol"
		} else if _, ok := syntax.Keywords()[k.String()]; ok {
			group = "keyword"
		}
		fmt.Fprintf(&want, "%s %s\n", k, group)
	}

	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, want.String(), stdout)
}
