package semantic

import (
	"iter"
	"slices"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

type (
	staticSemanticsValidatorVisitor struct {
		ast *syntax.AST

		expectedReturnType syntax.Type
		lastExprType       syntax.Type
		currentDecl        syntax.Decl
		// currentStart is the token the node being validated begins at, which
		// together with currentDecl is what says where an error was found. It is
		// tracked rather than passed because nearly every check here reports about
		// the node it is already visiting.
		currentStart *syntax.Token
		// currentStaticField is the static field being validated, and is nil
		// while an instance field is. A generic argument is bound by the
		// instance a field is reached through, so this is what says there is no
		// instance to bind one.
		currentStaticField syntax.Field

		variableScopes base.Stack[map[string]binding]
		labelScopes    base.Stack[map[string]syntax.Stmt]
		// pendingLabel is a label written down but not yet claimed by the loop it
		// names. The loop may be nested in the block its initializer was hoisted
		// into rather than being the labelled statement itself, so the label is
		// left for the loop to take, the way codegen takes it.
		pendingLabel string
		// lambdas are the lambdas whose bodies are being validated, innermost
		// last. A name resolved from outside one of them is a name it closes
		// over.
		lambdas base.Stack[*lambdaContext]

		errs []error
	}

	// lambdaContext is a lambda whose body is being validated, and what that
	// body has been found to close over so far.
	lambdaContext struct {
		lambda *syntax.ExprLambda
		// depth is how many variable scopes stood open when the body was
		// entered, which is the mark a resolution has to reach past to have
		// come from outside the lambda.
		depth int
		// captured is what has already been recorded, so a name closed over
		// twice is carried once.
		captured map[string]struct{}
	}

	// binding is what a name in scope stands for. Resolving a name is the same
	// search whatever it names, but what it resolves to is what says how it may
	// be used: only a local lies in the frame the name was written in, so only a
	// local is closed over by a lambda reaching out for it, and only an instance
	// field needs a receiver to be reached through.
	binding interface {
		// bindingType is the type an expression naming the binding comes to.
		bindingType() syntax.Type
	}

	// node is anything an error can be reported at. Every kind of node carries
	// the token it begins at, and nothing else about one is needed to say where
	// it was written.
	node interface{ Start() *syntax.Token }

	// localBinding is a parameter, a capture or a declared variable.
	localBinding struct{ typ syntax.Type }

	// staticBinding is a static of the declaration the name was written in or
	// of one that declaration is nested inside.
	staticBinding struct{ typ syntax.Type }

	// instanceBinding is a field of the declaration the name was written in,
	// inherited ones among them. Only that declaration contributes these: an
	// enclosing declaration's instance is not the one the frame was entered on,
	// so there is nothing to reach one of its fields through.
	//
	// isMethod separates the two kinds, because only a member lies in the
	// object: a method is closed over the receiver rather than kept in it, so
	// naming one builds a closure that was nowhere until it was built.
	instanceBinding struct {
		typ      syntax.Type
		isMethod bool
	}
)

func (b localBinding) bindingType() syntax.Type    { return b.typ }
func (b staticBinding) bindingType() syntax.Type   { return b.typ }
func (b instanceBinding) bindingType() syntax.Type { return b.typ }

// validateStaticSemantics validates the static semantics of the AST.
// This includes type checking, scope checking, access checking, and inheritance validation.
func validateStaticSemantics(ast *syntax.AST) []error {
	v := &staticSemanticsValidatorVisitor{
		ast: ast,
	}
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Decl.Accept(v)
	}

	// The arguments are the run of strings the program was invoked with, which
	// is what a variadic says and a pointer only implies. The count still comes
	// separately: a variadic conveys no length, so nothing else would say where
	// the arguments stop.
	mainType := &syntax.TypeFunction{
		ReturnType: syntax.TypeVoid,
		ParameterTypes: []syntax.Type{
			syntax.TypeUint,
			syntax.NewWrappedType(syntax.TypeKindVariadic, syntax.TypeString),
		},
	}

	// This runs outside any visit, so it says for itself where it is looking: the
	// main declaration, which is the one thing both complaints are about.
	defer v.at(ast.MainCodeDecl)()
	v.currentDecl = ast.MainCodeDecl

	mainMethod, ok := ast.MainCodeDecl.GetStaticMethod(base.MainMethodName)
	if !ok {
		v.errf("main method not found in main code declaration %s", ast.MainCodeDecl.Name())
	} else if !mainMethod.FieldType().Equals(mainType) {
		v.errf("main method %s.%s is of type %q, not %q", ast.MainCodeDecl.Name(), base.MainMethodName, mainMethod.FieldType().String(), mainType.String())
	}
	return v.errs
}

func (v *staticSemanticsValidatorVisitor) VisitFile(f *syntax.File) error {
	f.Decl.Accept(v)
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitImport(i *syntax.Import) error {
	return nil
}

// visitDecl validates every field a declaration holds, with the declaration's
// statics in scope throughout. A nested declaration is validated from here too,
// under the scope this pushed, which is what makes an enclosing declaration's
// statics nameable from within a nested one — and shadowable there, the search
// running innermost outward like any other.
//
// Only statics are put in scope here. An instance field needs an instance to be
// reached through, and the only one there is is the receiver of the frame being
// validated, so those are put in scope by the field visitors instead.
func (v *staticSemanticsValidatorVisitor) visitDecl(d syntax.Decl) {
	oldDecl := v.currentDecl
	v.currentDecl = d
	v.variableScopes.Push(declStaticScope(d))
	for _, field := range d.StaticFields() {
		field.Accept(v)
	}
	for _, field := range d.InstanceFields() {
		field.Accept(v)
	}
	for child := range d.Decls() {
		child.Accept(v)
	}
	v.variableScopes.Pop()
	v.currentDecl = oldDecl
}

// declStaticScope binds the names a declaration's statics are written under. A
// nested declaration is a static field too, but it names a type rather than a
// value, so it has no type to be bound to and is left out.
func declStaticScope(d syntax.Decl) map[string]binding {
	scope := map[string]binding{}
	for name, field := range d.StaticFields() {
		indexable, ok := field.(syntax.FieldIndexable)
		if !ok {
			continue
		}
		if t, ok := staticFieldType(indexable); ok {
			scope[name] = staticBinding{typ: t}
		}
	}
	return scope
}

// declInstanceScope binds the names a declaration's instance fields are written
// under, inherited ones among them, since a field reached through the receiver
// is reached the same way however far up it was declared.
func declInstanceScope(d syntax.Decl) map[string]binding {
	scope := map[string]binding{}
	for _, entry := range d.AllInstanceFields().All() {
		if typeable, ok := entry.Value.(syntax.FieldTypeable); ok {
			_, isMethod := entry.Value.(*syntax.FieldMethod)
			scope[entry.Key] = instanceBinding{typ: typeable.FieldType(), isMethod: isMethod}
		}
	}
	return scope
}

func baseDecls(d syntax.DeclSubtypeable) iter.Seq[syntax.Decl] {
	return func(yield func(syntax.Decl) bool) {
		for ancestor := d.BaseDeclType(); ancestor != nil; {
			decl := ancestor.Type.(syntax.TypeResolvable).Decl()
			if !yield(decl) {
				return
			}

			inheritable, ok := decl.(syntax.DeclSubtypeable)
			if !ok {
				break
			}
			ancestor = inheritable.BaseDeclType()
		}
	}
}

// validateBaseDecl reports whether the base clause names something the
// declaration may descend from. A class descends from a class or an abstract;
// an abstract descends only from an abstract, an abstract being what is left
// unanswered rather than something a class could complete from below.
//
// An interface is never named here, and this is the one case worth an error of
// its own, because a base clause is the natural place to reach for and says
// nothing: what a declaration implements is read off the methods it has. It
// also costs, the slot being the one std.Object stands in, and a declaration
// that loses Object loses the definitions of the methods every object answers.
func (v *staticSemanticsValidatorVisitor) validateBaseDecl(d syntax.DeclSubtypeable) bool {
	baseType := d.BaseDeclType()
	if baseType == nil {
		return true
	}
	resolvable, ok := baseType.Type.(syntax.TypeResolvable)
	if !ok {
		return true
	}

	baseDecl := resolvable.Decl()
	switch baseDecl.Kind() {
	case syntax.KindAbstract:
		return true
	case syntax.KindClass:
		if d.Kind() == syntax.KindClass {
			return true
		}
	}

	expected := "a class or an abstract"
	if d.Kind() == syntax.KindAbstract {
		expected = "an abstract"
	}
	if baseDecl.Kind() == syntax.KindInterface {
		v.errf("base of %s must be %s; %s is an interface, which is implemented by having its methods rather than by naming it",
			d.Name(), expected, baseDecl.Name())
		return false
	}
	v.errf("base of %s must be %s; %s is declared %s",
		d.Name(), expected, baseDecl.Name(), baseDecl.Kind())
	return false
}

func (v *staticSemanticsValidatorVisitor) visitDeclSubtypeable(d syntax.DeclSubtypeable) bool {
	v.visitDecl(d)

	// A bad base makes everything read off the base chain meaningless, so the
	// checks below are left unrun rather than made to report a second time what
	// naming the wrong base already accounts for.
	if !v.validateBaseDecl(d) {
		return false
	}

	// Every method that overrides a base method must be a subtype of it.
	for base := range baseDecls(d) {
		for name, parentMethod := range base.InstanceMethods() {
			childMethod, ok := d.GetInstanceMethod(name)
			if !ok {
				continue
			}
			if !childMethod.FieldType().IsSubtypeOf(parentMethod.FieldType()) {
				v.errf(
					"%s.%s does not override %s.%s with a subtype",
					d.Name(), name, base.Name(), name)
			}
		}
	}
	return true
}

func (v *staticSemanticsValidatorVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	defer v.at(d)()
	v.visitDeclSubtypeable(d)
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	defer v.at(d)()
	if !v.visitDeclSubtypeable(d) {
		return nil
	}

	// A concrete class must override every inherited virtual method.
	effective := base.NewStaticOrderedMap[string, *syntax.FieldMethod]()
	for name, method := range d.InstanceMethods() {
		effective.Set(name, method)
	}
	for b := range baseDecls(d) {
		for name, method := range b.InstanceMethods() {
			if _, i := effective.Get(name); i < 0 {
				effective.Set(name, method)
			}
		}
	}

	for _, entry := range effective.All() {
		if entry.Value.Modifiers().Has(syntax.ModifierVirtual) {
			v.errf(
				"class %s does not override virtual method %q", d.Name(), entry.Key)
		}
	}
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	defer v.at(d)()
	v.visitDecl(d)
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	defer v.at(d)()
	v.visitDecl(d)
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	defer v.at(d)()
	v.visitDecl(d)
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	defer v.at(d)()
	v.visitDecl(d)
	return nil
}

// VisitFieldEnum implements [syntax.FieldVisitor]. An enum value is one instance
// of the enum it is written in, and the method the value names is run over that
// instance to set it up: it is called on the slot the value already lies in and
// works through the receiver, so what it takes is checked against what the value
// was written with and what it returns is not checked at all. A value naming no
// method is the instance its members default to and asks nothing. A value of an
// enum backed by a primitive is written with what it is backed by, which is
// checked against that primitive.
func (v *staticSemanticsValidatorVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	defer v.at(f)()
	outerDecl := f.OuterDecl().(*syntax.DeclEnum)

	if f.Value != nil {
		primitive := outerDecl.Primitive()
		if err := f.Value.Accept(v); err != nil {
			return nil
		}
		if !v.lastExprType.IsSubtypeOf(initTarget(primitive)) {
			v.errf("enum value %s.%s is not a subtype of %s", outerDecl.Name(), f.Name(), primitive.String())
		}
		return nil
	}

	if f.Init == nil {
		return nil
	}

	initMethod, ok := outerDecl.GetInstanceMethod(*f.Init)
	if !ok {
		v.errf("missing init method %s.%s for enum field %s", outerDecl.Name(), *f.Init, f.Name())
		return nil
	}

	tf := initMethod.FieldType().(*syntax.TypeFunction)
	if len(tf.ParameterTypes) != len(f.Parameters) {
		v.errf("init method %s.%s has %d parameters, but enum field %s.%s has %d parameters", outerDecl.Name(), *f.Init, len(tf.ParameterTypes), outerDecl.Name(), f.Name(), len(f.Parameters))
		return nil
	}

	for i, paramType := range tf.ParameterTypes {
		if err := f.Parameters[i].Accept(v); err != nil {
			return nil
		}

		if !v.lastExprType.IsSubtypeOf(initTarget(paramType)) {
			v.errf("init method %s.%s parameter is not a subtype of %s", outerDecl.Name(), *f.Init, paramType.String())
		}
	}
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	defer v.at(f)()
	v.currentStaticField = staticField(f)
	defer func() { v.currentStaticField = nil }()

	v.requireDeclarable(f.FieldType(), "member", f.OuterDecl().Name()+"."+f.Name())
	f.FieldType().Accept(v)
	if f.Expr != nil {
		v.variableScopes.Push(declInstanceScope(f.OuterDecl()))
		err := f.Expr.Accept(v)
		v.variableScopes.Pop()
		if err != nil {
			return nil
		}

		if !v.lastExprType.IsSubtypeOf(initTarget(f.FieldType())) {
			v.errf("field %s.%s expression is not a subtype of %s", f.OuterDecl().Name(), f.Name(), f.FieldType().String())
		}
	}
	return nil
}

func (v *staticSemanticsValidatorVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	defer v.at(f)()
	v.currentStaticField = staticField(f)
	defer func() { v.currentStaticField = nil }()

	v.requireDeclarable(f.FieldType(), "method", f.OuterDecl().Name()+"."+f.Name())
	f.FieldType().Accept(v)
	if f.Body != nil {
		ft := f.FieldType().(*syntax.TypeFunction)
		oldExpectedReturnType := v.expectedReturnType
		v.expectedReturnType = ft.ReturnType
		v.variableScopes.Push(declInstanceScope(f.OuterDecl()))
		v.variableScopes.Push(parameterScope(f.Parameters, ft.ParameterTypes))
		f.Body.Accept(v)
		v.variableScopes.Pop()
		v.variableScopes.Pop()
		if ft.ReturnType.Kind() != syntax.TypeKindVoid && !terminates(f.Body) {
			v.errf("method %s.%s can be left without returning a %s",
				f.OuterDecl().Name(), f.Name(), ft.ReturnType.String())
		}
		v.expectedReturnType = oldExpectedReturnType
	}
	return nil
}

// VisitStmtBlock implements [syntax.StmtVisitor].
func (v *staticSemanticsValidatorVisitor) VisitStmtBlock(s *syntax.StmtBlock) error {
	defer v.at(s)()
	v.variableScopes.Push(map[string]binding{})
	for _, stmt := range s.Stmts {
		stmt.Accept(v)
	}
	v.variableScopes.Pop()
	return nil
}

// VisitStmtExpr implements [syntax.StmtVisitor].
func (v *staticSemanticsValidatorVisitor) VisitStmtExpr(s *syntax.StmtExpr) error {
	defer v.at(s)()
	if s.Expr != nil {
		if err := s.Expr.Accept(v); err != nil {
			return nil
		}
	}
	switch s.Kind() {
	case syntax.StmtKindDelete:
		// Delete runs the receiver's Delete override, which is a question any
		// value may ask: a class, abstract, or interface pointer dispatches,
		// and every other receiver falls through unchanged. Nothing is reaped
		// here, so there is no reason the operand has to name a location — a
		// value is as valid a receiver as a pointer to one.
		if v.lastExprType.Kind() == syntax.TypeKindVoid {
			v.errf("cannot delete %s, which is not a value", v.lastExprType.String())
			return nil
		}
	case syntax.StmtKindFree:
		if v.lastExprType.Kind() != syntax.TypeKindPointer && s.Expr.Kind() != syntax.ExprKindAddressOf {
			v.errf("free expression %s is not an lvalue", v.lastExprType.String())
			return nil
		}
	case syntax.StmtKindMark:
		// What a value reaches is read off its type id at the mark itself, so
		// every type may be marked and nothing here has to prove a method
		// exists: a value that reaches nothing is walked as reaching nothing.
		// A void is the one thing excluded, being no value at all — there would
		// be nothing on the stack for the mark to spend.
		if v.lastExprType.Kind() == syntax.TypeKindVoid {
			v.errf("cannot mark %s, which is not a value", v.lastExprType.String())
		}
	case syntax.StmtKindReturn:
		// A bare return leaves with nothing, which only a method owing nothing
		// may do. There is no expression to have typed, so what the last one
		// left behind says nothing about this statement.
		if s.Expr == nil {
			if v.expectedReturnType.Kind() != syntax.TypeKindVoid {
				v.errf("return without a value in a method returning %s", v.expectedReturnType.String())
			}
		} else if !v.lastExprType.IsSubtypeOf(initTarget(v.expectedReturnType)) {
			v.errf("return expression is not a subtype of %s", v.expectedReturnType.String())
		}
	}
	return nil
}

// VisitStmtFor implements [syntax.StmtVisitor].
func (v *staticSemanticsValidatorVisitor) VisitStmtFor(s *syntax.StmtFor) error {
	defer v.at(s)()

	// The names this loop answers to: the empty one every bare jump asks for,
	// and its label if it was given one.
	scope := map[string]syntax.Stmt{"": s}
	if v.pendingLabel != "" {
		scope[v.pendingLabel] = s
		v.pendingLabel = ""
	}
	v.labelScopes.Push(scope)

	if err := s.Condition.Accept(v); err == nil && !v.lastExprType.Equals(syntax.TypeBool) {
		v.errf("for condition is not a boolean")
		v.labelScopes.Pop()
	} else {
		v.variableScopes.Push(map[string]binding{})
		s.Body.Accept(v)
		v.variableScopes.Pop()

		if s.Step != nil {
			s.Step.Accept(v)
		}

		// The else runs on the way out of the loop, so it lies beside the loop
		// rather than within it, and a jump written there names whatever a jump
		// written just after the whole statement would name. Leaving the loop's
		// scope first is what says so: its own names are gone by the time the
		// else is looked at, and a bare jump reaches the loop enclosing this one.
		v.labelScopes.Pop()

		if s.Else != nil {
			v.variableScopes.Push(map[string]binding{})
			s.Else.Accept(v)
			v.variableScopes.Pop()
		}
	}
	return nil
}

// VisitStmtIf implements [syntax.StmtVisitor].
func (v *staticSemanticsValidatorVisitor) VisitStmtIf(s *syntax.StmtIf) error {
	defer v.at(s)()
	if err := s.Condition.Accept(v); err == nil && !v.lastExprType.Equals(syntax.TypeBool) {
		v.errf("if condition is not a boolean")
	}

	v.variableScopes.Push(map[string]binding{})
	s.Then.Accept(v)
	v.variableScopes.Pop()

	if s.Else != nil {
		v.variableScopes.Push(map[string]binding{})
		s.Else.Accept(v)
		v.variableScopes.Pop()
	}
	return nil
}

// VisitStmtLabel implements [syntax.StmtVisitor]. The label is left for the loop
// to claim rather than put in scope here, so that the loop holds both the names
// it answers to and can leave them together when its else begins.
func (v *staticSemanticsValidatorVisitor) VisitStmtLabel(s *syntax.StmtLabel) error {
	defer v.at(s)()
	v.pendingLabel = s.Label
	s.LoopStmt.Accept(v)
	return nil
}

// VisitStmtLoopJump implements [syntax.StmtVisitor]. A label is searched for
// innermost outward: the loop a labelled jump names encloses the loop the jump
// sits in, so the label is out beyond the enclosing loop's own scope.
//
// A bare jump asks after the empty label, which every loop answers to, so it
// fails the same search a misspelled label does and is reported apart from it. A
// jump lying in no loop at all named nothing that could be missing.
func (v *staticSemanticsValidatorVisitor) VisitStmtLoopJump(s *syntax.StmtLoopJump) error {
	defer v.at(s)()
	for scope := range v.labelScopes.All() {
		if _, ok := scope[s.Label]; ok {
			return nil
		}
	}

	jump := "break"
	if s.Kind() == syntax.StmtKindContinue {
		jump = "continue"
	}
	if s.Label == "" {
		v.errf("%s is not in a loop", jump)
	} else {
		v.errf("%s names label %s, which is not a loop in scope", jump, s.Label)
	}
	return nil
}

// terminates reports whether control cannot run past a statement. A method
// declared to give a value back has to be certain of leaving by a return: a body
// that runs off its end unwinds its frame with nothing on it, and the caller
// reads a value out of where the frame stood regardless.
func terminates(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.StmtExpr:
		return s.Kind() == syntax.StmtKindReturn
	case *syntax.StmtBlock:
		// Whatever follows a statement that does not run on is unreachable, so
		// a block runs on only if every statement in it does.
		for _, stmt := range s.Stmts {
			if terminates(stmt) {
				return true
			}
		}
	case *syntax.StmtIf:
		// A missing else is a branch that does nothing, and doing nothing runs
		// on.
		return s.Else != nil && terminates(s.Then) && terminates(s.Else)
	case *syntax.StmtFor:
		return terminatesLoop(s)
	case *syntax.StmtLabel:
		return terminates(s.LoopStmt)
	}
	return false
}

// terminatesLoop reports whether control cannot run past a loop, which is a
// question about the ways out of a loop rather than about what its body does. A
// loop is left when its condition fails, which lands in the else it was written
// with or past the loop entirely, and by a break, which lands past the loop
// either way. So a loop whose condition cannot fail and which nothing breaks
// out of is never left at all, and what follows it is unreachable.
func terminatesLoop(loop *syntax.StmtFor) bool {
	if breaksOut(loop.Body, nil) {
		return false
	}
	if unconditional(loop.Condition) {
		return true
	}
	return loop.Else != nil && terminates(loop.Else)
}

// unconditional reports whether a loop's condition cannot fail. A for written
// without one is given a true literal by the parser, so a loop written to run on
// true and one written to run forever are the same statement.
func unconditional(condition syntax.Expr) bool {
	return condition.Kind() == syntax.ExprKindLiteralBoolean && condition.Start().Kind == syntax.TTTrue
}

// breaksOut reports whether a break somewhere within a loop's body leaves that
// loop. The inner labels are those of the loops between the break and the one
// being asked about, which is what says whether the break reaches out that far:
// a bare break leaves the innermost loop it lies in, so one written inside a
// nested loop leaves that one instead, and a labelled break leaves the loop the
// label names, which lies further out unless a loop between claims the name. A
// break naming the loop being asked about leaves it, and that loop's own label
// is not among these.
func breaksOut(s syntax.Stmt, inner []string) bool {
	switch s := s.(type) {
	case *syntax.StmtLoopJump:
		if s.Kind() != syntax.StmtKindBreak {
			return false
		}
		if s.Label == "" {
			return len(inner) == 0
		}
		return !slices.Contains(inner, s.Label)
	case *syntax.StmtBlock:
		for _, stmt := range s.Stmts {
			if breaksOut(stmt, inner) {
				return true
			}
		}
	case *syntax.StmtIf:
		return breaksOut(s.Then, inner) || (s.Else != nil && breaksOut(s.Else, inner))
	case *syntax.StmtLabel:
		return breaksOut(s.LoopStmt, append(slices.Clone(inner), s.Label))
	case *syntax.StmtFor:
		// A nested loop claims every bare break written inside its body, so it
		// stands among the inner loops whether it was labelled or not. Its else
		// is not its body: that runs on the way out of the loop and lies beside
		// it, so a break written there is one the nested loop never claimed and
		// reaches out exactly as far as it would past the whole statement.
		nested := append(slices.Clone(inner), "")
		return breaksOut(s.Body, nested) || (s.Else != nil && breaksOut(s.Else, inner))
	}
	return false
}

// lookupVariable resolves name to what it stands for, searching the innermost
// scope outward, so a declaration shadows any enclosing one of the same name.
// That is what puts a local ahead of a field of the same name, and a nested
// declaration's static ahead of the enclosing declaration's.
//
// Only a local is closed over. A static outlives every frame and a field is
// reached through the receiver a closure already carries, so neither is
// something a lambda has to be handed a copy of.
// bindingOf resolves name the way [staticSemanticsValidatorVisitor.lookupVariable]
// does, but without recording a capture. It answers what a name already resolved
// once stands for, which is a question asked about the name rather than a second
// reach for it: counting it as another capture would close a lambda over a name
// its body never reached out for.
// A name that resolves to nothing comes back nil, which no binding kind matches,
// so a use that has already been reported as unresolved is not reported again as
// something else.
func (v *staticSemanticsValidatorVisitor) bindingOf(name string) binding {
	for scope := range v.variableScopes.All() {
		if b, ok := scope[name]; ok {
			return b
		}
	}
	return nil
}

func (v *staticSemanticsValidatorVisitor) lookupVariable(name string) (binding, bool) {
	depth := v.variableScopes.Len()
	for scope := range v.variableScopes.All() {
		if b, ok := scope[name]; ok {
			if _, isLocal := b.(localBinding); isLocal {
				v.capture(name, depth)
			}
			return b, true
		}
		depth--
	}
	return nil, false
}

// capture records a name as closed over by each lambda it was resolved from
// outside of. A lambda's body opens its scopes above those that stood open when
// it began, so a name found at or below that mark was not the lambda's own — and
// a lambda nested in another carries the name along for it, since what the inner
// one closes over it can only reach through the outer.
func (v *staticSemanticsValidatorVisitor) capture(name string, depth int) {
	for lambda := range v.lambdas.All() {
		if depth > lambda.depth {
			return
		}
		if _, ok := lambda.captured[name]; !ok {
			lambda.captured[name] = struct{}{}
			lambda.lambda.Captures = append(lambda.lambda.Captures, name)
		}
	}
}

// parameterScope binds a function's parameter names to the types its signature
// declares them with, the two being of one length by the time the parser is
// done with them. A parameter written as [syntax.BlankName] is left out of
// the scope: the slot the caller pushed is still there, but no later name
// resolves to it.
func parameterScope(names []string, types []syntax.Type) map[string]binding {
	scope := make(map[string]binding, len(names))
	for i, name := range names {
		if name == syntax.BlankName {
			continue
		}
		scope[name] = localBinding{typ: syntax.ParameterBindingType(types[i])}
	}
	return scope
}

// VisitStmtVariable implements [syntax.StmtVisitor]. Only the innermost scope is
// checked for a redeclaration: shadowing an enclosing variable is allowed. A
// variable written as [syntax.BlankName] is skipped by both checks — nothing
// resolves to it, so there is nothing to redeclare and nothing to shadow —
// but its type and expression are still validated so the slot it reserves is
// a well-typed one.
func (v *staticSemanticsValidatorVisitor) VisitStmtVariable(s *syntax.StmtVariable) error {
	defer v.at(s)()
	name := s.Name()
	if name != syntax.BlankName {
		if _, ok := v.variableScopes.Peek()[name]; ok {
			v.errf("variable %s already declared", name)
		}
		v.variableScopes.Peek()[name] = localBinding{typ: s.Type}
	}

	v.requireDeclarable(s.Type, "variable", name)
	s.Type.Accept(v)
	if s.Expr != nil {
		if err := s.Expr.Accept(v); err == nil && !v.lastExprType.IsSubtypeOf(initTarget(s.Type)) {
			v.errf("variable expression is not a subtype of %s", s.Type.String())
		}
	}
	return nil
}

// VisitExprCast implements [syntax.ExprVisitor].
func (v *staticSemanticsValidatorVisitor) VisitExprCast(e *syntax.ExprCast) error {
	defer v.at(e)()
	if err := e.Expr.Accept(v); err != nil {
		return err
	}
	e.Type.Accept(v)
	if !castableTo(v.lastExprType, e.Type) {
		return v.errf("cannot cast %s to %s", v.lastExprType.String(), e.Type.String())
	}
	v.lastExprType = e.Type
	return nil
}

// castableTo reports whether a value of type src may be explicitly cast to
// target.
func castableTo(src, target syntax.Type) bool {
	// Const is a promise about a slot, not about the value held in it, so an
	// explicit cast lets the program drop it or put it back on at will. Casting
	// away const is undefined — the compiler will not stop a program that then
	// writes through the stripped type to a slot it was told not to — but the
	// cast itself goes through, which is what the escape hatch is for.
	if syntax.IsConst(src) || syntax.IsConst(target) {
		return castableTo(syntax.StripConst(src), syntax.StripConst(target))
	}
	if src.Equals(target) {
		return true
	}

	// A value of an enum backed by a primitive reads as that primitive, and so
	// casts to whatever the primitive does. The way back is not a cast: most
	// words name no value.
	if enum, ok := syntax.EnumBackingCast(src, target); ok {
		return castableTo(enum.Primitive(), target)
	}

	sk, tk := src.Kind(), target.Kind()

	// Every value can be written out as a string: a primitive by what its word
	// means, a type by the name it was declared under, a declaration by the
	// method that says how it reads, and anything else by the address it lives
	// at. A void is not a value and has nothing to write out.
	if tk == syntax.TypeKindString {
		return sk != syntax.TypeKindVoid
	}

	// A cast along a line of descent reads a value as another type it is: upward
	// that holds of every value of the source type, and downward of those values
	// that really are of the target type, which is left to the runtime to
	// settle.
	if src.IsSubtypeOf(target) || target.IsSubtypeOf(src) {
		return true
	}

	// A value held as a reference is one word naming an object that lies
	// elsewhere, so what remains of it read as an address is that address, read
	// as a number is that address counted, and read as a truth is whether it
	// names anything at all.
	if heldAsReference(src) {
		switch {
		case tk == syntax.TypeKindInt, tk == syntax.TypeKindUint, tk == syntax.TypeKindBool,
			tk == syntax.TypeKindPointer && isVoidPointer(target):
			return true
		}
	}

	// The numeric primitives are a single value apiece, so each converts to any
	// other.
	switch sk {
	case syntax.TypeKindBool:
		return tk == syntax.TypeKindChar || tk == syntax.TypeKindByte || tk == syntax.TypeKindInt || tk == syntax.TypeKindUint || tk == syntax.TypeKindFloat
	case syntax.TypeKindChar:
		return tk == syntax.TypeKindInt || tk == syntax.TypeKindUint || tk == syntax.TypeKindByte || tk == syntax.TypeKindBool || tk == syntax.TypeKindFloat
	case syntax.TypeKindByte:
		return tk == syntax.TypeKindInt || tk == syntax.TypeKindUint || tk == syntax.TypeKindChar || tk == syntax.TypeKindBool || tk == syntax.TypeKindFloat
	case syntax.TypeKindFloat:
		return tk == syntax.TypeKindInt || tk == syntax.TypeKindUint || tk == syntax.TypeKindChar || tk == syntax.TypeKindByte || tk == syntax.TypeKindBool
	case syntax.TypeKindInt, syntax.TypeKindUint:
		switch tk {
		case syntax.TypeKindInt, syntax.TypeKindUint,
			syntax.TypeKindFloat, syntax.TypeKindChar, syntax.TypeKindByte, syntax.TypeKindBool:
			return true
		case syntax.TypeKindPointer:
			// An int or uint is a whole word, which is what an address is, but
			// it says nothing of what lies at it, and only a void* asks nothing.
			return isVoidPointer(target)
		}
		return false
	case syntax.TypeKindString:
		if tk == syntax.TypeKindBool {
			return true
		}
		// A string's own bytes are reachable by casting to byte* or char*: the
		// resulting pointer addresses the string's bytes field (NUL-terminated),
		// which is what makes a string pass directly to a C API that reads a
		// char*. A void* works the same way and promises nothing of what lies at
		// it. Any other pointer target would be a reinterpretation the string
		// does not support.
		if tk == syntax.TypeKindPointer {
			if isVoidPointer(target) {
				return true
			}
			pointee := target.(*syntax.TypeWrapped).Type.Kind()
			return pointee == syntax.TypeKindByte || pointee == syntax.TypeKindChar
		}
		return false
	case syntax.TypeKindPointer:
		switch tk {
		case syntax.TypeKindBool, syntax.TypeKindInt, syntax.TypeKindUint:
			return true
		case syntax.TypeKindPointer:
			// A void* addresses anything, so it reads as any pointer. Two
			// pointers of unrelated types are not each other's.
			return isVoidPointer(src)
		case syntax.TypeKindFunction:
			// Something callable is reached by its address like anything else,
			// so a void* reads as a signature. This is how a native is called:
			// what it takes and gives back is written in C, and the cast is the
			// source saying what that is.
			return isVoidPointer(src)
		}
		return false
	case syntax.TypeKindFunction:
		// A signature says what a call does, not where it is, so what remains of
		// one read as an address is the address.
		return tk == syntax.TypeKindPointer && isVoidPointer(target)
	}
	return false
}

// heldAsReference reports whether a value of the type is one word naming an
// object that lies elsewhere rather than being that object. An abstract or an
// interface always is: neither is laid out anywhere, so all a value of one can
// be is a reference to the implementor that is. A class, struct or enum is the
// object itself, however wide that makes it, and & is what names where it lies.
func heldAsReference(t syntax.Type) bool {
	resolvable, ok := t.(syntax.TypeResolvable)
	if !ok {
		return false
	}
	switch resolvable.Decl().Kind() {
	case syntax.KindAbstract, syntax.KindInterface:
		return true
	}
	return false
}

// bareReference finds an abstract or an interface written as a value rather than
// as a pointer to one, and reports whether there was one to find.
//
// Neither is ever laid out anywhere, so no slot can hold one and no argument can
// carry one: what a program means by such a type is always the pointer, and
// writing the pointer is what says which of the two meanings a * was going to
// have. Left legal, a bare one would make &s a second and unrelated way to
// arrive at Shape*, one addressing the slot rather than the implementor.
func bareReference(t syntax.Type) (syntax.Type, bool) {
	return bareReferenceUnder(t, false)
}

// bareReferenceUnder carries whether the type reached is only being pointed at.
// Being pointed at is the form the rule asks for, so a type reached that way is
// not itself judged — but what is written inside it still is, because a pointer
// says nothing about what the declaration it addresses stores. Box[Shape]* is as
// much a Shape-shaped slot as Box[Shape] is, the pointer standing in front of
// the box rather than in front of the Shape.
//
// A generic argument is judged wherever it appears for that reason, while an
// open one is judged nowhere: what T will be bound to is the instantiation's to
// say, so Box[T] declares nothing and only Box[Shape] does. Arrays, threads and
// variadics hold their element as a value, and a function type declares every
// value it takes and returns as surely as a variable declares its own.
func bareReferenceUnder(t syntax.Type, pointedAt bool) (syntax.Type, bool) {
	if !pointedAt && heldAsReference(t) {
		return t, true
	}
	switch t := t.(type) {
	case *syntax.TypeGeneric:
		for _, arg := range t.ParameterTypes {
			if bad, found := bareReferenceUnder(arg, false); found {
				return bad, true
			}
		}
	case *syntax.TypeFunction:
		for _, p := range t.ParameterTypes {
			if bad, found := bareReferenceUnder(p, false); found {
				return bad, true
			}
		}
		if t.ReturnType != nil {
			return bareReferenceUnder(t.ReturnType, false)
		}
	case *syntax.TypeArray:
		return bareReferenceUnder(t.Type, false)
	case *syntax.TypeWrapped:
		switch t.Kind() {
		case syntax.TypeKindPointer:
			return bareReferenceUnder(t.Type, true)
		case syntax.TypeKindVariadic, syntax.TypeKindThread:
			return bareReferenceUnder(t.Type, false)
		case syntax.TypeKindConst:
			return bareReferenceUnder(t.Type, pointedAt)
		}
	}
	return nil, false
}

// requireDeclarable rejects a written type that describes a value nothing can
// hold. kind and name say what was being declared, so the error points at where
// the pointer belongs rather than only at the type.
func (v *staticSemanticsValidatorVisitor) requireDeclarable(t syntax.Type, kind, name string) {
	bad, found := bareReference(t)
	if !found {
		return
	}
	v.errf("%s %s cannot be declared %s; %s is declared %s, which is only ever a reference to the implementor it names, so write %s*",
		kind, name, t.String(), bad.String(), bad.(syntax.TypeResolvable).Decl().Kind(), bad.String())
}

// isVoidPointer reports whether t is the void* type.
func isVoidPointer(t syntax.Type) bool {
	w, ok := t.(*syntax.TypeWrapped)
	return ok && w.Kind() == syntax.TypeKindPointer && w.Type.Kind() == syntax.TypeKindVoid
}

// VisitExprField implements [syntax.ExprVisitor]. A field is selected out of
// whatever the object names a declaration by, which a pointer does as much as a
// bare type: [syntax.SelectionTarget] is what says so, and it is what lets a
// method be called on the pointer every subtype relation is written between.
func (v *staticSemanticsValidatorVisitor) VisitExprField(e *syntax.ExprField) error {
	defer v.at(e)()
	if err := e.Expr.Accept(v); err != nil {
		return err
	}

	rt, ok := syntax.SelectionTarget(v.lastExprType)
	if !ok {
		return v.errf("expression %s is not a resolvable type", v.lastExprType.String())
	}

	f, ok := rt.Decl().GetInstanceField(e.Field)
	if !ok {
		return v.errf("field %s.%s not found", rt.Decl().Name(), e.Field)
	}

	accessible := f.OuterDecl().File() == v.currentDecl.File()
	if modifiers := f.Modifiers(); !accessible {
		switch {
		case modifiers.Has(syntax.ModifierPublic), !modifiers.Has(syntax.ModifierPrivate) && !modifiers.Has(syntax.ModifierProtected):
			accessible = true
		case modifiers.Has(syntax.ModifierProtected):
			outerDeclTypeable, isTypeable := f.OuterDecl().(syntax.DeclTypeable)
			declSubtypeable, isSubtypeable := v.currentDecl.(syntax.DeclSubtypeable)
			accessible = isTypeable && isSubtypeable && declSubtypeable.IsSubtypeOf(outerDeclTypeable.DeclType())
		}
	}
	if !accessible {
		return v.errf("field %s.%s is not accessible", f.OuterDecl().Name(), f.Name())
	}

	e.ComputedType = f.FieldType()
	v.lastExprType = e.ComputedType
	return nil
}

// VisitExprLambda implements [syntax.ExprVisitor]. The enclosing variables stay
// in view while the body is validated, since a name reaching out to one of them
// is what a capture is and being in view is what lets it resolve at all. The
// labels do not: a label names a loop to jump out of, and no jump leaves the
// lambda it was written in.
func (v *staticSemanticsValidatorVisitor) VisitExprLambda(e *syntax.ExprLambda) error {
	defer v.at(e)()
	e.Type.Accept(v)
	if e.Body != nil {
		oldExpectedReturnType := v.expectedReturnType
		oldLabelScopes := v.labelScopes
		oldPendingLabel := v.pendingLabel
		v.expectedReturnType = e.Type.ReturnType
		v.labelScopes = base.Stack[map[string]syntax.Stmt]{}
		v.pendingLabel = ""

		v.lambdas.Push(&lambdaContext{
			lambda:   e,
			depth:    v.variableScopes.Len(),
			captured: map[string]struct{}{},
		})
		v.variableScopes.Push(parameterScope(e.Parameters, e.Type.ParameterTypes))
		e.Body.Accept(v)
		if e.Type.ReturnType.Kind() != syntax.TypeKindVoid && !terminates(e.Body) {
			v.errf("lambda can be left without returning a %s", e.Type.ReturnType.String())
		}
		v.variableScopes.Pop()
		v.lambdas.Pop()

		v.expectedReturnType = oldExpectedReturnType
		v.labelScopes = oldLabelScopes
		v.pendingLabel = oldPendingLabel
	}
	v.lastExprType = e.Type
	return nil
}

// VisitExprNAry implements [syntax.ExprVisitor].
//
// On the first type error it records the error and returns it, so the caller
// stops type checking the enclosing expression and doesn't cascade errors until
// the next statement.
func (v *staticSemanticsValidatorVisitor) VisitExprNAry(e *syntax.ExprNAry) error {
	defer v.at(e)()
	ts, err := v.operandTypes(e)
	if err != nil {
		return err
	}
	op := e.Kind().String()
	switch e.Kind() {
	// ternary
	case syntax.ExprKindTernary:
		if syntax.StripConst(ts[0]).Kind() != syntax.TypeKindBool {
			return v.errf("ternary condition must be a boolean, got %s", ts[0].String())
		} else if !ts[1].IsSubtypeOf(ts[2]) {
			return v.errf("ternary then expression is not a subtype of the else expression")
		}
		v.lastExprType = ts[1]
	// function call
	case syntax.ExprKindAsync:
		if e.Exprs[0].Kind() != syntax.ExprKindCall {
			return v.errf("async expression must be a function call, got %s", e.Exprs[0].Kind().String())
		}
		v.lastExprType = syntax.NewWrappedType(syntax.TypeKindThread, v.lastExprType)
	// thread
	case syntax.ExprKindWait:
		if v.lastExprType.Kind() != syntax.TypeKindThread {
			return v.errf("wait expression must be a thread, got %s", v.lastExprType.String())
		}
		v.lastExprType = v.lastExprType.(*syntax.TypeWrapped).Type
	// lvalue, subtype of value
	case syntax.ExprKindAssign:
		if !v.isLValue(e.Exprs[0]) {
			return v.errf("assignment target is not an lvalue")
		}
		if syntax.IsConst(ts[0]) {
			return v.errf("cannot assign to const %s", ts[0].String())
		}
		if !ts[1].IsSubtypeOf(ts[0]) {
			return v.errf("cannot assign %s to %s", ts[1].String(), ts[0].String())
		}
		v.lastExprType = ts[0]

	// Equal nullable types
	case syntax.ExprKindNullCoalesce:
		if syntax.StripConst(ts[0]).Kind() != syntax.TypeKindPointer {
			return v.errf("left operand of %s must be a pointer, got %s", op, ts[0].String())
		}
		// A chain evaluates to whichever operand it reaches, and the leftmost one
		// names the result, so every operand after it has to fit in that.
		for _, t := range ts[1:] {
			if !t.IsSubtypeOf(ts[0]) {
				return v.errf("operands of %s have mismatched types %s and %s", op, ts[0].String(), t.String())
			}
		}
		v.lastExprType = ts[0]

	// Pointer
	case syntax.ExprKindDereference:
		w, ok := syntax.StripConst(ts[0]).(*syntax.TypeWrapped)
		if !ok || w.Kind() != syntax.TypeKindPointer {
			return v.errf("cannot dereference non-pointer type %s", ts[0].String())
		}
		// Dereferencing reads what lies at an address, and nothing of an
		// abstract or an interface is ever laid out to be read: the pointer is
		// the whole of what such a type ever is, so a value of one stripped of
		// its pointer would be the same word under a type that cannot describe
		// it. Only the outermost level is the pointer itself, so a pointer to
		// one of these can still be dereferenced down to it.
		if heldAsReference(w.Type) {
			return v.errf("cannot dereference %s; %s is declared %s, which is only ever a reference to the implementor it names and is laid out nowhere to be read out of",
				ts[0].String(), w.Type.String(), w.Type.(syntax.TypeResolvable).Decl().Kind())
		}
		v.lastExprType = w.Type
	case syntax.ExprKindCall:
		ft, ok := syntax.StripConst(ts[0]).(*syntax.TypeFunction)
		if !ok {
			return v.errf("cannot call non-function type %s", ts[0].String())
		}
		if err := v.checkCallArgs(ft, e.Exprs[1:], ts[1:], op); err != nil {
			return err
		}
		v.lastExprType = ft.ReturnType

	// Objects or equivalent types
	case syntax.ExprKindEqual, syntax.ExprKindNotEqual,
		syntax.ExprKindGreaterThan, syntax.ExprKindLessThan,
		syntax.ExprKindGreaterThanOrEqual, syntax.ExprKindLessThanOrEqual:
		if err := v.requireComparable(ts, op); err != nil {
			return err
		}
		v.lastExprType = syntax.TypeBool
	case syntax.ExprKindSpaceship:
		if err := v.requireComparable(ts, op); err != nil {
			return err
		}
		v.lastExprType = syntax.TypeInt

	// Types. Descent relates two types and nothing else — a value stands below
	// nothing, it only has a type that does — so both operands are ids, however
	// they were arrived at: written as type(T), or read off a value with @.
	case syntax.ExprKindSubtypeOf:
		if len(ts) != 2 {
			return v.errf("%s relates two operands, got %d", op, len(ts))
		}
		if err := v.requireAll(ts, syntax.TypeType, op); err != nil {
			return err
		}
		v.lastExprType = syntax.TypeBool

	// lvalue
	case syntax.ExprKindPreIncrement, syntax.ExprKindPreDecrement,
		syntax.ExprKindPostIncrement, syntax.ExprKindPostDecrement:
		if !v.isLValue(e.Exprs[0]) {
			return v.errf("operand of %s is not an lvalue", op)
		}
		if syntax.IsConst(ts[0]) {
			return v.errf("cannot %s const %s", op, ts[0].String())
		}
		if !numeric(ts[0]) {
			return v.errf("operand of %s must be numeric, got %s", op, ts[0].String())
		}
		v.lastExprType = ts[0]

	// Boolean
	case syntax.ExprKindLogicalOR, syntax.ExprKindLogicalAND:
		if err := v.requireAll(ts, syntax.TypeBool, op); err != nil {
			return err
		}
		v.lastExprType = syntax.TypeBool

	// Boolean, Integer, Char or Byte, in any combination: each is a single word
	// the operator works on bit by bit. The leftmost operand names the result,
	// with const stripped since what the operator gives back is a fresh value
	// no one holds a slot to.
	case syntax.ExprKindBinaryOR, syntax.ExprKindBinaryAND,
		syntax.ExprKindBinaryXOR, syntax.ExprKindBinaryNegate:
		if err := v.requireAllAnyOf(ts, op, syntax.TypeBool, syntax.TypeInt, syntax.TypeUint, syntax.TypeChar, syntax.TypeByte); err != nil {
			return err
		}
		v.lastExprType = syntax.StripConst(ts[0])

	// Boolean, Integer, Char or Byte, asked whether it is zero, which it answers
	// as a boolean whatever it was asked as.
	case syntax.ExprKindLogicalNegate:
		if err := v.requireAllAnyOf(ts, op, syntax.TypeBool, syntax.TypeInt, syntax.TypeUint, syntax.TypeChar, syntax.TypeByte); err != nil {
			return err
		}
		v.lastExprType = syntax.TypeBool

	// Integer, Char or Byte
	case syntax.ExprKindBinaryShiftLeft, syntax.ExprKindBinaryShiftRight,
		syntax.ExprKindBinaryShiftRightRight, syntax.ExprKindModulus:
		if err := v.requireAllAnyOf(ts, op, syntax.TypeInt, syntax.TypeUint, syntax.TypeChar, syntax.TypeByte); err != nil {
			return err
		}
		v.lastExprType = syntax.StripConst(ts[0])

	// Integer or Float
	case syntax.ExprKindAdd, syntax.ExprKindSubtract:
		t, err := v.checkAdditive(e, ts, op)
		if err != nil {
			return err
		}
		v.lastExprType = t
	case syntax.ExprKindMultiply, syntax.ExprKindDivide:
		t, err := v.requireAllSame(ts, op, syntax.TypeInt, syntax.TypeUint, syntax.TypeFloat, syntax.TypeChar, syntax.TypeByte)
		if err != nil {
			return err
		}
		v.lastExprType = t
	case syntax.ExprKindArithmeticNegate:
		if !numeric(ts[0]) {
			return v.errf("operand of %s must be numeric, got %s", op, ts[0].String())
		}
		v.lastExprType = syntax.StripConst(ts[0])

	// lvalue
	case syntax.ExprKindAddressOf:
		// Only a location has an address to take. A temporary could be given
		// one — OpAddressOf is written to materialize a value that has no home,
		// which is how a method call on an rvalue reaches a receiver — but that
		// is a home the source never named and cannot name again, so & of one
		// hands back a pointer to a place the program has no other way to reach.
		if !v.isLValue(e.Exprs[0]) {
			return v.errf("operand of %s is not an lvalue", op)
		}
		v.lastExprType = syntax.NewWrappedType(syntax.TypeKindPointer, ts[0])

	// Anything
	case syntax.ExprKindHash:
		v.lastExprType = syntax.TypeInt
	case syntax.ExprKindTypeof:
		v.lastExprType = syntax.TypeType

	default:
		panic("unreachable")
	}
	e.ComputedType = v.lastExprType
	return nil
}

// at reports errors at n until the call it answers with is made. A visit runs
// nested inside the visit of whatever holds the node, so a check that runs after
// visiting a child has to be put back where it is written rather than left
// wherever the child ended up.
func (v *staticSemanticsValidatorVisitor) at(n node) func() {
	previous := v.currentStart
	v.currentStart = n.Start()
	return func() { v.currentStart = previous }
}

// errf records a new error and returns it so callers can bubble it up.
func (v *staticSemanticsValidatorVisitor) errf(format string, args ...any) error {
	err := errfAt(v.currentDecl, v.currentStart, format, args...)
	v.errs = append(v.errs, err)
	return err
}

// operandTypes visits every operand of e and returns their types. It stops at
// the first operand that fails to type check and returns that error; the failing
// operand has already recorded it. Types are read from lastExprType, which each
// visit sets as a side effect.
func (v *staticSemanticsValidatorVisitor) operandTypes(e *syntax.ExprNAry) ([]syntax.Type, error) {
	ts := make([]syntax.Type, len(e.Exprs))
	for i, arg := range e.Exprs {
		inner := spreadInner(arg)
		if inner != nil {
			if e.Kind() != syntax.ExprKindCall || i != len(e.Exprs)-1 {
				restore := v.at(arg)
				err := v.errf("spread may only appear as the last argument of a call")
				restore()
				return nil, err
			}
			if err := inner.Accept(v); err != nil {
				return nil, err
			}
			arg.(*syntax.ExprNAry).ComputedType = v.lastExprType
			ts[i] = v.lastExprType
			continue
		}
		if err := arg.Accept(v); err != nil {
			return nil, err
		}
		ts[i] = v.lastExprType
	}
	return ts, nil
}

// spreadInner returns the inner expression of a spread, or nil when e is not
// a spread. A spread is a modifier on the argument it wraps and is only legal
// as the last argument of a call; this is what lets the caller recognize it
// without reaching into the AST shape elsewhere.
func spreadInner(e syntax.Expr) syntax.Expr {
	nary, ok := e.(*syntax.ExprNAry)
	if !ok || nary.Kind() != syntax.ExprKindSpread {
		return nil
	}
	return nary.Exprs[0]
}

// requireAll returns an error for the first operand whose type is not want.
// Const is transparent to operators: a const value read into one is a copy
// taken out of a slot nothing else can write to, and the copy stands on its
// own without the promise its source carried.
func (v *staticSemanticsValidatorVisitor) requireAll(ts []syntax.Type, want syntax.Type, op string) error {
	for _, t := range ts {
		if !syntax.StripConst(t).Equals(want) {
			return v.errf("operand of %s must be %s, got %s", op, want.String(), t.String())
		}
	}
	return nil
}

// requireAllAnyOf returns an error for the first operand whose type is none of
// allowed.
func (v *staticSemanticsValidatorVisitor) requireAllAnyOf(ts []syntax.Type, op string, allowed ...syntax.Type) error {
	for _, t := range ts {
		bare := syntax.StripConst(t)
		if !slices.ContainsFunc(allowed, bare.Equals) {
			return v.errf("operand of %s has unsupported type %s", op, t.String())
		}
	}
	return nil
}

// requireAllSame checks that the first operand is one of allowed and that every
// other operand has that same type, returning the first operand's type stripped
// of const. The result of an operator is a fresh value no one holds a slot to,
// so what the operator gives back carries none of the promises its operands
// carried into it.
func (v *staticSemanticsValidatorVisitor) requireAllSame(ts []syntax.Type, op string, allowed ...syntax.Type) (syntax.Type, error) {
	got := syntax.StripConst(ts[0])
	if !slices.ContainsFunc(allowed, got.Equals) {
		return got, v.errf("operand of %s has unsupported type %s", op, ts[0].String())
	}
	for _, t := range ts[1:] {
		if !syntax.StripConst(t).Equals(got) {
			return got, v.errf("operands of %s must all be %s, got %s", op, got.String(), t.String())
		}
	}
	return got, nil
}

// requireComparable checks that the operands of a comparison are comparable with
// each other. A comparison is of two values and answers with something that is
// not one of them — whether they are equal, or how they are ordered — so a run of
// them is not a chain the way a run of sums is, and there is nothing a third
// operand could be compared against.
func (v *staticSemanticsValidatorVisitor) requireComparable(ts []syntax.Type, op string) error {
	if len(ts) != 2 {
		return v.errf("%s compares two operands, got %d", op, len(ts))
	}
	if !comparableTypes(ts[0], ts[1]) {
		return v.errf("operands of %s are not comparable: %s and %s", op, ts[0].String(), ts[1].String())
	}
	return nil
}

// checkAdditive type checks + and -. A pointer or an array steps by integer
// offsets; a string concatenates strings and chars, and cannot be subtracted;
// everything else adds operands of its own type.
func (v *staticSemanticsValidatorVisitor) checkAdditive(e *syntax.ExprNAry, ts []syntax.Type, op string) (syntax.Type, error) {
	first := syntax.StripConst(ts[0])
	switch {
	case concatenates(ts):
		if e.Kind() != syntax.ExprKindAdd {
			return syntax.TypeString, v.errf("operands of %s cannot be strings", op)
		}
		return syntax.TypeString, v.requireAllAnyOf(ts, op, syntax.TypeString, syntax.TypeChar)
	case first.Kind() == syntax.TypeKindPointer:
		return first, v.requireAllAnyOf(ts[1:], op, syntax.TypeInt, syntax.TypeUint)
	case first.Kind() == syntax.TypeKindArray:
		if err := v.requireAllAnyOf(ts[1:], op, syntax.TypeInt, syntax.TypeUint); err != nil {
			return first, err
		}
		// An array is a value of the whole run, so a sum over one is the only
		// place its elements are stepped over rather than taken, and what the
		// step needs is where the run begins. A run that lies nowhere has no
		// beginning to name: it stands on the stack as the elements themselves,
		// and there is no address of it for the offset to be added to.
		if !v.isLValue(e.Exprs[0]) {
			return first, v.errf("left operand of %s is a %s that is not an lvalue; stepping over a run of elements needs somewhere it begins", op, first.String())
		}
		return syntax.NewWrappedType(syntax.TypeKindPointer, first.(*syntax.TypeArray).Type), nil
	default:
		return v.requireAllSame(ts, op, syntax.TypeInt, syntax.TypeUint, syntax.TypeFloat, syntax.TypeChar, syntax.TypeByte)
	}
}

// concatenates reports whether any operand is a string, which makes the whole
// expression a concatenation rather than an addition.
func concatenates(ts []syntax.Type) bool {
	return slices.ContainsFunc(ts, func(t syntax.Type) bool {
		return syntax.StripConst(t).Kind() == syntax.TypeKindString
	})
}

// checkCallArgs checks argument arity and subtyping against a function type,
// accounting for a trailing variadic parameter.
//
// A variadic payload may be forwarded whole with a trailing spread rather than
// written out one argument at a time: the callee receives a pointer either way,
// so a T* (which a variadic parameter already is inside the body) or a T[N]
// decaying to one is handed on without being unpacked and repacked. The spread
// stands alone — no other arguments may feed the variadic in the same call —
// and its element type must match the variadic's, since a mismatch would need
// per-element conversion the forward does not do.
func (v *staticSemanticsValidatorVisitor) checkCallArgs(ft *syntax.TypeFunction, args []syntax.Expr, ts []syntax.Type, op string) error {
	params := ft.ParameterTypes
	var variadic syntax.Type
	if n := len(params); n > 0 {
		if element, ok := syntax.VariadicElement(params[n-1]); ok {
			variadic = element
			params = params[:n-1]
		}
	}

	if variadic != nil {
		if len(args) < len(params) {
			return v.errf("%s expects at least %d arguments, got %d", op, len(params), len(args))
		}
	} else if len(args) != len(params) {
		return v.errf("%s expects %d arguments, got %d", op, len(params), len(args))
	}

	for i, p := range params {
		if spreadInner(args[i]) != nil {
			return v.errf("argument %d of %s is a spread, which may only feed a variadic parameter", i+1, op)
		}
		if !ts[i].IsSubtypeOf(initTarget(p)) {
			return v.errf("argument %d of %s is not a subtype of %s", i+1, op, p.String())
		}
	}
	if variadic == nil {
		return nil
	}

	trailing := args[len(params):]
	trailingTypes := ts[len(params):]
	spreadIdx := -1
	for i, a := range trailing {
		if spreadInner(a) != nil {
			spreadIdx = i
			break
		}
	}
	if spreadIdx >= 0 {
		if len(trailing) != 1 {
			return v.errf("spread in %s must be the only variadic argument", op)
		}
		return v.checkSpreadArg(spreadInner(trailing[0]), trailingTypes[0], variadic, op)
	}
	for _, t := range trailingTypes {
		if !t.IsSubtypeOf(initTarget(variadic)) {
			return v.errf("variadic argument of %s is not a subtype of %s", op, variadic.String())
		}
	}
	return nil
}

// checkSpreadArg verifies the inner of a spread may be forwarded as the
// variadic payload. A pointer forwards verbatim and an array decays to its
// first element the same way a sum over one does; either way the element type
// must match the variadic's, since the elements are not reboxed on the way in.
// A run that lies nowhere has no address for the forward to hand on, so an
// array spread needs its inner to be an lvalue — the same rule a sum over an
// array needs.
func (v *staticSemanticsValidatorVisitor) checkSpreadArg(inner syntax.Expr, innerType, variadic syntax.Type, op string) error {
	bare := syntax.StripConst(innerType)
	var element syntax.Type
	switch bare.Kind() {
	case syntax.TypeKindPointer:
		element = bare.(*syntax.TypeWrapped).Type
	case syntax.TypeKindArray:
		if !v.isLValue(inner) {
			return v.errf("spread in %s is an array that is not an lvalue; forwarding a run of elements needs somewhere it begins", op)
		}
		element = bare.(*syntax.TypeArray).Type
	default:
		return v.errf("spread in %s must be a pointer or array, got %s", op, innerType.String())
	}
	if !syntax.StripConst(element).Equals(syntax.StripConst(variadic)) {
		return v.errf("spread in %s has element type %s, which does not match the variadic element %s", op, element.String(), variadic.String())
	}
	return nil
}

// isLValue reports whether e denotes an assignable/addressable location. A
// member lies in the object it is selected out of, but a method does not: a
// method is closed over the object rather than kept in it, so there is nothing
// to assign to and selecting one names a value rather than a location.
//
// A member is only a location if the object it lies in is one. That holds
// wherever the object is reached through an address, and otherwise only as far
// as the object is itself a location — an inline value no expression gave a home
// to lies nowhere the program can name, so a member of it is a value that would
// be discarded along with the temporary it was read out of.
func (v *staticSemanticsValidatorVisitor) isLValue(e syntax.Expr) bool {
	switch e.Kind() {
	case syntax.ExprKindIdentifier:
		b, isInstance := v.bindingOf(e.Start().Value.(string)).(instanceBinding)
		return !isInstance || !b.isMethod
	case syntax.ExprKindDereference:
		return true
	case syntax.ExprKindField:
		field := e.(*syntax.ExprField)
		resolvable, ok := syntax.SelectionTarget(field.Expr.ResolvedType())
		if !ok {
			return false
		}
		f, _ := resolvable.Decl().GetInstanceField(field.Field)
		if _, isMember := f.(*syntax.FieldMember); !isMember {
			return false
		}
		return v.hasHome(field.Expr)
	case syntax.ExprKindType:
		// A static lies in a slot of the static region for the length of the
		// run, which is as much a location as a frame slot is. Naming a type
		// with no static after it is the type's id, and an id is a value that
		// lies nowhere.
		return e.(*syntax.ExprType).Field != ""
	default:
		return false
	}
}

// hasHome reports whether e leaves the object it names somewhere that can be
// pointed into, rather than as words that exist only for as long as the
// expression they were computed in.
//
// This is a wider question than assignability. An assignment is not a valid
// assignment target, but it does leave its result in the slot it assigned to, so
// a member selected out of one is still a member of something that lies
// somewhere. What has no home is a value a call or a constructor produced and
// nothing kept.
func (v *staticSemanticsValidatorVisitor) hasHome(e syntax.Expr) bool {
	if syntax.ReachedThroughAddress(e.ResolvedType()) {
		return true
	}
	switch e.Kind() {
	case syntax.ExprKindIdentifier, syntax.ExprKindDereference,
		syntax.ExprKindThis, syntax.ExprKindSuper, syntax.ExprKindAssign:
		return true
	case syntax.ExprKindField:
		return v.isLValue(e)
	case syntax.ExprKindType:
		return e.(*syntax.ExprType).Field != ""
	}
	return false
}

// numeric reports whether t is a number: an int, a uint, a float, a char, or a
// byte — each a narrow or wide integer except the float. Const is seen through:
// an operator reads a copy of the slot's value, which is a number if the slot
// was a number.
func numeric(t syntax.Type) bool {
	t = syntax.StripConst(t)
	return t.Equals(syntax.TypeInt) || t.Equals(syntax.TypeUint) || t.Equals(syntax.TypeFloat) || t.Equals(syntax.TypeChar) || t.Equals(syntax.TypeByte)
}

// comparableTypes reports whether a and b can be compared. The non-any rule
// is subtype in either direction: one has to stand below the other for the
// pair to mean anything. The any rule is wider: an any paired with any value
// is comparable, with codegen lifting the pair to a shape the runtime knows
// how to settle — equality boxes the non-any side so the pair compares
// box-vs-box, and an order hashes each side so the pair compares as two
// ints. The one thing an any cannot be paired with is a non-value, so void
// is still refused.
func comparableTypes(a, b syntax.Type) bool {
	a, b = syntax.StripConst(a), syntax.StripConst(b)
	aIsAny := a.Kind() == syntax.TypeKindAny
	bIsAny := b.Kind() == syntax.TypeKindAny
	if aIsAny || bIsAny {
		if aIsAny && bIsAny {
			return true
		}
		other := b
		if bIsAny {
			other = a
		}
		return other.IsSubtypeOf(syntax.TypeAny)
	}
	return a.IsSubtypeOf(b) || b.IsSubtypeOf(a)
}

// VisitExprNative implements [syntax.ExprVisitor]. A native is an address and
// nothing more: what it takes and gives back is written in C, which this
// compiler does not read, so there is nothing here to check the name against.
// The source says what the address is by casting it to a signature before
// calling it, and that cast is the whole of what is known about it.
func (v *staticSemanticsValidatorVisitor) VisitExprNative(e *syntax.ExprNative) error {
	defer v.at(e)()
	e.ComputedType = syntax.NewWrappedType(syntax.TypeKindPointer, syntax.TypeVoid)
	v.lastExprType = e.ComputedType
	return nil
}

// VisitExprNew implements [syntax.ExprVisitor].
func (v *staticSemanticsValidatorVisitor) VisitExprNew(e *syntax.ExprNew) error {
	defer v.at(e)()
	e.Type.Accept(v)

	// An abstract and an interface are laid out nowhere, so there is no
	// allocation to make of a value of one — new(SomeAbstract) and
	// new(SomeInterface) are the pointer the program meant without the pointer
	// written down. A run of them is the same mistake with a count on it.
	//
	// A pointer to an abstract or interface is fine and is what the user wants:
	// new(SomeAbstract*) and new(SomeAbstract[5]) are both runs of pointers,
	// each slot one word naming an allocation elsewhere.
	bare := e.Type
	for {
		switch tt := bare.(type) {
		case *syntax.TypeArray:
			bare = tt.Type
			continue
		case *syntax.TypeTailed:
			bare = tt.Type
			continue
		}
		break
	}
	if resolvable, ok := bare.(syntax.TypeResolvable); ok {
		switch resolvable.Decl().Kind() {
		case syntax.KindAbstract, syntax.KindInterface:
			v.errf("cannot allocate %s; use %s* instead",
				e.Type.String(), bare.String())
		}
	}

	if e.Size != nil {
		if err := e.Size.Accept(v); err == nil && !v.lastExprType.Equals(syntax.TypeInt) && !v.lastExprType.Equals(syntax.TypeUint) {
			v.errf("size expression is not an integer")
		}
	}
	if e.Tail != nil {
		if tailable, ok := e.Type.(syntax.TypeTailable); !ok {
			v.errf("tail expression for non-tailable type %s", e.Type.String())
		} else if !tailable.IsTailable() {
			// A tail length written into the type is part of the type, and the
			// runtime reads it back out of the type id rather than off the
			// stack. A second length here names nothing, so the two could
			// disagree and only one of them would ever be allocated.
			v.errf("tail length of %s is already part of the type", e.Type.String())
		} else if err := e.Tail.Accept(v); err == nil && !v.lastExprType.Equals(syntax.TypeInt) && !v.lastExprType.Equals(syntax.TypeUint) {
			v.errf("tail expression is not an integer")
		}
	}
	v.lastExprType = e.ResolvedType()
	return nil
}

// VisitExprSymbol implements [syntax.ExprVisitor].
func (v *staticSemanticsValidatorVisitor) VisitExprSymbol(e *syntax.ExprSymbol) error {
	defer v.at(e)()
	switch e.Kind() {
	case syntax.ExprKindIdentifier:
		name := e.Start().Value.(string)
		b, ok := v.lookupVariable(name)
		if !ok {
			return v.errf("variable %s not found", name)
		}
		// An instance field written on its own is written on the receiver, and a
		// static field has none to be written on.
		if _, isInstance := b.(instanceBinding); isInstance && v.currentStaticField != nil {
			return v.errf("instance field %s cannot be named in static %s.%s",
				name, v.currentDecl.Name(), v.currentStaticField.Name())
		}
		v.lastExprType = b.bindingType()
	// A receiver is a pointer whatever its declaration's width. Whether it
	// addresses the caller's own instance or the copy a closure took of a
	// homeless one is the caller's to decide; either way what the frame holds is
	// the one word, which is what super's base type already is.
	case syntax.ExprKindThis:
		declTypeable, ok := v.currentDecl.(syntax.DeclTypeable)
		if !ok {
			return v.errf("current declaration %s is not a typeable", v.currentDecl.Name())
		}
		v.lastExprType = syntax.NewWrappedType(syntax.TypeKindPointer, declTypeable.DeclType())
	case syntax.ExprKindSuper:
		declSubtypeable, ok := v.currentDecl.(syntax.DeclSubtypeable)
		if !ok {
			return v.errf("current declaration %s is not a subtypeable", v.currentDecl.Name())
		}
		v.lastExprType = declSubtypeable.BaseDeclType()
	case syntax.ExprKindLiteralNull:
		v.lastExprType = syntax.TypeNull
	case syntax.ExprKindLiteralBoolean:
		v.lastExprType = syntax.TypeBool
	case syntax.ExprKindLiteralChar:
		v.lastExprType = syntax.TypeChar
	case syntax.ExprKindLiteralString:
		v.lastExprType = syntax.TypeString
	case syntax.ExprKindLiteralInteger:
		v.lastExprType = syntax.TypeInt
	case syntax.ExprKindLiteralUint:
		v.lastExprType = syntax.TypeUint
	case syntax.ExprKindLiteralFloat:
		v.lastExprType = syntax.TypeFloat
	default:
		panic("unreachable")
	}
	e.ComputedType = v.lastExprType
	return nil
}

// VisitExprType implements [syntax.ExprVisitor].
func (v *staticSemanticsValidatorVisitor) VisitExprType(e *syntax.ExprType) error {
	defer v.at(e)()
	e.Type.Accept(v)

	if e.Field == "" {
		if len(e.TypeArgs) > 0 {
			return v.errf("type arguments %v cannot be written without a field name", e.TypeArgs)
		}
		v.lastExprType = syntax.TypeType
		return nil
	}

	// A static field lives on the template, not on any one instantiation of it:
	// it serves every binding alike, and the field pass already rejects a static
	// signature or body that names an enclosing generic argument. Writing the
	// args here would promise something the template cannot carry, so the name
	// has to be given without them.
	if _, isGeneric := e.Type.(*syntax.TypeGeneric); isGeneric {
		return v.errf("static field %s cannot be accessed on generic instantiation %s; name the template without type arguments", e.Field, e.Type.String())
	}

	tr, ok := e.Type.(syntax.TypeResolvable)
	if !ok {
		return v.errf("type %s is not resolvable", e.Type.String())
	}

	fi, ok := tr.Decl().GetStaticField(e.Field)
	if !ok {
		return v.errf("static field %s.%s not found", tr.Decl().Name(), e.Field)
	}

	if len(e.TypeArgs) > 0 {
		for _, arg := range e.TypeArgs {
			if err := arg.Accept(v); err != nil {
				return err
			}
		}
		method, ok := fi.(*syntax.FieldMethod)
		if !ok {
			return v.errf("type arguments can only be given to a method, got %s.%s", tr.Decl().Name(), e.Field)
		}
		if method.Generics() == nil {
			return v.errf("method %s.%s is not generic", tr.Decl().Name(), e.Field)
		}
		if method.Generics().Len() != len(e.TypeArgs) {
			return v.errf("method %s.%s expects %d type arguments, got %d",
				tr.Decl().Name(), e.Field, method.Generics().Len(), len(e.TypeArgs))
		}
		bindings := make(map[syntax.GenericArgKey]syntax.Type, len(e.TypeArgs))
		for i, entry := range method.Generics().All() {
			bindings[entry.Key] = e.TypeArgs[i]
		}
		e.ComputedType = method.FieldType().Map(bindings)
		v.lastExprType = e.ComputedType
		return nil
	}

	// A generic static method must be named with its arguments: without them the
	// field type still carries open generic parameters, and the call that
	// follows would try to work on parameters nothing has bound. The user has to
	// write type(Decl)[A,...].Field rather than type(Decl).Field.
	if method, isMethod := fi.(*syntax.FieldMethod); isMethod && method.Generics() != nil {
		return v.errf("method %s.%s is generic over %d parameter(s); name it with its type arguments",
			tr.Decl().Name(), e.Field, method.Generics().Len())
	}

	ct, ok := staticFieldType(fi)
	if !ok {
		return v.errf("static field %s.%s is not typeable", tr.Decl().Name(), e.Field)
	}
	e.ComputedType = ct
	v.lastExprType = e.ComputedType

	return nil
}

// staticFieldType is the type naming a static field comes to. An enum value is
// static like any other and lies in a slot of its own, but it is not among the
// typeable fields: the only type it could have is the enum it is a value of, so
// it answers with that rather than carrying one written for it.
func staticFieldType(f syntax.FieldIndexable) (syntax.Type, bool) {
	switch f := f.(type) {
	case *syntax.FieldEnum:
		return f.Type(), true
	case syntax.FieldTypeable:
		return f.FieldType(), true
	}
	return nil, false
}

// VisitTypeArray implements [syntax.TypeVisitor].
func (v *staticSemanticsValidatorVisitor) VisitTypeArray(t *syntax.TypeArray) error {
	defer v.at(t)()
	t.Type.Accept(v)
	return nil
}

// VisitTypeFunction implements [syntax.TypeVisitor].
func (v *staticSemanticsValidatorVisitor) VisitTypeFunction(t *syntax.TypeFunction) error {
	defer v.at(t)()
	t.ReturnType.Accept(v)
	for _, param := range t.ParameterTypes {
		param.Accept(v)
	}
	return nil
}

// VisitTypeGeneric implements [syntax.TypeVisitor].
func (v *staticSemanticsValidatorVisitor) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	defer v.at(t)()
	t.Type.Accept(v)
	for _, param := range t.ParameterTypes {
		param.Accept(v)
	}
	return nil
}

// VisitTypeGenericArg implements [syntax.TypeVisitor]. A generic argument is
// bound by the instance a field is reached through, and a static field is
// reached through none: one of them serves the whole template, so there is
// nothing an argument could be resolved against. Every type a field writes is
// visited, so naming one anywhere — in a signature, or in a type the body
// declares, allocates or casts to — is caught here.
//
// A static generic method is the one exception: its own `[T,...]` list opens
// a parameter scope that monomorphization grounds at every call site, so a
// method-level T inside its own signature or body is bound at the site. Only
// an enclosing decl's T is still rejected under a static field.
func (v *staticSemanticsValidatorVisitor) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	defer v.at(t)()
	if f := v.currentStaticField; f != nil {
		if method, ok := f.(*syntax.FieldMethod); ok && method == t.Owner() {
			return nil
		}
		v.errf("static field %s.%s cannot name generic argument %s",
			f.OuterDecl().Name(), f.Name(), t.String())
	}
	return nil
}

// staticField is f when f is static, and nil when it is not. Fields do not nest,
// so what it answers for the field being validated stands until that field is
// done.
func staticField(f syntax.Field) syntax.Field {
	if f.Modifiers().Has(syntax.ModifierStatic) {
		return f
	}
	return nil
}

// initTarget is the type an initializer is checked against, which is the
// declared type with its outermost const stripped. A const slot is given its
// one and only value here, which is the one place a non-const may land in one;
// every later write is caught by the regular rejection of writes to const.
func initTarget(t syntax.Type) syntax.Type {
	return syntax.StripConst(t)
}

// VisitTypePrimitive implements [syntax.TypeVisitor].
func (v *staticSemanticsValidatorVisitor) VisitTypePrimitive(t *syntax.TypePrimitive) error {
	defer v.at(t)()
	return nil
}

// VisitTypeReference implements [syntax.TypeVisitor]. What the modifiers are read
// off is the declaration the name resolves to, and what they are read against is
// the file the name was written in. Asking where the reference itself sits would
// answer with that same file every time, the reference being part of what is
// written there.
func (v *staticSemanticsValidatorVisitor) VisitTypeReference(t *syntax.TypeReference) error {
	defer v.at(t)()
	if t.Decl().File() == v.currentDecl.File() {
		return nil
	}
	modifiers := t.Decl().Modifiers()
	if modifiers.Has(syntax.ModifierPublic) {
		return nil
	} else if modifiers.Has(syntax.ModifierProtected) {
		if declSubtypeable, ok := v.currentDecl.(syntax.DeclSubtypeable); ok && declSubtypeable.IsSubtypeOf(t.Decl().DeclType()) {
			return nil
		}
	} else if !modifiers.Has(syntax.ModifierPrivate) {
		return nil
	}

	v.errf("type %s is not accessible", t.String())
	return nil
}

// VisitTypeTailed implements [syntax.TypeVisitor].
func (v *staticSemanticsValidatorVisitor) VisitTypeTailed(t *syntax.TypeTailed) error {
	defer v.at(t)()
	t.Type.Accept(v)
	return nil
}

// VisitTypeWrapped implements [syntax.TypeVisitor].
func (v *staticSemanticsValidatorVisitor) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	defer v.at(t)()
	t.Type.Accept(v)
	return nil
}
