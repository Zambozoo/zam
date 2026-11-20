//go:build !zam_static

package lld

// #cgo CPPFLAGS: -I/usr/lib/llvm-23/include -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
// #cgo CXXFLAGS: -std=c++17
// #cgo LDFLAGS: -L/usr/lib/llvm-23/lib -Wl,-rpath,/usr/lib/llvm-23/lib
// #cgo LDFLAGS: -llldMachO -llldELF -llldMinGW -llldCOFF -llldCommon -lLLVM -lz -lzstd -lstdc++
import "C"
