package base

import (
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zambozoo/zam/internal/std"
)

const (
	// CodeFileExtension is the extension for code files.
	CodeFileExtension = ".zam"

	// projectFileNameShort is the short name of the project file.
	projectFileNameShort = "zam.yml"
	// projectFileNameLong is the long name of the project file.
	projectFileNameLong = "zam.yaml"

	// stdScheme prefixes the synthetic, unvalidated paths of standard library files.
	stdScheme = "std://"

	// StdPackage is the reserved package name for the standard library
	StdPackage = "std"
)

// Project manifest filenames searched when walking up from a path.
var projectFileNames = Set[string]{
	projectFileNameLong:  {},
	projectFileNameShort: {},
}

type (
	// Path is a normalized path to a file or directory: absolute on disk, or
	// std:// for the embedded standard library.
	Path string

	FileProvider struct{}
)

// StdPath returns the synthetic path for a standard library file by name.
func StdPath(name string) Path {
	return Path(stdScheme + name)
}

// IsStdPath reports whether the path addresses a standard library file.
func IsStdPath(p Path) bool {
	return strings.HasPrefix(string(p), stdScheme)
}

// isStdPathStr reports whether pathStr spells a standard library path, for
// callers that still have a raw string rather than a parsed Path.
func isStdPathStr(pathStr string) bool {
	return strings.HasPrefix(pathStr, stdScheme)
}

// StdName returns the name a standard library path addresses its file by.
func StdName(p Path) string {
	return strings.TrimPrefix(string(p), stdScheme)
}

// Resolve answers pathStr as it names a file from inside p: one that is not
// already absolute names a file beside p rather than beside whatever directory
// the compiler happened to be run from.
//
// What comes back is a path string rather than a Path because normalizing it
// is [FileProvider.NewPath]'s to do.
func (p Path) Resolve(pathStr string) string {
	// An absolute path names a file outright; one already spelled std:// is
	// itself a standard library path, which a user import by package name was
	// rewritten to by ResolveInto. Either is returned as it is.
	if filepath.IsAbs(pathStr) || isStdPathStr(pathStr) {
		return pathStr
	}

	// A standard library file reaches its siblings by joining against the
	// directory it sits in, with the scheme put back in front.
	if IsStdPath(p) {
		return stdScheme + path.Join(path.Dir(StdName(p)), filepath.ToSlash(pathStr))
	}
	return path.Join(path.Dir(filepath.ToSlash(string(p))), filepath.ToSlash(pathStr))
}

// ResolveInto answers pathStr as it names a file inside the directory p, where
// p is the root a dependency was extracted to rather than a file with
// neighbours. It is the counterpart to [Path.Resolve], which resolves beside p.
// A path that leaves p is refused.
func (p Path) ResolveInto(pathStr string) (string, error) {
	clean := path.Clean(filepath.ToSlash(pathStr))
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path %q leaves %s", pathStr, p)
	}
	if IsStdPath(p) {
		return string(p) + clean, nil
	}
	return path.Join(filepath.ToSlash(string(p)), clean), nil
}

// Contains reports whether other is p or sits somewhere beneath it.
func (p Path) Contains(other Path) bool {
	rel, err := filepath.Rel(string(p), string(other))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ComparePath orders paths by their spelling.
func ComparePath(a, b Path) int {
	return strings.Compare(string(a), string(b))
}

// Base returns the base name of the path.
func (p Path) Base() string {
	return path.Base(string(p))
}

// Dir names the directory the path's file sits in, which is the package its
// declarations belong to.
func (p Path) Dir() string {
	if IsStdPath(p) {
		return stdScheme + path.Dir(StdName(p))
	}
	return path.Dir(filepath.ToSlash(string(p)))
}

// Hash returns the hash of the path.
func (p Path) Hash() uint64 {
	return FNV(string(p))
}

// String implements the [fmt.Stringer] interface.
func (p Path) String() string {
	return string(p)
}

// FileExists checks if a file exists at the given path.
// An error is returned if the file is a directory.
func (fp *FileProvider) FileExists(path Path) (bool, error) {
	if st, err := os.Stat(string(path)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	} else if st.IsDir() {
		return false, fmt.Errorf("path is a directory: %s", path)
	}

	return true, nil
}

// NewPath creates a new path from the given string.
func (fp *FileProvider) NewPath(p string) (Path, error) {
	return ParsePath(p)
}

// ParsePath reads a path as it was written — on the command line or in an
// import — and answers the one path that spelling names.
//
// Normalizing is what makes two spellings of the same file the same file. A
// path is what a file is known by: it is the key files are stored under while
// parsing, and the order those keys sort in decides the order type ids are
// assigned and static initializers run. Left as written, `./Main.zam` and its
// absolute form would be two different files holding one file's contents, and
// would compile to different bytecode.
//
// It sits here rather than only on [FileProvider] so that a path can be
// normalized before any file provider is in hand.
func ParsePath(p string) (Path, error) {
	// A std:// path is the synthetic address of an embedded file and is not
	// made absolute: filepath.Abs would prepend the cwd and turn `std:` into
	// a directory name sitting under it.
	if isStdPathStr(p) {
		return Path(p), nil
	}

	abs, err := filepath.Abs(filepath.FromSlash(p))
	if err != nil {
		return "", err
	}
	return Path(filepath.Clean(abs)), nil
}

func ancestorDirs(filePath string) iter.Seq[string] {
	first := path.Dir(path.Clean(filepath.ToSlash(filePath)))
	return func(yield func(string) bool) {
		for dir := first; ; {
			if !yield(dir) {
				return
			}
			parent := path.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	}
}

// ImplicitProjectFilePath returns the path a project file for entry would have
// if it sat beside it. It names the project compiled without a project file.
func ImplicitProjectFilePath(entry Path) Path {
	return Path(filepath.Join(filepath.Dir(string(entry)), projectFileNameShort))
}

// ProjectFileIn returns the project file governing the directory dir.
func (fp *FileProvider) ProjectFileIn(dir Path) (Path, error) {
	return fp.ProjectFilePath(Path(filepath.Join(string(dir), projectFileNameShort)))
}

// ProjectFilePath returns the project file path for the given path.
func (fp *FileProvider) ProjectFilePath(p Path) (Path, error) {
	if IsStdPath(p) {
		return StdPath(projectFileNameShort), nil
	}

	for dir := range ancestorDirs(string(p)) {
		for name := range projectFileNames {
			projectFilePath := filepath.Join(dir, name)
			if fi, statErr := os.Stat(projectFilePath); statErr == nil && fi.Mode().IsRegular() {
				return Path(projectFilePath), nil
			}
		}
	}

	return "", fmt.Errorf("project file missing for %s", p.String())
}

// ListCodeFiles answers the code files sitting directly in dir, one Path for
// each, sorted by base name. The listing is not recursive — a file in a
// subdirectory is not reached by this call, since a glob import only names
// what sits one level inside the directory it is written for. Non-code files
// are skipped; the extension is the one thing that says a file is source.
//
// The directory is addressed the same way a file is: a std:// path or an
// absolute filesystem path. The children come back in the same shape, so a
// caller that is already walking Paths does not need to know which backing
// store answered.
func (fp *FileProvider) ListCodeFiles(dir Path) ([]Path, error) {
	if IsStdPath(dir) {
		return listStdCodeFiles(dir)
	}
	return listFSCodeFiles(dir)
}

// listStdCodeFiles reads a directory out of the embedded stdlib FS. The outer
// of a std path is the whole address — std:// scheme plus the inner key — and
// embed.FS does not spell the scheme, so the walk is over the key alone.
func listStdCodeFiles(dir Path) ([]Path, error) {
	key := StdName(dir)
	if key == "" {
		key = "."
	}
	entries, err := fs.ReadDir(std.FS, key)
	if err != nil {
		return nil, fmt.Errorf("cannot list stdlib directory %q: %w", dir.String(), err)
	}
	var children []Path
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), CodeFileExtension) {
			continue
		}
		childKey := entry.Name()
		if key != "." {
			childKey = path.Join(key, entry.Name())
		}
		children = append(children, StdPath(childKey))
	}
	sortByBase(children)
	return children, nil
}

// listFSCodeFiles reads a directory from disk. dir.Outer is already absolute
// — [ParsePath] made it so — and so the children's paths are too.
func listFSCodeFiles(dir Path) ([]Path, error) {
	entries, err := os.ReadDir(string(dir))
	if err != nil {
		return nil, fmt.Errorf("cannot list directory %q: %w", string(dir), err)
	}
	var children []Path
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), CodeFileExtension) {
			continue
		}
		children = append(children, Path(filepath.Join(string(dir), entry.Name())))
	}
	sortByBase(children)
	return children, nil
}

// sortByBase sorts the paths by their file name, so that the order the glob
// expands in is the order the files sit under the directory and not the order
// the backing store happened to return them. The parser's own ordering is by
// absolute path and sorts them again on the way in, so this is the stable
// label a diagnostic quotes and nothing load-bearing.
func sortByBase(paths []Path) {
	sort.Slice(paths, func(i, j int) bool {
		return paths[i].Base() < paths[j].Base()
	})
}

// Reader returns a reader for the given path.
func (fp *FileProvider) Reader(p Path) (io.ReadCloser, error) {
	if IsStdPath(p) {
		return std.FS.Open(StdName(p))
	}
	return os.Open(string(p))
}

func (fp *FileProvider) WriteFile(path string, r io.Reader) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, r)
	return err
}
