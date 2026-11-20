// Package build drives the per-project link: it compiles the embedded runtime
// once into a cache for each target, compiles the project's own natives, and
// links them with the program's object into a single executable. No runtime
// binary has to be installed separately.
package build

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	zamruntime "github.com/zambozoo/zam/internal/5_build/runtime"
	"github.com/zambozoo/zam/internal/target"
)

// runtimeEmbedPrefix is where the runtime tree sits inside zamruntime.FS: at
// its root.
const runtimeEmbedPrefix = "."

// Options is everything a link needs beside the program's object: where to put
// the binary, where to look for the project's own native sources, and the
// target it is for.
type Options struct {
	// OutputPath is where the linked binary is written.
	OutputPath string
	// Project is the project being compiled, read for its native.files and
	// native.links. These are resolved relative to Project.Path.
	Project *syntax.Project
	// Debugger has a native link on macOS write the program's DWARF into a
	// .dSYM bundle beside the binary, for a debugger to read.
	Debugger bool
	// Output is where the C compiler's output goes, the standard streams if
	// nil.
	Output io.Writer
	// RuntimeCacheDir is where the compiled runtime is cached, under
	// $ZAM_CACHE_DIR or else the user's cache directory when empty.
	RuntimeCacheDir string
	// Target is the platform a link is for, the host when unset.
	Target target.Target
}

// extractRuntime copies the embedded runtime tree into dir and returns the
// absolute paths of the .c files that should be fed to the compiler. The headers are extracted alongside them so each .c
// can include "common.h" and <zam/native.h> as it does in the source tree.
func extractRuntime(dir string) ([]string, error) {
	var sources []string
	err := fs.WalkDir(zamruntime.FS, runtimeEmbedPrefix, func(entryPath string, entry fs.DirEntry, walkErr error) error {
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

		bytes, err := zamruntime.FS.ReadFile(entryPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, bytes, 0644); err != nil {
			return err
		}

		if strings.HasSuffix(entry.Name(), ".c") {
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
		if _, statErr := os.Stat(string(path)); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, fmt.Errorf("native source %q not found at %s", file, string(path))
			}
			return nil, statErr
		}
		sources = append(sources, string(path))
	}
	return sources, nil
}
