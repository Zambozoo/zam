package semantic

import (
	"fmt"

	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

// errAt answers err as having been found where tok was written.
//
// A pass this far from the parse has no lexer to ask, so it says where it is
// looking with the two things it always has: the node it is complaining about,
// which carries the token it starts at, and the declaration being validated,
// which is what says the file that token's offset is an offset into.
func errAt(d syntax.Decl, tok *syntax.Token, err error) error {
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

	aliasUses, errs := expandAliases(syntaxAST)
	if len(errs) > 0 {
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

	if errs := checkAliasUses(aliasUses); len(errs) > 0 {
		return errs
	}

	if errs := validateStaticSemantics(syntaxAST); len(errs) > 0 {
		return errs
	}

	monomorphize(syntaxAST)
	collectInterfaces(syntaxAST)
	return nil
}
