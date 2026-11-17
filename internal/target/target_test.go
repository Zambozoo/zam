package target

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	t.Parallel()
	for _, name := range Names() {
		parsed, err := Parse(name)
		require.NoError(t, err)
		assert.Equal(t, name, parsed.Name)
	}
	_, err := Parse("riscv64-linux-musl")
	assert.ErrorContains(t, err, "aarch64-linux-musl")
}

func TestHost(t *testing.T) {
	t.Parallel()
	host, err := Host()
	require.NoError(t, err)
	assert.True(t, host.IsHost())
	assert.Equal(t, map[string]OS{"darwin": MacOS, "linux": Linux}[runtime.GOOS], host.OS)
	assert.Equal(t, map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH], host.Arch)
}
