package syntax

import (
	"fmt"
	"iter"

	base "github.com/zambozoo/zam/internal/0_base"
)

type (
	// Decl is a declaration in the AST.
	Decl interface {
		Field
		// Start returns the token that started the declaration.
		Start() *Token
		// File returns the file of the declaration.
		File() *File
		// OuterDecl returns the outer declaration of the declaration.
		OuterDecl() Decl

		// Hash returns the hash of the declaration.
		Hash() uint64

		// StaticFields returns the static fields of the declaration.
		StaticFields() iter.Seq2[string, Field]
		// InstanceFields returns the instance fields of the declaration.
		InstanceFields() iter.Seq2[string, Field]
		// InstanceMembers returns the instance members of the declaration.
		InstanceMembers() iter.Seq2[string, *FieldMember]
		// InstanceMethods returns the instance methods of the declaration.
		InstanceMethods() iter.Seq2[string, *FieldMethod]
		// AllInstanceFields returns the instance fields an instance of the
		// declaration actually has, in layout order, which for a class or an
		// abstract means the ones it inherits as well as the ones it declares.
		AllInstanceFields() *base.StaticOrderedMap[string, Field]
		// InstanceMethodCount returns the number of instance methods in the declaration.
		InstanceMethodCount() int
		// InstanceMemberCount returns the number of instance members in the declaration.
		InstanceMemberCount() int
		// TypeIndex returns the index of the declaration's type definition within
		// its codegen bucket.
		TypeIndex() *int
		// InstanceByteSize returns a pointer to the cached fixed byte size of an
		// instance of the declaration.
		InstanceByteSize() *uint64
		// InstanceTailByteSize returns a pointer to the cached byte size of a
		// single tail element of an instance of the declaration.
		InstanceTailByteSize() *uint64

		// Decls returns the declarations of the declaration.
		Decls() iter.Seq[Decl]
		// GetDecl returns the declaration with the given id.
		GetDecl(id string) (Decl, bool)

		// GetInstanceField returns the instance field with the given id.
		GetInstanceField(id string) (FieldTypeable, bool)
		// GetStaticField returns the static field with the given id.
		GetStaticField(id string) (FieldIndexable, bool)
		// GetInstanceMethod returns the instance method with the given id.
		GetInstanceMethod(id string) (*FieldMethod, bool)
		// GetStaticMethod returns the static method with the given id.
		GetStaticMethod(id string) (*FieldMethod, bool)
	}
	// DeclTypeable is a declaration that can be referenced by type.
	DeclTypeable interface {
		Decl
		// DeclType returns the type of the declaration.
		DeclType() Type
	}

	// GenericArgKey is a key for a generic argument. The owner is a declaration
	// for a class-level T and a method for a method-level one, and the name is
	// the identifier the parameter was written with. Two parameters that share a
	// name under two different owners are two different keys.
	GenericArgKey struct {
		// Owner is what the generic argument belongs to.
		Owner GenericOwner
		// Name is the name of the generic argument.
		Name string
	}
	// GenericOwner is anything that can hold a generic parameter list. A
	// generic declaration owns its class-level parameters; a generic method
	// owns its own method-level parameters, which are a scope of their own and
	// resolve against a different binding than the class's.
	GenericOwner interface {
		// Generics returns the parameter map the owner was written with.
		Generics() *base.StaticOrderedMap[GenericArgKey, GenericBound]
		// GenericTypes returns the open generic types referenced within the
		// owner's signature and body.
		GenericTypes() *TypeSet
		// Name returns the owner's identifier.
		Name() string
		// Hash returns a stable hash of the owner's identity, used when a
		// TypeGenericArg standing for one of its parameters is hashed.
		Hash() uint64
	}
	// DeclGeneric is a declaration with generics.
	DeclGeneric interface {
		DeclTypeable
		// Generics returns the generics of the declaration.
		Generics() *base.StaticOrderedMap[GenericArgKey, GenericBound]
		// Concretes returns the concretes declarations of the generic declaration.
		Concretes() *TypeMap[DeclTypeable]
		// AddConcreteType returns the concrete declaration for the given type, returning true if it was added.
		GetConcreteDecl(t *TypeGeneric) (DeclTypeable, bool)
		// GenericTypes returns the open generic types referenced within the declaration.
		GenericTypes() *TypeSet
	}

	// DeclTailable is a declaration that can be tailed.
	DeclTailable interface {
		DeclGeneric
		// IsTailed returns whether the declaration is tailed.
		IsTailed() bool
	}
	// DeclSubtypeable is a declaration that can be subclassed.
	DeclSubtypeable interface {
		DeclTailable
		DeclInheritable
		// BaseDeclType returns the base declaration type of the declaration.
		BaseDeclType() *TypeWrapped
	}
	// DeclInheritable is a declaration that can inherit from other declarations.
	DeclInheritable interface {
		DeclGeneric
		// HasInterface returns whether the declaration has the given interface.
		HasInterface(t Type) bool
		// PutInterface puts the given interface into the declaration.
		PutInterface(t Type)
		// Interfaces returns the inherited types of the declaration.
		Interfaces() iter.Seq[Type]
		// InterfaceCount returns the number of inherited types of the declaration.
		InterfaceCount() int
		// IsSubtypeOf returns whether the declaration is a subtype of the given type.
		IsSubtypeOf(t Type) bool
	}

	// decl is the base declaration.
	decl struct {
		// start is the token that started the declaration.
		start *Token
		// file is the file of the declaration.
		file *File
		// outerDecl is the outer declaration of the declaration.
		outerDecl Decl

		// kind is the kind of the declaration.
		kind FieldKind
		// modifiers is the modifiers of the declaration.
		modifiers Modifier
		// name is the name of the declaration.
		name string

		// staticMethodCount is the number of static methods in the declaration.
		staticMethodCount int
		// staticMemberCount is the number of static members in the declaration.
		staticMemberCount int
		// staticFields is the static fields of the declaration.
		staticFields *base.StaticOrderedMap[string, Field]

		// innerDecls is the nested declarations of the declaration. It is kept
		// separate from staticFields so an inner class can share a name with a
		// method or member on the same declaration: the inner's name is a type
		// name, not a value, and the two never resolve against the same lookup.
		innerDecls *base.StaticOrderedMap[string, Decl]

		// instanceMethodCount is the number of instance methods in the declaration.
		instanceMethodCount int
		// instanceMemberCount is the number of instance members in the declaration.
		instanceMemberCount int
		// instanceFields is the instance fields of the declaration.
		instanceFields *base.StaticOrderedMap[string, Field]

		// typeIndex is the index of this declaration's type definition within its
		// codegen bucket. It is assigned while the skeletons are built so later
		// passes can find the definition to populate.
		typeIndex int

		// instanceByteSize is the cached fixed byte size of an instance of the
		// declaration.
		instanceByteSize uint64
		// instanceTailByteSize is the cached byte size of a single tail element
		// of an instance of the declaration.
		instanceTailByteSize uint64
	}

	// declGeneric is the base generic declaration.
	declGeneric struct {
		decl
		// generics is the generic arguments and bounds of the declaration.
		generics *base.StaticOrderedMap[GenericArgKey, GenericBound]
		// concretes is the concrete declarations of the generic declaration.
		concretes *TypeMap[DeclTypeable]
		// genericTypes is the set of open generic types (those with a generic
		// argument among their parameters) referenced within the declaration.
		// Mapping each through a concrete's bindings yields that concrete's
		// dependent instantiations.
		genericTypes *TypeSet
	}
	// GenericBound is a generic bound.
	GenericBound interface {
		// Bounds returns the bounds of the generic bound.
		Bounds() []Type
	}
	// GenericLowerBounded is a generic lower bounded.
	GenericLowerBounded []Type
	// GenericUpperBounded is a generic upper bounded.
	GenericUpperBounded []Type

	// DeclClass is a class declaration.
	DeclClass struct {
		declGeneric
		// isTailed is whether the declaration is tailed.
		isTailed bool
		// baseDeclType is the base declaration type.
		baseDeclType *TypeWrapped
		// interfaces is the set of interfaces that the class implements.
		interfaces *TypeSet
	}
	// DeclAbstract is a abstract declaration.
	DeclAbstract struct {
		declGeneric
		// isTailed is whether the declaration is tailed.
		isTailed bool
		// baseDeclType is the base declaration type.
		baseDeclType *TypeWrapped
		// interfaces is the set of interfaces that the abstract implements.
		interfaces *TypeSet
	}
	// DeclEnum is a enum declaration.
	DeclEnum struct {
		decl
	}
	// DeclInterface is a interface declaration.
	DeclInterface struct {
		declGeneric
		// interfaces is the set of interfaces that the interface extends.
		interfaces *TypeSet
	}
	// DeclNamespace is a namespace declaration.
	DeclNamespace struct {
		decl
	}
	// DeclStruct is a struct declaration.
	DeclStruct struct {
		declGeneric
		// isTailed is whether the declaration is tailed.
		isTailed bool
	}

	// DeclConcrete is a concrete instantiation of a generic declaration.
	DeclConcrete interface {
		DeclTypeable
		// MapType substitutes the concrete's binding into the given type.
		MapType(t Type) Type
	}
	// declConcrete is a concrete declaration.
	declConcrete struct {
		DeclGeneric
		// types is the types of the concrete.
		types map[GenericArgKey]Type
		// interfaces is what this instantiation implements and the template does
		// not. An interface the template implements is every instantiation's,
		// so it is read from there and mapped; one that holds only once the
		// parameters are bound belongs to this instantiation alone and cannot be
		// written back to the template, which its siblings read.
		interfaces *TypeSet

		// instanceByteSize is the cached fixed byte size of an instance of this
		// concrete instantiation. Each instantiation is sized independently, so
		// this shadows the generic declaration's shared size.
		instanceByteSize uint64
		// instanceTailByteSize is the cached byte size of a single tail element
		// of an instance of this concrete instantiation.
		instanceTailByteSize uint64

		// cachedAllInstanceFields holds the one copy of this concrete's mapped
		// fields, built once and returned to every caller after. The copies carry
		// their own offset storage, so a layout written to them is what the next
		// reader sees — building a fresh set on each call would lose the offsets
		// to the garbage collector between the write and the read.
		cachedAllInstanceFields *base.StaticOrderedMap[string, Field]
	}
	// DeclConcreteAbstract is a concrete abstract declaration.
	DeclConcreteAbstract struct {
		declConcrete
		// concreteID is this instantiation's position among the fully-concrete
		// instantiations that share the generic declaration's type definition.
		concreteID uint16
	}
	// DeclConcreteClass is a concrete class declaration.
	DeclConcreteClass struct {
		declConcrete
		// concreteID is this instantiation's position among the fully-concrete
		// instantiations that share the generic declaration's type definition.
		concreteID uint16
	}
	// DeclConcreteInterface is a concrete interface declaration.
	DeclConcreteInterface struct {
		declConcrete
		// concreteID is this instantiation's position among the fully-concrete
		// instantiations that share the generic declaration's type definition.
		concreteID uint16
	}
	// DeclConcreteStruct is a concrete struct declaration.
	DeclConcreteStruct struct {
		declConcrete
		// typeIndex is the index of this concrete struct's type definition within
		// its codegen bucket. Unlike concrete classes/abstracts/interfaces (which
		// share their generic declaration's definition), each concrete struct is
		// flattened into its own definition and so has its own index.
		typeIndex int
	}
)

// tailedBase reports whether what a declaration extends ends in a tail rather
// than in a fixed layout, and rejects a subtype that could not sit behind one.
//
// An unsized tail runs to the end of whatever was allocated for it, so there is
// no offset left over for a subtype to put anything at. A subtype therefore
// declares no instance member of its own, and is itself tailed, because a tail
// is what it inherits and having one is the whole of what being tailed means. It
// is a name for the base's layout under another type rather than an extension of
// it.
//
// Methods and statics are untouched: a method is closed over the instance rather
// than kept in it, and a static lies in its own slot, so neither asks the layout
// for room.
func tailedBase(name string, isTailed bool, baseDeclType *TypeWrapped, instanceFields *base.StaticOrderedMap[string, Field]) (bool, error) {
	if baseDeclType == nil {
		return false, nil
	}
	tailed, ok := baseDeclType.Type.(*TypeTailed)
	if !ok || tailed.Size != nil {
		return false, nil
	}

	if !isTailed {
		return true, fmt.Errorf("%q extends the tailed type %q, so it must be tailed itself", name, tailed.String())
	}
	for _, entry := range instanceFields.All() {
		if member, ok := entry.Value.(*FieldMember); ok {
			return true, fmt.Errorf("%q extends the tailed type %q, so instance member %q has nowhere to lie: nothing follows an unsized tail",
				name, tailed.String(), member.Name())
		}
	}
	return true, nil
}

// newkind creates a new declaration kind.
func newkind(token TokenKind, s string) FieldKind {
	kind := newFieldKind(s)
	declTokenKinds = append(declTokenKinds, token)
	return kind
}

var (
	// declTokenKinds is the token kinds of the declaration.
	declTokenKinds []TokenKind

	// Namespace declaration for static fields and methods
	KindNamespace = newkind(TTNamespace, "namespace")
	// Class declaration for real garbage-collected objects
	KindClass = newkind(TTClass, "class")
	// Abstract declaration for virtual object with base implementations
	KindAbstract = newkind(TTAbstract, "abstract")
	// Interface declaration for virtual object without base implementations
	KindInterface = newkind(TTInterface, "interface")
	// Enum declaration for singleton values
	KindEnum = newkind(TTEnum, "enum")
	// Struct declaration for real non-garbage-collected objects
	KindStruct = newkind(TTStruct, "struct")
)

// newDecl creates a new declaration.
func newDecl(parser SyntaxParser, modifiers Modifier) (Decl, error) {
	tok, err := parser.ConsumeAny(declTokenKinds...)
	if err != nil {
		return nil, err
	}

	var decl Decl
	switch tok.Kind {
	case TTAbstract:
		decl, err = newDeclAbstract(parser, tok, modifiers)
	case TTClass:
		decl, err = newDeclClass(parser, tok, modifiers)
	case TTEnum:
		decl, err = newDeclEnum(parser, tok, modifiers)
	case TTNamespace:
		decl, err = newDeclNamespace(parser, tok, modifiers)
	case TTInterface:
		decl, err = newDeclInterface(parser, tok, modifiers)
	case TTStruct:
		decl, err = newDeclStruct(parser, tok, modifiers)
	}

	if err != nil {
		return nil, err
	}
	return decl, nil
}

// kind returns the kind of the declaration.
func (d *decl) Kind() FieldKind {
	return d.kind
}

// orderFields orders the fields of the declaration.
func (d *decl) orderFields() {
	for _, entry := range d.staticFields.All() {
		field, ok := entry.Value.(FieldIndexable)
		if !ok {
			continue
		}

		i := &d.staticMemberCount
		if field.Kind() == FieldKindMethod {
			i = &d.staticMethodCount
		}
		*field.Index() = *i
		*i++
	}

	for _, entry := range d.instanceFields.All() {
		field := entry.Value.(FieldIndexable)

		i := &d.instanceMemberCount
		if field.Kind() == FieldKindMethod {
			i = &d.instanceMethodCount
		}
		*field.Index() = *i
		*i++
	}
}

// Start implements the [Decl] interface.
func (d *decl) Start() *Token {
	return d.start
}

// Hash implements the [Decl] interface.
func (d *decl) Hash() uint64 {
	if d.outerDecl == nil {
		return base.Hash(d.file.Path.Hash(), base.FNV(d.name))
	}
	return base.Hash(d.outerDecl.Hash(), base.FNV(d.name))
}

// Name implements the [Field] interface.
func (d *decl) Name() string {
	return d.name
}

// Modifiers implements the [Field] interface.
func (d *decl) Modifiers() Modifier {
	return d.modifiers
}

// File implements the [Decl] interface.
func (d *decl) File() *File {
	return d.file
}

// OuterDecl implements the [Decl] interface.
func (d *decl) OuterDecl() Decl {
	return d.outerDecl
}

// orderedFieldsIter returns an iterator over the fields of the declaration for the given field type.
func orderedFieldsIter[T Field](fields *base.StaticOrderedMap[string, Field]) iter.Seq2[string, T] {
	return func(yield func(string, T) bool) {
		for _, entry := range fields.All() {
			if field, ok := entry.Value.(T); ok {
				if !yield(entry.Key, field) {
					break
				}
			}
		}
	}
}

// Decls returns the inner declarations of the declaration.
func (d *decl) Decls() iter.Seq[Decl] {
	return func(yield func(Decl) bool) {
		for _, entry := range d.innerDecls.All() {
			if !yield(entry.Value) {
				break
			}
		}
	}
}

// GetDecl returns the declaration with the given id.
func (d *decl) GetDecl(id string) (Decl, bool) {
	nested, i := d.innerDecls.Get(id)
	if i < 0 {
		return nil, false
	}
	return nested, true
}

// StaticMethods returns the static methods of the declaration.
func (d *decl) StaticMethods() iter.Seq2[string, *FieldMethod] {
	return orderedFieldsIter[*FieldMethod](d.staticFields)
}

// StaticMembers returns the static members of the declaration.
func (d *decl) StaticMembers() iter.Seq2[string, FieldTypeable] {
	return orderedFieldsIter[FieldTypeable](d.staticFields)
}

// StaticFields returns the static fields of the declaration.
func (d *decl) StaticFields() iter.Seq2[string, Field] {
	return func(yield func(string, Field) bool) {
		for _, entry := range d.staticFields.All() {
			if !yield(entry.Key, entry.Value) {
				break
			}
		}
	}
}

// GetStaticField returns the static field with the given id.
func (d *decl) GetStaticField(id string) (FieldIndexable, bool) {
	field, i := d.staticFields.Get(id)
	if i < 0 {
		return nil, false
	}
	if field, ok := field.(FieldIndexable); ok {
		return field, true
	}
	return nil, false
}

// InstanceMethods returns the instance methods of the declaration.
func (d *decl) InstanceMethods() iter.Seq2[string, *FieldMethod] {
	return orderedFieldsIter[*FieldMethod](d.instanceFields)
}

// InstanceMethodCount returns the number of instance methods in the declaration.
func (d *decl) InstanceMethodCount() int {
	return d.instanceMethodCount
}

// InstanceMemberCount returns the number of instance members in the declaration.
func (d *decl) InstanceMemberCount() int {
	return d.instanceMemberCount
}

// TypeIndex returns a pointer to the index of the declaration's type definition
// within its codegen bucket.
func (d *decl) TypeIndex() *int {
	return &d.typeIndex
}

// InstanceByteSize returns a pointer to the cached fixed byte size of an
// instance of the declaration.
func (d *decl) InstanceByteSize() *uint64 {
	return &d.instanceByteSize
}

// InstanceTailByteSize returns a pointer to the cached byte size of a single
// tail element of an instance of the declaration.
func (d *decl) InstanceTailByteSize() *uint64 {
	return &d.instanceTailByteSize
}

// GetInstanceMethod returns the instance method with the given id.
func (d *decl) GetInstanceMethod(id string) (*FieldMethod, bool) {
	field, i := d.instanceFields.Get(id)
	if i < 0 {
		return nil, false
	}
	if field, ok := field.(*FieldMethod); ok {
		return field, true
	}
	return nil, false
}

// GetStaticMethod returns the static method with the given id.
func (d *decl) GetStaticMethod(id string) (*FieldMethod, bool) {
	field, i := d.staticFields.Get(id)
	if i < 0 {
		return nil, false
	}
	if field, ok := field.(*FieldMethod); ok {
		return field, true
	}
	return nil, false
}

// InstanceMembers returns the instance members of the declaration.
func (d *decl) InstanceMembers() iter.Seq2[string, *FieldMember] {
	return orderedFieldsIter[*FieldMember](d.instanceFields)
}

// InstanceFields returns the instance fields of the declaration.
func (d *decl) InstanceFields() iter.Seq2[string, Field] {
	return func(yield func(string, Field) bool) {
		for _, entry := range d.instanceFields.All() {
			if !yield(entry.Key, entry.Value) {
				break
			}
		}
	}
}

// GetInstanceField returns the instance field with the given id.
func (d *decl) GetInstanceField(id string) (FieldTypeable, bool) {
	field, i := d.instanceFields.Get(id)
	if i < 0 {
		return nil, false
	}
	return field.(FieldTypeable), true
}

// AllInstanceFields implements the [Decl] interface. A declaration that cannot
// inherit has exactly the fields it declares.
func (d *decl) AllInstanceFields() *base.StaticOrderedMap[string, Field] {
	return d.instanceFields
}

// flattenedInstanceFields lays a declaration's inherited fields out ahead of the
// ones it declares itself, outermost ancestor foremost. A field that shadows an
// inherited one takes the inherited field's place rather than a new one, which is
// what an override is and what makes a subclass usable where its base is: the two
// agree on where every inherited field sits, so an index derived from either
// reaches the same field.
//
// This resolves the base chain, so it must not be called before types are
// resolved or before inheritance is known to be acyclic, which is why it backs
// [Decl.AllInstanceFields] rather than the plain field accessors the earlier
// semantic passes walk.
func flattenedInstanceFields(own *decl, baseType *TypeWrapped) *base.StaticOrderedMap[string, Field] {
	fields := base.NewStaticOrderedMap[string, Field]()
	if inherited := baseDecl(baseType); inherited != nil {
		for _, entry := range inherited.AllInstanceFields().All() {
			fields.Set(entry.Key, entry.Value)
		}
	}
	for _, entry := range own.instanceFields.All() {
		fields.Set(entry.Key, entry.Value)
	}
	return fields
}

// baseDecl resolves a base declaration type to the declaration it names, or nil
// when there is no base to resolve.
func baseDecl(baseType *TypeWrapped) Decl {
	if baseType == nil {
		return nil
	}
	resolvable, ok := baseType.Type.(TypeResolvable)
	if !ok {
		return nil
	}
	return resolvable.Decl()
}

// inheritedInstanceField resolves an instance field against a declaration and
// then against the classes it descends from, since a member a subclass did not
// declare itself is still a member of it. A subclass that declares the name
// shadows the base, so its own fields are searched first, and the search recurses
// through the whole chain because the base resolves the same way.
func inheritedInstanceField(own *decl, baseType *TypeWrapped, id string) (FieldTypeable, bool) {
	if field, ok := own.GetInstanceField(id); ok {
		return field, true
	}
	if base := baseDecl(baseType); base != nil {
		return base.GetInstanceField(id)
	}
	return nil, false
}

// inheritedInstanceMethod is [inheritedInstanceField] for methods, where the
// shadowing it resolves is an override.
func inheritedInstanceMethod(own *decl, baseType *TypeWrapped, id string) (*FieldMethod, bool) {
	if method, ok := own.GetInstanceMethod(id); ok {
		return method, true
	}
	if base := baseDecl(baseType); base != nil {
		return base.GetInstanceMethod(id)
	}
	return nil, false
}

// Bounds implements the [GenericBound] interface.
func (g GenericLowerBounded) Bounds() []Type {
	return []Type(g)
}

// Bounds implements the [GenericBound] interface.
func (g GenericUpperBounded) Bounds() []Type {
	return []Type(g)
}

// newGenerics creates a new generic bound map for the given owner. The owner
// is passed explicitly so a method's parameter list can be read into a map
// keyed against the method itself rather than against the enclosing decl.
func newGenerics(parser SyntaxParser, owner GenericOwner) (*base.StaticOrderedMap[GenericArgKey, GenericBound], error) {
	generics := base.NewStaticOrderedMap[GenericArgKey, GenericBound]()
	for {
		tok, err := parser.Consume(TTIdentifier)
		if err != nil {
			return nil, err
		}
		id := tok.Value.(string)

		key := GenericArgKey{
			Owner: owner,
			Name:  id,
		}
		if _, i := generics.Get(key); i >= 0 {
			return nil, fmt.Errorf("duplicate generic %q", id)
		}

		var bound GenericBound
		if op, err := parser.Match(TTSubtype, TTSupertype); err != nil {
			return nil, err
		} else if op != nil {
			var bounds []Type
			if tok, err := parser.Match(TTLeftBracket); err != nil {
				return nil, err
			} else if tok == nil {
				_type, err := newType(parser)
				if err != nil {
					return nil, err
				}
				bounds = append(bounds, _type)
			} else {
				for {
					_type, err := newType(parser)
					if err != nil {
						return nil, err
					}
					bounds = append(bounds, _type)

					if tok, err := parser.ConsumeAny(TTComma, TTRightBracket); err != nil {
						return nil, err
					} else if tok.Kind == TTRightBracket {
						break
					}
				}
			}

			switch op.Kind {
			case TTSubtype:
				bound = GenericUpperBounded(bounds)
			case TTSupertype:
				bound = GenericLowerBounded(bounds)
			}
		}

		generics.Set(key, bound)

		if tok, err := parser.ConsumeAny(TTComma, TTRightBracket); err != nil {
			return nil, err
		} else if tok.Kind == TTRightBracket {
			break
		}
	}
	return generics, nil
}

// Concretes implements the [DeclGeneric] interface.
func (d *declGeneric) Concretes() *TypeMap[DeclTypeable] {
	return d.concretes
}

// Generics implements the [DeclGeneric] interface.
func (d *declGeneric) Generics() *base.StaticOrderedMap[GenericArgKey, GenericBound] {
	return d.generics
}

// GenericTypes implements the [DeclGeneric] interface.
func (d *declGeneric) GenericTypes() *TypeSet {
	return d.genericTypes
}

// newGenericType creates a new generic type for the declaration.
func newGenericType(d DeclGeneric) Type {
	var t TypeResolvable = &TypeReference{
		outerDecl: d,
		Ids:       []string{d.Name()},
		decl:      d,
	}
	if d.Generics() != nil {
		parameterTypes := make([]Type, d.Generics().Len())
		for i, entry := range d.Generics().All() {
			genericArg := &TypeGenericArg{
				start: d.Start(),
				owner: d,
				Name:  entry.Key.Name,
			}
			parameterTypes[i] = genericArg
		}
		t = &TypeGeneric{
			outerDecl:      d,
			Type:           t,
			ParameterTypes: parameterTypes,
		}
	}
	return t
}

// duplicateFieldError returns an error for a duplicate field.
func duplicateFieldError(field Field) error {
	modifierStr := "instance"
	if field.Modifiers().Has(ModifierStatic) {
		modifierStr = "static"
	}
	return fmt.Errorf("duplicate %s field %q", modifierStr, field.Name())
}

// checkFieldCollision reports an error when adding field would clash with
// something already recorded on the declaration. Methods and members share a
// static/instance namespace — a static method still rejects a same-name
// instance method — while an inner class lives in its own namespace, so an
// inner class and a method/member of the same name coexist whether the
// method/member is static or instance.
func checkFieldCollision(static, instance *base.StaticOrderedMap[string, Field], inner *base.StaticOrderedMap[string, Decl], field Field) error {
	if _, ok := field.(Decl); ok {
		if _, i := inner.Get(field.Name()); i >= 0 {
			return duplicateFieldError(field)
		}
		return nil
	}

	if field.Modifiers().Has(ModifierStatic) {
		if _, i := static.Get(field.Name()); i >= 0 {
			return duplicateFieldError(field)
		}
		if _, i := instance.Get(field.Name()); i >= 0 {
			return duplicateFieldError(field)
		}
		return nil
	}

	if _, i := instance.Get(field.Name()); i >= 0 {
		return duplicateFieldError(field)
	}
	if _, i := static.Get(field.Name()); i >= 0 {
		return duplicateFieldError(field)
	}
	return nil
}

// PutInterfaceIfImplemented records i on d if d has a method for every method i
// declares, which is what implementing an interface is.
//
// A subtype check reaches the same test on its own, but only ever for the pair
// it was asked about, and the answer is recorded on the declaration it was
// asked of rather than on the one the instance turns out to be. Exposing the
// test lets it be run over every pair before the dispatch tables are built.
func PutInterfaceIfImplemented(d DeclInheritable, i DeclTypeable) {
	if t, ok := i.DeclType().(TypeResolvable); ok {
		declInherits(d, t)
	}
}

// declInherits checks if the declaration inherits from the parent type.
func declInherits(d DeclInheritable, parentType TypeResolvable) bool {
	if d.HasInterface(parentType) {
		return true
	}
	for name, parentMethod := range parentType.Decl().InstanceMethods() {
		method, ok := d.GetInstanceMethod(name)
		if !ok {
			return false
		}

		if !method.FieldType().IsSubtypeOf(parentMethod.FieldType()) {
			return false
		}
	}

	d.PutInterface(parentType)
	return true
}

// declSubtypeableIsSubtypeOf checks if the declaration is a subtype of the parent type.
func declSubtypeableIsSubtypeOf(d DeclSubtypeable, parentType TypeResolvable) bool {
	parentDecl := parentType.Decl()
	if parentDecl.Kind() == KindInterface {
		return declInherits(d, parentType)
	}

	for ancestor := d.BaseDeclType(); ancestor != nil; {
		decl := ancestor.Type.(TypeResolvable).Decl()
		if decl == parentDecl {
			return true
		}

		inheritable, ok := decl.(DeclSubtypeable)
		if !ok || inheritable.BaseDeclType() == nil {
			break
		}
		ancestor = inheritable.BaseDeclType()
	}

	return false
}

// objectBaseDeclType returns a default base declaration type for the std.Obj declaration.
func objectBaseDeclType(parser SyntaxParser, d Decl) *TypeWrapped {
	f := *parser.File()
	// Obj is the root of the base-class chain, so Obj itself must not descend
	// from Obj: the file holding it is the one std file where no auto-base is
	// wired in. Every other std file is a class or an interface like any
	// other and gets Obj as its base the same way a user file does.
	objectPath := base.StdPath("lang/Obj" + base.CodeFileExtension)
	if base.IsStdPath(f.Path) && f.Path.String() == objectPath.String() {
		return nil
	}

	// Obj sits under the std package key beside anything else the file
	// imports from std. The user may have written `import std("Pair.zam")`,
	// which already claims the key for Pair, but Obj still has to be
	// reachable from here — the base of every class — so this appends rather
	// than overwrites, and skips when a previous call has already appended it.
	already := false
	for _, imp := range f.Imports[base.StdPackage] {
		if imp.Path != nil && imp.Path.String() == objectPath.String() {
			already = true
			break
		}
	}
	if !already {
		pkg := base.StdPackage
		f.Imports[base.StdPackage] = append(f.Imports[base.StdPackage], &Import{
			File:    f,
			Package: &pkg,
			Path:    objectPath,
		})
		parser.AddPath(objectPath)
	}

	return &TypeWrapped{
		kind:      TypeKindPointer,
		outerDecl: d,
		Type: &TypeReference{
			outerDecl: d,
			Ids:       []string{base.StdPackage, "Obj"},
		},
	}
}

// putObjectMethods records on an interface the methods every object has. An
// interface is implemented only by a class or an abstract, and each of those
// descends from std.Object, so an implementor carries these whether the interface
// asked for them or not. Recording them is what lets a value held as the
// interface be compared, hashed, written out or marked, the same as a value held
// as its own type: the interface is a view of an object, not of something less.
//
// The signatures are written out here rather than read off Object, because a file
// is parsed on its own and Object need not be parsed yet — what this point can
// name is a reference to Object, not its fields. An interface that declares one
// of these itself has already said what it wants of an implementor, so its own
// declaration is what stands and this adds nothing.
func putObjectMethods(parser SyntaxParser, d *DeclInterface) {
	object := objectBaseDeclType(parser, d)
	if object == nil {
		return
	}

	for _, m := range objectMethods(object) {
		if _, i := d.staticFields.Get(m.name); i >= 0 {
			continue
		} else if _, i := d.instanceFields.Get(m.name); i >= 0 {
			continue
		}

		d.instanceFields.Set(m.name, &FieldMethod{
			field: field{
				start:     d.start,
				outerDecl: d,
				name:      m.name,
			},
			// A method an interface asks for is one an implementor answers,
			// which is what virtual says, and the answer is reached from
			// wherever the interface is, which is what public says.
			modifiers: ModifierVirtual | ModifierPublic,
			fieldType: m.signature,
		})
	}
}

// objectMethod is one of the methods std.Object declares.
type objectMethod struct {
	name      string
	signature *TypeFunction
}

// objectMethods are the instance methods std.Object declares, in the order it
// declares them, given the Object* that its comparisons take.
func objectMethods(object *TypeWrapped) []objectMethod {
	outer := object.OuterDecl()
	return []objectMethod{
		{base.EqualMethodName, &TypeFunction{
			outerDecl:      outer,
			ParameterTypes: []Type{object},
			ReturnType:     TypeBool,
		}},
		{base.CompareMethodName, &TypeFunction{
			outerDecl:      outer,
			ParameterTypes: []Type{object},
			ReturnType:     TypeInt,
		}},
		{base.HashMethodName, &TypeFunction{outerDecl: outer, ReturnType: TypeInt}},
		{base.StringMethodName, &TypeFunction{outerDecl: outer, ReturnType: TypeString}},
		{base.MarkMethodName, &TypeFunction{outerDecl: outer, ReturnType: TypeVoid}},
		{base.DeleteMethodName, &TypeFunction{outerDecl: outer, ReturnType: TypeVoid}},
	}
}

// newDeclAbstract creates a new abstract declaration.
func newDeclAbstract(parser SyntaxParser, start *Token, modifiers Modifier) (*DeclAbstract, error) {
	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("abstracts cannot be virtual")
	}

	id, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	name := id.Value.(string)

	decl := &DeclAbstract{
		declGeneric: declGeneric{
			decl: decl{
				start:          start,
				file:           *parser.File(),
				outerDecl:      *parser.Decl(),
				kind:           KindAbstract,
				modifiers:      modifiers,
				name:           name,
				staticFields:   base.NewStaticOrderedMap[string, Field](),
				instanceFields: base.NewStaticOrderedMap[string, Field](),
				innerDecls:     base.NewStaticOrderedMap[string, Decl](),
			},
		},
		interfaces: NewTypeSet(),
	}
	*parser.Decl() = decl

	if tok, err := parser.Match(TTTilde); err != nil {
		return nil, err
	} else if tok != nil {
		decl.isTailed = true
	}

	if tok, err := parser.Match(TTLeftBracket); err != nil {
		return nil, err
	} else if tok != nil {
		if decl.generics, err = newGenerics(parser, decl); err != nil {
			return nil, err
		}
		decl.concretes = NewTypeMap[DeclTypeable]()
		decl.genericTypes = NewTypeSet()
	}

	if tok, err := parser.Match(TTSubtype); err != nil {
		return nil, err
	} else if tok != nil {
		baseDeclType, err := newType(parser)
		if err != nil {
			return nil, err
		}
		// The pointer is the wrapper and the declaration it names is what the
		// wrapper holds, which is the shape objectBaseDeclType builds and the
		// shape every reader of BaseDeclType unwraps.
		wt, ok := baseDeclType.(*TypeWrapped)
		if ok && wt.Kind() == TypeKindPointer {
			_, ok = wt.Type.(TypeResolvable)
		}
		if !ok || wt.Kind() != TypeKindPointer {
			return nil, fmt.Errorf("base declaration type %q is not a pointer to a resolvable type", baseDeclType.String())
		}
		decl.baseDeclType = wt
	}

	if decl.baseDeclType == nil {
		decl.baseDeclType = objectBaseDeclType(parser, decl)
	}

	if _, err := parser.Consume(TTLeftBrace); err != nil {
		return nil, err
	}

	var tailedField *FieldMember
	for {
		if tok, err := parser.Match(TTRightBrace); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		field, err := newField(parser, decl.isTailed, false)
		if err != nil {
			return nil, err
		}

		if err := checkFieldCollision(decl.staticFields, decl.instanceFields, decl.innerDecls, field); err != nil {
			return nil, err
		}

		if nested, ok := field.(Decl); ok {
			decl.innerDecls.Set(field.Name(), nested)
		} else if field.Modifiers().Has(ModifierStatic) {
			decl.staticFields.Set(field.Name(), field)
		} else {
			if tailedField != nil && field.Kind() == FieldKindMember {
				return nil, fmt.Errorf("tailed field %q is not the last instance member", tailedField.Name())
			}
			ft, ok := field.(FieldTypeable)
			if !ok {
				return nil, fmt.Errorf("instance field %q is not typeable", field.Name())
			}
			decl.instanceFields.Set(field.Name(), ft)
			if (ft.FieldType().Kind() == TypeKindArray && ft.FieldType().(*TypeArray).Size == nil) ||
				(ft.FieldType().Kind() == TypeKindTailed && ft.FieldType().(*TypeTailed).Size == nil) {
				tailedField = ft.(*FieldMember)
			}
		}
	}

	baseDeclIsTailed, err := tailedBase(decl.name, decl.isTailed, decl.baseDeclType, decl.instanceFields)
	if err != nil {
		return nil, err
	}
	if decl.isTailed && tailedField == nil && !baseDeclIsTailed {
		return nil, fmt.Errorf("missing tailed field")
	}

	*parser.Decl() = decl.outerDecl
	decl.orderFields()
	return decl, nil
}

// Accept implements the [Decl] interface.
func (d *DeclAbstract) Accept(visitor FieldVisitor) error {
	return visitor.VisitDeclAbstract(d)
}

// BaseDeclType implements the [DeclSubtypeable] interface.
func (a *DeclAbstract) BaseDeclType() *TypeWrapped {
	return a.baseDeclType
}

// GetInstanceField implements the [Decl] interface, searching the base chain.
func (a *DeclAbstract) GetInstanceField(id string) (FieldTypeable, bool) {
	return inheritedInstanceField(&a.decl, a.baseDeclType, id)
}

// GetInstanceMethod implements the [Decl] interface, searching the base chain.
func (a *DeclAbstract) GetInstanceMethod(id string) (*FieldMethod, bool) {
	return inheritedInstanceMethod(&a.decl, a.baseDeclType, id)
}

// AllInstanceFields implements the [Decl] interface, laying inherited fields out
// ahead of declared ones.
func (a *DeclAbstract) AllInstanceFields() *base.StaticOrderedMap[string, Field] {
	return flattenedInstanceFields(&a.decl, a.baseDeclType)
}

// DeclType implements the [Decl] interface.
func (a *DeclAbstract) DeclType() Type {
	return newGenericType(a)
}

// GetConcreteDecl implements the [DeclGeneric] interface.
func (a *DeclAbstract) GetConcreteDecl(t *TypeGeneric) (DeclTypeable, bool) {
	cd, index := a.concretes.Get(t)
	if index >= 0 {
		return cd, false
	}
	types := make(map[GenericArgKey]Type, len(t.ParameterTypes))
	for i, entry := range a.generics.All() {
		types[entry.Key] = t.ParameterTypes[i]
	}
	cd = &DeclConcreteAbstract{
		declConcrete: declConcrete{
			DeclGeneric: a,
			types:       types,
			interfaces:  NewTypeSet(),
		},
	}
	a.concretes.Set(t, cd)
	return cd, true
}

// HasInterface implements the [DeclInheritable] interface.
func (a *DeclAbstract) HasInterface(t Type) bool {
	return a.interfaces.Contains(t)
}

// PutInterface implements the [DeclInheritable] interface.
func (a *DeclAbstract) PutInterface(t Type) {
	a.interfaces.Add(t)
}

// IsSubtypeOf implements the [DeclInheritable] interface.
func (a *DeclAbstract) IsSubtypeOf(t Type) bool {
	if t.Kind() != TypeKindPointer {
		return false
	}
	tr, ok := t.(*TypeWrapped).Type.(TypeResolvable)
	if !ok {
		return false
	}
	return declSubtypeableIsSubtypeOf(a, tr)
}

// Interfaces implements the [DeclInheritable] interface.
func (a *DeclAbstract) Interfaces() iter.Seq[Type] {
	return a.interfaces.Values()
}

// InterfaceCount implements the [DeclInheritable] interface.
func (a *DeclAbstract) InterfaceCount() int {
	return a.interfaces.Len()
}

// IsTailed implements the [DeclSubtypeable] interface.
func (a *DeclAbstract) IsTailed() bool {
	return a.isTailed
}

// newDeclClass creates a new class declaration.
func newDeclClass(parser SyntaxParser, start *Token, modifiers Modifier) (*DeclClass, error) {
	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("classes cannot be virtual")
	}

	id, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	name := id.Value.(string)

	decl := &DeclClass{
		declGeneric: declGeneric{
			decl: decl{
				start:          start,
				file:           *parser.File(),
				outerDecl:      *parser.Decl(),
				kind:           KindClass,
				modifiers:      modifiers,
				name:           name,
				staticFields:   base.NewStaticOrderedMap[string, Field](),
				instanceFields: base.NewStaticOrderedMap[string, Field](),
				innerDecls:     base.NewStaticOrderedMap[string, Decl](),
			},
		},
		interfaces: NewTypeSet(),
	}
	*parser.Decl() = decl

	if tok, err := parser.Match(TTTilde); err != nil {
		return nil, err
	} else if tok != nil {
		decl.isTailed = true
	}

	if tok, err := parser.Match(TTLeftBracket); err != nil {
		return nil, err
	} else if tok != nil {
		if decl.generics, err = newGenerics(parser, decl); err != nil {
			return nil, err
		}
		decl.concretes = NewTypeMap[DeclTypeable]()
		decl.genericTypes = NewTypeSet()
	}

	if tok, err := parser.Match(TTSubtype); err != nil {
		return nil, err
	} else if tok != nil {
		baseDeclType, err := newType(parser)
		if err != nil {
			return nil, err
		}
		// The pointer is the wrapper and the declaration it names is what the
		// wrapper holds, which is the shape objectBaseDeclType builds and the
		// shape every reader of BaseDeclType unwraps.
		wt, ok := baseDeclType.(*TypeWrapped)
		if ok && wt.Kind() == TypeKindPointer {
			_, ok = wt.Type.(TypeResolvable)
		}
		if !ok || wt.Kind() != TypeKindPointer {
			return nil, fmt.Errorf("base declaration type %q is not a pointer to a resolvable type", baseDeclType.String())
		}
		decl.baseDeclType = wt
	}

	if decl.baseDeclType == nil {
		decl.baseDeclType = objectBaseDeclType(parser, decl)
	}

	if _, err := parser.Consume(TTLeftBrace); err != nil {
		return nil, err
	}

	var tailedField *FieldMember
	for {
		if tok, err := parser.Match(TTRightBrace); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		field, err := newField(parser, decl.isTailed, false)
		if err != nil {
			return nil, err
		}

		if err := checkFieldCollision(decl.staticFields, decl.instanceFields, decl.innerDecls, field); err != nil {
			return nil, err
		}

		if nested, ok := field.(Decl); ok {
			decl.innerDecls.Set(field.Name(), nested)
		} else if field.Modifiers().Has(ModifierStatic) {
			decl.staticFields.Set(field.Name(), field)
		} else {
			if field.Modifiers().Has(ModifierVirtual) {
				return nil, fmt.Errorf("virtual field %q is not allowed in class", field.Name())
			} else if tailedField != nil && field.Kind() == FieldKindMember {
				return nil, fmt.Errorf("tailed field %q is not the last instance member", tailedField.Name())
			}
			ft, ok := field.(FieldTypeable)
			if !ok {
				return nil, fmt.Errorf("instance field %q is not typeable", field.Name())
			}
			decl.instanceFields.Set(field.Name(), ft)
			if (ft.FieldType().Kind() == TypeKindArray && ft.FieldType().(*TypeArray).Size == nil) ||
				(ft.FieldType().Kind() == TypeKindTailed && ft.FieldType().(*TypeTailed).Size == nil) {
				tailedField = ft.(*FieldMember)
			}
		}
	}

	baseDeclIsTailed, err := tailedBase(decl.name, decl.isTailed, decl.baseDeclType, decl.instanceFields)
	if err != nil {
		return nil, err
	}
	if decl.isTailed && tailedField == nil && !baseDeclIsTailed {
		return nil, fmt.Errorf("missing tailed field")
	}

	*parser.Decl() = decl.outerDecl
	decl.orderFields()
	return decl, nil
}

// Accept implements the [Decl] interface.
func (d *DeclClass) Accept(visitor FieldVisitor) error {
	return visitor.VisitDeclClass(d)
}

// BaseDeclType implements the [DeclSubtypeable] interface.
func (c *DeclClass) BaseDeclType() *TypeWrapped {
	return c.baseDeclType
}

// GetInstanceField implements the [Decl] interface, searching the base chain.
func (c *DeclClass) GetInstanceField(id string) (FieldTypeable, bool) {
	return inheritedInstanceField(&c.decl, c.baseDeclType, id)
}

// GetInstanceMethod implements the [Decl] interface, searching the base chain.
func (c *DeclClass) GetInstanceMethod(id string) (*FieldMethod, bool) {
	return inheritedInstanceMethod(&c.decl, c.baseDeclType, id)
}

// AllInstanceFields implements the [Decl] interface, laying inherited fields out
// ahead of declared ones.
func (c *DeclClass) AllInstanceFields() *base.StaticOrderedMap[string, Field] {
	return flattenedInstanceFields(&c.decl, c.baseDeclType)
}

// DeclType implements the [Decl] interface.
func (c *DeclClass) DeclType() Type {
	return newGenericType(c)
}

// GetConcreteDecl implements the [DeclGeneric] interface.
func (c *DeclClass) GetConcreteDecl(t *TypeGeneric) (DeclTypeable, bool) {
	cd, index := c.concretes.Get(t)
	if index >= 0 {
		return cd, false
	}
	types := make(map[GenericArgKey]Type, len(t.ParameterTypes))
	for i, entry := range c.generics.All() {
		types[entry.Key] = t.ParameterTypes[i]
	}
	cd = &DeclConcreteClass{
		declConcrete: declConcrete{
			DeclGeneric: c,
			types:       types,
			interfaces:  NewTypeSet(),
		},
	}
	c.concretes.Set(t, cd)
	return cd, true
}

// HasInterface implements the [DeclInheritable] interface.
func (c *DeclClass) HasInterface(t Type) bool {
	return c.interfaces.Contains(t)
}

// PutInterface implements the [DeclInheritable] interface.
func (c *DeclClass) PutInterface(t Type) {
	c.interfaces.Add(t)
}

// IsSubtypeOf implements the [DeclInheritable] interface.
func (c *DeclClass) IsSubtypeOf(t Type) bool {
	if t.Kind() != TypeKindPointer {
		return false
	}
	tr, ok := t.(*TypeWrapped).Type.(TypeResolvable)
	if !ok {
		return false
	}
	return declSubtypeableIsSubtypeOf(c, tr)
}

// Interfaces implements the [DeclInheritable] interface.
func (c *DeclClass) Interfaces() iter.Seq[Type] {
	return c.interfaces.Values()
}

// InterfaceCount implements the [DeclInheritable] interface.
func (c *DeclClass) InterfaceCount() int {
	return c.interfaces.Len()
}

// IsTailed implements the [DeclSubtypeable] interface.
func (c *DeclClass) IsTailed() bool {
	return c.isTailed
}

// newDeclEnum creates a new enum declaration.
func newDeclEnum(parser SyntaxParser, start *Token, modifiers Modifier) (*DeclEnum, error) {
	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("enums cannot be virtual")
	}

	id, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	name := id.Value.(string)

	decl := &DeclEnum{
		decl: decl{
			start:          start,
			file:           *parser.File(),
			outerDecl:      *parser.Decl(),
			kind:           KindEnum,
			modifiers:      modifiers,
			name:           name,
			staticFields:   base.NewStaticOrderedMap[string, Field](),
			instanceFields: base.NewStaticOrderedMap[string, Field](),
			innerDecls:     base.NewStaticOrderedMap[string, Decl](),
		},
	}
	*parser.Decl() = decl

	if _, err := parser.Consume(TTLeftBrace); err != nil {
		return nil, err
	}

	for {
		if tok, err := parser.Match(TTSemiColon); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		field, err := newFieldEnum(parser)
		if err != nil {
			return nil, err
		}

		_, staticIndex := decl.staticFields.Get(field.Name())
		if staticIndex >= 0 {
			return nil, duplicateFieldError(field)
		}
		decl.staticFields.Set(field.Name(), field)

		if tok, err := parser.ConsumeAny(TTComma, TTSemiColon); err != nil {
			return nil, err
		} else if tok.Kind == TTSemiColon {
			break
		}
	}

	for {
		if tok, err := parser.Match(TTRightBrace); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		field, err := newField(parser, false, false)
		if err != nil {
			return nil, err
		}

		if err := checkFieldCollision(decl.staticFields, decl.instanceFields, decl.innerDecls, field); err != nil {
			return nil, err
		}

		if nested, ok := field.(Decl); ok {
			decl.innerDecls.Set(field.Name(), nested)
		} else if field.Modifiers().Has(ModifierStatic) {
			decl.staticFields.Set(field.Name(), field)
		} else {
			if field.Modifiers().Has(ModifierVirtual) {
				return nil, fmt.Errorf("virtual field %q is not allowed in enum", field.Name())
			}
			decl.instanceFields.Set(field.Name(), field)
		}
	}

	*parser.Decl() = decl.outerDecl
	decl.orderFields()
	return decl, nil
}

// Accept implements the [Decl] interface.
func (d *DeclEnum) Accept(visitor FieldVisitor) error {
	return visitor.VisitDeclEnum(d)
}

// DeclType implements the [DeclTypeable] interface.
func (d *DeclEnum) DeclType() Type {
	return &TypeReference{
		decl:      d,
		outerDecl: d,
		Ids:       []string{d.name},
	}
}

// newDeclInterface creates a new interface declaration.
func newDeclInterface(parser SyntaxParser, start *Token, modifiers Modifier) (*DeclInterface, error) {
	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("interfaces cannot be virtual")
	}

	id, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	name := id.Value.(string)

	decl := &DeclInterface{
		declGeneric: declGeneric{
			decl: decl{
				start:          start,
				file:           *parser.File(),
				outerDecl:      *parser.Decl(),
				kind:           KindInterface,
				modifiers:      modifiers,
				name:           name,
				staticFields:   base.NewStaticOrderedMap[string, Field](),
				instanceFields: base.NewStaticOrderedMap[string, Field](),
				innerDecls:     base.NewStaticOrderedMap[string, Decl](),
			},
		},
		interfaces: NewTypeSet(),
	}
	*parser.Decl() = decl

	if tok, err := parser.Match(TTLeftBracket); err != nil {
		return nil, err
	} else if tok != nil {
		if decl.generics, err = newGenerics(parser, decl); err != nil {
			return nil, err
		}
		decl.concretes = NewTypeMap[DeclTypeable]()
		decl.genericTypes = NewTypeSet()
	}

	if _, err := parser.Consume(TTLeftBrace); err != nil {
		return nil, err
	}

	for {
		if tok, err := parser.Match(TTRightBrace); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		field, err := newField(parser, false, false)
		if err != nil {
			return nil, err
		}

		if err := checkFieldCollision(decl.staticFields, decl.instanceFields, decl.innerDecls, field); err != nil {
			return nil, err
		}

		if nested, ok := field.(Decl); ok {
			decl.innerDecls.Set(field.Name(), nested)
		} else if field.Modifiers().Has(ModifierStatic) {
			decl.staticFields.Set(field.Name(), field)
		} else {
			f, ok := field.(*FieldMethod)
			if !ok {
				return nil, fmt.Errorf("instance member %q is not allowed in interface", field.Name())
			} else if !field.Modifiers().Has(ModifierVirtual) {
				return nil, fmt.Errorf("instance method %q must be virtual in interface", field.Name())
			} else if f.Modifiers().Has(ModifierProtected) {
				return nil, fmt.Errorf("instance method %q cannot be protected in interface", field.Name())
			}
			decl.instanceFields.Set(field.Name(), f)
		}
	}

	putObjectMethods(parser, decl)

	*parser.Decl() = decl.outerDecl
	decl.orderFields()
	return decl, nil
}

// Accept implements the [Decl] interface.
func (d *DeclInterface) Accept(visitor FieldVisitor) error {
	return visitor.VisitDeclInterface(d)
}

// DeclType implements the [DeclTypeable] interface.
func (i *DeclInterface) DeclType() Type {
	return newGenericType(i)
}

// GetConcreteDecl implements the [DeclGeneric] interface.
func (i *DeclInterface) GetConcreteDecl(t *TypeGeneric) (DeclTypeable, bool) {
	cd, index := i.concretes.Get(t)
	if index >= 0 {
		return cd, false
	}
	types := make(map[GenericArgKey]Type, len(t.ParameterTypes))
	for i, entry := range i.generics.All() {
		types[entry.Key] = t.ParameterTypes[i]
	}
	cd = &DeclConcreteInterface{
		declConcrete: declConcrete{
			DeclGeneric: i,
			types:       types,
			interfaces:  NewTypeSet(),
		},
	}
	i.concretes.Set(t, cd)
	return cd, true
}

// HasInterface implements the [DeclInheritable] interface.
func (i *DeclInterface) HasInterface(t Type) bool {
	return i.interfaces.Contains(t)
}

// PutInterface implements the [DeclInheritable] interface.
func (i *DeclInterface) PutInterface(t Type) {
	i.interfaces.Add(t)
}

// IsSubtypeOf implements the [DeclInheritable] interface.
func (i *DeclInterface) IsSubtypeOf(t Type) bool {
	if t.Kind() != TypeKindPointer {
		return false
	}
	tr, ok := t.(*TypeWrapped).Type.(TypeResolvable)
	if !ok {
		return false
	}
	return declInherits(i, tr)
}

// Interfaces implements the [DeclInheritable] interface.
func (i *DeclInterface) Interfaces() iter.Seq[Type] {
	return i.interfaces.Values()
}

// InterfaceCount implements the [DeclInheritable] interface.
func (i *DeclInterface) InterfaceCount() int {
	return i.interfaces.Len()
}

// newDeclNamespace creates a new namespace declaration.
func newDeclNamespace(parser SyntaxParser, start *Token, modifiers Modifier) (*DeclNamespace, error) {
	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("namespaces cannot be virtual")
	}

	id, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	name := id.Value.(string)

	decl := &DeclNamespace{
		decl: decl{
			start:          start,
			file:           *parser.File(),
			outerDecl:      *parser.Decl(),
			kind:           KindNamespace,
			modifiers:      modifiers,
			name:           name,
			staticFields:   base.NewStaticOrderedMap[string, Field](),
			instanceFields: base.NewStaticOrderedMap[string, Field](),
			innerDecls:     base.NewStaticOrderedMap[string, Decl](),
		},
	}
	*parser.Decl() = decl

	if _, err := parser.Consume(TTLeftBrace); err != nil {
		return nil, err
	}

	for {
		if tok, err := parser.Match(TTRightBrace); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		field, err := newField(parser, false, true)
		if err != nil {
			return nil, err
		}

		if err := checkFieldCollision(decl.staticFields, decl.instanceFields, decl.innerDecls, field); err != nil {
			return nil, err
		}

		if nested, ok := field.(Decl); ok {
			decl.innerDecls.Set(field.Name(), nested)
		} else {
			decl.staticFields.Set(field.Name(), field)
		}
	}

	*parser.Decl() = decl.outerDecl
	decl.orderFields()
	return decl, nil
}

// Accept implements the [Decl] interface.
func (d *DeclNamespace) Accept(visitor FieldVisitor) error {
	return visitor.VisitDeclNamespace(d)
}

// DeclType implements the [DeclTypeable] interface. A namespace holds no
// instances and has no shape of its own, but needs to be reachable through a
// type expression — `type(Objs).Delete(obj)` is the only way to name its
// statics — and the one piece of a type that question reads is the name.
func (d *DeclNamespace) DeclType() Type {
	return &TypeReference{
		decl:      d,
		outerDecl: d,
		Ids:       []string{d.name},
	}
}

// newDeclStruct creates a new struct declaration.
func newDeclStruct(parser SyntaxParser, start *Token, modifiers Modifier) (*DeclStruct, error) {
	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("structs cannot be virtual")
	}

	id, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	name := id.Value.(string)

	decl := &DeclStruct{
		declGeneric: declGeneric{
			decl: decl{
				start:          start,
				file:           *parser.File(),
				outerDecl:      *parser.Decl(),
				kind:           KindStruct,
				modifiers:      modifiers,
				name:           name,
				staticFields:   base.NewStaticOrderedMap[string, Field](),
				instanceFields: base.NewStaticOrderedMap[string, Field](),
				innerDecls:     base.NewStaticOrderedMap[string, Decl](),
			},
		},
	}
	*parser.Decl() = decl

	if tok, err := parser.Match(TTTilde); err != nil {
		return nil, err
	} else if tok != nil {
		decl.isTailed = true
	}

	if tok, err := parser.Match(TTLeftBracket); err != nil {
		return nil, err
	} else if tok != nil {
		if decl.generics, err = newGenerics(parser, decl); err != nil {
			return nil, err
		}
		decl.concretes = NewTypeMap[DeclTypeable]()
		decl.genericTypes = NewTypeSet()
	}

	if _, err := parser.Consume(TTLeftBrace); err != nil {
		return nil, err
	}

	var tailedField *FieldMember
	for {
		if tok, err := parser.Match(TTRightBrace); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		field, err := newField(parser, decl.isTailed, false)
		if err != nil {
			return nil, err
		}

		if err := checkFieldCollision(decl.staticFields, decl.instanceFields, decl.innerDecls, field); err != nil {
			return nil, err
		}

		if nested, ok := field.(Decl); ok {
			decl.innerDecls.Set(field.Name(), nested)
		} else if field.Modifiers().Has(ModifierStatic) {
			decl.staticFields.Set(field.Name(), field)
		} else {
			if field.Modifiers().Has(ModifierVirtual) {
				return nil, fmt.Errorf("virtual field %q is not allowed in struct", field.Name())
			} else if tailedField != nil && field.Kind() == FieldKindMember {
				return nil, fmt.Errorf("tailed field %q is not the last instance member", tailedField.Name())
			}
			ft, ok := field.(FieldTypeable)
			if !ok {
				return nil, fmt.Errorf("instance field %q is not typeable", field.Name())
			}
			decl.instanceFields.Set(field.Name(), ft)
			if (ft.FieldType().Kind() == TypeKindArray && ft.FieldType().(*TypeArray).Size == nil) ||
				(ft.FieldType().Kind() == TypeKindTailed && ft.FieldType().(*TypeTailed).Size == nil) {
				tailedField = ft.(*FieldMember)
			}
		}
	}

	if decl.isTailed && tailedField == nil {
		return nil, fmt.Errorf("missing tailed field")
	}

	*parser.Decl() = decl.outerDecl
	decl.orderFields()
	return decl, nil
}

// Accept implements the [Decl] interface.
func (d *DeclStruct) Accept(visitor FieldVisitor) error {
	return visitor.VisitDeclStruct(d)
}

// DeclType implements the [DeclTypeable] interface. A tailed struct is spelled
// `S~[args]` wherever it is referred to, and the receiver and pointer-pointee
// types a later pass collects from this must match the keys the user wrote
// against. Returning the non-tailed form would open two concrete entries per
// instantiation that resolve to the same declaration under separate type ids,
// and the one created after the type-definition skeleton has been sized would
// land with no slot of its own.
func (s *DeclStruct) DeclType() Type {
	if s.isTailed {
		t := newGenericType(s).(*TypeGeneric)
		t.Type = &TypeTailed{
			start:     s.Start(),
			outerDecl: s,
			Type:      t.Type.(*TypeReference),
		}
		return t
	}
	return newGenericType(s)
}

// IsTailed implements the [DeclTailable] interface.
func (s *DeclStruct) IsTailed() bool {
	return s.isTailed
}

// GetConcreteDecl implements the [DeclGeneric] interface.
func (s *DeclStruct) GetConcreteDecl(t *TypeGeneric) (DeclTypeable, bool) {
	cd, index := s.concretes.Get(t)
	if index >= 0 {
		return cd, false
	}
	types := make(map[GenericArgKey]Type, len(t.ParameterTypes))
	for i, entry := range s.generics.All() {
		types[entry.Key] = t.ParameterTypes[i]
	}
	cd = &DeclConcreteStruct{
		declConcrete: declConcrete{
			DeclGeneric: s,
			types:       types,
			interfaces:  NewTypeSet(),
		},
	}
	s.concretes.Set(t, cd)
	return cd, true
}

// mapFields maps the fields to the given types.
func mapFields[T Field](fields iter.Seq2[string, T], types map[GenericArgKey]Type) iter.Seq2[string, T] {
	return func(yield func(string, T) bool) {
		for _, field := range fields {
			if field, ok := any(field).(FieldTypeable); ok {
				if !yield(field.Name(), field.Map(types).(T)) {
					return
				}
			}
		}
	}
}

// InstanceFields implements the [Decl] interface.
func (d *declConcrete) InstanceFields() iter.Seq2[string, Field] {
	return mapFields(d.DeclGeneric.InstanceFields(), d.types)
}

// InstanceMembers implements the [Decl] interface.
func (d *declConcrete) InstanceMembers() iter.Seq2[string, *FieldMember] {
	return mapFields(d.DeclGeneric.InstanceMembers(), d.types)
}

// InstanceMethods implements the [Decl] interface.
func (d *declConcrete) InstanceMethods() iter.Seq2[string, *FieldMethod] {
	return mapFields(d.DeclGeneric.InstanceMethods(), d.types)
}

// GetInstanceField implements the [Decl] interface.
func (d *declConcrete) GetInstanceField(id string) (FieldTypeable, bool) {
	df, ok := d.DeclGeneric.GetInstanceField(id)
	if !ok {
		return nil, false
	}
	return df.Map(d.types), true
}

// GetInstanceMethod implements the [Decl] interface.
func (d *declConcrete) GetInstanceMethod(id string) (*FieldMethod, bool) {
	fm, ok := d.DeclGeneric.GetInstanceMethod(id)
	if !ok {
		return nil, false
	}
	return fm.Map(d.types).(*FieldMethod), true
}

// AllInstanceFields implements the [Decl] interface. The template's layout is
// the concrete's layout with the bindings substituted in, so the order carries
// over and only the field types change.
//
// The result is cached, so every caller sees the same field copies. Codegen's
// layoutOf writes a per-concrete offset onto each copy and fillMembers reads it
// back; a fresh set on every call would lose those offsets to garbage between
// the write and the read.
func (d *declConcrete) AllInstanceFields() *base.StaticOrderedMap[string, Field] {
	if d.cachedAllInstanceFields != nil {
		return d.cachedAllInstanceFields
	}
	fields := base.NewStaticOrderedMap[string, Field]()
	for _, entry := range d.DeclGeneric.AllInstanceFields().All() {
		if field, ok := entry.Value.(FieldTypeable); ok {
			fields.Set(entry.Key, field.Map(d.types))
		}
	}
	d.cachedAllInstanceFields = fields
	return fields
}

// DeclType implements the [DeclTypeable] interface.
func (d *declConcrete) DeclType() Type {
	return d.DeclGeneric.DeclType().Map(d.types)
}

// mappedInterfaces is the interfaces one instantiation implements. An interface
// is recorded on the template as it was written, so it is this instantiation's
// bindings that say which interface the record names here: a List[E] written
// inside ArrList[E] is what ArrList[int] implements as List[int]. A record
// already closed when it was made maps to itself.
//
// Mapping can name the same interface twice, since a template reached through
// both List[E] and List[int] recorded both, and everything that consumes this
// collects it into a set.
//
// Only a concrete class, abstract or interface has interfaces at all, so this is
// exposed by each of those rather than promoted to every concrete.
func (d *declConcrete) mappedInterfaces() iter.Seq[Type] {
	return func(yield func(Type) bool) {
		for t := range d.DeclGeneric.(DeclInheritable).Interfaces() {
			if !yield(t.Map(d.types)) {
				return
			}
		}
	}
}

// mappedInterfaceCount is how many [declConcrete.mappedInterfaces] yields, which
// is the template's count, mapping being one for one.
func (d *declConcrete) mappedInterfaceCount() int {
	return d.DeclGeneric.(DeclInheritable).InterfaceCount()
}

// hasInterface answers both sets, since either is a way this instantiation comes
// to implement t. Asking the template alone would miss what only this
// instantiation implements; asking this instantiation alone would re-record what
// the template already carries.
func (d *declConcrete) hasInterface(t Type) bool {
	if d.interfaces.Contains(t) {
		return true
	}
	for mapped := range d.mappedInterfaces() {
		if mapped.Equals(t) {
			return true
		}
	}
	return false
}

// putInterface records t on this instantiation and not on the template. The
// template's set is read by every instantiation, so writing an interface there
// that only this one implements would claim it for all of them.
func (d *declConcrete) putInterface(t Type) {
	d.interfaces.Add(t)
}

// allInterfaces is everything this instantiation implements, however it came by
// it. The two sets are disjoint, since hasInterface is asked before anything is
// recorded.
func (d *declConcrete) allInterfaces() iter.Seq[Type] {
	return func(yield func(Type) bool) {
		for t := range d.mappedInterfaces() {
			if !yield(t) {
				return
			}
		}
		for t := range d.interfaces.Values() {
			if !yield(t) {
				return
			}
		}
	}
}

func (d *declConcrete) allInterfaceCount() int {
	return d.mappedInterfaceCount() + d.interfaces.Len()
}

// MapType implements the [DeclConcrete] interface.
func (d *declConcrete) MapType(t Type) Type {
	return t.Map(d.types)
}

// InstanceByteSize returns a pointer to this concrete instantiation's own
// cached fixed instance byte size, shadowing the generic declaration's shared
// size.
func (d *declConcrete) InstanceByteSize() *uint64 {
	return &d.instanceByteSize
}

// InstanceTailByteSize returns a pointer to this concrete instantiation's own
// cached tail element byte size.
func (d *declConcrete) InstanceTailByteSize() *uint64 {
	return &d.instanceTailByteSize
}

// IsSubtypeOf implements the [DeclInheritable] interface.
func (a *DeclConcreteAbstract) IsSubtypeOf(t Type) bool {
	if t.Kind() != TypeKindPointer {
		return false
	}
	tr, ok := t.(*TypeWrapped).Type.(TypeResolvable)
	if !ok {
		return false
	}
	return declSubtypeableIsSubtypeOf(a, tr)
}

func (a *DeclConcreteAbstract) BaseDeclType() *TypeWrapped {
	return a.DeclGeneric.(DeclSubtypeable).BaseDeclType().Map(a.types).(*TypeWrapped)
}

func (a *DeclConcreteAbstract) HasInterface(t Type) bool {
	return a.hasInterface(t)
}

func (a *DeclConcreteAbstract) Interfaces() iter.Seq[Type] {
	return a.allInterfaces()
}

func (a *DeclConcreteAbstract) InterfaceCount() int {
	return a.allInterfaceCount()
}

func (a *DeclConcreteAbstract) IsTailed() bool {
	return a.DeclGeneric.(DeclSubtypeable).IsTailed()
}

func (a *DeclConcreteAbstract) PutInterface(t Type) {
	a.putInterface(t)
}

// IsSubtypeOf implements the [DeclInheritable] interface.
func (c *DeclConcreteClass) IsSubtypeOf(t Type) bool {
	if t.Kind() != TypeKindPointer {
		return false
	}
	tr, ok := t.(*TypeWrapped).Type.(TypeResolvable)
	if !ok {
		return false
	}
	return declSubtypeableIsSubtypeOf(c, tr)
}

func (c *DeclConcreteClass) BaseDeclType() *TypeWrapped {
	return c.DeclGeneric.(DeclSubtypeable).BaseDeclType().Map(c.types).(*TypeWrapped)
}

func (c *DeclConcreteClass) HasInterface(t Type) bool {
	return c.hasInterface(t)
}

func (c *DeclConcreteClass) Interfaces() iter.Seq[Type] {
	return c.allInterfaces()
}

func (c *DeclConcreteClass) InterfaceCount() int {
	return c.allInterfaceCount()
}

func (c *DeclConcreteClass) IsTailed() bool {
	return c.DeclGeneric.(DeclSubtypeable).IsTailed()
}

func (c *DeclConcreteClass) PutInterface(t Type) {
	c.putInterface(t)
}

// IsSubtypeOf implements the [DeclInheritable] interface.
func (i *DeclConcreteInterface) IsSubtypeOf(t Type) bool {
	if t.Kind() != TypeKindPointer {
		return false
	}
	tr, ok := t.(*TypeWrapped).Type.(TypeResolvable)
	if !ok {
		return false
	}
	return declInherits(i, tr)
}

func (i *DeclConcreteInterface) HasInterface(t Type) bool {
	return i.hasInterface(t)
}

func (i *DeclConcreteInterface) PutInterface(t Type) {
	i.putInterface(t)
}

func (i *DeclConcreteInterface) Interfaces() iter.Seq[Type] {
	return i.allInterfaces()
}

func (i *DeclConcreteInterface) InterfaceCount() int {
	return i.allInterfaceCount()
}

// TypeIndex returns a pointer to the concrete struct's own type definition
// index, overriding the generic declaration's index that would otherwise be
// promoted from the embedded declConcrete.
func (d *DeclConcreteStruct) TypeIndex() *int {
	return &d.typeIndex
}

// ConcreteID returns a pointer to the concrete abstract's virtual generic id.
func (a *DeclConcreteAbstract) ConcreteID() *uint16 { return &a.concreteID }

// ConcreteID returns a pointer to the concrete class's virtual generic id.
func (c *DeclConcreteClass) ConcreteID() *uint16 { return &c.concreteID }

// ConcreteID returns a pointer to the concrete interface's virtual generic id.
func (i *DeclConcreteInterface) ConcreteID() *uint16 { return &i.concreteID }
