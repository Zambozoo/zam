#include "thread.h"

#include "vm.h"

// currentZamThread records which ZamThread the OS thread is driving. The C
// signal handler reads this to find the per-thread pending mask it should
// OR into; the mask lives on the ZamThread so each line of execution
// dispatches only the handlers it registered.
//
// Set at the top of a thread's run via zam_thread_bind_current. A thread
// that has not yet bound sees a NULL here, in which case the signal is
// dropped — nothing has claimed it, and reaching through NULL would be a
// crash before any program observed anything.
static __thread ZamThread *currentZamThread;

static void zamSignalHandler(int sig) {
   ZamThread *thread = currentZamThread;
    if (thread == NULL)
        return;
    if (sig < 0 || sig >= ZAM_SIGNAL_MAX)
        return;
    thread->pendingSignalMask |= (sig_atomic_t)1u << sig;
}

void zam_thread_bind_current(ZamThread *thread) { currentZamThread = thread; }

void zam_thread_install_signal_slot(ZamThread *thread, int sig, void *closure) {
    if (sig < 0 || sig >= ZAM_SIGNAL_MAX)
        return;
    thread->signalHandlers[sig] = closure;

    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = zamSignalHandler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_RESTART;
    (void)sigaction(sig, &sa, NULL);
}

int zam_thread_take_pending_signal(ZamThread *thread) {
    sig_atomic_t mask = thread->pendingSignalMask;
    if (mask == 0)
        return -1;
    for (int sig = 0; sig < ZAM_SIGNAL_MAX; sig++) {
        if (mask & ((sig_atomic_t)1u << sig)) {
            thread->pendingSignalMask &= ~((sig_atomic_t)1u << sig);
            return sig;
        }
    }
    return -1;
}

static const char *const resultNames[] = {
    "CONTINUE",
    "EXIT",
    "RETURNED",
    "PANIC",
};

const char *zam_result_name(ZamResult result) {
    return result <= ZAMRESULT_PANIC ? resultNames[result] : "<unknown result>";
}

// The header is left alone. Main lives embedded in the ZamVM and has no header
// to care about; a spawned thread lives in an allocation whose first word is
// the mark word the collector finds it through, written by the allocator
// before this is called. Every other field is set here, with the ones not
// named relying on their zero value — the thread is zero when it reaches here
// in both cases, main because the whole VM was, and a spawned one because the
// allocator zeroes.
void zam_thread_init(ZamThread *thread, ZamVM *vm, uint64_t initialStackSize) {
    thread->vm = vm;
    thread->result = ZAMRESULT_CONTINUE;
    thread->operandCapacity = initialStackSize;
    thread->markByteCapacity = initialStackSize * ZAMWORD_BYTES;
    thread->functionEntry = ZAMMARK_NONE;
    thread->scopeEntry = ZAMMARK_NONE;
    thread->packEntry = ZAMMARK_NONE;
    thread->operands = zam_alloc(thread->operandCapacity * sizeof(ZamWord));
    thread->marks = zam_alloc(thread->markByteCapacity);
}

void zam_thread_free(ZamThread *thread) {
    zam_heap_release_cache(&thread->vm->heap, &thread->heapCache);
    free(thread->operands);
    free(thread->marks);
    free(thread->dirtied);
    free(thread->held);
    free(thread->gray);

    // Every set the thread ever opened, not only the ones still open: a popped
    // set keeps its slots for the next comparison at that depth, so the
    // allocation outlives the comparison that asked for it.
    for (uint64_t at = 0; at < thread->visitedCapacity; at++)
        free(thread->visited[at].entries);
    free(thread->visited);

    // The struct itself is left as it was. A spawned thread lives in a heap
    // allocation that the VM's sweep is still to free, and the sweep walks the
    // allocation chain through the mark word — zeroing would snap that chain
    // at this block and strand everything behind it. Main is embedded in the
    // VM and so has no such chain to protect, but one of these two shapes is
    // the shared one.
}

// The stacks grow rather than being bounded, because how deep a program goes is
// not something the compiler wrote down: the initial size in the image is a
// starting point the program asked for and not a limit it agreed to.
#define ZAMDEFINE_GROW(name, field, countField, capacityField, elementType)    \
    static void name(ZamThread *thread, uint64_t needed) {                     \
        if (thread->countField + needed <= thread->capacityField)              \
            return;                                                            \
        while (thread->capacityField < thread->countField + needed)            \
            thread->capacityField *= 2;                                        \
        thread->field = zam_realloc(                                           \
            thread->field, thread->capacityField * sizeof(elementType));       \
    }

ZAMDEFINE_GROW(growOperands, operands, operandCount, operandCapacity, ZamWord)
ZAMDEFINE_GROW(growMarks, marks, markByteCount, markByteCapacity, uint8_t)

void zam_thread_push(ZamThread *thread, ZamWord word) {
    growOperands(thread, 1);
    thread->operands[thread->operandCount++] = word;
}

ZamWord zam_thread_pop(ZamThread *thread) {
    if (thread->operandCount == 0)
        zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
    return thread->operands[--thread->operandCount];
}

ZamWord zam_thread_peek(const ZamThread *thread, uint64_t depth) {
    if (depth >= thread->operandCount)
        zam_die(ZAMEXIT_PANIC, "operand stack underflow\n");
    return thread->operands[thread->operandCount - 1 - depth];
}

// A type id is written low word first, so pushing it leaves the packed short id
// deepest and the tail length on top. Popping reads them back in the order that
// undoes it.
void zam_thread_push_type(ZamThread *thread, ZamTypeID type) {
    growOperands(thread, ZAMTYPE_ID_WORDS);
    zam_thread_push(thread, (ZamWord)type.shortID << 32);
    zam_thread_push(thread, type.arrayLength);
    zam_thread_push(thread, type.tailLength);
}

ZamTypeID zam_thread_pop_type(ZamThread *thread) {
   ZamTypeID type;
    type.tailLength = zam_thread_pop(thread);
    type.arrayLength = zam_thread_pop(thread);
    type.shortID = (ZamTypeShort)(zam_thread_pop(thread) >> 32);
    return type;
}

ZamTypeID zam_thread_peek_type(const ZamThread *thread, uint64_t depth) {
    return (ZamTypeID){
        .tailLength = zam_thread_peek(thread, depth),
        .arrayLength = zam_thread_peek(thread, depth + 1),
        .shortID = (ZamTypeShort)(zam_thread_peek(thread, depth + 2) >> 32),
    };
}

static const uint64_t markEntryBytes[] = {
    [ZAMMARK_FUNCTION] = sizeof(ZamMarkFunction),
    [ZAMMARK_OBJECT] = sizeof(ZamMarkObject),
    [ZAMMARK_ARRAY] = sizeof(ZamMarkArray),
    [ZAMMARK_SCOPE] = sizeof(ZamMarkScope),
    [ZAMMARK_VALUE] = sizeof(ZamMarkValue),
    [ZAMMARK_INTERIOR] = sizeof(ZamMarkInterior),
    [ZAMMARK_PACK] = sizeof(ZamMarkPack),
};

uint64_t zam_mark_entry_bytes(ZamMarkKind kind) {
    if (kind > ZAMMARK_PACK)
        zam_die(ZAMEXIT_PANIC, "mark stack holds no kind %d\n", (int)kind);
    return markEntryBytes[kind];
}

ZamMarkKind zam_mark_kind_at(const ZamThread *thread, uint64_t offset) {
    if (offset >= thread->markByteCount)
        zam_die(ZAMEXIT_PANIC, "mark offset %llu is past the stack\n",
                (unsigned long long)offset);
    return (ZamMarkKind)thread->marks[offset];
}

// An entry is copied in and out whole. Every width is a multiple of a word and
// the buffer begins aligned, so nothing here is a misaligned access; memcpy is
// used rather than a cast because it says that without relying on it.
static uint64_t pushEntry(ZamThread *thread, const void *entry,
                          uint64_t byteCount) {
    uint64_t offset = thread->markByteCount;
    growMarks(thread, byteCount);
    memcpy(thread->marks + offset, entry, byteCount);
    thread->markByteCount = offset + byteCount;
    return offset;
}

static void readEntry(const ZamThread *thread, uint64_t offset, void *entry,
                      uint64_t byteCount) {
    if (offset + byteCount > thread->markByteCount)
        zam_die(ZAMEXIT_PANIC, "mark entry at %llu runs past the stack\n",
                (unsigned long long)offset);
    memcpy(entry, thread->marks + offset, byteCount);
}

void zam_thread_push_mark(ZamThread *thread, uint64_t operand) {
   ZamMarkObject entry = {.kind = ZAMMARK_OBJECT, .operand = operand};
    pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_push_array_mark(ZamThread *thread, ZamTypeID type,
                                uint64_t operand) {
    // The tail length goes no further than this: what is traversed is the run
    // of elements, which the element type and the count describe on their own.
   ZamMarkArray entry = {
        .kind = ZAMMARK_ARRAY,
        .operand = operand,
        .element = type.shortID,
        .length = type.arrayLength,
    };
    pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_push_indirect_array_mark(ZamThread *thread, ZamTypeID type,
                                         uint64_t operand) {
   ZamMarkArray entry = {
        .kind = ZAMMARK_ARRAY,
        .operand = operand,
        .element = type.shortID,
        .indirect = 1,
        .length = type.arrayLength,
    };
    pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_push_value_mark(ZamThread *thread, ZamTypeID type,
                                uint64_t operand) {
   ZamMarkValue entry = {.kind = ZAMMARK_VALUE, .operand = operand};
    zam_type_id_write((ZamWord *)&entry.type, type);
    pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_push_interior_mark(ZamThread *thread, uint64_t operand,
                                   uint64_t distance) {
   ZamMarkInterior entry = {
        .kind = ZAMMARK_INTERIOR,
        .operand = operand,
        .distance = distance,
    };
    pushEntry(thread, &entry, sizeof entry);
}

// Where a search for the topmost entry may begin. Everything below the innermost
// open scope or call belongs to an enclosing one, and an enclosing scope's
// entries are never the top while this one is open, so the walk starts at
// whichever of the two heads lies higher.
static uint64_t markSearchFloor(const ZamThread *thread) {
    uint64_t floor = 0;
    if (thread->functionEntry != ZAMMARK_NONE)
        floor = thread->functionEntry + sizeof(ZamMarkFunction);
    if (thread->scopeEntry != ZAMMARK_NONE &&
        thread->scopeEntry + sizeof(ZamMarkScope) > floor)
        floor = thread->scopeEntry + sizeof(ZamMarkScope);
    return floor;
}

// popMark takes the topmost entry off if it names the operand word, and
// answers its kind, or ZAMMARK_FUNCTION where it took nothing.
static ZamMarkKind popMark(ZamThread *thread, uint64_t operand,
                           uint64_t *distance) {
    uint64_t last = thread->markByteCount;
    for (uint64_t at = markSearchFloor(thread); at < thread->markByteCount;) {
        last = at;
        at += zam_mark_entry_bytes(zam_mark_kind_at(thread, at));
    }
    if (last == thread->markByteCount)
        return ZAMMARK_FUNCTION;

    // Every kind that names a word puts the operand offset in the same place,
    // so which one it is does not have to be told apart to ask whether it
    // names this word.
   ZamMarkKind kind = zam_mark_kind_at(thread, last);
    if (kind != ZAMMARK_OBJECT && kind != ZAMMARK_ARRAY &&
        kind != ZAMMARK_VALUE && kind != ZAMMARK_INTERIOR)
        return ZAMMARK_FUNCTION;

   ZamMarkObject entry;
    readEntry(thread, last, &entry, sizeof entry);
    if (entry.operand != operand)
        return ZAMMARK_FUNCTION;

    *distance = 0;
    if (kind == ZAMMARK_INTERIOR) {
       ZamMarkInterior interior;
        readEntry(thread, last, &interior, sizeof interior);
        *distance = interior.distance;
    }
    thread->markByteCount = last;
    return kind;
}

bool zam_thread_pop_mark(ZamThread *thread, uint64_t operand) {
    uint64_t distance;
    return popMark(thread, operand, &distance) != ZAMMARK_FUNCTION;
}

bool zam_thread_pop_object_mark(ZamThread *thread, uint64_t operand,
                                uint64_t *distance) {
   ZamMarkKind kind = popMark(thread, operand, distance);
    return kind == ZAMMARK_OBJECT || kind == ZAMMARK_INTERIOR;
}

void *zam_thread_location_address(const ZamThread *thread, ZamWord location) {
    if (!zam_is_frame_location(location))
        return (void *)(uintptr_t)location;

    uint64_t byteOffset = location & ~ZAMFRAME_LOCATION_BIT;
    if (byteOffset >= thread->operandCount * ZAMWORD_BYTES)
        zam_die(ZAMEXIT_PANIC,
                "frame location at byte %llu is past the %llu word stack\n",
                (unsigned long long)byteOffset,
                (unsigned long long)thread->operandCount);
    return (uint8_t *)thread->operands + byteOffset;
}

uint64_t zam_thread_frame_base(const ZamThread *thread) {
    if (thread->functionEntry == ZAMMARK_NONE)
        return 0;

   ZamMarkFunction entry;
    readEntry(thread, thread->functionEntry, &entry, sizeof entry);
    return entry.operandPop;
}

// heldBase is what a call made now records as the values held before it.
static uint64_t heldBase(const ZamThread *thread) {
    if (thread->heldCount >= ((uint64_t)1 << 31))
        zam_die(ZAMEXIT_PANIC, "held %llu values across one call\n",
                (unsigned long long)thread->heldCount);
    return thread->heldCount;
}

void zam_thread_hold(ZamThread *thread, ZamWord value) {
    if (thread->heldCount == thread->heldCapacity) {
        thread->heldCapacity =
            thread->heldCapacity == 0 ? 16 : thread->heldCapacity * 2;
        thread->held =
            zam_realloc(thread->held, thread->heldCapacity * sizeof *thread->held);
    }
    thread->held[thread->heldCount++] = value;
}

void zam_thread_release(ZamThread *thread) {
    uint64_t base = 0;
    if (thread->functionEntry != ZAMMARK_NONE) {
       ZamMarkFunction entry;
        readEntry(thread, thread->functionEntry, &entry, sizeof entry);
        base = entry.heldBase;
    }
    thread->heldCount = base;
}

void zam_thread_push_function(ZamThread *thread, uint64_t returnInstruction,
                              uint64_t operandPop) {
   ZamMarkFunction entry = {
        .kind = ZAMMARK_FUNCTION,
        .previous = thread->functionEntry,
        .returnInstruction = returnInstruction,
        .operandPop = operandPop,
        .heldBase = heldBase(thread),
    };
    thread->functionEntry = pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_push_direct_function(ZamThread *thread, uint64_t returnInstruction,
                                     uint64_t operandPop, ZamTypeShort receiverType) {
   ZamMarkFunction entry = {
        .kind = ZAMMARK_FUNCTION,
        .previous = thread->functionEntry,
        .returnInstruction = returnInstruction,
        .operandPop = operandPop,
        .receiverType = receiverType,
        .direct = 1,
        .heldBase = heldBase(thread),
    };
    thread->functionEntry = pushEntry(thread, &entry, sizeof entry);
}

bool zam_thread_frame_direct(const ZamThread *thread, ZamTypeID *receiverType) {
    if (thread->functionEntry == ZAMMARK_NONE)
        return false;
   ZamMarkFunction entry;
    readEntry(thread, thread->functionEntry, &entry, sizeof entry);
    if (!entry.direct)
        return false;
    *receiverType = (ZamTypeID){.shortID = (ZamTypeShort)entry.receiverType};
    return true;
}

// dropPacksAbove closes every pack opened above a mark offset that a rewind
// to it takes away.
static void dropPacksAbove(ZamThread *thread, uint64_t offset) {
    while (thread->packEntry != ZAMMARK_NONE && thread->packEntry > offset) {
       ZamMarkPack pack;
        readEntry(thread, thread->packEntry, &pack, sizeof pack);
        thread->packEntry = pack.previous;
    }
}

ZamMarkFunction zam_thread_pop_function(ZamThread *thread) {
    uint64_t offset = thread->functionEntry;
    if (offset == ZAMMARK_NONE)
        zam_die(ZAMEXIT_PANIC, "returned with no call open\n");

   ZamMarkFunction entry;
    readEntry(thread, offset, &entry, sizeof entry);

    // Every scope opened inside the call goes with it. The chain is walked
    // rather than saved on the entry because a scope entry can only be found
    // from the one after it, and the walk is bounded by the scopes this call
    // opened.
    while (thread->scopeEntry != ZAMMARK_NONE && thread->scopeEntry > offset) {
       ZamMarkScope scope;
        readEntry(thread, thread->scopeEntry, &scope, sizeof scope);
        thread->scopeEntry = scope.previous;
    }
    dropPacksAbove(thread, offset);

    thread->markByteCount = offset;
    thread->operandCount = entry.operandPop;
    thread->functionEntry = entry.previous;
    thread->heldCount = entry.heldBase;

    // The closure and the arguments were pushed by the caller, so their entries
    // lie beneath this call's and survive the rewind above — but the words are
    // the frame's and have just gone, which would leave the entries naming slots
    // the program has given up. They are dropped here, and here rather than at
    // the call because entries are pushed in the order their words are: the
    // first one naming a word at or above the frame is where the frame's begin,
    // and everything from there up went with it.
    for (uint64_t at = markSearchFloor(thread); at < thread->markByteCount;) {
       ZamMarkKind kind = zam_mark_kind_at(thread, at);
        if (kind == ZAMMARK_OBJECT || kind == ZAMMARK_ARRAY ||
            kind == ZAMMARK_VALUE || kind == ZAMMARK_INTERIOR) {
            // Every kind that names a word names it in the same place, so which
            // one it is does not have to be told apart to read the offset.
           ZamMarkObject named;
            readEntry(thread, at, &named, sizeof named);
            if (named.operand >= entry.operandPop) {
                thread->markByteCount = at;
                break;
            }
        }
        at += zam_mark_entry_bytes(kind);
    }
    return entry;
}

void zam_thread_push_scope(ZamThread *thread) {
   ZamMarkScope entry = {
        .kind = ZAMMARK_SCOPE,
        .previous = thread->scopeEntry,
        .frame = thread->operandCount,
    };
    thread->scopeEntry = pushEntry(thread, &entry, sizeof entry);
}

void zam_thread_pop_scope(ZamThread *thread) {
    uint64_t offset = thread->scopeEntry;
    if (offset == ZAMMARK_NONE)
        zam_die(ZAMEXIT_PANIC, "left a scope with none open\n");

   ZamMarkScope entry;
    readEntry(thread, offset, &entry, sizeof entry);
    thread->scopeEntry = entry.previous;
    dropPacksAbove(thread, offset);
    thread->operandCount = entry.frame;
    thread->markByteCount = offset;
}

void zam_thread_push_pack(ZamThread *thread) {
   ZamMarkPack entry = {
        .kind = ZAMMARK_PACK,
        .previous = thread->packEntry,
        .frame = thread->operandCount,
    };
    thread->packEntry = pushEntry(thread, &entry, sizeof entry);
}

static void reverseWords(ZamWord *words, uint64_t count) {
    for (uint64_t i = 0, j = count; i + 1 < j; i++, j--) {
       ZamWord word = words[i];
        words[i] = words[j - 1];
        words[j - 1] = word;
    }
}

static void reverseBytes(uint8_t *bytes, uint64_t count) {
    for (uint64_t i = 0, j = count; i + 1 < j; i++, j--) {
        uint8_t byte = bytes[i];
        bytes[i] = bytes[j - 1];
        bytes[j - 1] = byte;
    }
}

// packedEntryOperand reads the operand an entry above a pack names. Only
// entries naming a word lie there, since a pack opens and closes within one
// expression.
static uint64_t packedEntryOperand(const ZamThread *thread, uint64_t at) {
   ZamMarkKind kind = zam_mark_kind_at(thread, at);
    if (kind != ZAMMARK_OBJECT && kind != ZAMMARK_ARRAY &&
        kind != ZAMMARK_VALUE && kind != ZAMMARK_INTERIOR)
        zam_die(ZAMEXIT_PANIC, "a %u-kind entry inside a pack\n",
                (unsigned)kind);
   ZamMarkObject head;
    readEntry(thread, at, &head, sizeof head);
    return head.operand;
}

static void moveEntryOperand(ZamThread *thread, uint64_t at, uint64_t operand) {
   ZamMarkObject head;
    readEntry(thread, at, &head, sizeof head);
    head.operand = operand;
    memcpy(thread->marks + at, &head, sizeof head);
}

// packedEntriesFrom is the mark offset of the first entry above the innermost
// pack naming an operand at or above the given one, or the top of the stack.
static uint64_t packedEntriesFrom(const ZamThread *thread, uint64_t operand) {
    uint64_t at = thread->packEntry + sizeof(ZamMarkPack);
    for (; at < thread->markByteCount;
         at += zam_mark_entry_bytes(zam_mark_kind_at(thread, at)))
        if (packedEntryOperand(thread, at) >= operand)
            break;
    return at;
}

static ZamMarkPack innermostPack(const ZamThread *thread) {
    if (thread->packEntry == ZAMMARK_NONE)
        zam_die(ZAMEXIT_PANIC, "packed with no pack open\n");
   ZamMarkPack pack;
    readEntry(thread, thread->packEntry, &pack, sizeof pack);
    return pack;
}

// Rotating each part and then the whole leaves the two parts swapped, each in
// its own order, which is what moves the frame above the values without a
// buffer for either.
void zam_thread_sink_pack(ZamThread *thread, uint64_t valueWords) {
   ZamMarkPack pack = innermostPack(thread);
    if (valueWords > thread->operandCount - pack.frame)
        zam_die(ZAMEXIT_PANIC, "packed %llu words over a %llu word frame\n",
                (unsigned long long)valueWords,
                (unsigned long long)(thread->operandCount - pack.frame));
    uint64_t frameWords = thread->operandCount - valueWords - pack.frame;
    uint64_t valuesAt = pack.frame + frameWords;
   ZamWord *operands = thread->operands + pack.frame;
    reverseWords(operands, frameWords);
    reverseWords(operands + frameWords, valueWords);
    reverseWords(operands, frameWords + valueWords);

    uint64_t from = thread->packEntry + sizeof pack;
    uint64_t split = packedEntriesFrom(thread, valuesAt);
    uint64_t end = thread->markByteCount;
    reverseBytes(thread->marks + from, split - from);
    reverseBytes(thread->marks + split, end - split);
    reverseBytes(thread->marks + from, end - from);

    uint64_t valuesEnd = from + (end - split);
    for (uint64_t at = from; at < end;
         at += zam_mark_entry_bytes(zam_mark_kind_at(thread, at))) {
        uint64_t operand = packedEntryOperand(thread, at);
        moveEntryOperand(thread, at,
                         at < valuesEnd ? operand - frameWords
                                        : operand + valueWords);
    }

    pack.values = valueWords;
    memcpy(thread->marks + thread->packEntry, &pack, sizeof pack);
}

void zam_thread_pop_pack(ZamThread *thread) {
   ZamMarkPack pack = innermostPack(thread);
    uint64_t above = pack.frame + pack.values;
    if (above > thread->operandCount)
        zam_die(ZAMEXIT_PANIC, "a pack's values are past the stack\n");
    uint64_t resultWords = thread->operandCount - above;
    memmove(thread->operands + pack.frame, thread->operands + above,
            resultWords * ZAMWORD_BYTES);
    thread->operandCount = pack.frame + resultWords;

    uint64_t split = packedEntriesFrom(thread, above);
    uint64_t kept = thread->markByteCount - split;
    uint64_t at = thread->packEntry;
    memmove(thread->marks + at, thread->marks + split, kept);
    thread->markByteCount = at + kept;
    thread->packEntry = pack.previous;
    for (; at < thread->markByteCount;
         at += zam_mark_entry_bytes(zam_mark_kind_at(thread, at))) {
        uint64_t operand = packedEntryOperand(thread, at);
        moveEntryOperand(thread, at, operand - pack.values);
    }
}

bool zam_thread_pack_of(const ZamThread *thread, uint64_t base,
                        ZamMarkPack *pack) {
    if (thread->packEntry == ZAMMARK_NONE)
        return false;
    readEntry(thread, thread->packEntry, pack, sizeof *pack);
    return pack->values != 0 && pack->frame + pack->values == base;
}

// ZAMVISITED_INITIAL_SLOTS is what a set is given the first time it is opened.
// A descent that meets no pair at all is the common one and allocates nothing,
// the slots being taken on the first record rather than on the push.
#define ZAMVISITED_INITIAL_SLOTS 16

// Where a pair's slot search begins. The three fields are mixed rather than
// added: two pointers an allocation apart and two a type id apart would
// otherwise land on one slot, and the probe is linear, so pairs that hash alike
// are pairs that walk over each other.
static uint64_t visitHash(ZamWord left, ZamWord right, ZamTypeShort type) {
    uint64_t hash = 14695981039346656037ULL;
    uint64_t words[] = {left, right, type};
    for (uint64_t at = 0; at < sizeof words / sizeof *words; at++) {
        hash = (hash ^ words[at]) * 1099511628211ULL;
        hash ^= hash >> 32;
    }
    return hash;
}

static bool visitEquals(const ZamVisit *entry, ZamWord left, ZamWord right,
                       ZamTypeShort type) {
    return entry->left == left && entry->right == right && entry->type == type;
}

// Rehashing into twice the slots, which open addressing needs before the probe
// has to walk most of the table to find a hole. The entries are re-placed rather
// than copied: where one lands is the slot count's to say.
static void growVisited(ZamVisitedSet *set) {
    uint64_t capacity = set->capacity == 0 ? ZAMVISITED_INITIAL_SLOTS
                                           : set->capacity * 2;
   ZamVisit *entries = zam_alloc(capacity * sizeof *entries);
    for (uint64_t at = 0; at < capacity; at++)
        entries[at] = (ZamVisit){0};

    for (uint64_t at = 0; at < set->capacity; at++) {
        const ZamVisit *entry = &set->entries[at];
        if (entry->left == 0)
            continue;
        uint64_t slot =
            visitHash(entry->left, entry->right, entry->type) & (capacity - 1);
        while (entries[slot].left != 0)
            slot = (slot + 1) & (capacity - 1);
        entries[slot] = *entry;
    }

    free(set->entries);
    set->entries = entries;
    set->capacity = capacity;
}

void zam_thread_push_visited(ZamThread *thread) {
    if (thread->visitedCount == thread->visitedCapacity) {
        thread->visitedCapacity =
            thread->visitedCapacity == 0 ? 4 : thread->visitedCapacity * 2;
        thread->visited = zam_realloc(
            thread->visited, thread->visitedCapacity * sizeof *thread->visited);
        for (uint64_t at = thread->visitedCount; at < thread->visitedCapacity;
             at++)
            thread->visited[at] = (ZamVisitedSet){0};
    }
    thread->visitedCount++;
}

void zam_thread_pop_visited(ZamThread *thread) {
    if (thread->visitedCount == 0)
        zam_die(ZAMEXIT_PANIC, "closed a comparison with none open\n");

    // The slots are kept and only emptied. They are the room the next
    // comparison at this depth will ask for, and a program comparing in a loop
    // is the one that asks most often.
   ZamVisitedSet *set = &thread->visited[--thread->visitedCount];
    for (uint64_t at = 0; at < set->capacity; at++)
        set->entries[at] = (ZamVisit){0};
    set->count = 0;
}

bool zam_thread_visit(ZamThread *thread, ZamWord left, ZamWord right,
                     ZamTypeShort type) {
    if (thread->visitedCount == 0)
        zam_die(ZAMEXIT_PANIC, "recorded a pair with no comparison open\n");
    if (left == 0 || right == 0)
        zam_die(ZAMEXIT_PANIC, "descended into a null\n");

   ZamVisitedSet *set = &thread->visited[thread->visitedCount - 1];

    // Grown at three quarters full rather than when full, which is where a
    // linear probe stops being short.
    if ((set->count + 1) * 4 > set->capacity * 3)
        growVisited(set);

    uint64_t slot = visitHash(left, right, type) & (set->capacity - 1);
    while (set->entries[slot].left != 0) {
        if (visitEquals(&set->entries[slot], left, right, type))
            return true;
        slot = (slot + 1) & (set->capacity - 1);
    }

    set->entries[slot] =
        (ZamVisit){.left = left, .right = right, .type = type};
    set->count++;
    return false;
}
