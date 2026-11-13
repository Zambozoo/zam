package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateASTAcceptsDefers(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun int(int*) f = (p) {
        defer free(p);
        defer {
            var int i = 0;
            for(; i < 3; i++) {
                if(i == 1) {
                    break;
                }
            }
            var int() g = lambda int(): () {
                return 1;
            };
            defer g();
        }
        for(var int i = 0; i < 3; i++) {
            defer p[0] = i;
            if(i == 1) {
                continue;
            }
        }
        return p[0];
    }
}
`, nil)

	assert.Empty(t, errs)
}

func TestValidateASTRejectsDefersThatLeave(t *testing.T) {
	for name, body := range map[string]string{
		"return":            `defer return;`,
		"return in a block": `defer { return; }`,
		"break":             `for(;;) { defer break; }`,
		"continue":          `for(;;) { defer { continue; } }`,
		"labelled break":    `label outer: for(;;) { defer { for(;;) { break outer; } } }`,
		"declaration":       `defer var int x = 1;`,
		"defer":             `defer defer Main::f();`,
		"later variable":    `defer x = 2; var int x = 1;`,
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
