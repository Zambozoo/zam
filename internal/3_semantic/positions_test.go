package semantic

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestErrorsArePlacedOnWhatIsWrong pins where each kind of error is reported:
// on the expression or name at fault rather than on the statement or call it
// sits in.
func TestErrorsArePlacedOnWhatIsWrong(t *testing.T) {
	head := `
namespace Main {
    class Box {
        public var Box* next;
    }

    static fun int(int) one = (x) {
        return x;
    }

    static fun int(int...) many = (xs) {
        return 0;
    }

    static fun void(uint, str...) main = (argc, argv) {
        // a comment
        /* and a
           block */
`
	for name, test := range map[string]struct {
		body string
		want string
	}{
		"undefined variable":  {`        var int a = nope;`, "[19:21]"},
		"variable initializer": {`        var int a = "s";`, "[19:21]"},
		"missing field":        {"        var Box* b = null;\n        var Box* c = b.next.zz;", "[20:29]"},
		"argument":             {`        var int a = Main::one("s");`, "[19:31]"},
		"variadic argument":    {`        var int a = Main::many(1, "s");`, "[19:35]"},
		"missing static":       {`        var int a = Main::nope();`, "[19:27]"},
		"assigned value":       {"        var int a = 0;\n        a = \"s\";", "[20:13]"},
		"returned value":       {`        return "s";`, "[19:16]"},
	} {
		t.Run(name, func(t *testing.T) {
			errs := validate(t, head+test.body+"\n    }\n}\n", nil)
			require.NotEmpty(t, errs)
			assert.Contains(t, errs[0].Error(), test.want)
		})
	}
}
