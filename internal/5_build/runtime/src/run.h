#pragma once

#include "definition.h"
#include "thread.h"
#include "vm.h"

// What the interpreter answers about values and runs on behalf of the
// collector, which walks the same layouts the instructions do and runs the
// same methods.

// zam_type_is_markable_reference reports whether the one word a value of the
// type is names something with a mark word the collector owns.
bool zam_type_is_markable_reference(const ZamImage *image, ZamTypeID type);

// zam_type_contains_markable reports whether a value of the type holds
// anything the collector must visit wherever it lies.
bool zam_type_contains_markable(ZamVM *vm, ZamTypeID type);

// zam_type_is_class reports whether the declaration is a class or an abstract,
// generic or not: the kinds whose instances carry a method table.
bool zam_type_is_class(const ZamImage *image, ZamTypeShort shortID);

// zam_value_header_bytes is how far into a value of the type laid out inline
// its members begin.
uint64_t zam_value_header_bytes(const ZamImage *image, ZamTypeID type);

// zam_value_words is how many operand words a value of the type stands on.
uint64_t zam_value_words(const ZamImage *image, ZamTypeID type);

// zam_thread_call_method runs one of Object's methods on the instance at a
// location, through the method table of the instance's own type, to its
// return. The location is an address or, for an instance on the thread's own
// stack, a frame location.
ZamResult zam_thread_call_method(ZamThread *thread, ZamWord receiver,
                                 uint64_t slot);
