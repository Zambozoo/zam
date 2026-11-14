package base

import (
	"archive/tar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRepo commits files to a new git repository tagged v1 and returns its
// directory and the commit.
func newRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		file := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(body), 0o644))
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{
			"-c", "user.name=zam", "-c", "user.email=zam@example.com",
			"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
		}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	run("tag", "v1")
	return dir, run("rev-parse", "HEAD")
}

// newProvider returns a provider under a fresh root that fetches each repo from
// the directory repos maps it to.
func newProvider(t *testing.T, repos map[string]string) *PackageProvider {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() { _ = removeReadOnly(root) })
	pp := NewPackageProvider(root)
	pp.remote = func(repo string) string {
		return "file://" + repos[repo]
	}
	return pp
}

func TestPackageProviderFetch(t *testing.T) {
	t.Parallel()
	src, _ := newRepo(t, map[string]string{
		"zam.yml":     "name: \"dep\"\n",
		"Dep.zam":     "namespace Dep {}\n",
		"sub/Sub.zam": "namespace Sub {}\n",
	})
	pp := newProvider(t, map[string]string{"example.com/dep": src})

	dir, hash, err := pp.Fetch("example.com/dep@v1")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(pp.root, "example.com", "dep@v1"), string(dir))
	assert.FileExists(t, string(dir)+archiveExtension)
	assert.True(t, strings.HasPrefix(hash, hashPrefix), hash)

	body, err := os.ReadFile(filepath.Join(string(dir), "sub", "Sub.zam"))
	require.NoError(t, err)
	assert.Equal(t, "namespace Sub {}\n", string(body))
	assert.NoDirExists(t, filepath.Join(string(dir), ".git"))

	info, err := os.Stat(filepath.Join(string(dir), "Dep.zam"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o444), info.Mode().Perm())
	info, err = os.Stat(string(dir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o555), info.Mode().Perm())

	// Another root fetching the same commit packs the same files, and so has
	// the same hash.
	_, again, err := newProvider(t, map[string]string{"example.com/dep": src}).Fetch("example.com/dep@v1")
	require.NoError(t, err)
	assert.Equal(t, hash, again)

	// What is already kept is not fetched again, so it is found without its
	// repository.
	cached := NewPackageProvider(pp.root)
	cached.remote = func(string) string { return "file:///nowhere" }
	_, again, err = cached.Fetch("example.com/dep@v1")
	require.NoError(t, err)
	assert.Equal(t, hash, again)
}

// A full commit is fetched as a tag is.
func TestPackageProviderFetchesACommit(t *testing.T) {
	t.Parallel()
	src, commit := newRepo(t, map[string]string{"Dep.zam": "namespace Dep {}\n"})
	pp := newProvider(t, map[string]string{"example.com/dep": src})

	_, byTag, err := pp.Fetch("example.com/dep@v1")
	require.NoError(t, err)
	dir, byCommit, err := pp.Fetch("example.com/dep@" + commit)
	require.NoError(t, err)
	assert.Equal(t, byTag, byCommit)
	assert.FileExists(t, filepath.Join(string(dir), "Dep.zam"))
}

// Two repositories beside each other are each fetched.
func TestPackageProviderFetchesSiblings(t *testing.T) {
	t.Parallel()
	a, _ := newRepo(t, map[string]string{"A.zam": "a"})
	b, _ := newRepo(t, map[string]string{"B.zam": "b"})
	pp := newProvider(t, map[string]string{"example.com/a": a, "example.com/b": b})

	dirA, _, err := pp.Fetch("example.com/a@v1")
	require.NoError(t, err)
	dirB, _, err := pp.Fetch("example.com/b@v1")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(string(dirA), "A.zam"))
	assert.FileExists(t, filepath.Join(string(dirB), "B.zam"))
}

// The hash is taken from the archive on every fetch, so an archive changed
// after it was fetched answers with a hash of its own, and a removed extraction
// is restored from it.
func TestPackageProviderReadsTheArchiveEachTime(t *testing.T) {
	t.Parallel()
	src, _ := newRepo(t, map[string]string{"Dep.zam": "namespace Dep {}\n"})
	pp := newProvider(t, map[string]string{"example.com/dep": src})

	dir, hash, err := pp.Fetch("example.com/dep@v1")
	require.NoError(t, err)
	require.NoError(t, removeReadOnly(string(dir)))

	writeArchive(t, string(dir)+archiveExtension, map[string]string{"Dep.zam": "namespace Evil {}\n"})
	_, tampered, err := NewPackageProvider(pp.root).Fetch("example.com/dep@v1")
	require.NoError(t, err)
	assert.NotEqual(t, hash, tampered)

	body, err := os.ReadFile(filepath.Join(string(dir), "Dep.zam"))
	require.NoError(t, err)
	assert.Equal(t, "namespace Evil {}\n", string(body))
}

// An archive is refused rather than extracted when an entry would land outside
// its directory.
func TestPackageProviderRefusesAnEntryLeavingTheArchive(t *testing.T) {
	t.Parallel()
	pp := newProvider(t, nil)
	archive := filepath.Join(pp.root, "example.com", "dep@v1"+archiveExtension)
	require.NoError(t, os.MkdirAll(filepath.Dir(archive), 0o755))
	writeArchive(t, archive, map[string]string{"../Escape.zam": "x"})

	_, _, err := pp.Fetch("example.com/dep@v1")
	assert.ErrorContains(t, err, "leaves the archive")
	assert.NoFileExists(t, filepath.Join(pp.root, "example.com", "Escape.zam"))
}

func TestPackageProviderRefusesAnInvalidRef(t *testing.T) {
	t.Parallel()
	pp := newProvider(t, nil)

	for ref, want := range map[string]string{
		"example.com/dep":            "no version",
		"example.com/dep@":           "invalid version",
		"example.com/dep@-upload":    "invalid version",
		"example.com/dep@v1..v2":     "invalid version",
		"example.com/dep@abc1234":    "abbreviated commit",
		"dep@v1":                     "invalid repository",
		"/example.com/dep@v1":        "invalid repository",
		"example.com/../../etc@v1":   "invalid repository",
		"example.com/dep/./sub@v1":   "invalid repository",
		"https://example.com/dep@v1": "invalid repository",
	} {
		_, _, err := pp.Fetch(ref)
		assert.ErrorContains(t, err, want, ref)
	}
}

// writeArchive writes files to archive as a zstd-compressed tar.
func writeArchive(t *testing.T, archive string, files map[string]string) {
	t.Helper()
	f, err := os.Create(archive)
	require.NoError(t, err)
	zw, err := zstd.NewWriter(f)
	require.NoError(t, err)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
}

func TestPackageProviderClean(t *testing.T) {
	t.Parallel()
	src, _ := newRepo(t, map[string]string{"Dep.zam": "namespace Dep {}\n"})
	pp := newProvider(t, map[string]string{"example.com/dep": src})

	_, _, err := pp.Fetch("example.com/dep@v1")
	require.NoError(t, err)
	require.NoError(t, pp.Clean())
	assert.NoDirExists(t, pp.Root())
	require.NoError(t, pp.Clean(), "cleaning nothing is not an error")
}
