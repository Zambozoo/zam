// Package token provides tokens for the syntax parser.
package syntax

import (
	"sort"

	base "github.com/zambozoo/zam/internal/0_base"
)

type (
	TokenKind uint8
	Token     struct {
		// Kind of token
		Kind TokenKind
		// Offset, 0-indexed, of the token in the source code
		Offset uint
		// Value of the token, if non-boolean literal or identifier
		Value any
	}
)

var (
	tokenKindStrings []string
	symbols          = map[string]TokenKind{}
	keywords         = map[string]TokenKind{}
)

func newTokenKind(s string) TokenKind {
	tt := TokenKind(len(tokenKindStrings))
	tokenKindStrings = append(tokenKindStrings, s)
	return tt
}

func newSymbol(s string) TokenKind {
	tt := newTokenKind(s)
	symbols[s] = tt
	return tt
}

func newKeyword(s string) TokenKind {
	tt := newTokenKind(s)
	keywords[s] = tt
	return tt
}

func Keywords() map[string]TokenKind {
	return keywords
}

func Symbols() map[string]TokenKind {
	return symbols
}

var (
	TTNone = newTokenKind("NONE")

	// Symbols
	TTEqual                 = newSymbol("=")
	TTEqualEqual            = newSymbol("==")
	TTBangEqual             = newSymbol("!=")
	TTLess                  = newSymbol("<")
	TTGreater               = newSymbol(">")
	TTLessEqual             = newSymbol("<=")
	TTGreaterEqual          = newSymbol(">=")
	TTSpaceship             = newSymbol("<=>")
	TTAndAnd                = newSymbol("&&")
	TTOrOr                  = newSymbol("||")
	TTAnd                   = newSymbol("&")
	TTOr                    = newSymbol("|")
	TTCaret                 = newSymbol("^")
	TTTilde                 = newSymbol("~")
	TTLessLess              = newSymbol("<<")
	TTGreaterGreater        = newSymbol(">>")
	TTGreaterGreaterGreater = newSymbol(">>>")
	TTPlus                  = newSymbol("+")
	TTMinus                 = newSymbol("-")
	TTStar                  = newSymbol("*")
	TTSlash                 = newSymbol("/")
	TTBang                  = newSymbol("!")
	TTPercent               = newSymbol("%")
	TTPlusPlus              = newSymbol("++")
	TTMinusMinus            = newSymbol("--")
	TTPeriod                = newSymbol(".")
	TTPeriodPeriodPeriod    = newSymbol("...")
	TTColon                 = newSymbol(":")
	TTColonColon            = newSymbol("::")
	TTSemiColon             = newSymbol(";")
	TTLeftBrace             = newSymbol("{")
	TTRightBrace            = newSymbol("}")
	TTLeftBracket           = newSymbol("[")
	TTRightBracket          = newSymbol("]")
	TTLeftParen             = newSymbol("(")
	TTRightParen            = newSymbol(")")
	TTComma                 = newSymbol(",")
	TTQuestion              = newSymbol("?")
	TTQuestionQuestion      = newSymbol("??")
	TTAt                    = newSymbol("@")
	TTHash                  = newSymbol("#")
	TTDollar                = newSymbol("$")
	TTSubtype               = newSymbol("<:")
	TTSupertype             = newSymbol(":>")

	// Keywords
	TTAbstract  = newKeyword("abstract")
	TTAny       = newKeyword("any")
	TTAsync     = newKeyword("async")
	TTBool      = newKeyword("bool")
	TTBreak     = newKeyword("break")
	TTByte      = newKeyword("byte")
	TTChar      = newKeyword("char")
	TTClass     = newKeyword("class")
	TTContinue  = newKeyword("continue")
	TTDelete    = newKeyword("delete")
	TTElse      = newKeyword("else")
	TTEnum      = newKeyword("enum")
	TTFalse     = newKeyword("false")
	TTFloat     = newKeyword("float")
	TTFor       = newKeyword("for")
	TTFree      = newKeyword("free")
	TTFun       = newKeyword("fun")
	TTIf        = newKeyword("if")
	TTImport    = newKeyword("import")
	TTInt       = newKeyword("int")
	TTInterface = newKeyword("interface")
	TTLabel     = newKeyword("label")
	TTLambda    = newKeyword("lambda")
	TTMark      = newKeyword("mark")
	TTNamespace = newKeyword("namespace")
	TTNative    = newKeyword("native")
	TTNew       = newKeyword("new")
	TTNull      = newKeyword("null")
	TTPrivate   = newKeyword("private")
	TTProtected = newKeyword("protected")
	TTPublic    = newKeyword("public")
	TTReturn    = newKeyword("return")
	TTStatic    = newKeyword("static")
	TTString    = newKeyword("str")
	TTStruct    = newKeyword("struct")
	TTSuper     = newKeyword("super")
	TTThis      = newKeyword("this")
	TTThread    = newKeyword("thread")
	TTTrue      = newKeyword("true")
	TTType      = newKeyword("type")
	TTVar       = newKeyword("var")
	TTVirtual   = newKeyword("virtual")
	TTVoid      = newKeyword("void")
	TTUint      = newKeyword("uint")
	TTWait      = newKeyword("wait")

	// Literals
	TTIntLiteral    = newTokenKind("INT_LITERAL")
	TTUintLiteral   = newTokenKind("UINT_LITERAL")
	TTFloatLiteral  = newTokenKind("FLOAT_LITERAL")
	TTStringLiteral = newTokenKind("STRING_LITERAL")
	TTCharLiteral   = newTokenKind("CHAR_LITERAL")
	TTNullLiteral   = newTokenKind("NULL_LITERAL")
	TTIdentifier    = newTokenKind("IDENTIFIER")
	// TTUnderscore is the bare `_`, the one identifier the language refuses to
	// treat as a name. It is tokenized as its own kind so a parser that only
	// takes a [TTIdentifier] — a decl's name, a field's, a label's — refuses
	// it at the token rather than at the first use that cannot find what it
	// should. The places that do accept a blank — var declarations, function
	// parameters, lambda parameters — take [TTUnderscore] beside the
	// identifier.
	TTUnderscore = newTokenKind("UNDERSCORE")
	TTEOF        = newTokenKind("EOF")
)

func newToken(kind TokenKind, offset uint, value any) *Token {
	return &Token{
		Kind:   kind,
		Offset: offset,
		Value:  value,
	}
}

func (k TokenKind) String() string {
	return tokenKindStrings[k]
}

func (t *Token) Position(lineStarts []uint) *base.Position {
	lineIndex := max(0, sort.Search(len(lineStarts), func(i int) bool {
		return lineStarts[i] > t.Offset
	}))
	lineStart := lineStarts[lineIndex-1]

	return &base.Position{
		Line:   int(lineIndex),
		Column: int(t.Offset - lineStart + 1),
	}
}
