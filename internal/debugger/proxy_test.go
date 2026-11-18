package debugger

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	build "github.com/zambozoo/zam/internal/5_build"
)

// fakeAdapterVariable names, when set, the file the test binary run as a fake
// lldb-dap records what it is launched with in.
const fakeAdapterVariable = "ZAM_TEST_FAKE_LLDB_DAP"

// TestMain runs the test binary as the fake lldb-dap when asked to, and
// otherwise points native links at the packs built into the repository and
// shares Zig's caches across tests, unless the environment names others.
func TestMain(m *testing.M) {
	if record := os.Getenv(fakeAdapterVariable); record != "" {
		fakeAdapter(record)
		os.Exit(0)
	}
	if os.Getenv("ZAM_PACKS") == "" {
		packs, _ := filepath.Abs(filepath.Join("..", "..", "bin", "packs"))
		_ = os.Setenv("ZAM_PACKS", packs)
	}
	for _, variable := range []string{"ZIG_GLOBAL_CACHE_DIR", "ZIG_LOCAL_CACHE_DIR"} {
		if os.Getenv(variable) == "" {
			_ = os.Setenv(variable, filepath.Join(os.TempDir(), "zam-test-zig"))
		}
	}
	os.Exit(m.Run())
}

// fakeLaunch is what the fake lldb-dap records of a launch: its arguments and
// what lay at the paths they named.
type fakeLaunch struct {
	Arguments  map[string]any `json:"arguments"`
	Executable bool           `json:"executable"`
	Formatters string         `json:"formatters"`
}

// fakeAdapter answers as lldb-dap would the requests the tests send, recording
// a launch in the file at record.
func fakeAdapter(record string) {
	in := bufio.NewReader(os.Stdin)
	for {
		body, err := readMessage(in)
		if err != nil {
			return
		}
		var request struct {
			Seq       int            `json:"seq"`
			Command   string         `json:"command"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(body, &request)
		var answer any
		switch request.Command {
		case "initialize":
			answer = map[string]any{"supportsConfigurationDoneRequest": true}
		case "launch":
			launch := fakeLaunch{Arguments: request.Arguments}
			if program, ok := request.Arguments["program"].(string); ok {
				// Windows marks a program by its .exe, having no execute bits.
				info, err := os.Stat(program)
				launch.Executable = err == nil && (info.Mode()&0o111 != 0 ||
					runtime.GOOS == "windows" && strings.EqualFold(filepath.Ext(program), ".exe"))
			}
			if commands, ok := request.Arguments["initCommands"].([]any); ok && len(commands) == 1 {
				path := strings.Trim(strings.TrimPrefix(commands[0].(string), "command script import "), `"`)
				content, _ := os.ReadFile(path)
				launch.Formatters = string(content)
			}
			recorded, _ := json.Marshal(launch)
			_ = os.WriteFile(record, recorded, 0o600)
		case "stackTrace":
			answer = map[string]any{"stackFrames": []any{map[string]any{
				"id": 1, "name": "System.Panic", "line": 14, "column": 1,
				"source": map[string]any{"name": "System.zam", "path": "std://lang/System.zam"},
			}}}
		}
		_ = writeMessage(os.Stdout, response(request.Seq, request.Command, answer))
		if request.Command == "disconnect" {
			return
		}
	}
}

// proxyClient drives a proxy as an editor would, the proxy in front of the
// fake lldb-dap.
type proxyClient struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	seq    int
	record string
	done   chan error
}

func newProxyClient(t *testing.T) *proxyClient {
	t.Helper()
	t.Setenv("ZAM_CACHE_DIR", t.TempDir())
	record := filepath.Join(t.TempDir(), "launch.json")
	adapter := exec.Command(os.Args[0])
	adapter.Env = append(os.Environ(), fakeAdapterVariable+"="+record)
	p, err := newProxy(&base.FileProvider{}, base.NewPackageProvider(t.TempDir()), adapter)
	require.NoError(t, err)

	editorIn, toProxy := io.Pipe()
	fromProxy, editorOut := io.Pipe()
	c := &proxyClient{t: t, in: toProxy, out: bufio.NewReader(fromProxy), record: record, done: make(chan error, 1)}
	go func() {
		c.done <- p.serve(editorIn, editorOut)
		_ = editorOut.Close()
	}()
	t.Cleanup(func() {
		_ = toProxy.Close()
		assert.NoError(t, <-c.done)
	})
	return c
}

// request sends command with arguments and answers the response to it.
func (c *proxyClient) request(command string, arguments any) map[string]any {
	c.t.Helper()
	c.seq++
	body, err := json.Marshal(map[string]any{"seq": c.seq, "type": "request", "command": command, "arguments": arguments})
	require.NoError(c.t, err)
	require.NoError(c.t, writeMessage(c.in, body))
	for {
		reply, err := readMessage(c.out)
		require.NoError(c.t, err)
		var message map[string]any
		require.NoError(c.t, json.Unmarshal(reply, &message))
		if message["type"] == "response" && message["request_seq"] == float64(c.seq) {
			return message
		}
	}
}

// writeProject writes a one-file project and answers its entry file.
func writeProject(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"debugged\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(source), 0o600))
	return entry
}

// A launch of a zam program hands lldb-dap the program compiled natively, with
// its arguments, zam's formatters loaded and its entry at main; a source only
// zam can read is answered by the proxy.
func TestProxyLaunchesTheCompiledProgram(t *testing.T) {
	c := newProxyClient(t)
	entry := writeProject(t, `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        native(STD_SYSTEM_EXIT){void(int)}(0);
    }
}
`)
	assert.Equal(t, true, c.request("initialize", map[string]any{"adapterID": "zam"})["success"])
	launched := c.request("launch", map[string]any{"program": entry, "args": []string{"a"}, "stopOnEntry": true})
	require.Equal(t, true, launched["success"], launched["message"])

	recorded, err := os.ReadFile(c.record)
	require.NoError(t, err)
	var launch fakeLaunch
	require.NoError(t, json.Unmarshal(recorded, &launch))
	assert.True(t, launch.Executable, "%v", launch.Arguments["program"])
	assert.Equal(t, []any{"a"}, launch.Arguments["args"])
	assert.Equal(t, string(build.LLDBFormatters()), launch.Formatters)
	assert.Equal(t, []any{"breakpoint set --one-shot true --name Main.main"}, launch.Arguments["preRunCommands"])

	frames := c.request("stackTrace", map[string]any{"threadId": 1})["body"].(map[string]any)["stackFrames"].([]any)
	source := frames[0].(map[string]any)["source"].(map[string]any)
	reference, ok := source["sourceReference"].(float64)
	require.True(t, ok, "%v", source)
	content := c.request("source", map[string]any{"sourceReference": reference, "source": source})
	require.Equal(t, true, content["success"], content["message"])
	assert.Contains(t, content["body"].(map[string]any)["content"], "Panic")

	c.request("disconnect", map[string]any{})
}

// A launch of a program that does not compile fails, saying why, and lldb-dap
// is never asked to launch anything.
func TestProxyFailsALaunchThatDoesNotCompile(t *testing.T) {
	c := newProxyClient(t)
	entry := writeProject(t, `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var int x = "not an int";
    }
}
`)
	c.request("initialize", map[string]any{"adapterID": "zam"})
	launched := c.request("launch", map[string]any{"program": entry})
	assert.Equal(t, false, launched["success"])
	assert.NotEmpty(t, launched["message"])
	_, err := os.Stat(c.record)
	assert.True(t, os.IsNotExist(err), "lldb-dap was asked to launch")

	c.request("disconnect", map[string]any{})
}
