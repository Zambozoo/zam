// Package zamruntime is the C runtime every zam program is linked against: its
// sources and headers, and what they say of the standard library's natives.
package zamruntime

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sync"
)

// FS is every C source and header of the runtime: the runtime's own sources,
// the src/*.h headers each source includes, and the zam/*.h headers a
// project's native C writes against.
//
//go:embed src/*.c src/*.h include/zam/*.h
var FS embed.FS

// CoreNativeTable is the table of the natives every program links, whatever
// it calls.
const CoreNativeTable = "zamNativeTable"

var (
	tablePattern = regexp.MustCompile(`(?s)const ZamNativeEntry (zam\w*NativeTable)\[\] = \{(.*?)\};`)
	namePattern  = regexp.MustCompile(`\{"(STD_\w+)"`)
)

// NativeTables answers, for each standard library native, the table of the
// runtime source that answers it. Programs link a source's natives only when
// they call one, which is what keeps the libraries it wraps out of a program
// that does not.
var NativeTables = sync.OnceValues(func() (map[string]string, error) {
	tables := map[string]string{}
	sources, err := fs.Glob(FS, "src/*.c")
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		text, err := FS.ReadFile(source)
		if err != nil {
			return nil, err
		}
		for _, table := range tablePattern.FindAllSubmatch(text, -1) {
			for _, name := range namePattern.FindAllSubmatch(table[2], -1) {
				if other, ok := tables[string(name[1])]; ok {
					return nil, fmt.Errorf("native %s is answered by both %s and %s", name[1], other, table[1])
				}
				tables[string(name[1])] = string(table[1])
			}
		}
	}
	return tables, nil
})
