#pragma once

#include <pthread.h>
#include <signal.h>

#include "common.h"
#include "instance.h"
#include "type.h"

typedef struct ZamVM ZamVM;
typedef struct ZamThread ZamThread;

// How a step ended, which is the whole of what the loop reads off one.
typedef enum ZamResult {
    // ZAMRESULT_CONTINUE is the ordinary case: the instruction ran and the
    // next one should.
    ZAMRESULT_CONTINUE,
    // ZAMRESULT_EXIT is OpExit reached. The thread's exitStatus is what the
    // program said to end with.
    ZAMRESULT_EXIT,
    // ZAMRESULT_RETURNED is a thread other than main running off the end of
    // the body it was started on, which is how one finishes.
    ZAMRESULT_RETURNED,
    // ZAMRESULT_PANIC is the program brought down by something it did.
    ZAMRESULT_PANIC,
} ZamResult;

const char *zam_result_name(ZamResult result);

// The mark stack is a run of tightly packed variable-width entries, each led by
// a one-byte header naming its kind. It is bytes and not a struct array because
// the four kinds are four different widths and the narrow ones are the ones
// pushed most.
//
// Packing costs backward traversal: an entry can only be found by walking
// forward from the bottom, which is what the collector does anyway. The two
// kinds that have to be found from the top — function frames and scope frames —
// pay for it with a back pointer to the previous entry of their own kind, so
// each is the head of a linked list threaded through the stack.
typedef enum ZamMarkKind {
    // ZAMMARK_FUNCTION is a call: where to resume the caller, and how far to
    // rewind the operand stack when this returns.
    ZAMMARK_FUNCTION,
    // ZAMMARK_OBJECT is one markable object or function on the operand stack.
    ZAMMARK_OBJECT,
    // ZAMMARK_ARRAY is a markable array: its type id says how many entries lie
    // behind the pointer and what they are.
    ZAMMARK_ARRAY,
    // ZAMMARK_SCOPE is a lexical scope opened inside a function.
    ZAMMARK_SCOPE,
    // ZAMMARK_VALUE is a value laid out inline on the operand stack that holds
    // something the collector must visit: a class held by value, or a struct,
    // enum or run with one inside it. Its type is what walking it needs.
    ZAMMARK_VALUE,
    // ZAMMARK_INTERIOR is a location inside a markable object, such as the
    // address of one of its members, which keeps the object alive the way a
    // pointer to it would. Its distance is how far back the object lies.
    ZAMMARK_INTERIOR,
} ZamMarkKind;

// The kind byte is the first field of every entry, sharing its word with one
// location. No location needs more than 56 bits — the widest thing any of them
// names is an offset into a stack, and 56 bits is 64 petabytes of one — so the
// byte costs nothing and every entry comes out a whole number of words.
//
// That the field declared first is the byte stored first is a little-endian
// property, and so is guaranteed by the same host check `common.h` makes to read
// the image in place. It is what lets a forward walk read a kind without yet
// knowing which struct it is looking at.
#define ZAMMARK_LOCATION_BITS 56

// ZAMMARK_NONE ends a back pointer chain, and is every bit of a location set
// rather than UINT64_MAX, which would not survive the narrower field. Zero
// cannot say it, since the bottommost entry sits at offset zero.
#define ZAMMARK_NONE (((uint64_t)1 << ZAMMARK_LOCATION_BITS) - 1)

// Every location below is an offset — into the mark stack for a back pointer,
// into the operand stack for anything called a pointer to the operand stack —
// rather than an address. Both stacks grow by reallocation, and an address into
// either stops meaning anything the moment it moves. An offset survives it.
typedef struct ZamMarkFunction {
    uint64_t kind : 8;
    // previous is the mark offset of the enclosing function entry.
    uint64_t previous : ZAMMARK_LOCATION_BITS;
    // returnInstruction is where in the operations section the caller resumes.
    uint64_t returnInstruction;
    // operandPop is how far the operand stack rewinds when this returns.
    uint64_t operandPop;
} ZamMarkFunction;

typedef struct ZamMarkObject {
    uint64_t kind : 8;
    // operand is the operand stack offset of the markable word.
    uint64_t operand : ZAMMARK_LOCATION_BITS;
} ZamMarkObject;

typedef struct ZamMarkArray {
    uint64_t kind : 8;
    // operand is the operand stack offset of the run's first word, or, where
    // indirect is set, of the one word holding the address the run lies at.
    uint64_t operand : ZAMMARK_LOCATION_BITS;
    // element is the packed short id of what the array holds, and length is how
    // many of them there are. Together they are the whole of what traversing
    // the array needs.
    //
    // The tail length of the long type id these come off is deliberately not
    // kept. A tail belongs to one instance of a tailed type, and what is walked
    // here is the run of elements, so the tail would be a word carried for
    // nothing on the second most common entry there is.
    uint64_t element : 32;
    uint64_t indirect : 1;
    uint64_t spare : 31;
    uint64_t length;
} ZamMarkArray;

typedef struct ZamMarkScope {
    uint64_t kind : 8;
    // previous is the mark offset of the enclosing scope entry.
    uint64_t previous : ZAMMARK_LOCATION_BITS;
    // frame is the operand stack offset the scope began at, and so where
    // leaving it rewinds to.
    uint64_t frame;
} ZamMarkScope;

typedef struct ZamMarkValue {
    uint64_t kind : 8;
    // operand is the operand stack offset of the value's first word.
    uint64_t operand : ZAMMARK_LOCATION_BITS;
    // type is the value's type id as the wire writes one.
    ZamTypeIDWords type;
} ZamMarkValue;

typedef struct ZamMarkInterior {
    uint64_t kind : 8;
    // operand is the operand stack offset of the location.
    uint64_t operand : ZAMMARK_LOCATION_BITS;
    // distance is how many bytes before the location the mark word of the
    // object it lies inside begins.
    uint64_t distance;
} ZamMarkInterior;

// Entries are stacked with nothing between them, so a width that stopped being
// a multiple of a word would misalign every entry above it.
_Static_assert(sizeof(ZamMarkFunction) == 3 * ZAMWORD_BYTES, "function entry");
_Static_assert(sizeof(ZamMarkObject) == 1 * ZAMWORD_BYTES, "object entry");
_Static_assert(sizeof(ZamMarkArray) == 3 * ZAMWORD_BYTES, "array entry");
_Static_assert(sizeof(ZamMarkScope) == 2 * ZAMWORD_BYTES, "scope entry");
_Static_assert(sizeof(ZamMarkValue) == 4 * ZAMWORD_BYTES, "value entry");
_Static_assert(sizeof(ZamMarkInterior) == 2 * ZAMWORD_BYTES, "interior entry");

// zam_mark_entry_bytes is how wide an entry of the given kind is, header
// included. Walking the stack forward is this applied to each header in turn.
uint64_t zam_mark_entry_bytes(ZamMarkKind kind);

// zam_mark_kind_at reads the header byte at a mark stack offset.
ZamMarkKind zam_mark_kind_at(const ZamThread *thread, uint64_t offset);

// A pair of instances a structural comparison has already descended into, and
// the type it descended as. Two pointers at one pair of instances are asked
// about more than once exactly when the graph beneath them leads back to them,
// so recording the pair is what lets the second ask be answered instead of
// followed.
//
// The two are held as the pointer words themselves and not as what
// zam_thread_location_address resolves them to, because the resolved form is not
// stable for the whole of a descent: a pointer taken of a stack value is a frame
// location, the operand stack grows by realloc, and a dispatched Eq pushes — so
// the address a frame location names moves while the comparison that recorded it
// is still running. The word does not. An allocation's address is equally
// stable, the collector being mark-and-sweep and never moving one, so the word
// is the lasting name under both forms.
//
// The type is part of it because one pointer is reached as more than one type: a
// class laid out inline begins where the instance containing it does, so the
// same word stands for the container and for its first member, and the two are
// different questions.
//
// An empty slot is a zero left, which no real entry can be. A comparison
// settles a null before it descends — there is nothing to walk and nothing to
// ask — so the pairs that reach here are pairs of real instances.
typedef struct ZamVisit {
   ZamWord left;
   ZamWord right;
   ZamTypeShort type;
} ZamVisit;

// The pairs one structural comparison has descended into, open addressed over a
// power-of-two run of slots.
typedef struct ZamVisitedSet {
   ZamVisit *entries;
    uint64_t count;
    uint64_t capacity;
} ZamVisitedSet;

// Where a thread is with respect to a collection, which is what a collector
// waits on before it may read every thread's stacks.
typedef enum ZamThreadState {
    // ZAMTHREAD_RUNNING is executing instructions, and is what a collector
    // waits for a thread to stop doing. It is zero, which is what a thread
    // starts as.
    ZAMTHREAD_RUNNING,
    // ZAMTHREAD_PARKED is stopped at a safepoint until a collection is over.
    ZAMTHREAD_PARKED,
    // ZAMTHREAD_SAFE is inside a call that may block, touching neither the
    // heap nor its own stacks until it returns.
    ZAMTHREAD_SAFE,
    // ZAMTHREAD_EXITED has run its last instruction.
    ZAMTHREAD_EXITED,
} ZamThreadState;

//ZamThread is one line of execution: its two stacks and where it is in the
// program. The main thread is one of these like any other, which is what lets
// the loop be written once.
struct ZamThread {
    // Every entity but a struct and an enum begins with the header it is
    // branched on, so that an opcode or the collector holding the pointer can
    // ask what it has before it knows what it has. The main thread is embedded
    // in the ZamVM and is never collected, so its copy is room the shape costs
    // rather than a word that gets read.
   ZamTypedObjectHeader header;

   ZamVM *vm;
    pthread_t systemThread;

    // result is how the last step ended, and so whether there is a next one.
   ZamResult result;
    // exitStatus is what OpExit was given, kept to the low eight bits, since a
    // status is a byte to whoever started the program.
    uint8_t exitStatus;

    // operands are the values in flight. A value laid out inline spans as many
    // words as its layout, so this is counted in words and never in values.
   ZamWord *operands;
    uint64_t operandCount;
    uint64_t operandCapacity;

    // marks are the packed entries, counted in bytes because they are not all
    // one width.
    uint8_t *marks;
    uint64_t markByteCount;
    uint64_t markByteCapacity;

    // visited is a stack of sets rather than one set, because a comparison can
    // begin inside another one and the two are not the same descent: a
    // hand-written Eq is a body like any other and may compare whatever it
    // likes, and a pair that body reaches says nothing about the pair the
    // enclosing walk is partway through. The innermost open set is the one a
    // descent records into and asks of.
    //
    // visitedCount is how many are open and visitedCapacity how many have been
    // allocated. The two differ because a popped set keeps its slots: a program
    // that compares in a loop would otherwise allocate a table per comparison,
    // and the room a set needs is the room the next one of its kind will need.
    //
    // On the thread and not on the VM, which holds what every thread shares.
    // Two threads each evaluating a comparison are two descents, and one set
    // between them would have each recording pairs the other asks about.
   ZamVisitedSet *visited;
    uint64_t visitedCount;
    uint64_t visitedCapacity;

    // functionEntry and scopeEntry are the mark offsets of the innermost open
    // call and the innermost open scope, the heads of the two back pointer
    // chains. ZAMMARK_NONE when nothing of that kind is open.
    uint64_t functionEntry;
    uint64_t scopeEntry;

    // instructionOffset is where in the operations section the next instruction
    // begins, in bytes. A jump target is one of these.
    uint64_t instructionOffset;

    // allocatedByteCount is what this thread has allocated since the last
    // collection, which is what the collection after it is triggered by.
    uint64_t allocatedByteCount;

    // state is guarded by the VM's collectionLock. gcBlockDepth is how many
    // GC.Block calls this thread has made that no GC.Unblock has matched yet,
    // and is read and written only by the thread itself.
    ZamThreadState state;
    uint64_t gcBlockDepth;

    // joined is whether a join has reaped the system thread, which the
    // collector otherwise does when it frees a finished thread.
    bool joined;

    // next is the thread started after this one. The VM holds the head and
    // every thread is reachable from it, which is what the collector walks to
    // find every root.
    struct ZamThread *next;

    // Signal state lives here rather than process-wide so each thread
    // dispatches only the handlers it registered. The C signal handler reads
    // its ZamThread back out of thread-local storage (see currentZamThread
    // in native.c) and ORs the bit into this mask; the interpreter polls
    // between instructions on the thread it is driving.
    //
    // pendingSignalMask is written from the signal context, which is why it
    // is volatile sig_atomic_t rather than a plain uint. signalHandlers
    // holds the zam closure pointer the program handed in through
    // Signal.Handle, and is a root of every collection.
    volatile sig_atomic_t pendingSignalMask;
    void *signalHandlers[ZAM_SIGNAL_MAX];
};

_Static_assert(offsetof(ZamThread, header.mark) == 0,
               "the mark word must be the word the pointer names");

void zam_thread_init(ZamThread *thread, ZamVM *vm, uint64_t initialStackSize);
void zam_thread_free(ZamThread *thread);

// zam_thread_push and zam_thread_pop move one word. A value wider than a word
// is that many of these, since how wide a value is is the type's to say and
// these are told none.
void zam_thread_push(ZamThread *thread, ZamWord word);
ZamWord zam_thread_pop(ZamThread *thread);
// zam_thread_peek reads back from the top without spending anything, at zero
// for the topmost word.
ZamWord zam_thread_peek(const ZamThread *thread, uint64_t depth);

// zam_thread_push_type and zam_thread_pop_type move the three words a type id
// occupies, which is the unit almost every instruction told a type is told.
void zam_thread_push_type(ZamThread *thread, ZamTypeID type);
ZamTypeID zam_thread_pop_type(ZamThread *thread);
// zam_thread_peek_type reads a type id back without spending it, at the depth of
// its topmost word. It is what an instruction told two type ids with a value
// between them reads the deeper one through, that one not being on top and not
// being finished with.
ZamTypeID zam_thread_peek_type(const ZamThread *thread, uint64_t depth);

// zam_thread_push_mark and zam_thread_push_array_mark put on an entry naming an
// operand word the collector must start from. Nothing is done to what the word
// refers to — the mark word on an allocation is the collector's own and is a
// different thing entirely. Not every word owns an entry, a number owning
// nothing, which is why the two stacks are not one.
//
// The array form is the one entry a whole run of addresses owes, rather than one
// per element: the words arrive together under a type id naming the run, so the
// element type and the count ride on the entry and are what walking it needs.
void zam_thread_push_mark(ZamThread *thread, uint64_t operand);
void zam_thread_push_array_mark(ZamThread *thread, ZamTypeID type,
                                uint64_t operand);

// zam_thread_push_indirect_array_mark is the array form for a run that lies
// elsewhere, its address standing in the operand word.
void zam_thread_push_indirect_array_mark(ZamThread *thread, ZamTypeID type,
                                         uint64_t operand);

// zam_thread_push_value_mark names a value laid out inline from the operand
// word on, and zam_thread_push_interior_mark a location distance bytes past
// the mark word of the object it lies in.
void zam_thread_push_value_mark(ZamThread *thread, ZamTypeID type,
                                uint64_t operand);
void zam_thread_push_interior_mark(ZamThread *thread, uint64_t operand,
                                   uint64_t distance);

// zam_thread_pop_mark takes the entry the given operand word owns back off, if
// it owns one, and answers whether it did. It is what OpPopWord and OpStore
// spend a marked word through: the word goes, so the entry naming it has to go
// with it or the collector would start from a slot the program has given up.
//
// Nothing is being said about the object here. An entry is a word of the operand
// stack the collector has to visit, so pushing one and taking it back off is how
// the list of those words is kept level with the stack itself — an object
// reached through two words is named twice and outlives either of them.
//
// Only the topmost entry can be the one, since an entry is pushed exactly when
// its word is and the two stacks are spent in step. Finding it costs a forward
// walk, entries being packed and unheadered from the top — but the walk begins
// at the innermost open scope or call rather than at the bottom of the stack,
// so what it crosses is what the current scope pushed and not the whole
// program's history.
bool zam_thread_pop_mark(ZamThread *thread, uint64_t operand);

// zam_thread_pop_object_mark is zam_thread_pop_mark that also answers how far
// into its object the word points: zero for a markable reference, and the
// distance of an interior location. It is false, having popped nothing or an
// entry of another kind, where the word reaches no object.
bool zam_thread_pop_object_mark(ZamThread *thread, uint64_t operand,
                                uint64_t *distance);

// A location is an address, except where it names a slot on the operand stack.
// The stack is reallocated as it grows, so an address into it stops meaning
// anything the moment it moves; a frame slot is therefore named by how far into
// the stack it lies and turned into an address only at the instruction that
// reaches through it.
//
// The two are told apart by the top bit, which no address on the platforms this
// runs on has set: user space is the low half of the range and the kernel keeps
// the high half. What is beneath the bit is a byte offset rather than a word
// index, because a member selected out of a stack-resident value lies at its own
// byte offset inside one and is a location like any other.
//
// A frame location owns no mark entry and must never be given one. It is not an
// allocation and the words it names are already roots in their own right, so a
// collector handed one would follow a number.
#define ZAMFRAME_LOCATION_BIT ((ZamWord)1 << 63)

static inline ZamWord zam_frame_location(uint64_t byteOffset) {
    return ZAMFRAME_LOCATION_BIT | byteOffset;
}

static inline bool zam_is_frame_location(ZamWord location) {
    return (location & ZAMFRAME_LOCATION_BIT) != 0;
}

// zam_thread_location_address is where a location actually is, now. Every
// instruction that reads or writes through one goes through this rather than
// casting, which is what keeps the frame form from being an address anywhere
// but here.
void *zam_thread_location_address(const ZamThread *thread, ZamWord location);

// zam_thread_frame_base is the operand offset the innermost open call's frame
// begins at, which is what an OpPushVariable offset is measured from. It is zero
// when no call is open: that is the main body, which is fallen into rather than
// called and runs on the frame zam_vm_run lays at the bottom of the stack.
uint64_t zam_thread_frame_base(const ZamThread *thread);

// zam_thread_push_function opens a call and zam_thread_pop_function returns
// from the innermost one, rewinding both stacks to where it began and giving
// back the entry so the caller can be resumed at its return instruction.
void zam_thread_push_function(ZamThread *thread, uint64_t returnInstruction,
                              uint64_t operandPop);
ZamMarkFunction zam_thread_pop_function(ZamThread *thread);

// zam_thread_push_scope opens a lexical scope at the top of the operand stack,
// and zam_thread_pop_scope rewinds both stacks to where the innermost open one
// began.
void zam_thread_push_scope(ZamThread *thread);
void zam_thread_pop_scope(ZamThread *thread);

// zam_thread_push_visited opens a set over a structural comparison and
// zam_thread_pop_visited closes the innermost one. A descent that records
// pairs has to be bracketed by these, or it records into whatever set the last
// one left open.
void zam_thread_push_visited(ZamThread *thread);
void zam_thread_pop_visited(ZamThread *thread);

// zam_thread_visit says whether the pair has already been descended into by the
// innermost open comparison, recording it where it has not. True means the walk
// is back somewhere it has been and must answer rather than descend — equal for
// an equality, no order for a comparison, nothing further for a hash or a
// rendering — which is what makes a cyclic value terminate.
//
// Treating a pair met twice as equal is what Go's reflect.DeepEqual does and is
// the same approximation: it is not equality in general, since a walk could be
// cut short at a pair that only looks settled, but it agrees with equality on
// every finite value and it is an answer rather than a hang.
//
// It must not be asked outside a push, there being no set to record into, and
// neither pointer may be null — a null is not an instance and is settled before
// a descent, and a zero left is how an empty slot is told apart from a full one.
bool zam_thread_visit(ZamThread *thread, ZamWord left, ZamWord right,
                     ZamTypeShort type);

// zam_thread_run drives one thread until it stops, and is what a spawned thread
// is started on as much as what the main thread is run by.
ZamResult zam_thread_run(ZamThread *thread);

// zam_thread_install_signal_slot records the C signal handler on the OS side
// and binds this thread as the one that dispatches it. Storage for the
// closure pointer is on the thread itself.
void zam_thread_install_signal_slot(ZamThread *thread, int sig, void *closure);

// zam_thread_take_pending_signal returns the next signal this thread has
// pending, clearing the bit as part of the read, or -1 if nothing is
// pending. Called by the interpreter's poll between instructions.
int zam_thread_take_pending_signal(ZamThread *thread);

// zam_thread_bind_current records this thread as the one the C signal
// handler delivers to for the current OS thread. Called at the top of a
// thread's run, before any Signal.Handle registration could fire.
void zam_thread_bind_current(ZamThread *thread);
