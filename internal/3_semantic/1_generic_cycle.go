package semantic

import (
	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

type (
	// expansionGraph collects expansion edges keyed by source parameter. The bool
	// records whether any from->to edge is expansive (OR-merged, so a non-expansive
	// edge never clears an expansive one).
	expansionGraph map[syntax.GenericArgKey]map[syntax.GenericArgKey]bool

	// expansionEdge is a directed edge in the generic type-parameter expansion
	// graph: a constructed type flows the parameter `from` into the formal-parameter
	// position `to`, expansively when `from` is nested inside a wrapper there.
	expansionEdge struct {
		From      syntax.GenericArgKey
		To        syntax.GenericArgKey
		Expansive bool
	}

	genericArgLeaf struct {
		from      syntax.GenericArgKey
		expansive bool
	}

	// genericArgVisitor computes the generic argument leaves for a type.
	genericArgVisitor struct {
		depth  int
		leaves []genericArgLeaf
	}

	// genericCycleDetectorVisitor computes the generic type-parameter expansion edges induced
	// by each type. Only a constructed generic type contributes edges; every other
	// type yields nothing, and nested constructed types are reached via Children.
	genericCycleDetectorVisitor struct {
		graph expansionGraph
	}
)

func (g expansionGraph) add(e expansionEdge) {
	tos := g[e.From]
	if tos == nil {
		tos = map[syntax.GenericArgKey]bool{}
		g[e.From] = tos
	}
	tos[e.To] = tos[e.To] || e.Expansive
}

func (g expansionGraph) reachable(start, target syntax.GenericArgKey, visited base.Set[syntax.GenericArgKey]) bool {
	if start == target {
		return true
	}
	if _, ok := visited[start]; ok {
		return false
	}
	visited[start] = struct{}{}
	for next := range g[start] {
		if g.reachable(next, target, visited) {
			return true
		}
	}
	return false
}

func detectGenericCycles(ast *syntax.AST) []error {
	v := &genericCycleDetectorVisitor{
		graph: expansionGraph{},
	}
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Accept(v)
	}

	var errs []error
	reported := base.Set[syntax.DeclGeneric]{}
	for from, tos := range v.graph {
		fromDecl, ok := from.Owner.(syntax.DeclGeneric)
		if !ok {
			// Method-level parameters never participate in the recursion the
			// cycle detector guards against: a method is only instantiated
			// at a call site under ground arguments, not nested inside a
			// declaration whose instantiation set has to close.
			continue
		}
		if _, ok := reported[fromDecl]; ok {
			continue
		}
		for to, expansive := range tos {
			if !expansive {
				continue
			}
			if v.graph.reachable(to, from, base.Set[syntax.GenericArgKey]{}) {
				reported[fromDecl] = struct{}{}
				errs = append(errs, errfAt(fromDecl, fromDecl.Start(),
					"recursive generic expansion detected at %q", fromDecl.Name()))
				break
			}
		}
	}

	return errs
}

func (v *genericCycleDetectorVisitor) VisitFile(f *syntax.File) error {
	f.Decl.Accept(v)
	return nil
}

func (v *genericCycleDetectorVisitor) VisitImport(i *syntax.Import) error {
	return nil
}

func (v *genericCycleDetectorVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	if d.Generics() != nil {
		for _, entry := range d.Generics().All() {
			if entry.Value == nil {
				continue
			}
			for _, parameter := range entry.Value.Bounds() {
				parameter.Accept(v)
			}
		}
	}
	if d.BaseDeclType() != nil {
		d.BaseDeclType().Accept(v)
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
	return nil
}

func (v *genericCycleDetectorVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	if d.Generics() != nil {
		for _, entry := range d.Generics().All() {
			if entry.Value == nil {
				continue
			}
			for _, parameter := range entry.Value.Bounds() {
				parameter.Accept(v)
			}
		}
	}
	if d.BaseDeclType() != nil {
		d.BaseDeclType().Accept(v)
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
	return nil
}

func (v *genericCycleDetectorVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	for _, field := range d.StaticFields() {
		field.Accept(v)
	}
	for _, field := range d.InstanceFields() {
		field.Accept(v)
	}
	for child := range d.Decls() {
		child.Accept(v)
	}
	return nil
}

func (v *genericCycleDetectorVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	if d.Generics() != nil {
		for _, entry := range d.Generics().All() {
			if entry.Value == nil {
				continue
			}
			for _, parameter := range entry.Value.Bounds() {
				parameter.Accept(v)
			}
		}
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
	return nil
}

func (v *genericCycleDetectorVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	for _, field := range d.StaticFields() {
		field.Accept(v)
	}
	for _, field := range d.InstanceFields() {
		field.Accept(v)
	}
	for child := range d.Decls() {
		child.Accept(v)
	}
	return nil
}

func (v *genericCycleDetectorVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	if d.Generics() != nil {
		for _, entry := range d.Generics().All() {
			if entry.Value == nil {
				continue
			}
			for _, parameter := range entry.Value.Bounds() {
				parameter.Accept(v)
			}
		}
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
	return nil
}

func (v *genericCycleDetectorVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	f.FieldType().Accept(v)
	if f.Expr != nil {
		f.Expr.Accept(v)
	}
	return nil
}

func (v *genericCycleDetectorVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	f.FieldType().Accept(v)
	if f.Body != nil {
		f.Body.Accept(v)
	}
	return nil
}

func (v *genericCycleDetectorVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	f.Type().Accept(v)
	for _, parameter := range f.Parameters {
		parameter.Accept(v)
	}
	if f.Value != nil {
		f.Value.Accept(v)
	}
	return nil
}

// VisitStmtBlock implements [syntax.StmtVisitor].
func (v *genericCycleDetectorVisitor) VisitStmtBlock(s *syntax.StmtBlock) error {
	for _, stmt := range s.Stmts {
		stmt.Accept(v)
	}
	return nil
}

// VisitStmtExpr implements [syntax.StmtVisitor].
func (v *genericCycleDetectorVisitor) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Expr != nil {
		s.Expr.Accept(v)
	}
	return nil
}

// VisitStmtFor implements [syntax.StmtVisitor].
func (v *genericCycleDetectorVisitor) VisitStmtFor(s *syntax.StmtFor) error {
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

// VisitStmtIf implements [syntax.StmtVisitor].
func (v *genericCycleDetectorVisitor) VisitStmtIf(s *syntax.StmtIf) error {
	s.Condition.Accept(v)
	s.Then.Accept(v)
	if s.Else != nil {
		s.Else.Accept(v)
	}
	return nil
}

// VisitStmtLabel implements [syntax.StmtVisitor].
func (v *genericCycleDetectorVisitor) VisitStmtLabel(s *syntax.StmtLabel) error {
	s.LoopStmt.Accept(v)
	return nil
}

// VisitStmtLoopJump implements [syntax.StmtVisitor].
func (v *genericCycleDetectorVisitor) VisitStmtLoopJump(s *syntax.StmtLoopJump) error {
	return nil
}

// VisitStmtVariable implements [syntax.StmtVisitor].
func (v *genericCycleDetectorVisitor) VisitStmtVariable(s *syntax.StmtVariable) error {
	s.Type.Accept(v)
	if s.Expr != nil {
		s.Expr.Accept(v)
	}
	return nil
}

// VisitExprCast implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprCast(e *syntax.ExprCast) error {
	e.Type.Accept(v)
	e.Expr.Accept(v)
	return nil
}

// VisitExprField implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprField(e *syntax.ExprField) error {
	e.Expr.Accept(v)
	return nil
}

// VisitExprLambda implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprLambda(e *syntax.ExprLambda) error {
	e.Type.Accept(v)
	if e.Body != nil {
		e.Body.Accept(v)
	}
	return nil
}

// VisitExprNAry implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprNAry(e *syntax.ExprNAry) error {
	for _, arg := range e.Exprs {
		arg.Accept(v)
	}
	return nil
}

// VisitExprNative implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprNative(e *syntax.ExprNative) error {
	return nil
}

// VisitExprNew implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprNew(e *syntax.ExprNew) error {
	e.Type.Accept(v)
	if e.Size != nil {
		e.Size.Accept(v)
	}
	if e.Tail != nil {
		e.Tail.Accept(v)
	}
	return nil
}

// VisitExprSymbol implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprSymbol(e *syntax.ExprSymbol) error {
	return nil
}

// VisitExprType implements [syntax.ExprVisitor].
func (v *genericCycleDetectorVisitor) VisitExprType(e *syntax.ExprType) error {
	e.Type.Accept(v)
	return nil
}

func (v *genericCycleDetectorVisitor) VisitTypePrimitive(*syntax.TypePrimitive) error {
	return nil
}

func (v *genericCycleDetectorVisitor) VisitTypeArray(t *syntax.TypeArray) error {
	t.Type.Accept(v)
	return nil
}

func (v *genericCycleDetectorVisitor) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	t.Type.Accept(v)
	return nil
}

func (v *genericCycleDetectorVisitor) VisitTypeFunction(t *syntax.TypeFunction) error {
	t.ReturnType.Accept(v)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(v)
	}
	return nil
}

func (v *genericCycleDetectorVisitor) VisitTypeGenericArg(*syntax.TypeGenericArg) error {
	return nil
}

func (v *genericCycleDetectorVisitor) VisitTypeReference(*syntax.TypeReference) error {
	return nil
}

func (v *genericCycleDetectorVisitor) VisitTypeTailed(*syntax.TypeTailed) error {
	return nil
}

// VisitTypeGeneric pairs each argument's parameter leaves with the matching
// formal of the head decl, then recurses into the arguments so nested
// constructed generics emit their own edges.
func (v *genericCycleDetectorVisitor) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	genericDecl := t.Decl().(syntax.DeclGeneric)
	if genericDecl.Generics() == nil {
		return nil
	}

	for i, entry := range genericDecl.Generics().All() {
		to := syntax.GenericArgKey{Owner: genericDecl, Name: entry.Key.Name}

		var gav genericArgVisitor
		t.ParameterTypes[i].Accept(&gav)
		for _, leaf := range gav.leaves {
			v.graph.add(expansionEdge{From: leaf.from, To: to, Expansive: leaf.expansive})
		}

		t.ParameterTypes[i].Accept(v)
	}
	return nil
}

func (v *genericArgVisitor) VisitTypePrimitive(*syntax.TypePrimitive) error {
	return nil
}

func (v *genericArgVisitor) VisitTypeArray(t *syntax.TypeArray) error {
	v.depth++
	t.Type.Accept(v)
	v.depth--
	return nil
}

func (v *genericArgVisitor) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	v.depth++
	t.Type.Accept(v)
	v.depth--
	return nil
}

func (v *genericArgVisitor) VisitTypeFunction(t *syntax.TypeFunction) error {
	v.depth++
	t.ReturnType.Accept(v)
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(v)
	}
	v.depth--
	return nil
}

func (v *genericArgVisitor) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	from := syntax.GenericArgKey{
		Owner: t.Owner(),
		Name:  t.Name,
	}
	v.leaves = append(v.leaves, genericArgLeaf{
		from:      from,
		expansive: v.depth > 0,
	})
	return nil
}

func (v *genericArgVisitor) VisitTypeReference(*syntax.TypeReference) error {
	return nil
}

func (v *genericArgVisitor) VisitTypeTailed(*syntax.TypeTailed) error {
	return nil
}

func (v *genericArgVisitor) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	v.depth++
	for _, parameter := range t.ParameterTypes {
		parameter.Accept(v)
	}
	v.depth--
	return nil
}
