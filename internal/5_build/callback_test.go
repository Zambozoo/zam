package build_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// compileWithNatives builds a one-file project that ships C files of its own,
// named in its native block, under the same small heap compile uses.
func compileWithNatives(t *testing.T, source string, natives map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	var yml strings.Builder
	yml.WriteString("name: \"natives\"\nversion: \"0.0.0\"\ndependencies:\nnative:\n  files:\n")
	for name, body := range natives {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
		fmt.Fprintf(&yml, "    - %s\n", name)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte(yml.String()), 0o600))
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

const callbackNatives = `#include <stdlib.h>
#include <string.h>

#include "zam/native.h"

// APPLY(int(int) f, int x, int* out) writes f(x).
void APPLY(ZamThread *thread) {
    uint64_t f = zam_native_argument(thread, 0);
    uint64_t x = zam_native_argument(thread, 1);
    uint64_t r = 0;
    zam_native_call(thread, f, &x, 1, &r, 1);
    *(int64_t *)zam_native_argument_pointer(thread, 2) = (int64_t)r;
    zam_native_return_void(thread);
}

static __thread ZamThread *sortThread;
static __thread uint64_t sortCompare;

static int compare(const void *a, const void *b) {
    uint64_t args[2] = {*(const uint64_t *)a, *(const uint64_t *)b};
    uint64_t r = 0;
    zam_native_call(sortThread, sortCompare, args, 2, &r, 1);
    return (int)(int64_t)r;
}

// SORT(int* a, int n, int(int, int) cmp) sorts with qsort, calling cmp. The
// run is sorted in a copy, the argument pointer going stale once a callback
// runs.
void SORT(ZamThread *thread) {
    int64_t n = (int64_t)zam_native_argument(thread, 1);
    uint64_t *copy = malloc((size_t)n * sizeof *copy);
    memcpy(copy, zam_native_argument_pointer(thread, 0), (size_t)n * sizeof *copy);
    sortThread = thread;
    sortCompare = zam_native_argument(thread, 2);
    qsort(copy, (size_t)n, sizeof *copy, compare);
    memcpy(zam_native_argument_pointer(thread, 0), copy, (size_t)n * sizeof *copy);
    free(copy);
    zam_native_return_void(thread);
}

// EACH(int n, void(int) f) calls f(i) for each i below n.
void EACH(ZamThread *thread) {
    int64_t n = (int64_t)zam_native_argument(thread, 0);
    uint64_t f = zam_native_argument(thread, 1);
    for (int64_t i = 0; i < n; i++) {
        uint64_t arg = (uint64_t)i;
        zam_native_call(thread, f, &arg, 1, NULL, 0);
    }
    zam_native_return_void(thread);
}

// GREET(str(str) f, str* out) writes f("zam").
void GREET(ZamThread *thread) {
    uint64_t f = zam_native_argument(thread, 0);
    uint64_t name = zam_native_new_string(thread, "zam", 3);
    uint64_t r = 0;
    zam_native_call(thread, f, &name, 1, &r, 1);
    *(uint64_t *)zam_native_argument_pointer(thread, 1) = r;
    zam_native_return_void(thread);
}
`

// TestNativeCallback calls zam functions from C: plain, capturing and bound
// method callbacks, a qsort comparator, string results, a callback calling a
// native that calls back again, and enough allocating callbacks to collect
// while they run.
func TestNativeCallback(t *testing.T) {
	t.Parallel()
	binary := compileWithNatives(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static var int total = 0;

    class Scaler {
        var int factor;

        fun int(int) Scale = (x) {
            return x * this.factor;
        }
    }

    static fun int(int(int), int) apply = (f, x) {
        var int out;
        native(APPLY){void(int(int), int, int*)}(f, x, &out);
        return out;
    }

    static fun int(int) twice = (x) {
        return 2 * x;
    }

    static fun int(int, int) descending = (a, b) {
        return b - a;
    }

    static fun void(int) churn = (i) {
        var str s = i{str} + "-churn";
        Main::total = Main::total + Strs::ByteLen(s);
    }

    static fun str(str) hello = (name) {
        return "hello " + name + "!";
    }

    fun void(uint, str...) main = (argC, argV) {
        var int k = 10;
        var Scaler* sc = new(Scaler);
        sc.factor = 7;
        var str s = Main::apply(Main::twice, 21){str};
        s = s + " " + Main::apply(lambda int(int):(x) { return x + k; }, 5){str};
        s = s + " " + Main::apply(sc.Scale, 6){str};
        s = s + " " + Main::apply(lambda int(int):(x) { return Main::apply(Main::twice, x) + 1; }, 4){str} + "\n";

        var int* a = new(int, 6U);
        a[0] = 5;
        a[1] = 3;
        a[2] = 9;
        a[3] = 1;
        a[4] = 7;
        a[5] = 3;
        native(SORT){void(int*, int, int(int, int))}(a, 6, Main::descending);
        for(var int i = 0; i < 6; i++) {
            s = s + a[i]{str} + " ";
        }
        free(a);

        native(EACH){void(int, void(int))}(3000, Main::churn);
        var str greeting;
        native(GREET){void(str(str), str*)}(Main::hello, &greeting);
        s = s + Main::total{str} + " " + greeting + "\n";
        File::StdOut.WriteStr(s);
    }
}
`, map[string]string{"callbacks.c": callbackNatives})
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	total := 0
	for i := range 3000 {
		total += len(strconv.Itoa(i)) + len("-churn")
	}
	assert.Equal(t, "42 15 42 9\n9 7 5 3 3 1 "+strconv.Itoa(total)+" hello zam!\n", stdout)
}
