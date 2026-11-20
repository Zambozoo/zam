package debugger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
	"github.com/zambozoo/zam/internal/test"
)

// adapterVariable names the lldb-dap to debug through, in place of the one
// found on the path or among the developer tools.
const adapterVariable = "ZAM_LLDB_DAP"

// adapterPaths are where lldb-dap is looked for when it is not on the path.
var adapterPaths = []string{
	"/Library/Developer/CommandLineTools/usr/bin/lldb-dap",
	"/Applications/Xcode.app/Contents/Developer/usr/bin/lldb-dap",
	"/opt/homebrew/opt/llvm/bin/lldb-dap",
	"/usr/local/opt/llvm/bin/lldb-dap",
}

// sourceReferenceBase is where the references to sources not on disk the
// proxy answers for begin, above any lldb-dap hands out.
const sourceReferenceBase = 1 << 30

// Run serves the debug adapter protocol over stdin and stdout in front of
// lldb-dap: it compiles what an editor launches natively with debug info,
// hands lldb-dap the program, and passes everything else between the two.
func Run(files *base.FileProvider, packages *base.PackageProvider) error {
	adapter, err := findAdapter()
	if err != nil {
		return err
	}
	p, err := newProxy(files, packages, exec.Command(adapter))
	if err != nil {
		return err
	}
	return p.serve(os.Stdin, os.Stdout)
}

// findAdapter is the lldb-dap to debug through.
func findAdapter() (string, error) {
	if path := os.Getenv(adapterVariable); path != "" {
		return path, nil
	}
	if path, err := exec.LookPath("lldb-dap"); err == nil {
		return path, nil
	}
	for _, path := range adapterPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("lldb-dap not found: install the Xcode Command Line Tools or LLVM, or set $%s", adapterVariable)
}

// consoleTerminal is the console that runs the program in the editor's
// terminal.
const consoleTerminal = "integratedTerminal"

// launchArguments are what an editor launches a program with.
type launchArguments struct {
	// Program is the program's entry file, or a test file to debug its tests.
	Program string `json:"program"`
	// Args are the arguments the program is run with.
	Args []string `json:"args"`
	// Cwd is the directory the program is run in, the debugger's own if
	// empty.
	Cwd string `json:"cwd"`
	// StopOnEntry stops the program where main begins.
	StopOnEntry bool `json:"stopOnEntry"`
	// Test picks by a regular expression the tests of a test file that run,
	// as zam test's --run does.
	Test string `json:"test"`
	// Console is integratedTerminal to run the program in the editor's
	// terminal, where it reads what is typed, rather than writing to the
	// debug console.
	Console string `json:"console"`
	// NoDebug runs the program without the debugger: nothing stops it.
	NoDebug bool `json:"noDebug"`
}

// proxy is one debugging session passed between an editor and lldb-dap.
type proxy struct {
	files       *base.FileProvider
	packages    *base.PackageProvider
	adapter     *exec.Cmd
	toAdapter   io.WriteCloser
	fromAdapter *bufio.Reader

	// editorLock orders what is written to the editor, and adapterLock what
	// is written to lldb-dap.
	editorLock  sync.Mutex
	adapterLock sync.Mutex

	// dir holds what the proxy builds for the session.
	dir string

	// sources are the files not on disk the editor has been shown, by the
	// reference past sourceReferenceBase it asks for them by.
	sourcesLock sync.Mutex
	sources     []string
}

func newProxy(files *base.FileProvider, packages *base.PackageProvider, adapter *exec.Cmd) (*proxy, error) {
	dir, err := os.MkdirTemp("", "zam-debug-*")
	if err != nil {
		return nil, fmt.Errorf("cannot create debug directory: %w", err)
	}
	in, err := adapter.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := adapter.StdoutPipe()
	if err != nil {
		return nil, err
	}
	adapter.Stderr = os.Stderr
	if err := adapter.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cannot start %s: %w", adapter.Path, err)
	}
	return &proxy{files: files, packages: packages, adapter: adapter, toAdapter: in,
		fromAdapter: bufio.NewReader(out), dir: dir}, nil
}

// serve passes messages between the editor, on in and out, and lldb-dap until
// either ends.
func (p *proxy) serve(in io.Reader, out io.Writer) error {
	defer func() { _ = os.RemoveAll(p.dir) }()
	done := make(chan error, 1)
	go func() { done <- p.fromLLDB(out) }()

	err := p.fromEditor(bufio.NewReader(in), out)
	_ = p.toAdapter.Close()
	if adapterErr := <-done; err == nil {
		err = adapterErr
	}
	_ = p.adapter.Wait()
	return err
}

// fromEditor passes the editor's messages to lldb-dap, compiling what a launch
// asks for and answering for the sources only the proxy can read.
func (p *proxy) fromEditor(in *bufio.Reader, out io.Writer) error {
	for {
		body, err := readMessage(in)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var request struct {
			Seq       int             `json:"seq"`
			Type      string          `json:"type"`
			Command   string          `json:"command"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			return fmt.Errorf("cannot read a message from the editor: %w", err)
		}
		if request.Type == "request" {
			switch request.Command {
			case "launch":
				body, err = p.launch(body, request.Arguments)
				if err != nil {
					p.toEditor(out, errorResponse(request.Seq, request.Command, err))
					continue
				}
			case "source":
				if text, ok, err := p.source(request.Arguments); ok {
					if err != nil {
						p.toEditor(out, errorResponse(request.Seq, request.Command, err))
					} else {
						p.toEditor(out, response(request.Seq, request.Command, map[string]any{"content": text}))
					}
					continue
				}
			}
		}
		p.adapterLock.Lock()
		err = writeMessage(p.toAdapter, body)
		p.adapterLock.Unlock()
		if err != nil {
			return err
		}
	}
}

// fromLLDB passes lldb-dap's messages to the editor, naming each source not
// on disk by a reference the proxy answers for.
func (p *proxy) fromLLDB(out io.Writer) error {
	for {
		body, err := readMessage(p.fromAdapter)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var message any
		if err := json.Unmarshal(body, &message); err == nil && p.referSources(message) {
			if rewritten, err := json.Marshal(message); err == nil {
				body = rewritten
			}
		}
		p.toEditor(out, body)
	}
}

// toEditor writes one message to the editor.
func (p *proxy) toEditor(out io.Writer, body []byte) {
	p.editorLock.Lock()
	defer p.editorLock.Unlock()
	_ = writeMessage(out, body)
}

// launch compiles the program the editor's launch request asks for and
// answers the request lldb-dap is sent in its place.
func (p *proxy) launch(body []byte, arguments json.RawMessage) ([]byte, error) {
	var args launchArguments
	if err := json.Unmarshal(arguments, &args); err != nil {
		return nil, fmt.Errorf("cannot read launch arguments: %w", err)
	}
	if args.Program == "" {
		return nil, errors.New("launch needs a program")
	}

	object, project, runArgs, mainName, err := p.compile(args)
	if err != nil {
		return nil, err
	}
	opts := build.Options{Project: project, Debugger: true}
	binary, err := build.ExecutablePath(opts, filepath.Join(p.dir, strings.TrimSuffix(filepath.Base(args.Program), filepath.Ext(args.Program))))
	if err != nil {
		return nil, err
	}
	opts.OutputPath = binary
	if err := build.LinkObject(object.Bytes, opts); err != nil {
		return nil, err
	}
	formatters := filepath.Join(p.dir, "zam_lldb.py")
	if err := os.WriteFile(formatters, build.LLDBFormatters(), 0o644); err != nil {
		return nil, err
	}

	launch := map[string]any{
		"program":      binary,
		"args":         runArgs,
		"initCommands": []string{"command script import " + strconv.Quote(formatters)},
	}
	if args.Cwd != "" {
		launch["cwd"] = args.Cwd
	}
	if args.StopOnEntry {
		// lldb-dap's own stops before the runtime has started; the program's
		// entry is where its main begins.
		launch["preRunCommands"] = []string{"breakpoint set --one-shot true --name " + mainName}
	}
	if args.Console == consoleTerminal {
		launch["runInTerminal"] = true
	}
	if args.NoDebug {
		launch["noDebug"] = true
	}

	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	request["arguments"] = launch
	return json.Marshal(request)
}

// compile compiles the program, or the tests of the test file, args launch
// natively for debugging, answering it, its project, the arguments it is run
// with and the name of its main.
func (p *proxy) compile(args launchArguments) (*codegen.Object, *syntax.Project, []string, string, error) {
	debug := codegen.ObjectOptions{Debug: true}
	if strings.HasSuffix(args.Program, base.TestFileSuffix) {
		argv := []string{"zam", "test"}
		if args.Test != "" {
			argv = append(argv, "--run", args.Test)
		}
		argv = append(argv, args.Program)
		if len(args.Args) > 0 {
			argv = append(append(argv, "--"), args.Args...)
		}
		cmd, err := cli.New(argv, p.files)
		if err != nil {
			return nil, nil, nil, "", err
		}
		object, project, _, mainName, err := test.Object(cmd, p.files, p.packages, debug)
		if err != nil {
			return nil, nil, nil, "", err
		}
		return object, project, append([]string{"0"}, cmd.RunArgs...), mainName, nil
	}

	cmd, err := cli.New(append([]string{"zam", "run", args.Program}, args.Args...), p.files)
	if err != nil {
		return nil, nil, nil, "", err
	}
	ast, errs := syntax.NewAST(cmd, p.files, p.packages)
	if len(errs) > 0 {
		return nil, nil, nil, "", errors.Join(errs...)
	}
	if errs := semantic.ValidateAST(ast); len(errs) > 0 {
		return nil, nil, nil, "", errors.Join(errs...)
	}
	object, err := codegen.GenerateObject(cmd, ast, debug)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return object, ast.MainCodeDecl.File().Project, cmd.RunArgs, codegen.MainFunctionName(ast), nil
}

// referSources names, in message, each source whose path is a file only zam
// can read by a reference the editor asks the proxy for, and reports whether
// it named any.
func (p *proxy) referSources(message any) bool {
	changed := false
	switch m := message.(type) {
	case map[string]any:
		if path, ok := m["path"].(string); ok && base.Path(path).IsStd() {
			if _, hasName := m["name"]; hasName || m["sourceReference"] != nil {
				m["sourceReference"] = p.reference(path)
				m["presentationHint"] = "deemphasize"
				changed = true
			}
		}
		for _, v := range m {
			changed = p.referSources(v) || changed
		}
	case []any:
		for _, v := range m {
			changed = p.referSources(v) || changed
		}
	}
	return changed
}

// reference is the number the editor asks for the file at path by.
func (p *proxy) reference(path string) int {
	p.sourcesLock.Lock()
	defer p.sourcesLock.Unlock()
	for i, known := range p.sources {
		if known == path {
			return sourceReferenceBase + i
		}
	}
	p.sources = append(p.sources, path)
	return sourceReferenceBase + len(p.sources) - 1
}

// source answers the text of a source the editor asks for by a reference the
// proxy handed out, and reports whether it was one.
func (p *proxy) source(arguments json.RawMessage) (string, bool, error) {
	var args struct {
		SourceReference int `json:"sourceReference"`
		Source          struct {
			SourceReference int `json:"sourceReference"`
		} `json:"source"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return "", false, nil
	}
	reference := args.Source.SourceReference
	if reference == 0 {
		reference = args.SourceReference
	}
	p.sourcesLock.Lock()
	index := reference - sourceReferenceBase
	ok := index >= 0 && index < len(p.sources)
	var path string
	if ok {
		path = p.sources[index]
	}
	p.sourcesLock.Unlock()
	if !ok {
		return "", false, nil
	}
	r, err := p.files.Reader(base.Path(path))
	if err != nil {
		return "", true, err
	}
	defer func() { _ = r.Close() }()
	content, err := io.ReadAll(r)
	return string(content), true, err
}

// response is a successful response to the request seq of command.
func response(seq int, command string, body any) []byte {
	message, _ := json.Marshal(map[string]any{
		"seq": 0, "type": "response", "request_seq": seq, "command": command, "success": true, "body": body,
	})
	return message
}

// errorResponse is the response saying the request seq of command failed.
func errorResponse(seq int, command string, err error) []byte {
	message, _ := json.Marshal(map[string]any{
		"seq": 0, "type": "response", "request_seq": seq, "command": command, "success": false,
		"message": err.Error(),
		"body":    map[string]any{"error": map[string]any{"id": 1, "format": err.Error()}},
	})
	return message
}

// readMessage reads one message's body as the debug adapter protocol frames
// it: headers, a blank line, and Content-Length bytes.
func readMessage(r *bufio.Reader) ([]byte, error) {
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
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if length, err = strconv.Atoi(strings.TrimSpace(value)); err != nil {
				return nil, fmt.Errorf("bad Content-Length %q", value)
			}
		}
	}
	if length < 0 {
		return nil, errors.New("a message with no Content-Length")
	}
	body := make([]byte, length)
	_, err := io.ReadFull(r, body)
	return body, err
}

// writeMessage writes body framed as one message.
func writeMessage(w io.Writer, body []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}
