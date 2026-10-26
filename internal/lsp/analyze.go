package lsp

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	protocol "github.com/tliron/glsp/protocol_3_16"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
)

// gitProvider downloads a project's dependencies.
type gitProvider interface {
	Download(url string) (*base.Path, error)
	ArchiveName(url string) string
}

// analysis checks one document as the entry of a program and reports what is
// wrong with it.
type analysis struct {
	files *overlay
	entry *base.Path
	// text and tokens are entry as last read, lexed on first need.
	text   *text
	tokens []syntax.LexedToken
}

// analyze returns the diagnostics for checking entry as the entry of a program,
// and the AST checking it built, whose references are filled in wherever it was
// checked semantically.
//
// Syntax errors are always reported, and semantic errors wherever every file
// was parsed into the AST, even where the parse recovered: what a recovered
// parse kept still binds the names it declares. Semantic errors located in a
// file with a broken import are left out, since a name reached through that
// import cannot resolve however it is written.
//
// An error located in entry is reported where it lies. The errors located in
// another file — an import or the standard library — are reported together at
// the head of entry, as are errors located nowhere at all, so that each
// document reports what checking it found without competing with another
// document's analysis for the same file.
func analyze(files *overlay, git gitProvider, entry *base.Path, threads int) (diagnostics []protocol.Diagnostic, ast *syntax.AST) {
	a := &analysis{files: files, entry: entry}
	defer func() {
		if r := recover(); r != nil {
			diagnostics = append(diagnostics, newDiagnostic(protocol.Range{}, fmt.Sprintf("internal compiler error: %v", r)))
			ast = nil
		}
	}()

	ast, errs := syntax.NewAST(&command.Command{InputPath: entry, MaxThreads: threads}, files, git)
	if ast != nil {
		broken := map[string]bool{}
		for _, file := range ast.CodeFiles.All() {
			if file.Value.BrokenImports {
				broken[file.Key.String()] = true
			}
		}
		for _, err := range semantic.ValidateAST(ast) {
			if path, ok := pathOfError(err); !ok || !broken[path] {
				errs = append(errs, err)
			}
		}
	}

	diagnostics = []protocol.Diagnostic{}
	elsewhere := map[string][]error{}
	var order []string
	for _, err := range errs {
		path, ok := pathOfError(err)
		switch {
		case !ok:
			diagnostics = append(diagnostics, newDiagnostic(protocol.Range{}, err.Error()))
		case path == entry.String():
			diagnostics = append(diagnostics, a.diagnostic(err))
		default:
			if _, seen := elsewhere[path]; !seen {
				order = append(order, path)
			}
			elsewhere[path] = append(elsewhere[path], err)
		}
	}
	for _, path := range order {
		diagnostics = append(diagnostics, summary(path, elsewhere[path]))
	}
	return diagnostics, ast
}

// pathOfError returns the path of the file an error is located in, if any.
func pathOfError(err error) (string, bool) {
	var located *base.Error
	if !errors.As(err, &located) || located.Location == nil {
		return "", false
	}
	return located.Location.Path.String(), true
}

// summary reports at the head of entry the errors located in another file: the
// first of them in full, and how many more there are.
func summary(path string, errs []error) protocol.Diagnostic {
	if len(errs) == 1 {
		return newDiagnostic(protocol.Range{}, errs[0].Error())
	}
	return newDiagnostic(protocol.Range{}, fmt.Sprintf("%s (and %d more in %s)", errs[0].Error(), len(errs)-1, path))
}

// diagnostic reports an error located in entry, over the token it was found at.
func (a *analysis) diagnostic(err error) protocol.Diagnostic {
	var located *base.Error
	errors.As(err, &located)
	message := err.Error()
	if inner := located.Unwrap(); inner != nil {
		message = inner.Error()
	}
	return newDiagnostic(a.rangeOf(located.Location.Position), message)
}

// rangeOf returns the range of the token starting at a position in entry, found
// by lexing entry again. A position no token starts at, such as one inside a
// token the lexer could not read, is an empty range there, and no position at
// all is the head of the file.
func (a *analysis) rangeOf(position *base.Position) protocol.Range {
	if position == nil {
		return protocol.Range{}
	}
	if a.text == nil {
		source, err := a.files.read(a.entry)
		if err != nil {
			return protocol.Range{}
		}
		a.text = newText(source)
		a.tokens, _ = syntax.Lex(io.NopCloser(strings.NewReader(source)))
	}

	start := a.text.offset(position.Line, position.Column)
	end := start
	i := sort.Search(len(a.tokens), func(i int) bool { return int(a.tokens[i].Offset) >= start })
	if i < len(a.tokens) && int(a.tokens[i].Offset) == start {
		end = int(a.tokens[i].End)
	}
	return protocol.Range{Start: a.text.position(start), End: a.text.position(end)}
}

func newDiagnostic(rng protocol.Range, message string) protocol.Diagnostic {
	severity := protocol.DiagnosticSeverityError
	source := "zam"
	return protocol.Diagnostic{Range: rng, Severity: &severity, Source: &source, Message: message}
}
