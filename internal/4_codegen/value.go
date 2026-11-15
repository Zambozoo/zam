package codegen

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// ValueKind is how the bytes of a value are read.
type ValueKind int

const (
	// ValueKindOpaque is a value whose layout is not known, such as one whose
	// type an instantiation decides.
	ValueKindOpaque ValueKind = iota
	ValueKindBool
	ValueKindByte
	ValueKindChar
	ValueKindInt
	ValueKindUint
	ValueKindFloat
	ValueKindPosit
	// ValueKindStr is the address of a string: a mark word, a length, and
	// the bytes.
	ValueKindStr
	ValueKindType
	// ValueKindAny is the address of a box holding a value of any type.
	ValueKindAny
	ValueKindPointer
	ValueKindThread
	ValueKindFunction
	// ValueKindArray is Length elements laid inline, or with a Length of
	// zero, the address of elements of a length the value does not say.
	ValueKindArray
	ValueKindTuple
	ValueKindStruct
	ValueKindClass
	// ValueKindEnum is a value whose first word is the ordinal of the value
	// of the enum it is.
	ValueKindEnum
	// ValueKindInterface is the address of an instance of a class that an
	// abstract or interface names, whose header says which.
	ValueKindInterface
)

type (
	// ValueType says how to read a value of a type.
	ValueType struct {
		// Name is the type as written.
		Name string
		Kind ValueKind
		// Size is how many bytes a value takes laid inline.
		Size uint64
		// Elem is what a pointer points to, or an array holds.
		Elem *ValueType
		// Length is how many elements an array holds inline.
		Length uint64
		// Header is how many bytes lie ahead of the fields.
		Header uint64
		// Fields are the members of a struct, class or enum, and the elements
		// of a tuple, at their offsets after the header.
		Fields []ValueField
	}

	// ValueField is a member of a value.
	ValueField struct {
		Name   string
		Offset uint64
		Type   *ValueType
	}

	// valueTypes builds the value types of a program's types, each once, so
	// that a type that holds a pointer to itself is a type that refers to
	// itself.
	valueTypes struct {
		byName map[string]*ValueType
	}
)

func newValueTypes() *valueTypes {
	return &valueTypes{byName: map[string]*ValueType{}}
}

// of answers the value type of t.
func (b *valueTypes) of(t syntax.Type) *ValueType {
	t = syntax.StripConst(t)
	name := t.String()
	if vt, ok := b.byName[name]; ok {
		return vt
	}
	vt := &ValueType{Name: name}
	b.byName[name] = vt
	if t.ContainsGenericArg() {
		return vt
	}
	vt.Size, _ = fieldByteInfo(t)

	switch tt := t.(type) {
	case *syntax.TypePrimitive:
		vt.Kind = primitiveValueKinds[tt.Kind()]
	case *syntax.TypeWrapped:
		switch tt.Kind() {
		case syntax.TypeKindPointer:
			vt.Kind, vt.Elem = ValueKindPointer, b.of(tt.Type)
		case syntax.TypeKindThread:
			vt.Kind = ValueKindThread
		case syntax.TypeKindVariadic:
			vt.Kind, vt.Elem = ValueKindArray, b.of(tt.Type)
		}
	case *syntax.TypeArray:
		vt.Kind, vt.Elem = ValueKindArray, b.of(tt.Type)
		if tt.Size != nil {
			vt.Length = *tt.Size
		}
	case *syntax.TypeFunction:
		vt.Kind = ValueKindFunction
	case *syntax.TypeTuple:
		vt.Kind = ValueKindTuple
		_, _, offsets := tupleLayout(tt)
		for i, element := range tt.Types {
			vt.Fields = append(vt.Fields, ValueField{Name: strconv.Itoa(i), Offset: offsets[i], Type: b.of(element)})
		}
	case *syntax.TypeReference:
		b.decl(vt, tt.Decl())
	case *syntax.TypeGeneric:
		if concrete := tt.Decl(); concrete != nil {
			b.decl(vt, concrete)
		}
	}
	return vt
}

// decl fills vt in as a value of d. A generic declaration's members are laid
// out by its instantiations, and so are known only of one of those.
func (b *valueTypes) decl(vt *ValueType, d syntax.DeclTypeable) {
	if _, concrete := d.(syntax.DeclConcrete); concrete {
		if d.DeclType().ContainsGenericArg() {
			return
		}
	} else if gd, ok := d.(syntax.DeclGeneric); ok && gd.Generics() != nil {
		return
	}
	switch d.Kind() {
	case syntax.KindClass:
		vt.Kind = ValueKindClass
	case syntax.KindStruct:
		vt.Kind = ValueKindStruct
	case syntax.KindEnum:
		vt.Kind = ValueKindEnum
	case syntax.KindAbstract, syntax.KindInterface:
		vt.Kind = ValueKindInterface
		return
	default:
		return
	}

	vt.Size, _, _ = layoutOf(d)
	vt.Header = instanceHeaderByteSize(d)
	for name, m := range instanceFields[*syntax.FieldMember](d) {
		if isUnboundedTail(m.FieldType()) {
			continue
		}
		vt.Fields = append(vt.Fields, ValueField{Name: name, Offset: *m.Offset(), Type: b.of(m.FieldType())})
	}
}

// primitiveValueKinds are the value kinds of the primitive types.
var primitiveValueKinds = map[syntax.TypeKind]ValueKind{
	syntax.TypeKindBool:   ValueKindBool,
	syntax.TypeKindByte:   ValueKindByte,
	syntax.TypeKindChar:   ValueKindChar,
	syntax.TypeKindInt:    ValueKindInt,
	syntax.TypeKindUint:   ValueKindUint,
	syntax.TypeKindFloat:  ValueKindFloat,
	syntax.TypeKindPosit:  ValueKindPosit,
	syntax.TypeKindString: ValueKindStr,
	syntax.TypeKindType:   ValueKindType,
	syntax.TypeKindAny:    ValueKindAny,
}

// classValueTypes answers the value type of every class an instance can be
// of, and the instantiations of every generic class and abstract, by the short
// type id the header of an instance names them by.
func classValueTypes(ast *syntax.AST, types *valueTypes, r *typeResolver) (map[uint32]*ValueType, map[uint32]syntax.DeclConcrete) {
	classes := map[uint32]*ValueType{}
	instances := map[uint32]syntax.DeclConcrete{}
	var visit func(d syntax.Decl)
	visit = func(d syntax.Decl) {
		typeable, ok := d.(syntax.DeclTypeable)
		kind := d.Kind()
		if ok && (kind == syntax.KindClass || kind == syntax.KindAbstract) {
			id := uint32(r.declTypeID(d, *typeable.TypeIndex())) << 16
			gd, generic := d.(syntax.DeclGeneric)
			switch {
			case !generic || gd.Generics() == nil:
				if kind == syntax.KindClass {
					vt := &ValueType{Name: qualifiedName(d)}
					types.decl(vt, typeable)
					classes[id] = vt
				}
			default:
				for _, entry := range gd.Concretes().All() {
					concrete, ok := entry.Value.(syntax.DeclConcrete)
					identified, numbered := entry.Value.(interface{ ConcreteID() *uint16 })
					if !ok || !numbered || concrete.DeclType().ContainsGenericArg() {
						continue
					}
					short := id | uint32(*identified.ConcreteID())
					instances[short] = concrete
					if kind == syntax.KindClass {
						classes[short] = types.of(entry.Key)
					}
				}
			}
		}
		for nested := range d.Decls() {
			visit(nested)
		}
	}
	for _, entry := range ast.CodeFiles.All() {
		visit(entry.Value.Decl)
	}
	return classes, instances
}

// Static is a static member of the program, in the static region.
type Static struct {
	// Decl is the declaration it is a member of, and Name its own name.
	Decl, Name string
	// Offset is where in the static region it lies.
	Offset uint64
	Type   *ValueType
	// Std is a member of the standard library's.
	Std bool
}

// staticVariables answers every static member of the program, offsets being
// where the static region holds the slot each is numbered by.
func staticVariables(ast *syntax.AST, types *valueTypes, offsets []int) []Static {
	var statics []Static
	var visit func(d syntax.Decl)
	visit = func(d syntax.Decl) {
		for name, field := range d.StaticFields() {
			member, ok := field.(*syntax.FieldMember)
			if !ok || *member.Index() < 0 || *member.Index() >= len(offsets) {
				continue
			}
			vt := &ValueType{Name: member.FieldType().String()}
			if !member.FieldType().ContainsGenericArg() {
				vt = types.of(member.FieldType())
			}
			statics = append(statics, Static{
				Decl:   qualifiedName(d),
				Name:   name,
				Offset: uint64(offsets[*member.Index()]),
				Type:   vt,
				Std:    d.File().Path.IsStd(),
			})
		}
		for nested := range d.Decls() {
			visit(nested)
		}
	}
	for _, entry := range ast.CodeFiles.All() {
		visit(entry.Value.Decl)
	}
	slices.SortFunc(statics, func(a, b Static) int {
		return cmp.Or(strings.Compare(a.Decl, b.Decl), strings.Compare(a.Name, b.Name))
	})
	return statics
}
