//go:build !zam_static

package lld

// #cgo CPPFLAGS: -I/opt/homebrew/opt/lld/include -I/opt/homebrew/opt/llvm/include -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
// #cgo CXXFLAGS: -std=c++17
// #cgo LDFLAGS: -L/opt/homebrew/opt/lld/lib -L/opt/homebrew/opt/llvm/lib -Wl,-rpath,/opt/homebrew/opt/lld/lib -Wl,-rpath,/opt/homebrew/opt/llvm/lib
// #cgo LDFLAGS: -llldMachO -llldELF -llldMinGW -llldCOFF -llldCommon -lLLVM -lc++
import "C"
