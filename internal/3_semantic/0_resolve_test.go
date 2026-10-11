package semantic

import (
	"os"
	"path/filepath"
	"testing"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mainMethod is the entry point a project has to have, so that what a test
// writes around it is the only thing the test is asking about.
const mainMethod = `
    static fun void(uint, str...) main = (argc, argv) {
        return;
    }
`

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// validate parses a project whose entry file is entry, with others written
// beside it, and answers what validating the result found. Parsing itself is
// required to succeed: a test here is about resolution, so a file that did not
// even parse would be asking the wrong question.
func validate(t *testing.T, entry string, others map[string]string) []error {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, dir, "zam.yaml", "name: \"resolve\"\nversion: \"0.0.0\"\ndependencies:\n")
	for name, body := range others {
		writeFile(t, dir, name, body)
	}
	entryPath := writeFile(t, dir, "Main.zam", entry)

	ast, errs := syntax.NewAST(
		&command.Command{InputPath: base.NewPath(entryPath, ""), MaxThreads: 2},
		&base.FileProvider{}, nil)
	require.Empty(t, errs)

	return ValidateAST(ast)
}

// A nested declaration is reachable by a dotted path from the file that wrote
// it. Deciding at the parse that the head of any dotted name was a package made
// this unwritable: the head was looked up among the imports, which a declaration
// in the same file is not among, so a nested declaration could be named from
// every file except its own.
func TestValidateASTResolvesANestedPathWithinItsOwnFile(t *testing.T) {
	errs := validate(t, `
namespace Main {
    class Outer {
        public class Inner { }
    }
`+mainMethod+`
    static var Outer.Inner nested;
}
`, nil)

	assert.Empty(t, errs)
}

// A path may start with the name of a declaration it is written inside, that
// being how a declaration is named from outside itself looking in.
func TestValidateASTResolvesAPathHeadedByTheEnclosingDecl(t *testing.T) {
	errs := validate(t, `
namespace Main {
    class Hidden { }
`+mainMethod+`
    static var Main.Hidden hidden;
}
`, nil)

	assert.Empty(t, errs)
}

// Reaching into another file still names the import, the declaration that file
// holds, and then whatever is nested inside it.
func TestValidateASTResolvesAPathThroughAnImport(t *testing.T) {
	errs := validate(t, `
import sec = "Sec.zam";

namespace Main {
`+mainMethod+`
    static var sec.Sec.Inner nested;
}
`, map[string]string{"Sec.zam": `
namespace Sec {
    public class Inner { }
}
`})

	assert.Empty(t, errs)
}

// TestValidateASTAcceptsBlankVariable covers the easy case: a `var _` declares
// a slot the collector and the destructor can walk but no later expression can
// name. The expression is still typed, so a type mismatch would still fail
// here — it is only the name that is left out.
func TestValidateASTAcceptsBlankVariable(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int _ = 42;
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// Two `_` variables in one block compile without colliding: nothing resolves
// to them, so there is nothing to collide on. This is the point of the blank
// identifier — a result that must be popped off the stack still gets a slot
// and a destructor without the writer having to invent a fresh name for each.
func TestValidateASTAllowsRepeatedBlankVariables(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void() f = () {
        var int _ = 1;
        var int _ = 2;
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A method parameter written as `_` reserves its slot but goes unnamed in
// the body: the caller still pushes a value, the receiver still steps past
// it, and two `_` parameters do not collide because neither sits in the
// lookup scope.
func TestValidateASTAcceptsBlankMethodParameters(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun int(int, int) f = (_, _) {
        return 0;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A declaration in scope shadows an import of the same name, the search running
// innermost outward like the variable and label scopes do. Only the local Sec
// holds an Inner, so resolving at all is what says the local one was taken.
func TestValidateASTPrefersALocalDeclToASameNamedImport(t *testing.T) {
	errs := validate(t, `
import sec = "Sec.zam";

namespace Main {
    class sec {
        public class Sec {
            public class Inner { }
        }
    }
`+mainMethod+`
    static var sec.Sec.Inner nested;
}
`, map[string]string{"Sec.zam": `
namespace Sec {
    public class Unrelated { }
}
`})

	assert.Empty(t, errs)
}

// Binding the head settles what the rest of the path is looked up in. Here only
// the import could complete the path, and the local sec having answered to the
// head is what keeps it from being reached — otherwise a mistyped path would
// silently name something in another file.
func TestValidateASTDoesNotFallBackToAnImportOnceTheHeadIsBound(t *testing.T) {
	errs := validate(t, `
import sec = "Sec.zam";

namespace Main {
    class sec {
        public class Unrelated { }
    }
`+mainMethod+`
    static var sec.Sec.Inner nested;
}
`, map[string]string{"Sec.zam": `
namespace Sec {
    public class Inner { }
}
`})

	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), `declaration "sec.Sec.Inner" not found`)
}

// A trailing spread forwards a variadic payload whole rather than being written
// out one element at a time. The inner is already the pointer the callee reads
// — a parameter typed T... is bound as T* inside the body — so a call that only
// hands the pointer on is what the forwarding compiles down to.
func TestValidateASTAcceptsForwardingAVariadicWithASpread(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void(int, int...) take = (len, xs) {
        return;
    }
    static fun void(int, int...) forward = (len, xs) {
        take(len, xs...);
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A spread may name any pointer whose element matches the variadic's, not just
// another variadic binding. The forward is to a slot that holds a pointer
// either way, and the two have the same element type.
func TestValidateASTAcceptsSpreadOfABarePointer(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void(int, int...) take = (len, xs) {
        return;
    }
    static fun void(int*) f = (ptr) {
        take(0, ptr...);
        return;
    }
}
`, nil)

	assert.Empty(t, errs)
}

// A mismatched element type is rejected because the forward hands on the
// elements as they lie: there is no per-element conversion between a char and
// an int, so the callee would read the wrong width out of the slot.
func TestValidateASTRejectsSpreadWithWrongElementType(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void(int...) take = (xs) {
        return;
    }
    static fun void(char*) f = (p) {
        take(p...);
        return;
    }
}
`, nil)

	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "does not match the variadic element")
}

// A spread stands alone: mixing it with other variadic arguments would mean
// concatenating a run onto the one the pointer names, which the forward is
// not what writes.
func TestValidateASTRejectsSpreadMixedWithOtherVariadicArgs(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void(int...) take = (xs) {
        return;
    }
    static fun void(int*) f = (p) {
        take(1, p...);
        return;
    }
}
`, nil)

	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "must be the only variadic argument")
}

// A spread outside the trailing position would spread into a fixed slot, which
// takes one value rather than a run. The parse accepts the dots on any
// argument and the semantic pass is what pins them to the variadic.
func TestValidateASTRejectsSpreadInANonTrailingPosition(t *testing.T) {
	errs := validate(t, `
namespace Main {
`+mainMethod+`
    static fun void(int, int) take = (a, b) {
        return;
    }
    static fun void(int*) f = (p) {
        take(p..., 1);
        return;
    }
}
`, nil)

	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0].Error(), "spread may only appear as the last argument")
}
