package build

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
	"github.com/zambozoo/zam/internal/lld"
	"github.com/zambozoo/zam/internal/target"
)

// cacheVariable names a directory zam keeps its caches in, in place of the
// user's cache directory.
const cacheVariable = "ZAM_CACHE_DIR"

// cacheDir is where zam keeps its caches: $ZAM_CACHE_DIR, else under the
// user's cache directory.
func cacheDir() (string, error) {
	if dir := os.Getenv(cacheVariable); dir != "" {
		return dir, nil
	}
	userCache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot find a cache directory: %w", err)
	}
	return filepath.Join(userCache, "zam"), nil
}

// cachedDirs are the directories of zam's cache that zam fills: target packs,
// and compiled runtimes with Zig's cache beside them.
var cachedDirs = []string{"packs", "runtime"}

// CleanCache removes what zam keeps in its cache: the target packs it
// downloaded and the runtimes it compiled. It answers the directories removed.
func CleanCache() ([]string, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	removed := make([]string, len(cachedDirs))
	for i, name := range cachedDirs {
		removed[i] = filepath.Join(dir, name)
		if err := base.RemoveReadOnly(removed[i]); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

// runtimeCacheDir is where the compiled runtime archive is cached: as opts
// says, else in zam's cache.
func runtimeCacheDir(opts Options) (string, error) {
	if opts.RuntimeCacheDir != "" {
		return opts.RuntimeCacheDir, nil
	}
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime"), nil
}

// platform is the target opts link for: opts.Target, the host when unset.
func platform(opts Options) (target.Target, error) {
	if opts.Target.Name != "" {
		return opts.Target, nil
	}
	return target.Host()
}

// ExecutablePath answers path as the name of an executable for the target
// opts link for: with .exe added for Windows.
func ExecutablePath(opts Options, path string) (string, error) {
	t, err := platform(opts)
	if err != nil {
		return "", err
	}
	return t.Executable(path), nil
}

// LinkObject links a compiled program's object into an executable at
// opts.OutputPath for opts.Target, the host when unset, against the runtime
// and the libraries of the target's pack.
func LinkObject(object []byte, opts Options) error {
	t, err := platform(opts)
	if err != nil {
		return err
	}
	pack, err := Pack(t)
	if err != nil {
		return err
	}
	cacheDir, err := runtimeCacheDir(opts)
	if err != nil {
		return err
	}
	archive, err := RuntimeArchive(cacheDir, t, pack)
	if err != nil {
		return err
	}
	outputPath, err := filepath.Abs(opts.OutputPath)
	if err != nil {
		return fmt.Errorf("cannot resolve output path %q: %w", opts.OutputPath, err)
	}

	dir, err := os.MkdirTemp("", "zam-link-*")
	if err != nil {
		return fmt.Errorf("cannot create link directory: %w", err)
	}
	defer os.RemoveAll(dir)
	objectPath := filepath.Join(dir, "program.o")
	if err := os.WriteFile(objectPath, object, 0o600); err != nil {
		return err
	}
	projectObjects, err := compileProjectNatives(opts, dir, newZig(cacheDir, t, pack), filepath.Dir(archive))
	if err != nil {
		return err
	}

	inputs := append(append([]string{objectPath}, projectObjects...), archive)
	var links []string
	if opts.Project != nil {
		links = opts.Project.Native.Links
	}
	if err := linkers[t.OS](t, pack, outputPath, inputs, links, opts.Debugger); err != nil {
		return err
	}
	// An ELF executable carries its own DWARF.
	if !opts.Debugger || t.OS != target.MacOS {
		return nil
	}
	// The executable only names the object its DWARF lies in, which goes with
	// the link directory, so a debugger is given a bundle of its own beside it.
	if out, err := exec.Command("dsymutil", outputPath).CombinedOutput(); err != nil {
		return fmt.Errorf("dsymutil failed: %w\n%s", err, out)
	}
	return nil
}

// linkers link an executable for a target at output from inputs, the objects
// and archives of the program, against the target's pack and links, the
// project's own linker flags, with debug information only when debuggable.
var linkers = map[target.OS]func(t target.Target, pack, output string, inputs, links []string, debuggable bool) error{
	target.MacOS:   linkMachO,
	target.Linux:   linkELF,
	target.Windows: linkCOFF,
}

// machOArchs are the -arch names of each architecture.
var machOArchs = map[string]string{"aarch64": "arm64", "x86_64": "x86_64"}

// linkMachO links a macOS executable against the pack's libSystem and
// framework stubs, which name what the system provides.
func linkMachO(t target.Target, pack, output string, inputs, links []string, debuggable bool) error {
	args := []string{
		"-arch", machOArchs[t.Arch],
		"-platform_version", "macos", target.MacOSMinimum, target.MacOSMinimum,
		"-Z", "-L", filepath.Join(pack, "lib"), "-F", filepath.Join(pack, "frameworks"),
		"-o", output,
		// Only the frameworks what the program calls needs are kept.
		"-dead_strip_dylibs",
	}
	if !debuggable {
		args = append(args, "-S")
	}
	args = append(args, inputs...)
	args = append(args, target.PackLibraries...)
	args = append(args, links...)
	args = append(args, "-lSystem", "-lcompiler_rt",
		"-framework", "SystemConfiguration", "-framework", "CoreFoundation")
	return lld.Link(lld.MachO, args)
}

// linkELF links a static Linux executable against the pack's musl.
func linkELF(t target.Target, pack, output string, inputs, links []string, debuggable bool) error {
	libc := filepath.Join(pack, "libc", "lib")
	args := []string{
		"-static", "--eh-frame-hdr", "-o", output,
		filepath.Join(libc, "crt1.o"), filepath.Join(libc, "crti.o"),
		"-L", filepath.Join(pack, "lib"), "-L", libc,
	}
	if !debuggable {
		args = append(args, "--strip-debug")
	}
	args = append(args, inputs...)
	args = append(args, "--start-group")
	args = append(args, target.PackLibraries...)
	args = append(args, links...)
	args = append(args, "-lc", "-lcompiler_rt", "--end-group", filepath.Join(libc, "crtn.o"))
	return lld.Link(lld.ELF, args)
}

// linkCOFF links a Windows executable in lld's mingw mode against the pack's
// libraries, C runtime, start-up object and import libraries, with the flags
// its link.txt says Zig links with.
func linkCOFF(t target.Target, pack, output string, inputs, links []string, debuggable bool) error {
	flags, err := os.ReadFile(filepath.Join(pack, "link.txt"))
	if err != nil {
		return fmt.Errorf("the %s pack says nothing of how to link: %w", t.Name, err)
	}
	args := append([]string{"-OUT:" + output}, strings.Fields(string(flags))...)
	// Like an executable for any other target, it keeps its symbols, and its
	// DWARF only for a debugger.
	if debuggable {
		args = append(args, "-DEBUG:DWARF")
	} else {
		args = append(args, "-DEBUG:SYMTAB")
	}
	lib := filepath.Join(pack, "lib")
	startup, err := filepath.Glob(filepath.Join(lib, "*.obj"))
	if err != nil {
		return err
	}
	args = append(args, startup...)
	args = append(args, inputs...)
	for _, library := range target.PackLibraries {
		args = append(args, filepath.Join(lib, "lib"+strings.TrimPrefix(library, "-l")+".a"))
	}
	system, err := filepath.Glob(filepath.Join(lib, "*.lib"))
	if err != nil {
		return err
	}
	args = append(args, system...)
	args = append(args, links...)
	return lld.Link(lld.COFF, args)
}

// lldbFormatters is the lldb script zam's values are shown with.
//
//go:embed zam_lldb.py
var lldbFormatters []byte

// LLDBFormatters is the lldb script that shows zam's values: the text of a
// str, what an any holds and the function a closure runs.
func LLDBFormatters() []byte {
	return lldbFormatters
}

// compileProjectNatives compiles the project's native files with Zig into dir,
// against the headers of the runtime staged in runtimeDir and of the pack, and
// answers the objects. Zig is only needed when there are files to compile.
func compileProjectNatives(opts Options, dir string, z *zig, runtimeDir string) ([]string, error) {
	sources, err := resolveProjectSources(opts.Project)
	if err != nil || len(sources) == 0 {
		return nil, err
	}
	if z == nil {
		return nil, errors.New("the project has native files to compile, which needs zig on PATH")
	}
	objects := make([]string, len(sources))
	for i, source := range sources {
		objects[i] = filepath.Join(dir, fmt.Sprintf("native%d.o", i))
		args := append(z.cflags(), "-c",
			"-I"+filepath.Join(runtimeDir, "include"), "-I"+filepath.Join(runtimeDir, "src"),
			"-o", objects[i], source)
		cmd := z.command("cc", args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if opts.Output != nil {
			cmd.Stdout, cmd.Stderr = opts.Output, opts.Output
		}
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("zig cc failed on %s: %w", source, err)
		}
	}
	return objects, nil
}
