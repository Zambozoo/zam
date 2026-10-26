package lsp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	base "github.com/zambozoo/zam/internal/0_base"
)

// overlay is a file provider that answers for open documents from the editor's
// buffers rather than from disk, so that what is analyzed is what is being
// edited. Once the standard library's source has been opened from disk, its
// files are read from there rather than from what is compiled in. Every other
// question goes to the file system.
type overlay struct {
	*base.FileProvider

	mu sync.RWMutex
	// buffers holds each open document's text by the path the compiler knows
	// it under.
	buffers map[string]string
	// stdRoot is where the standard library's source was opened from on disk,
	// if it has been.
	stdRoot string
}

func newOverlay() *overlay {
	return &overlay{FileProvider: &base.FileProvider{}, buffers: map[string]string{}}
}

// set records the text of an open document.
func (o *overlay) set(path *base.Path, text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buffers[path.String()] = text
}

// remove forgets an open document, so that it is read from disk again.
func (o *overlay) remove(path *base.Path) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.buffers, path.String())
}

// setStdRoot records where the standard library's source is on disk.
func (o *overlay) setStdRoot(root string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stdRoot = root
}

// diskPath returns where a file is on disk. A standard library file is on
// disk only once its source has been opened from there.
func (o *overlay) diskPath(path *base.Path) (string, bool) {
	if path.InZip() {
		return "", false
	}
	if !base.IsStdPath(path) {
		return path.Outer, true
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.stdRoot == "" {
		return "", false
	}
	return filepath.Join(o.stdRoot, filepath.FromSlash(base.StdName(path))), true
}

// onDisk returns where a standard library file is on disk, if its source has
// been opened from there and has the file.
func (o *overlay) onDisk(path *base.Path) (string, bool) {
	if !base.IsStdPath(path) {
		return "", false
	}
	filePath, ok := o.diskPath(path)
	if !ok {
		return "", false
	}
	_, err := os.Stat(filePath)
	return filePath, err == nil
}

// buffer returns the text of an open document.
func (o *overlay) buffer(path *base.Path) (string, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	text, ok := o.buffers[path.String()]
	return text, ok
}

// FileExists reports an open document as existing whether or not it has been
// saved.
func (o *overlay) FileExists(path *base.Path) (bool, error) {
	if _, ok := o.buffer(path); ok {
		return true, nil
	}
	if _, ok := o.onDisk(path); ok {
		return true, nil
	}
	return o.FileProvider.FileExists(path)
}

// Reader reads an open document from its buffer.
func (o *overlay) Reader(path *base.Path) (io.ReadCloser, error) {
	if text, ok := o.buffer(path); ok {
		return io.NopCloser(strings.NewReader(text)), nil
	}
	if filePath, ok := o.onDisk(path); ok {
		return os.Open(filePath)
	}
	return o.FileProvider.Reader(path)
}

// read returns the whole text of a file as the compiler would read it.
func (o *overlay) read(path *base.Path) (string, error) {
	r, err := o.Reader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	return string(b), err
}
