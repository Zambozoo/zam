package selfhost_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	"github.com/zambozoo/zam/internal/test"
)

// TestZamTests runs the tests written in zam under src, so they run on every
// platform the suite does.
func TestZamTests(t *testing.T) {
	files := &base.FileProvider{}
	cmd, err := cli.New([]string{"zam", "test", filepath.ToSlash(filepath.Join(root, "src")) + "/..."}, files)
	require.NoError(t, err)

	var out bytes.Buffer
	passed, err := test.Run(cmd, files, base.NewPackageProvider(t.TempDir()), &out)
	require.NoError(t, err)
	assert.True(t, passed, out.String())
}
