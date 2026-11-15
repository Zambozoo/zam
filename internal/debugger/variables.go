package debugger

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/go-dap"
	base "github.com/zambozoo/zam/internal/0_base"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
)

const (
	// wordBytes is how many bytes a word is.
	wordBytes = 8
	// stringHeadBytes is how far into a string its bytes begin: past its mark
	// word and its length.
	stringHeadBytes = 2 * wordBytes
	// objectHeadBytes is how far into an instance of a class its type word
	// lies, and how far its type word runs.
	objectHeadBytes = 2 * wordBytes
	// maxStringBytes is the most of a string shown.
	maxStringBytes = 1024
	// maxElements is the most elements of an array shown.
	maxElements = 100
	// thisName is the name of a method's receiver.
	thisName = "this"
	// nullText is how a reference to nothing is written.
	nullText = "null"
	// frameLocationBit marks an address as an offset into a thread's operands.
	frameLocationBit = 1 << 63
)

type (
	// variables are what a stop's variable references name, numbered from one.
	// They are given out as the editor expands values, and forgotten once the
	// program runs again.
	variables struct {
		frames     []frame
		containers []container
	}

	// container is something the editor can expand into variables, of the
	// thread it is read as.
	container interface {
		children(s *server, p *program) ([]child, error)
		thread() int
	}

	// child is a variable a container expands into, with the type of its value
	// and where that lies, which is 0 for a value that lies nowhere it can be
	// set.
	child struct {
		dap.Variable
		typ     *codegen.ValueType
		data    []byte
		address uint64
	}

	// frameLocals are the variables of a frame.
	frameLocals struct {
		frame frame
	}

	// globals are the program's own static members, read as thread reads
	// them.
	globals struct {
		on int
	}

	// inline is a value whose bytes are in hand, lying at address, expanding
	// into its members.
	inline struct {
		on      int
		typ     *codegen.ValueType
		data    []byte
		address uint64
	}

	// pointee is what a pointer points to, read once it is expanded.
	pointee struct {
		on      int
		typ     *codegen.ValueType
		address uint64
	}
)

func (l frameLocals) thread() int { return l.frame.thread }
func (g globals) thread() int     { return g.on }
func (i inline) thread() int      { return i.on }
func (e pointee) thread() int     { return e.on }

// slotAddress is where slot of a frame based at base lies, as an offset into
// the thread's operands marked as one.
func slotAddress(base, slot uint64) uint64 {
	return frameLocationBit | (base+slot)*wordBytes
}

// at answers address advanced by offset, or 0 for a value that lies nowhere.
func at(address, offset uint64) uint64 {
	if address == 0 {
		return 0
	}
	return address + offset
}

// frame records f, answering the id the editor names it by.
func (v *variables) frame(f frame) int {
	v.frames = append(v.frames, f)
	return len(v.frames)
}

// reference records c, answering the variables reference the editor names it
// by.
func (v *variables) reference(c container) int {
	v.containers = append(v.containers, c)
	return len(v.containers)
}

// scopes answers the scopes of the frame the editor names by id.
func (s *server) scopes(id int) ([]dap.Scope, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if id < 1 || id > len(s.variables.frames) {
		return nil, fmt.Errorf("no frame %d", id)
	}
	f := s.variables.frames[id-1]
	return []dap.Scope{
		{Name: "Locals", PresentationHint: "locals", VariablesReference: s.variables.reference(frameLocals{frame: f})},
		{Name: "Globals", PresentationHint: "globals", VariablesReference: s.variables.reference(globals{on: f.thread})},
	}, nil
}

func (g globals) children(s *server, p *program) ([]child, error) {
	base, err := p.statics()
	if err != nil {
		return nil, err
	}
	var children []child
	for _, static := range p.debug.Statics() {
		if static.Std {
			continue
		}
		name := static.Decl + "." + static.Name
		if static.Type.Kind == codegen.ValueKindOpaque {
			children = append(children, child{Variable: dap.Variable{Name: name, Value: "<unavailable>", Type: static.Type.Name}, typ: static.Type})
			continue
		}
		address := base + static.Offset
		data, err := p.memory(g.on, address, max(static.Type.Size, 1))
		if err != nil {
			children = append(children, unreadable(name, static.Type))
			continue
		}
		children = append(children, s.variable(p, g.on, name, static.Type, data, address))
	}
	return children, nil
}

// expand answers the variables of the container the editor names by
// reference.
func (s *server) expand(reference int) ([]dap.Variable, error) {
	p := s.running()
	if p == nil {
		return nil, errExited
	}
	s.lock.Lock()
	if reference < 1 || reference > len(s.variables.containers) {
		s.lock.Unlock()
		return nil, fmt.Errorf("no variables %d", reference)
	}
	c := s.variables.containers[reference-1]
	s.lock.Unlock()
	children, err := c.children(s, p)
	variables := make([]dap.Variable, 0, len(children))
	for _, c := range children {
		variables = append(variables, c.Variable)
	}
	return variables, err
}

func (l frameLocals) children(s *server, p *program) ([]child, error) {
	var children []child
	for _, local := range p.debug.Locals(l.frame.offset, l.instance(p)) {
		size := max(local.Type.Size, wordBytes)
		var data []byte
		var address uint64
		var err error
		if local.Receiver {
			var receiver uint64
			if receiver, err = p.receiver(l.frame); err == nil && receiver == 0 {
				err = errUnreadable
			}
			data = binary.LittleEndian.AppendUint64(nil, receiver)
		} else if local.Type.Kind != codegen.ValueKindOpaque {
			address = slotAddress(l.frame.base, local.Slot)
			data, err = p.words(l.frame.thread, l.frame.base+local.Slot, (size+wordBytes-1)/wordBytes)
		}
		if err != nil {
			children = append(children, unreadable(local.Name, local.Type))
			continue
		}
		children = append(children, s.variable(p, l.frame.thread, local.Name, local.Type, data, address))
	}
	return children, nil
}

// unreadable is a variable whose value cannot be read.
func unreadable(name string, typ *codegen.ValueType) child {
	return child{Variable: dap.Variable{Name: name, Value: "<unreadable>", Type: typ.Name}, typ: typ}
}

// instance answers the instantiation the frame's receiver is of the generic
// declaration whose instantiations share its body, which its variables are
// read as, or 0 if there is none or it cannot be read.
func (l frameLocals) instance(p *program) uint32 {
	template, ok := p.debug.Template(l.frame.offset)
	if !ok {
		return 0
	}
	locals := p.debug.Locals(l.frame.offset, 0)
	if !slices.ContainsFunc(locals, func(local codegen.Local) bool { return local.Open }) {
		return 0
	}
	i := slices.IndexFunc(locals, func(local codegen.Local) bool { return local.Name == thisName })
	if i < 0 {
		return 0
	}
	var receiver uint64
	if locals[i].Receiver {
		var err error
		if receiver, err = p.receiver(l.frame); err != nil {
			return 0
		}
	} else {
		pointer, err := p.words(l.frame.thread, l.frame.base+locals[i].Slot, 1)
		if err != nil || len(pointer) != wordBytes {
			return 0
		}
		receiver = binary.LittleEndian.Uint64(pointer)
	}
	instance, err := p.instance(l.frame.thread, receiver, template)
	if err != nil {
		return 0
	}
	return instance
}

func (i inline) children(s *server, p *program) ([]child, error) {
	return s.members(p, i.on, i.typ, i.data, i.address), nil
}

func (e pointee) children(s *server, p *program) ([]child, error) {
	typ := s.dynamic(p, e.on, e.typ, e.address)
	if typ.Kind == codegen.ValueKindOpaque || typ.Kind == codegen.ValueKindInterface {
		return nil, nil
	}
	data, err := p.memory(e.on, e.address, max(typ.Size, 1))
	if err != nil {
		return []child{unreadable("*", typ)}, nil
	}
	switch typ.Kind {
	case codegen.ValueKindClass, codegen.ValueKindStruct, codegen.ValueKindEnum,
		codegen.ValueKindTuple, codegen.ValueKindArray:
		return s.members(p, e.on, typ, data, e.address), nil
	}
	return []child{s.variable(p, e.on, "*", typ, data, e.address)}, nil
}

// dynamic answers the type of what lies at address: typ, unless it is a class
// or what an abstract or interface names, whose header says which class it is.
func (s *server) dynamic(p *program, thread int, typ *codegen.ValueType, address uint64) *codegen.ValueType {
	if typ.Kind != codegen.ValueKindClass && typ.Kind != codegen.ValueKindInterface {
		return typ
	}
	head, err := p.memory(thread, address, objectHeadBytes)
	if err != nil {
		return typ
	}
	if class, ok := p.debug.Class(uint32(binary.LittleEndian.Uint64(head[wordBytes:]))); ok {
		return class
	}
	return typ
}

// members answers the members of a value of typ whose bytes are data, and
// which lies at address.
func (s *server) members(p *program, thread int, typ *codegen.ValueType, data []byte, address uint64) []child {
	var children []child
	if typ.Kind == codegen.ValueKindArray {
		stride := typ.Elem.Size
		for i := range min(typ.Length, maxElements) {
			offset := i * stride
			if offset+stride > uint64(len(data)) {
				break
			}
			name := "[" + strconv.FormatUint(i, 10) + "]"
			children = append(children, s.variable(p, thread, name, typ.Elem, data[offset:offset+stride], at(address, offset)))
		}
		return children
	}
	for _, field := range typ.Fields {
		offset := typ.Header + field.Offset
		end := offset + field.Type.Size
		if end > uint64(len(data)) {
			children = append(children, unreadable(field.Name, field.Type))
			continue
		}
		children = append(children, s.variable(p, thread, field.Name, field.Type, data[offset:end], at(address, offset)))
	}
	return children
}

// variable renders a value of typ whose bytes are data, and which lies at
// address, as name.
func (s *server) variable(p *program, thread int, name string, typ *codegen.ValueType, data []byte, address uint64) child {
	v := dap.Variable{Name: name, Type: typ.Name}
	word := func() uint64 {
		var b [wordBytes]byte
		copy(b[:], data)
		return binary.LittleEndian.Uint64(b[:])
	}

	switch typ.Kind {
	case codegen.ValueKindBool:
		v.Value = strconv.FormatBool(len(data) > 0 && data[0] != 0)
	case codegen.ValueKindByte:
		v.Value = fmt.Sprintf("%d %q", data[0], rune(data[0]))
	case codegen.ValueKindChar:
		var b [4]byte
		copy(b[:], data)
		v.Value = strconv.QuoteRune(rune(binary.LittleEndian.Uint32(b[:])))
	case codegen.ValueKindInt:
		v.Value = strconv.FormatInt(int64(word()), 10)
	case codegen.ValueKindUint:
		v.Value = strconv.FormatUint(word(), 10)
	case codegen.ValueKindFloat:
		v.Value = strconv.FormatFloat(math.Float64frombits(word()), 'g', -1, 64)
	case codegen.ValueKindStr:
		v.Value = s.str(p, thread, word())
	case codegen.ValueKindPointer, codegen.ValueKindInterface:
		target := word()
		if target == 0 {
			v.Value = nullText
			break
		}
		v.Value = fmt.Sprintf("0x%x", target)
		elem := typ.Elem
		if typ.Kind == codegen.ValueKindInterface {
			elem = typ
		}
		if elem != nil && elem.Kind != codegen.ValueKindOpaque {
			v.VariablesReference = s.reference(pointee{on: thread, typ: elem, address: target})
		}
	case codegen.ValueKindArray:
		if typ.Length == 0 {
			v.Value = fmt.Sprintf("0x%x", word())
			break
		}
		v.Value = fmt.Sprintf("[%d]%s", typ.Length, typ.Elem.Name)
		v.VariablesReference = s.reference(inline{on: thread, typ: typ, data: data, address: address})
	case codegen.ValueKindClass, codegen.ValueKindStruct, codegen.ValueKindTuple:
		v.Value = typ.Name
		v.VariablesReference = s.reference(inline{on: thread, typ: typ, data: data, address: address})
	case codegen.ValueKindEnum:
		v.Value = typ.Name + "#" + strconv.FormatUint(word(), 10)
		if len(typ.Fields) > 0 {
			v.VariablesReference = s.reference(inline{on: thread, typ: typ, data: data, address: address})
		}
	case codegen.ValueKindPosit:
		v.Value = base.PositString(word())
	case codegen.ValueKindAny, codegen.ValueKindThread, codegen.ValueKindFunction, codegen.ValueKindType:
		v.Value = fmt.Sprintf("%s(0x%x)", typ.Name, word())
	default:
		v.Value = "<unavailable>"
	}
	return child{Variable: v, typ: typ, data: data, address: address}
}

// str renders the string at address.
func (s *server) str(p *program, thread int, address uint64) string {
	if address == 0 {
		return nullText
	}
	head, err := p.memory(thread, address, stringHeadBytes)
	if err != nil {
		return "<unreadable>"
	}
	length := binary.LittleEndian.Uint64(head[wordBytes:])
	shown := min(length, maxStringBytes)
	text := ""
	if shown > 0 {
		b, err := p.memory(thread, address+stringHeadBytes, shown)
		if err != nil {
			return "<unreadable>"
		}
		text = string(b)
	}
	quoted := strconv.Quote(text)
	if shown < length {
		quoted += "…"
	}
	return quoted
}

// reference records c among the stop's variables.
func (s *server) reference(c container) int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.variables.reference(c)
}

// set sets the variable called name among those the editor names by reference
// to the value text writes, answering it as it then is.
func (s *server) set(reference int, name, text string) (dap.Variable, error) {
	p := s.running()
	if p == nil {
		return dap.Variable{}, errExited
	}
	s.lock.Lock()
	if reference < 1 || reference > len(s.variables.containers) {
		s.lock.Unlock()
		return dap.Variable{}, fmt.Errorf("no variables %d", reference)
	}
	c := s.variables.containers[reference-1]
	s.lock.Unlock()

	children, err := c.children(s, p)
	if err != nil {
		return dap.Variable{}, err
	}
	i := slices.IndexFunc(children, func(c child) bool { return c.Name == name })
	if i < 0 {
		return dap.Variable{}, fmt.Errorf("no variable %s", name)
	}
	target := children[i]
	if target.address == 0 {
		return dap.Variable{}, fmt.Errorf("%s cannot be set", name)
	}
	if target.typ.Kind == codegen.ValueKindStr {
		return s.setStr(p, c.thread(), target, text)
	}
	data, err := encode(target.typ, text)
	if err != nil {
		return dap.Variable{}, err
	}
	// A slot is a whole word, whatever the value in it is narrower than.
	if target.address&frameLocationBit != 0 {
		data = append(data, make([]byte, wordBytes-len(data))...)
	}
	if err := p.write(c.thread(), target.address, data); err != nil {
		return dap.Variable{}, err
	}
	return s.variable(p, c.thread(), name, target.typ, data, target.address).Variable, nil
}

// setStr sets a string variable to the string text quotes, or to null.
func (s *server) setStr(p *program, thread int, target child, text string) (dap.Variable, error) {
	text = strings.TrimSpace(text)
	var pointer uint64
	if text != nullText {
		value, err := strconv.Unquote(text)
		if err != nil {
			return dap.Variable{}, fmt.Errorf("%s is not a quoted string or null", text)
		}
		if pointer, err = p.setString(thread, target.address, value); err != nil {
			return dap.Variable{}, err
		}
	} else if err := p.write(thread, target.address, make([]byte, wordBytes)); err != nil {
		return dap.Variable{}, err
	}
	data := binary.LittleEndian.AppendUint64(nil, pointer)
	return s.variable(p, thread, target.Name, target.typ, data, target.address).Variable, nil
}

// encode answers the bytes of the value of typ that text writes. A posit is
// written as a quadruple-precision float, rounded to the nearest posit, or as
// NaR.
func encode(typ *codegen.ValueType, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	var word uint64
	var err error
	switch typ.Kind {
	case codegen.ValueKindPosit:
		word, err = base.PositFromFloat128(text)
	case codegen.ValueKindInt:
		var v int64
		v, err = strconv.ParseInt(text, 0, 64)
		word = uint64(v)
	case codegen.ValueKindUint:
		word, err = strconv.ParseUint(text, 0, 64)
	case codegen.ValueKindFloat:
		var v float64
		v, err = strconv.ParseFloat(text, 64)
		word = math.Float64bits(v)
	case codegen.ValueKindBool:
		var v bool
		v, err = strconv.ParseBool(text)
		if v {
			word = 1
		}
	case codegen.ValueKindByte:
		word, err = character(text, math.MaxUint8)
	case codegen.ValueKindChar:
		word, err = character(text, utf8.MaxRune)
	default:
		return nil, fmt.Errorf("a value of %s cannot be set", typ.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("%q is not a %s: %w", text, typ.Name, err)
	}
	return binary.LittleEndian.AppendUint64(nil, word)[:typ.Size], nil
}

// character reads a character quoted, or as a number, no greater than limit.
func character(text string, limit uint64) (uint64, error) {
	if unquoted, err := strconv.Unquote(text); err == nil && utf8.RuneCountInString(unquoted) == 1 {
		r, _ := utf8.DecodeRuneInString(unquoted)
		if uint64(r) > limit {
			return 0, strconv.ErrRange
		}
		return uint64(r), nil
	}
	return strconv.ParseUint(text, 0, bits.Len64(limit))
}
