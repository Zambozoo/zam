package selfhost_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base/target"
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
