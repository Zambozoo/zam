package selfhost_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base/target"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
)

// linked is every program of the codegen corpus the compiler written in zam
// builds and links into an executable of its own.
var linked = runPorted

// TestLinkedMatchesGo builds every linked program of the codegen corpus into
// an executable with the compiler written in zam alone, and with Go, and
// expects both to print the same and exit the same.
func TestLinkedMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	for _, entry := range codegenEntries(t) {
		name := filepath.Base(filepath.Dir(entry))
		if !linked[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, goRun(t, entry), zamBuildRun(t, zam, entry))
		})
	}
}

// TestLinksForEveryTarget builds the hello program of the codegen corpus for
// every target with the compiler written in zam alone, and expects an
// executable of the target's format.
func TestLinksForEveryTarget(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)
	entry := filepath.Join(root, "internal", "selfhost", "testdata", "codegen", "hello", "Main.zam")
	magics := map[target.OS][]byte{
		target.MacOS:   {0xcf, 0xfa, 0xed, 0xfe},
		target.Linux:   {0x7f, 'E', 'L', 'F'},
		target.Windows: {'M', 'Z'},
	}
	for _, name := range target.Names() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			named, err := target.Parse(name)
			require.NoError(t, err)
			out := named.Executable(filepath.Join(t.TempDir(), "main"))
			var stderr bytes.Buffer
			build := exec.Command(zam, "build", "--target", name, "-o", out, entry)
			build.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"))
			build.Stderr = &stderr
			require.NoError(t, build.Run(), stderr.String())
			executable, err := os.ReadFile(out)
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(executable, magics[named.OS]), "%s is no %s executable", out, named.OS)
		})
	}
}

// TestZamRunMatchesGo runs every linked program of the codegen corpus with
// zam run, with the compiler written in zam alone, and expects it to print the
// same and exit the same as Go's build of it.
func TestZamRunMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	for _, entry := range codegenEntries(t) {
		name := filepath.Base(filepath.Dir(entry))
		if !linked[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, goRun(t, entry), zamRunCommand(t, zam, entry))
		})
	}
}

// TestCleanMatchesGo cleans with the compiler written in zam alone, and
// expects it to remove and report what Go's clean does: the fetched packages,
// and the packs and runtimes of the cache, leaving the rest of the cache.
func TestCleanMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)
	zamPath, cache := t.TempDir(), t.TempDir()
	removed := []string{filepath.Join(zamPath, "pkg"), filepath.Join(cache, "packs"), filepath.Join(cache, "runtime")}
	for _, dir := range append(removed, filepath.Join(cache, "kept")) {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "x"), 0o700))
	}

	clean := exec.Command(zam, "clean")
	clean.Env = append(os.Environ(), "ZAMPATH="+zamPath, "ZAM_CACHE_DIR="+cache)
	out, err := clean.Output()
	require.NoError(t, err)
	want := ""
	for _, dir := range removed {
		want += "removed " + dir + "\n"
		assert.NoDirExists(t, dir)
	}
	assert.Equal(t, want, string(out))
	assert.DirExists(t, filepath.Join(cache, "kept"))
}

// zamRunCommand runs the program entered at entry with zam run, with the
// compiler at zam, with no arguments and no input, leaving out of what it
// wrote to stderr the warnings of functions compiled as traps.
func zamRunCommand(t *testing.T, zam, entry string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	run := exec.Command(zam, "run", entry)
	run.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"))
	run.Stdout = &stdout
	run.Stderr = &stderr
	err := run.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else {
		require.NoError(t, err)
	}
	var written strings.Builder
	for line := range strings.Lines(stderr.String()) {
		if !strings.HasPrefix(line, "compiled as a trap: ") {
			written.WriteString(line)
		}
	}
	return runResult{Stdout: stdout.String(), Stderr: written.String(), Code: code}
}

// TestRunsProgram runs a program with zam run, with the compiler written in
// zam alone, and expects it to have the arguments after its file, write to
// this process's stdout and exit with its own status.
func TestRunsProgram(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"run\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(`import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr(argV[0] + " " + argV[1] + "\n");
        System::Exit(argC{int});
    }
}
`), 0o600))

	var stdout, stderr bytes.Buffer
	run := exec.Command(zam, "run", entry, "a", "b")
	run.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"))
	run.Stdout = &stdout
	run.Stderr = &stderr
	err = run.Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, stderr.String())
	assert.Equal(t, 2, exit.ExitCode(), stderr.String())
	assert.Equal(t, "a b\n", stdout.String())
}

// TestPrintsVersion expects zam version, with the compiler written in zam
// alone, to print what Go's does.
func TestPrintsVersion(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)
	out, err := exec.Command(zam, "version").Output()
	require.NoError(t, err)
	assert.Equal(t, "zam "+build.Version+"\n", string(out))
}

// zamBuildRun builds the program entered at entry into an executable with the
// compiler at zam, and runs it with no arguments and no input.
func zamBuildRun(t *testing.T, zam, entry string) runResult {
	t.Helper()
	host, err := target.Host()
	require.NoError(t, err)
	out := host.Executable(filepath.Join(t.TempDir(), "main"))
	var stderr bytes.Buffer
	build := exec.Command(zam, "build", "-o", out, entry)
	build.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"))
	build.Stderr = &stderr
	require.NoError(t, build.Run(), stderr.String())
	return runExecutable(t, out)
}
