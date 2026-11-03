// Package build drives the per-project link: it stages the embedded runtime,
// writes a generated wrapper C file that bakes the bytecode image into the
// binary and names the project's natives, and shells out to cc. What comes
// out is a single executable the user can run; no bytecode file is left on
// disk and no runtime binary has to be installed separately.
package build

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

// runtimeEmbedPrefix is where the embedded runtime tree sits inside the
// embed.FS. The embed pattern names it, so the extractor walks from here
// rather than from the root of the FS.
const runtimeEmbedPrefix = "runtime"

// excludedRuntimeSources are the runtime .c files the build driver must not
// link against: the stock main.c carries main() and the empty project table,
// both of which the generated wrapper replaces.
var excludedRuntimeSources = map[string]bool{
	"main.c": true,
}

// Options is everything the driver needs that is not the image or the native
// name list: where to put the binary, where to look for the project's own
// native sources, and whether to keep the build directory around for the
// user to inspect.
type Options struct {
	// OutputPath is where the linked binary is written.
	OutputPath string
	// Project is the project being compiled, read for its native.files and
	// native.links. These are resolved relative to Project.Path.
	Project *syntax.Project
	// KeepBuildDir leaves the temporary build directory in place and prints
	// its path, so the generated wrapper and extracted runtime can be
	// inspected after a successful or failed compile.
	KeepBuildDir bool
}

// Build produces an executable at opts.OutputPath that embeds the given
// bytecode image and binds every nativeName to a C function the project
// ships. It is the one entry point of the package.
func Build(image []byte, nativeNames []string, opts Options) error {
	cc, err := resolveCC()
	if err != nil {
		return err
	}

	buildDir, err := os.MkdirTemp("", "zam-build-*")
	if err != nil {
		return fmt.Errorf("cannot create build directory: %w", err)
	}
	cleanup := func() {
		if opts.KeepBuildDir {
			fmt.Fprintf(os.Stderr, "kept build directory: %s\n", buildDir)
			return
		}
		_ = os.RemoveAll(buildDir)
	}
	defer cleanup()

	runtimeSrcDir := filepath.Join(buildDir, "src")
	runtimeIncludeDir := filepath.Join(buildDir, "include")
	runtimeCSources, err := extractRuntime(buildDir)
	if err != nil {
		return err
	}

	wrapperPath := filepath.Join(buildDir, "zam_wrapper.c")
	if err := writeWrapper(wrapperPath, image, nativeNames); err != nil {
		return err
	}

	projectSources, err := resolveProjectSources(opts.Project)
	if err != nil {
		return err
	}

	outputPath, err := filepath.Abs(opts.OutputPath)
	if err != nil {
		return fmt.Errorf("cannot resolve output path %q: %w", opts.OutputPath, err)
	}

	codecCFlags, codecLibs, err := CodecFlags()
	if err != nil {
		return err
	}

	args := []string{
		"-std=gnu11",
		"-O3",
		"-I" + runtimeIncludeDir,
		"-I" + runtimeSrcDir,
		"-o", outputPath,
	}
	args = append(args, codecCFlags...)
	args = append(args, runtimeCSources...)
	args = append(args, wrapperPath)
	args = append(args, projectSources...)
	args = append(args, "-lpthread", "-lm")
	args = append(args, codecLibs...)
	args = append(args, opts.Project.Native.Links...)

	cmd := exec.Command(cc, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", cc, err)
	}
	return nil
}

// codecPackages are the pkg-config names of the C libraries the codec natives
// wrap, every one of which each binary links.
var codecPackages = []string{
	"json-c", "yaml-0.1", "libxml-2.0", "bson2", "libcbor",
	"zlib", "liblz4", "libzstd", "libbrotlienc", "libbrotlidec",
	"openssl", "libcurl", "libllhttp", "libsodium", "libxcrypt",
}

// codecLinks are the codec libraries linked by name, having no pkg-config
// file where they ship: libarchive comes with macOS without one.
var codecLinks = []string{"-larchive"}

// codecKegs are where Homebrew keeps libraries it leaves off the default
// search path, curl and libxcrypt among them, so that the system's copy stays
// in charge.
var codecKegs = []string{
	"/opt/homebrew/opt/curl/lib/pkgconfig",
	"/usr/local/opt/curl/lib/pkgconfig",
	"/opt/homebrew/opt/libxcrypt/lib/pkgconfig",
	"/usr/local/opt/libxcrypt/lib/pkgconfig",
}

// codecPkgConfigPath is PKG_CONFIG_PATH with every keg that exists ahead of
// it, so a keg's library is preferred to the system's.
func codecPkgConfigPath() string {
	var dirs []string
	for _, dir := range codecKegs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	if existing := os.Getenv("PKG_CONFIG_PATH"); existing != "" {
		dirs = append(dirs, existing)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// CodecFlags asks pkg-config for the compile and link flags of the C
// libraries the codec natives wrap.
func CodecFlags() (cflags, libs []string, err error) {
	query := func(flag string) ([]string, error) {
		cmd := exec.Command("pkg-config", append([]string{flag}, codecPackages...)...)
		cmd.Env = append(os.Environ(), "PKG_CONFIG_PATH="+codecPkgConfigPath())
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("pkg-config cannot resolve the codec libraries %s: %w", strings.Join(codecPackages, ", "), err)
		}
		return strings.Fields(string(out)), nil
	}
	if cflags, err = query("--cflags"); err != nil {
		return nil, nil, err
	}
	if libs, err = query("--libs"); err != nil {
		return nil, nil, err
	}
	return cflags, append(libs, codecLinks...), nil
}

// resolveCC looks up the C compiler the driver will invoke. The $CC
// environment variable wins when set, matching the convention every build
// system that calls out to a C compiler already follows; otherwise cc, which
// is what the runtime's own Makefile uses. The lookup happens here so a
// missing compiler is reported before any temp directory or codegen work.
func resolveCC() (string, error) {
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "cc"
	}
	resolved, err := exec.LookPath(cc)
	if err != nil {
		return "", fmt.Errorf("C compiler %q not found in PATH (set $CC to override): %w", cc, err)
	}
	return resolved, nil
}

// extractRuntime copies the embedded runtime tree into dir and returns the
// absolute paths of the .c files that should be fed to the compiler, with
// main.c filtered out. The headers are extracted alongside them so each .c
// can include "common.h" and <zam/native.h> as it does in the source tree.
func extractRuntime(dir string) ([]string, error) {
	var sources []string
	err := fs.WalkDir(runtimeFS, runtimeEmbedPrefix, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(runtimeEmbedPrefix, entryPath)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		bytes, err := runtimeFS.ReadFile(entryPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, bytes, 0644); err != nil {
			return err
		}

		if strings.HasSuffix(entry.Name(), ".c") && !excludedRuntimeSources[entry.Name()] {
			sources = append(sources, target)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cannot stage runtime sources: %w", err)
	}
	return sources, nil
}

// resolveProjectSources resolves the project's native.files entries to
// absolute paths. Each entry is read as a path beside the project file, the
// same way an .zam import is read as a path beside the file it appears in.
func resolveProjectSources(project *syntax.Project) ([]string, error) {
	if project == nil {
		return nil, nil
	}

	sources := make([]string, 0, len(project.Native.Files))
	for _, file := range project.Native.Files {
		resolved := project.Path.Resolve(file)
		path, err := base.ParsePath(resolved)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve native source %q: %w", file, err)
		}
		if path.InZip() {
			return nil, fmt.Errorf("native source %q lives inside an archive, which the build driver cannot read from", file)
		}
		if _, statErr := os.Stat(path.Outer); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, fmt.Errorf("native source %q not found at %s", file, path.Outer)
			}
			return nil, statErr
		}
		sources = append(sources, path.Outer)
	}
	return sources, nil
}
