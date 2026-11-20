package codegen

import (
	"strings"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// qualifiedName names a declaration by the declarations it lies in, outermost
// first.
func qualifiedName(d syntax.Decl) string {
	var names []string
	for ; d != nil; d = d.OuterDecl() {
		names = append(names, d.Name())
	}
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return strings.Join(names, ".")
}

// thisVariableName is the name bound to an instance method's receiver. It is a
// keyword, so no declared variable can shadow it.
const thisVariableName = "this"

// selection is the object a field selection is made out of and the field's
// name, for either way a selection is written.
func selection(e syntax.Expr) (syntax.Expr, string, bool) {
	switch s := e.(type) {
	case *syntax.ExprField:
		return s.Expr, s.Field, true
	case *syntax.ExprFields:
		return s.Receiver(), s.Field(), true
	}
	return nil, "", false
}

// receiverType is what `this` is bound as: a pointer to the declaration,
// whatever that declaration's own width is. It is a pointer because what the
// closure holds is an address, either of the caller's own instance or of the
// copy the closure took of a homeless one, and one compiled body serves both.
func receiverType(d syntax.Decl) syntax.Type {
	return syntax.NewWrappedType(syntax.TypeKindPointer, d.(syntax.DeclTypeable).DeclType())
}

// spreadInner returns the inner expression of a spread, or nil when e is not
// a spread. The static pass has already said the spread stands alone and that
// its inner is a pointer or array of the right element type, so what is left
// at codegen is handing a pointer to the callee rather than packing a run.
func spreadInner(e syntax.Expr) syntax.Expr {
	nary, ok := e.(*syntax.ExprNAry)
	if !ok || nary.Kind() != syntax.ExprKindSpread {
		return nil
	}
	return nary.Exprs[0]
}

// packs reports whether the arguments feeding a variadic are packed into a
// fresh run, which keeps them beneath the call's frame until it returns.
func packs(packed []syntax.Expr) bool {
	if len(packed) == 1 && spreadInner(packed[0]) != nil {
		return false
	}
	return len(packed) != 0
}

// isNativeCallee reports whether a call's callee is an [syntax.ExprNative], seen
// through any signature casts the source put over it. The native expression is
// always written and immediately cast — nothing else types as a function
// address — so a native stored in a variable and called later is not what this
// detects, nor is it a shape the compiler supports.
func isNativeCallee(e syntax.Expr) bool {
	for {
		switch n := e.(type) {
		case *syntax.ExprCast:
			e = n.Expr
		case *syntax.ExprNative:
			return true
		default:
			return false
		}
	}
}

// virtuallyTyped reports whether a value's type is only what it was written as,
// with the value being some declaration below it in reality. An abstract or an
// interface always is: it is a reference to one of its implementors. A class is
// exactly what it says when it is laid out inline, since there is no room in it
// for anything more, but a pointer to one reaches a subclass: a Foo* that holds
// a Bar is a Bar*.
func virtuallyTyped(t syntax.Type) bool {
	var pointer bool
	for {
		wrapped, ok := t.(*syntax.TypeWrapped)
		if !ok || wrapped.Kind() != syntax.TypeKindPointer {
			break
		}
		pointer = true
		t = wrapped.Type
	}

	resolvable, ok := t.(syntax.TypeResolvable)
	if !ok {
		return false
	}
	switch resolvable.Decl().Kind() {
	case syntax.KindAbstract, syntax.KindInterface:
		return true
	case syntax.KindClass:
		return pointer
	}
	return false
}

// isStringBytesPointer reports whether t is a pointer the string cast resolves
// to the string's bytes field: byte* or char*. A void* goes through the plain
// word cast instead, promising nothing of what lies at the address — including
// that it is the bytes rather than the mark word in front of them.
func isStringBytesPointer(t syntax.Type) bool {
	w, ok := syntax.StripConst(t).(*syntax.TypeWrapped)
	if !ok || w.Kind() != syntax.TypeKindPointer {
		return false
	}
	k := syntax.StripConst(w.Type).Kind()
	return k == syntax.TypeKindByte || k == syntax.TypeKindChar
}

// stringBytesOffset is how far past the string pointer its bytes begin: the
// mark word and the length word. It matches ZamString's layout in
// internal/5_build/runtime/src/instance.h and is what [emitStringBytesCast] shifts a string by.
const stringBytesOffset = 2 * wordByteSize

// isDowncast reports whether a cast claims what a value is rather than saying
// what to do to it. Reading a value as its own type, or as one above it, holds
// for every value of the source type, so there is nothing to settle; reading it
// as one below holds only for those values that are of that type. A void pointer
// claims nothing about what it addresses, so a cast to or from one is the word
// read as the other type and no claim at all.
func isDowncast(from, to syntax.Type) bool {
	from, to = syntax.StripConst(from), syntax.StripConst(to)
	if isVoidPointer(from) || isVoidPointer(to) {
		return false
	}
	return !from.IsSubtypeOf(to) && to.IsSubtypeOf(from)
}

// isVoidPointer reports whether t is the void* type, which is any address at
// all: it says nothing of what lies at it.
func isVoidPointer(t syntax.Type) bool {
	w, ok := syntax.StripConst(t).(*syntax.TypeWrapped)
	return ok && w.Kind() == syntax.TypeKindPointer && w.Type.Kind() == syntax.TypeKindVoid
}

// typeIDWords is how many operand words a runtime type id occupies, which is
// what [typeDefinitionLong.packed] spells out.
const typeIDWords = 3
