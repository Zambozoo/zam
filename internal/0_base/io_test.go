package base

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathResolve(t *testing.T) {
	main := Path{Outer: "/a/b/Main.zam"}
	archived := Path{Outer: "/a/lib.zip", Inner: "src/Main.zam"}

	for name, test := range map[string]struct {
		from     Path
		pathStr  string
		expected string
	}{
		"a file beside it":          {main, "A.zam", "/a/b/A.zam"},
		"a file below it":           {main, "sub/A.zam", "/a/b/sub/A.zam"},
		"a file above it":           {main, "../A.zam", "/a/A.zam"},
		"a file named outright":     {main, "/c/A.zam", "/c/A.zam"},
		"an archive beside it":      {main, "lib.zip#src/A.zam", "/a/b/lib.zip#src/A.zam"},
		"a file in its own archive": {archived, "A.zam", "/a/lib.zip#src/A.zam"},
		"a file below in it":        {archived, "deep/A.zam", "/a/lib.zip#src/deep/A.zam"},
		// A standard library file reaches its siblings the way a file in a zip
		// reaches its own: by joining against the directory the current file
		// sits in, with the std scheme put back on the front.
		"a standard library file beside":  {*StdPath("Obj.zam"), "A.zam", "std://A.zam"},
		"a standard library file above":   {*StdPath("io/Writer.zam"), "../Error.zam", "std://Error.zam"},
		"a standard library file below":   {*StdPath("Obj.zam"), "io/Writer.zam", "std://io/Writer.zam"},
		"a standard library file already": {*StdPath("io/Writer.zam"), "std://Error.zam", "std://Error.zam"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.expected, test.from.Resolve(test.pathStr))
		})
	}
}

// TestListCodeFilesOnDisk pins the one thing a glob import depends on from the
// filesystem backend: it answers the .zam files sitting directly in the
// directory and nothing else — not the manifest beside them, not the file in
// a subdirectory, not anything with a different extension. The order is by
// base name, since that is what a diagnostic would quote.
func TestListCodeFilesOnDisk(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "B.zam"), []byte(""), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "A.zam"), []byte(""), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yaml"), []byte(""), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "Deep.zam"), []byte(""), 0o600))

	var fileProvider FileProvider
	children, err := fileProvider.ListCodeFiles(NewPath(dir, ""))
	require.NoError(t, err)

	var bases []string
	for _, child := range children {
		bases = append(bases, child.Base())
	}
	assert.Equal(t, []string{"A.zam", "B.zam"}, bases)
}

// TestListCodeFilesInsideZip covers the archive backend. A dependency on disk
// is a zip, and a glob import names the files sitting one directory deep in
// it the same way as on a filesystem. The archive is scanned once and the
// entries whose directory is the one asked for are kept.
func TestListCodeFilesInsideZip(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "lib.zip")
	f, err := os.Create(archivePath)
	require.NoError(t, err)
	w := zip.NewWriter(f)
	for _, name := range []string{"src/B.zam", "src/A.zam", "src/sub/Deep.zam", "src/zam.yaml"} {
		entry, err := w.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(entry, "")
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())

	var fileProvider FileProvider
	children, err := fileProvider.ListCodeFiles(NewPath(archivePath, "src"))
	require.NoError(t, err)

	var inners []string
	for _, child := range children {
		inners = append(inners, child.Inner)
	}
	assert.Equal(t, []string{"src/A.zam", "src/B.zam"}, inners)
}

// TestListCodeFilesInStd walks the embedded stdlib under a known subdirectory.
// std://io is the one subpackage the stdlib ships, so its contents are what
// pins the std backend without reaching for a file that may be renamed next
// week; the assertion is a subset so the test does not break on a new file
// being added beside the ones named here.
func TestListCodeFilesInStd(t *testing.T) {
	var fileProvider FileProvider
	children, err := fileProvider.ListCodeFiles(StdPath("io"))
	require.NoError(t, err)

	outers := make(map[string]bool, len(children))
	for _, child := range children {
		outers[child.Outer] = true
	}
	for _, name := range []string{"std://io/File.zam"} {
		assert.True(t, outers[name], "expected %q in listing, got %v", name, outers)
	}
}

// A directory that does not exist is reported rather than returned as an empty
// listing. The glob import catches a zero-match separately, which is a
// different mistake — one is a typo'd directory, the other is a directory with
// no code in it yet — and conflating the two would make either harder to find.
func TestListCodeFilesReportsAMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")

	var fileProvider FileProvider
	_, err := fileProvider.ListCodeFiles(NewPath(dir, ""))
	assert.Error(t, err)
}

// Resolving builds the path inside an archive by joining, so what a file
// imports decides what that path is. It is still refused if it leaves the
// archive it was written in.
func TestNewPathRefusesAResolvedPathThatLeavesItsArchive(t *testing.T) {
	from := Path{Outer: "/a/lib.zip", Inner: "src/Main.zam"}

	var fileProvider FileProvider
	inside, err := fileProvider.NewPath(from.Resolve("../Sibling.zam"))
	require.NoError(t, err)
	assert.Equal(t, "Sibling.zam", inside.Inner)

	_, err = fileProvider.NewPath(from.Resolve("../../elsewhere/A.zam"))
	assert.ErrorContains(t, err, "escapes archive root")
}
