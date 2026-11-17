package llvm

/*
#include "llvm-c/Core.h"
#include "llvm-c/DebugInfo.h"
#include <stdlib.h>
*/
import "C"

import "unsafe"

// ModuleFlagBehavior is how a module flag merges with the same flag of
// another module when the two are linked.
type ModuleFlagBehavior C.LLVMModuleFlagBehavior

const (
	ModuleFlagBehaviorError    ModuleFlagBehavior = C.LLVMModuleFlagBehaviorError
	ModuleFlagBehaviorWarning  ModuleFlagBehavior = C.LLVMModuleFlagBehaviorWarning
	ModuleFlagBehaviorOverride ModuleFlagBehavior = C.LLVMModuleFlagBehaviorOverride
)

// DebugMetadataVersion is the version of debug metadata this LLVM reads, which
// the "Debug Info Version" module flag must name for debug info to be kept.
func DebugMetadataVersion() uint32 {
	return uint32(C.LLVMDebugMetadataVersion())
}

// AddModuleFlag adds the module flag key with value, merged as behavior says.
func (m Module) AddModuleFlag(behavior ModuleFlagBehavior, key string, value Metadata) {
	ckey := C.CString(key)
	defer C.free(unsafe.Pointer(ckey))
	C.LLVMAddModuleFlag(m.C, C.LLVMModuleFlagBehavior(behavior), ckey, C.size_t(len(key)), value.C)
}

// ValueAsMetadata wraps v as metadata.
func ValueAsMetadata(v Value) (md Metadata) {
	md.C = C.LLVMValueAsMetadata(v.C)
	return
}

// InsertDeclareAtEnd says, at the end of bb, that the variable diVarInfo lies
// at storage, as expr locates it from there.
func (d *DIBuilder) InsertDeclareAtEnd(storage Value, diVarInfo, expr Metadata, l DebugLoc, bb BasicBlock) {
	loc := C.LLVMDIBuilderCreateDebugLocation(
		d.m.Context().C, C.uint(l.Line), C.uint(l.Col), l.Scope.C, l.InlinedAt.C)
	C.LLVMDIBuilderInsertDeclareRecordAtEnd(d.ref, storage.C, diVarInfo.C, expr.C, loc, bb.C)
}

// DIEnumerator is one named value of an enumeration type.
type DIEnumerator struct {
	Name  string
	Value int64
}

// DIEnumerationType holds the values for creating enumeration type debug
// metadata.
type DIEnumerationType struct {
	Name        string
	File        Metadata
	Line        int
	SizeInBits  uint64
	AlignInBits uint32
	Elements    []DIEnumerator
	ClassType   Metadata
}

// CreateEnumerationType creates enumeration type debug metadata, whose values
// are its elements.
func (d *DIBuilder) CreateEnumerationType(scope Metadata, t DIEnumerationType) Metadata {
	elements := make([]Metadata, len(t.Elements))
	for i, e := range t.Elements {
		name := C.CString(e.Name)
		elements[i] = Metadata{C: C.LLVMDIBuilderCreateEnumerator(d.ref, name, C.size_t(len(e.Name)), C.int64_t(e.Value), 0)}
		C.free(unsafe.Pointer(name))
	}
	ptr, length := llvmMetadataRefs(elements)
	name := C.CString(t.Name)
	defer C.free(unsafe.Pointer(name))
	return Metadata{C: C.LLVMDIBuilderCreateEnumerationType(d.ref, scope.C, name, C.size_t(len(t.Name)),
		t.File.C, C.unsigned(t.Line), C.uint64_t(t.SizeInBits), C.uint32_t(t.AlignInBits),
		ptr, C.unsigned(length), t.ClassType.C)}
}
