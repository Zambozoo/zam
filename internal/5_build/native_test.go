package build_test

import (
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
	"github.com/zambozoo/zam/internal/target"
)

// nativeRuntimeCache is the runtime archive cache every native test links
// against, so the runtime is compiled once for the package.
var nativeRuntimeCache = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "zam-native-runtime-")
	if err != nil {
		return "", err
	}
	pack, err := build.Pack(testTarget())
	if err != nil {
		return "", err
	}
	_, err = build.RuntimeArchive(dir, testTarget(), pack)
	return dir, err
})

// generateObject compiles a one-file program to native code for testTarget.
func generateObject(t *testing.T, source string) (*codegen.Object, error) {
	t.Helper()
	return generateObjectFor(t, source, testTarget().Triple)
}

// generateObjectFor compiles a one-file program to native code for triple.
func generateObjectFor(t *testing.T, source, triple string) (*codegen.Object, error) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"native\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(source), 0o600))

	files := &base.FileProvider{}
	cmd, err := command.New([]string{"zam", "build", entry}, files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	return codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: triple})
}

// runNative compiles a one-file program to native code, links it and runs it
// over args, answering what it wrote and the status it exited with.
func runNative(t *testing.T, source string, args ...string) (string, int) {
	t.Helper()

	object, err := generateObject(t, source)
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := testTarget().Executable(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget()}))

	output, err := targetCommand(out, args...).CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(output), exitStatus(exit.ExitCode())
	}
	require.NoError(t, err, string(output))
	return string(output), 0
}

// exiting wraps a body of main that leaves its answer in result, which the
// program exits with.
func exiting(body string) string {
	return `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int result = 0;
` + body + `
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`
}

func TestNativeProgramRuns(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        return;
    }
}
`, "one", "two")
	assert.Empty(t, output)
	assert.Zero(t, status)
}

// TestNativeStatements runs programs whose answer is their exit status.
func TestNativeStatements(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"arguments": {`result = argC{int};`, 2},
		"continue": {`
        for(var int i = 0; i < 10; i++) {
            if(i % 2 == 0) {
                continue;
            }
            result = result + i;
        }`, 25},
		"labelled break": {`
        label outer: for(var int i = 0; i < 10; i++) {
            for(var int j = 0; j < 10; j++) {
                if(i * j > 20) {
                    break outer;
                }
                result++;
            }
        }`, 37},
		"else when exhausted": {`
        for(var int i = 0; i < 3; i++) {
            result = result + 1;
        } else {
            result = result * 10;
        }`, 30},
		"break skips else": {`
        for(var int i = 0; i < 3; i++) {
            break;
        } else {
            result = 99;
        }
        result = result + 7;`, 7},
		"while": {`
        var int n = 27;
        for(; n != 1;) {
            if(n % 2 == 0) {
                n = n / 2;
            } else {
                n = 3 * n + 1;
            }
            result++;
        }`, 111},
		"signed division": {`result = (-7 / 2) * 10 + (-7 % 2) + 50;`, 19},
		"unsigned division": {`
        var uint big = 18446744073709551614U;
        result = (big / 2U - 9223372036854775800U){int};`, 7},
		"shifts": {`result = (1 << 65) + ((-16) >>> 2) + 10 + ((-1) >> 62){int};`, 11},
		"short circuit": {`
        var int calls = 0;
        var bool b = false && (calls++ > 0);
        b = true || (calls++ > 0);
        b = true && (calls++ > 0);
        result = calls;`, 1},
		"ternary":  {`result = 3 > 2 ? 40 : 2;`, 40},
		"negation": {`result = -(-5) + ~0 + (!false){int};`, 5},
		"switch": {`
        for(var int i = 0; i < 6; i++) {
            switch(i) {
                case(0, 1): { result = result + 1; }
                case(2): { result = result + 10; continue; }
                case(3): { result = result + 20; }
            } else { result = result + 3; }
        }`, 58},
		"switch break": {`
        switch(2) {
            case(2): { result = 5; break; result = 6; }
        } else { result = 7; }
        result = result + 1;`, 6},
		"defer order": {`
        var int x = 1;
        {
            defer result = result * 10 + x;
            x = 2;
            defer result = result * 10 + 5;
        }`, 52},
		"defer break": {`
        for(var int i = 0; i < 3; i++) {
            defer result = result + 1;
            if(i == 1) { break; }
        }`, 2},
		"floats": {`
        var flt f = 7.5;
        var flt g = f / 2.0 + 0.25;
        result = (g * 10.0){int};`, 40},
		"float saturates": {`
        var flt big = 1.0e30;
        result = big{int};`, 255},
		"float nan": {`
        var flt nan = 0.0 / 0.0;
        result = (nan < 1.0){int} + (nan <= 1.0){int} * 2 + (nan == nan){int} * 4 + (nan != nan){int} * 8 + (-0.0 == 0.0){int} * 16 + nan{int} * 32 + (nan){bool}{int} * 64;`, 90},
		"float steps": {`
        var flt f = 0.5;
        f++;
        f++;
        result = (f * 2.0){int};`, 5},
		"posits": {`
        var pst p = 3{pst};
        var pst q = p * p + 1{pst};
        result = q{int} + (-1{pst} < 2{pst}){int} * 100;`, 110},
		"posit steps": {`
        var pst p = 1.5{pst};
        p++;
        result = (p * 4{pst}){int};`, 10},
		"bytes and chars": {`
        var byte b = 250{byte};
        b = b + 10{byte};
        var char c = 65{char};
        c++;
        result = b{int} + c{int};`, 70},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			output, status := runNative(t, exiting(tc.body), "a", "b")
			assert.Empty(t, output)
			assert.Equal(t, tc.want, status)
		})
	}
}

func TestNativeDivideByZeroPanics(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, exiting(`
        var int zero = 0;
        result = 1 / zero;`))
	assert.Equal(t, 67, status)
	assert.Contains(t, output, "divide by zero")
}

// TestNativeDeferRunsAtReturn runs what main deferred as it returns, reading
// its variables as they are then.
func TestNativeDeferRunsAtReturn(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int result = 3;
        defer native(STD_SYSTEM_EXIT){void(int)}(result);
        result = 9;
        return;
    }
}
`)
	assert.Equal(t, 9, status)
}

// TestNativeStatics initializes statics, one from another, before main runs,
// and reads and writes them from main.
func TestNativeStatics(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    static var int counter = 5;
    static var int doubled = counter * 2;
    static var str greeting = "hi";

    fun void(uint, str...) main = (argC, argV) {
        counter = counter + doubled;
        var str s = greeting + " there";
        var int result = counter;
        if(s == "hi there") {
            result = result + 100;
        }
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 115, status)
}

func TestNativeStrings(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, exiting(`
        result = ("abc" < "abd"){int} + ("b" > "a"){int} * 2 + (5{str} == "5"){int} * 4 + ((-1.5){str} == "-1.5"){int} * 8 + ("a" + "b" + "c" == "abc"){int} * 16 + ("x" != "x"){int} * 32 + (true{str} == "true"){int} * 64;`))
	assert.Equal(t, 95, status)
}

// TestNativeFrameRootsKeepStrings collects at every loop poll while a string
// is held only by a local, allocating garbage of the same size alongside, and
// finds the string unchanged.
func TestNativeFrameRootsKeepStrings(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, exiting(`
        var str kept = "x" + "y";
        for(var int i = 0; i < 50; i++) {
            native(STD_GC_MAJOR){void()}();
            var str junk = "z" + "w";
        }
        result = (kept == "xy"){int};`))
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := testTarget().Executable(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget()}))

	cmd := targetCommand(out)
	cmd.Env = append(os.Environ(), "ZAM_GC_STATS=1")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), string(output))
	assert.Equal(t, 1, exit.ExitCode(), string(output))
	assert.GreaterOrEqual(t, strings.Count(string(output), " major pause="), 50, string(output))
}

// TestNativeCalls calls static methods directly: recursively, mutually
// recursively, returning strings, and returning a value taken before the
// method's defers run.
func TestNativeCalls(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    fun int(int) fib = (n) {
        if(n < 2) {
            return n;
        }
        return Main::fib(n - 1) + Main::fib(n - 2);
    }

    fun str(int) describe = (n) {
        if(n == 0) {
            return "zero";
        }
        return n{str} + "!";
    }

    fun int() deferred = () {
        var int x = 1;
        defer x = 5;
        return x;
    }

    fun bool(int) even = (n) {
        if(n == 0) {
            return true;
        }
        return Main::odd(n - 1);
    }

    fun bool(int) odd = (n) {
        if(n == 0) {
            return false;
        }
        return Main::even(n - 1);
    }

    fun void(uint, str...) main = (argC, argV) {
        var int result = Main::fib(12) - 140;
        result = result + (Main::describe(3) == "3!"){int} * 8 + (Main::describe(0) == "zero"){int} * 16;
        result = result + Main::deferred() * 32 + Main::even(10){int} * 64 + Main::odd(7){int} * 128;
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 252, status)
}

// TestNativeCallResultsSurviveCollections keeps one call's string result while
// the next call collects, and finds both intact.
func TestNativeCallResultsSurviveCollections(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, `namespace Main {
    fun str(int) make = (n) {
        native(STD_GC_MAJOR){void()}();
        var int pad = 0;
        for(var int i = 0; i < 2; i++) {
            pad++;
        }
        return "v" + n{str};
    }

    fun void(uint, str...) main = (argC, argV) {
        var str joined = Main::make(1) + Main::make(2) + Main::make(3);
        native(STD_SYSTEM_EXIT){void(int)}((joined == "v1v2v3"){int});
    }
}
`)
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := testTarget().Executable(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget()}))

	cmd := targetCommand(out)
	cmd.Env = append(os.Environ(), "ZAM_GC_STATS=1")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), string(output))
	assert.Equal(t, 1, exit.ExitCode(), string(output))
	assert.GreaterOrEqual(t, strings.Count(string(output), " major pause="), 3, string(output))
}

// TestNativeClosures passes lambdas and static methods as values, captures by
// value, and calls a static closure initialized from a method declared after
// it.
func TestNativeClosures(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    static var int(int) stored = Main::twice;
    static var int(int) early = Main::later;

    fun int(int(int), int) apply = (f, x) {
        return f(x);
    }

    fun int(int) twice = (x) {
        return x * 2;
    }

    fun int(int) later = (x) {
        return x + 100;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int k = 3;
        var int(int) add = lambda int(int):(x) { return x + k; };
        k = 50;
        var int result = Main::apply(add, 5);
        result = result + Main::apply(Main::twice, 4);
        result = result + Main::stored(10);
        result = result + Main::early(1);
        var str(str) wrap = lambda str(str):(w) { return "[" + w + "]"; };
        result = result + (wrap("a") == "[a]"){int} * 64;
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 201, status)
}

// TestNativeClosureCapturesSurviveCollections collects while the only
// reference to a string is the copy a closure captured.
func TestNativeClosureCapturesSurviveCollections(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, exiting(`
        var str s = "c" + "d";
        var str() get = lambda str():() { return s; };
        s = "zz";
        for(var int i = 0; i < 50; i++) {
            native(STD_GC_MAJOR){void()}();
            var str junk = "y" + "w";
        }
        result = (get() == "cd"){int};`))
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := testTarget().Executable(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget()}))

	cmd := targetCommand(out)
	cmd.Env = append(os.Environ(), "ZAM_GC_STATS=1")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), string(output))
	assert.Equal(t, 1, exit.ExitCode(), string(output))
	assert.GreaterOrEqual(t, strings.Count(string(output), " major pause="), 50, string(output))
}

// TestNativeClasses allocates classes with initialized and inherited members,
// reads and writes members of every width, and calls methods directly,
// through an abstract and through an interface.
func TestNativeClasses(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    interface Shape {
        fun int() Area;
    }

    abstract Base {
        var int scale = 2;

        virtual fun int() Area;

        fun int() Scaled = () {
            return this.Area() * scale;
        }
    }

    class Square <: Base* {
        var int side = 3;
        var bool flag;
        var char letter = 'a';
        var str name = "sq";

        fun int() Area = () {
            return side * side;
        }

        fun void(int) Grow = (by) {
            side = side + by;
        }
    }

    class Pair {
        var Square* left;
        var Square* right;
    }

    fun int(Shape*) area = (s) {
        return s.Area();
    }

    fun void(uint, str...) main = (argC, argV) {
        var Square* sq = new(Square);
        sq.Grow(1);
        var Base* b = sq;
        var int result = b.Scaled();
        result = result + Main::area(sq);
        sq.flag = true;
        result = result + sq.flag{int} * 100;
        result = result + (sq.letter == 'a'){int} * 50 + (sq.name == "sq"){int} * 5;
        var Pair* p = new(Pair);
        p.left = sq;
        result = result + (p.left.side == 4){int} * 2 + (p.right == null){int};
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 206, status)
}

func TestNativeNullMemberPanics(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `namespace Main {
    class Box {
        var int n;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Box* box = null;
        native(STD_SYSTEM_EXIT){void(int)}(box.n);
    }
}
`)
	assert.Equal(t, 67, status)
	assert.Contains(t, output, "selected a member of a null pointer")
}

// TestNativeObjectGraphsSurviveCollections collects while an object is
// reachable only through a member of another, written after both were
// allocated, and finds the string it holds intact.
func TestNativeObjectGraphsSurviveCollections(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, `namespace Main {
    class Node {
        var str tag;
        var Node* next;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Node* head = new(Node);
        for(var int i = 0; i < 20; i++) {
            native(STD_GC_MAJOR){void()}();
            var Node* junk = new(Node);
            junk.tag = "j" + i{str};
        }
        head.next = new(Node);
        head.next.tag = "x" + "y";
        for(var int i = 0; i < 30; i++) {
            native(STD_GC_MAJOR){void()}();
            var Node* junk = new(Node);
            junk.tag = "z" + "w";
        }
        native(STD_SYSTEM_EXIT){void(int)}((head.next.tag == "xy"){int});
    }
}
`)
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := testTarget().Executable(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget()}))

	cmd := targetCommand(out)
	cmd.Env = append(os.Environ(), "ZAM_GC_STATS=1", "ZAM_GC_VERIFY=1")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), string(output))
	assert.Equal(t, 1, exit.ExitCode(), string(output))
	assert.GreaterOrEqual(t, strings.Count(string(output), " major pause="), 50, string(output))
}

// TestNativeEnums casts backed enums to their backing values, calls an enum's
// method on values in a local and a static, and compares and switches on
// enum values.
func TestNativeEnums(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    enum Flag : int {
        Read(1), Write(2), Both(3);
    }

    enum Mode : byte {
        Low(1{byte}), High(200{byte});
    }

    enum Color {
        Red, Green, Blue;

        fun bool() IsRed = () {
            return *this == Color::Red;
        }
    }

    static var Color favorite = Color::Green;

    fun int(Flag) weight = (f) {
        return f{int} * 10;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int result = Main::weight(Flag::Write);
        result = result + Flag::Both{int};
        var Color c = Color::Red;
        result = result + c.IsRed(){int} * 100;
        result = result + (favorite == Color::Green){int} * 50;
        result = result + favorite.IsRed(){int} * 7;
        switch(c) {
            case(Color::Red): { result = result + 1; }
        } else { result = result + 2; }
        var Color* p = &favorite;
        result = result + (*p == Color::Green){int} * 4;
        result = result + (Mode::High{int} == 200){int} * 8 + (Mode::Low{int} == 1){int} * 16;
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 202, status)
}

// TestNativeTuples returns, passes, copies, unpacks and compares tuples, and
// keeps them in objects, writing a narrow one without touching the member
// after it.
func TestNativeTuples(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    class Holder {
        var (byte, bool) flags;
        var int after;
        var (str, int) named;
    }

    fun (int, str)(int) pair = (n) {
        return (n * 2, "v" + n{str});
    }

    fun int((int, int)) sum = (p) {
        return p.0 + p.1;
    }

    fun void(uint, str...) main = (argC, argV) {
        var (int, str) t = Main::pair(5);
        var int result = t.0;
        result = result + (t.1 == "v5"){int} * 2;
        t.0 = 7;
        var (int, str) copy = t;
        t.0 = 100;
        result = result + copy.0;
        result = result + Main::sum((3, 4));
        var int a, str b = Main::pair(1);
        result = result + a + (b == "v1"){int};
        var Holder* h = new(Holder);
        h.after = 9;
        h.flags = (200{byte}, true);
        h.named = ("n", 2);
        result = result + h.flags.0{int} - 190 + h.flags.1{int} + h.after + h.named.1;
        result = result + ((1, "x") == (1, "x")){int} * 64;
        var int* first = &(t.0);
        *first = 3;
        result = result + t.0;
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 118, status)
}

// TestNativeTupleRootsKeepStrings collects while a string is held only inside
// a tuple local.
func TestNativeTupleRootsKeepStrings(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, exiting(`
        var (int, str) kept = (1, "a" + "b");
        for(var int i = 0; i < 50; i++) {
            native(STD_GC_MAJOR){void()}();
            var str junk = "c" + "d";
        }
        result = (kept.1 == "ab"){int};`))
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := testTarget().Executable(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget()}))

	cmd := targetCommand(out)
	cmd.Env = append(os.Environ(), "ZAM_GC_STATS=1")
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), string(output))
	assert.Equal(t, 1, exit.ExitCode(), string(output))
	assert.GreaterOrEqual(t, strings.Count(string(output), " major pause="), 50, string(output))
}

// TestNativePointers indexes and offsets runs of words, bytes and strings,
// writes through element addresses, frees the runs, and allocates an empty
// one.
func TestNativePointers(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int* nums = new(int, 5);
        for(var int i = 0; i < 5; i++) {
            nums[i] = i * i;
        }
        var int result = nums[4] + *(nums + 3);
        var int* third = &(nums[2]);
        *third = 40;
        result = result + nums[2];
        var byte* bytes = new(byte, 3);
        bytes[1] = 255{byte};
        bytes[2] = 7{byte};
        result = result + bytes[1]{int} + (bytes + 2)[0]{int};
        var int* last = nums + 4;
        result = result + (last - 1)[0];
        free(nums);
        free(bytes);
        var str* names = new(str, 2);
        names[0] = "a" + "b";
        result = result + (names[0] == "ab"){int};
        free(names);
        var int* none = new(int, 0);
        result = result + (none == null){int} * 2;
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 83, status)
}

// nullComparisonSource compares a pointer to a primitive with null.
const nullComparisonSource = `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int* none = null;
        native(STD_SYSTEM_EXIT){void(int)}((none == null){int} + (none != null){int} * 2);
    }
}
`

func TestNativePointerComparesWithNull(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, nullComparisonSource)
	assert.Equal(t, 1, status)
}

func TestPointerComparesWithNull(t *testing.T) {
	t.Parallel()
	_, _, status := run(t, compile(t, nullComparisonSource))
	assert.Equal(t, 1, status)
}

// TestNativeGenericMethods instantiates generic static methods over words,
// strings, runs and two type arguments, one calling another with its own type
// argument.
func TestNativeGenericMethods(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    fun[A] A(A, A) pick = (first, second) {
        return first;
    }

    fun[A] A*(int) fresh = (n) {
        return new(A, n);
    }

    fun[A, B] B(A, B) second = (a, b) {
        return b;
    }

    fun[A] A(A) twice = (x) {
        return Main::[A]pick(x, x);
    }

    fun void(uint, str...) main = (argC, argV) {
        var int result = Main::[int]pick(3, 4);
        var str s = Main::[str]pick("x", "y");
        var byte* bytes = Main::[byte]fresh(4);
        bytes[0] = 9{byte};
        result = result + bytes[0]{int};
        result = result + Main::[str, int]second("a", 10);
        result = result + (s == "x"){int} * 100;
        result = result + Main::[int]twice(5);
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 127, status)
}

// TestNativeGenericClasses allocates instantiations of generic classes over
// words, strings and narrow members, calls their methods directly and through
// an instantiated generic interface, and runs a member initializer.
func TestNativeGenericClasses(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    interface Getter[A] {
        fun A() Get;
    }

    class Box[A] {
        var A value;

        fun A() Get = () {
            return value;
        }

        fun void(A) Set = (v) {
            value = v;
        }
    }

    class Pair[A, B] {
        var A first;
        var B second;
        var int count = 7;

        fun B() Second = () {
            return second;
        }
    }

    fun[A] A(Getter[A]*) read = (g) {
        return g.Get();
    }

    fun void(uint, str...) main = (argC, argV) {
        var Box[int]* bi = new(Box[int]);
        bi.Set(41);
        var int result = bi.Get();
        var Box[str]* bs = new(Box[str]);
        bs.Set("s");
        result = result + (Main::[str]read(bs) == "s"){int} * 100;
        var Pair[byte, str]* p = new(Pair[byte, str]);
        p.first = 3{byte};
        p.second = "two";
        result = result + p.first{int} + p.count + (p.Second() == "two"){int} * 50;
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 201, status)
}

// TestNativeArrays keeps sized arrays of words, bytes and strings in locals,
// parameters and members, copies one, and writes through an element's
// address.
func TestNativeArrays(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    class Holder {
        var int[3] counts;
        var bool after;
    }

    fun int(int[3]) sum = (a) {
        return a[0] + a[1] + a[2];
    }

    fun void(uint, str...) main = (argC, argV) {
        var int[3] a;
        for(var int i = 0; i < 3; i++) {
            a[i] = (i + 1) * 10;
        }
        var int[3] b = a;
        a[0] = 1;
        var int result = Main::sum(b);
        result = result + a[0];
        var byte[5] bytes;
        bytes[4] = 7{byte};
        result = result + bytes[4]{int} + bytes[3]{int};
        var Holder* h = new(Holder);
        h.counts[2] = 5;
        h.after = true;
        result = result + h.counts[2] + h.after{int};
        var str[2] names;
        names[1] = "a" + "b";
        result = result + (names[1] == "ab"){int} * 100;
        var int* p = &(a[1]);
        *p = 2;
        result = result + a[1];
        native(STD_SYSTEM_EXIT){void(int)}(result);
    }
}
`)
	assert.Equal(t, 176, status)
}

// TestNativeVoidReturnOfVoidCall returns a void call from a void method.
func TestNativeVoidReturnOfVoidCall(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, `namespace Main {
    static var int count = 0;

    fun void() bump = () {
        count = count + 5;
    }

    fun void() forward = () {
        return Main::bump();
    }

    fun void(uint, str...) main = (argC, argV) {
        Main::forward();
        Main::forward();
        native(STD_SYSTEM_EXIT){void(int)}(count);
    }
}
`)
	assert.Equal(t, 10, status)
}

// TestNativeStdList builds a generic std list of strings and writes what it
// holds.
func TestNativeStdList(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");
import std("data/list/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var ArrList[str]* list = ArrList::[str]New(0U);
        list.AddLast("a");
        list.AddLast("b" + "c");
        File::StdOut.WriteStr(list.Len(){str} + " " + list.Get(1U) + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "2 bc\n", output)
}

// TestNativeCompilesConcurrently compiles programs with rooted locals from
// several goroutines at once, as the CLI's callers and the tests do. Nothing
// a compilation builds may live in LLVM's global context, which they share.
func TestNativeCompilesConcurrently(t *testing.T) {
	t.Parallel()
	source := exiting(`
        var str a = "x" + "y";
        var (int, str) pair = (1, a);
        result = (pair.1 == "xy"){int};`)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 3 {
				_, err := generateObject(t, source)
				assert.NoError(t, err)
			}
		})
	}
	wg.Wait()
}

// staticOrderSource initializes statics that read a nested enum's value, a
// static of a nested class, and a static the nested class reads, each written
// after what it reads. It exits 21 when a file's statics are initialized in
// the order they are written, whichever declaration holds them.
const staticOrderSource = `namespace Main {
    enum Color {
        Red, Green;
    }

    static var Color favorite = Color::Green;

    static var int a = 1;

    class Inner {
        static var int b = Main::a + 1;
    }

    static var int c = Inner::b * 10;

    fun void(uint, str...) main = (argC, argV) {
        native(STD_SYSTEM_EXIT){void(int)}((favorite == Color::Green){int} + c);
    }
}
`

func TestNativeStaticsInitializeInLineOrder(t *testing.T) {
	t.Parallel()
	_, status := runNative(t, staticOrderSource)
	assert.Equal(t, 21, status)
}

func TestStaticsInitializeInLineOrder(t *testing.T) {
	t.Parallel()
	_, _, status := run(t, compile(t, staticOrderSource))
	assert.Equal(t, 21, status)
}

// TestNativeTrapsWhatItCannotCompile compiles a method native compilation
// cannot compile yet as a trap, says so in the object, and ends the program
// saying why only once the method is called.
func TestNativeTrapsWhatItCannotCompile(t *testing.T) {
	t.Parallel()
	source := `import std("lang/Obj.zam");

namespace Main {
    fun int() unused = () {
        var thread[void] t = async(native(STD_SYSTEM_EXIT){void(int)}(1));
        return 1;
    }

    fun int() used = () {
        var thread[void] t = async(native(STD_SYSTEM_EXIT){void(int)}(1));
        return 2;
    }

    fun void(uint, str...) main = (argC, argV) {
        if(argC > 0U) {
            native(STD_SYSTEM_EXIT){void(int)}(Main::used());
        }
        native(STD_SYSTEM_EXIT){void(int)}(9);
    }
}
`
	object, err := generateObject(t, source)
	require.NoError(t, err)
	require.Len(t, object.Unsupported, 2, object.Unsupported)
	assert.Contains(t, strings.Join(object.Unsupported, "\n"), "Main.unused")

	output, status := runNative(t, source)
	assert.Equal(t, 9, status, "nothing traps that is not called")
	output, status = runNative(t, source, "call")
	assert.Equal(t, 70, status)
	assert.Contains(t, output, "Main.used")
	assert.Contains(t, output, "async of call")
}

// TestNativeStdHelloWorld imports the standard library and writes through it.
func TestNativeStdHelloWorld(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr("hello " + argC{str} + "\n");
    }
}
`, "x", "y")
	assert.Zero(t, status, output)
	assert.Equal(t, "hello 2\n", output)
}

// TestNativeThreads runs static, closure and tuple-returning calls on threads
// of their own and waits for each, checked against the VM.
func TestNativeThreads(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Box { var int n; }

    static fun int(int) fill = (n) {
        var str s = "";
        for(var int i = 0; i < 2000; i++) {
            s = i{str} + "-x";
        }
        return n * 10;
    }

    static fun (str, int)(str) pair = (s) {
        return s + "!", 3;
    }

    fun void(uint, str...) main = (argC, argV) {
        var thread[int] a = async(Main::fill(1));
        var thread[int] b = async(Main::fill(2));
        var Box* box = new(Box);
        box.n = 7;
        var thread[(str, int)] c = async(Main::pair("hi" + box.n{str}));
        var int k = 5;
        var thread[int] d = async((lambda int(): () { return k + 1; })());
        var (str, int) p = wait(c);
        File::StdOut.WriteStr((wait(a) + wait(b)){str} + " " + p.0 + " " + p.1{str} + " " + wait(d){str} + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "30 hi7! 3 6\n", output)
}

// TestNativeByteStatics reads and writes byte statics laid out side by side,
// through their declaration's name and bare, at their own width.
func TestNativeByteStatics(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Flags {
        static var byte$ a = 0{byte};
        static var byte b = 1{byte};
        static var byte$ c = 2{byte};
    }

    fun void(uint, str...) main = (argC, argV) {
        Flags::b = 9{byte};
        File::StdOut.WriteStr(Flags::a{int}{str} + " " + Flags::b{int}{str} + " " + Flags::c{int}{str} + " " + (Flags::a == 0{byte}){str} + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "0 9 2 true\n", output)
}

// TestNativeSuper calls a base's method through super from an override of
// it, checked against the VM.
func TestNativeSuper(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Base {
        fun str(str) Name = (s) {
            return "base:" + s;
        }
    }

    class Derived <: Base* {
        fun str(str) Name = (s) {
            return "derived(" + super.Name(s) + ")";
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Derived* d = new(Derived);
        File::StdOut.WriteStr(d.Name("x") + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "derived(base:x)\n", output)
}

// TestNativeTailedStructs allocates tailed structs with a tail length given at
// runtime and reads and writes their members and tails.
func TestNativeTailedStructs(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    struct Node~ {
        struct Link {
            var Node~* n;
        }
        var int key;
        var byte height;
        var Node~* prev;
        var Link[~] nexts;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Node~* a = new(Node~, 1, 3U);
        var Node~* b = new(Node~, 1, 2U);
        a.key = 1;
        b.key = 2;
        a.nexts[0].n = b;
        a.nexts[2].n = b;
        b.prev = a;
        var Node~* p = a.nexts[2].n;
        File::StdOut.WriteStr(a.key{str} + " " + (a.nexts[0].n == b){str} + " " + (a.nexts[1].n == null){str} + " " + p.key{str} + " " + p.prev.key{str} + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "1 true true 2 1\n", output)
}

// TestNativeCollectsBeforeTheNextStatement asks for a major collection and
// expects what became unreachable, the statement before, deleted by the next
// statement, as the interpreter does.
func TestNativeCollectsBeforeTheNextStatement(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static var int deleted = 0;

    class Counted {
        var str name;
        public fun void() Delete = () {
            Main::deleted++;
        }
    }

    static fun void() build = () {
        var Counted* c = new(Counted);
        c.name = "x";
    }

    fun void(uint, str...) main = (argC, argV) {
        var Counted* kept = new(Counted);
        Main::build();
        System.GC::Major();
        File::StdOut.WriteStr(Main::deleted{str} + "\n");
        kept = null;
        System.GC::Major();
        File::StdOut.WriteStr(Main::deleted{str} + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "1\n2\n", output)
}

// TestNativeInlineInstances keeps class instances inline: as locals, through
// their methods, passed and returned by value, as a member of an object, and
// pinned by Struct, whose Mark keeps what it holds alive through a collection.
func TestNativeInlineInstances(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Pair {
        var int a;
        var str b;

        fun str() Show = () {
            return this.a{str} + this.b;
        }
    }

    class Holder {
        var Pair p;
    }

    class Node {
        var str name;
    }

    static fun Pair(int) make = (n) {
        var Pair p;
        p.a = n;
        p.b = "-" + n{str};
        return p;
    }

    static fun str(Pair) show = (p) {
        return p.Show();
    }

    static fun void(Node*) markNode = (n) {
        mark(n);
    }

    fun void(uint, str...) main = (argC, argV) {
        var Pair p = Main::make(3);
        var Pair q = p;
        q.a = 4;
        var Holder* h = new(Holder);
        h.p = q;
        var Node* n = new(Node);
        n.name = "pinned" + "!";
        var Struct[Node*] pin;
        pin.Init(n, Main::markNode, null);
        n = null;
        System.GC::Major();
        File::StdOut.WriteStr(Main::show(p) + " " + q.Show() + " " + h.p.Show() + " " + pin.s.name + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "3-3 4-3 4-3 pinned!\n", output)
}

// TestNativeLambdaCaptures captures the receiver of the method a lambda is
// written in and an aggregate local, each by value.
func TestNativeLambdaCaptures(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    class Counter {
        var int n;

        fun int() () Reader = () {
            var (int, str) t = 10, "x";
            var int() f = lambda int(): () { return this.n + t.0 + (t.1 == "x" ? 1 : 0); };
            t.0 = 0;
            return f;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Counter* c = new(Counter);
        c.n = 5;
        var int() f = c.Reader();
        c.n = 7;
        File::StdOut.WriteStr(f(){str} + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "18\n", output)
}

// TestNativeTrace traces a failure, a panic and a fault through compiled
// frames, each frame at the line it was at, and reads a caller's line.
func TestNativeTrace(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, traceSource, "null")
	assert.Equal(t, 67, status)
	assert.Regexp(t, `dereferenced a null pointer
\x1b\[0m\tat Main.Inner \(\S*:9\)
\tat Main.Outer \(\S*:13\)
\tat Main.main \(\S*:20\)
$`, output)

	output, status = runNative(t, traceSource, "panic")
	assert.Equal(t, 255, status)
	assert.Regexp(t, `^boom
\tat System.Panic \(std://lang/System.zam:\d+\)
\tat Main.Inner \(\S*:7\)
\tat Main.Outer \(\S*:13\)
\tat Main.main \(\S*:22\)
$`, output)

	output, status = runNative(t, traceSource, "caller")
	assert.Zero(t, status)
	assert.Equal(t, "27\n", output)

	output, status = runNative(t, faultSource)
	assert.Equal(t, faultStatus(), status)
	assert.Regexp(t, `invalid memory access at 0x[0-9a-f]+
\x1b\[0m\tat Main.Read \(\S*:3\)
\tat Main.main \(\S*:8\)
$`, output)
}

// TestNativeProjectNatives builds a project whose natives are its own C,
// compiled by the system C compiler and linked into the program.
func TestNativeProjectNatives(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"),
		[]byte("name: \"natives\"\nversion: \"0.0.0\"\ndependencies:\nnative:\n  files:\n    - add.c\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "add.c"), []byte(`#include "zam/native.h"

// ADD(int a, int b, int* out) writes a + b.
void ADD(ZamThread *thread) {
    int64_t a = (int64_t)zam_native_argument(thread, 0);
    int64_t b = (int64_t)zam_native_argument(thread, 1);
    *(int64_t *)zam_native_argument_pointer(thread, 2) = a + b;
    zam_native_return_void(thread);
}
`), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(`namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int sum;
        native(ADD){void(int, int, int*)}(40, 2, &sum);
        native(STD_SYSTEM_EXIT){void(int)}(sum);
    }
}
`), 0o600))

	files := &base.FileProvider{}
	cmd, err := command.New([]string{"zam", "build", entry}, files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	object, err := codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: testTarget().Triple})
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := testTarget().Executable(filepath.Join(dir, "out"))
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{
		OutputPath:      out,
		Project:         ast.MainCodeDecl.File().Project,
		RuntimeCacheDir: cache,
		Target:          testTarget(),
	}))

	err = targetCommand(out).Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 42, exit.ExitCode())
}

// TestNativeMethodValues binds methods to their receivers as function values,
// through a class, an abstract and an interface, and calls them later.
func TestNativeMethodValues(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    interface Named {
        virtual fun str(str) Name;
    }

    abstract Base {
        virtual fun int(int) Scale;
    }

    class Twice <: Base* {
        var str tag;

        fun int(int) Scale = (x) {
            return x * 2;
        }

        fun str(str) Name = (s) {
            return this.tag + s;
        }
    }

    static fun int(int(int), int) apply = (f, x) {
        return f(x);
    }

    fun void(uint, str...) main = (argC, argV) {
        var Twice* t = new(Twice);
        t.tag = "t:";
        var Base* b = t;
        var Named* n = t;
        var str(str) name = n.Name;
        var int(int) scale = b.Scale;
        t = null;
        System.GC::Major();
        File::StdOut.WriteStr(Main::apply(scale, 21){str} + " " + name("x") + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "42 t:x\n", output)
}

// TestNativeSignals raises a signal the program handles, through an enum value
// with no address of its own, and expects the handler to run by the next call,
// each time. SIGTERM is one every target has.
func TestNativeSignals(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static var int seen = 0;

    static fun void() tick = () {
    }

    fun void(uint, str...) main = (argC, argV) {
        var int bump = 2;
        System.Signal::SIGTERM.Handle(lambda void(): () { Main::seen = Main::seen + bump; });
        System.Signal::SIGTERM.Raise();
        Main::tick();
        System.Signal::SIGTERM.Raise();
        Main::tick();
        File::StdOut.WriteStr(Main::seen{str} + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "4\n", output)
}

// objectDWARF is the DWARF of an object file, Mach-O, COFF or ELF.
func objectDWARF(object []byte) (*dwarf.Data, error) {
	if file, err := macho.NewFile(bytes.NewReader(object)); err == nil {
		return file.DWARF()
	}
	if file, err := pe.NewFile(bytes.NewReader(object)); err == nil {
		return file.DWARF()
	}
	file, err := elf.NewFile(bytes.NewReader(object))
	if err != nil {
		return nil, err
	}
	return file.DWARF()
}

// TestNativeDWARF describes a compiled program for debuggers: each function
// by its zam name, the lines its statements are on, and its locals by their
// zam types.
func TestNativeDWARF(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, `namespace Main {
    fun int(int) twice = (n) {
        var int doubled = n * 2;
        return doubled;
    }

    fun void(uint, str...) main = (argC, argV) {
        var str name = "zam";
        var int count = Main::twice(21);
        native(STD_SYSTEM_EXIT){void(int)}(count);
    }
}
`)
	require.NoError(t, err)
	data, err := objectDWARF(object.Bytes)
	require.NoError(t, err)

	functions := map[string]bool{}
	variables := map[string]string{}
	reader := data.Reader()
	for {
		entry, err := reader.Next()
		require.NoError(t, err)
		if entry == nil {
			break
		}
		name, _ := entry.Val(dwarf.AttrName).(string)
		switch entry.Tag {
		case dwarf.TagSubprogram:
			functions[name] = true
		case dwarf.TagVariable:
			assert.NotNil(t, entry.Val(dwarf.AttrLocation), "%s has no location", name)
			offset, ok := entry.Val(dwarf.AttrType).(dwarf.Offset)
			require.True(t, ok, name)
			typ, err := data.Type(offset)
			require.NoError(t, err)
			variables[name] = typ.String()
		}
	}
	assert.True(t, functions["Main.main"], "%v", functions)
	assert.True(t, functions["Main.twice"], "%v", functions)
	assert.Equal(t, "int", variables["doubled"])
	assert.Equal(t, "int", variables["count"])
	assert.Equal(t, "str", variables["name"])

	lines := map[int]bool{}
	starts := map[int]uint64{}
	units := data.Reader()
	for {
		unit, err := units.Next()
		require.NoError(t, err)
		if unit == nil {
			break
		}
		if unit.Tag != dwarf.TagCompileUnit {
			units.SkipChildren()
			continue
		}
		lineReader, err := data.LineReader(unit)
		require.NoError(t, err)
		var line dwarf.LineEntry
		for lineReader.Next(&line) == nil {
			lines[line.Line] = true
			if start, ok := starts[line.Line]; !ok || line.Address < start {
				starts[line.Line] = line.Address
			}
		}
	}
	for _, want := range []int{3, 4, 8, 9, 10} {
		assert.True(t, lines[want], "line %d in %v", want, lines)
	}
	// A line begins after the lines before it, or a breakpoint on it stops
	// before they have run.
	assert.Less(t, starts[8], starts[9])
	assert.Less(t, starts[9], starts[10])
}

// TestNativeDWARFTypes describes classes, structs and tuples as DWARF structs
// whose members lie where the program lays them out, and pointers to them as
// pointers, so a debugger expands them itself.
func TestNativeDWARFTypes(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, `namespace Main {
    struct Point {
        var int x;
        var byte tag;
    }

    class Node {
        var int value;
        var Node* next;
        var Point at;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Node* node = new(Node);
        var (int, bool) pair = 1, true;
        node.next = node;
        native(STD_SYSTEM_EXIT){void(int)}(node.value);
    }
}
`)
	require.NoError(t, err)
	data, err := objectDWARF(object.Bytes)
	require.NoError(t, err)

	variables := map[string]dwarf.Type{}
	reader := data.Reader()
	for {
		entry, err := reader.Next()
		require.NoError(t, err)
		if entry == nil {
			break
		}
		if entry.Tag != dwarf.TagVariable {
			continue
		}
		typ, err := data.Type(entry.Val(dwarf.AttrType).(dwarf.Offset))
		require.NoError(t, err)
		variables[entry.Val(dwarf.AttrName).(string)] = typ
	}

	pointer, ok := variables["node"].(*dwarf.PtrType)
	require.True(t, ok, "%T", variables["node"])
	node, ok := pointer.Type.(*dwarf.StructType)
	require.True(t, ok, "%T", pointer.Type)
	assert.Equal(t, "Node", node.StructName)
	fields := map[string]*dwarf.StructField{}
	for _, field := range node.Field {
		fields[field.Name] = field
	}
	require.Contains(t, fields, "value")
	require.Contains(t, fields, "next")
	require.Contains(t, fields, "at")
	assert.Equal(t, int64(16), fields["value"].ByteOffset, "past the class header")
	assert.Equal(t, "Node", fields["next"].Type.(*dwarf.PtrType).Type.(*dwarf.StructType).StructName)
	assert.Equal(t, "Point", fields["at"].Type.(*dwarf.StructType).StructName)

	pair, ok := variables["pair"].(*dwarf.StructType)
	require.True(t, ok, "%T", variables["pair"])
	require.Len(t, pair.Field, 2)
	assert.Equal(t, "0", pair.Field[0].Name)
	assert.Equal(t, "bool", pair.Field[1].Type.String())
	assert.Equal(t, int64(8), pair.Field[1].ByteOffset)
}

// TestNativeEnumStrs writes enum values out by their backing values and
// pointers to them through their Str, in tuples and anys too, as the
// interpreter does.
func TestNativeEnumStrs(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    enum Color : str {
        Red("red"),
        Blue("blue");
    }

    enum Shade : str {
        Dark("dark"),
        Light("light");

        public fun str() Str = () {
            return "shade";
        }
    }

    enum Size : byte {
        Small(1{byte}),
        Big(2{byte});
    }

    struct P {
        var int x;

        public fun str() Str = () {
            return "P" + this.x{str};
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Color c = Color::Blue;
        var Shade s = Shade::Light;
        var Size z = Size::Big;
        var P p;
        p.x = 3;
        var any r = Color::Red;
        File::StdOut.WriteStr(c{str} + " " + (&c){str} + " " + c.Str() + " " + s{str} + " " + (&s){str} + " " + z{str} + "\n");
        File::StdOut.WriteStr((c, s, z){str} + " " + (&c, &s){str} + " " + s{any}{str} + " " + (&p, 1){str} + "\n");
        File::StdOut.WriteStr((r == c{any}){str} + " " + (r == Color::Red{any}){str} + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "blue blue blue light shade 2\n(blue, light, 2) (blue, shade) light (P3, 1)\nfalse true\n", output)
}

// TestNativeDWARFEnumsAndTypeNames names an enum's values in DWARF, and lays
// out the table zam's lldb formatters find the type of what an any holds by.
func TestNativeDWARFEnumsAndTypeNames(t *testing.T) {
	t.Parallel()
	object, err := generateObject(t, `namespace Main {
    enum Color {
        Red,
        Blue;
    }

    fun void(uint, str...) main = (argC, argV) {
        var Color color = Color::Blue;
        var any boxed = 7;
        native(STD_SYSTEM_EXIT){void(int)}(0);
    }
}
`)
	require.NoError(t, err)
	data, err := objectDWARF(object.Bytes)
	require.NoError(t, err)

	var color *dwarf.EnumType
	reader := data.Reader()
	for {
		entry, err := reader.Next()
		require.NoError(t, err)
		if entry == nil {
			break
		}
		if entry.Tag == dwarf.TagVariable && entry.Val(dwarf.AttrName) == "color" {
			typ, err := data.Type(entry.Val(dwarf.AttrType).(dwarf.Offset))
			require.NoError(t, err)
			color, _ = typ.(*dwarf.EnumType)
		}
	}
	require.NotNil(t, color)
	assert.Equal(t, "Color", color.EnumName)
	names := map[string]int64{}
	for _, value := range color.Val {
		names[value.Name] = value.Val
	}
	assert.Equal(t, map[string]int64{"Red": 0, "Blue": 1}, names)

	symbols, err := objectSymbols(object.Bytes)
	require.NoError(t, err)
	assert.Contains(t, symbols, "zam_debug_type_names")
	assert.Contains(t, symbols, "zam_debug_type_names_count")
}

// objectSymbols names the symbols of an object file or executable, Mach-O,
// COFF or ELF, as C names them.
func objectSymbols(object []byte) ([]string, error) {
	var symbols []string
	if file, err := macho.NewFile(bytes.NewReader(object)); err == nil {
		for _, symbol := range file.Symtab.Syms {
			symbols = append(symbols, strings.TrimPrefix(symbol.Name, "_"))
		}
		return symbols, nil
	}
	if file, err := pe.NewFile(bytes.NewReader(object)); err == nil {
		for _, symbol := range file.Symbols {
			symbols = append(symbols, symbol.Name)
		}
		return symbols, nil
	}
	file, err := elf.NewFile(bytes.NewReader(object))
	if err != nil {
		return nil, err
	}
	elfSymbols, err := file.Symbols()
	if err != nil {
		return nil, err
	}
	for _, symbol := range elfSymbols {
		symbols = append(symbols, symbol.Name)
	}
	return symbols, nil
}

// importedLibraries names the shared libraries an executable, Mach-O, PE or
// ELF, loads.
func importedLibraries(path string) ([]string, error) {
	if file, err := macho.Open(path); err == nil {
		defer file.Close()
		return file.ImportedLibraries()
	}
	if file, err := pe.Open(path); err == nil {
		defer file.Close()
		return file.ImportedLibraries()
	}
	file, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ImportedLibraries()
}

// TestNativeDebugBundle links a debuggable program on macOS with its DWARF in
// a debug bundle beside it. The bundle holds no script: lldb warns of one it is
// not told to trust, and zam debug imports the formatters itself.
func TestNativeDebugBundle(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("only macOS keeps DWARF beside the executable")
	}
	object, err := generateObject(t, exiting(`result = 0;`))
	require.NoError(t, err)
	cache, err := nativeRuntimeCache()
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "my-program")
	require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget(), Debugger: true}))

	_, err = os.Stat(filepath.Join(out+".dSYM", "Contents", "Resources", "DWARF", "my-program"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(out+".dSYM", "Contents", "Resources", "Python"))
	assert.True(t, os.IsNotExist(err), "the bundle holds a script")
}

// TestNativeEnumsWithMembers lays out enum values that carry members, set by
// the method each names: read through the value and a pointer to it, copied,
// compared, switched on, passed, held by an object and kept through a
// collection, as the interpreter does.
func TestNativeEnumsWithMembers(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    enum Planet {
        Mercury.Init(1, "hot"),
        Earth.Init(3, "home");

        var int order;
        var str note;

        fun void(int, str) Init = (o, n) {
            this.order = o;
            this.note = n + "!";
        }

        fun str() Describe = () {
            return this.note + "#" + this.order{str};
        }
    }

    class Holder {
        var Planet p;
    }

    static fun str(Planet) describe = (p) {
        switch(p) {
            case(Planet::Mercury): { return "first"; }
        } else { return p.Describe(); }
        return "";
    }

    static fun void() churn = () {
        for(var int i = 0; i < 20000; i++) {
            var str s = i{str} + "-churn";
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Planet p = Planet::Earth;
        var Planet q = p;
        var Holder* h = new(Holder);
        h.p = Planet::Mercury;
        Main::churn();
        System.GC::Major();
        File::StdOut.WriteStr(p.order{str} + " " + q.note + " " + h.p.note + " " + (p == Planet::Earth){str} + " " + (h.p == Planet::Earth){str} + "\n");
        File::StdOut.WriteStr(Main::describe(p) + " " + Main::describe(h.p) + " " + Planet::Mercury.Describe() + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "3 home! hot! true false\nhome!#3 first hot!#1\n", output)
}

// TestNativeInheritedGenericAbstractMethods calls, through a generic
// abstract, methods an instantiation inherits from it and overrides.
func TestNativeInheritedGenericAbstractMethods(t *testing.T) {
	t.Parallel()
	output, status := runNative(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    abstract Holder[A] {
        var A held;

        fun A() Get = () {
            return this.held;
        }

        virtual fun str() Name;
    }

    class Box[A] <: Holder[A]* {
        fun str() Name = () {
            return "box:" + this.held{str};
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Box[int]* b = new(Box[int]);
        b.held = 4;
        var Box[str]* s = new(Box[str]);
        s.held = "s";
        var Holder[int]* h = b;
        File::StdOut.WriteStr(h.Get(){str} + " " + h.Name() + " " + s.Get() + " " + s.Name() + "\n");
    }
}
`)
	assert.Zero(t, status, output)
	assert.Equal(t, "4 box:4 s box:s\n", output)
}

// TestNativeLinksOnlyWhatItCalls links the libraries the standard library
// wraps into a program only where it calls a native of theirs, and loads no
// shared library but the system's.
func TestNativeLinksOnlyWhatItCalls(t *testing.T) {
	t.Parallel()
	link := func(source string) (symbols, libraries []string) {
		object, err := generateObject(t, source)
		require.NoError(t, err)
		cache, err := nativeRuntimeCache()
		require.NoError(t, err)
		out := testTarget().Executable(filepath.Join(t.TempDir(), "out"))
		require.NoError(t, build.LinkObject(object.Bytes, build.Options{OutputPath: out, RuntimeCacheDir: cache, Target: testTarget()}))
		executable, err := os.ReadFile(out)
		require.NoError(t, err)
		symbols, err = objectSymbols(executable)
		require.NoError(t, err)
		libraries, err = importedLibraries(out)
		require.NoError(t, err)
		return symbols, libraries
	}

	hello, helloLibraries := link(`import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr("hello\n");
    }
}
`)
	assert.NotContains(t, hello, "json_tokener_parse")
	assert.NotContains(t, hello, "curl_easy_init")
	assert.True(t, systemOnly(helloLibraries), "%v", helloLibraries)

	json, jsonLibraries := link(`import std("lang/*");
import std("io/local/*");
import std("codec/format/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr(JSON::Parse("[1]").0{str} + "\n");
    }
}
`)
	assert.Contains(t, json, "json_tokener_parse")
	assert.NotContains(t, json, "curl_easy_init")
	assert.True(t, systemOnly(jsonLibraries), "%v", jsonLibraries)
}

// runsHere answers how to run a program built for t on this machine: itself
// for the host, under Wine for Windows on Linux of t's architecture, and nil
// where it cannot run.
func runsHere(t target.Target) func(path string) *exec.Cmd {
	if t.IsHost() {
		return func(path string) *exec.Cmd { return exec.Command(path) }
	}
	host, err := target.Host()
	if err != nil || t.OS != target.Windows || host.OS != target.Linux || host.Arch != t.Arch {
		return nil
	}
	if _, err := exec.LookPath("wine"); err != nil {
		return nil
	}
	return func(path string) *exec.Cmd { return exec.Command("wine", path) }
}

// TestNativeLinksForEveryTarget compiles and links one program for every
// target from its pack: a static ELF executable for Linux without debug
// information, a Mach-O one for macOS and a PE one for Windows, each for its
// architecture, and runs the ones this machine can.
func TestNativeLinksForEveryTarget(t *testing.T) {
	t.Parallel()
	source := `import std("lang/*");
import std("io/local/*");
import std("codec/format/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr(JSON::Parse("[1, 2]").0.At(1U).AsInt(){str} + "\n");
    }
}
`
	machines := map[string]struct {
		elf   elf.Machine
		macho macho.Cpu
		pe    uint16
	}{
		"aarch64": {elf.EM_AARCH64, macho.CpuArm64, pe.IMAGE_FILE_MACHINE_ARM64},
		"x86_64":  {elf.EM_X86_64, macho.CpuAmd64, pe.IMAGE_FILE_MACHINE_AMD64},
	}
	for _, name := range target.Names() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			platform, err := target.Parse(name)
			require.NoError(t, err)
			if _, err := build.Pack(platform); errors.Is(err, build.ErrNoPack) {
				t.Skip(err)
			}
			object, err := generateObjectFor(t, source, platform.Triple)
			require.NoError(t, err)
			out := platform.Executable(filepath.Join(t.TempDir(), "out"))
			require.NoError(t, build.LinkObject(object.Bytes, build.Options{
				OutputPath: out, RuntimeCacheDir: t.TempDir(), Target: platform,
			}))

			switch platform.OS {
			case target.Linux:
				file, err := elf.Open(out)
				require.NoError(t, err)
				defer file.Close()
				assert.Equal(t, machines[platform.Arch].elf, file.Machine)
				assert.False(t, slices.ContainsFunc(file.Progs, func(p *elf.Prog) bool { return p.Type == elf.PT_INTERP }),
					"a static executable names no loader")
				assert.Nil(t, file.Section(".debug_info"), "only a debuggable executable carries DWARF")
			case target.Windows:
				file, err := pe.Open(out)
				require.NoError(t, err)
				defer file.Close()
				assert.Equal(t, machines[platform.Arch].pe, file.Machine)
				libraries, err := file.ImportedLibraries()
				require.NoError(t, err)
				assert.True(t, systemOnly(libraries), "%v", libraries)
			case target.MacOS:
				file, err := macho.Open(out)
				require.NoError(t, err)
				defer file.Close()
				assert.Equal(t, machines[platform.Arch].macho, file.Cpu)
				libraries, err := file.ImportedLibraries()
				require.NoError(t, err)
				assert.True(t, systemOnly(libraries), "%v", libraries)
			}

			if run := runsHere(platform); run != nil {
				output, err := run(out).Output()
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					require.NoError(t, err, string(exit.Stderr))
				}
				require.NoError(t, err)
				assert.Equal(t, "2\n", string(output))
			}
		})
	}
}

// windowsSystemLibraries are the Windows DLLs a pack links against, besides
// the Universal C Runtime's.
var windowsSystemLibraries = []string{
	"kernel32.dll", "ntdll.dll", "advapi32.dll", "user32.dll", "shell32.dll", "ws2_32.dll",
	"crypt32.dll", "bcrypt.dll", "gdi32.dll", "secur32.dll", "iphlpapi.dll",
}

// systemOnly reports whether every shared library named is the system's: a
// static Linux executable loads none, a macOS one only what /usr/lib and
// /System hold, and a Windows one only the system's DLLs and the Universal C
// Runtime.
func systemOnly(libraries []string) bool {
	return !slices.ContainsFunc(libraries, func(l string) bool {
		name := strings.ToLower(l)
		return !strings.HasPrefix(l, "/usr/lib/") && !strings.HasPrefix(l, "/System/") &&
			!strings.HasPrefix(name, "api-ms-win-crt-") && !slices.Contains(windowsSystemLibraries, name)
	})
}
