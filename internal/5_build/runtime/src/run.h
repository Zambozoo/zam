#pragma once

#include "definition.h"
#include "thread.h"
#include "vm.h"

// What the runtime answers about values and runs on behalf of compiled code
// and the collector, which walk the same layouts and run the same methods.

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

// zam_thread_push_value pushes the value of type at words onto the thread's
// operand stack, rooted there.
void zam_thread_push_value(ZamThread *thread, ZamTypeID type, const ZamWord *words);

// zam_thread_join waits for target to finish, in a safe region.
void zam_thread_join(ZamThread *thread, ZamThread *target);

// zam_value_words is how many operand words a value of the type stands on.
uint64_t zam_value_words(const ZamImage *image, ZamTypeID type);

// zam_type_stamp writes the headers of a fresh, zeroed run of elementCount
// values of the type at object, as new would. A markable run is stamped as
// contained by whatever lies backDistance before it, or by nothing when the
// distance is zero.
void zam_type_stamp(const ZamImage *image, void *object, ZamTypeID element,
                    uint64_t elementCount, uint64_t backDistance);

// zam_thread_new_instance allocates one class instance of the type for the
// collector, as new would, with its headers written and its initializer not
// yet run.
void *zam_thread_new_instance(ZamThread *thread, ZamTypeID element);

// zam_thread_allocate allocates elementCount zeroed, stamped values of type's
// element, as new does before it runs an initializer: a run new is told to
// leave to the program, or of a type the collector does not visit, comes from
// memory the collector does not sweep.
void *zam_thread_allocate(ZamThread *thread, ZamTypeID type, uint64_t elementCount, bool manual);

// zam_value_free releases what pointer, of the pointer type type, points
// at: raw memory back to the heap, a collected allocation marked dead.
void zam_value_free(ZamThread *thread, ZamTypeID type, ZamWord pointer);

// zam_string_concat allocates the string left then right.
void *zam_string_concat(ZamThread *thread, const void *left, const void *right);

// zam_instance_type is the type of the instance at instance, read off its
// header.
ZamTypeID zam_instance_type(const void *instance);

// zam_interface_method_index is the index, among concrete's methods, of the
// methodIndex'th method of interfaceType, panicking when concrete implements
// none.
uint32_t zam_interface_method_index(const ZamImage *image, ZamTypeShort concrete,
                                    ZamTypeShort interfaceType, uint32_t methodIndex);

// zam_any_box allocates an any holding a copy of the value of payloadType laid
// out at value, zam_any_type is the type the any at anyBox holds, and
// zam_any_unbox lays what it holds out at out as a value of target, panicking
// unless what it holds is of a subtype of target. Both panic on a null any.
void *zam_any_box(ZamThread *thread, ZamTypeID payloadType, const void *value);
ZamTypeID zam_any_type(const void *anyBox);
void zam_any_unbox(ZamThread *thread, const void *anyBox, ZamTypeID target, void *out);

// zam_value_eq, zam_value_cmp, zam_value_hash and zam_value_str are
// equality, order, hashing and the string form of values laid out at the
// addresses given as a frame lays them out, calling a type's own Eq, Cmp, Hash
// or Str where it has one.
bool zam_value_eq(ZamThread *thread, ZamTypeID leftType, const void *left,
                  ZamTypeID rightType, const void *right);
ZamInt zam_value_cmp(ZamThread *thread, ZamTypeID leftType, const void *left,
                     ZamTypeID rightType, const void *right);
ZamWord zam_value_hash(ZamThread *thread, ZamTypeID type, const void *value);
ZamString *zam_value_str(ZamThread *thread, ZamTypeID type, const void *value);

// zam_value_stamp writes the headers a fresh value of type laid out at value
// carries, as one standing in a frame: a class instance's, contained by
// nothing, and its inline members'; a tailed struct's tail length.
void zam_value_stamp(ZamThread *thread, ZamTypeID type, void *value);

// zam_value_delete runs the Delete of the instance the value of type at value
// is or points at, doing nothing for a null pointer or a value that answers
// through no instance.
void zam_value_delete(ZamThread *thread, ZamTypeID type, const void *value);

// zam_value_mark says the value of type at value is reachable when the thread
// is collecting, and does nothing otherwise.
void zam_value_mark(ZamThread *thread, ZamTypeID type, const void *value);

// zam_type_is_subtype reports whether a value of child may stand where parent
// is asked for.
bool zam_type_is_subtype(const ZamImage *image, ZamTypeID child, ZamTypeID parent);

// zam_thread_initialize runs the type's compiled initializer over a stamped
// run of elementCount values at object.
void zam_thread_initialize(ZamThread *thread, void *object, ZamTypeID element,
                           uint64_t elementCount);

// zam_thread_call_method runs one of Object's methods on the instance at a
// location, through the method table of the instance's own type, to its
// return. The location is an address or, for an instance on the thread's own
// stack, a frame location.
void zam_thread_call_method(ZamThread *thread, ZamWord receiver, uint64_t slot);

