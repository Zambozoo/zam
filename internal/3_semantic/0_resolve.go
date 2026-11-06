package semantic

import (
	"fmt"
	"slices"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// referenceTypeResolverVisitor is a visitor that resolves reference types in the AST.
type referenceTypeResolverVisitor struct {
	// ast is the AST to resolve the references in.
	ast *syntax.AST
	// errs is the accumulated list of errors.
	errs []error
}

// VisitAST visits the AST and resolves the types.
func resolveReferenceTypes(ast *syntax.AST) []error {
	r := &referenceTypeResolverVisitor{
		ast: ast,
	}

	for _, entry := range r.ast.CodeFiles.All() {
		r.VisitFile(entry.Value)
	}

	return r.errs
}

// err records err as having been found where t was written. Every failure this
// pass reports is about a type, and a type carries both the token it starts at
// and the declaration it was written inside.
func (r *referenceTypeResolverVisitor) err(t syntax.Type, err error) {
	r.errs = append(r.errs, errAt(t.OuterDecl(), t.Start(), err))
}

// errf is err over a formatted message.
func (r *referenceTypeResolverVisitor) errf(t syntax.Type, format string, args ...any) {
	r.err(t, fmt.Errorf(format, args...))
}

// VisitFile implements [syntax.FileVisitor].
func (r *referenceTypeResolverVisitor) VisitFile(f *syntax.File) error {
	// A package name may hold more than one file, so every import under every
	// id is visited rather than one per id.
	for _, imports := range f.Imports {
		for _, i := range imports {
			i.Accept(r)
		}
	}
	return f.Decl.Accept(r)
}

// VisitImport implements [syntax.FileVisitor].
func (r *referenceTypeResolverVisitor) VisitImport(i *syntax.Import) error {
	file, _ := r.ast.CodeFiles.Get(*i.Path) // syntax parsing passed, index is valid.
	i.ImportedFile = file
	return nil
}

func (r *referenceTypeResolverVisitor) visitDecl(d syntax.Decl) {
	for _, field := range d.InstanceFields() {
		field.Accept(r)
	}
	for _, field := range d.StaticFields() {
		field.Accept(r)
	}
	for child := range d.Decls() {
		child.Accept(r)
	}
}

func (r *referenceTypeResolverVisitor) visitDeclGeneric(g syntax.DeclGeneric) {
	r.visitDecl(g)
	r.visitGenerics(g.Generics())
}

// visitGenerics resolves the types the bounds of a parameter list are written
// with.
func (r *referenceTypeResolverVisitor) visitGenerics(generics *base.StaticOrderedMap[syntax.GenericArgKey, syntax.GenericBound]) {
	if generics == nil {
		return
	}
	for _, entry := range generics.All() {
		if entry.Value == nil {
			continue
		}
		for _, b := range entry.Value.Bounds() {
			b.Accept(r)
		}
	}
}

func (r *referenceTypeResolverVisitor) visitDeclSubtypeable(s syntax.DeclSubtypeable) {
	r.visitDeclGeneric(s)
	if baseDeclType := s.BaseDeclType(); baseDeclType != nil {
		baseDeclType.Accept(r)
	}
}

// VisitDeclAbstract implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	r.visitDeclSubtypeable(d)
	return nil
}

// VisitDeclClass implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	r.visitDeclSubtypeable(d)
	return nil
}

// VisitDeclEnum implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	r.visitDecl(d)
	return nil
}

// VisitDeclInterface implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	r.visitDeclGeneric(d)
	return nil
}

// VisitDeclNamespace implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	r.visitDecl(d)
	return nil
}

// VisitDeclStruct implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	r.visitDeclGeneric(d)
	return nil
}

// VisitDeclAlias implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitDeclAlias(d *syntax.DeclAlias) error {
	r.visitGenerics(d.Generics())
	d.Type.Accept(r)
	return nil
}

// VisitFieldEnum implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	for _, param := range f.Parameters {
		param.Accept(r)
	}
	if f.Value != nil {
		f.Value.Accept(r)
	}
	return nil
}

// VisitFieldMember implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	f.FieldType().Accept(r)
	if f.Expr != nil {
		f.Expr.Accept(r)
	}
	return nil
}

// VisitFieldMethod implements [syntax.FieldVisitor].
func (r *referenceTypeResolverVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	r.visitGenerics(f.Generics())
	f.FieldType().Accept(r)
	if f.Body != nil {
		f.Body.Accept(r)
	}
	return nil
}

// VisitStmtBlock implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtBlock(s *syntax.StmtBlock) error {
	for _, stmt := range s.Stmts {
		stmt.Accept(r)
	}
	return nil
}

// VisitStmtExpr implements [syntax.StmtVisitor]. A statement owing no value has
// no expression to resolve names in: a return leaving a void method is written
// with nothing after it.
func (r *referenceTypeResolverVisitor) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Expr != nil {
		s.Expr.Accept(r)
	}
	return nil
}

// VisitStmtFor implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtFor(s *syntax.StmtFor) error {
	s.Condition.Accept(r)
	s.Body.Accept(r)
	if s.Step != nil {
		s.Step.Accept(r)
	}
	if s.Else != nil {
		s.Else.Accept(r)
	}
	return nil
}

// VisitStmtSwitch implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtSwitch(s *syntax.StmtSwitch) error {
	s.Expr.Accept(r)
	for _, switchCase := range s.Cases {
		for _, value := range switchCase.Values {
			value.Accept(r)
		}
	}
	for body := range s.Bodies() {
		body.Accept(r)
	}
	return nil
}

// VisitStmtIf implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtIf(s *syntax.StmtIf) error {
	s.Condition.Accept(r)
	s.Then.Accept(r)
	if s.Else != nil {
		s.Else.Accept(r)
	}
	return nil
}

// VisitStmtLabel implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtLabel(s *syntax.StmtLabel) error {
	s.LoopStmt.Accept(r)
	return nil
}

// VisitStmtLoopJump implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtLoopJump(s *syntax.StmtLoopJump) error {
	return nil
}

// VisitStmtVariable implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtVariable(s *syntax.StmtVariable) error {
	s.Type.Accept(r)
	if s.Expr != nil {
		s.Expr.Accept(r)
	}
	return nil
}

// VisitStmtUnpack implements [syntax.StmtVisitor].
func (r *referenceTypeResolverVisitor) VisitStmtUnpack(s *syntax.StmtUnpack) error {
	for _, variable := range s.Variables {
		variable.Accept(r)
	}
	if s.Expr != nil {
		s.Expr.Accept(r)
	}
	return nil
}

// VisitExprCast implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprCast(e *syntax.ExprCast) error {
	e.Type.Accept(r)
	e.Expr.Accept(r)
	return nil
}

// VisitExprField implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprField(e *syntax.ExprField) error {
	e.Expr.Accept(r)
	return nil
}

// VisitExprFields implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprFields(e *syntax.ExprFields) error {
	e.Head.Accept(r)
	return nil
}

// VisitExprLambda implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprLambda(e *syntax.ExprLambda) error {
	e.Type.Accept(r)
	if e.Body != nil {
		e.Body.Accept(r)
	}
	return nil
}

// VisitExprNAry implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprNAry(e *syntax.ExprNAry) error {
	for _, arg := range e.Exprs {
		arg.Accept(r)
	}
	return nil
}

// VisitExprNative implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprNative(e *syntax.ExprNative) error {
	return nil
}

// VisitExprNew implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprNew(e *syntax.ExprNew) error {
	e.Type.Accept(r)
	if e.Size != nil {
		e.Size.Accept(r)
	}
	if e.Tail != nil {
		e.Tail.Accept(r)
	}
	return nil
}

// VisitExprSymbol implements [syntax.ExprVisitor].
func (r *referenceTypeResolverVisitor) VisitExprSymbol(e *syntax.ExprSymbol) error {
	return nil
}

// VisitExprType implements [syntax.ExprVisitor].
//
// A generic class is normally rejected as a type reference, since using it as
// a value requires its parameters be filled in. Field selection through a
// type is different: the Arr in Arr::[string]New names the template a
// static method lives on, not a value, so the reference is resolved without
// the generic check. Each written type argument is resolved on its own.
func (r *referenceTypeResolverVisitor) VisitExprType(e *syntax.ExprType) error {
	if e.Field != "" {
		if tr, ok := e.Type.(*syntax.TypeReference); ok {
			if err := r.visitTypeReference(tr); err != nil {
				r.err(e.Type, err)
			}
			for _, ta := range e.TypeArgs {
				ta.Accept(r)
			}
			return nil
		}
	}
	e.Type.Accept(r)
	for _, ta := range e.TypeArgs {
		ta.Accept(r)
	}
	return nil
}

// VisitTypeArray implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeArray(t *syntax.TypeArray) error {
	t.Type.Accept(r)
	return nil
}

// VisitTypeFunction implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeFunction(t *syntax.TypeFunction) error {
	t.ReturnType.Accept(r)
	for _, param := range t.ParameterTypes {
		param.Accept(r)
	}
	return nil
}

// VisitTypeGeneric implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	var err error
	switch t.Type.Kind() {
	case syntax.TypeKindReference:
		err = r.visitTypeReference(t.Type.(*syntax.TypeReference))
	case syntax.TypeKindTailed:
		err = r.visitTypeReference(t.Type.(*syntax.TypeTailed).Type)
	default:
		panic("unreachable")
	}
	if err != nil {
		r.err(t, err)
		return nil
	}

	if tr, ok := t.Type.(*syntax.TypeReference); ok && tr.Alias() != nil {
		for _, param := range t.ParameterTypes {
			param.Accept(r)
		}
		return nil
	} else if tt, ok := t.Type.(*syntax.TypeTailed); ok && tt.Type.Alias() != nil {
		r.errf(t, "alias %q cannot be tailed", tt.Type.String())
		return nil
	}

	decl := t.Type.Decl()
	genericDecl, ok := decl.(syntax.DeclGeneric)
	if !ok {
		r.errf(t, "declaration %q is not a generic type", t.String())
		return nil
	} else if genericDecl.Generics().Len() != len(t.ParameterTypes) {
		r.errf(t, "generic type %q has %d parameters, but %d were provided", t.String(), genericDecl.Generics().Len(), len(t.ParameterTypes))
		return nil
	}

	_, isTailedType := t.Type.(*syntax.TypeTailed)
	if tailedDecl, isTailedDecl := decl.(syntax.DeclTailable); (isTailedDecl && tailedDecl.IsTailed()) != isTailedType {
		if isTailedType {
			r.errf(t, "declaration %q is a tailed type, but %s was provided", t.String(), decl)
		} else {
			r.errf(t, "declaration %q is not a tailed type, but %s was provided", t.String(), decl)
		}
		return nil
	}

	for _, param := range t.ParameterTypes {
		param.Accept(r)
	}
	return nil
}

// VisitTypeGenericArg implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	return nil
}

// VisitTypePrimitive implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypePrimitive(t *syntax.TypePrimitive) error {
	return nil
}

// nestedDecl walks ids down from d, and answers whether the whole path was
// there. An empty path answers with d, a path of no steps having arrived.
func nestedDecl(d syntax.Decl, ids []string) (syntax.Decl, bool) {
	for _, id := range ids {
		var ok bool
		if d, ok = d.GetDecl(id); !ok {
			return nil, false
		}
	}
	return d, true
}

// setDecl points t at the declaration it was resolved to, which has to be one a
// type can be written of or an alias for one.
func (r *referenceTypeResolverVisitor) setDecl(t *syntax.TypeReference, d syntax.Decl) error {
	if alias, ok := d.(*syntax.DeclAlias); ok {
		t.SetAlias(alias)
	} else if declTypeable, ok := d.(syntax.DeclTypeable); ok {
		t.SetDecl(declTypeable)
	} else {
		return fmt.Errorf("declaration %q is not a typeable", t.String())
	}
	if n := len(t.IdTokens); n > 0 {
		r.ast.References.Add(t.OuterDecl().File(), t.IdTokens[n-1], syntax.TargetOf(d), nil)
	}
	return nil
}

// visitTypeReference resolves a written name to the declaration it stands for.
//
// The search runs innermost outward: every enclosing declaration in turn, and
// then the file's imports. A declaration in scope therefore shadows an import of
// the same name, a name meaning the nearest thing it could mean — the discipline
// the variable and label scopes already follow. Nothing here counts the ids to
// decide what the head is: a head is an import only where nothing nearer
// answered to it, which is why a nested declaration can be named from inside the
// file that wrote it as well as from outside.
//
// An import shadowed this way is reached by renaming it where it is imported,
// there being no other way to write it.
func (r *referenceTypeResolverVisitor) visitTypeReference(t *syntax.TypeReference) error {
	for outerDecl := t.OuterDecl(); outerDecl != nil; outerDecl = outerDecl.OuterDecl() {
		// The head is bound by the nearest rung that has a name for it, either
		// its own — a declaration being named from outside itself looking in —
		// or one of the declarations it holds.
		head, ids := outerDecl, t.Ids
		if outerDecl.Name() == ids[0] {
			ids = ids[1:]
		} else if nested, ok := outerDecl.GetDecl(ids[0]); ok {
			head, ids = nested, ids[1:]
		} else {
			continue
		}

		// Binding the head settles what the rest of the path is looked up in.
		// Reaching further out for a name this rung already answered to would
		// mean a mistyped path silently naming something in another file
		// entirely, so the rest of the path is required to be here.
		decl, ok := nestedDecl(head, ids)
		if !ok {
			return fmt.Errorf("declaration %q not found", t.String())
		}
		return r.setDecl(t, decl)
	}

	// An import names a file and a file names one declaration, so what follows
	// the import has to be that declaration before anything nested inside it
	// can be reached through it. One id may hold more than one import — a
	// package name is the usual case — so the right one is the one whose file
	// declares a top-level type whose name matches the next id.
	if imports, ok := t.OuterDecl().File().Imports[t.Ids[0]]; ok && len(t.Ids) > 1 {
		if decl, err := importedDecl(t, imports, t.Ids[1:], false); err != nil {
			return err
		} else if decl != nil {
			return r.setDecl(t, decl)
		}
	}

	// A single-id reference also resolves against the file's imports when one
	// of them carries a top-level declaration of that name. The dotted form
	// above is the fully-qualified spelling; this is the shorthand that lets a
	// `import "../Err.zam"` or `import std("Err.zam")` be reached as just
	// `Err`. Walking every import rather than keying by id covers both the
	// relative form (whose id is "Err" already) and the package form (whose
	// id is the package name, "std"). An aliased import is reached only by its
	// alias, which is what lets two imports declaring one name be told apart.
	var all []*syntax.Import
	for _, imports := range t.OuterDecl().File().Imports {
		all = append(all, imports...)
	}
	if decl, err := importedDecl(t, all, t.Ids, true); err != nil {
		return err
	} else if decl != nil {
		return r.setDecl(t, decl)
	}

	return fmt.Errorf("declaration %q not found", t.String())
}

// importedDecl finds what ids name among imports, ids[0] being the name of an
// imported file's declaration and the rest a path nested inside it, skipping
// aliased imports when told to. Two imports answering with different
// declarations make the name ambiguous, since which one was meant is for an
// alias to say.
func importedDecl(t *syntax.TypeReference, imports []*syntax.Import, ids []string, skipAliased bool) (syntax.Decl, error) {
	var found syntax.Decl
	for _, imp := range imports {
		if imp.ImportedFile == nil || (skipAliased && imp.Alias != nil) {
			continue
		}
		fileDecl := imp.ImportedFile.Decl
		if fileDecl.Name() != ids[0] {
			continue
		}
		decl, ok := nestedDecl(fileDecl, ids[1:])
		if !ok || decl == found {
			continue
		}
		if found != nil {
			paths := []string{found.File().Path.String(), decl.File().Path.String()}
			slices.Sort(paths)
			return nil, fmt.Errorf("declaration %q is ambiguous between %s and %s; alias one of their imports", t.String(), paths[0], paths[1])
		}
		found = decl
	}
	return found, nil
}

// VisitTypeReference implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeReference(t *syntax.TypeReference) error {
	if err := r.visitTypeReference(t); err != nil {
		r.err(t, err)
		return nil
	} else if t.Alias() != nil {
		return nil
	}

	typeDecl := t.Decl()
	if gd, ok := typeDecl.(syntax.DeclGeneric); ok && gd.Generics() != nil {
		r.errf(t, "declaration %q is a generic type", t.String())
	} else if td, ok := typeDecl.(syntax.DeclTailable); ok && td.IsTailed() {
		r.errf(t, "declaration %q is a tailed type", t.String())
	}
	return nil
}

// VisitTypeTailed implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeTailed(t *syntax.TypeTailed) error {
	if err := r.visitTypeReference(t.Type); err != nil {
		r.err(t, err)
		return nil
	} else if t.Type.Alias() != nil {
		r.errf(t, "alias %q cannot be tailed", t.Type.String())
		return nil
	}

	decl := t.Type.Decl()

	if tailedDecl, ok := decl.(syntax.DeclTailable); !ok || !tailedDecl.IsTailed() {
		r.errf(t, "declaration %q is not a tailed type", t.String())
	} else if genericDecl, ok := decl.(syntax.DeclGeneric); ok && genericDecl.Generics() != nil {
		r.errf(t, "declaration %q is a generic type", t.String())
	}
	return nil
}

// VisitTypeWrapped implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	t.Type.Accept(r)
	return nil
}

// VisitTypeTuple implements [syntax.TypeVisitor].
func (r *referenceTypeResolverVisitor) VisitTypeTuple(t *syntax.TypeTuple) error {
	for _, element := range t.Types {
		element.Accept(r)
	}
	return nil
}
