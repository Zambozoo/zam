package debugger

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-dap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
)

// debugSource lays out lines the tests below stop on and step through. Run
// with an argument it starts a thread, and with two it then spins forever.
const debugSource = `import std("io/local/*");

namespace Main {
    fun int(int) Twice = (x) {
        var int y = x;
        return y + y;
    }

    fun int(int) Spin = (n) {
        for(var int i = 0; i < 1; i = 0) {
        }
        return n;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int a = Main::Twice(1);

        var int b = Main::Twice(a);
        File::StdOut.WriteStr(b{str} + "\n");
        if (argC > 0U) {
            var thread[int] t = async(Main::Twice(5));
            wait(t);
        }
        if (argC > 1U) {
            Main::Spin(1);
        }
    }
}
`

// timeout is how long a test waits on the adapter, which compiles the program
// and the runtime under it before it answers a launch.
const timeout = 3 * time.Minute

// client drives a server as an editor would.
type client struct {
	t        *testing.T
	out      io.Writer
	seq      int
	messages chan dap.Message
	pending  []dap.Message
	// path is the program's entry file.
	path string
}

// connect starts a server for a client to drive.
func connect(t *testing.T) *client {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	s := newServer(&base.FileProvider{}, base.NewPackageProvider(t.TempDir()), serverOut)
	served := make(chan error, 1)
	go func() {
		served <- s.serve(serverIn)
		_ = serverOut.Close()
	}()

	c := &client{t: t, out: clientOut, messages: make(chan dap.Message, 64)}
	go func() {
		defer close(c.messages)
		reader := bufio.NewReader(clientIn)
		for {
			message, err := dap.ReadProtocolMessage(reader)
			if err != nil {
				return
			}
			c.messages <- message
		}
	}()
	t.Cleanup(func() {
		_ = clientOut.Close()
		assert.NoError(t, <-served)
	})
	return c
}

// project writes a project whose entry file holds source, answering its path.
func project(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"debug\"\n"), 0o600))
	path := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(path, []byte(source), 0o600))
	return path
}

// launch asks for the program at path to be launched with args.
func (c *client) launch(path string, stopOnEntry bool, args ...string) dap.ResponseMessage {
	c.t.Helper()
	c.path = path
	arguments, err := json.Marshal(launchArguments{Program: path, Args: args, StopOnEntry: stopOnEntry})
	require.NoError(c.t, err)
	return c.send(&dap.LaunchRequest{Request: request("launch"), Arguments: arguments})
}

// session launches debugSource with args, and answers once the program is
// waiting to be configured.
func session(t *testing.T, stopOnEntry bool, args ...string) *client {
	t.Helper()
	return sessionOf(t, debugSource, stopOnEntry, args...)
}

// sessionOf is [session] for a program of source.
func sessionOf(t *testing.T, source string, stopOnEntry bool, args ...string) *client {
	t.Helper()
	c := connect(t)
	c.call(&dap.InitializeRequest{Request: request("initialize"),
		Arguments: dap.InitializeRequestArguments{AdapterID: "zam", LinesStartAt1: true, ColumnsStartAt1: true}})
	response := c.launch(project(t, source), stopOnEntry, args...)
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	c.await(func(m dap.Message) bool { _, ok := m.(*dap.InitializedEvent); return ok })
	return c
}

func request(command string) dap.Request {
	return dap.Request{ProtocolMessage: dap.ProtocolMessage{Type: "request"}, Command: command}
}

// await answers the first message that matches, holding on to the others.
func (c *client) await(match func(dap.Message) bool) dap.Message {
	c.t.Helper()
	if i := slices.IndexFunc(c.pending, match); i >= 0 {
		m := c.pending[i]
		c.pending = slices.Delete(c.pending, i, i+1)
		return m
	}
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-c.messages:
			require.True(c.t, ok, "the adapter stopped sending")
			if match(m) {
				return m
			}
			c.pending = append(c.pending, m)
		case <-deadline:
			require.FailNow(c.t, "timed out waiting on the adapter")
		}
	}
}

// send sends r, answering its response.
func (c *client) send(r dap.RequestMessage) dap.ResponseMessage {
	c.t.Helper()
	c.seq++
	r.GetRequest().Seq = c.seq
	require.NoError(c.t, dap.WriteProtocolMessage(c.out, r))
	seq := c.seq
	return c.await(func(m dap.Message) bool {
		response, ok := m.(dap.ResponseMessage)
		return ok && response.GetResponse().RequestSeq == seq
	}).(dap.ResponseMessage)
}

// call sends r, requiring that it succeeds.
func (c *client) call(r dap.RequestMessage) dap.ResponseMessage {
	c.t.Helper()
	response := c.send(r)
	require.True(c.t, response.GetResponse().Success, "%s failed: %s", r.GetRequest().Command, response.GetResponse().Message)
	return response
}

func (c *client) breakpoints(lines ...int) []dap.Breakpoint {
	c.t.Helper()
	var requested []dap.SourceBreakpoint
	for _, line := range lines {
		requested = append(requested, dap.SourceBreakpoint{Line: line})
	}
	response := c.call(&dap.SetBreakpointsRequest{Request: request("setBreakpoints"),
		Arguments: dap.SetBreakpointsArguments{Source: dap.Source{Path: c.path}, Breakpoints: requested}})
	return response.(*dap.SetBreakpointsResponse).Body.Breakpoints
}

func (c *client) configurationDone() {
	c.t.Helper()
	c.call(&dap.ConfigurationDoneRequest{Request: request("configurationDone")})
}

func (c *client) stopped() dap.StoppedEventBody {
	c.t.Helper()
	return c.await(func(m dap.Message) bool { _, ok := m.(*dap.StoppedEvent); return ok }).(*dap.StoppedEvent).Body
}

// stack answers thread's frames as the function and line each stands at.
func (c *client) stack(thread int) []string {
	c.t.Helper()
	response := c.call(&dap.StackTraceRequest{Request: request("stackTrace"),
		Arguments: dap.StackTraceArguments{ThreadId: thread}})
	var frames []string
	for _, f := range response.(*dap.StackTraceResponse).Body.StackFrames {
		frame := f.Name
		if f.Source != nil {
			frame += " " + filepath.Base(f.Source.Path) + ":" + strconv.Itoa(f.Line)
		}
		frames = append(frames, frame)
	}
	return frames
}

func (c *client) resume() {
	c.t.Helper()
	c.call(&dap.ContinueRequest{Request: request("continue"), Arguments: dap.ContinueArguments{ThreadId: mainThread}})
}

// exited waits for the program to end, answering its status and what it wrote
// to its standard output.
func (c *client) exited() (int, string) {
	c.t.Helper()
	code := c.await(func(m dap.Message) bool { _, ok := m.(*dap.ExitedEvent); return ok }).(*dap.ExitedEvent).Body.ExitCode
	c.await(func(m dap.Message) bool { _, ok := m.(*dap.TerminatedEvent); return ok })
	var stdout string
	for _, m := range c.pending {
		if output, ok := m.(*dap.OutputEvent); ok && output.Body.Category == "stdout" {
			stdout += output.Body.Output
		}
	}
	return code, stdout
}

func TestBreakpointStopsAndShowsTheStack(t *testing.T) {
	t.Parallel()
	c := session(t, false)

	breakpoints := c.breakpoints(5)
	require.Len(t, breakpoints, 1)
	assert.True(t, breakpoints[0].Verified)
	assert.Equal(t, 5, breakpoints[0].Line)
	c.configurationDone()

	stopped := c.stopped()
	assert.Equal(t, "breakpoint", stopped.Reason)
	assert.Equal(t, mainThread, stopped.ThreadId)
	assert.Equal(t, []string{"Main.Twice Main.zam:5", "Main.main Main.zam:16"}, c.stack(mainThread))

	c.resume()
	c.stopped()
	assert.Equal(t, []string{"Main.Twice Main.zam:5", "Main.main Main.zam:18"}, c.stack(mainThread))

	c.resume()
	code, stdout := c.exited()
	assert.Equal(t, 0, code)
	assert.Equal(t, "4\n", stdout)
}

func TestSteppingMovesByLineAndCall(t *testing.T) {
	t.Parallel()
	c := session(t, false)

	breakpoints := c.breakpoints(17)
	require.Len(t, breakpoints, 1)
	assert.Equal(t, 18, breakpoints[0].Line, "a line with no code moves to the next that has some")
	c.configurationDone()
	c.stopped()
	assert.Equal(t, []string{"Main.main Main.zam:18"}, c.stack(mainThread))

	c.call(&dap.StepInRequest{Request: request("stepIn"), Arguments: dap.StepInArguments{ThreadId: mainThread}})
	assert.Equal(t, "step", c.stopped().Reason)
	frames := c.stack(mainThread)
	require.Len(t, frames, 2)
	assert.Regexp(t, `^Main\.Twice Main\.zam:[45]$`, frames[0])

	for i := 0; i < 3 && c.stack(mainThread)[0] != "Main.Twice Main.zam:6"; i++ {
		c.call(&dap.NextRequest{Request: request("next"), Arguments: dap.NextArguments{ThreadId: mainThread}})
		c.stopped()
	}
	assert.Equal(t, []string{"Main.Twice Main.zam:6", "Main.main Main.zam:18"}, c.stack(mainThread))

	c.call(&dap.StepOutRequest{Request: request("stepOut"), Arguments: dap.StepOutArguments{ThreadId: mainThread}})
	c.stopped()
	assert.Equal(t, []string{"Main.main Main.zam:19"}, c.stack(mainThread),
		"the call was the last of its line, so stepping out of it ends on the next")

	c.call(&dap.NextRequest{Request: request("next"), Arguments: dap.NextArguments{ThreadId: mainThread}})
	c.stopped()
	assert.Equal(t, []string{"Main.main Main.zam:20"}, c.stack(mainThread))

	c.resume()
	code, _ := c.exited()
	assert.Equal(t, 0, code)
}

func TestSteppingIntoStdShowsItsSource(t *testing.T) {
	t.Parallel()
	c := session(t, false)

	c.breakpoints(19)
	c.configurationDone()
	c.stopped()
	c.call(&dap.StepInRequest{Request: request("stepIn"), Arguments: dap.StepInArguments{ThreadId: mainThread}})
	c.stopped()

	response := c.call(&dap.StackTraceRequest{Request: request("stackTrace"),
		Arguments: dap.StackTraceArguments{ThreadId: mainThread}})
	top := response.(*dap.StackTraceResponse).Body.StackFrames[0]
	require.NotNil(t, top.Source)
	assert.True(t, base.Path(top.Source.Path).IsStd(), "%s is in std", top.Source.Path)
	require.Positive(t, top.Source.SourceReference, "a file not on disk is fetched by reference")

	content := c.call(&dap.SourceRequest{Request: request("source"),
		Arguments: dap.SourceArguments{Source: top.Source, SourceReference: top.Source.SourceReference}})
	text := content.(*dap.SourceResponse).Body.Content
	lines := strings.Split(text, "\n")
	require.Greater(t, len(lines), top.Line-1)
	assert.NotEmpty(t, strings.TrimSpace(lines[top.Line-1]), "the line stopped at has code")

	c.resume()
	code, _ := c.exited()
	assert.Equal(t, 0, code)
}

func TestThreadsAndPause(t *testing.T) {
	t.Parallel()
	c := session(t, false, "async", "spin")

	c.breakpoints(5)
	c.configurationDone()
	c.stopped()
	c.resume()
	c.stopped()
	c.resume()

	stopped := c.stopped()
	assert.NotEqual(t, mainThread, stopped.ThreadId, "the third call is on a thread of its own")
	assert.Equal(t, "Main.Twice Main.zam:5", c.stack(stopped.ThreadId)[0])
	threads := c.call(&dap.ThreadsRequest{Request: request("threads")}).(*dap.ThreadsResponse).Body.Threads
	assert.Contains(t, threads, dap.Thread{Id: mainThread, Name: "main"})
	assert.Contains(t, threads, dap.Thread{Id: stopped.ThreadId, Name: "thread " + strconv.Itoa(stopped.ThreadId)})

	c.breakpoints()
	var top string
	for range 100 {
		c.resume()
		time.Sleep(10 * time.Millisecond)
		c.call(&dap.PauseRequest{Request: request("pause"), Arguments: dap.PauseArguments{ThreadId: mainThread}})
		assert.Equal(t, "pause", c.stopped().Reason)
		if top = c.stack(mainThread)[0]; top != "Main.main Main.zam:22" {
			break
		}
	}
	assert.Regexp(t, `^Main\.Spin Main\.zam:1[01]$`, top, "main waits on the thread, then spins")

	c.call(&dap.TerminateRequest{Request: request("terminate")})
	c.exited()
}

func TestStopOnEntryStopsWhereMainBegins(t *testing.T) {
	t.Parallel()
	c := session(t, true)

	c.configurationDone()
	stopped := c.stopped()
	assert.Equal(t, "entry", stopped.Reason)
	assert.Equal(t, []string{"Main.main Main.zam:15"}, c.stack(mainThread), "a body opens on the line it is declared on")

	c.resume()
	code, stdout := c.exited()
	assert.Equal(t, 0, code)
	assert.Equal(t, "4\n", stdout)
}

func TestLaunchFailsOnAProgramThatDoesNotCompile(t *testing.T) {
	t.Parallel()
	c := connect(t)
	response := c.launch(project(t, "namespace Main {\n"), false)
	assert.False(t, response.GetResponse().Success)
	assert.NotEmpty(t, response.GetResponse().Message)
}

// variablesSource holds a value of each kind the debugger reads, to be looked
// at from the lines below.
const variablesSource = `import std("io/local/*");

namespace Main {
    struct P {
        var int x;
        var str y;
    }

    class Node {
        var int n;
        var str name;

        fun int(int) Add = (k) {
            return this.n + k;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var int i = 7;
        var str s = "hi";
        var bool b = true;
        var flt f = 1.5;
        var (int, str) t = 3, "t";
        var P p;
        p.x = 4;
        var int[3] xs;
        xs[1] = 9;
        var Node* node = new(Node);
        node.n = 5;
        node.name = "nd";
        var int sum = node.Add(1);
        var int() g = lambda int(): () {
            return i + 1;
        };
        {
            var int inner = 1;
        }
        File::StdOut.WriteStr((sum + g() + p.x + xs[1]){str} + s + "\n");
    }
}
`

// variables answers the variables a reference names, by name, as their
// values and, for those that expand, their members.
func (c *client) variables(reference int) map[string]dap.Variable {
	c.t.Helper()
	response := c.call(&dap.VariablesRequest{Request: request("variables"),
		Arguments: dap.VariablesArguments{VariablesReference: reference}})
	variables := map[string]dap.Variable{}
	for _, v := range response.(*dap.VariablesResponse).Body.Variables {
		variables[v.Name] = v
	}
	return variables
}

// locals answers the variables of thread's innermost frame.
func (c *client) locals(thread int) map[string]dap.Variable {
	c.t.Helper()
	trace := c.call(&dap.StackTraceRequest{Request: request("stackTrace"),
		Arguments: dap.StackTraceArguments{ThreadId: thread}}).(*dap.StackTraceResponse)
	require.NotEmpty(c.t, trace.Body.StackFrames)
	return c.scope(trace.Body.StackFrames[0].Id, "Locals")
}

// scope answers the variables of a frame's scope called name.
func (c *client) scope(frame int, name string) map[string]dap.Variable {
	c.t.Helper()
	scopes := c.call(&dap.ScopesRequest{Request: request("scopes"),
		Arguments: dap.ScopesArguments{FrameId: frame}}).(*dap.ScopesResponse)
	for _, scope := range scopes.Body.Scopes {
		if scope.Name == name {
			return c.variables(scope.VariablesReference)
		}
	}
	require.FailNow(c.t, "no scope "+name)
	return nil
}

// top answers the id of thread's innermost frame.
func (c *client) top(thread int) int {
	c.t.Helper()
	trace := c.call(&dap.StackTraceRequest{Request: request("stackTrace"),
		Arguments: dap.StackTraceArguments{ThreadId: thread}}).(*dap.StackTraceResponse)
	require.NotEmpty(c.t, trace.Body.StackFrames)
	return trace.Body.StackFrames[0].Id
}

// values answers each variable's value by its name.
func values(variables map[string]dap.Variable) map[string]string {
	values := map[string]string{}
	for name, v := range variables {
		values[name] = v.Value
	}
	return values
}

func TestVariablesShowEachKindOfValue(t *testing.T) {
	t.Parallel()
	c := sessionOf(t, variablesSource, false)
	c.breakpoints(14, 38, 33)
	c.configurationDone()

	c.stopped()
	locals := c.locals(mainThread)
	assert.Equal(t, "1", locals["k"].Value)
	require.Contains(t, locals, "this")
	assert.NotEqual(t, "null", locals["this"].Value)
	assert.Equal(t, map[string]string{"n": "5", "name": `"nd"`}, values(c.variables(locals["this"].VariablesReference)))

	c.resume()
	c.stopped()
	locals = c.locals(mainThread)
	assert.Equal(t, "7", locals["i"].Value)
	assert.Equal(t, "int", locals["i"].Type)
	assert.Equal(t, `"hi"`, locals["s"].Value)
	assert.Equal(t, "true", locals["b"].Value)
	assert.Equal(t, "1.5", locals["f"].Value)
	assert.Equal(t, "6", locals["sum"].Value)
	assert.Equal(t, "0", locals["argC"].Value)
	assert.NotContains(t, locals, "inner", "a variable is gone once its block is")

	assert.Equal(t, map[string]string{"0": "3", "1": `"t"`}, values(c.variables(locals["t"].VariablesReference)))
	assert.Equal(t, "4", c.variables(locals["p"].VariablesReference)["x"].Value)
	xs := c.variables(locals["xs"].VariablesReference)
	assert.Len(t, xs, 3)
	assert.Equal(t, "9", xs["[1]"].Value)
	assert.Equal(t, map[string]string{"n": "5", "name": `"nd"`}, values(c.variables(locals["node"].VariablesReference)))

	c.resume()
	c.stopped()
	locals = c.locals(mainThread)
	assert.Equal(t, map[string]string{"i": "7"}, values(locals), "a lambda's variables are its captures")

	c.resume()
	code, stdout := c.exited()
	assert.Equal(t, 0, code)
	assert.Equal(t, "27hi\n", stdout)
}

// genericSource has a generic class whose method is run for two of its
// instantiations.
const genericSource = `import std("io/local/*");

namespace Main {
    class Box[A] {
        var A v;
        var (A, int) pair;

        fun A() Get = () {
            var A x = this.v;
            var (A, int) p = this.pair;
            var int after = 3;
            return x;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Box[str]* bs = new(Box[str]);
        bs.v = "s";
        bs.pair.0 = "q";
        bs.pair.1 = 2;
        var Box[int]* bi = new(Box[int]);
        bi.v = 4;
        File::StdOut.WriteStr(bs.Get() + bi.Get(){str} + "\n");
    }
}
`

func TestVariablesInGenericCode(t *testing.T) {
	t.Parallel()
	c := sessionOf(t, genericSource, false)
	c.breakpoints(12, 23)
	c.configurationDone()

	c.stopped()
	locals := c.locals(mainThread)
	assert.Equal(t, `"s"`, c.variables(locals["bs"].VariablesReference)["v"].Value,
		"an instance of a generic class shows its instantiation's fields")
	assert.Equal(t, "4", c.variables(locals["bi"].VariablesReference)["v"].Value)

	c.resume()
	c.stopped()
	locals = c.locals(mainThread)
	assert.Equal(t, `"s"`, locals["x"].Value)
	assert.Equal(t, "str", locals["x"].Type)
	assert.Equal(t, map[string]string{"0": `"q"`, "1": "2"}, values(c.variables(locals["p"].VariablesReference)))
	assert.Equal(t, "3", locals["after"].Value, "a variable past one the instantiation sizes is read where it lies")
	assert.Equal(t, `"s"`, c.variables(locals["this"].VariablesReference)["v"].Value)

	c.resume()
	c.stopped()
	locals = c.locals(mainThread)
	assert.Equal(t, "4", locals["x"].Value)
	assert.Equal(t, "int", locals["x"].Type)
	assert.Equal(t, "3", locals["after"].Value)

	c.resume()
	code, stdout := c.exited()
	assert.Equal(t, 0, code)
	assert.Equal(t, "s4\n", stdout)
}

// positSource holds a posit, an int and a string to be read and set.
const positSource = `import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var pst q = 1.5P;
        var int n = 2;
        var str w = "a";
        File::StdOut.WriteStr(q{str} + " " + n{str} + " " + w + "\n");
    }
}
`

// set sets the variable called name among those reference names to value.
func (c *client) set(reference int, name, value string) dap.ResponseMessage {
	c.t.Helper()
	return c.send(&dap.SetVariableRequest{Request: request("setVariable"),
		Arguments: dap.SetVariableArguments{VariablesReference: reference, Name: name, Value: value}})
}

func TestVariablesCanBeSet(t *testing.T) {
	t.Parallel()
	c := sessionOf(t, positSource, false)
	c.breakpoints(8)
	c.configurationDone()
	c.stopped()

	trace := c.call(&dap.StackTraceRequest{Request: request("stackTrace"),
		Arguments: dap.StackTraceArguments{ThreadId: mainThread}}).(*dap.StackTraceResponse)
	scopes := c.call(&dap.ScopesRequest{Request: request("scopes"),
		Arguments: dap.ScopesArguments{FrameId: trace.Body.StackFrames[0].Id}}).(*dap.ScopesResponse)
	reference := scopes.Body.Scopes[0].VariablesReference

	locals := c.variables(reference)
	assert.Equal(t, "1.5", locals["q"].Value, "a posit shows as the decimal it is")
	assert.Equal(t, "pst", locals["q"].Type)

	response := c.set(reference, "q", "0.1")
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	assert.Equal(t, "0.1", response.(*dap.SetVariableResponse).Body.Value)

	for _, refused := range []string{"inf", "NaN", "1e5000", "one"} {
		assert.False(t, c.set(reference, "q", refused).GetResponse().Success, refused)
	}
	response = c.set(reference, "q", "nar")
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	assert.Equal(t, "NaR", response.(*dap.SetVariableResponse).Body.Value)
	assert.Equal(t, "NaR", c.variables(reference)["q"].Value)

	assert.False(t, c.set(reference, "n", "two").GetResponse().Success)
	require.True(t, c.set(reference, "q", "2.25").GetResponse().Success)
	require.True(t, c.set(reference, "n", "40").GetResponse().Success)

	assert.Equal(t, `"a"`, locals["w"].Value)
	assert.False(t, c.set(reference, "w", "unquoted").GetResponse().Success, "a string is written quoted")
	response = c.set(reference, "w", "null")
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	assert.Equal(t, "null", response.(*dap.SetVariableResponse).Body.Value)
	response = c.set(reference, "w", `"héllo"`)
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	assert.Equal(t, `"héllo"`, response.(*dap.SetVariableResponse).Body.Value)
	assert.Equal(t, `"héllo"`, c.variables(reference)["w"].Value)

	c.resume()
	code, stdout := c.exited()
	assert.Equal(t, 0, code)
	assert.Equal(t, "2.25 40 héllo\n", stdout, "the program goes on with the values set")
}

// failureSource fails in the way its first argument names.
const failureSource = `import std("io/local/*");
import std("lang/System.zam");

namespace Main {
    fun int(int*, int) Inner = (p, d) {
        if (d == 0) {
            System::Panic("boom\n");
        }
        return *p / d;
    }

    fun int(int*, int) Read = (p, i) {
        return p[i];
    }

    fun void(uint, str...) main = (argC, argV) {
        var int x = 4;
        var str how = argV[0];
        if (how == "null") {
            Main::Inner(null, 1);
        } else if (how == "panic") {
            Main::Inner(&x, 0);
        } else if (how == "fault") {
            Main::Read(new(int, 1), 1099511627776);
        } else if (how == "async") {
            var thread[int] t = async(Main::Inner(null, 1));
            wait(t);
        }
    }
}
`

func TestFailuresStopTheProgram(t *testing.T) {
	t.Parallel()
	for how, want := range map[string]struct {
		failure string
		frame   string
		status  []int
	}{
		"null":  {"dereferenced a null pointer", "Main.Inner Main.zam:9", []int{67}},
		"panic": {"boom", "Main.Inner Main.zam:7", []int{255}},
		"fault": {" at 0x", "Main.Read Main.zam:13", []int{128 + 10, 128 + 11}},
		"async": {"dereferenced a null pointer", "Main.Inner Main.zam:9", []int{67}},
	} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			c := sessionOf(t, failureSource, false, how)
			c.call(&dap.SetExceptionBreakpointsRequest{Request: request("setExceptionBreakpoints"),
				Arguments: dap.SetExceptionBreakpointsArguments{Filters: []string{"failures"}}})
			c.configurationDone()

			stopped := c.stopped()
			assert.Equal(t, "exception", stopped.Reason)
			assert.Contains(t, stopped.Text, want.failure)
			if how == "async" {
				assert.NotEqual(t, mainThread, stopped.ThreadId, "the thread that failed is the one stopped at")
			}
			info := c.call(&dap.ExceptionInfoRequest{Request: request("exceptionInfo"),
				Arguments: dap.ExceptionInfoArguments{ThreadId: stopped.ThreadId}}).(*dap.ExceptionInfoResponse)
			assert.Contains(t, info.Body.Description, want.failure)
			stack := c.stack(stopped.ThreadId)
			assert.Contains(t, stack[:min(2, len(stack))], want.frame, "it stops where it failed, or in the panic it called")
			if stack[0] == want.frame {
				assert.Contains(t, c.locals(stopped.ThreadId), "p", "a failing frame's variables read")
			}

			c.resume()
			code, _ := c.exited()
			assert.Contains(t, want.status, code, "the program ends as it would have")
		})
	}
}

func TestFailuresDoNotStopWhenTurnedOff(t *testing.T) {
	t.Parallel()
	c := sessionOf(t, failureSource, false, "null")
	c.call(&dap.SetExceptionBreakpointsRequest{Request: request("setExceptionBreakpoints"),
		Arguments: dap.SetExceptionBreakpointsArguments{Filters: []string{}}})
	c.configurationDone()
	code, _ := c.exited()
	assert.Equal(t, 67, code)
	for _, m := range c.pending {
		_, stopped := m.(*dap.StoppedEvent)
		assert.False(t, stopped, "nothing stops the program")
	}
}

// evaluate evaluates expression in the frame of thread's the editor would.
func (c *client) evaluate(expression string, frame int) dap.ResponseMessage {
	c.t.Helper()
	return c.send(&dap.EvaluateRequest{Request: request("evaluate"),
		Arguments: dap.EvaluateArguments{Expression: expression, FrameId: frame, Context: "watch"}})
}

func TestEvaluateReadsNamesMembersAndIndexes(t *testing.T) {
	t.Parallel()
	c := sessionOf(t, variablesSource, false, "first")
	c.breakpoints(14, 38)
	c.configurationDone()

	c.stopped()
	result := c.evaluate("this.n", 0)
	require.True(t, result.GetResponse().Success, result.GetResponse().Message)
	assert.Equal(t, "5", result.(*dap.EvaluateResponse).Body.Result, "with no frame, the stopped thread's innermost")

	c.resume()
	c.stopped()
	trace := c.call(&dap.StackTraceRequest{Request: request("stackTrace"),
		Arguments: dap.StackTraceArguments{ThreadId: mainThread}}).(*dap.StackTraceResponse)
	frame := trace.Body.StackFrames[0].Id
	for expression, want := range map[string]string{
		"i":           "7",
		"node.name":   `"nd"`,
		"node . n":    "5",
		"t.0":         "3",
		"t.1":         `"t"`,
		"xs[1]":       "9",
		"p.x":         "4",
		"argV[0]":     `"first"`,
		"argC":        "1",
		"s":           `"hi"`,
		"xs[0x2]":     "0",
		"node.name ":  `"nd"`,
		" b":          "true",
		"f":           "1.5",
		"sum":         "6",
		"t . 1":       `"t"`,
		"xs [ 1 ]":    "9",
		"node.n":      "5",
		"p . x":       "4",
		"argV [ 0 ] ": `"first"`,
	} {
		result := c.evaluate(expression, frame)
		require.True(t, result.GetResponse().Success, "%s: %s", expression, result.GetResponse().Message)
		assert.Equal(t, want, result.(*dap.EvaluateResponse).Body.Result, expression)
	}
	node := c.evaluate("node", frame).(*dap.EvaluateResponse).Body
	assert.NotZero(t, node.VariablesReference, "a watched value expands")
	assert.Equal(t, `"nd"`, c.variables(node.VariablesReference)["name"].Value)

	for _, refused := range []string{"1 + 2", "nope", "i.x", "xs[3]", "node.missing", "s[", "3"} {
		assert.False(t, c.evaluate(refused, frame).GetResponse().Success, refused)
	}

	c.resume()
	code, _ := c.exited()
	assert.Equal(t, 0, code)
}

// testedSource is code under test, and testSource its tests: First fails a
// check, and Second passes.
const (
	testedSource = `namespace X {
    public fun int(int) Twice = (n) {
        return n + n;
    }
}
`
	testSource = `import std("test/Assert.zam");
import "X.zam";

public namespace XTest {
    public fun void(uint, str...) First = (argC, argV) {
        var int got = X::Twice(1);
        Assert::[int]Equal(3, got);
    }

    public fun void(uint, str...) Second = (argC, argV) {
        var int got = X::Twice(2);
        Assert::[int]Equal(4, got);
    }
}
`
)

// testSession launches the tests test picks of a test project, and answers
// once they wait to be configured.
func testSession(t *testing.T, test string) *client {
	t.Helper()
	dir := filepath.Dir(project(t, testedSource))
	require.NoError(t, os.Rename(filepath.Join(dir, "Main.zam"), filepath.Join(dir, "X.zam")))
	path := filepath.Join(dir, "X.test.zam")
	require.NoError(t, os.WriteFile(path, []byte(testSource), 0o600))

	c := connect(t)
	c.path = path
	arguments, err := json.Marshal(launchArguments{Program: path, Test: test})
	require.NoError(t, err)
	response := c.send(&dap.LaunchRequest{Request: request("launch"), Arguments: arguments})
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	c.await(func(m dap.Message) bool { _, ok := m.(*dap.InitializedEvent); return ok })
	return c
}

// stdout answers what the program wrote to its standard output so far.
func (c *client) stdout() string {
	var stdout string
	for _, m := range c.pending {
		if output, ok := m.(*dap.OutputEvent); ok && output.Body.Category == "stdout" {
			stdout += output.Body.Output
		}
	}
	return stdout
}

func TestDebuggingTestsStopsOnAFailedCheck(t *testing.T) {
	t.Parallel()
	c := testSession(t, "")
	c.configurationDone()

	stopped := c.stopped()
	assert.Equal(t, "exception", stopped.Reason)
	assert.Contains(t, stopped.Text, "want 3, got 2")
	assert.Contains(t, c.stack(stopped.ThreadId), "XTest.First X.test.zam:7", "the failed check is among the frames")

	c.resume()
	code, _ := c.exited()
	assert.Equal(t, 1, code, "a test that failed fails the run")
	assert.Contains(t, c.stdout(), "--- FAIL: XTest.First")
	assert.Contains(t, c.stdout(), "--- PASS: XTest.Second", "the tests go on after a failed check")
}

func TestDebuggingTestsRunsThoseTestPicks(t *testing.T) {
	t.Parallel()
	c := testSession(t, "Second")
	c.breakpoints(6, 11)
	c.configurationDone()

	assert.Equal(t, "breakpoint", c.stopped().Reason)
	assert.Equal(t, "XTest.Second X.test.zam:11", c.stack(mainThread)[0], "First does not run")

	c.resume()
	code, _ := c.exited()
	assert.Equal(t, 0, code)
	assert.Contains(t, c.stdout(), "--- PASS: XTest.Second")
	assert.NotContains(t, c.stdout(), "XTest.First")
}

// loopSource loops ten times over a line the tests below break on.
const loopSource = `import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var str s = "a";
        for (var int i = 0; i < 10; i++) {
            if (i == 5) {
                s = "b";
            }
            var int sq = i * i;
        }
        File::StdOut.WriteStr(s + "\n");
    }
}
`

func TestBreakpointRules(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		breakpoint dap.SourceBreakpoint
		// stops are the values of i the program stops at.
		stops   []string
		console string
	}{
		"condition":        {dap.SourceBreakpoint{Condition: "i == 3"}, []string{"3"}, ""},
		"string condition": {dap.SourceBreakpoint{Condition: `s == "b"`}, []string{"5", "6", "7", "8", "9"}, ""},
		"hit count":        {dap.SourceBreakpoint{HitCondition: "4"}, []string{"3"}, ""},
		"every third hit":  {dap.SourceBreakpoint{HitCondition: "%3"}, []string{"2", "5", "8"}, ""},
		"both":             {dap.SourceBreakpoint{Condition: "i > 4", HitCondition: ">=8"}, []string{"7", "8", "9"}, ""},
		"logpoint":         {dap.SourceBreakpoint{LogMessage: "i={i} s={s}"}, nil, "i=0 s=\"a\"\n"},
		"broken condition": {dap.SourceBreakpoint{Condition: "i =="}, []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}, "condition \"i ==\""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := sessionOf(t, loopSource, false)
			test.breakpoint.Line = 10
			c.call(&dap.SetBreakpointsRequest{Request: request("setBreakpoints"), Arguments: dap.SetBreakpointsArguments{
				Source: dap.Source{Path: c.path}, Breakpoints: []dap.SourceBreakpoint{test.breakpoint},
			}})
			c.configurationDone()

			var stops []string
			for range test.stops {
				assert.Equal(t, "breakpoint", c.stopped().Reason)
				stops = append(stops, c.locals(mainThread)["i"].Value)
				c.resume()
			}
			code, _ := c.exited()
			assert.Equal(t, 0, code)
			assert.Equal(t, test.stops, stops)

			var console string
			for _, m := range c.pending {
				if output, ok := m.(*dap.OutputEvent); ok && output.Body.Category == "console" {
					console += output.Body.Output
				}
				_, stopped := m.(*dap.StoppedEvent)
				assert.False(t, stopped, "no more stops than those expected")
			}
			assert.Contains(t, console, test.console)
			if name == "logpoint" {
				assert.Contains(t, console, "i=9 s=\"b\"\n")
			}
		})
	}
}

// arenaSource keeps a static count of what an arena deletes, and runs a
// class's initializer and Delete from inside the arena's natives.
const arenaSource = `import std("lang/*");
import std("io/local/*");

namespace Main {
    static var int deleted = 0;

    class Counted {
        var str name = "fresh";

        public fun void() Delete = () {
            Main::deleted++;
        }
    }

    fun void(uint, str...) main = (argC, argV) {
        var Arena~* a = Arena::New(1024U);
        var Counted* first = Arena::[Counted]Alloc(a, 1U);
        first.name = "first";
        a.Reset();
        File::StdOut.WriteStr(Main::deleted{str} + "\n");
    }
}
`

func TestGlobalsAndCodeRunFromNatives(t *testing.T) {
	t.Parallel()
	c := sessionOf(t, arenaSource, false)
	c.breakpoints(8, 11, 20)
	c.configurationDone()

	c.stopped()
	stack := c.stack(mainThread)
	assert.Equal(t, "Main.Counted.<init> Main.zam:8", stack[0], "an initializer an arena runs stops at its breakpoints")
	assert.Contains(t, stack, "Main.main Main.zam:17")

	c.resume()
	c.stopped()
	stack = c.stack(mainThread)
	assert.Equal(t, "Main.Counted.Delete Main.zam:11", stack[0], "so does a Delete an arena runs")
	assert.Contains(t, stack, "Main.main Main.zam:19")
	locals := c.locals(mainThread)
	assert.Equal(t, `"first"`, c.variables(locals["this"].VariablesReference)["name"].Value)
	globals := c.scope(c.top(mainThread), "Globals")
	assert.Equal(t, "0", globals["Main.deleted"].Value)
	for name := range globals {
		assert.True(t, strings.HasPrefix(name, "Main."), "%s is the program's own", name)
	}

	c.call(&dap.NextRequest{Request: request("next"), Arguments: dap.NextArguments{ThreadId: mainThread}})
	assert.Equal(t, "step", c.stopped().Reason)

	c.resume()
	c.stopped()
	frame := c.top(mainThread)
	assert.Equal(t, "1", c.scope(frame, "Globals")["Main.deleted"].Value)
	for _, expression := range []string{"Main::deleted", "Main.deleted", "deleted"} {
		result := c.evaluate(expression, frame)
		require.True(t, result.GetResponse().Success, "%s: %s", expression, result.GetResponse().Message)
		assert.Equal(t, "1", result.(*dap.EvaluateResponse).Body.Result, "a static is evaluated as the program names it: %s", expression)
	}
	scopes := c.call(&dap.ScopesRequest{Request: request("scopes"),
		Arguments: dap.ScopesArguments{FrameId: frame}}).(*dap.ScopesResponse)
	response := c.set(scopes.Body.Scopes[1].VariablesReference, "Main.deleted", "41")
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)

	c.resume()
	code, stdout := c.exited()
	assert.Equal(t, 0, code)
	assert.Equal(t, "41\n", stdout, "a global set goes on holding what it was set to")
}

// inputSource writes back what it reads from its standard input.
const inputSource = `import std("io/local/*");
import std("lang/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var (str, Err*) read = File::StdIn.ReadAll();
        File::StdOut.WriteStr("got " + read.0 + "\n");
    }
}
`

func TestProgramsRunInTheEditorsTerminal(t *testing.T) {
	t.Parallel()
	c := connect(t)
	c.call(&dap.InitializeRequest{Request: request("initialize"),
		Arguments: dap.InitializeRequestArguments{AdapterID: "zam", SupportsRunInTerminalRequest: true}})
	c.path = project(t, inputSource)
	arguments, err := json.Marshal(launchArguments{Program: c.path, Console: "integratedTerminal"})
	require.NoError(t, err)
	c.seq++
	launch := &dap.LaunchRequest{Request: request("launch"), Arguments: arguments}
	launch.Seq = c.seq
	require.NoError(t, dap.WriteProtocolMessage(c.out, launch))

	// The client is the editor, and runs what it is asked to in a terminal
	// of its own: a process whose input is a line typed ahead.
	asked := c.await(func(m dap.Message) bool { _, ok := m.(*dap.RunInTerminalRequest); return ok }).(*dap.RunInTerminalRequest)
	process := exec.Command(asked.Arguments.Args[0], asked.Arguments.Args[1:]...)
	process.Dir = asked.Arguments.Cwd
	process.Env = os.Environ()
	for name, value := range asked.Arguments.Env {
		process.Env = append(process.Env, name+"="+value.(string))
	}
	process.Stdin = strings.NewReader("hello")
	var terminal strings.Builder
	process.Stdout, process.Stderr = &terminal, &terminal
	require.NoError(t, process.Start())
	require.NoError(t, dap.WriteProtocolMessage(c.out, &dap.RunInTerminalResponse{
		Response: dap.Response{ProtocolMessage: dap.ProtocolMessage{Type: "response"}, RequestSeq: asked.Seq, Success: true, Command: "runInTerminal"},
		Body:     dap.RunInTerminalResponseBody{ProcessId: process.Process.Pid},
	}))

	response := c.await(func(m dap.Message) bool {
		r, ok := m.(dap.ResponseMessage)
		return ok && r.GetResponse().RequestSeq == launch.Seq
	}).(dap.ResponseMessage)
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	c.await(func(m dap.Message) bool { _, ok := m.(*dap.InitializedEvent); return ok })

	c.breakpoints(7)
	c.configurationDone()
	assert.Equal(t, "breakpoint", c.stopped().Reason)
	result := c.evaluate("read.0", 0)
	require.True(t, result.GetResponse().Success, result.GetResponse().Message)
	assert.Equal(t, `"hello"`, result.(*dap.EvaluateResponse).Body.Result, "the program reads what is typed at the terminal")

	c.resume()
	code, _ := c.exited()
	assert.Equal(t, 0, code, "the status comes from the terminal's process")
	require.NoError(t, process.Wait())
	assert.Equal(t, "got hello\n", terminal.String())
}

func TestRestartRunsTheProgramAgainAsItNowIs(t *testing.T) {
	t.Parallel()
	c := session(t, false)
	c.breakpoints(5)
	c.configurationDone()
	c.stopped()
	assert.Equal(t, []string{"Main.Twice Main.zam:5", "Main.main Main.zam:16"}, c.stack(mainThread))

	edited := strings.Replace(debugSource, "return y + y;", "return y + y + 1;", 1)
	require.NoError(t, os.WriteFile(c.path, []byte(edited), 0o600))
	c.call(&dap.RestartRequest{Request: request("restart")})
	c.stopped()
	assert.Equal(t, []string{"Main.Twice Main.zam:5", "Main.main Main.zam:16"}, c.stack(mainThread),
		"the program starts over, stopping at the breakpoint it first reaches")

	c.call(&dap.SetBreakpointsRequest{Request: request("setBreakpoints"),
		Arguments: dap.SetBreakpointsArguments{Source: dap.Source{Path: c.path}}})
	c.resume()
	code, stdout := c.exited()
	assert.Equal(t, 0, code)
	assert.Equal(t, "7\n", stdout, "the program restarted is built from the source as it is now")
	for _, m := range c.pending {
		_, exited := m.(*dap.ExitedEvent)
		assert.False(t, exited, "the program ended to restart is not said to have exited")
	}
}

func TestRunningWithoutDebuggingStopsAtNothing(t *testing.T) {
	t.Parallel()
	c := connect(t)
	c.call(&dap.InitializeRequest{Request: request("initialize"), Arguments: dap.InitializeRequestArguments{AdapterID: "zam"}})
	c.path = project(t, failureSource)
	arguments, err := json.Marshal(launchArguments{Program: c.path, Args: []string{"null"}, NoDebug: true})
	require.NoError(t, err)
	c.call(&dap.LaunchRequest{Request: request("launch"), Arguments: arguments})
	c.await(func(m dap.Message) bool { _, ok := m.(*dap.InitializedEvent); return ok })
	c.breakpoints(20)
	c.configurationDone()

	code, _ := c.exited()
	assert.Equal(t, 67, code, "neither the breakpoint nor the failure stops it")
	for _, m := range c.pending {
		_, stopped := m.(*dap.StoppedEvent)
		assert.False(t, stopped)
	}
}

// lockedSource dies through a native that holds the runtime's thread list.
const (
	lockedSource = `namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        native(DIE_LOCKED){void()}();
    }
}
`
	lockedNative = `#include "zam/native.h"
#include "vm.h"

void DIE_LOCKED(ZamThread *thread) {
    pthread_mutex_lock(&thread->vm->threadLock);
    zam_die(ZAMEXIT_PANIC, "died holding the thread list\n");
}
`
)

func TestAFailureHoldingTheRuntimesLocksDoesNotStop(t *testing.T) {
	t.Parallel()
	path := project(t, lockedSource)
	dir := filepath.Dir(path)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "locked.c"), []byte(lockedNative), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"),
		[]byte("name: \"locked\"\nversion: \"0.0.0\"\ndependencies:\nnative:\n  files:\n    - locked.c\n"), 0o600))

	c := connect(t)
	c.call(&dap.InitializeRequest{Request: request("initialize"), Arguments: dap.InitializeRequestArguments{AdapterID: "zam"}})
	response := c.launch(path, false)
	require.True(t, response.GetResponse().Success, response.GetResponse().Message)
	c.await(func(m dap.Message) bool { _, ok := m.(*dap.InitializedEvent); return ok })
	c.call(&dap.SetExceptionBreakpointsRequest{Request: request("setExceptionBreakpoints"),
		Arguments: dap.SetExceptionBreakpointsArguments{Filters: []string{"failures"}}})
	c.configurationDone()

	code, _ := c.exited()
	assert.Equal(t, 67, code, "it fails as it would have, rather than hanging")
	var stderr string
	for _, m := range c.pending {
		if output, ok := m.(*dap.OutputEvent); ok && output.Body.Category == "stderr" {
			stderr += output.Body.Output
		}
		_, stopped := m.(*dap.StoppedEvent)
		assert.False(t, stopped)
	}
	assert.Contains(t, stderr, "died holding the thread list")
	assert.Contains(t, stderr, "the debugger cannot stop the program here")
}
