package build_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
)

// phasesEntry is the program BenchmarkCompilePhases compiles, as zam run does.
var phasesEntry = filepath.Join("..", "..", "..", "docs", "0_example", "fizzbuzz", "Main.zam")

// BenchmarkCompilePhases times each phase zam run takes a program through,
// with the runtime already compiled: parsing, checking, generating code
// unoptimized and at the default optimization, linking, and running the
// binary. Each phase is timed alone, over the output of the ones before it.
func BenchmarkCompilePhases(b *testing.B) {
	entry, err := filepath.Abs(phasesEntry)
	require.NoError(b, err)
	out := testTarget().Executable(filepath.Join(b.TempDir(), "out"))
	files := &base.FileProvider{}
	packages := base.NewPackageProvider(b.TempDir())
	command := func(b *testing.B, optimization string) *cli.Command {
		cmd, err := cli.New([]string{"zam", "build", "-O" + optimization, "-o", out, entry}, files)
		require.NoError(b, err)
		return cmd
	}
	parse := func(b *testing.B, cmd *cli.Command) *syntax.AST {
		ast, errs := syntax.NewAST(cmd, files, packages)
		require.Empty(b, errs)
		return ast
	}
	check := func(b *testing.B, ast *syntax.AST) {
		require.Empty(b, semantic.ValidateAST(ast))
	}
	generate := func(b *testing.B, cmd *cli.Command, ast *syntax.AST) []byte {
		object, err := codegen.GenerateObject(cmd, ast, codegen.ObjectOptions{Triple: testTarget().Triple, Optimization: cmd.Optimization})
		require.NoError(b, err)
		return object.Bytes
	}
	cache, err := nativeRuntimeCache()
	require.NoError(b, err)
	link := func(b *testing.B, object []byte, project *syntax.Project) {
		require.NoError(b, build.LinkObject(object, build.Options{
			OutputPath:      out,
			Project:         project,
			RuntimeCacheDir: cache,
			Target:          testTarget(),
		}))
	}

	b.Run("parse", func(b *testing.B) {
		cmd := command(b, "2")
		for range b.N {
			parse(b, cmd)
		}
	})
	b.Run("check", func(b *testing.B) {
		cmd := command(b, "2")
		for range b.N {
			b.StopTimer()
			ast := parse(b, cmd)
			b.StartTimer()
			check(b, ast)
		}
	})
	for _, level := range []string{"0", "2"} {
		b.Run("codegen/O"+level, func(b *testing.B) {
			cmd := command(b, level)
			for range b.N {
				b.StopTimer()
				ast := parse(b, cmd)
				check(b, ast)
				b.StartTimer()
				generate(b, cmd, ast)
			}
		})
	}
	cmd := command(b, "2")
	ast := parse(b, cmd)
	check(b, ast)
	object := generate(b, cmd, ast)
	link(b, object, ast.MainCodeDecl.File().Project)
	b.Run("link", func(b *testing.B) {
		for range b.N {
			link(b, object, ast.MainCodeDecl.File().Project)
		}
	})
	b.Run("run", func(b *testing.B) {
		for range b.N {
			_, stderr, status := run(b, out)
			require.Zero(b, status, stderr)
		}
	})
	_ = os.Remove(out)
}
