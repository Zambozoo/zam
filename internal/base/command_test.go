package base

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base/target"
)

func errorContains(want string) assert.ErrorAssertionFunc {
	return func(tt assert.TestingT, err error, a ...any) bool {
		return assert.ErrorContains(tt, err, want, a)
	}
}

func errorNotContains(want string) assert.ErrorAssertionFunc {
	return func(tt assert.TestingT, err error, a ...any) bool {
		if h, ok := tt.(interface {
			Helper()
		}); ok {
			h.Helper()
		}
		if !assert.Error(tt, err, a...) {
			return false
		}

		actual := err.Error()
		if strings.Contains(actual, want) {
			return assert.Fail(tt, fmt.Sprintf("Error %#v does not contain %#v", actual, want), a...)
		}

		return true
	}
}

func TestUsageError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ue      *usageError
		errFunc assert.ErrorAssertionFunc
	}{
		{
			name: "basic",
			ue: &usageError{
				name: "zam",
			},
			errFunc: errorNotContains("\nUsage"),
		},
		{
			name: "error",
			ue: &usageError{
				name: "zam",
				err:  assert.AnError,
			},
			errFunc: errorContains("\nUsage"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.errFunc(t, tt.ue)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	// The entry file is normalized, so what the file provider is asked about is
	// the absolute path rather than the argument as it was typed.
	absInput, err := filepath.Abs("input")
	require.NoError(t, err)
	inputPath := Path(absInput)

	tests := []struct {
		name         string
		argv         []string
		fileProvider fileProvider
		want         *Command
		errFunc      assert.ErrorAssertionFunc
	}{
		{
			name:    "missing program name",
			errFunc: errorContains("missing program name"),
		},
		{
			name:    "missing command",
			argv:    []string{"zam"},
			errFunc: errorContains("missing command"),
		},
		{
			name:    "unknown command",
			argv:    []string{"zam", "input"},
			errFunc: errorContains(`unknown command "input"`),
		},
		{
			name: "help",
			argv: []string{"zam", "--help"},
			errFunc: func(tt assert.TestingT, err error, a ...any) bool {
				var ue *usageError
				return assert.ErrorAs(tt, err, &ue) && assert.NoError(tt, ue.err) &&
					assert.Contains(tt, ue.flags, "clean")
			},
		},
		{
			name:    "get",
			argv:    []string{"zam", "get", "example.com/a@v1", "example.com/b@v2"},
			want:    &Command{Verb: VerbGet, Refs: []string{"example.com/a@v1", "example.com/b@v2"}, WriteProject: true},
			errFunc: assert.NoError,
		},
		{
			name:    "get everything",
			argv:    []string{"zam", "get"},
			want:    &Command{Verb: VerbGet, Refs: []string{}, WriteProject: true},
			errFunc: assert.NoError,
		},
		{
			name:    "get as",
			argv:    []string{"zam", "get", "--as", "list", "example.com/zam-list@v1"},
			want:    &Command{Verb: VerbGet, Refs: []string{"example.com/zam-list@v1"}, As: "list", WriteProject: true},
			errFunc: assert.NoError,
		},
		{
			name:    "get as names one dependency",
			argv:    []string{"zam", "get", "--as", "list", "example.com/a@v1", "example.com/b@v1"},
			errFunc: errorContains("--as names one dependency, but 2 were given"),
		},
		{
			name:    "clean",
			argv:    []string{"zam", "clean"},
			want:    &Command{Verb: VerbClean},
			errFunc: assert.NoError,
		},
		{
			name:    "clean takes no arguments",
			argv:    []string{"zam", "clean", "input"},
			errFunc: errorContains("too many arguments"),
		},
		{
			name:    "lsp",
			argv:    []string{"zam", "lsp"},
			want:    &Command{Verb: VerbLSP},
			errFunc: assert.NoError,
		},
		{
			name:    "debug",
			argv:    []string{"zam", "debug"},
			want:    &Command{Verb: VerbDebug},
			errFunc: assert.NoError,
		},
		{
			name:    "version",
			argv:    []string{"zam", "version"},
			want:    &Command{Verb: VerbVersion},
			errFunc: assert.NoError,
		},
		{
			name:    "version flag",
			argv:    []string{"zam", "--version"},
			want:    &Command{Verb: VerbVersion},
			errFunc: assert.NoError,
		},
		{
			name: "build help flag",
			argv: []string{"zam", "build", "--help"},
			errFunc: func(tt assert.TestingT, err error, a ...any) bool {
				var ue *usageError
				return assert.ErrorAs(tt, err, &ue) && assert.NoError(tt, ue.err)
			},
		},
		{
			name:    "invalid flag",
			argv:    []string{"zam", "build", "--invalid"},
			errFunc: errorContains("unknown flag: --invalid"),
		},
		{
			name:    "missing input file",
			argv:    []string{"zam", "build"},
			errFunc: errorContains("missing input file argument"),
		},
		{
			name:    "too many arguments",
			argv:    []string{"zam", "build", "input.txt", "output.txt"},
			errFunc: errorContains("too many arguments"),
		},
		{
			name: "output file exists error",
			argv: []string{"zam", "build", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(false, assert.AnError)
				return m
			}(),
			errFunc: errorContains(assert.AnError.Error()),
		},
		{
			name: "input file exists error",
			argv: []string{"zam", "build", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(false, assert.AnError)
				return m
			}(),
			errFunc: errorContains(assert.AnError.Error()),
		},
		{
			name: "input file not found",
			argv: []string{"zam", "build", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(false, nil)
				return m
			}(),
			errFunc: errorContains("input file not found: " + absInput),
		},
		{
			name: "invalid threads",
			argv: []string{"zam", "build", "--threads", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				Verbose:          false,
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeDefault,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			// Paths are the keys files are known by, and their order decides
			// type ids and the order static initializers run in. Two spellings
			// of one file have to name one file, or the same source compiles to
			// a different program depending on how it was invoked.
			name: "entry path is the same file however it is spelled",
			argv: []string{"zam", "build", "./sub/../input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				Verbose:          false,
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeDefault,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "build for a target",
			argv: []string{"zam", "build", "--target", "x86_64-macos", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeDefault,
				Target:           mustTarget(t, "x86_64-macos"),
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "unknown target",
			argv: []string{"zam", "build", "--target", "riscv64-linux-musl", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains(`unknown target "riscv64-linux-musl"`),
		},
		{
			name:    "run takes no target",
			argv:    []string{"zam", "run", "--target", "x86_64-macos", "input"},
			errFunc: errorContains("unknown flag: --target"),
		},
		{
			name: "sizes take a unit suffix",
			argv: []string{"zam", "build", "--stack-size", "1MB", "--heap-size", "2GB", "--gc-ratio", "0.5", "-N", "1MB", "-M", "4GB", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				Verbose:          false,
				MaxThreads:       1,
				GCTriggerRatio:   0.5,
				InitialStackSize: 1 << 20,
				InitialHeapSize:  2 << 30,
				NurserySize:      1 << 20,
				MaxHeapSize:      4 << 30,
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeDefault,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "run passes trailing arguments and skips the output check",
			argv: []string{"zam", "run", "input", "a", "-b"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				Verb:             VerbRun,
				InputPath:        inputPath,
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeDefault,
				RunArgs:          []string{"a", "-b"},
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "no project",
			argv: []string{"zam", "build", "-n", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeDefault,
				NoProject:        true,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "short flags combine",
			argv: []string{"zam", "run", "-nv", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				Verb:             VerbRun,
				InputPath:        inputPath,
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeDefault,
				Verbose:          true,
				RunArgs:          []string{},
				NoProject:        true,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name:    "single dash long flag",
			argv:    []string{"zam", "build", "-verbose", "input"},
			errFunc: errorContains("unknown shorthand flag: 'e'"),
		},
		{
			name:    "run does not take an output",
			argv:    []string{"zam", "run", "-o", "out", "input"},
			errFunc: errorContains("unknown shorthand flag: 'o'"),
		},
		{
			name:    "unparsable size",
			argv:    []string{"zam", "build", "--heap-size", "16TB", "input"},
			errFunc: errorContains(`invalid size "16TB"`),
		},
		{
			name:    "non positive size",
			argv:    []string{"zam", "build", "--stack-size", "0KB", "input"},
			errFunc: errorContains(`size must be positive: "0KB"`),
		},
		{
			name: "heap larger than its maximum",
			argv: []string{"zam", "build", "--max-heap-size", "1MB", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("heap size 16MB and nursery size 8MB must not exceed max heap size 1MB"),
		},
		{
			name: "gc ratio the heap can never reach",
			argv: []string{"zam", "build", "--gc-ratio", "1.5", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc trigger ratio must be within (0, 1]: 1.5"),
		},
		{
			name: "gc ratio of zero",
			argv: []string{"zam", "build", "--gc-ratio", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc trigger ratio must be within (0, 1]: 0"),
		},
		{
			name: "gc flags",
			argv: []string{"zam", "build", "--gc-markers", "4", "--gc-concurrent=false", "--gc-remark-growth", "0.5", "--gc-preclean-passes", "0", "--gc-return-age", "1", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        4,
				GCConcurrent:     false,
				GCRemarkGrowth:   0.5,
				GCPrecleanPasses: 0,
				GCReturnAge:      1,
				Optimization:     OptimizeDefault,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "negative gc markers",
			argv: []string{"zam", "build", "--gc-markers", "-1", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc markers must be within [0, 64]: -1"),
		},
		{
			name: "too many gc markers",
			argv: []string{"zam", "build", "--gc-markers", "65", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc markers must be within [0, 64]: 65"),
		},
		{
			name: "gc remark growth of zero",
			argv: []string{"zam", "build", "--gc-remark-growth", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc remark growth must be positive: 0"),
		},
		{
			name: "too many gc preclean passes",
			argv: []string{"zam", "build", "--gc-preclean-passes", "17", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc preclean passes must be within [0, 16]: 17"),
		},
		{
			name: "optimization",
			argv: []string{"zam", "build", "-O3", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeAggressive,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "optimization for size",
			argv: []string{"zam", "build", "-Os", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       Path("out"),
				MaxThreads:       1,
				GCTriggerRatio:   defaultGCRatio,
				InitialStackSize: int(defaultStackSize),
				InitialHeapSize:  int(defaultHeapSize),
				NurserySize:      int(defaultNursery),
				MaxHeapSize:      int(defaultMaxHeap),
				GCMarkers:        defaultGCMarkers,
				GCConcurrent:     defaultGCConcurrent,
				GCRemarkGrowth:   defaultGCRemarkGrowth,
				GCPrecleanPasses: defaultGCPrecleanPasses,
				GCReturnAge:      defaultGCReturnAge,
				Optimization:     OptimizeSize,
				WriteProject:     true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "unknown optimization",
			argv: []string{"zam", "build", "-O4", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("optimization must be one of 0, 1, 2, 3 or s: 4"),
		},
		{
			name: "gc return age of zero",
			argv: []string{"zam", "build", "--gc-return-age", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(Path("out")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc return age must be positive: 0"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := NewCommand(tt.argv, tt.fileProvider)
			tt.errFunc(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// mustTarget answers the target called name.
func mustTarget(t *testing.T, name string) target.Target {
	t.Helper()
	parsed, err := target.Parse(name)
	require.NoError(t, err)
	return parsed
}

func TestCommand_String(t *testing.T) {
	tests := []struct {
		name string
		cmd  *Command
		want string
	}{
		{
			name: "basic",
			cmd: &Command{
				InputPath:        Path("input"),
				OutputPath:       Path("out"),
				Verbose:          true,
				MaxThreads:       10,
				GCTriggerRatio:   0.75,
				InitialStackSize: 1 << 19,
				InitialHeapSize:  1 << 24,
			},
			want: `{"Verb":0,"InputPath":"input","OutputPath":"out","Verbose":true,"MaxThreads":10,"GCTriggerRatio":0.75,"InitialStackSize":524288,"InitialHeapSize":16777216,"NurserySize":0,"MaxHeapSize":0,"GCMarkers":0,"GCConcurrent":false,"GCRemarkGrowth":0,"GCPrecleanPasses":0,"GCReturnAge":0,"Optimization":"","Target":{"Name":"","OS":"","Arch":"","Triple":"","Zig":""},"RunArgs":null,"TestTargets":null,"TestPaths":null,"RunPattern":"","Parallel":0,"Refs":null,"As":"","NoProject":false,"WriteProject":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cmd.String())
		})
	}
}

func TestNewTest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		argv     []string
		targets  []string
		runArgs  []string
		pattern  string
		parallel int
		errFunc  assert.ErrorAssertionFunc
	}{
		{
			name:     "defaults",
			argv:     []string{"zam", "test"},
			targets:  []string{"."},
			parallel: runtime.NumCPU(),
			errFunc:  assert.NoError,
		},
		{
			name:     "targets, flags and arguments",
			argv:     []string{"zam", "test", "--run", "Append$", "--parallel", "2", "list", "map/...", "--", "x", "-y"},
			targets:  []string{"list", "map/..."},
			runArgs:  []string{"x", "-y"},
			pattern:  "Append$",
			parallel: 2,
			errFunc:  assert.NoError,
		},
		{
			name:     "arguments without targets",
			argv:     []string{"zam", "test", "--", "x"},
			targets:  []string{"."},
			runArgs:  []string{"x"},
			parallel: runtime.NumCPU(),
			errFunc:  assert.NoError,
		},
		{
			name:    "invalid pattern",
			argv:    []string{"zam", "test", "--run", "(", "."},
			errFunc: errorContains("invalid --run pattern"),
		},
		{
			name:    "no parallelism",
			argv:    []string{"zam", "test", "--parallel", "0"},
			errFunc: errorContains("parallel must be positive: 0"),
		},
		{
			name:    "test does not take an output",
			argv:    []string{"zam", "test", "-o", "out"},
			errFunc: errorContains("unknown shorthand flag: 'o'"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewCommand(tt.argv, nil)
			if !tt.errFunc(t, err) || err != nil {
				return
			}
			assert.Equal(t, VerbTest, got.Verb)
			assert.Equal(t, tt.targets, got.TestTargets)
			assert.Equal(t, tt.runArgs, got.RunArgs)
			assert.Equal(t, tt.pattern, got.RunPattern)
			assert.Equal(t, tt.parallel, got.Parallel)
			assert.Equal(t, defaultGCRatio, got.GCTriggerRatio, "the compile flags apply too")
			assert.True(t, got.WriteProject)
		})
	}
}
