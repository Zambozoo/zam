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

// TestOpenWidthFrames lays a template's frame with slots whose width its
// instantiation decides, ahead of slots it reads afterwards.
func TestOpenWidthFrames(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class G[B] {
        var B v;

        fun int(B, int) F = (b, n) {
            var Pair[B, bool] out;
            out.first = b;
            var G[B]* self = this;
            var int total = 0;
            for(var int i = 0; i < n; i++) {
                total = total + i;
            }
            this.v = out.first;
            return total;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Pair[int, int] p;
        p.first = 1;
        p.second = 2;
        var G[bool]* gb = new(G[bool]);
        var G[Pair[int, int]]* gp = new(G[Pair[int, int]]);
        var G[int]* gi = new(G[int]);
        type(File).StdOut.WriteStr(gb.F(true, 4){str} + " " + gp.F(p, 5){str} + " " + gi.F(3, 6){str} + " " + gp.v.second{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "6 10 15 2\n", stdout)
}

// TestGenericStructMethods calls a generic struct's methods under
// instantiations of different widths, each with a body of its own.
func TestGenericStructMethods(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct Box[A] {
        var A v;
        var int n;

        fun str() Show = () {
            var A copy = this.v;
            return copy{str} + "/" + this.n{str};
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Box[bool] b;
        b.v = true;
        b.n = 7;
        var Box[str] s;
        s.v = "hi";
        s.n = 9;
        var str none;
        type(File).StdOut.WriteStr(b.Show() + " " + s.Show() + " " + (none == "x"){str} + (none == none){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true/7 hi/9 falsetrue\n", stdout)
}

// TestSubclassesOfGenericInstantiations calls methods inherited from a generic
// class on a plain subclass, a generic subclass, and a subclass two generic
// levels down, collecting while the inherited Mark is all that holds their
// elements.
func TestSubclassesOfGenericInstantiations(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Vec <: Arr[flt]* {
        static fun Vec*(uint, flt...) New = (len, args) {
            var Vec* v = new(Vec);
            v.Init(len, args...);
            return v;
        }

        fun flt() Sum = () {
            var flt s = 0.0;
            for(var uint i = 0U; i < this.Len(); i++) {
                s = s + this.Get(i);
            }
            return s;
        }
    }

    class Named[X] <: Arr[X]* {
        var str name;

        fun str() Label = () {
            return this.name + this.Str();
        }
    }

    class Tagged <: Named[str]* {
        fun str(uint) At = (i) {
            return this.Get(i) + "!";
        }
    }

    static fun str(int) churn = (n) {
        var str s = "";
        for(var int i = 0; i < n; i++) {
            s = i{str} + "-churn";
        }
        return s;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Vec* v = type(Vec).New(3U, 1.5, 2.0, 3.0);
        v.Set(1U, 4.0);

        var Named[str]* n = new(Named[str]);
        n.Init(2U, "a" + type(Main).churn(1), "b" + type(Main).churn(1));
        n.name = "n";

        var Tagged* t = new(Tagged);
        t.Init(2U, "c" + type(Main).churn(1), "d" + type(Main).churn(1));
        t.name = "t";

        type(Main).churn(5000);
        type(File).StdOut.WriteStr(v.Sum(){str} + " " + v.Len(){str} + " " + v{str} + " " + n.Label() + " " + t.Label() + " " + t.At(1U) + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "8.5 3 [1.5, 4, 3] n[a0-churn, b0-churn] t[c0-churn, d0-churn] d0-churn!\n", stdout)
}

// TestPointerDowncasts reads an Obj* back as the class it holds, and as a
// class it does not hold, which answers null.
func TestPointerDowncasts(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Cat {
        var int lives;
    }

    class Dog {
        var int barks;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Cat* c = new(Cat);
        c.lives = 9;
        var Obj* o = c;
        var Cat* back = o{Cat*};
        var Dog* wrong = o{Dog*};
        type(File).StdOut.WriteStr(back.lives{str} + " " + (wrong{void*} == null){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "9 true\n", stdout)
}

// TestNullCastsToTheNullStr pins that a null of every reference kind casts to
// the null str rather than to text describing it.
func TestNullCastsToTheNullStr(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Box {
        var int n;
    }

    interface Shape {
        virtual fun int() Sides;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Box* box = null;
        var Shape* shape = null;
        var any a = null;
        var void() f = null;
        var str s = null;
        var str unset;
        var bool ok = box{str} == null && shape{str} == null && a{str} == null && f{str} == null &&
            s{str} == null && s == null && unset == null && null{str} == null;
        var Box* full = new(Box);
        ok = ok && full{str} != null && 0{str} == "0";
        type(File).StdOut.WriteStr(ok{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true\n", stdout)
}

// TestArrsOf builds raw runs of primitives, strings and structs from their
// elements, and an empty one as null.
func TestArrsOf(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct Point {
        var int x;
        var int y;
    }

    static fun Point(int, int) point = (x, y) {
        var Point p;
        p.x = x;
        p.y = y;
        return p;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int* ints = type(Arrs)[int].Of(3, 1, 4, 1, 5);
        var int sum = 0;
        for(var uint i = 0U; i < 5U; i++) {
            sum = sum + ints[i];
        }
        free(ints);

        type(System.GC).Block();
        var str* words = type(Arrs)[str].Of("raw", "run");
        var str joined = words[0] + " " + words[1];
        free(words);
        type(System.GC).Unblock();

        var Point* points = type(Arrs)[Point].Of(type(Main).point(1, 2), type(Main).point(3, 4));
        var int span = points[1].x + points[1].y - points[0].x;
        free(points);

        var int* none = type(Arrs)[int].Of();
        type(File).StdOut.WriteStr(sum{str} + " " + joined + " " + span{str} + " " + (none == null){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "14 raw run 6 true\n", stdout)
}
