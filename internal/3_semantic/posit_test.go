package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateASTAcceptsPosits(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun pst(pst, int, flt) f = (p, i, x) {
        var pst q = p * 1.5P - 2P / p + -p;
        q++;
        var bool b = q < p && q != 0P && q{bool};
        return q + i{pst} + x{pst} + q{int}{pst} + q{flt}{pst} + q{uint}{pst};
    }
}
`, nil)

	assert.Empty(t, errs)
}

func TestValidateASTRejectsPositMisuse(t *testing.T) {
	for name, body := range map[string]string{
		"modulus":           `var pst p = 1P % 2P;`,
		"mixed with a flt":  `var pst p = 1P + 2.0;`,
		"mixed with an int": `var pst p = 1P + 2;`,
		"bitwise":           `var pst p = 1P & 2P;`,
		"implicitly a flt":  `var flt x = 1P;`,
	} {
		t.Run(name, func(t *testing.T) {
			errs := validate(t, `
namespace Main {
`+mainMethod+`
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
