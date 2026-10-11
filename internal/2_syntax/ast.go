package syntax

import base "github.com/zambozoo/zam/internal/0_base"

// BlankName is the string a local binding carries when the source wrote `_`.
// A variable, parameter or lambda parameter written as `_` reserves the stack
// room its type needs — so the value is still built, its destructor still
// runs, and the collector still walks it — but is not placed in any lookup
// scope: two `_` in one scope do not conflict because there is no name to
// conflict on. The read side is caught at the lexer, which turns a bare `_`
// into [TTUnderscore] rather than an identifier, so an expression can never
// reach for one.
const BlankName = "_"

type (
	SyntaxParser interface {
		NewPath(pathStr string) (*base.Path, error)
		AddPath(path *base.Path)
		ListCodeFiles(dir *base.Path) ([]*base.Path, error)
		Consume(kind TokenKind) (*Token, error)
		ConsumeAll(kinds ...TokenKind) ([]Token, error)
		ConsumeAny(kinds ...TokenKind) (*Token, error)
		Decl() *Decl
		// Method returns a pointer to the FieldMethod currently being parsed,
		// or nil-pointing when no method parse is active. The method is held
		// so a generic parameter named inside the method's signature or body
		// can be resolved against the method's own parameter list, which
		// shadows the enclosing decl's.
		Method() **FieldMethod
		File() **File
		Match(kinds ...TokenKind) (*Token, error)
		Peek() (*Token, error)
		Project() *Project
		UnexpectedToken(got TokenKind, want ...TokenKind) error
	}
)
