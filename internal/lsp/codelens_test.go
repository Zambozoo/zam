package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	protocol "github.com/tliron/glsp/protocol_3_16"
	base "github.com/zambozoo/zam/internal/0_base"
)

const lensTestSource = `public namespace XTest {
    public fun void(uint, str...) First = (argC, argV) {
    }

    public fun void(uint, str...) Second = (argC, argV) {
    }
}
`

// lenses opens path holding text, and answers its code lenses once it has
// been checked.
func lenses(t *testing.T, path base.Path, text string) []protocol.CodeLens {
	t.Helper()
	uri := fileURI(path.String())
	c := newClient(t)
	c.call(protocol.MethodInitialize, protocol.InitializeParams{})
	c.call(protocol.MethodTextDocumentDidOpen, protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{
		URI: uri, LanguageID: "zam", Version: 1, Text: text,
	}})
	c.next()
	result := c.call(protocol.MethodTextDocumentCodeLens, protocol.CodeLensParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	lenses, _ := result.([]protocol.CodeLens)
	return lenses
}

func TestCodeLensesRunAndDebugTests(t *testing.T) {
	t.Parallel()
	paths := project(t, map[string]string{"X.test.zam": lensTestSource})
	path := paths["X.test.zam"]

	got := lenses(t, path, lensTestSource)
	require.Len(t, got, 6)

	type lens struct {
		line, character protocol.UInteger
		title, command  string
		name            any
	}
	var described []lens
	for _, l := range got {
		require.NotNil(t, l.Command)
		require.Len(t, l.Command.Arguments, 2)
		assert.Equal(t, path.String(), l.Command.Arguments[0])
		described = append(described, lens{l.Range.Start.Line, l.Range.Start.Character, l.Command.Title, l.Command.Command, l.Command.Arguments[1]})
	}
	assert.Equal(t, []lens{
		{0, 17, "▶ run tests", commandRunTest, ""},
		{0, 17, "debug tests", commandDebugTest, ""},
		{1, 34, "▶ run test", commandRunTest, "XTest.First"},
		{1, 34, "debug test", commandDebugTest, "XTest.First"},
		{4, 34, "▶ run test", commandRunTest, "XTest.Second"},
		{4, 34, "debug test", commandDebugTest, "XTest.Second"},
	}, described)
}

func TestCodeLensesOnlyForTestFiles(t *testing.T) {
	t.Parallel()
	main := "namespace Main {\n    fun void(uint, str...) main = (argC, argV) {\n    }\n}\n"
	wrong := "public namespace Wrong {\n    public fun void(uint, str...) First = (argC, argV) {\n    }\n}\n"
	paths := project(t, map[string]string{"Main.zam": main, "Y.test.zam": wrong})

	assert.Empty(t, lenses(t, paths["Main.zam"], main), "a file that is not a test file")
	assert.Empty(t, lenses(t, paths["Y.test.zam"], wrong), "a test file the runner would refuse")
}
