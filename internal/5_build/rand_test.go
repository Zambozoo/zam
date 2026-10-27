package build_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRandStaysInRange draws from every Rand method many times and reports
// whether each draw landed in its range and each small range was covered.
func TestRandStaysInRange(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("math/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var bool inRange = true;
        var bool[7] ints;
        var bool[3] bytes;
        var bool[2] bools;
        var uint distinct = 0U;
        var uint last = 0U;
        for(var int i = 0; i < 2000; i++) {
            var int n = type(Rand).IntRange(-3, 3);
            inRange = inRange && n >= -3 && n <= 3;
            ints[(n + 3){uint}] = true;

            var byte b = type(Rand).ByteRange(3{byte}, 5{byte});
            inRange = inRange && b >= 3{byte} && b <= 5{byte};
            bytes[(b - 3{byte}){uint}] = true;

            var uint c = type(Rand).CharRange(55290U{char}, 57350U{char}){uint};
            inRange = inRange && (c <= 55295U || c >= 57344U) && c >= 55290U && c <= 57350U;
            var uint ch = type(Rand).Char(){uint};
            inRange = inRange && ch <= 1114111U && (ch < 55296U || ch > 57343U);

            var flt f = type(Rand).Flt();
            inRange = inRange && f >= 0.0 && f < 1.0;
            var flt g = type(Rand).FltRange(-2.0, 2.0);
            inRange = inRange && g >= -2.0 && g <= 2.0;

            var uint u = type(Rand).UintRange(10U, 12U);
            inRange = inRange && u >= 10U && u <= 12U;
            type(Rand).UintRange(0U, (-1){uint});
            type(Rand).IntRange(-9223372036854775807 - 1, 9223372036854775807);
            type(Rand).Byte();
            type(Rand).Int();

            if(type(Rand).Bool()) {
                bools[1] = true;
            } else {
                bools[0] = true;
            }

            var uint r = type(Rand).Uint();
            if(r != last) {
                distinct++;
            }
            last = r;
        }
        var bool covered = bools[0] && bools[1] && bytes[0] && bytes[1] && bytes[2];
        for(var uint i = 0U; i < 7U; i++) {
            covered = covered && ints[i];
        }
        type(File).StdOut.WriteStr(inRange{str} + " " + covered{str} + " " + (distinct > 1990U){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "true true true\n", stdout)
}
