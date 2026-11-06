package semantic

import (
	"fmt"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

type (
	// aliasUse is an alias written where a type is, kept to be checked once
	// the declarations its arguments are checked against are known to be
	// well formed.
	aliasUse struct {
		// alias is the alias written.
		alias *syntax.DeclAlias
		// args is the type arguments it was written with, in parameter order.
		args []syntax.Type
		// at is the reference the alias was written as.
		at *syntax.TypeReference
	}

	// aliasState is how far the type an alias names has been expanded.
	aliasState uint8

	// aliasExpander replaces every reference to an alias with the type the
	// alias names.
	aliasExpander struct {
		// errs is the accumulated list of errors.
		errs []error
		// uses is every alias written where a type is.
		uses []aliasUse
		// state is how far each alias's own type has been expanded.
		state map[*syntax.DeclAlias]aliasState
		// stack is the aliases whose types are being expanded, outermost first.
		stack []*syntax.DeclAlias
	}
)

const (
	aliasUnexpanded aliasState = iota
	aliasExpanding
	aliasExpanded
)

// expandAliases replaces every reference to an alias in the AST with the type
// the alias names, its arguments substituted, and answers with each use for
// [checkAliasUses].
func expandAliases(ast *syntax.AST) ([]aliasUse, []error) {
	x := &aliasExpander{state: map[*syntax.DeclAlias]aliasState{}}

	var aliases []*syntax.DeclAlias
	var collect func(d syntax.Decl)
	collect = func(d syntax.Decl) {
		if alias, ok := d.(*syntax.DeclAlias); ok {
			aliases = append(aliases, alias)
		}
		for child := range d.Decls() {
			collect(child)
		}
	}
	for _, entry := range ast.CodeFiles.All() {
		collect(entry.Value.Decl)
	}

	for _, alias := range aliases {
		x.checkParameters(alias)
	}
	if len(x.errs) > 0 {
		return nil, x.errs
	}

	for _, alias := range aliases {
		x.expandAlias(alias)
	}
	if len(x.errs) > 0 {
		return nil, x.errs
	}

	for _, entry := range ast.CodeFiles.All() {
		x.visitDecl(entry.Value.Decl)
	}
	return x.uses, x.errs
}

// errf records an error found where tok was written inside d.
func (x *aliasExpander) errf(d syntax.Decl, tok *syntax.Token, format string, args ...any) {
	x.errs = append(x.errs, errfAt(d, tok, format, args...))
}

// checkParameters requires that the type an alias names, as written, uses
// every parameter of the alias. An alias that ignored a parameter would make
// every argument for it name one type.
func (x *aliasExpander) checkParameters(alias *syntax.DeclAlias) {
	used := base.Set[string]{}
	for arg := range genericArgsIn(alias.Type) {
		used[arg.Name] = struct{}{}
	}
	if alias.Generics() == nil {
		return
	}
	for _, entry := range alias.Generics().All() {
		if _, ok := used[entry.Key.Name]; !ok {
			x.errf(alias, alias.NameToken(), "alias %s does not use its parameter %s", alias.Name(), entry.Key.Name)
		}
	}
}

// genericArgsIn yields every generic argument written in t.
func genericArgsIn(t syntax.Type) func(yield func(*syntax.TypeGenericArg) bool) {
	return func(yield func(*syntax.TypeGenericArg) bool) {
		var walk func(t syntax.Type) bool
		each := func(ts []syntax.Type) bool {
			for _, t := range ts {
				if !walk(t) {
					return false
				}
			}
			return true
		}
		walk = func(t syntax.Type) bool {
			switch t := t.(type) {
			case *syntax.TypeArray:
				return walk(t.Type)
			case *syntax.TypeFunction:
				return walk(t.ReturnType) && each(t.ParameterTypes)
			case *syntax.TypeGeneric:
				return each(t.ParameterTypes)
			case *syntax.TypeGenericArg:
				return yield(t)
			case *syntax.TypeTuple:
				return each(t.Types)
			case *syntax.TypeWrapped:
				return walk(t.Type)
			}
			return true
		}
		walk(t)
	}
}

// expandAlias expands the type alias names, and the types of the aliases it
// names first, reporting whether it could. Following aliases back to one
// already being expanded is a cycle: the type would never stop expanding.
func (x *aliasExpander) expandAlias(alias *syntax.DeclAlias) bool {
	switch x.state[alias] {
	case aliasExpanded:
		return true
	case aliasExpanding:
		var names []string
		for i, outer := range x.stack {
			if outer == alias {
				for _, a := range x.stack[i:] {
					names = append(names, a.Name())
				}
				break
			}
		}
		names = append(names, alias.Name())
		x.errf(alias, alias.NameToken(), "alias %s refers to itself through %s", alias.Name(), strings.Join(names, " -> "))
		return false
	}

	x.state[alias] = aliasExpanding
	x.stack = append(x.stack, alias)
	alias.Type = x.expand(alias.Type)
	x.stack = x.stack[:len(x.stack)-1]
	x.state[alias] = aliasExpanded
	return true
}

// expand returns t with every alias in it replaced by what the alias names.
// The types t is made of are replaced in place, so a type held by a slot that
// cannot be reassigned is expanded all the same, unless t is itself an alias.
func (x *aliasExpander) expand(t syntax.Type) syntax.Type {
	switch t := t.(type) {
	case *syntax.TypeArray:
		t.Type = x.expand(t.Type)
	case *syntax.TypeFunction:
		t.ReturnType = x.expand(t.ReturnType)
		x.expandAll(t.ParameterTypes)
	case *syntax.TypeGeneric:
		x.expandAll(t.ParameterTypes)
		if reference, ok := t.Type.(*syntax.TypeReference); ok && reference.Alias() != nil {
			return x.use(reference, t.ParameterTypes, t)
		}
	case *syntax.TypeReference:
		if t.Alias() != nil {
			return x.use(t, nil, t)
		}
	case *syntax.TypeTuple:
		x.expandAll(t.Types)
	case *syntax.TypeWrapped:
		t.Type = x.expand(t.Type)
	}
	return t
}

// expandAll expands each of ts in place.
func (x *aliasExpander) expandAll(ts []syntax.Type) {
	for i, t := range ts {
		ts[i] = x.expand(t)
	}
}

// use returns what the alias reference resolved to names under args, where
// written is the type the alias was written as.
func (x *aliasExpander) use(reference *syntax.TypeReference, args []syntax.Type, written syntax.Type) syntax.Type {
	alias := reference.Alias()
	if len(args) != alias.Arity() {
		x.errf(reference.OuterDecl(), reference.Start(), "alias %s takes %d type arguments, but %d were provided", alias.Name(), alias.Arity(), len(args))
		return written
	}
	if !x.expandAlias(alias) {
		return written
	}

	bindings := make(map[syntax.GenericArgKey]syntax.Type, len(args))
	if alias.Generics() != nil {
		for i, entry := range alias.Generics().All() {
			bindings[entry.Key] = args[i]
		}
	}
	x.uses = append(x.uses, aliasUse{alias: alias, args: args, at: reference})
	return syntax.Instantiate(alias.Type, bindings, reference.Start(), reference.OuterDecl())
}

// expandBounds expands the types a parameter list's bounds are written with.
func (x *aliasExpander) expandBounds(generics *base.StaticOrderedMap[syntax.GenericArgKey, syntax.GenericBound]) {
	if generics == nil {
		return
	}
	for _, entry := range generics.All() {
		if entry.Value != nil {
			x.expandAll(entry.Value.Bounds())
		}
	}
}

// visitDecl expands every type written in d and in the declarations it holds.
func (x *aliasExpander) visitDecl(d syntax.Decl) {
	if g, ok := d.(syntax.DeclGeneric); ok {
		x.expandBounds(g.Generics())
	}
	if s, ok := d.(syntax.DeclSubtypeable); ok && s.BaseDeclType() != nil {
		x.expand(s.BaseDeclType())
		if _, ok := s.BaseDeclType().Type.(syntax.TypeResolvable); !ok {
			x.errf(d, s.BaseDeclType().Start(), "base declaration type %q is not a pointer to a resolvable type", s.BaseDeclType().String())
		}
	}
	for _, field := range d.StaticFields() {
		x.visitField(field)
	}
	for _, field := range d.InstanceFields() {
		x.visitField(field)
	}
	for child := range d.Decls() {
		x.visitDecl(child)
	}
}

// visitField expands every type written in f.
func (x *aliasExpander) visitField(f syntax.Field) {
	switch f := f.(type) {
	case *syntax.FieldEnum:
		x.visitExprs(f.Parameters)
		x.visitExpr(f.Value)
	case *syntax.FieldMember:
		f.SetFieldType(x.expand(f.FieldType()))
		x.visitExpr(f.Expr)
	case *syntax.FieldMethod:
		x.expandBounds(f.Generics())
		x.expand(f.FieldType())
		x.visitStmt(f.Body)
	default:
		panic(fmt.Sprintf("unknown field %T", f))
	}
}

// visitStmt expands every type written in s, which may be nil.
func (x *aliasExpander) visitStmt(s syntax.Stmt) {
	switch s := s.(type) {
	case nil:
	case *syntax.StmtBlock:
		for _, stmt := range s.Stmts {
			x.visitStmt(stmt)
		}
	case *syntax.StmtExpr:
		x.visitExpr(s.Expr)
	case *syntax.StmtFor:
		x.visitExpr(s.Condition)
		x.visitStmt(s.Body)
		x.visitStmt(s.Step)
		x.visitStmt(s.Else)
	case *syntax.StmtIf:
		x.visitExpr(s.Condition)
		x.visitStmt(s.Then)
		x.visitStmt(s.Else)
	case *syntax.StmtLabel:
		x.visitStmt(s.LoopStmt)
	case *syntax.StmtSwitch:
		x.visitExpr(s.Expr)
		for _, c := range s.Cases {
			x.visitExprs(c.Values)
		}
		for body := range s.Bodies() {
			x.visitStmt(body)
		}
	case *syntax.StmtLoopJump:
	case *syntax.StmtUnpack:
		for _, variable := range s.Variables {
			x.visitStmt(variable)
		}
		x.visitExpr(s.Expr)
	case *syntax.StmtVariable:
		s.Type = x.expand(s.Type)
		x.visitExpr(s.Expr)
	default:
		panic(fmt.Sprintf("unknown statement %T", s))
	}
}

// visitExprs expands every type written in each of es.
func (x *aliasExpander) visitExprs(es []syntax.Expr) {
	for _, e := range es {
		x.visitExpr(e)
	}
}

// visitExpr expands every type written in e, which may be nil.
func (x *aliasExpander) visitExpr(e syntax.Expr) {
	switch e := e.(type) {
	case nil:
	case *syntax.ExprCast:
		e.Type = x.expand(e.Type)
		x.visitExpr(e.Expr)
	case *syntax.ExprField:
		x.visitExpr(e.Expr)
	case *syntax.ExprFields:
	case *syntax.ExprLambda:
		x.expand(e.Type)
		x.visitStmt(e.Body)
	case *syntax.ExprNAry:
		x.visitExprs(e.Exprs)
	case *syntax.ExprNative:
	case *syntax.ExprNew:
		e.Type = x.expand(e.Type)
		x.visitExpr(e.Size)
		x.visitExpr(e.Tail)
	case *syntax.ExprSymbol:
	case *syntax.ExprType:
		e.Type = x.expand(e.Type)
		x.expandAll(e.TypeArgs)
	default:
		panic(fmt.Sprintf("unknown expression %T", e))
	}
}

// checkAliasUses requires that each alias be accessible where it was written
// and that its arguments there satisfy its bounds. It runs once inheritance is
// known to be acyclic, which judging a bound depends on.
func checkAliasUses(uses []aliasUse) []error {
	var errs []error
	for _, use := range uses {
		at := use.at.OuterDecl()
		if !accessibleFrom(at, use.alias.Modifiers(), use.alias) {
			errs = append(errs, errfAt(at, use.at.Start(), "alias %s is not accessible", use.alias.Name()))
		} else if err := unsatisfiedBound(use.alias.Generics(), use.args); err != nil {
			errs = append(errs, errfAt(at, use.at.Start(), "alias %s: %v", use.alias.Name(), err))
		}
	}
	return errs
}

// unsatisfiedBound reports the first of args that does not satisfy the bound
// of the parameter of generics it is given for. A bound may name the other
// parameters, so it is judged with every argument substituted into it.
func unsatisfiedBound(generics *base.StaticOrderedMap[syntax.GenericArgKey, syntax.GenericBound], args []syntax.Type) error {
	if generics == nil || generics.Len() != len(args) {
		return nil
	}
	bindings := make(map[syntax.GenericArgKey]syntax.Type, len(args))
	for i, entry := range generics.All() {
		bindings[entry.Key] = args[i]
	}

	for i, entry := range generics.All() {
		arg := args[i]
		switch bound := entry.Value.(type) {
		case syntax.GenericUpperBounded:
			for _, b := range bound {
				if b = b.Map(bindings); !arg.IsSubtypeOf(b) {
					return fmt.Errorf("type argument %s for %s is not a subtype of %s", arg, entry.Key.Name, b)
				}
			}
		case syntax.GenericLowerBounded:
			for _, b := range bound {
				if b = b.Map(bindings); !b.IsSubtypeOf(arg) {
					return fmt.Errorf("type argument %s for %s is not a supertype of %s", arg, entry.Key.Name, b)
				}
			}
		}
	}
	return nil
}
