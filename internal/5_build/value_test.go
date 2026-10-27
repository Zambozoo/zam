package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemberOfATemporary selects members straight out of values that calls
// return, which have no home to take an address of.
func TestMemberOfATemporary(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct P {
        var int a;
        var str b;
    }

    class C {
        var int a;
        var P p;
    }

    static fun P() mp = () {
        var P p;
        p.a = 7;
        p.b = "x" + "y";
        return p;
    }

    static fun C() mc = () {
        var C c;
        c.a = 8;
        c.p = type(Main).mp();
        return c;
    }

    fun void(uint, str...) main = (argC, argV) {
        type(File).StdOut.WriteStr(type(Main).mp().a{str} + type(Main).mp().b + type(Main).mc().a{str} + type(Main).mc().p.b + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "7xy8xy\n", stdout)
}

// TestSameNamedNestedGenerics instantiates one generic over two declarations'
// nested types that share a name, which print alike but are different types and
// need instantiations of their own.
func TestSameNamedNestedGenerics(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    class First[A] {
        struct Node[A] {
            var A v;
        }
        var ArrList[Node[A]*]* l;
    }

    class Second[A] {
        struct Node[A] {
            var A v;
            var A w;
        }
        var ArrList[Node[A]*]* l;
    }

    fun void(uint, str...) main = (argC, argV) {
        var First[int]* f = new(First[int]);
        f.l = type(ArrList)[First.Node[int]*].New(0U);
        var Second[int]* s = new(Second[int]);
        s.l = type(ArrList)[Second.Node[int]*].New(0U);
        for(var int i = 0; i < 20; i++) {
            f.l.AddLast(null);
            s.l.AddLast(null);
        }
        type(File).StdOut.WriteStr(f.l.Len(){str} + " " + s.l.Len(){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "20 20\n", stdout)
}

// TestMethodOfAnInlineLocalInATemplate calls a method on a local whose type is
// open in the enclosing template, which is called through the local's address.
func TestMethodOfAnInlineLocalInATemplate(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Holder[A] {
        fun bool(A*) Wrap = (p) {
            var Struct[A*] w;
            w.Init(p, null, null);
            return w.s == p;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Holder[int]* h = new(Holder[int]);
        var int x = 3;
        type(File).StdOut.WriteStr(h.Wrap(&x){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true\n", stdout)
}
