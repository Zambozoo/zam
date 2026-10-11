package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A pointer or an array stepped by a uint is as valid as one stepped by an int:
// `arr[i]` desugars to `*(arr + i)`, and both offsets name a non-negative
// element position. Rejecting one would force every uint index — a length, a
// capacity, an index returned by a method of its own — to be cast through int
// just to reach the element it already named.
func TestValidateASTAcceptsUintIndexingAPointer(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun int(int*, uint) at = (p, i) {
        return p[i];
    }
}
`, nil)

	assert.Empty(t, errs)
}

func TestValidateASTAcceptsUintIndexingAnArray(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun int(uint) first = (i) {
        var int[8] run;
        return run[i];
    }
}
`, nil)

	assert.Empty(t, errs)
}

// `new(T, count)` and `new(T~tail)` accept a uint count the same way they
// accept an int one. A count is naturally non-negative, so forcing it through
// int would mean casting every uint length — a `Len()` return, a cached
// capacity — before an allocation.
func TestValidateASTAcceptsUintCountForNew(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun int*(uint) alloc = (n) {
        return new(int, n);
    }
}
`, nil)

	assert.Empty(t, errs)
}

// An integer literal with a trailing `u` or `U` is a uint. The suffix is
// resolved in the lexer, so by the time semantics sees the literal its type is
// already uint — no cast is needed to assign one to a uint slot, and the usual
// int/uint rules decide the rest.
func TestValidateASTAcceptsUintLiteralSuffix(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun uint() zero = () {
        return 0u;
    }
    static fun uint() answer = () {
        return 42U;
    }
}
`, nil)

	assert.Empty(t, errs)
}
