package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validateNamespace validates a namespace holding Ns, a generic Box, and the
// given declarations.
func validateNamespace(t *testing.T, decls string) []error {
	t.Helper()
	return validate(t, `
namespace Main {
`+mainMethod+`
    namespace Ns {
        public static fun int() One = () { return 1; }
    }
    class Box[A] { var A a; }
`+decls+`
}
`, nil)
}

func TestValidateASTAcceptsNamespaceStaticAccess(t *testing.T) {
	t.Parallel()

	for name, decls := range map[string]string{
		"direct": `
    static fun int() f = () { return Ns::One(); }`,
		"through an alias": `
    alias N = Ns;
    static fun int() f = () { return N::One(); }`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, validateNamespace(t, decls))
		})
	}
}

func TestValidateASTRejectsNamespaceAsType(t *testing.T) {
	t.Parallel()

	for name, decls := range map[string]string{
		"static":            `static var Ns n;`,
		"pointer":           `static var Ns* p;`,
		"array":             `static var Ns[2] a;`,
		"parameter":         `static fun void(Ns*) f = (n) { return; }`,
		"return":            `static fun Ns*() f = () { return null; }`,
		"local":             `static fun void() f = () { var Ns* n = null; return; }`,
		"generic argument":  `static var Box[Ns*]* b;`,
		"tuple element":     `static var (Ns*, int) t;`,
		"alias use":         "alias N = Ns;\n    static var N* n;",
		"alias of pointer":  `alias P = Ns*;`,
		"new":               `static fun void() f = () { var any a = new(Ns); return; }`,
		"type without name": `static fun void() f = () { var bool b = type(Ns) <: type(any); return; }`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			errs := validateNamespace(t, decls)
			require.NotEmpty(t, errs)
			assert.Contains(t, errs[0].Error(), "namespace Ns is not a type")
		})
	}
}
