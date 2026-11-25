package test

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
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
func Run(cmd *cli.Command, files *base.FileProvider, packages packageProvider, out io.Writer) (bool, error) {
	runner, tests, err := prepare(cmd, files, packages)
	switch {
	case errors.Is(err, ErrNoTestFiles):
		fmt.Fprintln(out, "no test files")
		return true, nil
	case errors.Is(err, ErrNoTests):
		fmt.Fprintln(out, "no tests to run")
		return true, nil
	case err != nil:
		return false, err
	}

	dir, err := os.MkdirTemp("", "zam-test-*")
	if err != nil {
		return false, fmt.Errorf("cannot create test directory: %w", err)
	}
	defer os.RemoveAll(dir)

	opts := build.Options{Project: runner.ast.MainCodeDecl.File().Project}
	binary, err := build.ExecutablePath(opts, filepath.Join(dir, "test"))
	if err != nil {
		return false, err
	}
	opts.OutputPath = binary
	object, err := codegen.GenerateObject(runner.cmd, runner.ast, codegen.ObjectOptions{
		Optimization: runner.cmd.Optimization,
		Coverage:     cmd.CoverProfile != "",
	})
	if err != nil {
		return false, err
	}
	if err := build.LinkObject(object.Bytes, opts); err != nil {
		return false, err
	}
	coverDir := ""
	if cmd.CoverProfile != "" {
		coverDir = dir
	}
	passed, err := runFiles(cmd, tests, binary, coverDir, out)
	if err != nil || cmd.CoverProfile == "" {
		return passed, err
	}
	return passed, writeCoverage(cmd.CoverProfile, object.Coverage, coverFiles(coverDir, len(tests)), out)
}

// program is the validated runner of a set of tests, and the command it is
// compiled under.
type program struct {
	cmd *cli.Command
	ast *syntax.AST
}

var (
	// ErrNoTestFiles is what Run and Object answer when cmd targets no test
	// file.
	ErrNoTestFiles = errors.New("no test files")
	// ErrNoTests is what Run and Object answer when no test is picked.
	ErrNoTests = errors.New("no tests to run")
)

// Object compiles the tests in the test files cmd targets into one program, as
// opts says, whose first argument is the index among the files answered of the
// file whose tests it runs, and whose other arguments it passes on to each
// test. It answers the program, its project, the files and the name its main is
// compiled as.
func Object(cmd *cli.Command, files *base.FileProvider, packages packageProvider, opts codegen.ObjectOptions) (*codegen.Object, *syntax.Project, []File, string, error) {
	runner, tests, err := prepare(cmd, files, packages)
	if err != nil {
		return nil, nil, nil, "", err
	}
	object, err := codegen.GenerateObject(runner.cmd, runner.ast, opts)
	if err != nil {
		return nil, nil, nil, "", err
	}
	return object, runner.ast.MainCodeDecl.File().Project, tests, codegen.MainFunctionName(runner.ast), nil
}

// prepare writes the runner of the tests in the test files cmd targets and
// validates it, answering it and the files.
func prepare(cmd *cli.Command, files *base.FileProvider, packages packageProvider) (program, []File, error) {
	paths, err := Resolve(files, cmd.TestTargets)
	if err != nil {
		return program{}, nil, err
	}
	if len(paths) == 0 {
		return program{}, nil, ErrNoTestFiles
	}

	tests, err := discover(cmd, files, packages, paths)
	if err != nil {
		return program{}, nil, err
	}
	if len(tests) == 0 {
		return program{}, nil, ErrNoTests
	}

	p, err := validate(cmd, files, packages, tests)
	if err != nil {
		return program{}, nil, err
	}
	return p, tests, nil
}

// discover parses paths, with what they import, and finds their tests.
func discover(cmd *cli.Command, files *base.FileProvider, packages packageProvider, paths []base.Path) ([]File, error) {
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

// validate writes the runner of tests and validates it.
func validate(cmd *cli.Command, files *base.FileProvider, packages packageProvider, tests []File) (program, error) {
	runner := RunnerPath(tests[0].Path)
	if _, err := os.Stat(string(runner)); !errors.Is(err, fs.ErrNotExist) {
		return program{}, fmt.Errorf("%s is the test runner's own path and must not exist", runner)
	}
	source, err := Source(tests)
	if err != nil {
		return program{}, err
	}

	compile := *cmd
	compile.InputPath, compile.TestPaths = runner, nil
	overlay := &runnerFileProvider{FileProvider: files, path: runner, source: source}

	ast, errs := syntax.NewAST(&compile, overlay, packages)
	if len(errs) > 0 {
		return program{}, errors.Join(errs...)
	}
	if errs := semantic.ValidateAST(ast); len(errs) > 0 {
		return program{}, errors.Join(errs...)
	}
	return program{cmd: &compile, ast: ast}, nil
}

// runFiles runs binary once for each of tests, at most cmd.Parallel at once,
// and writes each one's output and outcome to out in the order of tests. Each
// writes its coverage into coverDir, unless coverDir is empty.
func runFiles(cmd *cli.Command, tests []File, binary, coverDir string, out io.Writer) (bool, error) {
	results := make([]chan result, len(tests))
	slots := make(chan struct{}, cmd.Parallel)
	var wg sync.WaitGroup
	for i := range tests {
		results[i] = make(chan result, 1)
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			cover := ""
			if coverDir != "" {
				cover = coverFiles(coverDir, len(tests))[i]
			}
			results[i] <- runFile(binary, i, cmd.RunArgs, cover)
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

// runFile runs the tests of the file at index in binary, passing args on, and
// writing its coverage to cover unless cover is empty.
func runFile(binary string, index int, args []string, cover string) result {
	process := exec.Command(binary, append([]string{strconv.Itoa(index)}, args...)...)
	if cover != "" {
		process.Env = append(os.Environ(), coverageVariable+"="+cover)
	}
	start := time.Now()
	output, err := process.CombinedOutput()
	r := result{output: output, duration: time.Since(start)}

	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.status, r.ending = exit.ExitCode(), exit.String()
		if runtime.GOOS == "windows" {
			r.status, r.ending = windowsStatus(r.status, r.ending)
		}
	case err != nil:
		r.err = fmt.Errorf("cannot run tests: %w", err)
	}
	return r
}

// windowsStatus answers a Windows process's exit code as a POSIX status: an
// exception, as an access violation is, ends it as a signal does, with status
// -1, and any other code is its low byte, as a POSIX system keeps.
func windowsStatus(code int, ending string) (int, string) {
	if c := uint32(code); c >= 0xC0000000 && c < 0xD0000000 {
		return -1, fmt.Sprintf("exception %#08x", c)
	}
	return code & 0xff, ending
}
