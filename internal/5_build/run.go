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

// Run builds the image into a temporary binary, runs it with args and the
// current process's standard streams, and removes it. opts.OutputPath is
// ignored. The returned code is the program's exit status; the error is
// non-nil only when the program could not be built or started.
//
// Interrupts and terminations are forwarded to the program rather than ending
// the caller, so the binary is removed however the program exits.
func Run(image []byte, nativeNames []string, opts Options, args []string) (int, error) {
	runDir, err := os.MkdirTemp("", "zam-run-*")
	if err != nil {
		return 0, fmt.Errorf("cannot create run directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(runDir) }()

	opts.OutputPath = filepath.Join(runDir, "main")
	if err := Build(image, nativeNames, opts); err != nil {
		return 0, err
	}

	cmd := exec.Command(opts.OutputPath, args...)
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

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return 0, fmt.Errorf("cannot run program: %w", err)
		}
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	}
	return 0, nil
}
