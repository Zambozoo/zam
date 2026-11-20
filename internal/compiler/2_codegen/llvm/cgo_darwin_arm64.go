//go:build !zam_static

package llvm

// #cgo CPPFLAGS: -I/opt/homebrew/opt/llvm/include -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
// #cgo CXXFLAGS: -std=c++17
// #cgo LDFLAGS: -L/opt/homebrew/opt/llvm/lib -Wl,-rpath,/opt/homebrew/opt/llvm/lib -lLLVM -lc++
import "C"
