package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/zambozoo/zam/internal/base/target"
	zamruntime "github.com/zambozoo/zam/internal/compiler/3_build/runtime"
)

// runtimeArchiveName is the file the runtime archive is written to inside its
// cache entry.
const runtimeArchiveName = "libzamrt.a"

// zig is the Zig toolchain, compiling C for a target against its pack.
type zig struct {
	path string
	// cacheDir is where Zig keeps its caches, unless the environment names
	// others and forced is false.
	cacheDir string
	forced   bool
	target   target.Target
	pack     string
}

// newZig answers Zig compiling for t against pack, keeping its caches in
// zigCacheDir over the environment's, or else under cacheDir unless the
// environment names others, or nil when zig is not on PATH.
func newZig(cacheDir, zigCacheDir string, t target.Target, pack string) *zig {
	path, err := exec.LookPath("zig")
	if err != nil {
		return nil
	}
	if zigCacheDir != "" {
		return &zig{path: path, cacheDir: zigCacheDir, forced: true, target: t, pack: pack}
	}
	return &zig{path: path, cacheDir: filepath.Join(cacheDir, "zig"), target: t, pack: pack}
}

// command answers Zig running tool over args.
func (z *zig) command(tool string, args ...string) *exec.Cmd {
	cmd := exec.Command(z.path, append([]string{tool}, args...)...)
	cmd.Env = os.Environ()
	for _, variable := range []string{"ZIG_GLOBAL_CACHE_DIR", "ZIG_LOCAL_CACHE_DIR"} {
		if z.forced || os.Getenv(variable) == "" {
			cmd.Env = append(cmd.Env, variable+"="+z.cacheDir)
		}
	}
	return cmd
}

// cflags are the flags C is compiled with for the target, against the pack's
// headers.
func (z *zig) cflags() []string {
	return append([]string{"-target", z.target.Zig, "-std=gnu11", "-O3", "-fPIC"}, target.PackCFlags(z.pack)...)
}

// RuntimeArchive answers the path of the runtime compiled for t into a static
// archive under cacheDir, compiling it with Zig against pack the first time it
// is asked for. The entry is keyed by the runtime's sources, Zig, the pack and
// the flags, so a change to any of them builds a fresh archive beside the old
// one. Without Zig, the pack's own prebuilt runtime is answered.
func RuntimeArchive(cacheDir string, t target.Target, pack string) (string, error) {
	return runtimeArchive(cacheDir, "", t, pack)
}

// runtimeArchive is [RuntimeArchive] with Zig keeping its caches as
// [newZig] says.
func runtimeArchive(cacheDir, zigCacheDir string, t target.Target, pack string) (string, error) {
	z := newZig(cacheDir, zigCacheDir, t, pack)
	if z == nil {
		prebuilt := filepath.Join(pack, "lib", runtimeArchiveName)
		if _, err := os.Stat(prebuilt); err != nil {
			return "", fmt.Errorf("zig is not on PATH to compile the runtime with, and the %s pack holds no prebuilt runtime", t.Name)
		}
		return prebuilt, nil
	}
	version, err := z.command("version").Output()
	if err != nil {
		return "", fmt.Errorf("zig version failed: %w", err)
	}
	sources, err := os.ReadFile(filepath.Join(pack, "sources.txt"))
	if err != nil {
		return "", err
	}

	key, err := runtimeArchiveKey(z.path+"\x00"+string(version)+"\x00"+string(sources), z.cflags())
	if err != nil {
		return "", err
	}
	entry := filepath.Join(cacheDir, key)
	archive := filepath.Join(entry, runtimeArchiveName)
	if _, err := os.Stat(archive); err == nil {
		return archive, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create runtime cache %q: %w", cacheDir, err)
	}
	staging, err := os.MkdirTemp(cacheDir, key+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("cannot stage runtime archive: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := compileRuntimeArchive(staging, z); err != nil {
		return "", err
	}
	if err := os.Rename(staging, entry); err != nil {
		// Another build finishing first leaves the same archive behind.
		if _, statErr := os.Stat(archive); statErr == nil {
			return archive, nil
		}
		return "", fmt.Errorf("cannot store runtime archive: %w", err)
	}
	return archive, nil
}

// compileRuntimeArchive compiles the runtime into dir with z and archives its
// objects as runtimeArchiveName there.
func compileRuntimeArchive(dir string, z *zig) error {
	sources, err := extractRuntime(dir)
	if err != nil {
		return err
	}
	flags := append(z.cflags(), "-I"+filepath.Join(dir, "include"), "-I"+filepath.Join(dir, "src"))

	objects := make([]string, len(sources))
	var group errgroup.Group
	group.SetLimit(runtime.GOMAXPROCS(0))
	for i, source := range sources {
		objects[i] = strings.TrimSuffix(source, ".c") + ".o"
		group.Go(func() error {
			args := append(append([]string{"-c"}, flags...), "-o", objects[i], source)
			if out, err := z.command("cc", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("zig cc failed on %s: %w\n%s", filepath.Base(source), err, out)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}

	args := append([]string{"rcs", filepath.Join(dir, runtimeArchiveName)}, objects...)
	if out, err := z.command("ar", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("zig ar failed: %w\n%s", err, out)
	}
	return nil
}

// runtimeArchiveKey hashes everything a runtime archive is built from: the
// compiler and pack, as identity says, the flags and the runtime's sources.
func runtimeArchiveKey(identity string, flags []string) (string, error) {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00", identity, strings.Join(flags, "\x00"))

	var paths []string
	err := fs.WalkDir(zamruntime.FS, runtimeEmbedPrefix, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, path := range paths {
		bytes, err := zamruntime.FS.ReadFile(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", path, len(bytes))
		hash.Write(bytes)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}
