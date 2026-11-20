package build_test

import (
	"fmt"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
)

// referencePrecision is how many bits the reference carries, far past what
// any function below loses on the inputs the test draws.
const referencePrecision = 1600

func newReference() *big.Float {
	return new(big.Float).SetPrec(referencePrecision)
}

func referenceInt(n int64) *big.Float {
	return newReference().SetInt64(n)
}

// referenceAtanhSeries sums t + t^3/3 + t^5/5 + … for |t| < 1/2.
func referenceAtanhSeries(t *big.Float) *big.Float {
	square := newReference().Mul(t, t)
	sum := newReference().Set(t)
	power := newReference().Set(t)
	for n := int64(3); ; n += 2 {
		power.Mul(power, square)
		term := newReference().Quo(power, referenceInt(n))
		if term.Sign() == 0 || term.MantExp(nil) < sum.MantExp(nil)-referencePrecision {
			return sum
		}
		sum.Add(sum, term)
	}
}

// referenceAtanInverse is atan(1/n) by its series.
func referenceAtanInverse(n int64) *big.Float {
	x := newReference().Quo(referenceInt(1), referenceInt(n))
	square := newReference().Mul(x, x)
	sum := newReference().Set(x)
	power := newReference().Set(x)
	for k := int64(3); ; k += 2 {
		power.Neg(power.Mul(power, square))
		term := newReference().Quo(power, referenceInt(k))
		if term.Sign() == 0 || term.MantExp(nil) < -referencePrecision {
			return sum
		}
		sum.Add(sum, term)
	}
}

var (
	referenceLn2 = func() *big.Float {
		third := newReference().Quo(referenceInt(1), referenceInt(3))
		return newReference().Mul(referenceInt(2), referenceAtanhSeries(third))
	}()
	referenceLn10 = func() *big.Float {
		ninth := newReference().Quo(referenceInt(1), referenceInt(9))
		quarterLog := newReference().Mul(referenceInt(2), referenceAtanhSeries(ninth))
		return newReference().Add(newReference().Mul(referenceInt(3), referenceLn2), quarterLog)
	}()
	referencePi = func() *big.Float {
		pi := newReference().Mul(referenceInt(16), referenceAtanInverse(5))
		return pi.Sub(pi, newReference().Mul(referenceInt(4), referenceAtanInverse(239)))
	}()
)

// referenceExp answers 2^±4000 past where any posit saturates.
func referenceExp(x *big.Float) *big.Float {
	switch {
	case greater(x, 2000):
		return newReference().SetMantExp(referenceInt(1), 4000)
	case less(x, -2000):
		return newReference().SetMantExp(referenceInt(1), -4000)
	}
	n, _ := newReference().Quo(x, referenceLn2).Int64()
	r := newReference().Sub(x, newReference().Mul(referenceInt(n), referenceLn2))
	r.SetMantExp(r, -40)
	sum := referenceInt(1)
	term := referenceInt(1)
	for k := int64(1); ; k++ {
		term.Quo(term.Mul(term, r), referenceInt(k))
		if term.Sign() == 0 || term.MantExp(nil) < -referencePrecision {
			break
		}
		sum.Add(sum, term)
	}
	for range 40 {
		sum.Mul(sum, sum)
	}
	return sum.SetMantExp(sum, int(n))
}

func referenceLog(x *big.Float) *big.Float {
	m := newReference()
	exponent := x.MantExp(m)
	t := newReference().Quo(newReference().Sub(m, referenceInt(1)), newReference().Add(m, referenceInt(1)))
	log := newReference().Mul(referenceInt(2), referenceAtanhSeries(t))
	return log.Add(log, newReference().Mul(referenceInt(int64(exponent)), referenceLn2))
}

// referenceSinCos reduces x by multiples of 2π and sums the series.
func referenceSinCos(x *big.Float, cosine bool) *big.Float {
	tau := newReference().Mul(referenceInt(2), referencePi)
	turns := newReference().Quo(x, tau)
	whole, _ := turns.Int(nil)
	r := newReference().Sub(x, newReference().Mul(newReference().SetInt(whole), tau))
	square := newReference().Mul(r, r)
	term := referenceInt(1)
	n := int64(1)
	if !cosine {
		term.Set(r)
		n = 2
	}
	sum := newReference().Set(term)
	for ; ; n += 2 {
		term.Neg(term.Quo(term.Mul(term, square), referenceInt(n*(n+1))))
		if term.Sign() == 0 || term.MantExp(nil) < -referencePrecision {
			return sum
		}
		sum.Add(sum, term)
	}
}

func referenceSqrt(x *big.Float) *big.Float {
	return newReference().Sqrt(x)
}

// referenceAtan halves the angle until the series converges quickly.
func referenceAtan(x *big.Float) *big.Float {
	x = newReference().Set(x)
	for range 8 {
		root := referenceSqrt(newReference().Add(referenceInt(1), newReference().Mul(x, x)))
		x.Quo(x, root.Add(root, referenceInt(1)))
	}
	square := newReference().Mul(x, x)
	sum := newReference().Set(x)
	power := newReference().Set(x)
	for n := int64(3); ; n += 2 {
		power.Neg(power.Mul(power, square))
		term := newReference().Quo(power, referenceInt(n))
		if term.Sign() == 0 || term.MantExp(nil) < sum.MantExp(nil)-referencePrecision {
			break
		}
		sum.Add(sum, term)
	}
	return sum.SetMantExp(sum, 8)
}

func less(x *big.Float, n int64) bool    { return x.Cmp(referenceInt(n)) < 0 }
func greater(x *big.Float, n int64) bool { return x.Cmp(referenceInt(n)) > 0 }
func absolute(x *big.Float) *big.Float   { return newReference().Abs(x) }

// positUnaries are the reference for each unary function, nil where the
// real result does not exist.
var positUnaries = []struct {
	name string
	f    func(x *big.Float) *big.Float
}{
	{"Sqrt", func(x *big.Float) *big.Float {
		if x.Sign() < 0 {
			return nil
		}
		return referenceSqrt(x)
	}},
	{"Cbrt", func(x *big.Float) *big.Float {
		if x.Sign() == 0 {
			return x
		}
		root := referenceExp(newReference().Quo(referenceLog(absolute(x)), referenceInt(3)))
		if x.Sign() < 0 {
			root.Neg(root)
		}
		return root
	}},
	{"Exp", referenceExp},
	{"Exp2", func(x *big.Float) *big.Float { return referenceExp(newReference().Mul(x, referenceLn2)) }},
	{"Expm1", func(x *big.Float) *big.Float { return newReference().Sub(referenceExp(x), referenceInt(1)) }},
	{"Log", func(x *big.Float) *big.Float {
		if x.Sign() <= 0 {
			return nil
		}
		return referenceLog(x)
	}},
	{"Log2", func(x *big.Float) *big.Float {
		if x.Sign() <= 0 {
			return nil
		}
		return newReference().Quo(referenceLog(x), referenceLn2)
	}},
	{"Log10", func(x *big.Float) *big.Float {
		if x.Sign() <= 0 {
			return nil
		}
		return newReference().Quo(referenceLog(x), referenceLn10)
	}},
	{"Log1p", func(x *big.Float) *big.Float {
		if !greater(x, -1) {
			return nil
		}
		return referenceLog(newReference().Add(x, referenceInt(1)))
	}},
	{"Floor", func(x *big.Float) *big.Float { return referenceRound(x, big.ToNegativeInf) }},
	{"Ceil", func(x *big.Float) *big.Float { return referenceRound(x, big.ToPositiveInf) }},
	{"Round", func(x *big.Float) *big.Float { return referenceRound(x, big.AwayFromZero) }},
	{"Trunc", func(x *big.Float) *big.Float { return referenceRound(x, big.ToZero) }},
	{"Sin", func(x *big.Float) *big.Float { return referenceSinCos(x, false) }},
	{"Cos", func(x *big.Float) *big.Float { return referenceSinCos(x, true) }},
	{"Tan", func(x *big.Float) *big.Float {
		return newReference().Quo(referenceSinCos(x, false), referenceSinCos(x, true))
	}},
	{"Asin", func(x *big.Float) *big.Float {
		if greater(absolute(x), 1) {
			return nil
		}
		if absolute(x).Cmp(referenceInt(1)) == 0 {
			half := newReference().Quo(referencePi, referenceInt(2))
			return half.Mul(half, referenceInt(int64(x.Sign())))
		}
		root := referenceSqrt(newReference().Sub(referenceInt(1), newReference().Mul(x, x)))
		return referenceAtan(newReference().Quo(x, root))
	}},
	{"Acos", func(x *big.Float) *big.Float {
		if greater(absolute(x), 1) {
			return nil
		}
		if x.Cmp(referenceInt(-1)) == 0 {
			return referencePi
		}
		ratio := newReference().Quo(newReference().Sub(referenceInt(1), x), newReference().Add(referenceInt(1), x))
		return newReference().Mul(referenceInt(2), referenceAtan(referenceSqrt(ratio)))
	}},
	{"Atan", referenceAtan},
	{"Sinh", func(x *big.Float) *big.Float {
		difference := newReference().Sub(referenceExp(x), referenceExp(newReference().Neg(x)))
		return difference.Quo(difference, referenceInt(2))
	}},
	{"Cosh", func(x *big.Float) *big.Float {
		sum := newReference().Add(referenceExp(x), referenceExp(newReference().Neg(x)))
		return sum.Quo(sum, referenceInt(2))
	}},
	{"Tanh", func(x *big.Float) *big.Float {
		e := referenceExp(newReference().Mul(x, referenceInt(2)))
		return newReference().Quo(newReference().Sub(e, referenceInt(1)), newReference().Add(e, referenceInt(1)))
	}},
	{"Asinh", func(x *big.Float) *big.Float {
		root := referenceSqrt(newReference().Add(newReference().Mul(x, x), referenceInt(1)))
		value := referenceLog(newReference().Add(absolute(x), root))
		if x.Sign() < 0 {
			value.Neg(value)
		}
		return value
	}},
	{"Acosh", func(x *big.Float) *big.Float {
		if less(x, 1) {
			return nil
		}
		root := referenceSqrt(newReference().Sub(newReference().Mul(x, x), referenceInt(1)))
		return referenceLog(newReference().Add(x, root))
	}},
	{"Atanh", func(x *big.Float) *big.Float {
		if !less(absolute(x), 1) {
			return nil
		}
		ratio := newReference().Quo(newReference().Add(referenceInt(1), x), newReference().Sub(referenceInt(1), x))
		log := referenceLog(ratio)
		return log.Quo(log, referenceInt(2))
	}},
}

func referenceRound(x *big.Float, mode big.RoundingMode) *big.Float {
	whole := newReference()
	whole.SetMode(mode)
	if mode == big.AwayFromZero {
		half := newReference().Quo(referenceInt(1), referenceInt(2))
		if x.Sign() < 0 {
			half.Neg(half)
		}
		shifted := newReference().Add(x, half)
		integer, _ := shifted.Int(nil)
		return whole.SetInt(integer)
	}
	integer, accuracy := x.Int(nil)
	value := whole.SetInt(integer)
	switch {
	case mode == big.ToNegativeInf && accuracy == big.Above:
		value.Sub(value, referenceInt(1))
	case mode == big.ToPositiveInf && accuracy == big.Below:
		value.Add(value, referenceInt(1))
	}
	return value
}

// referenceQuotient is x / y rounded to a whole number by mode, exactly.
func referenceQuotient(x, y *big.Float, mode big.RoundingMode) *big.Float {
	quotient := newReference().Quo(x, y)
	if mode == big.ToNearestEven {
		integer, _ := quotient.Int(nil)
		rest := newReference().Sub(quotient, newReference().SetInt(integer))
		half := newReference().Quo(referenceInt(1), referenceInt(2))
		switch c := absolute(rest).Cmp(half); {
		case c > 0, c == 0 && integer.Bit(0) == 1:
			integer.Add(integer, big.NewInt(int64(rest.Sign())))
		}
		return newReference().SetInt(integer)
	}
	integer, _ := quotient.Int(nil)
	return newReference().SetInt(integer)
}

var positBinaries = []struct {
	name string
	f    func(x, y *big.Float) *big.Float
}{
	{"Pow", func(x, y *big.Float) *big.Float {
		if y.Sign() == 0 || x.Cmp(referenceInt(1)) == 0 {
			return referenceInt(1)
		}
		if x.Sign() == 0 {
			if y.Sign() < 0 {
				return nil
			}
			return x
		}
		if x.Sign() > 0 {
			return referenceExp(newReference().Mul(y, referenceLog(x)))
		}
		if !y.IsInt() {
			return nil
		}
		value := referenceExp(newReference().Mul(y, referenceLog(absolute(x))))
		integer, _ := y.Int(nil)
		if integer.Bit(0) == 1 {
			value.Neg(value)
		}
		return value
	}},
	{"Mod", func(x, y *big.Float) *big.Float {
		if y.Sign() == 0 {
			return nil
		}
		return newReference().Sub(x, newReference().Mul(referenceQuotient(x, y, big.ToZero), y))
	}},
	{"Remainder", func(x, y *big.Float) *big.Float {
		if y.Sign() == 0 {
			return nil
		}
		return newReference().Sub(x, newReference().Mul(referenceQuotient(x, y, big.ToNearestEven), y))
	}},
	{"Hypot", func(x, y *big.Float) *big.Float {
		return referenceSqrt(newReference().Add(newReference().Mul(x, x), newReference().Mul(y, y)))
	}},
	{"Atan2", func(y, x *big.Float) *big.Float {
		switch {
		case x.Sign() == 0 && y.Sign() == 0:
			return x
		case x.Sign() == 0:
			half := newReference().Quo(referencePi, referenceInt(2))
			return half.Mul(half, referenceInt(int64(y.Sign())))
		}
		angle := referenceAtan(absolute(newReference().Quo(y, x)))
		if x.Sign() < 0 {
			angle.Sub(referencePi, angle)
		}
		if y.Sign() < 0 {
			angle.Neg(angle)
		}
		return angle
	}},
}

// referencePosit rounds a reference result, NaR where there is none.
func referencePosit(value *big.Float) uint64 {
	if value == nil {
		return base.PositNaR
	}
	return base.PositFromFloat(value)
}

// randomPosit draws a posit of scale within ±limit.
func randomPosit(random *rand.Rand, limit int) uint64 {
	for {
		bits := random.Uint64()
		if bits == base.PositNaR || bits == 0 {
			continue
		}
		scale := positValue(bits).MantExp(nil) - 1
		if scale >= -limit && scale <= limit {
			return bits
		}
	}
}

// TestPositMathRoundsOnce checks each pst function the runtime computes
// against a reference carried with 1600 bits, rounded once.
func TestPositMathRoundsOnce(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(2))
	decimal := func(literal string) uint64 {
		bits, err := base.PositFromDecimal(literal)
		require.NoError(t, err)
		return bits
	}
	inputs := []uint64{0, base.PositNaR, base.PositMaxPos, base.PositMinPos, decimal("1"), decimal("-1"),
		decimal("0.5"), decimal("-0.5"), decimal("2.5"), decimal("-2.5"), decimal("1e10"), decimal("1e-10"),
		decimal("3.14159265358979323846"), decimal("1e30"), decimal("0.999999999"), decimal("100")}
	for range 40 {
		inputs = append(inputs, randomPosit(random, 12))
	}
	for range 10 {
		inputs = append(inputs, randomPosit(random, 100))
	}
	var pairs [][2]uint64
	for range 60 {
		pairs = append(pairs, [2]uint64{randomPosit(random, 12), randomPosit(random, 12)})
	}
	for range 20 {
		pairs = append(pairs, [2]uint64{randomPosit(random, 60), randomPosit(random, 8)})
	}
	pairs = append(pairs, [2]uint64{decimal("-8"), decimal("3")}, [2]uint64{decimal("-8"), decimal("0.5")},
		[2]uint64{0, decimal("-1")}, [2]uint64{decimal("7"), decimal("2")}, [2]uint64{decimal("5"), decimal("2")},
		[2]uint64{decimal("-5"), decimal("2")}, [2]uint64{decimal("1"), 0}, [2]uint64{0, 0},
		[2]uint64{decimal("-1"), 0}, [2]uint64{decimal("3"), decimal("2")})

	var program strings.Builder
	for _, x := range inputs {
		fmt.Fprintf(&program, "        Main::unary(%dU);\n", x)
	}
	for _, pair := range pairs {
		fmt.Fprintf(&program, "        Main::binary(%dU, %dU);\n", pair[0], pair[1])
	}
	for _, x := range inputs[:12] {
		fmt.Fprintf(&program, "        Main::fma(%dU, %dU, %dU);\n", x, inputs[len(inputs)-1-int(x%7)], pairs[int(x%11)][0])
	}

	var unaryCalls, binaryCalls []string
	for _, f := range positUnaries {
		unaryCalls = append(unaryCalls, fmt.Sprintf("Psts::ToBits(p.Psts::%s(x)){str}", f.name))
	}
	for _, f := range positBinaries {
		binaryCalls = append(binaryCalls, fmt.Sprintf("Psts::ToBits(p.Psts::%s(x, y)){str}", f.name))
	}
	binary := compile(t, `import std("lang/*");
import std("io/local/*");
import p = std("math/Psts.zam");

namespace Main {
    static fun void(uint) unary = (u) {
        var pst x = Psts::FromBits(u);
        File::StdOut.WriteStr(`+strings.Join(unaryCalls, ` + " " + `)+` + "\n");
    }

    static fun void(uint, uint) binary = (u, v) {
        var pst x = Psts::FromBits(u);
        var pst y = Psts::FromBits(v);
        File::StdOut.WriteStr(`+strings.Join(binaryCalls, ` + " " + `)+` + "\n");
    }

    static fun void(uint, uint, uint) fma = (u, v, w) {
        var pst r = p.Psts::Fma(Psts::FromBits(u), Psts::FromBits(v), Psts::FromBits(w));
        File::StdOut.WriteStr(Psts::ToBits(r){str} + "\n");
    }

    fun void(uint, str...) main = (argC, argV) {
`+program.String()+`    }
}
`)
	stdout, stderr, status := run(t, binary)
	require.Zero(t, status, stderr)
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	require.Len(t, lines, len(inputs)+len(pairs)+12)

	for index, x := range inputs {
		got := strings.Fields(lines[index])
		for f, function := range positUnaries {
			want := base.PositNaR
			if x != base.PositNaR {
				want = referencePosit(function.f(positValue(x)))
			}
			assert.Equal(t, fmt.Sprint(want), got[f], "%s(%#x)", function.name, x)
		}
	}
	for index, pair := range pairs {
		got := strings.Fields(lines[len(inputs)+index])
		for f, function := range positBinaries {
			want := referencePosit(function.f(positValue(pair[0]), positValue(pair[1])))
			assert.Equal(t, fmt.Sprint(want), got[f], "%s(%#x, %#x)", function.name, pair[0], pair[1])
		}
	}
	for index, x := range inputs[:12] {
		y, z := inputs[len(inputs)-1-int(x%7)], pairs[int(x%11)][0]
		want := base.PositNaR
		if x != base.PositNaR && y != base.PositNaR && z != base.PositNaR {
			product := newReference().Mul(positValue(x), positValue(y))
			want = base.PositFromFloat(product.Add(product, positValue(z)))
		}
		assert.Equal(t, fmt.Sprint(want), lines[len(inputs)+len(pairs)+index], "Fma(%#x, %#x, %#x)", x, y, z)
	}
}
