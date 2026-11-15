// Package debugger serves the debug adapter protocol, through which an editor
// runs a program under the debugger: stopping it at breakpoints, stepping it,
// and reading its threads' stacks.
package debugger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/google/go-dap"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
	"github.com/zambozoo/zam/internal/testrunner"
)

const (
	// mainThread is the id the runtime gives the thread a program begins on.
	mainThread = 1
	// failuresFilter names the stops the program makes as a thread fails.
	failuresFilter = "failures"
	// exceptionReason is why the program stops as a thread fails.
	exceptionReason = "exception"
)

type (
	// server is one debugging session: the editor on one side and, once it
	// has been launched, the program on the other.
	server struct {
		files    *base.FileProvider
		packages *base.PackageProvider

		// writeLock orders what is written to the editor, and seq numbers it.
		writeLock sync.Mutex
		out       io.Writer
		seq       int

		// lock guards what follows, which the program's stops read.
		lock    sync.Mutex
		program *program
		// breakpoints are where in the program each file's breakpoints stop
		// it, by the file's path, and rules what decides whether a stop at
		// one stands, by the file's path and then by the offset.
		breakpoints map[string][]uint64
		rules       map[string]map[uint64]*breakpointRule
		// stopOnEntry is whether the program stops where main begins, and
		// entryPending whether it has yet to.
		stopOnEntry  bool
		entryPending bool
		breakpointID int
		// references number the files not on disk the editor has been shown,
		// which it asks for by number.
		references []string
		// variables are the frames and values of the stop the program is at.
		variables variables
		// stoppedThread is the thread that stopped the program last, and
		// failure what it failed with, if that is why.
		stoppedThread int
		failure       string
		// stopOnFailure is whether a failing thread stops the program.
		stopOnFailure bool
		// runsInTerminal is whether the editor runs programs in its terminal
		// when asked.
		runsInTerminal bool
		// launched is what the program running was launched with, and
		// requested the breakpoints asked for in each file, by its path,
		// which a restart places again in what it compiles.
		launched  launchArguments
		requested map[string][]dap.SourceBreakpoint

		// pending are the requests sent to the editor awaiting its response,
		// by sequence number.
		pendingLock sync.Mutex
		pending     map[int]chan dap.ResponseMessage
	}

	// launchArguments are what an editor launches a program with.
	launchArguments struct {
		// Program is the program's entry file.
		Program string `json:"program"`
		// Args are the arguments the program is run with.
		Args []string `json:"args"`
		// Cwd is the directory the program is run in, the debugger's own if
		// empty.
		Cwd string `json:"cwd"`
		// StopOnEntry stops the program where main begins.
		StopOnEntry bool `json:"stopOnEntry"`
		// Test picks by a regular expression the tests of a test file that
		// run, as zam test's --run does.
		Test string `json:"test"`
		// Console is integratedTerminal to run the program in the editor's
		// terminal, where it reads what is typed, rather than writing to the
		// debug console.
		Console string `json:"console"`
		// NoDebug runs the program without the debugger: nothing stops it.
		NoDebug bool `json:"noDebug"`
	}
)

// Run serves the debug adapter protocol over stdin and stdout, resolving
// imports through files and packages.
func Run(files *base.FileProvider, packages *base.PackageProvider) error {
	return newServer(files, packages, os.Stdout).serve(os.Stdin)
}

func newServer(files *base.FileProvider, packages *base.PackageProvider, out io.Writer) *server {
	return &server{files: files, packages: packages, out: out, breakpoints: map[string][]uint64{},
		rules: map[string]map[uint64]*breakpointRule{}, requested: map[string][]dap.SourceBreakpoint{}, stopOnFailure: true, pending: map[int]chan dap.ResponseMessage{}}
}

// serve answers requests read from in until the editor disconnects or goes.
func (s *server) serve(in io.Reader) error {
	defer s.end()
	reader := bufio.NewReader(in)
	for {
		message, err := dap.ReadProtocolMessage(reader)
		if err != nil {
			var unsupported *dap.DecodeProtocolMessageFieldError
			if errors.As(err, &unsupported) {
				s.fail(unsupported.Seq, unsupported.FieldValue, err)
				continue
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if response, ok := message.(dap.ResponseMessage); ok {
			s.answered(response)
			continue
		}
		request, ok := message.(dap.RequestMessage)
		if !ok {
			continue
		}
		if err := s.handle(request); err != nil {
			s.fail(request.GetRequest().Seq, request.GetRequest().Command, err)
		}
		if _, ok := request.(*dap.DisconnectRequest); ok {
			return nil
		}
	}
}

// end ends the program, if there is one still running.
func (s *server) end() {
	if p := s.running(); p != nil {
		p.kill()
	}
}

func (s *server) running() *program {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.program
}

// handle answers request.
func (s *server) handle(request dap.RequestMessage) error {
	switch r := request.(type) {
	case *dap.InitializeRequest:
		s.lock.Lock()
		s.runsInTerminal = r.Arguments.SupportsRunInTerminalRequest
		s.lock.Unlock()
		s.send(&dap.InitializeResponse{Response: s.response(r), Body: dap.Capabilities{
			SupportsConfigurationDoneRequest:  true,
			SupportsTerminateRequest:          true,
			SupportsSetVariable:               true,
			SupportsRestartRequest:            true,
			SupportsEvaluateForHovers:         true,
			SupportsExceptionInfoRequest:      true,
			SupportsConditionalBreakpoints:    true,
			SupportsHitConditionalBreakpoints: true,
			SupportsLogPoints:                 true,
			ExceptionBreakpointFilters: []dap.ExceptionBreakpointsFilter{{
				Filter:      failuresFilter,
				Label:       "Failures",
				Description: "Stop when the program panics or the runtime fails",
				Default:     true,
			}},
		}})
	case *dap.LaunchRequest:
		// A launch in the editor's terminal waits on the editor's answer,
		// which this loop must stay free to read.
		go func() {
			if err := s.launch(r.Arguments); err != nil {
				s.fail(r.Seq, r.Command, err)
				return
			}
			s.send(&dap.LaunchResponse{Response: s.response(r)})
			s.send(&dap.InitializedEvent{Event: s.event("initialized")})
		}()
	case *dap.SetBreakpointsRequest:
		breakpoints, err := s.setBreakpoints(r.Arguments)
		if err != nil {
			return err
		}
		s.send(&dap.SetBreakpointsResponse{Response: s.response(r),
			Body: dap.SetBreakpointsResponseBody{Breakpoints: breakpoints}})
	case *dap.ConfigurationDoneRequest:
		if err := s.configurationDone(); err != nil {
			return err
		}
		s.send(&dap.ConfigurationDoneResponse{Response: s.response(r)})
	case *dap.ThreadsRequest:
		threads, err := s.threads()
		if err != nil {
			return err
		}
		s.send(&dap.ThreadsResponse{Response: s.response(r), Body: dap.ThreadsResponseBody{Threads: threads}})
	case *dap.StackTraceRequest:
		frames, err := s.stackTrace(r.Arguments.ThreadId)
		if err != nil {
			return err
		}
		total := len(frames)
		first := min(r.Arguments.StartFrame, total)
		last := total
		if r.Arguments.Levels > 0 {
			last = min(first+r.Arguments.Levels, total)
		}
		s.send(&dap.StackTraceResponse{Response: s.response(r),
			Body: dap.StackTraceResponseBody{StackFrames: frames[first:last], TotalFrames: total}})
	case *dap.SourceRequest:
		content, err := s.source(r.Arguments.SourceReference)
		if err != nil {
			return err
		}
		s.send(&dap.SourceResponse{Response: s.response(r),
			Body: dap.SourceResponseBody{Content: content, MimeType: "text/x-zam"}})
	case *dap.ScopesRequest:
		scopes, err := s.scopes(r.Arguments.FrameId)
		if err != nil {
			return err
		}
		s.send(&dap.ScopesResponse{Response: s.response(r), Body: dap.ScopesResponseBody{Scopes: scopes}})
	case *dap.VariablesRequest:
		children, err := s.expand(r.Arguments.VariablesReference)
		if err != nil {
			return err
		}
		s.send(&dap.VariablesResponse{Response: s.response(r),
			Body: dap.VariablesResponseBody{Variables: children}})
	case *dap.SetExceptionBreakpointsRequest:
		if err := s.setFailures(slices.Contains(r.Arguments.Filters, failuresFilter)); err != nil {
			return err
		}
		s.send(&dap.SetExceptionBreakpointsResponse{Response: s.response(r)})
	case *dap.ExceptionInfoRequest:
		s.lock.Lock()
		failure := s.failure
		s.lock.Unlock()
		if failure == "" {
			return errors.New("the program has not failed")
		}
		s.send(&dap.ExceptionInfoResponse{Response: s.response(r), Body: dap.ExceptionInfoResponseBody{
			ExceptionId: failuresFilter, Description: failure, BreakMode: "always",
		}})
	case *dap.EvaluateRequest:
		v, err := s.evaluate(r.Arguments.Expression, r.Arguments.FrameId)
		if err != nil {
			return err
		}
		s.send(&dap.EvaluateResponse{Response: s.response(r), Body: dap.EvaluateResponseBody{
			Result: v.Value, Type: v.Type, VariablesReference: v.VariablesReference,
		}})
	case *dap.SetVariableRequest:
		v, err := s.set(r.Arguments.VariablesReference, r.Arguments.Name, r.Arguments.Value)
		if err != nil {
			return err
		}
		s.send(&dap.SetVariableResponse{Response: s.response(r), Body: dap.SetVariableResponseBody{
			Value: v.Value, Type: v.Type, VariablesReference: v.VariablesReference,
		}})
	case *dap.ContinueRequest:
		if err := s.tell("continue"); err != nil {
			return err
		}
		s.send(&dap.ContinueResponse{Response: s.response(r),
			Body: dap.ContinueResponseBody{AllThreadsContinued: true}})
	case *dap.NextRequest:
		if err := s.step(r.Arguments.ThreadId, "over"); err != nil {
			return err
		}
		s.send(&dap.NextResponse{Response: s.response(r)})
	case *dap.StepInRequest:
		if err := s.step(r.Arguments.ThreadId, "in"); err != nil {
			return err
		}
		s.send(&dap.StepInResponse{Response: s.response(r)})
	case *dap.StepOutRequest:
		if err := s.step(r.Arguments.ThreadId, "out"); err != nil {
			return err
		}
		s.send(&dap.StepOutResponse{Response: s.response(r)})
	case *dap.PauseRequest:
		if err := s.tell("pause"); err != nil {
			return err
		}
		s.send(&dap.PauseResponse{Response: s.response(r)})
	case *dap.RestartRequest:
		go func() {
			if err := s.restart(r.Arguments); err != nil {
				s.fail(r.Seq, r.Command, err)
				return
			}
			s.send(&dap.RestartResponse{Response: s.response(r)})
		}()
	case *dap.TerminateRequest:
		s.end()
		s.send(&dap.TerminateResponse{Response: s.response(r)})
	case *dap.DisconnectRequest:
		s.end()
		s.send(&dap.DisconnectResponse{Response: s.response(r)})
	default:
		return fmt.Errorf("%s is not supported", request.GetRequest().Command)
	}
	return nil
}

// launch compiles the program arguments name and starts it, held before its
// first instruction until configuration is done.
func (s *server) launch(arguments json.RawMessage) error {
	var args launchArguments
	if err := json.Unmarshal(arguments, &args); err != nil {
		return fmt.Errorf("cannot read launch arguments: %w", err)
	}
	if args.Program == "" {
		return errors.New("launch needs a program")
	}
	if s.running() != nil {
		return errors.New("a program is already running")
	}

	compile := s.compileProgram
	if strings.HasSuffix(args.Program, base.TestFileSuffix) {
		compile = s.compileTests
	}
	result, project, runArgs, err := compile(args)
	if err != nil {
		return err
	}
	path, remove, err := build.Prepare(result.Image, result.NativeNames, build.Options{
		Project:  project,
		Debugger: true,
		Output:   outputWriter{category: "console", output: s.output},
	})
	if err != nil {
		return err
	}

	s.lock.Lock()
	terminal := args.Console == consoleTerminal && s.runsInTerminal
	s.lock.Unlock()
	var p *program
	if terminal {
		p, err = s.startInTerminal(path, runArgs, args.Cwd, "Zam: "+filepath.Base(args.Program), result.Debug, remove)
	} else {
		p, err = start(path, runArgs, args.Cwd, result.Debug, remove, s.output, s.stopped, s.exited)
		if err != nil {
			remove()
		}
	}
	if err != nil {
		return err
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	s.program = p
	s.launched = args
	s.stopOnEntry = args.StopOnEntry
	return p.failures(s.stopOnFailure)
}

// compileProgram compiles the program args launch, answering it, its project
// and the arguments it is run with.
func (s *server) compileProgram(args launchArguments) (*codegen.Result, *syntax.Project, []string, error) {
	cmd, err := command.New(append([]string{"zam", "run", args.Program}, args.Args...), s.files)
	if err != nil {
		return nil, nil, nil, err
	}
	ast, errs := syntax.NewAST(cmd, s.files, s.packages)
	if len(errs) > 0 {
		return nil, nil, nil, errors.Join(errs...)
	}
	if errs := semantic.ValidateAST(ast); len(errs) > 0 {
		return nil, nil, nil, errors.Join(errs...)
	}
	result, err := codegen.GenerateCode(cmd, ast)
	if err != nil {
		return nil, nil, nil, err
	}
	return result, ast.MainCodeDecl.File().Project, cmd.RunArgs, nil
}

// compileTests compiles the tests of the test file args launch, as zam test
// would, answering them, their project and the arguments they are run with:
// the file's index among the runner's, and then the arguments each test is
// given.
func (s *server) compileTests(args launchArguments) (*codegen.Result, *syntax.Project, []string, error) {
	argv := []string{"zam", "test"}
	if args.Test != "" {
		argv = append(argv, "--run", args.Test)
	}
	argv = append(argv, args.Program)
	if len(args.Args) > 0 {
		argv = append(append(argv, "--"), args.Args...)
	}
	cmd, err := command.New(argv, s.files)
	if err != nil {
		return nil, nil, nil, err
	}
	result, project, _, err := testrunner.Compile(cmd, s.files, s.packages)
	if err != nil {
		return nil, nil, nil, err
	}
	return result, project, append([]string{"0"}, cmd.RunArgs...), nil
}

// setBreakpoints replaces the breakpoints in a file with those asked for,
// each moved to the first line from it that has code.
func (s *server) setBreakpoints(args dap.SetBreakpointsArguments) ([]dap.Breakpoint, error) {
	path, err := base.ParsePath(args.Source.Path)
	if err != nil {
		return nil, err
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	s.requested[path.String()] = args.Breakpoints
	return s.place(path.String(), args.Source, args.Breakpoints), s.sendBreakpoints()
}

// place places the breakpoints asked for in the file at path in the program,
// answering where each landed. The caller holds the lock.
func (s *server) place(path string, source dap.Source, requests []dap.SourceBreakpoint) []dap.Breakpoint {
	var offsets []uint64
	rules := map[uint64]*breakpointRule{}
	breakpoints := make([]dap.Breakpoint, 0, len(requests))
	for _, requested := range requests {
		s.breakpointID++
		breakpoint := dap.Breakpoint{Id: s.breakpointID, Line: requested.Line, Source: &source}
		if s.program != nil {
			found, line := s.program.debug.Breakpoints(path, requested.Line)
			breakpoint.Verified, breakpoint.Line = len(found) > 0, line
			offsets = append(offsets, found...)
			if rule := newBreakpointRule(requested); rule != nil {
				for _, offset := range found {
					rules[offset] = rule
				}
			}
		}
		if !breakpoint.Verified {
			breakpoint.Message = "no code at or after this line"
		}
		breakpoints = append(breakpoints, breakpoint)
	}
	s.breakpoints[path] = offsets
	s.rules[path] = rules
	return breakpoints
}

// sendBreakpoints tells the program every breakpoint. The caller holds the
// lock.
func (s *server) sendBreakpoints() error {
	if s.program == nil {
		return nil
	}
	var all []uint64
	for _, offsets := range s.breakpoints {
		all = append(all, offsets...)
	}
	if s.entryPending {
		all = append(all, s.program.debug.Main)
	}
	slices.Sort(all)
	return s.program.breakpoints(slices.Compact(all))
}

// setFailures says whether a failing thread stops the program, now and in a
// program launched later.
func (s *server) setFailures(stop bool) error {
	s.lock.Lock()
	s.stopOnFailure = stop
	p := s.program
	s.lock.Unlock()
	if p == nil {
		return nil
	}
	return p.failures(stop)
}

// configurationDone lets the program go, now that its breakpoints are set.
func (s *server) configurationDone() error {
	s.lock.Lock()
	p := s.program
	if p == nil {
		s.lock.Unlock()
		return errors.New("no program has been launched")
	}
	if s.launched.NoDebug {
		s.lock.Unlock()
		p.detach()
		return nil
	}
	s.entryPending = s.stopOnEntry
	err := s.sendBreakpoints()
	s.lock.Unlock()
	if err != nil {
		return err
	}
	return p.send("continue")
}

// restart ends the program running, without the editor being told, and
// launches it again with args, or as it was launched if there are none: built
// from the source as it is now, with every breakpoint placed in it again.
func (s *server) restart(arguments json.RawMessage) error {
	s.lock.Lock()
	args := s.launched
	p := s.program
	s.lock.Unlock()
	var restart struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &restart); err != nil {
			return fmt.Errorf("cannot read restart arguments: %w", err)
		}
	}
	if len(restart.Arguments) == 0 {
		var err error
		if restart.Arguments, err = json.Marshal(args); err != nil {
			return err
		}
	}

	if p != nil {
		p.silent.Store(true)
		p.kill()
		s.lock.Lock()
		s.program = nil
		s.lock.Unlock()
	}
	if err := s.launch(restart.Arguments); err != nil {
		return err
	}

	s.lock.Lock()
	for path, requests := range s.requested {
		s.place(path, dap.Source{Name: filepath.Base(path), Path: path}, requests)
	}
	s.lock.Unlock()
	return s.configurationDone()
}

func (s *server) threads() ([]dap.Thread, error) {
	p := s.running()
	if p == nil {
		return []dap.Thread{}, nil
	}
	ids, err := p.threads()
	if errors.Is(err, errExited) {
		return []dap.Thread{}, nil
	}
	if err != nil {
		return nil, err
	}
	threads := make([]dap.Thread, 0, len(ids))
	for _, id := range ids {
		name := "thread " + strconv.FormatUint(id, 10)
		if id == mainThread {
			name = "main"
		}
		threads = append(threads, dap.Thread{Id: int(id), Name: name})
	}
	slices.SortFunc(threads, func(a, b dap.Thread) int { return a.Id - b.Id })
	return threads, nil
}

func (s *server) stackTrace(thread int) ([]dap.StackFrame, error) {
	p := s.running()
	if p == nil {
		return nil, errors.New("no program is running")
	}
	stack, err := p.stack(thread)
	if err != nil {
		return nil, err
	}
	frames := make([]dap.StackFrame, 0, len(stack))
	for _, f := range stack {
		s.lock.Lock()
		frame := dap.StackFrame{Id: s.variables.frame(f), Name: "?", Column: 1}
		s.lock.Unlock()
		if name, ok := p.debug.Function(f.offset); ok {
			frame.Name = name
		}
		if file, line, ok := p.debug.Locate(f.offset); ok {
			frame.Line, frame.Source = line, s.describe(file)
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// describe names file to the editor. A file of the standard library is not on
// disk, and is given a reference the editor asks for its text by.
func (s *server) describe(file string) *dap.Source {
	if !base.Path(file).IsStd() {
		return &dap.Source{Name: filepath.Base(file), Path: file}
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	i := slices.Index(s.references, file)
	if i < 0 {
		i = len(s.references)
		s.references = append(s.references, file)
	}
	return &dap.Source{Name: filepath.Base(file), Path: file, SourceReference: i + 1,
		PresentationHint: "deemphasize"}
}

// source answers the text of the file numbered reference.
func (s *server) source(reference int) (string, error) {
	s.lock.Lock()
	ok := reference > 0 && reference <= len(s.references)
	var file string
	if ok {
		file = s.references[reference-1]
	}
	s.lock.Unlock()
	if !ok {
		return "", fmt.Errorf("no source numbered %d", reference)
	}
	r, err := s.files.Reader(base.Path(file))
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	content, err := io.ReadAll(r)
	return string(content), err
}

func (s *server) step(thread int, how string) error {
	return s.tell("step " + strconv.Itoa(thread) + " " + how)
}

// tell sends command to the program.
func (s *server) tell(command string) error {
	p := s.running()
	if p == nil {
		return errors.New("no program is running")
	}
	return p.send(command)
}

// stopped tells the editor the program has stopped.
func (s *server) stopped(st stop) {
	reason := st.reason
	s.lock.Lock()
	s.variables = variables{}
	s.stoppedThread = st.thread
	s.failure = strings.TrimRight(st.failure, "\n")
	var rule *breakpointRule
	if reason == "breakpoint" {
		rule = s.ruleAt(st.offset)
	}
	if s.entryPending {
		if st.offset == s.program.debug.Main {
			reason = "entry"
		}
		s.entryPending = false
		_ = s.sendBreakpoints()
	}
	s.lock.Unlock()
	if rule != nil && !s.stands(rule) {
		_ = s.tell("continue")
		return
	}
	body := dap.StoppedEventBody{Reason: reason, ThreadId: st.thread, AllThreadsStopped: true}
	if reason == exceptionReason {
		body.Description, body.Text = "Failure", strings.TrimRight(st.failure, "\n")
	}
	s.send(&dap.StoppedEvent{Event: s.event("stopped"), Body: body})
}

// exited tells the editor the program has ended.
func (s *server) exited(code int) {
	s.lock.Lock()
	s.program = nil
	s.lock.Unlock()
	s.send(&dap.ExitedEvent{Event: s.event("exited"), Body: dap.ExitedEventBody{ExitCode: code}})
	s.send(&dap.TerminatedEvent{Event: s.event("terminated")})
}

// output tells the editor of text the program wrote.
func (s *server) output(category, text string) {
	s.send(&dap.OutputEvent{Event: s.event("output"), Body: dap.OutputEventBody{Category: category, Output: text}})
}

func (s *server) nextSeq() int {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	s.seq++
	return s.seq
}

func (s *server) response(request dap.RequestMessage) dap.Response {
	r := request.GetRequest()
	return dap.Response{
		ProtocolMessage: dap.ProtocolMessage{Seq: s.nextSeq(), Type: "response"},
		RequestSeq:      r.Seq,
		Success:         true,
		Command:         r.Command,
	}
}

func (s *server) event(name string) dap.Event {
	return dap.Event{ProtocolMessage: dap.ProtocolMessage{Seq: s.nextSeq(), Type: "event"}, Event: name}
}

// fail answers the request numbered seq with err.
func (s *server) fail(seq int, command string, err error) {
	s.send(&dap.ErrorResponse{
		Response: dap.Response{
			ProtocolMessage: dap.ProtocolMessage{Seq: s.nextSeq(), Type: "response"},
			RequestSeq:      seq,
			Command:         command,
			Message:         err.Error(),
		},
		Body: dap.ErrorResponseBody{Error: &dap.ErrorMessage{Id: 1, Format: err.Error(), ShowUser: true}},
	})
}

func (s *server) send(message dap.Message) {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	_ = dap.WriteProtocolMessage(s.out, message)
}
