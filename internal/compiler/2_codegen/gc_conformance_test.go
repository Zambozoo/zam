package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
)

// gcImage compiles a one-file program with a concurrent collector into an
// image file, answering its path.
func gcImage(t *testing.T, source string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte("name: \"gc\"\nversion: \"0.0.0\"\ndependencies:\n"), 0o600))
	entry := filepath.Join(dir, "Main.zam")
	require.NoError(t, os.WriteFile(entry, []byte(source), 0o600))

	files := &base.FileProvider{}
	cmd, err := cli.New([]string{"zam", "build", "-m", "16KB", "-N", "4KB", "--gc-concurrent=true", entry}, files)
	require.NoError(t, err)
	ast, errs := syntax.NewAST(cmd, files, base.NewPackageProvider(t.TempDir()))
	require.Empty(t, errs)
	require.Empty(t, semantic.ValidateAST(ast))
	result, err := GenerateCode(cmd, ast)
	require.NoError(t, err)

	image := filepath.Join(dir, "image")
	require.NoError(t, os.WriteFile(image, result.Image, 0o600))
	return image
}

// wholeRuntime is every runtime source a harness that runs a VM links, beside
// the readers buildHarness always brings.
var wholeRuntime = []string{
	"run.c", "thread.c", "vm.c", "gc.c", "heap.c", "native.c", "json.c", "yaml.c", "xml.c", "bson.c", "compress.c", "cbor.c", "archive.c", "net.c", "tls.c", "curl.c", "http.c", "crypto.c", "bcrypt.c", "blowfish.c", "pki.c", "time.c", "arena.c", "posit.c", "zamrt.c", "stdtables.c",
}

// runGCHarness runs one case of the collector harness over image, answering
// what it reported by name and the collector's statistics lines.
func runGCHarness(t *testing.T, name, image string) (map[string]string, []string) {
	t.Helper()
	return runVMHarness(t, "gc_harness.c", "gc", name, image)
}

// runVMHarness runs one case of a harness over a VM loaded from image,
// answering what it reported by name and what it wrote to stderr by line.
func runVMHarness(t *testing.T, source, harness, name, image string) (map[string]string, []string) {
	t.Helper()

	cmd := exec.Command(buildHarness(t, source, harness, wholeRuntime...), name, image)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())

	got := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, line)
		got[key] = value
	}
	return got, strings.Split(strings.TrimSpace(stderr.String()), "\n")
}

// TestGCHandsOverWhatAMinorPromotesAlongsideTheMarker steps a box allocated
// while the marker is paused through two minor collections alongside it: the
// first keeps it as a fresh survivor, the second promotes it and hands it to
// the marker, and the remark and sweep after keep it.
func TestGCHandsOverWhatAMinorPromotesAlongsideTheMarker(t *testing.T) {
	t.Parallel()
	image := gcImage(t, `namespace Main {
    class Box {
        var str name;
    }

    static var Box* held;

    fun void(uint, str...) main = (argC, argV) {
        return;
    }
}
`)

	got, stats := runGCHarness(t, "handOver", image)
	assert.Equal(t, map[string]string{
		"marking":        "1",
		"first.fresh":    "1",
		"first.old":      "0",
		"second.old":     "1",
		"second.marking": "1",
		"finished.old":   "1",
		"finished.held":  "1",
	}, got)

	var minors []string
	for _, line := range stats {
		if strings.Contains(line, " minor ") {
			minors = append(minors, line)
		}
	}
	require.Len(t, minors, 2, strings.Join(stats, "\n"))
	assert.Regexp(t, ` survived=[1-9][^\n]* promoted=0 [^\n]*alongside=1$`, minors[0])
	assert.Regexp(t, ` promoted=[1-9][^\n]*alongside=1$`, minors[1])
	assert.Regexp(t, ` concurrent pause=`, stats[len(stats)-1])
}

// TestGCMarksWhatCompiledFramesHold collects while a compiled frame holds one
// box in its slots and another behind a slot pointing at it, and again once
// the frame is left.
func TestGCMarksWhatCompiledFramesHold(t *testing.T) {
	t.Parallel()
	image := gcImage(t, `namespace Main {
    class Box {
        var str name;
    }

    static var Box* held;

    fun void(uint, str...) main = (argC, argV) {
        return;
    }
}
`)

	got, _ := runGCHarness(t, "frameRoots", image)
	assert.Equal(t, map[string]string{
		"held.inline":   "1",
		"held.indirect": "1",
		"held.loose":    "0",
		"left.inline":   "0",
		"left.indirect": "0",
	}, got)
}
