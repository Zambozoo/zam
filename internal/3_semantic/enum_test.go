package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A value of a primitive-backed enum is written with any expression of that
// primitive, constant or not, and reads back as it through a cast: from the
// value itself, or from the receiver once it is dereferenced.
func TestValidateASTAcceptsPrimitiveEnum(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun int(int) twice = (x) {
        return x * 2;
    }

    enum Mask : int {
        A(1),
        B(1 << 7),
        C(type(Main).twice(3));

        fun int() Bits = () {
            return (*this){int};
        }
    }

    enum Name : str {
        X("x"),
        Y("y" + "z");
    }

    static fun void() f = () {
        var int m = type(Mask).A{int} | type(Mask).B{int};
        var int c = type(Mask).C.Bits();
        var str s = type(Name).Y{str};
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A value is checked against the primitive its enum is backed by.
func TestValidateASTRejectsPrimitiveEnumValueOfAnotherType(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    enum Mask : int {
        A("one");
    }
}
`, nil)

	assert.NotEmpty(t, errs)
}

// A primitive-backed enum casts to whatever its primitive casts to among the
// primitives.
func TestValidateASTAcceptsPrimitiveEnumWideningCasts(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    enum Small : byte {
        A(1{byte});

        fun int() Number = () {
            return (*this){int};
        }
    }

    static fun void() f = () {
        var int i = type(Small).A{int};
        var flt x = type(Small).A{flt};
        var bool b = type(Small).A{bool};
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A primitive-backed enum casts to nothing its primitive does not, and no
// primitive casts back to it.
func TestValidateASTRejectsPrimitiveEnumCasts(t *testing.T) {
	for name, body := range map[string]string{
		"to what its primitive cannot be": `var int x = type(Name).A{int};`,
		"from its primitive":              `var Mask m = 1{Mask};`,
	} {
		t.Run(name, func(t *testing.T) {
			errs := validate(t, `
namespace Main {
`+mainMethod+`
    enum Mask : int {
        A(1);
    }

    enum Name : str {
        A("a");
    }

    static fun void() f = () {
        `+body+`
        return;
    }
}
`, nil)

			assert.NotEmpty(t, errs)
		})
	}
}
