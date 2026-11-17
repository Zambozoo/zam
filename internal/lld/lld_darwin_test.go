package lld

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLinkMachORuns links a macOS executable against the SDK's libSystem and
// runs it.
func TestLinkMachORuns(t *testing.T) {
	sdk, err := exec.Command("xcrun", "--show-sdk-path").Output()
	require.NoError(t, err)

	dir := t.TempDir()
	obj := filepath.Join(dir, "hello.o")
	exe := filepath.Join(dir, "hello")
	emitObject(t, obj, "arm64-apple-macosx14.0.0", "main", true)

	require.NoError(t, Link(MachO, []string{
		"-arch", "arm64", "-platform_version", "macos", "14.0", "14.0",
		"-syslibroot", strings.TrimSpace(string(sdk)), "-lSystem", "-o", exe, obj,
	}))

	out, err := exec.Command(exe).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "hello from lld\n", string(out))
}
