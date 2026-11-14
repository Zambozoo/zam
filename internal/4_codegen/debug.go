package codegen

import (
	"encoding/binary"
	"io"
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
	if n := len(d.open); n > 0 {
		parent = uint64(d.open[n-1].index)
	}
	d.open = append(d.open, openFunction{index: len(d.functions), name: name})
	d.functions = append(d.functions, debugFunction{start: uint64(offset), parent: parent, name: d.intern(name)})
}

// exit ends the body begun last at offset.
func (d *debugInfo) exit(offset int) {
	n := len(d.open)
	d.functions[d.open[n-1].index].end = uint64(offset)
	d.open = d.open[:n-1]
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
