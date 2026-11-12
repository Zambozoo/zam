#pragma once

#include "thread.h"
#include "vm.h"

// zam_gc_collect marks everything the roots reach, runs the Delete of every
// unreachable instance nothing else unreachable reaches, and frees what is
// left unreachable. Every thread but collector is stopped.
//
// A minor collection does all of that over the young generation alone, taking
// the old generation's dirty cards as roots, and promotes what survives. A
// major one does it over both. It answers whether the collection was major,
// which verification makes every one.
//
// Marking runs every reached instance's Mark: Object's own walks the
// instance's members, and an override walks whatever it says to instead.
// Mark bodies are run on collector, between its instructions.
bool zam_gc_collect(ZamVM *vm, ZamThread *collector, bool major);

// zam_gc_deletes_custom reports whether an allocation of the kind and type has
// a Delete of the program's own, which decides the list it is kept on.
bool zam_gc_deletes_custom(ZamVM *vm, ZamObjectKind kind, ZamTypeShort shortID);

// zam_gc_collecting reports whether the thread is running a collection, which
// is when mark(x) and Object's Mark mean anything.
bool zam_gc_collecting(const ZamThread *thread);

// zam_gc_mark_value says the value of the type at a location is reachable: a
// reference is queued, and a value laid out inline is walked where it lies.
void zam_gc_mark_value(ZamThread *thread, ZamWord location, ZamTypeID type);

// zam_gc_mark_members walks the members of the instance at a location, which
// is what Object's Mark does.
void zam_gc_mark_members(ZamThread *thread, ZamWord instance);

// zam_gc_mark_only says the instance a pointer names is reachable without
// tracing anything it holds, which is what Objs.Mark does.
void zam_gc_mark_only(ZamThread *thread, ZamWord instance);
