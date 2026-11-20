//go:build zam_static

package llvm

// A static build takes LLVM's include path and libraries from CGO_CPPFLAGS and
// CGO_LDFLAGS, as build/release/zam.sh sets them.

// #cgo CPPFLAGS: -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
// #cgo CXXFLAGS: -std=c++17
import "C"
