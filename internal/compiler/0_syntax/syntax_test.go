package syntax

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
)

//go:embed testdata
var testdata embed.FS

// entryName is the file each case's parse begins at.
const entryName = "Main.zam"

// depHash is the hash every dependency under testdata/deps is fetched with.
const depHash = "h1"

// depDirs maps each repository a dependency is fetched as onto its directory
// under testdata/deps.
var depDirs = map[string]string{
	"example.com/dep@v1":      "dep",
	"example.com/rootless@v1": "rootless",
	"example.com/outside@v1":  "outside",
}

// errInjected is the failure a case injects into a provider.
var errInjected = errors.New("injected")

// cases answers the case directories under dir in the embedded testdata.
func cases(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := fs.ReadDir(testdata, path.Join("testdata", dir))
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	require.NotEmpty(t, names)
	return names
}

// testdataRoot is where the embedded testdata is addressed from: a directory
// that is not on disk, so that nothing a parse writes reaches the source tree.
func testdataRoot(t *testing.T) (base.Path, fs.FS) {
	t.Helper()
	root, err := base.ParsePath("embedded")
	require.NoError(t, err)
	fsys, err := fs.Sub(testdata, "testdata")
	require.NoError(t, err)
	return root, fsys
}

// newFiles answers a file provider that reads paths under root from fsys and
// the standard library as the compiler does. Expectations set by override
// are matched before the ones that read fsys.
func newFiles(t *testing.T, root base.Path, fsys fs.FS, override func(*mock_FileProvider)) *mock_FileProvider {
	t.Helper()
	std := &base.FileProvider{}
	files := newMock_FileProvider(t)
	if override != nil {
		override(files)
	}

	key := func(p base.Path) (string, bool) {
		rel, err := filepath.Rel(string(root), string(p))
		if err != nil || !root.Contains(p) {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}

	files.EXPECT().NewPath(mock.Anything).RunAndReturn(base.ParsePath).Maybe()
	files.EXPECT().Reader(mock.Anything).RunAndReturn(func(p base.Path) (io.ReadCloser, error) {
		if base.IsStdPath(p) {
			return std.Reader(p)
		}
		name, ok := key(p)
		if !ok {
			return nil, fs.ErrNotExist
		}
		return fsys.Open(name)
	}).Maybe()
	files.EXPECT().ListCodeFiles(mock.Anything).RunAndReturn(func(dir base.Path) ([]base.Path, error) {
		if base.IsStdPath(dir) {
			return std.ListCodeFiles(dir)
		}
		name, ok := key(dir)
		if !ok {
			return nil, fs.ErrNotExist
		}
		entries, err := fs.ReadDir(fsys, name)
		if err != nil {
			return nil, err
		}
		var children []base.Path
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), base.CodeFileExtension) {
				children = append(children, base.Path(filepath.Join(string(dir), entry.Name())))
			}
		}
		return children, nil
	}).Maybe()
	files.EXPECT().ProjectFilePath(mock.Anything).RunAndReturn(func(p base.Path) (base.Path, error) {
		if base.IsStdPath(p) {
			return std.ProjectFilePath(p)
		}
		name, ok := key(p)
		if !ok {
			return "", fs.ErrNotExist
		}
		for dir := path.Dir(name); ; dir = path.Dir(dir) {
			for _, project := range []string{"zam.yml", "zam.yaml"} {
				if _, err := fs.Stat(fsys, path.Join(dir, project)); err == nil {
					return base.Path(filepath.Join(string(root), filepath.FromSlash(path.Join(dir, project)))), nil
				}
			}
			if dir == "." {
				return "", fs.ErrNotExist
			}
		}
	}).Maybe()
	return files
}

// newPackages answers a package provider that fetches the repositories of
// depDirs from under root, and fails to fetch anything else.
func newPackages(t *testing.T, root base.Path) *mock_PackageProvider {
	t.Helper()
	packages := newMock_PackageProvider(t)
	packages.EXPECT().Fetch(mock.Anything).RunAndReturn(func(repo string) (base.Path, string, error) {
		dir, ok := depDirs[repo]
		if !ok {
			return "", "", errInjected
		}
		return base.Path(filepath.Join(string(root), "deps", dir)), depHash, nil
	}).Maybe()
	return packages
}

// parse parses the program whose entry is dir's Main.zam under root.
func parse(t *testing.T, cmd *cli.Command, root base.Path, dir string, files fileProvider, packages packageProvider) (*AST, []error) {
	t.Helper()
	cmd.InputPath = base.Path(filepath.Join(string(root), filepath.FromSlash(dir), entryName))
	if cmd.MaxThreads == 0 {
		cmd.MaxThreads = 4
	}
	return NewAST(cmd, files, packages)
}

// Every valid program parses without an error.
func TestNewASTValid(t *testing.T) {
	t.Parallel()
	root, fsys := testdataRoot(t)
	for _, name := range cases(t, "valid") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ast, errs := parse(t, &cli.Command{}, root, path.Join("valid", name), newFiles(t, root, fsys, nil), newPackages(t, root))
			require.Empty(t, errs)
			assert.NotNil(t, ast)
		})
	}
}

// Every invalid program fails to parse.
func TestNewASTInvalid(t *testing.T) {
	t.Parallel()
	root, fsys := testdataRoot(t)
	for _, name := range cases(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, errs := parse(t, &cli.Command{}, root, path.Join("invalid", name), newFiles(t, root, fsys, nil), newPackages(t, root))
			assert.NotEmpty(t, errs)
		})
	}
}

// What a command asks of the parse beyond its entry is honoured.
func TestNewASTCommand(t *testing.T) {
	t.Parallel()
	root, fsys := testdataRoot(t)
	for name, tc := range map[string]struct {
		cmd     *cli.Command
		dir     string
		invalid bool
	}{
		"without a project file": {cmd: &cli.Command{NoProject: true}, dir: "valid/long"},
		"with test files": {
			cmd: &cli.Command{TestPaths: []base.Path{base.Path(filepath.Join(string(root), "tests", "Case.test.zam"))}},
			dir: "tests",
		},
		"one thread":                       {cmd: &cli.Command{MaxThreads: 1}, dir: "valid/project"},
		"recording hashes it cannot write": {cmd: &cli.Command{WriteProject: true}, dir: "unhashed", invalid: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, errs := parse(t, tc.cmd, root, tc.dir, newFiles(t, root, fsys, nil), newPackages(t, root))
			if tc.invalid {
				assert.NotEmpty(t, errs)
			} else {
				assert.Empty(t, errs)
			}
		})
	}
}

// A project whose dependencies lack hashes has them written into its project
// file when the command asks.
func TestNewASTWritesHashes(t *testing.T) {
	t.Parallel()
	_, fsys := testdataRoot(t)
	dir := t.TempDir()
	root, err := base.ParsePath(dir)
	require.NoError(t, err)
	require.NoError(t, os.CopyFS(dir, fsys))

	_, errs := parse(t, &cli.Command{WriteProject: true}, root, "unhashed",
		newFiles(t, root, os.DirFS(dir), nil), newPackages(t, root))
	assert.Empty(t, errs)
}

// byteReader answers a reader of source that yields a byte per read, giving
// io.EOF with the last byte, or fail once it has yielded failAfter bytes when
// fail is set.
func byteReader(t *testing.T, source string, failAfter int, fail error) io.ReadCloser {
	t.Helper()
	r := newMock_ReadCloser(t)
	read := 0
	r.EXPECT().Read(mock.Anything).RunAndReturn(func(b []byte) (int, error) {
		switch {
		case fail != nil && read == failAfter:
			return 0, fail
		case read == len(source):
			return 0, io.EOF
		}
		b[0] = source[read]
		read++
		if read == len(source) {
			return 1, io.EOF
		}
		return 1, nil
	}).Maybe()
	r.EXPECT().Close().Return(nil).Maybe()
	return r
}

// A provider that fails fails the parse, and one that reads unusually does not.
func TestNewASTProviders(t *testing.T) {
	t.Parallel()
	root, fsys := testdataRoot(t)
	entry := base.Path(filepath.Join(string(root), "valid", "project", entryName))
	isEntry := mock.MatchedBy(func(p base.Path) bool { return p == entry })
	isProject := mock.MatchedBy(func(p base.Path) bool { return strings.HasSuffix(string(p), "zam.yml") && !base.IsStdPath(p) })
	isLocalDir := mock.MatchedBy(func(p base.Path) bool { return !base.IsStdPath(p) })

	for name, tc := range map[string]struct {
		override func(t *testing.T, files *mock_FileProvider)
		valid    bool
	}{
		"entry unreadable": {override: func(_ *testing.T, files *mock_FileProvider) {
			files.EXPECT().Reader(isEntry).Return(nil, errInjected)
		}},
		"entry read fails": {override: func(t *testing.T, files *mock_FileProvider) {
			files.EXPECT().Reader(isEntry).Return(byteReader(t, "", 0, errInjected), nil)
		}},
		"entry read fails within a rune": {override: func(t *testing.T, files *mock_FileProvider) {
			files.EXPECT().Reader(isEntry).Return(byteReader(t, "\xe2\x82\xac", 1, errInjected), nil)
		}},
		"entry reads nothing without ending": {override: func(t *testing.T, files *mock_FileProvider) {
			r := newMock_ReadCloser(t)
			r.EXPECT().Read(mock.Anything).Return(0, nil).Maybe()
			r.EXPECT().Close().Return(nil).Maybe()
			files.EXPECT().Reader(isEntry).Return(r, nil)
		}},
		"entry ends with its last byte": {valid: true, override: func(t *testing.T, files *mock_FileProvider) {
			files.EXPECT().Reader(isEntry).Return(byteReader(t, "public namespace Main {\n}\n", 0, nil), nil)
		}},
		"project file unreadable": {override: func(_ *testing.T, files *mock_FileProvider) {
			files.EXPECT().Reader(isProject).Return(nil, errInjected)
		}},
		"project file read fails": {override: func(t *testing.T, files *mock_FileProvider) {
			files.EXPECT().Reader(isProject).Return(byteReader(t, "", 0, errInjected), nil)
		}},
		"project file missing": {override: func(_ *testing.T, files *mock_FileProvider) {
			files.EXPECT().ProjectFilePath(isEntry).Return("", errInjected)
		}},
		"path refused": {override: func(_ *testing.T, files *mock_FileProvider) {
			files.EXPECT().NewPath(mock.Anything).Return("", errInjected)
		}},
		"directory unlistable": {override: func(_ *testing.T, files *mock_FileProvider) {
			files.EXPECT().ListCodeFiles(isLocalDir).Return(nil, errInjected)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := newFiles(t, root, fsys, func(files *mock_FileProvider) { tc.override(t, files) })
			_, errs := parse(t, &cli.Command{}, root, "valid/project", files, newPackages(t, root))
			if tc.valid {
				assert.Empty(t, errs)
			} else {
				assert.NotEmpty(t, errs)
			}
		})
	}
}

// badByte is a byte no UTF-8 sequence begins with, which the lexer cannot
// read past, and bad is the token made of it, which the token before it ends
// without reading.
const (
	badByte = "\xff"
	bad     = " " + badByte
)

// Sources that open a field, a statement or an expression for the sources of
// TestNewASTUnreadable to go on from.
const (
	inField = "namespace Main {\n"
	inStmt  = inField + "fun void() Run = () {\n"
	inExpr  = inStmt + "a = "
)

// A source the lexer cannot read fails to parse, wherever the parse stops.
// Each is the entry of an otherwise empty project.
func TestNewASTUnreadable(t *testing.T) {
	t.Parallel()
	for name, source := range map[string]string{
		"first token":                      badByte,
		"after import":                     "import" + bad,
		"after an import brace":            "import {" + bad,
		"after an import group":            `import { "A.zam"; }` + bad,
		"within an import group":           `import { "A.zam"` + bad,
		"after an import name":             "import a" + bad,
		"after an import alias":            "import a =" + bad,
		"after an import package":          "import a = b" + bad,
		"after a modifier":                 "public" + bad,
		"after a class tilde":              "class A~" + bad,
		"after a class bracket":            "class A[" + bad,
		"after a class subtype":            "class A <:" + bad,
		"after a class base":               "class A <: B" + bad,
		"after a class body":               "class A {}" + bad,
		"within a class body":              "class A { var" + bad,
		"after an abstract tilde":          "abstract A~" + bad,
		"after an abstract bracket":        "abstract A[" + bad,
		"after an abstract subtype":        "abstract A <:" + bad,
		"after an abstract base":           "abstract A <: B" + bad,
		"after an abstract body":           "abstract A {}" + bad,
		"within an abstract body":          "abstract A { var" + bad,
		"after an enum colon":              "enum A:" + bad,
		"after an enum primitive":          "enum A: int" + bad,
		"after enum values":                "enum A {;" + bad,
		"within enum values":               "enum A { B" + bad,
		"after an enum body":               "enum A {;}" + bad,
		"within an enum body":              "enum A {; var" + bad,
		"after an enum value paren":        "enum A { B(" + bad,
		"after an enum value period":       "enum A { B." + bad,
		"after enum value arguments":       "enum A { B.C()" + bad,
		"after an interface bracket":       "interface A[" + bad,
		"after an interface body":          "interface A {}" + bad,
		"within an interface body":         "interface A { fun" + bad,
		"after a namespace body":           "namespace A {}" + bad,
		"after a struct tilde":             "struct A~" + bad,
		"after a struct bracket":           "struct A[" + bad,
		"after a struct body":              "struct A {}" + bad,
		"within a struct body":             "struct A { var" + bad,
		"after a bound":                    "class A[T <:" + bad,
		"after a bound bracket":            "class A[T <: [" + bad,
		"within a bound list":              "class A[T <: [B" + bad,
		"after a bound list comma":         "class A[T <: [B," + bad,
		"after an alias bracket":           "alias A[" + bad,
		"after an alias subtype":           "alias A <:" + bad,
		"after a member equals":            inField + "var int a =" + bad,
		"after a method bracket":           inField + "fun[" + bad,
		"after a method equals":            inField + "fun void() A =" + bad,
		"after method parameters":          inField + "fun void() A = ()" + bad,
		"after a statement keyword":        inStmt + "for" + bad,
		"after a break label":              inStmt + "break a" + bad,
		"after a bare return":              inStmt + "return;" + bad,
		"after a for paren":                inStmt + "for (" + bad,
		"after a for init":                 inStmt + "for (;" + bad,
		"after a for condition":            inStmt + "for (;;" + bad,
		"within a for condition":           inStmt + "for (; a" + bad,
		"after a for step":                 inStmt + "for (;;)" + bad,
		"within a for step":                inStmt + "for (;; a" + bad,
		"after a for else":                 inStmt + "for {} else" + bad,
		"within an if condition":           inStmt + "if (a" + bad,
		"after an if else":                 inStmt + "if (a) {} else" + bad,
		"within a switch value":            inStmt + "switch (a" + bad,
		"after a switch else":              inStmt + "switch (a) {} else" + bad,
		"after a block":                    inStmt + "{}" + bad,
		"after a block import":             inStmt + "import" + bad,
		"after a block import brace":       inStmt + "import {" + bad,
		"after a variable comma":           inStmt + "var int a," + bad,
		"within a variable value":          inStmt + "var int a = b" + bad,
		"while skipping":                   inStmt + "a b" + bad,
		"while skipping a brace":           inStmt + "a b {" + bad,
		"after an expression comma":        inExpr + "b," + bad,
		"within an assigned value":         inExpr + "b" + bad,
		"after a question":                 inExpr + "b ?" + bad,
		"after an operator":                inExpr + "b +" + bad,
		"after a repeated operator":        inExpr + "b + c +" + bad,
		"after a prefix":                   inExpr + "-" + bad,
		"after a postfix":                  inExpr + "b++" + bad,
		"after a typeof brace":             inExpr + "@{" + bad,
		"within parentheses":               inExpr + "(b" + bad,
		"after colons":                     inExpr + "b::" + bad,
		"after colons and a bracket":       inExpr + "b::[" + bad,
		"after call parentheses":           inExpr + "f()" + bad,
		"after a spread":                   inExpr + "f(b..." + bad,
		"within a native":                  inExpr + "native(X" + bad,
		"after new":                        inExpr + "new(" + bad,
		"within a new type":                inExpr + "new(int" + bad,
		"after a new type":                 inExpr + "new(int," + bad,
		"within a new size":                inExpr + "new(int, b" + bad,
		"after a new size":                 inExpr + "new(int, b," + bad,
		"within a new tail":                inExpr + "new(int, b, c" + bad,
		"after a new tail":                 inExpr + "new(int, b, c)" + bad,
		"within a lambda type":             inExpr + "lambda int" + bad,
		"after a lambda colon":             inExpr + "lambda int():" + bad,
		"after lambda parentheses":         inExpr + "lambda int(): (" + bad,
		"within a lambda body":             inExpr + "lambda int(): () {" + bad,
		"after a tuple index":              inExpr + "b.0" + bad,
		"after a type period":              inField + "var A." + bad,
		"after a type tilde":               inField + "var A~" + bad,
		"after a tail size":                inField + "var A~4" + bad,
		"after a type bracket":             inField + "var A[" + bad,
		"after an array size":              inField + "var A[4" + bad,
		"after a type argument bound":      inField + "var A[B <:" + bad,
		"after a type star":                inField + "var int*" + bad,
		"after function parentheses":       inField + "var int()" + bad,
		"after a variadic":                 inField + "var int(int..." + bad,
		"after a slash":                    "/" + badByte,
		"within a block comment":           "/*" + badByte,
		"within a block comment end":       "/* " + badByte,
		"within a number":                  "1" + badByte,
		"within a fraction":                "1." + badByte,
		"within a long fraction":           "1.5" + badByte,
		"within an exponent":               "1e" + badByte,
		"within an exponent sign":          "1e-" + badByte,
		"within a symbol":                  "=" + badByte,
		"slash at the end":                 "/",
		"within an identifier":             "zz" + badByte,
		"within a keyword":                 "in" + badByte,
		"within a string":                  `"` + badByte,
		"within a char":                    "'" + badByte,
		"after a char":                     "'a" + badByte,
		"within an escape":                 `"\` + badByte,
		"within a short code point":        `"\u` + badByte,
		"within a long code point":         `"\U` + badByte,
		"unterminated block comment":       "/*",
		"unterminated block comment end":   "/* a",
		"tuple index overflow":             inExpr + "b.99999999999999999999;",
		"number at the end":                "12",
		"fraction missing":                 "1.",
		"fraction not a digit":             inExpr + "1.x;",
		"fraction at the end":              "1.5",
		"exponent at the end":              "1e5",
		"invalid posit":                    "1ep",
		"integer overflow":                 inExpr + "99999999999999999999;",
		"invalid float":                    inExpr + "1e;",
		"code point too large":             `"\U00110000"`,
		"surrogate code point":             `"\uD800"`,
		"escape at the end":                `"\`,
		"single quote escaped in a string": `"\'"`,
		"double quote escaped in a char":   `'\"'`,
		"short code point at the end":      `"\u12`,
		"short code point invalid":         `"\u12x"`,
		"long code point at the end":       `"\U1234`,
		"long code point invalid":          `"\U1234x"`,
		"unknown escape":                   `"\q"`,
		"unterminated string":              `"abc`,
		"unterminated char":                "'",
		"empty char":                       "''",
		"char at the end":                  "'a",
		"long char":                        "'ab'",
		"identifier at the end":            "zz",
		"invalid symbol":                   "`",
		"continuation lead byte":           "\x80",
		"incomplete rune":                  "\xe2\x82",
		"invalid continuation":             "\xe2\x28\xa1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root, err := base.ParsePath("unreadable")
			require.NoError(t, err)
			fsys := fstest.MapFS{
				"zam.yml": {Data: []byte("name: unreadable\nversion: 0.0.0\n")},
				entryName: {Data: []byte(source)},
			}
			_, errs := parse(t, &cli.Command{}, root, ".", newFiles(t, root, fsys, nil), newPackages(t, root))
			assert.NotEmpty(t, errs)
		})
	}
}
