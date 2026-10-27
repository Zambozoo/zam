package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHeapsAgree runs random adds and removals from both ends against every std
// heap and priority queue and a reference multiset, then drains each in order,
// and merges two of each kind.
func TestHeapsAgree(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/heap/*");

namespace Main {
    interface Iter[A] {
        virtual fun Pair[A, bool]() Next;
        virtual fun Pair[A, bool]() Prev;
    }

    interface Heap[A] {
        virtual fun uint() Len;
        virtual fun void(A) Add;
        virtual fun A() GetMin;
        virtual fun A() GetMax;
        virtual fun A() RemoveMin;
        virtual fun A() RemoveMax;
        virtual fun void() Clear;
        virtual fun Arr[A]*() Arr;
        virtual fun Iter[A]*() Iter;
    }

    interface Queue[A, B] {
        virtual fun uint() Len;
        virtual fun void(A, B) Add;
        virtual fun Pair[A, B]() GetMin;
        virtual fun Pair[A, B]() GetMax;
        virtual fun Pair[A, B]() RemoveMin;
        virtual fun Pair[A, B]() RemoveMax;
    }

    static var int size = 100;

    static fun int(uint*) least = (counts) {
        for(var int v = 0; v < type(Main).size; v++) {
            if(counts[v{uint}] > 0U) {
                return v;
            }
        }
        return -1;
    }

    static fun int(uint*) greatest = (counts) {
        for(var int v = type(Main).size - 1; v >= 0; v--) {
            if(counts[v{uint}] > 0U) {
                return v;
            }
        }
        return -1;
    }

    static fun bool(Heap[int]*) run = (h) {
        var uint* counts = new(uint, type(Main).size{uint});
        var uint len = 0U;
        var bool ok = true;
        var uint x = 13U;
        for(var int i = 0; i < 6000; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint r = x >> 33U;
            var uint op = r % 5U;
            if(len == 0U || op < 2U) {
                var int v = ((r >> 8U) % type(Main).size{uint}){int};
                h.Add(v);
                counts[v{uint}]++;
                len++;
            } else if(op == 2U) {
                var int want = type(Main).least(counts);
                ok = ok && h.GetMin() == want && h.RemoveMin() == want;
                counts[want{uint}]--;
                len--;
            } else if(op == 3U) {
                var int want = type(Main).greatest(counts);
                ok = ok && h.GetMax() == want && h.RemoveMax() == want;
                counts[want{uint}]--;
                len--;
            } else {
                ok = ok && h.GetMin() == type(Main).least(counts) && h.GetMax() == type(Main).greatest(counts);
            }
            ok = ok && h.Len() == len;
        }

        var uint visited = 0U;
        var Iter[int]* it = h.Iter();
        for(var Pair[int, bool] p = it.Next(); p.second; p = it.Next()) {
            visited++;
        }
        ok = ok && visited == len && h.Arr().Len() == len;

        var int last = -1;
        for(; h.Len() > 0U;) {
            var int v = h.RemoveMin();
            ok = ok && last <= v;
            last = v;
        }
        h.Add(3);
        h.Clear();
        return ok && h.Len() == 0U;
    }

    static fun bool(Queue[int, int]*) queue = (q) {
        var bool ok = true;
        var uint x = 17U;
        for(var int i = 0; i < 500; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var int k = ((x >> 33U) % 50U){int};
            q.Add(k, k * 7);
        }
        var int last = 100;
        for(; q.Len() > 0U;) {
            var Pair[int, int] e = q.RemoveMax();
            ok = ok && e.second == e.first * 7 && e.first <= last;
            last = e.first;
        }
        q.Add(4, 1);
        q.Add(2, 2);
        q.Add(9, 3);
        return ok && q.GetMin().second == 2 && q.GetMax().second == 3 && q.RemoveMin().first == 2 && q.Len() == 2U;
    }

    // merged checks a heap that two others were merged into: every element
    // comes out, in order, and the merged-from heap is left empty and usable.
    static fun bool(Heap[int]*, Heap[int]*) merged = (into, from) {
        var bool ok = from.Len() == 0U && into.Len() == 60U && into.GetMin() == 0 && into.GetMax() == 59;
        var int last = -1;
        for(; into.Len() > 0U;) {
            var int v = into.RemoveMin();
            ok = ok && v == last + 1;
            last = v;
        }
        from.Add(1);
        return ok && last == 59 && from.GetMax() == 1;
    }

    static fun bool(Queue[int, int]*, Queue[int, int]*) mergedQueue = (into, from) {
        var bool ok = from.Len() == 0U && into.Len() == 60U;
        var int last = -1;
        for(; into.Len() > 0U;) {
            var Pair[int, int] e = into.RemoveMin();
            ok = ok && e.first == last + 1 && e.second == e.first * 7;
            last = e.first;
        }
        return ok && last == 59;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str out = type(Main).run(type(ArrHeap)[int].New(0U)){str} + " ";
        out = out + type(Main).run(type(Treap)[int].New(0U)){str} + " ";
        out = out + type(Main).run(type(PairHeap)[int].New(0U)){str} + " ";
        out = out + type(Main).queue(type(ArrPriorityQueue)[int, int].New(0U)){str} + " ";
        out = out + type(Main).queue(type(TreapPriorityQueue)[int, int].New(0U)){str} + " ";
        out = out + type(Main).queue(type(PairPriorityQueue)[int, int].New(0U)){str} + "\n";

        var ArrHeap[int]* ah = type(ArrHeap)[int].New(0U);
        var ArrHeap[int]* ao = type(ArrHeap)[int].New(0U);
        var Treap[int]* th = type(Treap)[int].New(0U);
        var Treap[int]* to = type(Treap)[int].New(0U);
        var PairHeap[int]* ph = type(PairHeap)[int].New(0U);
        var PairHeap[int]* po = type(PairHeap)[int].New(0U);
        var ArrPriorityQueue[int, int]* aq = type(ArrPriorityQueue)[int, int].New(0U);
        var ArrPriorityQueue[int, int]* aqo = type(ArrPriorityQueue)[int, int].New(0U);
        var TreapPriorityQueue[int, int]* tq = type(TreapPriorityQueue)[int, int].New(0U);
        var TreapPriorityQueue[int, int]* tqo = type(TreapPriorityQueue)[int, int].New(0U);
        var PairPriorityQueue[int, int]* pq = type(PairPriorityQueue)[int, int].New(0U);
        var PairPriorityQueue[int, int]* pqo = type(PairPriorityQueue)[int, int].New(0U);
        for(var int i = 0; i < 60; i++) {
            var int v = (i * 37) % 60;
            if(i % 3 == 0) {
                ah.Add(v);
                th.Add(v);
                ph.Add(v);
                aq.Add(v, v * 7);
                tq.Add(v, v * 7);
                pq.Add(v, v * 7);
            } else {
                ao.Add(v);
                to.Add(v);
                po.Add(v);
                aqo.Add(v, v * 7);
                tqo.Add(v, v * 7);
                pqo.Add(v, v * 7);
            }
        }
        ah.Merge(ao);
        th.Merge(to);
        ph.Merge(po);
        aq.Merge(aqo);
        tq.Merge(tqo);
        pq.Merge(pqo);
        out = out + type(Main).merged(ah, ao){str} + " " + type(Main).merged(th, to){str} + " " + type(Main).merged(ph, po){str} + " ";
        out = out + type(Main).mergedQueue(aq, aqo){str} + " " + type(Main).mergedQueue(tq, tqo){str} + " " + type(Main).mergedQueue(pq, pqo){str} + "\n";
        out = out + type(Treap)[int].New(3U, 5, 1, 3){str} + " " + type(ArrHeap)[str].New(2U, "b", "a").GetMin() + "\n";
        type(File).StdOut.WriteStr(out);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true true true true true true\ntrue true true true true true\n[1, 3, 5] a\n", stdout)
}
