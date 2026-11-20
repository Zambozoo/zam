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
	assert.Equal(t, map[string]OS{"darwin": MacOS, "linux": Linux, "windows": Windows}[runtime.GOOS], host.OS)
	assert.Equal(t, map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH], host.Arch)
}

func TestExecutable(t *testing.T) {
	t.Parallel()
	windows, err := Parse("x86_64-windows-gnu")
	require.NoError(t, err)
	linux, err := Parse("x86_64-linux-musl")
	require.NoError(t, err)
	assert.Equal(t, "out.exe", windows.Executable("out"))
	assert.Equal(t, "out.exe", windows.Executable("out.exe"))
	assert.Equal(t, "out", linux.Executable("out"))
}
