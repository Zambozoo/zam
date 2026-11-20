package codegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base/target"
)

// harnessCache memoizes each compiled harness binary by its config, so a
// package-wide run compiles each one once rather than once per test.
//
// Entries live for the test binary's whole run — the binary itself sits in a
// package-level temp dir that the OS cleans up — so a later test that asks
// for the same harness gets back the path the first test already compiled to.
var (
	harnessCacheMu sync.Mutex
	harnessCache   = map[string]*harnessEntry{}
	harnessDir     string
	harnessDirOnce sync.Once
)

// harnessEntry holds one harness's build, guarded by a sync.Once so parallel
// tests that ask for the same binary wait for one compile rather than racing
// the compiler over the same output path.
type harnessEntry struct {
	once sync.Once
	path string
	err  error
}

// harnessSystemLinks are what a harness links from the system beyond the pack,
// by the host's OS.
var harnessSystemLinks = map[target.OS][]string{
	target.Linux: {"-lpthread", "-lm"},
	target.MacOS: {"-framework", "SystemConfiguration", "-framework", "CoreFoundation"},
	target.Windows: {
		"-lws2_32", "-lcrypt32", "-lbcrypt", "-ladvapi32", "-lsecur32", "-liphlpapi",
		"-luser32", "-lgdi32", "-lshell32", "-lntdll",
	},
}

// buildHarness compiles one of the readers in testdata against the runtime's
// own headers and sources, so what it reads with is what the runtime reads with
// rather than a copy that could be updated separately.
//
// The runtime sources a harness needs beyond the four every one of them reads
// with are named by the caller. A harness brings its own entry point.
func buildHarness(t *testing.T, source, name string, extra ...string) string {
	t.Helper()
	runtime := filepath.Join("..", "3_build", "runtime")
	sources := []string{
		filepath.Join("testdata", source),
		filepath.Join(runtime, "src", "definition.c"),
		filepath.Join(runtime, "src", "instance.c"),
		filepath.Join(runtime, "src", "image.c"),
		filepath.Join(runtime, "src", "common.c"),
	}
	for _, extraSrc := range extra {
		sources = append(sources, filepath.Join(runtime, "src", extraSrc))
	}

	// Opening the sources here is what puts them in each test's own cache key:
	// the compiler runs in a subprocess, so nothing it opens is visible to go
	// test, and a run whose only change was on the C side would be answered
	// from a previous one. Every test does this read even when the compile
	// itself is served from the package cache, so test caching invalidates for
	// every test alike when a .c or .h is touched.
	headers := append(headersIn(t, filepath.Join(runtime, "src")), headersIn(t, "testdata")...)
	for _, s := range append(sources, headers...) {
		_, err := os.ReadFile(s)
		require.NoError(t, err, "the harness cannot be held to a source it cannot read")
	}

	// Cache the compile by the tuple that decides it — the entry source, the
	// output name, and the extra runtime .c files — so two tests asking for
	// the same harness share one build.
	key := strings.Join(append([]string{source, name}, extra...), "|")

	harnessCacheMu.Lock()
	entry, ok := harnessCache[key]
	if !ok {
		entry = &harnessEntry{}
		harnessCache[key] = entry
	}
	harnessCacheMu.Unlock()

	entry.once.Do(func() {
		entry.path, entry.err = compileHarness(name, sources, runtime)
	})
	require.NoError(t, entry.err, "building the %s harness", name)
	return entry.path
}

// compileHarness runs zig cc for the host against its pack, with the flags the
// runtime is built with — plus the undefined behaviour sanitizer, which the
// runtime is not. These harnesses are the only place the C side is run outside
// a compiled program, so they are the only place UB can be caught at all, and
// the kind this catches is the kind no assertion would: an image is read in
// place, so a field reached at an address the type may not be read from is a
// bug that gives the right answer on x86-64 and arm64 and would fault
// elsewhere. Recovery is off so that the first one fails the test it is in
// rather than printing into output nothing reads.
func compileHarness(name string, sources []string, runtime string) (string, error) {
	host, pack, err := hostPack()
	if err != nil {
		return "", err
	}

	harnessDirOnce.Do(func() {
		harnessDir, _ = os.MkdirTemp("", "zam-harness-")
	})
	if harnessDir == "" {
		return "", fmt.Errorf("could not create a scratch directory for harness binaries")
	}

	harness := filepath.Join(harnessDir, host.Executable(name))
	args := append([]string{
		"-target", host.Zig,
		"-std=gnu11", "-Wall", "-Wextra", "-Wpedantic", "-Werror", "-O1",
		"-fsanitize=undefined", "-fno-sanitize-recover=all",
		"-I" + filepath.Join(runtime, "src"),
		"-I" + filepath.Join(runtime, "include"),
		"-o", harness,
	}, target.PackCFlags(pack)...)
	args = append(args, sources...)
	args = append(args, "-L"+filepath.Join(pack, "lib"), "-F"+filepath.Join(pack, "frameworks"))
	args = append(args, target.PackLibraries...)
	args = append(args, harnessSystemLinks[host.OS]...)
	cmd, err := zigCC(args...)
	if err != nil {
		return "", err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return harness, nil
}

// hostPack answers the host's target and its pack: under $ZAM_PACKS when it is
// set, else among the packs built into the repository.
func hostPack() (target.Target, string, error) {
	host, err := target.Host()
	if err != nil {
		return target.Target{}, "", err
	}
	root := os.Getenv("ZAM_PACKS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "bin", "packs")
	}
	pack, err := filepath.Abs(filepath.Join(root, host.Name))
	if err != nil {
		return target.Target{}, "", err
	}
	if _, err := os.Stat(filepath.Join(pack, "sources.txt")); err != nil {
		return target.Target{}, "", fmt.Errorf("a harness is built against the %s pack, which %s lacks: %w", host.Name, root, err)
	}
	return host, pack, nil
}

// zigCC answers zig cc over args, keeping Zig's caches in the system temp
// directory unless the environment names others.
func zigCC(args ...string) (*exec.Cmd, error) {
	zig, err := exec.LookPath("zig")
	if err != nil {
		return nil, fmt.Errorf("a harness cannot be built without zig: %w", err)
	}
	cmd := exec.Command(zig, append([]string{"cc"}, args...)...)
	cmd.Env = os.Environ()
	for _, variable := range []string{"ZIG_GLOBAL_CACHE_DIR", "ZIG_LOCAL_CACHE_DIR"} {
		if os.Getenv(variable) == "" {
			cmd.Env = append(cmd.Env, variable+"="+filepath.Join(os.TempDir(), "zam-test-zig"))
		}
	}
	return cmd, nil
}

// headersIn is every header beside the sources, which the compiler reads but
// never names on the command line.
func headersIn(t *testing.T, directory string) []string {
	t.Helper()

	headers, err := filepath.Glob(filepath.Join(directory, "*.h"))
	require.NoError(t, err)
	return headers
}
