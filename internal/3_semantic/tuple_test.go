package semantic

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validateBody(t *testing.T, body string) []error {
	t.Helper()
	return validate(t, `
namespace Main {
`+mainMethod+`
    class Base {}
    class Cls <: Base* {}

    static fun void() f = () {
`+body+`
    }
}
`, nil)
}

func TestValidateASTAcceptsTuples(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"pack":             `var (int, str) t = 1, "a";`,
		"unpack literal":   `var int a, str b = 1, "a";`,
		"unpack value":     `var (int, str) t = 1, "a"; var int a, str b = t;`,
		"access":           `var int a = (1, 2).0;`,
		"nested access":    `var ((int, str), int) t = (1, "a"), 2; var str s = t.0.1;`,
		"assign element":   `var (int, str) t; t.0 = 3;`,
		"null element":     `var (str, int) t = null, 1;`,
		"element widening": `var (Base*, any) t = new(Cls), 1;`,
		"unpack widening":  `var Base* o, any a = new(Cls), 1;`,
		"return":           `var (int, int)() g = lambda (int, int)(): () { return 0, 0; };`,
		"compare":          `var bool b = (1, "a") < (2, "b");`,
		"hash":             `var int h = #(1, 2);`,
		"string":           `var str s = (1, 2){str};`,
		"any":              `var any a = (1, 2); var (int, int) t = a{(int, int)};`,
		"pointer access":   `var (int, int) t; var (int, int)* p = &t; p.1 = 2;`,
		"subtype of any":   `var bool b = type((int, int)) <: type(any);`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, validateBody(t, body))
		})
	}
}

func TestValidateASTRejectsTupleMisuse(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body string
		want string
	}{
		"built tuple does not widen": {
			body: `var (Cls*, int) c = new(Cls), 1; var (Base*, int) o = c;`,
			want: "variable expression is not a subtype of (Base*, int)",
		},
		"element out of range": {
			body: `var (int, int) t; var int a = t.2;`,
			want: "tuple (int, int) has no element 2",
		},
		"unpack arity": {
			body: `var (int, int) t; var int a, int b, int c = t;`,
			want: "cannot unpack (int, int) into 3 variables",
		},
		"unpack element type": {
			body: `var int a, int b = 1, "x";`,
			want: "element 1 is not a subtype of int",
		},
		"pack element type": {
			body: `var (int, int) t = 1, "x";`,
			want: "variable expression is not a subtype of (int, int)",
		},
		"redeclared": {
			body: `var int a, int a = 1, 2;`,
			want: "variable a already declared",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			errs := validateBody(t, test.body)
			require.NotEmpty(t, errs)
			assert.Contains(t, fmt.Sprint(errs), test.want)
		})
	}
}
