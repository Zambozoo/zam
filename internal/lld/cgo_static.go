//go:build zam_static

package lld

// A static build takes the include paths and libraries of LLVM and lld from
// CGO_CPPFLAGS and CGO_LDFLAGS, as release/zam.sh sets them.

// #cgo CPPFLAGS: -D__STDC_CONSTANT_MACROS -D__STDC_FORMAT_MACROS -D__STDC_LIMIT_MACROS
// #cgo CXXFLAGS: -std=c++17
import "C"
