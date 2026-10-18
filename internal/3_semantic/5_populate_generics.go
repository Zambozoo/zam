package semantic

import (
	"sort"
	"strings"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// populateGenericTypes forces creation of every concrete generic instantiation
// the program can reach — full monomorphization.
//
// The problem: resolving source only materializes the instantiations that are
// *written down*. Given
//
//	abstract A1[T] {}
//	class C1[T] { fun void() f = () { var A1[T] a; } }
//	class C2   { fun void() f = () { var C1[int] c; } }
//
// resolving C2 creates the concrete C1[int], but nothing ever creates A1[int]:
// it only appears once C1's body is read under the binding {T -> int}.
//
// The fix is a worklist/fixpoint. Each concrete carries a binding map from the
// generic decl's formal parameters to concrete arguments. We walk the generic
// decl's tree under that binding; every TypeGeneric we meet is substituted via
// Map, and if the result is ground (no remaining TypeGenericArg) we force its
// concrete decl into existence. Newly created concretes go on the worklist and
// are walked in turn. Termination is guaranteed because detectGenericCycles has
// already ruled out infinitely expansive generic recursion, so the set of
// reachable instantiations is finite.
//
// Phase placement: this runs after detectMemberCycles (which resolves concretes
// lazily and does not need the closure) and before collectTypes (which iterates
// each decl's concretes to assign interfaces, and therefore does need the full
// set).
//
// NOTE — required companion change in 2_syntax/6_type.go: TypeGeneric.Map (and
// the other composite Map implementations) currently reuse the receiver's
// ParameterTypes backing slice, so substituting into a shared template node
// mutates that template in place. Monomorphization substitutes the same template
// under many different bindings, so Map MUST allocate a fresh slice rather than
// writing through the alias. We can only call Map here — the semantic package
// cannot construct syntax types itself (their outerDecl fields are unexported) —
// so the non-mutating guarantee has to live in the syntax package.

// genericWork is one pending concrete instantiation: the generic decl or
// method whose tree must be walked, paired with the binding that makes it
// concrete. Exactly one of decl and method is non-nil.
type genericWork struct {
	decl     syntax.Decl
	method   *syntax.FieldMethod
	bindings map[syntax.GenericArgKey]syntax.Type
}

type populateGenericDeclsVisitor struct {
	ast *syntax.AST
	// bindings is the substitution in effect for the tree currently being walked.
	// Empty while walking source decls; set per worklist item during draining.
	bindings map[syntax.GenericArgKey]syntax.Type
	queue    []genericWork
	// walked tracks the (owner, bindings) pairs whose body has been walked, so
	// that materialize's enqueue is idempotent independent of whether the
	// concrete was already created by an earlier pass. Prior passes call
	// TypeGeneric.Decl() — which creates the concrete under the hood — so the
	// isNew check that used to gate enqueueing fires too late and skipped
	// every walk.
	walked map[walkedKey]struct{}
}

// walkedKey identifies one (generic owner, bindings) pair for the walked set.
// The owner answers for the decl or method the body belongs to; the bindings
// are canonicalised to a signature that can key a map, since Go cannot key a
// map by a map.
type walkedKey struct {
	owner    any
	bindings string
}

// bindingsSignature is a stable canonical string for a bindings map. Pass
// iteration is otherwise random, which would make two walks under the same
// substitution key differently in walkedKey.
func bindingsSignature(bindings map[syntax.GenericArgKey]syntax.Type) string {
	keys := make([]syntax.GenericArgKey, 0, len(bindings))
	for k := range bindings {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Name != keys[j].Name {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Owner.Hash() < keys[j].Owner.Hash()
	})
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k.Name)
		sb.WriteByte('=')
		sb.WriteString(bindings[k].String())
		sb.WriteByte(';')
	}
	return sb.String()
}

func populateGenericDecls(ast *syntax.AST) {
	v := &populateGenericDeclsVisitor{
		ast:    ast,
		walked: map[walkedKey]struct{}{},
	}

	// Seed: walk all source decls with no binding. This materializes every
	// directly-written concrete (e.g. C1[int]) and enqueues it.
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Accept(v)
	}

	// Drain: walk each queued concrete's generic template under its binding,
	// which may materialize further concretes (e.g. A1[int] from inside C1).
	// A method work item visits the method's signature and body alone; a decl
	// item visits the decl's whole tree.
	for len(v.queue) > 0 {
		w := v.queue[len(v.queue)-1]
		v.queue = v.queue[:len(v.queue)-1]
		v.bindings = w.bindings
		switch {
		case w.method != nil:
			w.method.Accept(v)
		case w.decl != nil:
			w.decl.Accept(v)
		}
	}
}

// materialize forces the concrete decl for a ground instantiation into existence
// and, if it is newly created, enqueues its template for walking under the
// binding that produced it.
func (v *populateGenericDeclsVisitor) materialize(t *syntax.TypeGeneric) {
	gd, ok := t.Type.Decl().(syntax.DeclGeneric)
	if !ok || gd.Generics() == nil {
		return
	}

	// GetConcreteDecl creates the concrete the first time it is asked, which
	// means earlier passes that call TypeGeneric.Decl() have already created
	// every concrete the source directly names. isNew only tells us whether
	// the concrete existed before this call — the real question is whether
	// we have walked its body under its bindings, which the walked set below
	// tracks.
	gd.GetConcreteDecl(t)

	bindings := make(map[syntax.GenericArgKey]syntax.Type, len(t.ParameterTypes))
	i := 0
	for _, entry := range gd.Generics().All() {
		bindings[entry.Key] = t.ParameterTypes[i]
		i++
	}

	key := walkedKey{owner: gd, bindings: bindingsSignature(bindings)}
	if _, done := v.walked[key]; done {
		return
	}
	v.walked[key] = struct{}{}
	v.queue = append(v.queue, genericWork{decl: gd, bindings: bindings})
}

// walkDecl visits every type-bearing position of a declaration: generic bounds,
// its base/super type, its fields (and their bodies), and its nested decls.
func (v *populateGenericDeclsVisitor) walkDecl(d syntax.Decl) {
	if gd, ok := d.(syntax.DeclGeneric); ok && gd.Generics() != nil {
		for _, entry := range gd.Generics().All() {
			if entry.Value == nil {
				continue
			}
			for _, bound := range entry.Value.Bounds() {
				bound.Accept(v)
			}
		}
	}
	if sd, ok := d.(syntax.DeclSubtypeable); ok && sd.BaseDeclType() != nil {
		sd.BaseDeclType().Accept(v)
	}
	for _, field := range d.StaticFields() {
		field.Accept(v)
	}
	for _, field := range d.InstanceFields() {
		field.Accept(v)
	}
	for child := range d.Decls() {
		child.Accept(v)
	}
}

// ----- FileVisitor -----

func (v *populateGenericDeclsVisitor) VisitFile(file *syntax.File) error {
	return file.Decl.Accept(v)
}

func (v *populateGenericDeclsVisitor) VisitImport(importDecl *syntax.Import) error {
	return nil
}

// ----- FieldVisitor: declarations -----

func (v *populateGenericDeclsVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	v.walkDecl(d)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	v.walkDecl(d)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	v.walkDecl(d)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	v.walkDecl(d)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	v.walkDecl(d)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	v.walkDecl(d)
	return nil
}

// ----- FieldVisitor: fields -----

func (v *populateGenericDeclsVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	f.FieldType().Accept(v)
	if f.Expr != nil {
		f.Expr.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	f.FieldType().Accept(v)
	if f.Body != nil {
		f.Body.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	f.Type().Accept(v)
	for _, parameter := range f.Parameters {
		parameter.Accept(v)
	}
	if f.Value != nil {
		f.Value.Accept(v)
	}
	return nil
}

// ----- StmtVisitor -----

func (v *populateGenericDeclsVisitor) VisitStmtLoopJump(s *syntax.StmtLoopJump) error {
	return nil
}

func (v *populateGenericDeclsVisitor) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Expr != nil {
		s.Expr.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitStmtFor(s *syntax.StmtFor) error {
	s.Condition.Accept(v)
	s.Body.Accept(v)
	if s.Step != nil {
		s.Step.Accept(v)
	}
	if s.Else != nil {
		s.Else.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitStmtIf(s *syntax.StmtIf) error {
	s.Condition.Accept(v)
	s.Then.Accept(v)
	if s.Else != nil {
		s.Else.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitStmtLabel(s *syntax.StmtLabel) error {
	s.LoopStmt.Accept(v)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitStmtBlock(s *syntax.StmtBlock) error {
	for _, stmt := range s.Stmts {
		stmt.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitStmtVariable(s *syntax.StmtVariable) error {
	s.Type.Accept(v)
	if s.Expr != nil {
		s.Expr.Accept(v)
	}
	return nil
}

// ----- ExprVisitor -----

func (v *populateGenericDeclsVisitor) VisitExprCast(e *syntax.ExprCast) error {
	e.Type.Accept(v)
	e.Expr.Accept(v)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitExprField(e *syntax.ExprField) error {
	e.Expr.Accept(v)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitExprLambda(e *syntax.ExprLambda) error {
	e.Type.Accept(v)
	if e.Body != nil {
		e.Body.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitExprNAry(e *syntax.ExprNAry) error {
	for _, arg := range e.Exprs {
		arg.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitExprNative(e *syntax.ExprNative) error {
	return nil
}

func (v *populateGenericDeclsVisitor) VisitExprNew(e *syntax.ExprNew) error {
	e.Type.Accept(v)
	if e.Size != nil {
		e.Size.Accept(v)
	}
	if e.Tail != nil {
		e.Tail.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitExprSymbol(e *syntax.ExprSymbol) error {
	return nil
}

func (v *populateGenericDeclsVisitor) VisitExprType(e *syntax.ExprType) error {
	e.Type.Accept(v)
	if len(e.TypeArgs) > 0 {
		mapped := make([]syntax.Type, len(e.TypeArgs))
		ground := true
		for i, t := range e.TypeArgs {
			mapped[i] = t.Map(v.bindings)
			if mapped[i].ContainsGenericArg() {
				ground = false
			}
		}
		for _, t := range mapped {
			t.Accept(v)
		}
		if ground {
			v.materializeMethod(e, mapped)
		}
	}
	return nil
}

// materializeMethod forces the concrete instantiation of a generic static
// method for the given ground type arguments into existence and, if new,
// enqueues its body for walking under the resolved bindings so any nested
// generic use within is materialized too.
func (v *populateGenericDeclsVisitor) materializeMethod(e *syntax.ExprType, typeArgs []syntax.Type) {
	tr, ok := e.Type.(syntax.TypeResolvable)
	if !ok {
		return
	}
	fi, ok := tr.Decl().GetStaticField(e.Field)
	if !ok {
		return
	}
	method, ok := fi.(*syntax.FieldMethod)
	if !ok || method.Generics() == nil {
		return
	}
	concrete, _ := method.GetConcreteMethod(typeArgs)
	if concrete == nil {
		return
	}
	e.Concrete = concrete

	// Enqueue the walk under the same walked-set guard materialize uses. isNew
	// on the concrete only reports whether it existed, not whether the body
	// has been seen under these bindings, and both are needed before the
	// method's registry entry can be filled from its concretes.
	key := walkedKey{owner: method, bindings: bindingsSignature(concrete.Bindings())}
	if _, done := v.walked[key]; done {
		return
	}
	v.walked[key] = struct{}{}
	v.queue = append(v.queue, genericWork{method: method, bindings: concrete.Bindings()})
}

// ----- TypeVisitor -----

func (v *populateGenericDeclsVisitor) VisitTypeArray(t *syntax.TypeArray) error {
	t.Type.Accept(v)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitTypeFunction(t *syntax.TypeFunction) error {
	t.ReturnType.Accept(v)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(v)
	}
	return nil
}

// VisitTypeGeneric is the heart of the pass: substitute the current binding into
// the instantiation, materialize it when ground, then recurse into the arguments
// so nested instantiations are materialized too.
func (v *populateGenericDeclsVisitor) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	if applied, ok := t.Map(v.bindings).(*syntax.TypeGeneric); ok && !applied.ContainsGenericArg() {
		v.materialize(applied)
	}
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(v)
	}
	return nil
}

func (v *populateGenericDeclsVisitor) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	return nil
}

func (v *populateGenericDeclsVisitor) VisitTypePrimitive(t *syntax.TypePrimitive) error {
	return nil
}

func (v *populateGenericDeclsVisitor) VisitTypeReference(t *syntax.TypeReference) error {
	return nil
}

func (v *populateGenericDeclsVisitor) VisitTypeTailed(t *syntax.TypeTailed) error {
	t.Type.Accept(v)
	return nil
}

func (v *populateGenericDeclsVisitor) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	t.Type.Accept(v)
	return nil
}
