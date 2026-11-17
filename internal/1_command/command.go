// Package command provides command line interface functionality.
package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	base "github.com/zambozoo/zam/internal/0_base"
	"github.com/zambozoo/zam/internal/target"
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

	flagNurserySize     = "N"
	flagNurserySizeLong = "nursery-size"
	usageNurserySize    = "bytes allocated since the last collection that trigger a minor collection"

	flagMaxHeapSize     = "M"
	flagMaxHeapSizeLong = "max-heap-size"
	usageMaxHeapSize    = "most memory the heap may ever hold"

	flagGCMarkersLong = "gc-markers"
	usageGCMarkers    = "threads that mark during a major collection, 0 for half the processors up to 8"

	flagGCConcurrentLong = "gc-concurrent"
	usageGCConcurrent    = "mark major collections the heap's growth asks for alongside the program"

	flagGCRemarkGrowthLong = "gc-remark-growth"
	usageGCRemarkGrowth    = "fraction of its size the heap may grow by while marking alongside the program before the remark is forced"

	flagGCPrecleanPassesLong = "gc-preclean-passes"
	usageGCPrecleanPasses    = "passes over the cards dirtied while marking alongside the program, before the remark"

	flagGCReturnAgeLong = "gc-return-age"
	usageGCReturnAge    = "collections a run of free pages waits before it is given back to the system"

	flagEmitImage     = "E"
	flagEmitImageLong = "emit-image"
	usageEmitImage    = "write the raw bytecode image instead of linking a binary"

	flagNativeLong = "native"
	usageNative    = "compile to native code through LLVM (experimental: code it cannot compile yet ends the program when reached)"

	flagTargetLong = "target"
	usageTarget    = "the platform to compile native code for, the host when unset: one of "

	flagKeepBuild     = "k"
	flagKeepBuildLong = "keep-build"
	usageKeepBuild    = "keep the temporary build directory and print its path on failure or success"

	flagRunPatternLong = "run"
	usageRunPattern    = "run only the tests whose names this regular expression matches"

	flagParallelLong = "parallel"
	usageParallel    = "test files run at once"

	flagAsLong = "as"
	usageAs    = "package name to add the dependency under, the last element of its repository by default"

	flagNoProject     = "n"
	flagNoProjectLong = "no-project"
	usageNoProject    = "ignore project files and compile local files as one project depending only on std"

	defaultOutput    = "out"
	defaultThreads   = 1
	defaultVerbose   = false
	defaultGCRatio   = 0.75
	defaultStackSize = byteSize(512 << 10)
	defaultHeapSize  = byteSize(16 << 20)
	defaultNursery   = byteSize(8 << 20)
	defaultMaxHeap   = byteSize(64 << 30)

	defaultGCMarkers        = 0
	defaultGCConcurrent     = true
	defaultGCRemarkGrowth   = 1.0
	defaultGCPrecleanPasses = 2
	defaultGCReturnAge      = 3

	maxGCMarkers        = 64
	maxGCPrecleanPasses = 16
)

// Verbs are what the command line can ask the compiler to do.
const (
	// VerbBuild compiles a program to a binary or image.
	VerbBuild Verb = iota
	// VerbRun compiles a program to a temporary binary and runs it.
	VerbRun
	// VerbGet adds dependencies to the nearest project file and fetches every
	// dependency it has.
	VerbGet
	// VerbTest compiles the tests in test files and runs each file's tests in
	// a process of its own.
	VerbTest
	// VerbClean removes every fetched dependency.
	VerbClean
	// VerbLSP serves the language server protocol over stdin and stdout.
	VerbLSP
	// VerbDebug serves the debug adapter protocol over stdin and stdout.
	VerbDebug
)

// verbs are the names of the verbs, in the order usage lists them.
var verbs = []struct {
	name  string
	verb  Verb
	usage string
}{
	{"build", VerbBuild, "compile a program to a binary"},
	{"run", VerbRun, "compile a program and run it, passing on the arguments after its file"},
	{"test", VerbTest, "compile and run the tests in test files, passing on the arguments after --"},
	{"get", VerbGet, "add dependencies to the project file and fetch every dependency"},
	{"clean", VerbClean, "remove every fetched dependency"},
	{"lsp", VerbLSP, "serve the language server protocol over stdin and stdout"},
	{"debug", VerbDebug, "serve the debug adapter protocol over stdin and stdout"},
}

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

	// Verb is what the command line asks the compiler to do.
	Verb int

	Command struct {
		// Verb is what the command asks for, and decides which other fields
		// are set.
		Verb Verb

		// InputPath is the file to read from
		// This is the last argument to the command
		// Required, must not be empty, must be a valid non-directory file path.
		InputPath base.Path

		// OutputFile is the file to write to
		// Uses the -o flag with a default of `out`
		// Must be a valid file path.
		OutputPath base.Path

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

		// NurserySize is how many bytes may be allocated since the last
		// collection before a minor collection is triggered.
		// Uses the -N flag with a default of `8MB`
		NurserySize int

		// MaxHeapSize is the most memory the heap may ever hold, and is reserved
		// up front as address space.
		// Uses the -M flag with a default of `64GB`
		MaxHeapSize int

		// GCMarkers is how many threads mark during a major collection, zero
		// leaving it to the runtime: half the processors, up to 8.
		// Uses the --gc-markers flag with a default of `0`, must be within [0, 64].
		GCMarkers int

		// GCConcurrent has a major collection the heap's growth asks for mark
		// alongside the program.
		// Uses the --gc-concurrent flag with a default of `true`.
		GCConcurrent bool

		// GCRemarkGrowth is how far the heap may grow while marking alongside
		// the program, as a fraction of its size, before the remark is forced.
		// Uses the --gc-remark-growth flag with a default of `1`, must be positive.
		GCRemarkGrowth float64

		// GCPrecleanPasses is how many times the cards dirtied while marking
		// alongside the program are scanned before the remark.
		// Uses the --gc-preclean-passes flag with a default of `2`, must be within [0, 16].
		GCPrecleanPasses int

		// GCReturnAge is how many collections a run of free pages waits before
		// it is given back to the system.
		// Uses the --gc-return-age flag with a default of `3`, must be positive.
		GCReturnAge int

		// EmitImage skips the build driver and writes the raw bytecode image to
		// OutputPath instead of linking a native binary. Uses the -E flag with a
		// default of `false`.
		EmitImage bool

		// Native compiles the program to native code through LLVM instead of
		// embedding its bytecode image. Uses the --native flag with a default
		// of `false`.
		Native bool

		// Target is the platform native code is compiled for, the host when
		// unset. Uses the --target flag, which needs --native.
		Target target.Target

		// KeepBuild leaves the build driver's temporary directory in place and
		// prints its path, so the generated wrapper and extracted runtime can be
		// inspected. Uses the -k flag with a default of `false`.
		KeepBuild bool

		// RunArgs are the arguments after the input file, passed to the program
		// it runs.
		RunArgs []string

		// TestTargets are what test was asked to test, as written: test files,
		// directories, or directories followed by /... for every directory
		// beneath them.
		TestTargets []string

		// TestPaths are test files parsed as roots beside InputPath.
		TestPaths []base.Path

		// RunPattern picks the tests test runs by name.
		// Uses the --run flag, and must be a regular expression.
		RunPattern string

		// Parallel is how many test files test runs at once.
		// Uses the --parallel flag with a default of the number of processors.
		Parallel int

		// Refs are the dependencies get adds, each spelled repo@version.
		Refs []string

		// As is the package name get adds its one dependency under.
		As string

		// NoProject skips the search for project files, so every local file
		// belongs to an implicit project that depends only on std. Uses the -n
		// flag with a default of `false`.
		NoProject bool

		// WriteProject lets the compiler record the hash of a dependency the
		// project file lacks one for. It is set for the command line, and not
		// for tools that only read a project.
		WriteProject bool
	}

	fileProvider interface {
		FileExists(path base.Path) (bool, error)
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

// New builds a Command from the given argument list and file provider. The
// first argument after the program's name is the verb, and the rest are its
// flags and arguments.
func New(argv []string, fileProvider fileProvider) (*Command, error) {
	if len(argv) == 0 {
		return nil, errors.New("missing program name")
	}
	if len(argv) == 1 {
		return nil, &usageError{name: argv[0], flags: verbUsages(), err: errors.New("missing command")}
	}

	name := argv[1]
	switch name {
	case "-h", "--help", "help":
		return nil, &usageError{name: argv[0], flags: verbUsages()}
	}
	for _, v := range verbs {
		if v.name != name {
			continue
		}
		name := argv[0] + " " + name
		switch v.verb {
		case VerbBuild, VerbRun, VerbTest:
			return newCompile(v.verb, name, argv[2:], fileProvider)
		case VerbGet:
			return newGet(name, argv[2:])
		default:
			return newBare(v.verb, name, argv[2:])
		}
	}
	return nil, &usageError{name: argv[0], flags: verbUsages(), err: fmt.Errorf("unknown command %q", name)}
}

// verbUsages lists the verbs and what each does.
func verbUsages() string {
	var b strings.Builder
	for _, v := range verbs {
		fmt.Fprintf(&b, "  %-6s %s\n", v.name, v.usage)
	}
	return b.String()
}

// newFlagSet returns an empty flag set for the verb called name.
func newFlagSet(name string) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.SetInterspersed(false)
	fs.SortFlags = false
	return fs
}

// parse parses args into fs, answering a failure or a request for help with
// the verb's usage.
func parse(fs *pflag.FlagSet, name string, args []string) error {
	if err := fs.Parse(args); err != nil {
		usageErr := &usageError{name: name, flags: fs.FlagUsages()}
		if !errors.Is(err, pflag.ErrHelp) {
			usageErr.err = err
		}
		return usageErr
	}
	return nil
}

// newGet builds the command for get, whose arguments are the dependencies to
// add.
func newGet(name string, args []string) (*Command, error) {
	fs := newFlagSet(name)
	var as string
	fs.StringVar(&as, flagAsLong, "", usageAs)
	if err := parse(fs, name, args); err != nil {
		return nil, err
	}

	refs := fs.Args()
	if as != "" && len(refs) != 1 {
		return nil, fmt.Errorf("--%s names one dependency, but %d were given", flagAsLong, len(refs))
	}
	return &Command{Verb: VerbGet, Refs: refs, As: as, WriteProject: true}, nil
}

// newBare builds the command for a verb that takes no flags or arguments.
func newBare(verb Verb, name string, args []string) (*Command, error) {
	fs := newFlagSet(name)
	if err := parse(fs, name, args); err != nil {
		return nil, err
	}
	if len(fs.Args()) > 0 {
		return nil, fmt.Errorf("too many arguments: %v", fs.Args())
	}
	return &Command{Verb: verb}, nil
}

// newCompile builds the command for build or run.
func newCompile(verb Verb, name string, args []string, fileProvider fileProvider) (*Command, error) {
	fs := newFlagSet(name)

	var out string
	var verbose bool
	var threads int
	var gcRatio float64
	var emitImage bool
	var keepBuild bool
	var native bool
	var targetName string
	var noProject bool
	var gcMarkers, gcPrecleanPasses, gcReturnAge int
	var gcConcurrent bool
	var gcRemarkGrowth float64
	var runPattern string
	var parallel int
	stackSize, heapSize := defaultStackSize, defaultHeapSize
	nurserySize, maxHeapSize := defaultNursery, defaultMaxHeap

	if verb == VerbBuild {
		fs.StringVarP(&out, flagOutputLong, flagOutput, defaultOutput, usageOutput)
		fs.BoolVarP(&emitImage, flagEmitImageLong, flagEmitImage, false, usageEmitImage)
	}
	fs.BoolVarP(&verbose, flagVerboseLong, flagVerbose, defaultVerbose, usageVerbose)
	fs.IntVarP(&threads, flagThreadsLong, flagThreads, defaultThreads, usageThreads)
	fs.Float64VarP(&gcRatio, flagGCRatioLong, flagGCRatio, defaultGCRatio, usageGCRatio)
	fs.VarP(&stackSize, flagStackSizeLong, flagStackSize, usageStackSize)
	fs.VarP(&heapSize, flagHeapSizeLong, flagHeapSize, usageHeapSize)
	fs.VarP(&nurserySize, flagNurserySizeLong, flagNurserySize, usageNurserySize)
	fs.VarP(&maxHeapSize, flagMaxHeapSizeLong, flagMaxHeapSize, usageMaxHeapSize)
	fs.IntVar(&gcMarkers, flagGCMarkersLong, defaultGCMarkers, usageGCMarkers)
	fs.BoolVar(&gcConcurrent, flagGCConcurrentLong, defaultGCConcurrent, usageGCConcurrent)
	fs.Float64Var(&gcRemarkGrowth, flagGCRemarkGrowthLong, defaultGCRemarkGrowth, usageGCRemarkGrowth)
	fs.IntVar(&gcPrecleanPasses, flagGCPrecleanPassesLong, defaultGCPrecleanPasses, usageGCPrecleanPasses)
	fs.IntVar(&gcReturnAge, flagGCReturnAgeLong, defaultGCReturnAge, usageGCReturnAge)
	fs.BoolVarP(&keepBuild, flagKeepBuildLong, flagKeepBuild, false, usageKeepBuild)
	if verb == VerbBuild || verb == VerbRun || verb == VerbTest {
		fs.BoolVar(&native, flagNativeLong, false, usageNative)
	}
	if verb == VerbBuild {
		fs.StringVar(&targetName, flagTargetLong, "", usageTarget+strings.Join(target.Names(), ", "))
	}
	fs.BoolVarP(&noProject, flagNoProjectLong, flagNoProject, false, usageNoProject)
	if verb == VerbTest {
		fs.StringVar(&runPattern, flagRunPatternLong, "", usageRunPattern)
		fs.IntVar(&parallel, flagParallelLong, runtime.NumCPU(), usageParallel)
	}

	if err := parse(fs, name, args); err != nil {
		return nil, err
	}

	args = fs.Args()
	var inputPath base.Path
	var runArgs, testTargets []string
	if verb == VerbTest {
		testTargets = args
		if dash := fs.ArgsLenAtDash(); dash >= 0 {
			testTargets, runArgs = args[:dash], args[dash:]
		} else if dash := slices.Index(args, "--"); dash >= 0 {
			testTargets, runArgs = args[:dash], args[dash+1:]
		}
		if len(testTargets) == 0 {
			testTargets = []string{"."}
		}
		if _, err := regexp.Compile(runPattern); err != nil {
			return nil, fmt.Errorf("invalid --%s pattern: %w", flagRunPatternLong, err)
		}
		if parallel < 1 {
			return nil, fmt.Errorf("parallel must be positive: %d", parallel)
		}
	} else {
		switch {
		case len(args) == 0:
			return nil, errors.New("missing input file argument")
		case len(args) > 1 && verb == VerbBuild:
			return nil, fmt.Errorf("too many arguments: %v", args)
		}

		// The entry file is named on the command line but is otherwise an
		// ordinary file, and every other path reaches the parser normalized.
		// Taken as typed, the bytecode would depend on how the compiler was
		// invoked, and a file importing the entry by another spelling would
		// parse it a second time under a second name.
		var err error
		if inputPath, err = base.ParsePath(args[0]); err != nil {
			return nil, err
		}

		if verb == VerbRun {
			runArgs = args[1:]
		} else if _, err := fileProvider.FileExists(base.Path(out)); err != nil {
			return nil, err
		}
	}

	var platform target.Target
	if targetName != "" {
		if !native {
			return nil, fmt.Errorf("--%s needs --%s", flagTargetLong, flagNativeLong)
		}
		var err error
		if platform, err = target.Parse(targetName); err != nil {
			return nil, err
		}
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

	if gcMarkers < 0 || gcMarkers > maxGCMarkers {
		return nil, fmt.Errorf("gc markers must be within [0, %d]: %d", maxGCMarkers, gcMarkers)
	}
	if gcRemarkGrowth <= 0 {
		return nil, fmt.Errorf("gc remark growth must be positive: %g", gcRemarkGrowth)
	}
	if gcPrecleanPasses < 0 || gcPrecleanPasses > maxGCPrecleanPasses {
		return nil, fmt.Errorf("gc preclean passes must be within [0, %d]: %d", maxGCPrecleanPasses, gcPrecleanPasses)
	}
	if gcReturnAge < 1 {
		return nil, fmt.Errorf("gc return age must be positive: %d", gcReturnAge)
	}

	if heapSize > maxHeapSize || nurserySize > maxHeapSize {
		return nil, fmt.Errorf("heap size %s and nursery size %s must not exceed max heap size %s",
			heapSize, nurserySize, maxHeapSize)
	}

	if verb != VerbTest {
		if exists, err := fileProvider.FileExists(inputPath); err != nil {
			return nil, err
		} else if !exists {
			return nil, fmt.Errorf("input file not found: %s", inputPath)
		}
	}

	return &Command{
		Verb:             verb,
		InputPath:        inputPath,
		OutputPath:       base.Path(out),
		Verbose:          verbose,
		MaxThreads:       threads,
		GCTriggerRatio:   gcRatio,
		InitialStackSize: int(stackSize),
		InitialHeapSize:  int(heapSize),
		NurserySize:      int(nurserySize),
		MaxHeapSize:      int(maxHeapSize),
		GCMarkers:        gcMarkers,
		GCConcurrent:     gcConcurrent,
		GCRemarkGrowth:   gcRemarkGrowth,
		GCPrecleanPasses: gcPrecleanPasses,
		GCReturnAge:      gcReturnAge,
		EmitImage:        emitImage,
		KeepBuild:        keepBuild,
		Native:           native,
		Target:           platform,
		RunArgs:          runArgs,
		TestTargets:      testTargets,
		RunPattern:       runPattern,
		Parallel:         parallel,
		NoProject:        noProject,
		WriteProject:     true,
	}, nil
}

// String implements the [fmt.Stringer] interface.
func (c *Command) String() string {
	json, _ := json.Marshal(c) //nolint:errcheck
	return string(json)
}
