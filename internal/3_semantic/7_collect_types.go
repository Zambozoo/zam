package semantic

import syntax "github.com/zambozoo/zam/internal/2_syntax"

// collectTypes gathers every ground (fully concrete) function type and
// thread type argument the program uses into the AST's FunctionTypes and
// ThreadTypes sets.
//
// Function and thread types cannot be read straight off the source tree: inside
// a generic decl every type is written against the formal parameters (e.g.
// fun T() f, thread[T]), so it is open, not a real type. To ground them we walk
// the generic template once per concrete instantiation, holding that concrete on
// the collector and substituting its binding into every type we meet via
// MapType. A concrete only maps its field *types*, not their bodies or
// expressions, so we drive the substitution ourselves rather than reading the
// concrete's fields — that way types buried in method bodies get grounded too.
// Non-generic decls are already ground and are walked with no concrete in effect.
//
// Static fields cannot reference generic args, so they are always ground and are
// walked once, outside the per-concrete loop.
//
// Same Map caveat as populateGenericTypes: substitution must not mutate the
// shared template node (see the note in populate_generics.go).
func collectTypes(ast *syntax.AST) {
	c := &functionTypeCollector{
		functionTypes: syntax.NewTypeSet(),
		threadTypes:   syntax.NewTypeSet(),
		pointerTypes:  syntax.NewTypeSet(),
	}
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Decl.Accept(c)
	}
	ast.FunctionTypes = *c.functionTypes
	ast.ThreadTypes = *c.threadTypes
	ast.PointerTypes = *c.pointerTypes
}

type functionTypeCollector struct {
	functionTypes *syntax.TypeSet
	threadTypes   *syntax.TypeSet
	pointerTypes  *syntax.TypeSet
	// concrete is the instantiation whose binding is applied to every type in the
	// tree currently being walked, or nil while walking ground types (a
	// non-generic decl, or any static field).
	concrete syntax.DeclConcrete
	// methodBindings is the extra substitution a generic method concrete adds
	// on top of concrete's binding. A static method's own T is bound only at
	// a call-site concrete, not by the decl concrete, so each method concrete
	// contributes its own one while its signature and body are being walked.
	methodBindings map[syntax.GenericArgKey]syntax.Type
}

// collectDecl walks a declaration's field types and bodies. Static fields are
// ground and walked once. Instance fields of a generic decl are walked once per
// concrete, with that concrete set so its binding is applied; a non-generic
// decl's instance fields are walked directly. Nested decls are handled by the
// VisitDeclX methods.
func (c *functionTypeCollector) collectDecl(d syntax.Decl) {
	c.concrete = nil
	for _, field := range d.StaticFields() {
		field.Accept(c)
	}

	if gd, ok := d.(syntax.DeclGeneric); ok && gd.Generics() != nil {
		for _, entry := range gd.Concretes().All() {
			if cd, ok := entry.Value.(syntax.DeclConcrete); ok {
				c.concrete = cd
				c.walkInstanceFields(d)
			}
		}
		c.concrete = nil
		return
	}
	c.walkInstanceFields(d)
}

func (c *functionTypeCollector) walkInstanceFields(d syntax.Decl) {
	c.collectReceiverType(d)
	for _, field := range d.InstanceFields() {
		field.Accept(c)
	}
}

// collectReceiverType records the pointer a receiver slot holds. Nothing else
// would record it, the way nothing but [functionTypeCollector.VisitExprNew]
// records the pointer an allocation evaluates to: a receiver is a pointer
// whatever the declaration's width, and a declaration is reached through one in
// every instance method and in the initializer every typeable one gets, whether
// or not the program ever writes T*.
//
// A namespace has no instances and no instance methods, so there is no receiver
// to record. It is DeclTypeable so that type(NS).staticField resolves, but
// nothing at runtime holds a pointer to a namespace to be collected here.
func (c *functionTypeCollector) collectReceiverType(d syntax.Decl) {
	if d.Kind() == syntax.KindNamespace {
		return
	}
	typeable, ok := d.(syntax.DeclTypeable)
	if !ok {
		return
	}
	syntax.NewWrappedType(syntax.TypeKindPointer, typeable.DeclType()).Accept(c)
}

// apply substitutes the current concrete's binding into t, or returns t
// unchanged when walking a ground tree. A method-concrete binding, when one
// is in effect, is applied after the decl concrete's, so the method's own T
// resolves on top of the enclosing class's.
func (c *functionTypeCollector) apply(t syntax.Type) syntax.Type {
	if c.concrete != nil {
		t = c.concrete.MapType(t)
	}
	if c.methodBindings != nil {
		t = t.Map(c.methodBindings)
	}
	return t
}

// ----- FieldVisitor: declarations -----

func (c *functionTypeCollector) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	c.collectDecl(d)
	c.visitDecls(d)
	return nil
}

func (c *functionTypeCollector) VisitDeclClass(d *syntax.DeclClass) error {
	c.collectDecl(d)
	c.visitDecls(d)
	return nil
}

func (c *functionTypeCollector) VisitDeclInterface(d *syntax.DeclInterface) error {
	c.collectDecl(d)
	c.visitDecls(d)
	return nil
}

func (c *functionTypeCollector) VisitDeclEnum(d *syntax.DeclEnum) error {
	c.collectDecl(d)
	c.visitDecls(d)
	return nil
}

func (c *functionTypeCollector) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	c.collectDecl(d)
	c.visitDecls(d)
	return nil
}

func (c *functionTypeCollector) VisitDeclStruct(d *syntax.DeclStruct) error {
	c.collectDecl(d)
	c.visitDecls(d)
	return nil
}

func (c *functionTypeCollector) visitDecls(d syntax.Decl) {
	for child := range d.Decls() {
		child.Accept(c)
	}
}

// ----- FieldVisitor: fields -----

func (c *functionTypeCollector) VisitFieldMember(f *syntax.FieldMember) error {
	f.FieldType().Accept(c)
	if f.Expr != nil {
		f.Expr.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitFieldMethod(f *syntax.FieldMethod) error {
	if f.Generics() != nil {
		for _, concrete := range f.Concretes() {
			outer := c.methodBindings
			c.methodBindings = concrete.Bindings()
			f.FieldType().Accept(c)
			if f.Body != nil {
				f.Body.Accept(c)
			}
			c.methodBindings = outer
		}
		return nil
	}
	f.FieldType().Accept(c)
	if f.Body != nil {
		f.Body.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitFieldEnum(f *syntax.FieldEnum) error {
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

func (c *functionTypeCollector) VisitStmtLoopJump(s *syntax.StmtLoopJump) error {
	return nil
}

func (c *functionTypeCollector) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Expr != nil {
		s.Expr.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitStmtFor(s *syntax.StmtFor) error {
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

func (c *functionTypeCollector) VisitStmtIf(s *syntax.StmtIf) error {
	s.Condition.Accept(c)
	s.Then.Accept(c)
	if s.Else != nil {
		s.Else.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitStmtLabel(s *syntax.StmtLabel) error {
	s.LoopStmt.Accept(c)
	return nil
}

func (c *functionTypeCollector) VisitStmtBlock(s *syntax.StmtBlock) error {
	for _, stmt := range s.Stmts {
		stmt.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitStmtVariable(s *syntax.StmtVariable) error {
	s.Type.Accept(c)
	if s.Expr != nil {
		s.Expr.Accept(c)
	}
	return nil
}

// ----- ExprVisitor -----

func (c *functionTypeCollector) VisitExprCast(e *syntax.ExprCast) error {
	e.Type.Accept(c)
	e.Expr.Accept(c)
	return nil
}

func (c *functionTypeCollector) VisitExprField(e *syntax.ExprField) error {
	e.Expr.Accept(c)
	return nil
}

func (c *functionTypeCollector) VisitExprLambda(e *syntax.ExprLambda) error {
	e.Type.Accept(c)
	if e.Body != nil {
		e.Body.Accept(c)
	}
	return nil
}

// VisitExprNAry walks the operands, then the type the expression resolved to:
// an operator can arrive at a type nothing in the source spells, as taking an
// address does, and as indexing an array does by decaying it to a pointer.
func (c *functionTypeCollector) VisitExprNAry(e *syntax.ExprNAry) error {
	for _, arg := range e.Exprs {
		arg.Accept(c)
	}
	if t := e.ResolvedType(); t != nil {
		t.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitExprNative(e *syntax.ExprNative) error {
	return nil
}

// VisitExprNew walks the type allocated and then the pointer to it that the
// expression evaluates to. Nothing else would record that pointer: a program may
// allocate a type it never writes a pointer to, and the id of the pointer is
// asked for all the same by whatever the allocation is stored into.
func (c *functionTypeCollector) VisitExprNew(e *syntax.ExprNew) error {
	e.Type.Accept(c)
	e.ResolvedType().Accept(c)
	if e.Size != nil {
		e.Size.Accept(c)
	}
	if e.Tail != nil {
		e.Tail.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitExprSymbol(e *syntax.ExprSymbol) error {
	return nil
}

func (c *functionTypeCollector) VisitExprType(e *syntax.ExprType) error {
	e.Type.Accept(c)
	return nil
}

// ----- TypeVisitor -----

func (c *functionTypeCollector) VisitTypeArray(t *syntax.TypeArray) error {
	t.Type.Accept(c)
	return nil
}

// VisitTypeFunction records the signature — substituted through the current
// concrete's binding when one is in effect — if it is ground, then recurses so
// function types nested in the return or parameters are recorded too.
func (c *functionTypeCollector) VisitTypeFunction(t *syntax.TypeFunction) error {
	if applied := c.apply(t); !applied.ContainsGenericArg() {
		c.functionTypes.Add(applied)
	}
	t.ReturnType.Accept(c)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(c)
	}
	return nil
}

func (c *functionTypeCollector) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	return nil
}

func (c *functionTypeCollector) VisitTypePrimitive(t *syntax.TypePrimitive) error {
	return nil
}

func (c *functionTypeCollector) VisitTypeReference(t *syntax.TypeReference) error {
	return nil
}

func (c *functionTypeCollector) VisitTypeTailed(t *syntax.TypeTailed) error {
	t.Type.Accept(c)
	return nil
}

// VisitTypeWrapped records a thread's type argument, or a pointer type itself —
// substituted through the current concrete's binding when one is in effect — if
// it is ground, then recurses into the wrapped type.
//
// A variadic records a pointer too, because that is what its slot holds: the
// arguments are packed before the call and the frame is handed a reference to
// them. Nothing else would record it, since a signature that takes T... is a
// signature that never writes T*, and the type list emitted for that signature
// asks for T* all the same.
func (c *functionTypeCollector) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	switch t.Kind() {
	case syntax.TypeKindThread:
		if applied := c.apply(t.Type); !applied.ContainsGenericArg() {
			c.threadTypes.Add(applied)
		}
	case syntax.TypeKindPointer, syntax.TypeKindVariadic:
		if applied := c.apply(syntax.ParameterBindingType(t)); !applied.ContainsGenericArg() {
			c.pointerTypes.Add(applied)
		}
	}
	t.Type.Accept(c)
	return nil
}
