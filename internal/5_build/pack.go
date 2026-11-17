package build

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	base "github.com/zambozoo/zam/internal/0_base"
	"github.com/zambozoo/zam/internal/target"
)

// Version is the version of zam, which names the release its target packs are
// downloaded from. A release build sets it; a development build has none.
var Version = developmentVersion

// developmentVersion is the Version of a build that is not a release.
const developmentVersion = "dev"

// packsVariable names a directory holding target packs, each in a directory
// named for its target, in place of the packs in zam's cache.
const packsVariable = "ZAM_PACKS"

// releasesVariable names where releases are downloaded from, in place of zam's
// own: a URL holding a directory for each version.
const releasesVariable = "ZAM_RELEASES_URL"

// releasesURL is where zam's releases are downloaded from.
const releasesURL = "https://github.com/zambozoo/zam/releases/download"

// checksumsName is the file of a release holding the SHA-256 of every other.
const checksumsName = "checksums.txt"

// packClient downloads packs, which run to tens of megabytes.
var packClient = &http.Client{Timeout: 10 * time.Minute}

// packMu serializes the downloads of a process, so that two links needing the
// same pack download it once.
var packMu sync.Mutex

// Pack answers the directory of t's target pack: under $ZAM_PACKS when it is
// set, else in zam's cache, downloaded from the release of this version of zam
// the first time it is needed.
func Pack(t target.Target) (string, error) {
	if root := os.Getenv(packsVariable); root != "" {
		pack := filepath.Join(root, t.Name)
		if !isPack(pack) {
			return "", fmt.Errorf("no target pack for %s in $%s (%s)", t.Name, packsVariable, root)
		}
		return pack, nil
	}
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	releases := os.Getenv(releasesVariable)
	if releases == "" {
		releases = releasesURL
	}
	return cachedPack(filepath.Join(dir, "packs"), releases, Version, t)
}

// isPack reports whether dir holds a pack.
func isPack(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "sources.txt"))
	return err == nil
}

// cachedPack answers t's pack for version under root, downloading it from the
// release of version under releases first when it is not there yet. The
// download is checked against the release's checksums before it is extracted.
func cachedPack(root, releases, version string, t target.Target) (string, error) {
	if version == developmentVersion {
		return "", fmt.Errorf("a development build of zam has no released target packs: build them with `task packs` and set $%s to bin/packs",
			packsVariable)
	}
	pack := filepath.Join(root, version, t.Name)
	if isPack(pack) {
		return pack, nil
	}
	packMu.Lock()
	defer packMu.Unlock()
	if isPack(pack) {
		return pack, nil
	}

	release, err := url.JoinPath(releases, version)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("zam-pack-%s-%s.tar.zst", version, t.Name)
	want, err := releaseChecksum(release, name)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(pack), 0o755); err != nil {
		return "", err
	}
	archive, err := os.CreateTemp(filepath.Dir(pack), ".pack-*.tar.zst")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	hash := sha256.New()
	err = download(release+"/"+name, io.MultiWriter(archive, hash))
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return "", fmt.Errorf("%s has SHA-256 %s, but the release's checksums say %s", name, got, want)
	}
	if err := base.Extract(archive.Name(), pack); err != nil {
		return "", fmt.Errorf("cannot extract %s: %w", name, err)
	}
	return pack, nil
}

// releaseChecksum answers the SHA-256 the checksums of release list for name.
func releaseChecksum(release, name string) (string, error) {
	var sums bytes.Buffer
	if err := download(release+"/"+checksumsName, &sums); err != nil {
		return "", err
	}
	lines := bufio.NewScanner(&sums)
	for lines.Scan() {
		if sum, file, ok := strings.Cut(lines.Text(), "  "); ok && file == name {
			return sum, nil
		}
	}
	return "", fmt.Errorf("the release at %s lists no checksum for %s", release, name)
}

// download writes what url answers to w.
func download(url string, w io.Writer) error {
	response, err := packClient.Get(url)
	if err != nil {
		return fmt.Errorf("cannot download %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("cannot download %s: %s", url, response.Status)
	}
	if _, err := io.Copy(w, response.Body); err != nil {
		return fmt.Errorf("cannot download %s: %w", url, err)
	}
	return nil
}
