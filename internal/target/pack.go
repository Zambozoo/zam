package target

import "path/filepath"

// PackLibraries are the libraries of a pack the runtime's natives wrap, each
// before those it calls. A link takes from them only what the program reaches.
var PackLibraries = []string{
	"-lcurl", "-lssl", "-lcrypto", "-larchive", "-lxml2", "-ljson-c", "-lyaml", "-lbson2", "-lcbor",
	"-lllhttp", "-lsodium", "-lbrotlienc", "-lbrotlidec", "-lbrotlicommon", "-lzstd", "-llz4", "-lz",
}

// PackCFlags are the flags C is compiled with against the headers of pack.
func PackCFlags(pack string) []string {
	include := filepath.Join(pack, "include")
	flags := []string{
		"-DCURL_STATICLIB", "-DBSON_STATIC", "-DLIBXML_STATIC",
		"-I" + include, "-I" + filepath.Join(include, "libxml2"),
	}
	bson, _ := filepath.Glob(filepath.Join(include, "bson-*"))
	for _, dir := range bson {
		flags = append(flags, "-I"+dir)
	}
	return flags
}
