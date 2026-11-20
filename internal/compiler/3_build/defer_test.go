package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeferRunsOnEveryWayOut leaves blocks with deferred statements by their
// end, by returns with and without a value, by breaks and continues, labelled
// and not, and by falling through a switch, and checks what ran and in what
// order.
func TestDeferRunsOnEveryWayOut(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static fun void(str*, str) add = (out, s) {
        *out = *out + s;
    }

    static fun void(str*, bool) order = (out, early) {
        defer Main::add(out, "1");
        defer Main::add(out, "2");
        if(early) {
            return;
        }
        defer Main::add(out, "3");
        {
            defer Main::add(out, "4");
            Main::add(out, "b");
        }
        Main::add(out, "e");
    }

    static fun int(str*) value = (out) {
        var int x = 1;
        defer x = 99;
        defer Main::add(out, x{str});
        x = 2;
        return x * 10;
    }

    static fun void(str*) loops = (out) {
        for(var int i = 0; i < 4; i++) {
            defer Main::add(out, i{str});
            if(i == 1) {
                continue;
            }
            if(i == 3) {
                break;
            }
            Main::add(out, ".");
        }
        label outer: for(var int i = 0; i < 2; i++) {
            defer Main::add(out, "o");
            for(var int j = 0; j < 2; j++) {
                defer Main::add(out, "i");
                if(j == 1) {
                    break outer;
                }
            }
        }
    }

    static fun int(str*) shadow = (out) {
        var str x = "outer";
        defer Main::add(out, x);
        {
            var str x = "inner";
            return 1;
        }
    }

    static fun str(str*) busy = (out) {
        var str kept = "k";
        defer {
            var int n = 0;
            for(var int i = 0; i < 3; i++) {
                n = n + i;
            }
            Main::add(out, n{str} + kept);
        }
        var str later = "l";
        return later + kept;
    }

    static fun void(str*, int) switches = (out, v) {
        switch (v) {
            case (1): {
                defer Main::add(out, "a");
                continue;
            }
            case (2): {
                defer Main::add(out, "b");
            }
        } else {
            defer Main::add(out, "c");
        }
    }

    static fun[T] T(str*, T) generic = (out, v) {
        defer Main::add(out, "g");
        return v;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str s = "";
        defer File::StdOut.WriteStr("end\n");
        Main::order(&s, false);
        s = s + " ";
        Main::order(&s, true);
        s = s + " ";
        var int v = Main::value(&s);
        s = s + v{str} + " ";
        Main::loops(&s);
        s = s + " ";
        var int sh = Main::shadow(&s);
        s = s + sh{str} + " ";
        var str b = Main::busy(&s);
        s = s + b + " ";
        Main::switches(&s, 1);
        Main::switches(&s, 3);
        s = s + " ";
        var str(str) wrap = lambda str(str): (w) {
            defer Main::add(&w, "!");
            return w + "?";
        };
        s = s + wrap("x") + " ";
        var (int, str) r = Main::[(int, str)]generic(&s, (7, "t"));
        s = s + r.0{str} + r.1;
        File::StdOut.WriteStr(s + "\n");
    }
}
`)
	want := "b4e321 21 220 .01.23iio outer1 3klk abc x? g7t\nend\n"
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, want, stdout)

	stdout, stderr, status = runVerified(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, want, stdout)
}
