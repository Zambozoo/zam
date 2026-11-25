package codegen

import (
	"cmp"
	"slices"

	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	"github.com/zambozoo/zam/internal/compiler/2_codegen/llvm"
)

// CoveredStatement is a statement a program compiled for coverage records
// running, by where it starts.
type CoveredStatement struct {
	Path   base.Path
	Line   int
	Column int
}

// llvmCoverage is the byte a program compiled for coverage sets when each of
// its covered statements runs.
type llvmCoverage struct {
	// slots is the byte of each statement, by the token it starts at.
	slots      map[*syntax.Token]int
	statements []CoveredStatement
	counters   llvm.Value
}

// covered reports whether the statements of the file at path are covered:
// those of the program's own files, and not the standard library's or its
// tests'.
func covered(path base.Path) bool {
	return !base.IsStdPath(path) && !path.IsTestFile()
}

// newLLVMCoverage gives every statement of the covered files of ast a byte of
// the counters it lays out, in the order they are written.
func newLLVMCoverage(ctx llvm.Context, mod llvm.Module, ast *syntax.AST) *llvmCoverage {
	type started struct {
		tok       *syntax.Token
		statement CoveredStatement
	}
	var all []started
	for _, entry := range ast.CodeFiles.All() {
		file := entry.Value
		if !covered(file.Path) {
			continue
		}
		c := &collector{statement: func(s syntax.Stmt) {
			position := file.Location(s.Start()).Position
			if position == nil {
				return
			}
			all = append(all, started{s.Start(), CoveredStatement{Path: file.Path, Line: position.Line, Column: position.Column}})
		}}
		c.decl(file.Decl)
	}
	slices.SortStableFunc(all, func(a, b started) int {
		return cmp.Or(
			cmp.Compare(a.statement.Path, b.statement.Path),
			cmp.Compare(a.statement.Line, b.statement.Line),
			cmp.Compare(a.statement.Column, b.statement.Column),
		)
	})

	c := &llvmCoverage{slots: make(map[*syntax.Token]int, len(all))}
	for _, s := range all {
		if _, ok := c.slots[s.tok]; ok {
			continue
		}
		c.slots[s.tok] = len(c.statements)
		c.statements = append(c.statements, s.statement)
	}
	byteType := ctx.Int8Type()
	c.counters = llvm.AddGlobal(mod, llvm.ArrayType(byteType, len(c.statements)), "zam_coverage")
	c.counters.SetInitializer(llvm.ConstNull(llvm.ArrayType(byteType, len(c.statements))))
	c.counters.SetLinkage(llvm.PrivateLinkage)
	return c
}

// cover sets the byte of the statement tok starts, when it is covered.
func (e *funcEmitter) cover(tok *syntax.Token) {
	if e.p.coverage == nil {
		return
	}
	slot, ok := e.p.coverage.slots[tok]
	if !ok {
		return
	}
	at := e.b.CreateInBoundsGEP(e.ctx.Int8Type(), e.p.coverage.counters, []llvm.Value{e.const64(uint64(slot))}, "")
	e.b.CreateStore(llvm.ConstInt(e.ctx.Int8Type(), 1, false), at)
}
