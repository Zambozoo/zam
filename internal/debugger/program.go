package debugger

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
)

// debugFD is the descriptor the program is handed its connection to the
// debugger on: the first after the standard streams.
const debugFD = 3

var (
	// errExited is what a question put to a program that has ended is
	// answered with.
	errExited = errors.New("the program has exited")
	// errUnreadable is what asking for memory the runtime will not read is
	// answered with.
	errUnreadable = errors.New("cannot read the program's memory there")
)

type (
	// program is a program running under the debugger, and the connection
	// its runtime is stopped, stepped and asked about over.
	program struct {
		debug *codegen.DebugTable
		conn  net.Conn
		// end ends the program.
		end func()
		// silent is set on a program ended to be started again, whose ending
		// the editor is not told of.
		silent atomic.Bool

		// writeLock orders what is sent to the runtime. askLock lets one
		// question at a time wait on answers.
		writeLock sync.Mutex
		askLock   sync.Mutex
		answers   chan []string

		// done is closed once the program has ended.
		done chan struct{}
	}

	// stop is the runtime saying the program has stopped.
	stop struct {
		thread int
		reason string
		offset uint64
		// failure is what a failing thread failed with.
		failure string
	}
)

// start runs the binary at path with args under the debugger, sending what it
// writes to output and each time it stops to stopped. Once it has ended,
// remove is called, and then exited with its status.
func start(path string, args []string, dir string, debug *codegen.DebugTable, remove func(),
	output func(category, text string), stopped func(stop), exited func(int)) (*program, error) {
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the program: %w", err)
	}
	ours := os.NewFile(uintptr(fds[0]), "debugger")
	theirs := os.NewFile(uintptr(fds[1]), "program")
	defer func() { _ = theirs.Close() }()
	conn, err := net.FileConn(ours)
	_ = ours.Close()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the program: %w", err)
	}

	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ZAM_DEBUG_FD="+strconv.Itoa(debugFD))
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Stdout = outputWriter{category: "stdout", output: output}
	cmd.Stderr = outputWriter{category: "stderr", output: output}
	if err := cmd.Start(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot start program: %w", err)
	}

	p := newProgram(debug, conn, func() { _ = cmd.Process.Kill() })
	p.serve(stopped)
	go func() {
		code, _ := build.ExitStatus(cmd.Wait())
		_ = conn.Close()
		remove()
		close(p.done)
		if !p.silent.Load() {
			exited(code)
		}
	}()
	return p, nil
}

// newProgram is a program the runtime of which is connected on conn, and
// which end ends.
func newProgram(debug *codegen.DebugTable, conn net.Conn, end func()) *program {
	return &program{
		debug:   debug,
		conn:    conn,
		end:     end,
		answers: make(chan []string, 1),
		done:    make(chan struct{}),
	}
}

// serve reads what the runtime sends, handing each stop to stopped. A stop is
// handled apart from the reading, since deciding whether it stands asks the
// runtime questions whose answers the reading brings.
func (p *program) serve(stopped func(stop)) {
	stops := make(chan stop, 16)
	go func() {
		for st := range stops {
			stopped(st)
		}
	}()
	go func() {
		p.read(stops)
		close(stops)
	}()
}

// read passes on each line the runtime sends: a stop to stops, and anything
// else as the answer to the question waiting on it.
func (p *program) read(stops chan<- stop) {
	scanner := bufio.NewScanner(p.conn)
	scanner.Buffer(nil, 1<<24)
	for scanner.Scan() {
		words := strings.Fields(scanner.Text())
		if len(words) == 0 {
			continue
		}
		if words[0] != "stopped" {
			p.answers <- words
			continue
		}
		if len(words) != 4 && len(words) != 5 {
			continue
		}
		thread, _ := strconv.Atoi(words[1])
		offset, _ := strconv.ParseUint(words[3], 10, 64)
		var failure []byte
		if len(words) == 5 {
			failure, _ = hex.DecodeString(words[4])
		}
		stops <- stop{thread: thread, reason: words[2], offset: offset, failure: string(failure)}
	}
}

// send tells the runtime command.
func (p *program) send(command string) error {
	p.writeLock.Lock()
	defer p.writeLock.Unlock()
	_, err := io.WriteString(p.conn, command+"\n")
	return err
}

// answer puts command to the runtime and answers the words it answers with.
func (p *program) answer(command string) ([]string, error) {
	p.askLock.Lock()
	defer p.askLock.Unlock()
	if err := p.send(command); err != nil {
		return nil, errExited
	}
	select {
	case words := <-p.answers:
		return words, nil
	case <-p.done:
		return nil, errExited
	}
}

// ask puts command to the runtime and answers the numbers it answers with,
// after the skip words naming the answer and what it is about.
func (p *program) ask(command string, skip int) ([]uint64, error) {
	words, err := p.answer(command)
	if err != nil {
		return nil, err
	}
	var numbers []uint64
	for _, word := range words[min(skip, len(words)):] {
		n, err := strconv.ParseUint(word, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot read %q from the runtime", word)
		}
		numbers = append(numbers, n)
	}
	return numbers, nil
}

// threads answers the ids of the program's threads.
func (p *program) threads() ([]uint64, error) {
	return p.ask("threads", 1)
}

// frame is where one of a thread's frames stands: an offset inside the
// instruction, the operand its slots begin at, and the mark offset of the
// entry its call made.
type frame struct {
	thread              int
	offset, base, entry uint64
}

// stack answers where each of thread's frames stands, innermost first.
func (p *program) stack(thread int) ([]frame, error) {
	numbers, err := p.ask("stack "+strconv.Itoa(thread), 2)
	if err != nil {
		return nil, err
	}
	frames := make([]frame, 0, len(numbers)/3)
	for i := 0; i+2 < len(numbers); i += 3 {
		frames = append(frames, frame{thread: thread, offset: numbers[i], base: numbers[i+1], entry: numbers[i+2]})
	}
	return frames, nil
}

// words answers count of thread's operand words from index on, as bytes.
func (p *program) words(thread int, index, count uint64) ([]byte, error) {
	numbers, err := p.ask(fmt.Sprintf("words %d %d %d", thread, index, count), 1)
	if err != nil {
		return nil, err
	}
	if uint64(len(numbers)) != count {
		return nil, errUnreadable
	}
	b := make([]byte, 0, count*8)
	for _, n := range numbers {
		b = binary.LittleEndian.AppendUint64(b, n)
	}
	return b, nil
}

// memory answers count bytes at address, read as thread would read them.
func (p *program) memory(thread int, address, count uint64) ([]byte, error) {
	words, err := p.answer(fmt.Sprintf("read %d %d %d", thread, address, count))
	if err != nil {
		return nil, err
	}
	if len(words) != 2 {
		return nil, errUnreadable
	}
	b, err := hex.DecodeString(words[1])
	if err != nil || uint64(len(b)) != count {
		return nil, errUnreadable
	}
	return b, nil
}

// write writes data at address, as thread would write it.
func (p *program) write(thread int, address uint64, data []byte) error {
	numbers, err := p.ask(fmt.Sprintf("write %d %d %s", thread, address, hex.EncodeToString(data)), 1)
	if err != nil {
		return err
	}
	if len(numbers) != 1 || numbers[0] != 1 {
		return errors.New("cannot write the program's memory there")
	}
	return nil
}

// setString stores at address a new string of text, allocated by thread,
// answering where the string lies.
func (p *program) setString(thread int, address uint64, text string) (uint64, error) {
	numbers, err := p.ask(fmt.Sprintf("string %d %d %s", thread, address, hex.EncodeToString([]byte(text))), 1)
	if err != nil {
		return 0, err
	}
	if len(numbers) != 1 || numbers[0] == 0 {
		return 0, errors.New("cannot store a string there")
	}
	return numbers[0], nil
}

// statics answers where the program's static region lies.
func (p *program) statics() (uint64, error) {
	numbers, err := p.ask("statics", 1)
	if err != nil || len(numbers) != 1 {
		return 0, errUnreadable
	}
	return numbers[0], nil
}

// failures has the program stop, or not, when a thread of it fails.
func (p *program) failures(stop bool) error {
	if stop {
		return p.send("failures 1")
	}
	return p.send("failures 0")
}

// instance answers which instantiation of the generic declaration template
// the instance at address is, or descends from, as a short type id, or 0.
func (p *program) instance(thread int, address uint64, template uint16) (uint32, error) {
	numbers, err := p.ask(fmt.Sprintf("instance %d %d %d", thread, address, template), 1)
	if err != nil || len(numbers) != 1 {
		return 0, errUnreadable
	}
	return uint32(numbers[0]), nil
}

// receiver answers the receiver of the call a frame's entry made, or 0.
func (p *program) receiver(f frame) (uint64, error) {
	numbers, err := p.ask(fmt.Sprintf("receiver %d %d", f.thread, f.entry), 1)
	if err != nil || len(numbers) != 1 {
		return 0, errUnreadable
	}
	return numbers[0], nil
}

// breakpoints has the program stop before each instruction at offsets.
func (p *program) breakpoints(offsets []uint64) error {
	var b strings.Builder
	b.WriteString("break")
	for _, offset := range offsets {
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(offset, 10))
	}
	return p.send(b.String())
}

// kill ends the program, and waits for it to have ended.
func (p *program) kill() {
	p.end()
	<-p.done
}

// detach lets the program run on as though it had no debugger, its runtime
// taking the end of the connection for the debugger leaving.
func (p *program) detach() {
	_ = p.conn.Close()
}

// outputWriter passes what a program writes to one of its streams on as
// output of category.
type outputWriter struct {
	category string
	output   func(category, text string)
}

func (w outputWriter) Write(b []byte) (int, error) {
	w.output(w.category, string(b))
	return len(b), nil
}
