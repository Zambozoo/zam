package build_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcStepPrelude is what every stepped case is written with: a box, and the
// natives that step a concurrent collection. trace begins one and returns once
// the marker has traced everything it will before its remark, which waits;
// finish lets the remark run and waits for the sweep. What a case changes
// between the two is what only the remark can find.
const gcStepPrelude = `import std("lang/*");
import std("io/local/*");
import std("data/list/*");
import std("data/map/*");

namespace Main {
    class Box {
        var str name;
    }

    static fun Box*(str) box = (name) {
        var Box* b = new(Box);
        b.name = name;
        return b;
    }

    static fun void() trace = () {
        native(GC_STEP_TRACE){void()}();
    }

    static fun void() finish = () {
        native(GC_STEP_FINISH){void()}();
    }

    // age takes what the program holds through a whole collection, which
    // leaves it old.
    static fun void() age = () {
        Main::trace();
        Main::finish();
    }

    static fun bool(Box*) kept = (b) {
        var bool listed;
        native(GC_STEP_LISTED){void(void*, bool*)}(b{void*}, &listed);
        return listed && b.name != null;
    }
`

// compileStepped builds a case, written after gcStepPrelude, with the stepping
// natives and a concurrent collector.
func compileStepped(t *testing.T, source string) string {
	t.Helper()
	natives, err := os.ReadFile("testdata/gc_step.c")
	require.NoError(t, err)
	return compileWithNativesAndFlags(t, []string{"--gc-concurrent=true"}, gcStepPrelude+source+"}\n",
		map[string]string{"gc_step.c": string(natives)})
}

// Each case changes, between the marker's tracing and its remark, what holds a
// box allocated meanwhile, so that only what the remark rescans says the box
// is held: a dirtied card, a Mark of the program's own run again against what
// memory holds then, or a container grown or rearranged.
func TestRemarkFindsWhatChangedWhileTracing(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		source string
		want   string
		// remarked is what the remark's statistics show it rescanned.
		remarked string
	}{
		"a card the store dirtied": {`
    class Node {
        var Box* box;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Node* node = new(Node);
        Main::age();
        Main::trace();
        var Box* b = Main::box("moved");
        node.box = b;
        b = null;
        Main::finish();
        File::StdOut.WriteStr(Main::kept(node.box){str} + " " + node.box.name + "\n");
    }
`, "true moved\n", ` cards=[1-9]`},
		"a word only a Mark decodes": {`
    class Coded {
        var uint word;

        public fun void() Mark = () {
            if (this.word != 0U) {
                mark((this.word ^ 4099U){void*}{Box*});
            }
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Coded* coded = new(Coded);
        Main::age();
        Main::trace();
        var Box* c = Main::box("coded");
        coded.word = c{uint} ^ 4099U;
        c = null;
        Main::finish();
        var Box* back = (coded.word ^ 4099U){void*}{Box*};
        File::StdOut.WriteStr(Main::kept(back){str} + " " + back.name + "\n");
    }
`, "true coded\n", ` retraced=[1-9]`},
		"memory of the program's own a Mark reads, replaced and freed": {`
    class Slots {
        var uint* at;

        public fun void() Mark = () {
            if (this.at == null) {
                return;
            }
            for (var uint i = 0U; i < 4U; i++) {
                if (this.at[i] != 0U) {
                    mark((this.at[i] ^ 257U){void*}{Box*});
                }
            }
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Slots* slots = new(Slots);
        slots.at = new(uint, 4U);
        for (var uint k = 0U; k < 4U; k++) {
            slots.at[k] = 0U;
        }
        Main::age();
        Main::trace();
        var Box* s = Main::box("slot");
        var uint* old = slots.at;
        var uint* fresh = new(uint, 4U);
        for (var uint k = 0U; k < 4U; k++) {
            fresh[k] = old[k];
        }
        fresh[1] = s{uint} ^ 257U;
        slots.at = fresh;
        s = null;
        free(old);
        Main::finish();
        var Box* back = (slots.at[1] ^ 257U){void*}{Box*};
        File::StdOut.WriteStr(Main::kept(back){str} + " " + back.name + "\n");
    }
`, "true slot\n", ` retraced=[1-9]`},
		"a list grown into a new run": {`
    fun void(uint, str...) main = (argC, argV) {
        var ArrList[Box*]* list = ArrList::[Box*]New(0U);
        list.AddLast(Main::box("0"));
        Main::age();
        Main::trace();
        for (var int i = 1; i < 40; i++) {
            list.AddLast(Main::box(i{str}));
        }
        Main::finish();
        var int bad = 0;
        for (var int i = 0; i < 40; i++) {
            var Box* b = list.Get(i{uint});
            if (!Main::kept(b) || b.name != i{str}) {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + "\n");
    }
`, "0\n", ` cards=[1-9][^\n]* retraced=[1-9]`},
		"maps rehashed, rebalanced and removed from": {`
    fun void(uint, str...) main = (argC, argV) {
        var HashMap[int, Box*]* hashed = HashMap::[int, Box*]New(0U);
        var TreeMap[int, Box*]* sorted = TreeMap::[int, Box*]New(0U);
        for (var int i = 0; i < 10; i++) {
            hashed.Put(i, Main::box("h" + i{str}));
            sorted.Put(i, Main::box("s" + i{str}));
        }
        Main::age();
        Main::trace();
        for (var int i = 10; i < 60; i++) {
            hashed.Put(i, Main::box("h" + i{str}));
            sorted.Put(i, Main::box("s" + i{str}));
        }
        for (var int i = 0; i < 10; i++) {
            hashed.Remove(i);
            sorted.Remove(i);
        }
        Main::finish();
        var int bad = 0;
        for (var int i = 10; i < 60; i++) {
            var Box* h = hashed.Get(i).0;
            var Box* s = sorted.Get(i).0;
            if (!Main::kept(h) || h.name != "h" + i{str} || !Main::kept(s) || s.name != "s" + i{str}) {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + " " + hashed.Len(){str} + " " + sorted.Len(){str} + "\n");
    }
`, "0 50 50\n", ` cards=[1-9][^\n]* retraced=[1-9]`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			binary := compileStepped(t, test.source)
			stdout, stderr, status := runWith(t, []string{"ZAM_GC_STATS=1", "ZAM_GC_VERIFY=1"}, binary)
			require.Zero(t, status, stderr)
			assert.Equal(t, test.want, stdout)
			var remark string
			for line := range strings.SplitSeq(stderr, "\n") {
				if strings.Contains(line, " concurrent ") {
					remark = line
				}
			}
			assert.Regexp(t, test.remarked, remark)
		})
	}
}
