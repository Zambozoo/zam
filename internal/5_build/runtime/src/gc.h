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
// Mark bodies are run on collector, between its instructions, and a major
// collection's also on the helpers marking alongside it.
bool zam_gc_collect(ZamVM *vm, ZamThread *collector, bool major);

// zam_gc_begin is the first stop of a concurrent major collection: it leaves
// both generations unreached, cleans the cards so that every store from here
// on is seen, and marks what the threads and statics hold without tracing it.
// Every thread but collector is stopped.
void zam_gc_begin(ZamThread *collector);

// zam_gc_mark_concurrently traces on the marker and its helpers, alongside the
// program, until there is nothing left to trace or the remark is asked for. An
// allocation whose marking runs a Mark of the program's own is left for the
// remark. The cards the program dirtied meanwhile are scanned and cleaned as
// it runs, so the remark scans only those dirtied since.
void zam_gc_mark_concurrently(ZamThread *marker);

// zam_gc_request_finish asks the markers to stop tracing alongside the program
// and leave what is left to the remark.
void zam_gc_request_finish(ZamVM *vm);

// zam_gc_pause_markers stops every marker at its next checkpoint, and
// zam_gc_resume_markers sets them going again.
void zam_gc_pause_markers(ZamVM *vm);
void zam_gc_resume_markers(ZamVM *vm);

// zam_gc_stop_helpers ends the helper threads, which no collection may be
// using.
void zam_gc_stop_helpers(ZamVM *vm);

// zam_gc_collect_fresh is a minor collection of what was allocated while the
// marker runs, with every thread stopped and every marker paused. What
// survives stays grey for the remark.
void zam_gc_collect_fresh(ZamThread *collector);

// zam_gc_remark ends the marking of a concurrent major collection with every
// thread but collector stopped. Whatever the program could have hidden a
// reference in while the marker ran is marked again: the threads and statics
// and every dirty card. Every Mark of the program's own runs here, once, for
// everything reached. Then it finalizes, and sets aside what is to be swept.
void zam_gc_remark(ZamThread *collector);

// zam_gc_sweep_concurrently sweeps, on the marker and alongside the program,
// what the remark set aside, and gives back to the system what has stayed
// free. No collection runs until it is done.
void zam_gc_sweep_concurrently(ZamThread *marker);

// zam_gc_allocated is told of every allocation the collector makes, which is
// reached already. One made while marking, by a Mark, is traced as well, since
// it may hold what nothing else does; one a Delete makes is only kept.
void zam_gc_allocated(ZamThread *thread, void *object);

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
