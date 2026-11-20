// Package target names the platforms zam compiles native programs for, each of
// which a target pack is built for.
package target

import (
	"fmt"
	"runtime"
	"strings"
)

// OS is the operating system a target runs on.
type OS string

const (
	// Linux targets link statically against musl.
	Linux OS = "linux"
	// MacOS targets link against libSystem and the system frameworks.
	MacOS OS = "macos"
	// Windows targets link against mingw-w64's C runtime and the Universal C
	// Runtime.
	Windows OS = "windows"
)

// MacOSMinimum is the oldest macOS a macOS target runs on.
const MacOSMinimum = "13.0"

// Target is a platform native programs are compiled for.
type Target struct {
	// Name is the target's name, as its pack is named.
	Name string
	OS   OS
	// Arch is the architecture as Zig and the packs name it.
	Arch string
	// Triple is the LLVM target triple objects are compiled for.
	Triple string
	// Zig is the -target Zig compiles C for the target with.
	Zig string
}

// targets are the targets packs are built for.
var targets = []Target{
	{Name: "aarch64-linux-musl", OS: Linux, Arch: "aarch64", Triple: "aarch64-unknown-linux-musl", Zig: "aarch64-linux-musl"},
	{Name: "x86_64-linux-musl", OS: Linux, Arch: "x86_64", Triple: "x86_64-unknown-linux-musl", Zig: "x86_64-linux-musl"},
	{Name: "aarch64-macos", OS: MacOS, Arch: "aarch64", Triple: "arm64-apple-macosx" + MacOSMinimum, Zig: "aarch64-macos." + MacOSMinimum},
	{Name: "x86_64-macos", OS: MacOS, Arch: "x86_64", Triple: "x86_64-apple-macosx" + MacOSMinimum, Zig: "x86_64-macos." + MacOSMinimum},
	{Name: "aarch64-windows-gnu", OS: Windows, Arch: "aarch64", Triple: "aarch64-pc-windows-gnu", Zig: "aarch64-windows-gnu"},
	{Name: "x86_64-windows-gnu", OS: Windows, Arch: "x86_64", Triple: "x86_64-pc-windows-gnu", Zig: "x86_64-windows-gnu"},
}

// archs are the architectures of each GOARCH zam runs on.
var archs = map[string]string{"arm64": "aarch64", "amd64": "x86_64"}

// oses are the operating systems of each GOOS zam runs on.
var oses = map[string]OS{"linux": Linux, "darwin": MacOS, "windows": Windows}

// Names answers the name of every target.
func Names() []string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return names
}

// Parse answers the target called name.
func Parse(name string) (Target, error) {
	for _, t := range targets {
		if t.Name == name {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("unknown target %q, expected one of %s", name, strings.Join(Names(), ", "))
}

// Host answers the target of the machine zam runs on.
func Host() (Target, error) {
	for _, t := range targets {
		if t.OS == oses[runtime.GOOS] && t.Arch == archs[runtime.GOARCH] {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("native code is not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
}

// Executable answers the file name of an executable for t called name: with
// .exe added on Windows.
func (t Target) Executable(name string) string {
	if t.OS == Windows && !strings.HasSuffix(name, ".exe") {
		return name + ".exe"
	}
	return name
}

// IsHost reports whether t is the target of the machine zam runs on.
func (t Target) IsHost() bool {
	host, err := Host()
	return err == nil && host == t
}
