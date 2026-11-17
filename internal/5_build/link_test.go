package build

import (
	"debug/pe"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zambozoo/zam/internal/llvm"
	"github.com/zambozoo/zam/internal/target"
)

// putsObject answers an object for triple whose main writes a line with puts,
// which every C library has.
func putsObject(t *testing.T, triple string) []byte {
	t.Helper()
	llvm.InitializeAllTargetInfos()
	llvm.InitializeAllTargets()
	llvm.InitializeAllTargetMCs()
	llvm.InitializeAllAsmPrinters()

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("puts")
	defer mod.Dispose()
	builder := ctx.NewBuilder()
	defer builder.Dispose()

	i32 := ctx.Int32Type()
	putsType := llvm.FunctionType(i32, []llvm.Type{llvm.PointerType(ctx.Int8Type(), 0)}, false)
	puts := llvm.AddFunction(mod, "puts", putsType)
	main := llvm.AddFunction(mod, "main", llvm.FunctionType(i32, nil, false))
	builder.SetInsertPointAtEnd(ctx.AddBasicBlock(main, "entry"))
	builder.CreateCall(putsType, puts, []llvm.Value{builder.CreateGlobalStringPtr("hello", "greeting")}, "")
	builder.CreateRet(llvm.ConstInt(i32, 0, false))
	require.NoError(t, llvm.VerifyModule(mod, llvm.ReturnStatusAction))

	llvmTarget, err := llvm.GetTargetFromTriple(triple)
	require.NoError(t, err)
	machine := llvmTarget.CreateTargetMachine(triple, "", "", llvm.CodeGenLevelDefault, llvm.RelocPIC, llvm.CodeModelDefault)
	defer machine.Dispose()
	mod.SetTarget(triple)
	data := machine.CreateTargetData()
	defer data.Dispose()
	mod.SetDataLayout(data.String())
	buf, err := machine.EmitToMemoryBuffer(mod, llvm.ObjectFile)
	require.NoError(t, err)
	defer buf.Dispose()
	return append([]byte(nil), buf.Bytes()...)
}

// runsUnderWine reports whether a program for t runs here under Wine: Wine is
// installed on Linux and t is for the machine's architecture.
func runsUnderWine(t target.Target) bool {
	host, err := target.Host()
	if err != nil || host.OS != target.Linux || host.Arch != t.Arch {
		return false
	}
	_, err = exec.LookPath("wine")
	return err == nil
}

// wineCommand runs the Windows program at path under Wine, which offers no
// Mono or Gecko install it would wait on, and says nothing of its own.
func wineCommand(path string, args ...string) *exec.Cmd {
	cmd := exec.Command("wine", append([]string{path}, args...)...)
	cmd.Env = append(os.Environ(), "WINEDEBUG=-all", "WINEDLLOVERRIDES=mscoree,mshtml=")
	return cmd
}

// TestLinkCOFF links a console program for each Windows target against its
// pack's C runtime and import libraries, as Zig would, and runs the one Wine
// runs here.
func TestLinkCOFF(t *testing.T) {
	t.Parallel()
	machines := map[string]uint16{"aarch64": pe.IMAGE_FILE_MACHINE_ARM64, "x86_64": pe.IMAGE_FILE_MACHINE_AMD64}
	for _, name := range target.Names() {
		platform, err := target.Parse(name)
		require.NoError(t, err)
		if platform.OS != target.Windows {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pack, err := Pack(platform)
			require.NoError(t, err)
			dir := t.TempDir()
			object := filepath.Join(dir, "puts.obj")
			require.NoError(t, os.WriteFile(object, putsObject(t, platform.Triple), 0o600))
			exe := filepath.Join(dir, "puts.exe")
			require.NoError(t, linkCOFF(platform, pack, exe, []string{object}, nil, false))

			file, err := pe.Open(exe)
			require.NoError(t, err)
			defer file.Close()
			assert.Equal(t, machines[platform.Arch], file.Machine)
			header, ok := file.OptionalHeader.(*pe.OptionalHeader64)
			require.True(t, ok)
			assert.Equal(t, uint16(pe.IMAGE_SUBSYSTEM_WINDOWS_CUI), header.Subsystem, "a console program")

			if runsUnderWine(platform) {
				output, err := wineCommand(exe).Output()
				require.NoError(t, err)
				assert.Equal(t, "hello", strings.TrimSpace(string(output)))
			}
		})
	}
}

// An executable for Windows is named with .exe, one for any other target as
// asked, and one for no target in particular as the host names it.
func TestExecutablePath(t *testing.T) {
	t.Parallel()
	windows, err := target.Parse("aarch64-windows-gnu")
	require.NoError(t, err)
	linux, err := target.Parse("x86_64-linux-musl")
	require.NoError(t, err)
	host, err := target.Host()
	require.NoError(t, err)

	for _, test := range []struct {
		opts Options
		want string
	}{
		{Options{Target: windows}, "out.exe"},
		{Options{Target: linux}, "out"},
		{Options{}, host.Executable("out")},
	} {
		got, err := ExecutablePath(test.opts, "out")
		require.NoError(t, err)
		assert.Equal(t, test.want, got)
	}
}
