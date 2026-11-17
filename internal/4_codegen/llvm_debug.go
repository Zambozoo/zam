package codegen

import (
	"debug/dwarf"
	"maps"
	"slices"
	"strconv"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
	"github.com/zambozoo/zam/internal/llvm"
)

// dwarfLanguageC99 is the language the compile unit claims, which is what
// debuggers read plain words and the base types by. It is C99's place in
// LLVM's own enumeration of languages, which is not DWARF's numbering.
const dwarfLanguageC99 = llvm.DwarfLang(11)

// dwarfOpPlusUconst is DW_OP_plus_uconst, which offsets a location by its
// operand.
const dwarfOpPlusUconst = 0x23

// dwarfVersion is the DWARF version the module is emitted at.
const dwarfVersion = 4

// llvmDebug is the DWARF a module describes its program with: one compile
// unit, a file for each source, a subprogram for each compiled function, and
// a type for each type a local is declared as.
type llvmDebug struct {
	ctx   llvm.Context
	mod   llvm.Module
	di    *llvm.DIBuilder
	unit  llvm.Metadata
	files map[string]llvm.Metadata
	types map[string]llvm.Metadata
	word  llvm.Metadata
	ref   llvm.Metadata

	// shortOf is the short type id of a type, and names the name of the
	// DWARF type of each short id described, for a debugger to find the
	// type of what an any holds by.
	shortOf func(syntax.Type) (uint64, bool)
	names   map[uint64]typeName
}

// newLLVMDebug starts the DWARF of mod, whose program begins in the file at
// path.
func newLLVMDebug(ctx llvm.Context, mod llvm.Module, path string) *llvmDebug {
	d := &llvmDebug{
		ctx:   ctx,
		mod:   mod,
		di:    llvm.NewDIBuilder(mod),
		files: map[string]llvm.Metadata{},
		types: map[string]llvm.Metadata{},
		names: map[uint64]typeName{},
	}
	d.unit = d.di.CreateCompileUnit(llvm.DICompileUnit{Language: dwarfLanguageC99, File: path, Producer: "zam"})
	d.word = d.di.CreateBasicType(llvm.DIBasicType{Name: "word", SizeInBits: 64, Encoding: llvm.DW_ATE_unsigned})
	return d
}

// file is the DWARF file of the source at path.
func (d *llvmDebug) file(path string) llvm.Metadata {
	if f, ok := d.files[path]; ok {
		return f
	}
	f := d.di.CreateFile(path, "")
	d.files[path] = f
	return f
}

// function describes fn as the function called name, beginning at line of
// the source at path.
func (d *llvmDebug) function(fn llvm.Value, name, path string, line int) llvm.Metadata {
	file := d.file(path)
	sp := d.di.CreateFunction(file, llvm.DIFunction{
		Name:         name,
		LinkageName:  fn.Name(),
		File:         file,
		Line:         line,
		Type:         d.di.CreateSubroutineType(llvm.DISubroutineType{File: file}),
		IsDefinition: true,
		ScopeLine:    line,
	})
	fn.SetSubprogram(sp)
	return sp
}

// typeOf is the DWARF type of a value of t: a base type for a primitive, a
// struct for a class, struct or tuple laid out inline, a pointer to one of
// those for a pointer to one, and otherwise its words named after t, which is
// what a debugger's formatter for t matches.
func (d *llvmDebug) typeOf(t syntax.Type) llvm.Metadata {
	key := t.String()
	if md, ok := d.types[key]; ok {
		return md
	}
	if d.shortOf != nil {
		if short, ok := d.shortOf(t); ok {
			header := uint64(0)
			if decl, ok := instanceDecl(t); ok {
				header = instanceHeaderByteSizeOf(syntax.KindClass, isTailed(decl))
			}
			d.names[short] = typeName{name: key, header: header}
		}
	}
	var md llvm.Metadata
	base := func(encoding llvm.DwarfTypeEncoding, bits uint64) llvm.Metadata {
		return d.di.CreateBasicType(llvm.DIBasicType{Name: key, SizeInBits: bits, Encoding: encoding})
	}
	switch kind, err := primitiveKind(t); {
	case err == nil && kind == syntax.TypeKindInt:
		md = base(llvm.DW_ATE_signed, 64)
	case err == nil && kind == syntax.TypeKindUint:
		md = base(llvm.DW_ATE_unsigned, 64)
	case err == nil && kind == syntax.TypeKindBool:
		md = base(llvm.DW_ATE_boolean, 8)
	case err == nil && kind == syntax.TypeKindFloat:
		md = base(llvm.DW_ATE_float, 64)
	case err == nil && kind == syntax.TypeKindChar:
		md = base(llvm.DW_ATE_UTF, 32)
	case err == nil && kind == syntax.TypeKindByte:
		md = base(llvm.DW_ATE_unsigned_char, 8)
	case d.laidOut(t):
		return d.structOf(t, key)
	case d.isEnum(t):
		md = d.enumeration(syntax.StripConst(t).(syntax.TypeResolvable).Decl().(*syntax.DeclEnum), key)
	case d.pointsAtLayout(t):
		pointee := syntax.StripConst(t).(*syntax.TypeWrapped).Type
		md = d.di.CreatePointerType(llvm.DIPointerType{Pointee: d.typeOf(pointee), SizeInBits: 64, AlignInBits: 64})
	case d.isReference(t):
		md = d.di.CreateTypedef(llvm.DITypedef{Type: d.reference(), Name: key})
	default:
		words := stackWords(t)
		inner := d.word
		if words > 1 {
			inner = d.di.CreateArrayType(llvm.DIArrayType{
				SizeInBits:  64 * words,
				AlignInBits: 64,
				ElementType: d.word,
				Subscripts:  []llvm.DISubrange{{Count: int64(words)}},
			})
		}
		md = d.di.CreateTypedef(llvm.DITypedef{Type: inner, Name: key})
	}
	d.types[key] = md
	return md
}

// finish completes the DWARF and marks the module as carrying it.
func (d *llvmDebug) finish() {
	d.emitTypeNames()
	d.di.Finalize()
	i32 := d.ctx.Int32Type()
	d.mod.AddModuleFlag(llvm.ModuleFlagBehaviorWarning, "Debug Info Version",
		llvm.ValueAsMetadata(llvm.ConstInt(i32, uint64(llvm.DebugMetadataVersion()), false)))
	d.mod.AddModuleFlag(llvm.ModuleFlagBehaviorWarning, "Dwarf Version",
		llvm.ValueAsMetadata(llvm.ConstInt(i32, dwarfVersion, false)))
	d.di.Destroy()
}

// laidOut reports whether a value of t has members DWARF can describe where
// they lie: a class or struct instance, or a tuple.
func (d *llvmDebug) laidOut(t syntax.Type) bool {
	if _, ok := syntax.StripConst(t).(*syntax.TypeTuple); ok {
		return true
	}
	_, _, ok := layoutDecl(t)
	return ok
}

// pointsAtLayout reports whether t is a pointer to something DWARF describes:
// a primitive, or what laidOut answers for.
func (d *llvmDebug) pointsAtLayout(t syntax.Type) bool {
	w, ok := syntax.StripConst(t).(*syntax.TypeWrapped)
	if !ok || w.Kind() != syntax.TypeKindPointer {
		return false
	}
	_, err := primitiveKind(w.Type)
	return err == nil || d.laidOut(w.Type)
}

// structOf is the DWARF struct of the inline value of t named key, built
// against a placeholder so that a member pointing back at it finds one.
func (d *llvmDebug) structOf(t syntax.Type, key string) llvm.Metadata {
	size := byteSize(t)
	decl, kind, isDecl := layoutDecl(t)
	header := uint64(0)
	if isDecl {
		header = instanceHeaderByteSizeOf(kind, isTailed(decl))
		size, _, _ = layoutOf(decl)
	}
	placeholder := d.di.CreateReplaceableCompositeType(d.unit, llvm.DIReplaceableCompositeType{
		Tag: dwarf.TagStructType, Name: key, SizeInBits: size * 8, AlignInBits: 64,
	})
	d.types[key] = placeholder

	var members []llvm.Metadata
	member := func(name string, offset uint64, typ syntax.Type) {
		width, _ := fieldByteInfo(typ)
		members = append(members, d.di.CreateMemberType(placeholder, llvm.DIMemberType{
			Name: name, SizeInBits: width * 8, OffsetInBits: offset * 8, Type: d.typeOf(typ),
		}))
	}
	if tuple, ok := syntax.StripConst(t).(*syntax.TypeTuple); ok {
		_, _, offsets := tupleLayout(tuple)
		for i, element := range tuple.Types {
			member(strconv.Itoa(i), offsets[i], element)
		}
	} else {
		if enum, ok := decl.(*syntax.DeclEnum); ok {
			members = append(members, d.di.CreateMemberType(placeholder, llvm.DIMemberType{
				Name: "value", SizeInBits: 64, Type: d.enumeration(enum, key+".value"),
			}))
		}
		for _, field := range instanceFields[*syntax.FieldMember](decl) {
			if isTail(field.FieldType()) {
				continue
			}
			member(field.Name(), header+uint64(*field.Offset()), field.FieldType())
		}
	}

	real := d.di.CreateStructType(d.unit, llvm.DIStructType{
		Name: key, SizeInBits: size * 8, AlignInBits: 64, Elements: members,
	})
	placeholder.ReplaceAllUsesWith(real)
	d.types[key] = real
	return real
}

// layoutDecl is the class, abstract, struct or enum with members an instance
// of t is laid out by, and which of those it is.
func layoutDecl(t syntax.Type) (syntax.Decl, syntax.FieldKind, bool) {
	if decl, ok := instanceDecl(t); ok {
		return decl, syntax.KindClass, true
	}
	if decl, ok := structDecl(t); ok {
		return decl, syntax.KindStruct, true
	}
	if decl, ok := memberEnum(t); ok {
		return decl, syntax.KindEnum, true
	}
	r, ok := syntax.StripConst(t).(syntax.TypeResolvable)
	if !ok || r.Decl() == nil {
		return nil, 0, false
	}
	switch decl := r.Decl().(type) {
	case *syntax.DeclAbstract, *syntax.DeclConcreteAbstract:
		return decl, syntax.KindAbstract, true
	}
	return nil, 0, false
}

// enumeration is the DWARF enumeration named key of enum's values by their
// discriminants.
func (d *llvmDebug) enumeration(enum *syntax.DeclEnum, key string) llvm.Metadata {
	var values []llvm.DIEnumerator
	for _, value := range enumValues(enum) {
		values = append(values, llvm.DIEnumerator{Name: value.Name(), Value: int64(enumDiscriminant(value))})
	}
	return d.di.CreateEnumerationType(d.unit, llvm.DIEnumerationType{
		Name: key, SizeInBits: 64, AlignInBits: 64, Elements: values, ClassType: d.word,
	})
}

// isEnum reports whether t is a value of an enum that is its discriminant
// alone, which DWARF names by its values.
func (d *llvmDebug) isEnum(t syntax.Type) bool {
	kind, err := valueKind(t)
	return err == nil && kind == kindEnum
}

// typeName is the name of a DWARF type, and how much of the front of a value
// of it an any's box leaves out: an inline class instance's header.
type typeName struct {
	name   string
	header uint64
}

// emitTypeNames lays out, for a debugger, the name of the DWARF type of each
// short type id described, ordered by id, with the header an any leaves out
// of a value of it.
func (d *llvmDebug) emitTypeNames() {
	ptr := llvm.PointerType(d.ctx.Int8Type(), 0)
	i64 := d.ctx.Int64Type()
	entryType := d.ctx.StructType([]llvm.Type{i64, ptr, i64}, false)
	shorts := slices.Sorted(maps.Keys(d.names))
	entries := make([]llvm.Value, len(shorts))
	for i, short := range shorts {
		name := d.names[short]
		text := llvm.AddGlobal(d.mod, llvm.ArrayType(d.ctx.Int8Type(), len(name.name)+1), "")
		text.SetInitializer(d.ctx.ConstString(name.name, true))
		text.SetGlobalConstant(true)
		text.SetLinkage(llvm.PrivateLinkage)
		entries[i] = llvm.ConstNamedStruct(entryType, []llvm.Value{
			llvm.ConstInt(i64, short, false), text, llvm.ConstInt(i64, name.header, false),
		})
	}
	table := llvm.AddGlobal(d.mod, llvm.ArrayType(entryType, len(entries)), typeNamesSymbol)
	table.SetInitializer(llvm.ConstArray(entryType, entries))
	table.SetGlobalConstant(true)
	table.SetLinkage(llvm.InternalLinkage)
	count := llvm.AddGlobal(d.mod, i64, typeNamesSymbol+"_count")
	count.SetInitializer(llvm.ConstInt(i64, uint64(len(entries)), false))
	count.SetGlobalConstant(true)
	count.SetLinkage(llvm.InternalLinkage)
}

// typeNamesSymbol is the symbol emitTypeNames lays its table out under, which
// zam's lldb formatters read.
const typeNamesSymbol = "zam_debug_type_names"

// isReference reports whether t is a reference zam's lldb formatters show by
// what it names: a str, an any, a function or a thread.
func (d *llvmDebug) isReference(t syntax.Type) bool {
	switch kind, err := valueKind(t); {
	case err != nil:
		return false
	case kind == syntax.TypeKindString, kind == syntax.TypeKindAny,
		kind == syntax.TypeKindFunction, kind == syntax.TypeKindThread:
		return true
	}
	return false
}

// reference is the struct of one word a reference is described as, so that a
// debugger shows only what its formatter says of it and not the word too.
func (d *llvmDebug) reference() llvm.Metadata {
	if d.ref.C == nil {
		placeholder := d.di.CreateReplaceableCompositeType(d.unit, llvm.DIReplaceableCompositeType{
			Tag: dwarf.TagStructType, Name: "zam_reference", SizeInBits: 64, AlignInBits: 64,
		})
		member := d.di.CreateMemberType(placeholder, llvm.DIMemberType{Name: "address", SizeInBits: 64, Type: d.word})
		d.ref = d.di.CreateStructType(d.unit, llvm.DIStructType{
			Name: "zam_reference", SizeInBits: 64, AlignInBits: 64, Elements: []llvm.Metadata{member},
		})
		placeholder.ReplaceAllUsesWith(d.ref)
	}
	return d.ref
}
