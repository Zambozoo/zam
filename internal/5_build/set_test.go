package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetsAgree runs random adds, removes and lookups against every std set and
// a reference table, then checks contents, order and iteration.
func TestSetsAgree(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/set/*");

namespace Main {
    interface Iter[A] {
        virtual fun Pair[A, bool]() Next;
        virtual fun Pair[A, bool]() Prev;
    }

    interface Set[A] {
        virtual fun uint() Len;
        virtual fun bool(A) Add;
        virtual fun bool(A) Contains;
        virtual fun bool(A) Remove;
        virtual fun void() Clear;
        virtual fun Arr[A]*() Arr;
        virtual fun Iter[A]*() Iter;
    }

    interface Sorted[A] {
        virtual fun A() GetFirst;
        virtual fun A() GetLast;
    }

    static var int size = 200;

    // run checks a set against the reference after random operations, and
    // whether iteration visits each member once, forwards and back.
    static fun bool(Set[int]*) run = (s) {
        var bool ok = s.Add(5) && !s.Add(5) && s.Contains(5) && s.Remove(5) && !s.Remove(5) && !s.Contains(5);
        var bool* ref = new(bool, type(Main).size{uint});
        var uint count = 0U;
        var uint x = 3U;
        for(var int i = 0; i < 5000; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint r = x >> 33U;
            var int v = (r % type(Main).size{uint}){int};
            var uint op = (r >> 8U) % 3U;
            if(op == 0U) {
                ok = ok && s.Add(v) == !ref[v{uint}];
                if(!ref[v{uint}]) {
                    count++;
                }
                ref[v{uint}] = true;
            } else if(op == 1U) {
                ok = ok && s.Remove(v) == ref[v{uint}];
                if(ref[v{uint}]) {
                    count--;
                }
                ref[v{uint}] = false;
            } else {
                ok = ok && s.Contains(v) == ref[v{uint}];
            }
        }
        ok = ok && s.Len() == count;

        var bool* seen = new(bool, type(Main).size{uint});
        var uint forward = 0U;
        var Iter[int]* it = s.Iter();
        for(var Pair[int, bool] p = it.Next(); p.second; p = it.Next()) {
            ok = ok && ref[p.first{uint}] && !seen[p.first{uint}];
            seen[p.first{uint}] = true;
            forward++;
        }
        var uint backward = 0U;
        for(var Pair[int, bool] p = it.Prev(); p.second; p = it.Prev()) {
            ok = ok && ref[p.first{uint}];
            backward++;
        }
        ok = ok && forward == count && backward == count && s.Arr().Len() == count;

        s.Clear();
        ok = ok && s.Len() == 0U && !s.Contains(0) && s.Add(0);
        return ok;
    }

    // sorted checks that a sorted set lists its members in ascending order
    // between its first and last.
    static fun bool(Set[int]*, Sorted[int]*) sorted = (s, o) {
        for(var int i = 0; i < 100; i++) {
            s.Add((i * 37) % 101);
        }
        var Arr[int]* a = s.Arr();
        var bool ok = a.Len() == 100U && o.GetFirst() == a.Get(0U) && o.GetLast() == a.Get(99U);
        for(var uint i = 1U; i < a.Len(); i++) {
            ok = ok && a.Get(i - 1U) < a.Get(i);
        }
        var Iter[int]* it = s.Iter();
        var int prev = -1;
        for(var Pair[int, bool] p = it.Next(); p.second; p = it.Next()) {
            ok = ok && prev < p.first;
            prev = p.first;
        }
        return ok;
    }

    // blackHeight returns the black height of a left-leaning red-black
    // subtree, or -1 if it breaks an invariant.
    static fun int(TreeSet.Node[int]*) blackHeight = (n) {
        if(n == null) {
            return 0;
        }
        if(n.right != null && n.right.red) {
            return -1;
        }
        if(n.red && n.left != null && n.left.red) {
            return -1;
        }
        var int l = type(Main).blackHeight(n.left);
        var int r = type(Main).blackHeight(n.right);
        if(l < 0 || l != r) {
            return -1;
        }
        if(n.red) {
            return l;
        }
        return l + 1;
    }

    static fun bool() balanced = () {
        var TreeSet[int]* t = type(TreeSet)[int].New(0U);
        var bool ok = true;
        var uint x = 9U;
        for(var int i = 0; i < 3000; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint r = x >> 33U;
            var int v = (r % 500U){int};
            if(((r >> 9U) & 1U) == 0U) {
                t.Add(v);
            } else {
                t.Remove(v);
            }
            if(i % 100 == 0) {
                ok = ok && type(Main).blackHeight(t.root) >= 0;
            }
        }
        return ok && type(Main).blackHeight(t.root) >= 0 && (t.root == null || !t.root.red);
    }

    fun void(uint, str...) main = (argC, argV) {
        var str out = type(Main).run(type(HashSet)[int].New(0U)){str} + " ";
        out = out + type(Main).run(type(SkipSet)[int].New(0U)){str} + " ";
        out = out + type(Main).run(type(TreeSet)[int].New(0U)){str} + " ";

        var SkipSet[int]* k = type(SkipSet)[int].New(0U);
        out = out + type(Main).sorted(k, k){str} + " ";
        var TreeSet[int]* t = type(TreeSet)[int].New(0U);
        out = out + type(Main).sorted(t, t){str} + " ";

        var HashSet[str]* h = type(HashSet)[str].New(2U, "ab", "cd");
        out = out + (h.Contains("a" + "b") && !h.Add("c" + "d") && h.Len() == 2U){str};
        out = out + " " + type(Main).balanced(){str};
        type(File).StdOut.WriteStr(out + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true true true true true true true\n", stdout)
}
