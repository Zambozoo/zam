package build_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zambozoo/zam/internal/base/target"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
)

// generate compiles the program ast holds, parsed by cmd, to an object as opts
// says: with the Go compiler, or with the compiler written in zam when
// ZAM_CODEGEN names its executable, which is run as build over argv, the
// arguments after the command name a zam build of the program takes.
func generate(t testing.TB, argv []string, cmd *cli.Command, ast *syntax.AST, opts codegen.ObjectOptions) (*codegen.Object, error) {
	t.Helper()
	zam := os.Getenv("ZAM_CODEGEN")
	if zam == "" {
		return codegen.GenerateObject(cmd, ast, opts)
	}
	std, err := filepath.Abs(filepath.Join("..", "..", "..", "src", "std"))
	if err != nil {
		return nil, err
	}
	runtime, err := filepath.Abs("runtime")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(t.TempDir(), "main.o")
	args := []string{"build"}
	if opts.Debug {
		args = append(args, "-O", "0")
	}
	if host, err := target.Host(); err == nil && host.Triple != opts.Triple {
		for _, name := range target.Names() {
			if named, err := target.Parse(name); err == nil && named.Triple == opts.Triple {
				args = append(args, "--target", name)
			}
		}
	}
	var stderr bytes.Buffer
	c := exec.Command(zam, append(args, argv...)...)
	c.Env = append(os.Environ(), "ZAM_STD="+std, "ZAM_RUNTIME="+runtime, "ZAM_OBJECT="+path)
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	object, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &codegen.Object{Bytes: object, Triple: opts.Triple}, nil
}
