package lld

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/compiler/2_codegen/llvm"
)

// emitObject writes an object for triple holding a function named entry, which
// calls puts with a greeting when withPuts is set and otherwise loops forever.
func emitObject(t *testing.T, path, triple, entry string, withPuts bool) {
	t.Helper()

	llvm.InitializeAllTargetInfos()
	llvm.InitializeAllTargets()
	llvm.InitializeAllTargetMCs()
	llvm.InitializeAllAsmPrinters()

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("lld")
	defer mod.Dispose()
	builder := ctx.NewBuilder()
	defer builder.Dispose()

	i32 := ctx.Int32Type()
	fnType := llvm.FunctionType(i32, nil, false)
	fn := llvm.AddFunction(mod, entry, fnType)
	entryBlock := ctx.AddBasicBlock(fn, "entry")
	builder.SetInsertPointAtEnd(entryBlock)
	if withPuts {
		putsType := llvm.FunctionType(i32, []llvm.Type{llvm.PointerType(ctx.Int8Type(), 0)}, false)
		puts := llvm.AddFunction(mod, "puts", putsType)
		greeting := builder.CreateGlobalStringPtr("hello from lld", "greeting")
		builder.CreateCall(putsType, puts, []llvm.Value{greeting}, "")
		builder.CreateRet(llvm.ConstInt(i32, 0, false))
	} else {
		loop := ctx.AddBasicBlock(fn, "loop")
		builder.CreateBr(loop)
		builder.SetInsertPointAtEnd(loop)
		builder.CreateBr(loop)
	}
	require.NoError(t, llvm.VerifyModule(mod, llvm.ReturnStatusAction))

	target, err := llvm.GetTargetFromTriple(triple)
	require.NoError(t, err)
	machine := target.CreateTargetMachine(triple, "", "", llvm.CodeGenLevelDefault, llvm.RelocPIC, llvm.CodeModelDefault)
	defer machine.Dispose()
	mod.SetTarget(triple)
	data := machine.CreateTargetData()
	defer data.Dispose()
	mod.SetDataLayout(data.String())

	buf, err := machine.EmitToMemoryBuffer(mod, llvm.ObjectFile)
	require.NoError(t, err)
	defer buf.Dispose()
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

// TestLinkELFAndCOFF links freestanding Linux and Windows executables, which
// cannot run here, and checks that each came out in its format.
func TestLinkELFAndCOFF(t *testing.T) {
	dir := t.TempDir()

	elfObj := filepath.Join(dir, "start.o")
	elfExe := filepath.Join(dir, "start")
	emitObject(t, elfObj, "x86_64-unknown-linux-gnu", "_start", false)
	require.NoError(t, Link(ELF, []string{"-static", "-e", "_start", "-o", elfExe, elfObj}))
	assertMagic(t, elfExe, "\x7fELF")

	coffObj := filepath.Join(dir, "start.obj")
	coffExe := filepath.Join(dir, "start.exe")
	emitObject(t, coffObj, "x86_64-pc-windows-msvc", "start", false)
	require.NoError(t, Link(COFF, []string{"/entry:start", "/subsystem:console", "/nodefaultlib", "/out:" + coffExe, coffObj}))
	assertMagic(t, coffExe, "MZ")
}

// TestLinkReportsErrors answers lld's diagnostics rather than exiting.
func TestLinkReportsErrors(t *testing.T) {
	err := Link(ELF, []string{"-o", filepath.Join(t.TempDir(), "out"), "missing.o"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.o")

	require.NoError(t, Link(ELF, []string{"--version"}))
}

func assertMagic(t *testing.T, path, magic string) {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(b), len(magic))
	assert.Equal(t, magic, string(b[:len(magic)]))
}
