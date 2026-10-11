package base

import (
	"archive/zip"
	"errors"
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
	// zipDelimiter is used to separate the outer and inner paths in a zip archive.
	zipDelimiter = "#"

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
	// Path represents a path in a file system.
	Path struct {
		Outer string
		Inner string `json:"Inner,omitempty"`
	}

	// zipEntryReadCloser closes the zip reader after the entry body is closed.
	zipEntryReadCloser struct {
		z    *zip.ReadCloser
		body io.ReadCloser
	}

	FileProvider struct{}
)

// NewPath creates a new path without any validation.
func NewPath(outer string, inner string) *Path {
	return &Path{
		Outer: outer,
		Inner: inner,
	}
}

// StdPath returns the synthetic path for a standard library file by name.
func StdPath(name string) *Path {
	return &Path{Outer: stdScheme + name}
}

// IsStdPath reports whether the path addresses a standard library file.
func IsStdPath(p *Path) bool {
	return strings.HasPrefix(p.Outer, stdScheme)
}

// isStdPathStr reports whether pathStr spells a standard library path, for
// callers that still have a raw string rather than a parsed Path.
func isStdPathStr(pathStr string) bool {
	return strings.HasPrefix(pathStr, stdScheme)
}

// stdKey returns the StdFS key for a standard library path.
func stdKey(p *Path) string {
	return strings.TrimPrefix(p.Outer, stdScheme)
}

// Resolve answers pathStr as it names a file from inside p: one that is not
// already absolute names a file beside p rather than beside whatever directory
// the compiler happened to be run from, and one written inside an archive stays
// in that archive.
//
// What comes back is a path string rather than a Path because normalizing it,
// and refusing it if it leaves the archive, is [FileProvider.NewPath]'s to do.
func (p *Path) Resolve(pathStr string) string {
	// An absolute path names a file outright; one already spelled std:// is
	// itself a standard library path, which a user import by package name was
	// rewritten to by ResolveInto. Either is returned as it is.
	if filepath.IsAbs(pathStr) || isStdPathStr(pathStr) {
		return pathStr
	}

	// A standard library file reaches its siblings the way a file in a zip
	// reaches its own: the import names a path inside the same archive, so the
	// join is against the directory the current file sits in and the scheme is
	// put back in front. The stdlib's archive happens to be the compiler
	// binary, which is why there is no zip indirection to walk.
	if IsStdPath(p) {
		return stdScheme + path.Join(path.Dir(stdKey(p)), filepath.ToSlash(pathStr))
	}

	if p.InZip() {
		return p.Outer + zipDelimiter + path.Join(path.Dir(p.Inner), filepath.ToSlash(pathStr))
	}
	return path.Join(path.Dir(filepath.ToSlash(p.Outer)), filepath.ToSlash(pathStr))
}

// ResolveInto answers pathStr as it names a file inside p, where p is the
// archive a dependency was downloaded to rather than a file with neighbours.
// It is the counterpart to [Path.Resolve], which resolves beside p.
//
// What comes back is a path string for the same reason Resolve's does:
// normalizing it, and refusing it if it leaves the archive, is
// [FileProvider.NewPath]'s to do.
func (p *Path) ResolveInto(pathStr string) string {
	// The stdlib is embedded in the compiler and not an archive: its paths are
	// std://name, no zip indirection, so a file inside it is reached by
	// appending the name to the scheme rather than through the # delimiter.
	if IsStdPath(p) {
		return p.Outer + filepath.ToSlash(pathStr)
	}
	return p.Outer + zipDelimiter + filepath.ToSlash(pathStr)
}

// ComparePath orders paths by the file they live in and then by the entry
// within it, which is the order the two are written in by String.
func ComparePath(a, b Path) int {
	if c := strings.Compare(a.Outer, b.Outer); c != 0 {
		return c
	}
	return strings.Compare(a.Inner, b.Inner)
}

// Base returns the base name of the path.
func (p *Path) Base() string {
	return path.Base(p.Outer)
}

// Hash returns the hash of the path.
func (p *Path) Hash() uint64 {
	if p.Inner == "" {
		return FNV(p.Outer)
	}
	return FNV(p.Outer, "#", p.Inner)
}

// InZip returns true if the path is inside a zip archive.
func (p *Path) InZip() bool {
	return p.Inner != ""
}

// String implements the [fmt.Stringer] interface.
func (p *Path) String() string {
	if p.Inner == "" {
		return p.Outer
	}
	return fmt.Sprintf("%s%s%s", p.Outer, zipDelimiter, p.Inner)
}

func (z *zipEntryReadCloser) Read(p []byte) (int, error) {
	return z.body.Read(p)
}

func (z *zipEntryReadCloser) Close() error {
	return errors.Join(z.body.Close(), z.z.Close())
}

// FileExists checks if a file exists at the given path.
// An error is returned if the file is a directory.
func (fp *FileProvider) FileExists(path *Path) (bool, error) {
	if st, err := os.Stat(path.Outer); err != nil {
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
func (fp *FileProvider) NewPath(p string) (*Path, error) {
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
func ParsePath(p string) (*Path, error) {
	// A std:// path is the synthetic address of an embedded file and is not
	// made absolute: filepath.Abs would prepend the cwd and turn `std:` into
	// a directory name sitting under it. The inner half of the # convention
	// does not apply here either, since the stdlib is not an archive, so the
	// whole string is the outer half.
	if isStdPathStr(p) {
		return &Path{Outer: p}, nil
	}

	parts := strings.SplitN(p, zipDelimiter, 2)
	outer := parts[0]
	var inner string
	if len(parts) > 1 {
		inner = parts[1]
	}

	absOuter, err := filepath.Abs(filepath.ToSlash(outer))
	if err != nil {
		return nil, err
	}
	absOuter = filepath.Clean(absOuter)

	if inner == "" {
		path := NewPath(absOuter, "")
		return path, nil
	}

	innerClean := path.Clean(filepath.ToSlash(inner))
	if innerClean == "." || innerClean == ".." {
		return nil, fmt.Errorf("invalid path inside zip: %s", inner)
	} else if strings.Split(innerClean, "/")[0] == ".." {
		return nil, fmt.Errorf("path inside zip escapes archive root: %s", inner)
	}

	return NewPath(absOuter, innerClean), nil
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

// ProjectFilePath returns the project file path for the given path.
func (fp *FileProvider) ProjectFilePath(p *Path) (*Path, error) {
	if IsStdPath(p) {
		return StdPath(projectFileNameLong), nil
	}

	if !p.InZip() {
		for dir := range ancestorDirs(p.Outer) {
			for name := range projectFileNames {
				projectFilePath := filepath.Join(dir, name)
				if fi, statErr := os.Stat(projectFilePath); statErr == nil && fi.Mode().IsRegular() {
					return NewPath(projectFilePath, ""), nil
				}
			}
		}

		return nil, fmt.Errorf("project file missing for %s", p.String())
	}

	zr, err := zip.OpenReader(p.Outer)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	possibleProjectFilePaths := make(map[string]bool)
	for dir := range ancestorDirs(p.Inner) {
		for name := range projectFileNames {
			possibleProjectFilePaths[path.Join(dir, name)] = false
		}
	}

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		key := path.Clean(filepath.ToSlash(f.Name))
		if key == "." {
			continue
		}

		if _, ok := possibleProjectFilePaths[key]; !ok {
			continue
		}
		possibleProjectFilePaths[key] = true
	}

	for dir := range ancestorDirs(p.Inner) {
		for name := range projectFileNames {
			projectPath := path.Join(dir, name)
			if possibleProjectFilePaths[projectPath] {
				return NewPath(p.Outer, projectPath), nil
			}
		}
	}

	return nil, fmt.Errorf("project file missing for %s", p.String())
}

// ListCodeFiles answers the code files sitting directly in dir, one Path for
// each, sorted by base name. The listing is not recursive — a file in a
// subdirectory is not reached by this call, since a glob import only names
// what sits one level inside the directory it is written for. Non-code files
// are skipped; the extension is the one thing that says a file is source.
//
// The directory is addressed the same way a file is: a std:// path, a path
// inside a zip (Inner set), or an absolute filesystem path. The children come
// back in the same shape, so a caller that is already walking Paths does not
// need to know which backing store answered.
func (fp *FileProvider) ListCodeFiles(dir *Path) ([]*Path, error) {
	if IsStdPath(dir) {
		return listStdCodeFiles(dir)
	}
	if dir.InZip() {
		return listZipCodeFiles(dir)
	}
	return listFSCodeFiles(dir)
}

// listStdCodeFiles reads a directory out of the embedded stdlib FS. The outer
// of a std path is the whole address — std:// scheme plus the inner key — and
// embed.FS does not spell the scheme, so the walk is over the key alone.
func listStdCodeFiles(dir *Path) ([]*Path, error) {
	key := stdKey(dir)
	if key == "" {
		key = "."
	}
	entries, err := fs.ReadDir(std.FS, key)
	if err != nil {
		return nil, fmt.Errorf("cannot list stdlib directory %q: %w", dir.String(), err)
	}
	var children []*Path
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

// listZipCodeFiles walks a zip archive once, picking the entries whose
// directory is dir.Inner. Nested files are reached by [Path.Dir] on their
// cleaned name, which is what makes the walk non-recursive without a
// separate depth test.
func listZipCodeFiles(dir *Path) ([]*Path, error) {
	zr, err := zip.OpenReader(dir.Outer)
	if err != nil {
		return nil, fmt.Errorf("cannot open archive %q: %w", dir.Outer, err)
	}
	defer zr.Close()

	wantDir := dir.Inner
	if wantDir == "" {
		wantDir = "."
	}

	var children []*Path
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		cleaned := path.Clean(filepath.ToSlash(f.Name))
		if path.Dir(cleaned) != wantDir || !strings.HasSuffix(cleaned, CodeFileExtension) {
			continue
		}
		children = append(children, NewPath(dir.Outer, cleaned))
	}
	sortByBase(children)
	return children, nil
}

// listFSCodeFiles reads a directory from disk. dir.Outer is already absolute
// — [ParsePath] made it so — and so the children's paths are too.
func listFSCodeFiles(dir *Path) ([]*Path, error) {
	entries, err := os.ReadDir(dir.Outer)
	if err != nil {
		return nil, fmt.Errorf("cannot list directory %q: %w", dir.Outer, err)
	}
	var children []*Path
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), CodeFileExtension) {
			continue
		}
		children = append(children, NewPath(filepath.Join(dir.Outer, entry.Name()), ""))
	}
	sortByBase(children)
	return children, nil
}

// sortByBase sorts the paths by their file name, so that the order the glob
// expands in is the order the files sit under the directory and not the order
// the backing store happened to return them. [Path.Base] names the archive
// rather than the entry for a zip path — it is the base of Outer, which is
// one filename shared by every entry in the archive — so the inner is read
// first when there is one. The parser's own ordering is by absolute path and
// sorts them again on the way in, so this is the stable label a diagnostic
// quotes and nothing load-bearing.
func sortByBase(paths []*Path) {
	sort.Slice(paths, func(i, j int) bool {
		return childName(paths[i]) < childName(paths[j])
	})
}

func childName(p *Path) string {
	if p.Inner != "" {
		return path.Base(p.Inner)
	}
	return path.Base(p.Outer)
}

// Reader returns a reader for the given path.
func (fp *FileProvider) Reader(p *Path) (io.ReadCloser, error) {
	if IsStdPath(p) {
		return std.FS.Open(stdKey(p))
	}

	if !p.InZip() {
		return os.Open(p.Outer)
	}
	zr, err := zip.OpenReader(p.Outer)
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if path.Clean(filepath.ToSlash(f.Name)) != p.Inner {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = zr.Close()
			return nil, fmt.Errorf("zip entry %q is a directory", p.Inner)
		}
		body, err := f.Open()
		if err != nil {
			_ = zr.Close()
			return nil, err
		}
		return &zipEntryReadCloser{z: zr, body: body}, nil
	}
	_ = zr.Close()
	return nil, fmt.Errorf("missing zip entry for %s", p.String())
}

// MakeDir creates a directory at the given path, and returns true if the directory was created.
func (fp *FileProvider) MakeDir(path *Path) (bool, error) {
	dir := filepath.Dir(path.Outer)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return false, nil
	}
	err := os.MkdirAll(dir, 0755)
	return err == nil, err
}

// Create creates a file at the given path.
func (fp *FileProvider) Create(path string) (*os.File, error) {
	return os.Create(path)
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
