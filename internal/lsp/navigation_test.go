package lsp

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// TestServerAnswersQuestionsAboutNames covers hover, go-to-definition and
// find-references over a name declared in the open document and one declared in
// a file it imports.
func TestServerAnswersQuestionsAboutNames(t *testing.T) {
	main := "import \"Other.zam\";\n" +
		"\n" +
		"namespace Main {\n" +
		"    static fun int() f = () {\n" +
		"        var int x = type(Other).g();\n" +
		"        return x + x;\n" +
		"    }\n" +
		"}\n"
	paths := project(t, map[string]string{
		"Main.zam":  main,
		"Other.zam": "namespace Other {\n    // g returns *one*.\n    static fun int() g = () { return 1; }\n}\n",
	})
	uri := (&url.URL{Scheme: "file", Path: paths["Main.zam"].String()}).String()
	otherURI := (&url.URL{Scheme: "file", Path: paths["Other.zam"].String()}).String()

	c := newClient(t)
	c.call(protocol.MethodInitialize, protocol.InitializeParams{})
	c.call(protocol.MethodTextDocumentDidOpen, protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{
		URI: uri, LanguageID: "zam", Version: 1, Text: main,
	}})
	require.Empty(t, c.next().Diagnostics)

	at := func(line, character protocol.UInteger) protocol.TextDocumentPositionParams {
		return protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: line, Character: character},
		}
	}

	t.Run("hover", func(t *testing.T) {
		hover := c.call(protocol.MethodTextDocumentHover, protocol.HoverParams{TextDocumentPositionParams: at(5, 15)}).(*protocol.Hover)
		content := hover.Contents.(protocol.MarkupContent)
		assert.Contains(t, content.Value, "var int x = type(Other).g();")
		assert.Contains(t, content.Value, "Type: `int`")
		assert.Equal(t, rangeOf(5, 15, 5, 16), *hover.Range)
	})

	t.Run("hover shows the doc comment", func(t *testing.T) {
		hover := c.call(protocol.MethodTextDocumentHover, protocol.HoverParams{TextDocumentPositionParams: at(4, 32)}).(*protocol.Hover)
		content := hover.Contents.(protocol.MarkupContent)
		assert.Equal(t, "```zam\nstatic fun int() g = ()\n```\n\ng returns \\*one\\*.\n\nType: `int()`", content.Value)
	})

	t.Run("hover outlines a declaration", func(t *testing.T) {
		hover := c.call(protocol.MethodTextDocumentHover, protocol.HoverParams{TextDocumentPositionParams: at(4, 26)}).(*protocol.Hover)
		content := hover.Contents.(protocol.MarkupContent)
		assert.Equal(t, "```zam\nnamespace Other {\n    static fun int() g\n}\n```", content.Value)
	})

	t.Run("a cursor just past a name is on it", func(t *testing.T) {
		hover := c.call(protocol.MethodTextDocumentHover, protocol.HoverParams{TextDocumentPositionParams: at(5, 16)}).(*protocol.Hover)
		assert.Equal(t, rangeOf(5, 15, 5, 16), *hover.Range)
	})

	t.Run("definition in another file", func(t *testing.T) {
		location := c.call(protocol.MethodTextDocumentDefinition, protocol.DefinitionParams{TextDocumentPositionParams: at(4, 32)}).(protocol.Location)
		assert.Equal(t, otherURI, location.URI)
		assert.Equal(t, rangeOf(2, 21, 2, 22), location.Range)
	})

	t.Run("references", func(t *testing.T) {
		locations := c.call(protocol.MethodTextDocumentReferences, protocol.ReferenceParams{
			TextDocumentPositionParams: at(4, 16),
			Context:                    protocol.ReferenceContext{IncludeDeclaration: true},
		}).([]protocol.Location)
		require.Len(t, locations, 3)
		assert.Equal(t, rangeOf(4, 16, 4, 17), locations[0].Range)
		assert.Equal(t, rangeOf(5, 15, 5, 16), locations[1].Range)
		assert.Equal(t, rangeOf(5, 19, 5, 20), locations[2].Range)

		locations = c.call(protocol.MethodTextDocumentReferences, protocol.ReferenceParams{
			TextDocumentPositionParams: at(4, 16),
		}).([]protocol.Location)
		assert.Len(t, locations, 2, "without the declaration")
	})

	t.Run("nothing under the cursor", func(t *testing.T) {
		assert.Nil(t, c.call(protocol.MethodTextDocumentDefinition, protocol.DefinitionParams{TextDocumentPositionParams: at(1, 0)}))
	})
}
