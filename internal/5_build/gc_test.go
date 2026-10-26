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

// run runs a binary to its end and returns what it wrote and how it exited.
func run(t *testing.T, binary string) (stdout, stderr string, status int) {
	t.Helper()

	out, err := exec.Command(binary).Output()
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
            type(Main).deleted++;
        }
    }

    static fun Box*(int) box = (i) {
        var str s = i{str} + "-box";
        var Box* b = new(Box);
        b.name = s;
        b.payload = s{any};
        b.f = lambda int(int):(x) {
            return x + type(Strs).ByteLen(s);
        };
        return b;
    }

    fun void(uint, str...) main = (argC, argV) {
        type(Main).kept = type(ArrList)[str].New(0U);
        var LinkList[str]* linked = type(LinkList)[str].New(0U);
        var Pair[str, int] pair;
        pair.first = "pair-" + 7{str};
        var any held = ("held-" + 8{str}){any};
        var Box* anchor = type(Main).box(-1);

        for (var int i = 0; i < 5000; i++) {
            var Box* b = type(Main).box(i);
            if (i % 100 == 0) {
                type(Main).kept.AddLast(b.name);
                linked.AddLast(b.name + "!");
            }
        }

        type(File).StdOut.WriteStr(type(Main).kept.Get(49U) + " " + linked.Get(49U) + " " + pair.first + " " + held{str} + " " + anchor.name + " " + anchor.f(1){str} + " " + anchor.payload{str} + "\n");
        type(File).StdOut.WriteStr((type(Main).deleted > 0){str} + "\n");
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
            total = total + type(Strs).ByteLen(s);
        }
        return total;
    }

    static fun int(int) frozen = (n) {
        var int total = 0;
        for (var int i = 0; i < n; i++) {
            type(System.GC).Block();
            var str s = i{str} + "-frozen";
            total = total + type(Strs).ByteLen(s);
            type(System.GC).Unblock();
        }
        return total;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[int] a = async(type(Main).churn(20000));
        var thread[int] b = async(type(Main).frozen(20000));
        var thread[int] c = async(type(Main).churn(20000));
        var int total = wait(a) + wait(b) + wait(c);
        type(File).StdOut.WriteStr(total{str} + "\n");
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
        var thread[int] a = async(type(Main).one());
        type(System.GC).Block();
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
            if (type(Main).assigning == this.id) {
                type(Main).early++;
            }
        }
    }

    static fun Holder*(int) make = (id) {
        var Holder* h = new(Holder);
        h.id = id;
        return h;
    }

    static fun str(int) churn = (id) {
        type(Main).assigning = id;
        var str s = "";
        for (var int i = 0; i < 3000; i++) {
            s = i{str} + "-churn";
        }
        type(Main).assigning = 0;
        return s;
    }

    fun void(uint, str...) main = (argC, argV) {
        type(Main).make(1).name = type(Main).churn(1);
        type(Main).make(2).names[1] = type(Main).churn(2);
        type(File).StdOut.WriteStr(type(Main).early{str} + "\n");
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
            total = total + type(Strs).ByteLen(s);
        }
        return total;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[int] a = async(type(Main).churn(100000000));
        var thread[int] b = async(type(Main).churn(100000000));
    }
}
`)
	stdout, stderr, status := run(t, binary)
	assert.Zero(t, status, stderr)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}
