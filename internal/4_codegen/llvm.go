package codegen

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"

	base "github.com/zambozoo/zam/internal/0_base"
	command "github.com/zambozoo/zam/internal/1_command"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
	zamruntime "github.com/zambozoo/zam/internal/5_build/runtime"
	"github.com/zambozoo/zam/internal/llvm"
	"github.com/zambozoo/zam/internal/target"
)

// irVariable names a file GenerateObject writes the module's textual IR to
// once optimized, before compiling it, for looking into what native
// compilation emits.
const irVariable = "ZAM_LLVM_IR"

// passesVariable names an LLVM pass pipeline GenerateObject runs over the
// module in place of the one its optimization names, unless it compiles for a
// debugger.
const passesVariable = "ZAM_LLVM_PASSES"

// llvmOptimization is how a module is compiled at an optimization: the
// backend's level, the pass pipeline it runs, none when empty, and whether
// every function is marked to be optimized for size.
type llvmOptimization struct {
	level   llvm.CodeGenOptLevel
	passes  string
	optsize bool
}

// llvmOptimizations are how a module is compiled at each optimization. LLVM 23
// has no size pipeline, so size is O2 over functions marked optsize.
var llvmOptimizations = map[command.Optimization]llvmOptimization{
	command.OptimizeNone:       {level: llvm.CodeGenLevelNone},
	command.OptimizeLess:       {level: llvm.CodeGenLevelLess, passes: "default<O1>"},
	command.OptimizeDefault:    {level: llvm.CodeGenLevelDefault, passes: "default<O2>"},
	command.OptimizeAggressive: {level: llvm.CodeGenLevelAggressive, passes: "default<O3>"},
	command.OptimizeSize:       {level: llvm.CodeGenLevelDefault, passes: "default<O2>", optsize: true},
}

// stdNativePrefix starts the name of every native the runtime itself answers
// for.
const stdNativePrefix = "STD_"

// Object is a program compiled to native code: an object file for Triple that
// links against the runtime archive into an executable.
type Object struct {
	Bytes  []byte
	Triple string
	// Unsupported says, for each function compiled as a trap, what native
	// compilation could not compile in it. Calling one ends the program.
	Unsupported []string
}

// initializeTargets registers every target LLVM is linked with, once.
var initializeTargets = sync.OnceFunc(func() {
	llvm.InitializeAllTargetInfos()
	llvm.InitializeAllTargets()
	llvm.InitializeAllTargetMCs()
	llvm.InitializeAllAsmPrinters()
})

// ObjectOptions is how GenerateObject compiles a program.
type ObjectOptions struct {
	// Triple is the target to compile for, the host when empty.
	Triple string
	// Optimization is how far the program is optimized, OptimizeDefault when
	// empty.
	Optimization command.Optimization
	// Debug compiles without optimizing, whatever Optimization says, so that a
	// debugger stepping the program sees each line's effects by the next.
	Debug bool
}

// GenerateObject compiles the program into an object file as opts says.
func GenerateObject(cmd *command.Command, ast *syntax.AST, opts ObjectOptions) (object *Object, err error) {
	defer func() {
		if r := recover(); r != nil {
			unsupported, ok := r.(unsupportedPanic)
			if !ok {
				panic(r)
			}
			object, err = nil, unsupported.err
		}
	}()
	result, err := GenerateCode(cmd, ast)
	if err != nil {
		return nil, err
	}
	tables, err := zamruntime.NativeTables()
	if err != nil {
		return nil, err
	}
	for _, name := range result.NativeNames {
		if strings.HasPrefix(name, stdNativePrefix) {
			if _, ok := tables[name]; !ok {
				return nil, fmt.Errorf("native %q is not one the runtime answers", name)
			}
		} else if !cSymbol.MatchString(name) {
			return nil, fmt.Errorf("native %q is not a valid C identifier", name)
		}
	}

	initializeTargets()
	triple := opts.Triple
	if triple == "" {
		host, err := target.Host()
		if err != nil {
			return nil, err
		}
		triple = host.Triple
	}
	llvmTarget, err := llvm.GetTargetFromTriple(triple)
	if err != nil {
		return nil, err
	}
	optimization := opts.Optimization
	if optimization == "" {
		optimization = command.OptimizeDefault
	}
	if opts.Debug {
		optimization = command.OptimizeNone
	}
	optimize, ok := llvmOptimizations[optimization]
	if !ok {
		return nil, fmt.Errorf("unknown optimization %q", optimization)
	}
	if override := os.Getenv(passesVariable); override != "" && !opts.Debug {
		optimize.passes = override
	}
	level := optimize.level
	machine := llvmTarget.CreateTargetMachine(triple, "", "", level, llvm.RelocPIC, llvm.CodeModelDefault)
	defer machine.Dispose()

	ctx := llvm.NewContext()
	defer ctx.Dispose()
	mod := ctx.NewModule("zam")
	defer mod.Dispose()
	mod.SetTarget(triple)
	data := machine.CreateTargetData()
	defer data.Dispose()
	mod.SetDataLayout(data.String())

	unsupported, err := emitModule(ctx, mod, ast, result)
	if err != nil {
		return nil, err
	}
	if err := llvm.VerifyModule(mod, llvm.ReturnStatusAction); err != nil {
		return nil, fmt.Errorf("generated module is invalid: %w", err)
	}
	if optimize.optsize {
		optsize := ctx.CreateEnumAttribute(llvm.AttributeKindID("optsize"), 0)
		for fn := mod.FirstFunction(); !fn.IsNil(); fn = llvm.NextFunction(fn) {
			if !fn.IsDeclaration() {
				fn.AddFunctionAttr(optsize)
			}
		}
	}
	if optimize.passes != "" {
		options := llvm.NewPassBuilderOptions()
		defer options.Dispose()
		if err := mod.RunPasses(optimize.passes, machine, options); err != nil {
			return nil, fmt.Errorf("running %s: %w", optimize.passes, err)
		}
		if err := llvm.VerifyModule(mod, llvm.ReturnStatusAction); err != nil {
			return nil, fmt.Errorf("optimized module is invalid: %w", err)
		}
	}

	if path := os.Getenv(irVariable); path != "" {
		if err := os.WriteFile(path, []byte(mod.String()), 0o644); err != nil {
			return nil, fmt.Errorf("cannot write the module's IR to %s: %w", path, err)
		}
	}
	buf, err := machine.EmitToMemoryBuffer(mod, llvm.ObjectFile)
	if err != nil {
		return nil, err
	}
	defer buf.Dispose()
	return &Object{Bytes: append([]byte(nil), buf.Bytes()...), Triple: triple, Unsupported: unsupported}, nil
}

// MainFunctionName is what the program's main is compiled as, and named by in
// its DWARF and traces.
func MainFunctionName(ast *syntax.AST) string {
	return qualifiedName(ast.MainCodeDecl) + "." + base.MainMethodName
}

// cSymbol is what a project native's name has to be: the C function the
// project ships under it.
var cSymbol = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// emitProjectNatives lays out the table the runtime finds the project's own
// natives in, each bound to the C function of its name, for the linker to
// find in the project's native files.
func emitProjectNatives(ctx llvm.Context, mod llvm.Module, names []string) {
	ptr := llvm.PointerType(ctx.Int8Type(), 0)
	entryType := ctx.StructType([]llvm.Type{ptr, ptr}, false)
	nativeType := llvm.FunctionType(ctx.VoidType(), []llvm.Type{ptr}, false)
	var entries []llvm.Value
	for _, name := range names {
		if strings.HasPrefix(name, stdNativePrefix) {
			continue
		}
		text := llvm.AddGlobal(mod, llvm.ArrayType(ctx.Int8Type(), len(name)+1), "")
		text.SetInitializer(ctx.ConstString(name, true))
		text.SetGlobalConstant(true)
		text.SetLinkage(llvm.PrivateLinkage)
		fn := mod.NamedFunction(name)
		if fn.IsNil() {
			fn = llvm.AddFunction(mod, name, nativeType)
		}
		entries = append(entries, llvm.ConstNamedStruct(entryType, []llvm.Value{text, fn}))
	}
	count := len(entries)
	if count == 0 {
		// The runtime walks the table by its count and never reads the
		// sentinel.
		entries = []llvm.Value{llvm.ConstNull(entryType)}
	}
	table := llvm.AddGlobal(mod, llvm.ArrayType(entryType, len(entries)), "zamProjectNativeTable")
	table.SetInitializer(llvm.ConstArray(entryType, entries))
	table.SetGlobalConstant(true)
	tableCount := llvm.AddGlobal(mod, ctx.Int64Type(), "zamProjectNativeTableCount")
	tableCount.SetInitializer(llvm.ConstInt(ctx.Int64Type(), uint64(count), false))
	tableCount.SetGlobalConstant(true)
}

// emitStdNativeTables lays out the standard library's tables of natives the
// program calls into, beyond the core one every program has, so that the link
// takes in only the sources, and the libraries they wrap, that it calls.
func emitStdNativeTables(ctx llvm.Context, mod llvm.Module, names []string) error {
	tables, err := zamruntime.NativeTables()
	if err != nil {
		return err
	}
	var used []string
	for _, name := range names {
		if table, ok := tables[name]; ok && table != zamruntime.CoreNativeTable && !slices.Contains(used, table) {
			used = append(used, table)
		}
	}
	slices.Sort(used)

	ptr := llvm.PointerType(ctx.Int8Type(), 0)
	i64 := ctx.Int64Type()
	entryType := ctx.StructType([]llvm.Type{ptr, ptr}, false)
	entries := make([]llvm.Value, len(used))
	for i, table := range used {
		entries[i] = llvm.ConstNamedStruct(entryType, []llvm.Value{
			llvm.AddGlobal(mod, ctx.Int8Type(), table), llvm.AddGlobal(mod, i64, table+"Count"),
		})
	}
	count := len(entries)
	if count == 0 {
		entries = []llvm.Value{llvm.ConstNull(entryType)}
	}
	list := llvm.AddGlobal(mod, llvm.ArrayType(entryType, len(entries)), "zamStdNativeTables")
	list.SetInitializer(llvm.ConstArray(entryType, entries))
	list.SetGlobalConstant(true)
	listCount := llvm.AddGlobal(mod, i64, "zamStdNativeTableCount")
	listCount.SetInitializer(llvm.ConstInt(i64, uint64(count), false))
	listCount.SetGlobalConstant(true)
	return nil
}

// emitModule lays the program out in mod: its image as constant data, the
// project's natives table, its compiled main, and the C entry point
// that starts the runtime on them.
func emitModule(ctx llvm.Context, mod llvm.Module, ast *syntax.AST, result *Result) ([]string, error) {
	image := result.Image
	ptr := llvm.PointerType(ctx.Int8Type(), 0)
	i32, i64 := ctx.Int32Type(), ctx.Int64Type()

	imageData := llvm.AddGlobal(mod, llvm.ArrayType(ctx.Int8Type(), len(image)), "zam_image")
	imageData.SetInitializer(ctx.ConstString(string(image), false))
	imageData.SetGlobalConstant(true)
	imageData.SetLinkage(llvm.PrivateLinkage)
	imageData.SetAlignment(8)

	emitProjectNatives(ctx, mod, result.NativeNames)
	if err := emitStdNativeTables(ctx, mod, result.NativeNames); err != nil {
		return nil, err
	}

	builder := ctx.NewBuilder()
	defer builder.Dispose()

	main, ok := ast.MainCodeDecl.GetStaticMethod(base.MainMethodName)
	if !ok {
		return nil, fmt.Errorf("no %s method", base.MainMethodName)
	}
	program := newLLVMProgram(ctx, mod, result)
	program.debug = newLLVMDebug(ctx, mod, string(ast.MainCodeDecl.File().Path))
	program.debug.shortOf = func(t syntax.Type) (short uint64, ok bool) {
		// A type the image cannot name, or one a short id names only part of,
		// is only left out of the table.
		defer func() {
			if r := recover(); r != nil {
				if _, unsupported := r.(unsupportedPanic); !unsupported {
					panic(r)
				}
				ok = false
			}
		}()
		a, arrayLength, tailLength := program.typeInstance(t).packed()
		return a >> 32, arrayLength == 0 && tailLength == 0
	}
	staticInit, err := program.emitStaticInitializer(ast)
	if err != nil {
		return nil, err
	}
	zamMainBody := program.functionFor(main)
	classes, classCount, err := program.emitClassTable(ast)
	if err != nil {
		return nil, err
	}
	if err := program.emitPending(); err != nil {
		return nil, err
	}
	// A main compiled as a trap is a different function from the one declared.
	zamMainBody = program.functions[main]
	zamMain := program.emitProgramEntry(staticInit, zamMainBody)
	functions, functionCount := program.emitFunctionNames()
	program.debug.finish()

	startType := llvm.FunctionType(ctx.VoidType(), []llvm.Type{i32, ptr, ptr, i64, ptr, ptr, i64, ptr, i64}, false)
	start := llvm.AddFunction(mod, "zamrt_start", startType)
	start.AddFunctionAttr(ctx.CreateEnumAttribute(llvm.AttributeKindID("noreturn"), 0))

	cMain := llvm.AddFunction(mod, "main", llvm.FunctionType(i32, []llvm.Type{i32, ptr}, false))
	builder.SetInsertPointAtEnd(ctx.AddBasicBlock(cMain, "entry"))
	builder.CreateCall(startType, start, []llvm.Value{
		cMain.Param(0), cMain.Param(1), imageData, llvm.ConstInt(i64, uint64(len(image)), false), zamMain,
		classes, llvm.ConstInt(i64, uint64(classCount), false),
		functions, llvm.ConstInt(i64, uint64(functionCount), false),
	}, "")
	builder.CreateUnreachable()
	return program.stubs, nil
}

// emitFunctionNames lays out what traces name every compiled function by, in
// function id order, and answers where it lies and how many there are.
func (p *llvmProgram) emitFunctionNames() (llvm.Value, int) {
	entryType := p.ctx.StructType([]llvm.Type{p.ptr, p.ptr}, false)
	text := map[string]llvm.Value{}
	stringOf := func(s string) llvm.Value {
		if v, ok := text[s]; ok {
			return v
		}
		v := llvm.AddGlobal(p.mod, llvm.ArrayType(p.ctx.Int8Type(), len(s)+1), "")
		v.SetInitializer(p.ctx.ConstString(s, true))
		v.SetGlobalConstant(true)
		v.SetLinkage(llvm.PrivateLinkage)
		text[s] = v
		return v
	}
	entries := make([]llvm.Value, len(p.names))
	for i, name := range p.names {
		entries[i] = llvm.ConstNamedStruct(entryType, []llvm.Value{stringOf(name.name), stringOf(name.file)})
	}
	if len(entries) == 0 {
		return llvm.ConstNull(p.ptr), 0
	}
	table := llvm.AddGlobal(p.mod, llvm.ArrayType(entryType, len(entries)), "zam_function_names")
	table.SetInitializer(llvm.ConstArray(entryType, entries))
	table.SetGlobalConstant(true)
	table.SetLinkage(llvm.PrivateLinkage)
	return table, len(entries)
}
