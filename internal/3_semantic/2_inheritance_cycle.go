package semantic

import (
	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

type inheritanceCycleDetectorVisitor struct {
	cycle   base.Set[syntax.Decl]
	visited base.Set[syntax.Decl]
	errs    []error
}

func detectInheritanceCycles(ast *syntax.AST) []error {
	v := &inheritanceCycleDetectorVisitor{
		cycle:   base.Set[syntax.Decl]{},
		visited: base.Set[syntax.Decl]{},
	}
	for _, entry := range ast.CodeFiles.All() {
		v.VisitFile(entry.Value)
	}
	return v.errs
}

func (v *inheritanceCycleDetectorVisitor) VisitFile(f *syntax.File) error {
	f.Decl.Accept(v)
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitImport(i *syntax.Import) error {
	return nil
}

func (v *inheritanceCycleDetectorVisitor) visitDeclSubtypeable(d syntax.DeclSubtypeable) {
	if _, ok := v.visited[d]; ok {
		return
	} else if _, ok := v.cycle[d]; ok {
		v.errs = append(v.errs, errfAt(d, d.Start(),
			"inheritance cycle detected: %s inherits from %s", d.Name(), d.BaseDeclType().Type.String()))
		return
	}
	v.cycle[d] = struct{}{}
	if d.BaseDeclType() != nil {
		d.BaseDeclType().Type.(syntax.TypeResolvable).Decl().Accept(v)
	}
	delete(v.cycle, d)
	v.visited[d] = struct{}{}

	prevCycle := v.cycle
	v.cycle = base.Set[syntax.Decl]{}
	for decl := range d.Decls() {
		decl.Accept(v)
	}
	v.cycle = prevCycle
}

func (v *inheritanceCycleDetectorVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	v.visitDeclSubtypeable(d)
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	v.visitDeclSubtypeable(d)
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	return nil
}

func (v *inheritanceCycleDetectorVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	return nil
}
