package lsp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDocOfReadsTheCommentsJustAbove(t *testing.T) {
	source := "// Detached.\n" +
		"\n" +
		"    // Doc begins here\n" +
		"    //\n" +
		"    //not spaced\n" +
		"    static fun int() f = () { return 1; }\n" +
		"var int x; // trailing\n" +
		"var int y;\n"
	text := newText(source)

	assert.Equal(t, []string{"Doc begins here", "", "not spaced"}, text.docOf(strings.Index(source, "fun")))
	assert.Empty(t, text.docOf(strings.Index(source, "var int y")), "a trailing comment above is not a doc comment")
	assert.Empty(t, text.docOf(0))
}

func TestMarkdownRendersGoDocComments(t *testing.T) {
	doc := []string{
		"Sum adds a_b *and* [c].",
		"",
		"For example:",
		"",
		"    var int s = Sum(a, b);",
		"",
		"      s += 1;",
		"",
		"Then <done>.",
	}
	assert.Equal(t, "Sum adds a\\_b \\*and\\* \\[c\\].\n"+
		"\n"+
		"For example:\n"+
		"\n"+
		"```\n"+
		"var int s = Sum(a, b);\n"+
		"\n"+
		"  s += 1;\n"+
		"```\n"+
		"\n"+
		"Then \\<done\\>.", markdown(doc))
}
