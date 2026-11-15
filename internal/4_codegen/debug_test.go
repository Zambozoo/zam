package codegen

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
)

// debugSource lays its statements a line each, so that each line of the
// tables below names one statement.
const debugSource = `namespace Main {
    fun int(int) Twice = (x) {
        var int y = x;
        return y + y;
    }

    fun void(uint, str...) main = (argc, argv) {
        var int a = Main::Twice(1);
        var int() f = lambda int(): () {
            return a;
        };
        {
            defer a = 3;
            if (a > 0) {
                return;
            }
        }
        a = f();
    }
}
`

// decodedDebug is the debug section of an image read back.
type decodedDebug struct {
	strings   []string
	functions []debugFunction
}

// decodeDebug reads the debug section back out of image, decoding each
// function's line program into its lines.
func decodeDebug(t *testing.T, image []byte) decodedDebug {
	t.Helper()

	header := len((&counts{}).words()) * wordByteSize
	word := func(at int) uint64 { return binary.LittleEndian.Uint64(image[at:]) }
	at := header + int(word(header-wordByteSize))

	var d decodedDebug
	count := int(word(at))
	at += wordByteSize
	for range count {
		length := int(word(at))
		d.strings = append(d.strings, string(image[at+wordByteSize:at+wordByteSize+length]))
		at += stringLiteral(make([]byte, length)).ByteCount()
	}

	count = int(word(at))
	at += wordByteSize
	var programs []uint64
	for range count {
		d.functions = append(d.functions, debugFunction{
			start: word(at), end: word(at + wordByteSize),
			parent: word(at + 2*wordByteSize), name: word(at + 3*wordByteSize),
		})
		programs = append(programs, word(at+4*wordByteSize))
		at += debugFunctionWords * wordByteSize
	}

	length := int(word(at))
	at += wordByteSize
	blob := image[at : at+length]
	for i := range d.functions {
		program := blob[programs[i]:]
		next := func() uint64 {
			v, n := binary.Uvarint(program)
			require.Positive(t, n)
			program = program[n:]
			return v
		}
		start, line, file := d.functions[i].start, int64(0), uint32(0)
		for range next() {
			step := next()
			delta, n := binary.Varint(program)
			require.Positive(t, n)
			program = program[n:]
			start, line = start+step>>1, line+delta
			if step&1 != 0 {
				file = uint32(next())
			}
			d.functions[i].lines = append(d.functions[i].lines, debugLine{start: start, file: file, line: uint32(line)})
		}
	}
	at += int(alignUp(uint64(length), wordByteSize))
	require.Equal(t, len(image), at, "the debug section runs to the end of the image")
	return d
}

// function answers the function called name.
func (d decodedDebug) function(t *testing.T, name string) (int, debugFunction) {
	t.Helper()
	i := slices.IndexFunc(d.functions, func(f debugFunction) bool { return d.strings[f.name] == name })
	require.GreaterOrEqual(t, i, 0, "no function %s in %v", name, d.strings)
	return i, d.functions[i]
}

// lineAt answers the line the instruction at offset was emitted for, read the
// way the runtime reads it: from the innermost function holding it.
func (d decodedDebug) lineAt(offset uint64) uint32 {
	var holder *debugFunction
	for i := range d.functions {
		if f := &d.functions[i]; f.start <= offset && offset < f.end {
			holder = f
		}
	}
	var line uint32
	if holder != nil {
		for _, l := range holder.lines {
			if l.start > offset {
				break
			}
			line = l.line
		}
	}
	return line
}

func TestDebugSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"debug\"\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(debugSource), 0o600))

	cmd := &command.Command{InputPath: base.Path(entry), MaxThreads: 1}
	ast, errs := syntax.NewAST(cmd, &base.FileProvider{}, nil)
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	result, err := GenerateCode(cmd, ast)
	require.NoError(t, err)
	d := decodeDebug(t, result.Image)

	assert.True(t, slices.IsSortedFunc(d.functions, func(a, b debugFunction) int { return int(a.start) - int(b.start) }))
	for _, f := range d.functions {
		assert.True(t, slices.IsSortedFunc(f.lines, func(a, b debugLine) int { return int(a.start) - int(b.start) }))
	}

	_, static := d.function(t, "<static init>")
	assert.Equal(t, uint64(0), static.start, "the static initializers are where the program begins")
	_, twice := d.function(t, "Main.Twice")
	assert.NotEqual(t, noParent, twice.parent, "a static method's body lies inside the static initializers")

	mainIndex, main := d.function(t, "Main.main")
	assert.Equal(t, noParent, main.parent)
	_, lambda := d.function(t, "Main.main.lambda")
	assert.Equal(t, uint64(mainIndex), lambda.parent, "a lambda's body lies inside the body it was written in")
	assert.True(t, main.start < lambda.start && lambda.end <= main.end)

	assert.Equal(t, uint32(2), d.lineAt(twice.start))
	assert.Equal(t, uint32(4), d.lineAt(twice.end-1), "a body ends on its last statement")
	assert.Equal(t, uint32(7), d.lineAt(main.start), "a body opens on the line it is declared on")
	assert.Equal(t, uint32(10), d.lineAt(lambda.end-1))
	assert.Equal(t, uint32(9), d.lineAt(lambda.end),
		"what follows a lambda is the rest of the statement it was written in")

	var deferred int
	for _, l := range main.lines {
		if l.line == 13 {
			deferred++
		}
	}
	assert.Equal(t, 2, deferred, "a deferred statement is emitted at each way out of its block")
	assert.Contains(t, d.strings, entry, "a file is named by its path")

	table := result.Debug
	assert.Equal(t, main.start, table.Main)
	file, line, ok := table.Locate(lambda.end)
	assert.True(t, ok)
	assert.Equal(t, entry, file)
	assert.Equal(t, 9, line)
	name, ok := table.Function(lambda.start)
	assert.True(t, ok)
	assert.Equal(t, "Main.main.lambda", name)

	offsets, line := table.Breakpoints(entry, 6)
	assert.Equal(t, 7, line, "a line with no code moves to the next that has some")
	assert.Equal(t, []uint64{main.start}, offsets)
	offsets, line = table.Breakpoints(entry, 9)
	assert.Equal(t, 9, line)
	assert.Contains(t, offsets, lambda.start, "a lambda's body begins on the line it is written on")
	assert.NotContains(t, offsets, lambda.end, "the rest of a line after a lambda is not where the line begins")
	offsets, _ = table.Breakpoints(entry, 13)
	assert.Len(t, offsets, 2, "a deferred statement begins at each way out of its block")
	offsets, _ = table.Breakpoints("nowhere.zam", 1)
	assert.Empty(t, offsets)

	names := func(offset uint64) []string {
		var names []string
		for _, l := range table.Locals(offset, 0) {
			names = append(names, l.Name+" "+l.Type.Name)
		}
		return names
	}
	var returning uint64
	for _, l := range twice.lines {
		if l.line == 4 {
			returning = l.start
		}
	}
	assert.Equal(t, []string{"x int"}, names(twice.start), "a variable holds nothing until its value is emitted")
	assert.Equal(t, []string{"x int", "y int"}, names(returning))
	assert.Contains(t, names(lambda.start), "a int", "a lambda's captures are its own")
	assert.NotContains(t, names(lambda.start), "f int()", "the body a lambda is written in is not its own")
	assert.Contains(t, names(lambda.end), "a int")
}

// genericSource has a generic class whose method's variables lie where and
// are of what its instantiation decides.
const genericSource = `namespace Main {
    class Box[A] {
        var A v;

        fun A() Get = () {
            var (A, int) p = this.v, 2;
            var int after = 3;
            return p.0;
        }
    }

    fun void(uint, str...) main = (argc, argv) {
        var Box[str]* b = new(Box[str]);
        b.Get();
    }
}
`

func TestDebugTableGroundsGenericVariables(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"debug\"\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(genericSource), 0o600))

	cmd := &command.Command{InputPath: base.Path(entry), MaxThreads: 1}
	ast, errs := syntax.NewAST(cmd, &base.FileProvider{}, nil)
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	result, err := GenerateCode(cmd, ast)
	require.NoError(t, err)
	table := result.Debug

	offsets, _ := table.Breakpoints(entry, 8)
	require.NotEmpty(t, offsets)
	template, ok := table.Template(offsets[0])
	require.True(t, ok, "a generic class's method is shared by its instantiations")

	var instance uint32
	for short, concrete := range table.instances {
		if short>>16 == uint32(template) && strings.Contains(concrete.DeclType().String(), "[str]") {
			instance = short
		}
	}
	require.NotZero(t, instance)

	describe := func(locals []Local) map[string]string {
		described := map[string]string{}
		for _, l := range locals {
			described[l.Name] = fmt.Sprintf("%d %s %v", l.Slot, l.Type.Name, l.Open)
		}
		return described
	}
	open := describe(table.Locals(offsets[0], 0))
	assert.Equal(t, "1 (A, int) true", open["p"])
	assert.Equal(t, "1 int true", open["after"], "a slot past one an instantiation sizes is placed by it")

	grounded := describe(table.Locals(offsets[0], instance))
	assert.Equal(t, "1 (str, int) false", grounded["p"])
	assert.Equal(t, "3 int false", grounded["after"])
	assert.Contains(t, grounded["this"], "[str]")
}

// staticSource has static members of a namespace and of a class, beside a
// static method.
const staticSource = `namespace Main {
    static var int counter = 3;

    class Box {
        static var str title = "b";
    }

    fun int() Get = () {
        return Main::counter;
    }

    fun void(uint, str...) main = (argc, argv) {
        Main::counter = Main::Get();
    }
}
`

func TestDebugTableListsStatics(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"debug\"\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(staticSource), 0o600))

	cmd := &command.Command{InputPath: base.Path(entry), MaxThreads: 1}
	ast, errs := syntax.NewAST(cmd, &base.FileProvider{}, nil)
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	result, err := GenerateCode(cmd, ast)
	require.NoError(t, err)

	statics := map[string]Static{}
	for _, s := range result.Debug.Statics() {
		statics[s.Decl+"."+s.Name] = s
	}
	require.Contains(t, statics, "Main.counter")
	require.Contains(t, statics, "Main.Box.title")
	assert.Equal(t, "int", statics["Main.counter"].Type.Name)
	assert.Equal(t, "str", statics["Main.Box.title"].Type.Name)
	assert.NotEqual(t, statics["Main.counter"].Offset, statics["Main.Box.title"].Offset)
	assert.False(t, statics["Main.counter"].Std)
	assert.NotContains(t, statics, "Main.Get", "a static method is not a variable")
	assert.NotContains(t, statics, "Main.main")
}
