package lsp

import (
	"sort"
	"unicode/utf16"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// text is a document's source indexed the way the compiler and the editor each
// count positions. The compiler counts runes, from a 1-based line and column;
// the editor counts UTF-16 code units, from a 0-based line and character.
type text struct {
	runes []rune
	// lineStarts holds the rune offset each line begins at.
	lineStarts []int
}

func newText(s string) *text {
	t := &text{runes: []rune(s), lineStarts: []int{0}}
	for i, r := range t.runes {
		if r == '\n' {
			t.lineStarts = append(t.lineStarts, i+1)
		}
	}
	return t
}

// offset returns the rune offset of a compiler position, clamped to the text.
func (t *text) offset(line, column int) int {
	line = min(max(line, 1), len(t.lineStarts))
	start := t.lineStarts[line-1]
	end := len(t.runes)
	if line < len(t.lineStarts) {
		end = t.lineStarts[line] - 1
	}
	return min(start+max(column, 1)-1, end)
}

// position returns the editor position of a rune offset, clamped to the text.
func (t *text) position(offset int) protocol.Position {
	offset = min(max(offset, 0), len(t.runes))
	line := sort.Search(len(t.lineStarts), func(i int) bool {
		return t.lineStarts[i] > offset
	}) - 1

	var character int
	for _, r := range t.runes[t.lineStarts[line]:offset] {
		character += utf16Len(r)
	}
	return protocol.Position{Line: protocol.UInteger(line), Character: protocol.UInteger(character)}
}

// offsetAt returns the rune offset of an editor position, clamped to the text.
func (t *text) offsetAt(p protocol.Position) int {
	line := min(int(p.Line), len(t.lineStarts)-1)
	offset := t.lineStarts[line]
	for units := 0; offset < len(t.runes) && t.runes[offset] != '\n' && units < int(p.Character); offset++ {
		units += utf16Len(t.runes[offset])
	}
	return offset
}

// lineOf returns the text of the line a rune offset lies on.
func (t *text) lineOf(offset int) string {
	return t.line(int(t.position(offset).Line))
}

// line returns the text of a line, counted from 0.
func (t *text) line(line int) string {
	end := len(t.runes)
	if line+1 < len(t.lineStarts) {
		end = t.lineStarts[line+1] - 1
	}
	return string(t.runes[t.lineStarts[line]:end])
}

// utf16Len returns how many UTF-16 code units r takes. A rune that is not valid
// Unicode is written as the replacement character, which takes one.
func utf16Len(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1
}
