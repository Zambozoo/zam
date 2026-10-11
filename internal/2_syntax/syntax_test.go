package syntax

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mainSource is an entry file that does nothing but import what it is given.
const mainSource = `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        return;
    }
}
`

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func writeManifest(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, "zam.yaml", "name: \"imports\"\nversion: \"0.0.0\"\ndependencies:\n")
}

// namespaceSource is a file holding nothing but a namespace, so that importing
// it is all the test is asking about.
func namespaceSource(name string) string {
	return fmt.Sprintf("namespace %s {\n    static var int v = 1;\n}\n", name)
}

// writeProject lays down a project whose entry file imports one file per name,
// and answers the entry file's path.
func writeProject(t *testing.T, names ...string) *base.Path {
	t.Helper()

	dir := t.TempDir()
	writeManifest(t, dir)

	var imports strings.Builder
	for _, name := range names {
		writeFile(t, dir, name+base.CodeFileExtension, namespaceSource(name))
		fmt.Fprintf(&imports, "import %s = %q;\n",
			strings.ToLower(name), name+base.CodeFileExtension)
	}

	return base.NewPath(writeFile(t, dir, "Main"+base.CodeFileExtension,
		imports.String()+mainSource), "")
}

// codeFilePaths answers where every file in the tree was read from, relative to
// the directory the entry file is in.
func codeFilePaths(t *testing.T, ast *AST, dir string) []string {
	t.Helper()

	var paths []string
	for _, entry := range ast.CodeFiles.All() {
		relative, err := filepath.Rel(dir, entry.Key.Outer)
		require.NoError(t, err)
		paths = append(paths, filepath.ToSlash(relative))
	}
	return paths
}

// Every project has std in its dependencies without the yaml having to say
// so. A project's own imports can reach the standard library the same way
// they reach any other dependency, by naming it in `import std("...")`, and
// a project file never has to repeat it.
func TestNewASTAutoIncludesStdInEveryProjectsDependencies(t *testing.T) {
	entry := writeProject(t, "A")

	ast, errs := NewAST(
		&command.Command{InputPath: entry, MaxThreads: 1},
		&base.FileProvider{}, &mock_GitProvider{})

	require.Empty(t, errs)
	require.NotNil(t, ast)

	// Every project the parse reaches has std among its dependencies, with
	// the one exception: the std project does not depend on itself. The test
	// project here is the only one actually loaded from disk, since no file
	// imports anything out of std yet, but what is pinned is the Dependencies
	// map rather than which projects happened to be loaded.
	require.Positive(t, ast.Projects.Len())
	for _, entry := range ast.Projects.All() {
		stdDep, ok := entry.Value.Dependencies[base.StdPackage]
		assert.True(t, ok, "project %q is missing the std dependency", entry.Value.Name)
		assert.True(t, base.IsStdPath(&stdDep),
			"project %q has std dependency %q, not a std path", entry.Value.Name, stdDep.String())
	}
}

// An import names a file beside the file it is written in, which for a file in a
// subdirectory is a different place than beside the entry file — and a different
// place again than beside whatever directory the compiler was run from. Only the
// first of those finds sub/Leaf.zam from the import written in sub/Branch.zam.
func TestNewASTResolvesImportsRelativeToTheImportingFile(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)

	writeFile(t, dir, "sub/Leaf.zam", namespaceSource("Leaf"))
	writeFile(t, dir, "sub/Branch.zam", "import leaf = \"Leaf.zam\";\n"+namespaceSource("Branch"))
	entry := writeFile(t, dir, "Main.zam", "import branch = \"sub/Branch.zam\";\n"+mainSource)

	ast, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 2},
		&base.FileProvider{}, &mock_GitProvider{})

	require.Empty(t, errs)
	assert.Equal(t, []string{"Main.zam", "sub/Branch.zam", "sub/Leaf.zam"},
		codeFilePaths(t, ast, dir))
}

// A file's imports are handed on by the parse that finds them, so a queue that
// same parse has to drain is one a single file can fill. It did: the queue held
// one path per thread, and at the default of one thread any file with two
// imports stopped the compiler where it stood. What matters is the count of
// imports against the count of threads, so this asks for more of the first.
func TestNewASTParsesMoreImportsThanThreads(t *testing.T) {
	entry := writeProject(t, "A", "B", "C", "D")

	for _, threads := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("%d threads", threads), func(t *testing.T) {
			cmd := &command.Command{InputPath: entry, MaxThreads: threads}

			type result struct {
				ast  *AST
				errs []error
			}
			done := make(chan result, 1)
			go func() {
				ast, errs := NewAST(cmd, &base.FileProvider{}, &mock_GitProvider{})
				done <- result{ast, errs}
			}()

			// A pending timer is what keeps the runtime from calling a stalled
			// parse a deadlock and taking the whole test binary down with it:
			// the failure is reported here instead.
			select {
			case got := <-done:
				require.Empty(t, got.errs)
				assert.NotNil(t, got.ast.MainCodeDecl, "the entry file is what the run begins in")
				assert.Equal(t, 5, got.ast.CodeFiles.Len(), "the entry file and its four imports")
			case <-time.After(30 * time.Second):
				t.Fatal("parsing did not finish: a file cannot hand on more imports than it has room to queue")
			}
		})
	}
}

// countingFileProvider is the real provider with a count kept of how many
// parses are inside it at once.
type countingFileProvider struct {
	base.FileProvider
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (fp *countingFileProvider) Reader(path *base.Path) (io.ReadCloser, error) {
	inFlight := fp.inFlight.Add(1)
	defer fp.inFlight.Add(-1)
	for peak := fp.peak.Load(); inFlight > peak; peak = fp.peak.Load() {
		if fp.peak.CompareAndSwap(peak, inFlight) {
			break
		}
	}

	// Held here long enough that parses which are allowed to overlap do, so a
	// peak below the limit means the limit was the reason.
	time.Sleep(20 * time.Millisecond)

	return fp.FileProvider.Reader(path)
}

// What MaxThreads bounds is how many files are being parsed at once, which is
// where the file handles and the work are. It is not a bound on goroutines:
// there is one per file discovered, and all but a few of them are asleep
// waiting for a turn.
func TestNewASTBoundsParsingToThreadCount(t *testing.T) {
	entry := writeProject(t, "A", "B", "C", "D", "E", "F", "G", "H")

	for _, threads := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("%d threads", threads), func(t *testing.T) {
			fileProvider := &countingFileProvider{}

			_, errs := NewAST(
				&command.Command{InputPath: entry, MaxThreads: threads},
				fileProvider, &mock_GitProvider{})

			require.Empty(t, errs)
			assert.Equal(t, int32(threads), fileProvider.peak.Load(),
				"nine files were there to parse, so the count at once is the limit's doing")
		})
	}
}

// Parsing runs concurrently and over a map that does not order itself, so the
// order the files come out in has to be put there. It reaches type ids and the
// order static initializers run in, which is to say the program's meaning.
func TestNewASTOrdersCodeFilesByPath(t *testing.T) {
	entry := writeProject(t, "D", "C", "B", "A")
	cmd := &command.Command{InputPath: entry, MaxThreads: 4}

	var orders [][]string
	for range 8 {
		ast, errs := NewAST(cmd, &base.FileProvider{}, &mock_GitProvider{})
		require.Empty(t, errs)

		var paths []string
		for _, entry := range ast.CodeFiles.All() {
			paths = append(paths, entry.Key.Base())
		}
		orders = append(orders, paths)
	}

	assert.Equal(t, []string{"A.zam", "B.zam", "C.zam", "D.zam", "Main.zam"}, orders[0])
	for _, order := range orders[1:] {
		assert.Equal(t, orders[0], order)
	}
}

// writeArchive lays down a zip holding a project of its own, which is what a
// downloaded dependency is, and answers the path to it.
func writeArchive(t *testing.T, dir, name string, files map[string]string) *base.Path {
	t.Helper()

	archivePath := filepath.Join(dir, name)
	f, err := os.Create(archivePath)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	for entryName, body := range files {
		entry, err := w.Create(entryName)
		require.NoError(t, err)
		_, err = io.WriteString(entry, body)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())

	return base.NewPath(archivePath, "")
}

// TestNewASTExpandsGlobImportOnDisk writes a `dir/*` import and checks every
// code file beside the glob target is reached. The resolution is beside the
// importing file, so the dir written in Main.zam is a directory beside it; a
// file one level deeper would be reached only by its own glob.
func TestNewASTExpandsGlobImportOnDisk(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)

	writeFile(t, dir, "sub/A.zam", namespaceSource("A"))
	writeFile(t, dir, "sub/B.zam", namespaceSource("B"))
	writeFile(t, dir, "sub/C.zam", namespaceSource("C"))
	writeFile(t, dir, "sub/deeper/D.zam", namespaceSource("D"))
	entry := writeFile(t, dir, "Main.zam", "import \"sub/*\";\n"+mainSource)

	ast, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 2},
		&base.FileProvider{}, &mock_GitProvider{})

	require.Empty(t, errs)
	assert.Equal(t,
		[]string{"Main.zam", "sub/A.zam", "sub/B.zam", "sub/C.zam"},
		codeFilePaths(t, ast, dir))
}

// A package import that globs reaches the files sitting at the top of the
// dependency, which is where the user would have written one file at a time
// without the glob. The resolution rule stays the one package imports already
// use — the path is read inside the declared dependency rather than beside the
// importing file.
func TestNewASTExpandsGlobImportFromStd(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	entry := writeFile(t, dir, "Main.zam", "import std(\"io/*\");\n"+mainSource)

	ast, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 2},
		&base.FileProvider{}, &mock_GitProvider{})

	require.Empty(t, errs)

	outers := map[string]bool{}
	for _, entry := range ast.CodeFiles.All() {
		outers[entry.Key.Outer] = true
	}
	for _, name := range []string{"std://io/File.zam"} {
		assert.True(t, outers[name], "expected %q in CodeFiles, got %v", name, outers)
	}
}

// A glob matching nothing is refused rather than treated as an empty import.
// The user asked for files that are not there — either the directory is
// wrong or the files are, and the next build is as likely to find them as
// this one — and catching it here keeps the mistake close to its source.
func TestNewASTRejectsAGlobImportThatMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "empty"), 0o750))
	entry := writeFile(t, dir, "Main.zam", "import \"empty/*\";\n"+mainSource)

	_, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 1},
		&base.FileProvider{}, &mock_GitProvider{})

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "matched no")
}

// Aliasing a glob is refused: an alias is one name in local scope and a glob
// resolves to many files, so there is no mapping the rest of the compiler
// would read. The duplicate-alias check elsewhere catches the related case of
// two imports under one name; this one catches the case of asking for it on
// purpose.
func TestNewASTRejectsAnAliasedGlobImport(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	writeFile(t, dir, "sub/A.zam", namespaceSource("A"))
	entry := writeFile(t, dir, "Main.zam", "import sub = \"sub/*\";\n"+mainSource)

	_, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 1},
		&base.FileProvider{}, &mock_GitProvider{})

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "glob")
}

// A package name resolves the path against the dependency the project declared
// under that name rather than against the importing file's own directory, which
// is the only way a file inside a downloaded archive can be named at all: the
// archive sits nowhere near the file importing from it.
func TestNewASTResolvesAPackageImportInsideItsDependency(t *testing.T) {
	dir := t.TempDir()
	archive := writeArchive(t, dir, "dep.zip", map[string]string{
		"zam.yaml": "name: \"dep\"\nversion: \"0.0.0\"\ndependencies:\n",
		"Dep.zam":  namespaceSource("Dep"),
	})

	writeFile(t, dir, "zam.yaml", "name: \"imports\"\nversion: \"0.0.0\"\n"+
		"dependencies:\n  dep: \"https://example.invalid/dep.zip\"\n")
	entry := writeFile(t, dir, "Main.zam", "import dep(\"Dep.zam\");\n"+mainSource)

	git := &mock_GitProvider{}
	git.EXPECT().Download("https://example.invalid/dep.zip").Return(archive, nil)
	git.EXPECT().ArchiveName(archive.Outer).Return("example.invalid/dep.zip")

	ast, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 2},
		&base.FileProvider{}, git)

	require.Empty(t, errs)

	var inners []string
	for _, codeFile := range ast.CodeFiles.All() {
		inners = append(inners, codeFile.Key.Inner)
	}
	assert.Contains(t, inners, "Dep.zam", "the import was read from inside the archive")

	// The archive is what a declaration is qualified by in a diagnostic, and the
	// project it was read into is where that name is kept. The project being
	// compiled was downloaded from nowhere and so is qualified by nothing.
	archives := map[string]string{}
	for _, project := range ast.Projects.All() {
		archives[project.Value.Name] = project.Value.Archive
	}
	assert.Equal(t, map[string]string{
		"imports": "",
		"dep":     "example.invalid/dep.zip",
	}, archives)
}

// A package the project file does not declare is an error rather than a path
// resolved beside the importing file. The two name different files, and only the
// project file says which was meant; a relative import wanting a name of its own
// writes the alias form.
func TestNewASTRejectsAPackageImportWithNoSuchDependency(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	writeFile(t, dir, "Dep.zam", namespaceSource("Dep"))
	entry := writeFile(t, dir, "Main.zam", "import dep(\"Dep.zam\");\n"+mainSource)

	_, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 2},
		&base.FileProvider{}, &mock_GitProvider{})

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), `no dependency "dep"`)
}

// A syntax error is located at the token the parse stopped at. The line and
// column come from the offsets the lexer records as it passes newlines, and a
// newline ends the line it sits on rather than starting the next one — counting
// it as the start put every column on every line but the first one out by one.
func TestNewASTLocatesASyntaxError(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		expected string
	}{{
		name:     "the first token of the file",
		source:   "fun\n",
		expected: "[1:1]",
	}, {
		name:     "a token on a later line",
		source:   "namespace P {\n    static var int a = 1;\n    static var fun b = 2;\n}\n",
		expected: "[3:16]",
	}, {
		name:     "a token past a block comment spanning lines",
		source:   "namespace P {\n/* one\n two\n three */\n    static var fun b = 2;\n}\n",
		expected: "[5:16]",
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir)
			entry := writeFile(t, dir, "Main.zam", test.source)

			_, errs := NewAST(
				&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 1},
				&base.FileProvider{}, &mock_GitProvider{})

			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), test.expected)
		})
	}
}

// parseEntry runs a one-file project through the parser and answers the errors
// it collected, without failing the test on them: this is the shape a blank-
// identifier test wants, since a parse that was supposed to fail is what the
// test is about.
func parseEntry(t *testing.T, source string) []error {
	t.Helper()

	dir := t.TempDir()
	writeManifest(t, dir)
	entry := writeFile(t, dir, "Main.zam", source)

	_, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 1},
		&base.FileProvider{}, &mock_GitProvider{})
	return errs
}

// TestNewASTAcceptsBlankIdentifierBindings pins the three places the language
// does take a `_`: a `var` statement's name (also the for-loop initializer,
// which is one of these), a method parameter, and a lambda parameter. The
// slots are reserved and the parse finishes.
func TestNewASTAcceptsBlankIdentifierBindings(t *testing.T) {
	for name, source := range map[string]string{
		"var statement": `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int _ = 42;
        return;
    }
}
`,
		"for initializer": `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        for (var int _ = 0; false;) {
            return;
        }
        return;
    }
}
`,
		"method parameters": `
namespace Main {
    static fun int(int, int) f = (_, _) {
        return 0;
    }
    static fun void(int, str...) main = (argc, argv) {
        return;
    }
}
`,
		"lambda parameters": `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int(int) g = lambda int(int):(_) {
            return 0;
        };
        return;
    }
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, parseEntry(t, source))
		})
	}
}

// TestNewASTRejectsBlankIdentifierAsName is the other half: everywhere else a
// name is written, `_` arrives as [TTUnderscore] and the parser answers with
// "unexpected token" because none of these productions accept it. The exact
// wording is not pinned — the parser's own "expected one of" message carries
// the position, which is what matters when a diagnostic lands in a file.
func TestNewASTRejectsBlankIdentifierAsName(t *testing.T) {
	// Each entry writes `_` where the production normally takes a name, so
	// the one difference between the passing form and the failing form is
	// the `_` itself.
	cases := map[string]string{
		"class name": `
namespace Main {
    class _ { }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
		"struct name": `
namespace Main {
    struct _ { }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
		"enum name": `
namespace Main {
    enum _ { A, B }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
		"interface name": `
namespace Main {
    interface _ { }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
		"namespace name": `
namespace _ {
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
		"field name": `
namespace Main {
    class Foo {
        var int _ = 1;
    }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
		"method name": `
namespace Main {
    static fun int() _ = () { return 0; }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
		"label name": `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        label _: for (false;) { break _; }
        return;
    }
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			errs := parseEntry(t, source)
			require.NotEmpty(t, errs, "expected a parse error for %s = _", name)
		})
	}
}

// Where a node was written outlives the lexer that read it, which is what any
// pass after parsing needs to say where the source it is complaining about is.
func TestNewASTKeepsLineStartsForLaterPasses(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	entry := writeFile(t, dir, "Main.zam", "\n\nnamespace Main {\n"+
		"    static fun void(int, str...) main = (argc, argv) {\n        return;\n    }\n}\n")

	ast, errs := NewAST(
		&command.Command{InputPath: base.NewPath(entry, ""), MaxThreads: 1},
		&base.FileProvider{}, &mock_GitProvider{})
	require.Empty(t, errs)

	file, index := ast.CodeFiles.Get(*base.NewPath(entry, ""))
	require.NotEqual(t, -1, index)
	assert.Equal(t, "[3:1]", file.Location(ast.MainCodeDecl.Start()).Position.String(),
		"the declaration begins on the third line, the first two being blank")
}
