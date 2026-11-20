package codegen

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zamrtSource declares a class extending an abstract, each rooted by a static.
const zamrtSource = `namespace Main {
    abstract Base { }

    class Box <: Base* {
        var str name;
        var int count;
    }

    static var Box* box;
    static var Base* base;

    fun void(uint, str...) main = (argC, argV) {
        return;
    }
}
`

func TestZamrtFrames(t *testing.T) {
	t.Parallel()
	got, _ := runVMHarness(t, "zamrt_harness.c", "zamrt", "frames", gcImage(t, zamrtSource))
	assert.Equal(t, map[string]string{
		"empty": "1", "innermost": "1", "caller": "1", "outerCaller": "1",
		"zeroed": "1", "pastSlots": "1", "left": "1", "leftAll": "1",
	}, got)
}

func TestZamrtAllocatesAndRecordsWrites(t *testing.T) {
	t.Parallel()
	got, _ := runVMHarness(t, "zamrt_harness.c", "zamrt", "alloc", gcImage(t, zamrtSource))
	assert.Equal(t, map[string]string{
		"sized": "1", "none": "1", "shortID": "1", "elementCount": "3",
		"cleanBefore": "1", "dirtyAfter": "1", "stringLength": "8", "stringBytes": "1",
	}, got)
}

func TestZamrtSubtypes(t *testing.T) {
	t.Parallel()
	got, _ := runVMHarness(t, "zamrt_harness.c", "zamrt", "types", gcImage(t, zamrtSource))
	assert.Equal(t, map[string]string{"reflexive": "1", "oneWay": "1"}, got)
}

func TestZamrtPollCollectsWhenAsked(t *testing.T) {
	t.Parallel()
	got, _ := runVMHarness(t, "zamrt_harness.c", "zamrt", "poll", gcImage(t, zamrtSource))
	assert.Equal(t, map[string]string{"idle": "1", "collected": "1"}, got)
}

func TestZamrtPanicAndExit(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "zamrt_harness.c", "zamrt", wholeRuntime...)
	image := gcImage(t, zamrtSource)

	out, err := exec.Command(harness, "panic", image).CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), string(out))
	assert.Equal(t, 67, exit.ExitCode())
	assert.Contains(t, string(out), "divide by zero")

	err = exec.Command(harness, "exit", image).Run()
	require.True(t, errors.As(err, &exit))
	// POSIX keeps the low byte of the status, and Windows all of it.
	assert.Equal(t, 300&0xff, exit.ExitCode()&0xff)
}

// TestZamrtValues asks equality, order, hashing and the string form of
// primitives, strings and boxes whose class overrides each, the harness
// standing in for the compiled overrides with its own.
func TestZamrtValues(t *testing.T) {
	t.Parallel()
	image := gcImage(t, `import std("lang/Obj.zam");

namespace Main {
    class Box {
        var int n;

        public fun bool(Obj*) Eq = (o) {
            return this.Hash() == o.Hash();
        }

        public fun int(Obj*) Cmp = (o) {
            return this.Hash() - o.Hash();
        }

        public fun int() Hash = () {
            return this.n % 10;
        }

        public fun str() Str = () {
            return "box" + this.n{str};
        }
    }

    static var Box* box;

    fun void(uint, str...) main = (argC, argV) {
        return;
    }
}
`)
	got, _ := runVMHarness(t, "zamrt_harness.c", "zamrt", "values", image)
	assert.Equal(t, map[string]string{
		"int.eq": "1", "int.ne": "0", "int.cmp": "9", "int.hash": "1", "int.str": "-42",
		"str.eq": "1", "str.cmp": "9", "str.hash": "1", "str.str": "1",
		"box.eq": "1", "box.ne": "0", "box.nullEq": "1", "box.nullNe": "0",
		"box.cmp": "8", "box.hash": "3", "box.str": "box13", "box.nullStr": "<null>",
		"returned": "1",
	}, got)
}

// TestZamrtDispatch finds a box's type, and the methods of the abstract it
// extends and the interface it implements among its own.
func TestZamrtDispatch(t *testing.T) {
	t.Parallel()
	image := gcImage(t, `namespace Main {
    interface Shape {
        fun int() Area;
    }

    abstract Base {
        virtual fun int() Area;
    }

    class Box <: Base* {
        fun int() Area = () {
            return 4;
        }
    }

    static var Box* box;
    static var Base* base;
    static var Shape* shape;

    fun void(uint, str...) main = (argC, argV) {
        return;
    }
}
`)
	got, _ := runVMHarness(t, "zamrt_harness.c", "zamrt", "dispatch", image)
	assert.Equal(t, map[string]string{
		"typeOf": "1", "overrides": "1", "interface": "1",
	}, got)
}

// TestZamrtAny boxes an int and a box, reads back what each holds and unboxes
// it, and is stopped unboxing the int as a uint.
func TestZamrtAny(t *testing.T) {
	t.Parallel()
	harness := buildHarness(t, "zamrt_harness.c", "zamrt", wholeRuntime...)
	cmd := exec.Command(harness, "anys", gcImage(t, zamrtSource))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), string(out))
	assert.Equal(t, 67, exit.ExitCode())
	assert.Contains(t, stderr.String(), "any holds a value of a type the unbox did not ask for")
	assert.Equal(t, "int.type=1\nint.value=7\nbox.type=1\nbox.value=1\n", string(out))
}
