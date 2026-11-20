package base

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPositFromDecimal(t *testing.T) {
	t.Parallel()
	for literal, want := range map[string]uint64{
		"0":      0,
		"1":      0x4000000000000000,
		"2":      0x4800000000000000,
		"3":      0x4C00000000000000,
		"0.5":    0x3800000000000000,
		"16":     0x6000000000000000,
		"0.0625": 0x2000000000000000,
		"1e100":  PositMaxPos,
		"1e-100": PositMinPos,
		// 1 + 2^-60 lies halfway between 1 and the posit above it, and ties
		// to the even one.
		"1.000000000000000000867361737988403547205962240695953369140625": 0x4000000000000000,
	} {
		got, err := PositFromDecimal(literal)
		require.NoError(t, err, literal)
		assert.Equal(t, want, got, literal)
	}
}

func TestPositFromDecimalRejectsGarbage(t *testing.T) {
	t.Parallel()
	_, err := PositFromDecimal("1.2.3")
	assert.Error(t, err)
}

func TestPositString(t *testing.T) {
	t.Parallel()
	for bits, want := range map[uint64]string{
		0:                  "0",
		0x4000000000000000: "1",
		0xC000000000000000: "-1",
		0x3800000000000000: "0.5",
		0x4C00000000000000: "3",
		0x6000000000000000: "16",
		PositNaR:           "NaR",
	} {
		assert.Equal(t, want, PositString(bits), "%#x", bits)
	}

	for _, literal := range []string{"0.1", "-2.75", "1e-30", "123456.789", "1e70"} {
		bits, err := PositFromDecimal(literal)
		require.NoError(t, err)
		text := PositString(bits)
		back, err := PositFromDecimal(text)
		require.NoError(t, err)
		assert.Equal(t, bits, back, "%s writes as %s, which reads back", literal, text)
	}
	assert.Equal(t, "0.1", PositString(must(PositFromDecimal("0.1"))))
	assert.Equal(t, PositMaxPos, must(PositFromDecimal(PositString(PositMaxPos))))
	assert.Equal(t, PositMinPos, must(PositFromDecimal(PositString(PositMinPos))))
}

func TestPositToFloatReadsEveryField(t *testing.T) {
	t.Parallel()
	for _, bits := range []uint64{1, 2, 0x123456789ABCDEF, 0x4000000000000001, PositMaxPos, PositMaxPos - 1, PositNaR + 1, 0xC000000000000001} {
		x, ok := PositToFloat(bits)
		require.True(t, ok)
		assert.Equal(t, bits, PositFromFloat(x), "%#x", bits)
	}
}

func TestPositFromFloat128(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]uint64{
		"1.5":      0x4400000000000000,
		" -1 ":     0xC000000000000000,
		"NaR":      PositNaR,
		"nar":      PositNaR,
		"0":        0,
		"1e300":    PositMaxPos,
		"1e-300":   PositMinPos,
		"1e-5000":  0,
		"0.25":     0x3000000000000000,
		"1.0e4000": PositMaxPos,
	} {
		got, err := PositFromFloat128(text)
		require.NoError(t, err, text)
		assert.Equal(t, want, got, text)
	}
	for _, text := range []string{"inf", "-Inf", "nan", "NaN", "1e5000", "pi", ""} {
		_, err := PositFromFloat128(text)
		assert.Error(t, err, text)
	}
}

func must(bits uint64, err error) uint64 {
	if err != nil {
		panic(err)
	}
	return bits
}
