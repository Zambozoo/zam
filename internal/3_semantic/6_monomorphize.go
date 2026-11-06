package semantic

import (
	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// monomorphize forces every concrete generic instantiation the program can
// reach into existence, and in the same walk records the types codegen numbers:
// the open types of each generic template, and every ground function, thread,
// pointer and tuple type.
//
// Resolving source only materializes the instantiations that are written down.
// Given
//
//	abstract A1[T] {}
//	class C1[T] { fun void() f = () { var A1[T] a; } }
//	class C2   { fun void() f = () { var C1[int] c; } }
//
// resolving C2 creates C1[int], but nothing creates A1[int]: it only appears
// once C1's body is read under the binding {T -> int}. So the walk is a
// worklist. Every source declaration is walked once with no binding, which
// materializes each instantiation written ground and records each template's
// open types. Each newly materialized instantiation is queued and its template
// walked under its binding, which may materialize more. Termination follows
// from detectGenericCycles having ruled out infinitely expansive recursion.
//
// A generic argument names only the declaration or method it is written in,
// and a static field may name none of its declaration's, so walking an
// instantiation means walking its bounds, base type and instance fields. Static
// fields and nested declarations are the same under every binding and are
// walked once, with the source.
//
// Substitution goes through [syntax.Type.Map], which must not mutate the shared
// template node: the same template is substituted under many bindings.
func monomorphize(ast *syntax.AST) {
	v := &monomorphizer{
		walked:        map[walkedKey][]map[syntax.GenericArgKey]syntax.Type{},
		functionTypes: syntax.NewTypeSet(),
		threadTypes:   syntax.NewTypeSet(),
		pointerTypes:  syntax.NewTypeSet(),
		tupleTypes:    syntax.NewTypeSet(),
	}
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Accept(v)
	}
	v.drain()

	ast.FunctionTypes = *v.functionTypes
	ast.ThreadTypes = *v.threadTypes
	ast.PointerTypes = *v.pointerTypes
	ast.TupleTypes = *v.tupleTypes
}

type (
	// genericWork is one pending concrete instantiation: the generic decl or
	// method whose tree must be walked, paired with the binding that makes it
	// concrete. Exactly one of decl and method is non-nil.
	genericWork struct {
		decl     syntax.DeclGeneric
		method   *syntax.FieldMethod
		bindings map[syntax.GenericArgKey]syntax.Type
	}

	// walkedKey buckets the walked set by owner and a hash of the bindings. Two
	// bindings that hash alike are told apart by [syntax.Type.Equals], since
	// types that print alike, such as two declarations' nested Node[int], are
	// not the same type.
	walkedKey struct {
		owner any
		hash  uint64
	}

	monomorphizer struct {
		// bindings is the substitution in effect for the tree being walked: nil
		// while walking the source, an instantiation's binding while draining.
		bindings map[syntax.GenericArgKey]syntax.Type
		queue    []genericWork
		// walked is every (owner, bindings) pair already queued. Earlier passes
		// create concretes as a side effect of [syntax.TypeGeneric.Decl], so
		// whether a concrete exists says nothing about whether it was walked.
		walked map[walkedKey][]map[syntax.GenericArgKey]syntax.Type
		// templates is every generic declaration in the source.
		templates []syntax.DeclGeneric

		// target is the template whose open types are being recorded, or nil
		// where none are. Only generic classes, abstracts and interfaces record
		// them, and generic methods within those.
		target syntax.GenericOwner

		functionTypes *syntax.TypeSet
		threadTypes   *syntax.TypeSet
		pointerTypes  *syntax.TypeSet
		tupleTypes    *syntax.TypeSet
	}
)

// bindingsHash is an order-independent hash of a bindings map.
func bindingsHash(bindings map[syntax.GenericArgKey]syntax.Type) uint64 {
	var h uint64
	for k, t := range bindings {
		h += base.Hash(base.FNV(k.Name), k.Owner.Hash(), t.Hash())
	}
	return h
}

func sameBindings(a, b map[syntax.GenericArgKey]syntax.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for k, t := range a {
		other, ok := b[k]
		if !ok || !t.Equals(other) {
			return false
		}
	}
	return true
}

// markWalked records that owner is walked under bindings, reporting false if it
// already was.
func (v *monomorphizer) markWalked(owner any, bindings map[syntax.GenericArgKey]syntax.Type) bool {
	key := walkedKey{owner: owner, hash: bindingsHash(bindings)}
	for _, seen := range v.walked[key] {
		if sameBindings(seen, bindings) {
			return false
		}
	}
	v.walked[key] = append(v.walked[key], bindings)
	return true
}

// drain walks queued instantiations until none remain. A ground concrete that
// an earlier pass created without the source naming it is queued too, so every
// instantiation codegen will emit has been walked.
func (v *monomorphizer) drain() {
	for {
		for len(v.queue) > 0 {
			w := v.queue[len(v.queue)-1]
			v.queue = v.queue[:len(v.queue)-1]
			v.bindings = w.bindings
			if w.method != nil {
				w.method.Accept(v)
			} else {
				v.walkInstance(w.decl)
			}
		}
		v.bindings = nil

		for _, template := range v.templates {
			for _, entry := range template.Concretes().All() {
				if t, ok := entry.Key.(*syntax.TypeGeneric); ok && !t.ContainsGenericArg() {
					v.materialize(t)
				}
			}
		}
		if len(v.queue) == 0 {
			return
		}
	}
}

// materialize forces the concrete decl for a ground instantiation into
// existence and queues its template for walking under the binding that
// produced it.
func (v *monomorphizer) materialize(t *syntax.TypeGeneric) {
	gd, ok := t.Type.Decl().(syntax.DeclGeneric)
	if !ok || gd.Generics() == nil {
		return
	}
	gd.GetConcreteDecl(t)

	bindings := make(map[syntax.GenericArgKey]syntax.Type, len(t.ParameterTypes))
	i := 0
	for _, entry := range gd.Generics().All() {
		bindings[entry.Key] = t.ParameterTypes[i]
		i++
	}
	if v.markWalked(gd, bindings) {
		v.queue = append(v.queue, genericWork{decl: gd, bindings: bindings})
	}
}

// materializeMethod forces the concrete instantiation of a generic static
// method for the given ground type arguments into existence and queues its
// signature and body for walking under the resolved bindings.
//
// stampConcrete is true only when the call site's type arguments were already
// ground in the source. Open sites must leave e.Concrete unset so codegen
// emits OpPushGenericStaticSlot against the receiver's binding.
func (v *monomorphizer) materializeMethod(e *syntax.ExprType, typeArgs []syntax.Type, stampConcrete bool) {
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
	if stampConcrete {
		e.Concrete = concrete
	}
	if v.markWalked(method, concrete.Bindings()) {
		v.queue = append(v.queue, genericWork{method: method, bindings: concrete.Bindings()})
	}
}

// visitType handles a type written in the source.
func (v *monomorphizer) visitType(t syntax.Type) {
	t.Accept(materializer{v})
	v.recordOpen(t)
	v.recordGround(t)
}

// recordingOpen reports whether the tree being walked is a template whose open
// types are recorded. A template is walked once with no binding, so an
// instantiation's walk records nothing open.
func (v *monomorphizer) recordingOpen() bool {
	return v.target != nil && v.bindings == nil
}

// recordOpen records t and every open type nested within it on the template
// being walked. Expression result types go through here alone: they are what
// codegen pushes for intermediate values, so they have to be enumerated even
// when they are never written.
func (v *monomorphizer) recordOpen(t syntax.Type) {
	if t != nil && v.recordingOpen() {
		t.Accept(openTypeRecorder{v.target})
	}
}

// recordGround records every function, thread, pointer and tuple type within t
// that is ground under the binding in effect.
func (v *monomorphizer) recordGround(t syntax.Type) {
	if t != nil {
		t.Accept(groundTypeRecorder{v})
	}
}

// recordAddressed records the pointer through which a method of an inline
// value with a home is called, which is a type the call names too.
func (v *monomorphizer) recordAddressed(receiver syntax.Type) {
	if receiver == nil || syntax.ReachedThroughAddress(receiver) {
		return
	}
	if _, ok := syntax.StripConst(receiver).(syntax.TypeResolvable); ok {
		v.recordOpen(syntax.NewWrappedType(syntax.TypeKindPointer, receiver))
	}
}

// collectsOpenTypes reports whether d's open types are recorded on it. Structs
// do not dynamically dispatch and carry their own type-specific logic, and
// enums and namespaces are not instantiated through an open receiver.
func collectsOpenTypes(d syntax.Decl) bool {
	switch d.(type) {
	case *syntax.DeclClass, *syntax.DeclAbstract, *syntax.DeclInterface:
		return true
	}
	return false
}

// visitDecl walks a source declaration and the declarations nested in it.
func (v *monomorphizer) visitDecl(d syntax.Decl) {
	previous := v.target
	v.target = nil
	if gd, ok := d.(syntax.DeclGeneric); ok && gd.Generics() != nil {
		v.templates = append(v.templates, gd)
		if collectsOpenTypes(d) {
			v.enter(gd)
		}
	}

	v.walkHeader(d)
	for _, field := range d.StaticFields() {
		field.Accept(v)
	}
	v.walkInstanceFields(d)
	if _, ok := d.(syntax.DeclSubtypeable); ok && v.recordingOpen() {
		v.visitInheritedFields(d)
	}

	v.target = nil
	for child := range d.Decls() {
		child.Accept(v)
	}
	v.target = previous
}

// enter makes d the template being recorded into and records d's own type and
// receiver pointer. Every instance method pushes its receiver under that type
// and reads `this` as a pointer to it, whether or not the source names either.
func (v *monomorphizer) enter(d syntax.DeclGeneric) {
	v.target = d
	declType := d.(syntax.DeclTypeable).DeclType()
	v.recordOpen(declType)
	v.recordOpen(syntax.NewWrappedType(syntax.TypeKindPointer, declType))
}

// walkInstance walks every position of d that may name d's generic arguments.
func (v *monomorphizer) walkInstance(d syntax.Decl) {
	v.walkHeader(d)
	v.walkInstanceFields(d)
}

// walkHeader walks d's generic bounds and base type.
func (v *monomorphizer) walkHeader(d syntax.Decl) {
	if gd, ok := d.(syntax.DeclGeneric); ok && gd.Generics() != nil {
		for _, entry := range gd.Generics().All() {
			if entry.Value == nil {
				continue
			}
			for _, bound := range entry.Value.Bounds() {
				v.visitType(bound)
			}
		}
	}
	if sd, ok := d.(syntax.DeclSubtypeable); ok && sd.BaseDeclType() != nil {
		v.visitType(sd.BaseDeclType())
	}
}

// walkInstanceFields walks d's receiver and instance fields.
func (v *monomorphizer) walkInstanceFields(d syntax.Decl) {
	v.recordReceiver(d)
	for _, field := range d.InstanceFields() {
		field.Accept(v)
	}
}

// recordReceiver records the pointer a receiver slot holds. A receiver is a
// pointer whatever the declaration's width, and every instance method and
// initializer is entered through one whether or not the program writes T*. A
// namespace has no instances, so it has no receiver.
func (v *monomorphizer) recordReceiver(d syntax.Decl) {
	if d.Kind() == syntax.KindNamespace {
		return
	}
	if typeable, ok := d.(syntax.DeclTypeable); ok {
		v.recordGround(syntax.NewWrappedType(syntax.TypeKindPointer, typeable.DeclType()))
	}
}

// visitInheritedFields records the types of the fields d inherits, which its
// initializer and the bodies it inherits name under d's own arguments: a
// Named[X] based on Arr[X] holds Arr's A* as an X*.
func (v *monomorphizer) visitInheritedFields(d syntax.Decl) {
	own := map[string]bool{}
	for name := range d.InstanceFields() {
		own[name] = true
	}
	for _, entry := range d.AllInstanceFields().All() {
		if own[entry.Key] {
			continue
		}
		if field, ok := entry.Value.(syntax.FieldTypeable); ok {
			v.recordOpen(field.FieldType())
		}
	}
}

func (v *monomorphizer) VisitFile(f *syntax.File) error {
	return f.Decl.Accept(v)
}

func (v *monomorphizer) VisitImport(*syntax.Import) error {
	return nil
}

func (v *monomorphizer) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	v.visitDecl(d)
	return nil
}

func (v *monomorphizer) VisitDeclClass(d *syntax.DeclClass) error {
	v.visitDecl(d)
	return nil
}

func (v *monomorphizer) VisitDeclEnum(d *syntax.DeclEnum) error {
	v.visitDecl(d)
	return nil
}

func (v *monomorphizer) VisitDeclInterface(d *syntax.DeclInterface) error {
	v.visitDecl(d)
	return nil
}

func (v *monomorphizer) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	v.visitDecl(d)
	return nil
}

// VisitDeclAlias implements [syntax.FieldVisitor]. Every reference to an
// alias has been replaced by the type it names, so it holds nothing for this
// pass.
func (v *monomorphizer) VisitDeclAlias(*syntax.DeclAlias) error {
	return nil
}

func (v *monomorphizer) VisitDeclStruct(d *syntax.DeclStruct) error {
	v.visitDecl(d)
	return nil
}

func (v *monomorphizer) VisitFieldMember(f *syntax.FieldMember) error {
	v.visitType(f.FieldType())
	if f.Expr != nil {
		f.Expr.Accept(v)
	}
	return nil
}

// VisitFieldMethod implements [syntax.FieldVisitor]. A generic method within a
// recorded template records its open types on itself rather than on the
// template, since it is resolved against its own bindings at every call site.
func (v *monomorphizer) VisitFieldMethod(f *syntax.FieldMethod) error {
	if f.Generics() != nil && v.target != nil {
		previous := v.target
		v.target = f
		defer func() { v.target = previous }()
	}
	v.visitType(f.FieldType())
	if f.Body != nil {
		f.Body.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitFieldEnum(f *syntax.FieldEnum) error {
	v.visitType(f.Type())
	for _, parameter := range f.Parameters {
		parameter.Accept(v)
	}
	if f.Value != nil {
		f.Value.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitStmtLoopJump(*syntax.StmtLoopJump) error {
	return nil
}

func (v *monomorphizer) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Expr != nil {
		s.Expr.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitStmtFor(s *syntax.StmtFor) error {
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

func (v *monomorphizer) VisitStmtSwitch(s *syntax.StmtSwitch) error {
	s.Expr.Accept(v)
	for _, switchCase := range s.Cases {
		for _, value := range switchCase.Values {
			value.Accept(v)
		}
	}
	for body := range s.Bodies() {
		body.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitStmtIf(s *syntax.StmtIf) error {
	s.Condition.Accept(v)
	s.Then.Accept(v)
	if s.Else != nil {
		s.Else.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitStmtLabel(s *syntax.StmtLabel) error {
	s.LoopStmt.Accept(v)
	return nil
}

func (v *monomorphizer) VisitStmtBlock(s *syntax.StmtBlock) error {
	for _, stmt := range s.Stmts {
		stmt.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitStmtVariable(s *syntax.StmtVariable) error {
	v.visitType(s.Type)
	if s.Expr != nil {
		s.Expr.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitStmtUnpack(s *syntax.StmtUnpack) error {
	for _, variable := range s.Variables {
		variable.Accept(v)
	}
	if s.Expr != nil {
		s.Expr.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitExprCast(e *syntax.ExprCast) error {
	v.visitType(e.Type)
	e.Expr.Accept(v)
	return nil
}

func (v *monomorphizer) VisitExprField(e *syntax.ExprField) error {
	e.Expr.Accept(v)
	if v.recordingOpen() {
		v.recordOpen(e.ComputedType)
		v.recordAddressed(e.Expr.ResolvedType())
	}
	return nil
}

func (v *monomorphizer) VisitExprFields(e *syntax.ExprFields) error {
	e.Head.Accept(v)
	if !v.recordingOpen() {
		return nil
	}
	receiver := e.Head.ResolvedType()
	for _, t := range e.ComputedTypes {
		v.recordOpen(t)
		v.recordAddressed(receiver)
		receiver = t
	}
	return nil
}

func (v *monomorphizer) VisitExprLambda(e *syntax.ExprLambda) error {
	v.visitType(e.Type)
	if e.Body != nil {
		e.Body.Accept(v)
	}
	return nil
}

// VisitExprNAry implements [syntax.ExprVisitor]. An operator can arrive at a
// type nothing in the source spells, as taking an address does, and as indexing
// an array does by decaying it to a pointer.
func (v *monomorphizer) VisitExprNAry(e *syntax.ExprNAry) error {
	for _, arg := range e.Exprs {
		arg.Accept(v)
	}
	v.recordOpen(e.ComputedType)
	v.recordGround(e.ResolvedType())
	return nil
}

func (v *monomorphizer) VisitExprNative(e *syntax.ExprNative) error {
	v.recordOpen(e.ComputedType)
	return nil
}

// VisitExprNew implements [syntax.ExprVisitor]. A program may allocate a type
// it never writes a pointer to, and the id of the pointer the allocation
// evaluates to is asked for all the same by whatever it is stored into.
func (v *monomorphizer) VisitExprNew(e *syntax.ExprNew) error {
	v.visitType(e.Type)
	v.recordGround(e.ResolvedType())
	if e.Size != nil {
		e.Size.Accept(v)
	}
	if e.Tail != nil {
		e.Tail.Accept(v)
	}
	return nil
}

func (v *monomorphizer) VisitExprSymbol(e *syntax.ExprSymbol) error {
	v.recordOpen(e.ComputedType)
	return nil
}

// VisitExprType implements [syntax.ExprVisitor].
//
// Call sites written with an open class-level T, such as Arr.Mark's
// Arrs::[A]MarkAll, share one ExprType across every instantiation of the
// enclosing decl. Mapping under the current binding can make those arguments
// ground for materialization, but the site itself stays open: which concrete
// runs is a question for the receiver at run time, and stamping e.Concrete
// would pin every instantiation to whichever binding walked last.
func (v *monomorphizer) VisitExprType(e *syntax.ExprType) error {
	v.visitType(e.Type)
	v.recordOpen(e.ComputedType)
	if len(e.TypeArgs) == 0 {
		return nil
	}

	siteOpen, ground := false, true
	mapped := make([]syntax.Type, len(e.TypeArgs))
	for i, t := range e.TypeArgs {
		if t.ContainsGenericArg() {
			siteOpen = true
		}
		mapped[i] = t.Map(v.bindings)
		if mapped[i].ContainsGenericArg() {
			ground = false
		}
	}
	for _, t := range mapped {
		t.Accept(materializer{v})
	}
	if ground {
		v.materializeMethod(e, mapped, !siteOpen)
	}
	return nil
}

// materializer materializes every instantiation within a type that is ground
// under the binding in effect.
type materializer struct {
	v *monomorphizer
}

func (m materializer) VisitTypeArray(t *syntax.TypeArray) error {
	return t.Type.Accept(m)
}

func (m materializer) VisitTypeFunction(t *syntax.TypeFunction) error {
	t.ReturnType.Accept(m)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(m)
	}
	return nil
}

func (m materializer) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	if applied, ok := t.Map(m.v.bindings).(*syntax.TypeGeneric); ok && !applied.ContainsGenericArg() {
		m.v.materialize(applied)
	}
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(m)
	}
	return nil
}

func (m materializer) VisitTypeGenericArg(*syntax.TypeGenericArg) error {
	return nil
}

func (m materializer) VisitTypePrimitive(*syntax.TypePrimitive) error {
	return nil
}

func (m materializer) VisitTypeReference(*syntax.TypeReference) error {
	return nil
}

func (m materializer) VisitTypeTailed(t *syntax.TypeTailed) error {
	return t.Type.Accept(m)
}

func (m materializer) VisitTypeTuple(t *syntax.TypeTuple) error {
	for _, element := range t.Types {
		element.Accept(m)
	}
	return nil
}

func (m materializer) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	return t.Type.Accept(m)
}

// openTypeRecorder records on target every open type within a type. A type
// is open when a generic argument appears anywhere in it, and each nested open
// type needs an entry of its own: a body holding a Node[T]* names that whole
// type, not just the Node[T] or the T within it. A concrete resolves these by
// mapping each through its bindings, which is how codegen recovers the ids of
// a concrete's dependent instantiations.
type openTypeRecorder struct {
	target syntax.GenericOwner
}

func (r openTypeRecorder) record(t syntax.Type) {
	if t.ContainsGenericArg() {
		r.target.GenericTypes().Add(t)
	}
}

func (r openTypeRecorder) VisitTypeArray(t *syntax.TypeArray) error {
	r.record(t)
	return t.Type.Accept(r)
}

func (r openTypeRecorder) VisitTypeFunction(t *syntax.TypeFunction) error {
	r.record(t)
	t.ReturnType.Accept(r)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(r)
	}
	return nil
}

func (r openTypeRecorder) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	r.record(t)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(r)
	}
	return nil
}

func (r openTypeRecorder) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	r.record(t)
	return nil
}

func (r openTypeRecorder) VisitTypePrimitive(*syntax.TypePrimitive) error {
	return nil
}

func (r openTypeRecorder) VisitTypeReference(*syntax.TypeReference) error {
	return nil
}

func (r openTypeRecorder) VisitTypeTailed(t *syntax.TypeTailed) error {
	r.record(t)
	return t.Type.Accept(r)
}

func (r openTypeRecorder) VisitTypeTuple(t *syntax.TypeTuple) error {
	r.record(t)
	for _, element := range t.Types {
		element.Accept(r)
	}
	return nil
}

// VisitTypeWrapped implements [syntax.TypeVisitor]. A variadic parameter is
// written as `T...` but binds inside the body as the pointer `T*` to the
// arguments the caller packed, so the pointer is recorded too.
func (r openTypeRecorder) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	r.record(t)
	if t.Kind() == syntax.TypeKindVariadic {
		r.record(syntax.NewWrappedType(syntax.TypeKindPointer, t.Type))
	}
	return t.Type.Accept(r)
}

// groundTypeRecorder records every function, thread, pointer and tuple type
// within a type that is ground once the binding in effect is substituted in.
type groundTypeRecorder struct {
	v *monomorphizer
}

// ground reports t under the binding in effect, and whether it is ground.
func (r groundTypeRecorder) ground(t syntax.Type) (syntax.Type, bool) {
	applied := t.Map(r.v.bindings)
	return applied, !applied.ContainsGenericArg()
}

func (r groundTypeRecorder) VisitTypeArray(t *syntax.TypeArray) error {
	return t.Type.Accept(r)
}

func (r groundTypeRecorder) VisitTypeFunction(t *syntax.TypeFunction) error {
	if applied, ok := r.ground(t); ok {
		r.v.functionTypes.Add(applied)
	}
	t.ReturnType.Accept(r)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(r)
	}
	return nil
}

func (r groundTypeRecorder) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(r)
	}
	return nil
}

func (r groundTypeRecorder) VisitTypeGenericArg(*syntax.TypeGenericArg) error {
	return nil
}

func (r groundTypeRecorder) VisitTypePrimitive(*syntax.TypePrimitive) error {
	return nil
}

func (r groundTypeRecorder) VisitTypeReference(*syntax.TypeReference) error {
	return nil
}

func (r groundTypeRecorder) VisitTypeTailed(t *syntax.TypeTailed) error {
	return t.Type.Accept(r)
}

func (r groundTypeRecorder) VisitTypeTuple(t *syntax.TypeTuple) error {
	if applied, ok := r.ground(t); ok {
		r.v.tupleTypes.Add(applied)
	}
	for _, element := range t.Types {
		element.Accept(r)
	}
	return nil
}

// VisitTypeWrapped implements [syntax.TypeVisitor]. A variadic records a
// pointer, because that is what its slot holds: the arguments are packed
// before the call and the frame is handed a reference to them.
func (r groundTypeRecorder) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	switch t.Kind() {
	case syntax.TypeKindThread:
		if applied, ok := r.ground(t); ok {
			r.v.threadTypes.Add(applied)
		}
	case syntax.TypeKindPointer, syntax.TypeKindVariadic:
		if applied, ok := r.ground(syntax.ParameterBindingType(t)); ok {
			r.v.pointerTypes.Add(applied)
		}
	}
	return t.Type.Accept(r)
}
