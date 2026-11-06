package semantic

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateASTAcceptsSwitches(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"values":            `switch (1) { case (1, 2): {} case (3): {} } else {}`,
		"no else":           `switch (1) { case (1): {} }`,
		"empty":             `switch (1) {}`,
		"tuple values":      `var (int, int) p = 1, 2; switch (p) { case ((1, 1), (2, 2)): {} case ((1, 2)): {} }`,
		"fallthrough":       `switch (1) { case (1): continue; case (2): {} }`,
		"fallthrough else":  `switch (1) { case (1): continue; } else {}`,
		"break":             `switch (1) { case (1): { if (true) { break; } } }`,
		"labelled loop":     `label l: for (var int i = 0; i < 2; i++) { switch (i) { case (0): continue l; } else break l; }`,
		"labelled switch":   `label s: switch (1) { case (1): for (;;) { break s; } }`,
		"nested":            `switch (1) { case (1): switch (2) { case (2): continue; } else {} }`,
		"loop in a body":    `switch (1) { case (1): for (;;) { continue; } }`,
		"widened value":     `switch (new(Cls)) { case (new(Cls)): {} }`,
		"scoped body names": `switch (1) { case (1): var int a = 1; case (2): var int a = 2; }`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, validateBody(t, body))
		})
	}
}

func TestValidateASTRejectsSwitchMisuse(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body string
		want string
	}{
		"incomparable value": {
			body: `switch (1) { case ("a"): {} }`,
			want: "case value str is not comparable with the switch value int",
		},
		"fallthrough from the last case": {
			body: `switch (1) { case (1): continue; }`,
			want: "continue falls through the last body of a switch",
		},
		"fallthrough from else": {
			body: `switch (1) { case (1): {} } else continue;`,
			want: "continue falls through the last body of a switch",
		},
		"bare continue meant for a loop": {
			body: `for (;;) { switch (1) { case (1): continue; } }`,
			want: "continue falls through the last body of a switch",
		},
		"jump outside": {
			body: `continue;`,
			want: "continue is not in a loop or switch",
		},
		"unknown label": {
			body: `switch (1) { case (1): break l; }`,
			want: "break names label l, which is not a loop or switch in scope",
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

func TestValidateASTJudgesWhetherASwitchReturns(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body    string
		returns bool
	}{
		"every body returns":            {body: `switch (x) { case (1): return 1; } else return 2;`, returns: true},
		"falls through to a return":     {body: `switch (x) { case (1): continue; } else return 2;`, returns: true},
		"no else":                       {body: `switch (x) { case (1): return 1; }`},
		"a body runs on":                {body: `switch (x) { case (1): {} } else return 2;`},
		"a body breaks":                 {body: `switch (x) { case (1): break; } else return 2;`},
		"a nested loop's continue":      {body: `switch (x) { case (1): for (var int i = 0; i < x; i++) { continue; } } else return 2;`},
		"a nested switch's fallthrough": {body: `switch (x) { case (1): switch (x) { case (1): continue; } else return 1; } else return 2;`, returns: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun int(int) g = (x) {
        `+test.body+`
    }
}
`, nil)
			if test.returns {
				assert.Empty(t, errs)
				return
			}
			assert.Contains(t, fmt.Sprint(errs), "method Main.g can be left without returning a int")
		})
	}
}
