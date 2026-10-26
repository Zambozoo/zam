package lsp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	protocol "github.com/tliron/glsp/protocol_3_16"
	base "github.com/zambozoo/zam/internal/0_base"
)

// project writes a project holding the given files and returns the path of each.
func project(t *testing.T, files map[string]string) map[string]*base.Path {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"lsp\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	paths := map[string]*base.Path{}
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
		path, err := base.ParsePath(filepath.Join(dir, name))
		require.NoError(t, err)
		paths[name] = path
	}
	return paths
}

// check returns the diagnostics for checking an entry.
func check(files *overlay, entry *base.Path) []protocol.Diagnostic {
	diagnostics, _ := analyze(files, nil, entry, 1)
	return diagnostics
}

func rangeOf(startLine, startChar, endLine, endChar protocol.UInteger) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: startLine, Character: startChar},
		End:   protocol.Position{Line: endLine, Character: endChar},
	}
}

func TestAnalyzeReportsNothingForAValidFile(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": `
namespace Main {
    static fun int() f = () { return 1; }
}
`})

	assert.Empty(t, check(newOverlay(), paths["Main.zam"]))
}

// A file checked on its own has no entry to answer for, so having no main
// method is not an error there; it is only one when the file is built.
func TestAnalyzeDoesNotRequireAMainMethod(t *testing.T) {
	paths := project(t, map[string]string{"Lib.zam": `
namespace Lib {
    static fun void() f = () { return; }
}
`})

	assert.Empty(t, check(newOverlay(), paths["Lib.zam"]))
}

// A semantic error is reported over the token it was found at, whose length is
// found by lexing the file again.
func TestAnalyzeReportsASemanticErrorOverItsToken(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": "namespace Main {\n" +
		"    static fun void() f = () {\n" +
		"        var int x = \"s\";\n" +
		"        return;\n" +
		"    }\n" +
		"}\n"})

	diagnostics := check(newOverlay(), paths["Main.zam"])
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "variable expression is not a subtype of int", diagnostics[0].Message)
	assert.Equal(t, rangeOf(2, 8, 2, 11), diagnostics[0].Range)
	assert.Equal(t, protocol.DiagnosticSeverityError, *diagnostics[0].Severity)
}

// A syntax error is reported over the token the parser could not take.
func TestAnalyzeReportsASyntaxErrorOverItsToken(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": "namespace Main {\n" +
		"    static fun void() f = () {\n" +
		"        return return;\n" +
		"    }\n" +
		"}\n"})

	diagnostics := check(newOverlay(), paths["Main.zam"])
	require.Len(t, diagnostics, 1)
	assert.Equal(t, rangeOf(2, 15, 2, 21), diagnostics[0].Range)
}

// What is checked is what is being edited: an open buffer is read in place of
// the file on disk, and closing it reads the disk again.
func TestAnalyzeReadsOpenBuffersOverTheDisk(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": `
namespace Main {
    static fun int() f = () { return 1; }
}
`})
	files := newOverlay()
	files.set(paths["Main.zam"], `
namespace Main {
    static fun int() f = () { return "s"; }
}
`)

	assert.NotEmpty(t, check(files, paths["Main.zam"]))

	files.remove(paths["Main.zam"])
	assert.Empty(t, check(files, paths["Main.zam"]))
}

// An error in a file the entry imports is reported at the head of the entry,
// with where it lies in the message.
func TestAnalyzeReportsAnImportedFilesErrorAtTheHead(t *testing.T) {
	paths := project(t, map[string]string{
		"Main.zam": `import "Other.zam";

namespace Main {
    static fun int() f = () { return type(Other).g(); }
}
`,
		"Other.zam": `
namespace Other {
    static fun int() g = () { return "s"; }
}
`,
	})

	diagnostics := check(newOverlay(), paths["Main.zam"])
	require.Len(t, diagnostics, 1)
	assert.Equal(t, protocol.Range{}, diagnostics[0].Range)
	assert.Contains(t, diagnostics[0].Message, "Other.zam[3:")
}

// A file the parse recovered in is still checked: what the parse kept binds
// the names it declares, so a real semantic error is reported beside the
// syntax error and nothing is reported about what the syntax error dropped.
func TestAnalyzeChecksABrokenEntry(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": "namespace Main {\n" +
		"    static var int = 1;\n" +
		"    static fun int() f = () { return \"s\"; }\n" +
		"}\n"})

	diagnostics := check(newOverlay(), paths["Main.zam"])
	require.Len(t, diagnostics, 2)
	lines := []protocol.UInteger{diagnostics[0].Range.Start.Line, diagnostics[1].Range.Start.Line}
	assert.ElementsMatch(t, []protocol.UInteger{1, 2}, lines)
}

// What a syntax error leaves kept is enough that nothing which refers to it is
// reported: a variable or member whose initializer did not parse, a method
// whose parameters did not, an enum value whose value did not, and a block that
// lost the return ending it.
func TestAnalyzeReportsNothingAboutWhatASyntaxErrorDropped(t *testing.T) {
	for name, source := range map[string]string{
		"a variable": `namespace Main {
    static fun int() f = () {
        var int x = ;
        return x;
    }
}
`,
		"a member": `namespace Main {
    static var int count = ;
    static fun int() f = () { return count; }
}
`,
		"a method": `namespace Main {
    static fun int() f = () { return h(1); }
    static fun int(int) h = (a b) { return a; }
}
`,
		"an enum value": `namespace Main {
    enum E : int { A(), B(2); }
    static fun int() f = () { return type(E).A{int}; }
}
`,
		"a return": `namespace Main {
    static fun int() f = () {
        return 1 + ;
    }
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			paths := project(t, map[string]string{"Main.zam": source})
			diagnostics := check(newOverlay(), paths["Main.zam"])
			require.Len(t, diagnostics, 1, "%v", diagnostics)
			assert.Contains(t, diagnostics[0].Message, "unexpected token")
		})
	}
}

// A name reached through an import that failed cannot resolve however it is
// written, so what checking finds in a file with a broken import is left out.
func TestAnalyzeLeavesOutSemanticErrorsBehindABrokenImport(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": `import "Missing/*";

namespace Main {
    static fun int() f = () { return type(Missing).g(); }
}
`})

	diagnostics := check(newOverlay(), paths["Main.zam"])
	require.Len(t, diagnostics, 1, "%v", diagnostics)
	assert.Equal(t, protocol.UInteger(0), diagnostics[0].Range.Start.Line)
}

// An entry is checked even when a file it imports did not parse cleanly, and
// what is wrong with the import is reported together at the head of the entry.
func TestAnalyzeChecksAnEntryWhoseImportIsBroken(t *testing.T) {
	paths := project(t, map[string]string{
		"Main.zam": `import "Other.zam";

namespace Main {
    static fun int() f = () { return "s"; }
}
`,
		"Other.zam": `
namespace Other {
    static var int = 1;
    static var str = "s";
    static fun int() g = () { return "s"; }
}
`,
	})

	diagnostics := check(newOverlay(), paths["Main.zam"])
	require.Len(t, diagnostics, 2)

	var head, located protocol.Diagnostic
	for _, d := range diagnostics {
		if d.Range == (protocol.Range{}) {
			head = d
		} else {
			located = d
		}
	}
	assert.Contains(t, head.Message, "Other.zam[3:")
	assert.Contains(t, head.Message, "(and 2 more in ")
	assert.Equal(t, protocol.UInteger(3), located.Range.Start.Line)
}
