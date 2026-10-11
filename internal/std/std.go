package std

import "embed"

// FS holds the built-in standard library source and project files. Subpackage
// directories (lang, io, …) are embedded whole so a file added underneath them
// is picked up without this list growing.
//
//go:embed *.yml codec crypto data io lang math sync
var FS embed.FS
