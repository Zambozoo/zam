package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/llvm"
	"github.com/zambozoo/zam/internal/target"
)

// TestRuntimeArchiveLinksWithLLVMOutput links an LLVM object calling into the
// runtime archive, compiled for the host against its pack, with the built-in
// lld, and runs it. The object's main exits zero only when the runtime's posit
// arithmetic answers 2 + 3 = 5.
func TestRuntimeArchiveLinksWithLLVMOutput(t *testing.T) {
	host, err := target.Host()
	require.NoError(t, err)
	pack, err := Pack(host)
	require.NoError(t, err)
	cacheDir := t.TempDir()
	archive, err := RuntimeArchive(cacheDir, host, pack)
	require.NoError(t, err)
	again, err := RuntimeArchive(cacheDir, host, pack)
	require.NoError(t, err)
	assert.Equal(t, archive, again, "a second ask answers the cached archive")

	llvm.InitializeAllTargetInfos()
	llvm.InitializeAllTargets()
	llvm.InitializeAllTargetMCs()
	llvm.InitializeAllAsmPrinters()

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("posit")
	defer mod.Dispose()
	builder := ctx.NewBuilder()
	defer builder.Dispose()

	i32, i64 := ctx.Int32Type(), ctx.Int64Type()
	fromIntType := llvm.FunctionType(i64, []llvm.Type{i64}, false)
	fromInt := llvm.AddFunction(mod, "zam_posit_from_int", fromIntType)
	addType := llvm.FunctionType(i64, []llvm.Type{i64, i64}, false)
	add := llvm.AddFunction(mod, "zam_posit_add", addType)

	main := llvm.AddFunction(mod, "main", llvm.FunctionType(i32, nil, false))
	builder.SetInsertPointAtEnd(ctx.AddBasicBlock(main, "entry"))
	posit := func(n uint64) llvm.Value {
		return builder.CreateCall(fromIntType, fromInt, []llvm.Value{llvm.ConstInt(i64, n, false)}, "")
	}
	sum := builder.CreateCall(addType, add, []llvm.Value{posit(2), posit(3)}, "")
	differs := builder.CreateICmp(llvm.IntNE, sum, posit(5), "")
	builder.CreateRet(builder.CreateZExt(differs, i32, ""))
	require.NoError(t, llvm.VerifyModule(mod, llvm.ReturnStatusAction))

	triple := host.Triple
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

	dir := t.TempDir()
	obj := filepath.Join(dir, "posit.o")
	exe := host.Executable(filepath.Join(dir, "posit"))
	require.NoError(t, os.WriteFile(obj, buf.Bytes(), 0o600))

	require.NoError(t, linkers[host.OS](host, pack, exe, []string{obj, archive}, nil, false))

	out, err := exec.Command(exe).CombinedOutput()
	require.NoError(t, err, string(out))
}
