package syntax

import (
	"fmt"

	base "github.com/zambozoo/zam/internal/0_base"
)

type (
	// Modifier is a modifier on a Field.
	Modifier uint8

	// FieldKind is the kind of field.
	FieldKind uint8

	FieldVisitor interface {
		VisitFieldMember(f *FieldMember) error
		VisitFieldMethod(f *FieldMethod) error
		VisitFieldEnum(f *FieldEnum) error

		VisitDeclAbstract(d *DeclAbstract) error
		VisitDeclClass(d *DeclClass) error
		VisitDeclEnum(d *DeclEnum) error
		VisitDeclInterface(d *DeclInterface) error
		VisitDeclNamespace(d *DeclNamespace) error
		VisitDeclStruct(d *DeclStruct) error
	}

	// Field is a field in the AST.
	Field interface {
		Kind() FieldKind
		Name() string
		Modifiers() Modifier
		OuterDecl() Decl
		// Start returns the token the field begins at, which is where an error
		// about the field is reported from.
		Start() *Token

		// Accept visits the field with the given visitor.
		Accept(visitor FieldVisitor) error
	}

	// FieldIndexable is a field that can be indexed.
	FieldIndexable interface {
		Field
		Index() *int
	}

	// FieldTypeable is a field that has a type.
	FieldTypeable interface {
		FieldIndexable
		FieldType() Type
		Map(types map[GenericArgKey]Type) FieldTypeable
	}

	// field is the base field.
	field struct {
		start     *Token
		outerDecl Decl
		name      string
		index     int
	}

	// FieldMember is a field that can be modified.
	FieldMember struct {
		field
		modifiers Modifier
		fieldType Type
		Expr      Expr
		// offset is where the member lies within an instance, filled in by the
		// codegen pass that lays the declaration out. It lies on the field for
		// the same reason a method's instruction does: members are flattened
		// over the base chain with the base first, so a member a subclass does
		// not declare is the very same field at the very same offset in the
		// subclass's layout as in the base's. A generic instantiation carries
		// its own copy of the field, so one offset per layout is still one
		// offset per field.
		offset uint64
	}

	// FieldMethod is a field that can be called.
	FieldMethod struct {
		field
		modifiers Modifier
		fieldType *TypeFunction
		// Parameters names the slots the caller pushes its arguments into, in
		// order, and is as long as the type's parameter list.
		Parameters []string
		Body       Stmt
		// instruction is where the body was emitted, filled in by codegen once
		// it has been. It lies on the field rather than on any one type's method
		// table because a method a subclass does not override is the very same
		// field in the subclass's table as in the base's, so recording it here
		// records it for every table the method appears in at once.
		instruction uint64

		// generics is the parameter map a generic method was written with, or
		// nil for a method with no `[T,...]` list of its own. A method's T is
		// a separate scope from the enclosing class's T: there is no receiver
		// a static method could resolve a class-level T against, and an
		// instance method's class-level T is already read off the receiver.
		generics *base.StaticOrderedMap[GenericArgKey, GenericBound]
		// genericTypes is the set of open types referenced inside a generic
		// method's signature and body. A concrete instantiation walks it
		// under its bindings to recover the ground types the body needs.
		genericTypes *TypeSet
		// concretes is the list of ground instantiations materialized for
		// this method, each one its own compiled body and its own static
		// slot. The order is insertion order so a later walk is stable.
		concretes []*MethodConcrete
	}

	// MethodConcrete is one monomorphization of a generic static method: the
	// type arguments it was called with, the signature those arguments
	// resolve to, and the slot and body offset codegen will fill in for it.
	MethodConcrete struct {
		// method is the generic method this is an instantiation of.
		method *FieldMethod
		// typeArgs is the list of ground types passed for the method's
		// generic parameters, in parameter order.
		typeArgs []Type
		// bindings maps each of the method's generic parameters to the
		// ground type the instantiation bound it to. Compiling the body
		// walks every signature position and local type through this map.
		bindings map[GenericArgKey]Type
		// fieldType is the method's signature with the bindings applied,
		// which is the type the call site types as.
		fieldType *TypeFunction
		// index is the static slot the instantiation's closure is written
		// into; it is assigned once static layout has run.
		index int
		// instruction is where the instantiation's body was emitted, filled
		// in by codegen once the body has been.
		instruction uint64
	}

	// FieldEnum is an enum value.
	FieldEnum struct {
		field
		Init       *string
		Parameters []Expr
		// Value is what the value is backed by in an enum declared with a
		// primitive, and nil otherwise.
		Value Expr
		// valueIndex is the static slot Value is held in, assigned once
		// static layout has run.
		valueIndex int
	}
)

// newModifier creates a new modifier. Each one takes a bit of its own, because
// a field carries the whole set it was written with and [Modifier.Has] reads
// that set one bit at a time.
func newModifier(kind TokenKind, s string) Modifier {
	modifier := Modifier(1) << len(modifierTokens)
	modifierTokens = append(modifierTokens, kind)
	modifiersMap[kind] = modifier
	modifiersStrings = append(modifiersStrings, s)
	return modifier
}

var (
	// modifierTokens is the tokens of the modifiers.
	modifierTokens []TokenKind
	// modifiersMap is the map of the modifiers.
	modifiersMap = map[TokenKind]Modifier{}
	// modifiersStrings is the strings of the modifiers.
	modifiersStrings = []string{}

	// Public modifier for public visibility
	ModifierPublic = newModifier(TTPublic, "public")
	// Private modifier for private visibility
	ModifierPrivate = newModifier(TTPrivate, "private")
	// Protected modifier for protected visibility
	ModifierProtected = newModifier(TTProtected, "protected")
	// Static modifier for static scoping
	ModifierStatic = newModifier(TTStatic, "static")
	// Virtual modifier for virtual inheritance
	ModifierVirtual = newModifier(TTVirtual, "virtual")

	// modifiersCollisionMap is the map of modifier collisions.
	modifiersCollisionMap = map[Modifier][]Modifier{
		ModifierPublic:    {ModifierPrivate, ModifierProtected},
		ModifierPrivate:   {ModifierPublic, ModifierProtected},
		ModifierProtected: {ModifierPublic, ModifierPrivate, ModifierStatic},
		ModifierStatic:    {ModifierVirtual, ModifierProtected},
		ModifierVirtual:   {ModifierStatic},
	}
)

// newModifiers creates a new modifier set.
func newModifiers(parser SyntaxParser) (Modifier, error) {
	var modifiers Modifier

	for {
		tok, err := parser.Match(modifierTokens...)
		if err != nil {
			return 0, err
		} else if tok == nil {
			break
		}

		modifier := modifiersMap[tok.Kind]
		if modifiers.Has(modifier) {
			return 0, fmt.Errorf("duplicate modifier %q", modifier)
		}
		modifiers |= modifier
	}

	if err := checkModifierCollisions(modifiers); err != nil {
		return 0, err
	}
	return modifiers, nil
}

// checkModifierCollisions reports the first pair of modifiers in the set that
// the collision map forbids together. It is split out so that the namespace
// loop can validate a modifier set after injecting an implicit static — a
// namespace member a user wrote as `virtual` would collide once the implied
// `static` is added, and the user should see the same error as if they had
// written both.
func checkModifierCollisions(modifiers Modifier) error {
	for modifier, collisions := range modifiersCollisionMap {
		if !modifiers.Has(modifier) {
			continue
		}
		for _, collision := range collisions {
			if modifiers.Has(collision) {
				return fmt.Errorf("modifier %q cannot be used with %q", modifier, collision)
			}
		}
	}
	return nil
}

// Has checks if the modifier set has the modifier.
func (m Modifier) Has(modifier Modifier) bool {
	return m&modifier != 0
}

// newFieldKind creates a new field kind.
func newFieldKind(s string) FieldKind {
	fieldKindStrings = append(fieldKindStrings, s)
	return FieldKind(len(fieldKindStrings) - 1)
}

var (
	// fieldKindStrings is the strings of the field kinds.
	fieldKindStrings []string
	// fieldTokenKinds is the token kinds of the field.
	fieldTokenKinds = []TokenKind{
		TTVar,
		TTFun,
		TTAbstract,
		TTClass,
		TTEnum,
		TTInterface,
		TTNamespace,
		TTStruct,
	}

	// FieldKindMember is a field for modifiable values
	FieldKindMember = newFieldKind("member")
	// FieldKindMethod is a field for executable methods
	FieldKindMethod = newFieldKind("method")
	// FieldKindDecl is a field for nested declarations
	FieldKindDecl = newFieldKind("declaration")
	// FieldKindEnum is a field for singleton values
	FieldKindEnum = newFieldKind("enum")
)

// String implements the [fmt.Stringer] interface.
func (k FieldKind) String() string {
	return fieldKindStrings[k]
}

// Start returns the start token of the field.
func (f *field) Start() *Token {
	return f.start
}

// OuterDecl returns the outer declaration of the field.
func (f *field) OuterDecl() Decl {
	return f.outerDecl
}

// Name returns the name of the field.
func (f *field) Name() string {
	return f.name
}

// Index returns the index of the field.
func (f *field) Index() *int {
	return &f.index
}

// newField creates a new field. defaultStatic says the enclosing declaration
// holds no instance and so every field it holds is implicitly static — a
// namespace is the one such declaration. A user who writes `static` on a field
// in one is writing what the compiler would have added, so the modifier is
// accepted rather than rejected as redundant.
func newField(parser SyntaxParser, canBeTailed, defaultStatic bool) (Field, error) {
	modifiers, err := newModifiers(parser)
	if err != nil {
		return nil, err
	}

	if defaultStatic && !modifiers.Has(ModifierStatic) {
		modifiers |= ModifierStatic
		if err := checkModifierCollisions(modifiers); err != nil {
			return nil, err
		}
	}

	tok, err := parser.ConsumeAny(fieldTokenKinds...)
	if err != nil {
		return nil, err
	}

	var field Field
	switch tok.Kind {
	case TTVar:
		field, err = newFieldMember(parser, tok, modifiers, canBeTailed && !modifiers.Has(ModifierStatic))
	case TTFun:
		field, err = newFieldMethod(parser, tok, modifiers)
	}

	if err != nil {
		return nil, err
	} else if field != nil {
		return field, nil
	}

	// A declaration field is static whether or not the user wrote so, since the
	// enclosing declaration never has an instance for it to hang off. Writing
	// the modifier is redundant in a class or struct — nothing else makes
	// sense there — but accepted in a namespace, where every field is already
	// defaulted to static and the user is restating the rule rather than
	// adding a new one.
	if modifiers.Has(ModifierStatic) && !defaultStatic {
		return nil, fmt.Errorf("redundant static modifier for declaration field")
	}
	modifiers |= ModifierStatic

	switch tok.Kind {
	case TTAbstract:
		field, err = newDeclAbstract(parser, tok, modifiers)
	case TTClass:
		field, err = newDeclClass(parser, tok, modifiers)
	case TTEnum:
		field, err = newDeclEnum(parser, tok, modifiers)
	case TTInterface:
		field, err = newDeclInterface(parser, tok, modifiers)
	case TTNamespace:
		field, err = newDeclNamespace(parser, tok, modifiers)
	case TTStruct:
		field, err = newDeclStruct(parser, tok, modifiers)
	}

	if err != nil {
		return nil, err
	}

	return field, nil
}

// newFieldMember creates a new field member.
func newFieldMember(parser SyntaxParser, start *Token, modifiers Modifier, canBeTailed bool) (*FieldMember, error) {
	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("virtual variable is not allowed")
	}

	var _type Type
	var err error
	if canBeTailed {
		_type, err = newTailableType(parser)
	} else {
		_type, err = newType(parser)
	}
	if err != nil {
		return nil, err
	}

	name, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}

	if tok, err := parser.Match(TTEqual); err != nil {
		return nil, err
	} else if tok == nil {
		if _, err := parser.Consume(TTSemiColon); err != nil {
			return nil, err
		}

		return &FieldMember{
			field: field{
				start:     start,
				outerDecl: *parser.Decl(),
				name:      name.Value.(string),
			},
			modifiers: modifiers,
			fieldType: _type,
		}, nil
	}

	expr, err := newExpr(parser)
	if err != nil {
		return nil, err
	}

	if _, err := parser.Consume(TTSemiColon); err != nil {
		return nil, err
	}

	return &FieldMember{
		field: field{
			start:     start,
			outerDecl: *parser.Decl(),
			name:      name.Value.(string),
		},
		fieldType: _type,
		modifiers: modifiers,
		Expr:      expr,
	}, nil
}

// Kind implements the [Field] interface.
func (f *FieldMember) Kind() FieldKind {
	return FieldKindMember
}

// Modifiers implements the [Field] interface.
func (f *FieldMember) Modifiers() Modifier {
	return f.modifiers
}

// FieldType implements the [FieldTypeable] interface.
func (f *FieldMember) FieldType() Type {
	return f.fieldType
}

// Offset is where the member lies within an instance, once codegen has laid the
// declaration out. The first member of any layout lies at zero, so a zero here
// says nothing on its own about whether the layout has been taken.
func (f *FieldMember) Offset() *uint64 {
	return &f.offset
}

// Accept implements the [Field] interface.
func (f *FieldMember) Accept(visitor FieldVisitor) error {
	return visitor.VisitFieldMember(f)
}

// Map implements the [FieldTypeable] interface.
func (f *FieldMember) Map(types map[GenericArgKey]Type) FieldTypeable {
	field := *f
	field.fieldType = field.fieldType.Map(types)
	return &field
}

// parseParameterNames reads the comma-separated parameter list between the
// current parenthesis and the next right paren, which both method and lambda
// bodies open the same way. A parameter written as `_` arrives as a
// [TTUnderscore] and is kept as [BlankName]; anything else is a plain
// identifier whose value is the written name.
func parseParameterNames(parser SyntaxParser) ([]string, error) {
	if tok, err := parser.Match(TTRightParen); err != nil {
		return nil, err
	} else if tok != nil {
		return nil, nil
	}

	var parameters []string
	for {
		tok, err := parser.ConsumeAny(TTIdentifier, TTUnderscore)
		if err != nil {
			return nil, err
		}
		name := BlankName
		if tok.Kind == TTIdentifier {
			name = tok.Value.(string)
		}
		parameters = append(parameters, name)

		if tok, err := parser.ConsumeAny(TTComma, TTRightParen); err != nil {
			return nil, err
		} else if tok.Kind == TTRightParen {
			return parameters, nil
		}
	}
}

// newFieldMethod creates a new field method. The method is published on the
// parser before the signature is read so a generic argument named inside the
// signature can resolve to the method's own parameter list rather than only
// the enclosing decl's.
func newFieldMethod(parser SyntaxParser, start *Token, modifiers Modifier) (*FieldMethod, error) {
	method := &FieldMethod{
		field: field{
			start:     start,
			outerDecl: *parser.Decl(),
		},
		modifiers: modifiers,
	}

	methodSlot := parser.Method()
	outerMethod := *methodSlot
	*methodSlot = method
	defer func() { *methodSlot = outerMethod }()

	// A generic method opens its own parameter scope. The parameter list is
	// read before the return type so the return type can name one; a method
	// with no `[T,...]` leaves generics nil and nothing further depends on
	// it. The method must not be virtual: a virtual method has no body and
	// so no site at which to resolve the T a signature would reference.
	if tok, err := parser.Match(TTLeftBracket); err != nil {
		return nil, err
	} else if tok != nil {
		if modifiers.Has(ModifierVirtual) {
			return nil, fmt.Errorf("virtual methods cannot be generic")
		}
		if !modifiers.Has(ModifierStatic) {
			return nil, fmt.Errorf("instance methods cannot be generic")
		}
		generics, err := newGenerics(parser, method)
		if err != nil {
			return nil, err
		}
		method.generics = generics
		method.genericTypes = NewTypeSet()
	}

	_type, err := newType(parser)
	if err != nil {
		return nil, err
	}

	typeFunction, ok := _type.(*TypeFunction)
	if !ok {
		return nil, fmt.Errorf("method type is not a function")
	}
	method.fieldType = typeFunction

	name, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	method.name = name.Value.(string)

	if tok, err := parser.Match(TTEqual); err != nil {
		return nil, err
	} else if tok == nil {
		if _, err = parser.Consume(TTSemiColon); err != nil {
			return nil, err
		}

		if !modifiers.Has(ModifierVirtual) {
			return nil, fmt.Errorf("non-virtual methods must have a body")
		}

		return method, nil
	}

	if modifiers.Has(ModifierVirtual) {
		return nil, fmt.Errorf("virtual methods cannot have a body")
	}

	if _, err = parser.Consume(TTLeftParen); err != nil {
		return nil, err
	}

	parameters, err := parseParameterNames(parser)
	if err != nil {
		return nil, err
	}

	if len(parameters) != len(typeFunction.ParameterTypes) {
		return nil, fmt.Errorf("method parameter length mismatch, expected %d, got %d", len(typeFunction.ParameterTypes), len(parameters))
	}
	method.Parameters = parameters

	body, err := newStmt(parser)
	if err != nil {
		return nil, err
	}
	method.Body = body

	return method, nil
}

// Kind implements the [Field] interface.
func (f *FieldMethod) Kind() FieldKind {
	return FieldKindMethod
}

// Modifiers implements the [Field] interface.
func (f *FieldMethod) Modifiers() Modifier {
	return f.modifiers
}

// FieldType implements the [FieldTypeable] interface.
func (f *FieldMethod) FieldType() Type {
	return f.fieldType
}

// Instruction is where the method's body begins, once codegen has emitted it. A
// method with no body never has one recorded and so keeps offset zero, which is
// where the program begins and so names no body.
func (f *FieldMethod) Instruction() *uint64 {
	return &f.instruction
}

// Accept implements the [Field] interface.
func (f *FieldMethod) Accept(visitor FieldVisitor) error {
	return visitor.VisitFieldMethod(f)
}

// Map implements the [FieldTypeable] interface.
func (f *FieldMethod) Map(types map[GenericArgKey]Type) FieldTypeable {
	field := *f
	field.fieldType = field.fieldType.Map(types).(*TypeFunction)
	return &field
}

// Generics implements the [GenericOwner] interface. A non-generic method
// answers with nil, matching how a non-generic declaration does.
func (f *FieldMethod) Generics() *base.StaticOrderedMap[GenericArgKey, GenericBound] {
	return f.generics
}

// GenericTypes implements the [GenericOwner] interface. A non-generic method
// answers with nil, since there are no open types to collect against it.
func (f *FieldMethod) GenericTypes() *TypeSet {
	return f.genericTypes
}

// Hash implements the [GenericOwner] interface. The outer decl's hash and
// the method name together name the method uniquely, which matches how
// [decl.Hash] identifies nested declarations.
func (f *FieldMethod) Hash() uint64 {
	return base.Hash(f.outerDecl.Hash(), base.FNV(f.name))
}

// Concretes returns the ground instantiations materialized for this generic
// method, in insertion order. A caller is a monomorphization pass that must
// walk each instantiation in a stable order.
func (f *FieldMethod) Concretes() []*MethodConcrete {
	return f.concretes
}

// GetConcreteMethod finds or creates the ground instantiation of this method
// for the given type arguments. The second return value is true when the
// instantiation was newly created, so the caller can enqueue it for walking.
// A non-generic method answers nil, false.
func (f *FieldMethod) GetConcreteMethod(typeArgs []Type) (*MethodConcrete, bool) {
	if f.generics == nil {
		return nil, false
	}
	for _, existing := range f.concretes {
		if typesEqual(existing.typeArgs, typeArgs) {
			return existing, false
		}
	}
	bindings := make(map[GenericArgKey]Type, f.generics.Len())
	for i, entry := range f.generics.All() {
		bindings[entry.Key] = typeArgs[i]
	}
	concrete := &MethodConcrete{
		method:    f,
		typeArgs:  typeArgs,
		bindings:  bindings,
		fieldType: f.fieldType.Map(bindings).(*TypeFunction),
	}
	f.concretes = append(f.concretes, concrete)
	return concrete, true
}

// typesEqual reports whether two type lists are element-wise [Type.Equals].
func typesEqual(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equals(b[i]) {
			return false
		}
	}
	return true
}

// Method returns the generic method this instantiation descends from.
func (c *MethodConcrete) Method() *FieldMethod { return c.method }

// TypeArgs returns the ground types the instantiation bound the method's
// generic parameters to, in parameter order.
func (c *MethodConcrete) TypeArgs() []Type { return c.typeArgs }

// Bindings returns the substitution the instantiation walks the method's
// signature and body under. A caller must not mutate the returned map.
func (c *MethodConcrete) Bindings() map[GenericArgKey]Type { return c.bindings }

// FieldType returns the instantiation's signature, the method's own with the
// bindings substituted in. The call site types as this signature.
func (c *MethodConcrete) FieldType() *TypeFunction { return c.fieldType }

// Index returns a pointer to the static slot the instantiation's closure is
// written into, assigned once static layout has run.
func (c *MethodConcrete) Index() *int { return &c.index }

// Instruction returns a pointer to the body offset the instantiation was
// emitted at, assigned once codegen has emitted the body.
func (c *MethodConcrete) Instruction() *uint64 { return &c.instruction }

// newFieldEnum creates a new field enum. A value of an enum backed by a
// primitive is written with the constant it stands for, NAME(expr), and names no
// initializer; a value of any other enum is written bare or as NAME.init(...).
func newFieldEnum(parser SyntaxParser, enum *DeclEnum) (*FieldEnum, error) {
	start, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	name := start.Value.(string)

	if enum.primitive != nil {
		if _, err := parser.Consume(TTLeftParen); err != nil {
			return nil, fmt.Errorf("enum value %s.%s must be written with a %s value: %w", enum.name, name, enum.primitive.String(), err)
		}
		value, err := newExpr(parser)
		if err != nil {
			return nil, err
		}
		if _, err := parser.Consume(TTRightParen); err != nil {
			return nil, err
		}
		return &FieldEnum{
			field: field{
				start:     start,
				outerDecl: *parser.Decl(),
				name:      name,
			},
			Value: value,
		}, nil
	}

	if tok, err := parser.Match(TTLeftParen); err != nil {
		return nil, err
	} else if tok != nil {
		return nil, fmt.Errorf("enum value %s.%s cannot be written with a value: %s is not backed by a primitive", enum.name, name, enum.name)
	}

	if tok, err := parser.Match(TTPeriod); err != nil {
		return nil, err
	} else if tok == nil {
		return &FieldEnum{
			field: field{
				start:     start,
				outerDecl: *parser.Decl(),
				name:      name,
			},
		}, nil
	}

	tok, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	init := new(string)
	*init = tok.Value.(string)

	if _, err := parser.Consume(TTLeftParen); err != nil {
		return nil, err
	}

	var parameters []Expr
	if tok, err := parser.Match(TTRightParen); err != nil {
		return nil, err
	} else if tok == nil {
		for {
			expr, err := newExpr(parser)
			if err != nil {
				return nil, err
			}
			parameters = append(parameters, expr)

			if tok, err := parser.ConsumeAny(TTComma, TTRightParen); err != nil {
				return nil, err
			} else if tok.Kind == TTRightParen {
				break
			}
		}
	}

	return &FieldEnum{
		field: field{
			start:     start,
			outerDecl: *parser.Decl(),
			name:      start.Value.(string),
		},
		Init:       init,
		Parameters: parameters,
	}, nil
}

// Kind implements the [Field] interface.
func (f *FieldEnum) Kind() FieldKind {
	return FieldKindEnum
}

// Type is the type of an enum value, which is the enum it is a value of.
//
// It is the enclosing declaration's own type rather than a reference built here,
// because a reference built here would name the enum without resolving to it:
// the resolve pass has already run by the time anything asks, so the decl has to
// be the one the enum already carries or the type answers nothing when it is
// asked what it is.
func (f *FieldEnum) Type() Type {
	return f.outerDecl.(DeclTypeable).DeclType()
}

// Modifiers implements the [Field] interface.
func (f *FieldEnum) Modifiers() Modifier {
	return ModifierStatic
}

// Accept implements the [Field] interface.
func (f *FieldEnum) Accept(visitor FieldVisitor) error {
	return visitor.VisitFieldEnum(f)
}

// ValueIndex returns a pointer to the static slot Value is held in, assigned
// once static layout has run.
func (f *FieldEnum) ValueIndex() *int {
	return &f.valueIndex
}
