// Package lsp is a language server for zam, speaking the Language Server
// Protocol over stdio. It reports the compiler's diagnostics for open documents
// and colors them from their tokens, and answers hover, go-to-definition and
// find-references from what checking them resolved.
package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	glspserver "github.com/tliron/glsp/server"
	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

// debounce is how long the server waits after the last edit before checking
// the open documents again.
const debounce = 200 * time.Millisecond

// server holds the open documents and checks them as they change.
type server struct {
	handler  protocol.Handler
	files    *overlay
	packages packageProvider
	threads  int
	delay    time.Duration

	mu        sync.Mutex
	documents map[protocol.DocumentUri]*document
	// asts holds the AST the last check of each open document built, which is
	// what a question about a name in it is answered from.
	asts   map[protocol.DocumentUri]*syntax.AST
	notify glsp.NotifyFunc
	timer  *time.Timer

	// analyzing keeps two checks from running at once.
	analyzing sync.Mutex
}

// document is an open document.
type document struct {
	path    base.Path
	version protocol.Integer
}

func newServer(packages packageProvider, delay time.Duration) *server {
	s := &server{
		files:     newOverlay(),
		packages:  packages,
		threads:   runtime.GOMAXPROCS(0),
		delay:     delay,
		documents: map[protocol.DocumentUri]*document{},
		asts:      map[protocol.DocumentUri]*syntax.AST{},
	}
	s.handler = protocol.Handler{
		Initialize:                     s.initialize,
		Initialized:                    func(*glsp.Context, *protocol.InitializedParams) error { return nil },
		Shutdown:                       func(*glsp.Context) error { return nil },
		Exit:                           func(*glsp.Context) error { return nil },
		SetTrace:                       func(*glsp.Context, *protocol.SetTraceParams) error { return nil },
		TextDocumentDidOpen:            s.didOpen,
		TextDocumentDidChange:          s.didChange,
		TextDocumentDidSave:            s.didSave,
		TextDocumentDidClose:           s.didClose,
		TextDocumentSemanticTokensFull: s.semanticTokensFull,
		TextDocumentHover:              s.hover,
		TextDocumentDefinition:         s.definition,
		TextDocumentReferences:         s.references,
		TextDocumentCodeLens:           s.codeLens,
	}
	return s
}

// Run serves the protocol over stdin and stdout until the client exits.
func Run() error {
	zamPath, err := base.ZamPath()
	if err != nil {
		return err
	}
	s := newServer(base.NewPackageProvider(filepath.Join(zamPath, "pkg")), debounce)
	return glspserver.NewServer(&s.handler, "zam", false).RunStdio()
}

func (s *server) initialize(_ *glsp.Context, _ *protocol.InitializeParams) (any, error) {
	capabilities := s.handler.CreateServerCapabilities()
	full := protocol.TextDocumentSyncKindFull
	capabilities.TextDocumentSync.(*protocol.TextDocumentSyncOptions).Change = &full
	capabilities.SemanticTokensProvider = &protocol.SemanticTokensOptions{Legend: tokenLegend, Full: true}
	return protocol.InitializeResult{
		Capabilities: capabilities,
		ServerInfo:   &protocol.InitializeResultServerInfo{Name: "zam"},
	}, nil
}

func (s *server) didOpen(ctx *glsp.Context, params *protocol.DidOpenTextDocumentParams) error {
	path, err := pathOf(params.TextDocument.URI)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if root, ok := stdRootOf(s.files.FileProvider, path); ok {
		if stdPath, ok := stdPathOf(root, path); ok {
			path = stdPath
			s.files.setStdRoot(root)
		}
	}
	s.files.set(path, params.TextDocument.Text)
	s.notify = ctx.Notify
	s.documents[params.TextDocument.URI] = &document{path: path, version: params.TextDocument.Version}
	s.schedule()
	return nil
}

func (s *server) didChange(ctx *glsp.Context, params *protocol.DidChangeTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.documents[params.TextDocument.URI]
	if !ok {
		return nil
	}
	for _, change := range params.ContentChanges {
		if whole, ok := change.(protocol.TextDocumentContentChangeEventWhole); ok {
			s.files.set(doc.path, whole.Text)
		}
	}
	doc.version = params.TextDocument.Version
	s.notify = ctx.Notify
	s.schedule()
	return nil
}

func (s *server) didSave(ctx *glsp.Context, _ *protocol.DidSaveTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = ctx.Notify
	s.schedule()
	return nil
}

func (s *server) didClose(ctx *glsp.Context, params *protocol.DidCloseTextDocumentParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.documents[params.TextDocument.URI]
	if !ok {
		return nil
	}
	s.files.remove(doc.path)
	delete(s.documents, params.TextDocument.URI)
	delete(s.asts, params.TextDocument.URI)
	ctx.Notify(protocol.ServerTextDocumentPublishDiagnostics, protocol.PublishDiagnosticsParams{
		URI:         params.TextDocument.URI,
		Diagnostics: []protocol.Diagnostic{},
	})
	s.schedule()
	return nil
}

func (s *server) semanticTokensFull(_ *glsp.Context, params *protocol.SemanticTokensParams) (*protocol.SemanticTokens, error) {
	s.mu.Lock()
	doc, ok := s.documents[params.TextDocument.URI]
	s.mu.Unlock()
	if !ok {
		return &protocol.SemanticTokens{Data: []protocol.UInteger{}}, nil
	}
	source, err := s.files.read(doc.path)
	if err != nil {
		return nil, err
	}
	return &protocol.SemanticTokens{Data: semanticTokens(source)}, nil
}

// schedule checks every open document once edits have stopped for the delay.
// Every one is checked, not only the one edited, since an edit to one file can
// change what is wrong with any file that imports it. The caller holds mu.
func (s *server) schedule() {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.delay, s.check)
}

// check analyzes every open document and publishes what each analysis found.
func (s *server) check() {
	s.analyzing.Lock()
	defer s.analyzing.Unlock()

	s.mu.Lock()
	notify := s.notify
	documents := make(map[protocol.DocumentUri]document, len(s.documents))
	for uri, doc := range s.documents {
		documents[uri] = *doc
	}
	s.mu.Unlock()
	if notify == nil {
		return
	}

	for uri, doc := range documents {
		diagnostics, ast := analyze(s.files, s.packages, doc.path, s.threads)

		s.mu.Lock()
		if _, open := s.documents[uri]; open {
			s.asts[uri] = ast
		}
		s.mu.Unlock()

		version := protocol.UInteger(doc.version)
		notify(protocol.ServerTextDocumentPublishDiagnostics, protocol.PublishDiagnosticsParams{
			URI:         uri,
			Version:     &version,
			Diagnostics: diagnostics,
		})
	}
}

// pathOf returns the path the compiler knows a document's file under.
func pathOf(uri protocol.DocumentUri) (base.Path, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported document URI %q: only file URIs are checked", uri)
	}
	p := u.Path
	// A Windows path is written after a slash, as /C:/dir/file.
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return base.ParsePath(filepath.FromSlash(p))
}

// fileURI returns the file URI of the file at path.
func fileURI(path string) protocol.DocumentUri {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
