package lsp

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// client drives a server through its handler the way the transport would, and
// collects what the server publishes.
type client struct {
	t         *testing.T
	server    *server
	published chan protocol.PublishDiagnosticsParams
}

func newClient(t *testing.T) *client {
	return &client{t: t, server: newServer(nil, 10*time.Millisecond), published: make(chan protocol.PublishDiagnosticsParams, 16)}
}

func (c *client) call(method string, params any) any {
	c.t.Helper()

	raw, err := json.Marshal(params)
	require.NoError(c.t, err)
	ctx := &glsp.Context{
		Method: method,
		Params: raw,
		Notify: func(method string, params any) {
			if method == protocol.ServerTextDocumentPublishDiagnostics {
				c.published <- params.(protocol.PublishDiagnosticsParams)
			}
		},
	}
	r, validMethod, validParams, err := c.server.handler.Handle(ctx)
	require.True(c.t, validMethod, method)
	require.True(c.t, validParams, method)
	require.NoError(c.t, err, method)
	return r
}

func (c *client) next() protocol.PublishDiagnosticsParams {
	c.t.Helper()
	select {
	case p := <-c.published:
		return p
	case <-time.After(10 * time.Second):
		require.FailNow(c.t, "no diagnostics were published")
		return protocol.PublishDiagnosticsParams{}
	}
}

// TestServerPublishesDiagnosticsAsDocumentsChange runs a session from
// initialization through an edit that breaks a document, one that fixes it,
// and closing it.
func TestServerPublishesDiagnosticsAsDocumentsChange(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": ""})
	uri := (&url.URL{Scheme: "file", Path: paths["Main.zam"].String()}).String()
	c := newClient(t)

	result := c.call(protocol.MethodInitialize, protocol.InitializeParams{}).(protocol.InitializeResult)
	sync := result.Capabilities.TextDocumentSync.(*protocol.TextDocumentSyncOptions)
	assert.Equal(t, protocol.TextDocumentSyncKindFull, *sync.Change)
	assert.Equal(t, tokenLegend, result.Capabilities.SemanticTokensProvider.(*protocol.SemanticTokensOptions).Legend)
	c.call(protocol.MethodInitialized, protocol.InitializedParams{})

	c.call(protocol.MethodTextDocumentDidOpen, protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{
		URI: uri, LanguageID: "zam", Version: 1,
		Text: "namespace Main {\n    static fun int() f = () { return \"s\"; }\n}\n",
	}})
	published := c.next()
	assert.Equal(t, uri, published.URI)
	assert.Equal(t, protocol.UInteger(1), *published.Version)
	assert.Len(t, published.Diagnostics, 1)

	c.call(protocol.MethodTextDocumentDidChange, map[string]any{
		"textDocument":   protocol.VersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri}, Version: 2},
		"contentChanges": []protocol.TextDocumentContentChangeEventWhole{{Text: "namespace Main {\n    static fun int() f = () { return 1; }\n}\n"}},
	})
	published = c.next()
	assert.Equal(t, protocol.UInteger(2), *published.Version)
	assert.Empty(t, published.Diagnostics)

	tokens := c.call(protocol.MethodTextDocumentSemanticTokensFull, protocol.SemanticTokensParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	}).(*protocol.SemanticTokens)
	assert.Equal(t, token{0, 0, 9, tokenKeyword}, decode(tokens.Data)[0])

	c.call(protocol.MethodTextDocumentDidClose, protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	published = c.next()
	assert.Equal(t, uri, published.URI)
	assert.Empty(t, published.Diagnostics)

	c.call(protocol.MethodShutdown, nil)
}
