package base

import (
	"encoding/binary"
	"hash/fnv"
)

// These hashes are bucket keys for maps that live only inside the compiler, so
// what they are matters far less than that the same input always gives the same
// answer. maphash cannot: its seed is chosen at random, per Hash value, so two
// calls on identical input land in different buckets and every lookup misses.
// FNV-1a takes no seed and so has nothing to vary.
//
// Bytes go in big-endian, unlike the little-endian bytecode, because nothing
// outside this package ever sees them.

// Hash returns the hash of the given words.
func Hash(data ...uint64) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	for _, d := range data {
		binary.BigEndian.PutUint64(buf[:], d)
		h.Write(buf[:])
	}
	return h.Sum64()
}

// FNV returns the hash of the given strings.
func FNV(s ...string) uint64 {
	h := fnv.New64a()
	for _, s := range s {
		h.Write([]byte(s))
	}
	return h.Sum64()
}
