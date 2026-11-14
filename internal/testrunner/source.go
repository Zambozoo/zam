package testrunner

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	base "github.com/zambozoo/zam/internal/0_base"
)

// runnerDecl names the runner's root declaration, and so its file.
const runnerDecl = "ZamTestMain"

// runnerFileProvider serves the runner's source at its path, which is on no
// disk, and everything else from the files it wraps.
type runnerFileProvider struct {
	*base.FileProvider
	path   base.Path
	source string
}

// RunnerPath returns where the runner of tests whose first file is first sits.
func RunnerPath(first base.Path) base.Path {
	return base.Path(filepath.Join(filepath.Dir(string(first)), runnerDecl+base.TestFileSuffix))
}

// Source returns the runner of files: a program whose first argument is the
// index of the file whose tests it runs, and whose other arguments it passes on
// to each test.
func Source(files []File) (string, error) {
	var b strings.Builder
	b.WriteString("import std(\"test/Test.zam\");\n")
	for i, file := range files {
		path := filepath.ToSlash(string(file.Path))
		if strings.ContainsAny(path, "\"\\") {
			return "", fmt.Errorf("%s cannot be imported by the test runner", file.Path)
		}
		fmt.Fprintf(&b, "import t%d = \"%s\";\n", i, path)
	}

	b.WriteString("\nnamespace " + runnerDecl + " {\n")
	b.WriteString("    fun void(uint, str...) main = (argC, argV) {\n")
	b.WriteString("        var uint n = argC - 1U;\n")
	b.WriteString("        var str* args = new(str, argC);\n")
	b.WriteString("        for (var uint i = 0U; i < n; i = i + 1U) {\n")
	b.WriteString("            args[i] = argV[i + 1U];\n")
	b.WriteString("        }\n")
	for i, file := range files {
		keyword := "if"
		if i > 0 {
			keyword = "} else if"
		}
		fmt.Fprintf(&b, "        %s (argV[0] == \"%d\") {\n", keyword, i)
		for _, test := range file.Tests {
			fmt.Fprintf(&b, "            Test::Begin(\"%s.%s\");\n", file.Decl, test)
			fmt.Fprintf(&b, "            t%d.%s::%s(n, args...);\n", i, file.Decl, test)
			b.WriteString("            Test::End();\n")
		}
	}
	b.WriteString("        }\n")
	b.WriteString("        Test::Finish();\n")
	b.WriteString("    }\n")
	b.WriteString("}\n")
	return b.String(), nil
}

// Reader serves the runner's source at its path.
func (fp *runnerFileProvider) Reader(path base.Path) (io.ReadCloser, error) {
	if path == fp.path {
		return io.NopCloser(strings.NewReader(fp.source)), nil
	}
	return fp.FileProvider.Reader(path)
}

// FileExists reports the runner as existing.
func (fp *runnerFileProvider) FileExists(path base.Path) (bool, error) {
	if path == fp.path {
		return true, nil
	}
	return fp.FileProvider.FileExists(path)
}
