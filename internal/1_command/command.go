// Package command provides command line interface functionality.
package command

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

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
	// an optional unit suffix. It is a [flag.Value] rather than a plain int so
	// that the short and long spellings of one flag can share a single target,
	// as the other flags here do.
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
	}

	fileProvider interface {
		FileExists(path *base.Path) (bool, error)
	}

	usageError struct {
		name string
		err  error
	}
)

var (
	_ error      = (*usageError)(nil)
	_ flag.Value = (*byteSize)(nil)
)

// String implements the [flag.Value] interface, writing the size back with the
// largest unit that divides it so that what is echoed reads like what was typed.
func (b byteSize) String() string {
	for _, unit := range byteSizeUnits {
		if int(b) >= unit.multiplier && int(b)%unit.multiplier == 0 {
			return strconv.Itoa(int(b)/unit.multiplier) + unit.suffix
		}
	}
	return strconv.Itoa(int(b)) + "B"
}

// Set implements the [flag.Value] interface. A bare number is a count of bytes.
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

// Error implements the [error] interface.
func (ue *usageError) Error() string {
	outputLine := fmt.Sprintf("  -%s, --%s string\t%s (default %q)\n", flagOutput, flagOutputLong, usageOutput, defaultOutput)
	verboseLine := fmt.Sprintf("  -%s, --%s\t%s\n", flagVerbose, flagVerboseLong, usageVerbose)
	threadsLine := fmt.Sprintf("  -%s, --%s int\t%s (default %d)\n", flagThreads, flagThreadsLong, usageThreads, defaultThreads)
	gcRatioLine := fmt.Sprintf("  -%s, --%s float\t%s (default %g)\n", flagGCRatio, flagGCRatioLong, usageGCRatio, defaultGCRatio)
	stackLine := fmt.Sprintf("  -%s, --%s size\t%s (default %s)\n", flagStackSize, flagStackSizeLong, usageStackSize, defaultStackSize)
	heapLine := fmt.Sprintf("  -%s, --%s size\t%s (default %s)\n", flagHeapSize, flagHeapSizeLong, usageHeapSize, defaultHeapSize)
	emitImageLine := fmt.Sprintf("  -%s, --%s\t%s\n", flagEmitImage, flagEmitImageLong, usageEmitImage)
	keepBuildLine := fmt.Sprintf("  -%s, --%s\t%s\n", flagKeepBuild, flagKeepBuildLong, usageKeepBuild)
	var errorLine string
	if ue.err != nil {
		errorLine = fmt.Sprintf("Error: %s\n", ue.err.Error())
	}
	return fmt.Sprintf("%sUsage of %s:\n%s%s%s%s%s%s%s%s", errorLine, ue.name, outputLine, verboseLine, threadsLine, gcRatioLine, stackLine, heapLine, emitImageLine, keepBuildLine)
}

// New builds a Command from the given argument list and file provider.
func New(argv []string, fileProvider fileProvider) (*Command, error) {
	if len(argv) == 0 {
		return nil, errors.New("missing program name")
	}

	fs := flag.NewFlagSet(argv[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	out := new(base.Path)
	var verbose bool
	var threads int
	var gcRatio float64
	var emitImage bool
	var keepBuild bool
	stackSize, heapSize := defaultStackSize, defaultHeapSize

	fs.StringVar(&out.Outer, flagOutput, defaultOutput, usageOutput)
	fs.StringVar(&out.Outer, flagOutputLong, defaultOutput, usageOutput)
	fs.BoolVar(&verbose, flagVerbose, defaultVerbose, usageVerbose)
	fs.BoolVar(&verbose, flagVerboseLong, defaultVerbose, usageVerbose)
	fs.IntVar(&threads, flagThreads, defaultThreads, usageThreads)
	fs.IntVar(&threads, flagThreadsLong, defaultThreads, usageThreads)
	fs.Float64Var(&gcRatio, flagGCRatio, defaultGCRatio, usageGCRatio)
	fs.Float64Var(&gcRatio, flagGCRatioLong, defaultGCRatio, usageGCRatio)
	fs.Var(&stackSize, flagStackSize, usageStackSize)
	fs.Var(&stackSize, flagStackSizeLong, usageStackSize)
	fs.Var(&heapSize, flagHeapSize, usageHeapSize)
	fs.Var(&heapSize, flagHeapSizeLong, usageHeapSize)
	fs.BoolVar(&emitImage, flagEmitImage, false, usageEmitImage)
	fs.BoolVar(&emitImage, flagEmitImageLong, false, usageEmitImage)
	fs.BoolVar(&keepBuild, flagKeepBuild, false, usageKeepBuild)
	fs.BoolVar(&keepBuild, flagKeepBuildLong, false, usageKeepBuild)

	fs.Usage = func() {}

	if err := fs.Parse(argv[1:]); err != nil {
		usageErr := &usageError{name: argv[0]}
		if !errors.Is(err, flag.ErrHelp) {
			usageErr.err = err
		}
		return nil, usageErr
	}

	var inputPath *base.Path
	switch args := fs.Args(); len(args) {
	case 0:
		return nil, errors.New("missing input file argument")
	case 1:
		// The entry file is named on the command line but is otherwise an
		// ordinary file, and every other path reaches the parser normalized.
		// Taken as typed, the bytecode would depend on how the compiler was
		// invoked, and a file importing the entry by another spelling would
		// parse it a second time under a second name.
		p, err := base.ParsePath(args[0])
		if err != nil {
			return nil, err
		}
		inputPath = p
	default:
		return nil, fmt.Errorf("too many arguments: %v", args)
	}

	if _, err := fileProvider.FileExists(out); err != nil {
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
	}, nil
}

// String implements the [fmt.Stringer] interface.
func (c *Command) String() string {
	json, _ := json.Marshal(c) //nolint:errcheck
	return string(json)
}
