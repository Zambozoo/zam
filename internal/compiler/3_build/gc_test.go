package build_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/base/target"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
)

// compile builds a one-file project into a binary whose heap and nursery are
// small enough that the program collects many times over, minor and major.
func compile(t *testing.T, source string) string {
	t.Helper()
	return compileWith(t, nil, source)
}

// compileWith is compile with flags added to the compiler's command line.
func compileWith(t testing.TB, flags []string, source string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"gc\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(source), 0o600))
	out := testTarget().Executable(filepath.Join(dir, "out"))

	files := &base.FileProvider{}
	argv := append([]string{"zam", "build", "-m", "16KB", "-N", "4KB", "-o", out}, flags...)
	cmd, err := cli.New(append(argv, entry), files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	object, err := codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: testTarget().Triple, Optimization: cmd.Optimization})
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{
		OutputPath:      out,
		Project:         ast.MainCodeDecl.File().Project,
		RuntimeCacheDir: cache,
		Target:          testTarget(),
	}))
	return out
}

// targetTestVariable names, when set, the target the end-to-end tests compile
// their programs for and run them for: under Wine for Windows.
const targetTestVariable = "ZAM_TEST_TARGET"

// testTarget is the target the end-to-end tests compile for: as
// $ZAM_TEST_TARGET says, else the host.
var testTarget = sync.OnceValue(func() target.Target {
	if name := os.Getenv(targetTestVariable); name != "" {
		platform, err := target.Parse(name)
		if err != nil {
			panic(err)
		}
		return platform
	}
	host, err := target.Host()
	if err != nil {
		panic(err)
	}
	return host
})

// faultStatus is the status a program ends with when it touches memory it may
// not: killed by the signal on POSIX, or ended with the access violation's
// code on Windows, of which Wine passes on only the low byte.
func faultStatus() int {
	if testTarget().OS != target.Windows {
		return -1
	}
	const accessViolation = 0xC0000005
	if runtime.GOOS == "windows" {
		return accessViolation
	}
	return accessViolation & 0xff
}

// exitStatus is the status a program exited with as POSIX reports it, its low
// byte, where Windows reports all 32 bits; a fault's status, which a POSIX
// program has no byte for, is kept whole.
func exitStatus(code int) int {
	if code == faultStatus() {
		return code
	}
	return code & 0xff
}

// targetCommand runs the program at path, built for testTarget, with args:
// under Wine when it is for Windows and this machine is not.
func targetCommand(path string, args ...string) *exec.Cmd {
	if testTarget().OS == target.Windows && runtime.GOOS != "windows" {
		return exec.Command("wine", append([]string{path}, args...)...)
	}
	return exec.Command(path, args...)
}

// run runs a binary with args to its end and returns what it wrote and how it
// exited.
func run(t testing.TB, binary string, args ...string) (stdout, stderr string, status int) {
	t.Helper()

	out, err := targetCommand(binary, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), string(exit.Stderr), exitStatus(exit.ExitCode())
	}
	require.NoError(t, err)
	return string(out), "", 0
}

// runVerified runs a binary as run does, with every collection checking that a
// minor collection in its place would have freed nothing reachable.
func runVerified(t testing.TB, binary string, args ...string) (stdout, stderr string, status int) {
	t.Helper()
	return runWith(t, []string{"ZAM_GC_VERIFY=1"}, binary, args...)
}

// runWith runs a binary as run does, with variables added to its environment.
func runWith(t testing.TB, variables []string, binary string, args ...string) (stdout, stderr string, status int) {
	t.Helper()

	var out, errOut strings.Builder
	cmd := targetCommand(binary, args...)
	cmd.Env = append(os.Environ(), variables...)
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), errOut.String(), exitStatus(exit.ExitCode())
	}
	require.NoError(t, err)
	return out.String(), errOut.String(), 0
}

// TestMinorCollectionKeepsWhatTheOldHold stores young values into old objects,
// containers and an arena, so that only the write barrier tells a minor
// collection they are reachable. Each is promoted by churning before it is
// stored into, and stored into again while churning.
func TestMinorCollectionKeepsWhatTheOldHold(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("data/list/*");
import std("data/map/*");

namespace Main {
    class Node {
        var str name;
        var Node* next;
    }

    struct Pair {
        var str s;
        var int i;
    }

    static var str last = "";

    static fun void() churn = () {
        for (var int i = 0; i < 300; i++) {
            Main::last = i{str} + "-churn";
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Node* root = new(Node);
        var ArrList[str]* list = ArrList::[str]New(0U);
        var HashMap[str, str]* map = HashMap::[str, str]New(0U);
        var Arena~* arena = Arena::New(8192U);
        var Pair* pairs = Arena::[Pair]Alloc(arena, 100U);
        Main::churn();

        for (var int i = 0; i < 100; i++) {
            var Node* n = new(Node);
            n.name = i{str} + "-node";
            n.next = root.next;
            root.next = n;
            list.AddLast(i{str} + "-list");
            map.Put(i{str}, i{str} + "-map");
            pairs[i{uint}].s = i{str} + "-arena";
            Main::churn();
        }

        var int bad = 0;
        var Node* n = root.next;
        for (var int i = 99; i >= 0; i--) {
            if (n.name != i{str} + "-node") {
                bad++;
            }
            n = n.next;
        }
        for (var int i = 0; i < 100; i++) {
            if (list.Get(i{uint}) != i{str} + "-list" || map.Get(i{str}).0 != i{str} + "-map") {
                bad++;
            }
            if (pairs[i{uint}].s != i{str} + "-arena") {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + "\n");
    }
}
`)
	for _, runner := range []func(testing.TB, string, ...string) (string, string, int){run, runVerified} {
		stdout, stderr, status := runner(t, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "0\n", stdout)
	}
}

// TestMinorCollectionRerunsOldMarks keeps the only reference to each young
// box encoded, where only an old object's own Mark can decode it: in a struct
// laid inline, in an object the Mark reads through and that is written through
// an alias, and in memory of the program's own. A minor collection has to run
// those Marks again to keep the boxes.
func TestMinorCollectionRerunsOldMarks(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Box {
        var str name;
    }

    struct S {
        var uint i;
    }

    // Nested keeps an encoded box in a struct laid inline.
    class Nested {
        var S s;

        public fun void() Mark = () {
            if (this.s.i != 0U) {
                mark((this.s.i ^ 4099U){void*}{Box*});
            }
        }
    }

    class Cell {
        var uint c;
    }

    // Reader decodes a box out of a cell it only points at.
    class Reader {
        var Cell* cell;

        public fun void() Mark = () {
            mark(this.cell);
            if (this.cell.c != 0U) {
                mark((this.cell.c ^ 65537U){void*}{Box*});
            }
        }
    }

    // Bag keeps encoded boxes in memory of the program's own.
    class Bag {
        var uint* slots;

        public fun void() Mark = () {
            if (this.slots == null) {
                return;
            }
            for (var uint i = 0U; i < 4U; i++) {
                if (this.slots[i] != 0U) {
                    mark((this.slots[i] ^ 257U){void*}{Box*});
                }
            }
        }
    }

    static var str last = "";

    static fun void() churn = () {
        for (var int i = 0; i < 300; i++) {
            Main::last = i{str} + "-churn";
        }
    }

    static fun Box*(str) box = (name) {
        var Box* b = new(Box);
        b.name = name;
        return b;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Nested* n = new(Nested);
        var Reader* r = new(Reader);
        r.cell = new(Cell);
        var Bag* bag = new(Bag);
        bag.slots = new(uint, 4U);
        Main::churn();

        var int bad = 0;
        for (var int i = 0; i < 100; i++) {
            var Box* b1 = Main::box(i{str} + "-nested");
            var Box* b2 = Main::box(i{str} + "-reader");
            var Box* b3 = Main::box(i{str} + "-bag");
            n.s.i = b1{uint} ^ 4099U;
            var Cell* other = r.cell;
            other.c = b2{uint} ^ 65537U;
            bag.slots[(i % 4){uint}] = b3{uint} ^ 257U;
            b1 = null;
            b2 = null;
            b3 = null;
            Main::churn();
            if ((n.s.i ^ 4099U){void*}{Box*}.name != i{str} + "-nested") {
                bad++;
            }
            if ((r.cell.c ^ 65537U){void*}{Box*}.name != i{str} + "-reader") {
                bad++;
            }
            if ((bag.slots[(i % 4){uint}] ^ 257U){void*}{Box*}.name != i{str} + "-bag") {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + "\n");
    }
}
`)
	for _, runner := range []func(testing.TB, string, ...string) (string, string, int){run, runVerified} {
		stdout, stderr, status := runner(t, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "0\n", stdout)
	}
}

// TestCollectionStats churns past the nursery and the heap with ZAM_GC_STATS
// set, which reports minor collections and concurrent major ones, or major
// ones stopping the program where concurrent collection is off, survivors kept
// young, and one Delete of the program's own being run.
func TestCollectionStats(t *testing.T) {
	t.Parallel()
	source := `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    static var int deleted = 0;

    class Tracked {
        fun void() Delete = () {
            Main::deleted++;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var ArrList[str]* kept = ArrList::[str]New(0U);
        for (var int i = 0; i < 3000; i++) {
            var str s = i{str} + "-churn";
            if (i % 10 == 0) {
                kept.AddLast(s);
                new(Tracked);
            }
        }
        System.GC::Major();
        File::StdOut.WriteStr(kept.Len(){str} + " " + (Main::deleted > 0){str} + "\n");
    }
}
`
	for concurrent, major := range map[string]string{
		"true":  ` concurrent pause=[^\n]* remark=`,
		"false": ` major pause=`,
	} {
		binary := compileWith(t, []string{"--gc-concurrent=" + concurrent}, source)
		stdout, stderr, status := runWith(t, []string{"ZAM_GC_STATS=1", "ZAM_GC_VERIFY="}, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "300 true\n", stdout)
		assert.Contains(t, stderr, " minor pause=")
		assert.Regexp(t, major, stderr)
		assert.Regexp(t, `finalized=[1-9]`, stderr)
		assert.Regexp(t, `survived=[1-9]`, stderr)
	}
}

// TestParallelMarkKeepsEverything holds a wide tree whose nodes each keep a
// box only through a Mark of the program's own, and churns while four markers
// trace it alongside the program and with it stopped. The helpers have to take
// part, and every node and box has to survive. A single marker has none.
func TestParallelMarkKeepsEverything(t *testing.T) {
	t.Parallel()
	source := `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Box {
        var str name;
    }

    // Coded keeps a box only as a word its Mark decodes.
    class Coded {
        var uint word;

        public fun void() Mark = () {
            if (this.word != 0U) {
                mark((this.word ^ 4099U){void*}{Box*});
            }
        }
    }

    class Tree {
        var Tree* left;
        var Tree* right;
        var str name;
        var Coded* coded;
    }

    static fun Tree*(int, int) grow = (depth, at) {
        var Tree* t = new(Tree);
        t.name = at{str};
        var Box* b = new(Box);
        b.name = at{str} + "b";
        t.coded = new(Coded);
        t.coded.word = b{uint} ^ 4099U;
        if (depth > 0) {
            t.left = Main::grow(depth - 1, at * 2);
            t.right = Main::grow(depth - 1, at * 2 + 1);
        }
        return t;
    }

    static fun int(Tree*, int) bad = (t, at) {
        if (t == null) {
            return 0;
        }
        var int n = Main::bad(t.left, at * 2) + Main::bad(t.right, at * 2 + 1);
        if (t.name != at{str}) {
            n++;
        }
        if ((t.coded.word ^ 4099U){void*}{Box*}.name != at{str} + "b") {
            n++;
        }
        return n;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Tree* root = Main::grow(10, 1);
        var str last = "";
        for (var int r = 0; r < 4; r++) {
            for (var int i = 0; i < 5000; i++) {
                last = i{str} + "-churn";
            }
            System.GC::Major();
        }
        File::StdOut.WriteStr(Main::bad(root, 1){str} + "\n");
    }
}
`
	for _, concurrent := range []string{"true", "false"} {
		binary := compileWith(t, []string{"--gc-markers=4", "--gc-concurrent=" + concurrent}, source)
		stdout, stderr, status := runWith(t, []string{"ZAM_GC_STATS=1", "ZAM_GC_VERIFY=1"}, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "0\n", stdout)
		assert.Regexp(t, ` (major|concurrent) pause=[^\n]* helped=[1-9]`, stderr)

		stdout, stderr, status = runWith(t, []string{"ZAM_GC_VERIFY="}, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "0\n", stdout)
	}

	binary := compileWith(t, []string{"--gc-markers=1"}, source)
	stdout, stderr, status := runWith(t, []string{"ZAM_GC_STATS=1", "ZAM_GC_VERIFY=1"}, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "0\n", stdout)
	assert.Contains(t, stderr, " helped=0 ")
	assert.NotRegexp(t, `helped=[1-9]`, stderr)
}

// TestCollectionKeepsVariadicArguments passes freshly built references to
// variadic calls whose callees allocate before storing them, let go of the
// parameter, free the run, or run on a thread of their own, all of which the
// arguments kept beneath the call's frame have to survive.
func TestCollectionKeepsVariadicArguments(t *testing.T) {
	t.Parallel()
	source := `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    class Box {
        var str name;
    }

    static fun Box*(str) box = (name) {
        var Box* b = new(Box);
        b.name = name;
        return b;
    }

    static fun void() churn = () {
        for(var int i = 0; i < 200; i++) {
            var str s = i{str} + "-churn";
        }
    }

    // last keeps its run only in a local, the parameter let go of.
    static fun Box*(uint, Box*...) last = (n, boxes) {
        var Box** run = boxes;
        boxes = null;
        Main::churn();
        return run[n - 1U];
    }

    // freed releases its run before collecting.
    static fun str(uint, Box*...) freed = (n, boxes) {
        var str name = boxes[n - 1U].name;
        free(boxes);
        Main::churn();
        return name;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int bad = 0;
        for(var int i = 0; i < 300; i++) {
            var ArrList[Box*]* l = ArrList::[Box*]New(2U, Main::box(i{str} + "a"), Main::box(i{str} + "b"));
            if(l.Get(0U).name != i{str} + "a" || l.Get(1U).name != i{str} + "b") {
                bad++;
            }
            var Box* nested = Main::last(2U, Main::box(i{str} + "c"), Main::last(1U, Main::box(i{str} + "d")));
            if(nested.name != i{str} + "d") {
                bad++;
            }
            if(Main::freed(1U, Main::box(i{str} + "e")) != i{str} + "e") {
                bad++;
            }
            var thread[Box*] spawned = async(Main::last(2U, Main::box(i{str} + "f"), Main::box(i{str} + "g")));
            Main::churn();
            var Box* g = wait(spawned);
            if(g.name != i{str} + "g") {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + "\n");
    }
}
`
	for _, concurrent := range []string{"false", "true"} {
		binary := compileWith(t, []string{"--gc-concurrent=" + concurrent}, source)
		stdout, stderr, status := runWith(t, []string{"ZAM_GC_VERIFY="}, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "0\n", stdout)
	}
	stdout, stderr, status := runVerified(t, compile(t, source))
	require.Zero(t, status, stderr)
	assert.Equal(t, "0\n", stdout)
}

// TestCollectionKeepsATemporaryReceiver reads memory a method hands out of an
// owner nothing names, whose Delete frees that memory, while collections run
// before the expression the owner was called in ends.
func TestCollectionKeepsATemporaryReceiver(t *testing.T) {
	t.Parallel()
	source := `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Owner {
        var uint* words;

        public fun uint*() Words = () {
            return this.words;
        }

        public fun void() Delete = () {
            var uint* w = this.words;
            this.words = null;
            free(w);
        }
    }

    static fun Owner*(uint) owner = (i) {
        var Owner* o = new(Owner);
        o.words = new(uint, 8U);
        for(var uint k = 0U; k < 8U; k++) {
            o.words[k] = i;
        }
        return o;
    }

    static fun uint(uint*) sum = (w) {
        for(var int i = 0; i < 200; i++) {
            var str s = i{str} + "-churn";
        }
        var uint total = 0U;
        for(var uint k = 0U; k < 8U; k++) {
            total = total + w[k];
        }
        return total;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int bad = 0;
        for(var uint i = 0U; i < 2000U; i++) {
            if(Main::sum(Main::owner(i).Words()) != i * 8U) {
                bad++;
            }
        }
        File::StdOut.WriteStr(bad{str} + "\n");
    }
}
`
	for _, concurrent := range []string{"false", "true"} {
		binary := compileWith(t, []string{"--gc-concurrent=" + concurrent}, source)
		stdout, stderr, status := runWith(t, []string{"ZAM_GC_VERIFY="}, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "0\n", stdout)
	}
	stdout, stderr, status := runVerified(t, compile(t, source))
	require.Zero(t, status, stderr)
	assert.Equal(t, "0\n", stdout)
}

// TestRequestedCollections asks for a minor and then a major collection,
// which ZAM_GC_STATS reports running one after the other, in that order.
func TestRequestedCollections(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        System.GC::Minor();
        System.GC::Major();
        File::StdOut.WriteStr("done");
    }
}
`)
	stdout, stderr, status := runWith(t, []string{"ZAM_GC_STATS=1", "ZAM_GC_VERIFY="}, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "done", stdout)
	assert.Regexp(t, `gc \d+ minor [^\n]*\ngc \d+ major `, stderr)
}

// TestFreedPagesAreReturned frees a large block of the program's own memory
// and collects until its pages have stayed free long enough to be given back
// to the system, which ZAM_GC_STATS reports.
func TestFreedPagesAreReturned(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var uint n = 1048576U;
        var byte* b = new(byte, n);
        for (var uint i = 0U; i < n; i = i + 4096U) {
            b[i] = 1{byte};
        }
        free(b);
        for (var int i = 0; i < 4; i++) {
            System.GC::Minor();
        }
        File::StdOut.WriteStr("done");
    }
}
`)
	stdout, stderr, status := runWith(t, []string{"ZAM_GC_STATS=1", "ZAM_GC_VERIFY="}, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "done", stdout)
	assert.Regexp(t, `returned=[1-9]`, stderr)
}

// TestMinorCollectionAcrossThreads promotes a shared list and has several
// threads add young values to it while each churns, so cards are dirtied from
// every thread and blocks are freed into pages other threads own.
func TestMinorCollectionAcrossThreads(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("sync/*");
import std("data/list/*");

namespace Main {
    static var ArrList[str]* shared = null;
    static var Mutex* lock = null;

    static fun int(int) fill = (id) {
        var int total = 0;
        for (var int i = 0; i < 200; i++) {
            var str s = id{str} + ":" + i{str};
            Main::lock.Lock();
            Main::shared.AddLast(s);
            Main::lock.Unlock();
            for (var int j = 0; j < 50; j++) {
                total = total + Strs::ByteLen(j{str} + "-churn");
            }
        }
        return total;
    }

    fun void(uint, str...) main = (argC, argV) {
        Main::shared = ArrList::[str]New(0U);
        Main::lock = Mutex::New();
        var thread[int] a = async(Main::fill(1));
        var thread[int] b = async(Main::fill(2));
        var thread[int] c = async(Main::fill(3));
        var int total = wait(a) + wait(b) + wait(c);
        var int bytes = 0;
        for (var uint i = 0U; i < Main::shared.Len(); i++) {
            bytes = bytes + Strs::ByteLen(Main::shared.Get(i));
        }
        File::StdOut.WriteStr(Main::shared.Len(){str} + " " + bytes{str} + "\n");
    }
}
`)
	for _, runner := range []func(testing.TB, string, ...string) (string, string, int){run, runVerified} {
		stdout, stderr, status := runner(t, binary)
		require.Zero(t, status, stderr)
		assert.Equal(t, "600 2670\n", stdout)
	}
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
