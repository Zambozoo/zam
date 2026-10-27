package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTriesAgree runs random puts, removes and lookups against both trie maps
// and a string-keyed reference map, then checks entries, order, prefixes and
// the sets built on them.
func TestTriesAgree(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/map/*");
import std("data/trie/*");

namespace Main {
    interface TrieMap[A, B] {
        virtual fun uint() Len;
        virtual fun bool(uint, A*, B) Put;
        virtual fun Pair[B, bool](uint, A*) Get;
        virtual fun bool(uint, A*) Contains;
        virtual fun bool(uint, A*) HasPrefix;
        virtual fun Pair[B, bool](uint, A*) Remove;
        virtual fun void() Clear;
        virtual fun Arr[Pair[Arr[A]*, B]]*() Arr;
    }

    static fun str(Arr[char]*) text = (a) {
        return type(Strs).ToChars(a.Arr(), a.Len(){int});
    }

    static fun bool(TrieMap[char, int]*, bool) run = (t, ordered) {
        var HashMap[str, int]* ref = type(HashMap)[str, int].New(0U);
        var char* k = new(char, 8U);
        var bool ok = true;
        var uint x = 21U;
        for(var int i = 0; i < 6000; i++) {
            x = x * 6364136223846793005U + 1442695040888963407U;
            var uint r = x >> 33U;
            var uint n = r % 7U;
            for(var uint j = 0U; j < n; j++) {
                k[j] = (97 + ((r >> (3U + 2U * j)) % 3U){int}){char};
            }
            var str s = type(Strs).ToChars(k, n{int});
            var uint op = (r >> 27U) % 3U;
            if(op == 0U) {
                ok = ok && t.Put(n, k, i) == ref.Put(s, i);
            } else if(op == 1U) {
                var Pair[int, bool] a = t.Remove(n, k);
                var Pair[int, bool] b = ref.Remove(s);
                ok = ok && a.second == b.second && (!a.second || a.first == b.first);
            } else {
                var Pair[int, bool] a = t.Get(n, k);
                var Pair[int, bool] b = ref.Get(s);
                ok = ok && a.second == b.second && (!a.second || a.first == b.first) && t.Contains(n, k) == b.second;
            }
            ok = ok && t.Len() == ref.Len();
        }

        var Arr[Pair[Arr[char]*, int]]* entries = t.Arr();
        ok = ok && entries.Len() == ref.Len();
        for(var uint i = 0U; i < entries.Len(); i++) {
            var str key = type(Main).text(entries.Get(i).first);
            var Pair[int, bool] want = ref.Get(key);
            ok = ok && want.second && want.first == entries.Get(i).second;
            if(ordered && i > 0U) {
                ok = ok && type(Main).text(entries.Get(i - 1U).first) < key;
            }
        }

        t.Clear();
        k[0] = 'a';
        k[1] = 'b';
        k[2] = 'c';
        k[3] = 'd';
        ok = ok && t.Len() == 0U && !t.HasPrefix(0U, k);
        t.Put(3U, k, 1);
        ok = ok && t.HasPrefix(0U, k) && t.HasPrefix(2U, k) && t.HasPrefix(3U, k) && !t.HasPrefix(4U, k);
        t.Put(0U, k, 9);
        ok = ok && t.Get(0U, k).first == 9 && t.Len() == 2U && t.Remove(0U, k).second && t.Len() == 1U;
        return ok;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str out = type(Main).run(type(HashTrieMap)[char, int].New(), false){str} + " ";
        out = out + type(Main).run(type(TreeTrieMap)[char, int].New(), true){str} + "\n";

        var char* k = new(char, 3U);
        k[0] = 'b';
        k[1] = 'a';
        k[2] = 'd';
        var TreeTrieSet[char]* ts = type(TreeTrieSet)[char].New();
        var HashTrieSet[char]* hs = type(HashTrieSet)[char].New();
        ts.Add(3U, k);
        ts.Add(2U, k);
        ts.Add(1U, k);
        hs.Add(3U, k);
        k[0] = 'a';
        ts.Add(1U, k);
        out = out + ts{str} + " " + (ts.Add(1U, k) == false){str} + " " + ts.Remove(3U, k){str} + " " + hs.Contains(3U, k){str} + " " + ts.Len(){str} + "\n";
        type(File).StdOut.WriteStr(out);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true true\n{[a], [b], [b, a], [b, a, d]} true false false 4\n", stdout)
}
