package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// The compiler counts runes from 1 and the editor counts UTF-16 code units from
// 0, so a rune outside the Basic Multilingual Plane is one to the compiler and
// two to the editor.
func TestTextConvertsBetweenCompilerAndEditorPositions(t *testing.T) {
	tx := newText("ab\n😀cd\n")

	assert.Equal(t, 0, tx.offset(1, 1))
	assert.Equal(t, 4, tx.offset(2, 2))
	assert.Equal(t, 2, tx.offset(1, 99), "a column past the line clamps to its end")
	assert.Equal(t, 7, tx.offset(99, 1), "a line past the text clamps to the last")

	assert.Equal(t, protocol.Position{Line: 0, Character: 1}, tx.position(1))
	assert.Equal(t, protocol.Position{Line: 1, Character: 2}, tx.position(4))
	assert.Equal(t, protocol.Position{Line: 1, Character: 4}, tx.position(6))
	assert.Equal(t, protocol.Position{Line: 2, Character: 0}, tx.position(99))
}
