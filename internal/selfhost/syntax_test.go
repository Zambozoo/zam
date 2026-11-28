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
	"go.yaml.in/yaml/v3"

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
	if err := link(object.Bytes, ast, out); err != nil {
		return "", err
	}
	if os.Getenv("ZAM_SELFHOST_STAGE") != "2" {
		return out, nil
	}
	return rebuilt(out, dir)
})

// rebuilt has the compiler at zam, written in zam, build and link itself from
// its sources into dir with no Go involved, and answers the executable it built.
func rebuilt(zam string, dir string) (string, error) {
	host, err := target.Host()
	if err != nil {
		return "", err
	}
	out := host.Executable(filepath.Join(dir, "zam2"))
	var stderr bytes.Buffer
	cmd := exec.Command(zam, "build", "-o", out, filepath.Join(root, "src", "Main.zam"))
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"))
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, stderr.String())
	}
	return out, nil
}

// link links object, generated for the program ast holds, into an executable
// for the host at out.
func link(object []byte, ast *syntax.AST, out string) error {
	host, err := target.Host()
	if err != nil {
		return err
	}
	return build.LinkObject(object, build.Options{Project: ast.MainCodeDecl.File().Project, OutputPath: out, Target: host})
}

// TestSyntaxMatchesGo parses every program in the Go parser's testdata with
// the compiler written in zam and with the Go one, and expects the same errors
// from both: every program without project files, and each one whose project
// file declares no dependency, which the compiler written in zam cannot yet
// fetch, with them.
func TestSyntaxMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	testdata := filepath.Join(root, "internal", "compiler", "0_syntax", "testdata")
	entries, err := filepath.Glob(filepath.Join(testdata, "*", "*", "Main.zam"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		name, err := filepath.Rel(testdata, filepath.Dir(entry))
		require.NoError(t, err)
		t.Run("no-project/"+filepath.ToSlash(name), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, goErrors(t, entry, true), zamErrors(t, zam, entry, true, "syntax"))
		})
		if declaresDependencies(t, entry) {
			continue
		}
		t.Run("project/"+filepath.ToSlash(name), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, goErrors(t, entry, false), zamErrors(t, zam, entry, false, "syntax"))
		})
	}
}

// declaresDependencies reports whether the project file governing entry, if
// there is one, declares a dependency.
func declaresDependencies(t *testing.T, entry string) bool {
	t.Helper()
	manifest, err := (&base.FileProvider{}).ProjectFilePath(base.Path(entry))
	if err != nil {
		return false
	}
	source, err := os.ReadFile(string(manifest))
	require.NoError(t, err)
	var project struct {
		Dependencies map[string]any `yaml:"dependencies"`
	}
	if yaml.Unmarshal(source, &project) != nil {
		return false
	}
	return len(project.Dependencies) > 0
}

var (
	// stepLine matches an error the compiler reports in some step.
	stepLine = regexp.MustCompile(`^\[[a-z]+\] `)
	// alternatives matches the tokens an error lists as expected.
	alternatives = regexp.MustCompile(`expected one of ((?:"[^"]*", )+or "[^"]*")`)
	// modifiers matches the modifiers an error names, which Go writes as the
	// raw bits of the set and in no fixed order.
	modifiers = regexp.MustCompile(`^(duplicate modifier|modifier) ('[^']*'|"[a-z]+")( cannot be used with ('[^']*'|"[a-z]+"))?`)
	// projectFile matches an error located at a project file, whose message
	// is the YAML library's own.
	projectFile = regexp.MustCompile(`(?s)^.* \((.*zam\.ya?ml)\)$`)
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
// modifiers are named, where an error about a field or import is located, and
// what an error reading a project file says.
func normalized(message string) string {
	message = projectFile.ReplaceAllString(message, "project file error ($1)")
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
// at entry, sorted, without project files when noProject.
func goErrors(t *testing.T, entry string, noProject bool) []string {
	t.Helper()
	files := &base.FileProvider{}
	cmd, err := cli.New(arguments(entry, noProject), files)
	require.NoError(t, err)
	_, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	got := []string{}
	for _, err := range errs {
		got = append(got, normalized(err.Error()))
	}
	slices.Sort(got)
	return got
}

// zamErrors answers the errors the compiler at zam reports in step for the
// program entered at entry, sorted, without project files when noProject.
func zamErrors(t *testing.T, zam, entry string, noProject bool, step string) []string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(zam, arguments(entry, noProject)[1:]...)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"))
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
		message, ok := strings.CutPrefix(line, "["+step+"] ")
		if !ok {
			require.True(t, stepLine.MatchString(line), fmt.Sprintf("not an error of a step: %s\n%s", line, stderr.String()))
			continue
		}
		// Go reads nothing past a file's declaration.
		if strings.Contains(message, `expected "EOF"`) {
			continue
		}
		got = append(got, normalized(message))
	}
	slices.Sort(got)
	return got
}

// arguments answers the command line building entry, without project files
// when noProject.
func arguments(entry string, noProject bool) []string {
	if noProject {
		return []string{"zam", "build", "-n", entry}
	}
	return []string{"zam", "build", entry}
}
