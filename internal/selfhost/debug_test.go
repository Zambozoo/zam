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

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/debugger"
)

// echoSource is a program that writes back every byte it reads as soon as it
// reads it, standing in for lldb-dap.
const echoSource = `#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#define read _read
#define write _write
#else
#include <unistd.h>
#endif

int main(void) {
#ifdef _WIN32
    _setmode(0, _O_BINARY);
    _setmode(1, _O_BINARY);
#endif
    char buffer[4096];
    int n;
    while ((n = read(0, buffer, sizeof buffer)) > 0)
        write(1, buffer, n);
    return 0;
}
`

// TestDebugMatchesGo plays one debugging session through zam debug of the
// compiler written in zam and through Go's proxy, both in front of a program
// that hands every message back, and expects the same replies: a source only
// zam can read named by a reference and answered by the proxy, a launch
// compiled and handed on, a launch that does not compile refused, and anything
// else passed through.
func TestDebugMatchesGo(t *testing.T) {
	t.Parallel()
	zam, err := compiler()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: debugged\n"), 0o644))
	main := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(main, []byte("namespace Main {\n    fun void(uint, str...) main = (argC, argV) {\n    }\n}\n"), 0o644))
	broken := filepath.Join(dir, "Broken.zam")
	require.NoError(t, os.WriteFile(broken, []byte("namespace Broken {\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "echo.c"), []byte(echoSource), 0o644))
	echo := filepath.Join(dir, "echo")
	if strings.HasSuffix(zam, ".exe") {
		echo += ".exe"
	}
	out, err := exec.Command("zig", "cc", filepath.Join(dir, "echo.c"), "-o", echo).CombinedOutput()
	require.NoError(t, err, string(out))

	script := []string{
		`{"seq":1,"type":"event","event":"loadedSource","body":{"source":{"name":"Obj.zam","path":"std://lang/Obj.zam"}}}`,
		`{"seq":2,"type":"request","command":"source","arguments":{"source":{"sourceReference":1073741824}}}`,
		`{"seq":3,"type":"request","command":"launch","arguments":{"program":` + strconv.Quote(main) + `,"args":["a"],"stopOnEntry":true,"cwd":` + strconv.Quote(dir) + `}}`,
		`{"seq":4,"type":"request","command":"launch","arguments":{"program":` + strconv.Quote(broken) + `}}`,
		`{"seq":5,"type":"request","command":"threads"}`,
	}

	goIn, goEditor := io.Pipe()
	goReplies, goOut := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- debugger.Serve(&base.FileProvider{}, base.NewPackageProvider(t.TempDir()), echo, goIn, goOut)
		_ = goOut.Close()
	}()
	want := session(t, goEditor, bufio.NewReader(goReplies), script)
	require.NoError(t, goEditor.Close())
	require.NoError(t, <-served)

	cmd := exec.Command(zam, "debug")
	cmd.Env = append(os.Environ(), "ZAM_STD="+filepath.Join(root, "src", "std"), "ZAMPATH="+t.TempDir(), "ZAM_LLDB_DAP="+echo)
	zamEditor, err := cmd.StdinPipe()
	require.NoError(t, err)
	zamReplies, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	got := session(t, zamEditor, bufio.NewReader(zamReplies), script)
	require.NoError(t, zamEditor.Close())
	require.NoError(t, cmd.Wait(), stderr.String())

	assert.Equal(t, want, got)
}

// session writes each message of script to a proxy at editor, reading one
// reply to each from replies, and answers the replies with what differs
// between sessions by nature, where each builds its program and keeps zam's
// formatters, left out, and errors normalized as the parsers' are.
func session(t *testing.T, editor io.Writer, replies *bufio.Reader, script []string) []any {
	t.Helper()
	var got []any
	for _, message := range script {
		_, err := fmt.Fprintf(editor, "Content-Length: %d\r\n\r\n%s", len(message), message)
		require.NoError(t, err)
		body, err := readFramed(replies)
		require.NoError(t, err)
		var reply map[string]any
		require.NoError(t, json.Unmarshal(body, &reply))
		if launch, ok := reply["arguments"].(map[string]any); ok && reply["command"] == "launch" {
			program, _ := launch["program"].(string)
			launch["program"] = filepath.Base(program)
			if commands, ok := launch["initCommands"].([]any); ok && len(commands) == 1 {
				if command, _ := commands[0].(string); strings.HasSuffix(command, `zam_lldb.py"`) {
					commands[0] = "command script import zam_lldb.py"
				}
			}
		}
		if message, ok := reply["message"].(string); ok {
			reply["message"] = normalized(message)
			if body, ok := reply["body"].(map[string]any); ok {
				if failure, ok := body["error"].(map[string]any); ok {
					failure["format"] = normalized(message)
				}
			}
		}
		got = append(got, reply)
	}
	return got
}

// readFramed reads one message's body framed by a Content-Length header.
func readFramed(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if value, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			if length, err = strconv.Atoi(strings.TrimSpace(value)); err != nil {
				return nil, err
			}
		}
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}
