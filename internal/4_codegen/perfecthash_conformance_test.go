package codegen

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildCReference compiles the reference lookup in testdata and answers where
// the harness around it was put.
func buildCReference(t *testing.T) string {
	t.Helper()

	compiler, err := exec.LookPath("cc")
	require.NoError(t, err,
		"the reference lookup cannot be held to the compiler without a C compiler")

	harness := filepath.Join(t.TempDir(), "fks")
	build := exec.Command(compiler, "-std=c99", "-O2", "-Wall", "-Wextra", "-Werror",
		"-o", harness, filepath.Join("testdata", "fks_harness.c"))
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building the reference lookup: %s", out)

	return harness
}

func runCReference(t *testing.T, harness string, input string, arg ...string) []string {
	t.Helper()

	run := exec.Command(harness, arg...)
	run.Stdin = strings.NewReader(input)
	out, err := run.Output()
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// TestPerfectHashCConformance runs the reference lookup over tables the
// compiler built, key by key.
//
// The table holds no key to compare against, so a C side whose arithmetic has
// drifted from the compiler's does not fail a lookup: it lands on another
// method's slot and calls that instead. There is no moment at which the runtime
// could notice, so the divergence has to be caught here, where both
// implementations can be run over the same vectors.
func TestPerfectHashCConformance(t *testing.T) {
	t.Parallel()
	harness := buildCReference(t)

	shapes := map[string]map[uint64]uint32{
		// One entry is the degenerate table: a single bucket of a single slot,
		// where the block's width comes from a sentinel one word further on.
		"a single method":                        interfaceMethodKeys(1, 1),
		"one interface":                          interfaceMethodKeys(1, 5),
		"several interfaces":                     interfaceMethodKeys(6, 8),
		"widely spread type ids":                 interfaceMethodKeys(40, 6),
		"keys sharing no pattern":                randomKeys(257),
		"more keys than a class would ever have": randomKeys(1024),
	}

	for name, entries := range shapes {
		t.Run(name, func(t *testing.T) {
			h := buildPerfectHash(entries)

			table := make([]byte, h.ByteCount())
			_, err := h.Read(table)
			require.NoError(t, err)

			path := filepath.Join(t.TempDir(), "table.bin")
			require.NoError(t, os.WriteFile(path, table, 0o600))

			keys := slices.Sorted(maps.Keys(entries))
			var input strings.Builder
			for _, key := range keys {
				fmt.Fprintf(&input, "%d\n", key)
			}

			answers := runCReference(t, harness, input.String(), "lookup", path)

			require.Len(t, answers, len(keys))
			for i, key := range keys {
				assert.Equal(t, fmt.Sprint(entries[key]), answers[i],
					"the reference lookup resolved key %#016x differently", key)
			}
		})
	}
}

// TestPerfectHashCPrimitives holds the reference to the compiler's arithmetic
// piece by piece, at parameters chosen so that every bit of it matters.
//
// Resolving keys is not enough on its own. An index is read from the high bits
// of a product, so a mixing constant that is wrong in its low bits moves the
// index by less than an index is wide and resolves table after table correctly
// — until one table where it does not, and nothing is watching by then. Asking
// at a range near the width of the product is what gives such a difference
// somewhere to show.
func TestPerfectHashCPrimitives(t *testing.T) {
	t.Parallel()
	harness := buildCReference(t)

	type probe struct {
		key, seed uint64
		n         uint32
	}

	var probes []probe
	r := rand.New(rand.NewPCG(3, 4))
	// A range this near the width of the product is what makes the low bits of
	// the mixing reach the answer: at an index per two hashes, a difference of
	// one in the hash is a difference of one in the index half the time, where
	// at a few dozen it would be lost every time.
	for _, n := range []uint32{1, 2, 48, 1024, 1 << 31, 0xFFFFFFFF} {
		for range 4 {
			probes = append(probes, probe{key: r.Uint64(), seed: r.Uint64() % fksSeedLimit, n: n})
		}
	}
	// Small keys and seeds are the case the finalizer exists for: they are what
	// a bare multiply would leave sharing an index. Both levels reach here with
	// small seeds — level two starts its search at 1 — so this is the ordinary
	// case, not the corner.
	for key := range uint64(8) {
		probes = append(probes, probe{key: key, seed: 1, n: 1 << 31})
	}

	var input strings.Builder
	for _, p := range probes {
		fmt.Fprintf(&input, "%d %d %d\n", p.key, p.seed, p.n)
	}

	answers := runCReference(t, harness, input.String(), "index")

	require.Len(t, answers, len(probes))
	for i, p := range probes {
		expected := fmt.Sprintf("%d %d", fksHash(p.key, p.seed), fksIndex(p.key, p.seed, p.n))
		assert.Equal(t, expected, answers[i],
			"key %#016x seed %d n %d", p.key, p.seed, p.n)
	}
}

// A class implementing no interfaces emits a table with no buckets. Nothing
// should ever dispatch through one, but the runtime cannot bounds-check a table
// that carries no keys, so an empty one has to answer rather than index off its
// own end.
func TestPerfectHashCEmptyTableAnswersWithoutReadingPastItself(t *testing.T) {
	t.Parallel()
	harness := buildCReference(t)

	h := buildPerfectHash(nil)
	table := make([]byte, h.ByteCount())
	_, err := h.Read(table)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "table.bin")
	require.NoError(t, os.WriteFile(path, table, 0o600))

	assert.Equal(t, []string{"0"}, runCReference(t, harness, "12345\n", "lookup", path))
}

func randomKeys(count int) map[uint64]uint32 {
	entries := map[uint64]uint32{}
	r := rand.New(rand.NewPCG(2, uint64(count)))
	for len(entries) < count {
		entries[r.Uint64()] = uint32(len(entries))
	}
	return entries
}
