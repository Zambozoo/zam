package build_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points native links at the packs built into the repository and
// shares Zig's caches across tests, unless the environment names others.
func TestMain(m *testing.M) {
	if os.Getenv("ZAM_PACKS") == "" {
		packs, _ := filepath.Abs(filepath.Join("..", "..", "bin", "packs"))
		_ = os.Setenv("ZAM_PACKS", packs)
	}
	for _, variable := range []string{"ZIG_GLOBAL_CACHE_DIR", "ZIG_LOCAL_CACHE_DIR"} {
		if os.Getenv(variable) == "" {
			_ = os.Setenv(variable, filepath.Join(os.TempDir(), "zam-test-zig"))
		}
	}
	os.Exit(m.Run())
}
