package testrunner

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
)

// skipInterpreterOnWindows skips a test of the interpreter's build, which is
// not ported to Windows.
func skipInterpreterOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the interpreter's build is not ported to Windows")
	}
}

// run runs zam test with args after the verb, returning what it wrote and
// whether every test passed.
func run(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	files := &base.FileProvider{}
	cmd, err := command.New(append([]string{"zam", "test", "-m", "16KB"}, args...), files)
	require.NoError(t, err)

	var out bytes.Buffer
	passed, err := Run(cmd, files, base.NewPackageProvider(t.TempDir()), &out)
	require.NoError(t, err)
	return out.String(), passed
}

// A recorded failure fails its test and the rest of the file still runs; a
// fatal one ends its file; a crash ends its file and is reported by how it
// ended. No file's outcome reaches any other's.
func TestRun(t *testing.T) {
	t.Parallel()
	skipInterpreterOnWindows(t)
	dir := writeFiles(t, map[string]string{
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
	})

	out, passed := run(t, "--parallel", "2", dir+"/...", "--", "x", "y")
	assert.False(t, passed)

	crash := filepath.Join(dir, "crash", "Crash.test.zam")
	fatal := filepath.Join(dir, "fatal", "Fatal.test.zam")
	list := filepath.Join(dir, "list", "List.test.zam")
	pass := filepath.Join(dir, "pass", "Pass.test.zam")
	assert.Regexp(t, `^=== RUN   CrashTest.Panics
boom
\tat System.Panic \(std://lang/System.zam:\d+\)
\tat CrashTest.Panics \(\S*[/\\]Crash.test.zam:5\)
\tat ZamTestMain.main \(\S*[/\\]ZamTestMain\.test\.zam:\d+\)
FAIL \S*`+regexpQuote(crash)+` \(exit 255\)
=== RUN   FatalTest.Stops
    Fatal.test.zam:5: stopped
--- FAIL: FatalTest.Stops
FAIL
FAIL \S*`+regexpQuote(fatal)+` \S+
=== RUN   ListTest.Doubles
--- PASS: ListTest.Doubles
=== RUN   ListTest.Wrong
    List.test.zam:11: want 5, got 4
    List.test.zam:12: still running
--- FAIL: ListTest.Wrong
=== RUN   ListTest.Args
--- PASS: ListTest.Args
FAIL
FAIL \S*`+regexpQuote(list)+` \S+
=== RUN   PassTest.Passes
--- PASS: PassTest.Passes
PASS
ok   \S*`+regexpQuote(pass)+` \S+
$`, out)
}

// Only the tests the pattern picks run, and a run of only passing tests
// passes.
func TestRunPicksTests(t *testing.T) {
	t.Parallel()
	skipInterpreterOnWindows(t)
	dir := writeFiles(t, map[string]string{
		"List.test.zam": `import std("test/Assert.zam");

public namespace ListTest {
    public fun void(uint, str...) Passes = (argC, argV) {}

    public fun void(uint, str...) Fails = (argC, argV) {
        Assert::Fatal("fails");
    }
}
`,
	})

	out, passed := run(t, "--run", "Passes", dir)
	assert.True(t, passed)
	assert.Contains(t, out, "--- PASS: ListTest.Passes\nPASS\nok   ")
	assert.NotContains(t, out, "Fails")

	out, passed = run(t, "--run", "Nothing", dir)
	assert.True(t, passed)
	assert.Equal(t, "no tests to run\n", out)
}

// regexpQuote quotes the trailing elements of path, which is how far a
// summary line names it once it is made relative to the working directory.
func regexpQuote(path string) string {
	return regexp.QuoteMeta(filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)))
}

// A native run compiles the tests to native code and reports them as the
// interpreter's run does, a crash's trace included.
func TestRunNative(t *testing.T) {
	t.Setenv("ZAM_CACHE_DIR", t.TempDir())
	dir := writeFiles(t, map[string]string{
		"list/List.test.zam": `import std("test/Assert.zam");

public namespace ListTest {
    public fun void(uint, str...) Doubles = (argC, argV) {
        Assert::[int]Equal(4, 2 * 2);
    }

    public fun void(uint, str...) Wrong = (argC, argV) {
        Assert::[int]Equal(5, 2 * 2);
    }
}
`,
		"crash/Crash.test.zam": `import std("lang/System.zam");

public namespace CrashTest {
    public fun void(uint, str...) Panics = (argC, argV) {
        System::Panic("boom\n");
    }
}
`,
	})

	out, passed := run(t, "--native", dir+"/...")
	assert.False(t, passed)

	crash := filepath.Join(dir, "crash", "Crash.test.zam")
	list := filepath.Join(dir, "list", "List.test.zam")
	assert.Regexp(t, `^=== RUN   CrashTest.Panics
boom
\tat System.Panic \(std://lang/System.zam:\d+\)
\tat CrashTest.Panics \(\S*[/\\]Crash.test.zam:5\)
\tat ZamTestMain.main \(\S*[/\\]ZamTestMain\.test\.zam:\d+\)
FAIL \S*`+regexpQuote(crash)+` \(exit 255\)
=== RUN   ListTest.Doubles
--- PASS: ListTest.Doubles
=== RUN   ListTest.Wrong
    List.test.zam:9: want 5, got 4
--- FAIL: ListTest.Wrong
FAIL
FAIL \S*`+regexpQuote(list)+` \S+
$`, out)
}

// TestWindowsStatusReadsAsPOSIX reads Windows exit codes as POSIX statuses: a
// panic's -1 as its low byte, and an access violation as a crash.
func TestWindowsStatusReadsAsPOSIX(t *testing.T) {
	for _, test := range []struct {
		code   int
		status int
		ending string
	}{
		{code: 1, status: 1, ending: "exit status 1"},
		{code: 0xFFFFFFFF, status: 255, ending: "exit status 4294967295"},
		{code: 0xC0000005, status: -1, ending: "exception 0xc0000005"},
	} {
		status, ending := windowsStatus(test.code, fmt.Sprintf("exit status %d", test.code))
		assert.Equal(t, test.status, status, "%#x", test.code)
		assert.Equal(t, test.ending, ending, "%#x", test.code)
	}
}
