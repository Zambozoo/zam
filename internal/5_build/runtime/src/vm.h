#pragma once

#include <pthread.h>
#include <stdatomic.h>
#include <time.h>

#include "common.h"
#include "heap.h"
#include "zam/native.h"
#include "image.h"
#include "instance.h"
#include "thread.h"

// ZamCollectionStats is what one collection did, for ZAM_GC_STATS.
typedef struct ZamCollectionStats {
    uint64_t cards;
    uint64_t precleaned;
    uint64_t retraced;
    uint64_t finalized;
    uint64_t survived;
    uint64_t promoted;
    uint64_t returned;
} ZamCollectionStats;

// The old generation's four lists, by which of Mark and Delete are the
// program's own. A Mark may decide what is reachable from anything at all, so
// a minor collection runs every Mark of the program's own again rather than
// trusting the card table to have seen what it depends on; and a collection
// has to find every Delete of the program's own to run it. The index is the
// two answers as bits.
typedef enum ZamOldList {
    ZAMOLD_PLAIN = 0,
    ZAMOLD_MARKS = 1,
    ZAMOLD_DELETES = 2,
    ZAMOLD_BOTH = ZAMOLD_MARKS | ZAMOLD_DELETES,
    ZAMOLD_COUNT,
} ZamOldList;

//ZamVM is everything one program has that is not one thread's: the image it
// was loaded from, the static region every thread shares, the heap the
// collector owns, and the list of threads it collects over.
typedef struct ZamVM {
   ZamImage image;

    // statics is the region every static lives in, allocated at the size the
    // image asked for. The string literals are copied to the head of it on
    // load, since a literal's slot address is already the string and nothing is
    // ever loaded through it; everything after them starts zeroed.
    uint8_t *statics;

    // natives is the image's slot numbering resolved to the functions this
    // interpreter was built with, so a call is an index rather than a lookup.
   ZamNativeFn *natives;
    uint64_t nativeCount;

    // heap is the range every allocation the collector owns and every block
    // of the program's own memory is carved from.
    ZamHeap heap;

    // young is the young generation's newest allocations while a collection
    // runs: every allocation made since the last one, threaded through the
    // upvalue field of each one's mark word. Between collections each thread
    // keeps its own, and the collection splices them all on here first. It costs no memory beyond the word a markable
    // allocation already carries, and it is the half of a sweep the roots
    // cannot supply: an allocation nothing refers to is exactly the one to be
    // freed, and it has to be findable to be freed.
    //
    // Only the heap is on it. A markable value laid out on the operand stack
    // carries the same header — OpDefaultValue writes one — but is not an
    // allocation and is never linked, its storage being the stack's to reclaim.
    // That is what makes the link a reliable answer to "did the allocator hand
    // this out", rather than only to "does this have a mark word".
    //
    // What the collector allocates while collecting goes here directly: it is
    // allocated black, and has to survive the sweep it was made during.
    //
    // survivors is the rest of the young generation: what has survived one
    // minor collection, left white for the next to trace. What survives a
    // second is promoted.
    ZamObjectList young[ZAMYOUNG_COUNT];
    ZamObjectList survivors[ZAMYOUNG_COUNT];

    // fresh is what was allocated while the marker runs, grey as it was
    // allocated, and freshSurvivors what of it has survived one minor
    // collection alongside the marker. What survives a second is promoted
    // black for the marker to trace. The remark gathers both with the rest of
    // the young generation.
    ZamObjectList fresh[ZAMYOUNG_COUNT];
    ZamObjectList freshSurvivors[ZAMYOUNG_COUNT];

    // old is the old generation, linked the same way: everything promoted.
    // Its marks are left black between collections, which is what stops a
    // minor collection tracing into it, and only the collector touches it.
    // Each old allocation is on one of its lists.
    ZamObjectList old[ZAMOLD_COUNT];

    // The unswept lists are what a collection has yet to sweep, set aside
    // from the lists above so that what survives can be put back on them.
    ZamObjectList unsweptYoung[ZAMYOUNG_COUNT];
    ZamObjectList unsweptSurvivors[ZAMYOUNG_COUNT];
    ZamObjectList unsweptOld[ZAMOLD_COUNT];

    // heldCards is every card a minor collection found pointing into the young
    // generation, which it marks dirty again once it has swept: what those
    // cards hold may have survived without being promoted, and the next minor
    // collection has to see it again.
    uint8_t **heldCards;
    uint64_t heldCardCount;
    uint64_t heldCardCapacity;

    // allocatedByteCount is what the heap holds, and nextCollectionByteCount is
    // what it has to reach for a major collection to be asked for: the trigger
    // ratio of heapByteCount, the size the heap is let grow to. The heap starts
    // at the size the image asks for and doubles whenever what a major
    // collection leaves would already ask for the next one. The ratio is the
    // program's to set, and comes in on the image.
    //
    // youngByteCount is what has been allocated since the last collection, and
    // a minor collection is asked for once it reaches nurseryByteCount.
    //
    // Threads add to both counts flushByteCount at a time, so neither is
    // contended on every allocation. A collection flushes every thread first.
    _Atomic uint64_t allocatedByteCount;
    _Atomic uint64_t nextCollectionByteCount;
    _Atomic uint64_t youngByteCount;
    uint64_t nurseryByteCount;
    uint64_t flushByteCount;
    uint64_t heapByteCount;
    double gcTriggerRatio;

    // verify has every collection check that a minor collection in its place
    // would have freed nothing reachable, and then collect in full.
    bool verify;

    // stats has every collection write a line of what it did to stderr, and
    // collections counts them. The counts are the current collection's.
    bool stats;
    uint64_t collections;
    ZamCollectionStats current;

    // concurrent has a major collection asked for by the heap's growth mark
    // alongside the program, which ZAM_GC_CONCURRENT=0 turns off.
    //
    // A concurrent collection stops the program twice. The first stop marks
    // what the threads and statics hold and sets marking; marker then traces
    // from there while the program runs, allocating grey. The second, the
    // remark, is the marker's: it marks again everything the program could
    // have hidden a reference in meanwhile, and sweeps.
    //
    // finishRequested has the marker stop tracing and remark at once: the
    // heap has grown too far, a major collection was asked for, or the VM is
    // going. markerQuit has it end once it has.
    //
    // A minor collection asked for meanwhile collects only what was allocated
    // since marking began, with the marker paused: markerPause asks it to
    // stop at its next checkpoint, and markerParked says it has. markerBusy is
    // set while it traces, when only a checkpoint is somewhere it can stop.
    bool concurrent;
    atomic_bool marking;

    // sweeping is set from the remark until the marker has swept what it set
    // aside, alongside the program; no collection starts meanwhile.
    atomic_bool sweeping;
    atomic_bool finishRequested;
    bool markerQuit;
    atomic_bool markerPause;
    bool markerParked;
    bool markerBusy;
    _Atomic(ZamThread *) marker;
    pthread_t markerThread;

    // retrace is what the marker reached and could not trace alongside the
    // program, for the remark to trace.
    void **retrace;
    uint64_t retraceCount;
    uint64_t retraceCapacity;

    // cycleStart is when the concurrent collection under way first stopped
    // the program, cycleBefore and cycleYoung what the heap and the young
    // generation held then, and initialPause, markTime and remarkPause how
    // long its first stop, its marking and its remark took.
    struct timespec cycleStart;
    uint64_t cycleBefore;
    uint64_t cycleYoung;
    double initialPause;
    double markTime;
    double remarkPause;

    // collectionRequested is set by whichever thread crossed the threshold.
    // A collection happens only at a safepoint, between instructions: a thread
    // mid-instruction is a thread whose stacks do not yet say what they will.
    //
    // stopRequested is set while a collector is stopping the other threads or
    // collecting, and is what every other thread parks on at its next
    // safepoint. Both are read without the lock on every instruction, and
    // changed only under it.
    atomic_bool collectionRequested;

    // majorRequested is set by GC.Major, and makes the next collection major
    // whatever the heap holds.
    atomic_bool majorRequested;
    atomic_bool stopRequested;

    // collectionLock guards every thread's state and the two flags above, and
    // collectionChanged is signalled whenever any of them changes: a thread
    // parking or entering a safe region wakes the collector waiting for it, and
    // the end of a collection wakes every thread waiting to resume.
    pthread_mutex_t collectionLock;
    pthread_cond_t collectionChanged;
    pthread_mutex_t threadLock;

    // markableDecls caches, per declaration, whether a value of it holds
    // anything the collector must visit: zero until asked, then one for no
    // and two for yes.
    _Atomic uint8_t *markableDecls;

    // customMarkDecls caches, the same way, whether marking a value of a
    // declaration runs any Mark but Object's own, and customDeleteDecls
    // whether a class's Delete is anything but Object's own.
    _Atomic uint8_t *customMarkDecls;
    _Atomic uint8_t *customDeleteDecls;

    // collector is the thread collecting, while one is. It and marker are read
    // by threads in a safe region, which run alongside a collection. Everything a
    // collection runs runs on it, and every other thread is stopped; the
    // marker is the one thread that also runs what a collection does, while
    // marking.
    _Atomic(ZamThread *) collector;

    // traceAllocations is set while the collector marks, when what a Mark
    // allocates has to be traced as anything reached is.
    bool traceAllocations;

    // freshOnly is set while a minor collection runs alongside the marker, when
    // only the fresh allocations youngSpans lays out are reached.
    bool freshOnly;

    // gray is the collector's work: allocations it has reached and not yet
    // traced through.
    void **gray;
    uint64_t grayCount;
    uint64_t grayCapacity;

    // youngSpans is the young generation sorted by address during a minor
    // collection, as a start and an end for each allocation, which is what a
    // word on a dirty card is looked up in.
    uintptr_t *youngSpans;
    uint64_t youngSpanCount;
    uint64_t youngSpanCapacity;

    // main is the thread control begins on and the head of the list of every
    // live thread. That list is what the collector walks to find every root, so
    // a thread not reachable from here is a thread whose stack is not scanned.
   ZamThread main;
} ZamVM;

// zam_vm_marker_checkpoint is where the marker stops for a minor collection
// that asked it to, between one piece of its work and the next.
void zam_vm_marker_checkpoint(ZamVM *vm);

void zam_vm_init(ZamVM *vm, const char *imagePath);

// zam_vm_init_from_bytes is zam_vm_init over an image whose bytes already sit
// in memory, as the compiler-generated wrapper hands over the byte array it
// baked into the binary. The bytes are read in place and are not freed by the
// VM; everything else about init is unchanged.
void zam_vm_init_from_bytes(ZamVM *vm, const uint8_t *bytes, size_t byteCount);

void zam_vm_free(ZamVM *vm);

// ZAMMAIN_FRAME_WORDS is how wide the frame main runs on is: the slot a frame
// keeps for its receiver, then the two parameters main is written with. Main is
// fallen into rather than called, so no caller lays these and zam_vm_run does —
// which is what makes main's first local land at offset three the way the
// compiler numbered it.
#define ZAMMAIN_FRAME_WORDS 3

// zam_vm_run drives the main thread to a stop and answers with how it stopped.
// The status the process ends with is on the thread rather than here, since it
// is what the program said and not what the runtime made of it.
//
// argC and argV are the program's own arguments, with the runner's own words
// — the runner's binary name, the image path — already stripped by the caller.
// They land in main's parameter slots as `(uint, str...)`: argC becomes the
// variadic's count, and argV becomes the pointer the body reads strings off.
// Zero arguments give argC == 0 and a null argV, matching OpNewArray's answer
// for an empty variadic call.
ZamResult zam_vm_run(ZamVM *vm, int argC, const char *argV[]);

// zam_vm_exit ends the process as a run that stopped with result says to. A
// thread still running when main stops is ended with the process, so the VM
// is torn down only where none is: the rest still read it to the last.
_Noreturn void zam_vm_exit(ZamVM *vm, ZamResult result);

// zam_vm_static is the address of a static slot, which is what a push of one
// leaves and what a load or store then reads through.
uint8_t *zam_vm_static(ZamVM *vm, uint64_t slot);

// zam_vm_alloc lays down one allocation of objectBytes and fills in the header
// the kind calls for, and answers the pointer the program holds — which is the
// allocation itself, so that zam_mark_word_of finds the word it begins with.
//
// objectBytes is the whole entity, header included, because every entity but a
// struct and an enum begins with its header: sizeof(ZamClosure) already counts
// it, and so does the instanceByteSize the compiler records. All of it is
// zeroed, since a caller that has not written a field yet must not have the
// collector read whatever was there before.
//
// shortID and elementCount fill the type header, and are ignored for a string,
// which has none. A tailed allocation is asked for with the tailed kind and is
// then given its length by the caller, that being the one header word the
// allocator is not told.
//
// Every allocation is threaded onto its thread's young list as it is made, and
// every list is reachable from the VM's thread list, so the heap is walkable
// from the VM alone.
void *zam_vm_alloc(ZamVM *vm, ZamThread *thread, ZamObjectKind kind,
                  ZamTypeShort shortID, uint64_t elementCount,
                   uint64_t objectBytes);

// zam_vm_alloc_raw carves a block of the program's own memory, which the
// collector does not free and which the program releases with
// zam_vm_free_raw. It is not zeroed.
void *zam_vm_alloc_raw(ZamThread *thread, uint64_t byteCount);

// zam_vm_free_raw releases a block of the program's own memory, whether the
// heap or the C allocator handed it out.
void zam_vm_free_raw(ZamThread *thread, void *block);

// zam_vm_collect is the collection itself, run by collector. It is run only
// by zam_vm_safepoint, with every other thread stopped.
void zam_vm_collect(ZamVM *vm, ZamThread *collector);

// zam_vm_safepoint is where a thread stops for a collection, and is run
// between every two instructions. A collection asked for is run by the first
// thread to reach one, which stops every other thread before collecting and
// resumes them after; a thread reaching one while another collects waits there
// until the collection is over.
//
// A thread that has frozen the collector with GC.Block passes its safepoints
// as if nothing were asked, so a collection waits for every frozen thread to
// unfreeze and reach its next one.
void zam_vm_safepoint(ZamVM *vm, ZamThread *thread);

// zam_vm_enter_safe and zam_vm_leave_safe bracket a call that may block, such
// as a join or a read. A thread between the two counts as stopped, so a
// collection need not wait for the call to return; leaving waits out any
// collection in progress. Between the two the thread must not touch the heap
// or its own stacks, which the collector may be reading.
//
// A frozen thread may not block. Whatever it waits on may be waiting on a
// collection, which is waiting on it, so entering dies instead.
void zam_vm_enter_safe(ZamThread *thread);
void zam_vm_leave_safe(ZamThread *thread);

// zam_vm_thread_exited records that a thread has run its last instruction, so
// a collection no longer waits for it.
void zam_vm_thread_exited(ZamThread *thread);

// zam_natives_resolve binds every name the image carries to a function in the
// table this interpreter was built with. It lives in native.c beside that
// table.
void zam_natives_resolve(ZamVM *vm);
