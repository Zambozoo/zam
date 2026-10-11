package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Naming a generic static method without its type arguments is refused: the
// field type still carries open generic parameters, and a call compiled against
// those would try to work on parameters nothing has bound. The user has to
// write type(Decl)[A,...].Field rather than type(Decl).Field.
func TestValidateASTRejectsGenericStaticMethodWithoutTypeArgs(t *testing.T) {
	errs := validate(t, `
namespace Main {
    class Box[A] {
        static fun[A] Box[A]*(A) New = (v) {
            var Box[A]* b = new(Box[A]);
            return b;
        }
    }
`+mainMethod+`
    static fun void() build = () {
        type(Box).New(0);
    }
}
`, nil)

	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "generic")
}

// Naming the same method with its type arguments is accepted — the arguments
// bind the parameters and the field type becomes a concrete signature.
func TestValidateASTAcceptsGenericStaticMethodWithTypeArgs(t *testing.T) {
	errs := validate(t, `
namespace Main {
    class Box[A] {
        static fun[A] Box[A]*(A) New = (v) {
            var Box[A]* b = new(Box[A]);
            return b;
        }
    }
`+mainMethod+`
    static fun void() build = () {
        type(Box)[int].New(0);
    }
}
`, nil)

	assert.Empty(t, errs)
}
