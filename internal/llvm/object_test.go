package llvm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmitObjectLinksAndRuns emits an object for a main that calls puts, links
// it with the system C compiler, and runs it.
func TestEmitObjectLinksAndRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no system C compiler for its own objects; lld links them in TestLinkCOFF")
	}
	InitializeAllTargetInfos()
	InitializeAllTargets()
	InitializeAllTargetMCs()
	InitializeAllAsmPrinters()

	ctx := NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("hello")
	defer mod.Dispose()
	builder := ctx.NewBuilder()
	defer builder.Dispose()

	i32 := ctx.Int32Type()
	ptr := PointerType(ctx.Int8Type(), 0)
	putsType := FunctionType(i32, []Type{ptr}, false)
	puts := AddFunction(mod, "puts", putsType)

	mainType := FunctionType(i32, nil, false)
	main := AddFunction(mod, "main", mainType)
	builder.SetInsertPointAtEnd(ctx.AddBasicBlock(main, "entry"))
	greeting := builder.CreateGlobalStringPtr("hello from llvm", "greeting")
	builder.CreateCall(putsType, puts, []Value{greeting}, "")
	builder.CreateRet(ConstInt(i32, 0, false))
	require.NoError(t, VerifyModule(mod, ReturnStatusAction))

	triple := DefaultTargetTriple()
	target, err := GetTargetFromTriple(triple)
	require.NoError(t, err)
	machine := target.CreateTargetMachine(triple, "", "", CodeGenLevelDefault, RelocPIC, CodeModelDefault)
	defer machine.Dispose()
	mod.SetTarget(triple)
	data := machine.CreateTargetData()
	defer data.Dispose()
	mod.SetDataLayout(data.String())

	buf, err := machine.EmitToMemoryBuffer(mod, ObjectFile)
	require.NoError(t, err)
	defer buf.Dispose()

	dir := t.TempDir()
	obj := filepath.Join(dir, "hello.o")
	exe := filepath.Join(dir, "hello")
	require.NoError(t, os.WriteFile(obj, buf.Bytes(), 0o600))
	out, err := exec.Command("cc", obj, "-o", exe).CombinedOutput()
	require.NoError(t, err, string(out))

	out, err = exec.Command(exe).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "hello from llvm\n", string(out))
}
