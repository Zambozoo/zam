package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOptimizations runs one program at every optimization -O takes and holds
// each to the same output: arithmetic in a loop, recursion, a failed check
// recovered in a function that defers and reads what was stored before it, and
// a string built by many allocations.
func TestOptimizations(t *testing.T) {
	t.Parallel()
	source := recovering(`
    fun int(int) Fib = (n) {
        if (n < 2) {
            return n;
        }
        return Main::Fib(n - 1) + Main::Fib(n - 2);
    }

    fun int(int) Divide = (d) {
        return 100 / d;
    }

    fun void(uint, str...) main = (argC, argV) {
        var int sum = 0;
        for (var int i = 1; i <= 1000; i++) {
            sum = sum + i * i % 7;
        }
        Main::Out(sum{str} + " " + Main::Fib(20){str} + "\n");
        var int kept = 0;
        {
            defer Main::Out(kept{str} + " " + System::Recover() + "\n");
            kept = Main::Divide(4);
            kept = Main::Divide(0);
        }
        var str joined = "";
        for (var int i = 0; i < 200; i++) {
            joined = joined + "x";
        }
        Main::Out(Strs::CharLen(joined){str} + "\n");
    }`)

	for _, level := range []string{"0", "1", "2", "3", "s"} {
		t.Run("O"+level, func(t *testing.T) {
			t.Parallel()
			binary := compileWith(t, []string{"-O" + level}, source)
			stdout, stderr, status := run(t, binary)
			assert.Equal(t, 0, status, stderr)
			assert.Equal(t, "2002 6765\n25 divide by zero\n200\n", stdout)
		})
	}
}
