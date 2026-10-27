package semantic

import syntax "github.com/zambozoo/zam/internal/2_syntax"

// collectGenericTypes records, for every generic class, abstract, and interface
// declaration, the open types it references — a type is "open" when a generic
// argument appears anywhere in it, whether it is the argument itself (T), an
// instantiation naming it (C[T]), or a type built over either (C[T]*). Structs
// are excluded: they do not dynamic dispatch and carry their own type-specific
// logic, so this problem does not apply to them.
//
// The open types are stored on the template. A concrete instantiation resolves
// them by mapping each through its bindings (e.g. C[T] -> C[int]), which is how
// codegen recovers the ids of a concrete's dependent instantiations without the
// runtime having to construct an open type it cannot know the arguments for.
func collectGenericTypes(ast *syntax.AST) {
	c := &genericTypeCollector{}
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Decl.Accept(c)
	}
}

// genericTypeCollector walks a single declaration's type positions — generic
// bounds, base type, fields, and method bodies — and adds every open generic
// type it meets to that declaration's set. It is entered via Accept on the
// target decl and deliberately does not descend into nested declarations, which
// the driver collects independently.
//
// A generic method opens its own open-type set, which the collector swaps the
// decl's for while walking the method's signature and body. The method is a
// scope of its own, resolved against its own bindings at every call site, so
// its open types belong to it rather than to the decl it lives on.
type genericTypeCollector struct {
	target syntax.GenericOwner
}

// enter makes d the declaration being collected into and enumerates d's own
// type, which is open whenever d is generic. Nothing in the source has to name
// C[T] for C to need it: every instance method of C pushes its receiver under
// that type, so it is the one open type a declaration always references whether
// it is written anywhere in the body or not.
//
// The receiver pointer is enumerated for the same reason: `this` is a C* in
// every instance method and in the initializer, so codegen needs that id
// available alongside the declaration's own. Nothing in the source has to name
// C* for it to be read.
func (c *genericTypeCollector) enter(d syntax.DeclGeneric) {
	c.target = d
	declType := d.(syntax.DeclTypeable).DeclType()
	c.record(declType)
	c.record(syntax.NewWrappedType(syntax.TypeKindPointer, declType))
}

func (c *genericTypeCollector) visitBounds(d syntax.DeclGeneric) {
	for _, entry := range d.Generics().All() {
		if entry.Value == nil {
			continue
		}
		for _, bound := range entry.Value.Bounds() {
			bound.Accept(c)
		}
	}
}

func (c *genericTypeCollector) visitFields(d syntax.Decl) {
	for _, field := range d.StaticFields() {
		field.Accept(c)
	}
	for _, field := range d.InstanceFields() {
		field.Accept(c)
	}
}

// visitDecls descends into a declaration's nested declarations, letting the
// single collector traverse the whole tree while it swaps target as it enters
// each generic decl.
func (c *genericTypeCollector) visitDecls(d syntax.Decl) {
	for child := range d.Decls() {
		child.Accept(c)
	}
}

// ----- FieldVisitor -----
//
// The three collected decl kinds walk their own type positions; every other
// declaration is a no-op, since nested decls are collected by the driver.

func (c *genericTypeCollector) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	if d.Generics() != nil {
		previous := c.target
		c.enter(d)
		c.visitBounds(d)
		d.BaseDeclType().Accept(c)
		c.visitFields(d)
		c.target = previous
	}
	c.visitDecls(d)
	return nil
}

func (c *genericTypeCollector) VisitDeclClass(d *syntax.DeclClass) error {
	if d.Generics() != nil {
		previous := c.target
		c.enter(d)
		c.visitBounds(d)
		d.BaseDeclType().Accept(c)
		c.visitFields(d)
		c.target = previous
	}
	c.visitDecls(d)
	return nil
}

func (c *genericTypeCollector) VisitDeclInterface(d *syntax.DeclInterface) error {
	if d.Generics() != nil {
		previous := c.target
		c.enter(d)
		c.visitBounds(d)
		c.visitFields(d)
		c.target = previous
	}
	c.visitDecls(d)
	return nil
}

func (c *genericTypeCollector) VisitDeclEnum(d *syntax.DeclEnum) error {
	c.visitDecls(d)
	return nil
}

func (c *genericTypeCollector) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	c.visitDecls(d)
	return nil
}

func (c *genericTypeCollector) VisitDeclStruct(d *syntax.DeclStruct) error {
	c.visitDecls(d)
	return nil
}

func (c *genericTypeCollector) VisitFieldMember(f *syntax.FieldMember) error {
	f.FieldType().Accept(c)
	if f.Expr != nil {
		f.Expr.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitFieldMethod(f *syntax.FieldMethod) error {
	if f.Generics() != nil {
		previous := c.target
		c.target = f
		defer func() { c.target = previous }()
	}
	f.FieldType().Accept(c)
	if f.Body != nil {
		f.Body.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitFieldEnum(f *syntax.FieldEnum) error {
	f.Type().Accept(c)
	for _, parameter := range f.Parameters {
		parameter.Accept(c)
	}
	if f.Value != nil {
		f.Value.Accept(c)
	}
	return nil
}

// ----- StmtVisitor -----

func (c *genericTypeCollector) VisitStmtLoopJump(s *syntax.StmtLoopJump) error {
	return nil
}

func (c *genericTypeCollector) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Expr != nil {
		s.Expr.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitStmtFor(s *syntax.StmtFor) error {
	s.Condition.Accept(c)
	s.Body.Accept(c)
	if s.Step != nil {
		s.Step.Accept(c)
	}
	if s.Else != nil {
		s.Else.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitStmtIf(s *syntax.StmtIf) error {
	s.Condition.Accept(c)
	s.Then.Accept(c)
	if s.Else != nil {
		s.Else.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitStmtLabel(s *syntax.StmtLabel) error {
	s.LoopStmt.Accept(c)
	return nil
}

func (c *genericTypeCollector) VisitStmtBlock(s *syntax.StmtBlock) error {
	for _, stmt := range s.Stmts {
		stmt.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitStmtVariable(s *syntax.StmtVariable) error {
	s.Type.Accept(c)
	if s.Expr != nil {
		s.Expr.Accept(c)
	}
	return nil
}

// ----- ExprVisitor -----

func (c *genericTypeCollector) VisitExprCast(e *syntax.ExprCast) error {
	e.Type.Accept(c)
	e.Expr.Accept(c)
	return nil
}

func (c *genericTypeCollector) VisitExprField(e *syntax.ExprField) error {
	e.Expr.Accept(c)
	c.recordComputed(e.ComputedType)
	// A method of an inline value with a home is called through the value's
	// address, so the pointer to it is a type the call names too.
	if t := e.Expr.ResolvedType(); t != nil && !syntax.ReachedThroughAddress(t) {
		if _, ok := syntax.StripConst(t).(syntax.TypeResolvable); ok {
			c.recordComputed(syntax.NewWrappedType(syntax.TypeKindPointer, t))
		}
	}
	return nil
}

func (c *genericTypeCollector) VisitExprLambda(e *syntax.ExprLambda) error {
	e.Type.Accept(c)
	if e.Body != nil {
		e.Body.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitExprNAry(e *syntax.ExprNAry) error {
	for _, arg := range e.Exprs {
		arg.Accept(c)
	}
	c.recordComputed(e.ComputedType)
	return nil
}

func (c *genericTypeCollector) VisitExprNative(e *syntax.ExprNative) error {
	c.recordComputed(e.ComputedType)
	return nil
}

func (c *genericTypeCollector) VisitExprNew(e *syntax.ExprNew) error {
	e.Type.Accept(c)
	if e.Size != nil {
		e.Size.Accept(c)
	}
	if e.Tail != nil {
		e.Tail.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitExprSymbol(e *syntax.ExprSymbol) error {
	c.recordComputed(e.ComputedType)
	return nil
}

func (c *genericTypeCollector) VisitExprType(e *syntax.ExprType) error {
	e.Type.Accept(c)
	c.recordComputed(e.ComputedType)
	return nil
}

// ----- TypeVisitor -----

// record enumerates t when it is open, then leaves the recursion to the caller
// so the open types nested inside it are enumerated too. Each one needs an entry
// of its own: a method body holding a Node[T]* names that whole type, not just
// the Node[T] or the T within it.
func (c *genericTypeCollector) record(t syntax.Type) {
	if t.ContainsGenericArg() {
		c.target.GenericTypes().Add(t)
	}
}

// recordComputed enumerates the type an expression evaluates to, and every open
// type nested within it, when that type is open. Expression-result types are
// what codegen pushes for intermediate values (a field access through a
// generic receiver, an indexing into a generic array), so they have to be in
// the enumeration even when they never appear as a written type annotation.
func (c *genericTypeCollector) recordComputed(t syntax.Type) {
	if t == nil {
		return
	}
	t.Accept(c)
}

func (c *genericTypeCollector) VisitTypeArray(t *syntax.TypeArray) error {
	c.record(t)
	t.Type.Accept(c)
	return nil
}

func (c *genericTypeCollector) VisitTypeFunction(t *syntax.TypeFunction) error {
	c.record(t)
	t.ReturnType.Accept(c)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	c.record(t)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(c)
	}
	return nil
}

func (c *genericTypeCollector) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	c.record(t)
	return nil
}

func (c *genericTypeCollector) VisitTypePrimitive(t *syntax.TypePrimitive) error {
	return nil
}

func (c *genericTypeCollector) VisitTypeReference(t *syntax.TypeReference) error {
	return nil
}

func (c *genericTypeCollector) VisitTypeTailed(t *syntax.TypeTailed) error {
	c.record(t)
	t.Type.Accept(c)
	return nil
}

func (c *genericTypeCollector) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	c.record(t)
	// A variadic parameter is written as `T...` but binds inside the body as
	// a pointer `T*` — the array of arguments the caller packed. Codegen asks
	// for the pointer id whenever the body reads the parameter (every
	// identifier push and every stride for args[i]), so the pointer form has
	// to be enumerated here even though the source never writes it. Without
	// this the lookup in genericTypeIndex misses and the runtime walks off
	// the end of the enumeration.
	if t.Kind() == syntax.TypeKindVariadic {
		c.record(syntax.NewWrappedType(syntax.TypeKindPointer, t.Type))
	}
	t.Type.Accept(c)
	return nil
}
