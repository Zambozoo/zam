#include "heap.h"

#if defined(_WIN32)
#include <windows.h>
#else
#include <sys/mman.h>

#ifndef MAP_NORESERVE
#define MAP_NORESERVE 0
#endif
#endif

#if defined(__has_feature)
#if __has_feature(address_sanitizer)
#define ZAMHEAP_ASAN 1
#endif
#endif
#if defined(__SANITIZE_ADDRESS__)
#define ZAMHEAP_ASAN 1
#endif

#ifdef ZAMHEAP_ASAN
#include <sanitizer/asan_interface.h>
// A free block is poisoned but for the link at its front, so that a sanitized
// build reports a write through a dangling pointer where it happens.
#define POISON(at, bytes) ASAN_POISON_MEMORY_REGION((at), (bytes))
#define UNPOISON(at, bytes) ASAN_UNPOISON_MEMORY_REGION((at), (bytes))
#else
#define POISON(at, bytes) ((void)(at), (void)(bytes))
#define UNPOISON(at, bytes) ((void)(at), (void)(bytes))
#endif

typedef enum PageState {
    PAGE_FREE,
    PAGE_SMALL,
    PAGE_LARGE,
    PAGE_LARGE_TAIL,
} PageState;

// A page of a free run has its length on the run's first and last page, which
// is what lets a run being freed find the runs either side of it. The first
// page also says when the run was freed, as the heap's epoch, and whether its
// memory has been returned to the system since. A small page
// keeps its own free list; a block freed into it by a thread other than the
// one allocating from it goes onto remoteFree instead, for the owner to take.
struct ZamHeapPage {
    uint8_t state;
    uint8_t sizeClass;
    bool listed;
    uint32_t used;
    uint32_t carved;
    uint64_t runPages;
    uint64_t freedAt;
    bool returned;
    const ZamHeapCache *owner;
    void *freeList;
    _Atomic(void *) remoteFree;
    uint64_t next;
    uint64_t prev;
};

static const uint32_t classBytes[ZAMHEAP_CLASS_COUNT] = {
    16,    32,    48,    64,    80,    96,    112,   128,   160,   192,
    224,   256,   320,   384,   448,   512,   640,   768,   896,   1024,
    1280,  1536,  1792,  2048,  2560,  3072,  3584,  4096,  5120,  6144,
    7168,  8192,  10240, 12288, 14336, 16384, 20480, 24576, 28672, 32768,
};

_Static_assert(ZAMHEAP_SMALL_MAX_BYTES == 32768,
               "the last size class must be the largest small block");

static unsigned classOf(uint64_t byteCount) {
    if (byteCount <= 128)
        return (unsigned)((byteCount + 15) / 16) - 1;
    unsigned c = 8;
    while (classBytes[c] < byteCount)
        c++;
    return c;
}

static void poisonFree(void *block, uint64_t bytes) {
#ifdef ZAMHEAP_ASAN
    if (__asan_address_is_poisoned((uint8_t *)block + sizeof(void *)))
        zam_die(ZAMEXIT_PANIC, "heap block %p freed twice\n", block);
#endif
    POISON((uint8_t *)block + sizeof(void *), bytes - sizeof(void *));
}

static uint64_t slotsOf(unsigned sizeClass) {
    return ZAMHEAP_PAGE_BYTES / classBytes[sizeClass];
}

// OBJECT_MAP_BYTES is a page's share of the object map: a bit for each slot of
// the smallest class, a large block taking the first.
#define OBJECT_MAP_BYTES (ZAMHEAP_PAGE_BYTES / 16 / 8)

#if defined(_WIN32)
// RESERVATIONS_MAX is how many reservations the process may hold at once:
// four for each heap.
#define RESERVATIONS_MAX 64

// reservations are the address ranges reserve has handed out, which
// commitOnTouch backs with memory as they are first touched. A slot is
// claimed by its start and then filled; one emptied by release is not reused.
static struct {
    _Atomic(uintptr_t) start;
    _Atomic(uintptr_t) end;
} reservations[RESERVATIONS_MAX];
static _Atomic(unsigned) reservationCount;

// commitOnTouch commits the page an access faulted on when it lies in a
// reservation, and resumes the access, so that reserved memory is backed on
// first touch as POSIX's MAP_NORESERVE backs it. Any other fault is left to
// the handlers after it.
static LONG CALLBACK commitOnTouch(EXCEPTION_POINTERS *exception) {
    const EXCEPTION_RECORD *record = exception->ExceptionRecord;
    if (record->ExceptionCode != EXCEPTION_ACCESS_VIOLATION || record->NumberParameters < 2)
        return EXCEPTION_CONTINUE_SEARCH;
    uintptr_t at = (uintptr_t)record->ExceptionInformation[1];
    unsigned count = atomic_load(&reservationCount);
    for (unsigned i = 0; i < count && i < RESERVATIONS_MAX; i++) {
        if (at >= atomic_load(&reservations[i].start) && at < atomic_load(&reservations[i].end)) {
            if (VirtualAlloc((void *)at, 1, MEM_COMMIT, PAGE_READWRITE) != NULL)
                return EXCEPTION_CONTINUE_EXECUTION;
            return EXCEPTION_CONTINUE_SEARCH;
        }
    }
    return EXCEPTION_CONTINUE_SEARCH;
}

static void *reserve(uint64_t byteCount) {
    static _Atomic(bool) handling;
    if (!atomic_exchange(&handling, true))
        AddVectoredExceptionHandler(1, commitOnTouch);
    void *at = VirtualAlloc(NULL, byteCount, MEM_RESERVE, PAGE_READWRITE);
    unsigned slot = atomic_fetch_add(&reservationCount, 1);
    if (at == NULL || slot >= RESERVATIONS_MAX)
        zam_die(ZAMEXIT_OOM,
                "could not reserve %llu bytes of address space for the heap\n",
                (unsigned long long)byteCount);
    atomic_store(&reservations[slot].end, (uintptr_t)at + byteCount);
    atomic_store(&reservations[slot].start, (uintptr_t)at);
    return at;
}

static void release(void *at, uint64_t byteCount) {
    (void)byteCount;
    unsigned count = atomic_load(&reservationCount);
    for (unsigned i = 0; i < count && i < RESERVATIONS_MAX; i++) {
        if (atomic_load(&reservations[i].start) == (uintptr_t)at) {
            atomic_store(&reservations[i].start, 0);
            atomic_store(&reservations[i].end, 0);
        }
    }
    VirtualFree(at, 0, MEM_RELEASE);
}
#else
static void *reserve(uint64_t byteCount) {
    void *at = mmap(NULL, byteCount, PROT_READ | PROT_WRITE,
                    MAP_PRIVATE | MAP_ANON | MAP_NORESERVE, -1, 0);
    if (at == MAP_FAILED)
        zam_die(ZAMEXIT_OOM,
                "could not reserve %llu bytes of address space for the heap\n",
                (unsigned long long)byteCount);
    return at;
}

static void release(void *at, uint64_t byteCount) { munmap(at, byteCount); }
#endif

void zam_heap_init(ZamHeap *heap, uint64_t byteCount, uint64_t returnAge) {
    *heap = (ZamHeap){0};
    heap->returnAge = returnAge;
    if (byteCount < ZAMHEAP_PAGE_BYTES)
        byteCount = ZAMHEAP_PAGE_BYTES;
    heap->pageCount = (byteCount + ZAMHEAP_PAGE_BYTES - 1) / ZAMHEAP_PAGE_BYTES;
    heap->byteCount = heap->pageCount * ZAMHEAP_PAGE_BYTES;

    // Over-reserved by a page so the base can be rounded up to one, which is
    // what makes a block's page its address divided by the page size.
    uint8_t *raw = reserve(heap->byteCount + ZAMHEAP_PAGE_BYTES);
    uint8_t *base = (uint8_t *)(((uintptr_t)raw + ZAMHEAP_PAGE_BYTES - 1) &
                                ~(uintptr_t)(ZAMHEAP_PAGE_BYTES - 1));
    heap->reservation = raw;
    heap->base = base;
    heap->cards = reserve(heap->byteCount >> ZAMHEAP_CARD_SHIFT);
    heap->pages = reserve(heap->pageCount * sizeof *heap->pages);
    heap->objects = reserve(heap->pageCount * OBJECT_MAP_BYTES);
    heap->allocated = reserve(heap->pageCount * OBJECT_MAP_BYTES);
    pthread_mutex_init(&heap->lock, NULL);
}

void zam_heap_destroy(ZamHeap *heap) {
    if (heap->reservation == NULL)
        return;
    release(heap->reservation, heap->byteCount + ZAMHEAP_PAGE_BYTES);
    release(heap->cards, heap->byteCount >> ZAMHEAP_CARD_SHIFT);
    release(heap->pages, heap->pageCount * sizeof *heap->pages);
    release(heap->objects, heap->pageCount * OBJECT_MAP_BYTES);
    release(heap->allocated, heap->pageCount * OBJECT_MAP_BYTES);
    pthread_mutex_destroy(&heap->lock);
    *heap = (ZamHeap){0};
}

static uint64_t pageIndexOf(const ZamHeap *heap, const void *at) {
    return ((uintptr_t)at - (uintptr_t)heap->base) / ZAMHEAP_PAGE_BYTES;
}

static uint8_t *pageAddress(const ZamHeap *heap, uint64_t index) {
    return heap->base + index * ZAMHEAP_PAGE_BYTES;
}

// objectBit is the byte of the object map a block's bit lies in, and the bit.
static uint8_t *objectBit(const ZamHeap *heap, const void *block, uint8_t *bit) {
    uint64_t index = pageIndexOf(heap, block);
    const ZamHeapPage *page = &heap->pages[index];
    uint64_t slot = page->state == PAGE_LARGE
                        ? 0
                        : ((uintptr_t)block - (uintptr_t)pageAddress(heap, index)) /
                              classBytes[page->sizeClass];
    *bit = (uint8_t)(1u << (slot % 8));
    return heap->objects + index * OBJECT_MAP_BYTES + slot / 8;
}

static void forgetObject(const ZamHeap *heap, const void *block) {
    uint8_t bit;
    uint8_t *byte = objectBit(heap, block, &bit);
    __atomic_fetch_and(byte, (uint8_t)~bit, __ATOMIC_RELAXED);
    __atomic_fetch_and(heap->allocated + (byte - heap->objects), (uint8_t)~bit, __ATOMIC_RELAXED);
}

// handOut records block as handed out.
static void *handOut(const ZamHeap *heap, void *block) {
    uint8_t bit;
    uint8_t *byte = objectBit(heap, block, &bit);
    __atomic_fetch_or(heap->allocated + (byte - heap->objects), bit, __ATOMIC_RELAXED);
    return block;
}

bool zam_heap_take_allocated(ZamHeap *heap, const void *block) {
    uint8_t bit;
    uint8_t *byte = objectBit(heap, block, &bit);
    return (__atomic_fetch_and(heap->allocated + (byte - heap->objects), (uint8_t)~bit, __ATOMIC_RELAXED) & bit) != 0;
}

static void listPush(ZamHeap *heap, uint64_t *head, uint64_t index) {
    ZamHeapPage *page = &heap->pages[index];
    page->prev = 0;
    page->next = *head;
    if (*head != 0)
        heap->pages[*head - 1].prev = index + 1;
    *head = index + 1;
}

static void listRemove(ZamHeap *heap, uint64_t *head, uint64_t index) {
    ZamHeapPage *page = &heap->pages[index];
    if (page->prev != 0)
        heap->pages[page->prev - 1].next = page->next;
    else
        *head = page->next;
    if (page->next != 0)
        heap->pages[page->next - 1].prev = page->prev;
    page->next = page->prev = 0;
}

// returnPages gives a run's memory back to the system, keeping the address
// range. What it held may read as anything afterwards, which is no more than
// the allocator ever promises of a fresh block.
static void returnPages(uint8_t *at, uint64_t byteCount) {
#if defined(_WIN32)
    // Decommitted, the range is backed again on its next touch.
    VirtualFree(at, byteCount, MEM_DECOMMIT);
#elif defined(MADV_FREE_REUSABLE)
    madvise(at, byteCount, MADV_FREE_REUSABLE);
#elif defined(MADV_FREE)
    madvise(at, byteCount, MADV_FREE);
#else
    madvise(at, byteCount, MADV_DONTNEED);
#endif
}

// reusePages takes back memory returnPages gave away, which only the system
// whose accounting needs telling has a word for.
static void reusePages(uint8_t *at, uint64_t byteCount) {
#if defined(MADV_FREE_REUSE)
    madvise(at, byteCount, MADV_FREE_REUSE);
#else
    (void)at;
    (void)byteCount;
#endif
}

static void runInsert(ZamHeap *heap, uint64_t first, uint64_t count,
                      uint64_t freedAt, bool returned) {
    for (uint64_t index = first; index < first + count; index++)
        heap->pages[index] = (ZamHeapPage){.state = PAGE_FREE};
    heap->pages[first].runPages = count;
    heap->pages[first].freedAt = freedAt;
    heap->pages[first].returned = returned;
    heap->pages[first + count - 1].runPages = count;
    listPush(heap, &heap->freeRuns, first);
}

// releaseRun frees a run of pages, joining it with a free run either side.
// The run is freed now and its memory still held, whatever its neighbours had
// been. The caller holds the lock.
static void releaseRun(ZamHeap *heap, uint64_t first, uint64_t count) {
    if (first > 0 && heap->pages[first - 1].state == PAGE_FREE) {
        uint64_t before = first - heap->pages[first - 1].runPages;
        uint64_t beforeCount = heap->pages[before].runPages;
        listRemove(heap, &heap->freeRuns, before);
        first = before;
        count += beforeCount;
    }
    uint64_t after = first + count;
    if (after < heap->frontier && heap->pages[after].state == PAGE_FREE) {
        uint64_t afterCount = heap->pages[after].runPages;
        listRemove(heap, &heap->freeRuns, after);
        count += afterCount;
    }
    runInsert(heap, first, count, heap->epoch, false);
}

// commitPages backs a run with memory before it is handed out. Windows leaves
// reserved memory unbacked until it is committed, and the system itself, a
// read into a block, faults rather than commits; elsewhere a reservation is
// backed as it is touched.
static void commitPages(uint8_t *at, uint64_t byteCount) {
#if defined(_WIN32)
    if (VirtualAlloc(at, byteCount, MEM_COMMIT, PAGE_READWRITE) == NULL)
        zam_die(ZAMEXIT_OOM, "could not commit %llu bytes of the heap\n",
                (unsigned long long)byteCount);
#else
    (void)at;
    (void)byteCount;
#endif
}

// takeRun hands out count contiguous pages, from the first free run long
// enough or else from past the frontier. The caller holds the lock.
static uint64_t takeRun(ZamHeap *heap, uint64_t count) {
    for (uint64_t entry = heap->freeRuns; entry != 0;
         entry = heap->pages[entry - 1].next) {
        uint64_t first = entry - 1;
        uint64_t length = heap->pages[first].runPages;
        if (length < count)
            continue;
        uint64_t freedAt = heap->pages[first].freedAt;
        bool returned = heap->pages[first].returned;
        listRemove(heap, &heap->freeRuns, first);
        if (length > count)
            runInsert(heap, first + count, length - count, freedAt, returned);
        if (returned)
            reusePages(pageAddress(heap, first), count * ZAMHEAP_PAGE_BYTES);
        commitPages(pageAddress(heap, first), count * ZAMHEAP_PAGE_BYTES);
        return first;
    }

    if (count > heap->pageCount - heap->frontier)
        zam_die(ZAMEXIT_OOM,
                "the heap is out of its %llu bytes; raise the maximum heap "
                "size\n",
                (unsigned long long)heap->byteCount);
    uint64_t first = heap->frontier;
    heap->frontier += count;
    commitPages(pageAddress(heap, first), count * ZAMHEAP_PAGE_BYTES);
    return first;
}

// drainRemote takes back what other threads freed into a page.
static void drainRemote(ZamHeapPage *page) {
    void *remote = atomic_exchange(&page->remoteFree, NULL);
    while (remote != NULL) {
        void *next = *(void **)remote;
        *(void **)remote = page->freeList;
        page->freeList = remote;
        page->used--;
        remote = next;
    }
}

static void *takeSlot(ZamHeap *heap, uint64_t index) {
    ZamHeapPage *page = &heap->pages[index];
    if (page->freeList == NULL && page->carved == slotsOf(page->sizeClass))
        drainRemote(page);
    if (page->freeList != NULL) {
        void *block = page->freeList;
        page->freeList = *(void **)block;
        page->used++;
        UNPOISON(block, classBytes[page->sizeClass]);
        return block;
    }
    if (page->carved < slotsOf(page->sizeClass)) {
        void *block = pageAddress(heap, index) +
                      (uint64_t)page->carved * classBytes[page->sizeClass];
        page->carved++;
        page->used++;
        // A page freed whole and handed out again may still be poisoned
        // from the blocks it held before.
        UNPOISON(block, classBytes[page->sizeClass]);
        return block;
    }
    return NULL;
}

// settle puts a page no cache owns where its occupancy says it belongs: freed
// when empty, on its class's list when it has room. The caller holds the lock.
static void settle(ZamHeap *heap, uint64_t index) {
    ZamHeapPage *page = &heap->pages[index];
    uint64_t *list = &heap->available[page->sizeClass];
    if (page->used == 0) {
        if (page->listed)
            listRemove(heap, list, index);
        releaseRun(heap, index, 1);
        return;
    }
    if (!page->listed && page->used < slotsOf(page->sizeClass)) {
        listPush(heap, list, index);
        page->listed = true;
    }
}

// retire gives a page back from a cache. The caller holds the lock, which is
// what keeps a thread freeing into the page from seeing it owned after its
// last remote push was drained.
static void retire(ZamHeap *heap, uint64_t index) {
    ZamHeapPage *page = &heap->pages[index];
    drainRemote(page);
    page->owner = NULL;
    settle(heap, index);
}

static uint64_t acquire(ZamHeap *heap, const ZamHeapCache *cache,
                        unsigned sizeClass) {
    uint64_t *list = &heap->available[sizeClass];
    if (*list != 0) {
        uint64_t index = *list - 1;
        listRemove(heap, list, index);
        heap->pages[index].listed = false;
        heap->pages[index].owner = cache;
        return index;
    }

    uint64_t index = takeRun(heap, 1);
    heap->pages[index] = (ZamHeapPage){
        .state = PAGE_SMALL,
        .sizeClass = (uint8_t)sizeClass,
        .owner = cache,
    };
    return index;
}

static void *allocLarge(ZamHeap *heap, uint64_t byteCount) {
    uint64_t count = (byteCount + ZAMHEAP_PAGE_BYTES - 1) / ZAMHEAP_PAGE_BYTES;
    pthread_mutex_lock(&heap->lock);
    uint64_t first = takeRun(heap, count);
    heap->pages[first] = (ZamHeapPage){.state = PAGE_LARGE, .runPages = count};
    for (uint64_t index = first + 1; index < first + count; index++)
        heap->pages[index] = (ZamHeapPage){.state = PAGE_LARGE_TAIL,
                                           .runPages = index - first};
    pthread_mutex_unlock(&heap->lock);
    UNPOISON(pageAddress(heap, first), count * ZAMHEAP_PAGE_BYTES);
    return pageAddress(heap, first);
}

void *zam_heap_alloc(ZamHeap *heap, ZamHeapCache *cache, uint64_t byteCount) {
    if (byteCount > ZAMHEAP_SMALL_MAX_BYTES)
        return handOut(heap, allocLarge(heap, byteCount));

    unsigned sizeClass = classOf(byteCount == 0 ? 1 : byteCount);
    for (;;) {
        uint64_t entry = cache->pages[sizeClass];
        if (entry != 0) {
            void *block = takeSlot(heap, entry - 1);
            if (block != NULL)
                return handOut(heap, block);
        }

        pthread_mutex_lock(&heap->lock);
        if (entry != 0)
            retire(heap, entry - 1);
        cache->pages[sizeClass] = acquire(heap, cache, sizeClass) + 1;
        pthread_mutex_unlock(&heap->lock);
    }
}

void zam_heap_free(ZamHeap *heap, ZamHeapCache *cache, void *block) {
    uint64_t index = pageIndexOf(heap, block);
    ZamHeapPage *page = &heap->pages[index];

    forgetObject(heap, block);
    if (page->state == PAGE_LARGE) {
        pthread_mutex_lock(&heap->lock);
        releaseRun(heap, index, page->runPages);
        pthread_mutex_unlock(&heap->lock);
        return;
    }

    poisonFree(block, classBytes[page->sizeClass]);
    if (cache != NULL && page->owner == cache) {
        *(void **)block = page->freeList;
        page->freeList = block;
        page->used--;
        return;
    }

    pthread_mutex_lock(&heap->lock);
    if (page->owner != NULL) {
        void *head = atomic_load(&page->remoteFree);
        do
            *(void **)block = head;
        while (!atomic_compare_exchange_weak(&page->remoteFree, &head, block));
    } else {
        *(void **)block = page->freeList;
        page->freeList = block;
        page->used--;
        settle(heap, index);
    }
    pthread_mutex_unlock(&heap->lock);
}

void zam_heap_free_stopped(ZamHeap *heap, void *block) {
    uint64_t index = pageIndexOf(heap, block);
    ZamHeapPage *page = &heap->pages[index];
    forgetObject(heap, block);
    if (page->state == PAGE_LARGE) {
        releaseRun(heap, index, page->runPages);
        return;
    }

    poisonFree(block, classBytes[page->sizeClass]);
    *(void **)block = page->freeList;
    page->freeList = block;
    page->used--;
    if (page->owner == NULL)
        settle(heap, index);
}

void zam_heap_set_object(ZamHeap *heap, const void *block) {
    uint8_t bit;
    uint8_t *byte = objectBit(heap, block, &bit);
    __atomic_fetch_or(byte, bit, __ATOMIC_RELEASE);
}

// blockOf is the block an address lies in by what the page table says now,
// which a page being handed out at the same moment may make nonsense of; the
// bit is what says whether to believe it. The block's bit is answered through
// byte and bit.
static uint8_t *blockOf(const ZamHeap *heap, const uint8_t *at, uint8_t **byte,
                        uint8_t *bit) {
    uint64_t index = pageIndexOf(heap, at);
    const ZamHeapPage *page = &heap->pages[index];
    uint8_t state = __atomic_load_n(&page->state, __ATOMIC_RELAXED);
    if (state == PAGE_LARGE_TAIL) {
        uint64_t back = __atomic_load_n(&page->runPages, __ATOMIC_RELAXED);
        if (back == 0 || back > index)
            return NULL;
        index -= back;
        page = &heap->pages[index];
        state = __atomic_load_n(&page->state, __ATOMIC_RELAXED);
        if (state != PAGE_LARGE)
            return NULL;
    }

    uint64_t slot = 0;
    uint8_t *block = pageAddress(heap, index);
    if (state == PAGE_SMALL) {
        uint8_t sizeClass = __atomic_load_n(&page->sizeClass, __ATOMIC_RELAXED);
        if (sizeClass >= ZAMHEAP_CLASS_COUNT)
            return NULL;
        slot = (uint64_t)(at - block) / classBytes[sizeClass];
        if (slot >= slotsOf(sizeClass))
            return NULL;
        block += slot * classBytes[sizeClass];
    } else if (state != PAGE_LARGE)
        return NULL;
    *byte = heap->objects + index * OBJECT_MAP_BYTES + slot / 8;
    *bit = (uint8_t)(1u << (slot % 8));
    return block;
}

void *zam_heap_object_of(const ZamHeap *heap, const void *at) {
    if (!zam_heap_contains(heap, at))
        return NULL;
    uint8_t *byte;
    uint8_t bit;
    uint8_t *block = blockOf(heap, at, &byte, &bit);
    if (block == NULL || (__atomic_load_n(byte, __ATOMIC_ACQUIRE) & bit) == 0)
        return NULL;
    // Read again now that the bit has said the page was set up: a page holding
    // an object is not handed out anew while a collection may look at it.
    uint8_t *again = blockOf(heap, at, &byte, &bit);
    return again == block ? block : NULL;
}

void *zam_heap_block_of(const ZamHeap *heap, const void *at) {
    uint8_t *byte;
    uint8_t bit;
    return blockOf(heap, at, &byte, &bit);
}

uint64_t zam_heap_size(const ZamHeap *heap, const void *block) {
    const ZamHeapPage *page = &heap->pages[pageIndexOf(heap, block)];
    if (page->state == PAGE_LARGE)
        return page->runPages * ZAMHEAP_PAGE_BYTES;
    return classBytes[page->sizeClass];
}

void zam_heap_release_cache(ZamHeap *heap, ZamHeapCache *cache) {
    pthread_mutex_lock(&heap->lock);
    for (unsigned sizeClass = 0; sizeClass < ZAMHEAP_CLASS_COUNT; sizeClass++) {
        if (cache->pages[sizeClass] != 0)
            retire(heap, cache->pages[sizeClass] - 1);
        cache->pages[sizeClass] = 0;
    }
    pthread_mutex_unlock(&heap->lock);
}

uint64_t zam_heap_scavenge(ZamHeap *heap) {
    pthread_mutex_lock(&heap->lock);
    heap->epoch++;
    uint64_t returned = 0;
    for (uint64_t entry = heap->freeRuns; entry != 0;
         entry = heap->pages[entry - 1].next) {
        ZamHeapPage *page = &heap->pages[entry - 1];
        if (page->returned || heap->epoch - page->freedAt < heap->returnAge)
            continue;
        uint64_t bytes = page->runPages * ZAMHEAP_PAGE_BYTES;
        returnPages(pageAddress(heap, entry - 1), bytes);
        page->returned = true;
        returned += bytes;
    }
    pthread_mutex_unlock(&heap->lock);
    return returned;
}

static uint64_t cardCount(const ZamHeap *heap) {
    return heap->frontier * (ZAMHEAP_PAGE_BYTES >> ZAMHEAP_CARD_SHIFT);
}

static void visitDirtyCards(ZamHeap *heap,
                            void (*visit)(void *context, const uint8_t *start,
                                          const uint8_t *end),
                            void *context, bool clean) {
    uint64_t count = cardCount(heap);
    for (uint64_t card = 0; card < count;) {
        // A word of clean cards is skipped whole, the table being mostly clean.
        if (card % sizeof(uint64_t) == 0 && card + sizeof(uint64_t) <= count) {
            uint64_t word;
            memcpy(&word, heap->cards + card, sizeof word);
            if (word == 0) {
                card += sizeof word;
                continue;
            }
        }
        if (heap->cards[card] == 0) {
            card++;
            continue;
        }
        uint64_t first = card;
        while (card < count && heap->cards[card] != 0) {
            if (clean)
                heap->cards[card] = 0;
            card++;
        }

        // A run is visited a page at a time, and a free page not at all:
        // nothing on it is anything's to hold.
        uint64_t perPage = ZAMHEAP_PAGE_BYTES >> ZAMHEAP_CARD_SHIFT;
        for (uint64_t at = first; at < card;) {
            uint64_t page = at / perPage;
            uint64_t end = (page + 1) * perPage < card ? (page + 1) * perPage : card;
            if (heap->pages[page].state != PAGE_FREE)
                visit(context, heap->base + (at << ZAMHEAP_CARD_SHIFT),
                      heap->base + (end << ZAMHEAP_CARD_SHIFT));
            at = end;
        }
    }
}

void zam_heap_dirty_cards(ZamHeap *heap,
                          void (*visit)(void *context, const uint8_t *start,
                                        const uint8_t *end),
                          void *context) {
    visitDirtyCards(heap, visit, context, true);
}

void zam_heap_scan_cards(ZamHeap *heap,
                         void (*visit)(void *context, const uint8_t *start,
                                       const uint8_t *end),
                         void *context) {
    visitDirtyCards(heap, visit, context, false);
}

// It reads the page table as the program changes it, which a sanitized build
// is told is deliberate.
#if defined(__has_feature)
#if __has_feature(thread_sanitizer)
__attribute__((no_sanitize("thread")))
#endif
#endif
uint64_t zam_heap_preclean_cards(ZamHeap *heap,
                                 void (*visit)(void *context,
                                               const uint8_t *start,
                                               const uint8_t *end),
                                 void *context) {
    uint64_t perPage = ZAMHEAP_PAGE_BYTES >> ZAMHEAP_CARD_SHIFT;
    uint64_t count = *(const volatile uint64_t *)&heap->frontier * perPage;
    uint64_t cleaned = 0;
    for (uint64_t card = 0; card < count;) {
        if (card % sizeof(uint64_t) == 0 && card + sizeof(uint64_t) <= count &&
            __atomic_load_n((const uint64_t *)(heap->cards + card),
                            __ATOMIC_RELAXED) == 0) {
            card += sizeof(uint64_t);
            continue;
        }
        if (__atomic_load_n(&heap->cards[card], __ATOMIC_RELAXED) != 0 &&
            __atomic_exchange_n(&heap->cards[card], 0, __ATOMIC_ACQUIRE) != 0) {
            cleaned++;
            if (*(const volatile uint8_t *)&heap->pages[card / perPage].state !=
                PAGE_FREE)
                visit(context, heap->base + (card << ZAMHEAP_CARD_SHIFT),
                      heap->base + ((card + 1) << ZAMHEAP_CARD_SHIFT));
        }
        card++;
    }
    return cleaned;
}

void zam_heap_clear_cards(ZamHeap *heap) {
    memset(heap->cards, 0, cardCount(heap));
}
