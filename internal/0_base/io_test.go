package base

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathResolve(t *testing.T) {
	main := Path("/a/b/Main.zam")

	for name, test := range map[string]struct {
		from     Path
		pathStr  string
		expected string
	}{
		"a file beside it":      {main, "A.zam", "/a/b/A.zam"},
		"a file below it":       {main, "sub/A.zam", "/a/b/sub/A.zam"},
		"a file above it":       {main, "../A.zam", "/a/A.zam"},
		"a file named outright": {main, "/c/A.zam", "/c/A.zam"},
		// A standard library file reaches its siblings by joining against the
		// directory it sits in, with the std scheme put back on the front.
		"a standard library file beside":  {StdPath("Obj.zam"), "A.zam", "std://A.zam"},
		"a standard library file above":   {StdPath("io/Writer.zam"), "../Error.zam", "std://Error.zam"},
		"a standard library file below":   {StdPath("Obj.zam"), "io/Writer.zam", "std://io/Writer.zam"},
		"a standard library file already": {StdPath("io/Writer.zam"), "std://Error.zam", "std://Error.zam"},
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zam.yml"), []byte(""), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "Deep.zam"), []byte(""), 0o600))

	var fileProvider FileProvider
	children, err := fileProvider.ListCodeFiles(Path(dir))
	require.NoError(t, err)

	var bases []string
	for _, child := range children {
		bases = append(bases, child.Base())
	}
	assert.Equal(t, []string{"A.zam", "B.zam"}, bases)
}

// TestListCodeFilesInStd walks the embedded standard library under a known
// subdirectory. std://io/local is one of the sub-packages the standard
// library ships, so its contents are what pins the std backend without
// reaching for a file that may be renamed next week; the assertion is a
// subset so the test does not break on a new file being added beside the
// ones named here.
func TestListCodeFilesInStd(t *testing.T) {
	var fileProvider FileProvider
	children, err := fileProvider.ListCodeFiles(StdPath("io/local"))
	require.NoError(t, err)

	outers := make(map[string]bool, len(children))
	for _, child := range children {
		outers[string(child)] = true
	}
	for _, name := range []string{"std://io/local/File.zam"} {
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
	_, err := fileProvider.ListCodeFiles(Path(dir))
	assert.Error(t, err)
}

// A package import names a file inside the directory its dependency was
// extracted to, and is refused if it would leave it.
func TestPathResolveInto(t *testing.T) {
	dependency := Path("/zam/pkg/example.com/dep@v1")

	for name, test := range map[string]struct {
		from     Path
		pathStr  string
		expected string
		err      bool
	}{
		"a file at its root":       {dependency, "A.zam", "/zam/pkg/example.com/dep@v1/A.zam", false},
		"a file below its root":    {dependency, "sub/../src/A.zam", "/zam/pkg/example.com/dep@v1/src/A.zam", false},
		"a file above its root":    {dependency, "../A.zam", "", true},
		"a file named outright":    {dependency, "/etc/A.zam", "", true},
		"a standard library file":  {StdPath(""), "io/Writer.zam", "std://io/Writer.zam", false},
		"leaving the standard one": {StdPath(""), "../Writer.zam", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := test.from.ResolveInto(test.pathStr)
			if test.err {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.expected, got)
		})
	}
}

func TestPathContains(t *testing.T) {
	dir := Path("/zam/pkg/example.com/dep@v1")

	assert.True(t, dir.Contains(dir))
	assert.True(t, dir.Contains(Path("/zam/pkg/example.com/dep@v1/src/A.zam")))
	assert.False(t, dir.Contains(Path("/zam/pkg/example.com/dep@v1.tar.zst")))
	assert.False(t, dir.Contains(Path("/zam/pkg/example.com/dep@v10/A.zam")))
	assert.False(t, dir.Contains(Path("/zam/pkg/example.com/other@v1/A.zam")))
}
