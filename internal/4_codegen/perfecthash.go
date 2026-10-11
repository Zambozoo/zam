package codegen

import (
	"fmt"
	"io"
	"slices"
)

const (
	// fksSlotFactor is how many slots a key is given. Corollary 1 of
	// Fredman-Komlós-Szemerédi proves a level-1 seed exists whose buckets satisfy
	// sum |W|^2 < 3n, so three slots a key is a bound the search can be held to
	// rather than a width it settles on. A seed whose buckets do not fit inside
	// it is discarded for another, which is what lets the width be answered from
	// the key count before the table has been built.
	fksSlotFactor = 3
	// fksSeedLimit is how many seeds one bucket is tried at before the level-1
	// seed that made it is given up on. A bucket of w keys is injective in w^2
	// slots for about half of all seeds, so this is reached only by a bucket the
	// level above should not have built, and reaching for a new level-1 seed
	// costs less than going on searching under the old one.
	//
	// The bucket's seed rides in the low half of its word, so the search could
	// not run past a uint32 in any case.
	fksSeedLimit = 1 << 16
)

// perfectHash is a two-level perfect hash of fixed width. A level-1 seed sorts
// the keys into as many buckets as there are keys, and each bucket carries a
// seed of its own that lands its own keys in a block of |W|^2 slots without
// collision. The bucket's block is found by redirection: the bucket holds where
// its block begins, and the block after it holds where it ends.
//
// Nothing records which keys were built in. A key that was not lands in some
// other key's slot and reads its value as though it were its own, so this may
// only be asked about keys already known to be present — which is what makes it
// suitable for interface dispatch, where the type checker has proven the method
// is implemented before the lookup is ever emitted.
//
// The width is fksSlotFactor slots a key whatever the keys turn out to be. A
// build that has to try again costs time and never bytes.
type perfectHash struct {
	seed uint64
	// buckets hold a block's start in the high half and the block's seed in the
	// low half, and run one past the last bucket so that every bucket's width is
	// the distance to the one after it.
	buckets []uint64
	slots   []uint32
}

// reduce maps x into [0, n) by multiplying and keeping the high half. It is the
// answer a modulo would give without the division a modulo would cost. The paper
// hashes by (kx mod p) mod n; this is its section 5 variation, which asks for no
// prime and no division on a path every interface call runs down.
func reduce(x, n uint32) uint32 {
	return uint32((uint64(x) * uint64(n)) >> 32)
}

// fksHash mixes a key until every bit of it has reached every bit of the answer.
// Multiplying by the seed alone will not do: reduce answers from the high bits
// of what it is given, and a small key times a small seed is a small product
// whose high bits are all zero, so whole tables would come back sharing one
// bucket. The rounds below are the standard 64-bit finalizer, which avalanches
// whatever it is handed however little of it differs.
func fksHash(key, seed uint64) uint64 {
	h := key ^ seed
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// fksIndex is the one arithmetic both levels are addressed by: hash under a seed
// and reduce into the range asked for. The two levels differ only in the seed
// they use and the range they land in.
func fksIndex(key, seed uint64, n uint32) uint32 {
	return reduce(uint32(fksHash(key, seed)>>32), n)
}

// lookup resolves a key the way the runtime does. It exists so tests can hold
// the compiler to the same arithmetic the C side implements.
func (h *perfectHash) lookup(key uint64) uint32 {
	index := fksIndex(key, h.seed, uint32(len(h.buckets)-1))
	bucket := h.buckets[index]
	offset := uint32(bucket >> 32)
	width := uint32(h.buckets[index+1]>>32) - offset
	return h.slots[offset+fksIndex(key, uint64(uint32(bucket)), width)]
}

// perfectHashByteCount is how wide the table over count keys will be, which is
// settled by the count alone and so can be asked before the table is built.
func perfectHashByteCount(count int) int {
	if count == 0 {
		return 3 * wordByteSize
	}
	return (3 + count + 1 + fksSlotFactor*count) * wordByteSize
}

func (h *perfectHash) ByteCount() int {
	return (3 + len(h.buckets) + len(h.slots)) * wordByteSize
}

// Read writes the seed, the slot count and the bucket count, then each bucket's
// block start and seed, then the value in each slot.
func (h *perfectHash) Read(p []byte) (int, error) {
	if len(p) < h.ByteCount() {
		return 0, io.ErrShortBuffer
	}

	totalBytes := 0
	for _, value := range []uint64{
		h.seed,
		uint64(len(h.slots)),
		uint64(len(h.buckets)),
	} {
		n, _ := readUint64(p[totalBytes:], value)
		totalBytes += n
	}

	for _, bucket := range h.buckets {
		n, _ := readUint64(p[totalBytes:], bucket)
		totalBytes += n
	}
	for _, slot := range h.slots {
		n, _ := readUint64(p[totalBytes:], uint64(slot))
		totalBytes += n
	}

	return totalBytes, nil
}

// buildPerfectHash constructs the hash over the entries given it, by the
// Fredman-Komlós-Szemerédi method: a level-1 seed spreads the keys across as
// many buckets as there are keys, and each bucket is then resolved on its own in
// a block of its size squared, which is small enough that a seed making it
// injective is found in a try or two.
//
// The search only ever reaches for another seed. Both the number of buckets and
// the number of slots are fixed by the key count, so a seed that does not fit is
// abandoned rather than the table widened, and every build of n keys is the same
// number of bytes.
//
// The search is unbounded, and so cannot fail. The keys arrive as a map and are
// therefore distinct, and the mixer avalanches the seed into every bit of the
// key, so no pair of keys collides under every seed: a seed that fits is only
// ever a matter of reaching it. Corollary 1 is satisfied by better than one seed
// in three, which is what makes reaching it a question of a try or two rather
// than a search worth putting a ceiling on.
func buildPerfectHash(entries map[uint64]uint32) *perfectHash {
	count := len(entries)
	if count == 0 {
		return &perfectHash{}
	}

	// The keys are sorted so the construction is decided by the keys alone. Go
	// randomizes map iteration, so bucketing them as they came would build a
	// different table on every compile and leave the output irreproducible.
	keys := make([]uint64, 0, count)
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for seed := uint64(1); ; seed++ {
		if h, ok := tryPerfectHash(entries, keys, seed); ok {
			verifyPerfectHash(h, entries)
			return h
		}
	}
}

// verifyPerfectHash resolves every key the way the runtime will, and refuses to
// let a table that answers any of them wrongly reach the wire.
//
// A dispatch table cannot be checked where it is read: there is nothing in a
// slot to compare a key against, so a key placed wrongly does not fail at
// dispatch, it calls a different method. Construction is therefore the only
// moment at which that is still detectable, and the cost of looking is a few
// multiplies per entry per compile.
func verifyPerfectHash(h *perfectHash, entries map[uint64]uint32) {
	for key, value := range entries {
		if got := h.lookup(key); got != value {
			panic(fmt.Sprintf(
				"perfect hash resolved key %#016x to %d, not %d", key, got, value))
		}
	}
}

func tryPerfectHash(entries map[uint64]uint32, keys []uint64, seed uint64) (*perfectHash, bool) {
	count := uint32(len(keys))

	buckets := make([][]uint64, count)
	for _, key := range keys {
		bucket := fksIndex(key, seed, count)
		buckets[bucket] = append(buckets[bucket], key)
	}

	// Corollary 1 is what the slot array was sized by, so a seed whose buckets
	// ask for more than it promised is the seed to give up on.
	total := 0
	for _, bucket := range buckets {
		total += len(bucket) * len(bucket)
	}
	if total > fksSlotFactor*len(keys) {
		return nil, false
	}

	h := &perfectHash{
		seed:    seed,
		buckets: make([]uint64, count+1),
		slots:   make([]uint32, fksSlotFactor*count),
	}

	offset := uint32(0)
	for i, bucket := range buckets {
		width := uint32(len(bucket) * len(bucket))
		bucketSeed, ok := resolveBucket(entries, bucket, h.slots[offset:offset+width])
		if !ok {
			return nil, false
		}
		h.buckets[i] = uint64(offset)<<32 | uint64(bucketSeed)
		offset += width
	}
	// The bucket one past the last is where the last bucket's block ends, which
	// is what gives every bucket before it a width.
	h.buckets[count] = uint64(offset) << 32

	return h, true
}

// resolveBucket finds a seed landing every key of one bucket in a slot of its
// own within the bucket's block, and fills the block in.
//
// Corollary 2 proves such a seed exists for a block of |W|^2, and the odds are
// better than even for any one seed, so this settles almost at once. A bucket of
// one key is answered by the first seed tried: one slot is the whole block, and
// reduce lands everything in it.
func resolveBucket(entries map[uint64]uint32, keys []uint64, block []uint32) (uint32, bool) {
	if len(keys) == 0 {
		return 0, true
	}

	width := uint32(len(block))
	slots := make([]uint32, 0, len(keys))
	for seed := uint32(1); seed <= fksSeedLimit; seed++ {
		slots = slots[:0]
		fits := true
		for _, key := range keys {
			slot := fksIndex(key, uint64(seed), width)
			if slices.Contains(slots, slot) {
				fits = false
				break
			}
			slots = append(slots, slot)
		}
		if !fits {
			continue
		}

		for i, key := range keys {
			block[slots[i]] = entries[key]
		}
		return seed, true
	}
	return 0, false
}
