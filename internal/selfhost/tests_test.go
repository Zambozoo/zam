package selfhost_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	"github.com/zambozoo/zam/internal/test"
)

// TestTestFilesMatchGo parses the test files zam test is given with the
// compiler written in zam and with the Go one, and expects the same errors
// from both: for the Go parser's own test files, and for test files with
// errors in a directory and one beneath it.
func TestTestFilesMatchGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	broken := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(broken, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, "zam.yml"), []byte("name: broken\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(broken, "A.test.zam"), []byte("public namespace ATest { var int x = ; }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(broken, "sub", "B.test.zam"), []byte("public namespace BTest { fun void() f; }\n"), 0o644))

	for name, target := range map[string]string{
		"tests":            filepath.Join(root, "internal", "compiler", "0_syntax", "testdata", "tests"),
		"broken":           broken,
		"broken recursive": filepath.ToSlash(broken) + "/...",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			want := goTestErrors(t, target)
			if strings.HasPrefix(name, "broken") {
				require.NotEmpty(t, want)
			}
			assert.Equal(t, want, zamTestErrors(t, zam, target))
		})
	}
}

// goTestErrors answers the errors the Go parser reports for the test files
// target names, sorted.
func goTestErrors(t *testing.T, target string) []string {
	t.Helper()
	files := &base.FileProvider{}
	paths, err := test.Resolve(files, []string{target})
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	cmd := &cli.Command{InputPath: paths[0], TestPaths: paths[1:], MaxThreads: 4, WriteProject: true}
	_, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	got := []string{}
	for _, err := range errs {
		got = append(got, normalized(err.Error()))
	}
	slices.Sort(got)
	return got
}

// zamTestErrors answers the errors the compiler at zam reports for the test
// files target names, sorted.
func zamTestErrors(t *testing.T, zam, target string) []string {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(zam, "test", target)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAMPATH="+t.TempDir())
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
		require.True(t, ok, "not a syntax error: "+line)
		got = append(got, normalized(message))
	}
	slices.Sort(got)
	return got
}
