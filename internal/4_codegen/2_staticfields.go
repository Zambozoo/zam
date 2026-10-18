package codegen

import (
	"encoding/binary"
	"iter"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// instanceFields iterates the fields of the given kind an instance actually has,
// in layout order, which for a class or an abstract is what it inherits followed
// by what it declares. The runtime's member and method tables are indexed in this
// order, so everything that builds or reads them counts it rather than the
// declaration's own fields.
func instanceFields[T syntax.Field](d syntax.Decl) iter.Seq2[string, T] {
	return func(yield func(string, T) bool) {
		for _, entry := range d.AllInstanceFields().All() {
			if field, ok := entry.Value.(T); ok {
				if !yield(entry.Key, field) {
					return
				}
			}
		}
	}
}

// instanceFieldCount is how many entries [instanceFields] yields, which is how
// long the runtime table it fills has to be.
func instanceFieldCount[T syntax.Field](d syntax.Decl) int {
	var count int
	for range instanceFields[T](d) {
		count++
	}
	return count
}

// renumberInstanceFields assigns each declaration's instance fields the indices
// its runtime tables are keyed by, members and methods counted separately in
// layout order.
//
// The syntax pass numbers a declaration's own fields from zero, which is only
// right for a declaration that inherits nothing: an inherited field's index is
// not knowable until the base chain is resolved. Renumbering here gives every
// field the position its layout puts it in, so an index taken from a base type
// reaches the same field on a subclass, and an override lands on the index its
// base method was called through.
func renumberInstanceFields(d syntax.Decl) {
	var members, methods int
	for _, entry := range d.AllInstanceFields().All() {
		field, ok := entry.Value.(syntax.FieldIndexable)
		if !ok {
			continue
		}
		if field.Kind() == syntax.FieldKindMethod {
			*field.Index() = methods
			methods++
		} else {
			*field.Index() = members
			members++
		}
	}

	for child := range d.Decls() {
		renumberInstanceFields(child)
	}
}

// staticTypesandTypeDefinitionsPopulatorVisitor walks every declaration after the skeleton
// pass and fills the referential fields of each type definition — member and
// method type instances, interface lookup tables, and generic concrete maps. In
// the same traversal it lays out the declaration's static members and methods
// into the static region, recording each one's byte offset in the flat static
// instance maps and its slot index back on the field for the later operations
// pass. Static members and methods share a single running offset so their
// instances never overlap; the offset accumulates in declaration-then-field
// order across the whole traversal.
type staticTypesandTypeDefinitionsPopulatorVisitor struct {
	typeResolver
	ast    *syntax.AST
	reader *codegenReader
	offset uint64
}

// typeResolver turns a syntax type into the ids the runtime knows it by. Both
// the static-field pass and the operations pass need this: the former writes
// ids into type definitions, the latter into instructions.
type typeResolver struct {
	typeIndices *syntax.TypeMap[int]
	// counts holds where each definition bucket's type ids begin. A declaration
	// is numbered within its own bucket as it is built, which is an index into a
	// list and not yet a type id: a class and an abstract are both the nth of
	// their kind. Adding the bucket's start is what tells the two apart.
	counts *counts
}

// visitStaticFields dispatches each of d's static fields and nested
// declarations back through the visitor: static members, methods and enum
// values lay themselves out into the static region, and nested declarations
// recurse into their own VisitDecl*.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) visitStaticFields(d syntax.Decl) {
	for _, f := range d.StaticFields() {
		f.Accept(p)
	}
	for child := range d.Decls() {
		child.Accept(p)
	}
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitDeclStruct(d *syntax.DeclStruct) error {
	r := p.reader
	if d.Generics() != nil {
		for _, entry := range d.Concretes().All() {
			if entry.Key.ContainsGenericArg() {
				continue
			}
			concrete := entry.Value
			def := r.genericStructDefinitions[*concrete.TypeIndex()].(*typeDefinitionStruct)
			p.fillMembers(def.instanceMembers, concrete)
			p.fillMethods(def.instanceMethods, concrete)
		}
	} else {
		def := r.structDefinitions[*d.TypeIndex()].(*typeDefinitionStruct)
		p.fillMembers(def.instanceMembers, d)
		p.fillMethods(def.instanceMethods, d)
	}
	p.visitStaticFields(d)
	return nil
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitDeclEnum(d *syntax.DeclEnum) error {
	def := p.reader.enumDefinitions[*d.TypeIndex()].(*typeDefinitionEnum)
	p.fillMembers(def.instanceMembers, d)
	p.fillMethods(def.instanceMethods, d)
	p.visitStaticFields(d)
	p.layEnumValues(def, d)
	return nil
}

// layEnumValues lays the primitives an enum's values are backed by back to back
// in declaration order, each in a static slot of its own, so that one is
// reached from the first by its value's discriminant.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) layEnumValues(def *typeDefinitionEnum, d *syntax.DeclEnum) {
	primitive := d.Primitive()
	if primitive == nil {
		return
	}

	r := p.reader
	size, align := fieldByteInfo(primitive)
	p.offset = alignUp(p.offset, align)
	def.valueSlot = uint64(len(r.staticInstanceMaps))
	for _, value := range enumValues(d) {
		*value.ValueIndex() = len(r.staticInstanceMaps)
		r.staticInstanceMaps = append(r.staticInstanceMaps, int(p.offset))
		p.stampStaticHeaders(p.offset, primitive)
		p.offset += size
		def.valueCount++
	}
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitDeclClass(d *syntax.DeclClass) error {
	r := p.reader
	if d.Generics() != nil {
		def := r.genericClassDefinitions[*d.TypeIndex()].(*typeDefinitionGenericClass)
		p.fillGenericClassConcretes(def, d)
	} else {
		def := r.classDefinitions[*d.TypeIndex()].(*typeDefinitionClass)
		p.fillMembers(def.instanceMembers, d)
		p.fillMethods(def.instanceMethods, d)
		def.supertypeSet = p.supertypeSetOf(d)
		def.dispatchEntries = p.fillDispatch(d)
	}
	p.visitStaticFields(d)
	return nil
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	r := p.reader
	if d.Generics() != nil {
		def := r.genericAbstractDefinitions[*d.TypeIndex()].(*typeDefinitionGenericAbstract)
		p.fillGenericAbstractConcretes(def, d)
	} else {
		def := r.abstractDefinitions[*d.TypeIndex()].(*typeDefinitionAbstract)
		p.fillMembers(def.instanceMembers, d)
		p.fillMethods(def.instanceMethods, d)
		def.supertypeSet = p.supertypeSetOf(d)
	}
	p.visitStaticFields(d)
	return nil
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	r := p.reader
	if d.Generics() != nil {
		def := r.genericInterfaceDefinitions[*d.TypeIndex()].(*typeDefinitionGenericInterface)
		instantiations := concreteInstantiations(d)
		def.concreteSupertypeSets = def.concreteSupertypeSets[:len(instantiations)]
		for gid, instantiation := range instantiations {
			def.concreteSupertypeSets[gid] = p.supertypeSetOf(instantiation.decl)
		}
	} else {
		def := r.interfaceDefinitions[*d.TypeIndex()].(*interfaceDefinition)
		def.supertypeSet = p.supertypeSetOf(d)
	}
	p.visitStaticFields(d)
	return nil
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	p.visitStaticFields(d)
	return nil
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	r := p.reader
	size, align := fieldByteInfo(f.FieldType())
	p.offset = alignUp(p.offset, align)
	*f.Index() = len(r.staticInstanceMaps)
	r.staticInstanceMaps = append(r.staticInstanceMaps, int(p.offset))
	p.stampStaticHeaders(p.offset, f.FieldType())
	p.offset += size
	return nil
}

// stampStaticHeaders writes the headers the value in a static slot begins with,
// which is the one thing about a static that cannot be left as the zeroes the
// loader hands it. A mark word of zeroes reads as ZAMOBJECT_STRING, so
// zam_object_header_bytes answers one word where the layout reserved two, and
// every member of the slot is then reached a word short of where it was laid.
//
// Only the headers. What a static holds before something assigns to it is the
// program's own business — there is no initializer ordering pass, by design — and
// a header says what the storage is rather than what it holds.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) stampStaticHeaders(
	offset uint64, t syntax.Type,
) {
	value, ok := inlineClassOf(t)
	if !ok {
		return
	}

	// Nothing here lies inside anything, a static region being neither an
	// allocation nor a frame, so the front element's own distance back is zero.
	// This is stampContained and stampArrayTail in internal/5_build/runtime/src/run.c saying the
	// same of a fresh value; the two have to agree, since the same instruction
	// reads a static and a fresh value through the same header.
	p.stampInline(offset, value, value.elementCount, 0)
}

// stampInline writes a run's headers — for each element the mark word saying
// what it is and how far back what holds it lies, the type id and element count
// behind it, and a tail length where the type states one — and then the headers
// of each element's own members, a member of a class being able to be a class
// with members of its own.
//
// The front element answers for the whole run: it carries how many there are,
// and each element behind it carries how far back the front lies. A location
// holding a single value is that same statement with a count of one.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) stampInline(
	offset uint64, value inlineClass, elementCount, backDistance uint64,
) {
	// A value laid out inline can only be tailed by a length its own type states,
	// an unsized tail having no width to lay anything after it, so the type is
	// what says which of the two kinds this is.
	tailed := value.tailLength != 0
	kind := uint64(objectKindInstance)
	if tailed {
		kind = objectKindTailedInstance
	}

	headerBytes := instanceHeaderByteSizeOf(syntax.KindClass, tailed)
	header := p.reader.staticImageAt(offset, headerBytes)
	binary.LittleEndian.PutUint64(header, staticMarkWord(kind, backDistance))
	binary.LittleEndian.PutUint64(header[wordByteSize:],
		uint64(p.typeInstanceShortOf(value.typ).packed())|elementCount<<32)
	if tailed {
		binary.LittleEndian.PutUint64(header[2*wordByteSize:], value.tailLength)
	}

	for _, m := range instanceFields[*syntax.FieldMember](value.decl) {
		member, ok := inlineClassOf(m.FieldType())
		if !ok {
			continue
		}
		// The container's mark word is at the container's own start, so how far
		// back it lies from the member is exactly the member's offset within it,
		// the offsets recorded being net of the header.
		//
		// A member written as an array is a run laid inline, and its count is
		// what the front of that run answers with — the same statement a slot
		// holding a run makes, which is why it is the same call.
		back := headerBytes + *m.Offset()
		p.stampInline(offset+back, member, member.elementCount, back)
	}

	// And the elements behind the front, each contained by the front rather than
	// by whatever the front is itself contained by. That keeps a walk out of an
	// interior element one step from anywhere in the run, and it is what makes
	// the distance the same number wherever the run happens to lie.
	for index := uint64(1); index < elementCount; index++ {
		back := index * value.stride
		p.stampInline(offset+back, value, 1, back)
	}
}

// inlineClass is the markable value a location of some type holds inline, if it
// holds one at all.
type inlineClass struct {
	// decl is the class laid out there, whose members are stamped in turn, and
	// typ is the type naming it, which is what the object header records.
	decl syntax.DeclTypeable
	typ  syntax.Type
	// elementCount is how many of them lie in a row and stride is the bytes from
	// one to the next, a tailed element's tail counting toward its own.
	elementCount uint64
	stride       uint64
	// tailLength is how many tail elements each one carries, which is a third
	// header word wherever it is not zero.
	tailLength uint64
}

// inlineClassOf reports the markable value laid out at a location of the given
// type, if one is. Array and tail wrappers are peeled to reach it, their lengths
// being how much of it lies there rather than what it is.
//
// A class is the only answer, which is the split [declFieldByteInfo] already
// takes for the width. A struct and an enum are laid out inline and carry no
// header, being the coder's own memory and never collected; an abstract, an
// interface, a pointer and a function type are one word naming something whose
// header is its own. An unsized array or tail is one word too, there being no
// length to lay anything out from.
func inlineClassOf(t syntax.Type) (inlineClass, bool) {
	value := inlineClass{elementCount: 1}
	element := t
	for {
		switch wt := t.(type) {
		case *syntax.TypeWrapped:
			if wt.Kind() == syntax.TypeKindVariadic || wt.Kind() == syntax.TypeKindConst {
				t = wt.Type
				continue
			}
		case *syntax.TypeTailed:
			if wt.Size == nil {
				return inlineClass{}, false
			}
			value.tailLength = *wt.Size
			t = wt.Type
			continue
		case *syntax.TypeArray:
			if wt.Size == nil {
				return inlineClass{}, false
			}
			// Multiplied rather than assigned, and the element type taken from
			// inside the innermost wrapper: a run of runs is one run in memory,
			// and its front answers for every element behind it.
			value.elementCount *= *wt.Size
			element, t = wt.Type, wt.Type
			continue
		}
		break
	}

	resolvable, ok := t.(syntax.TypeResolvable)
	if !ok {
		return inlineClass{}, false
	}
	decl := resolvable.Decl()
	if decl.Kind() != syntax.KindClass {
		return inlineClass{}, false
	}

	value.decl, value.typ = decl, t
	value.stride, _ = fieldByteInfo(element)
	return value, true
}

// VisitFieldMethod lays out the slot a static method's closure is built into.
// It is one word, being a closure, and it is numbered out of the same map every
// other static is: a static is named on the wire by one index, so a method
// numbered in a map of its own would name whichever member held that index.
//
// A generic static method is monomorphized per instantiation, so each concrete
// gets its own one-word slot and its own index. The generic method itself
// never holds a closure; its slot is left unassigned, with its concretes
// standing in its place in the static region.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	r := p.reader
	if f.Generics() != nil {
		for _, concrete := range f.Concretes() {
			p.offset = alignUp(p.offset, wordByteSize)
			*concrete.Index() = len(r.staticInstanceMaps)
			r.staticInstanceMaps = append(r.staticInstanceMaps, int(p.offset))
			p.offset += wordByteSize
		}
		return nil
	}
	p.offset = alignUp(p.offset, wordByteSize)
	*f.Index() = len(r.staticInstanceMaps)
	r.staticInstanceMaps = append(r.staticInstanceMaps, int(p.offset))
	p.offset += wordByteSize
	return nil
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	r := p.reader
	size, _, align := layoutOf(f.OuterDecl())
	p.offset = alignUp(p.offset, align)
	*f.Index() = len(r.staticInstanceMaps)
	r.staticInstanceMaps = append(r.staticInstanceMaps, int(p.offset))
	p.offset += size
	return nil
}

// typeInstanceOf resolves a syntax type to its runtime type, peeling variadic,
// tailed and array wrappers to accumulate tail length and array length before
// resolving the underlying type id. A pointer is not peeled: it is a type of its
// own, which is what keeps E[5]* apart from E*[5]. Const is a compile-time
// promise about a slot and has no runtime trace, so it is peeled too.
func (p *typeResolver) typeInstanceOf(t syntax.Type) typeInstance {
	var ti typeInstance
	for {
		switch wt := t.(type) {
		case *syntax.TypeWrapped:
			switch wt.Kind() {
			case syntax.TypeKindVariadic, syntax.TypeKindConst:
				t = wt.Type
				continue
			}
		case *syntax.TypeTailed:
			if wt.Size != nil {
				ti.tailedLength = *wt.Size
			}
			t = wt.Type
			continue
		case *syntax.TypeArray:
			if wt.Size != nil {
				ti.arrayLength = *wt.Size
			}
			t = wt.Type
			continue
		}
		break
	}
	ti.typeInstanceShort = p.typeInstanceShortOf(t)
	return ti
}

// typeInstanceShortOf resolves the type id and generic id of an unwrapped type.
func (p *typeResolver) typeInstanceShortOf(t syntax.Type) typeInstanceShort {
	switch tt := t.(type) {
	case *syntax.TypePrimitive:
		return typeInstanceShort{typeID: uint16(tt.Kind())}
	case *syntax.TypeFunction:
		return typeInstanceShort{typeID: p.internedTypeID(tt)}
	// A thread is interned by the type it runs, a pointer by the whole type.
	case *syntax.TypeWrapped:
		key := syntax.Type(tt)
		if tt.Kind() == syntax.TypeKindThread {
			key = tt.Type
		}
		return typeInstanceShort{typeID: p.internedTypeID(key)}
	// A tailed type names the declaration a reference to it would, the tail
	// saying how much of it was allocated rather than which one it is.
	case *syntax.TypeReference:
		return p.declTypeInstanceShort(tt.Decl())
	case *syntax.TypeTailed:
		return p.declTypeInstanceShort(tt.Decl())
	case *syntax.TypeGeneric:
		return p.genericTypeInstanceShort(tt)
	}
	panic("unreachable")
}

// declTypeInstanceShort resolves the type id of a non-generic declaration.
func (p *typeResolver) declTypeInstanceShort(d syntax.DeclTypeable) typeInstanceShort {
	return typeInstanceShort{typeID: p.declTypeID(d, *d.TypeIndex())}
}

// declTypeID turns a declaration's index within its bucket into the type id the
// runtime names it by, which is where the bucket begins plus how far into it the
// declaration was built. The two cannot be the same number: every bucket is
// indexed from zero, so the first class, the first abstract and the first
// interface would otherwise all be type id zero, and so would the primitive that
// already holds it.
func (p *typeResolver) declTypeID(d syntax.Decl, index int) uint16 {
	return uint16(p.declBucketStart(d) + index)
}

// declBucketStart is where the type ids of the bucket d was built into begin. A
// declaration's bucket is settled by its kind and by whether it is generic,
// which is the same pair buildTypeDefinitionSkeletons sorted it by.
func (p *typeResolver) declBucketStart(d syntax.Decl) int {
	generic := false
	if gd, ok := d.(syntax.DeclGeneric); ok {
		generic = gd.Generics() != nil
	}

	c := p.counts
	switch d.Kind() {
	case syntax.KindStruct:
		// A concrete struct is laid out on its own rather than shared, so every
		// instantiation of a generic struct is its own definition in the generic
		// struct bucket.
		if generic {
			return c.genericStructDefinitionsStartIndex
		}
		return c.structDefinitionsStartIndex
	case syntax.KindEnum:
		return c.enumDefinitionsStartIndex
	case syntax.KindClass:
		if generic {
			return c.genericClassDefinitionsStartIndex
		}
		return c.classDefinitionsStartIndex
	case syntax.KindAbstract:
		if generic {
			return c.genericAbstractDefinitionsStartIndex
		}
		return c.abstractDefinitionsStartIndex
	case syntax.KindInterface:
		if generic {
			return c.genericInterfaceDefinitionsStartIndex
		}
		return c.interfaceDefinitionsStartIndex
	}
	panic("unreachable")
}

// genericTypeInstanceShort resolves the type id and generic id of a generic
// instantiation. Concrete structs are flattened into their own definitions;
// concrete classes, abstracts and interfaces share their generic declaration's
// definition and are distinguished by generic id.
func (p *typeResolver) genericTypeInstanceShort(t *syntax.TypeGeneric) typeInstanceShort {
	parent := t.Type.Decl().(syntax.DeclGeneric)
	concrete, _ := parent.GetConcreteDecl(t)
	switch pd := parent.(type) {
	case *syntax.DeclStruct:
		return typeInstanceShort{typeID: p.declTypeID(pd, *concrete.TypeIndex())}
	case *syntax.DeclClass, *syntax.DeclAbstract, *syntax.DeclInterface:
		return typeInstanceShort{
			typeID:    p.declTypeID(parent, *parent.(syntax.DeclTypeable).TypeIndex()),
			genericID: *concrete.(virtualConcrete).ConcreteID(),
		}
	}
	panic("unreachable")
}

// assignConcreteIDs numbers each fully-concrete instantiation of a generic
// class, abstract or interface by its position among concrete-only entries
// (those whose arguments contain no open generic arguments). Concrete structs
// are flattened into their own definitions and numbered by type index instead.
func (cv *typeDefinitionsInstantiatorVisitor) assignConcreteIDs(d syntax.DeclGeneric) {
	var id uint16
	for _, entry := range d.Concretes().All() {
		if entry.Key.ContainsGenericArg() {
			continue
		}
		*entry.Value.(virtualConcrete).ConcreteID() = id
		id++
	}
}

// fillMembers resolves every instance member's offset and type into the member
// slice, indexed by the member's field index. The offset is read off the field
// rather than worked out here, [layoutOf] having recorded it there while taking
// the layout that settles the instance's size.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) fillMembers(dst []memberDefinition, d syntax.Decl) {
	for _, m := range instanceFields[*syntax.FieldMember](d) {
		dst[*m.Index()] = memberDefinition{
			Offset: *m.Offset(),
			Type:   p.typeInstanceOf(m.FieldType()),
		}
	}
}

// fillMethods resolves every instance method's function type id into the method
// slice, indexed by the method's field index. Instructions are left zeroed for
// [operationVisitor.recordMethodInstructions], which fills them once the bodies
// they name have been emitted and their offsets are known.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) fillMethods(dst []methodDefinition, d syntax.Decl) {
	for _, m := range instanceFields[*syntax.FieldMethod](d) {
		dst[*m.Index()] = methodDefinition{typeListID: p.functionTypeID(m.FieldType())}
	}
}

// internedTypeID returns the global type id of a thread, function or pointer
// type, all of which are numbered by the semantic pass that collects them.
//
// A type reached here that was never collected is a hole in that pass, not a
// type without an id, and it has to be said out loud: the miss would otherwise
// read as index zero and be emitted as some other type's id.
func (p *typeResolver) internedTypeID(t syntax.Type) uint16 {
	idx, at := p.typeIndices.Get(t)
	if at < 0 {
		panic("unreachable")
	}
	return reservedTypeIDCount + uint16(idx)
}

// functionTypeID returns the global type id of a function type.
func (p *typeResolver) functionTypeID(t syntax.Type) uint64 {
	return uint64(p.internedTypeID(t))
}

// functionTypeList resolves a function type into an inline type list of its
// return type followed by its parameter types.
//
// A parameter is listed as the frame holds it rather than as it was written,
// which differs only for a variadic: the arguments are packed before the call,
// so the slot holds a pointer to them.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) functionTypeList(t syntax.Type) typeListInstance {
	ft := t.(*syntax.TypeFunction)
	ids := make([]typeInstance, 0, len(ft.ParameterTypes)+1)
	ids = append(ids, p.typeInstanceOf(ft.ReturnType))
	for _, param := range ft.ParameterTypes {
		ids = append(ids, p.typeInstanceOf(syntax.ParameterBindingType(param)))
	}
	return typeListInstance{typeIDs: ids}
}

// genericTypeList resolves every open type the declaration mentions through one
// instantiation's bindings, keeping each at its enumerated position. A method
// body is emitted once for the whole template, so an operation naming an open
// type carries that position and the runtime reads the resolved type out of the
// receiver's own entry: inside LinkedList[T], a Node[T]* lands on Node[int]* or
// Node[string]* depending on which concrete is executing.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) genericTypeList(d syntax.DeclGeneric, t *syntax.TypeGeneric) typeListInstance {
	bindings := make(map[syntax.GenericArgKey]syntax.Type, len(t.ParameterTypes))
	for i, entry := range d.Generics().All() {
		bindings[entry.Key] = t.ParameterTypes[i]
	}

	openTypes := d.GenericTypes()
	ids := make([]typeInstance, openTypes.Len())
	for open := range openTypes.Values() {
		ids[openTypes.Index(open)] = p.typeInstanceOf(open.Map(bindings))
	}
	return typeListInstance{typeIDs: ids}
}

// supertypeSetOf gathers every type a value of d's may be read as: d itself,
// each declaration up its base chain, and each interface it implements. An
// interface reaches neither of the first two — it extends interfaces and is
// based on nothing — so its set is itself and what it extends.
//
// d is in its own set because that is the commonest thing a cast asks about. The
// left side of a subtype test is the type the instance actually is, and reading
// a value as the class it already is holds, so leaving d out would mean
// answering the ordinary case by a comparison the table exists to spare.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) supertypeSetOf(
	d syntax.DeclTypeable,
) map[typeInstanceShort]struct{} {
	set := map[typeInstanceShort]struct{}{
		p.typeInstanceShortOf(d.DeclType()): {},
	}

	// The chain is walked rather than read off d because a base contributes its
	// own bases too, and nothing flattens them: an instance's layout inherits
	// from every ancestor, but the record names only the one directly above.
	if subtypeable, ok := d.(syntax.DeclSubtypeable); ok {
		for ancestor := subtypeable.BaseDeclType(); ancestor != nil; {
			resolvable, ok := ancestor.Type.(syntax.TypeResolvable)
			if !ok {
				break
			}
			set[p.typeInstanceShortOf(ancestor.Type)] = struct{}{}

			above, ok := resolvable.Decl().(syntax.DeclSubtypeable)
			if !ok {
				break
			}
			ancestor = above.BaseDeclType()
		}
	}

	if inheritable, ok := d.(syntax.DeclInheritable); ok {
		for ifaceType := range inheritable.Interfaces() {
			set[p.typeInstanceShortOf(ifaceType)] = struct{}{}
		}
	}

	return set
}

// fillDispatch returns what the dispatch table will be built from: every
// interface method keyed to the index of the method implementing it.
//
// The table itself is not built here. Nothing between here and the wire asks it
// anything, and its width follows from how many entries there are rather than
// from how the search went, so building it is left to the serialization that
// needs the bytes.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) fillDispatch(
	d inheritableDecl,
) map[uint64]uint32 {
	entries := map[uint64]uint32{}
	classMethods := make(map[string]uint32)
	for name, m := range instanceFields[*syntax.FieldMethod](d) {
		classMethods[name] = uint32(*m.Index())
	}

	for ifaceType := range d.Interfaces() {
		short := p.typeInstanceShortOf(ifaceType)

		resolvable, ok := ifaceType.(syntax.TypeResolvable)
		if !ok {
			continue
		}
		ifaceDecl, ok := resolvable.Decl().(syntax.Decl)
		if !ok {
			continue
		}

		for name, im := range ifaceDecl.InstanceMethods() {
			classMethodIndex, ok := classMethods[name]
			// An interface method left out of the table is not a method that
			// cannot be called: the table holds no key to check against, so
			// its key lands on some other method's slot and calls that
			// instead. Leaving it out quietly would turn a declaration that
			// fails to implement what it says it does into a call to whatever
			// happened to be placed there.
			if !ok {
				panic("unreachable")
			}
			entries[classOrAbstractInterfaceMethodKey{
				InterfaceType: short,
				methodIndex:   uint32(*im.Index()),
			}.packed()] = classMethodIndex
		}
	}

	return entries
}

// concreteInstantiations returns the fully-concrete instantiations of a generic
// declaration in the order [assignConcreteIDs] numbers them, which is the order
// the generic map is indexed by.
//
// The declaration collected more than these: an instantiation still holding an
// open argument — LinkedList[T] within LinkedList[T]'s own body — is given no
// generic id and has no layout to describe, so it is left out rather than
// serialized as zeroes the runtime would index into.
func concreteInstantiations(d syntax.DeclGeneric) []concreteInstantiation {
	concretes := make([]concreteInstantiation, 0, d.Concretes().Len())
	for _, entry := range d.Concretes().All() {
		if entry.Key.ContainsGenericArg() {
			continue
		}
		concretes = append(concretes, concreteInstantiation{
			arguments: entry.Key.(*syntax.TypeGeneric),
			decl:      entry.Value.(inheritableDecl),
		})
	}
	return concretes
}

type concreteInstantiation struct {
	arguments *syntax.TypeGeneric
	decl      inheritableDecl
}

// fillGenericConcrete populates everything about an instantiation that holds
// whether or not its declaration is one an instance can be.
func (p *staticTypesandTypeDefinitionsPopulatorVisitor) fillGenericConcrete(
	gm *genericMapEntry,
	d syntax.DeclGeneric,
	instantiation concreteInstantiation,
) {
	concrete := instantiation.decl
	gm.genericTypes = p.genericTypeList(d, instantiation.arguments)

	gm.instanceByteSize, gm.instanceTailByteSize, _ = layoutOf(concrete)

	gm.instanceMembers = make([]memberDefinition, instanceFieldCount[*syntax.FieldMember](concrete))
	p.fillMembers(gm.instanceMembers, concrete)

	// The same resolution fillMethods makes for a plain declaration. A concrete's
	// field types are already substituted, so the signature met here is ground —
	// and collectTypes walks a template's instance fields once per concrete with
	// that binding in effect, which is what makes it one of the function types
	// the program numbered rather than one only this entry knows.
	gm.instanceMethodTypeIDs = make([]uint64, instanceFieldCount[*syntax.FieldMethod](concrete))
	for _, m := range instanceFields[*syntax.FieldMethod](concrete) {
		gm.instanceMethodTypeIDs[*m.Index()] = p.functionTypeID(m.FieldType())
	}

	gm.supertypeSet = p.supertypeSetOf(concrete)
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) fillGenericClassConcretes(def *typeDefinitionGenericClass, d syntax.DeclGeneric) {
	instantiations := concreteInstantiations(d)
	def.concreteTypesSizeMap = def.concreteTypesSizeMap[:len(instantiations)]
	for gid, instantiation := range instantiations {
		gm := &def.concreteTypesSizeMap[gid]
		p.fillGenericConcrete(&gm.genericMapEntry, d, instantiation)
		gm.dispatchEntries = p.fillDispatch(instantiation.decl)
	}
}

func (p *staticTypesandTypeDefinitionsPopulatorVisitor) fillGenericAbstractConcretes(def *typeDefinitionGenericAbstract, d syntax.DeclGeneric) {
	instantiations := concreteInstantiations(d)
	def.concreteTypesSizeMap = def.concreteTypesSizeMap[:len(instantiations)]
	for gid, instantiation := range instantiations {
		p.fillGenericConcrete(&def.concreteTypesSizeMap[gid], d, instantiation)
	}
}

// alignUp rounds offset up to the next multiple of align.
func alignUp(offset, align uint64) uint64 {
	if align == 0 {
		return offset
	}
	return (offset + align - 1) / align * align
}

// primitiveByteInfo returns the byte size and alignment of a primitive type.
func primitiveByteInfo(k syntax.TypeKind) (size, align uint64) {
	switch k {
	case syntax.TypeKindVoid:
		return 0, 1
	case syntax.TypeKindBool:
		return 1, 1
	case syntax.TypeKindByte:
		return 1, 1
	case syntax.TypeKindChar:
		return 4, 4
	// A type is its packed id, which is the three words [emitPushTypeID] pushes.
	case syntax.TypeKindType:
		return 3 * wordByteSize, wordByteSize
	default: // int, uint, float, string
		return wordByteSize, wordByteSize
	}
}

// isUnboundedTail reports whether a type is an unsized array or unsized tailed
// type, i.e. the dynamic trailing member of a tailed declaration.
func isUnboundedTail(t syntax.Type) bool {
	switch tt := t.(type) {
	case *syntax.TypeArray:
		return tt.Size == nil
	case *syntax.TypeTailed:
		return tt.Size == nil
	}
	return false
}

// instanceHeaderByteSize is the room an instance of the declaration keeps in
// front of its members, which for a class or an abstract is the mark word and
// then the type id — the shape ZamTypedObjectHeader gives an allocation in
// internal/5_build/runtime/src/instance.h — and a third word for the tail length if it is tailed.
// Nothing held as a one-word reference keeps any.
//
// A struct and an enum are the coder's own memory and are never marked, so they
// keep none either, with the one exception a tail forces: a tailed struct keeps
// the length word and nothing else. It has no base, so it is never reached under
// another type's id and needs no type word to say what it actually is, and it is
// never collected, so it needs no mark word. What is left is the one thing
// nothing else can supply. See ZAMTAILED_STRUCT_HEADER_BYTES, which is this
// same claim on the runtime's side.
//
// The header belongs to the layout rather than to the allocator because a class
// value is not only ever reached through a pointer the allocator handed out. It
// is copied onto the operand stack whenever one is read for its value, laid
// inline inside another class, and taken into a closure, and zam_mark_word_of is
// the word it begins with in all of those places or in none of them. A string
// literal already reserves its mark word for the same reason; see
// internedString.
//
// An abstract is never laid out anywhere, and is laid out here all the same: a
// class flattens its base's members ahead of its own, so the two agree on every
// offset only if they agree on where the members begin.
func instanceHeaderByteSize(d syntax.Decl) uint64 {
	tailable, ok := d.(syntax.DeclTailable)
	return instanceHeaderByteSizeOf(d.Kind(), ok && tailable.IsTailed())
}

// instanceHeaderByteSizeOf is that rule with the declaration taken out of it, so
// that the four answers can be asked for directly. A declaration carrying a tail
// cannot be built without a parser, which would otherwise put the one case with
// no other cover — the tailed struct — out of a test's reach.
func instanceHeaderByteSizeOf(kind syntax.FieldKind, tailed bool) uint64 {
	switch kind {
	case syntax.KindClass, syntax.KindAbstract:
		if tailed {
			return 3 * wordByteSize
		}
		return 2 * wordByteSize
	case syntax.KindStruct:
		if tailed {
			return wordByteSize
		}
	case syntax.KindEnum:
		// An enum's instances are the values named in the declaration, and
		// their only intrinsic difference is which one of those they are —
		// no user-declared member separates BigEndian from LittleEndian when
		// neither was given a body. A one-word discriminant held at the head
		// of every enum value is what makes them distinguishable; equality
		// reads this word to decide, and emitEnumValue writes each value's
		// ordinal here as the value is laid down.
		return wordByteSize
	}
	return 0
}

// layoutOf computes and caches a declaration's instance layout following C
// struct rules: each non-tail member is placed at the next offset satisfying its
// alignment, and the fixed size is padded up to the declaration's own alignment
// (the maximum member alignment) so arrays of it have the correct stride. A
// trailing unbounded member is treated as a C flexible array member: the fixed
// size is the aligned offset at which its elements begin, and its per-element
// stride is returned separately. Results are memoized on the declaration; inline
// member cycles are rejected during semantic analysis, so the recursion
// terminates.
func layoutOf(d syntax.Decl) (size, tail, align uint64) {
	var offset uint64
	align = 1

	// A markable instance keeps its header ahead of its members, so the size
	// covers it and anything holding the value inline reserves room for it. The
	// offsets recorded are net of it, so that a member sits where it does whether
	// or not the header in front of it turned out to carry a tail length. What
	// reads a member adds the width the kind in the header calls for.
	header := instanceHeaderByteSize(d)
	if header != 0 {
		offset, align = header, wordByteSize
	}

	var hasTail bool
	for _, m := range instanceFields[*syntax.FieldMember](d) {
		ft := m.FieldType()
		if isUnboundedTail(ft) {
			elemSize, elemAlign := tailByteInfo(ft)
			if elemAlign > align {
				align = elemAlign
			}
			offset = alignUp(offset, elemAlign)
			*m.Offset() = offset - header
			tail = elemSize
			hasTail = true
			continue
		}
		sz, al := fieldByteInfo(ft)
		if al > align {
			align = al
		}
		offset = alignUp(offset, al)
		*m.Offset() = offset - header
		offset += sz
	}
	if hasTail {
		size = offset
	} else {
		size = alignUp(offset, align)
	}
	*d.InstanceByteSize() = size
	*d.InstanceTailByteSize() = tail
	return size, tail, align
}

// tailByteInfo returns the stride and alignment of a single element of an
// unbounded tail member: the element size and alignment of an unsized array, or
// the tail element size and alignment of the referenced declaration for an
// unsized tailed type.
func tailByteInfo(t syntax.Type) (size, align uint64) {
	switch tt := t.(type) {
	case *syntax.TypeArray:
		return fieldByteInfo(tt.Type)
	case *syntax.TypeTailed:
		_, tail, align := layoutOf(tt.Type.Decl())
		return tail, align
	}
	return 0, 1
}

// fieldByteInfo returns the inline byte size and alignment of a member of the
// given type. Pointers, interfaces, abstracts, function types and unbounded
// tails are stored as a single-word reference; primitives use their primitive
// size and alignment; sized arrays multiply their length by their element
// stride; sized tailed types add their tail elements to the referenced
// declaration's fixed size; and classes, structs and enums are laid out inline.
func fieldByteInfo(t syntax.Type) (size, align uint64) {
	switch tt := t.(type) {
	case *syntax.TypeWrapped:
		// Const is a compile-time promise about a slot and has no runtime
		// trace, so const T is laid out exactly as T. Every other wrapper
		// (pointer, thread, variadic) is a one-word reference to its element.
		if tt.Kind() == syntax.TypeKindConst {
			return fieldByteInfo(tt.Type)
		}
		return wordByteSize, wordByteSize
	case *syntax.TypePrimitive:
		return primitiveByteInfo(tt.Kind())
	case *syntax.TypeArray:
		if tt.Size == nil {
			return wordByteSize, wordByteSize
		}
		elemSize, elemAlign := fieldByteInfo(tt.Type)
		return *tt.Size * elemSize, elemAlign
	case *syntax.TypeTailed:
		if tt.Size == nil {
			return wordByteSize, wordByteSize
		}
		fixed, tail, align := layoutOf(tt.Type.Decl())
		return fixed + *tt.Size*tail, align
	case *syntax.TypeReference:
		return declFieldByteInfo(tt.Decl())
	case *syntax.TypeGeneric:
		return declFieldByteInfo(tt.Decl())
	case *syntax.TypeFunction:
		return wordByteSize, wordByteSize
	}
	return wordByteSize, wordByteSize
}

// stackWords is how many operand stack slots a value of the given type occupies.
// A value is on the stack the same way it is in a field: a class, struct or enum
// lies there inline and is as wide as its layout, so it is more than one slot
// whenever its layout is, while everything stored as a reference is one.
func stackWords(t syntax.Type) uint64 {
	size, _ := fieldByteInfo(t)
	return alignUp(size, wordByteSize) / wordByteSize
}

// declFieldByteInfo returns the inline byte size and alignment of a member whose
// type is a bare reference to the given declaration. Interfaces and abstracts
// are stored as a single-word pointer; classes, structs and enums are laid out
// inline.
func declFieldByteInfo(d syntax.DeclTypeable) (size, align uint64) {
	switch d.Kind() {
	case syntax.KindClass, syntax.KindStruct, syntax.KindEnum:
		size, _, align := layoutOf(d)
		return size, align
	default:
		return wordByteSize, wordByteSize
	}
}
