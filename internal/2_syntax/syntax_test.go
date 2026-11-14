package syntax

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	writeFile(t, dir, "zam.yml", "name: \"imports\"\nversion: \"0.0.0\"\ndependencies:\n")
}

// namespaceSource is a file holding nothing but a namespace, so that importing
// it is all the test is asking about.
func namespaceSource(name string) string {
	return fmt.Sprintf("namespace %s {\n    static var int v = 1;\n}\n", name)
}

// writeProject lays down a project whose entry file imports one file per name,
// and answers the entry file's path.
func writeProject(t *testing.T, names ...string) base.Path {
	t.Helper()

	dir := t.TempDir()
	writeManifest(t, dir)

	var imports strings.Builder
	for _, name := range names {
		writeFile(t, dir, name+base.CodeFileExtension, namespaceSource(name))
		fmt.Fprintf(&imports, "import %s = %q;\n",
			strings.ToLower(name), name+base.CodeFileExtension)
	}

	return base.Path(writeFile(t, dir, "Main"+base.CodeFileExtension,
		imports.String()+mainSource))

}

// codeFilePaths answers where every file in the tree was read from, relative to
// the directory the entry file is in.
func codeFilePaths(t *testing.T, ast *AST, dir string) []string {
	t.Helper()

	var paths []string
	for _, entry := range ast.CodeFiles.All() {
		relative, err := filepath.Rel(dir, string(entry.Key))
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
		&base.FileProvider{}, &mock_PackageProvider{})

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
		assert.True(t, base.IsStdPath(stdDep),
			"project %q has std dependency %q, not a std path", entry.Value.Name, stdDep.String())
	}
}

// Skipping project files puts every local file in an implicit project that
// depends only on std, whether or not a project file sits above it.
func TestNewASTSkipsProjectFilesWhenAsked(t *testing.T) {
	tests := []struct {
		name     string
		manifest bool
	}{
		{name: "without a project file"},
		{name: "under a project file", manifest: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.manifest {
				writeManifest(t, dir)
			}
			writeFile(t, dir, "sub/Leaf.zam", namespaceSource("Leaf"))
			entry := writeFile(t, dir, "Main.zam", "import leaf = \"sub/Leaf.zam\";\n"+mainSource)

			ast, errs := NewAST(
				&command.Command{InputPath: base.Path(entry), MaxThreads: 2, NoProject: true},
				&base.FileProvider{}, &mock_PackageProvider{})

			require.Empty(t, errs)
			assert.Equal(t, []string{"Main.zam", "sub/Leaf.zam"}, codeFilePaths(t, ast, dir))
			require.Equal(t, 1, ast.Projects.Len())
			project := ast.MainCodeDecl.File().Project
			assert.Equal(t, "Main", project.Name)
			assert.Equal(t, map[string]base.Path{base.StdPackage: base.StdPath("")}, project.Dependencies)
			assert.Empty(t, project.Native.Files)
			for _, entry := range ast.CodeFiles.All() {
				assert.Same(t, project, entry.Value.Project, entry.Key.String())
			}
		})
	}
}

// Without the flag, an entry file with no project file above it is an error.
func TestNewASTRejectsAnEntryWithoutAProjectFile(t *testing.T) {
	entry := writeFile(t, t.TempDir(), "Main.zam", mainSource)

	_, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})

	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "project file missing")
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
		&command.Command{InputPath: base.Path(entry), MaxThreads: 2},
		&base.FileProvider{}, &mock_PackageProvider{})

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
				ast, errs := NewAST(cmd, &base.FileProvider{}, &mock_PackageProvider{})
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

func (fp *countingFileProvider) Reader(path base.Path) (io.ReadCloser, error) {
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
				fileProvider, &mock_PackageProvider{})

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
		ast, errs := NewAST(cmd, &base.FileProvider{}, &mock_PackageProvider{})
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

// writeDependency lays down a project of its own in a directory, which is what
// a fetched dependency is extracted to, and answers the path to it.
func writeDependency(t *testing.T, files map[string]string) base.Path {
	t.Helper()

	dir := t.TempDir()
	for name, body := range files {
		writeFile(t, dir, name, body)
	}
	return base.Path(dir)
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
		&command.Command{InputPath: base.Path(entry), MaxThreads: 2},
		&base.FileProvider{}, &mock_PackageProvider{})

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
	entry := writeFile(t, dir, "Main.zam", "import std(\"io/local/*\");\n"+mainSource)

	ast, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 2},
		&base.FileProvider{}, &mock_PackageProvider{})

	require.Empty(t, errs)

	outers := map[string]bool{}
	for _, entry := range ast.CodeFiles.All() {
		outers[string(entry.Key)] = true
	}
	for _, name := range []string{"std://io/local/File.zam"} {
		assert.True(t, outers[name], "expected %q in CodeFiles, got %v", name, outers)
	}
}

// A bare * globs the directory the importing file sits in, as ./* does, the
// importing file among them.
func TestNewASTGlobsTheImportingDirectoryForABareStar(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	other := writeFile(t, dir, "Other.zam", "namespace Other {}\n")
	entry := writeFile(t, dir, "Main.zam", "import \"*\";\n"+mainSource)

	ast, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})

	require.Empty(t, errs)
	outers := map[string]bool{}
	for _, entry := range ast.CodeFiles.All() {
		outers[string(entry.Key)] = true
	}
	assert.True(t, outers[other], "expected %q in CodeFiles, got %v", other, outers)
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
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})

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
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "glob")
}

// A package name resolves the path against the dependency the project declared
// under that name rather than against the importing file's own directory, which
// is the only way a file of a fetched dependency can be named at all: it sits
// nowhere near the file importing from it.
func TestNewASTResolvesAPackageImportInsideItsDependency(t *testing.T) {
	dir := t.TempDir()
	dependency := writeDependency(t, map[string]string{
		"zam.yml": "name: \"dep\"\nversion: \"0.0.0\"\ndependencies:\n",
		"Dep.zam": namespaceSource("Dep"),
	})

	writeFile(t, dir, "zam.yml", "name: \"imports\"\nversion: \"0.0.0\"\n"+
		"dependencies:\n  dep:\n    repo: example.com/dep@v1\n    hash: h1:abc\n")
	entry := writeFile(t, dir, "Main.zam", "import dep(\"Dep.zam\");\n"+mainSource)

	packages := &mock_PackageProvider{}
	packages.EXPECT().Fetch("example.com/dep@v1").Return(dependency, "h1:abc", nil)

	ast, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 2, WriteProject: true},
		&base.FileProvider{}, packages)

	require.Empty(t, errs)

	var paths []string
	for _, codeFile := range ast.CodeFiles.All() {
		paths = append(paths, string(codeFile.Key))
	}
	assert.Contains(t, paths, filepath.Join(string(dependency), "Dep.zam"), "the import was read from the dependency")

	// The repo@version is what a declaration is qualified by in a diagnostic,
	// and the project it was read into is where that name is kept. The project
	// being compiled was fetched from nowhere and so is qualified by nothing.
	archives := map[string]string{}
	for _, project := range ast.Projects.All() {
		archives[project.Value.Name] = project.Value.Archive
	}
	assert.Equal(t, map[string]string{
		"imports": "",
		"dep":     "example.com/dep@v1",
	}, archives)
}

// A dependency whose files do not hash to what the project file pinned is
// refused, whether its tag moved or its copy was changed.
func TestNewASTRejectsADependencyNotMatchingItsHash(t *testing.T) {
	dir := t.TempDir()
	dependency := writeDependency(t, map[string]string{
		"zam.yml": "name: \"dep\"\n",
		"Dep.zam": namespaceSource("Dep"),
	})
	writeFile(t, dir, "zam.yml", "name: \"imports\"\n"+
		"dependencies:\n  dep:\n    repo: example.com/dep@v1\n    hash: h1:pinned\n")
	entry := writeFile(t, dir, "Main.zam", "import dep(\"Dep.zam\");\n"+mainSource)

	packages := &mock_PackageProvider{}
	packages.EXPECT().Fetch("example.com/dep@v1").Return(dependency, "h1:fetched", nil)

	_, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1, WriteProject: true},
		&base.FileProvider{}, packages)

	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], `dependency "dep" does not match its hash`)
}

// A dependency written as a bare string, as an archive URL once was, is refused.
func TestNewASTRejectsADependencyWithoutARepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "zam.yml", "name: \"imports\"\ndependencies:\n  dep: \"https://example.com/dep.zip\"\n")
	entry := writeFile(t, dir, "Main.zam", mainSource)

	_, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})

	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], "cannot unmarshal")
}

// A hash the project file lacks is recorded in it by the compiler, beside the
// repo it was fetched by, and the rest of the file is kept. A tool that only
// reads the project leaves it as it was.
func TestNewASTRecordsAMissingHash(t *testing.T) {
	const project = "# the project\nname: \"imports\"\ndependencies:\n  dep:\n    repo: example.com/dep@v1 # pinned by tag\n"

	for name, test := range map[string]struct {
		write    bool
		expected string
	}{
		"by the compiler": {true, "# the project\nname: \"imports\"\ndependencies:\n  dep:\n    repo: example.com/dep@v1 # pinned by tag\n    hash: h1:fetched\n"},
		"by a reader":     {false, project},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dependency := writeDependency(t, map[string]string{"zam.yml": "name: \"dep\"\n"})
			projectFile := writeFile(t, dir, "zam.yml", project)
			entry := writeFile(t, dir, "Main.zam", mainSource)

			packages := &mock_PackageProvider{}
			packages.EXPECT().Fetch("example.com/dep@v1").Return(dependency, "h1:fetched", nil)

			_, errs := NewAST(
				&command.Command{InputPath: base.Path(entry), MaxThreads: 1, WriteProject: test.write},
				&base.FileProvider{}, packages)
			require.Empty(t, errs)

			got, err := os.ReadFile(projectFile)
			require.NoError(t, err)
			assert.Equal(t, test.expected, string(got))
		})
	}
}

// A fetched dependency is only read, so a hash its own project file lacks is
// trusted rather than recorded, and its files may not import from outside the
// directory it was extracted to.
func TestNewASTKeepsAFetchedDependencyToItself(t *testing.T) {
	dir := t.TempDir()
	const dependencyProject = "name: \"dep\"\ndependencies:\n  inner:\n    repo: example.com/inner@v1\n"
	inner := writeDependency(t, map[string]string{"zam.yml": "name: \"inner\"\n"})
	dependency := writeDependency(t, map[string]string{
		"zam.yml": dependencyProject,
		"Dep.zam": "import \"../Outside.zam\";\n" + namespaceSource("Dep"),
	})
	writeFile(t, filepath.Dir(string(dependency)), "Outside.zam", namespaceSource("Outside"))
	writeFile(t, dir, "zam.yml", "name: \"imports\"\n"+
		"dependencies:\n  dep:\n    repo: example.com/dep@v1\n    hash: h1:dep\n")
	entry := writeFile(t, dir, "Main.zam", "import dep(\"Dep.zam\");\n"+mainSource)

	packages := &mock_PackageProvider{}
	packages.EXPECT().Fetch("example.com/dep@v1").Return(dependency, "h1:dep", nil)
	packages.EXPECT().Fetch("example.com/inner@v1").Return(inner, "h1:inner", nil)

	_, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1, WriteProject: true},
		&base.FileProvider{}, packages)

	require.Len(t, errs, 1)
	assert.ErrorContains(t, errs[0], `import "../Outside.zam" leaves example.com/dep@v1`)

	got, err := os.ReadFile(filepath.Join(string(dependency), "zam.yml"))
	require.NoError(t, err)
	assert.Equal(t, dependencyProject, string(got))
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
		&command.Command{InputPath: base.Path(entry), MaxThreads: 2},
		&base.FileProvider{}, &mock_PackageProvider{})

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
				&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
				&base.FileProvider{}, &mock_PackageProvider{})

			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), test.expected)
		})
	}
}

// TestNewASTParsesPrimitiveEnum pins the shape of an enum backed by a primitive:
// the primitive follows a colon, and every value is written with what it is
// backed by.
func TestNewASTParsesPrimitiveEnum(t *testing.T) {
	for _, primitive := range []string{"bool", "char", "byte", "int", "uint", "flt", "str"} {
		t.Run(primitive, func(t *testing.T) {
			errs := parseEntry(t, `
namespace Main {
    enum Flag : `+primitive+` {
        A(x),
        B(y);
    }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`)
			assert.Empty(t, errs)
		})
	}
}

// TestNewASTRejectsMalformedPrimitiveEnum holds an enum's values to the form its
// declaration asks for: a value of a primitive-backed enum names no initializer
// and must be written with a value, a value of any other enum cannot be, and
// only a primitive may back an enum.
func TestNewASTRejectsMalformedPrimitiveEnum(t *testing.T) {
	for name, test := range map[string]struct {
		source   string
		expected string
	}{
		"initializer in a primitive enum": {
			source: `
namespace Main {
    enum Flag : int {
        A.init(1);
        private fun void(int) init = (x) {}
    }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
			expected: "enum value Flag::A must be written with a int value",
		},
		"bare value in a primitive enum": {
			source: `
namespace Main {
    enum Flag : int { A; }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
			expected: "enum value Flag::A must be written with a int value",
		},
		"value in an enum without a primitive": {
			source: `
namespace Main {
    enum Flag { A(1); }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
			expected: "enum value Flag::A cannot be written with a value",
		},
		"backed by a declaration": {
			source: `
namespace Main {
    enum Flag : Main { A(1); }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
			expected: "enum Flag cannot be backed by",
		},
		"backed by any": {
			source: `
namespace Main {
    enum Flag : any { A(1); }
    static fun void(int, str...) main = (argc, argv) { return; }
}
`,
			expected: "enum Flag cannot be backed by",
		},
	} {
		t.Run(name, func(t *testing.T) {
			errs := parseEntry(t, test.source)
			require.NotEmpty(t, errs)
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
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})
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
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})
	require.Empty(t, errs)

	file, index := ast.CodeFiles.Get(base.Path(entry))
	require.NotEqual(t, -1, index)
	assert.Equal(t, "[3:1]", file.Location(ast.MainCodeDecl.Start()).Position.String(),
		"the declaration begins on the third line, the first two being blank")
}

// TestNewASTRecoversFromSyntaxErrors pins where a parse picks up again after an
// error: past the next `;`, or at the `}` closing the braces the failed element
// began in. Each case's errors are listed by the line they are found on, which
// is what says both that the parse went on and where it went on from.
func TestNewASTRecoversFromSyntaxErrors(t *testing.T) {
	for name, test := range map[string]struct {
		source string
		lines  []int
	}{
		"statements in a block": {
			source: `namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int = 1;
        var int y = 2;
        return return;
    }
}
`,
			lines: []int{3, 5},
		},
		"fields in a declaration": {
			source: `namespace Main {
    static var int = 1;
    static var int y = 2;
    static fun int() = () { return 1; }
}
`,
			lines: []int{2, 4},
		},
		"a statement missing its semicolon before the block closes": {
			source: `namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int x = 1
    }
    static var int = 2;
}
`,
			lines: []int{4, 5},
		},
		"braces opened while skipping are skipped whole": {
			source: `namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int = lambda int():() { return 1; };
        var int = 3;
    }
}
`,
			lines: []int{3, 3, 4},
		},
		"a cast is part of the expression being skipped": {
			source: `namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int = a{int} * b;
        var int = a{int} - b;
        var int = @{int} == @{int};
        var int = 1;
    }
}
`,
			lines: []int{3, 4, 5, 6},
		},
		"a block holding only blocks": {
			source: `namespace Main {
    static fun void(int) g = (a b) { if (a) {} }
    static fun void() h = () {}
    static var int = 1;
}
`,
			lines: []int{2, 4},
		},
		"an empty block": {
			source: `namespace Main {
    static fun void(int) g = (a b) {}
    static var int = 1;
}
`,
			lines: []int{2, 3},
		},
		"a method whose header fails": {
			source: `namespace Main {
    static fun int(int) h = (a b) { return a; }
    static var int = 1;
    static fun int() g = () { return 1 }
    static var int = 2;
}
`,
			lines: []int{2, 3, 4, 5},
		},
		"a nested declaration": {
			source: `namespace Main {
    class C {
        var int = 1;
    }
    static var int = 2;
}
`,
			lines: []int{3, 5},
		},
		"an enum value list": {
			source: `namespace Main {
    enum E { A, 1; }
    static var int = 2;
}
`,
			lines: []int{2, 3},
		},
		"an enum value list missing its semicolon": {
			source: `namespace Main {
    enum E { A, B }
    static var int = 2;
}
`,
			lines: []int{2, 3},
		},
		"an import": {
			source: `import ;

namespace Main {
    static var int = 2;
}
`,
			lines: []int{1, 4},
		},
		"the end of the file": {
			source: `namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int = 1;
`,
			lines: []int{3, 4},
		},
		"a lexing error after a syntax error": {
			source: `namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int = 1;
        var str s = "open
`,
			lines: []int{3, 4},
		},
	} {
		t.Run(name, func(t *testing.T) {
			errs := parseEntry(t, test.source)
			var lines []int
			for _, err := range errs {
				var located *base.Error
				require.ErrorAs(t, err, &located)
				require.NotNil(t, located.Location.Position, "%v", err)
				lines = append(lines, located.Location.Position.Line)
			}
			slices.Sort(lines)
			assert.Equal(t, test.lines, lines, "%v", errs)
		})
	}
}

// An error the parse recovered from leaves the AST in place beside it, with the
// file marked as missing what failed to parse; one the parse gave up on leaves
// no AST at all.
func TestNewASTHandsBackARecoveredTree(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)

	recovered := writeFile(t, dir, "Main.zam", "namespace Main {\n    static var int = 1;\n    static var int y = 2;\n}\n")
	ast, errs := NewAST(&command.Command{InputPath: base.Path(recovered), MaxThreads: 1}, &base.FileProvider{}, &mock_PackageProvider{})
	require.Len(t, errs, 1)
	require.NotNil(t, ast)
	file, _ := ast.CodeFiles.Get(base.Path(recovered))
	require.NotNil(t, file)
	assert.True(t, file.HasSyntaxErrors)
	_, ok := ast.MainCodeDecl.GetStaticField("y")
	assert.True(t, ok, "what parsed after the error is kept")

	abandoned := writeFile(t, dir, "Broken.zam", "namespace Broken {\n")
	ast, errs = NewAST(&command.Command{InputPath: base.Path(abandoned), MaxThreads: 1}, &base.FileProvider{}, &mock_PackageProvider{})
	assert.NotEmpty(t, errs)
	assert.Nil(t, ast)
}

// TestNewASTKeepsWhatAFailedElementDeclared pins what a recovered parse keeps
// of an element that failed once its name had been read: the name, with what
// failed after it left off, so that the name still resolves. A block that lost
// a statement says so.
func TestNewASTKeepsWhatAFailedElementDeclared(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	entry := writeFile(t, dir, "Main.zam", `import "Missing/*";

namespace Main {
    static var int count = ;
    static fun int(int) h = (a b) { return a; }
    enum E : int { A(), B(2); }
    static fun int() f = () {
        var int x = ;
        return x;
    }
}
`)
	ast, errs := NewAST(&command.Command{InputPath: base.Path(entry), MaxThreads: 1}, &base.FileProvider{}, &mock_PackageProvider{})
	require.Len(t, errs, 5, "%v", errs)
	require.NotNil(t, ast)

	file, _ := ast.CodeFiles.Get(base.Path(entry))
	assert.True(t, file.BrokenImports)

	decl := ast.MainCodeDecl
	count, ok := decl.GetStaticField("count")
	require.True(t, ok)
	assert.Nil(t, count.(*FieldMember).Expr)

	h, ok := decl.GetStaticMethod("h")
	require.True(t, ok)
	assert.True(t, h.Body.(*StmtBlock).Broken)

	enum, ok := decl.GetDecl("E")
	require.True(t, ok)
	a, ok := enum.GetStaticField("A")
	require.True(t, ok)
	assert.Nil(t, a.(*FieldEnum).Value)

	f, ok := decl.GetStaticMethod("f")
	require.True(t, ok)
	body := f.Body.(*StmtBlock)
	assert.True(t, body.Broken)
	require.Len(t, body.Stmts, 2)
	assert.Equal(t, "x", body.Stmts[0].(*StmtVariable).Name())
	assert.Nil(t, body.Stmts[0].(*StmtVariable).Expr)
}

// TestNewASTPlacesStrFirstInEnumsAndStructs pins where an enum's or struct's Str
// is found: first among its instance methods, whether written there, written
// later, or given by default to a declaration that wrote none. A Str of any
// other kind than an instance str() is reported.
func TestNewASTPlacesStrFirstInEnumsAndStructs(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	entry := writeFile(t, dir, "Main.zam", `namespace Main {
    struct Plain {
        var int x;
        fun int() Get = () { return this.x; }
    }
    struct Written {
        fun int() Get = () { return 1; }
        fun str() Str = () { return "written"; }
    }
    enum Bare { A, B; }
    static fun void(uint, str...) main = (_, _) { return; }
}
`)
	ast, errs := NewAST(&command.Command{InputPath: base.Path(entry), MaxThreads: 1}, &base.FileProvider{}, &mock_PackageProvider{})
	require.Empty(t, errs)

	for _, name := range []string{"Plain", "Written", "Bare"} {
		t.Run(name, func(t *testing.T) {
			decl, ok := ast.MainCodeDecl.GetDecl(name)
			require.True(t, ok)
			str, ok := decl.GetInstanceMethod(StrMethodName)
			require.True(t, ok)
			assert.Equal(t, 0, *str.Index())
			assert.NotNil(t, str.Body)
		})
	}

	for name, source := range map[string]string{
		"of another type": `namespace Main { struct S { fun int() Str = () { return 1; } } }`,
		"static":          `namespace Main { enum E { A; static fun str() Str = () { return "e"; } } }`,
		"a member":        `namespace Main { struct S { var str Str; } }`,
	} {
		t.Run("a Str "+name, func(t *testing.T) {
			errs := parseEntry(t, source)
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), "must be an instance method of type str()")
		})
	}
}

// TestNewASTTakesOnlyPublicOnATopLevelDecl pins that a file's declaration may
// be marked public, widening it past its package, and nothing else.
func TestNewASTTakesOnlyPublicOnATopLevelDecl(t *testing.T) {
	body := ` namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        return;
    }
}
`
	assert.Empty(t, parseEntry(t, "public"+body))
	for _, modifier := range []string{"private", "protected", "static", "virtual"} {
		t.Run(modifier, func(t *testing.T) {
			errs := parseEntry(t, modifier+body)
			require.NotEmpty(t, errs)
			assert.Contains(t, errs[0].Error(), "a top-level declaration can only be public")
		})
	}
}

// TestNewASTReadsStaticPaths pins what may follow an identifier: a dotted name
// and :: lead to a static, with type arguments between them for a generic
// method, and a dotted name with no :: is a variable's fields. Anything else
// before or after :: is a parse error.
func TestNewASTReadsStaticPaths(t *testing.T) {
	source := func(body string) string {
		return `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        ` + body + `
        return;
    }
}
`
	}
	for name, body := range map[string]string{
		"static":         `var int x = Main::y;`,
		"nested path":    `var int x = A.B::y;`,
		"type arguments": `Arrs::[int, (int, str)]Copy(1U, null, null);`,
		"then selection": `File::StdOut.WriteStr("x");`,
		"then index":     `var int x = Main::table[3];`,
		"then call":      `Main::f(1)(2);`,
		"fields":         `var int x = a.b.c;`,
		"type value":     `native(N){void(type)}(type(A.B));`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, parseEntry(t, source(body)))
		})
	}
	for name, body := range map[string]string{
		"after a call":       `var int x = f()::m;`,
		"after an index":     `var int x = a[0]::m;`,
		"after this":         `var int x = this::m;`,
		"twice":              `var int x = A::b::c;`,
		"no member":          `var int x = A::;`,
		"no member after []": `var int x = A::[int];`,
		"type selector":      `var int x = type(A).b;`,
		"type type args":     `var int x = type(A)[int].b;`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.NotEmpty(t, parseEntry(t, source(body)))
		})
	}
}

// TestNewASTParsesSwitches pins the forms a switch is written in.
func TestNewASTParsesSwitches(t *testing.T) {
	for name, stmt := range map[string]string{
		"values":       `switch (x) { case (1, 2): return; case (3): {} } else {}`,
		"tuple values": `switch (x) { case ((1, 1), (2, 2)): return; }`,
		"empty":        `switch (x) {}`,
		"labelled":     `label s: switch (x) { case (1): break s; }`,
		"if body":      `switch (x) { case (1): if (true) return; } else {}`,
		"if-else body": `switch (x) { case (1): if (true) return; else return; } else {}`,
		"switch body":  `if (true) switch (x) { case (1): {} } else {}`,
		"for body":     `switch (x) { case (1): for (;;) {} } else {}`,
	} {
		t.Run(name, func(t *testing.T) {
			errs := parseEntry(t, `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int x = 1;
        `+stmt+`
    }
}
`)
			assert.Empty(t, errs)
		})
	}
}

// TestNewASTSplitsCaseValuesOnCommas pins that the parentheses after case hold
// one value per comma, so a tuple value is written in parentheses of its own.
func TestNewASTSplitsCaseValuesOnCommas(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir)
	entry := writeFile(t, dir, "Main.zam", `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        switch (argc) { case (1, 2): {} case ((1, 2)): {} }
    }
}
`)
	ast, errs := NewAST(
		&command.Command{InputPath: base.Path(entry), MaxThreads: 1},
		&base.FileProvider{}, &mock_PackageProvider{})
	require.Empty(t, errs)

	main, ok := ast.MainCodeDecl.GetStaticMethod("main")
	require.True(t, ok)
	cases := main.Body.(*StmtBlock).Stmts[0].(*StmtSwitch).Cases
	require.Len(t, cases, 2)
	assert.Len(t, cases[0].Values, 2)
	require.Len(t, cases[1].Values, 1)
	assert.Equal(t, ExprKindTuple, cases[1].Values[0].Kind())
}

// TestNewASTRejectsMalformedSwitches holds a switch to its form.
func TestNewASTRejectsMalformedSwitches(t *testing.T) {
	for name, test := range map[string]struct {
		stmt     string
		expected string
	}{
		"case without parentheses": {
			stmt:     `switch (x) { case 1: {} }`,
			expected: `unexpected token: "INT_LITERAL", expected "("`,
		},
		"else inside the braces": {
			stmt:     `switch (x) { case (1): {} else {} }`,
			expected: `unexpected token: "else", expected one of "case", or "}"`,
		},
		"label on another statement": {
			stmt:     `label s: if (true) {}`,
			expected: `unexpected token: "if", expected one of "for", or "switch"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			errs := parseEntry(t, `
namespace Main {
    static fun void(int, str...) main = (argc, argv) {
        var int x = 1;
        `+test.stmt+`
    }
}
`)
			assert.Contains(t, fmt.Sprint(errs), test.expected)
		})
	}
}

// Get adds a dependency under the last element of its repository with the hash
// of what it fetched, starting the project file's dependencies when it has
// none, and keeps what the file already said.
func TestGetAddsADependency(t *testing.T) {
	dir := t.TempDir()
	projectFile := writeFile(t, dir, "zam.yml", "# the project\nname: \"imports\"\ndependencies:\n")
	dependency := writeDependency(t, map[string]string{"zam.yml": "name: \"list\"\n"})

	packages := &mock_PackageProvider{}
	packages.EXPECT().Fetch("example.com/x/list@v1").Return(dependency, "h1:list", nil)

	require.NoError(t, Get(base.Path(projectFile), []string{"example.com/x/list@v1"}, "", &base.FileProvider{}, packages))

	got, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	assert.Equal(t, "# the project\nname: \"imports\"\ndependencies:\n  list:\n    repo: example.com/x/list@v1\n    hash: h1:list\n", string(got))
}

// Get replaces the version and hash of a dependency already of that name.
func TestGetUpdatesADependency(t *testing.T) {
	dir := t.TempDir()
	projectFile := writeFile(t, dir, "zam.yml", "name: \"imports\"\ndependencies:\n  list:\n    repo: example.com/x/list@v1 # the list\n    hash: h1:v1\n")
	dependency := writeDependency(t, map[string]string{"zam.yml": "name: \"list\"\n"})

	packages := &mock_PackageProvider{}
	packages.EXPECT().Fetch("example.com/x/list@v2").Return(dependency, "h1:v2", nil)

	require.NoError(t, Get(base.Path(projectFile), []string{"example.com/x/list@v2"}, "list", &base.FileProvider{}, packages))

	got, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	assert.Equal(t, "name: \"imports\"\ndependencies:\n  list:\n    repo: example.com/x/list@v2 # the list\n    hash: h1:v2\n", string(got))
}

// Get with nothing to add fetches every dependency the project reaches,
// through those its dependencies have, and records the hashes its own project
// file lacks.
func TestGetFetchesEveryDependency(t *testing.T) {
	dir := t.TempDir()
	projectFile := writeFile(t, dir, "zam.yml", "name: \"imports\"\ndependencies:\n  a:\n    repo: example.com/a@v1\n")
	b := writeDependency(t, map[string]string{"zam.yml": "name: \"b\"\n"})
	a := writeDependency(t, map[string]string{"zam.yml": "name: \"a\"\ndependencies:\n  b:\n    repo: example.com/b@v1\n"})

	packages := &mock_PackageProvider{}
	packages.EXPECT().Fetch("example.com/a@v1").Return(a, "h1:a", nil).Once()
	packages.EXPECT().Fetch("example.com/b@v1").Return(b, "h1:b", nil).Once()

	require.NoError(t, Get(base.Path(projectFile), nil, "", &base.FileProvider{}, packages))
	packages.AssertExpectations(t)

	got, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	assert.Equal(t, "name: \"imports\"\ndependencies:\n  a:\n    repo: example.com/a@v1\n    hash: h1:a\n", string(got))
}

// A repository whose last element no import could be written with has to be
// named, and the project file is left alone until it is.
func TestGetRefusesAnUnwritablePackageName(t *testing.T) {
	const project = "name: \"imports\"\n"
	dir := t.TempDir()
	projectFile := writeFile(t, dir, "zam.yml", project)

	for ref, want := range map[string]string{
		"example.com/x/zam-list@v1": `"zam-list" is not a valid package name: name it with --as`,
		"example.com/x/std@v1":      `"std" is a reserved package name`,
		"example.com/x/class@v1":    `"class" is not a valid package name`,
	} {
		err := Get(base.Path(projectFile), []string{ref}, "", &base.FileProvider{}, &mock_PackageProvider{})
		assert.ErrorContains(t, err, want, ref)
	}

	got, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	assert.Equal(t, project, string(got))
}
