package semantic

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVisibility pins who may name what: a file's own declarations are always
// in reach, public and virtual members are in reach everywhere, protected ones
// from subtypes, and unmarked ones only from files in the same directory.
func TestVisibility(t *testing.T) {
	t.Parallel()
	lib := `
public class Lib {
    public static fun int() Open = () { return 1; }
    static fun int() hidden = () { return 2; }
    public var int shown;
    var int kept;
    public static fun Lib*() New = () { return new(Lib); }
}
`
	for name, test := range map[string]struct {
		dir   string
		files map[string]string
		body  string
		want  string
	}{
		"package members in the same directory": {
			files: map[string]string{"Pkg.zam": `
class Pkg {
    static fun int() hidden = () { return 2; }
    var int kept;
}
`},
			body: `var int a = Pkg::hidden() + new(Pkg).kept;`,
		},
		"package class from another directory": {
			files: map[string]string{"lib/Pkg.zam": `
class Pkg {
    public static fun int() Open = () { return 1; }
}
`},
			body: `var int a = Pkg::Open();`,
			want: "type Pkg is not accessible",
		},
		"public members from another directory": {
			files: map[string]string{"lib/Lib.zam": lib},
			body:  `var int a = Lib::Open() + Lib::New().shown;`,
		},
		"package static from another directory": {
			files: map[string]string{"lib/Lib.zam": lib},
			body:  `var int a = Lib::hidden();`,
			want:  "static field Lib::hidden is not accessible",
		},
		"package field from another directory": {
			files: map[string]string{"lib/Lib.zam": lib},
			body:  `var int a = Lib::New().kept;`,
			want:  "field Lib.kept is not accessible",
		},
		"virtual members from another directory": {
			files: map[string]string{
				"lib/Shape.zam": `
public interface Shape {
    virtual fun int() Sides;
}
`,
				"lib/Square.zam": `
import "Shape.zam";

public class Square {
    public fun int() Sides = () { return 4; }
    public static fun Shape*() New = () { return new(Square); }
}
`,
			},
			body: `var int a = Square::New().Sides();`,
		},
		"enum values from another directory": {
			files: map[string]string{"lib/Color.zam": `
public enum Color {
    Red,
    Blue;
}
`},
			body: `var Color c = Color::Red;`,
		},
		"private member from the same directory": {
			files: map[string]string{"Pkg.zam": `
class Pkg {
    private static fun int() secret = () { return 2; }
}
`},
			body: `var int a = Pkg::secret();`,
			want: "static field Pkg::secret is not accessible",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var imports string
			for file := range test.files {
				imports += fmt.Sprintf("import %q;\n", file)
			}
			errs := validate(t, imports+`
namespace Main {
`+mainMethod+`
    static fun void() use = () {
        `+test.body+`
    }
}
`, test.files)
			if test.want == "" {
				assert.Empty(t, errs)
				return
			}
			assert.Len(t, errs, 1)
			for _, err := range errs {
				assert.Contains(t, err.Error(), test.want)
			}
		})
	}
}

// TestVisibilityOfProtectedMembers pins that a protected member is in reach of
// a subtype in another directory, through an instantiation of a generic base,
// and of nothing else there.
func TestVisibilityOfProtectedMembers(t *testing.T) {
	t.Parallel()
	files := map[string]string{"lib/Base.zam": `
public class Base[A] {
    protected var A value;
}
`}
	errs := validate(t, `import "lib/Base.zam";

namespace Main {
`+mainMethod+`
    class Sub <: Base[int]* {
        fun int() Get = () {
            return this.value;
        }
    }

    static fun int(Base[int]*) peek = (b) {
        return b.value;
    }
}
`, files)
	assert.Len(t, errs, 1)
	for _, err := range errs {
		assert.Contains(t, err.Error(), "field Base.value is not accessible")
	}
}
