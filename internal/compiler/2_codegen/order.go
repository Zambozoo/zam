package codegen

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

// canonicalOrder reports whether the lists type ids are numbered by are put in
// one canonical order rather than the order the passes before codegen happened
// to fill them in, so that two compilers can be held to the same image.
//
// TODO: remove ZAM_CANONICAL_ORDER, and the ordering it switches on, once the Go
// compiler is dropped.
func canonicalOrder() bool {
	return os.Getenv("ZAM_CANONICAL_ORDER") != ""
}

// orderedTypes answers the types of set, in canonical order when it is asked for.
func orderedTypes(set *syntax.TypeSet) []syntax.Type {
	types := slices.Collect(set.Values())
	if canonicalOrder() {
		slices.SortStableFunc(types, func(a, b syntax.Type) int {
			return strings.Compare(canonicalKey(a), canonicalKey(b))
		})
	}
	return types
}

// orderedConcretes answers the instantiations of d, in canonical order when it is
// asked for.
func orderedConcretes(d syntax.DeclGeneric) []base.MapEntry[syntax.Type, syntax.DeclTypeable] {
	var concretes []base.MapEntry[syntax.Type, syntax.DeclTypeable]
	for _, entry := range d.Concretes().All() {
		concretes = append(concretes, entry)
	}
	if canonicalOrder() {
		slices.SortStableFunc(concretes, func(a, b base.MapEntry[syntax.Type, syntax.DeclTypeable]) int {
			return strings.Compare(canonicalKey(a.Key), canonicalKey(b.Key))
		})
	}
	return concretes
}

// orderedMethodConcretes answers the instantiations of f, in canonical order when
// it is asked for.
func orderedMethodConcretes(f *syntax.FieldMethod) []*syntax.MethodConcrete {
	concretes := slices.Clone(f.Concretes())
	if canonicalOrder() {
		slices.SortStableFunc(concretes, func(a, b *syntax.MethodConcrete) int {
			return strings.Compare(canonicalKeys(a.TypeArgs()), canonicalKeys(b.TypeArgs()))
		})
	}
	return concretes
}

// canonicalKey writes t with every declaration it names as the file it is written
// in and the declarations it is nested in, which is the same however the type
// happened to be written.
func canonicalKey(t syntax.Type) string {
	switch t := t.(type) {
	case *syntax.TypeReference:
		return "{" + canonicalDecl(t.Decl()) + "}"
	case *syntax.TypeGeneric:
		return canonicalKey(t.Type) + "[" + canonicalKeys(t.ParameterTypes) + "]"
	case *syntax.TypeTailed:
		if t.Size == nil {
			return canonicalKey(t.Type) + "~"
		}
		return canonicalKey(t.Type) + "~" + strconv.FormatUint(*t.Size, 10)
	case *syntax.TypeArray:
		if t.Size == nil {
			return canonicalKey(t.Type) + "[~]"
		}
		return canonicalKey(t.Type) + "[" + strconv.FormatUint(*t.Size, 10) + "]"
	case *syntax.TypeTuple:
		return "(" + canonicalKeys(t.Types) + ")"
	case *syntax.TypeFunction:
		return canonicalKey(t.ReturnType) + "(" + canonicalKeys(t.ParameterTypes) + ")"
	case *syntax.TypeWrapped:
		switch t.Kind() {
		case syntax.TypeKindPointer:
			return canonicalKey(t.Type) + "*"
		case syntax.TypeKindThread:
			return "thread[" + canonicalKey(t.Type) + "]"
		case syntax.TypeKindVariadic:
			return canonicalKey(t.Type) + "..."
		}
		return canonicalKey(t.Type) + "$"
	}
	return t.String()
}

func canonicalKeys(ts []syntax.Type) string {
	keys := make([]string, len(ts))
	for i, t := range ts {
		keys[i] = canonicalKey(t)
	}
	return strings.Join(keys, ", ")
}

// canonicalDecl names d by the file it is written in and the declarations it is
// nested in.
func canonicalDecl(d syntax.Decl) string {
	name := d.Name()
	for outer := d.OuterDecl(); outer != nil; outer = outer.OuterDecl() {
		name = outer.Name() + "." + name
	}
	return d.File().Path.String() + "|" + name
}
