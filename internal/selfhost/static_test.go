package selfhost_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
)

// TestStaticMatchesGo validates every program under testdata/static, each
// written to exercise the checks of the static pass ported so far, with the
// compiler written in zam and with the Go one, and expects the same errors.
func TestStaticMatchesGo(t *testing.T) {
	zam, err := compiler()
	require.NoError(t, err)

	entries, err := filepath.Glob(filepath.Join(root, "internal", "selfhost", "testdata", "static", "*", "*", "Main.zam"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		name, err := filepath.Rel(filepath.Join(root, "internal", "selfhost", "testdata", "static"), filepath.Dir(entry))
		require.NoError(t, err)
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, goValidateErrors(t, entry, semantic.PassStatic), zamErrors(t, zam, entry, false, "semantic"))
		})
	}
}
