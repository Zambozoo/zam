package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// token is one decoded semantic token, at its absolute position.
type token struct {
	line, character, length protocol.UInteger
	kind                    protocol.UInteger
}

// decode undoes the protocol's relative encoding.
func decode(data []protocol.UInteger) []token {
	var tokens []token
	var line, character protocol.UInteger
	for i := 0; i+4 < len(data); i += 5 {
		if data[i] != 0 {
			character = 0
		}
		line += data[i]
		character += data[i+1]
		tokens = append(tokens, token{line, character, data[i+2], data[i+3]})
	}
	return tokens
}

func TestSemanticTokensClassifiesEachToken(t *testing.T) {
	source := "var int x = 1 + \"s\"; // note\n" +
		"/* a\n   b */ if\n"

	assert.Equal(t, []token{
		{0, 0, 3, tokenKeyword},
		{0, 4, 3, tokenType},
		{0, 8, 1, tokenVariable},
		{0, 10, 1, tokenOperator},
		{0, 12, 1, tokenNumber},
		{0, 14, 1, tokenOperator},
		{0, 16, 3, tokenString},
		{0, 21, 7, tokenComment},
		{1, 0, 4, tokenComment},
		{2, 3, 4, tokenComment},
		{2, 8, 2, tokenKeyword},
	}, decode(semanticTokens(source)))
}

// A document that stops lexing is still colored up to where it stops.
func TestSemanticTokensStopsAtALexingError(t *testing.T) {
	assert.Equal(t, []token{
		{0, 0, 1, tokenVariable},
	}, decode(semanticTokens("x \"open")))
}

// A token is measured in the UTF-16 code units the editor counts in.
func TestSemanticTokensMeasuresInUTF16(t *testing.T) {
	assert.Equal(t, []token{
		{0, 0, 4, tokenString},
		{0, 5, 1, tokenVariable},
	}, decode(semanticTokens("\"😀\" x")))
}
