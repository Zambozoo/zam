package semantic

import (
	"fmt"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// errAt answers err as having been found where tok was written.
//
// A pass this far from the parse has no lexer to ask, so it says where it is
// looking with the two things it always has: the node it is complaining about,
// which carries the token it starts at, and the declaration being validated,
// which is what says the file that token's offset is an offset into. A node
// with neither is reported without a position rather than not reported.
func errAt(d syntax.Decl, tok *syntax.Token, err error) error {
	if d == nil || tok == nil {
		return err
	}
	return base.NewError(err, d.File().Location(tok))
}

// errfAt is errAt over a formatted message.
func errfAt(d syntax.Decl, tok *syntax.Token, format string, args ...any) error {
	return errAt(d, tok, fmt.Errorf(format, args...))
}

func ValidateAST(syntaxAST *syntax.AST) []error {
	if errs := resolveReferenceTypes(syntaxAST); len(errs) > 0 {
		return errs
	}

	if errs := detectGenericCycles(syntaxAST); len(errs) > 0 {
		return errs
	}

	if errs := detectInheritanceCycles(syntaxAST); len(errs) > 0 {
		return errs
	}

	if errs := detectMemberCycles(syntaxAST); len(errs) > 0 {
		return errs
	}

	if errs := validateStaticSemantics(syntaxAST); len(errs) > 0 {
		return errs
	}

	populateGenericDecls(syntaxAST)
	collectGenericTypes(syntaxAST)
	collectTypes(syntaxAST)
	collectInterfaces(syntaxAST)
	return nil
}
