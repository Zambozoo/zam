package main

import (
	"bytes"
	"os"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	semantic "github.com/zambozoo/zam/internal/3_semantic"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
	build "github.com/zambozoo/zam/internal/5_build"
)

type step string

const (
	stepCommand  step = "command"
	stepSyntax   step = "syntax"
	stepSemantic step = "semantic"
	stepCodegen  step = "codegen"
	stepExport   step = "export"
	stepBuild    step = "build"
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

	gitProvider := base.NewGitProvider(&fileProvider)
	syntaxAST, errs := syntax.NewAST(cmd, &fileProvider, gitProvider)
	handleErrors(&logger, stepSyntax, errs...)

	errs = semantic.ValidateAST(syntaxAST)
	handleErrors(&logger, stepSemantic, errs...)

	result, err := codegen.GenerateCode(cmd, syntaxAST)
	handleErrors(&logger, stepCodegen, err)

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
