package lsp

import (
	"regexp"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/test"
)

const (
	// commandRunTest and commandDebugTest are what the editor runs to run or
	// debug a test file's tests, given the file's path and the qualified name
	// of the test, or an empty name for all of them.
	commandRunTest   = "zam.runTest"
	commandDebugTest = "zam.debugTest"
)

// everyTest matches every test's name.
var everyTest = regexp.MustCompile("")

// codeLens answers links above each test of an open test file to run or debug
// it, and above its root declaration to run or debug them all. A file whose
// tests cannot be found, as the runner would find them, has none.
func (s *server) codeLens(_ *glsp.Context, params *protocol.CodeLensParams) ([]protocol.CodeLens, error) {
	s.mu.Lock()
	doc, open := s.documents[params.TextDocument.URI]
	ast := s.asts[params.TextDocument.URI]
	s.mu.Unlock()
	if !open || ast == nil || !doc.path.IsTestFile() {
		return nil, nil
	}

	files, err := test.Discover(ast, []base.Path{doc.path}, everyTest)
	if err != nil || len(files) == 0 {
		return nil, nil
	}
	codeFile, _ := ast.CodeFiles.Get(doc.path)
	if codeFile == nil {
		return nil, nil
	}

	var lenses []protocol.CodeLens
	add := func(rng protocol.Range, name, run, debug string) {
		arguments := []any{string(doc.path), name}
		lenses = append(lenses,
			protocol.CodeLens{Range: rng, Command: &protocol.Command{Title: run, Command: commandRunTest, Arguments: arguments}},
			protocol.CodeLens{Range: rng, Command: &protocol.Command{Title: debug, Command: commandDebugTest, Arguments: arguments}},
		)
	}
	if rng, ok := s.rangeOf(doc.path, codeFile.Decl.NameToken()); ok {
		add(rng, "", "▶ run tests", "debug tests")
	}
	for _, test := range files[0].Tests {
		for name, field := range codeFile.Decl.StaticFields() {
			if name != test {
				continue
			}
			if rng, ok := s.rangeOf(doc.path, field.NameToken()); ok {
				add(rng, files[0].Decl+"."+test, "▶ run test", "debug test")
			}
		}
	}
	return lenses, nil
}
