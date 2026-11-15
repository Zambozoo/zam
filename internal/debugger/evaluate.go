package debugger

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/go-dap"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
)

// errNotPath is what an expression evaluate cannot read is answered with.
var errNotPath = errors.New("only names, members and indexes can be evaluated")

// pathStep is one step of a path: a member by name, or an element by index.
type pathStep struct {
	member  string
	index   uint64
	indexed bool
}

// parsePath reads an expression as a name and the steps from it: .member,
// .digits and [index]. A static is named through its declaration with :: as
// the program names it, which reads as a member.
func parsePath(expression string) (string, []pathStep, error) {
	text := strings.ReplaceAll(strings.TrimSpace(expression), "::", ".")
	name, text := identifier(text)
	if name == "" {
		return "", nil, errNotPath
	}
	var steps []pathStep
	for text = strings.TrimSpace(text); text != ""; text = strings.TrimSpace(text) {
		switch text[0] {
		case '.':
			text = strings.TrimSpace(text[1:])
			member, rest := identifier(text)
			if member == "" {
				end := strings.IndexFunc(text, func(r rune) bool { return !unicode.IsDigit(r) })
				if end < 0 {
					end = len(text)
				}
				member, rest = text[:end], text[end:]
			}
			if member == "" {
				return "", nil, errNotPath
			}
			steps, text = append(steps, pathStep{member: member}), rest
		case '[':
			end := strings.IndexByte(text, ']')
			if end < 0 {
				return "", nil, errNotPath
			}
			index, err := strconv.ParseUint(strings.TrimSpace(text[1:end]), 0, 64)
			if err != nil {
				return "", nil, errNotPath
			}
			steps, text = append(steps, pathStep{index: index, indexed: true}), text[end+1:]
		default:
			return "", nil, errNotPath
		}
	}
	return name, steps, nil
}

// identifier splits a name off the front of text.
func identifier(text string) (string, string) {
	end := strings.IndexFunc(text, func(r rune) bool { return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if end < 0 {
		end = len(text)
	}
	if end == 0 || unicode.IsDigit(rune(text[0])) {
		return "", text
	}
	return text[:end], text[end:]
}

// evaluate answers the value expression names in the frame the editor names
// by id, or in the innermost frame of the thread that stopped the program.
func (s *server) evaluate(expression string, id int) (dap.Variable, error) {
	c, err := s.resolve(expression, id)
	return c.Variable, err
}

// resolve answers the variable expression names, as evaluate does.
func (s *server) resolve(expression string, id int) (child, error) {
	p := s.running()
	if p == nil {
		return child{}, errExited
	}
	name, steps, err := parsePath(expression)
	if err != nil {
		return child{}, err
	}
	f, err := s.evaluated(p, id)
	if err != nil {
		return child{}, err
	}

	locals, err := frameLocals{frame: f}.children(s, p)
	if err != nil {
		return child{}, err
	}
	current, ok := named(locals, name)
	if !ok {
		if current, steps, ok = s.static(p, f, name, steps); !ok {
			return child{}, fmt.Errorf("no variable %s", name)
		}
	}
	for _, step := range steps {
		if step.indexed {
			current, err = s.element(p, f.thread, current, step.index)
		} else {
			current, err = s.member(p, current, step.member)
		}
		if err != nil {
			return child{}, err
		}
	}
	current.Name = strings.TrimSpace(expression)
	return current, nil
}

// static answers the static a path begins with, and the steps after it: one
// named by its declaration, as in Main.counter, or one of a declaration the
// stopped function is inside of, named alone, the innermost first.
func (s *server) static(p *program, f frame, name string, steps []pathStep) (child, []pathStep, bool) {
	statics, err := globals{on: f.thread}.children(s, p)
	if err != nil {
		return child{}, nil, false
	}
	qualified := name
	for i := 0; ; i++ {
		if c, ok := named(statics, qualified); ok {
			return c, steps[i:], true
		}
		if i == len(steps) || steps[i].indexed {
			break
		}
		qualified += "." + steps[i].member
	}

	function, ok := p.debug.Function(f.offset)
	if !ok {
		return child{}, nil, false
	}
	decls := strings.Split(function, ".")
	for n := len(decls) - 1; n > 0; n-- {
		if c, ok := named(statics, strings.Join(decls[:n], ".")+"."+name); ok {
			return c, steps, true
		}
	}
	return child{}, nil, false
}

// evaluated answers the frame the editor names by id, or the innermost frame
// of the thread that stopped the program.
func (s *server) evaluated(p *program, id int) (frame, error) {
	s.lock.Lock()
	if id >= 1 && id <= len(s.variables.frames) {
		f := s.variables.frames[id-1]
		s.lock.Unlock()
		return f, nil
	}
	thread := s.stoppedThread
	s.lock.Unlock()
	if id != 0 {
		return frame{}, fmt.Errorf("no frame %d", id)
	}
	stack, err := p.stack(thread)
	if err != nil {
		return frame{}, err
	}
	if len(stack) == 0 {
		return frame{}, errors.New("the program is not stopped")
	}
	return stack[0], nil
}

// named answers the child called name.
func named(children []child, name string) (child, bool) {
	i := slices.IndexFunc(children, func(c child) bool { return c.Name == name })
	if i < 0 {
		return child{}, false
	}
	return children[i], true
}

// member answers the member called name of what of is, or of what it points
// to.
func (s *server) member(p *program, of child, name string) (child, error) {
	if of.VariablesReference == 0 {
		return child{}, fmt.Errorf("%s has no member %s", of.Name, name)
	}
	s.lock.Lock()
	c := s.variables.containers[of.VariablesReference-1]
	s.lock.Unlock()
	children, err := c.children(s, p)
	if err != nil {
		return child{}, err
	}
	member, ok := named(children, name)
	if !ok {
		return child{}, fmt.Errorf("%s has no member %s", of.Name, name)
	}
	return member, nil
}

// element answers the element at index of the array of is, or of the run of
// values it points to the first of.
func (s *server) element(p *program, thread int, of child, index uint64) (child, error) {
	typ := of.typ
	name := "[" + strconv.FormatUint(index, 10) + "]"
	switch {
	case typ.Kind == codegen.ValueKindArray && typ.Length > 0:
		if index >= typ.Length {
			return child{}, fmt.Errorf("index %d is past the %d elements of %s", index, typ.Length, of.Name)
		}
		stride := typ.Elem.Size
		start := index * stride
		if start+stride > uint64(len(of.data)) {
			return unreadable(name, typ.Elem), nil
		}
		return s.variable(p, thread, name, typ.Elem, of.data[start:start+stride], at(of.address, start)), nil
	case typ.Kind == codegen.ValueKindArray, typ.Kind == codegen.ValueKindPointer:
		if typ.Elem == nil || typ.Elem.Kind == codegen.ValueKindOpaque || len(of.data) < wordBytes {
			return child{}, fmt.Errorf("%s cannot be indexed", of.Name)
		}
		base := binary.LittleEndian.Uint64(of.data)
		if base == 0 {
			return child{}, fmt.Errorf("%s is null", of.Name)
		}
		size := max(typ.Elem.Size, 1)
		address := base + index*size
		data, err := p.memory(thread, address, size)
		if err != nil {
			return unreadable(name, typ.Elem), nil
		}
		return s.variable(p, thread, name, typ.Elem, data, address), nil
	}
	return child{}, fmt.Errorf("%s cannot be indexed", of.Name)
}
