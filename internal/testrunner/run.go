package testrunner

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
)

// packageProvider fetches a project's dependencies.
type packageProvider interface {
	Fetch(ref string) (base.Path, string, error)
}

// result is how one test file's process went.
type result struct {
	output []byte
	// status is the process's exit status, or -1 if a signal ended it.
	status int
	// ending says what ended a process that did not exit by itself.
	ending   string
	duration time.Duration
	err      error
}

// Run compiles the tests in the test files cmd targets into one program, runs
// the tests of each file in a process of its own, and writes what each wrote
// and how it went to out, a file at a time. It reports whether every test
// passed.
func Run(cmd *command.Command, files *base.FileProvider, packages packageProvider, out io.Writer) (bool, error) {
	paths, err := Resolve(files, cmd.TestTargets)
	if err != nil {
		return false, err
	}
	if len(paths) == 0 {
		fmt.Fprintln(out, "no test files")
		return true, nil
	}

	tests, err := discover(cmd, files, packages, paths)
	if err != nil {
		return false, err
	}
	if len(tests) == 0 {
		fmt.Fprintln(out, "no tests to run")
		return true, nil
	}

	dir, err := os.MkdirTemp("", "zam-test-*")
	if err != nil {
		return false, fmt.Errorf("cannot create test directory: %w", err)
	}
	defer os.RemoveAll(dir)

	binary := filepath.Join(dir, "test")
	if err := compile(cmd, files, packages, tests, binary); err != nil {
		return false, err
	}
	return runFiles(cmd, tests, binary, out)
}

// discover parses paths, with what they import, and finds their tests.
func discover(cmd *command.Command, files *base.FileProvider, packages packageProvider, paths []base.Path) ([]File, error) {
	pattern, err := regexp.Compile(cmd.RunPattern)
	if err != nil {
		return nil, err
	}

	parse := *cmd
	parse.InputPath, parse.TestPaths = paths[0], paths[1:]
	ast, errs := syntax.NewAST(&parse, files, packages)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return Discover(ast, paths, pattern)
}

// compile writes the runner of tests and builds it to binary.
func compile(cmd *command.Command, files *base.FileProvider, packages packageProvider, tests []File, binary string) error {
	runner := RunnerPath(tests[0].Path)
	if _, err := os.Stat(string(runner)); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is the test runner's own path and must not exist", runner)
	}
	source, err := Source(tests)
	if err != nil {
		return err
	}

	compile := *cmd
	compile.InputPath, compile.TestPaths = runner, nil
	overlay := &runnerFileProvider{FileProvider: files, path: runner, source: source}

	ast, errs := syntax.NewAST(&compile, overlay, packages)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if errs := semantic.ValidateAST(ast); len(errs) > 0 {
		return errors.Join(errs...)
	}
	result, err := codegen.GenerateCode(&compile, ast)
	if err != nil {
		return err
	}
	return build.Build(result.Image, result.NativeNames, build.Options{
		OutputPath:   binary,
		Project:      ast.MainCodeDecl.File().Project,
		KeepBuildDir: cmd.KeepBuild,
	})
}

// runFiles runs binary once for each of tests, at most cmd.Parallel at once,
// and writes each one's output and outcome to out in the order of tests.
func runFiles(cmd *command.Command, tests []File, binary string, out io.Writer) (bool, error) {
	results := make([]chan result, len(tests))
	slots := make(chan struct{}, cmd.Parallel)
	var wg sync.WaitGroup
	for i := range tests {
		results[i] = make(chan result, 1)
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			results[i] <- runFile(binary, i, cmd.RunArgs)
		})
	}
	defer wg.Wait()

	cwd, _ := os.Getwd()
	passed := true
	for i, file := range tests {
		r := <-results[i]
		if r.err != nil {
			return false, r.err
		}
		out.Write(r.output)

		name := string(file.Path)
		if rel, err := filepath.Rel(cwd, name); err == nil {
			name = rel
		}
		switch r.status {
		case 0:
			fmt.Fprintf(out, "ok   %s %s\n", name, r.duration.Round(time.Millisecond))
		case 1:
			passed = false
			fmt.Fprintf(out, "FAIL %s %s\n", name, r.duration.Round(time.Millisecond))
		case -1:
			passed = false
			fmt.Fprintf(out, "FAIL %s (%s)\n", name, r.ending)
		default:
			passed = false
			fmt.Fprintf(out, "FAIL %s (exit %d)\n", name, r.status)
		}
	}
	return passed, nil
}

// runFile runs the tests of the file at index in binary, passing args on.
func runFile(binary string, index int, args []string) result {
	process := exec.Command(binary, append([]string{strconv.Itoa(index)}, args...)...)
	start := time.Now()
	output, err := process.CombinedOutput()
	r := result{output: output, duration: time.Since(start)}

	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.status, r.ending = exit.ExitCode(), exit.String()
	case err != nil:
		r.err = fmt.Errorf("cannot run tests: %w", err)
	}
	return r
}
