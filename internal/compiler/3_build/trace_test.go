package build_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// traceSource fails in the way its first argument names, two calls deep.
const traceSource = `import std("io/local/*");
import std("lang/System.zam");

namespace Main {
    fun int(int*, int) Inner = (p, d) {
        if (d == 0) {
            System::Panic("boom\n");
        }
        return *p / d;
    }

    fun int(int*, int) Outer = (p, d) {
        return Main::Inner(p, d);
    }

    fun void(uint, str...) main = (argC, argV) {
        var int x = 4;
        var str how = argV[0];
        if (how == "null") {
            Main::Outer(null, 1);
        } else if (how == "panic") {
            Main::Outer(&x, 0);
        } else if (how == "async") {
            var thread[int] t = async(Main::Outer(null, 1));
            wait(t);
        } else {
            var (str, uint, bool) c = System::Caller(0U);
            File::StdOut.WriteStr(c.1{str} + "\n");
        }
    }
}
`

// A failure says where the program was: the message, then a line for each
// call it was inside of, innermost first, each with the function and the line
// of source it was at.
func TestTrace(t *testing.T) {
	t.Parallel()
	binary := compile(t, traceSource)

	_, stderr, status := run(t, binary, "null")
	assert.Equal(t, 67, status)
	assert.Regexp(t, `dereferenced a null pointer
\x1b\[0m\tat Main.Inner \(\S*[/\\]Main.zam:9\)
\tat Main.Outer \(\S*[/\\]Main.zam:13\)
\tat Main.main \(\S*[/\\]Main.zam:20\)
$`, stderr)

	_, stderr, status = run(t, binary, "panic")
	assert.Equal(t, 255, status, "a panic keeps the status it exits with")
	assert.Regexp(t, `^boom
\tat System.Panic \(std://lang/System.zam:\d+\)
\tat Main.Inner \(\S*[/\\]Main.zam:7\)
\tat Main.Outer \(\S*[/\\]Main.zam:13\)
\tat Main.main \(\S*[/\\]Main.zam:22\)
$`, stderr)

	// A spawned thread's trace ends where it began, which is all it was
	// inside of.
	_, stderr, status = run(t, binary, "async")
	assert.Equal(t, 67, status)
	assert.Regexp(t, `\tat Main.Inner \(\S*[/\\]Main.zam:9\)
\tat Main.Outer \(\S*[/\\]Main.zam:13\)
$`, stderr)
	assert.NotRegexp(t, regexp.MustCompile(`Main\.main`), stderr)

	stdout, _, status := run(t, binary, "caller")
	assert.Zero(t, status)
	assert.Equal(t, "27\n", stdout, "Caller(0) is the line that called it")
}

// faultSource reads far past the end of an array, which no check catches.
const faultSource = `namespace Main {
    fun int(int*, int) Read = (p, i) {
        return p[i];
    }

    fun void(uint, str...) main = (argC, argV) {
        var int* p = new(int, 1);
        Main::Read(p, 1099511627776);
    }
}
`

// An invalid memory access says what was touched and where, then ends the
// process by the signal.
func TestTraceOnFault(t *testing.T) {
	t.Parallel()
	binary := compile(t, faultSource)

	_, stderr, status := run(t, binary)
	assert.Equal(t, faultStatus(), status, "the process ends by the fault")
	assert.Regexp(t, `^\x1b\[31m(invalid memory access|bus error) at 0x[0-9a-f]+
\x1b\[0m\tat Main.Read \(\S*[/\\]Main.zam:3\)
\tat Main.main \(\S*[/\\]Main.zam:8\)
$`, stderr)
}
