package codegen

import (
	"cmp"
	"encoding/binary"
	"io"
	"slices"
	"sort"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// noParent is the parent of a function no other function encloses.
const noParent = ^uint64(0)

// debugFunctionWords is how many words a function takes in the image: its
// start, end, parent, name and where its line program begins.
const debugFunctionWords = 5

type (
	// debugInfo says where each instruction came from: the function body it
	// lies in, and the line of source it was emitted for. The functions are
	// built as the operations are emitted, which is in the order of their
	// offsets, so they come out sorted by where they begin without being sorted.
	debugInfo struct {
		// strings are the file paths and function names the tables name by
		// index.
		strings *base.StaticOrderedMap[string, uint64]
		// functions are the function bodies, in the order they begin. Bodies
		// nest, a lambda's lying inside the body it was written in, so each
		// names the one enclosing it.
		functions []debugFunction
		// open are the functions whose bodies are being emitted, innermost
		// last.
		open []openFunction
	}

	// debugFunction is one function body: the instructions from start up to
	// end, the function enclosing it, its name, and where the line its own
	// instructions were emitted for changes. A body nested inside it has lines
	// of its own and is not among these.
	debugFunction struct {
		start, end, parent, name uint64
		lines                    []debugLine
		locals                   []debugLocal
		// template is the type id of the generic class or abstract whose
		// instantiations share the body, learning their type arguments from
		// the receiver, or 0.
		template uint16
	}

	// debugLocal is a variable of a function body: its name and type, the
	// slot of the frame it lies in, and the instructions from start up to end
	// it holds its value over. An open variable lies where an instantiation
	// decides, or is of a type one does, and openSlots are the slots ahead of
	// it whose widths one decides. A receiver is the call's `this`, read
	// through the call rather than out of the slot.
	debugLocal struct {
		name       string
		typ        syntax.Type
		slot       uint64
		openSlots  []syntax.Type
		open       bool
		receiver   bool
		start, end uint64
	}

	// localRef names a variable among a function body's.
	localRef struct {
		function, local int
	}

	// openFunction is a function whose body is being emitted: its index among
	// the functions, and its name.
	openFunction struct {
		index int
		name  string
	}

	// debugLine says the instructions from start on were emitted for line of
	// file, until the next entry.
	debugLine struct {
		start      uint64
		file, line uint32
	}

	// debugPosition is a line of a file, which is what an instruction is
	// emitted for.
	debugPosition struct {
		file, line uint32
	}
)

func newDebugInfo() *debugInfo {
	return &debugInfo{strings: base.NewStaticOrderedMap[string, uint64]()}
}

// intern returns the index s is named by.
func (d *debugInfo) intern(s string) uint64 {
	if i, ok := d.strings.Get(s); ok >= 0 {
		return i
	}
	i := uint64(d.strings.Len())
	d.strings.Set(s, i)
	return i
}

// at says the instructions from offset on, in the body being emitted, are
// emitted for position. An entry already starting at offset is replaced,
// having had no instruction to name.
func (d *debugInfo) at(offset int, position debugPosition) {
	f := d.emitting()
	if f == nil {
		return
	}
	entry := debugLine{start: uint64(offset), file: position.file, line: position.line}
	if n := len(f.lines); n > 0 {
		last := &f.lines[n-1]
		switch {
		case last.start == entry.start:
			*last = entry
			if n > 1 && f.lines[n-2].file == entry.file && f.lines[n-2].line == entry.line {
				f.lines = f.lines[:n-1]
			}
			return
		case last.file == entry.file && last.line == entry.line:
			return
		}
	}
	f.lines = append(f.lines, entry)
}

// emitting returns the function whose body is being emitted, or nil.
func (d *debugInfo) emitting() *debugFunction {
	if len(d.open) == 0 {
		return nil
	}
	return &d.functions[d.open[len(d.open)-1].index]
}

// position returns the position of tok in file.
func (d *debugInfo) position(file *syntax.File, tok *syntax.Token) (debugPosition, bool) {
	if file == nil || tok == nil {
		return debugPosition{}, false
	}
	location := file.Location(tok)
	if location.Position == nil {
		return debugPosition{}, false
	}
	return debugPosition{file: uint32(d.intern(string(file.Path))), line: uint32(location.Position.Line)}, true
}

// current returns the position the instructions emitted last in the body
// being emitted were emitted for.
func (d *debugInfo) current() (debugPosition, bool) {
	f := d.emitting()
	if f == nil || len(f.lines) == 0 {
		return debugPosition{}, false
	}
	last := f.lines[len(f.lines)-1]
	return debugPosition{file: last.file, line: last.line}, true
}

// enter begins the body of the function called name at offset.
func (d *debugInfo) enter(offset int, name string) {
	parent := noParent
	var template uint16
	if n := len(d.open); n > 0 {
		parent = uint64(d.open[n-1].index)
		template = d.functions[parent].template
	}
	d.open = append(d.open, openFunction{index: len(d.functions), name: name})
	d.functions = append(d.functions, debugFunction{
		start: uint64(offset), parent: parent, name: d.intern(name), template: template,
	})
}

// template says the body being emitted is shared by the instantiations of the
// generic declaration whose type id is id.
func (d *debugInfo) template(id uint16) {
	if f := d.emitting(); f != nil {
		f.template = id
	}
}

// exit ends the body begun last at offset, and with it any of its variables
// still holding their values.
func (d *debugInfo) exit(offset int) {
	n := len(d.open)
	f := &d.functions[d.open[n-1].index]
	f.end = uint64(offset)
	for i := range f.locals {
		if f.locals[i].end == 0 {
			f.locals[i].end = f.end
		}
	}
	d.open = d.open[:n-1]
}

// declare records local as declared at offset in the body being emitted.
func (d *debugInfo) declare(offset int, local debugLocal) (localRef, bool) {
	if len(d.open) == 0 {
		return localRef{}, false
	}
	index := d.open[len(d.open)-1].index
	local.start = uint64(offset)
	d.functions[index].locals = append(d.functions[index].locals, local)
	return localRef{function: index, local: len(d.functions[index].locals) - 1}, true
}

// begin says the variable ref names holds its value from offset on.
func (d *debugInfo) begin(ref localRef, offset int) {
	d.functions[ref.function].locals[ref.local].start = uint64(offset)
}

// end says the variables refs name hold their values no further than offset.
func (d *debugInfo) end(refs []localRef, offset int) {
	for _, ref := range refs {
		if l := &d.functions[ref.function].locals[ref.local]; l.end == 0 {
			l.end = uint64(offset)
		}
	}
}

// innermost returns the name of the function whose body is being emitted.
func (d *debugInfo) innermost() string {
	if len(d.open) == 0 {
		return ""
	}
	return d.open[len(d.open)-1].name
}

// lineProgram encodes the lines of f, each by how far it lies from the one
// before it, which is what keeps them small: a run of instructions is rarely
// long, and the next line is rarely far. It opens with how many lines there
// are. Each is then the distance from the start before it, shifted up a bit
// whose being set says the file changes, then the distance from the line
// before it, then the new file if it changed. The first is measured from
// where the function starts and from line zero, and always names its file.
func lineProgram(f debugFunction) []byte {
	program := binary.AppendUvarint(nil, uint64(len(f.lines)))
	start, line, file := f.start, int64(0), uint32(0)
	for i, l := range f.lines {
		changed := i == 0 || l.file != file
		step := (l.start - start) << 1
		if changed {
			step |= 1
		}
		program = binary.AppendUvarint(program, step)
		program = binary.AppendVarint(program, int64(l.line)-line)
		if changed {
			program = binary.AppendUvarint(program, uint64(l.file))
		}
		start, line, file = l.start, int64(l.line), l.file
	}
	return program
}

// programs answers every function's line program run together, and where
// each begins in it.
func (d *debugInfo) programs() ([]byte, []uint64) {
	var programs []byte
	offsets := make([]uint64, len(d.functions))
	for i, f := range d.functions {
		offsets[i] = uint64(len(programs))
		programs = append(programs, lineProgram(f)...)
	}
	return programs, offsets
}

// ByteCount implements the [byteCountable] interface. A nil debugInfo is the
// section of an image that says nothing of where its instructions came from.
func (d *debugInfo) ByteCount() int {
	if d == nil {
		d = newDebugInfo()
	}
	programs, _ := d.programs()
	return countedByteCount(d.stringLiterals()) +
		wordByteSize + len(d.functions)*debugFunctionWords*wordByteSize +
		wordByteSize + int(alignUp(uint64(len(programs)), wordByteSize))
}

// Read implements the [byteCountable] interface: the strings, then the
// functions as their start, end, parent, name and the offset of their line
// program, then how many bytes the line programs take and the programs
// themselves, padded out to a word.
func (d *debugInfo) Read(p []byte) (int, error) {
	if d == nil {
		d = newDebugInfo()
	}
	total, err := readCounted(p, d.stringLiterals())
	if err != nil {
		return 0, err
	}

	programs, offsets := d.programs()
	words := []uint64{uint64(len(d.functions))}
	for i, f := range d.functions {
		words = append(words, f.start, f.end, f.parent, f.name, offsets[i])
	}
	words = append(words, uint64(len(programs)))
	n, err := readAllWords(p[total:], words...)
	if err != nil {
		return 0, err
	}
	total += n

	padded := int(alignUp(uint64(len(programs)), wordByteSize))
	if len(p)-total < padded {
		return 0, io.ErrShortBuffer
	}
	copy(p[total:], programs)
	clear(p[total+len(programs) : total+padded])
	return total + padded, nil
}

func (d *debugInfo) stringLiterals() []stringLiteral {
	literals := make([]stringLiteral, 0, d.strings.Len())
	for _, entry := range d.strings.All() {
		literals = append(literals, stringLiteral(entry.Key))
	}
	return literals
}

// DebugTable says which line of source and which function each instruction of
// an image came from, and where the instructions of each line begin.
type DebugTable struct {
	strings   []string
	functions []debugFunction
	types     *valueTypes
	classes   map[uint32]*ValueType
	instances map[uint32]syntax.DeclConcrete
	statics   []Static
	// Main is where the body of the main method begins.
	Main uint64
}

// Local is a variable a frame holds.
type Local struct {
	Name string
	// Slot is the word of the frame it begins at.
	Slot uint64
	Type *ValueType
	// Receiver is `this`, which lies where the call says rather than in the
	// slot.
	Receiver bool
	// Open is a variable whose place or type an instantiation decides, and
	// that none has.
	Open bool
}

// table answers the tables d has recorded, with main the name of the main
// method, classes the value types of the classes by their short type ids, and
// instances the instantiations of generic declarations by theirs.
func (d *debugInfo) table(main string, types *valueTypes, classes map[uint32]*ValueType,
	instances map[uint32]syntax.DeclConcrete) *DebugTable {
	t := &DebugTable{functions: d.functions, types: types, classes: classes, instances: instances}
	for _, entry := range d.strings.All() {
		t.strings = append(t.strings, entry.Key)
	}
	if i := slices.IndexFunc(t.functions, func(f debugFunction) bool { return t.strings[f.name] == main }); i >= 0 {
		t.Main = t.functions[i].start
	}
	return t
}

// innermost answers the innermost function whose body holds offset.
func (t *DebugTable) innermost(offset uint64) (debugFunction, bool) {
	i := sort.Search(len(t.functions), func(i int) bool { return t.functions[i].start > offset }) - 1
	for i >= 0 {
		if f := t.functions[i]; offset < f.end {
			return f, true
		}
		i = int(t.functions[i].parent)
	}
	return debugFunction{}, false
}

// Locate answers the file and line the instruction at offset was emitted for.
func (t *DebugTable) Locate(offset uint64) (string, int, bool) {
	f, ok := t.innermost(offset)
	if !ok {
		return "", 0, false
	}
	i := sort.Search(len(f.lines), func(i int) bool { return f.lines[i].start > offset }) - 1
	if i < 0 {
		return "", 0, false
	}
	return t.strings[f.lines[i].file], int(f.lines[i].line), true
}

// Locals answers the variables holding their values at the instruction at
// offset, in the order they lie in the frame. A variable whose place or type
// an instantiation decides, and none has, is of an opaque type.
//
// A body that instantiations of a generic declaration share is told which by
// instance, the short type id of the receiver's instantiation of the
// declaration [DebugTable.Template] names, which grounds those variables. An
// instance of 0 grounds nothing.
func (t *DebugTable) Locals(offset uint64, instance uint32) []Local {
	f, ok := t.innermost(offset)
	if !ok {
		return nil
	}
	concrete, grounded := t.instances[instance]
	var locals []Local
	for _, l := range f.locals {
		if l.start > offset || offset >= l.end {
			continue
		}
		local := Local{Name: l.name, Slot: l.slot, Type: &ValueType{Name: l.typ.String()}, Receiver: l.receiver, Open: l.open}
		typ := l.typ
		if l.open && grounded {
			typ = concrete.MapType(typ)
			for _, open := range l.openSlots {
				local.Slot += stackWords(concrete.MapType(open))
			}
			local.Open = typ.ContainsGenericArg()
		}
		if !local.Open {
			local.Type = t.types.of(typ)
		}
		locals = append(locals, local)
	}
	slices.SortStableFunc(locals, func(a, b Local) int { return cmp.Compare(a.Slot, b.Slot) })
	return locals
}

// Statics answers the program's static members, by declaration and then
// name.
func (t *DebugTable) Statics() []Static {
	return t.statics
}

// Template answers the type id of the generic declaration whose
// instantiations share the body holding the instruction at offset, if they do.
func (t *DebugTable) Template(offset uint64) (uint16, bool) {
	f, ok := t.innermost(offset)
	return f.template, ok && f.template != 0
}

// Class answers the value type of the class whose short type id is id, which
// is what the header of an instance of it names.
func (t *DebugTable) Class(id uint32) (*ValueType, bool) {
	vt, ok := t.classes[id]
	return vt, ok
}

// Function answers the name of the innermost function whose body holds the
// instruction at offset.
func (t *DebugTable) Function(offset uint64) (string, bool) {
	f, ok := t.innermost(offset)
	if !ok {
		return "", false
	}
	return t.strings[f.name], true
}

// Breakpoints answers where the instructions emitted for line of file begin.
// A line with none is moved to the first line after it that has some, and the
// line answered is the one moved to. A line is begun again where a body
// written inside it ends, which is the rest of a line already begun and not
// where it begins.
func (t *DebugTable) Breakpoints(file string, line int) ([]uint64, int) {
	fileIndex := slices.Index(t.strings, file)
	if fileIndex < 0 {
		return nil, line
	}
	resumed := map[uint64]bool{}
	for _, f := range t.functions {
		if f.parent != noParent {
			resumed[f.end] = true
		}
	}

	var offsets []uint64
	found := 0
	for _, f := range t.functions {
		for _, l := range f.lines {
			if l.file != uint32(fileIndex) || int(l.line) < line || resumed[l.start] {
				continue
			}
			switch {
			case found == 0 || int(l.line) < found:
				found, offsets = int(l.line), []uint64{l.start}
			case int(l.line) == found:
				offsets = append(offsets, l.start)
			}
		}
	}
	if found == 0 {
		return nil, line
	}
	slices.Sort(offsets)
	return offsets, found
}

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
