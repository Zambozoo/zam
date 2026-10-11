package base

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A downloaded archive is written to a file named for where it came from, but
// the file it is written to is absolute and says as much about this machine as
// about the download. A diagnostic wanting to say which archive a declaration
// came from needs the URL back, so the two are recorded against each other.
//
// The recording is what the download did, not whether it ran: an archive
// already on disk is skipped, and is still the archive it was fetched from.
func TestGitProviderArchiveName(t *testing.T) {
	t.Parallel()

	fp := &mock_FileProvider{}
	fp.EXPECT().NewPath("example.invalid/dep/archive/v1.zip").
		Return(NewPath("/cache/example.invalid/dep/archive/v1.zip", ""), nil)
	fp.EXPECT().MakeDir(mock.Anything).Return(false, nil)

	gp := NewGitProvider(fp)
	path, err := gp.Download("https://example.invalid/dep/archive/v1.zip")
	require.NoError(t, err)

	assert.Equal(t, "example.invalid/dep/archive/v1.zip", gp.ArchiveName(path.Outer))
	assert.Empty(t, gp.ArchiveName("/some/file/nothing/downloaded"),
		"a file no download wrote has no archive to name")
}
