package semantic

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// occurrence returns the offset of the nth whole-word occurrence of word in an
// ASCII source, counting from zero.
func occurrence(t *testing.T, source, word string, n int) uint {
	t.Helper()
	matches := regexp.MustCompile(`\b`+regexp.QuoteMeta(word)+`\b`).FindAllStringIndex(source, -1)
	require.Greater(t, len(matches), n, "%q occurs fewer than %d times", word, n+1)
	return uint(matches[n][0])
}

// TestValidateASTRecordsWhatNamesReferTo pins each kind of name semantic
// validation resolves to the declaration it resolves to: locals, parameters,
// instance and static fields named bare and through a selection, a static named
// through its type, and a type. A declaration's own name refers to itself.
func TestValidateASTRecordsWhatNamesReferTo(t *testing.T) {
	source := `namespace Main {
    static var int count = 1;
    class Box {
        var int size;
        fun int(int) grow = (by) {
            var int next = size + by;
            return next;
        }
    }
    static fun int(Box) measure = (b) {
        return b.size + type(Main).count + count;
    }
` + mainMethod + `}
`
	dir := t.TempDir()
	writeFile(t, dir, "zam.yml", "name: \"references\"\nversion: \"0.0.0\"\ndependencies:\n")
	entry := base.NewPath(writeFile(t, dir, "Main.zam", source), "")
	ast, errs := syntax.NewAST(&command.Command{InputPath: entry, MaxThreads: 1}, &base.FileProvider{}, nil)
	require.Empty(t, errs)
	require.Empty(t, ValidateAST(ast))
	file, _ := ast.CodeFiles.Get(*entry)

	for _, test := range []struct {
		name         string
		word         string
		use, declare int
		typ          syntax.Type
	}{
		{"a parameter", "by", 1, 0, syntax.TypeInt},
		{"an instance member named bare", "size", 1, 0, syntax.TypeInt},
		{"an instance member selected", "size", 2, 0, syntax.TypeInt},
		{"a local", "next", 1, 0, syntax.TypeInt},
		{"a type", "Box", 1, 0, nil},
		{"a static selected through its type", "count", 1, 0, syntax.TypeInt},
		{"a static named bare", "count", 2, 0, syntax.TypeInt},
		{"the type a static is selected through", "Main", 1, 0, nil},
		{"a declaration's own name", "grow", 0, 0, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref, ok := ast.References.At(file, occurrence(t, source, test.word, test.use))
			require.True(t, ok)
			assert.Same(t, file, ref.Target.File)
			assert.Equal(t, occurrence(t, source, test.word, test.declare), ref.Target.Name.Offset)
			if test.typ != nil {
				assert.True(t, test.typ.Equals(ref.Type), "%v", ref.Type)
			}
		})
	}

	declaration, _ := ast.References.At(file, occurrence(t, source, "size", 0))
	assert.Len(t, ast.References.To(declaration.Target), 3, "the declaration and both uses")
}
