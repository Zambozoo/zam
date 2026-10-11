#pragma once

#include <pthread.h>

#include "common.h"
#include "zam/native.h"
#include "image.h"
#include "instance.h"
#include "thread.h"

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

    // allocations is the head of every allocation the collector owns, threaded
    // through the upvalue field of each one's mark word. It costs no memory
    // beyond the word a markable allocation already carries, and it is the half
    // of a sweep the roots cannot supply: an allocation nothing refers to is
    // exactly the one to be freed, and it has to be findable to be freed.
    //
    // Only the heap is on it. A markable value laid out on the operand stack
    // carries the same header — OpDefaultValue writes one — but is not an
    // allocation and is never linked, its storage being the stack's to reclaim.
    // That is what makes the link a reliable answer to "did the allocator hand
    // this out", rather than only to "does this have a mark word".
    void *allocations;

    // allocatedByteCount is what the heap holds, and nextCollectionByteCount is
    // what it has to reach for a collection to be asked for. The ratio between
    // them is the program's to set, and comes in on the image.
    uint64_t allocatedByteCount;
    uint64_t nextCollectionByteCount;
    double gcTriggerRatio;

    // collectionRequested is set by whichever thread crossed the threshold and
    // read by the loop, which is the only place a collection may actually
    // happen: a thread mid-instruction is a thread whose stacks do not yet say
    // what they will.
    bool collectionRequested;
    pthread_mutex_t collectionLock;
    pthread_mutex_t threadLock;

    // main is the thread control begins on and the head of the list of every
    // live thread. That list is what the collector walks to find every root, so
    // a thread not reachable from here is a thread whose stack is not scanned.
   ZamThread main;
} ZamVM;

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
// Every allocation is threaded onto vm->allocations as it is made, so the heap
// is walkable from the VM alone. Nothing is freed yet — zam_vm_collect is still
// a stub — but the list a sweep needs is now kept rather than deferred.
void *zam_vm_alloc(ZamVM *vm, ZamThread *thread, ZamObjectKind kind,
                  ZamTypeShort shortID, uint64_t elementCount,
                   uint64_t objectBytes);

// zam_vm_collect is the collection itself, run from the loop between
// instructions and never from inside one.
void zam_vm_collect(ZamVM *vm);

// zam_natives_resolve binds every name the image carries to a function in the
// table this interpreter was built with. It lives in native.c beside that
// table.
void zam_natives_resolve(ZamVM *vm);
