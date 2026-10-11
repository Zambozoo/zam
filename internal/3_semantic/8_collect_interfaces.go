package semantic

import (
	"iter"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// collectInterfaces records on every class, abstract and interface each
// interface it implements.
//
// Implementing is structural, and a subtype check already decides it: converting
// a class to an interface tests the one against the other and records the answer
// on the way past. Being asked is not enough, though, because the record lands
// on the declaration that was asked, not on the one the instance turns out to
// be. Reaching an interface through a base
//
//	Base* b = derived;
//	I* i = b;
//
// records I on Base, while a call through i dispatches on the table built for
// the class the instance actually is. That table would not have been given I's
// methods, and a dispatch table is minimal, so the call would not fail to find
// them: it would find whatever else was placed where they would have gone.
//
// Running every declaration against every interface here means a table is
// complete before it is built, whoever asked and whatever they asked through.
//
// Abstracts and interfaces are run too, though nothing dispatches on a table of
// theirs. What they get out of it is the supertype set beside that table: an
// abstract's or an interface's type id is a thing the source can name and ask
// about, and answering means reading the record that id points at. Left
// unmatched, those records would hold only whatever some resolution happened to
// record on them along the way, which is the same partial answer this pass
// exists to replace.
//
// A generic interface is matched by its instantiations rather than by itself.
// I[T] is not something an instance can be, and asking whether a class
// implements it would mean solving for what T stands for, which is inference and
// not what a subtype check does. I[int] asks nothing: its methods are the
// template's with the binding substituted in, so comparing against it is the
// same comparison as for any other interface.
//
// A generic class is matched both ways for the same reason. What C[T] implements
// whatever T stands for is every instantiation's, and is recorded once on the
// template; what holds only once T is bound — a T-returning method implementing
// Getter[int] at C[int] and nowhere else — is recorded on the instantiation.
func collectInterfaces(ast *syntax.AST) {
	c := &interfaceCollector{}
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Decl.Accept(c)
	}

	for _, implementor := range c.implementors {
		for _, iface := range c.interfaces {
			syntax.PutInterfaceIfImplemented(implementor, iface)
		}
	}
}

// interfaceCollector gathers the two sides to be matched.
type interfaceCollector struct {
	implementors []syntax.DeclInheritable
	interfaces   []syntax.DeclTypeable
}

// collect records d and its ground instantiations as things to be matched
// against every interface. A template is added before its instantiations so that
// what holds whatever its arguments are is recorded once, on the template, and
// no instantiation records it again.
func (c *interfaceCollector) collect(d syntax.DeclInheritable) {
	c.implementors = append(c.implementors, d)
	generic, ok := d.(syntax.DeclGeneric)
	if !ok {
		return
	}
	for concrete := range groundConcretes(generic) {
		if inheritable, ok := concrete.(syntax.DeclInheritable); ok {
			c.implementors = append(c.implementors, inheritable)
		}
	}
}

// groundConcretes iterates the instantiations of d that name one type each. An
// instantiation can be left open by the tree it was written in — a List[E]
// inside ArrList[E] — and one of those is no more a single type than the
// template is. Monomorphization has already run, so every instantiation the
// program can reach is here.
func groundConcretes(d syntax.DeclGeneric) iter.Seq[syntax.DeclTypeable] {
	return func(yield func(syntax.DeclTypeable) bool) {
		if d.Generics() == nil {
			return
		}
		for _, entry := range d.Concretes().All() {
			if !entry.Key.ContainsGenericArg() && !yield(entry.Value) {
				return
			}
		}
	}
}

func (c *interfaceCollector) VisitFile(f *syntax.File) error {
	f.Decl.Accept(c)
	return nil
}

func (c *interfaceCollector) VisitImport(i *syntax.Import) error {
	return nil
}

func (c *interfaceCollector) visitNested(d syntax.Decl) {
	for decl := range d.Decls() {
		decl.Accept(c)
	}
}

func (c *interfaceCollector) VisitDeclClass(d *syntax.DeclClass) error {
	c.collect(d)
	c.visitNested(d)
	return nil
}

func (c *interfaceCollector) VisitDeclInterface(d *syntax.DeclInterface) error {
	c.collect(d)
	if d.Generics() == nil {
		c.interfaces = append(c.interfaces, d)
	} else {
		for concrete := range groundConcretes(d) {
			c.interfaces = append(c.interfaces, concrete)
		}
	}

	c.visitNested(d)
	return nil
}

func (c *interfaceCollector) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	c.collect(d)
	c.visitNested(d)
	return nil
}

func (c *interfaceCollector) VisitDeclEnum(d *syntax.DeclEnum) error {
	c.visitNested(d)
	return nil
}

func (c *interfaceCollector) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	c.visitNested(d)
	return nil
}

func (c *interfaceCollector) VisitDeclStruct(d *syntax.DeclStruct) error {
	c.visitNested(d)
	return nil
}

func (c *interfaceCollector) VisitFieldEnum(f *syntax.FieldEnum) error {
	return nil
}

func (c *interfaceCollector) VisitFieldMember(f *syntax.FieldMember) error {
	return nil
}

func (c *interfaceCollector) VisitFieldMethod(f *syntax.FieldMethod) error {
	return nil
}
