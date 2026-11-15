package base

import (
	"errors"
	"math/big"
	"strings"
)

const (
	// PositNaR is the posit that is not a real number.
	PositNaR uint64 = 1 << 63
	// PositMaxPos is the largest positive posit, 2^248.
	PositMaxPos uint64 = PositNaR - 1
	// PositMinPos is the smallest positive posit, 2^-248.
	PositMinPos uint64 = 1

	positBits     = 64
	positMaxScale = 4 * (positBits - 2)
	positFraction = 256

	// float128Precision is the bits a quadruple-precision float's significand
	// holds, and float128MaxScale and float128MinScale bound the powers of two
	// it reaches, its subnormals included.
	float128Precision = 113
	float128MaxScale  = 16383
	float128MinScale  = -16494

	// positNaRText is how NaR is written.
	positNaRText = "NaR"
)

var (
	errInvalidPosit    = errors.New("invalid posit")
	errInvalidFloat128 = errors.New("not a finite quadruple-precision number, or NaR")
)

// PositFromDecimal rounds a decimal literal, digits with an optional fraction
// and exponent, to the nearest 64-bit posit with two exponent bits, ties to
// even. A nonzero value never rounds to zero, and none rounds to NaR.
func PositFromDecimal(literal string) (uint64, error) {
	x, _, err := big.ParseFloat(literal, 10, 2*positFraction, big.ToNearestEven)
	if err != nil || x.IsInf() {
		return 0, errInvalidPosit
	}
	return PositFromFloat(x), nil
}

// PositFromFloat rounds x to the nearest posit, as PositFromDecimal does.
func PositFromFloat(x *big.Float) uint64 {
	if x.Sign() == 0 {
		return 0
	}
	negative := x.Sign() < 0
	magnitude := new(big.Float).SetPrec(x.Prec()).Abs(x)
	scale := magnitude.MantExp(nil) - 1

	var bits uint64
	switch {
	case scale >= positMaxScale:
		bits = PositMaxPos
	case scale < -positMaxScale:
		bits = PositMinPos
	default:
		bits = positRound(magnitude, scale)
	}
	if negative {
		return -bits
	}
	return bits
}

// positRound lays out the regime, exponent and fraction of a magnitude
// 2^scale ≤ x < 2^(scale+1) and rounds the pattern to the 63 bits after the
// sign.
func positRound(x *big.Float, scale int) uint64 {
	k := scale >> 2
	exponent := int64(scale & 3)

	pattern := new(big.Int)
	length := 0
	if k >= 0 {
		pattern.Lsh(big.NewInt(1), uint(k+1))
		pattern.Sub(pattern, big.NewInt(1))
		pattern.Lsh(pattern, 1)
		length = k + 2
	} else {
		pattern.SetInt64(1)
		length = -k + 1
	}
	pattern.Lsh(pattern, 2)
	pattern.Or(pattern, big.NewInt(exponent))
	length += 2

	fraction := new(big.Float).SetPrec(x.Prec()).SetMantExp(x, positFraction-scale)
	whole, accuracy := fraction.Int(nil)
	whole.Sub(whole, new(big.Int).Lsh(big.NewInt(1), positFraction))
	pattern.Lsh(pattern, positFraction)
	pattern.Or(pattern, whole)
	length += positFraction

	shift := uint(length - (positBits - 1))
	rounded := new(big.Int).Rsh(pattern, shift)
	guard := pattern.Bit(int(shift-1)) == 1
	rest := new(big.Int).And(pattern, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), shift-1), big.NewInt(1)))
	sticky := rest.Sign() != 0 || accuracy != big.Exact
	bits := rounded.Uint64()
	if guard && (sticky || bits&1 == 1) {
		bits++
	}
	switch {
	case bits >= PositNaR:
		return PositMaxPos
	case bits == 0:
		return PositMinPos
	}
	return bits
}

// PositToFloat answers the value of a posit exactly, or false for NaR.
func PositToFloat(bits uint64) (*big.Float, bool) {
	x := new(big.Float).SetPrec(positBits)
	switch bits {
	case 0:
		return x, true
	case PositNaR:
		return nil, false
	}
	negative := bits>>(positBits-1) == 1
	if negative {
		bits = -bits
	}

	rest, remaining := bits<<1, positBits-1
	first := rest >> (positBits - 1)
	run := 0
	for run < remaining && rest>>(positBits-1) == first {
		run++
		rest <<= 1
	}
	remaining -= run
	if remaining > 0 {
		rest <<= 1
		remaining--
	}
	k := -run
	if first == 1 {
		k = run - 1
	}

	taken := min(2, remaining)
	var exponent uint64
	if taken > 0 {
		exponent = rest >> (positBits - taken)
	}
	exponent <<= 2 - taken
	rest <<= taken
	remaining -= taken

	var fraction uint64
	if remaining > 0 {
		fraction = rest >> (positBits - remaining)
	}
	significand := new(big.Int).Lsh(big.NewInt(1), uint(remaining))
	significand.Or(significand, new(big.Int).SetUint64(fraction))
	x.SetInt(significand)
	x.SetMantExp(x, 4*k+int(exponent)-remaining)
	if negative {
		x.Neg(x)
	}
	return x, true
}

// PositString writes a posit as the fewest decimal digits that round back to
// it, or as NaR.
func PositString(bits uint64) string {
	x, ok := PositToFloat(bits)
	if !ok {
		return positNaRText
	}
	var text string
	for digits := 1; ; digits++ {
		text = x.Text('g', digits)
		if back, err := PositFromDecimal(text); err == nil && back == bits {
			return text
		}
	}
}

// PositFromFloat128 reads text as a quadruple-precision float, and rounds that
// to the nearest posit as [PositFromFloat] does. A float out of the range of
// one is refused, being infinite, as are a NaN and an infinity, there being
// no posit of either: NaR is written as NaR.
func PositFromFloat128(text string) (uint64, error) {
	text = strings.TrimSpace(text)
	if strings.EqualFold(text, positNaRText) {
		return PositNaR, nil
	}
	x, _, err := big.ParseFloat(text, 10, float128Precision, big.ToNearestEven)
	if err != nil || x.IsInf() {
		return 0, errInvalidFloat128
	}
	if x.Sign() == 0 {
		return 0, nil
	}
	switch scale := x.MantExp(nil) - 1; {
	case scale > float128MaxScale:
		return 0, errInvalidFloat128
	case scale < float128MinScale:
		return 0, nil
	}
	return PositFromFloat(x), nil
}
