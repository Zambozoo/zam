package std_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	"github.com/zambozoo/zam/internal/std"
)

// mock_GitProvider is a no-op git provider. The stdlib's project declares no
// dependencies, so nothing is ever downloaded; a method a parse happens to
// call still has to answer, and panicking from here would mask the test's
// own failure.
type mock_GitProvider struct{}

func (mock_GitProvider) Download(string) (*base.Path, error) { return nil, nil }
func (mock_GitProvider) ArchiveName(string) string           { return "" }

// TestStdFSHasExpectedFiles pins the stdlib's embedded contents to the names
// the compiler reaches for by hand elsewhere (Obj as the implicit base, Objs
// as the override namespace, Err/Pair/StrErr/Strs/System under lang, and the
// io subpackage). A file renamed or removed from the embed would otherwise
// only surface when a program that used one tried to import it.
func TestStdFSHasExpectedFiles(t *testing.T) {
	for _, name := range []string{"lang/Obj", "lang/Objs", "lang/Err", "lang/Pair", "lang/StrErr", "lang/Strs", "lang/System", "io/File"} {
		t.Run(name, func(t *testing.T) {
			f, err := std.FS.Open(name + base.CodeFileExtension)
			require.NoError(t, err, "%s is missing from the embedded stdlib", name)
			_ = f.Close()
		})
	}
}

// TestStdFilesParse runs each stdlib file through the whole syntax parser,
// so a file whose grammar is wrong is caught here rather than at the first
// program to import it. Semantic validation is deliberately not run: a
// stdlib file is not a program and has no main method, which is the one
// thing the semantic pass asks of an entry.
func TestStdFilesParse(t *testing.T) {
	for _, name := range []string{"lang/Err", "lang/Pair", "lang/StrErr", "lang/Strs", "lang/Objs", "lang/System", "io/File"} {
		t.Run(name, func(t *testing.T) {
			cmd := &command.Command{
				InputPath:  base.StdPath(name + base.CodeFileExtension),
				MaxThreads: 1,
			}
			ast, errs := syntax.NewAST(cmd, &base.FileProvider{}, mock_GitProvider{})
			require.Empty(t, errs)
			assert.NotNil(t, ast)
			assert.NotNil(t, ast.MainCodeDecl, "the entry file is parsed as the main decl")
		})
	}
}
