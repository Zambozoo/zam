package selfhost_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// zamTestErrors answers the syntax errors the compiler at zam reports for the
// test files target names, sorted.
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
		if message, ok := strings.CutPrefix(line, "[syntax] "); ok {
			got = append(got, normalized(message))
		}
	}
	slices.Sort(got)
	return got
}

// TestZamTestCompiles runs zam test with the compiler written in zam over test
// files with no tests, with tests none of which --run picks, and beside a file
// at the runner's own path, and expects it to say there are no tests and to
// refuse the runner's path.
func TestZamTestCompiles(t *testing.T) {
	t.Parallel()
	zam, err := compiler()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: tests\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "A.test.zam"), []byte(`import std("test/Assert.zam");

public namespace ATest {
    public fun void(uint, str...) Passes = (argC, argV) {
        Assert::True(true, "true is false");
    }
}
`), 0o644))
	empty := t.TempDir()

	for name, c := range map[string]struct {
		args   []string
		stdout string
	}{
		"no test files": {args: []string{empty}, stdout: "no test files\n"},
		"no tests":      {args: []string{"--run", "Missing", dir}, stdout: "no tests to run\n"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, code := zamTest(t, zam, c.args...)
			assert.Zero(t, code, stderr)
			assert.Equal(t, c.stdout, stdout)
		})
	}

	t.Run("runner path", func(t *testing.T) {
		t.Parallel()
		taken := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(taken, "zam.yml"), []byte("name: tests\n"), 0o644))
		source, err := os.ReadFile(filepath.Join(dir, "A.test.zam"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(taken, "A.test.zam"), source, 0o644))
		runner := filepath.Join(taken, "ZamTestMain.test.zam")
		require.NoError(t, os.WriteFile(runner, []byte("public namespace ZamTestMainTest {}\n"), 0o644))
		_, stderr, code := zamTest(t, zam, filepath.Join(taken, "A.test.zam"))
		assert.Equal(t, 1, code)
		assert.Equal(t, "[test] "+runner+" is the test runner's own path and must not exist\n", stderr)
	})
}

// zamTest runs zam test over args with the compiler at zam, answering what it
// wrote and the status it exited with.
func zamTest(t *testing.T, zam string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(zam, append([]string{"test"}, args...)...)
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAMPATH="+t.TempDir())
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), stderr.String(), exit.ExitCode()
	}
	require.NoError(t, err)
	return stdout.String(), stderr.String(), 0
}

// durations matches the duration ending an ok or FAIL line of zam test.
var durations = regexp.MustCompile(`(?m)^((?:ok  |FAIL) \S+) \d\S*$`)

// TestZamTestMatchesGo runs the cases of Go's own runner tests through zam test
// with the compiler written in zam, and through Go's, and expects both to
// write the same, durations aside, to agree on whether every test passed, and
// to write the same coverage profile: recorded and fatal failures, a crash and
// run arguments; tests --run picks and none; and coverage merged across the
// processes of two test files.
func TestZamTestMatchesGo(t *testing.T) {
	t.Parallel()
	zam, err := compiler()
	require.NoError(t, err)
	picked := map[string]string{
		"List.test.zam": `import std("test/Assert.zam");

public namespace ListTest {
    public fun void(uint, str...) Passes = (argC, argV) {}

    public fun void(uint, str...) Fails = (argC, argV) {
        Assert::Fatal("fails");
    }
}
`,
	}
	for name, c := range map[string]struct {
		files map[string]string
		args  func(dir string) []string
		cover bool
	}{
		"run": {files: map[string]string{
			"list/List.zam": `namespace List {
    fun int(int) Double = (x) {
        return x * 2;
    }
}
`,
			"list/List.test.zam": `import std("test/Assert.zam");
import std("test/Test.zam");
import "List.zam";

public namespace ListTest {
    public fun void(uint, str...) Doubles = (argC, argV) {
        Assert::[int]Equal(4, List::Double(2));
    }

    public fun void(uint, str...) Wrong = (argC, argV) {
        Assert::[int]Equal(5, List::Double(2));
        Test::Log("still running");
    }

    public fun void(uint, str...) Args = (argC, argV) {
        Assert::[uint]Equal(2U, argC);
        Assert::[str]Equal("y", argV[1]);
    }
}
`,
			"fatal/Fatal.test.zam": `import std("test/Assert.zam");

public namespace FatalTest {
    public fun void(uint, str...) Stops = (argC, argV) {
        Assert::Fatal("stopped");
    }

    public fun void(uint, str...) Skipped = (argC, argV) {}
}
`,
			"crash/Crash.test.zam": `import std("lang/System.zam");

public namespace CrashTest {
    public fun void(uint, str...) Panics = (argC, argV) {
        System::Panic("boom\n");
    }

    public fun void(uint, str...) Skipped = (argC, argV) {}
}
`,
			"pass/Pass.test.zam": `public namespace PassTest {
    public fun void(uint, str...) Passes = (argC, argV) {}
}
`,
		}, args: func(dir string) []string { return []string{"--parallel", "2", dir + "/...", "--", "x", "y"} }},
		"picks":   {files: picked, args: func(dir string) []string { return []string{"--run", "Passes", dir} }},
		"nothing": {files: picked, args: func(dir string) []string { return []string{"--run", "Nothing", dir} }},
		"coverage": {files: map[string]string{
			"lib/Lib.zam": `namespace Lib {
    fun int(int) Sign = (x) {
        if(x < 0) {
            return -1;
        }
        return 1;
    }

    fun int() Unused = () {
        return 0;
    }
}
`,
			"lib/Positive.test.zam": `import std("test/Assert.zam");
import "Lib.zam";

public namespace PositiveTest {
    public fun void(uint, str...) Positive = (argC, argV) {
        Assert::[int]Equal(1, Lib::Sign(5));
    }
}
`,
			"lib/Negative.test.zam": `import std("test/Assert.zam");
import "Lib.zam";

public namespace NegativeTest {
    public fun void(uint, str...) Negative = (argC, argV) {
        Assert::[int]Equal(-1, Lib::Sign(-5));
    }
}
`,
		}, args: func(dir string) []string { return []string{dir + "/..."} }, cover: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: tests\n"), 0o644))
			for path, source := range c.files {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(source), 0o644))
			}
			goArgs, zamArgs := c.args(filepath.ToSlash(dir)), c.args(filepath.ToSlash(dir))
			goProfile, zamProfile := filepath.Join(t.TempDir(), "go.lcov"), filepath.Join(t.TempDir(), "zam.lcov")
			if c.cover {
				goArgs = append([]string{"--coverprofile", goProfile}, goArgs...)
				zamArgs = append([]string{"--coverprofile", zamProfile}, zamArgs...)
			}

			files := &base.FileProvider{}
			cmd, err := cli.New(append([]string{"zam", "test"}, goArgs...), files)
			require.NoError(t, err)
			var out bytes.Buffer
			passed, err := test.Run(cmd, files, base.NewPackageProvider(t.TempDir()), &out)
			require.NoError(t, err)

			stdout, stderr, code := zamTest(t, zam, zamArgs...)
			assert.Equal(t, passed, code == 0, stderr)
			assert.Equal(t, durations.ReplaceAllString(out.String(), "$1 D"), durations.ReplaceAllString(stdout, "$1 D"))
			if c.cover {
				want, err := os.ReadFile(goProfile)
				require.NoError(t, err)
				got, err := os.ReadFile(zamProfile)
				require.NoError(t, err)
				assert.Equal(t, string(want), string(got))
			}
		})
	}
}

// TestZamTestsWithZam runs the tests written in zam under src with zam test of
// the compiler written in zam, and expects every one to pass.
func TestZamTestsWithZam(t *testing.T) {
	t.Parallel()
	zam, err := compiler()
	require.NoError(t, err)
	stdout, stderr, code := zamTest(t, zam, filepath.ToSlash(filepath.Join(root, "src"))+"/...")
	assert.Zero(t, code, stdout+stderr)
}
