package lsp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// outlineOf checks a project and returns how its entry's file outlines the
// declaration with a name.
func outlineOf(t *testing.T, files map[string]string, entry string, name func(*syntax.File) *syntax.Token) string {
	t.Helper()

	paths := project(t, files)
	o := newOverlay()
	diagnostics, ast := analyze(o, nil, paths[entry], 1)
	require.Empty(t, diagnostics)

	file, _ := ast.CodeFiles.Get(*paths[entry])
	d, ok := declAt(file, name(file))
	require.True(t, ok)
	return outline(func(f *syntax.File) (*text, bool) {
		source, err := o.read(f.Path)
		return newText(source), err == nil
	}, d)
}

func TestOutlineListsWhatADeclarationDeclares(t *testing.T) {
	files := map[string]string{"Point.zam": "struct Point {\n" +
		"    // Origin is where both are zero.\n" +
		"    static var Point Origin;\n" +
		"    var int x = 1;\n" +
		"    var int y;\n" +
		"    fun int() Sum = () { return (*this).x + (*this).y; }\n" +
		"    enum Axis { X, Y; }\n" +
		"}\n"}

	assert.Equal(t, "struct Point {\n"+
		"    static var Point Origin\n"+
		"    var int x\n"+
		"    var int y\n"+
		"    fun int() Sum\n"+
		"    enum Axis\n"+
		"}", outlineOf(t, files, "Point.zam", func(f *syntax.File) *syntax.Token { return f.Decl.NameToken() }))

	assert.Equal(t, "enum Axis {\n    X\n    Y\n}", outlineOf(t, files, "Point.zam", func(f *syntax.File) *syntax.Token {
		axis, _ := f.Decl.GetDecl("Axis")
		return axis.NameToken()
	}))
}

func TestOutlineListsWhatAClassInherits(t *testing.T) {
	files := map[string]string{
		"Shape.zam": "abstract Shape {\n" +
			"    var int sides;\n" +
			"    fun int() Area = () { return 0; }\n" +
			"    fun str() Name = () { return \"shape\"; }\n" +
			"}\n",
		"Square.zam": "import \"Shape.zam\";\n" +
			"\n" +
			"class Square <: Shape* {\n" +
			"    fun int() Area = () { return 1; }\n" +
			"}\n",
	}

	assert.Equal(t, "class Square <: Shape* {\n"+
		"    fun int() Area\n"+
		"\n"+
		"    // from Shape\n"+
		"    var int sides\n"+
		"    fun str() Name\n"+
		"\n"+
		"    // from Obj\n"+
		"    fun bool(Obj*) Eq\n"+
		"    fun int(Obj*) Cmp\n"+
		"    fun int() Hash\n"+
		"    fun str() Str\n"+
		"    fun void() Mark\n"+
		"    fun void() Delete\n"+
		"}", outlineOf(t, files, "Square.zam", func(f *syntax.File) *syntax.Token { return f.Decl.NameToken() }))
}

func TestDeclaredLineLeavesOutALambdaBody(t *testing.T) {
	source := "    static fun str(byte*, int) FromBytes = (b, i) {\n" +
		"    var int n = x{int};\n"
	text := newText(source)
	name := func(value string, offset int) *syntax.Token {
		return &syntax.Token{Value: value, Offset: uint(offset)}
	}

	assert.Equal(t, "static fun str(byte*, int) FromBytes = (b, i)", text.declaredLine(name("FromBytes", 31)))
	assert.Equal(t, "var int n = x{int};", text.declaredLine(name("n", 64)))
}
