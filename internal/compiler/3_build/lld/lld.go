// Package lld links objects into executables with the lld linker built into
// zam.
package lld

// #include <stdlib.h>
// #include "lld.h"
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// Flavor is the object format a link produces.
type Flavor int

const (
	// ELF links Linux executables, and Windows ones in mingw mode.
	ELF Flavor = iota
	// MachO links macOS executables.
	MachO
	// COFF links Windows executables in MSVC mode.
	COFF
)

// driverNames are the names lld's own executables run under, which is how
// lld tells its flavors apart.
var driverNames = [...]string{ELF: "ld.lld", MachO: "ld64.lld", COFF: "lld-link"}

var (
	// mu serializes links, since lld keeps global state.
	mu sync.Mutex
	// unusable is set once lld has reported that it cannot run again.
	unusable bool
)

// ErrUnusable is returned once lld has failed in a way that leaves it unable
// to run again in this process.
var ErrUnusable = errors.New("lld cannot run again in this process")

// Link runs lld as flavor over args, the arguments its executable would take.
func Link(flavor Flavor, args []string) error {
	mu.Lock()
	defer mu.Unlock()
	// A child forked while lld holds its output open keeps it open until the
	// child execs, and running the output meanwhile fails with ETXTBSY.
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	if unusable {
		return ErrUnusable
	}

	argv := make([]*C.char, 0, len(args)+1)
	for _, arg := range append([]string{driverNames[flavor]}, args...) {
		cs := C.CString(arg)
		defer C.free(unsafe.Pointer(cs))
		argv = append(argv, cs)
	}

	var output *C.char
	var reusable C.int
	code := C.zam_lld_link(C.int(len(argv)), &argv[0], &output, &reusable)
	diagnostics := ""
	if output != nil {
		diagnostics = strings.TrimSpace(C.GoString(output))
		C.free(unsafe.Pointer(output))
	}
	unusable = reusable == 0
	if code != 0 {
		return fmt.Errorf("lld exited %d: %s", int(code), diagnostics)
	}
	return nil
}
