package lsp

import (
	"iter"
	"regexp"
	"sort"
	"strings"

	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// declAt returns the declaration in a file whose name is written at a token.
func declAt(file *syntax.File, name *syntax.Token) (syntax.Decl, bool) {
	if file == nil || file.Decl == nil {
		return nil, false
	}
	var find func(d syntax.Decl) (syntax.Decl, bool)
	find = func(d syntax.Decl) (syntax.Decl, bool) {
		if d.NameToken() == name {
			return d, true
		}
		for inner := range d.Decls() {
			if found, ok := find(inner); ok {
				return found, true
			}
		}
		return nil, false
	}
	return find(file.Decl)
}

// outline returns a declaration as written, its header followed by the
// signature of each field it declares, in the order they are written. A class
// or an abstract goes on to the instance fields it inherits, base by base under
// each base's name, less any a nearer declaration overrides. A field the
// compiler supplied rather than the source wrote is left out.
//
// texts returns the text of the file a declaration is written in.
func outline(texts func(*syntax.File) (*text, bool), d syntax.Decl) string {
	t, ok := texts(d.File())
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString(t.header(d))

	seen := map[string]bool{}
	group := func(t *text, fields []syntax.Field) {
		sort.Slice(fields, func(i, j int) bool { return fields[i].NameToken().Offset < fields[j].NameToken().Offset })
		for _, f := range fields {
			seen[f.Name()] = true
			b.WriteString("    ")
			b.WriteString(t.signature(f))
			b.WriteString("\n")
		}
	}

	b.WriteString(" {\n")
	empty := b.Len()
	own := declaredFields(d, true)
	group(t, own)
	for base := range bases(d) {
		var inherited []syntax.Field
		for _, f := range declaredFields(base, false) {
			if !seen[f.Name()] {
				inherited = append(inherited, f)
			}
		}
		baseText, ok := texts(base.File())
		if len(inherited) == 0 || !ok {
			continue
		}
		if b.Len() > empty {
			b.WriteString("\n")
		}
		b.WriteString("    // from ")
		b.WriteString(base.Name())
		b.WriteString("\n")
		group(baseText, inherited)
	}
	if b.Len() == empty {
		return t.header(d)
	}
	b.WriteString("}")
	return b.String()
}

// declaredFields returns the fields a declaration's source writes: its instance
// fields, and when statics is set, its static fields and nested declarations.
func declaredFields(d syntax.Decl, statics bool) []syntax.Field {
	var fields []syntax.Field
	// A concrete instantiation of a generic declaration shares the generic's
	// name token, and its fields may be the generic's own.
	own := func(f syntax.Field) {
		if f.NameToken() != nil && f.OuterDecl() != nil && f.OuterDecl().NameToken() == d.NameToken() {
			fields = append(fields, f)
		}
	}
	for _, f := range d.InstanceFields() {
		own(f)
	}
	if statics {
		for _, f := range d.StaticFields() {
			own(f)
		}
		for inner := range d.Decls() {
			own(inner)
		}
	}
	return fields
}

// bases returns the chain of declarations a class or an abstract descends
// from, nearest first.
func bases(d syntax.Decl) iter.Seq[syntax.Decl] {
	return func(yield func(syntax.Decl) bool) {
		for {
			subtypeable, ok := d.(syntax.DeclSubtypeable)
			if !ok || subtypeable.BaseDeclType() == nil {
				return
			}
			resolvable, ok := subtypeable.BaseDeclType().Type.(syntax.TypeResolvable)
			if !ok {
				return
			}
			if generic, ok := resolvable.(*syntax.TypeGeneric); ok && generic.Type.Decl() == nil {
				return
			}
			if resolvable.Decl() == nil {
				return
			}
			d = resolvable.Decl()
			if !yield(d) {
				return
			}
		}
	}
}

// header returns the line a declaration's name is written on, up to the brace
// that opens its body.
func (t *text) header(d syntax.Decl) string {
	offset := int(d.NameToken().Offset)
	start := t.lineStarts[t.position(offset).Line]
	line := string(t.runes[start:])
	if end := strings.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	nameEnd := len(string(t.runes[start:offset])) + len(d.Name())
	if brace := strings.Index(line[nameEnd:], "{"); brace >= 0 {
		line = line[:nameEnd+brace]
	}
	return strings.TrimSpace(line)
}

// lambdaBody matches what follows a name up to the brace opening a lambda's
// body: its parameter names, which are names only, so the first brace after
// them is the body's.
var lambdaBody = regexp.MustCompile(`^\s*=\s*\([^)]*\)\s*\{`)

// declaredLine returns the line a name is written on, cut before the body of a
// lambda the name is bound to.
func (t *text) declaredLine(name *syntax.Token) string {
	offset := int(name.Offset)
	line := t.lineOf(offset)
	nameEnd := len(string(t.runes[t.lineStarts[t.position(offset).Line]:offset]))
	if value, ok := name.Value.(string); ok {
		nameEnd += len(value)
	}
	if match := lambdaBody.FindStringIndex(line[nameEnd:]); match != nil {
		line = line[:nameEnd+match[1]-1]
	}
	return strings.TrimSpace(line)
}

// signature returns what a field is declared as: the line its name is written
// on, up to the end of the name, which leaves out a method's body and a
// member's initializer. A nested declaration is its header, and an enum value
// is its name.
func (t *text) signature(f syntax.Field) string {
	if d, ok := f.(syntax.Decl); ok {
		return t.header(d)
	}
	name := f.NameToken()
	if f.Start() == name {
		return f.Name()
	}
	end := int(name.Offset) + len([]rune(f.Name()))
	start := t.lineStarts[t.position(int(name.Offset)).Line]
	return strings.TrimSpace(string(t.runes[start:end]))
}
