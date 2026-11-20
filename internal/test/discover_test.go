package test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

// writeFiles lays files down under a new project directory and returns it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["zam.yml"] = "name: \"project\"\n"
	for name, body := range files {
		file := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o750))
		require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
	}
	return dir
}

func TestResolve(t *testing.T) {
	t.Parallel()
	dir := writeFiles(t, map[string]string{
		"A.test.zam":     "",
		"A.zam":          "",
		"sub/B.test.zam": "",
	})
	a := base.Path(filepath.Join(dir, "A.test.zam"))
	b := base.Path(filepath.Join(dir, "sub", "B.test.zam"))
	files := &base.FileProvider{}

	for name, test := range map[string]struct {
		targets []string
		want    []base.Path
		err     string
	}{
		"a directory":              {targets: []string{dir}, want: []base.Path{a}},
		"a directory recursively":  {targets: []string{dir + "/..."}, want: []base.Path{a, b}},
		"a file":                   {targets: []string{string(b)}, want: []base.Path{b}},
		"in the order named, once": {targets: []string{string(b), dir + "/..."}, want: []base.Path{b, a}},
		"a missing target":         {targets: []string{filepath.Join(dir, "missing")}, err: "does not exist"},
		"a file that is not a test": {
			targets: []string{filepath.Join(dir, "A.zam")}, err: "is not a directory or a .test.zam file",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(files, test.targets)
			if test.err != "" {
				assert.ErrorContains(t, err, test.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

// parse parses the test files at paths under dir.
func parse(t *testing.T, paths ...base.Path) *syntax.AST {
	t.Helper()
	ast, errs := syntax.NewAST(&base.Command{InputPath: paths[0], TestPaths: paths[1:], MaxThreads: 1},
		&base.FileProvider{}, nil)
	require.Empty(t, errs)
	return ast
}

// A test is a public static method of a test file's root declaration with a
// main method's type, kept in the order it was written. Nothing else is.
func TestDiscover(t *testing.T) {
	t.Parallel()
	dir := writeFiles(t, map[string]string{
		"List.test.zam": `public namespace ListTest {
    public fun void(uint, str...) Zeta = (argC, argV) {}
    public fun void(uint, str...) Alpha = (argC, argV) {}
    fun void(uint, str...) Private = (argC, argV) {}
    public fun void() Helper = () {}
    public fun int(uint, str...) Returns = (argC, argV) { return 0; }
}
`,
		"Map.test.zam": `public namespace MapTest {
    public fun void(uint, str...) Get = (argC, argV) {}
}
`,
	})
	list := base.Path(filepath.Join(dir, "List.test.zam"))
	maps := base.Path(filepath.Join(dir, "Map.test.zam"))
	ast := parse(t, list, maps)

	got, err := Discover(ast, []base.Path{list, maps}, regexp.MustCompile(""))
	require.NoError(t, err)
	assert.Equal(t, []File{
		{Path: list, Decl: "ListTest", Tests: []string{"Zeta", "Alpha"}},
		{Path: maps, Decl: "MapTest", Tests: []string{"Get"}},
	}, got)

	got, err = Discover(ast, []base.Path{list, maps}, regexp.MustCompile(`^ListTest\.A`))
	require.NoError(t, err)
	assert.Equal(t, []File{{Path: list, Decl: "ListTest", Tests: []string{"Alpha"}}}, got,
		"the pattern matches the name qualified by the declaration, and a file left with no tests is dropped")
}

// The runner reaches a test file's root declaration by a name it works out
// from the file's, and from whatever package it sits in.
func TestDiscoverRefusesAnUnreachableRoot(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		source string
		want   string
	}{
		"misnamed":   {"public namespace Tests {}\n", "must be named ListTest, not Tests"},
		"not public": {"namespace ListTest {}\n", "must be public"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := writeFiles(t, map[string]string{"List.test.zam": test.source})
			list := base.Path(filepath.Join(dir, "List.test.zam"))

			_, err := Discover(parse(t, list), []base.Path{list}, regexp.MustCompile(""))
			assert.ErrorContains(t, err, test.want)
		})
	}
}

func TestSource(t *testing.T) {
	t.Parallel()

	got, err := Source([]File{
		{Path: "/p/list/List.test.zam", Decl: "ListTest", Tests: []string{"Append", "Remove"}},
		{Path: "/p/map/Map.test.zam", Decl: "MapTest", Tests: []string{"Get"}},
	})
	require.NoError(t, err)
	assert.Equal(t, `import std("test/Test.zam");
import t0 = "/p/list/List.test.zam";
import t1 = "/p/map/Map.test.zam";

namespace ZamTestMain {
    fun void(uint, str...) main = (argC, argV) {
        var uint n = argC - 1U;
        var str* args = new(str, argC);
        for (var uint i = 0U; i < n; i = i + 1U) {
            args[i] = argV[i + 1U];
        }
        if (argV[0] == "0") {
            Test::Begin("ListTest.Append");
            t0.ListTest::Append(n, args...);
            Test::End();
            Test::Begin("ListTest.Remove");
            t0.ListTest::Remove(n, args...);
            Test::End();
        } else if (argV[0] == "1") {
            Test::Begin("MapTest.Get");
            t1.MapTest::Get(n, args...);
            Test::End();
        }
        Test::Finish();
    }
}
`, got)

	_, err = Source([]File{{Path: `/p/"quoted"/A.test.zam`, Decl: "ATest", Tests: []string{"A"}}})
	assert.ErrorContains(t, err, "cannot be imported by the test runner")
}
