package build

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Prepare builds the image into a binary in a temporary directory, answering
// its path and a function that removes it. opts.OutputPath is ignored.
func Prepare(image []byte, nativeNames []string, opts Options) (string, func(), error) {
	dir, err := os.MkdirTemp("", "zam-run-*")
	if err != nil {
		return "", nil, fmt.Errorf("cannot create run directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(dir) }

	if opts.OutputPath, err = ExecutablePath(opts, filepath.Join(dir, "main")); err != nil {
		remove()
		return "", nil, err
	}
	if err := Build(image, nativeNames, opts); err != nil {
		remove()
		return "", nil, err
	}
	return opts.OutputPath, remove, nil
}

// Run builds the image into a temporary binary, runs it with args and the
// current process's standard streams, and removes it. opts.OutputPath is
// ignored. The returned code is the program's exit status; the error is
// non-nil only when the program could not be built or started.
//
// Interrupts and terminations are forwarded to the program rather than ending
// the caller, so the binary is removed however the program exits.
func Run(image []byte, nativeNames []string, opts Options, args []string) (int, error) {
	path, remove, err := Prepare(image, nativeNames, opts)
	if err != nil {
		return 0, err
	}
	defer remove()
	return runBinary(path, args)
}

// PrepareNative links a natively compiled program's object into a temporary
// binary, answering its path and how to remove it.
func PrepareNative(object []byte, opts Options) (string, func(), error) {
	dir, err := os.MkdirTemp("", "zam-run-*")
	if err != nil {
		return "", nil, fmt.Errorf("cannot create run directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(dir) }

	if opts.OutputPath, err = ExecutablePath(opts, filepath.Join(dir, "main")); err != nil {
		remove()
		return "", nil, err
	}
	if err := LinkObject(object, opts); err != nil {
		remove()
		return "", nil, err
	}
	return opts.OutputPath, remove, nil
}

// RunNative is Run for a natively compiled program's object.
func RunNative(object []byte, opts Options, args []string) (int, error) {
	path, remove, err := PrepareNative(object, opts)
	if err != nil {
		return 0, err
	}
	defer remove()
	return runBinary(path, args)
}

// runBinary runs the binary at path with args and the current process's
// standard streams, forwarding interrupts and terminations to it.
func runBinary(path string, args []string) (int, error) {
	cmd := exec.Command(path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("cannot start program: %w", err)
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case sig := <-signals:
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()

	return ExitStatus(cmd.Wait())
}

// ExitStatus answers the status a program ended with from what waiting on it
// returned: its exit code, or 128 and the signal that ended it. The error is
// non-nil only when the program could not be waited on.
func ExitStatus(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, fmt.Errorf("cannot run program: %w", err)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return exitErr.ExitCode(), nil
}
