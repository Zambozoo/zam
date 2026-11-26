package selfhost_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
)

// TestSemanticMatchesGo validates every valid and invalid program in the Go
// semantic testdata, and the compiler written in zam itself, with that
// compiler and with the Go one, and expects the same errors, but for the
// programs still pending.
func TestSemanticMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	testdata := filepath.Join(root, "internal", "compiler", "1_semantic", "testdata")
	var entries []string
	for _, category := range []string{"valid", "invalid"} {
		matches, err := filepath.Glob(filepath.Join(testdata, category, "*", "Main.zam"))
		require.NoError(t, err)
		require.NotEmpty(t, matches)
		entries = append(entries, matches...)
	}
	entries = append(entries, filepath.Join(root, "src", "Main.zam"))
	for _, entry := range entries {
		name, err := filepath.Rel(root, filepath.Dir(entry))
		require.NoError(t, err)
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			if reason, ok := pending[filepath.ToSlash(name)]; ok {
				t.Skip(reason)
			}
			t.Parallel()
			assert.Equal(t, goValidateErrors(t, entry, semantic.PassStatic), zamErrors(t, zam, entry, false, "semantic"))
		})
	}
}

// pending is the programs the compiler written in zam is not yet expected to
// validate as Go does, by directory, with why.
var pending = map[string]string{
	"internal/compiler/1_semantic/testdata/invalid/static":    "the static pass is ported in steps",
	"internal/compiler/1_semantic/testdata/invalid/enumbound": "static selection with type arguments is not ported yet",
	"internal/compiler/1_semantic/testdata/valid/std":         "the static pass is ported in steps",
	"src": "the static pass is ported in steps",
}

// goValidateErrors answers the errors Go validation up to last reports for
// the program entered at entry, which has to parse, sorted.
func goValidateErrors(t *testing.T, entry string, last semantic.Pass) []string {
	t.Helper()
	files := &base.FileProvider{}
	cmd, err := cli.New(arguments(entry, false), files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	got := []string{}
	for _, err := range semantic.ValidateUntil(ast, last) {
		got = append(got, err.Error())
	}
	slices.Sort(got)
	return got
}
