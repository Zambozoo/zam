package build_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
)

// compile builds a one-file project into a binary whose heap starts small
// enough that the program collects many times over.
func compile(t *testing.T, source string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"gc\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(source), 0o600))
	out := filepath.Join(dir, "out")

	files := &base.FileProvider{}
	cmd, err := command.New([]string{"zam", "-m", "16KB", "-o", out, entry}, files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewGitProvider(files))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	result, err := codegen.GenerateCode(cmd, ast)
	require.NoError(t, err)
	require.NoError(t, build.Build(result.Image, result.NativeNames, build.Options{
		OutputPath: out,
		Project:    ast.MainCodeDecl.File().Project,
	}))
	return out
}

// run runs a binary with args to its end and returns what it wrote and how it
// exited.
func run(t *testing.T, binary string, args ...string) (stdout, stderr string, status int) {
	t.Helper()

	out, err := exec.Command(binary, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), string(exit.Stderr), exit.ExitCode()
	}
	require.NoError(t, err)
	return string(out), "", 0
}

// TestCollectionKeepsWhatIsReachable allocates far past the heap while holding
// values through every kind of root, and reads them all back afterwards.
func TestCollectionKeepsWhatIsReachable(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    static var ArrList[str]* kept = null;
    static var int deleted = 0;

    class Box {
        var str name;
        var any payload;
        var int(int) f;

        fun void() Delete = () {
            Main::deleted++;
        }
    }

    static fun Box*(int) box = (i) {
        var str s = i{str} + "-box";
        var Box* b = new(Box);
        b.name = s;
        b.payload = s{any};
        b.f = lambda int(int):(x) {
            return x + Strs::ByteLen(s);
        };
        return b;
    }

    fun void(uint, str...) main = (argC, argV) {
        Main::kept = ArrList::[str]New(0U);
        var LinkList[str]* linked = LinkList::[str]New(0U);
        var (str, int) pair;
        pair.0 = "pair-" + 7{str};
        var any held = ("held-" + 8{str}){any};
        var Box* anchor = Main::box(-1);

        for (var int i = 0; i < 5000; i++) {
            var Box* b = Main::box(i);
            if (i % 100 == 0) {
                Main::kept.AddLast(b.name);
                linked.AddLast(b.name + "!");
            }
        }

        File::StdOut.WriteStr(Main::kept.Get(49U) + " " + linked.Get(49U) + " " + pair.0 + " " + held{str} + " " + anchor.name + " " + anchor.f(1){str} + " " + anchor.payload{str} + "\n");
        File::StdOut.WriteStr((Main::deleted > 0){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "4900-box 4900-box! pair-7 held-8 -1-box 7 -1-box\ntrue\n", stdout)
}

// TestCollectionStopsEveryThread collects from whichever thread crosses the
// threshold while the others run, one of them inside GC.Block, and main waits
// in a join.
func TestCollectionStopsEveryThread(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static fun int(int) churn = (n) {
        var int total = 0;
        for (var int i = 0; i < n; i++) {
            var str s = i{str} + "-churn";
            total = total + Strs::ByteLen(s);
        }
        return total;
    }

    static fun int(int) frozen = (n) {
        var int total = 0;
        for (var int i = 0; i < n; i++) {
            System.GC::Block();
            var str s = i{str} + "-frozen";
            total = total + Strs::ByteLen(s);
            System.GC::Unblock();
        }
        return total;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[int] a = async(Main::churn(20000));
        var thread[int] b = async(Main::frozen(20000));
        var thread[int] c = async(Main::churn(20000));
        var int total = wait(a) + wait(b) + wait(c);
        File::StdOut.WriteStr(total{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "646670\n", stdout)
}

// TestBlockingWhileFrozenPanics joins a thread while GC.Block is in effect,
// which could wait on a collection that waits on the frozen thread.
func TestBlockingWhileFrozenPanics(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");

namespace Main {
    static fun int() one = () {
        return 1;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[int] a = async(Main::one());
        System.GC::Block();
        var int x = wait(a);
    }
}
`)
	_, stderr, status := run(t, binary)
	assert.Equal(t, 67, status)
	assert.Contains(t, stderr, "a blocking call was made while GC.Block is in effect")
}

// TestCollectionKeepsATemporaryTarget assigns into an object nothing but the
// assignment's own target reaches, while the value being assigned allocates far
// past the heap. The object is finalized only once the store is done.
func TestCollectionKeepsATemporaryTarget(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static var int assigning = 0;
    static var int early = 0;

    class Holder {
        var int id;
        var str name;
        var str[3] names;

        fun void() Delete = () {
            if (Main::assigning == this.id) {
                Main::early++;
            }
        }
    }

    static fun Holder*(int) make = (id) {
        var Holder* h = new(Holder);
        h.id = id;
        return h;
    }

    static fun str(int) churn = (id) {
        Main::assigning = id;
        var str s = "";
        for (var int i = 0; i < 3000; i++) {
            s = i{str} + "-churn";
        }
        Main::assigning = 0;
        return s;
    }

    fun void(uint, str...) main = (argC, argV) {
        Main::make(1).name = Main::churn(1);
        Main::make(2).names[1] = Main::churn(2);
        File::StdOut.WriteStr(Main::early{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "0\n", stdout)
}

// TestMainEndsWithThreadsRunning returns from main while a thread it never
// waits for is still allocating, which ends the thread with the process.
func TestMainEndsWithThreadsRunning(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");

namespace Main {
    static fun int(int) churn = (n) {
        var int total = 0;
        for (var int i = 0; i < n; i++) {
            var str s = i{str} + "-churn";
            total = total + Strs::ByteLen(s);
        }
        return total;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[int] a = async(Main::churn(100000000));
        var thread[int] b = async(Main::churn(100000000));
    }
}
`)
	stdout, stderr, status := run(t, binary)
	assert.Zero(t, status, stderr)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}

// TestCollectionKeepsWhatALiteralLocalLaterHolds builds a string in a local
// first given a literal, then collects. The local's slot has to root the
// allocated strings stored into it afterwards.
func TestCollectionKeepsWhatALiteralLocalLaterHolds(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static fun str() churn = () {
        var str t = "";
        for(var int i = 0; i < 3000; i++) {
            t = i{str} + "-churn";
        }
        return t;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str s = "";
        for(var int i = 0; i < 5; i++) {
            s = s + i{str} + ",";
        }
        Main::churn();
        File::StdOut.WriteStr(s + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "0,1,2,3,4,\n", stdout)
}

// TestCollectionKeepsARunOfInstances holds an array of class instances only
// through an Arr. The array is the program's memory, which the collector never
// frees, and Arr's Mark keeps what its elements hold. Until the Arr holds it,
// nothing marks the run, so it is filled with collection blocked.
func TestCollectionKeepsARunOfInstances(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static fun Arr[(int, str)]*() build = () {
        System.GC::Block();
        var (int, str)* a = new((int, str), 100U);
        for(var uint i = 0U; i < 100U; i++) {
            a[i].0 = i{int};
            a[i].1 = i{str} + "!";
        }
        var Arr[(int, str)]* arr = Arr::[(int, str)]New(100U, a...);
        System.GC::Unblock();
        return arr;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Arr[(int, str)]* a = Main::build();
        var str t = "";
        for(var int i = 0; i < 3000; i++) {
            t = i{str} + "-churn";
        }
        var int bad = 0;
        for(var uint i = 0U; i < 100U; i++) {
            if(a.Get(i).0 != i{int} || a.Get(i).1 != i{str} + "!") {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "0\n", stdout)
}

// TestCollectionLeavesAnInstanceArrayAlone keeps an array of class instances
// only in manual memory the collector never traces. The array is the
// program's, so it survives every collection until it is freed.
func TestCollectionLeavesAnInstanceArrayAlone(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct Holder {
        var (int, int)* run;
    }

    static fun Holder*() build = () {
        var Holder* h = new(Holder);
        h.run = new((int, int), 50U);
        for(var uint i = 0U; i < 50U; i++) {
            h.run[i].1 = i{int} * 3;
        }
        return h;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Holder* h = Main::build();
        var str t = "";
        for(var int i = 0; i < 3000; i++) {
            t = i{str} + "-churn";
        }
        var int bad = 0;
        for(var uint i = 0U; i < 50U; i++) {
            if(h.run[i].1 != i{int} * 3) {
                bad++;
            }
        }
        free(h.run);
        free(h);
        File::StdOut.WriteStr(bad{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "0\n", stdout)
}

// TestCollectionDuringRehash grows a string-keyed set and map while
// collections run, which a rehash has to survive with every element reachable.
func TestCollectionDuringRehash(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/set/*");
import std("data/map/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var HashSet[str]* s = HashSet::[str]New(0U);
        var HashMap[str, int]* m = HashMap::[str, int]New(0U);
        for(var int i = 0; i < 400; i++) {
            s.Add(i{str} + "k");
            m.Put(i{str} + "k", i);
        }
        var int bad = 0;
        for(var int i = 0; i < 400; i++) {
            if(!s.Contains(i{str} + "k") || m.Get(i{str} + "k").0 != i) {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "0\n", stdout)
}

// TestMarkable checks GC.Markable across the kinds of type, and that a
// collection over a large array of bytes no longer walks its elements.
func TestMarkable(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct Plain {
        var int a;
        var flt b;
    }

    struct Holder {
        var int a;
        var str s;
    }

    class Box {
        var int v;
    }

    static fun bool(type) m = (t) {
        return System.GC::Markable(t);
    }

    fun void(uint, str...) main = (argC, argV) {
        var str s = Main::m(@{byte}){str} + " " + Main::m(@{int}){str} + " " + Main::m(@{flt}){str} + " " + Main::m(@{bool}){str};
        s = s + " " + Main::m(@{str}){str} + " " + Main::m(@{any}){str} + " " + Main::m(@{void()}){str} + " " + Main::m(@{Box*}){str};
        s = s + " " + Main::m(@{Plain}){str} + " " + Main::m(@{Holder}){str} + " " + Main::m(@{Plain*}){str} + " " + Main::m(@{Holder*}){str};
        s = s + " " + Main::m(@{int[4]}){str} + " " + Main::m(@{str[4]}){str} + " " + Main::m(@{Box}){str};
        s = s + " " + Main::m(@{(int, int)}){str} + " " + Main::m(@{(int, str)}){str} + "\n";

        var uint n = 50000U;
        var byte* raw = new(byte, n);
        for(var uint i = 0U; i < n; i++) {
            raw[i] = (i % 251U){byte};
        }
        var Arr[byte]* a = Arr::[byte]New(n, raw...);
        var uint sum = 0U;
        for(var uint i = 0U; i < n; i++) {
            sum = sum + a.Get(i){uint};
        }
        s = s + sum{str} + "\n";
        File::StdOut.WriteStr(s);
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "false false false false true true true true false true false false false true true false true\n6244900\n", stdout)
}
