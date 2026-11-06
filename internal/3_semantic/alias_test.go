package semantic

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validateAliases validates a namespace holding the given declarations beside
// a body that uses them.
func validateAliases(t *testing.T, decls, body string, others map[string]string) []error {
	t.Helper()
	var imports string
	for file := range others {
		imports += fmt.Sprintf("import %q;\n", file)
	}
	return validate(t, imports+`
namespace Main {
`+mainMethod+`
    class Base {}
    class Cls <: Base* {}
    class Box[A] { var A a; }
    class BaseBox[A <: Base*] { var A a; }
`+decls+`
    static fun void() f = () {
`+body+`
    }
}
`, others)
}

func TestValidateASTAcceptsAliases(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		decls  string
		body   string
		others map[string]string
	}{
		"plain": {
			decls: `alias Num = int;`,
			body:  `var Num n = 1; var int m = n;`,
		},
		"tuple": {
			decls: `alias Pair[A] = (A, bool);`,
			body:  `var Pair[int] p = 1, true; var int a = p.0; var (int, bool) q = p;`,
		},
		"function": {
			decls: `alias Visit[A] = void(A);`,
			body:  `var Visit[int] v = lambda void(int): (x) {}; v(1);`,
		},
		"pointer to alias": {
			decls: `alias B = Box[int];`,
			body:  `var B* b = new(Box[int]); var int a = b.a;`,
		},
		"alias of an alias": {
			decls: `alias Pair[A] = (A, bool); alias Pairs[A] = Pair[A][2];`,
			body:  `var Pairs[int] ps; var (int, bool) p = ps[0];`,
		},
		"bounded": {
			decls: `alias Boxed[A <: Base*] = Box[A];`,
			body:  `var Boxed[Cls*]* b = new(Box[Cls*]);`,
		},
		"bound satisfied by the alias's own bound": {
			decls: `alias Boxed[A <: Base*] = BaseBox[A];`,
			body:  `var Boxed[Cls*]* b = new(BaseBox[Cls*]);`,
		},
		"dotted": {
			decls: `class Outer { public alias Num = int; }`,
			body:  `var Outer.Num n = 1;`,
		},
		"imported": {
			others: map[string]string{"lib/Lib.zam": `
public class Lib {
    public alias Pair[A] = (A, int);
}
`},
			body: `var Lib.Pair[str] p = "a", 1;`,
		},
		"file of its own": {
			others: map[string]string{"lib/Pair.zam": `public alias Pair[A] = (A, int);`},
			body:   `var Pair[str] p = "a", 1;`,
		},
		"base type": {
			decls: `alias B = Base; class Sub <: B* {}`,
		},
		"in a signature": {
			decls: `alias Pair[A] = (A, bool); static fun Pair[int](int) g = (x) { return x, true; }`,
			body:  `var bool b = Main::g(1).1;`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, validateAliases(t, test.decls, test.body, test.others))
		})
	}
}

func TestValidateASTRejectsAliasMisuse(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		decls  string
		body   string
		others map[string]string
		want   string
	}{
		"undefined name": {
			decls: `alias Set[A] = Box[B];`,
			want:  `declaration "B" not found`,
		},
		"unused parameter": {
			decls: `alias Set[A] = Box[int];`,
			want:  "alias Set does not use its parameter A",
		},
		"enclosing parameter": {
			decls: `class Outer[T] { alias Inner = Box[T]; }`,
			want:  `declaration "T" not found`,
		},
		"cycle": {
			decls: `alias X = Y*; alias Y = X*;`,
			want:  "refers to itself through",
		},
		"self": {
			decls: `alias X = Box[X];`,
			want:  "alias X refers to itself through X -> X",
		},
		"missing arguments": {
			decls: `alias Pair[A] = (A, bool);`,
			body:  `var Pair p;`,
			want:  "alias Pair takes 1 type arguments, but 0 were provided",
		},
		"extra arguments": {
			decls: `alias Num = int;`,
			body:  `var Num[int] n;`,
			want:  "alias Num takes 0 type arguments, but 1 were provided",
		},
		"tailed": {
			decls: `alias B = Base;`,
			body:  `var B~* b;`,
			want:  `alias "B" cannot be tailed`,
		},
		"bound unsatisfied at a use": {
			decls: `alias Boxed[A <: Base*] = Box[A];`,
			body:  `var Boxed[int]* b;`,
			want:  "alias Boxed: type argument int for A is not a subtype of Base*",
		},
		"target bound unsatisfied at the alias": {
			decls: `alias Boxed[A] = BaseBox[A];`,
			want:  "type BaseBox[A]: type argument A for A is not a subtype of Base*",
		},
		"private from another directory": {
			others: map[string]string{"lib/Lib.zam": `
public class Lib {
    private alias Num = int;
}
`},
			body: `var Lib.Num n;`,
			want: "alias Num is not accessible",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			errs := validateAliases(t, test.decls, test.body, test.others)
			require.NotEmpty(t, errs)
			assert.Contains(t, fmt.Sprint(errs), test.want)
		})
	}
}

func TestValidateASTEnforcesGenericBounds(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		decls string
		body  string
		want  string
	}{
		"satisfied": {
			body: `var BaseBox[Cls*]* b = new(BaseBox[Cls*]);`,
		},
		"satisfied by a parameter's bound": {
			decls: `class Wrap[A <: Base*] { var BaseBox[A]* b; }`,
		},
		"satisfied by a method argument": {
			decls: `static fun[A <: Base*] void(A) g = (a) {}`,
			body:  `Main::[Cls*]g(new(Cls));`,
		},
		"unsatisfied in a variable": {
			body: `var BaseBox[int]* b;`,
			want: "type BaseBox[int]: type argument int for A is not a subtype of Base*",
		},
		"unsatisfied by an unbounded parameter": {
			decls: `class Wrap[A] { var BaseBox[A]* b; }`,
			want:  "type argument A for A is not a subtype of Base*",
		},
		"unsatisfied in a base": {
			decls: `class Sub <: BaseBox[int]* {}`,
			want:  "type argument int for A is not a subtype of Base*",
		},
		"unsatisfied by a method argument": {
			decls: `static fun[A <: Base*] void(A) g = (a) {}`,
			body:  `Main::[int]g(1);`,
			want:  "method Main::g: type argument int for A is not a subtype of Base*",
		},
		"lower bound": {
			decls: `class Sink[A :> Cls*] { var A a; }`,
			body:  `var Sink[Base*] s; var Sink[int] t;`,
			want:  "type argument int for A is not a supertype of Cls*",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			errs := validateAliases(t, test.decls, test.body, nil)
			if test.want == "" {
				assert.Empty(t, errs)
				return
			}
			require.NotEmpty(t, errs)
			assert.Contains(t, fmt.Sprint(errs), test.want)
		})
	}
}
