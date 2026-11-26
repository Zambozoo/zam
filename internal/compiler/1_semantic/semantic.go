package semantic

import (
	"fmt"

	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

// Pass is one of the checks validation runs, in the order they run.
type Pass int

const (
	// PassResolve binds every type reference and import to what it names.
	PassResolve Pass = iota
	// PassAlias replaces every alias written where a type is with what it names.
	PassAlias
	// PassGenericCycles rejects generic declarations that instantiate themselves without end.
	PassGenericCycles
	// PassInheritanceCycles rejects declarations that inherit from themselves.
	PassInheritanceCycles
	// PassMemberCycles rejects declarations that hold themselves by value.
	PassMemberCycles
	// PassAliasUses requires each alias be accessible where it is written and its arguments satisfy its bounds.
	PassAliasUses
	// PassStatic checks every field, statement and expression.
	PassStatic
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

// ValidateAST checks syntaxAST, and once every check passes, instantiates its
// generics and records the interfaces each declaration implements.
func ValidateAST(syntaxAST *syntax.AST) []error {
	if errs := ValidateUntil(syntaxAST, PassStatic); len(errs) > 0 {
		return errs
	}
	monomorphize(syntaxAST)
	collectInterfaces(syntaxAST)
	return nil
}

// ValidateUntil runs the passes of validation up to and including last,
// answering the errors of the first that finds any.
func ValidateUntil(syntaxAST *syntax.AST, last Pass) []error {
	if errs := resolve(syntaxAST); len(errs) > 0 || last == PassResolve {
		return errs
	}

	aliasUses, errs := expandAliases(syntaxAST)
	if len(errs) > 0 || last == PassAlias {
		return errs
	}

	if errs := detectGenericCycles(syntaxAST); len(errs) > 0 || last == PassGenericCycles {
		return errs
	}

	if errs := detectInheritanceCycles(syntaxAST); len(errs) > 0 || last == PassInheritanceCycles {
		return errs
	}

	if errs := detectMemberCycles(syntaxAST); len(errs) > 0 || last == PassMemberCycles {
		return errs
	}

	if errs := checkAliasUses(aliasUses); len(errs) > 0 || last == PassAliasUses {
		return errs
	}

	return validateStaticSemantics(syntaxAST)
}
