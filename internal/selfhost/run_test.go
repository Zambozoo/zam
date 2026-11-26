package selfhost_test

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/base/target"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
)

// runPorted is the programs of the codegen corpus the compiler written in zam
// is expected to build into ones that run as Go's builds do.
var runPorted = map[string]bool{"empty": true, "arithmetic": true}

// runResult is what running a program left behind.
type runResult struct {
	Stdout string
	Stderr string
	Code   int
}

// TestRunMatchesGo builds every ported program of the codegen corpus with the
// compiler written in zam and with the Go one, links both with the Go linker,
// and expects both to print the same and exit the same.
func TestRunMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	for _, entry := range codegenEntries(t) {
		name := filepath.Base(filepath.Dir(entry))
		if !runPorted[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, goRun(t, entry), zamRun(t, zam, entry))
		})
	}
}

// TestRunCorpus builds every program of the codegen corpus with Go and expects
// each to run to completion, so that what the comparison runs is known to run.
func TestRunCorpus(t *testing.T) {
	for _, entry := range codegenEntries(t) {
		name := filepath.Base(filepath.Dir(entry))
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := goRun(t, entry)
			assert.Equal(t, 0, got.Code, got.Stderr)
			if name == "hello" {
				assert.Equal(t, "hello, world\n", got.Stdout)
			}
		})
	}
}

// codegenEntries is every program in the codegen corpus.
func codegenEntries(t *testing.T) []string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(root, "internal", "selfhost", "testdata", "codegen", "*", "Main.zam"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	return entries
}

// parsed answers the program entered at entry, parsed and validated by Go, with
// the command it was parsed for.
func parsed(t *testing.T, entry string) (*cli.Command, *syntax.AST) {
	t.Helper()
	files := &base.FileProvider{}
	cmd, err := cli.New(arguments(entry, false), files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	return cmd, ast
}

// goRun builds the program entered at entry with Go and runs it.
func goRun(t *testing.T, entry string) runResult {
	t.Helper()
	cmd, ast := parsed(t, entry)
	host, err := target.Host()
	require.NoError(t, err)
	object, err := codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: host.Triple})
	require.NoError(t, err)
	return linkAndRun(t, object.Bytes, ast)
}

// zamRun builds the program entered at entry with the compiler at zam, links
// the object it writes with the Go linker, and runs it.
func zamRun(t *testing.T, zam, entry string) runResult {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.o")
	var stderr bytes.Buffer
	cmd := exec.Command(zam, arguments(entry, false)[1:]...)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAM_OBJECT="+path)
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	object, err := os.ReadFile(path)
	require.NoError(t, err)
	_, ast := parsed(t, entry)
	return linkAndRun(t, object, ast)
}

// linkAndRun links object, generated for the program ast holds, and runs it
// with no arguments and no input.
func linkAndRun(t *testing.T, object []byte, ast *syntax.AST) runResult {
	t.Helper()
	host, err := target.Host()
	require.NoError(t, err)
	out := host.Executable(filepath.Join(t.TempDir(), "main"))
	require.NoError(t, link(object, ast, out))

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(out)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else {
		require.NoError(t, err)
	}
	return runResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// TestObjectEmits builds the empty program of the codegen corpus with the
// compiler written in zam and expects an object file for the host that carries
// the program's image.
func TestObjectEmits(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)
	entry := filepath.Join(root, "internal", "selfhost", "testdata", "codegen", "empty", "Main.zam")
	path := filepath.Join(t.TempDir(), "main.o")
	var stderr bytes.Buffer
	cmd := exec.Command(zam, arguments(entry, false)[1:]...)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAM_OBJECT="+path)
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())

	object, err := os.ReadFile(path)
	require.NoError(t, err)
	host, err := target.Host()
	require.NoError(t, err)
	switch host.OS {
	case target.MacOS:
		_, err = macho.NewFile(bytes.NewReader(object))
	case target.Linux:
		_, err = elf.NewFile(bytes.NewReader(object))
	}
	require.NoError(t, err)
	assert.Contains(t, string(object), "ZAM_LANG")
}
