/*
 * Reference implementation of the interface dispatch table lookup.
 *
 * This is the C side of the table built by buildPerfectHash. It lives in
 * testdata so the Go test suite can compile and run it against tables the
 * compiler actually emits: the table carries no key to compare against, so
 * arithmetic here that disagrees with the compiler's does not fail a lookup, it
 * silently resolves to another method. Holding both to the same vectors is what
 * makes that impossible rather than unlikely.
 *
 * The runtime is expected to copy this file rather than reimplement it.
 */

#include <stddef.h>
#include <stdint.h>

/* The table is a run of little-endian 64-bit words, in the order Read writes
 * them: seed, slot count, bucket count, then one word per bucket and one value
 * per slot. It is decoded a byte at a time because the wire format is
 * little-endian whatever the host is. */
static uint64_t fks_word(const unsigned char *p) {
    return (uint64_t)p[0] | (uint64_t)p[1] << 8 | (uint64_t)p[2] << 16 |
           (uint64_t)p[3] << 24 | (uint64_t)p[4] << 32 | (uint64_t)p[5] << 40 |
           (uint64_t)p[6] << 48 | (uint64_t)p[7] << 56;
}

/* Maps x into [0, n) by multiplying and keeping the high half: what a modulo
 * would answer without the division it would cost. */
static uint32_t fks_reduce(uint32_t x, uint32_t n) {
    return (uint32_t)(((uint64_t)x * (uint64_t)n) >> 32);
}

/* The 64-bit finalizer. A bare multiply will not do: fks_reduce answers from
 * the high bits, and a small key times a small seed has none set. */
static uint64_t fks_hash(uint64_t key, uint64_t seed) {
    uint64_t h = key ^ seed;
    h ^= h >> 33;
    h *= 0xff51afd7ed558ccdULL;
    h ^= h >> 33;
    h *= 0xc4ceb9fe1a85ec53ULL;
    h ^= h >> 33;
    return h;
}

/* The one arithmetic both levels are addressed by: hash under a seed and reduce
 * into the range asked for. The levels differ only in seed and range. */
static uint32_t fks_index(uint64_t key, uint64_t seed, uint32_t n) {
    return fks_reduce((uint32_t)(fks_hash(key, seed) >> 32), n);
}

/* Resolves a key that is known to be present, in two levels: the table's seed
 * sorts the key into a bucket, and the bucket's own seed places it within the
 * block of slots the bucket owns. A bucket word holds where its block begins in
 * the high half and its seed in the low half; the bucket after it holds where
 * the block ends, which is what gives the block its width.
 *
 * A class implementing no interfaces has no buckets. Nothing should reach here
 * for such a class -- the type checker has proven the method is implemented
 * before a lookup is emitted -- but answering 0 is what keeps a table that is
 * asked anyway from being indexed off its end. */
uint32_t fks_lookup(const unsigned char *table, uint64_t key) {
    uint64_t seed = fks_word(table);
    uint32_t bucket_count = (uint32_t)fks_word(table + 16);
    const unsigned char *buckets = table + 24;
    const unsigned char *slots = buckets + (size_t)bucket_count * 8;

    if (bucket_count == 0) {
        return 0;
    }

    uint32_t index = fks_index(key, seed, bucket_count - 1);
    uint64_t bucket = fks_word(buckets + (size_t)index * 8);
    uint32_t offset = (uint32_t)(bucket >> 32);
    uint32_t width =
        (uint32_t)(fks_word(buckets + ((size_t)index + 1) * 8) >> 32) - offset;

    uint32_t slot = offset + fks_index(key, (uint64_t)(uint32_t)bucket, width);
    return (uint32_t)fks_word(slots + (size_t)slot * 8);
}
