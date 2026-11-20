package build

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base/target"
)

// packArchive answers a pack archive holding files, named as a pack's are.
func packArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	zw, err := zstd.NewWriter(&archive)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	return archive.Bytes()
}

// release serves a release of version holding a pack for t, whose checksums
// say sum, or the pack's own when sum is empty, and counts the requests made
// of it.
func release(t *testing.T, version string, platform target.Target, sum string) (string, *atomic.Int32) {
	t.Helper()
	name := fmt.Sprintf("zam-pack-%s-%s.tar.zst", version, platform.Name)
	archive := packArchive(t, map[string]string{"sources.txt": "zlib 1.3.1\n", "lib/libz.a": "archive"})
	if sum == "" {
		hash := sha256.Sum256(archive)
		sum = hex.EncodeToString(hash[:])
	}
	var requests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/"+version+"/"+checksumsName, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprintf(w, "0000  zam-%s-linux-arm64.tar.gz\n%s  %s\n", version, sum, name)
	})
	mux.HandleFunc("/"+version+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL, &requests
}

// writableTempDir is t.TempDir, made writable again before it is removed, as
// an extracted pack is read-only.
func writableTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(file string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(file, 0o755)
			}
			return nil
		})
	})
	return dir
}

// A pack is downloaded from the release of zam's version the first time it is
// needed, checked against the release's checksums, and kept.
func TestCachedPackDownloadsOnce(t *testing.T) {
	t.Parallel()
	platform, err := target.Parse("x86_64-linux-musl")
	require.NoError(t, err)
	releases, requests := release(t, "v1.2.3", platform, "")
	root := writableTempDir(t)

	pack, err := cachedPack(root, releases, "v1.2.3", platform)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "v1.2.3", "x86_64-linux-musl"), pack)
	library, err := os.ReadFile(filepath.Join(pack, "lib", "libz.a"))
	require.NoError(t, err)
	assert.Equal(t, "archive", string(library))
	assert.Equal(t, int32(2), requests.Load())

	again, err := cachedPack(root, releases, "v1.2.3", platform)
	require.NoError(t, err)
	assert.Equal(t, pack, again)
	assert.Equal(t, int32(2), requests.Load(), "a kept pack is not downloaded again")
}

// A pack that does not match the release's checksums is refused and not kept.
func TestCachedPackRefusesAMismatch(t *testing.T) {
	t.Parallel()
	platform, err := target.Parse("aarch64-macos")
	require.NoError(t, err)
	releases, _ := release(t, "v1.2.3", platform, "00ff")
	root := t.TempDir()

	_, err = cachedPack(root, releases, "v1.2.3", platform)
	assert.ErrorContains(t, err, "the release's checksums say 00ff")
	assert.NoDirExists(t, filepath.Join(root, "v1.2.3", "aarch64-macos"))
}

// A release listing no checksum for the pack is refused before it is
// downloaded.
func TestCachedPackNeedsAChecksum(t *testing.T) {
	t.Parallel()
	listed, err := target.Parse("aarch64-macos")
	require.NoError(t, err)
	missing, err := target.Parse("x86_64-macos")
	require.NoError(t, err)
	releases, requests := release(t, "v1.2.3", listed, "")

	_, err = cachedPack(t.TempDir(), releases, "v1.2.3", missing)
	assert.ErrorContains(t, err, "lists no checksum for zam-pack-v1.2.3-x86_64-macos.tar.zst")
	assert.Equal(t, int32(1), requests.Load())
}

// A development build has no release to download packs from.
func TestCachedPackNeedsARelease(t *testing.T) {
	t.Parallel()
	platform, err := target.Parse("aarch64-linux-musl")
	require.NoError(t, err)
	_, err = cachedPack(t.TempDir(), "http://unused.invalid", developmentVersion, platform)
	assert.ErrorContains(t, err, "$ZAM_PACKS")
}

// $ZAM_PACKS names packs in place of the cache.
func TestPackPrefersZamPacks(t *testing.T) {
	platform, err := target.Parse("x86_64-macos")
	require.NoError(t, err)
	root := t.TempDir()
	t.Setenv(packsVariable, root)

	_, err = Pack(platform)
	assert.ErrorContains(t, err, "no target pack for x86_64-macos in $ZAM_PACKS")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "x86_64-macos"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "x86_64-macos", "sources.txt"), nil, 0o644))
	pack, err := Pack(platform)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "x86_64-macos"), pack)
}

// CleanCache removes the packs and runtimes zam keeps, read-only packs
// included, and leaves anything else in its cache directory alone.
func TestCleanCache(t *testing.T) {
	dir := writableTempDir(t)
	t.Setenv(cacheVariable, dir)
	pack := filepath.Join(dir, "packs", "v1.2.3", "aarch64-macos")
	require.NoError(t, os.MkdirAll(filepath.Join(pack, "lib"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pack, "sources.txt"), nil, 0o444))
	require.NoError(t, os.Chmod(filepath.Join(pack, "lib"), 0o555))
	require.NoError(t, os.Chmod(pack, 0o555))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "runtime", "zig"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other"), nil, 0o644))

	removed, err := CleanCache()
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(dir, "packs"), filepath.Join(dir, "runtime")}, removed)
	assert.NoDirExists(t, filepath.Join(dir, "packs"))
	assert.NoDirExists(t, filepath.Join(dir, "runtime"))
	assert.FileExists(t, filepath.Join(dir, "other"))
}
