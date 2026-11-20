package build_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
)

// TestRun runs a program that exits with a status chosen by its arguments, so
// both the arguments reaching it and its status coming back are observed.
func TestRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"run\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(`import std("lang/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        if (argC == 2U && argV[1] == "b") {
            System::Exit(42);
        }
        System::Exit(1);
    }
}
`), 0o600))

	files := &base.FileProvider{}
	cmd, err := command.New([]string{"zam", "run", entry, "a", "b"}, files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	opts := build.Options{Project: ast.MainCodeDecl.File().Project}
	if !testTarget().IsHost() {
		t.Skip("zam run runs programs for the host")
	}
	object, err := codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: testTarget().Triple})
	require.NoError(t, err)
	opts.RuntimeCacheDir, err = nativeRuntimeCache()
	require.NoError(t, err)
	run := func(args []string) (int, error) { return build.Run(object.Bytes, opts, args) }

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "arguments reach the program", args: cmd.RunArgs, want: 42},
		{name: "no arguments", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, err := run(tt.args)
			require.NoError(t, err)
			assert.Equal(t, tt.want, exitStatus(code))
		})
	}
}
