package command

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
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
	inputPath := base.NewPath(absInput, "")

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
			name: "help flag",
			argv: []string{"zam", "--help"},
			errFunc: func(tt assert.TestingT, err error, a ...any) bool {
				var ue *usageError
				return assert.ErrorAs(tt, err, &ue) && assert.NoError(tt, ue.err)
			},
		},
		{
			name:    "invalid flag",
			argv:    []string{"zam", "--invalid"},
			errFunc: errorContains("unknown flag: --invalid"),
		},
		{
			name:    "missing input file",
			argv:    []string{"zam"},
			errFunc: errorContains("missing input file argument"),
		},
		{
			name:    "too many arguments",
			argv:    []string{"zam", "input.txt", "output.txt"},
			errFunc: errorContains("too many arguments"),
		},
		{
			name: "output file exists error",
			argv: []string{"zam", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(false, assert.AnError)
				return m
			}(),
			errFunc: errorContains(assert.AnError.Error()),
		},
		{
			name: "input file exists error",
			argv: []string{"zam", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(false, assert.AnError)
				return m
			}(),
			errFunc: errorContains(assert.AnError.Error()),
		},
		{
			name: "input file not found",
			argv: []string{"zam", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(false, nil)
				return m
			}(),
			errFunc: errorContains("input file not found: " + absInput),
		},
		{
			name: "invalid threads",
			argv: []string{"zam", "--threads", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       base.NewPath("out", ""),
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
			},
			errFunc: assert.NoError,
		},
		{
			// Paths are the keys files are known by, and their order decides
			// type ids and the order static initializers run in. Two spellings
			// of one file have to name one file, or the same source compiles to
			// different bytecode depending on how it was invoked.
			name: "entry path is the same file however it is spelled",
			argv: []string{"zam", "./sub/../input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       base.NewPath("out", ""),
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
			},
			errFunc: assert.NoError,
		},
		{
			name: "sizes take a unit suffix",
			argv: []string{"zam", "--stack-size", "1MB", "--heap-size", "2GB", "--gc-ratio", "0.5", "-N", "1MB", "-M", "4GB", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       base.NewPath("out", ""),
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
			},
			errFunc: assert.NoError,
		},
		{
			name: "run passes trailing arguments and skips the output check",
			argv: []string{"zam", "-r", "input", "a", "-b"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       base.NewPath("out", ""),
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
				Run:              true,
				RunArgs:          []string{"a", "-b"},
			},
			errFunc: assert.NoError,
		},
		{
			name: "no project",
			argv: []string{"zam", "-n", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       base.NewPath("out", ""),
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
				NoProject:        true,
			},
			errFunc: assert.NoError,
		},
		{
			name: "short flags combine",
			argv: []string{"zam", "-rn", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       base.NewPath("out", ""),
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
				Run:              true,
				RunArgs:          []string{},
				NoProject:        true,
			},
			errFunc: assert.NoError,
		},
		{
			name:    "single dash long flag",
			argv:    []string{"zam", "-run", "input"},
			errFunc: errorContains("unknown shorthand flag: 'u'"),
		},
		{
			name:    "run with emit image",
			argv:    []string{"zam", "-r", "-E", "input"},
			errFunc: errorContains("-r and -E cannot be combined"),
		},
		{
			name:    "unparsable size",
			argv:    []string{"zam", "--heap-size", "16TB", "input"},
			errFunc: errorContains(`invalid size "16TB"`),
		},
		{
			name:    "non positive size",
			argv:    []string{"zam", "--stack-size", "0KB", "input"},
			errFunc: errorContains(`size must be positive: "0KB"`),
		},
		{
			name: "heap larger than its maximum",
			argv: []string{"zam", "--max-heap-size", "1MB", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("heap size 16MB and nursery size 8MB must not exceed max heap size 1MB"),
		},
		{
			name: "gc ratio the heap can never reach",
			argv: []string{"zam", "--gc-ratio", "1.5", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc trigger ratio must be within (0, 1]: 1.5"),
		},
		{
			name: "gc ratio of zero",
			argv: []string{"zam", "--gc-ratio", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc trigger ratio must be within (0, 1]: 0"),
		},
		{
			name: "gc flags",
			argv: []string{"zam", "--gc-markers", "4", "--gc-concurrent=false", "--gc-remark-growth", "0.5", "--gc-preclean-passes", "0", "--gc-return-age", "1", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				m.EXPECT().FileExists(inputPath).Return(true, nil)
				return m
			}(),
			want: &Command{
				InputPath:        inputPath,
				OutputPath:       base.NewPath("out", ""),
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
			},
			errFunc: assert.NoError,
		},
		{
			name: "negative gc markers",
			argv: []string{"zam", "--gc-markers", "-1", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc markers must be within [0, 64]: -1"),
		},
		{
			name: "too many gc markers",
			argv: []string{"zam", "--gc-markers", "65", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc markers must be within [0, 64]: 65"),
		},
		{
			name: "gc remark growth of zero",
			argv: []string{"zam", "--gc-remark-growth", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc remark growth must be positive: 0"),
		},
		{
			name: "too many gc preclean passes",
			argv: []string{"zam", "--gc-preclean-passes", "17", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc preclean passes must be within [0, 16]: 17"),
		},
		{
			name: "gc return age of zero",
			argv: []string{"zam", "--gc-return-age", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc return age must be positive: 0"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := New(tt.argv, tt.fileProvider)
			tt.errFunc(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
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
				InputPath:        base.NewPath("input", ""),
				OutputPath:       base.NewPath("out", ""),
				Verbose:          true,
				MaxThreads:       10,
				GCTriggerRatio:   0.75,
				InitialStackSize: 1 << 19,
				InitialHeapSize:  1 << 24,
			},
			want: `{"InputPath":{"Outer":"input"},"OutputPath":{"Outer":"out"},"Verbose":true,"MaxThreads":10,"GCTriggerRatio":0.75,"InitialStackSize":524288,"InitialHeapSize":16777216,"NurserySize":0,"MaxHeapSize":0,"GCMarkers":0,"GCConcurrent":false,"GCRemarkGrowth":0,"GCPrecleanPasses":0,"GCReturnAge":0,"EmitImage":false,"KeepBuild":false,"Run":false,"RunArgs":null,"NoProject":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cmd.String())
		})
	}
}
