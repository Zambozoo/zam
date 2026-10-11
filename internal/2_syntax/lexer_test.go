package syntax

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestLexer_Next(t *testing.T) {
	t.Parallel()

	type want struct {
		token   *Token
		errFunc assert.ErrorAssertionFunc
	}

	type testCase struct {
		name       string
		runeReader runeReader
		want       []want
		errFunc    assert.ErrorAssertionFunc
	}

	newEOFTestCase := func(name, input string) testCase {
		return testCase{
			name:       name,
			runeReader: newRuneReader(io.NopCloser(strings.NewReader(input))),
			want: []want{
				{
					token:   newToken(TTEOF, uint(utf8.RuneCountInString(input)), nil),
					errFunc: assert.NoError,
				},
			},
			errFunc: assert.NoError,
		}
	}

	newSingleTokenTestCase := func(name, input string, kind TokenKind, value any) testCase {
		return testCase{
			name:       name,
			runeReader: newRuneReader(io.NopCloser(strings.NewReader(input))),
			want: []want{
				{
					token:   newToken(kind, 0, value),
					errFunc: assert.NoError,
				},
			},
			errFunc: assert.NoError,
		}
	}

	newParseErrorTestCase := func(name, input string, target error) testCase {
		return testCase{
			name:       name,
			runeReader: newRuneReader(io.NopCloser(strings.NewReader(input))),
			want: []want{
				{
					errFunc: func(t assert.TestingT, err error, a ...any) bool {
						return assert.ErrorIs(t, err, target, a...)
					},
				},
			},
		}
	}

	// newReadErrorTestCase feeds raw bytes so invalid UTF-8 surfaces a non-EOF
	// read error, exercising the Peek error branches in whitespace scanning.
	newReadErrorTestCase := func(name string, input []byte, target error) testCase {
		return testCase{
			name:       name,
			runeReader: newRuneReader(io.NopCloser(bytes.NewReader(input))),
			want: []want{
				{
					errFunc: func(t assert.TestingT, err error, a ...any) bool {
						return assert.ErrorIs(t, err, target, a...)
					},
				},
			},
		}
	}

	tests := []testCase{
		newEOFTestCase("empty", ""),
		newEOFTestCase("whitespace", " "),
		newEOFTestCase("newline then whitespace", "\n "),
		newEOFTestCase("inline comment", "// comment"),
		newEOFTestCase("comment", "/* comment */"),
		newEOFTestCase("multiline comment", "/* comment\ncomment */"),

		newSingleTokenTestCase("TTEqual", "=", TTEqual, nil),
		newSingleTokenTestCase("TTEqualEqual", "==", TTEqualEqual, nil),
		newSingleTokenTestCase("TTBangEqual", "!=", TTBangEqual, nil),
		newSingleTokenTestCase("TTLess", "<", TTLess, nil),
		newSingleTokenTestCase("TTGreater", ">", TTGreater, nil),
		newSingleTokenTestCase("TTLessEqual", "<=", TTLessEqual, nil),
		newSingleTokenTestCase("TTGreaterEqual", ">=", TTGreaterEqual, nil),
		newSingleTokenTestCase("TTSpaceship", "<=>", TTSpaceship, nil),
		newSingleTokenTestCase("TTAndAnd", "&&", TTAndAnd, nil),
		newSingleTokenTestCase("TTOrOr", "||", TTOrOr, nil),
		newSingleTokenTestCase("TTAnd", "&", TTAnd, nil),
		newSingleTokenTestCase("TTOr", "|", TTOr, nil),
		newSingleTokenTestCase("TTCaret", "^", TTCaret, nil),
		newSingleTokenTestCase("TTTilde", "~", TTTilde, nil),
		newSingleTokenTestCase("TTLessLess", "<<", TTLessLess, nil),
		newSingleTokenTestCase("TTGreaterGreater", ">>", TTGreaterGreater, nil),
		newSingleTokenTestCase("TTGreaterGreaterGreater", ">>>", TTGreaterGreaterGreater, nil),
		newSingleTokenTestCase("TTPlus", "+", TTPlus, nil),
		newSingleTokenTestCase("TTMinus", "-", TTMinus, nil),
		newSingleTokenTestCase("TTStar", "*", TTStar, nil),
		newSingleTokenTestCase("TTSlash", "/", TTSlash, nil),
		newSingleTokenTestCase("TTBang", "!", TTBang, nil),
		newSingleTokenTestCase("TTPercent", "%", TTPercent, nil),
		newSingleTokenTestCase("TTPlusPlus", "++", TTPlusPlus, nil),
		newSingleTokenTestCase("TTMinusMinus", "--", TTMinusMinus, nil),
		newSingleTokenTestCase("TTPeriod", ".", TTPeriod, nil),
		newSingleTokenTestCase("TTPeriodPeriodPeriod", "...", TTPeriodPeriodPeriod, nil),
		newSingleTokenTestCase("TTColon", ":", TTColon, nil),
		newSingleTokenTestCase("TTColonColon", "::", TTColonColon, nil),
		newSingleTokenTestCase("TTSemiColon", ";", TTSemiColon, nil),
		newSingleTokenTestCase("TTLeftBrace", "{", TTLeftBrace, nil),
		newSingleTokenTestCase("TTRightBrace", "}", TTRightBrace, nil),
		newSingleTokenTestCase("TTLeftBracket", "[", TTLeftBracket, nil),
		newSingleTokenTestCase("TTRightBracket", "]", TTRightBracket, nil),
		newSingleTokenTestCase("TTLeftParen", "(", TTLeftParen, nil),
		newSingleTokenTestCase("TTRightParen", ")", TTRightParen, nil),
		newSingleTokenTestCase("TTComma", ",", TTComma, nil),
		newSingleTokenTestCase("TTQuestion", "?", TTQuestion, nil),
		newSingleTokenTestCase("TTQuestionQuestion", "??", TTQuestionQuestion, nil),
		newSingleTokenTestCase("TTAt", "@", TTAt, nil),
		newSingleTokenTestCase("TTHash", "#", TTHash, nil),
		newSingleTokenTestCase("TTSubtype", "<:", TTSubtype, nil),
		newSingleTokenTestCase("TTSupertype", ":>", TTSupertype, nil),

		newSingleTokenTestCase("TTBreak", "break", TTBreak, nil),
		newSingleTokenTestCase("TTContinue", "continue", TTContinue, nil),
		newSingleTokenTestCase("TTFor", "for", TTFor, nil),
		newSingleTokenTestCase("TTFun", "fun", TTFun, nil),
		newSingleTokenTestCase("TTIf", "if", TTIf, nil),
		newSingleTokenTestCase("TTImport", "import", TTImport, nil),
		newSingleTokenTestCase("TTInterface", "interface", TTInterface, nil),
		newSingleTokenTestCase("TTClass", "class", TTClass, nil),
		newSingleTokenTestCase("TTAbstract", "abstract", TTAbstract, nil),
		newSingleTokenTestCase("TTEnum", "enum", TTEnum, nil),
		newSingleTokenTestCase("TTElse", "else", TTElse, nil),
		newSingleTokenTestCase("TTLambda", "lambda", TTLambda, nil),
		newSingleTokenTestCase("TTNative", "native", TTNative, nil),
		newSingleTokenTestCase("TTNew", "new", TTNew, nil),
		newSingleTokenTestCase("TTReturn", "return", TTReturn, nil),
		newSingleTokenTestCase("TTSuper", "super", TTSuper, nil),
		newSingleTokenTestCase("TTThis", "this", TTThis, nil),
		newSingleTokenTestCase("TTVoid", "void", TTVoid, nil),
		newSingleTokenTestCase("TTAny", "any", TTAny, nil),
		newSingleTokenTestCase("TTThread", "thread", TTThread, nil),
		newSingleTokenTestCase("TTVar", "var", TTVar, nil),
		newSingleTokenTestCase("TTPublic", "public", TTPublic, nil),
		newSingleTokenTestCase("TTPrivate", "private", TTPrivate, nil),
		newSingleTokenTestCase("TTProtected", "protected", TTProtected, nil),
		newSingleTokenTestCase("TTStatic", "static", TTStatic, nil),
		newSingleTokenTestCase("TTLabel", "label", TTLabel, nil),
		newSingleTokenTestCase("TTNamespace", "namespace", TTNamespace, nil),
		newSingleTokenTestCase("TTInt", "int", TTInt, nil),
		newSingleTokenTestCase("TTUint", "uint", TTUint, nil),
		newSingleTokenTestCase("TTFloat", "float", TTFloat, nil),
		newSingleTokenTestCase("TTBool", "bool", TTBool, nil),
		newSingleTokenTestCase("TTString", "str", TTString, nil),
		newSingleTokenTestCase("TTStruct", "struct", TTStruct, nil),
		newSingleTokenTestCase("TTChar", "char", TTChar, nil),
		newSingleTokenTestCase("TTByte", "byte", TTByte, nil),
		newSingleTokenTestCase("TTVirtual", "virtual", TTVirtual, nil),
		newSingleTokenTestCase("TTNull", "null", TTNull, nil),
		newSingleTokenTestCase("TTTrue", "true", TTTrue, nil),
		newSingleTokenTestCase("TTFalse", "false", TTFalse, nil),
		newSingleTokenTestCase("TTDelete", "delete", TTDelete, nil),
		newSingleTokenTestCase("TTMark", "mark", TTMark, nil),
		newSingleTokenTestCase("TTWait", "wait", TTWait, nil),
		newSingleTokenTestCase("TTAsync", "async", TTAsync, nil),

		newSingleTokenTestCase("TTIntLiteral", "123", TTIntLiteral, uint64(123)),
		newSingleTokenTestCase("TTUintLiteral lowercase", "123u", TTUintLiteral, uint64(123)),
		newSingleTokenTestCase("TTUintLiteral uppercase", "123U", TTUintLiteral, uint64(123)),
		newSingleTokenTestCase("TTFloatLiteral", "123.456", TTFloatLiteral, 123.456),
		newSingleTokenTestCase("TTFloatLiteral with exponent", "123.456e10", TTFloatLiteral, 123.456e10),
		newSingleTokenTestCase("TTFloatLiteral with negative exponent", "123.456e-10", TTFloatLiteral, 123.456e-10),
		newSingleTokenTestCase("TTStringLiteral", "\"hello\"", TTStringLiteral, "hello"),
		newSingleTokenTestCase("TTStringLiteral with newline", "\"hello\\nworld\"", TTStringLiteral, "hello\nworld"),
		newSingleTokenTestCase("TTStringLiteral with tab", "\"hello\\tworld\"", TTStringLiteral, "hello\tworld"),
		newSingleTokenTestCase("TTStringLiteral with carriage return", "\"hello\\rworld\"", TTStringLiteral, "hello\rworld"),
		newSingleTokenTestCase("TTStringLiteral with backslash", "\"hello\\\\world\"", TTStringLiteral, "hello\\world"),
		newSingleTokenTestCase("TTStringLiteral with quote", "\"hello\\\"world\"", TTStringLiteral, "hello\"world"),
		newSingleTokenTestCase("TTStringLiteral with \\0", "\"hello\\0world\"", TTStringLiteral, "hello\u0000world"),
		newSingleTokenTestCase("TTStringLiteral with \\u", "\"hello\\u0000world\"", TTStringLiteral, "hello\u0000world"),
		newSingleTokenTestCase("TTStringLiteral with \\uFFFD", "\"\\uFFFD\"", TTStringLiteral, "\uFFFD"),
		newSingleTokenTestCase("TTStringLiteral with \\ufffe", "\"\\ufffe\"", TTStringLiteral, "\ufffe"),
		newSingleTokenTestCase("TTStringLiteral with \\U", "\"hello\\U00000000world\"", TTStringLiteral, "hello\u0000world"),
		newSingleTokenTestCase("TTCharLiteral", "'a'", TTCharLiteral, 'a'),
		newSingleTokenTestCase("TTCharLiteral with escape", "'\\n'", TTCharLiteral, '\n'),
		newSingleTokenTestCase("TTIdentifier", "hello", TTIdentifier, "hello"),
		newSingleTokenTestCase("TTUnderscore from a bare _", "_", TTUnderscore, nil),
		// An underscore followed by other identifier characters is a plain
		// identifier that happens to begin with one, not a blank. The
		// language still has to let `_foo` and `foo_` through unchanged.
		newSingleTokenTestCase("TTIdentifier beginning with _", "_x", TTIdentifier, "_x"),
		newSingleTokenTestCase("TTIdentifier ending in _", "x_", TTIdentifier, "x_"),
		newSingleTokenTestCase("TTIdentifier of two underscores", "__", TTIdentifier, "__"),
		{
			name:       "identifier and period",
			runeReader: newRuneReader(io.NopCloser(strings.NewReader("hello."))),
			want: []want{
				{
					token:   newToken(TTIdentifier, 0, "hello"),
					errFunc: assert.NoError,
				},
				{
					token:   newToken(TTPeriod, 5, nil),
					errFunc: assert.NoError,
				},
			},
		},
		{
			name:       "inline comment then token",
			runeReader: newRuneReader(io.NopCloser(strings.NewReader("// comment\n42"))),
			want: []want{
				{
					token:   newToken(TTIntLiteral, 11, uint64(42)),
					errFunc: assert.NoError,
				},
			},
		},
		{
			name:       "slash not followed by comment",
			runeReader: newRuneReader(io.NopCloser(strings.NewReader("/+"))),
			want: []want{
				{
					token:   newToken(TTSlash, 0, nil),
					errFunc: assert.NoError,
				},
				{
					token:   newToken(TTPlus, 1, nil),
					errFunc: assert.NoError,
				},
			},
		},
		newParseErrorTestCase("invalid symbol", "`", errInvalidSymbol),
		newReadErrorTestCase("invalid utf-8 in keyword prefix", []byte{'i', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in identifier", []byte{'z', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in symbol", []byte{'<', 0x80}, errInvalidUTF8Input),
		newParseErrorTestCase("invalid float, no exponent", "123.456e", errInvalidFloat),
		newParseErrorTestCase("invalid float, no digits after decimal", "123.", errInvalidFloat),
		newParseErrorTestCase("invalid float, invalid digits after decimal", "123.a", errInvalidFloat),
		newParseErrorTestCase("invalid float, no negative exponent", "123.456e-", errInvalidFloat),
		newParseErrorTestCase("invalid float, invalid exponent", "123.456ea", errInvalidFloat),
		newParseErrorTestCase("invalid float, invalid negative exponent", "123.456e-a", errInvalidFloat),
		newParseErrorTestCase("integer overflow", "99999999999999999999", errInvalidInt),
		newParseErrorTestCase("float overflow", "1e999", errInvalidFloat),
		newReadErrorTestCase("invalid utf-8 in integer", []byte{'1', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in fraction start", []byte{'1', '.', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in fraction", []byte{'1', '.', '2', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in exponent", []byte{'1', 'e', '-', 0x80}, errInvalidUTF8Input),
		newParseErrorTestCase("unterminated string literal", "\"hello", errUnterminatedStringLiteral),
		newReadErrorTestCase("invalid utf-8 in string literal", []byte{'"', 0x80}, errInvalidUTF8Input),
		newParseErrorTestCase("invalid escape sequence in string literal", "\"\\", errInvalidEscapeSequence),
		newParseErrorTestCase("unknown escape sequence in string literal", "\"\\z", errInvalidEscapeSequence),
		newParseErrorTestCase("invalid single quote escape sequence in string literal", "\"\\'", errInvalidEscapeSequence),
		newParseErrorTestCase("invalid unicode escape sequence in string literal", "\"\\u", errInvalidEscapeSequence),
		newParseErrorTestCase("invalid full unicode escape sequence in string literal", "\"\\U", errInvalidEscapeSequence),
		newParseErrorTestCase("unterminated unicode escape sequence in string literal", "\"\\u0", errInvalidEscapeSequence),
		newParseErrorTestCase("unterminated full unicode escape sequence in string literal", "\"\\U0", errInvalidEscapeSequence),
		newParseErrorTestCase("non-hex digit in unicode escape sequence in string literal", "\"\\uz", errInvalidEscapeSequence),
		newParseErrorTestCase("non-hex digit infull unicode escape sequence in string literal", "\"\\Uz", errInvalidEscapeSequence),
		newParseErrorTestCase("invalid unicode escape: surrogate half in string literal", "\"\\ud800\"", errInvalidUTF8),
		newParseErrorTestCase("invalid unicode escape: above max in string literal", "\"\\U00110000\"", errInvalidUTF8),
		newReadErrorTestCase("invalid utf-8 after escape backslash", []byte{'"', '\\', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in unicode escape", []byte{'"', '\\', 'u', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in full unicode escape", []byte{'"', '\\', 'U', 0x80}, errInvalidUTF8Input),
		newParseErrorTestCase("unterminated char literal", "'", errUnterminatedCharLiteral),
		newParseErrorTestCase("char literal with no character", "''", errUnterminatedCharLiteral),
		newParseErrorTestCase("unterminated char literal with character", "'h", errUnterminatedCharLiteral),
		newParseErrorTestCase("multicharacter char literal", "'he", errUnterminatedCharLiteral),
		newReadErrorTestCase("invalid utf-8 as char", []byte{'\'', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 as char closing quote", []byte{'\'', 'a', 0x80}, errInvalidUTF8Input),
		newParseErrorTestCase("invalid double quote escape sequence in string literal", "'\\\"", errInvalidEscapeSequence),
		newParseErrorTestCase("unterminated comment", "/*", errUnterminatedComment),
		newParseErrorTestCase("unterminated comment, no closing */", "/* hello *", errUnterminatedComment),

		newReadErrorTestCase("invalid utf-8 in whitespace", []byte{0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 after slash", []byte{'/', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in inline comment", []byte{'/', '/', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 in block comment", []byte{'/', '*', 0x80}, errInvalidUTF8Input),
		newReadErrorTestCase("invalid utf-8 after block comment char", []byte{'/', '*', 'a', 0x80}, errInvalidUTF8Input),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lexer, err := NewLexer(tt.runeReader)
			assert.NoError(t, err)
			defer lexer.Close()

			for _, want := range tt.want {
				got, err := lexer.Next()
				want.errFunc(t, err)
				assert.Equal(t, want.token, got)
			}

			if tt.errFunc != nil {
				got, err := lexer.Next()
				tt.errFunc(t, err)
				assert.Equal(t, newToken(TTEOF, tt.runeReader.Offset(), nil), got)
			}
		})
	}
}
