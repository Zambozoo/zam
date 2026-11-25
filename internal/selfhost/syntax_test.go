package selfhost_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/base/target"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
)

// root is the repository's root directory.
var root, _ = filepath.Abs(filepath.Join("..", ".."))

// compiler builds the compiler written in zam, src/Main.zam, once for the
// package, and answers its executable.
var compiler = sync.OnceValues(func() (string, error) {
	files := &base.FileProvider{}
	cmd, err := cli.New([]string{"zam", "build", filepath.Join(root, "src", "Main.zam")}, files)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "zam-selfhost-*")
	if err != nil {
		return "", err
	}
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(filepath.Join(dir, "pkg")))
	if len(errs) > 0 {
		return "", errors.Join(errs...)
	}
	if errs := semantic.ValidateAST(ast); len(errs) > 0 {
		return "", errors.Join(errs...)
	}
	host, err := target.Host()
	if err != nil {
		return "", err
	}
	object, err := codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: host.Triple})
	if err != nil {
		return "", err
	}
	out := host.Executable(filepath.Join(dir, "zam"))
	opts := build.Options{Project: ast.MainCodeDecl.File().Project, OutputPath: out, Target: host}
	if err := build.LinkObject(object.Bytes, opts); err != nil {
		return "", err
	}
	return out, nil
})

// TestSyntaxMatchesGo parses every program in the Go parser's testdata with
// the compiler written in zam and with the Go one, without project files, and
// expects the same errors from both.
func TestSyntaxMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	entries, err := filepath.Glob(filepath.Join(root, "internal", "compiler", "0_syntax", "testdata", "*", "*", "Main.zam"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		name, err := filepath.Rel(filepath.Join(root, "internal", "compiler", "0_syntax", "testdata"), filepath.Dir(entry))
		require.NoError(t, err)
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, goErrors(t, entry), zamErrors(t, zam, entry))
		})
	}
}

var (
	// alternatives matches the tokens an error lists as expected.
	alternatives = regexp.MustCompile(`expected one of ((?:"[^"]*", )+or "[^"]*")`)
	// modifiers matches the modifiers an error names, which Go writes as the
	// raw bits of the set and in no fixed order.
	modifiers = regexp.MustCompile(`^(duplicate modifier|modifier) ('[^']*'|"[a-z]+")( cannot be used with ('[^']*'|"[a-z]+"))?`)
	// position matches where an error is located.
	position = regexp.MustCompile(`\[\d+:\d+\]\)$`)
	// atField lists the errors located at the field or import they are about,
	// which Go locates at the token after it.
	atField = []string{
		"duplicate import", "duplicate instance field", "duplicate static field", "tailed field",
		"missing tailed field", "virtual field", "instance member", "instance method", "field \"",
	}
)

// normalized answers message without what the two parsers are meant to write
// differently: the order the tokens an error expects are listed in, how
// modifiers are named, and where an error about a field or import is located.
func normalized(message string) string {
	message = alternatives.ReplaceAllStringFunc(message, func(match string) string {
		list := strings.TrimPrefix(match, "expected one of ")
		tokens := strings.Split(strings.Replace(list, ", or ", ", ", 1), ", ")
		slices.Sort(tokens)
		return "expected one of " + strings.Join(tokens[:len(tokens)-1], ", ") + ", or " + tokens[len(tokens)-1]
	})
	message = modifiers.ReplaceAllString(message, "$1 MODIFIER")
	if strings.Contains(message, "extends the tailed type") || slices.ContainsFunc(atField, func(prefix string) bool {
		return strings.HasPrefix(message, prefix)
	}) {
		message = position.ReplaceAllString(message, ")")
	}
	return message
}

// goErrors answers the errors the Go parser reports for the program entered
// at entry, sorted.
func goErrors(t *testing.T, entry string) []string {
	t.Helper()
	files := &base.FileProvider{}
	cmd, err := cli.New([]string{"zam", "build", "-n", entry}, files)
	require.NoError(t, err)
	_, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	got := []string{}
	for _, err := range errs {
		got = append(got, normalized(err.Error()))
	}
	slices.Sort(got)
	return got
}

// zamErrors answers the errors the compiler at zam reports for the program
// entered at entry, sorted.
func zamErrors(t *testing.T, zam, entry string) []string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(zam, "build", "-n", entry)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "internal", "std"))
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		require.NoError(t, err)
	}
	got := []string{}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if line == "" {
			continue
		}
		message, ok := strings.CutPrefix(line, "[syntax] ")
		require.True(t, ok, fmt.Sprintf("not a syntax error: %s", line))
		// Go reads nothing past a file's declaration.
		if strings.Contains(message, `expected "EOF"`) {
			continue
		}
		got = append(got, normalized(message))
	}
	slices.Sort(got)
	return got
}
