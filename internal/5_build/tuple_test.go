package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTuples(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct P {
        var int x;
        var str y;
    }

    class Box[A] {
        var (A, int) v;

        fun (int, A)() Swap = () {
            var A a, int i = this.v;
            return i, a;
        }
    }

    fun (int, int)() f = () { return 0, 0; }

    fun void(uint, str...) main = (argC, argV) {
        var (int, str) t = 1, "a";
        var int a, str b = t;
        var int c = (a, b).0;
        var int z0, int z1 = Main::f();
        File::StdOut.WriteStr(t{str} + " " + a{str} + b + " " + c{str} + " " + z0{str} + z1{str} + "\n");

        var ((int, str), P) n;
        n.0.1 = "q";
        n.1.x = 5;
        n.1.y = "w";
        File::StdOut.WriteStr(n.0{str} + n.1.x{str} + n.1.y + "\n");

        var (int, str) u = 1, "a" + "";
        File::StdOut.WriteStr((t == u){str} + (t != (1, "b")){str} + (t < (1, "b")){str} + ((2, "a") > t){str} + (#t == #u){str} + (t <=> u){str} + "\n");

        var any boxed = t;
        var (int, str) back = boxed{(int, str)};
        File::StdOut.WriteStr(boxed{str} + back.1 + (#boxed == #t){str} + (boxed == u{any}){str} + "\n");

        var (int, str)* p = &t;
        p.0 = 9;
        File::StdOut.WriteStr(t.0{str} + "\n");

        var (str, int) s = null, 2;
        var int x, int y = 1, 2;
        var int y2, int x2 = x, y;
        File::StdOut.WriteStr(s{str} + x2{str} + y2{str} + "\n");

        var Box[str]* bx = new(Box[str]);
        bx.v = ("k", 3);
        var int bi, str bs = bx.Swap();
        File::StdOut.WriteStr(bi{str} + bs + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "(1, a) 1a 1 00\n"+
		"(0, q)5w\n"+
		"truetruetruetruetrue0\n"+
		"(1, a)atruetrue\n"+
		"9\n"+
		"(null, 2)21\n"+
		"3k\n", stdout)
}

// TestCollectionTracesTuples holds collected values only inside tuples, through
// every kind of root a tuple can lie in, and reads them back after allocating
// far past the heap.
func TestCollectionTracesTuples(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    static var (str, int) kept;

    class Box {
        var (str, (any, Box*)) t;
    }

    class Marked {
        var int pad;
        var (str*, str)* run;
        var (int, str) held;

        fun void() Mark = () {
            mark(this.held);
            mark(this.run[0]);
        }
    }

    static fun str(int) s = (i) {
        return i{str} + "-s";
    }

    static fun void() churn = () {
        for (var int i = 0; i < 20000; i++) {
            var str junk = Main::s(i) + Main::s(i);
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        Main::kept = (Main::s(1), 1);
        var ((str, int), str) local = (Main::s(2), 2), Main::s(3);

        var Box* box = new(Box);
        var Box* inner = new(Box);
        inner.t = (Main::s(4), (null, null));
        box.t = (Main::s(5), (Main::s(6){any}, inner));

        var any boxed = (Main::s(8), 8);

        var ArrList[(str, int)]* list = ArrList::[(str, int)]New(0U);
        list.AddLast((Main::s(9), 9));

        var (str, int) captured = Main::s(10), 10;
        var str() f = lambda str(): () { return captured.0; };

        var Marked* m = new(Marked);
        m.held = (11, Main::s(11));
        m.run = new((str*, str), 1U);
        m.run[0] = (null, Main::s(12));

        Main::churn();

        var (str, int) back = boxed{(str, int)};
        File::StdOut.WriteStr(Main::kept.0 + " " + local.0.0 + " " + local.1 + " " + inner.t.0 + " " + box.t.0 + " " + box.t.1.0{str} + " " + box.t.1.1.t.0 + "\n");
        File::StdOut.WriteStr(back.0 + " " + list.Get(0U).0 + " " + f() + " " + m.held.1 + " " + m.run[0].1 + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "1-s 2-s 3-s 4-s 5-s 6-s 4-s\n8-s 9-s 10-s 11-s 12-s\n", stdout)
}

// TestCollectionTracesTupleThreadResults leaves tuples holding collected values
// as the results of threads that have finished but not yet been joined, and as
// the arguments of a thread still running, while main allocates far past the
// heap. A thread yielding a type that is interned in its own right, as a tuple
// or a pointer is, is a type apart from what it yields.
func TestCollectionTracesTupleThreadResults(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static fun str(int) s = (i) {
        return i{str} + "-s";
    }

    static fun void() churn = () {
        for (var int i = 0; i < 20000; i++) {
            var str junk = Main::s(i) + Main::s(i);
        }
    }

    static fun (str, (int, str))(int) make = (i) {
        return Main::s(i), (i, Main::s(i + 100));
    }

    class Box {
        var str v;
    }

    static fun Box*() box = () {
        var Box* b = new(Box);
        b.v = Main::s(3);
        return b;
    }

    static fun (str, int)((str, int)) hold = (t) {
        Main::churn();
        return t;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[(str, (int, str))] done = async(Main::make(1));
        var thread[(str, int)] held = async(Main::hold((Main::s(2), 2)));
        var thread[Box*] boxed = async(Main::box());
        Main::churn();

        var (str, (int, str)) a = wait(done);
        var str b, int n = wait(held);
        var Box* x = wait(boxed);
        File::StdOut.WriteStr(a.0 + " " + a.1.1 + " " + b + " " + n{str} + " " + x.v + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "1-s 101-s 2-s 2 3-s\n", stdout)
}
