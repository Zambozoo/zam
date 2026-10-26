package lsp

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	protocol "github.com/tliron/glsp/protocol_3_16"
	base "github.com/zambozoo/zam/internal/0_base"
	"github.com/zambozoo/zam/internal/std"
)

// TestServerChecksStdSourceAsTheStdLibrary opens a file of a copy of the
// standard library's source, which is checked as the standard library file it
// is rather than as a project's own beside the one compiled in, and whose
// siblings are then read from the copy.
func TestServerChecksStdSourceAsTheStdLibrary(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, std.FS))
	objPath := filepath.Join(dir, "lang", "Obj.zam")
	obj, err := os.ReadFile(objPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(objPath, append(obj, "\n// on disk\n"...), 0o600))

	filePath := filepath.Join(dir, "io", "local", "File.zam")
	text, err := os.ReadFile(filePath)
	require.NoError(t, err)
	uri := (&url.URL{Scheme: "file", Path: filePath}).String()

	c := newClient(t)
	c.call(protocol.MethodInitialize, protocol.InitializeParams{})
	c.call(protocol.MethodTextDocumentDidOpen, protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{
		URI: uri, LanguageID: "zam", Version: 1, Text: string(text),
	}})
	published := c.next()
	assert.Equal(t, uri, published.URI)
	assert.Empty(t, published.Diagnostics)

	source, err := c.server.files.read(base.StdPath("lang/Obj.zam"))
	require.NoError(t, err)
	assert.Equal(t, string(obj)+"\n// on disk\n", source)

	onDisk, ok := c.server.files.diskPath(base.StdPath("lang/Obj.zam"))
	assert.True(t, ok)
	assert.Equal(t, objPath, onDisk)
}

func TestStdRootOfIgnoresAProject(t *testing.T) {
	paths := project(t, map[string]string{"Main.zam": ""})
	_, ok := stdRootOf(&base.FileProvider{}, paths["Main.zam"])
	assert.False(t, ok)
}
