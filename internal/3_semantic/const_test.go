package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A const slot is set once at its declaration and never reassigned. The
// initializer is the one place a non-const value may land in a const slot:
// every later write is caught by the regular rejection of writes to const.
func TestValidateASTAcceptsConstVariableWithInitializer(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int$ x = 42;
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A const slot without an initializer is pinned to the zero of its type: the
// slot is still const, but no value has to be written down for it.
func TestValidateASTAcceptsConstVariableWithoutInitializer(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int$ x;
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// Assigning to a const variable is the whole point of const: the slot promises
// not to change, and a later store would break the promise.
func TestValidateASTRejectsAssignmentToConstVariable(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int$ x = 1;
        x = 2;
        return;
    }
}
`, nil)

	assert.NotEmpty(t, errs)
}

// ++ and -- mutate the slot in place, which is what the const promise
// forbids. The operand is still numeric; the error is about const, not type.
func TestValidateASTRejectsIncrementOnConstVariable(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int$ x = 1;
        x++;
        return;
    }
}
`, nil)

	assert.NotEmpty(t, errs)
}

// A const member is a slot the holding declaration pins at its own init and
// never writes to again. The whole point of a const field is to be read many
// times without the compiler having to prove no one can write to it.
func TestValidateASTAcceptsConstMemberWithInitializer(t *testing.T) {
	errs := validate(t, `
namespace Main {
    class Box {
        public static var int$ limit = 10;
    }
`+mainMethod+`
}
`, nil)

	assert.Empty(t, errs)
}

// A const value flows into a non-const parameter: the callee works on its own
// copy and the slot the caller holds is unchanged whatever the callee does.
func TestValidateASTAcceptsConstArgumentForNonConstParameter(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void(int) take = (n) {
        return;
    }
    static fun void() f = () {
        var int$ x = 1;
        take(x);
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A non-const value flows into a const parameter the same way an initializer
// does: the parameter is the slot's declaration and the argument is the one
// value it is given.
func TestValidateASTAcceptsNonConstArgumentForConstParameter(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void(int$) take = (n) {
        return;
    }
    static fun void() f = () {
        take(5);
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// Const in operands is transparent to operators: a const int added to an int
// is an int, since the result is a fresh value no slot holds the promise for.
func TestValidateASTAcceptsConstInArithmetic(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int$ x = 1;
        var int y = x + 2;
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A cast strips const explicitly, and the language promises nothing of a
// program that then writes through the stripped type to a slot it was told
// not to.
func TestValidateASTAcceptsCastAwayConst(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int$ x = 1;
        var int y = x{int};
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}
