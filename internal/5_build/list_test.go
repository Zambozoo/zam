package build_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListsAgree runs the same operations against every std list, then a long
// run of random inserts, removals and updates, and expects identical results.
func TestListsAgree(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    interface Iter[A] {
        virtual fun Pair[A, bool]() Next;
        virtual fun Pair[A, bool]() Prev;
    }

    interface List[A] {
        virtual fun uint() Len;
        virtual fun void() Clear;
        virtual fun A() GetFirst;
        virtual fun A() GetLast;
        virtual fun A(uint) Get;
        virtual fun A(A) SetFirst;
        virtual fun A(A) SetLast;
        virtual fun A(uint, A) Set;
        virtual fun void(A) AddFirst;
        virtual fun void(A) AddLast;
        virtual fun void(uint, A) Add;
        virtual fun A() RemoveFirst;
        virtual fun A() RemoveLast;
        virtual fun A(uint) Remove;
        virtual fun Arr[A]*() Arr;
        virtual fun Iter[A]*() Iter;
    }

    static fun str(List[str]*) run = (l) {
        for(var int i = 0; i < 5; i++) {
            l.AddLast(i{str});
        }
        l.AddFirst("a");
        l.Add(3U, "b");
        var str s = l{str};
        s = s + " " + l.SetFirst("c") + l.SetLast("d") + l.Set(1U, "e");
        s = s + " " + l.GetFirst() + l.GetLast() + l.Get(3U) + " " + l{str};
        s = s + " " + l.RemoveFirst() + l.RemoveLast() + l.Remove(2U) + " " + l.Len(){str} + " " + l.Arr(){str} + " ";

        var Iter[str]* it = l.Iter();
        for(var Pair[str, bool] p = it.Next(); p.second; p = it.Next()) {
            s = s + p.first;
        }
        for(var Pair[str, bool] p = it.Prev(); p.second; p = it.Prev()) {
            s = s + p.first;
        }

        l.Clear();
        var uint x = 7U;
        for(var int i = 0; i < 3000; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint r = x >> 33U;
            if(l.Len() == 0U || r % 3U != 0U) {
                l.Add(r % (l.Len() + 1U), i{str});
            } else if(r % 2U == 0U) {
                l.Remove(r % l.Len());
            } else {
                l.Set(r % l.Len(), "s" + i{str});
            }
        }
        var str got = "";
        for(var uint i = 0U; i < l.Len(); i++) {
            got = got + l.Get(i) + ",";
        }
        return s + "\n" + l.Len(){str} + " " + l{str} + " " + got + "\n";
    }

    fun void(uint, str...) main = (argC, argV) {
        type(File).StdOut.WriteStr(type(Main).run(type(ArrList)[str].New(0U)));
        type(File).StdOut.WriteStr(type(Main).run(type(LinkList)[str].New(0U)));
        type(File).StdOut.WriteStr(type(Main).run(type(SkipList)[str].New(0U)));
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	require.Len(t, lines, 6)
	for i := 0; i < 6; i += 2 {
		assert.Equal(t, "[a, 0, 1, b, 2, 3, 4] a40 cdb [c, e, 1, b, 2, 3, d] cdb 4 [e, 1, 2, 3] e123321e", lines[i])
	}
	assert.Equal(t, lines[1], lines[3], "LinkList")
	assert.Equal(t, lines[1], lines[5], "SkipList")
}

// TestBitListMatchesArrList runs the same operations against a BitList and an
// ArrList of bools, past several word boundaries and regrowths, and expects
// them to agree on every element throughout.
func TestBitListMatchesArrList(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    static fun bool(BitList*, ArrList[bool]*) same = (b, a) {
        if(b.Len() != a.Len()) {
            return false;
        }
        for(var uint i = 0U; i < a.Len(); i++) {
            if(b.Get(i) != a.Get(i)) {
                return false;
            }
        }
        return true;
    }

    fun void(uint, str...) main = (argC, argV) {
        var BitList* b = type(BitList).New(3U, true, false, true);
        b.AddFirst(false);
        b.AddLast(true);
        b.Add(2U, true);
        var str s = b{str};
        s = s + " " + b.SetFirst(true){str} + b.SetLast(false){str} + b.Set(1U, true){str} + " " + b{str};
        s = s + " " + b.RemoveFirst(){str} + b.RemoveLast(){str} + b.Remove(1U){str} + " " + b.Len(){str} + " " + b.Arr(){str} + " ";
        var Iter[bool]* it = b.Iter();
        for(var Pair[bool, bool] p = it.Next(); p.second; p = it.Next()) {
            s = s + p.first{str} + ",";
        }
        for(var Pair[bool, bool] p = it.Prev(); p.second; p = it.Prev()) {
            s = s + p.first{str} + ",";
        }

        b.Clear();
        var ArrList[bool]* a = type(ArrList)[bool].New(0U);
        var bool agree = true;
        var uint x = 11U;
        for(var int i = 0; i < 4000; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint r = x >> 33U;
            var bool v = (r & 1U) == 1U;
            var uint op = (r >> 1U) % 8U;
            if(a.Len() == 0U || op < 3U) {
                var uint at = (r >> 4U) % (a.Len() + 1U);
                a.Add(at, v);
                b.Add(at, v);
            } else if(op == 3U) {
                a.AddFirst(v);
                b.AddFirst(v);
            } else if(op == 4U) {
                a.AddLast(v);
                b.AddLast(v);
            } else if(op == 5U) {
                var uint at = (r >> 4U) % a.Len();
                agree = agree && a.Remove(at) == b.Remove(at);
            } else if(op == 6U) {
                agree = agree && a.RemoveFirst() == b.RemoveFirst();
            } else {
                var uint at = (r >> 4U) % a.Len();
                agree = agree && a.Set(at, v) == b.Set(at, v);
            }
        }
        agree = agree && type(Main).same(b, a);
        type(File).StdOut.WriteStr(s + "\n" + agree{str} + " " + (b.Len() > 64U){str} + "\n");
    }

    interface Iter[A] {
        virtual fun Pair[A, bool]() Next;
        virtual fun Pair[A, bool]() Prev;
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "[false, true, true, false, true, true] false"+
		"truetrue [true, true, true, false, true, false] truefalsetrue 3 [true, false, true] "+
		"true,false,true,true,false,true,\ntrue true\n", stdout)
}
