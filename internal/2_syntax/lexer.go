package syntax

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type (
	Lexer struct {
		rr         runeReader
		LineStarts []uint
	}

	// LexedToken is a token together with the offset, in runes, just past its
	// last rune.
	LexedToken struct {
		*Token
		End uint
	}
)

var (
	errUnterminatedComment       = errors.New("unterminated comment")
	errInvalidEscapeSequence     = errors.New("invalid escape sequence")
	errUnterminatedStringLiteral = errors.New("unterminated string literal")
	errUnterminatedCharLiteral   = errors.New("unterminated char literal")
	errInvalidUTF8               = errors.New("invalid UTF-8 sequence")
	errInvalidInt                = errors.New("invalid integer")
	errInvalidSymbol             = errors.New("invalid symbol")
	errInvalidFloat              = errors.New("invalid float")
)

// NewLexer opens path via streamer and reads UTF-8 runes through a RuneReader.
// symbolTrie and keywordTrie must be non-nil (populate keys before calling).
func NewLexer(runeReader runeReader) (*Lexer, error) {
	return &Lexer{
		rr:         runeReader,
		LineStarts: []uint{0},
	}, nil
}

// newLine records where the line following the newline at the reader's current
// position begins. The reader is still sitting on the newline when this is
// called, and the newline belongs to the line it ends, so the next line starts
// one rune further on.
func (l *Lexer) newLine() {
	l.LineStarts = append(l.LineStarts, l.rr.Offset()+1)
}

// Close closes the underlying file reader.
func (l *Lexer) Close() {
	l.rr.Close()
}

// Lex reads every token in r, each with where it ends. A token does not record
// its own length, so this is how one is found again after the fact: by lexing
// the source a second time. A lexing error ends the read, and the tokens before
// it are returned with the error.
func Lex(r io.ReadCloser) ([]LexedToken, error) {
	lexer, err := NewLexer(newRuneReader(r))
	if err != nil {
		return nil, err
	}
	defer lexer.Close()

	var tokens []LexedToken
	for {
		tok, err := lexer.Next()
		if err != nil {
			return tokens, err
		}
		if tok.Kind == TTEOF {
			return tokens, nil
		}
		tokens = append(tokens, LexedToken{Token: tok, End: lexer.rr.Offset()})
	}
}

func (l *Lexer) Next() (*Token, error) {
	if err := l.whitespace(); err != nil {
		if errors.Is(err, io.EOF) {
			return newToken(TTEOF, l.rr.Offset(), nil), nil
		}
		return nil, err
	}

	switch r, _ := l.rr.Peek(0); { // peeked already in whitespace
	case unicode.IsDigit(r):
		return l.number()
	case unicode.IsLetter(r) || r == '_':
		return l.identifier()
	case r == '"':
		return l.string()
	case r == '\'':
		return l.char()
	default:
		return l.symbol()
	}
}

func (l *Lexer) whitespace() error {
	for {
		r, err := l.rr.Peek(0)
		if err != nil {
			return err
		}
		if r == '\n' {
			l.newLine()
			l.rr.Advance(1)
			continue
		}
		if r == '/' {
			r1, err := l.rr.Peek(1)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			} else if r1 == '/' {
				for {
					c, err := l.rr.Peek(0)
					if err != nil {
						return err
					}
					l.rr.Advance(1)
					if c == '\n' {
						break
					}
				}
				continue
			} else if r1 == '*' {
				l.rr.Advance(2)
				for {
					c0, err := l.rr.Peek(0)
					if err != nil {
						if errors.Is(err, io.EOF) {
							return errUnterminatedComment
						}
						return err
					}
					c1, err := l.rr.Peek(1)
					if err != nil {
						if errors.Is(err, io.EOF) {
							return errUnterminatedComment
						}
						return err
					}
					if c0 == '*' && c1 == '/' {
						l.rr.Advance(2)
						break
					}
					if c0 == '\n' {
						l.newLine()
					}
					l.rr.Advance(1)
				}
				continue
			}
			return nil
		}
		if unicode.IsSpace(r) {
			l.rr.Advance(1)
			continue
		}
		return nil
	}
}

func (l *Lexer) digit() (*rune, error) {
	r, err := l.rr.Peek(0)
	if err != nil {
		return nil, err
	}
	if !unicode.IsDigit(r) {
		return nil, nil
	}
	l.rr.Advance(1)
	return &r, nil
}

func (l *Lexer) number() (*Token, error) {
	tokenType := TTIntLiteral
	offset := l.rr.Offset()
	var b strings.Builder

	for {
		r, err := l.digit()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		} else if r == nil {
			break
		}
		b.WriteRune(*r)
	}

	if dot, err := l.rr.Peek(0); err == nil && dot == '.' {
		tokenType = TTFloatLiteral
		l.rr.Advance(1)
		b.WriteRune('.')

		r, err := l.digit()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errInvalidFloat
			}
			return nil, err
		} else if r == nil {
			return nil, errInvalidFloat
		}

		b.WriteRune(*r)

		for {
			r, err := l.digit()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			} else if r == nil {
				break
			}
			b.WriteRune(*r)
		}
	}

	if e, err := l.rr.Peek(0); err == nil && (e == 'e' || e == 'E') {
		tokenType = TTFloatLiteral
		l.rr.Advance(1)
		b.WriteRune(e)

		if s, err := l.rr.Peek(0); err == nil && s == '-' {
			l.rr.Advance(1)
			b.WriteRune(s)
		}

		for {
			r, err := l.rr.Peek(0)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
			if !unicode.IsDigit(r) {
				break
			}
			b.WriteRune(r)
			l.rr.Advance(1)
		}
	}

	// A trailing `u` or `U` on digits with no fraction and no exponent makes
	// the literal a uint. Float literals are not promoted: `1.0u` reads as the
	// float 1.0 followed by an identifier, since the suffix is about the whole
	// number having been written as an integer and a float was not.
	if tokenType == TTIntLiteral {
		if u, err := l.rr.Peek(0); err == nil && (u == 'u' || u == 'U') {
			tokenType = TTUintLiteral
			l.rr.Advance(1)
		}
	}

	lit := b.String()
	if tokenType == TTIntLiteral || tokenType == TTUintLiteral {
		value, err := strconv.ParseUint(lit, 10, 64)
		if err != nil {
			return nil, errInvalidInt
		}
		return newToken(tokenType, offset, value), nil
	}
	value, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return nil, errInvalidFloat
	}
	return newToken(tokenType, offset, value), nil
}

func (l *Lexer) isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// unicodeScalarFromUint maps a numeric code point from a \u or \U escape to a rune.
// It rejects values above U+10FFFF and UTF-16 surrogate halves (U+D800–U+DFFF).
func unicodeScalarFromUint(codePoint uint64) (rune, error) {
	if codePoint > utf8.MaxRune {
		return 0, errInvalidUTF8
	}
	r := rune(codePoint)
	if !utf8.ValidRune(r) {
		return 0, errInvalidUTF8
	}
	return r, nil
}

func (l *Lexer) escapeSequence(isString bool) (rune, error) {
	l.rr.Advance(1)
	r, err := l.rr.Peek(0)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return 0, errInvalidEscapeSequence
		}
		return 0, err
	}
	switch r {
	case 'n':
		return '\n', nil
	case 't':
		return '\t', nil
	case 'r':
		return '\r', nil
	case '0':
		return '\u0000', nil
	case '\\':
		return '\\', nil
	case '"', '\'':
		if isString == (r == '\'') {
			return 0, errInvalidEscapeSequence
		}
		return r, nil
	case 'u':
		var hex string
		for i := 0; i < 4; i++ {
			d, err := l.rr.Peek(1)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return 0, errInvalidEscapeSequence
				}
				return 0, err
			}
			if !l.isHexDigit(d) {
				return 0, errInvalidEscapeSequence
			}
			hex += string(d)
			l.rr.Advance(1)
		}
		codePoint, _ := strconv.ParseUint(hex, 16, 32)
		return unicodeScalarFromUint(codePoint)
	case 'U':
		var hex string
		for i := 0; i < 8; i++ {
			d, err := l.rr.Peek(1)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return 0, errInvalidEscapeSequence
				}
				return 0, err
			}
			if !l.isHexDigit(d) {
				return 0, errInvalidEscapeSequence
			}
			hex += string(d)
			l.rr.Advance(1)
		}
		codePoint, _ := strconv.ParseUint(hex, 16, 64)
		return unicodeScalarFromUint(codePoint)
	default:
		return 0, errInvalidEscapeSequence
	}
}

func (l *Lexer) string() (*Token, error) {
	offset := l.rr.Offset()
	l.rr.Advance(1)
	var value strings.Builder
	for {
		r, err := l.rr.Peek(0)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errUnterminatedStringLiteral
			}
			return nil, err
		}
		if r == '"' {
			break
		}
		if r == '\\' {
			escape, err := l.escapeSequence(true)
			if err != nil {
				return nil, err
			}
			value.WriteRune(escape)
		} else {
			value.WriteRune(r)
		}
		l.rr.Advance(1)
	}

	l.rr.Advance(1)
	return newToken(TTStringLiteral, offset, value.String()), nil
}

func (l *Lexer) char() (*Token, error) {
	offset := l.rr.Offset()
	l.rr.Advance(1)
	r, err := l.rr.Peek(0)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errUnterminatedCharLiteral
		}
		return nil, err
	}

	switch r {
	case '\\':
		escape, err := l.escapeSequence(false)
		if err != nil {
			return nil, err
		}
		r = escape
	case '\'':
		return nil, errUnterminatedCharLiteral
	}

	l.rr.Advance(1)

	q, err := l.rr.Peek(0)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errUnterminatedCharLiteral
		}
		return nil, err
	}
	if q != '\'' {
		return nil, errUnterminatedCharLiteral
	}
	l.rr.Advance(1)
	return newToken(TTCharLiteral, offset, r), nil
}

func (l *Lexer) identifier() (*Token, error) {
	offset := l.rr.Offset()
	kw, path, err := keywordTrie.MatchPrefix(l.rr)
	if err != nil {
		return nil, err
	}
	prefix := *path
	if kw == nil {
		kw = &TTIdentifier
	}

	var b strings.Builder
	b.WriteString(prefix)

	for {
		r, err := l.rr.Peek(0)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return finishIdentifier(*kw, offset, b.String()), nil
			}
			return nil, err
		} else if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return finishIdentifier(*kw, offset, b.String()), nil
		}

		kw = &TTIdentifier
		b.WriteRune(r)
		l.rr.Advance(1)
	}
}

// finishIdentifier picks the token kind an identifier run finishes as. A bare
// `_` is tokenized as [TTUnderscore], the one identifier the language refuses
// as a name — see [TTUnderscore] for why it is its own kind rather than a
// value asked for after the fact. Anything else answers to the kind the
// prefix matched: a keyword if the prefix was one and nothing followed it, an
// identifier otherwise.
func finishIdentifier(kw TokenKind, offset uint, text string) *Token {
	if kw == TTIdentifier && text == "_" {
		return newToken(TTUnderscore, offset, nil)
	}
	var value any
	if kw == TTIdentifier {
		value = text
	}
	return newToken(kw, offset, value)
}

func (l *Lexer) symbol() (*Token, error) {
	offset := l.rr.Offset()
	val, path, err := symbolTrie.MatchPrefix(l.rr)
	if err != nil {
		return nil, err
	}
	if val == nil || path == nil || *path == "" {
		return nil, errInvalidSymbol
	}
	return newToken(*val, offset, nil), nil
}
