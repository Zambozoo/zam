#pragma once

#include <pthread.h>
#include <stdatomic.h>

#include "common.h"

// The heap is one reserved range of address space that every allocation the
// collector owns and every block of the program's own memory is carved from.
// Being one range is what lets "is this managed" be a compare, and what lets
// the card table beside it be indexed by subtraction.
//
// The range is cut into pages. A small block lies in a page given over to one
// size class; a large one takes a run of whole pages to itself.
#define ZAMHEAP_PAGE_BYTES ((uint64_t)64 << 10)
#define ZAMHEAP_SMALL_MAX_BYTES (ZAMHEAP_PAGE_BYTES / 2)
#define ZAMHEAP_CLASS_COUNT 40

// A card is the span of heap one byte of the card table speaks for. A store of
// a reference into the heap dirties its card, and a minor collection scans
// only the dirty ones for what the old generation holds of the young.

#define ZAMHEAP_CARD_SHIFT 9
#define ZAMHEAP_CARD_BYTES ((uint64_t)1 << ZAMHEAP_CARD_SHIFT)

typedef struct ZamHeapPage ZamHeapPage;

// ZamHeapCache is the page of each size class a thread allocates from without
// taking the heap's lock. An entry is a page index plus one, zero for none.
typedef struct ZamHeapCache {
    uint64_t pages[ZAMHEAP_CLASS_COUNT];
} ZamHeapCache;

typedef struct ZamHeap {
    uint8_t *reservation;
    uint8_t *base;
    uint64_t byteCount;
    uint8_t *cards;
    ZamHeapPage *pages;
    uint64_t pageCount;

    // objects has a bit for every block holding an allocation the collector
    // owns, set once the allocation is whole and cleared when it is freed.
    uint8_t *objects;

    // returnAge is how many collections a run of pages has to stay free for
    // before its memory is given back to the system, so that a heap shrinking
    // for a moment does not pay to grow again.
    uint64_t returnAge;

    // frontier is how many pages from the base have ever been handed out;
    // everything past it is untouched. It moves only under the lock.
    uint64_t frontier;

    // lock guards the free runs, the available lists, and every page no
    // cache owns.
    pthread_mutex_t lock;

    // freeRuns heads the list of runs of free pages, and available heads, per
    // size class, the list of pages no cache owns that have a slot to give.
    // Both are a page index plus one, zero for none.
    uint64_t freeRuns;

    // epoch counts scavenges, which is the clock a free run ages by.
    uint64_t epoch;
    uint64_t available[ZAMHEAP_CLASS_COUNT];
} ZamHeap;

// zam_heap_init reserves byteCount bytes of address space, rounded up to a
// page, along with the card table and the page table that describe it. A run
// of free pages is given back to the system once it has stayed free for
// returnAge scavenges.
void zam_heap_init(ZamHeap *heap, uint64_t byteCount, uint64_t returnAge);

// zam_heap_destroy gives the whole range back at once.
void zam_heap_destroy(ZamHeap *heap);

// zam_heap_alloc carves a block of at least byteCount bytes, aligned to 16
// and not zeroed, from the cache's pages. It dies when the range is spent.
void *zam_heap_alloc(ZamHeap *heap, ZamHeapCache *cache, uint64_t byteCount);

// zam_heap_free gives a block back. cache is the freeing thread's, which lets
// a block in one of its own pages go back without the lock; a block in any
// other thread's page is handed to that thread to take back.
void zam_heap_free(ZamHeap *heap, ZamHeapCache *cache, void *block);

// zam_heap_free_stopped gives a block back while every thread that allocates
// is stopped, as during a sweep. With nothing else touching the heap, the block
// goes straight onto its page whoever owns it, and no lock is taken.
void zam_heap_free_stopped(ZamHeap *heap, void *block);

// zam_heap_set_object records that a block holds an allocation the collector
// owns, which zam_heap_object_of answers for from then until it is freed. It is
// called once the allocation is whole, so that one found by it reads as one.
void zam_heap_set_object(ZamHeap *heap, const void *block);

// zam_heap_object_of is the allocation the collector owns that an address lies
// in, or NULL where there is none: outside the heap, in a free block, or in a
// block of the program's own memory. It is safe on any address at all, and
// while the program runs.
void *zam_heap_object_of(const ZamHeap *heap, const void *at);

// zam_heap_size is how many bytes the block was given, which is at least what
// was asked for.
uint64_t zam_heap_size(const ZamHeap *heap, const void *block);

// zam_heap_release_cache returns every page a cache holds to the heap, for a
// thread that will not allocate again.
void zam_heap_release_cache(ZamHeap *heap, ZamHeapCache *cache);

// zam_heap_scavenge advances the epoch and gives back to the system the memory
// of every run of pages free for returnAge epochs, answering how many
// bytes that was. The range stays reserved, and the run is handed out again as
// any other is.
uint64_t zam_heap_scavenge(ZamHeap *heap);

// zam_heap_dirty_cards calls visit with the start and end of every run of
// dirty cards in the part of the range ever handed out, and leaves them clean.
// A run is split at page boundaries, and the part on a free page is skipped.
void zam_heap_dirty_cards(ZamHeap *heap,
                          void (*visit)(void *context, const uint8_t *start,
                                        const uint8_t *end),
                          void *context);

// zam_heap_scan_cards is zam_heap_dirty_cards leaving every card as it found
// it.
void zam_heap_scan_cards(ZamHeap *heap,
                         void (*visit)(void *context, const uint8_t *start,
                                       const uint8_t *end),
                         void *context);

// zam_heap_preclean_cards is zam_heap_dirty_cards run while the program runs
// and dirties cards of its own. Each card is cleaned before its words are
// read, so a store landing on it after is seen by whatever next scans it. It
// answers how many cards it cleaned.
uint64_t zam_heap_preclean_cards(ZamHeap *heap,
                                 void (*visit)(void *context,
                                               const uint8_t *start,
                                               const uint8_t *end),
                                 void *context);

// zam_heap_clear_cards marks every card clean.
void zam_heap_clear_cards(ZamHeap *heap);

static inline bool zam_heap_contains(const ZamHeap *heap, const void *at) {
    return (uintptr_t)at - (uintptr_t)heap->base < heap->byteCount;
}

// zam_heap_dirty marks the cards covering byteCount bytes at an address dirty,
// and does nothing for an address outside the heap.
static inline void zam_heap_dirty(ZamHeap *heap, const void *at,
                                  uint64_t byteCount) {
    uint64_t offset = (uintptr_t)at - (uintptr_t)heap->base;
    if (offset >= heap->byteCount || byteCount == 0)
        return;
    uint64_t last = offset + byteCount - 1;
    if (last >= heap->byteCount)
        last = heap->byteCount - 1;
    uint64_t first = offset >> ZAMHEAP_CARD_SHIFT;
    uint64_t end = last >> ZAMHEAP_CARD_SHIFT;
    // Released, so that a marker that finds the card dirty and cleans it
    // reads what was stored before it was dirtied.
    if (first == end) {
        __atomic_store_n(&heap->cards[first], 1, __ATOMIC_RELEASE);
        return;
    }
    __atomic_thread_fence(__ATOMIC_RELEASE);
    for (uint64_t card = first; card <= end; card++)
        __atomic_store_n(&heap->cards[card], 1, __ATOMIC_RELAXED);
}
