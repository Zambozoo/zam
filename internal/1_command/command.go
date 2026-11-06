// Package command provides command line interface functionality.
package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	base "github.com/zambozoo/zam/internal/0_base"
)

const (
	flagOutput     = "o"
	flagOutputLong = "output"
	usageOutput    = "output file"

	flagVerbose     = "v"
	flagVerboseLong = "verbose"
	usageVerbose    = "verbose"

	flagThreads     = "t"
	flagThreadsLong = "threads"
	usageThreads    = "max threads"

	flagGCRatio     = "g"
	flagGCRatioLong = "gc-ratio"
	usageGCRatio    = "fraction of the heap that must be in use to trigger a collection"

	flagStackSize     = "s"
	flagStackSizeLong = "stack-size"
	usageStackSize    = "initial stack size"

	flagHeapSize     = "m"
	flagHeapSizeLong = "heap-size"
	usageHeapSize    = "initial heap size"

	flagEmitImage     = "E"
	flagEmitImageLong = "emit-image"
	usageEmitImage    = "write the raw bytecode image instead of linking a binary"

	flagKeepBuild     = "k"
	flagKeepBuildLong = "keep-build"
	usageKeepBuild    = "keep the temporary build directory and print its path on failure or success"

	flagRun     = "r"
	flagRunLong = "run"
	usageRun    = "build to a temporary binary and run it, passing any arguments after the input file"

	flagNoProject     = "n"
	flagNoProjectLong = "no-project"
	usageNoProject    = "ignore project files and compile local files as one project depending only on std"

	defaultOutput    = "out"
	defaultThreads   = 1
	defaultVerbose   = false
	defaultGCRatio   = 0.75
	defaultStackSize = byteSize(512 << 10)
	defaultHeapSize  = byteSize(16 << 20)
)

// byteSizeUnits are the suffixes a size may be written with, longest first so
// that the search below cannot match the "B" inside "KB" and read the rest as a
// count.
var byteSizeUnits = []struct {
	suffix     string
	multiplier int
}{
	{"GB", 1 << 30},
	{"MB", 1 << 20},
	{"KB", 1 << 10},
	{"B", 1},
}

type (
	// byteSize is a count of bytes written on the command line as a number with
	// an optional unit suffix.
	byteSize int

	Command struct {
		// InputPath is the file to read from
		// This is the last argument to the command
		// Required, must not be empty, must be a valid non-directory file path.
		InputPath *base.Path

		// OutputFile is the file to write to
		// Uses the -o flag with a default of `out`
		// Must be a valid file path.
		OutputPath *base.Path

		// Verbose is the verbose mode
		// Uses the -v flag with a default of `false`
		Verbose bool

		// MaxThreads is the maximum number of threads to use
		// Uses the -t flag with a default of `1`
		MaxThreads int

		// GCTriggerRatio is how full the heap must be for a collection to be
		// triggered, as a fraction of its size.
		// Uses the -g flag with a default of `0.75`, must be within (0, 1].
		GCTriggerRatio float64

		// InitialStackSize is the number of bytes of stack a thread starts with.
		// Uses the -s flag with a default of `512KB`
		InitialStackSize int

		// InitialHeapSize is the number of bytes of heap the program starts with.
		// Uses the -m flag with a default of `16MB`
		InitialHeapSize int

		// EmitImage skips the build driver and writes the raw bytecode image to
		// OutputPath instead of linking a native binary. Uses the -E flag with a
		// default of `false`.
		EmitImage bool

		// KeepBuild leaves the build driver's temporary directory in place and
		// prints its path, so the generated wrapper and extracted runtime can be
		// inspected. Uses the -k flag with a default of `false`.
		KeepBuild bool

		// Run builds the binary to a temporary location and runs it instead of
		// writing it to OutputPath. Uses the -r flag with a default of `false`.
		Run bool

		// RunArgs are the arguments after the input file, passed to the program
		// when Run is set.
		RunArgs []string

		// NoProject skips the search for project files, so every local file
		// belongs to an implicit project that depends only on std. Uses the -n
		// flag with a default of `false`.
		NoProject bool
	}

	fileProvider interface {
		FileExists(path *base.Path) (bool, error)
	}

	usageError struct {
		name  string
		flags string
		err   error
	}
)

var (
	_ error       = (*usageError)(nil)
	_ pflag.Value = (*byteSize)(nil)
)

// String implements the [pflag.Value] interface, writing the size back with the
// largest unit that divides it so that what is echoed reads like what was typed.
func (b byteSize) String() string {
	for _, unit := range byteSizeUnits {
		if int(b) >= unit.multiplier && int(b)%unit.multiplier == 0 {
			return strconv.Itoa(int(b)/unit.multiplier) + unit.suffix
		}
	}
	return strconv.Itoa(int(b)) + "B"
}

// Set implements the [pflag.Value] interface. A bare number is a count of bytes.
func (b *byteSize) Set(s string) error {
	digits := strings.ToUpper(strings.TrimSpace(s))
	multiplier := 1
	for _, unit := range byteSizeUnits {
		if rest, found := strings.CutSuffix(digits, unit.suffix); found {
			digits, multiplier = rest, unit.multiplier
			break
		}
	}

	count, err := strconv.Atoi(strings.TrimSpace(digits))
	if err != nil {
		return fmt.Errorf("invalid size %q", s)
	}
	if count <= 0 {
		return fmt.Errorf("size must be positive: %q", s)
	}
	if count > math.MaxInt/multiplier {
		return fmt.Errorf("size out of range: %q", s)
	}

	*b = byteSize(count * multiplier)
	return nil
}

// Type implements the [pflag.Value] interface.
func (*byteSize) Type() string {
	return "size"
}

// Error implements the [error] interface.
func (ue *usageError) Error() string {
	var errorLine string
	if ue.err != nil {
		errorLine = fmt.Sprintf("Error: %s\n", ue.err.Error())
	}
	return fmt.Sprintf("%sUsage of %s:\n%s", errorLine, ue.name, ue.flags)
}

// New builds a Command from the given argument list and file provider.
func New(argv []string, fileProvider fileProvider) (*Command, error) {
	if len(argv) == 0 {
		return nil, errors.New("missing program name")
	}

	fs := pflag.NewFlagSet(argv[0], pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.SetInterspersed(false)
	fs.SortFlags = false

	out := new(base.Path)
	var verbose bool
	var threads int
	var gcRatio float64
	var emitImage bool
	var keepBuild bool
	var run bool
	var noProject bool
	stackSize, heapSize := defaultStackSize, defaultHeapSize

	fs.StringVarP(&out.Outer, flagOutputLong, flagOutput, defaultOutput, usageOutput)
	fs.BoolVarP(&verbose, flagVerboseLong, flagVerbose, defaultVerbose, usageVerbose)
	fs.IntVarP(&threads, flagThreadsLong, flagThreads, defaultThreads, usageThreads)
	fs.Float64VarP(&gcRatio, flagGCRatioLong, flagGCRatio, defaultGCRatio, usageGCRatio)
	fs.VarP(&stackSize, flagStackSizeLong, flagStackSize, usageStackSize)
	fs.VarP(&heapSize, flagHeapSizeLong, flagHeapSize, usageHeapSize)
	fs.BoolVarP(&emitImage, flagEmitImageLong, flagEmitImage, false, usageEmitImage)
	fs.BoolVarP(&keepBuild, flagKeepBuildLong, flagKeepBuild, false, usageKeepBuild)
	fs.BoolVarP(&run, flagRunLong, flagRun, false, usageRun)
	fs.BoolVarP(&noProject, flagNoProjectLong, flagNoProject, false, usageNoProject)

	if err := fs.Parse(argv[1:]); err != nil {
		usageErr := &usageError{name: argv[0], flags: fs.FlagUsages()}
		if !errors.Is(err, pflag.ErrHelp) {
			usageErr.err = err
		}
		return nil, usageErr
	}

	if run && emitImage {
		return nil, fmt.Errorf("-%s and -%s cannot be combined", flagRun, flagEmitImage)
	}

	args := fs.Args()
	switch {
	case len(args) == 0:
		return nil, errors.New("missing input file argument")
	case len(args) > 1 && !run:
		return nil, fmt.Errorf("too many arguments: %v", args)
	}

	// The entry file is named on the command line but is otherwise an
	// ordinary file, and every other path reaches the parser normalized.
	// Taken as typed, the bytecode would depend on how the compiler was
	// invoked, and a file importing the entry by another spelling would
	// parse it a second time under a second name.
	inputPath, err := base.ParsePath(args[0])
	if err != nil {
		return nil, err
	}

	var runArgs []string
	if run {
		runArgs = args[1:]
	} else if _, err := fileProvider.FileExists(out); err != nil {
		return nil, err
	}

	if threads < 1 {
		threads = defaultThreads
	}

	// The fraction is of a heap that is already there, so past one names a
	// threshold the heap can never reach, and at or below zero asks for a
	// collection before anything has been allocated.
	if gcRatio <= 0 || gcRatio > 1 {
		return nil, fmt.Errorf("gc trigger ratio must be within (0, 1]: %g", gcRatio)
	}

	if exists, err := fileProvider.FileExists(inputPath); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("input file not found: %s", inputPath)
	}

	return &Command{
		InputPath:        inputPath,
		OutputPath:       out,
		Verbose:          verbose,
		MaxThreads:       threads,
		GCTriggerRatio:   gcRatio,
		InitialStackSize: int(stackSize),
		InitialHeapSize:  int(heapSize),
		EmitImage:        emitImage,
		KeepBuild:        keepBuild,
		Run:              run,
		RunArgs:          runArgs,
		NoProject:        noProject,
	}, nil
}

// String implements the [fmt.Stringer] interface.
func (c *Command) String() string {
	json, _ := json.Marshal(c) //nolint:errcheck
	return string(json)
}
