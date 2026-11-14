package base

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	// zamPathEnv names the directory dependencies are kept under.
	zamPathEnv = "ZAMPATH"
	// archiveExtension is the extension of a dependency's archive.
	archiveExtension = ".tar.zst"
	// hashPrefix prefixes a dependency hash with the scheme it was taken by.
	hashPrefix = "h1:"
)

var (
	repoPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*(/[A-Za-z0-9._~-]+)+$`)
	versionPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	shortSHAPattern = regexp.MustCompile(`^[0-9a-f]{7,39}$`)
)

type (
	// PackageProvider fetches dependencies from their git repositories. Each
	// version is kept under a root directory as a zstd-compressed tar, which its
	// hash is taken over, and extracted read-only beside it, which is what is
	// compiled.
	PackageProvider struct {
		root    string
		remote  func(repo string) string
		fetches SingleflightGroup[fetched]
	}

	// fetched is a dependency ready to be compiled.
	fetched struct {
		dir  Path
		hash string
	}
)

// ZamPath returns the directory ZAMPATH names, or zam in the user's home
// directory when it is unset.
func ZamPath() (string, error) {
	if dir := os.Getenv(zamPathEnv); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot find the home directory for %s: %w", zamPathEnv, err)
	}
	return filepath.Join(home, "zam"), nil
}

// NewPackageProvider returns a provider keeping dependencies under root.
func NewPackageProvider(root string) *PackageProvider {
	return &PackageProvider{
		root: root,
		remote: func(repo string) string {
			return "https://" + repo + ".git"
		},
	}
}

// Fetch returns the directory the dependency ref, spelled repo@version, is
// extracted to, and the hash of its contents. The repository is fetched only
// when its archive is missing, and extracted only when its directory is.
func (pp *PackageProvider) Fetch(ref string) (Path, string, error) {
	repo, version, err := parseRef(ref)
	if err != nil {
		return "", "", err
	}

	result, err, _ := pp.fetches.Do(ref, func() (fetched, error) {
		dir := filepath.Join(pp.root, filepath.FromSlash(repo)+"@"+version)
		archive := dir + archiveExtension

		if _, err := os.Stat(archive); errors.Is(err, fs.ErrNotExist) {
			if err := pp.download(repo, version, archive); err != nil {
				return fetched{}, fmt.Errorf("cannot fetch %s: %w", ref, err)
			}
		} else if err != nil {
			return fetched{}, err
		}

		hash, err := hashArchive(archive)
		if err != nil {
			return fetched{}, fmt.Errorf("cannot read %s: %w", archive, err)
		}

		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			if err := extract(archive, dir); err != nil {
				return fetched{}, fmt.Errorf("cannot extract %s: %w", archive, err)
			}
		} else if err != nil {
			return fetched{}, err
		}

		return fetched{dir: Path(dir), hash: hash}, nil
	})
	return result.dir, result.hash, err
}

// Root returns the directory dependencies are kept under.
func (pp *PackageProvider) Root() string {
	return pp.root
}

// Clean removes every dependency kept under the provider's root.
func (pp *PackageProvider) Clean() error {
	return removeReadOnly(pp.root)
}

// parseRef splits ref into its repository and version, refusing either when it
// could name something outside the provider's root or be read by git as an
// option.
func parseRef(ref string) (repo, version string, err error) {
	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return "", "", fmt.Errorf("dependency %q has no version: write repo@version", ref)
	}
	repo, version = ref[:at], ref[at+1:]

	if !repoPattern.MatchString(repo) {
		return "", "", fmt.Errorf("dependency %q has an invalid repository %q", ref, repo)
	}
	for _, element := range strings.Split(repo, "/") {
		if element == "." || element == ".." {
			return "", "", fmt.Errorf("dependency %q has an invalid repository %q", ref, repo)
		}
	}

	switch {
	case !versionPattern.MatchString(version) || strings.Contains(version, ".."):
		return "", "", fmt.Errorf("dependency %q has an invalid version %q", ref, version)
	case shortSHAPattern.MatchString(version):
		return "", "", fmt.Errorf("dependency %q names an abbreviated commit: use a tag or the full commit", ref)
	}
	return repo, version, nil
}

// download fetches version of repo and writes it to archive, which appears
// only once it is complete.
func (pp *PackageProvider) download(repo, version, archive string) error {
	parent := filepath.Dir(archive)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(parent, ".fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	tree := filepath.Join(work, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"fetch", "-q", "--depth", "1", pp.remote(repo), version},
		{"checkout", "-q", "FETCH_HEAD"},
	} {
		if err := git(tree, args...); err != nil {
			return err
		}
	}

	pending := filepath.Join(work, "archive")
	if err := pack(tree, pending); err != nil {
		return err
	}
	return os.Rename(pending, archive)
}

// git runs git with args in dir, failing rather than prompting for
// credentials.
func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, bytes.TrimSpace(out))
	}
	return nil
}

// pack writes the regular files of tree, other than git's own, to archive.
// Entries are sorted and carry nothing of the machine they were written on, so
// the same tree always packs to the same bytes.
func pack(tree, archive string) error {
	var names []string
	err := filepath.WalkDir(tree, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tree, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case rel == ".git":
			return filepath.SkipDir
		case entry.IsDir():
			return nil
		case !entry.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", rel)
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(names)

	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zw, err := zstd.NewWriter(f)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		header := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			ModTime:  time.Unix(0, 0),
			Typeflag: tar.TypeReg,
			Format:   tar.FormatPAX,
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

// entries calls fn with each file in archive and its contents, refusing any
// entry that is not a regular file safely inside the archive's root, or that
// appears twice.
func entries(archive string, fn func(name string, body io.Reader) error) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	seen := make(Set[string])
	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		name := header.Name
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("entry %q is not a regular file", name)
		}
		if clean := path.Clean(name); clean != name || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("entry %q leaves the archive", name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("entry %q appears twice", name)
		}
		seen[name] = struct{}{}
		if err := fn(name, tr); err != nil {
			return err
		}
	}
}

// hashArchive returns the hash of the files in archive: a sha256 over a line
// for each file, in name order, holding the sha256 of its contents and its
// name. It depends only on what the files hold and are called, not on how
// they were packed.
func hashArchive(archive string) (string, error) {
	sums := make(map[string][]byte)
	err := entries(archive, func(name string, body io.Reader) error {
		h := sha256.New()
		if _, err := io.Copy(h, body); err != nil {
			return err
		}
		sums[name] = h.Sum(nil)
		return nil
	})
	if err != nil {
		return "", err
	}

	names := make([]string, 0, len(sums))
	for name := range sums {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%x  %s\n", sums[name], name)
	}
	return hashPrefix + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// extract writes the files of archive to dir, read-only. dir appears only once
// it is complete; if another process extracted it first, its copy is kept.
func extract(archive, dir string) error {
	pending, err := os.MkdirTemp(filepath.Dir(dir), ".extract-")
	if err != nil {
		return err
	}
	defer removeReadOnly(pending)

	err = entries(archive, func(name string, body io.Reader) error {
		file := filepath.Join(pending, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, body)
		return errors.Join(err, f.Close())
	})
	if err != nil {
		return err
	}

	err = filepath.WalkDir(pending, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || file == pending {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		return os.Chmod(file, 0o444)
	})
	if err != nil {
		return err
	}
	if err := os.Rename(pending, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			return nil
		}
		return err
	}
	return readOnlyDirs(dir)
}

// readOnlyDirs makes every directory under dir, and dir itself, read-only.
// Children go first, so none is left unreachable for writing midway.
func readOnlyDirs(dir string) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			dirs = append(dirs, file)
		}
		return err
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}

// removeReadOnly removes dir, making everything under it writable first, as the
// read-only extraction of a dependency otherwise cannot be removed.
func removeReadOnly(dir string) error {
	_ = filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
		if err == nil {
			mode := os.FileMode(0o644)
			if entry.IsDir() {
				mode = 0o755
			}
			_ = os.Chmod(file, mode)
		}
		return nil
	})
	return os.RemoveAll(dir)
}
