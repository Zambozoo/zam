package syntax

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
)

// TestObjectMethodSlots holds the slot constants to the one thing that actually
// decides them: the order std.Object declares its methods in. A method's slot is
// its position in a flattened table and nothing else, so reordering the
// declarations silently renumbers every object's table, and the runtime calls
// Eq by the number rather than by the name. This is what fails instead.
//
// objectMethods is checked against the same list because an interface is given
// Object's methods from there rather than from the file.
func TestObjectMethodSlots(t *testing.T) {
	t.Parallel()
	expected := base.ObjectMethodNames[:]

	t.Run("the declaration order is the slot order", func(t *testing.T) {
		t.Parallel()
		root, err := base.ParsePath("object")
		require.NoError(t, err)
		fsys := fstest.MapFS{
			"zam.yml": {Data: []byte("name: object\nversion: 0.0.0\n")},
			entryName: {Data: []byte("import std(\"lang/Obj.zam\");\n\npublic namespace Main {\n}\n")},
		}
		ast, errs := parse(t, &cli.Command{}, root, ".", newFiles(t, root, fsys, nil), newPackages(t, root))
		require.Empty(t, errs)
		object, at := ast.CodeFiles.Get(base.StdPath("lang/Obj" + base.CodeFileExtension))
		require.GreaterOrEqual(t, at, 0)

		var declared []string
		for name := range object.Decl.InstanceMethods() {
			declared = append(declared, name)
		}
		assert.Equal(t, expected, declared)
	})

	t.Run("the interface's own copy agrees", func(t *testing.T) {
		t.Parallel()
		var declared []string
		for _, method := range objectMethods(&TypeWrapped{kind: TypeKindPointer}) {
			declared = append(declared, method.name)
		}
		assert.Equal(t, expected, declared)
	})
}
