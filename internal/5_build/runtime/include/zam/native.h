#pragma once

// This is the header a project's own C files are written against. It is the
// whole of what a native may assume about the runtime, and is deliberately
// narrow: everything a native does to the running program it does through the
// functions declared here.

#include <stdbool.h>
#include <stdint.h>

typedef struct ZamThread ZamThread;

// A native is a C function reached by name. The compiler writes the name into
// the image and the interpreter is built with a table of them, so a name that
// answers to nothing is caught when the two are put together rather than
// reported by a compiler that has never seen the C side.
//
// It is handed nothing but the thread. A native reads its arguments out of the
// frame the call built and leaves what it returns through that same frame, the
// same way a body compiled from Zamylene does — which is what lets a call to a
// native be a call like any other, with no instruction of its own.
//
// What a native takes and gives back is not written anywhere the compiler can
// check. The source says it by casting the address to a signature, and being
// wrong about that signature is not a thing the compiler can catch; the helpers
// below are the SDK's answer, and using them is how a native stays honest about
// its own arity.
typedef void (*ZamNativeFn)(ZamThread *thread);

//ZamNativeEntry binds a name to the function that answers to it. The names
// share C's flat namespace, so one name is one entry however many places in the
// program write it.
typedef struct ZamNativeEntry {
    const char *name;
   ZamNativeFn fn;
} ZamNativeEntry;

// The table the interpreter was built with. A project's natives are appended to
// this by the file the build generates from the project's `native:` block; what
// is here is the standard library's own.
extern const ZamNativeEntry zamNativeTable[];
extern const uint64_t zamNativeTableCount;

// The frame a native reads. Arguments were pushed by the caller in source
// order, so argument zero is deepest; a value laid out inline spans as many
// words as its layout, which is why these are counted in words and a native
// that takes a wide argument reads it as the run of words it is.
uint64_t zam_native_argument_words(ZamThread *thread);
uint64_t zam_native_argument(ZamThread *thread, uint64_t wordIndex);

// zam_native_argument_pointer reads a pointer argument and resolves it to a
// real address. Pointer slots sometimes hold the raw address and sometimes
// hold an offset into the current frame's operand stack — the compiler emits
// one or the other depending on where the pointer came from — and the runtime
// tells them apart by the top bit of the word. A native that treats a frame
// location as a plain address segfaults as soon as it dereferences it; this
// helper is the one safe way to read a pointer out of the frame.
void *zam_native_argument_pointer(ZamThread *thread, uint64_t wordIndex);

// zam_native_return leaves a value where the caller reads one, and
// zam_native_return_void leaves the caller the stack it had before the call.
// Either must run before the native returns to C, or the frame it was handed is
// still open when the dispatch loop sees it next — the opcode handler panics in
// that case rather than leaving the entry to shift every walk after it.
//
// markPointerBits names which words of the result are addresses the collector
// has to visit, as a bitmask with bit i set for word i. A result of plain
// numbers passes zero; one wholly made of pointers passes all ones. Only the
// low wordCount bits are read.
void zam_native_return(ZamThread *thread, const uint64_t *words,
                       uint64_t wordCount, uint64_t markPointerBits);
void zam_native_return_void(ZamThread *thread);

// zam_native_block_begin and zam_native_block_end bracket a call that may
// block — a read, a write, a wait on a lock — so that a collection need not
// wait for it to return. Between the two a native may fill a buffer of plain
// bytes it was handed, but must not read or write a reference or call any
// other function here, since a collection may be reading them. Read every
// argument before beginning, and write every result after ending.
//
// Blocking while the calling thread has frozen collection with GC.Block ends
// the run, since what the call waits on may be waiting on a collection.
void zam_native_block_begin(ZamThread *thread);
void zam_native_block_end(ZamThread *thread);

// zam_native_panic ends the run from inside a native, for the failures a native
// can see and the program cannot be handed back: there are no exceptions to
// throw and no Result to return.
_Noreturn void zam_native_panic(ZamThread *thread, const char *message);

// zam_native_new_string copies length bytes into a fresh string allocation and
// returns the one-word pointer the compiler holds a string by. The copy is
// taken, so the caller may free the source memory as soon as this returns. The
// result is tracked by the collector from the moment it exists and is not
// pushed anywhere here — a native that stores it in an out-param slot owned by
// a tracked lvalue has given the collector the one reference it needs.
uint64_t zam_native_new_string(ZamThread *thread, const char *bytes,
                               uint64_t length);
