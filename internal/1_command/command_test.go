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
			argv: []string{"zam", "-help"},
			errFunc: func(tt assert.TestingT, err error, a ...any) bool {
				var ue *usageError
				return assert.ErrorAs(tt, err, &ue) && assert.NoError(tt, ue.err)
			},
		},
		{
			name:    "invalid flag",
			argv:    []string{"zam", "-invalid"},
			errFunc: errorContains("flag provided but not defined: -invalid"),
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
			argv: []string{"zam", "-threads", "0", "input"},
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
			},
			errFunc: assert.NoError,
		},
		{
			name: "sizes take a unit suffix",
			argv: []string{"zam", "-stack-size", "1MB", "-heap-size", "2GB", "-gc-ratio", "0.5", "input"},
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
			},
			errFunc: assert.NoError,
		},
		{
			name:    "unparsable size",
			argv:    []string{"zam", "-heap-size", "16TB", "input"},
			errFunc: errorContains(`invalid size "16TB"`),
		},
		{
			name:    "non positive size",
			argv:    []string{"zam", "-stack-size", "0KB", "input"},
			errFunc: errorContains(`size must be positive: "0KB"`),
		},
		{
			name: "gc ratio the heap can never reach",
			argv: []string{"zam", "-gc-ratio", "1.5", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc trigger ratio must be within (0, 1]: 1.5"),
		},
		{
			name: "gc ratio of zero",
			argv: []string{"zam", "-gc-ratio", "0", "input"},
			fileProvider: func() fileProvider {
				m := newMock_FileProvider(t)
				m.EXPECT().FileExists(base.NewPath("out", "")).Return(true, nil)
				return m
			}(),
			errFunc: errorContains("gc trigger ratio must be within (0, 1]: 0"),
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
			want: `{"InputPath":{"Outer":"input"},"OutputPath":{"Outer":"out"},"Verbose":true,"MaxThreads":10,"GCTriggerRatio":0.75,"InitialStackSize":524288,"InitialHeapSize":16777216,"EmitImage":false,"KeepBuild":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.cmd.String())
		})
	}
}
