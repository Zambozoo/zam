package debugger

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-dap"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
)

const (
	// consoleTerminal is the console a program run in the editor's terminal
	// is launched with, where it reads what is typed.
	consoleTerminal = "integratedTerminal"
	// connectTimeout is how long a program the editor starts has to connect.
	connectTimeout = 30 * time.Second
	// tokenBytes is how many random bytes a launch's token is.
	tokenBytes = 16
)

// startInTerminal has the editor run the binary at path with args in its
// terminal, under the debugger. The program connects back on a loopback port,
// as does a parent the runtime forks it from to say its pid and how it ended,
// each opening with a token only this launch handed out. Once it has ended,
// remove is called, and then exited with its status.
func (s *server) startInTerminal(path string, args []string, dir, title string, debug *codegen.DebugTable,
	remove func()) (*program, error) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		remove()
		return nil, fmt.Errorf("cannot listen for the program: %w", err)
	}
	defer func() { _ = listener.Close() }()
	_ = listener.SetDeadline(time.Now().Add(connectTimeout))
	secret := make([]byte, tokenBytes)
	if _, err := rand.Read(secret); err != nil {
		remove()
		return nil, err
	}
	token := hex.EncodeToString(secret)
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	if dir == "" {
		dir = filepath.Dir(path)
	}
	_, err = s.request(&dap.RunInTerminalRequest{Request: dap.Request{
		ProtocolMessage: dap.ProtocolMessage{Type: "request"}, Command: "runInTerminal",
	}, Arguments: dap.RunInTerminalRequestArguments{
		Kind:  "integrated",
		Title: title,
		Cwd:   dir,
		Args:  append([]string{path}, args...),
		Env: map[string]any{
			"ZAM_DEBUG_PORT":        port,
			"ZAM_DEBUG_STATUS_PORT": port,
			"ZAM_DEBUG_TOKEN":       token,
		},
	}})
	if err != nil {
		remove()
		return nil, err
	}

	connections, err := accept(listener, token, "program", "status")
	if err != nil {
		remove()
		return nil, err
	}
	conn, supervisor := connections["program"], connections["status"]
	lines := bufio.NewReader(supervisor)
	pid, err := readNumber(lines, "pid")
	if err != nil {
		_ = conn.Close()
		_ = supervisor.Close()
		remove()
		return nil, err
	}

	p := newProgram(debug, conn, func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	p.serve(s.stopped)
	go func() {
		code, err := readNumber(lines, "exited")
		if err != nil {
			code = -1
		}
		_ = supervisor.Close()
		_ = conn.Close()
		remove()
		close(p.done)
		if !p.silent.Load() {
			s.exited(code)
		}
	}()
	return p, nil
}

// accept answers a connection for each of roles, each opening with hello,
// token and the role, and closes any other.
func accept(listener *net.TCPListener, token string, roles ...string) (map[string]*bufferedConn, error) {
	connections := map[string]*bufferedConn{}
	for len(connections) < len(roles) {
		conn, err := listener.Accept()
		if err != nil {
			for _, c := range connections {
				_ = c.Close()
			}
			return nil, fmt.Errorf("the program did not connect: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(connectTimeout))
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		fields := strings.Fields(line)
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil || len(fields) != 3 || fields[0] != "hello" || fields[1] != token ||
			!slices.Contains(roles, fields[2]) || connections[fields[2]] != nil {
			_ = conn.Close()
			continue
		}
		connections[fields[2]] = &bufferedConn{Conn: conn, reader: reader}
	}
	return connections, nil
}

// bufferedConn is a connection whose reading goes through what has already
// been read ahead of it.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

// readNumber reads a line of a word and a number, answering the number.
func readNumber(lines *bufio.Reader, word string) (int, error) {
	line, err := lines.ReadString('\n')
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != word {
		return 0, fmt.Errorf("the program said %q, not %s", strings.TrimSpace(line), word)
	}
	return strconv.Atoi(fields[1])
}

// request sends the editor a request, answering its response.
func (s *server) request(r dap.RequestMessage) (dap.ResponseMessage, error) {
	seq := s.nextSeq()
	r.GetRequest().Seq = seq
	answer := make(chan dap.ResponseMessage, 1)
	s.pendingLock.Lock()
	s.pending[seq] = answer
	s.pendingLock.Unlock()
	defer func() {
		s.pendingLock.Lock()
		delete(s.pending, seq)
		s.pendingLock.Unlock()
	}()

	s.send(r)
	select {
	case response := <-answer:
		if !response.GetResponse().Success {
			return nil, fmt.Errorf("%s failed: %s", r.GetRequest().Command, response.GetResponse().Message)
		}
		return response, nil
	case <-time.After(connectTimeout):
		return nil, errors.New("the editor did not answer " + r.GetRequest().Command)
	}
}

// answered hands a response from the editor to the request waiting on it.
func (s *server) answered(response dap.ResponseMessage) {
	s.pendingLock.Lock()
	answer, ok := s.pending[response.GetResponse().RequestSeq]
	s.pendingLock.Unlock()
	if ok {
		answer <- response
	}
}
