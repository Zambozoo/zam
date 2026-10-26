package lsp

import (
	"io"
	"strings"
	"unicode"

	protocol "github.com/tliron/glsp/protocol_3_16"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// The semantic token types the server reports, in the order of the legend
// announced at initialization; a token's type is its index here.
const (
	tokenKeyword protocol.UInteger = iota
	tokenType
	tokenVariable
	tokenString
	tokenNumber
	tokenOperator
	tokenComment
)

var tokenLegend = protocol.SemanticTokensLegend{
	TokenTypes:     []string{"keyword", "type", "variable", "string", "number", "operator", "comment"},
	TokenModifiers: []string{},
}

var (
	// typeKeywords are the keywords that name a type.
	typeKeywords = map[syntax.TokenKind]bool{
		syntax.TTAny: true, syntax.TTBool: true, syntax.TTByte: true, syntax.TTChar: true,
		syntax.TTFloat: true, syntax.TTInt: true, syntax.TTString: true, syntax.TTThread: true,
		syntax.TTUint: true, syntax.TTVoid: true,
	}
	// punctuation are the symbols that delimit rather than operate, which are
	// left uncolored.
	punctuation = map[syntax.TokenKind]bool{
		syntax.TTLeftParen: true, syntax.TTRightParen: true, syntax.TTLeftBrace: true,
		syntax.TTRightBrace: true, syntax.TTLeftBracket: true, syntax.TTRightBracket: true,
		syntax.TTComma: true, syntax.TTSemiColon: true, syntax.TTPeriod: true,
	}
	keywordKinds = setOf(syntax.Keywords())
	symbolKinds  = setOf(syntax.Symbols())
)

func setOf(m map[string]syntax.TokenKind) map[syntax.TokenKind]bool {
	set := make(map[syntax.TokenKind]bool, len(m))
	for _, kind := range m {
		set[kind] = true
	}
	return set
}

// span is a run of a document's runes that is one semantic token.
type span struct {
	start, end int
	kind       protocol.UInteger
}

// semanticTokens classifies a document's source from its tokens alone, so that
// a document that does not parse is still colored up to where it stops lexing.
// The lexer skips comments along with whitespace, so whatever is not whitespace
// between two tokens is a comment.
func semanticTokens(source string) []protocol.UInteger {
	t := newText(source)
	tokens, err := syntax.Lex(io.NopCloser(strings.NewReader(source)))

	var spans []span
	previous := 0
	for _, tok := range tokens {
		spans = appendComments(spans, t, previous, int(tok.Offset))
		if kind, ok := classify(tok.Kind); ok {
			spans = append(spans, span{start: int(tok.Offset), end: int(tok.End), kind: kind})
		}
		previous = int(tok.End)
	}
	if err == nil {
		spans = appendComments(spans, t, previous, len(t.runes))
	}
	return encode(t, spans)
}

// classify returns the semantic token type of a token kind, if it has one.
func classify(kind syntax.TokenKind) (protocol.UInteger, bool) {
	switch {
	case kind == syntax.TTIdentifier || kind == syntax.TTUnderscore:
		return tokenVariable, true
	case kind == syntax.TTStringLiteral || kind == syntax.TTCharLiteral:
		return tokenString, true
	case kind == syntax.TTIntLiteral || kind == syntax.TTUintLiteral || kind == syntax.TTFloatLiteral:
		return tokenNumber, true
	case typeKeywords[kind]:
		return tokenType, true
	case keywordKinds[kind]:
		return tokenKeyword, true
	case punctuation[kind]:
		return 0, false
	case symbolKinds[kind]:
		return tokenOperator, true
	}
	return 0, false
}

// appendComments appends a comment span for each line of text between from and
// to that is not only whitespace.
func appendComments(spans []span, t *text, from, to int) []span {
	for from < to {
		lineEnd := from
		for lineEnd < to && t.runes[lineEnd] != '\n' {
			lineEnd++
		}
		start, end := from, lineEnd
		for start < end && unicode.IsSpace(t.runes[start]) {
			start++
		}
		for end > start && unicode.IsSpace(t.runes[end-1]) {
			end--
		}
		if start < end {
			spans = append(spans, span{start: start, end: end, kind: tokenComment})
		}
		from = lineEnd + 1
	}
	return spans
}

// encode writes spans in the protocol's relative form: five integers per token,
// its line and start relative to the token before it, its length, its type, and
// its modifiers. A span crossing lines is written as one token per line, since a
// client need not support tokens that span lines.
func encode(t *text, spans []span) []protocol.UInteger {
	data := []protocol.UInteger{}
	var line, character protocol.UInteger
	for _, s := range spans {
		for start := s.start; start < s.end; {
			end := start
			length := 0
			for end < s.end && t.runes[end] != '\n' {
				length += utf16Len(t.runes[end])
				end++
			}
			if length > 0 {
				p := t.position(start)
				if p.Line != line {
					character = 0
				}
				data = append(data, p.Line-line, p.Character-character, protocol.UInteger(length), s.kind, 0)
				line, character = p.Line, p.Character
			}
			start = end + 1
		}
	}
	return data
}
