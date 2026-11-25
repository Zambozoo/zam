package test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
)

// run runs zam test with args after the verb, returning what it wrote and
// whether every test passed.
func run(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	files := &base.FileProvider{}
	cmd, err := cli.New(append([]string{"zam", "test", "-m", "16KB"}, args...), files)
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

// The coverage of every test file's process is merged, each line covered
// when a statement starting on it ran, and the standard library's and the
// tests' own statements are left out.
func TestRunCoverage(t *testing.T) {
	t.Parallel()
	dir := writeFiles(t, map[string]string{
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
	})
	profile := filepath.Join(t.TempDir(), "lcov.info")

	out, passed := run(t, "--coverprofile", profile, dir+"/...")
	assert.True(t, passed, out)
	assert.True(t, strings.HasSuffix(out, "coverage: 75.0% of statements\n"), out)
	lcov, err := os.ReadFile(profile)
	require.NoError(t, err)
	assert.Regexp(t, `^SF:\S*[/\\]lib[/\\]Lib\.zam
DA:3,1
DA:4,1
DA:6,1
DA:10,0
LF:4
LH:3
end_of_record
$`, string(lcov))
}

// regexpQuote quotes the trailing elements of path, which is how far a
// summary line names it once it is made relative to the working directory.
func regexpQuote(path string) string {
	return regexp.QuoteMeta(filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path)))
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
