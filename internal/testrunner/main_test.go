package testrunner

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points native links at the packs built into the repository, and
// shares Zig's caches and one compile of the runtime across tests, unless the
// environment names others.
func TestMain(m *testing.M) {
	var cache string
	if os.Getenv("ZAM_CACHE_DIR") == "" {
		cache, _ = os.MkdirTemp("", "zam-test-cache-*")
		_ = os.Setenv("ZAM_CACHE_DIR", cache)
	}
	if os.Getenv("ZAM_PACKS") == "" {
		packs, _ := filepath.Abs(filepath.Join("..", "..", "bin", "packs"))
		_ = os.Setenv("ZAM_PACKS", packs)
	}
	for _, variable := range []string{"ZIG_GLOBAL_CACHE_DIR", "ZIG_LOCAL_CACHE_DIR"} {
		if os.Getenv(variable) == "" {
			_ = os.Setenv(variable, filepath.Join(os.TempDir(), "zam-test-zig"))
		}
	}
	code := m.Run()
	if cache != "" {
		_ = os.RemoveAll(cache)
	}
	os.Exit(code)
}
