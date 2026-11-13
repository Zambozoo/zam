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
