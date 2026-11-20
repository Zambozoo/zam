package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/zambozoo/zam/internal/base"
	"github.com/zambozoo/zam/internal/cli"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	semantic "github.com/zambozoo/zam/internal/compiler/1_semantic"
	codegen "github.com/zambozoo/zam/internal/compiler/2_codegen"
	build "github.com/zambozoo/zam/internal/compiler/3_build"
	"github.com/zambozoo/zam/internal/debugger"
	"github.com/zambozoo/zam/internal/lsp"
	"github.com/zambozoo/zam/internal/test"
)

type step string

const (
	stepCommand  step = "command"
	stepSyntax   step = "syntax"
	stepSemantic step = "semantic"
	stepCodegen  step = "codegen"
	stepBuild    step = "build"
	stepRun      step = "run"
	stepLSP      step = "lsp"
	stepDebug    step = "debug"
	stepGet      step = "get"
	stepClean    step = "clean"
	stepTest     step = "test"
)

func handleErrors(logger *base.Logger, step step, errs ...error) {
	if len(errs) == 0 {
		return
	}

	var hasError bool
	for _, err := range errs {
		if err == nil {
			continue
		}

		if !hasError {
			hasError = true
			logger.Flush()
		}

		logger.Print(base.LogLevelError, "[%s] %s\n", step, err.Error())
	}

	if hasError {
		base.Exit(base.ExitCodeError)
	}
}

func main() {
	logger := base.Logger{}

	fileProvider := base.FileProvider{}

	cmd, err := cli.New(os.Args, &fileProvider)
	handleErrors(&logger, stepCommand, err)

	if cmd.Verb == cli.VerbVersion {
		fmt.Println("zam", build.Version)
		return
	}

	if cmd.Verb == cli.VerbLSP {
		handleErrors(&logger, stepLSP, lsp.Run())
		return
	}

	zamPath, err := base.ZamPath()
	handleErrors(&logger, stepCommand, err)
	packageProvider := base.NewPackageProvider(filepath.Join(zamPath, "pkg"))

	if cmd.Verb == cli.VerbDebug {
		handleErrors(&logger, stepDebug, debugger.Run(&fileProvider, packageProvider))
		return
	}

	switch cmd.Verb {
	case cli.VerbClean:
		handleErrors(&logger, stepClean, packageProvider.Clean())
		logger.Print(base.LogLevelInfo, "removed %s\n", packageProvider.Root())
		cached, err := build.CleanCache()
		handleErrors(&logger, stepClean, err)
		for _, dir := range cached {
			logger.Print(base.LogLevelInfo, "removed %s\n", dir)
		}
		logger.Flush()
		base.Exit(base.ExitCodeSuccess)
	case cli.VerbTest:
		passed, err := test.Run(cmd, &fileProvider, packageProvider, os.Stdout)
		handleErrors(&logger, stepTest, err)
		logger.Flush()
		if !passed {
			base.Exit(base.ExitCodeError)
		}
		base.Exit(base.ExitCodeSuccess)
	case cli.VerbGet:
		cwd, err := base.ParsePath(".")
		handleErrors(&logger, stepGet, err)
		projectFile, err := fileProvider.ProjectFileIn(cwd)
		handleErrors(&logger, stepGet, err)
		err = syntax.Get(projectFile, cmd.Refs, cmd.As, &fileProvider, packageProvider)
		handleErrors(&logger, stepGet, err)
		logger.Flush()
		base.Exit(base.ExitCodeSuccess)
	}

	syntaxAST, errs := syntax.NewAST(cmd, &fileProvider, packageProvider)
	handleErrors(&logger, stepSyntax, errs...)

	errs = semantic.ValidateAST(syntaxAST)
	handleErrors(&logger, stepSemantic, errs...)

	object, err := codegen.GenerateObject(cmd, syntaxAST, codegen.ObjectOptions{Triple: cmd.Target.Triple, Optimization: cmd.Optimization})
	handleErrors(&logger, stepCodegen, err)
	for _, unsupported := range object.Unsupported {
		logger.Print(base.LogLevelWarn, "compiled as a trap: %s\n", unsupported)
	}

	opts := build.Options{Project: syntaxAST.MainCodeDecl.File().Project, Target: cmd.Target}
	if cmd.Verb == cli.VerbRun {
		code, err := build.Run(object.Bytes, opts, cmd.RunArgs)
		handleErrors(&logger, stepRun, err)
		logger.Flush()
		base.Exit(base.ExitCode(code))
	}

	opts.OutputPath, err = build.ExecutablePath(opts, cmd.OutputPath.String())
	handleErrors(&logger, stepBuild, err)
	handleErrors(&logger, stepBuild, build.LinkObject(object.Bytes, opts))
	logger.Print(base.LogLevelInfo, "successfully generated code \n")
	logger.Flush()
	base.Exit(base.ExitCodeSuccess)
}
