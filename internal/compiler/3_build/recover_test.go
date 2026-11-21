package build_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// recovering wraps declarations of Main, whose main is among them, with what
// they print through and panic with.
func recovering(decls string) string {
	return `import std("io/local/*");
import std("lang/Arrs.zam");
import std("lang/Strs.zam");
import std("lang/System.zam");

namespace Main {
    fun void(str) Out = (s) {
        File::StdOut.WriteStr(s);
    }
` + decls + `
}
`
}

// A panic runs the defers of every scope it is inside of, innermost first, and
// one that recovers it ends it: the rest of that scope's defers run, and
// execution goes on past the scope, with the scopes around it still open.
func TestRecover(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		decls  string
		want   string
		status int
	}{
		"past the scope": {decls: `
    fun void(uint, str...) main = (argC, argV) {
        defer Main::Out("main\n");
        {
            defer Main::Out("recovered " + System::Recover());
            defer Main::Out("inner\n");
            System::Panic("boom\n");
            Main::Out("unreached\n");
        }
        Main::Out("after\n");
    }`, want: "inner\nrecovered boom\nafter\nmain\n"},

		"through calls": {decls: `
    fun void() Inner = () {
        defer Main::Out("inner\n");
        System::Panic("deep\n");
    }

    fun void() Middle = () {
        defer Main::Out("middle\n");
        Main::Inner();
        Main::Out("unreached\n");
    }

    fun void(uint, str...) main = (argC, argV) {
        {
            defer Main::Out("got " + System::Recover());
            Main::Middle();
        }
        Main::Out("done\n");
    }`, want: "inner\nmiddle\ngot deep\ndone\n"},

		"a failed check": {decls: `
    fun int(int) Divide = (d) {
        return 1 / d;
    }

    fun void(uint, str...) main = (argC, argV) {
        {
            defer Main::Out(System::Recover() + "\n");
            Main::Divide(0);
        }
        Main::Out("done\n");
    }`, want: "divide by zero\ndone\n"},

		"a loop body": {decls: `
    fun void(uint, str...) main = (argC, argV) {
        for (var int i = 0; i < 3; i++) {
            defer System::Recover();
            if (i == 1) {
                System::Panic("skip");
            }
            Main::Out(i{str} + "\n");
        }
        Main::Out("end\n");
    }`, want: "0\n2\nend\n"},

		"a result": {decls: `
    fun int() Answer = () {
        defer System::Recover();
        System::Panic("none");
        return 5;
    }

    fun void(uint, str...) main = (argC, argV) {
        Main::Out(Main::Answer(){str} + "\n");
    }`, want: "0\n"},

		"a panic in a defer": {decls: `
    fun void(uint, str...) main = (argC, argV) {
        {
            defer Main::Out("got " + System::Recover());
            defer System::Panic("second\n");
            defer Main::Out("ran\n");
            System::Panic("first\n");
        }
        Main::Out("done\n");
    }`, want: "ran\ngot second\ndone\n"},

		"a recovered panic within a defer": {decls: `
    fun void() Quiet = () {
        defer System::Recover();
        System::Panic("inner");
    }

    fun void(uint, str...) main = (argC, argV) {
        {
            defer Main::Out("got " + System::Recover());
            defer Main::Quiet();
            System::Panic("outer\n");
        }
        Main::Out("done\n");
    }`, want: "got outer\ndone\n"},

		"a defer in a deferred block": {decls: `
    fun void(uint, str...) main = (argC, argV) {
        {
            defer Main::Out("got " + System::Recover());
            defer {
                defer Main::Out("nested\n");
                Main::Out("block\n");
            }
            System::Panic("boom\n");
        }
        Main::Out("done\n");
    }`, want: "block\nnested\ngot boom\ndone\n"},

		"kept in a variable": {decls: `
    fun void(uint, str...) main = (argC, argV) {
        var str failure;
        {
            defer failure = System::Recover();
            System::Panic("kept\n");
        }
        Main::Out(failure);
    }`, want: "kept\n"},

		"another thread": {decls: `
    fun int() Safe = () {
        defer Main::Out("thread " + System::Recover());
        System::Panic("boom\n");
        return 1;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[int] t = async(Main::Safe());
        Main::Out(wait(t){str} + "\n");
    }`, want: "thread boom\n0\n"},

		"no panic": {decls: `
    fun void(uint, str...) main = (argC, argV) {
        if (System::Recover() == null) {
            Main::Out("null\n");
        }
        {
            defer Main::Out((System::Recover() == null){str} + "\n");
        }
    }`, want: "null\ntrue\n"},

		"not from a defer's own call": {decls: `
    fun void() Helper = () {
        Main::Out((System::Recover() == null){str} + "\n");
    }

    fun void(uint, str...) main = (argC, argV) {
        defer Main::Helper();
        System::Panic("boom\n");
    }`, want: "true\nboom\n", status: 255},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			output, status := runNative(t, recovering(tc.decls))
			assert.Equal(t, tc.status, status, output)
			if tc.status == 0 {
				assert.Equal(t, tc.want, output)
			} else {
				assert.Contains(t, output, tc.want)
			}
		})
	}
}

// A panic nothing recovers runs every defer, then says where it was raised.
func TestRecoverNone(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, recovering(`
    fun void() Inner = () {
        defer Main::Out("inner\n");
        System::Panic("boom\n");
    }

    fun void(uint, str...) main = (argC, argV) {
        defer Main::Out("main\n");
        Main::Inner();
    }`))
	assert.Equal(t, 255, status)
	assert.Regexp(t, `^inner
main
boom
\tat System.Panic \(std://lang/System.zam:\d+\)
\tat Main.Inner \(\S*:13\)
\tat Main.main \(\S*:18\)
$`, output)
}

// What a program does wrong that the runtime catches panics, so that a defer
// may recover it.
func TestRecoverAutomaticPanics(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"a null index":          {`var int* p = null; var int x = p[3];`, "dereferenced a null pointer"},
		"an invalid access":     {`var int* p = new(int, 1); var int x = p[1099511627776];`, "invalid memory access at 0x"},
		"a null string joined":  {`var str s = null; var str joined = s + "x";`, "joined a null string"},
		"a null string indexed": {`var str s = null; var byte b = s{byte*}[0];`, "dereferenced a null pointer"},
		"a null thread":         {`var thread[int] t; wait(t);`, "waited on a null thread"},
		"a null any":            {`var any a = null; var int x = a{int};`, "used a null any"},
		"an any of another type": {`var any a = "s"; var int x = a{int};`,
			"any holds a value of a type the unbox did not ask for"},
		"a negative count":  {`var int n = -1; var int* p = new(int, n);`, "allocated a negative number of elements"},
		"a count too large": {`var int* p = new(int, 4611686018427387904);`, "allocated more elements than memory can hold"},
		"an interior free":  {`var int* p = new(int, 4); free(p + 1);`, "freed a pointer into the middle of what new allocated"},
		"a free of a local": {`var int x = 1; free(&x);`, "freed memory new did not allocate"},
		"a double free":     {`var int* p = new(int, 2); free(p); free(p);`, "freed memory that was already freed"},
		"a zero divisor":    {`var int zero = 0; var int x = 7 % zero;`, "divide by zero"},
		"a native's panic": {`var byte* b = null; var str s = Strs::FromBytes(b, 1U);`,
			"Strs.FromBytes: null bytes"},
		"an invalid code point": {`var char* cs = new(char, 1); cs[0] = 55296{char}; var str s = Strs::ToChars(cs, 1U);`,
			"Strs.ToChars: invalid code point"},
		"a native handed null":  {`var str s = null; var uint n = Strs::ByteLen(s);`, "handed a native a null string"},
		"a native's bad length": {`var int* a = new(int, 1); Arrs::[int]Copy((-1){uint}, a, a);`, "Arrs.Copy: negative length"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			output, status := runNative(t, recovering(`
    fun void(uint, str...) main = (argC, argV) {
        {
            defer Main::Out("recovered " + System::Recover() + "\n");
            `+tc.body+`
        }
        Main::Out("done\n");
    }`))
			assert.Zero(t, status, output)
			assert.Regexp(t, "^recovered "+regexp.QuoteMeta(tc.want)+".*\ndone\n$", output)
		})
	}
}

// An invalid access nothing recovers runs every defer, then ends the process
// by the signal it raised.
func TestRecoverNoFault(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, recovering(`
    fun void(uint, str...) main = (argC, argV) {
        defer Main::Out("ran\n");
        var int* p = new(int, 1);
        var int x = p[1099511627776];
    }`))
	assert.Equal(t, faultStatus(), status)
	assert.Regexp(t, `^ran
\x1b\[31minvalid memory access at 0x[0-9a-f]+
\x1b\[0m\tat Main.main \(\S*:14\)
$`, output)
}

// A panic in an override the runtime calls unwinds out of the runtime to the
// defers of the code that asked for it.
func TestRecoverFromOverride(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, recovering(`
    class Bad {
        var int x;

        public fun str() Str = () {
            System::Panic("bad str");
            return "";
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Bad* b = new(Bad);
        {
            defer Main::Out("recovered " + System::Recover() + "\n");
            Main::Out((b, 1){str});
        }
        Main::Out("done\n");
    }`))
	assert.Zero(t, status, output)
	assert.Equal(t, "recovered bad str\ndone\n", output)
}
