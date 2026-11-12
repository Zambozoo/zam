package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArenaKeepsWhatItHolds lays classes, structs, primitives and tailed
// values in an arena and reads them back after many collections, the strings
// they hold being reachable only through the arena.
func TestArenaKeepsWhatItHolds(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Named {
        var str name = "unnamed";
        var int n;
    }

    struct Pair {
        var str s;
        var int i;
    }

    struct Run~ {
        var uint len;
        var int[~] vals;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Arena~* a = Arena::New(4096U);
        var Named* first = Arena::[Named]Alloc(a, 1U);
        var Named* many = Arena::[Named]Alloc(a, 3U);
        var Pair* p = Arena::[Pair]Alloc(a, 1U);
        var int* ints = Arena::[int]Alloc(a, 4U);
        var Run~3* r = Arena::[Run~3]Alloc(a, 1U);

        var str defaulted = first.name;
        first.name = 7{str} + "-first";
        for(var uint i = 0U; i < 3U; i++) {
            many[i].name = i{str} + "-many";
            many[i].n = i{int};
            ints[i] = i{int} * 10;
        }
        p.s = 9{str} + "-pair";
        r.len = 3U;
        for(var uint i = 0U; i < 3U; i++) {
            r.vals[i] = i{int} + 100;
        }

        var str t = "";
        for(var int i = 0; i < 3000; i++) {
            t = i{str} + "-churn";
        }

        File::StdOut.WriteStr(defaulted + " " + first.name + " " + many[2].name + " " + many[1].n{str} + " " + p.s + " " + ints[2]{str} + " " + r.vals[2]{str} + "\n");
        File::StdOut.WriteStr((a.Used() > 0U){str} + " " + (a.Used() <= a.Size()){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "unnamed 7-first 2-many 1 9-pair 20 102\ntrue true\n", stdout)
}

// TestArenaAnswersNullWhenFull asks a small arena for more than it holds, and
// for nothing at all.
func TestArenaAnswersNullWhenFull(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var Arena~* a = Arena::New(64U);
        var int* fits = Arena::[int]Alloc(a, 4U);
        var int* full = Arena::[int]Alloc(a, 1U);
        var int* none = Arena::[int]Alloc(a, 0U);
        var Arena~* empty = Arena::New(0U);
        var int* nothing = Arena::[int]Alloc(empty, 1U);
        File::StdOut.WriteStr((fits != null){str} + " " + (full == null){str} + " " + (none == null){str} + " " + (nothing == null){str} + " " + a.Used(){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true true true true 64\n", stdout)
}

// TestArenaLivesThroughItsValues drops every reference to an arena but one to
// a value inside it, which keeps the arena and its neighbours alive; once that
// goes too, the next major collection collects the arena and deletes each
// class value in it.
func TestArenaLivesThroughItsValues(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static var int deleted = 0;

    class Counted {
        var str name;
        var Counted* other;

        public fun void() Delete = () {
            Main::deleted++;
        }
    }

    static fun Counted*() build = () {
        var Arena~* a = Arena::New(1024U);
        var Counted* c = Arena::[Counted]Alloc(a, 1U);
        var Counted* d = Arena::[Counted]Alloc(a, 1U);
        c.name = 1{str} + "-c";
        d.name = 2{str} + "-d";
        c.other = d;
        d.other = c;
        return c;
    }

    static fun void() churn = () {
        var str t = "";
        for(var int i = 0; i < 3000; i++) {
            t = i{str} + "-churn";
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Counted* c = Main::build();
        Main::churn();
        File::StdOut.WriteStr(c.other.name + " " + c.other.other.name + " " + Main::deleted{str} + "\n");
        c = null;
        Main::churn();
        System.GC::Major();
        File::StdOut.WriteStr(Main::deleted{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "2-d 1-c 0\n2\n", stdout)
}

// TestArenaRefusesDeletingItsValues deletes a value laid out in an arena,
// which has no allocation of its own to release.
func TestArenaRefusesDeletingItsValues(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");

namespace Main {
    class Plain {
        var int n;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Arena~* a = Arena::New(1024U);
        var Plain* p = Arena::[Plain]Alloc(a, 1U);
        delete(p);
    }
}
`)
	_, stderr, status := run(t, binary)
	assert.NotZero(t, status)
	assert.Contains(t, stderr, "deleted a member of another object")
}

// TestArenaResetDeletesAndReuses resets an arena holding class values, which
// deletes each of them and lays the next value where the first one was, with
// none of what the first one held.
func TestArenaResetDeletesAndReuses(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static var int deleted = 0;

    class Counted {
        var str name = "fresh";

        public fun void() Delete = () {
            Main::deleted++;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Arena~* a = Arena::New(1024U);
        var Counted* first = Arena::[Counted]Alloc(a, 1U);
        var Counted* many = Arena::[Counted]Alloc(a, 3U);
        var int* ints = Arena::[int]Alloc(a, 2U);
        first.name = 1{str} + "-old";
        ints[1] = 5;
        var void* at = first{void*};

        a.Reset();
        var uint usedAfter = a.Used();
        var Counted* again = Arena::[Counted]Alloc(a, 1U);
        var int* zeroed = Arena::[int]Alloc(a, 2U);

        var str t = "";
        for(var int i = 0; i < 3000; i++) {
            t = i{str} + "-churn";
        }

        File::StdOut.WriteStr(Main::deleted{str} + " " + usedAfter{str} + " " + (again{void*} == at){str} + " " + again.name + " " + zeroed[1]{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "2 0 true fresh 0\n", stdout)
}
