package semantic

import (
	"embed"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

//go:embed testdata
var testdata embed.FS

// cases answers the case directories under dir in the embedded testdata.
func cases(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := fs.ReadDir(testdata, path.Join("testdata", dir))
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	require.NotEmpty(t, names)
	return names
}

// parse parses the program whose entry is the Main.zam of the case dir, copied
// out of the embedded testdata with the project file beside it.
func parse(t *testing.T, dir string) (*syntax.AST, []error) {
	t.Helper()
	sub, err := fs.Sub(testdata, path.Join("testdata", dir))
	require.NoError(t, err)
	root := t.TempDir()
	caseDir := filepath.Join(root, path.Base(dir))
	require.NoError(t, os.CopyFS(caseDir, sub))
	project, err := fs.ReadFile(testdata, path.Join("testdata", path.Dir(dir), "zam.yml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "zam.yml"), project, 0o600))
	entry, err := base.ParsePath(filepath.Join(caseDir, "Main.zam"))
	require.NoError(t, err)

	return syntax.NewAST(&cli.Command{InputPath: entry, MaxThreads: 4}, &base.FileProvider{}, nil)
}

// validate answers what validating the program of the case dir found. The
// program has to parse, so that what fails is only ever validation.
func validate(t *testing.T, dir string) []error {
	t.Helper()
	ast, errs := parse(t, dir)
	require.Empty(t, errs)
	return ValidateAST(ast)
}

// Every valid program validates.
func TestValidateASTValid(t *testing.T) {
	t.Parallel()
	for _, name := range cases(t, "valid") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, validate(t, path.Join("valid", name)))
		})
	}
}

// Every invalid program fails to validate.
func TestValidateASTInvalid(t *testing.T) {
	t.Parallel()
	for _, name := range cases(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.NotEmpty(t, validate(t, path.Join("invalid", name)))
		})
	}
}

// A program the parse recovered from is still validated, as the language
// server does, without validation failing on what the parse left broken.
func TestValidateASTBroken(t *testing.T) {
	t.Parallel()
	for _, name := range cases(t, "broken") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ast, errs := parse(t, path.Join("broken", name))
			require.NotEmpty(t, errs)
			require.NotNil(t, ast)
			assert.NotPanics(t, func() { ValidateAST(ast) })
		})
	}
}

// A type argument for a generic bounded to enums must be an enum.
func TestValidateASTEnumBound(t *testing.T) {
	t.Parallel()
	errs := validate(t, path.Join("invalid", "enumbound"))
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "type argument int for E is not an enum")
}

// A name three imports declare is ambiguous between the first two of their
// files in path order, whatever order they are imported in.
func TestValidateASTAmbiguous(t *testing.T) {
	t.Parallel()
	errs := validate(t, path.Join("invalid", "ambiguous"))
	require.Len(t, errs, 1)
	assert.Regexp(t, `^declaration "Twin" is ambiguous between \S*A\.zam and \S*B\.zam; alias one of their imports`, errs[0].Error())
}

// A generic static method that calls itself with a wrapped argument never
// stops instantiating; one passing its parameter on unwrapped does.
func TestValidateASTMethodCycle(t *testing.T) {
	t.Parallel()
	errs := validate(t, path.Join("invalid", "methodcycle"))
	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], `recursive generic expansion detected at "Grow"`)
}

// A declaration that holds itself by value is reported at the declaration whose
// member closes the cycle, a generic one with its arguments substituted.
func TestValidateASTMemberCycle(t *testing.T) {
	t.Parallel()
	errs := validate(t, path.Join("invalid", "membercycle"))
	got := make([]string, len(errs))
	for i, err := range errs {
		got[i], _, _ = strings.Cut(err.Error(), " (")
	}
	assert.Equal(t, []string{
		"member cycle detected: B.{a}",
		"member cycle detected: Self.{selves}",
		"member cycle detected: Box.{held}",
	}, got)
}

// A tailed class or abstract is named with its tail wherever the program names
// it, so its receiver and this are pointers to the tailed type too, as a tailed
// struct's are, rather than to a bare form the program cannot write.
func TestValidateASTTailedReceiver(t *testing.T) {
	t.Parallel()
	ast, errs := parse(t, path.Join("valid", "tailedclass"))
	require.Empty(t, errs)
	require.Empty(t, ValidateAST(ast))
	var pointers []string
	for p := range ast.PointerTypes.Values() {
		pointers = append(pointers, p.String())
	}
	assert.Contains(t, pointers, "Run~*")
	assert.Contains(t, pointers, "Span~*")
	assert.NotContains(t, pointers, "Run*")
	assert.NotContains(t, pointers, "Span*")
}
