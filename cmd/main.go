package main

import (
	"bytes"
	"os"
	"path/filepath"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
	"github.com/zambozoo/zam/internal/debugger"
	"github.com/zambozoo/zam/internal/lsp"
	"github.com/zambozoo/zam/internal/testrunner"
)

type step string

const (
	stepCommand  step = "command"
	stepSyntax   step = "syntax"
	stepSemantic step = "semantic"
	stepCodegen  step = "codegen"
	stepExport   step = "export"
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

	cmd, err := command.New(os.Args, &fileProvider)
	handleErrors(&logger, stepCommand, err)

	if cmd.Verb == command.VerbLSP {
		handleErrors(&logger, stepLSP, lsp.Run())
		return
	}

	zamPath, err := base.ZamPath()
	handleErrors(&logger, stepCommand, err)
	packageProvider := base.NewPackageProvider(filepath.Join(zamPath, "pkg"))

	if cmd.Verb == command.VerbDebug {
		handleErrors(&logger, stepDebug, debugger.Run(&fileProvider, packageProvider))
		return
	}

	switch cmd.Verb {
	case command.VerbClean:
		handleErrors(&logger, stepClean, packageProvider.Clean())
		logger.Print(base.LogLevelInfo, "removed %s\n", packageProvider.Root())
		cached, err := build.CleanCache()
		handleErrors(&logger, stepClean, err)
		for _, dir := range cached {
			logger.Print(base.LogLevelInfo, "removed %s\n", dir)
		}
		logger.Flush()
		base.Exit(base.ExitCodeSuccess)
	case command.VerbTest:
		passed, err := testrunner.Run(cmd, &fileProvider, packageProvider, os.Stdout)
		handleErrors(&logger, stepTest, err)
		logger.Flush()
		if !passed {
			base.Exit(base.ExitCodeError)
		}
		base.Exit(base.ExitCodeSuccess)
	case command.VerbGet:
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

	if cmd.Native {
		buildNative(&logger, cmd, syntaxAST)
	}

	result, err := codegen.GenerateCode(cmd, syntaxAST)
	handleErrors(&logger, stepCodegen, err)

	if cmd.Verb == command.VerbRun {
		code, err := build.Run(result.Image, result.NativeNames, build.Options{
			Project:      syntaxAST.MainCodeDecl.File().Project,
			KeepBuildDir: cmd.KeepBuild,
		}, cmd.RunArgs)
		handleErrors(&logger, stepRun, err)
		logger.Flush()
		base.Exit(base.ExitCode(code))
	}

	if cmd.EmitImage {
		err = fileProvider.WriteFile(cmd.OutputPath.String(), bytes.NewReader(result.Image))
		handleErrors(&logger, stepExport, err)
	} else {
		err = build.Build(result.Image, result.NativeNames, build.Options{
			OutputPath:   cmd.OutputPath.String(),
			Project:      syntaxAST.MainCodeDecl.File().Project,
			KeepBuildDir: cmd.KeepBuild,
		})
		handleErrors(&logger, stepBuild, err)
	}

	logger.Print(base.LogLevelInfo, "successfully generated code \n")
	logger.Flush()
	base.Exit(base.ExitCodeSuccess)
}

// buildNative compiles the program to native code and builds or runs it,
// saying which methods it could not compile yet.
func buildNative(logger *base.Logger, cmd *command.Command, syntaxAST *syntax.AST) {
	object, err := codegen.GenerateObject(cmd, syntaxAST, codegen.ObjectOptions{Triple: cmd.Target.Triple})
	handleErrors(logger, stepCodegen, err)
	for _, unsupported := range object.Unsupported {
		logger.Print(base.LogLevelWarn, "native: compiled as a trap: %s\n", unsupported)
	}

	opts := build.Options{Project: syntaxAST.MainCodeDecl.File().Project, Target: cmd.Target}
	if cmd.Verb == command.VerbRun {
		code, err := build.RunNative(object.Bytes, opts, cmd.RunArgs)
		handleErrors(logger, stepRun, err)
		logger.Flush()
		base.Exit(base.ExitCode(code))
	}

	opts.OutputPath = cmd.OutputPath.String()
	handleErrors(logger, stepBuild, build.LinkObject(object.Bytes, opts))
	logger.Print(base.LogLevelInfo, "successfully generated code \n")
	logger.Flush()
	base.Exit(base.ExitCodeSuccess)
}
