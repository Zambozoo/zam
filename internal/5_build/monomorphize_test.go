package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPartlyOpenInstantiationAlone declares a template that holds a partly open
// instantiation, Pair[int, E], and never instantiates either ground. Nothing is
// emitted for an instantiation that still holds an open argument, so the types
// its binding would ground are never asked for.
func TestPartlyOpenInstantiationAlone(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct Pair[K, V] {
        var K k;
        var V v;

        fun K() First = () {
            var K(K) same = lambda K(K): (x) { return x; };
            return same(this.k);
        }
    }

    class Holder[E] {
        var Pair[int, E] p;

        fun int() First = () {
            return this.p.First();
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr("ok\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "ok\n", stdout)
}

// TestPartlyOpenInstantiationGrounded is the program above with the outer
// template instantiated, which grounds Pair[int, str] through the binding of
// Holder[str] and so asks for the types Pair's body names under it.
func TestPartlyOpenInstantiationGrounded(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct Pair[K, V] {
        var K k;
        var V v;

        fun K() First = () {
            var K(K) same = lambda K(K): (x) { return x; };
            return same(this.k);
        }
    }

    class Holder[E] {
        var Pair[int, E] p;

        fun int() First = () {
            return this.p.First();
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Holder[str]* h = new(Holder[str]);
        h.p.k = 7;
        h.p.v = "x";
        File::StdOut.WriteStr(h.First(){str} + h.p.v + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "7x\n", stdout)
}
