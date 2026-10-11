package semantic

import (
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

type memberCycleDetectorVisitor struct {
	cycle   base.Set[syntax.Decl]
	visited base.Set[syntax.Decl]

	ownerDecl    syntax.Decl
	fieldName    string
	cyclicFields *base.StaticOrderedMap[syntax.Decl, []string]
}

func detectMemberCycles(ast *syntax.AST) []error {
	v := &memberCycleDetectorVisitor{
		cycle:        base.Set[syntax.Decl]{},
		visited:      base.Set[syntax.Decl]{},
		cyclicFields: base.NewStaticOrderedMap[syntax.Decl, []string](),
	}
	for _, entry := range ast.CodeFiles.All() {
		v.VisitFile(entry.Value)
	}

	errs := make([]error, 0, v.cyclicFields.Len())
	for _, entry := range v.cyclicFields.All() {
		errs = append(errs, errfAt(entry.Key, entry.Key.Start(),
			"member cycle detected: %s.{%s}", entry.Key.Name(), strings.Join(entry.Value, ", ")))
	}
	return errs
}

func (v *memberCycleDetectorVisitor) VisitFile(f *syntax.File) error {
	f.Decl.Accept(v)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitImport(i *syntax.Import) error {
	return nil
}

func (v *memberCycleDetectorVisitor) visitInnerDecls(d syntax.Decl) {
	prevCycle := v.cycle
	v.cycle = base.Set[syntax.Decl]{}
	for decl := range d.Decls() {
		decl.Accept(v)
	}
	v.cycle = prevCycle
}

func (v *memberCycleDetectorVisitor) visitDeclInstances(d syntax.Decl) {
	if _, ok := v.visited[d]; ok {
		return
	} else if _, ok := v.cycle[d]; ok {
		fields, _ := v.cyclicFields.Get(v.ownerDecl)
		v.cyclicFields.Set(v.ownerDecl, append(fields, v.fieldName))
		return
	}
	v.cycle[d] = struct{}{}

	for name, member := range d.InstanceMembers() {
		v.ownerDecl = d
		v.fieldName = name
		member.Accept(v)
	}

	delete(v.cycle, d)
	v.visited[d] = struct{}{}

	v.visitInnerDecls(d)
}

func (v *memberCycleDetectorVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	v.visitDeclInstances(d)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	v.visitDeclInstances(d)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	v.visitDeclInstances(d)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	v.visitInnerDecls(d)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	v.visitInnerDecls(d)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	v.visitDeclInstances(d)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	return nil
}

func (v *memberCycleDetectorVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	f.FieldType().Accept(v)
	return nil
}

func (v *memberCycleDetectorVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	return nil
}

// VisitTypeArray implements [syntax.TypeVisitor].
func (v *memberCycleDetectorVisitor) VisitTypeArray(t *syntax.TypeArray) error {
	t.Type.Accept(v)
	return nil
}

// VisitTypeFunction implements [syntax.TypeVisitor].
func (v *memberCycleDetectorVisitor) VisitTypeFunction(t *syntax.TypeFunction) error {
	return nil
}

// VisitTypeGeneric implements [syntax.TypeVisitor].
func (v *memberCycleDetectorVisitor) VisitTypeGeneric(t *syntax.TypeGeneric) error {
	t.Decl().Accept(v)
	return nil
}

// VisitTypeGenericArg implements [syntax.TypeVisitor].
func (v *memberCycleDetectorVisitor) VisitTypeGenericArg(t *syntax.TypeGenericArg) error {
	return nil
}

// VisitTypePrimitive implements [syntax.TypeVisitor].
func (v *memberCycleDetectorVisitor) VisitTypePrimitive(t *syntax.TypePrimitive) error {
	return nil
}

// VisitTypeReference implements [syntax.TypeVisitor].
func (v *memberCycleDetectorVisitor) VisitTypeReference(t *syntax.TypeReference) error {
	t.Decl().Accept(v)
	return nil
}

// VisitTypeTailed implements [syntax.TypeVisitor].
func (v *memberCycleDetectorVisitor) VisitTypeTailed(t *syntax.TypeTailed) error {
	t.Type.Accept(v)
	return nil
}

// VisitTypeWrapped implements [syntax.TypeVisitor]. Pointer, thread and
// variadic each put their element behind indirection, so following one is not
// a cycle in the member graph. Const adds no indirection — a const T is laid
// out exactly as T — so a const member is followed in through its inner type.
func (v *memberCycleDetectorVisitor) VisitTypeWrapped(t *syntax.TypeWrapped) error {
	if t.Kind() == syntax.TypeKindConst {
		t.Type.Accept(v)
	}
	return nil
}
