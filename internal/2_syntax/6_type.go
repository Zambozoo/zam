package syntax

import (
	"fmt"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
)

type (
	// TypeKind is the kind of type.
	TypeKind uint8

	TypeVisitor interface {
		VisitTypeArray(t *TypeArray) error
		VisitTypeFunction(t *TypeFunction) error
		VisitTypeGeneric(t *TypeGeneric) error
		VisitTypeGenericArg(t *TypeGenericArg) error
		VisitTypePrimitive(t *TypePrimitive) error
		VisitTypeReference(t *TypeReference) error
		VisitTypeTailed(t *TypeTailed) error
		VisitTypeWrapped(t *TypeWrapped) error
	}

	TypeResolvable interface {
		Type
		// Decl returns the declaration of the type.
		Decl() DeclTypeable
	}

	TypeSupertypeable interface {
		Type
		// IsSupertypeOf returns true if the type is a supertype of the other type.
		IsSupertypeOf(other Type) bool
	}

	TypeTailable interface {
		TypeResolvable
		// IsTailable returns true if the type is tailable.
		IsTailable() bool
	}

	// Type is a type in the AST.
	Type interface {
		fmt.Stringer
		// Start returns the token that started the type.
		Start() *Token
		// Kind returns the kind of the type.
		Kind() TypeKind
		// OuterDecl returns the containing declaration of the type.
		OuterDecl() Decl

		// Hash returns the hash of the type.
		Hash() uint64
		// Equals returns true if the type is equal to the other type.
		Equals(other Type) bool
		// IsSubtypeOf returns true if the type is a subtype of the other type.
		IsSubtypeOf(other Type) bool

		// Accept accepts the visitor.
		Accept(visitor TypeVisitor) error
		// Map maps the type to the given types.
		Map(types map[GenericArgKey]Type) Type
		// ContainsGenericArg reports whether the type references any open generic
		// argument.
		ContainsGenericArg() bool
	}

	// TypeArray is a type that is an array.
	TypeArray struct {
		// outerDecl is the containing declaration of the type.
		outerDecl Decl
		// Type is the type of the array.
		Type Type
		// Size is the size of the array.
		Size *uint64
	}

	// TypeFunction is a type that is a function.
	TypeFunction struct {
		// outerDecl is the containing declaration of the type.
		outerDecl Decl
		// ParameterTypes is the types of the parameters of the function.
		ParameterTypes []Type
		// ReturnType is the type of the return value of the function.
		ReturnType Type
	}

	// TypeGeneric is a type that is a generic.
	TypeGeneric struct {
		// outerDecl is the containing declaration of the type.
		outerDecl Decl
		// Type is the type of the generic.
		Type TypeResolvable
		// ParameterTypes is the types of the parameters of the generic.
		ParameterTypes []Type
	}

	// TypeGenericArg is a type that is a generic argument.
	TypeGenericArg struct {
		// start is the token that started the type.
		start *Token
		// owner is what declared the generic argument: a generic declaration
		// for a class-level parameter, a generic method for a method-level one.
		owner GenericOwner
		// Name is the name of the generic argument.
		Name string
	}

	// TypePrimitive is a type that is a primitive type.
	TypePrimitive struct {
		// start is the token that started the type.
		start *Token
		// outerDecl is the containing declaration of the type.
		outerDecl Decl
		// kind is the kind of the primitive type.
		kind TypeKind
	}

	// TypeReference is a type that is a reference to a declaration.
	TypeReference struct {
		// start is the token that started the type.
		start *Token
		// outerDecl is the containing declaration of the type.
		outerDecl Decl
		// Ids is the dotted name as it was written. What the head of it stands
		// for — an enclosing declaration, a nested one, or an import — is
		// settled by resolution rather than by the parse.
		Ids []string
		// IdTokens are the tokens Ids are written at, and are empty for a
		// reference no source was written for.
		IdTokens []*Token

		// decl is the declaration of the tailed type.
		decl DeclTypeable
	}

	// TypeTailed is a type that is a tailed type.
	TypeTailed struct {
		// start is the token that started the type.
		start *Token
		// outerDecl is the containing declaration of the type.
		outerDecl Decl
		// Type is the type of the tailed type.
		Type *TypeReference
		// Size is the size of the tailed type.
		Size *uint64
	}

	// TypeWrapped is a type that is a wrapped type.
	TypeWrapped struct {
		// start is the token that started the type.
		start *Token
		// kind is the kind of the wrapped type.
		kind TypeKind
		// outerDecl is the containing declaration of the type.
		outerDecl Decl
		// Type is the type of the wrapped type.
		Type Type
	}
)

// newTypeKind creates a new type kind.
func newTypeKind(string string) TypeKind {
	typeKindStrings = append(typeKindStrings, string)
	return TypeKind(len(typeKindStrings) - 1)
}

// String implements the [fmt.Stringer] interface.
func (k TypeKind) String() string {
	return typeKindStrings[k]
}

var (
	// typeKindStrings is the list of type kind strings.
	typeKindStrings []string

	// Void type for no value
	TypeKindVoid = newTypeKind("void")
	// Boolean type for true or false values
	TypeKindBool = newTypeKind("bool")
	// Character type for a single character
	TypeKindChar = newTypeKind("char")
	// Byte type for an unsigned eight-bit value
	TypeKindByte = newTypeKind("byte")
	// Integer type for whole numbers
	TypeKindInt = newTypeKind("int")
	// Float type for floating point numbers
	TypeKindFloat = newTypeKind("float")
	// String type for a sequence of characters
	TypeKindString = newTypeKind("str")
	// Type type for a type
	TypeKindType = newTypeKind("type")
	// Any type for a boxed value of any other type. A kind of its own rather
	// than a wrapper so a slot typed any has a fixed width — one pointer at
	// the slot, with the payload and its type id living in the any object on
	// the heap. Added at the end of the primitive run so none of the earlier
	// primitives shift under it; the runtime's primitive table counts up from
	// TypeKindVoid and would resize past every value if any were inserted
	// among them.
	TypeKindAny = newTypeKind("any")
	// Unsigned integer type for whole non-negative numbers. Appended after
	// TypeKindAny so the earlier primitives do not shift under it — the C
	// runtime's ZamPrimitive table counts up from TypeKindVoid and a kind
	// inserted among them would renumber every one of them past it.
	TypeKindUint = newTypeKind("uint")
	// Array type for a sequence of values
	TypeKindArray = newTypeKind("array")
	// Pointer type for a reference to a value
	TypeKindPointer = newTypeKind("pointer")
	// Function type for a callable value
	TypeKindFunction = newTypeKind("function")
	// Variadic type for a variable number of arguments
	TypeKindVariadic = newTypeKind("variadic")
	// Thread type for a thread of execution
	TypeKindThread = newTypeKind("thread")
	// Generic argument type for a generic type
	TypeKindGenericArg = newTypeKind("generic_arg")
	// Reference type for a reference to a value
	TypeKindReference = newTypeKind("reference")
	// Generic type for a generic type
	TypeKindGeneric = newTypeKind("generic")
	// Tailed type for a type with a variable tail member
	TypeKindTailed = newTypeKind("tailed")
	// Const type for a value whose slot cannot be reassigned
	TypeKindConst = newTypeKind("const")

	// primitiveTokenKindMap is the map of token kinds to primitive type kinds.
	primitiveTokenKindMap = map[TokenKind]TypeKind{
		TTVoid:   TypeKindVoid,
		TTAny:    TypeKindAny,
		TTBool:   TypeKindBool,
		TTChar:   TypeKindChar,
		TTByte:   TypeKindByte,
		TTInt:    TypeKindInt,
		TTUint:   TypeKindUint,
		TTFloat:  TypeKindFloat,
		TTString: TypeKindString,
		TTType:   TypeKindType,
	}

	// baseTypeTokenKinds is the list of token kinds that can start a base type.
	baseTypeTokenKinds = []TokenKind{
		TTVoid,
		TTAny,
		TTBool,
		TTChar,
		TTByte,
		TTInt,
		TTUint,
		TTFloat,
		TTString,
		TTType,
		TTThread,
		TTIdentifier,
	}

	TypeVoid = &TypePrimitive{
		kind: TypeKindVoid,
	}
	TypeAny = &TypePrimitive{
		kind: TypeKindAny,
	}
	TypeBool = &TypePrimitive{
		kind: TypeKindBool,
	}
	TypeChar = &TypePrimitive{
		kind: TypeKindChar,
	}
	TypeByte = &TypePrimitive{
		kind: TypeKindByte,
	}
	TypeInt = &TypePrimitive{
		kind: TypeKindInt,
	}
	TypeUint = &TypePrimitive{
		kind: TypeKindUint,
	}
	TypeFloat = &TypePrimitive{
		kind: TypeKindFloat,
	}
	TypeString = &TypePrimitive{
		kind: TypeKindString,
	}
	TypeType = &TypePrimitive{
		kind: TypeKindType,
	}
	TypeNull = &TypeWrapped{
		kind: TypeKindPointer,
		Type: TypeVoid,
	}
)

// NewWrappedType returns a pointer type wrapping the given element type.
func NewWrappedType(kind TypeKind, elem Type) *TypeWrapped {
	return &TypeWrapped{
		kind:      kind,
		outerDecl: elem.OuterDecl(),
		Type:      elem,
	}
}

// IsConst reports whether the outermost wrapper of t is const. Nested const —
// a const inside a pointer, for instance — is a const value reached through a
// non-const slot and is not what the question is about.
func IsConst(t Type) bool {
	w, ok := t.(*TypeWrapped)
	return ok && w.kind == TypeKindConst
}

// StripConst returns t with its outermost const wrapper removed. It is the
// right question for everywhere a const value is being used rather than
// mutated: arithmetic over it, a cast through it, a copy out of it.
func StripConst(t Type) Type {
	if w, ok := t.(*TypeWrapped); ok && w.kind == TypeKindConst {
		return w.Type
	}
	return t
}

// VariadicElement returns what one argument of a variadic parameter is, and
// whether t is a variadic parameter at all.
func VariadicElement(t Type) (Type, bool) {
	w, ok := t.(*TypeWrapped)
	if !ok || w.Kind() != TypeKindVariadic {
		return nil, false
	}
	return w.Type, true
}

// SelectionTarget is the type a field written after a dot is selected out of.
// That is the type itself where it names a declaration, and what a pointer
// addresses where it points at one: an instance is reached through a pointer as
// readily as it is named outright, and every subtype relation is written between
// pointers, so a selection that did not see through one could not reach a method
// on anything polymorphic.
//
// Exactly one level is seen through. A pointer to a pointer addresses a pointer,
// which declares nothing and has no field to select.
//
// Const is a compile-time promise about a slot and is peeled on either side of
// the pointer. A const pointer still addresses an instance, and a pointer to a
// const value still reaches a declaration — neither changes what field a
// selection resolves to.
func SelectionTarget(t Type) (TypeResolvable, bool) {
	t = StripConst(t)
	if w, ok := t.(*TypeWrapped); ok && w.Kind() == TypeKindPointer {
		t = StripConst(w.Type)
	}
	resolvable, ok := t.(TypeResolvable)
	return resolvable, ok
}

// ReachedThroughAddress reports whether a value of the type is the address of an
// instance rather than the instance itself. A pointer is written as one and an
// abstract or interface is one whether it is written as one or not: neither is
// ever laid out anywhere, so all a value of one can be is a reference to the
// implementor that is.
//
// This is what says a field selected out of such a value has a home. A class,
// struct or enum is the instance, so a value of one has a home only where the
// expression that produced it gave it one, and answering that takes the
// expression rather than the type.
func ReachedThroughAddress(t Type) bool {
	t = StripConst(t)
	if w, ok := t.(*TypeWrapped); ok && w.Kind() == TypeKindPointer {
		return true
	}
	resolvable, ok := t.(TypeResolvable)
	if !ok {
		return false
	}
	switch resolvable.Decl().Kind() {
	case KindAbstract, KindInterface:
		return true
	}
	return false
}

// ParameterBindingType is the type a parameter has inside the body bound to it,
// which is the type it was declared with everywhere but the variadic case.
//
// A variadic is written as the argument it takes but arrives as the arguments it
// took: the call site packs them into one allocation and passes a reference to
// it, so what the body is handed is the first of them and not one of them. That
// is what a pointer is, and saying so is what lets the body index and step
// through the arguments with the rules pointers already have.
//
// How many arrived is not passed and so cannot be asked.
func ParameterBindingType(t Type) Type {
	if element, ok := VariadicElement(t); ok {
		return NewWrappedType(TypeKindPointer, element)
	}
	return t
}

// newType creates a new non-tailable type.
func newType(parser SyntaxParser) (Type, error) {
	return newTypeHelper(parser, false)
}

// newTailableType creates a new tailable type.
func newTailableType(parser SyntaxParser) (Type, error) {
	return newTypeHelper(parser, true)
}

// newTypeHelper creates a new type helper.
func newTypeHelper(parser SyntaxParser, canBeTailed bool) (Type, error) {
	t, err := newTypeBase(parser, canBeTailed)
	if err != nil {
		return nil, err
	} else if ta, ok := t.(*TypeArray); ok && ta.Size == nil {
		return ta, nil
	}

	for {
		tok, err := parser.Match(TTStar, TTLeftBracket, TTLeftParen, TTDollar)
		if err != nil {
			return nil, err
		} else if tok == nil {
			if tt, ok := t.(*TypeTailed); ok && tt.Size == nil {
				if !canBeTailed {
					return nil, fmt.Errorf("unsized tailed type %q is not allowed", t.String())
				}
				return t, nil
			}
			return t, nil
		}

		switch tok.Kind {
		case TTStar:
			t = &TypeWrapped{
				kind:      TypeKindPointer,
				outerDecl: *parser.Decl(),
				Type:      t,
			}
		case TTDollar:
			if IsConst(t) {
				return nil, fmt.Errorf("type %q is already const", t.String())
			}
			t = &TypeWrapped{
				kind:      TypeKindConst,
				outerDecl: *parser.Decl(),
				Type:      t,
			}
		case TTLeftBracket:
			if tt, ok := t.(*TypeTailed); ok && tt.Size == nil {
				return nil, fmt.Errorf("unsized tailed type %q must be a pointer type", t.String())
			}
			t, err = newTypeArray(parser, t, canBeTailed)
			if t != nil && t.(*TypeArray).Size == nil {
				return t, nil
			}
		case TTLeftParen:
			if tt, ok := t.(*TypeTailed); ok && tt.Size == nil {
				return nil, fmt.Errorf("unsized tailed type %q must be a pointer type", t.String())
			}
			t, err = newTypeFunction(parser, t)
		}

		if err != nil {
			return nil, err
		}
	}
}

// resolveGenericArg searches the method and declaration currently being
// parsed for a generic parameter named id, innermost owner first: a method
// generic shadows a class generic of the same name because the method's
// parameter was written later and inside the class's scope.
func resolveGenericArg(parser SyntaxParser, id string) (GenericOwner, bool) {
	if methodPtr := parser.Method(); methodPtr != nil && *methodPtr != nil {
		method := *methodPtr
		if method.Generics() != nil {
			key := GenericArgKey{Owner: method, Name: id}
			if _, i := method.Generics().Get(key); i >= 0 {
				return method, true
			}
		}
	}
	if genericDecl, ok := (*parser.Decl()).(DeclGeneric); ok && genericDecl.Generics() != nil {
		key := GenericArgKey{Owner: genericDecl, Name: id}
		if _, i := genericDecl.Generics().Get(key); i >= 0 {
			return genericDecl, true
		}
	}
	return nil, false
}

// ownerContainingDecl is the declaration a generic owner lives inside. The
// decl itself is its own container; a method's is the decl it was declared
// on, which is what scoping and accessibility read against.
func ownerContainingDecl(o GenericOwner) Decl {
	switch o := o.(type) {
	case Decl:
		return o
	case *FieldMethod:
		return o.OuterDecl()
	}
	panic("unknown GenericOwner kind")
}

// newTypeBase creates a new base type.
func newTypeBase(parser SyntaxParser, canBeTailed bool) (Type, error) {
	start, err := parser.ConsumeAny(baseTypeTokenKinds...)
	if err != nil {
		return nil, err
	} else if start.Kind == TTThread {
		return newTypeThread(parser, start)

	} else if start.Kind != TTIdentifier {
		return &TypePrimitive{
			start:     start,
			outerDecl: *parser.Decl(),
			kind:      primitiveTokenKindMap[start.Kind],
		}, nil
	}

	ids := []string{start.Value.(string)}
	idTokens := []*Token{start}
	for {
		if tok, err := parser.Match(TTPeriod); err != nil {
			return nil, err
		} else if tok == nil {
			break
		}

		tok, err := parser.Consume(TTIdentifier)
		if err != nil {
			return nil, err
		}

		ids = append(ids, tok.Value.(string))
		idTokens = append(idTokens, tok)
	}

	var t Type
	// A generic argument is a name and nothing more, so only a lone id can be
	// one. Every declaration that can hold a type satisfies DeclGeneric, so what
	// says one was written with parameters is its map being there at all.
	if len(ids) == 1 {
		if owner, ok := resolveGenericArg(parser, ids[0]); ok {
			return &TypeGenericArg{
				start: start,
				owner: owner,
				Name:  ids[0],
			}, nil
		}
	}

	// What the head of a dotted name stands for is not something a parse can
	// settle. Whether `A.B` reaches a nested declaration or an imported file
	// depends on what is in scope where it was written, which is a question for
	// resolution; a parse that answered it by counting the ids would make a
	// nested declaration unreachable from the file it was written in.
	t = &TypeReference{
		start:     start,
		outerDecl: *parser.Decl(),
		Ids:       ids,
		IdTokens:  idTokens,
	}

	if tilde, err := parser.Match(TTTilde); err != nil {
		return nil, err
	} else if tilde != nil {
		var size *uint64
		if lit, err := parser.Match(TTIntLiteral); err != nil {
			return nil, err
		} else if lit != nil {
			size = new(uint64)
			*size = lit.Value.(uint64)
		}

		t = &TypeTailed{
			outerDecl: *parser.Decl(),
			Type:      t.(*TypeReference),
			Size:      size,
		}
	}

	if tok, err := parser.Match(TTLeftBracket); err != nil {
		return nil, err
	} else if tok == nil {
		return t, nil
	}

	expectedTokenKinds := []TokenKind{TTIntLiteral}
	if canBeTailed {
		expectedTokenKinds = append(expectedTokenKinds, TTTilde)
	}

	if tok, err := parser.Match(expectedTokenKinds...); err != nil {
		return nil, err
	} else if tok != nil {
		if tt, ok := t.(*TypeTailed); ok && tt.Size == nil {
			return nil, fmt.Errorf("unsized tailed type %q must be a pointer type", t.String())
		}

		var size *uint64
		if tok.Kind == TTIntLiteral {
			size = new(uint64)
			*size = tok.Value.(uint64)
		}

		if _, err := parser.Consume(TTRightBracket); err != nil {
			return nil, err
		}

		return &TypeArray{
			outerDecl: *parser.Decl(),
			Type:      t,
			Size:      size,
		}, nil
	} else if ga, ok := t.(*TypeGenericArg); ok {
		return nil, fmt.Errorf("generic argument %q cannot be used as a generic type", ga.String())
	}

	var parameters []Type
	for {
		t, err := newTypeHelper(parser, false)
		if err != nil {
			return nil, err
		}
		parameters = append(parameters, t)

		if tok, err := parser.ConsumeAny(TTComma, TTRightBracket); err != nil {
			return nil, err
		} else if tok.Kind == TTRightBracket {
			break
		}
	}

	return &TypeGeneric{
		outerDecl:      *parser.Decl(),
		Type:           t.(TypeResolvable),
		ParameterTypes: parameters,
	}, nil
}

// newTypeArray creates a new array type.
func newTypeArray(parser SyntaxParser, _type Type, canBeTailed bool) (Type, error) {
	var size *uint64

	expectedTokenKinds := []TokenKind{TTIntLiteral}
	if canBeTailed {
		expectedTokenKinds = append(expectedTokenKinds, TTTilde)
	}

	if tok, err := parser.ConsumeAny(expectedTokenKinds...); err != nil {
		return nil, err
	} else if tok.Kind == TTIntLiteral {
		size = new(uint64)
		*size = tok.Value.(uint64)
	}

	if _, err := parser.Consume(TTRightBracket); err != nil {
		return nil, err
	}

	return &TypeArray{
		outerDecl: *parser.Decl(),
		Type:      _type,
		Size:      size,
	}, nil
}

// newTypeFunction creates a new function type.
func newTypeFunction(parser SyntaxParser, _type Type) (Type, error) {
	if tok, err := parser.Match(TTRightParen); err != nil {
		return nil, err
	} else if tok != nil {
		return &TypeFunction{
			outerDecl:  *parser.Decl(),
			ReturnType: _type,
		}, nil
	}

	var parameters []Type
	for {
		t, err := newTypeHelper(parser, false)
		if err != nil {
			return nil, err
		}

		if tok, err := parser.Match(TTPeriodPeriodPeriod); err != nil {
			return nil, err
		} else if tok != nil {
			t = &TypeWrapped{
				kind:      TypeKindVariadic,
				outerDecl: *parser.Decl(),
				Type:      t,
			}
			parameters = append(parameters, t)
			if _, err := parser.Consume(TTRightParen); err != nil {
				return nil, err
			}
			break
		}
		parameters = append(parameters, t)

		if tok, err := parser.ConsumeAny(TTComma, TTRightParen); err != nil {
			return nil, err
		} else if tok.Kind == TTRightParen {
			break
		}
	}

	return &TypeFunction{
		outerDecl:      *parser.Decl(),
		ReturnType:     _type,
		ParameterTypes: parameters,
	}, nil
}

// newTypeThread creates a new thread type.
func newTypeThread(parser SyntaxParser, start *Token) (Type, error) {
	if _, err := parser.Consume(TTLeftBracket); err != nil {
		return nil, err
	}

	t, err := newTypeHelper(parser, false)
	if err != nil {
		return nil, err
	}

	if _, err := parser.Consume(TTRightBracket); err != nil {
		return nil, err
	}

	return &TypeWrapped{
		start:     start,
		kind:      TypeKindThread,
		outerDecl: *parser.Decl(),
		Type:      t,
	}, nil
}

// Accept implements the [Type] interface.
func (t *TypeArray) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypeArray(t)
}

// Equals implements the [Type] interface.
func (t *TypeArray) Equals(other Type) bool {
	otherArr, ok := other.(*TypeArray)
	if !ok || (t.Size == nil) != (otherArr.Size == nil) {
		return false
	}
	if t.Size != nil && *t.Size != *otherArr.Size {
		return false
	}
	return t.Type.Equals(otherArr.Type)
}

// Hash implements the [Type] interface.
func (t *TypeArray) Hash() uint64 {
	hs := make([]uint64, 0, 2)
	hs = append(hs, t.Type.Hash())
	if t.Size != nil {
		hs = append(hs, *t.Size)
	}
	return base.Hash(hs...)
}

func (t *TypeArray) IsSubtypeOf(other Type) bool {
	super, ok := other.(TypeSupertypeable)
	return (ok && super.IsSupertypeOf(t)) || t.Equals(other)
}

// Kind implements the [Type] interface.
func (t *TypeArray) Kind() TypeKind {
	return TypeKindArray
}

// Map implements the [Type] interface.
func (t *TypeArray) Map(types map[GenericArgKey]Type) Type {
	return &TypeArray{
		outerDecl: t.outerDecl,
		Type:      t.Type.Map(types),
		Size:      t.Size,
	}
}

// Start implements the [Type] interface.
func (t *TypeArray) Start() *Token {
	return t.Type.Start()
}

// OuterDecl implements the [Type] interface.
func (t *TypeArray) OuterDecl() Decl {
	return t.outerDecl
}

// String implements the [fmt.Stringer] interface.
func (t *TypeArray) String() string {
	if t.Size == nil {
		return fmt.Sprintf("%s[~]", t.Type.String())
	}
	return fmt.Sprintf("%s[%d]", t.Type.String(), *t.Size)
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypeArray) ContainsGenericArg() bool {
	return t.Type.ContainsGenericArg()
}

// Accept implements the [Type] interface.
func (t *TypeFunction) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypeFunction(t)
}

// Equals implements the [Type] interface.
func (t *TypeFunction) Equals(other Type) bool {
	otherFunc, ok := other.(*TypeFunction)
	if !ok || len(t.ParameterTypes) != len(otherFunc.ParameterTypes) || !t.ReturnType.Equals(otherFunc.ReturnType) {
		return false
	}
	for i, param := range t.ParameterTypes {
		if !param.Equals(otherFunc.ParameterTypes[i]) {
			return false
		}
	}
	return true
}

// Hash implements the [Type] interface.
func (t *TypeFunction) Hash() uint64 {
	hs := make([]uint64, 0, len(t.ParameterTypes)+2)
	hs = append(hs, uint64(TypeKindFunction), t.ReturnType.Hash())
	for _, param := range t.ParameterTypes {
		hs = append(hs, param.Hash())
	}
	return base.Hash(hs...)
}

// IsSubtypeOf relates two function types the usual way — covariant in the
// return and contravariant in each parameter — with one carve-out: const does
// not flow across the signature. A difference in const-ness at a parameter or
// return position fails the relation outright, so neither direction between
// fun(T) and fun(const T), nor between fun() const T and fun() T, is a
// subtype. The explicit cast through [castableTo] stays open for the coder
// who wants to coerce one into the other.
func (t *TypeFunction) IsSubtypeOf(other Type) bool {
	if super, ok := other.(TypeSupertypeable); ok && super.IsSupertypeOf(t) {
		return true
	}

	otherFunc, ok := other.(*TypeFunction)
	if !ok || len(t.ParameterTypes) != len(otherFunc.ParameterTypes) {
		return false
	}

	if IsConst(t.ReturnType) != IsConst(otherFunc.ReturnType) ||
		!t.ReturnType.IsSubtypeOf(otherFunc.ReturnType) {
		return false
	}

	for i, param := range t.ParameterTypes {
		otherParam := otherFunc.ParameterTypes[i]
		if IsConst(param) != IsConst(otherParam) || !otherParam.IsSubtypeOf(param) {
			return false
		}
	}

	return true
}

// Kind implements the [Type] interface.
func (t *TypeFunction) Kind() TypeKind {
	return TypeKindFunction
}

// Start implements the [Type] interface.
func (t *TypeFunction) Start() *Token {
	return t.ReturnType.Start()
}

// Map implements the [Type] interface.
func (t *TypeFunction) Map(types map[GenericArgKey]Type) Type {
	mapped := *t
	mapped.ReturnType = t.ReturnType.Map(types)
	mapped.ParameterTypes = make([]Type, len(t.ParameterTypes))
	for i, param := range t.ParameterTypes {
		mapped.ParameterTypes[i] = param.Map(types)
	}
	return &mapped
}

// OuterDecl implements the [Type] interface.
func (t *TypeFunction) OuterDecl() Decl {
	return t.outerDecl
}

// String implements the [fmt.Stringer] interface.
func (t *TypeFunction) String() string {
	paramsStr := make([]string, len(t.ParameterTypes))
	for i, param := range t.ParameterTypes {
		paramsStr[i] = param.String()
	}
	return fmt.Sprintf("%s(%s)", t.ReturnType.String(), strings.Join(paramsStr, ", "))
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypeFunction) ContainsGenericArg() bool {
	if t.ReturnType.ContainsGenericArg() {
		return true
	}
	for _, param := range t.ParameterTypes {
		if param.ContainsGenericArg() {
			return true
		}
	}
	return false
}

// Accept implements the [Type] interface.
func (t *TypeGeneric) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypeGeneric(t)
}

// Decl implements the [TypeResolvable] interface.
func (t *TypeGeneric) Decl() DeclTypeable {
	d := t.Type.Decl().(DeclGeneric)
	cd, _ := d.GetConcreteDecl(t)
	return cd
}

// Equals implements the [syntax.Type] interface.
//
// The template and the arguments are the whole of it. Comparing the concretes
// the two resolve to would be circular: a concrete is found by looking the
// generic up among the template's instantiations, which is a search that asks
// this question, so answering it that way asks it again.
func (t *TypeGeneric) Equals(other Type) bool {
	otherGeneric, ok := other.(*TypeGeneric)
	if !ok ||
		len(t.ParameterTypes) != len(otherGeneric.ParameterTypes) ||
		!t.Type.Equals(otherGeneric.Type) {
		return false
	}
	for i, param := range t.ParameterTypes {
		if !param.Equals(otherGeneric.ParameterTypes[i]) {
			return false
		}
	}
	return true
}

// Hash implements the [syntax.Type] interface.
func (t *TypeGeneric) Hash() uint64 {
	hs := make([]uint64, 0, len(t.ParameterTypes)+2)
	hs = append(hs, uint64(TypeKindGeneric), t.Type.Hash())
	for _, param := range t.ParameterTypes {
		hs = append(hs, param.Hash())
	}
	return base.Hash(hs...)
}

// IsSubtypeOf implements the [Type] interface.
func (t *TypeGeneric) IsSubtypeOf(other Type) bool {
	super, ok := other.(TypeSupertypeable)
	return (ok && super.IsSupertypeOf(t)) || t.Equals(other)
}

func (t *TypeGeneric) IsTailable() bool {
	tailable, ok := t.Type.(TypeTailable)
	return ok && tailable.IsTailable()
}

// Kind implements the [Type] interface.
func (t *TypeGeneric) Kind() TypeKind {
	return TypeKindGeneric
}

// Map implements the [Type] interface.
func (t *TypeGeneric) Map(types map[GenericArgKey]Type) Type {
	mapped := *t
	mapped.ParameterTypes = make([]Type, len(t.ParameterTypes))
	for i, param := range t.ParameterTypes {
		mapped.ParameterTypes[i] = param.Map(types)
	}
	return &mapped
}

// Start implements the [Type] interface.
func (t *TypeGeneric) Start() *Token {
	return t.Type.Start()
}

// OuterDecl implements the [Type] interface.
func (t *TypeGeneric) OuterDecl() Decl {
	return t.Type.OuterDecl()
}

// String implements the [fmt.Stringer] interface.
func (t *TypeGeneric) String() string {
	paramStrs := make([]string, len(t.ParameterTypes))
	for i, param := range t.ParameterTypes {
		paramStrs[i] = param.String()
	}
	return fmt.Sprintf("%s[%s]", t.Type.String(), strings.Join(paramStrs, ", "))
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypeGeneric) ContainsGenericArg() bool {
	for _, param := range t.ParameterTypes {
		if param.ContainsGenericArg() {
			return true
		}
	}
	return false
}

// Accept implements the [Type] interface.
func (t *TypeGenericArg) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypeGenericArg(t)
}

// Equals implements the [Type] interface. What a generic argument is depends on
// which declaration wrote it as much as on what it is called: the T of Outer[T]
// and the T of Inner[T] are two parameters that happen to share a name, and
// substituting one is not substituting the other. That pair is the identity
// [GenericArgKey] is built from and the one [TypeGenericArg.Hash] mixes in, so
// the name alone would leave two types that hash apart comparing equal.
func (t *TypeGenericArg) Equals(other Type) bool {
	otherGenericArg, ok := other.(*TypeGenericArg)
	return ok &&
		t.Name == otherGenericArg.Name &&
		t.owner == otherGenericArg.owner
}

// Hash implements the [Type] interface.
func (t *TypeGenericArg) Hash() uint64 {
	return base.Hash(uint64(t.Kind()), base.FNV(t.Name), t.owner.Hash())
}

// IsSubtypeOf implements the [Type] interface.
func (t *TypeGenericArg) IsSubtypeOf(other Type) bool {
	if super, ok := other.(TypeSupertypeable); (ok && super.IsSupertypeOf(t)) || t.Equals(other) {
		return true
	}

	key := GenericArgKey{
		Owner: t.owner,
		Name:  t.Name,
	}
	bound, _ := t.owner.Generics().Get(key)
	upperBound, ok := bound.(GenericUpperBounded)
	if !ok {
		return false
	}

	for _, boundType := range upperBound.Bounds() {
		if !boundType.IsSubtypeOf(other) {
			return false
		}
	}

	return true
}

// IsSupertypeOf implements the [Type] interface.
func (t *TypeGenericArg) IsSupertypeOf(child Type) bool {
	key := GenericArgKey{
		Owner: t.owner,
		Name:  t.Name,
	}
	bound, _ := t.owner.Generics().Get(key)
	lowerBound, ok := bound.(GenericLowerBounded)
	if !ok {
		return false
	}

	for _, boundType := range lowerBound.Bounds() {
		if !child.IsSubtypeOf(boundType) {
			return false
		}
	}

	return true
}

// Kind implements the [Type] interface.
func (t *TypeGenericArg) Kind() TypeKind {
	return TypeKindGenericArg
}

// Map implements the [Type] interface.
func (t *TypeGenericArg) Map(types map[GenericArgKey]Type) Type {
	key := GenericArgKey{
		Owner: t.owner,
		Name:  t.Name,
	}
	if mapped, ok := types[key]; ok {
		return mapped
	}
	return t
}

// Owner is who the generic argument was declared by: a generic decl or a
// generic method.
func (t *TypeGenericArg) Owner() GenericOwner {
	return t.owner
}

// OuterDecl implements the [Type] interface. For a class-level parameter the
// owner itself is the containing decl; for a method-level one it is the decl
// the method lives on.
func (t *TypeGenericArg) OuterDecl() Decl {
	return ownerContainingDecl(t.owner)
}

// Start implements the [Type] interface.
func (t *TypeGenericArg) Start() *Token {
	return t.start
}

// String implements the [fmt.Stringer] interface.
func (t *TypeGenericArg) String() string {
	return t.Name
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypeGenericArg) ContainsGenericArg() bool {
	return true
}

// Accept implements the [Type] interface.
func (t *TypePrimitive) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypePrimitive(t)
}

// Equals implements the [Type] interface.
func (t *TypePrimitive) Equals(other Type) bool {
	return t.kind == other.Kind()
}

// Hash implements the [Type] interface.
func (t *TypePrimitive) Hash() uint64 {
	return uint64(t.kind)
}

// IsSubtypeOf implements the [Type] interface.
func (t *TypePrimitive) IsSubtypeOf(other Type) bool {
	if super, ok := other.(TypeSupertypeable); ok && super.IsSupertypeOf(t) {
		return true
	}

	return t.Equals(other)
}

// IsSupertypeOf implements the [TypeSupertypeable] interface. The any primitive
// is the one supertype a plain primitive carries; it stands above every other
// type, which is what lets a value of any type be stored in an any slot. Every
// other primitive is its own line of descent and nothing else is below it, so
// the question is answered by whether this is any at all.
func (t *TypePrimitive) IsSupertypeOf(child Type) bool {
	return t.kind == TypeKindAny && child.Kind() != TypeKindVoid
}

// Kind implements the [Type] interface.
func (t *TypePrimitive) Kind() TypeKind {
	return t.kind
}

// Map implements the [Type] interface.
func (t *TypePrimitive) Map(types map[GenericArgKey]Type) Type {
	return t
}

// OuterDecl implements the [Type] interface.
func (t *TypePrimitive) OuterDecl() Decl {
	return t.outerDecl
}

// Start implements the [Type] interface.
func (t *TypePrimitive) Start() *Token {
	return t.start
}

// String implements the [fmt.Stringer] interface.
func (t *TypePrimitive) String() string {
	return t.kind.String()
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypePrimitive) ContainsGenericArg() bool {
	return false
}

// Accept implements the [Type] interface.
func (t *TypeReference) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypeReference(t)
}

// SetDecl sets the declaration of the type reference.
func (t *TypeReference) SetDecl(decl DeclTypeable) {
	t.decl = decl
}

// Decl implements the [TypeResolvable] interface.
func (t *TypeReference) Decl() DeclTypeable {
	return t.decl
}

// Equals implements the [syntax.Type] interface.
func (t *TypeReference) Equals(other Type) bool {
	otherReference, ok := other.(*TypeReference)
	return ok && t.decl == otherReference.decl
}

// Hash implements the [syntax.Type] interface.
func (t *TypeReference) Hash() uint64 {
	return base.Hash(uint64(TypeKindReference), t.decl.Hash())
}

func (t *TypeReference) IsSubtypeOf(other Type) bool {
	super, ok := other.(TypeSupertypeable)
	return (ok && super.IsSupertypeOf(t)) || t.Equals(other)
}

// Kind implements the [Type] interface.
func (t *TypeReference) Kind() TypeKind {
	return TypeKindReference
}

// OuterDecl implements the [Type] interface.
func (t *TypeReference) OuterDecl() Decl {
	return t.outerDecl
}

// Map implements the [Type] interface.
func (t *TypeReference) Map(types map[GenericArgKey]Type) Type {
	return t
}

// Start implements the [Type] interface.
func (t *TypeReference) Start() *Token {
	return t.start
}

// String implements the [fmt.Stringer] interface.
//
// A reference is written as it was spelled, preceded by the archive its
// declaration was found in. Two archives may declare the same name without
// being the same type, and a message naming only the spelling would read as
// though a type were not a subtype of itself. Nothing precedes a declaration
// from the project being compiled: there is no archive to name, and the file
// the message is already located to says the rest.
//
// The reference is spoken about before it is resolved — a name that resolves to
// nothing is reported by this very method — so an unattached declaration is the
// unqualified spelling rather than an error.
func (t *TypeReference) String() string {
	name := strings.Join(t.Ids, ".")
	if t.decl == nil {
		return name
	}
	if archive := t.decl.File().Project.Archive; archive != "" {
		return archive + ":" + name
	}
	return name
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypeReference) ContainsGenericArg() bool {
	return false
}

// Accept implements the [Type] interface.
func (t *TypeTailed) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypeTailed(t)
}

// Decl implements the [TypeResolvable] interface.
func (t *TypeTailed) Decl() DeclTypeable {
	return t.Type.Decl()
}

// Equals implements the [syntax.Type] interface.
func (t *TypeTailed) Equals(other Type) bool {
	otherTailed, ok := other.(*TypeTailed)
	if !ok || (t.Size == nil) != (otherTailed.Size == nil) {
		return false
	}
	if t.Size != nil && *t.Size != *otherTailed.Size {
		return false
	}
	return t.Type.Equals(otherTailed.Type)
}

// Hash implements the [syntax.Type] interface.
func (t *TypeTailed) Hash() uint64 {
	if t.Size == nil {
		return base.Hash(uint64(TypeKindTailed), t.Type.Hash())
	}
	return base.Hash(uint64(TypeKindTailed), t.Type.Hash(), *t.Size)
}

func (t *TypeTailed) IsSubtypeOf(other Type) bool {
	if super, ok := other.(TypeSupertypeable); (ok && super.IsSupertypeOf(t)) || t.Equals(other) {
		return true
	}

	return t.Type.Equals(other)
}

// Kind implements the [Type] interface.
func (t *TypeTailed) Kind() TypeKind {
	return TypeKindTailed
}

func (t *TypeTailed) IsTailable() bool {
	return t.Size == nil
}

// OuterDecl implements the [Type] interface.
func (t *TypeTailed) OuterDecl() Decl {
	return t.Type.OuterDecl()
}

// Map implements the [Type] interface.
func (t *TypeTailed) Map(types map[GenericArgKey]Type) Type {
	return t
}

// Start implements the [Type] interface.
func (t *TypeTailed) Start() *Token {
	return t.Type.Start()
}

// String implements the [fmt.Stringer] interface.
func (t *TypeTailed) String() string {
	if t.Size == nil {
		return fmt.Sprintf("%s~", t.Type.String())
	}
	return fmt.Sprintf("%s~%d", t.Type.String(), *t.Size)
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypeTailed) ContainsGenericArg() bool {
	return false
}

// Accept implements the [Type] interface.
func (t *TypeWrapped) Accept(visitor TypeVisitor) error {
	return visitor.VisitTypeWrapped(t)
}

// Equals implements the [Type] interface.
func (t *TypeWrapped) Equals(other Type) bool {
	otherWrapped, ok := other.(*TypeWrapped)
	return ok &&
		t.kind == otherWrapped.kind &&
		t.Type.Equals(otherWrapped.Type)
}

// Hash implements the [Type] interface.
func (t *TypeWrapped) Hash() uint64 {
	return base.Hash(uint64(t.kind), t.Type.Hash())
}

// IsSubtypeOf implements the [Type] interface.
func (t *TypeWrapped) IsSubtypeOf(other Type) bool {
	// A const value promises not to be mutated through the slot it lies in, so
	// it may stand in anywhere its base type does — including as a non-const
	// argument, which merely gives the callee a copy to work on. The reverse
	// is not true: a non-const slot the caller might still write to is not a
	// const value, and a parameter asking for one has to be given one.
	// Only the outermost const is seen through; nested const stays strict so
	// e.g. int$* is not a subtype of int*.
	if t.kind == TypeKindConst {
		if other, ok := other.(*TypeWrapped); ok && other.kind == TypeKindConst {
			return t.Type.IsSubtypeOf(other.Type)
		}
		return t.Type.IsSubtypeOf(other)
	}

	// The null literal — recognised by the shared TypeNull singleton — stands
	// in for a value of any reference-held type: a pointer the type system
	// knows about, a function, or a string. A user's own void* keeps its usual
	// rules; this branch only promotes the one sentinel value.
	if t == TypeNull {
		if otherWrapped, ok := other.(*TypeWrapped); ok && otherWrapped.kind == TypeKindPointer {
			return true
		}
		if otherPrim, ok := other.(*TypePrimitive); ok && otherPrim.kind == TypeKindString {
			return true
		}
		if _, ok := other.(*TypeFunction); ok {
			return true
		}
	}

	if super, ok := other.(TypeSupertypeable); (ok && super.IsSupertypeOf(t)) || t.Equals(other) {
		return true
	}

	otherWrapped, ok := other.(*TypeWrapped)
	if !ok {
		return false
	}

	if t.kind == TypeKindPointer && otherWrapped.kind == TypeKindPointer {
		if otherWrapped.Type.Kind() == TypeKindVoid {
			return true
		}

		// A tail's length rides with the type id rather than sitting in the
		// layout, so a sized tailed type is laid out exactly as the unsized one
		// it was written from: Buf~N* is a Buf~* whatever N is. The reverse does
		// not hold, since an unsized tail is the one that has no length to give.
		if otherTailed, ok := otherWrapped.Type.(*TypeTailed); ok && otherTailed.Size == nil {
			if tailed, ok := t.Type.(*TypeTailed); ok && tailed.Type.Equals(otherTailed.Type) {
				return true
			}
		}

		tr, ok := t.Type.(TypeResolvable)
		if !ok {
			return false
		}

		ds, ok := tr.Decl().(DeclSubtypeable)
		if !ok {
			return false
		}

		return ds.IsSubtypeOf(otherWrapped)
	}

	return false
}

// Kind implements the [Type] interface.
func (t *TypeWrapped) Kind() TypeKind {
	return t.kind
}

// Map implements the [Type] interface.
func (t *TypeWrapped) Map(types map[GenericArgKey]Type) Type {
	return &TypeWrapped{
		start:     t.start,
		outerDecl: t.outerDecl,
		kind:      t.kind,
		Type:      t.Type.Map(types),
	}
}

// OuterDecl implements the [Type] interface.
func (t *TypeWrapped) OuterDecl() Decl {
	return t.outerDecl
}

// Start implements the [Type] interface.
func (t *TypeWrapped) Start() *Token {
	if t.start != nil {
		return t.start
	}
	return t.Type.Start()
}

// String implements the [fmt.Stringer] interface.
func (t *TypeWrapped) String() string {
	switch t.kind {
	case TypeKindPointer:
		return fmt.Sprintf("%s*", t.Type.String())
	case TypeKindThread:
		return fmt.Sprintf("thread[%s]", t.Type.String())
	case TypeKindVariadic:
		return fmt.Sprintf("%s...", t.Type.String())
	case TypeKindConst:
		return fmt.Sprintf("%s$", t.Type.String())
	default:
		panic("unreachable")
	}
}

// ContainsGenericArg implements the [Type] interface.
func (t *TypeWrapped) ContainsGenericArg() bool {
	return t.Type.ContainsGenericArg()
}
