package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMapsAgree runs random puts, gets and removes against every std map and
// a reference table, then checks contents, order and iteration.
func TestMapsAgree(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/map/*");

namespace Main {
    interface Iter[A] {
        virtual fun (A, bool)() Next;
        virtual fun (A, bool)() Prev;
    }

    interface Map[A, B] {
        virtual fun uint() Len;
        virtual fun bool(A, B) Put;
        virtual fun (B, bool)(A) Get;
        virtual fun bool(A) Contains;
        virtual fun (B, bool)(A) Remove;
        virtual fun void() Clear;
        virtual fun Arr[(A, B)]*() Arr;
        virtual fun Iter[(A, B)]*() Iter;
    }

    interface Sorted[A, B] {
        virtual fun (A, B)() GetFirst;
        virtual fun (A, B)() GetLast;
    }

    static var int size = 200;

    // run checks a map against the reference after random operations, and
    // whether iteration visits each entry once, forwards and back.
    static fun bool(Map[int, int]*) run = (m) {
        var bool ok = m.Put(5, 50) && !m.Put(5, 51) && m.Get(5).0 == 51 && m.Contains(5);
        var (int, bool) gone = m.Remove(5);
        ok = ok && gone.1 && gone.0 == 51 && !m.Remove(5).1 && !m.Get(5).1;

        var bool* has = new(bool, Main::size{uint});
        var int* vals = new(int, Main::size{uint});
        var uint count = 0U;
        var uint x = 5U;
        for(var int i = 0; i < 5000; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint r = x >> 33U;
            var int k = (r % Main::size{uint}){int};
            var uint op = (r >> 8U) % 3U;
            if(op == 0U) {
                ok = ok && m.Put(k, i) == !has[k{uint}];
                if(!has[k{uint}]) {
                    count++;
                }
                has[k{uint}] = true;
                vals[k{uint}] = i;
            } else if(op == 1U) {
                var (int, bool) p = m.Remove(k);
                ok = ok && p.1 == has[k{uint}] && (!p.1 || p.0 == vals[k{uint}]);
                if(has[k{uint}]) {
                    count--;
                }
                has[k{uint}] = false;
            } else {
                var (int, bool) p = m.Get(k);
                ok = ok && p.1 == has[k{uint}] && (!p.1 || p.0 == vals[k{uint}]) && m.Contains(k) == has[k{uint}];
            }
        }
        ok = ok && m.Len() == count;

        var bool* seen = new(bool, Main::size{uint});
        var uint forward = 0U;
        var Iter[(int, int)]* it = m.Iter();
        for(var ((int, int), bool) p = it.Next(); p.1; p = it.Next()) {
            var uint k = p.0.0{uint};
            ok = ok && has[k] && !seen[k] && vals[k] == p.0.1;
            seen[k] = true;
            forward++;
        }
        var uint backward = 0U;
        for(var ((int, int), bool) p = it.Prev(); p.1; p = it.Prev()) {
            ok = ok && has[p.0.0{uint}];
            backward++;
        }
        var Arr[(int, int)]* a = m.Arr();
        ok = ok && forward == count && backward == count && a.Len() == count;
        for(var uint i = 0U; i < a.Len(); i++) {
            ok = ok && vals[a.Get(i).0{uint}] == a.Get(i).1;
        }

        m.Clear();
        ok = ok && m.Len() == 0U && !m.Contains(0) && m.Put(0, 0);
        return ok;
    }

    // sorted checks that a sorted map lists its entries in ascending key order
    // between its first and last.
    static fun bool(Map[int, int]*, Sorted[int, int]*) sorted = (m, o) {
        for(var int i = 0; i < 100; i++) {
            m.Put((i * 37) % 101, i);
        }
        var Arr[(int, int)]* a = m.Arr();
        var bool ok = a.Len() == 100U && o.GetFirst().0 == a.Get(0U).0 && o.GetLast().0 == a.Get(99U).0;
        for(var uint i = 1U; i < a.Len(); i++) {
            ok = ok && a.Get(i - 1U).0 < a.Get(i).0;
        }
        return ok;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str out = Main::run(HashMap::[int, int]New(0U)){str} + " ";
        out = out + Main::run(SkipMap::[int, int]New(0U)){str} + " ";
        out = out + Main::run(TreeMap::[int, int]New(0U)){str} + " ";

        var SkipMap[int, int]* k = SkipMap::[int, int]New(0U);
        out = out + Main::sorted(k, k){str} + " ";
        var TreeMap[int, int]* t = TreeMap::[int, int]New(0U);
        out = out + Main::sorted(t, t){str} + " ";

        var (str, str) e;
        e.0 = "ab";
        e.1 = "x";
        var HashMap[str, str]* h = HashMap::[str, str]New(1U, e);
        out = out + (h.Get("a" + "b").0 == "x" && !h.Put("a" + "b", "y") && h.Get("ab").0 == "y" && h.Len() == 1U){str} + "\n";
        out = out + h{str} + " " + t.Len(){str} + "\n";
        File::StdOut.WriteStr(out);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true true true true true true\n{ab: y} 100\n", stdout)
}
