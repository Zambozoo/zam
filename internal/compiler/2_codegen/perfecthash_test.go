package codegen

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
)

// interfaceMethodKeys builds keys shaped the way interface dispatch keys are:
// an interface type id and generic id in the high half, a method index in the
// low half, which is far from uniformly distributed.
func interfaceMethodKeys(typeCount, methodCount int) map[uint64]uint32 {
	entries := map[uint64]uint32{}
	for typeID := range typeCount {
		for methodIndex := range methodCount {
			key := uint64(typeID+1)<<48 | uint64(methodIndex)
			entries[key] = uint32(len(entries))
		}
	}
	return entries
}

func TestBuildPerfectHash(t *testing.T) {
	t.Run("every key finds its own value", func(t *testing.T) {
		for _, count := range []int{1, 2, 7, 33, 128, 501} {
			entries := map[uint64]uint32{}
			r := rand.New(rand.NewPCG(1, uint64(count)))
			for len(entries) < count {
				entries[r.Uint64()] = uint32(len(entries))
			}

			h := buildPerfectHash(entries)

			for key, value := range entries {
				assert.Equal(t, value, h.lookup(key),
					"key %#x resolved wrongly in a table of %d", key, count)
			}
		}
	})

	t.Run("keys shaped like dispatch keys are placed", func(t *testing.T) {
		// Every interface inherits Object's five methods, so a class
		// implementing several interfaces reaches a few dozen entries whose
		// keys share most of their bits.
		entries := interfaceMethodKeys(6, 8)

		h := buildPerfectHash(entries)

		for key, value := range entries {
			assert.Equal(t, value, h.lookup(key))
		}
	})

	// The whole reason for this scheme over one that widens on collision: the
	// width is settled by the key count, so it can be answered — and header
	// offsets committed — before the table has been built. A build that has to
	// try another seed costs time and never bytes.
	t.Run("the width is the key count's alone", func(t *testing.T) {
		for _, count := range []int{0, 1, 2, 7, 33, 128, 501} {
			entries := map[uint64]uint32{}
			r := rand.New(rand.NewPCG(5, uint64(count)))
			for len(entries) < count {
				entries[r.Uint64()] = uint32(len(entries))
			}

			h := buildPerfectHash(entries)

			assert.Equal(t, perfectHashByteCount(count), h.ByteCount(),
				"a table of %d keys is not the width promised for %d keys", count, count)
			if count > 0 {
				assert.Len(t, h.slots, fksSlotFactor*count)
				// One bucket per key, and one past the last so that every
				// bucket's width is the distance to the one after it.
				assert.Len(t, h.buckets, count+1)
			}
		}
	})

	t.Run("the same entries build the same table", func(t *testing.T) {
		// Go randomizes map iteration, so building twice from equal maps is
		// what catches a construction that depends on the order keys arrived.
		first := buildPerfectHash(interfaceMethodKeys(4, 5))
		second := buildPerfectHash(interfaceMethodKeys(4, 5))

		assert.Equal(t, first.seed, second.seed)
		assert.Equal(t, first.buckets, second.buckets)
		assert.Equal(t, first.slots, second.slots)
	})

	t.Run("no entries is an empty table", func(t *testing.T) {
		h := buildPerfectHash(nil)

		assert.Empty(t, h.slots)
		assert.Empty(t, h.buckets)
		assert.Equal(t, 3*wordByteSize, h.ByteCount())
	})
}

// Nothing in the table records how wide a block is; a bucket's width is read as
// the distance to the bucket after it. So the offsets have to climb, and the
// last of them has to stop inside the slots, or the runtime indexes past the
// end of a table it has no way to bounds-check.
func TestPerfectHashBlocksStayInsideTheSlots(t *testing.T) {
	h := buildPerfectHash(interfaceMethodKeys(6, 8))

	previous := uint32(0)
	for i, bucket := range h.buckets {
		offset := uint32(bucket >> 32)
		assert.GreaterOrEqual(t, offset, previous, "bucket %d begins before bucket %d ends", i, i-1)
		previous = offset
	}
	assert.Zero(t, uint32(h.buckets[0]>>32), "the first block does not begin at the first slot")
	assert.LessOrEqual(t, int(previous), len(h.slots),
		"the blocks run past the slots they were allotted")
}

func TestPerfectHashRead(t *testing.T) {
	h := buildPerfectHash(interfaceMethodKeys(2, 3))
	p := make([]byte, h.ByteCount())

	n, err := h.Read(p)

	assert.NoError(t, err)
	assert.Equal(t, h.ByteCount(), n)
	assert.Equal(t, h.seed, binary.LittleEndian.Uint64(p))
	assert.Equal(t, uint64(len(h.slots)), binary.LittleEndian.Uint64(p[wordByteSize:]))
	assert.Equal(t, uint64(len(h.buckets)), binary.LittleEndian.Uint64(p[2*wordByteSize:]))

	// The buckets follow the three header words and the slots follow them,
	// which is the order the runtime indexes them in.
	for i, bucket := range h.buckets {
		assert.Equal(t, bucket, binary.LittleEndian.Uint64(p[(3+i)*wordByteSize:]))
	}
	for i, slot := range h.slots {
		assert.Equal(t, uint64(slot),
			binary.LittleEndian.Uint64(p[(3+len(h.buckets)+i)*wordByteSize:]))
	}
}
