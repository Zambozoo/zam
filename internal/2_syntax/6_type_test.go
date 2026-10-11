package syntax

import (
	"testing"

	"github.com/stretchr/testify/assert"
	base "github.com/zambozoo/zam/internal/0_base"
)

func TestVariadicElement(t *testing.T) {
	t.Parallel()

	element, ok := VariadicElement(NewWrappedType(TypeKindVariadic, TypeInt))
	assert.True(t, ok)
	assert.Equal(t, TypeInt, element)

	_, ok = VariadicElement(NewWrappedType(TypeKindPointer, TypeInt))
	assert.False(t, ok)

	_, ok = VariadicElement(TypeInt)
	assert.False(t, ok)
}

// A variadic is the one parameter whose declared type is not the type the body
// binds. The call site packs the arguments and passes a reference, so a body
// that saw the declared type would be reading one argument where a pointer to
// all of them was handed over.
func TestParameterBindingType(t *testing.T) {
	t.Parallel()

	bound := ParameterBindingType(NewWrappedType(TypeKindVariadic, TypeInt))
	assert.Equal(t, TypeKindPointer, bound.Kind())
	assert.Equal(t, TypeInt, bound.(*TypeWrapped).Type)

	assert.Equal(t, TypeInt, ParameterBindingType(TypeInt))

	pointer := NewWrappedType(TypeKindPointer, TypeInt)
	assert.Equal(t, pointer, ParameterBindingType(pointer))
}

// Two archives may declare the same name without being the same type, so a
// message naming only the spelling would read as though a type were not a
// subtype of itself. The archive is what tells the two copies apart.
//
// Nothing precedes a name from the project being compiled. There is no archive
// to name, and every message is already located to a file.
func TestTypeReferenceStringNamesTheArchiveItWasDeclaredIn(t *testing.T) {
	t.Parallel()

	newFile := func(archive string) *File {
		return &File{
			Path:    base.NewPath("Dep.zam", ""),
			Project: &Project{Archive: archive},
		}
	}
	reference := func(file *File, ids ...string) *TypeReference {
		return &TypeReference{Ids: ids, decl: newTestClass(file, ids[len(ids)-1], nil)}
	}

	t.Run("a declaration from an archive is preceded by it", func(t *testing.T) {
		file := newFile("example.invalid/dep/archive/v1.zip")
		assert.Equal(t, "example.invalid/dep/archive/v1.zip:Dep.Thing",
			reference(file, "Dep", "Thing").String())
	})

	t.Run("two copies of one name are told apart", func(t *testing.T) {
		v1 := reference(newFile("example.invalid/dep/archive/v1.zip"), "Thing")
		v2 := reference(newFile("example.invalid/dep/archive/v2.zip"), "Thing")
		assert.NotEqual(t, v1.String(), v2.String())
	})

	t.Run("a declaration from the compiled project is not", func(t *testing.T) {
		assert.Equal(t, "Dep.Thing", reference(newFile(""), "Dep", "Thing").String())
	})

	// Resolution reports a name that stands for nothing through this very
	// method, so an unattached reference is asked about before it has anything
	// to be qualified by.
	t.Run("an unresolved reference is the spelling alone", func(t *testing.T) {
		assert.Equal(t, "Nowhere", (&TypeReference{Ids: []string{"Nowhere"}}).String())
	})
}

// A generic argument is named by the declaration that wrote it as much as by its
// spelling, which is the pair GenericArgKey substitutes on and the pair Hash has
// always mixed in. Equals asking only about the spelling left two types that
// hash apart comparing equal — the one way a hash and an equality may not
// disagree, since a TypeMap buckets by hash and only then confirms with Equals,
// so a lookup for one would walk straight past the other.
func TestGenericArgIsNamedByItsDeclarationToo(t *testing.T) {
	t.Parallel()

	file := &File{Path: base.NewPath("Dep.zam", ""), Project: &Project{}}
	outer, inner := newTestClass(file, "Outer", nil), newTestClass(file, "Inner", nil)
	arg := func(d DeclGeneric) *TypeGenericArg {
		return &TypeGenericArg{owner: d, Name: "T"}
	}

	t.Run("one declaration's parameter is itself", func(t *testing.T) {
		assert.True(t, arg(outer).Equals(arg(outer)))
		assert.Equal(t, arg(outer).Hash(), arg(outer).Hash())
	})

	t.Run("two declarations' parameters only share a name", func(t *testing.T) {
		assert.False(t, arg(outer).Equals(arg(inner)))
		assert.NotEqual(t, arg(outer).Hash(), arg(inner).Hash())
	})

	t.Run("a map finds back what it was given", func(t *testing.T) {
		types := NewTypeMap[string]()
		types.Set(arg(outer), "outer")

		found, i := types.Get(arg(outer))
		assert.GreaterOrEqual(t, i, 0)
		assert.Equal(t, "outer", found)

		_, i = types.Get(arg(inner))
		assert.Negative(t, i)
	})
}

// Any is the one supertype a plain primitive carries: every other type stands
// below it, which is what lets a value of any type be boxed into an any slot. A
// void is the lone exception, since there is no value of void to box in the
// first place.
func TestAnyIsSupertypeOfEveryType(t *testing.T) {
	t.Parallel()

	file := &File{Path: base.NewPath("Dep.zam", ""), Project: &Project{}}
	class := &TypeReference{Ids: []string{"Box"}, decl: newTestClass(file, "Box", nil)}

	t.Run("primitives are subtypes of any", func(t *testing.T) {
		assert.True(t, TypeInt.IsSubtypeOf(TypeAny))
		assert.True(t, TypeBool.IsSubtypeOf(TypeAny))
		assert.True(t, TypeString.IsSubtypeOf(TypeAny))
		assert.True(t, TypeFloat.IsSubtypeOf(TypeAny))
	})

	t.Run("a class is a subtype of any", func(t *testing.T) {
		assert.True(t, class.IsSubtypeOf(TypeAny))
	})

	t.Run("a pointer is a subtype of any", func(t *testing.T) {
		assert.True(t, NewWrappedType(TypeKindPointer, TypeInt).IsSubtypeOf(TypeAny))
	})

	t.Run("any is a subtype of itself", func(t *testing.T) {
		assert.True(t, TypeAny.IsSubtypeOf(TypeAny))
	})

	t.Run("any is not a subtype of anything else", func(t *testing.T) {
		assert.False(t, TypeAny.IsSubtypeOf(TypeInt))
		assert.False(t, TypeAny.IsSubtypeOf(class))
	})

	t.Run("void is not a subtype of any", func(t *testing.T) {
		assert.False(t, TypeVoid.IsSubtypeOf(TypeAny),
			"void is not a value and so has nothing to box")
	})
}

// An unsized tail takes its length at the allocation, so the type that names one
// has to stand for every length it could be given: new(Buf~, n) is written into a
// Buf~* and nothing about the type says which n it was.
func TestSizedTailIsAnyLengthOfTheUnsizedOne(t *testing.T) {
	t.Parallel()

	file := &File{Path: base.NewPath("Dep.zam", ""), Project: &Project{}}
	buf := &TypeReference{Ids: []string{"Buf"}, decl: newTestClass(file, "Buf", nil)}
	other := &TypeReference{Ids: []string{"Bin"}, decl: newTestClass(file, "Bin", nil)}
	tail := func(ref *TypeReference, size *uint64) *TypeWrapped {
		return NewWrappedType(TypeKindPointer, &TypeTailed{Type: ref, Size: size})
	}
	four, eight := uint64(4), uint64(8)

	t.Run("a length is a subtype of no length", func(t *testing.T) {
		assert.True(t, tail(buf, &four).IsSubtypeOf(tail(buf, nil)))
		assert.True(t, tail(buf, &eight).IsSubtypeOf(tail(buf, nil)))
	})

	t.Run("no length is not a subtype of a length", func(t *testing.T) {
		assert.False(t, tail(buf, nil).IsSubtypeOf(tail(buf, &four)),
			"an unsized tail is the one with no length to give")
	})

	t.Run("two lengths are unrelated", func(t *testing.T) {
		assert.False(t, tail(buf, &four).IsSubtypeOf(tail(buf, &eight)))
	})

	t.Run("the tail does not relate what the declaration does not", func(t *testing.T) {
		assert.False(t, tail(buf, &four).IsSubtypeOf(tail(other, nil)))
	})
}

// A method is reached on an instance whether the instance is named outright or
// addressed, and every subtype relation is written between pointers, so a
// selection that could not see through one could not reach a method on anything
// polymorphic at all.
//
// One level is seen through and no more: a pointer to a pointer addresses a
// pointer, which declares nothing there is a field to select out of.
func TestSelectionTargetSeesThroughOnePointer(t *testing.T) {
	t.Parallel()

	file := &File{Path: base.NewPath("Dep.zam", ""), Project: &Project{}}
	class := newTestClass(file, "Box", nil)
	value := &TypeReference{Ids: []string{"Box"}, decl: class}
	pointer := NewWrappedType(TypeKindPointer, value)

	t.Run("a type that names a declaration is its own target", func(t *testing.T) {
		target, ok := SelectionTarget(value)
		assert.True(t, ok)
		assert.Equal(t, class, target.Decl())
	})

	t.Run("a pointer is the declaration it addresses", func(t *testing.T) {
		target, ok := SelectionTarget(pointer)
		assert.True(t, ok)
		assert.Equal(t, class, target.Decl(),
			"a pointer reaches the same declaration the value does")
	})

	t.Run("a pointer to a pointer reaches nothing", func(t *testing.T) {
		_, ok := SelectionTarget(NewWrappedType(TypeKindPointer, pointer))
		assert.False(t, ok)
	})

	t.Run("a type declaring nothing reaches nothing", func(t *testing.T) {
		_, ok := SelectionTarget(TypeInt)
		assert.False(t, ok)

		_, ok = SelectionTarget(NewWrappedType(TypeKindPointer, TypeInt))
		assert.False(t, ok, "a pointer to a primitive has no declaration behind it")
	})

	// Only a pointer is seen through. A variadic is a wrapper too, and one whose
	// element may well declare something, but it is not an address of that
	// element and nothing is selected out of it.
	t.Run("another wrapper is not seen through", func(t *testing.T) {
		_, ok := SelectionTarget(NewWrappedType(TypeKindVariadic, value))
		assert.False(t, ok)
	})
}

// TestReachedThroughAddress covers what says a member selection has a home
// without having to look at the expression that produced the object.
func TestReachedThroughAddress(t *testing.T) {
	t.Parallel()

	file := &File{Path: base.NewPath("Dep.zam", ""), Project: &Project{}}
	class := newTestClass(file, "Box", nil)
	value := &TypeReference{Ids: []string{"Box"}, decl: class}

	reference := func(kind FieldKind) Type {
		d := newTestClass(file, "Ref", nil)
		d.kind = kind
		return &TypeReference{Ids: []string{"Ref"}, decl: d}
	}

	t.Run("a pointer is an address", func(t *testing.T) {
		assert.True(t, ReachedThroughAddress(NewWrappedType(TypeKindPointer, value)))
		assert.True(t, ReachedThroughAddress(NewWrappedType(TypeKindPointer, TypeInt)),
			"what it points at does not have to declare anything")
	})

	t.Run("an abstract or an interface is an address", func(t *testing.T) {
		assert.True(t, ReachedThroughAddress(reference(KindAbstract)))
		assert.True(t, ReachedThroughAddress(reference(KindInterface)))
	})

	// These are the instance rather than a reference to one, so whether they lie
	// anywhere is the expression's answer to give and not the type's.
	t.Run("a type laid out inline is not", func(t *testing.T) {
		assert.False(t, ReachedThroughAddress(value))
		assert.False(t, ReachedThroughAddress(reference(KindStruct)))
		assert.False(t, ReachedThroughAddress(reference(KindEnum)))
	})

	t.Run("a type declaring nothing is not", func(t *testing.T) {
		assert.False(t, ReachedThroughAddress(TypeInt))
		assert.False(t, ReachedThroughAddress(NewWrappedType(TypeKindVariadic, value)),
			"only a pointer is an address; another wrapper is not")
	})
}

// Const is a compile-time promise about a slot and reads as a trailing $. It
// chains with the other suffixes rather than standing in front of a base: a
// const of a pointer is int*$ and a pointer to a const is int$*, the one being
// the slot the compiler watches and the other what the pointed-at slot is.
func TestConstTypeRoundTrips(t *testing.T) {
	t.Parallel()

	constOf := func(t Type) *TypeWrapped {
		return NewWrappedType(TypeKindConst, t)
	}
	ptrOf := func(t Type) *TypeWrapped {
		return NewWrappedType(TypeKindPointer, t)
	}

	t.Run("const wraps a primitive", func(t *testing.T) {
		assert.Equal(t, "int$", constOf(TypeInt).String())
	})

	t.Run("const inside and outside a pointer are different", func(t *testing.T) {
		assert.Equal(t, "int$*", ptrOf(constOf(TypeInt)).String())
		assert.Equal(t, "int*$", constOf(ptrOf(TypeInt)).String())
	})

	t.Run("IsConst sees only the outermost wrapper", func(t *testing.T) {
		assert.True(t, IsConst(constOf(TypeInt)))
		assert.False(t, IsConst(ptrOf(constOf(TypeInt))),
			"the outermost wrapper is a pointer; the const lies behind it")
		assert.False(t, IsConst(TypeInt))
	})

	t.Run("StripConst peels the outermost const", func(t *testing.T) {
		assert.Equal(t, TypeInt, StripConst(constOf(TypeInt)))
		assert.Equal(t, TypeInt, StripConst(TypeInt),
			"a type without an outer const is passed through unchanged")
		stripped := StripConst(ptrOf(constOf(TypeInt)))
		assert.Equal(t, "int$*", stripped.String(),
			"only the outermost wrapper is seen through; nested const stays")
	})
}

// Const is covariant at the one level the subtype rule sees: const T stands
// where T does, since reading it as its base is a promise the compiler keeps.
// The reverse is not a subtype — a non-const slot the caller might still
// write to is not a const value, and a parameter asking for one has to be
// given one. Nested const stays strict: const int* is not a subtype of int*,
// since what one pointer sees and the other does differ.
func TestConstSubtyping(t *testing.T) {
	t.Parallel()

	constOf := func(t Type) *TypeWrapped {
		return NewWrappedType(TypeKindConst, t)
	}
	ptrOf := func(t Type) *TypeWrapped {
		return NewWrappedType(TypeKindPointer, t)
	}

	t.Run("const T is a subtype of T", func(t *testing.T) {
		assert.True(t, constOf(TypeInt).IsSubtypeOf(TypeInt))
	})

	t.Run("T is not a subtype of const T", func(t *testing.T) {
		assert.False(t, TypeInt.IsSubtypeOf(constOf(TypeInt)),
			"const is the slot the compiler watches, and only the slot can promise it")
	})

	t.Run("const T is a subtype of const T", func(t *testing.T) {
		assert.True(t, constOf(TypeInt).IsSubtypeOf(constOf(TypeInt)))
	})

	t.Run("nested const stays strict", func(t *testing.T) {
		assert.False(t, ptrOf(constOf(TypeInt)).IsSubtypeOf(ptrOf(TypeInt)),
			"const int* and int* see different things through the pointer")
	})
}
