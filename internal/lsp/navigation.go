package lsp

import (
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// referenceAt returns the reference written under an editor position in an open
// document, as the last check of that document resolved it. The position is
// read against the document as it is now, so an edit made since that check
// may leave it naming nothing until the next one.
func (s *server) referenceAt(params protocol.TextDocumentPositionParams) (syntax.Reference, bool) {
	s.mu.Lock()
	doc, open := s.documents[params.TextDocument.URI]
	ast := s.asts[params.TextDocument.URI]
	s.mu.Unlock()
	if !open || ast == nil {
		return syntax.Reference{}, false
	}

	file, _ := ast.CodeFiles.Get(*doc.path)
	if file == nil {
		return syntax.Reference{}, false
	}
	source, err := s.files.read(doc.path)
	if err != nil {
		return syntax.Reference{}, false
	}

	offset := newText(source).offsetAt(params.Position)
	tokens, _ := syntax.Lex(io.NopCloser(strings.NewReader(source)))
	// A cursor just past a name is still on it, which is where an editor
	// leaves one after the name is typed.
	i := sort.Search(len(tokens), func(i int) bool { return int(tokens[i].End) >= offset })
	for ; i < len(tokens) && int(tokens[i].Offset) <= offset; i++ {
		if ref, ok := ast.References.At(file, tokens[i].Offset); ok {
			return ref, true
		}
	}
	return syntax.Reference{}, false
}

func (s *server) hover(_ *glsp.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	ref, ok := s.referenceAt(params.TextDocumentPositionParams)
	if !ok {
		return nil, nil
	}

	var sections []string
	if declaration, doc, ok := s.declarationOf(ref.Target); ok {
		sections = append(sections, "```zam\n"+declaration+"\n```")
		if len(doc) > 0 {
			sections = append(sections, markdown(doc))
		}
	}
	if ref.Type != nil {
		sections = append(sections, "Type: `"+ref.Type.String()+"`")
	}
	if len(sections) == 0 {
		return nil, nil
	}

	rng, _ := s.rangeOf(ref.File.Path, ref.Name)
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: strings.Join(sections, "\n\n")},
		Range:    &rng,
	}, nil
}

func (s *server) definition(_ *glsp.Context, params *protocol.DefinitionParams) (any, error) {
	ref, ok := s.referenceAt(params.TextDocumentPositionParams)
	if !ok {
		return nil, nil
	}
	location, ok := s.locationOf(ref.Target.File.Path, ref.Target.Name)
	if !ok {
		return nil, nil
	}
	return location, nil
}

func (s *server) references(_ *glsp.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	ref, ok := s.referenceAt(params.TextDocumentPositionParams)
	if !ok {
		return nil, nil
	}

	s.mu.Lock()
	ast := s.asts[params.TextDocument.URI]
	s.mu.Unlock()

	locations := []protocol.Location{}
	for _, use := range ast.References.To(ref.Target) {
		if !params.Context.IncludeDeclaration && use.File == ref.Target.File && use.Name == ref.Target.Name {
			continue
		}
		if location, ok := s.locationOf(use.File.Path, use.Name); ok {
			locations = append(locations, location)
		}
	}
	sort.Slice(locations, func(i, j int) bool {
		a, b := locations[i], locations[j]
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		return a.Range.Start.Character < b.Range.Start.Character
	})
	return locations, nil
}

// locationOf returns where a name is written, for a file an editor can open.
// The standard library is compiled in, so a name written there has a location
// only once its source has been opened from disk.
func (s *server) locationOf(path *base.Path, name *syntax.Token) (protocol.Location, bool) {
	filePath, ok := s.files.diskPath(path)
	if !ok {
		return protocol.Location{}, false
	}
	rng, ok := s.rangeOf(path, name)
	if !ok {
		return protocol.Location{}, false
	}
	return protocol.Location{URI: (&url.URL{Scheme: "file", Path: filePath}).String(), Range: rng}, true
}

// rangeOf returns the range of a name written in a file. A name is an
// identifier, so its length is that of its value.
func (s *server) rangeOf(path *base.Path, name *syntax.Token) (protocol.Range, bool) {
	source, err := s.files.read(path)
	if err != nil {
		return protocol.Range{}, false
	}
	t := newText(source)
	start := int(name.Offset)
	end := start
	if value, ok := name.Value.(string); ok {
		end += len([]rune(value))
	}
	return protocol.Range{Start: t.position(start), End: t.position(end)}, true
}

// textOf returns the text of a file as the compiler reads it.
func (s *server) textOf(file *syntax.File) (*text, bool) {
	source, err := s.files.read(file.Path)
	if err != nil {
		return nil, false
	}
	return newText(source), true
}

// declarationOf returns how a target is declared and the doc comment of its
// declaration. A declaration is outlined with the fields it declares; anything
// else is the line its name is written on, less a body opened there.
func (s *server) declarationOf(target syntax.Target) (declaration string, doc []string, ok bool) {
	source, err := s.files.read(target.File.Path)
	if err != nil {
		return "", nil, false
	}
	t := newText(source)
	if target.Start != nil {
		doc = t.docOf(int(target.Start.Offset))
	}
	if d, ok := declAt(target.File, target.Name); ok {
		return outline(s.textOf, d), doc, true
	}
	return t.declaredLine(target.Name), doc, true
}
