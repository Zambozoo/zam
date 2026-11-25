package build_test

import (
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
)

const positPrecision = 2048

// positValue is the exact value of a posit other than NaR.
func positValue(bits uint64) *big.Float {
	value := new(big.Float).SetPrec(positPrecision)
	if bits == 0 {
		return value
	}
	negative := int64(bits) < 0
	if negative {
		bits = -bits
	}
	rest := bits << 1
	var run, k int
	if rest>>63 == 1 {
		for run = 0; run < 64 && rest&(1<<(63-run)) != 0; run++ {
		}
		k = run - 1
	} else {
		for run = 0; run < 64 && rest&(1<<(63-run)) == 0; run++ {
		}
		k = -run
	}
	if run+1 >= 64 {
		rest = 0
	} else {
		rest <<= run + 1
	}
	scale := 4*k + int(rest>>62)
	significand := uint64(1)<<63 | rest<<2>>1
	value.SetUint64(significand)
	value.SetMantExp(value, scale-63)
	if negative {
		value.Neg(value)
	}
	return value
}

// positArithmetic is what the four operators should answer, each rounded once
// from the exact result.
func positArithmetic(a, b uint64) [4]uint64 {
	if a == base.PositNaR || b == base.PositNaR {
		return [4]uint64{base.PositNaR, base.PositNaR, base.PositNaR, base.PositNaR}
	}
	x, y := positValue(a), positValue(b)
	result := func(f func(z, x, y *big.Float) *big.Float) uint64 {
		return base.PositFromFloat(f(new(big.Float).SetPrec(positPrecision), x, y))
	}
	quotient := base.PositNaR
	if b != 0 {
		quotient = result((*big.Float).Quo)
	}
	return [4]uint64{result((*big.Float).Add), result((*big.Float).Sub), result((*big.Float).Mul), quotient}
}

// positConversions is what each cast should answer: the posit as an int, a
// uint and a flt's bits, and the word as an int, a uint and a flt made posits.
func positConversions(word uint64) string {
	var asInt int64 = math.MinInt64
	var asUint uint64
	asFloat := uint64(0x7FF8000000000000)
	if word != base.PositNaR {
		value := positValue(word)
		asInt, _ = value.Int64()
		asUint, _ = value.Uint64()
		float, _ := value.Float64()
		asFloat = math.Float64bits(float)
	}
	fromFloat := base.PositNaR
	if f := math.Float64frombits(word); !math.IsNaN(f) && !math.IsInf(f, 0) {
		fromFloat = base.PositFromFloat(big.NewFloat(f))
	}
	return fmt.Sprintf("%d %d %d %d %d %d", asInt, asUint, asFloat,
		base.PositFromFloat(new(big.Float).SetInt64(int64(word))),
		base.PositFromFloat(new(big.Float).SetUint64(word)),
		fromFloat)
}

// TestPositArithmeticRoundsOnce checks every operator and cast the runtime
// computes against the exact result rounded once, over edge cases, random
// patterns, and pairs close enough to cancel.
func TestPositArithmeticRoundsOnce(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(1))
	special := []uint64{
		0, 1, 2, base.PositMaxPos, base.PositMaxPos - 1, base.PositNaR, base.PositNaR + 1,
		1 << 62, 0xC000000000000000, 1<<62 + 1, 1<<62 - 1, 0x4800000000000000,
		0x3800000000000000, 0x7F00000000000000, 0x00000000000000FF,
		math.Float64bits(1.5), math.Float64bits(-0.1), math.Float64bits(math.Inf(1)),
		math.Float64bits(5e-324), 1 << 63 >> 2, 42,
	}
	words := append([]uint64{}, special...)
	for range 60 {
		words = append(words, random.Uint64())
	}

	var pairs [][2]uint64
	for _, a := range special {
		for _, b := range special {
			pairs = append(pairs, [2]uint64{a, b})
		}
	}
	for range 200 {
		a := random.Uint64()
		pairs = append(pairs,
			[2]uint64{a, random.Uint64()},
			[2]uint64{a, a},
			[2]uint64{a, a + uint64(random.Intn(5))},
			[2]uint64{a, -a + uint64(random.Intn(5))})
	}

	var calls strings.Builder
	for _, pair := range pairs {
		fmt.Fprintf(&calls, "        Main::show(%dU, %dU);\n", pair[0], pair[1])
	}
	for _, word := range words {
		fmt.Fprintf(&calls, "        Main::convert(%dU);\n", word)
	}

	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    static fun void(uint, uint) show = (a, b) {
        var pst x = Psts::FromBits(a);
        var pst y = Psts::FromBits(b);
        File::StdOut.WriteStr(Psts::ToBits(x + y){str} + " " + Psts::ToBits(x - y){str} + " " +
            Psts::ToBits(x * y){str} + " " + Psts::ToBits(x / y){str} + " " +
            (x < y){str} + " " + (x == y){str} + "\n");
    }

    static fun void(uint) convert = (a) {
        var pst x = Psts::FromBits(a);
        File::StdOut.WriteStr(x{int}{str} + " " + x{uint}{str} + " " + Flts::ToBits(x{flt}){str} + " " +
            Psts::ToBits(a{int}{pst}){str} + " " + Psts::ToBits(a{pst}){str} + " " +
            Psts::ToBits(Flts::FromBits(a){pst}){str} + "\n");
    }

    fun void(uint, str...) main = (argC, argV) {
`+calls.String()+`    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	require.Len(t, lines, len(pairs)+len(words))
	for index, pair := range pairs {
		results := positArithmetic(pair[0], pair[1])
		want := fmt.Sprintf("%d %d %d %d %t %t", results[0], results[1], results[2], results[3],
			int64(pair[0]) < int64(pair[1]), pair[0] == pair[1])
		assert.Equal(t, want, lines[index], "%#x, %#x", pair[0], pair[1])
	}
	for index, word := range words {
		assert.Equal(t, positConversions(word), lines[len(pairs)+index], "%#x", word)
	}
}

func TestPositsInPrograms(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import std("math/Hash.zam");
import std("math/Rand.zam");
import std("codec/format/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        var pst a = 1.5P;
        var pst b = a;
        b++;
        File::StdOut.WriteStr(a{str} + " " + (a + 2P){str} + " " + (-a){str} + " " + b{str} + " " +
            Psts::NaR{str} + " " + (1P / 0P){str} + " " + (Psts::NaR < Psts::MinPos){str} + " " +
            (1P / 3P == 0.33333333333333333333333333333333333333P){str} + " " +
            (Psts::Max == 1e300P){str} + " " + (Hash::Pst(a) == Hash::Pst(1.5P)){str} + "\n");

        var bool inRange = true;
        for(var int i = 0; i < 1000; i++) {
            var pst r = Rand::Pst();
            inRange = inRange && r >= 0P && r < 1P;
        }
        File::StdOut.WriteStr(inRange{str} + " " + JSON::NewPst(0.25P).AsPst(){str} + " " +
            YAML::NewPst(-4P).AsPst(){str} + " " + CBOR::NewPst(8P).AsPst(){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	assert.Equal(t, "1.5 3.5 -1.5 2.5 NaR NaR true true true true\ntrue 0.25 -4 8\n", stdout)
}

func TestPositLiteralsMatchTheRuntime(t *testing.T) {
	t.Parallel()
	binary := compile(t, `import std("lang/*");
import std("io/local/*");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        File::StdOut.WriteStr(Psts::ToBits(10P * 0.1P){str} + " " + Psts::ToBits(0.1P){str} + "\n");
    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)

	ten, err := base.PositFromDecimal("10")
	require.NoError(t, err)
	tenth, err := base.PositFromDecimal("0.1")
	require.NoError(t, err)
	want := positArithmetic(ten, tenth)[2]
	assert.Equal(t, strconv.FormatUint(want, 10)+" "+strconv.FormatUint(tenth, 10)+"\n", stdout)
}

// TestPstsParseMatchesTheCompiler reads decimals with Psts::Parse as the
// compiler reads posit literals: plain values, ones past either end of the
// posits, the exact midpoints between neighboring posits, where a tie rounds
// to even, and decimals just above them. Past what the compiler can read, it
// saturates.
func TestPstsParseMatchesTheCompiler(t *testing.T) {
	t.Parallel()
	literals := []string{
		"0", "0.0", "1", "-1", "0.1", "1.5", "-2.25", "3.141592653589793238462643383279502884197169399",
		"123456789012345678901234567890", "0.000000000000000000000000001",
		"1e75", "1e-75", "1e100", "-1e100", "1e-100", "-1e-100",
		"9.99e75", "1e76", "1e-76", "9e-77", "1e-77", "0e5", "-0.0", "1.", ".5",
	}
	rng := rand.New(rand.NewSource(1))
	for range 60 {
		bits := 1 + uint64(rng.Int63n(int64(base.PositMaxPos-1)))
		mid := new(big.Float).SetPrec(1024).Add(positValue(bits), positValue(bits+1))
		mid.Quo(mid, big.NewFloat(2))
		text := mid.Text('e', 320)
		above := strings.Replace(text, "e", "1e", 1)
		literals = append(literals, text, above, "-"+text)
		literals = append(literals, fmt.Sprintf("%d.%de%d", rng.Intn(10), rng.Int63(), rng.Intn(161)-80))
	}

	// Past where big.Float overflows or underflows, the compiler answers an
	// error or zero, and Psts::Parse saturates as it does short of there.
	extremes := map[string]uint64{
		"1e1000000000":           base.PositMaxPos,
		"-1e1000000000":          base.PositNaR + 1,
		"1e-1000000000":          base.PositMinPos,
		"1e99999999999999999999": base.PositMaxPos,
	}
	for literal := range extremes {
		literals = append(literals, literal)
	}

	binary := compile(t, `import std("lang/Err.zam");
import std("lang/Psts.zam");
import std("io/local/File.zam");
import m = std("math/Psts.zam");

namespace Main {
    fun void(uint, str...) main = (argC, argV) {
        for(var uint i = 0U; i < argC; i++) {
            var (pst, Err*) p = m.Psts::Parse(argV[i]);
            File::StdOut.WriteStr(Psts::ToBits(p.0){str} + "\n");
        }
    }
}
`)
	stdout, stderr, status := run(t, binary, literals...)
	require.Zero(t, status, stderr)

	var want strings.Builder
	for _, literal := range literals {
		bits, extreme := extremes[literal]
		if !extreme {
			var err error
			bits, err = base.PositFromDecimal(literal)
			require.NoError(t, err, literal)
		}
		fmt.Fprintf(&want, "%d\n", bits)
	}
	assert.Equal(t, want.String(), stdout)
}
