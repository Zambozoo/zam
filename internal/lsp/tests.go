package lsp

import (
	"encoding/json"
	"regexp"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/test"
)

// methodTests is the request an editor asks the tests of a test file with.
const methodTests = "zam/tests"

// everyTest matches every test's name.
var everyTest = regexp.MustCompile("")

type (
	// testsParams names the test file whose tests are asked for.
	testsParams struct {
		URI protocol.DocumentUri `json:"uri"`
	}

	// testItem is a test of a file, or all of them, and where it is declared.
	testItem struct {
		// Name is the test's qualified name, as the runner's --run pattern
		// matches it, or empty for every test of the file.
		Name  string         `json:"name"`
		Range protocol.Range `json:"range"`
	}
)

// Handle implements [glsp.Handler]. It answers the requests zam adds to the
// protocol, and hands the rest to the protocol's handler.
func (s *server) Handle(ctx *glsp.Context) (result any, validMethod bool, validParams bool, err error) {
	if ctx.Method != methodTests {
		return s.handler.Handle(ctx)
	}
	var params testsParams
	if err := json.Unmarshal(ctx.Params, &params); err != nil {
		return nil, true, false, err
	}
	result, err = s.tests(params)
	return result, true, true, err
}

// tests answers the tests of a test file, as the runner finds them, the first
// standing for every one and ranging over the file's declaration. The file
// need not be open. One whose tests cannot be found has none.
func (s *server) tests(params testsParams) ([]testItem, error) {
	path, err := pathOf(params.URI)
	if err != nil {
		return nil, err
	}
	items := []testItem{}
	if !path.IsTestFile() {
		return items, nil
	}

	_, ast := analyze(s.files, s.packages, path, s.threads)
	if ast == nil {
		return items, nil
	}
	files, err := test.Discover(ast, []base.Path{path}, everyTest)
	if err != nil || len(files) == 0 {
		return items, nil
	}
	codeFile, _ := ast.CodeFiles.Get(path)
	if codeFile == nil {
		return items, nil
	}

	if rng, ok := s.rangeOf(path, codeFile.Decl.NameToken()); ok {
		items = append(items, testItem{Range: rng})
	}
	for _, name := range files[0].Tests {
		field, ok := codeFile.Decl.GetStaticField(name)
		if !ok {
			continue
		}
		if rng, ok := s.rangeOf(path, field.NameToken()); ok {
			items = append(items, testItem{Name: files[0].Decl + "." + name, Range: rng})
		}
	}
	return items, nil
}
