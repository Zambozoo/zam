package selfhost_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lspStep is one message of a scripted language server session: a request,
// whose response is awaited, or a notification after which the diagnostics
// published for a document's version are awaited when version is set.
type lspStep struct {
	message string
	id      int
	uri     string
	version int
}

// TestLSPMatchesGo plays one session through zam lsp of the compiler written
// in zam and through Go's language server, and expects every answer, and the
// diagnostics published as the documents change, to be the same.
func TestLSPMatchesGo(t *testing.T) {
	t.Parallel()
	zam, err := compiler()
	require.NoError(t, err)
	dir := t.TempDir()
	files := map[string]string{
		"zam.yml":    "name: served\n",
		"Other.zam":  "namespace Other {\n    // g returns *one*.\n    static fun int() g = () { return 1; }\n}\n",
		"X.test.zam": "public namespace XTest {\n    public fun void(uint, str...) First = (argC, argV) {\n    }\n}\n",
	}
	for name, source := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(source), 0o644))
	}
	broken := "import \"Other.zam\";\n\nnamespace Main {\n    static fun int() f = () {\n        var int x = nope;\n        return x;\n    }\n}\n"
	fixed := "import \"Other.zam\";\n\nnamespace Main {\n    class Box {\n        var Box* next;\n        var int n;\n    }\n\n    static fun int(Box*) f = (b) {\n        // é\n        var int x = Other::g() + b.next.n;\n        return x + x;\n    }\n}\n"
	uri := lspURI(filepath.Join(dir, "Main.zam"))
	position := func(id int, method string, line, character int, extra string) lspStep {
		return lspStep{id: id, message: fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{"textDocument":{"uri":%q},"position":{"line":%d,"character":%d}%s}}`, id, method, uri, line, character, extra)}
	}
	script := []lspStep{
		{id: 1, message: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`},
		{message: `{"jsonrpc":"2.0","method":"initialized","params":{}}`},
		{message: fmt.Sprintf(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":%q,"languageId":"zam","version":1,"text":%s}}}`, uri, strconv.Quote(broken)), uri: uri, version: 1},
		{message: fmt.Sprintf(`{"jsonrpc":"2.0","method":"textDocument/didChange","params":{"textDocument":{"uri":%q,"version":2},"contentChanges":[{"text":%s}]}}`, uri, strconv.Quote(fixed)), uri: uri, version: 2},
		{id: 2, message: fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"textDocument/semanticTokens/full","params":{"textDocument":{"uri":%q}}}`, uri)},
		position(3, "textDocument/hover", 10, 37, ""),
		position(4, "textDocument/hover", 10, 27, ""),
		position(5, "textDocument/hover", 10, 22, ""),
		position(6, "textDocument/hover", 0, 10, ""),
		position(7, "textDocument/hover", 10, 39, ""),
		position(8, "textDocument/definition", 10, 27, ""),
		position(9, "textDocument/definition", 10, 40, ""),
		position(10, "textDocument/references", 10, 16, `,"context":{"includeDeclaration":true}`),
		position(11, "textDocument/references", 10, 16, `,"context":{"includeDeclaration":false}`),
		position(12, "textDocument/hover", 1, 0, ""),
		{id: 13, message: fmt.Sprintf(`{"jsonrpc":"2.0","id":13,"method":"zam/tests","params":{"uri":%q}}`, lspURI(filepath.Join(dir, "X.test.zam")))},
		{id: 14, message: `{"jsonrpc":"2.0","id":14,"method":"zam/unknown"}`},
		{id: 15, message: `{"jsonrpc":"2.0","id":15,"method":"shutdown"}`},
		{message: `{"jsonrpc":"2.0","method":"exit"}`},
	}

	want := goSession(t, script)

	cmd := exec.Command(zam, "lsp")
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAMPATH="+t.TempDir())
	got := serve(t, cmd, script)

	require.Len(t, got, len(want))
	for i, step := range script {
		if response, ok := want[i].(map[string]any); ok && step.id != 0 && step.id != 12 && step.id != 15 {
			assert.True(t, response["result"] != nil || response["error"] != nil, "Go answers step %d with nothing: %s", i, step.message)
		}
	}
	for i := range want {
		assert.Equal(t, want[i], got[i], "step %d: %s", i, script[i].message)
	}
}

// goSession plays script through Go's language server, run as the test binary
// serving over its standard streams.
func goSession(t *testing.T, script []lspStep) []any {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), goLSPVariable+"=1", "ZAMPATH="+t.TempDir())
	return serve(t, cmd, script)
}

// serve plays script through the language server cmd runs.
func serve(t *testing.T, cmd *exec.Cmd, script []lspStep) []any {
	t.Helper()
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	got := lspSession(t, in, bufio.NewReader(out), script)
	require.NoError(t, in.Close())
	require.NoError(t, cmd.Wait(), stderr.String())
	return got
}

// lspSession writes each step of script to a language server at editor,
// awaiting from replies what each step is answered with, and answers those.
func lspSession(t *testing.T, editor io.Writer, replies *bufio.Reader, script []lspStep) []any {
	t.Helper()
	var got []any
	for _, step := range script {
		_, err := fmt.Fprintf(editor, "Content-Length: %d\r\n\r\n%s", len(step.message), step.message)
		require.NoError(t, err)
		switch {
		case step.id != 0:
			for {
				message := readLSP(t, replies)
				if id, ok := message["id"].(float64); ok && int(id) == step.id {
					delete(message, "jsonrpc")
					got = append(got, message)
					break
				}
			}
		case step.version != 0:
			for {
				message := readLSP(t, replies)
				params, _ := message["params"].(map[string]any)
				if version, ok := params["version"].(float64); ok && params["uri"] == step.uri && int(version) == step.version {
					got = append(got, params)
					break
				}
			}
		default:
			got = append(got, nil)
		}
	}
	return got
}

// readLSP reads one message from a language server.
func readLSP(t *testing.T, replies *bufio.Reader) map[string]any {
	t.Helper()
	body, err := readFramed(replies)
	require.NoError(t, err)
	var message map[string]any
	require.NoError(t, json.Unmarshal(body, &message))
	return message
}

// lspURI answers the file URI of the file at path, as both servers write it.
func lspURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}
