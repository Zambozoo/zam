package base

import (
	"errors"
	"math/big"
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
)

var errInvalidPosit = errors.New("invalid posit")

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
