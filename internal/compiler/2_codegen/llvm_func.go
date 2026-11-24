package codegen

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	"github.com/zambozoo/zam/internal/compiler/2_codegen/llvm"
)

// frameHeaderWords is how many words a ZamFrame holds ahead of its slots: the
// caller, the frame info and the line.
const frameHeaderWords = 3

// handlerWords is how many words a ZamHandler is given, ZAMHANDLER_WORDS.
const handlerWords = 80

// ErrUnsupported is the error native code generation answers for source it
// cannot compile yet.
var ErrUnsupported = errors.New("not supported by native code generation yet")

// unsupported reports what native code generation cannot compile yet.
func unsupported(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrUnsupported)
}

// llvmProgram is what every compiled function of one module shares.
type llvmProgram struct {
	ctx     llvm.Context
	mod     llvm.Module
	natives map[string]uint64

	i1, i32, i64, f64, ptr llvm.Type
	// entryType is the type of every compiled function, ZamEntry.
	entryType llvm.Type
	// frameInfoType is ZamFrameInfo.
	frameInfoType llvm.Type
	runtime       map[string]llvmRuntimeFunc
	// setjmp is the target's setjmp that unwinds nothing when jumped to, and
	// setjmpFrame whether it takes a frame after the buffer, given none.
	setjmp      llvmRuntimeFunc
	setjmpFrame bool
	// rootType is ZamFrameRoot, and typeIDType ZamTypeID.
	rootType   llvm.Type
	typeIDType llvm.Type
	// typeIDs holds each type id compiled code passes the runtime, as its
	// words.
	typeIDs map[[3]uint64]llvm.Value

	types  *typeResolver
	reader *codegenReader

	// functions is each method compiled or to be compiled, and pending the
	// ones whose bodies are yet to be.
	functions map[*syntax.FieldMethod]llvm.Value
	concretes map[*syntax.MethodConcrete]llvm.Value
	owned     map[llvmOwnedMethod]llvm.Value
	pending   []llvmPending
	// lambdas counts the lambdas compiled, to name each its own.
	lambdas int

	// names are the names and files traces give the functions compiled, the
	// first by function id 1.
	names []llvmFunctionName

	// debug is the DWARF the functions are described in, nil for none.
	debug *llvmDebug
	// initializers is the instance initializer compiled for each class that
	// has one.
	initializers map[syntax.Decl]llvm.Value
	// live is each declaration an instance of may exist, whose methods are
	// all compiled.
	live map[syntax.Decl]bool
	// staticValues is each static method named as a value, in the order first
	// named, whose closure the static initializer stores.
	staticValues     []*syntax.FieldMethod
	staticValuesSeen map[*syntax.FieldMethod]bool
	// stubs says what each function compiled as a trap could not compile.
	stubs []string
}

// llvmRuntimeFunc is a runtime function compiled code calls, and its type.
type llvmRuntimeFunc struct {
	fn  llvm.Value
	typ llvm.Type
}

func newLLVMProgram(ctx llvm.Context, mod llvm.Module, result *Result) *llvmProgram {
	nativeNames := result.NativeNames
	p := &llvmProgram{
		ctx:              ctx,
		mod:              mod,
		types:            result.types,
		reader:           result.reader,
		typeIDs:          map[[3]uint64]llvm.Value{},
		functions:        map[*syntax.FieldMethod]llvm.Value{},
		concretes:        map[*syntax.MethodConcrete]llvm.Value{},
		owned:            map[llvmOwnedMethod]llvm.Value{},
		initializers:     map[syntax.Decl]llvm.Value{},
		live:             map[syntax.Decl]bool{},
		staticValuesSeen: map[*syntax.FieldMethod]bool{},
		natives:          make(map[string]uint64, len(nativeNames)),
		i1:               ctx.Int1Type(),
		i32:              ctx.Int32Type(),
		i64:              ctx.Int64Type(),
		f64:              ctx.DoubleType(),
		ptr:              llvm.PointerType(ctx.Int8Type(), 0),
		runtime:          map[string]llvmRuntimeFunc{},
	}
	for index, name := range nativeNames {
		p.natives[name] = uint64(index)
	}
	p.entryType = llvm.FunctionType(ctx.VoidType(), []llvm.Type{p.ptr, p.ptr, p.ptr, p.ptr}, false)
	p.frameInfoType = ctx.StructType([]llvm.Type{p.i64, p.i64, p.i64, p.ptr}, false)
	p.typeIDType = ctx.StructType([]llvm.Type{p.i32, p.i64, p.i64}, false)
	p.rootType = ctx.StructType([]llvm.Type{p.i32, p.i32, p.i32, p.typeIDType}, false)

	void := ctx.VoidType()
	p.declare("zamrt_frame_push", void, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_frame_pop", void, p.ptr, p.ptr)
	p.declare("zamrt_panic", void, p.ptr, p.i32).fn.AddFunctionAttr(
		ctx.CreateEnumAttribute(llvm.AttributeKindID("noreturn"), 0))
	p.declare("zamrt_call_native", void, p.ptr, p.i64, p.ptr, p.i64, p.ptr, p.i64)
	p.declare("zamrt_unsupported", void, p.ptr, p.ptr).fn.AddFunctionAttr(
		ctx.CreateEnumAttribute(llvm.AttributeKindID("noreturn"), 0))
	p.declare("zamrt_safepoint", void, p.ptr)
	p.declare("zamrt_handler_push", void, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_panicking", ctx.Int8Type(), p.ptr, p.ptr)
	p.declare("zamrt_unwind", void, p.ptr, p.ptr).fn.AddFunctionAttr(
		ctx.CreateEnumAttribute(llvm.AttributeKindID("noreturn"), 0))
	triple := mod.Target()
	switch {
	case strings.HasPrefix(triple, "aarch64") && strings.Contains(triple, "windows"):
		p.setjmp = p.declare("__mingw_setjmp", p.i32, p.ptr)
	case strings.Contains(triple, "windows"):
		p.setjmp = p.declare("_setjmp", p.i32, p.ptr, p.ptr)
		p.setjmpFrame = true
	default:
		p.setjmp = p.declare("_setjmp", p.i32, p.ptr)
	}
	p.setjmp.fn.AddFunctionAttr(ctx.CreateEnumAttribute(llvm.AttributeKindID("returns_twice"), 0))
	p.declare("zamrt_static", p.ptr, p.ptr, p.i64)
	p.declare("zamrt_concat", p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_eq", ctx.Int8Type(), p.ptr, p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_cmp", p.i64, p.ptr, p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_str", p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_hash", p.i64, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_new_function_closure", p.ptr, p.ptr, p.ptr, p.ptr, p.i64, p.i64)
	p.declare("zamrt_closure_captures", p.ptr, p.ptr)
	p.declare("zamrt_closure_entry", p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_new_of", p.ptr, p.ptr, p.ptr, p.i64)
	p.declare("zamrt_record_write", void, p.ptr, p.ptr, p.i64)
	p.declare("zamrt_virtual_entry", p.ptr, p.ptr, p.ptr, p.i64)
	p.declare("zamrt_interface_entry", p.ptr, p.ptr, p.ptr, p.i32, p.i32)
	p.declare("llvm.memcpy.p0.p0.i64", void, p.ptr, p.ptr, p.i64, p.i1)
	p.declare("zamrt_new_run", p.ptr, p.ptr, p.ptr, p.i64)
	p.declare("zamrt_any_box", p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_any_unbox", void, p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_subtype_of", ctx.Int8Type(), p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_type_of_words", void, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_downcast", p.i64, p.ptr, p.i64, p.ptr)
	p.declare("zamrt_new_bound_closure", p.ptr, p.ptr, p.ptr, p.ptr, p.i64, p.ptr)
	p.declare("zamrt_stamp", void, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_new_tailed", p.ptr, p.ptr, p.ptr, p.i64, p.i64)
	p.declare("zamrt_spawn", p.ptr, p.ptr, p.ptr, p.ptr, p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_wait", void, p.ptr, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_any_type_words", void, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_free", void, p.ptr, p.ptr, p.i64)
	p.declare("zamrt_mark", void, p.ptr, p.ptr, p.ptr)
	p.declare("zamrt_delete", void, p.ptr, p.ptr, p.ptr)
	for _, name := range []string{"zam_posit_add", "zam_posit_sub", "zam_posit_mul", "zam_posit_div"} {
		p.declare(name, p.i64, p.i64, p.i64)
	}
	p.declare("zam_posit_from_int", p.i64, p.i64)
	p.declare("zam_posit_from_uint", p.i64, p.i64)
	p.declare("zam_posit_from_double", p.i64, p.f64)
	p.declare("zam_posit_to_int", p.i64, p.i64)
	p.declare("zam_posit_to_uint", p.i64, p.i64)
	p.declare("zam_posit_to_double", p.f64, p.i64)
	p.declare("llvm.fptosi.sat.i64.f64", p.i64, p.f64)
	p.declare("llvm.fptoui.sat.i64.f64", p.i64, p.f64)
	return p
}

// declare declares the runtime function name.
func (p *llvmProgram) declare(name string, ret llvm.Type, params ...llvm.Type) llvmRuntimeFunc {
	typ := llvm.FunctionType(ret, params, false)
	f := llvmRuntimeFunc{fn: llvm.AddFunction(p.mod, name, typ), typ: typ}
	p.runtime[name] = f
	return f
}

// unsupportedPanic carries an unsupported error out of code that has no error
// to answer, to the function being compiled, which becomes a trap.
type unsupportedPanic struct{ err error }

// typeInstance is t's type instance, where codegen laid one out.
func (p *llvmProgram) typeInstance(t syntax.Type) (instance typeInstance) {
	defer func() {
		if r := recover(); r != nil {
			panic(unsupportedPanic{unsupported("a type id for %s", t)})
		}
	}()
	return p.types.typeInstanceOf(t)
}

// typeID is a constant holding the words of t's type id.
func (p *llvmProgram) typeID(t syntax.Type) llvm.Value {
	a, b, c := p.typeInstance(t).packed()
	key := [3]uint64{a, b, c}
	if v, ok := p.typeIDs[key]; ok {
		return v
	}
	v := llvm.AddGlobal(p.mod, llvm.ArrayType(p.i64, 3), "zam.type")
	v.SetInitializer(llvm.ConstArray(p.i64, []llvm.Value{
		llvm.ConstInt(p.i64, a, false), llvm.ConstInt(p.i64, b, false), llvm.ConstInt(p.i64, c, false),
	}))
	v.SetGlobalConstant(true)
	v.SetLinkage(llvm.PrivateLinkage)
	p.typeIDs[key] = v
	return v
}

// rootOf is the ZamFrameRoot naming the slot slot as holding a value of t.
func (p *llvmProgram) rootOf(slot uint64, t syntax.Type) llvm.Value {
	a, b, c := p.typeInstance(t).packed()
	typeID := llvm.ConstNamedStruct(p.typeIDType, []llvm.Value{
		llvm.ConstInt(p.i32, a>>32, false), llvm.ConstInt(p.i64, b, false), llvm.ConstInt(p.i64, c, false),
	})
	return llvm.ConstNamedStruct(p.rootType, []llvm.Value{
		llvm.ConstInt(p.i32, 0, false), llvm.ConstInt(p.i32, slot, false), llvm.ConstInt(p.i32, 0, false), typeID,
	})
}

// llvmFunctionName is how a trace names a function: its name and its file.
type llvmFunctionName struct {
	name, file string
}

// named gives the function being compiled the name a trace and a debugger
// call it by, in the file of the declaration it is compiled in, beginning on
// the line start lies on.
func (e *funcEmitter) named(name string, start *syntax.Token) {
	file := ""
	if e.decl != nil && e.decl.File() != nil {
		file = string(e.decl.File().Path)
	}
	e.p.names = append(e.p.names, llvmFunctionName{name: name, file: file})
	e.functionID = uint64(len(e.p.names))
	e.name = name
	if e.p.debug == nil || file == "" {
		return
	}
	e.file = e.p.debug.file(file)
	e.scope = e.p.debug.function(e.fn, name, file, e.lineOf(start))
	e.debugged = true
	e.startLine = e.lineOf(start)
	e.line = e.startLine
	e.locate(e.line, 0)
}

// locate says the instructions emitted next are for line and column of the
// function's source, when it is described.
func (e *funcEmitter) locate(line, column int) {
	if e.debugged {
		e.b.SetCurrentDebugLocation(uint(line), uint(column), e.scope, llvm.Metadata{})
	}
}

// lineOf is the line tok lies on in the file of the declaration being
// compiled, 0 where that is unknown.
func (e *funcEmitter) lineOf(tok *syntax.Token) int {
	if tok == nil || e.decl == nil || e.decl.File() == nil {
		return 0
	}
	position := e.decl.File().Location(tok).Position
	if position == nil {
		return 0
	}
	return position.Line
}

// at records that the function is at the line tok lies on, for a trace to
// find.
func (e *funcEmitter) at(tok *syntax.Token) {
	if e.functionID == 0 || e.decl == nil || e.decl.File() == nil || tok == nil {
		return
	}
	position := e.decl.File().Location(tok).Position
	if position == nil {
		return
	}
	if e.debugged {
		e.line, e.column = position.Line, position.Column
		e.locate(e.line, e.column)
	}
	line := e.b.CreateInBoundsGEP(e.p.i64, e.frame, []llvm.Value{e.const64(frameLineWord)}, "")
	// Volatile, for a fault traces the line at any load or store, so the store
	// must not be dropped for the next statement's.
	e.b.CreateStore(e.const64(uint64(position.Line)), line).SetVolatile(true)
}

// frameLineWord is the word of a ZamFrame its line is.
const frameLineWord = 2

// defers reports whether s defers a statement, outside the lambdas it holds,
// which are functions of their own.
func defers(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.StmtDefer:
		return true
	case *syntax.StmtBlock:
		return slices.ContainsFunc(s.Stmts, defers)
	case *syntax.StmtFor:
		return defers(s.Body) || defers(s.Step) || defers(s.Else)
	case *syntax.StmtIf:
		return defers(s.Then) || defers(s.Else)
	case *syntax.StmtLabel:
		return defers(s.LoopStmt)
	case *syntax.StmtSwitch:
		for body := range s.Bodies() {
			if defers(body) {
				return true
			}
		}
	}
	return false
}

// llvmLocal is a local variable: the slot it begins at and its type.
type llvmLocal struct {
	slot uint64
	typ  syntax.Type
}

// llvmScope is the locals a block declares and the statements it defers.
type llvmScope struct {
	vars   map[string]llvmLocal
	defers []*llvmDefer
	// active is the defer innermost when the scope was opened, and resume
	// where execution goes on once a panic one of its defers recovers has
	// run the rest of them; a scope without one resumes at the function's end.
	active int
	resume llvm.BasicBlock
}

// llvmDefer is a deferred statement and the scopes its names mean what they
// meant in when it was deferred. id numbers it among the function's defers
// from 1, and outer is the id of the defer innermost before it, 0 for none.
type llvmDefer struct {
	stmt   syntax.Stmt
	scopes []*llvmScope
	scope  *llvmScope
	id     int
	outer  int
}

// llvmLoop is a loop or switch a break or continue may leave, and how many
// scopes were open around it.
type llvmLoop struct {
	label      string
	continueTo llvm.BasicBlock
	breakTo    llvm.BasicBlock
	scopeDepth int
}

// funcEmitter compiles one method body into one function.
type funcEmitter struct {
	p   *llvmProgram
	b   llvm.Builder
	fn  llvm.Value
	ctx llvm.Context

	thread llvm.Value
	frame  llvm.Value
	slots  llvm.Value
	entry  llvm.BasicBlock

	nextSlot  uint64
	slotCount uint64
	scopes    []*llvmScope
	loops     []*llvmLoop
	label     string

	// decl is the declaration whose statics, and those of the declarations
	// around it, bare names may mean.
	decl  syntax.Decl
	info  llvm.Value
	body  llvm.BasicBlock
	roots []llvm.Value

	// mapper substitutes the type arguments of the instantiation being
	// compiled, nil for code that is not generic.
	mapper func(syntax.Type) syntax.Type

	// returnType is what the function returns, and returnSlot the rooted slot
	// a return keeps it in while its defers run; nil for void.
	returnType syntax.Type
	returnSlot uint64

	// spawn is the async the next call expression starts a thread for, armed
	// that call's until its arguments are laid out, and spawning the call
	// once they are.
	spawn    *llvmSpawn
	armed    *llvmSpawn
	spawning *llvmSpawn

	// functionID is what the function's frames name it by in a trace, 0 for
	// none, and name the name it gives.
	functionID uint64
	name       string

	// scope is the function's DWARF subprogram and file its file, when
	// debugged says it is described; startLine is the line it begins on, and
	// line and column where it is compiling.
	scope     llvm.Metadata
	file      llvm.Metadata
	debugged  bool
	startLine int
	line      int
	column    int

	// temps are the rooted slots the statements being compiled hold
	// temporaries in, cleared as each statement ends so that what it no
	// longer needs can be collected.
	temps []llvmLocal

	// defers is every statement the function defers, by id less one, and
	// active the id of the innermost one deferred where it is compiling.
	// deferred holds the id of the innermost one deferred where it is
	// running, which is where a panic landing in it starts running them.
	defers   []*llvmDefer
	active   int
	deferred llvm.Value

	// volatileSlots is whether the function defers, so that its stores to its
	// frame are volatile. A fault longjmps into the landing pad at any load or
	// store, and the defers it runs read the frame, so no store to it may be
	// dropped for one that overwrites it.
	volatileSlots bool
}

// llvmSpawn is a call async runs on a thread of its own: the thread's type,
// how the call's arguments lie, and, once started, the thread.
type llvmSpawn struct {
	threadType syntax.Type
	info       llvm.Value
	result     syntax.Type
	thread     llvm.Value
}

// emitCallExpr calls a native, a method, a static or a closure.
func (e *funcEmitter) emitCallExpr(x *syntax.ExprNAry) (llvm.Value, error) {
	if isNativeCallee(x.Exprs[0]) {
		return e.emitNativeCall(x)
	}
	if object, decl, method, ok := e.methodCallee(x.Exprs[0]); ok {
		return e.emitMethodCall(x, object, decl, method)
	}
	if method, typeArgs, ok := staticCallee(x.Exprs[0]); ok {
		ft, _ := method.FieldType().(*syntax.TypeFunction)
		var fn llvm.Value
		if len(typeArgs) == 0 {
			fn = e.p.functionFor(method)
		} else {
			grounded := make([]syntax.Type, len(typeArgs))
			for i, t := range typeArgs {
				grounded[i] = e.ground(t)
			}
			concrete := e.p.concreteOf(method, grounded)
			if concrete == nil {
				return llvm.Value{}, fmt.Errorf("no instantiation of %s for %v", method.Name(), grounded)
			}
			ft, fn = concrete.FieldType(), e.p.concreteFunction(concrete)
		}
		return e.emitCall(x.Exprs[1:], ft, nil, func(args, result llvm.Value) {
			e.invoke(fn, llvm.ConstNull(e.p.ptr), args, result)
		})
	}
	ft, ok := syntax.StripConst(e.ground(x.Exprs[0].ResolvedType())).(*syntax.TypeFunction)
	if !ok {
		return llvm.Value{}, unsupported("calls through %T", x.Exprs[0])
	}
	closure, err := e.emitExpr(x.Exprs[0])
	if err != nil {
		return llvm.Value{}, err
	}
	closure = e.b.CreateIntToPtr(closure, e.p.ptr, "")
	return e.emitCall(x.Exprs[1:], ft, nil, func(args, result llvm.Value) {
		entry := e.call("zamrt_closure_entry", e.thread, closure)
		e.invoke(entry, closure, args, result)
	})
}

// emitAsync starts a thread running the call async wraps, its arguments
// evaluated here.
func (e *funcEmitter) emitAsync(x *syntax.ExprNAry) (llvm.Value, error) {
	call, ok := x.Exprs[0].(*syntax.ExprNAry)
	if !ok || isNativeCallee(call.Exprs[0]) {
		return llvm.Value{}, unsupported("async of %s", x.Exprs[0].Kind())
	}
	threadType := e.ground(x.ResolvedType())
	if _, err := valueKind(threadType); err != nil {
		return llvm.Value{}, err
	}
	spawn := &llvmSpawn{threadType: threadType}
	e.spawn = spawn
	_, err := e.emitExpr(call)
	e.spawn = nil
	if err != nil {
		return llvm.Value{}, err
	}
	if spawn.thread.IsNil() {
		return llvm.Value{}, unsupported("async of this call")
	}
	return spawn.thread, nil
}

// emitWait joins a thread and answers what its call returned.
func (e *funcEmitter) emitWait(x *syntax.ExprNAry) (llvm.Value, error) {
	thread, err := e.emitExpr(x.Exprs[0])
	if err != nil {
		return llvm.Value{}, err
	}
	target := e.b.CreateIntToPtr(thread, e.p.ptr, "")
	result := e.ground(x.ResolvedType())
	if isVoid(result) {
		null := llvm.ConstNull(e.p.ptr)
		e.call("zamrt_wait", e.thread, target, null, null)
		return e.const64(0), nil
	}
	if _, err := valueKind(result); err != nil {
		return llvm.Value{}, err
	}
	out := e.slotPlace(e.declareLocal("", result), result)
	e.call("zamrt_wait", e.thread, target, e.p.typeID(result), out.address)
	if isAggregate(result) {
		return out.address, nil
	}
	return e.b.CreateLoad(e.p.i64, out.address, ""), nil
}

// spawnInfo describes the frame a spawned call's thread starts with: the
// closure it is called through, then its receiver and arguments as a call lays
// them out, then the packed values of its variadic, which keep what the pack's
// run holds rooted for as long as the thread runs.
func (e *funcEmitter) spawnInfo(ft *syntax.TypeFunction, receiver *llvmReceiver, params []syntax.Type, element syntax.Type, packed int) llvm.Value {
	roots := []llvm.Value{e.p.rootOf(0, ft)}
	slot := uint64(1)
	if receiver != nil {
		if rooted(receiver.typ) {
			roots = append(roots, e.p.rootOf(slot, receiver.typ))
		}
		slot += stackWords(receiver.typ)
	}
	for _, param := range params {
		if rooted(param) {
			roots = append(roots, e.p.rootOf(slot, param))
		}
		slot += stackWords(param)
	}
	for range packed {
		roots = append(roots, e.p.rootOf(slot, element))
		slot += stackWords(element)
	}
	table := llvm.AddGlobal(e.p.mod, llvm.ArrayType(e.p.rootType, len(roots)), "")
	table.SetInitializer(llvm.ConstArray(e.p.rootType, roots))
	table.SetGlobalConstant(true)
	table.SetLinkage(llvm.PrivateLinkage)
	info := llvm.AddGlobal(e.p.mod, e.p.frameInfoType, "")
	info.SetInitializer(llvm.ConstNamedStruct(e.p.frameInfoType, []llvm.Value{
		e.const64(0), e.const64(slot), e.const64(uint64(len(roots))), table,
	}))
	info.SetGlobalConstant(true)
	info.SetLinkage(llvm.PrivateLinkage)
	return info
}

// invoke calls fn through self with args, writing what it answers to result,
// or starts a thread running the call when the call is an async's.
func (e *funcEmitter) invoke(fn, self, args, result llvm.Value) {
	if s := e.spawning; s != nil {
		resultType := llvm.ConstNull(e.p.ptr)
		if s.result != nil {
			resultType = e.p.typeID(s.result)
		}
		thread := e.call("zamrt_spawn", e.thread, e.p.typeID(s.threadType), fn, self, s.info, args, resultType)
		s.thread = e.keep(e.b.CreatePtrToInt(thread, e.p.i64, ""), s.threadType)
		return
	}
	e.b.CreateCall(e.p.entryType, fn, []llvm.Value{e.thread, self, args, result}, "")
}

// newFunc starts the body of fn: its frame, pushed in its entry block.
func (p *llvmProgram) newFunc(fn llvm.Value, decl syntax.Decl) *funcEmitter {
	e := &funcEmitter{p: p, b: p.ctx.NewBuilder(), ctx: p.ctx, decl: decl, fn: fn}
	name := fn.Name()
	e.thread = e.fn.Param(0)

	e.entry = p.ctx.AddBasicBlock(e.fn, "entry")
	e.body = p.ctx.AddBasicBlock(e.fn, "body")
	e.b.SetInsertPointAtEnd(e.entry)
	e.frame = e.b.CreateArrayAlloca(p.i64, llvm.ConstInt(p.i64, 0, false), "frame")
	e.info = llvm.AddGlobal(p.mod, p.frameInfoType, name+".frame")
	e.info.SetLinkage(llvm.PrivateLinkage)
	e.info.SetGlobalConstant(true)
	e.call("zamrt_frame_push", e.thread, e.frame, e.info)
	e.slots = e.b.CreateInBoundsGEP(p.i64, e.frame, []llvm.Value{e.const64(frameHeaderWords)}, "slots")
	e.b.SetInsertPointAtEnd(e.body)
	e.pushScope()
	return e
}

// stub replaces the function e was compiling, which it could not finish, with
// one that ends the program saying why when it is called, and answers it.
// Everything that named the function, or will, names the stub instead.
func (p *llvmProgram) stub(e *funcEmitter, err error) llvm.Value {
	e.b.Dispose()
	name := e.fn.Name()
	what := fmt.Sprintf("%s: %v", name, err)
	p.stubs = append(p.stubs, what)
	e.info.SetInitializer(llvm.ConstNull(p.frameInfoType))

	trap := llvm.AddFunction(p.mod, name+".unsupported", p.entryType)
	trap.SetLinkage(llvm.InternalLinkage)
	b := p.ctx.NewBuilder()
	defer b.Dispose()
	b.SetInsertPointAtEnd(p.ctx.AddBasicBlock(trap, "entry"))
	message := b.CreateGlobalStringPtr(what, "unsupported")
	f := p.runtime["zamrt_unsupported"]
	b.CreateCall(f.typ, f.fn, []llvm.Value{trap.Param(0), message}, "")
	b.CreateUnreachable()

	e.fn.ReplaceAllUsesWith(trap)
	replaceValue(p.functions, e.fn, trap)
	replaceValue(p.concretes, e.fn, trap)
	replaceValue(p.owned, e.fn, trap)
	replaceValue(p.initializers, e.fn, trap)
	for i := range p.pending {
		if p.pending[i].fn == e.fn {
			p.pending[i].fn = trap
		}
	}
	e.fn.EraseFromParentAsFunction()
	return trap
}

// replaceValue points every key of m that answered old at new instead.
func replaceValue[K comparable](m map[K]llvm.Value, old, new llvm.Value) {
	for k, v := range m {
		if v == old {
			m[k] = new
		}
	}
}

// compiles runs emit over e, finishing the function it compiles, or replacing
// it with a trap where it uses what native compilation does not support yet,
// and answers whichever function there is.
func (p *llvmProgram) compiles(e *funcEmitter, emit func() error) (fn llvm.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			unsupported, ok := r.(unsupportedPanic)
			if !ok {
				panic(r)
			}
			fn, err = p.stub(e, unsupported.err), nil
		}
	}()
	err = emit()
	if err == nil {
		err = e.end()
	}
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return p.stub(e, err), nil
		}
		e.b.Dispose()
		return llvm.Value{}, err
	}
	return e.finish(), nil
}

// newFunction declares a function named name of the entry type.
func (p *llvmProgram) newFunction(name string) llvm.Value {
	fn := llvm.AddFunction(p.mod, name, p.entryType)
	fn.SetLinkage(llvm.InternalLinkage)
	return fn
}

// functionFor is the function method compiles to, declared the first time it
// is asked for and compiled once emitPending gets to it.
func (p *llvmProgram) functionFor(method *syntax.FieldMethod) llvm.Value {
	if fn, ok := p.functions[method]; ok {
		return fn
	}
	fn := p.newFunction(qualifiedName(method.OuterDecl()) + "." + method.Name())
	p.functions[method] = fn
	p.pending = append(p.pending, llvmPending{method: method, fn: fn})
	return fn
}

// llvmPending is a function declared and not yet compiled: a method, or one
// instantiation of a generic method.
type llvmPending struct {
	method   *syntax.FieldMethod
	concrete *syntax.MethodConcrete
	owner    syntax.DeclConcrete
	fn       llvm.Value
}

// llvmOwnedMethod is a method of a generic declaration as one instantiation
// of it has it.
type llvmOwnedMethod struct {
	method *syntax.FieldMethod
	owner  syntax.DeclConcrete
}

// objectDefault reports whether the method named name that d has is Object's
// own, inherited unchanged, as it is for a class that never declares one.
func objectDefault(d syntax.Decl, name string) bool {
	method, ok := originMethod(d, name)
	return !ok || method.OuterDecl().File().Path == base.StdPath("lang/Obj"+base.CodeFileExtension)
}

// originMethod is the method named name that d has, as it is declared: d's
// template's own where d instantiates a generic declaration.
func originMethod(d syntax.Decl, name string) (*syntax.FieldMethod, bool) {
	if c, ok := d.(syntax.DeclConcrete); ok {
		d = c.Template()
	}
	field, at := d.AllInstanceFields().Get(name)
	if at < 0 {
		return nil, false
	}
	method, ok := field.(*syntax.FieldMethod)
	if !ok {
		return nil, false
	}
	// One inherited through an instantiation is a copy; the declaration's own
	// is the one compiled.
	if own, ok := method.OuterDecl().GetInstanceMethod(name); ok {
		method = own
	}
	return method, true
}

// methodFunction is the function owner's method compiles to: one per
// instantiation for a method its generic template declares, and the method's
// own otherwise.
func (p *llvmProgram) methodFunction(method *syntax.FieldMethod, owner syntax.Decl) llvm.Value {
	c := instantiationOf(owner, method.OuterDecl())
	if c == nil {
		return p.functionFor(method)
	}
	key := llvmOwnedMethod{method: method, owner: c}
	if fn, ok := p.owned[key]; ok {
		return fn
	}
	fn := p.newFunction(qualifiedName(method.OuterDecl()) + "[" + c.DeclType().String() + "]." + method.Name())
	p.owned[key] = fn
	p.pending = append(p.pending, llvmPending{method: method, owner: c, fn: fn})
	return fn
}

// instantiationOf is the declaration, owner or one it inherits from, that
// instantiates template, or nil when none does.
func instantiationOf(owner, template syntax.Decl) syntax.DeclConcrete {
	for d := owner; d != nil; {
		if c, ok := d.(syntax.DeclConcrete); ok && syntax.Decl(c.Template()) == template {
			return c
		}
		subtypeable, ok := d.(syntax.DeclSubtypeable)
		if !ok || subtypeable.BaseDeclType() == nil {
			return nil
		}
		base, ok := subtypeable.BaseDeclType().Type.(syntax.TypeResolvable)
		if !ok {
			return nil
		}
		d = base.Decl()
	}
	return nil
}

// concreteOf is the instantiation of method for typeArgs the semantic pass
// recorded, or nil when it recorded none.
func (p *llvmProgram) concreteOf(method *syntax.FieldMethod, typeArgs []syntax.Type) *syntax.MethodConcrete {
	for _, concrete := range method.Concretes() {
		if slices.EqualFunc(concrete.TypeArgs(), typeArgs, func(a, b syntax.Type) bool { return a.Equals(b) }) {
			return concrete
		}
	}
	return nil
}

// concreteFunction is the function one instantiation of a generic method
// compiles to, declared the first time it is asked for.
func (p *llvmProgram) concreteFunction(concrete *syntax.MethodConcrete) llvm.Value {
	if fn, ok := p.concretes[concrete]; ok {
		return fn
	}
	names := make([]string, len(concrete.TypeArgs()))
	for i, t := range concrete.TypeArgs() {
		names[i] = t.String()
	}
	method := concrete.Method()
	fn := p.newFunction(fmt.Sprintf("%s.%s[%s]", qualifiedName(method.OuterDecl()), method.Name(), strings.Join(names, ",")))
	p.concretes[concrete] = fn
	p.pending = append(p.pending, llvmPending{method: method, concrete: concrete, fn: fn})
	return fn
}

// emitPending compiles every method declared and not yet compiled, including
// those the ones it compiles call.
func (p *llvmProgram) emitPending() error {
	for len(p.pending) > 0 {
		next := p.pending[0]
		p.pending = p.pending[1:]
		if err := p.emitFunction(next.method, next.concrete, next.owner, next.fn); err != nil {
			return err
		}
	}
	return nil
}

// end returns from the end of the body, and enters it from the function's
// entry, by way of its landing pad if it defers.
func (e *funcEmitter) end() error {
	if !e.terminated() {
		e.emitFallthroughReturn()
	}
	if len(e.defers) > 0 {
		return e.emitLanding()
	}
	e.b.SetInsertPointAtEnd(e.entry)
	e.locate(e.startLine, 0)
	e.b.CreateBr(e.body)
	return nil
}

// finish sizes the function's frame and says which of its slots are roots,
// now that every one has been handed out.
func (e *funcEmitter) finish() llvm.Value {
	defer e.b.Dispose()
	e.frame.SetOperand(0, e.const64(frameHeaderWords+e.slotCount))

	roots := llvm.ConstNull(e.p.ptr)
	if len(e.roots) > 0 {
		table := llvm.AddGlobal(e.p.mod, llvm.ArrayType(e.p.rootType, len(e.roots)), "")
		table.SetInitializer(llvm.ConstArray(e.p.rootType, e.roots))
		table.SetGlobalConstant(true)
		table.SetLinkage(llvm.PrivateLinkage)
		roots = table
	}
	e.info.SetInitializer(llvm.ConstNamedStruct(e.p.frameInfoType, []llvm.Value{
		e.const64(e.functionID), e.const64(e.slotCount), e.const64(uint64(len(e.roots))), roots,
	}))
	return e.fn
}

// emitFunction compiles method's body into fn, as the instantiation concrete
// when it is one, and as owner has it when owner instantiates the generic
// declaration method belongs to.
func (p *llvmProgram) emitFunction(method *syntax.FieldMethod, concrete *syntax.MethodConcrete, owner syntax.DeclConcrete, fn llvm.Value) error {
	ft, ok := method.FieldType().(*syntax.TypeFunction)
	if concrete != nil {
		ft, ok = concrete.FieldType(), true
	}
	if owner != nil {
		ft, ok = owner.MapType(method.FieldType()).(*syntax.TypeFunction)
	}
	if !ok {
		return unsupported("method %s of type %s", method.Name(), method.FieldType())
	}
	if method.Body == nil {
		return unsupported("method %s with no body", method.Name())
	}

	e := p.newFunc(fn, method.OuterDecl())
	e.named(qualifiedName(method.OuterDecl())+"."+method.Name(), method.Start())
	if concrete != nil {
		bindings := concrete.Bindings()
		e.mapper = func(t syntax.Type) syntax.Type { return t.Map(bindings) }
	}
	var receiver syntax.Type
	if isInstanceMethod(method) {
		receiver = receiverType(method.OuterDecl())
	}
	if owner != nil {
		e.mapper = owner.MapType
		e.decl = owner
		if receiver != nil {
			receiver = receiverType(owner)
		}
	}
	_, err := p.compiles(e, func() error {
		return e.emitBody(ft, receiver, method.Parameters, nil, method.Body)
	})
	return err
}

// llvmCapture is a local a lambda captures, and its type.
type llvmCapture struct {
	name string
	typ  syntax.Type
}

// emitBody compiles a body over parameters of ft named params, then the
// captures a lambda's closure holds, each copied into a rooted slot of the
// frame before the first poll.
func (e *funcEmitter) emitBody(ft *syntax.TypeFunction, receiver syntax.Type, params []string, captures []llvmCapture, body syntax.Stmt) error {
	e.volatileSlots = defers(body)
	args := e.fn.Param(2)
	argWord := uint64(0)
	if receiver != nil {
		word := e.b.CreateLoad(e.p.i64, args, "")
		e.b.CreateStore(word, e.slot(e.declareLocal(thisVariableName, receiver)))
		argWord++
	}
	for i, typ := range ft.ParameterTypes {
		typ = syntax.ParameterBindingType(typ)
		if _, err := valueKind(typ); err != nil && stackWords(typ) != 1 {
			return unsupported("parameter of type %s", typ)
		}
		if _, err := valueKind(typ); err != nil && isAggregate(typ) {
			return err
		}
		name := ""
		if i < len(params) {
			name = params[i]
		}
		slot := e.declareLocal(name, typ)
		words := stackWords(typ)
		e.copyWords(e.slot(slot), e.b.CreateInBoundsGEP(e.p.i64, args, []llvm.Value{e.const64(argWord)}, ""), words)
		argWord += words
	}
	if len(captures) > 0 {
		entries := e.call("zamrt_closure_captures", e.fn.Param(1))
		base := uint64(0)
		for _, capture := range captures {
			at := e.b.CreateInBoundsGEP(e.p.i64, entries, []llvm.Value{e.const64(base + typeIDWords)}, "")
			e.copyWords(e.slot(e.declareLocal(capture.name, capture.typ)), at, stackWords(capture.typ))
			base += typeIDWords + stackWords(capture.typ)
		}
	}
	// The parameters and captures are rooted only once they are in the frame,
	// so nothing may collect before here.
	e.call("zamrt_safepoint", e.thread)
	if !isVoid(ft.ReturnType) {
		if _, err := valueKind(ft.ReturnType); err != nil {
			return err
		}
		e.returnType = ft.ReturnType
		e.returnSlot = e.declareLocal("", ft.ReturnType)
	}
	return e.emitStmt(body)
}

// emitStaticInitializers compiles what every static member and enum value is
// initialized to, in the order the program runs them in: file by file, and
// within a file in the order they are written, whichever declaration holds
// them.
func (p *llvmProgram) emitStaticInitializers(ast *syntax.AST) ([]llvm.Value, error) {
	var fns []llvm.Value
	for _, entry := range ast.CodeFiles.All() {
		for _, f := range staticInitializers(entry.Value.Decl) {
			fn, err := p.emitStaticInitializerOf(f)
			if err != nil {
				return nil, err
			}
			fns = append(fns, fn)
		}
	}
	return fns, nil
}

// emitStaticInitializer compiles what runs before main: storing the closure
// of every static method named as a value, so that an initializer may name
// one declared after it, then calling each of initializers.
func (p *llvmProgram) emitStaticInitializer(ast *syntax.AST, initializers []llvm.Value) (llvm.Value, error) {
	e := p.newFunc(p.newFunction("zam_static_init"), ast.MainCodeDecl)
	e.named("<static init>", nil)
	e.call("zamrt_safepoint", e.thread)
	for _, method := range p.staticValues {
		closure := e.call("zamrt_new_function_closure", e.thread, e.p.typeID(method.FieldType()), p.functions[method],
			e.const64(0), e.const64(0))
		e.b.CreateStore(e.b.CreatePtrToInt(closure, e.p.i64, ""), e.staticAddress(*method.Index()))
	}
	null := llvm.ConstNull(p.ptr)
	for _, fn := range initializers {
		e.b.CreateCall(p.entryType, fn, []llvm.Value{e.thread, null, null, null}, "")
	}
	if err := e.end(); err != nil {
		e.b.Dispose()
		return llvm.Value{}, err
	}
	return e.finish(), nil
}

// emitStaticInitializerOf compiles what one static member or enum value is
// initialized to into a function of its own, so that one native compilation
// cannot compile yet traps alone.
func (p *llvmProgram) emitStaticInitializerOf(f syntax.Field) (llvm.Value, error) {
	e := p.newFunc(p.newFunction("zam_static."+qualifiedName(f.OuterDecl())+"."+f.Name()), f.OuterDecl())
	e.named("<static init>", nil)
	return p.compiles(e, func() error {
		e.call("zamrt_safepoint", e.thread)
		switch f := f.(type) {
		case *syntax.FieldMember:
			return e.emitStaticMember(f)
		case *syntax.FieldEnum:
			return e.emitEnumValue(f)
		}
		return nil
	})
}

// staticValue records that the static method is named as a value, so the
// static initializer stores its closure.
func (p *llvmProgram) staticValue(method *syntax.FieldMethod) {
	if p.staticValuesSeen[method] {
		return
	}
	p.staticValuesSeen[method] = true
	if _, ok := method.FieldType().(*syntax.TypeFunction); !ok || method.Body == nil {
		return
	}
	p.functionFor(method)
	p.staticValues = append(p.staticValues, method)
}

// emitStaticMember stores the initial value of a static member.
func (e *funcEmitter) emitStaticMember(f *syntax.FieldMember) error {
	e.decl = f.OuterDecl()
	typ := f.FieldType()
	if _, err := valueKind(typ); err != nil {
		return err
	}
	pl, err := e.place(syntax.NewExprIdentifier(f.Name(), typ))
	if err != nil {
		return err
	}
	if f.Expr == nil {
		return e.initializeStruct(pl.address, typ)
	}
	v, err := e.emitExprAs(f.Expr, typ)
	if err != nil {
		return err
	}
	e.store(pl, v)
	return nil
}

// classMarksCustom and classDeletesCustom are the flags a class table entry
// carries when the class's Mark or Delete is its own rather than Object's.
const (
	classMarksCustom   = 1 << 0
	classDeletesCustom = 1 << 1
)

// eachClass calls f on every class, struct and enum the class table has an
// entry for, and on every closed instantiation of a generic one, saying
// whether the runtime calls only its Str, which an enum or struct is given as
// its first method.
func eachClass(ast *syntax.AST, f func(c syntax.DeclTypeable, strOnly bool) error) error {
	add := func(c syntax.DeclTypeable, strOnly bool) error {
		if supportedObject(c) != nil {
			return nil
		}
		return f(c, strOnly)
	}
	addGeneric := func(g syntax.DeclGeneric, strOnly bool) error {
		if g.Generics() == nil {
			return add(g, strOnly)
		}
		for _, entry := range g.Concretes().All() {
			if entry.Key.ContainsGenericArg() {
				continue
			}
			if err := add(entry.Value, strOnly); err != nil {
				return err
			}
		}
		return nil
	}
	var visit func(d syntax.Decl) error
	visit = func(d syntax.Decl) error {
		var err error
		switch d := d.(type) {
		case *syntax.DeclClass:
			err = addGeneric(d, false)
		case *syntax.DeclStruct:
			err = addGeneric(d, true)
		case *syntax.DeclEnum:
			err = add(d, true)
		}
		if err != nil {
			return err
		}
		for child := range d.Decls() {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	for _, entry := range ast.CodeFiles.All() {
		if err := visit(entry.Value.Decl); err != nil {
			return err
		}
	}
	return nil
}

// classMethods is the class table's methods for c, by slot, declaring each
// function it names. A class whose instance may exist has all its methods;
// any other only Object's, which the runtime calls on whatever instance it is
// handed, so that one made where codegen does not see it is marked and
// deleted all the same. A null slot is one no method fills.
func (p *llvmProgram) classMethods(c syntax.DeclTypeable, strOnly bool) []llvm.Value {
	methods := make([]llvm.Value, instanceFieldCount[*syntax.FieldMethod](c))
	if strOnly {
		methods = methods[:min(len(methods), 1)]
	}
	for i := range methods {
		methods[i] = llvm.ConstNull(p.ptr)
	}
	for name, method := range instanceFields[*syntax.FieldMethod](c) {
		if strOnly && name != syntax.StrMethodName {
			continue
		}
		origin, ok := originMethod(c, name)
		if !ok || method.Body == nil || method.Generics() != nil || *origin.Index() >= len(methods) {
			continue
		}
		if p.live[c] || strOnly || *origin.Index() <= base.DeleteMethodSlot {
			methods[*origin.Index()] = p.methodFunction(origin, c)
		}
	}
	return methods
}

// emitReachable compiles every function the program can reach: those pending,
// and the methods of every class table entry, until nothing more is.
func (p *llvmProgram) emitReachable(ast *syntax.AST) error {
	for {
		if err := p.emitPending(); err != nil {
			return err
		}
		live := len(p.live)
		err := eachClass(ast, func(c syntax.DeclTypeable, strOnly bool) error {
			p.classMethods(c, strOnly)
			if p.live[c] {
				_, err := p.initializerFor(c)
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(p.pending) == 0 && len(p.live) == live {
			return nil
		}
	}
}

// emitClassTable lays out the compiled methods of every class, sorted by short
// id, as the runtime looks them up for dispatch, with the routine that
// initializes a fresh instance and whether its Mark and Delete are its own. It
// runs once emitReachable has compiled every function it names.
func (p *llvmProgram) emitClassTable(ast *syntax.AST) (llvm.Value, int, error) {
	type class struct {
		short       uint64
		methods     llvm.Value
		count       int
		initializer llvm.Value
		flags       uint64
	}
	var classes []class
	err := eachClass(ast, func(c syntax.DeclTypeable, strOnly bool) error {
		initializer := llvm.ConstNull(p.ptr)
		if p.live[c] {
			fn, err := p.initializerFor(c)
			if err != nil {
				return err
			}
			if !fn.IsNil() {
				initializer = fn
			}
		}
		var flags uint64
		if !strOnly {
			if !objectDefault(c, base.MarkMethodName) {
				flags |= classMarksCustom
			}
			if !objectDefault(c, base.DeleteMethodName) {
				flags |= classDeletesCustom
			}
		}
		methods := p.classMethods(c, strOnly)
		table := llvm.AddGlobal(p.mod, llvm.ArrayType(p.ptr, max(len(methods), 1)), c.DeclType().String()+".methods")
		if len(methods) == 0 {
			methods = []llvm.Value{llvm.ConstNull(p.ptr)}
		}
		table.SetInitializer(llvm.ConstArray(p.ptr, methods))
		table.SetGlobalConstant(true)
		table.SetLinkage(llvm.PrivateLinkage)
		a, _, _ := p.typeInstance(c.DeclType()).packed()
		classes = append(classes, class{short: a >> 32, methods: table, count: len(methods), initializer: initializer, flags: flags})
		return nil
	})
	if err != nil {
		return llvm.Value{}, 0, err
	}
	if len(p.pending) > 0 {
		return llvm.Value{}, 0, errors.New("class table names a function not compiled")
	}
	if len(classes) == 0 {
		return llvm.ConstNull(p.ptr), 0, nil
	}
	slices.SortFunc(classes, func(a, b class) int { return cmp.Compare(a.short, b.short) })

	entryType := p.ctx.StructType([]llvm.Type{p.i32, p.i32, p.ptr, p.ptr, p.i32}, false)
	entries := make([]llvm.Value, len(classes))
	for i, c := range classes {
		entries[i] = llvm.ConstNamedStruct(entryType, []llvm.Value{
			llvm.ConstInt(p.i32, c.short, false), llvm.ConstInt(p.i32, uint64(c.count), false), c.methods,
			c.initializer, llvm.ConstInt(p.i32, c.flags, false),
		})
	}
	table := llvm.AddGlobal(p.mod, llvm.ArrayType(entryType, len(entries)), "zam.classes")
	table.SetInitializer(llvm.ConstArray(entryType, entries))
	table.SetGlobalConstant(true)
	table.SetLinkage(llvm.PrivateLinkage)
	return table, len(classes), nil
}

// emitEnumValue initializes the static an enum value is: its discriminant,
// then its backing value where it has one, or else what its initializer
// method makes of it.
func (e *funcEmitter) emitEnumValue(f *syntax.FieldEnum) error {
	enum := f.OuterDecl().(*syntax.DeclEnum)
	e.decl = enum
	e.b.CreateStore(e.const64(enumDiscriminant(f)), e.staticAddress(*f.Index()))
	if f.Value != nil {
		primitive := enum.Primitive()
		v, err := e.emitExprAs(f.Value, primitive)
		if err != nil {
			return err
		}
		width, _ := fieldByteInfo(primitive)
		e.store(llvmPlace{address: e.staticAddress(*f.ValueIndex()), typ: primitive, width: width}, v)
		return nil
	}
	if f.Init == nil {
		return nil
	}
	method, ok := enum.GetInstanceMethod(*f.Init)
	if !ok {
		return fmt.Errorf("enum %s has no method %s", enum.Name(), *f.Init)
	}
	ft, ok := method.FieldType().(*syntax.TypeFunction)
	if !ok {
		return unsupported("method %s of type %s", method.Name(), method.FieldType())
	}
	receiver := llvmReceiver{
		value: e.b.CreatePtrToInt(e.staticAddress(*f.Index()), e.p.i64, ""),
		typ:   syntax.NewWrappedType(syntax.TypeKindPointer, enum.DeclType()),
	}
	fn := e.p.functionFor(method)
	_, err := e.emitCall(f.Parameters, ft, &receiver, func(args, result llvm.Value) {
		e.b.CreateCall(e.p.entryType, fn, []llvm.Value{e.thread, llvm.ConstNull(e.p.ptr), args, result}, "")
	})
	return err
}

// emitProgramEntry compiles the entry the runtime starts the program at: the
// static initializer, then main.
func (p *llvmProgram) emitProgramEntry(staticInit, main llvm.Value) llvm.Value {
	b := p.ctx.NewBuilder()
	defer b.Dispose()
	fn := llvm.AddFunction(p.mod, "zam_entry", p.entryType)
	fn.SetLinkage(llvm.InternalLinkage)
	b.SetInsertPointAtEnd(p.ctx.AddBasicBlock(fn, "entry"))
	null := llvm.ConstNull(p.ptr)
	b.CreateCall(p.entryType, staticInit, []llvm.Value{fn.Param(0), null, null, null}, "")
	b.CreateCall(p.entryType, main, []llvm.Value{fn.Param(0), fn.Param(1), fn.Param(2), fn.Param(3)}, "")
	b.CreateRetVoid()
	return fn
}

// staticAddress is the address of the static slot slot.
func (e *funcEmitter) staticAddress(slot int) llvm.Value {
	return e.call("zamrt_static", e.thread, e.const64(uint64(slot)))
}

// staticPlace is where the static member lies, at its own width.
func (e *funcEmitter) staticPlace(member *syntax.FieldMember) llvmPlace {
	width, _ := fieldByteInfo(member.FieldType())
	if isAggregate(member.FieldType()) {
		width = byteSize(member.FieldType())
	}
	return llvmPlace{address: e.staticAddress(*member.Index()), typ: member.FieldType(), width: width}
}

// declareLocal gives a local of type t a slot of its own, rooted when t is
// one the collector visits, and binds name to it when there is one. Slots are
// never handed out twice, so that a rooted slot only ever holds what its root
// says it does.
func (e *funcEmitter) declareLocal(name string, t syntax.Type) uint64 {
	slot := e.reserve(stackWords(t))
	if rooted(t) {
		e.roots = append(e.roots, e.p.rootOf(slot, t))
		if name == "" {
			e.temps = append(e.temps, llvmLocal{slot: slot, typ: t})
		}
	}
	if name != "" {
		e.declare(name, llvmLocal{slot: slot, typ: t})
		if e.debugged {
			di := e.p.debug.di
			variable := di.CreateAutoVariable(e.scope, llvm.DIAutoVariable{
				Name: name, File: e.file, Line: e.line, Type: e.p.debug.typeOf(t), AlwaysPreserve: true,
			})
			// Located from the frame itself, which is what stays addressable
			// through instruction selection.
			offset := (frameHeaderWords + slot) * 8
			di.InsertDeclareAtEnd(e.frame, variable, di.CreateExpression([]uint64{dwarfOpPlusUconst, offset}),
				llvm.DebugLoc{Line: uint(e.line), Scope: e.scope}, e.b.GetInsertBlock())
		}
	}
	return slot
}

// ground is t with the type arguments of the instantiation being compiled
// substituted for its generic arguments.
func (e *funcEmitter) ground(t syntax.Type) syntax.Type {
	if t == nil || e.mapper == nil {
		return t
	}
	return e.mapper(t)
}

func (e *funcEmitter) const64(n uint64) llvm.Value { return llvm.ConstInt(e.p.i64, n, false) }

// call calls the runtime function name.
func (e *funcEmitter) call(name string, args ...llvm.Value) llvm.Value {
	f := e.p.runtime[name]
	return e.b.CreateCall(f.typ, f.fn, args, "")
}

// reserve takes words more slots for a local.
func (e *funcEmitter) reserve(words uint64) uint64 {
	slot := e.nextSlot
	e.nextSlot += words
	e.slotCount = max(e.slotCount, e.nextSlot)
	return slot
}

// slot is the address of the frame slot slot.
func (e *funcEmitter) slot(slot uint64) llvm.Value {
	return e.b.CreateInBoundsGEP(e.p.i64, e.slots, []llvm.Value{e.const64(slot)}, "")
}

// entryAlloca allocates words words for the whole of the function, in its
// entry block.
func (e *funcEmitter) entryAlloca(words uint64) llvm.Value {
	at := e.b.GetInsertBlock()
	e.b.SetInsertPointAtEnd(e.entry)
	e.locate(e.startLine, 0)
	v := e.b.CreateArrayAlloca(e.p.i64, e.const64(max(words, 1)), "")
	e.b.SetInsertPointAtEnd(at)
	e.locate(e.line, e.column)
	return v
}

func (e *funcEmitter) pushScope() {
	e.scopes = append(e.scopes, &llvmScope{vars: map[string]llvmLocal{}, active: e.active})
}

func (e *funcEmitter) declare(name string, local llvmLocal) {
	e.scopes[len(e.scopes)-1].vars[name] = local
}

func (e *funcEmitter) popScope() {
	e.active = e.scopes[len(e.scopes)-1].active
	e.scopes = e.scopes[:len(e.scopes)-1]
}

func (e *funcEmitter) lookup(name string) (llvmLocal, bool) {
	for i := len(e.scopes) - 1; i >= 0; i-- {
		if local, ok := e.scopes[i].vars[name]; ok {
			return local, true
		}
	}
	return llvmLocal{}, false
}

// terminated reports whether the block being emitted into has ended.
func (e *funcEmitter) terminated() bool {
	last := e.b.GetInsertBlock().LastInstruction()
	if last.IsNil() {
		return false
	}
	switch last.InstructionOpcode() {
	case llvm.Ret, llvm.UncondBr, llvm.CondBr, llvm.Switch, llvm.IndirectBr, llvm.Unreachable:
		return true
	}
	return false
}

// reopen starts a block for code that follows a jump, which nothing reaches.
func (e *funcEmitter) reopen() {
	e.b.SetInsertPointAtEnd(e.ctx.AddBasicBlock(e.fn, "unreachable"))
}

func (e *funcEmitter) emitReturnVoid() {
	e.call("zamrt_frame_pop", e.thread, e.frame)
	e.b.CreateRetVoid()
}

// primitiveKind is the kind of the word-sized primitive t, the only values
// native code generation compiles yet.
func primitiveKind(t syntax.Type) (syntax.TypeKind, error) {
	t = syntax.StripConst(t)
	if p, ok := t.(*syntax.TypePrimitive); ok {
		switch k := p.Kind(); k {
		case syntax.TypeKindInt, syntax.TypeKindUint, syntax.TypeKindBool,
			syntax.TypeKindChar, syntax.TypeKindByte, syntax.TypeKindFloat, syntax.TypeKindPosit:
			return k, nil
		}
	}
	return 0, unsupported("a value of type %s", t)
}

// valueKind is the kind of t where it is a value a local, a static or an
// argument may hold yet: a word-sized primitive or a string.
func valueKind(t syntax.Type) (syntax.TypeKind, error) {
	switch t := syntax.StripConst(t).(type) {
	case *syntax.TypePrimitive:
		if t.Kind() == syntax.TypeKindString || t.Kind() == syntax.TypeKindAny || t.Kind() == syntax.TypeKindType {
			return t.Kind(), nil
		}
	case *syntax.TypeFunction:
		return syntax.TypeKindFunction, nil
	case *syntax.TypeWrapped:
		if t.Kind() == syntax.TypeKindPointer || t.Kind() == syntax.TypeKindThread {
			return t.Kind(), nil
		}
	case syntax.TypeResolvable:
		if enum, ok := t.Decl().(*syntax.DeclEnum); ok {
			if stackWords(t) == 1 {
				return kindEnum, nil
			}
			for _, member := range instanceFields[*syntax.FieldMember](enum) {
				if _, err := valueKind(member.FieldType()); err != nil {
					return 0, err
				}
			}
			return kindMemberEnum, nil
		}
		if d, ok := structDecl(t); ok {
			if err := supportedObject(d); err != nil {
				return 0, err
			}
			for _, member := range instanceFields[*syntax.FieldMember](d) {
				if _, err := valueKind(member.FieldType()); err != nil {
					return 0, err
				}
			}
			return kindStruct, nil
		}
		if d, ok := instanceDecl(t); ok {
			if err := supportedObject(d); err != nil {
				return 0, err
			}
			for _, member := range instanceFields[*syntax.FieldMember](d) {
				if _, err := valueKind(member.FieldType()); err != nil && !isTail(member.FieldType()) {
					return 0, err
				}
			}
			return kindInstance, nil
		}
	case *syntax.TypeTuple:
		for _, element := range t.Types {
			if _, err := valueKind(element); err != nil {
				return 0, err
			}
		}
		return syntax.TypeKindTuple, nil
	case *syntax.TypeArray:
		// An unsized array is a declaration's tail, addressed where it lies.
		if _, err := valueKind(t.Type); err != nil {
			return 0, err
		}
		return syntax.TypeKindArray, nil
	}
	return primitiveKind(t)
}

// isAggregate reports whether a value of t is handled by its address rather
// than as a word: a tuple or a struct, whatever its width.
func isAggregate(t syntax.Type) bool {
	switch t := syntax.StripConst(t).(type) {
	case *syntax.TypeTuple:
		return true
	case *syntax.TypeArray:
		return t.Size != nil
	}
	if p, ok := syntax.StripConst(t).(*syntax.TypePrimitive); ok && p.Kind() == syntax.TypeKindType {
		return true
	}
	if _, ok := instanceDecl(t); ok {
		return true
	}
	if _, ok := memberEnum(t); ok {
		return true
	}
	_, ok := structDecl(t)
	return ok
}

// isTail reports whether t is an unsized array, which is only ever a
// declaration's tail and is named by where it begins.
func isTail(t syntax.Type) bool {
	a, ok := syntax.StripConst(t).(*syntax.TypeArray)
	return ok && a.Size == nil
}

// structDecl is the struct a value of t is, laid out inline.
func structDecl(t syntax.Type) (syntax.Decl, bool) {
	r, ok := syntax.StripConst(t).(syntax.TypeResolvable)
	if !ok || r.Decl() == nil {
		return nil, false
	}
	switch d := r.Decl().(type) {
	case *syntax.DeclStruct, *syntax.DeclConcreteStruct:
		return d, true
	}
	return nil, false
}

// kindStruct is the kind valueKind answers for a struct, which no type kind
// of the language's is.
var kindStruct = syntax.TypeKind(255)

// kindInstance is the kind valueKind answers for a class instance laid out
// inline rather than pointed at.
var kindInstance = syntax.TypeKind(254)

// instanceDecl is the class a value of t is an instance of, laid out inline.
func instanceDecl(t syntax.Type) (syntax.Decl, bool) {
	r, ok := syntax.StripConst(t).(syntax.TypeResolvable)
	if !ok || r.Decl() == nil {
		return nil, false
	}
	switch d := r.Decl().(type) {
	case *syntax.DeclClass, *syntax.DeclConcreteClass:
		return d, true
	}
	return nil, false
}

// byteSize is how many bytes a value of t spans where it is laid out.
func byteSize(t syntax.Type) uint64 {
	size, _ := fieldByteInfo(t)
	return size
}

// kindMemberEnum is the kind valueKind answers for a value of an enum with
// members: its discriminant's word, then its members, laid out inline.
var kindMemberEnum = syntax.TypeKind(253)

// memberEnum is the enum with members a value of t is.
func memberEnum(t syntax.Type) (*syntax.DeclEnum, bool) {
	r, ok := syntax.StripConst(t).(syntax.TypeResolvable)
	if !ok || r.Decl() == nil {
		return nil, false
	}
	enum, ok := r.Decl().(*syntax.DeclEnum)
	return enum, ok && stackWords(t) > 1
}

// kindEnum is the kind valueKind answers for an enum value, one word holding
// its discriminant.
var kindEnum = syntax.TypeKindReference

// rooted reports whether a value of t is a reference the collector visits.
func rooted(t syntax.Type) bool {
	kind, err := valueKind(t)
	if err != nil {
		return false
	}
	switch kind {
	case syntax.TypeKindString, syntax.TypeKindFunction, syntax.TypeKindAny, syntax.TypeKindThread, kindInstance:
		return true
	case syntax.TypeKindPointer:
		_, ok := objectDecl(t)
		return ok
	case syntax.TypeKindTuple:
		for _, element := range syntax.StripConst(t).(*syntax.TypeTuple).Types {
			if rooted(element) {
				return true
			}
		}
	case kindStruct, kindMemberEnum:
		d, ok := structDecl(t)
		if !ok {
			d, _ = memberEnum(t)
		}
		for _, member := range instanceFields[*syntax.FieldMember](d) {
			if rooted(member.FieldType()) {
				return true
			}
		}
	case syntax.TypeKindArray:
		return rooted(syntax.StripConst(t).(*syntax.TypeArray).Type)
	}
	return false
}

// objectDecl is the class, abstract or interface t points at.
func objectDecl(t syntax.Type) (syntax.Decl, bool) {
	w, ok := syntax.StripConst(t).(*syntax.TypeWrapped)
	if !ok || w.Kind() != syntax.TypeKindPointer {
		return nil, false
	}
	r, ok := syntax.StripConst(w.Type).(syntax.TypeResolvable)
	if !ok || r.Decl() == nil {
		return nil, false
	}
	switch d := r.Decl().(type) {
	case *syntax.DeclClass, *syntax.DeclAbstract, *syntax.DeclInterface,
		*syntax.DeclConcreteClass, *syntax.DeclConcreteAbstract, *syntax.DeclConcreteInterface:
		return d, true
	}
	return nil, false
}

// isInstanceMethod reports whether method is one of its declaration's
// instance methods.
func isInstanceMethod(method *syntax.FieldMethod) bool {
	f, ok := method.OuterDecl().GetInstanceMethod(method.Name())
	return ok && f == method
}

// supportedObject refuses a declaration native code generation cannot lay
// out yet: a generic or a tailed one.
func supportedObject(d syntax.Decl) error {
	if c, ok := d.(syntax.DeclConcrete); ok {
		if c.DeclType().ContainsGenericArg() {
			return unsupported("open instantiation %s", c.DeclType())
		}
	} else if g, ok := d.(syntax.DeclGeneric); ok && g.Generics() != nil {
		return unsupported("generic declaration %s", d.Name())
	}
	return nil
}

func (e *funcEmitter) emitStmt(stmt syntax.Stmt) error {
	if _, block := stmt.(*syntax.StmtBlock); !block {
		e.at(stmt.Start())
	}
	mark := len(e.temps)
	err := e.emitStmtKind(stmt)
	if err == nil && !e.terminated() {
		for _, temp := range e.temps[mark:] {
			for w := range stackWords(temp.typ) {
				e.b.CreateStore(e.const64(0), e.slot(temp.slot+w))
			}
		}
	}
	e.temps = e.temps[:mark]
	return err
}

// emitStmtKind compiles one statement of whatever kind it is.
func (e *funcEmitter) emitStmtKind(stmt syntax.Stmt) error {
	switch s := stmt.(type) {
	case *syntax.StmtBlock:
		e.pushScope()
		for _, inner := range s.Stmts {
			if e.terminated() {
				e.reopen()
			}
			if err := e.emitStmt(inner); err != nil {
				return err
			}
		}
		scope := e.scopes[len(e.scopes)-1]
		if !e.terminated() {
			if err := e.emitDefers(scope); err != nil {
				return err
			}
		}
		if len(scope.defers) > 0 {
			scope.resume = e.ctx.AddBasicBlock(e.fn, "resume")
			if !e.terminated() {
				e.b.CreateBr(scope.resume)
			}
			e.b.SetInsertPointAtEnd(scope.resume)
		}
		e.popScope()
		return nil

	case *syntax.StmtDefer:
		scope := e.scopes[len(e.scopes)-1]
		snapshot := append([]*llvmScope(nil), e.scopes...)
		snapshot[len(snapshot)-1] = &llvmScope{vars: maps.Clone(scope.vars)}
		d := &llvmDefer{stmt: s.Stmt, scopes: snapshot, scope: scope, id: len(e.defers) + 1, outer: e.active}
		scope.defers = append(scope.defers, d)
		e.defers = append(e.defers, d)
		e.active = d.id
		e.setDeferred(d.id)
		return nil

	case *syntax.StmtSwitch:
		return e.emitSwitch(s)

	case *syntax.StmtVariable:
		typ := e.ground(s.Type)
		if _, err := valueKind(typ); err != nil {
			return err
		}
		var value llvm.Value
		if s.Expr != nil {
			v, err := e.emitExprAs(s.Expr, typ)
			if err != nil {
				return err
			}
			value = v
		}
		pl := e.slotPlace(e.declareLocal(s.Name(), typ), typ)
		if !value.IsNil() {
			e.store(pl, value)
			return nil
		}
		if d, ok := instanceDecl(typ); ok {
			return e.initializeInstance(pl.address, typ, d)
		}
		return e.initializeStruct(pl.address, typ)

	case *syntax.StmtUnpack:
		return e.emitUnpack(s)

	case *syntax.StmtExpr:
		switch s.Kind() {
		case syntax.StmtKindReturn:
			if s.Expr != nil && e.returnType == nil {
				// A void method returns what a void call answers, which is
				// nothing: the call is made for what it does.
				if _, err := e.emitExpr(s.Expr); err != nil {
					return err
				}
				if err := e.emitDefersFrom(0); err != nil {
					return err
				}
				e.emitReturnVoid()
				return nil
			}
			if s.Expr != nil {
				v, err := e.emitExprAs(s.Expr, e.returnType)
				if err != nil {
					return err
				}
				e.store(e.slotPlace(e.returnSlot, e.returnType), v)
			}
			if err := e.emitDefersFrom(0); err != nil {
				return err
			}
			if s.Expr != nil {
				e.copyWords(e.fn.Param(3), e.slot(e.returnSlot), stackWords(e.returnType))
			}
			e.emitReturnVoid()
			return nil
		case syntax.StmtKindExpr:
			_, err := e.emitExpr(s.Expr)
			return err
		case syntax.StmtKindFree:
			if kind, err := valueKind(e.ground(s.Expr.ResolvedType())); err != nil || kind != syntax.TypeKindPointer {
				return unsupported("freeing %s", e.ground(s.Expr.ResolvedType()))
			}
			v, err := e.emitExpr(s.Expr)
			if err != nil {
				return err
			}
			e.call("zamrt_free", e.thread, e.p.typeID(e.ground(s.Expr.ResolvedType())), v)
			return nil
		case syntax.StmtKindMark, syntax.StmtKindDelete:
			t := e.ground(s.Expr.ResolvedType())
			if _, err := valueKind(t); err != nil {
				return err
			}
			v, err := e.emitExpr(s.Expr)
			if err != nil {
				return err
			}
			helper := "zamrt_mark"
			if s.Kind() == syntax.StmtKindDelete {
				helper = "zamrt_delete"
			}
			e.call(helper, e.thread, e.p.typeID(t), e.wordsOf(v, t))
			return nil
		}
		return unsupported("statement kind %d", s.Kind())

	case *syntax.StmtIf:
		cond, err := e.emitCondition(s.Condition)
		if err != nil {
			return err
		}
		then := e.ctx.AddBasicBlock(e.fn, "then")
		end := e.ctx.AddBasicBlock(e.fn, "endif")
		otherwise := end
		if s.Else != nil {
			otherwise = e.ctx.AddBasicBlock(e.fn, "else")
		}
		e.b.CreateCondBr(cond, then, otherwise)
		if err := e.emitBranch(then, s.Then, end); err != nil {
			return err
		}
		if s.Else != nil {
			if err := e.emitBranch(otherwise, s.Else, end); err != nil {
				return err
			}
		}
		e.b.SetInsertPointAtEnd(end)
		return nil

	case *syntax.StmtFor:
		return e.emitFor(s)

	case *syntax.StmtLabel:
		e.label = s.Label
		return e.emitStmt(s.LoopStmt)

	case *syntax.StmtLoopJump:
		loop := e.findLoop(s.Label)
		if loop == nil {
			return unsupported("a jump to label %q", s.Label)
		}
		if err := e.emitDefersFrom(loop.scopeDepth); err != nil {
			return err
		}
		if s.Kind() == syntax.StmtKindBreak {
			e.b.CreateBr(loop.breakTo)
		} else {
			e.b.CreateBr(loop.continueTo)
		}
		e.reopen()
		return nil
	}
	return unsupported("%T statements", stmt)
}

// emitBranch emits stmt into block in a scope of its own, then falls to end.
func (e *funcEmitter) emitBranch(block llvm.BasicBlock, stmt syntax.Stmt, end llvm.BasicBlock) error {
	e.b.SetInsertPointAtEnd(block)
	e.pushScope()
	if err := e.emitStmt(stmt); err != nil {
		return err
	}
	scope := e.scopes[len(e.scopes)-1]
	if !e.terminated() {
		if err := e.emitDefers(scope); err != nil {
			return err
		}
	}
	if len(scope.defers) > 0 {
		scope.resume = end
	}
	e.popScope()
	if !e.terminated() {
		e.b.CreateBr(end)
	}
	return nil
}

// emitDefers runs the statements scope deferred, last first, each with its
// names meaning what they meant when it was deferred.
func (e *funcEmitter) emitDefers(scope *llvmScope) error {
	scopes := e.scopes
	defer func() { e.scopes = scopes }()
	for i := len(scope.defers) - 1; i >= 0; i-- {
		if err := e.emitDeferred(scope.defers[i]); err != nil {
			return err
		}
	}
	return nil
}

// emitDeferred runs a deferred statement, with its names meaning what they
// meant when it was deferred, once it is no longer one a panic would run.
func (e *funcEmitter) emitDeferred(d *llvmDefer) error {
	e.setDeferred(d.outer)
	active := e.active
	e.active = d.outer
	defer func() { e.active = active }()
	e.scopes = append(d.scopes[:len(d.scopes):len(d.scopes)], &llvmScope{vars: map[string]llvmLocal{}})
	return e.emitStmt(d.stmt)
}

// setDeferred records id as the innermost defer where the function is
// running.
func (e *funcEmitter) setDeferred(id int) {
	if e.deferred.IsNil() {
		e.deferred = e.entryAlloca(1)
	}
	e.b.CreateStore(e.const64(uint64(id)), e.deferred).SetVolatile(true)
}

// emitLanding gives a function that defers its landing pad, which its entry
// sets up: a panic landing there runs the defers innermost where it was, each
// scope's last first. Once a scope's have run, execution goes on past the
// scope if one of them recovered, and with the next scope's otherwise, until
// none are left and the panic unwinds the function.
func (e *funcEmitter) emitLanding() error {
	landing := e.ctx.AddBasicBlock(e.fn, "landing")
	unwind := e.ctx.AddBasicBlock(e.fn, "unwind")
	handler := e.entryAlloca(handlerWords)
	handler.SetAlignment(16)

	e.b.SetInsertPointAtEnd(landing)
	innermost := e.b.CreateLoad(e.p.i64, e.deferred, "")
	innermost.SetVolatile(true)
	choose := e.b.CreateSwitch(innermost, unwind, len(e.defers))
	// A statement deferred inside a deferred block is a defer of its own each
	// time that block is compiled, here too, so each gets its block as it is
	// first named.
	blocks := map[int]llvm.BasicBlock{}
	blockOf := func(id int) llvm.BasicBlock {
		block, ok := blocks[id]
		if !ok {
			block = e.ctx.AddBasicBlock(e.fn, "landing.defer")
			blocks[id] = block
			choose.AddCase(e.const64(uint64(id)), block)
		}
		return block
	}

	var recovered llvm.BasicBlock
	scopes := e.scopes
	defer func() { e.scopes = scopes }()
	for i := 0; i < len(e.defers); i++ {
		d := e.defers[i]
		e.b.SetInsertPointAtEnd(blockOf(d.id))
		if err := e.emitDeferred(d); err != nil {
			return err
		}
		if e.terminated() {
			continue
		}
		next := unwind
		if d.outer != 0 {
			next = blockOf(d.outer)
			if e.defers[d.outer-1].scope == d.scope {
				e.b.CreateBr(next)
				continue
			}
		}
		resume := d.scope.resume
		if resume.IsNil() {
			if recovered.IsNil() {
				at := e.b.GetInsertBlock()
				recovered = e.ctx.AddBasicBlock(e.fn, "recovered")
				e.b.SetInsertPointAtEnd(recovered)
				e.emitFallthroughReturn()
				e.b.SetInsertPointAtEnd(at)
			}
			resume = recovered
		}
		panicking := e.call("zamrt_panicking", e.thread, handler)
		e.b.CreateCondBr(e.b.CreateICmp(llvm.IntNE, panicking, llvm.ConstInt(e.p.ctx.Int8Type(), 0, false), ""), next, resume)
	}

	e.b.SetInsertPointAtEnd(unwind)
	e.call("zamrt_unwind", e.thread, handler)
	e.b.CreateUnreachable()

	// The entry ends only once the defers above have taken what they need of
	// it.
	e.b.SetInsertPointAtEnd(e.entry)
	e.locate(e.startLine, 0)
	e.b.CreateStore(e.const64(0), e.deferred).SetVolatile(true)
	e.call("zamrt_handler_push", e.thread, handler, e.frame)
	args := []llvm.Value{handler}
	if e.p.setjmpFrame {
		args = append(args, llvm.ConstNull(e.p.ptr))
	}
	jumped := e.b.CreateCall(e.p.setjmp.typ, e.p.setjmp.fn, args, "")
	e.b.CreateCondBr(e.b.CreateICmp(llvm.IntEQ, jumped, llvm.ConstInt(e.p.i32, 0, false), ""), e.body, landing)
	return nil
}

// emitFallthroughReturn returns from the end of the function, answering what
// its return slot holds: nothing a return wrote unless a panic interrupted
// it, so the result's zero value.
func (e *funcEmitter) emitFallthroughReturn() {
	if e.returnType != nil {
		e.copyWords(e.fn.Param(3), e.slot(e.returnSlot), stackWords(e.returnType))
	}
	e.emitReturnVoid()
}

// emitDefersFrom runs the defers of every scope from depth inward, innermost
// first, as control leaves them all at once.
func (e *funcEmitter) emitDefersFrom(depth int) error {
	open := append([]*llvmScope(nil), e.scopes...)
	for i := len(open) - 1; i >= depth; i-- {
		if err := e.emitDefers(open[i]); err != nil {
			return err
		}
	}
	return nil
}

// emitSwitch tests the switch's value against each case's values in order and
// runs the first body that matches, or the else when none does. A body ends
// the switch unless it continues into the next one, the else coming after the
// last.
func (e *funcEmitter) emitSwitch(s *syntax.StmtSwitch) error {
	e.pushScope()
	typ := e.ground(s.Expr.ResolvedType())
	kind, err := primitiveKind(typ)
	enumKind, enumErr := valueKind(typ)
	if err != nil && enumErr == nil && (enumKind == kindEnum || enumKind == kindMemberEnum) {
		kind, err = syntax.TypeKindUint, nil
	}
	if err != nil {
		return err
	}
	// An enum with members is told apart by its discriminant, its first word.
	discriminant := func(v llvm.Value) llvm.Value {
		if enumErr == nil && enumKind == kindMemberEnum {
			return e.b.CreateLoad(e.p.i64, v, "")
		}
		return v
	}
	value, err := e.emitExprAs(s.Expr, typ)
	if err != nil {
		return err
	}
	value = discriminant(value)

	bodies := make([]llvm.BasicBlock, len(s.Cases))
	for i := range s.Cases {
		bodies[i] = e.ctx.AddBasicBlock(e.fn, "switch.case")
	}
	elseBlock := e.ctx.AddBasicBlock(e.fn, "switch.else")
	end := e.ctx.AddBasicBlock(e.fn, "switch.end")

	for i, c := range s.Cases {
		for _, caseValue := range c.Values {
			v, err := e.emitExprAs(caseValue, typ)
			if err != nil {
				return err
			}
			v = discriminant(v)
			next := e.ctx.AddBasicBlock(e.fn, "switch.test")
			e.b.CreateCondBr(e.equal(kind, value, v), bodies[i], next)
			e.b.SetInsertPointAtEnd(next)
		}
	}
	e.b.CreateBr(elseBlock)

	loop := &llvmLoop{label: e.label, breakTo: end, scopeDepth: len(e.scopes)}
	e.label = ""
	e.loops = append(e.loops, loop)
	defer func() { e.loops = e.loops[:len(e.loops)-1] }()
	for i, c := range s.Cases {
		loop.continueTo = elseBlock
		if i+1 < len(bodies) {
			loop.continueTo = bodies[i+1]
		}
		if err := e.emitBranch(bodies[i], c.Body, end); err != nil {
			return err
		}
	}
	loop.continueTo = end
	if s.Else == nil {
		e.b.SetInsertPointAtEnd(elseBlock)
		e.b.CreateBr(end)
	} else if err := e.emitBranch(elseBlock, s.Else, end); err != nil {
		return err
	}
	e.b.SetInsertPointAtEnd(end)
	e.popScope()
	return nil
}

// emitFor emits a loop: its condition, then its body and step for as long as
// the condition holds, then its else once it fails. A break leaves past the
// else and a continue goes to the step.
func (e *funcEmitter) emitFor(s *syntax.StmtFor) error {
	condBlock := e.ctx.AddBasicBlock(e.fn, "for.cond")
	bodyBlock := e.ctx.AddBasicBlock(e.fn, "for.body")
	stepBlock := e.ctx.AddBasicBlock(e.fn, "for.step")
	elseBlock := e.ctx.AddBasicBlock(e.fn, "for.else")
	endBlock := e.ctx.AddBasicBlock(e.fn, "for.end")

	loop := &llvmLoop{label: e.label, continueTo: stepBlock, breakTo: endBlock, scopeDepth: len(e.scopes)}
	e.label = ""
	e.b.CreateBr(condBlock)

	e.b.SetInsertPointAtEnd(condBlock)
	if s.Condition == nil {
		e.b.CreateBr(bodyBlock)
	} else {
		cond, err := e.emitCondition(s.Condition)
		if err != nil {
			return err
		}
		e.b.CreateCondBr(cond, bodyBlock, elseBlock)
	}

	e.loops = append(e.loops, loop)
	err := e.emitBranch(bodyBlock, s.Body, stepBlock)
	e.loops = e.loops[:len(e.loops)-1]
	if err != nil {
		return err
	}

	e.b.SetInsertPointAtEnd(stepBlock)
	if s.Step != nil {
		if err := e.emitStmt(s.Step); err != nil {
			return err
		}
	}
	if !e.terminated() {
		e.call("zamrt_safepoint", e.thread)
		e.b.CreateBr(condBlock)
	}

	if s.Else == nil {
		e.b.SetInsertPointAtEnd(elseBlock)
		e.b.CreateBr(endBlock)
	} else if err := e.emitBranch(elseBlock, s.Else, endBlock); err != nil {
		return err
	}
	e.b.SetInsertPointAtEnd(endBlock)
	return nil
}

// findLoop is the innermost loop labelled label, or the innermost loop for an
// empty label.
func (e *funcEmitter) findLoop(label string) *llvmLoop {
	for i := len(e.loops) - 1; i >= 0; i-- {
		if label == "" || e.loops[i].label == label {
			return e.loops[i]
		}
	}
	return nil
}

// emitCondition emits expr as an i1.
func (e *funcEmitter) emitCondition(expr syntax.Expr) (llvm.Value, error) {
	v, err := e.emitExpr(expr)
	if err != nil {
		return llvm.Value{}, err
	}
	return e.b.CreateICmp(llvm.IntNE, v, e.const64(0), ""), nil
}

// emitExprAs emits expr converted to target.
func (e *funcEmitter) emitExprAs(expr syntax.Expr, target syntax.Type) (llvm.Value, error) {
	if tuple, ok := syntax.StripConst(target).(*syntax.TypeTuple); ok {
		if x, ok := expr.(*syntax.ExprNAry); ok && x.Kind() == syntax.ExprKindTuple {
			return e.emitTuple(x, tuple)
		}
	}
	v, err := e.emitExpr(expr)
	if err != nil {
		return llvm.Value{}, err
	}
	return e.convert(v, e.ground(expr.ResolvedType()), target)
}

// convert converts the word v of type from to target.
func (e *funcEmitter) convert(v llvm.Value, from, target syntax.Type) (llvm.Value, error) {
	if syntax.StripConst(from).Kind() == syntax.TypeKindString && isStringBytesPointer(target) {
		null := e.b.CreateICmp(llvm.IntEQ, v, e.const64(0), "")
		return e.b.CreateSelect(null, v, e.b.CreateAdd(v, e.const64(stringBytesOffset), ""), ""), nil
	}
	toAny := syntax.StripConst(target).Kind() == syntax.TypeKindAny
	fromAny := syntax.StripConst(from).Kind() == syntax.TypeKindAny
	switch {
	case toAny && (fromAny || isNullType(from)):
		return v, nil
	case toAny:
		if _, err := valueKind(from); err != nil {
			return llvm.Value{}, err
		}
		if e.p.debug != nil {
			e.p.debug.typeOf(from)
		}
		boxed := e.call("zamrt_any_box", e.thread, e.p.typeID(from), e.wordsOf(v, from))
		return e.keep(e.b.CreatePtrToInt(boxed, e.p.i64, ""), target), nil
	case fromAny && syntax.StripConst(target).Kind() != syntax.TypeKindString:
		if _, err := valueKind(target); err != nil {
			return llvm.Value{}, err
		}
		out := e.slotPlace(e.declareLocal("", target), target)
		e.call("zamrt_any_unbox", e.thread, e.b.CreateIntToPtr(v, e.p.ptr, ""), e.p.typeID(target), out.address)
		if isAggregate(target) {
			return out.address, nil
		}
		return e.b.CreateLoad(e.p.i64, out.address, ""), nil
	}
	if enum, ok := syntax.EnumBackingCast(syntax.StripConst(from), syntax.StripConst(target)); ok {
		return e.enumBacking(v, enum, target)
	}
	if syntax.StripConst(target).Kind() == syntax.TypeKindString {
		if isNullType(from) || syntax.StripConst(from).Kind() == syntax.TypeKindString {
			return v, nil
		}
		if _, err := valueKind(from); err != nil {
			return llvm.Value{}, err
		}
		str := e.call("zamrt_str", e.thread, e.p.typeID(from), e.wordsOf(v, from))
		return e.keep(e.b.CreatePtrToInt(str, e.p.i64, ""), target), nil
	}
	if isAggregate(from) || isAggregate(target) {
		if syntax.StripConst(from).Equals(syntax.StripConst(target)) {
			return v, nil
		}
		return llvm.Value{}, unsupported("converting %s to %s", from, target)
	}
	if fromKind, err := valueKind(from); err == nil && fromKind == kindEnum {
		if toKind, err := valueKind(target); err == nil && toKind == kindEnum {
			return v, nil
		}
		return llvm.Value{}, unsupported("converting %s to %s", from, target)
	}
	if toKind, err := valueKind(target); err == nil && toKind == syntax.TypeKindThread {
		if fromKind, err := valueKind(from); (err == nil && fromKind == syntax.TypeKindThread) || isNullType(from) {
			return v, nil
		}
		return llvm.Value{}, unsupported("converting %s to %s", from, target)
	}
	if toKind, err := valueKind(target); err == nil && toKind == syntax.TypeKindFunction {
		if fromKind, err := valueKind(from); (err == nil && fromKind == syntax.TypeKindFunction) || isNullType(from) || isVoidPointer(from) {
			return v, nil
		}
		return llvm.Value{}, unsupported("converting %s to %s", from, target)
	}
	if fromKind, toKind, ok := pointerConversion(from, target); ok {
		word := func(k syntax.TypeKind) bool {
			return k == syntax.TypeKindPointer || k == syntax.TypeKindInt || k == syntax.TypeKindUint
		}
		if !word(fromKind) || !word(toKind) {
			return llvm.Value{}, unsupported("converting %s to %s", from, target)
		}
		_, fromObject := objectDecl(from)
		_, toObject := objectDecl(target)
		if fromObject && toObject && isDowncast(from, target) {
			pointee := syntax.StripConst(target)
			if w, ok := pointee.(*syntax.TypeWrapped); ok && w.Kind() == syntax.TypeKindPointer {
				pointee = w.Type
			}
			return e.call("zamrt_downcast", e.thread, v, e.p.typeID(pointee)), nil
		}
		return v, nil
	}
	fromKind, err := primitiveKind(from)
	if err != nil {
		return llvm.Value{}, err
	}
	toKind, err := primitiveKind(target)
	if err != nil {
		return llvm.Value{}, err
	}
	if fromKind == toKind {
		return v, nil
	}

	switch {
	case toKind == syntax.TypeKindBool && fromKind == syntax.TypeKindFloat:
		return e.b.CreateZExt(e.b.CreateFCmp(llvm.FloatUNE, e.toFloat(v), llvm.ConstFloat(e.p.f64, 0), ""), e.p.i64, ""), nil
	case toKind == syntax.TypeKindBool:
		return e.b.CreateZExt(e.b.CreateICmp(llvm.IntNE, v, e.const64(0), ""), e.p.i64, ""), nil

	case toKind == syntax.TypeKindPosit && fromKind == syntax.TypeKindUint:
		return e.call("zam_posit_from_uint", v), nil
	case toKind == syntax.TypeKindPosit && fromKind == syntax.TypeKindFloat:
		return e.call("zam_posit_from_double", e.toFloat(v)), nil
	case toKind == syntax.TypeKindPosit:
		return e.call("zam_posit_from_int", v), nil

	case fromKind == syntax.TypeKindPosit && toKind == syntax.TypeKindUint:
		return e.call("zam_posit_to_uint", v), nil
	case fromKind == syntax.TypeKindPosit && toKind == syntax.TypeKindFloat:
		return e.fromFloat(e.call("zam_posit_to_double", v)), nil
	case fromKind == syntax.TypeKindPosit:
		return e.narrow(e.call("zam_posit_to_int", v), toKind), nil

	case toKind == syntax.TypeKindFloat && fromKind == syntax.TypeKindUint:
		return e.fromFloat(e.b.CreateUIToFP(v, e.p.f64, "")), nil
	case toKind == syntax.TypeKindFloat:
		return e.fromFloat(e.b.CreateSIToFP(v, e.p.f64, "")), nil

	// A float out of an integer's range saturates and NaN becomes zero.
	case fromKind == syntax.TypeKindFloat && toKind == syntax.TypeKindUint:
		return e.call("llvm.fptoui.sat.i64.f64", e.toFloat(v)), nil
	case fromKind == syntax.TypeKindFloat:
		return e.narrow(e.call("llvm.fptosi.sat.i64.f64", e.toFloat(v)), toKind), nil
	}
	return e.narrow(v, toKind), nil
}

// enumBacking is the backing value of the enum value whose discriminant is v,
// converted to target. A backed enum's values lie back to back from its
// first value's static, and the discriminant is the offset into them.
func (e *funcEmitter) enumBacking(v llvm.Value, enum *syntax.DeclEnum, target syntax.Type) (llvm.Value, error) {
	values := enumValues(enum)
	if len(values) == 0 || values[0].Value == nil {
		return llvm.Value{}, unsupported("enum %s without backing values", enum.Name())
	}
	primitive := enum.Primitive()
	width, _ := fieldByteInfo(primitive)
	base := e.staticAddress(*values[0].ValueIndex())
	address := e.b.CreateGEP(e.ctx.Int8Type(), base, []llvm.Value{v}, "")
	backing := e.load(llvmPlace{address: address, typ: primitive, width: width})
	return e.convert(backing, primitive, target)
}

// pointerConversion reports the kinds of a conversion with a pointer on
// either side.
func pointerConversion(from, target syntax.Type) (syntax.TypeKind, syntax.TypeKind, bool) {
	fromKind, fromErr := valueKind(from)
	toKind, toErr := valueKind(target)
	if fromErr != nil || toErr != nil {
		return 0, 0, false
	}
	return fromKind, toKind, fromKind == syntax.TypeKindPointer || toKind == syntax.TypeKindPointer
}

// spill stores a word to memory of the function's own, for the runtime to read
// it through.
func (e *funcEmitter) spill(v llvm.Value) llvm.Value {
	at := e.entryAlloca(1)
	e.b.CreateStore(v, at)
	return at
}

// isNullType reports whether t is the type of null.
func isNullType(t syntax.Type) bool {
	return t == syntax.TypeNull
}

func (e *funcEmitter) toFloat(v llvm.Value) llvm.Value   { return e.b.CreateBitCast(v, e.p.f64, "") }
func (e *funcEmitter) fromFloat(v llvm.Value) llvm.Value { return e.b.CreateBitCast(v, e.p.i64, "") }

// equal is whether two words of kind are equal: a float as a number, so that
// 0.0 equals -0.0 and NaN equals nothing, and anything else as its bits.
func (e *funcEmitter) equal(kind syntax.TypeKind, left, right llvm.Value) llvm.Value {
	if kind == syntax.TypeKindFloat {
		return e.b.CreateFCmp(llvm.FloatOEQ, e.toFloat(left), e.toFloat(right), "")
	}
	return e.b.CreateICmp(llvm.IntEQ, left, right, "")
}

// narrow keeps a char or byte to its width after an operation on one.
func (e *funcEmitter) narrow(v llvm.Value, kind syntax.TypeKind) llvm.Value {
	switch kind {
	case syntax.TypeKindChar:
		return e.b.CreateAnd(v, e.const64(0xffffffff), "")
	case syntax.TypeKindByte:
		return e.b.CreateAnd(v, e.const64(0xff), "")
	}
	return v
}

func (e *funcEmitter) emitExpr(expr syntax.Expr) (llvm.Value, error) {
	switch x := expr.(type) {
	case *syntax.ExprSymbol:
		return e.emitSymbol(x)
	case *syntax.ExprNAry:
		return e.emitNAry(x)
	case *syntax.ExprCast:
		return e.emitExprAs(x.Expr, e.ground(x.Type))
	case *syntax.ExprType:
		return e.emitStaticValue(x)
	case *syntax.ExprLambda:
		return e.emitLambda(x)
	case *syntax.ExprField, *syntax.ExprFields:
		if object, decl, method, ok := e.methodCallee(x); ok {
			return e.emitMethodValue(x, object, decl, method)
		}
		pl, err := e.place(x)
		if err != nil {
			return llvm.Value{}, err
		}
		return e.load(pl), nil
	case *syntax.ExprNew:
		return e.emitNew(x)
	}
	return llvm.Value{}, unsupported("%T expressions", expr)
}

func (e *funcEmitter) emitSymbol(x *syntax.ExprSymbol) (llvm.Value, error) {
	start := x.Start()
	switch x.Kind() {
	case syntax.ExprKindLiteralInteger, syntax.ExprKindLiteralUint, syntax.ExprKindLiteralPosit:
		return e.const64(start.Value.(uint64)), nil
	case syntax.ExprKindLiteralFloat:
		return e.const64(math.Float64bits(start.Value.(float64))), nil
	case syntax.ExprKindLiteralChar:
		return e.const64(uint64(start.Value.(rune))), nil
	case syntax.ExprKindLiteralBoolean:
		if start.Kind == syntax.TTTrue {
			return e.const64(1), nil
		}
		return e.const64(0), nil
	case syntax.ExprKindIdentifier:
		pl, err := e.place(x)
		if err != nil {
			return llvm.Value{}, err
		}
		return e.load(pl), nil
	case syntax.ExprKindThis, syntax.ExprKindSuper:
		// A base shares its address with the object; super's type is what
		// selects the base's members.
		this, ok := e.lookup(thisVariableName)
		if !ok {
			return llvm.Value{}, unsupported("this outside an instance method")
		}
		return e.b.CreateLoad(e.p.i64, e.slot(this.slot), ""), nil
	case syntax.ExprKindLiteralString:
		slot, at := e.p.reader.stringLiterals.Get(stringLiteral(start.Value.(string)))
		if at < 0 {
			return llvm.Value{}, fmt.Errorf("string literal %q has no static slot", start.Value)
		}
		return e.b.CreatePtrToInt(e.staticAddress(int(slot)), e.p.i64, ""), nil
	case syntax.ExprKindLiteralNull:
		return e.const64(0), nil
	}
	return llvm.Value{}, unsupported("%s expressions", x.Kind())
}

// llvmPlace is somewhere a value lies: its address, its type, how many bytes
// of it there are, and whether it is in the heap, where storing a reference
// has to be recorded for the collector.
type llvmPlace struct {
	address llvm.Value
	typ     syntax.Type
	width   uint64
	heap    bool
}

// load reads the value at pl as a word, zero-extending a narrow one. An
// aggregate is answered as its address.
func (e *funcEmitter) load(pl llvmPlace) llvm.Value {
	if isAggregate(pl.typ) || isTail(pl.typ) {
		return pl.address
	}
	if pl.width == 8 {
		return e.b.CreateLoad(e.p.i64, pl.address, "")
	}
	narrow := e.ctx.IntType(int(pl.width * 8))
	return e.b.CreateZExt(e.b.CreateLoad(narrow, pl.address, ""), e.p.i64, "")
}

// store writes the word v to pl, truncating it to a narrow one, or copies an
// aggregate's bytes from the address v.
func (e *funcEmitter) store(pl llvmPlace, v llvm.Value) {
	volatile := e.volatileSlots && !pl.heap
	if d, ok := instanceDecl(pl.typ); ok {
		// A store moves what an instance holds and leaves where it lies alone:
		// the header in the heap says what contains it, and one in a frame is
		// written afresh.
		if pl.heap {
			header := instanceHeaderByteSizeOf(syntax.KindClass, isTailed(d))
			at := func(p llvm.Value) llvm.Value {
				return e.b.CreateGEP(e.ctx.Int8Type(), p, []llvm.Value{e.const64(header)}, "")
			}
			e.copyBytes(at(pl.address), at(v), pl.width-header)
		} else {
			e.copyMemory(pl.address, v, pl.width, volatile)
			e.p.live[d] = true
			e.call("zamrt_stamp", e.thread, e.p.typeID(pl.typ), pl.address)
		}
	} else if isAggregate(pl.typ) {
		e.copyMemory(pl.address, v, pl.width, volatile)
	} else if pl.width == 8 {
		e.b.CreateStore(v, pl.address).SetVolatile(volatile)
	} else {
		e.b.CreateStore(e.b.CreateTrunc(v, e.ctx.IntType(int(pl.width*8)), ""), pl.address).SetVolatile(volatile)
	}
	if pl.heap && rooted(pl.typ) {
		e.call("zamrt_record_write", e.thread, pl.address, e.const64(pl.width))
	}
}

// copyBytes copies n bytes from src to dst.
func (e *funcEmitter) copyBytes(dst, src llvm.Value, n uint64) {
	e.copyMemory(dst, src, n, false)
}

// copyMemory copies n bytes from src to dst, as a volatile copy when volatile.
func (e *funcEmitter) copyMemory(dst, src llvm.Value, n uint64, volatile bool) {
	flag := uint64(0)
	if volatile {
		flag = 1
	}
	e.call("llvm.memcpy.p0.p0.i64", dst, src, e.const64(n), llvm.ConstInt(e.p.i1, flag, false))
}

// slotPlace is the place a frame slot holding a value of t is.
func (e *funcEmitter) slotPlace(slot uint64, t syntax.Type) llvmPlace {
	width := uint64(8)
	if isAggregate(t) {
		width = byteSize(t)
	}
	return llvmPlace{address: e.slot(slot), typ: t, width: width}
}

// wordsOf is the address of the stack words a value of t at v lies in: the
// aggregate's own address, or a slot holding the word.
func (e *funcEmitter) wordsOf(v llvm.Value, t syntax.Type) llvm.Value {
	if isAggregate(t) {
		return v
	}
	return e.spill(v)
}

// place is where expr names: a local, an instance field of the receiver, a
// static of the declaration being compiled or of one around it, or a member
// selected from an object.
func (e *funcEmitter) place(expr syntax.Expr) (llvmPlace, error) {
	if nary, ok := expr.(*syntax.ExprNAry); ok && nary.Kind() == syntax.ExprKindDereference {
		return e.pointeePlace(nary.Exprs[0])
	}
	if x, ok := expr.(*syntax.ExprType); ok && x.Field != "" && len(x.TypeArgs) == 0 {
		resolvable, ok := x.Type.(syntax.TypeResolvable)
		if !ok {
			return llvmPlace{}, unsupported("statics of %s", x.Type)
		}
		f, ok := resolvable.Decl().GetStaticField(x.Field)
		member, isMember := f.(*syntax.FieldMember)
		if !ok || !isMember {
			return llvmPlace{}, unsupported("assigning to static %q", x.Field)
		}
		if _, err := valueKind(member.FieldType()); err != nil {
			return llvmPlace{}, err
		}
		return e.staticPlace(member), nil
	}
	if object, name, ok := selection(expr); ok {
		if tuple, ok := syntax.TupleSelectionTarget(e.ground(object.ResolvedType())); ok {
			return e.elementPlace(object, tuple, name)
		}
		obj, err := e.emitExpr(object)
		if err != nil {
			return llvmPlace{}, err
		}
		return e.memberPlace(obj, e.ground(object.ResolvedType()), name)
	}
	symbol, ok := expr.(*syntax.ExprSymbol)
	if !ok || symbol.Kind() != syntax.ExprKindIdentifier {
		return llvmPlace{}, unsupported("assigning to %s", expr.Kind())
	}
	name := symbol.Start().Value.(string)
	if local, ok := e.lookup(name); ok {
		return e.slotPlace(local.slot, local.typ), nil
	}
	if this, ok := e.lookup(thisVariableName); ok {
		if f, ok := e.decl.GetInstanceField(name); ok {
			if _, ok := f.(*syntax.FieldMember); ok {
				return e.memberPlace(e.b.CreateLoad(e.p.i64, e.slot(this.slot), ""), this.typ, name)
			}
		}
	}
	for d := e.decl; d != nil; d = d.OuterDecl() {
		f, ok := d.GetStaticField(name)
		if !ok {
			continue
		}
		if method, ok := f.(*syntax.FieldMethod); ok && method.Generics() == nil {
			e.p.staticValue(method)
			return llvmPlace{address: e.staticAddress(*method.Index()), typ: method.FieldType(), width: 8}, nil
		}
		if value, ok := f.(*syntax.FieldEnum); ok {
			return llvmPlace{address: e.staticAddress(*value.Index()), typ: value.Type(), width: 8}, nil
		}
		member, ok := f.(*syntax.FieldMember)
		if !ok {
			return llvmPlace{}, unsupported("static %q of kind %T", name, f)
		}
		if _, err := valueKind(member.FieldType()); err != nil {
			return llvmPlace{}, err
		}
		return e.staticPlace(member), nil
	}
	return llvmPlace{}, unsupported("name %q", name)
}

// elementPlace is the element name, a number, of the tuple object is or points
// at.
func (e *funcEmitter) elementPlace(object syntax.Expr, tuple *syntax.TypeTuple, name string) (llvmPlace, error) {
	if _, err := valueKind(tuple); err != nil {
		return llvmPlace{}, err
	}
	index, err := strconv.Atoi(name)
	if err != nil || index < 0 || index >= len(tuple.Types) {
		return llvmPlace{}, fmt.Errorf("no element %q of %s", name, tuple)
	}
	v, err := e.emitExpr(object)
	if err != nil {
		return llvmPlace{}, err
	}
	base := v
	if !isAggregate(e.ground(object.ResolvedType())) {
		e.nullCheck(v, panicNullMember)
		base = e.b.CreateIntToPtr(v, e.p.ptr, "")
	}
	_, _, offsets := tupleLayout(tuple)
	element := tuple.Types[index]
	address := e.b.CreateGEP(e.ctx.Int8Type(), base, []llvm.Value{e.const64(offsets[index])}, "")
	return llvmPlace{address: address, typ: element, width: byteSize(element), heap: true}, nil
}

// pointeePlace is what the pointer pointer evaluates to points at, panicking
// when it is null.
func (e *funcEmitter) pointeePlace(pointer syntax.Expr) (llvmPlace, error) {
	w, ok := syntax.StripConst(e.ground(pointer.ResolvedType())).(*syntax.TypeWrapped)
	if !ok || w.Kind() != syntax.TypeKindPointer {
		return llvmPlace{}, unsupported("dereferencing %s", e.ground(pointer.ResolvedType()))
	}
	if _, err := valueKind(w.Type); err != nil {
		return llvmPlace{}, err
	}
	width, _ := fieldByteInfo(w.Type)
	if width != 1 && width != 4 && width != 8 && !isAggregate(w.Type) {
		return llvmPlace{}, unsupported("dereferencing to %d bytes", width)
	}
	// An index, p[i], is read as *(p + i): it is p that may not be null.
	var v llvm.Value
	var err error
	if x, ok := pointer.(*syntax.ExprNAry); ok && (x.Kind() == syntax.ExprKindAdd || x.Kind() == syntax.ExprKindSubtract) {
		v, err = e.pointerArithmetic(x, true)
	} else {
		v, err = e.emitExpr(pointer)
	}
	if err != nil {
		return llvmPlace{}, err
	}
	e.nullCheck(v, panicNullDereference)
	return llvmPlace{address: e.b.CreateIntToPtr(v, e.p.ptr, ""), typ: w.Type, width: width, heap: true}, nil
}

// nullCheck panics with panic when v is null.
func (e *funcEmitter) nullCheck(v llvm.Value, panic uint64) {
	null := e.ctx.AddBasicBlock(e.fn, "null")
	ok := e.ctx.AddBasicBlock(e.fn, "nonnull")
	e.b.CreateCondBr(e.b.CreateICmp(llvm.IntEQ, v, e.const64(0), ""), null, ok)
	e.b.SetInsertPointAtEnd(null)
	e.call("zamrt_panic", e.thread, llvm.ConstInt(e.p.i32, panic, false))
	e.b.CreateUnreachable()
	e.b.SetInsertPointAtEnd(ok)
}

// memberPlace is the member name of the object obj, of type objectType,
// points at, panicking when obj is null.
func (e *funcEmitter) memberPlace(obj llvm.Value, objectType syntax.Type, name string) (llvmPlace, error) {
	if d, ok := structDecl(objectType); ok {
		return e.structMemberPlace(obj, d, name)
	}
	if d, ok := memberEnum(objectType); ok {
		return e.structMemberPlace(obj, d, name)
	}
	if w, ok := syntax.StripConst(objectType).(*syntax.TypeWrapped); ok && w.Kind() == syntax.TypeKindPointer {
		if d, ok := structDecl(w.Type); ok {
			e.nullCheck(obj, panicNullMember)
			return e.structMemberPlace(e.b.CreateIntToPtr(obj, e.p.ptr, ""), d, name)
		}
		if d, ok := memberEnum(w.Type); ok {
			e.nullCheck(obj, panicNullMember)
			return e.structMemberPlace(e.b.CreateIntToPtr(obj, e.p.ptr, ""), d, name)
		}
	}
	d, ok := objectDecl(objectType)
	inline := false
	if !ok {
		if d, ok = instanceDecl(objectType); !ok {
			return llvmPlace{}, unsupported("selecting %q from %s", name, objectType)
		}
		inline = true
	}
	if err := supportedObject(d); err != nil {
		return llvmPlace{}, err
	}
	f, at := d.AllInstanceFields().Get(name)
	if at < 0 {
		return llvmPlace{}, fmt.Errorf("no member %q", name)
	}
	member, ok := f.(*syntax.FieldMember)
	if !ok {
		return llvmPlace{}, unsupported("selecting %s %q as a value", f.Kind(), name)
	}
	if _, err := valueKind(member.FieldType()); err != nil {
		return llvmPlace{}, err
	}
	width, _ := fieldByteInfo(member.FieldType())
	if width != 1 && width != 4 && width != 8 && !isAggregate(member.FieldType()) {
		return llvmPlace{}, unsupported("a member %d bytes wide", width)
	}

	base := obj
	if !inline {
		e.nullCheck(obj, panicNullMember)
		base = e.b.CreateIntToPtr(obj, e.p.ptr, "")
	}
	offset := instanceHeaderByteSizeOf(syntax.KindClass, isTailed(d)) + uint64(*member.Offset())
	// Not inbounds: the pointer may name nothing, and an access through it has
	// to fault rather than be poison the optimizer may drop.
	address := e.b.CreateGEP(e.ctx.Int8Type(), base, []llvm.Value{e.const64(offset)}, "")
	return llvmPlace{address: address, typ: member.FieldType(), width: width, heap: true}, nil
}

// valueHeaderSize is how far into a struct or enum value d lays out its
// members begin: past a tailed struct's tail length, or an enum's
// discriminant.
func valueHeaderSize(d syntax.Decl) uint64 {
	if _, ok := d.(*syntax.DeclEnum); ok {
		return instanceHeaderByteSizeOf(syntax.KindEnum, false)
	}
	return instanceHeaderByteSizeOf(syntax.KindStruct, isTailed(d))
}

// structMemberPlace is the member name of the struct or enum d laid out at
// base.
func (e *funcEmitter) structMemberPlace(base llvm.Value, d syntax.Decl, name string) (llvmPlace, error) {
	if err := supportedObject(d); err != nil {
		return llvmPlace{}, err
	}
	f, at := d.AllInstanceFields().Get(name)
	if at < 0 {
		return llvmPlace{}, fmt.Errorf("no member %q", name)
	}
	member, ok := f.(*syntax.FieldMember)
	if !ok {
		return llvmPlace{}, unsupported("selecting %s %q as a value", f.Kind(), name)
	}
	if _, err := valueKind(member.FieldType()); err != nil {
		return llvmPlace{}, err
	}
	address := e.b.CreateGEP(e.ctx.Int8Type(), base,
		[]llvm.Value{e.const64(valueHeaderSize(d) + uint64(*member.Offset()))}, "")
	return llvmPlace{address: address, typ: member.FieldType(), width: byteSize(member.FieldType()), heap: true}, nil
}

// panicNullMember and panicNullDereference are ZAMPANIC_NULL_MEMBER and
// ZAMPANIC_NULL_DEREFERENCE.
const (
	panicNullMember      = 1
	panicNullDereference = 2
)

func (e *funcEmitter) emitNAry(x *syntax.ExprNAry) (llvm.Value, error) {
	switch x.Kind() {
	case syntax.ExprKindAssign:
		pl, err := e.place(x.Exprs[0])
		if err != nil {
			return llvm.Value{}, err
		}
		v, err := e.emitExprAs(x.Exprs[1], pl.typ)
		if err != nil {
			return llvm.Value{}, err
		}
		e.store(pl, v)
		return v, nil

	case syntax.ExprKindSpaceship:
		return e.emitSpaceship(x)

	case syntax.ExprKindSubtypeOf:
		child, err := e.emitExpr(x.Exprs[0])
		if err != nil {
			return llvm.Value{}, err
		}
		parent, err := e.emitExpr(x.Exprs[1])
		if err != nil {
			return llvm.Value{}, err
		}
		answer := e.call("zamrt_subtype_of", e.thread, child, parent)
		return e.b.CreateZExt(e.b.CreateICmp(llvm.IntNE, answer, llvm.ConstInt(e.ctx.Int8Type(), 0, false), ""), e.p.i64, ""), nil

	case syntax.ExprKindTypeof:
		return e.emitTypeof(x)

	case syntax.ExprKindHash:
		t := e.ground(x.Exprs[0].ResolvedType())
		if _, err := valueKind(t); err != nil {
			return llvm.Value{}, err
		}
		v, err := e.emitExpr(x.Exprs[0])
		if err != nil {
			return llvm.Value{}, err
		}
		return e.call("zamrt_hash", e.thread, e.p.typeID(t), e.wordsOf(v, t)), nil

	case syntax.ExprKindTuple:
		tuple, ok := syntax.StripConst(e.ground(x.ResolvedType())).(*syntax.TypeTuple)
		if !ok {
			return llvm.Value{}, unsupported("a tuple of type %s", e.ground(x.ResolvedType()))
		}
		return e.emitTuple(x, tuple)

	case syntax.ExprKindDereference:
		pl, err := e.place(x)
		if err != nil {
			return llvm.Value{}, err
		}
		return e.load(pl), nil

	case syntax.ExprKindAddressOf:
		pl, err := e.place(x.Exprs[0])
		if err != nil {
			return llvm.Value{}, err
		}
		return e.b.CreatePtrToInt(pl.address, e.p.i64, ""), nil

	case syntax.ExprKindPreIncrement, syntax.ExprKindPreDecrement,
		syntax.ExprKindPostIncrement, syntax.ExprKindPostDecrement:
		return e.emitStep(x)

	case syntax.ExprKindLogicalAND, syntax.ExprKindLogicalOR:
		return e.emitLogical(x)

	case syntax.ExprKindTernary:
		return e.emitTernary(x)

	case syntax.ExprKindAsync:
		return e.emitAsync(x)

	case syntax.ExprKindWait:
		return e.emitWait(x)

	case syntax.ExprKindCall:
		armed := e.armed
		e.armed, e.spawn = e.spawn, nil
		v, err := e.emitCallExpr(x)
		e.armed = armed
		return v, err

	case syntax.ExprKindArithmeticNegate, syntax.ExprKindLogicalNegate, syntax.ExprKindBinaryNegate:
		return e.emitNegation(x)

	case syntax.ExprKindEqual, syntax.ExprKindNotEqual, syntax.ExprKindLessThan,
		syntax.ExprKindGreaterThan, syntax.ExprKindLessThanOrEqual, syntax.ExprKindGreaterThanOrEqual:
		return e.emitComparison(x)

	case syntax.ExprKindAdd, syntax.ExprKindSubtract, syntax.ExprKindMultiply,
		syntax.ExprKindDivide, syntax.ExprKindModulus, syntax.ExprKindBinaryAND,
		syntax.ExprKindBinaryOR, syntax.ExprKindBinaryXOR, syntax.ExprKindBinaryShiftLeft,
		syntax.ExprKindBinaryShiftRight, syntax.ExprKindBinaryShiftRightRight:
		return e.emitBinary(x)
	}
	return llvm.Value{}, unsupported("%s expressions", x.Kind())
}

func (e *funcEmitter) emitStep(x *syntax.ExprNAry) (llvm.Value, error) {
	pl, err := e.place(x.Exprs[0])
	if err != nil {
		return llvm.Value{}, err
	}
	kind, err := primitiveKind(pl.typ)
	if err != nil {
		return llvm.Value{}, err
	}
	old := e.load(pl)
	up := x.Kind() == syntax.ExprKindPreIncrement || x.Kind() == syntax.ExprKindPostIncrement
	var stepped llvm.Value
	switch {
	case kind == syntax.TypeKindFloat && up:
		stepped = e.fromFloat(e.b.CreateFAdd(e.toFloat(old), llvm.ConstFloat(e.p.f64, 1), ""))
	case kind == syntax.TypeKindFloat:
		stepped = e.fromFloat(e.b.CreateFSub(e.toFloat(old), llvm.ConstFloat(e.p.f64, 1), ""))
	case kind == syntax.TypeKindPosit && up:
		stepped = e.call("zam_posit_add", old, e.const64(positOne))
	case kind == syntax.TypeKindPosit:
		stepped = e.call("zam_posit_sub", old, e.const64(positOne))
	case up:
		stepped = e.narrow(e.b.CreateAdd(old, e.const64(1), ""), kind)
	default:
		stepped = e.narrow(e.b.CreateSub(old, e.const64(1), ""), kind)
	}
	e.store(pl, stepped)
	if x.Kind() == syntax.ExprKindPostIncrement || x.Kind() == syntax.ExprKindPostDecrement {
		return old, nil
	}
	return stepped, nil
}

// emitLogical emits a chain of && or ||, which evaluates each operand only
// while the ones before it have not decided the answer.
func (e *funcEmitter) emitLogical(x *syntax.ExprNAry) (llvm.Value, error) {
	isOr := x.Kind() == syntax.ExprKindLogicalOR
	decided := llvm.ConstInt(e.p.i1, boolBit(isOr), false)
	end := e.ctx.AddBasicBlock(e.fn, "logic.end")

	var values []llvm.Value
	var blocks []llvm.BasicBlock
	for i, operand := range x.Exprs {
		v, err := e.emitCondition(operand)
		if err != nil {
			return llvm.Value{}, err
		}
		if i == len(x.Exprs)-1 {
			values = append(values, v)
			blocks = append(blocks, e.b.GetInsertBlock())
			e.b.CreateBr(end)
			break
		}
		next := e.ctx.AddBasicBlock(e.fn, "logic.next")
		values = append(values, decided)
		blocks = append(blocks, e.b.GetInsertBlock())
		if isOr {
			e.b.CreateCondBr(v, end, next)
		} else {
			e.b.CreateCondBr(v, next, end)
		}
		e.b.SetInsertPointAtEnd(next)
	}

	e.b.SetInsertPointAtEnd(end)
	phi := e.b.CreatePHI(e.p.i1, "")
	phi.AddIncoming(values, blocks)
	return e.b.CreateZExt(phi, e.p.i64, ""), nil
}

func boolBit(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func (e *funcEmitter) emitTernary(x *syntax.ExprNAry) (llvm.Value, error) {
	if isAggregate(e.ground(x.ResolvedType())) {
		return llvm.Value{}, unsupported("a ternary of tuples")
	}
	cond, err := e.emitCondition(x.Exprs[0])
	if err != nil {
		return llvm.Value{}, err
	}
	then := e.ctx.AddBasicBlock(e.fn, "ternary.then")
	otherwise := e.ctx.AddBasicBlock(e.fn, "ternary.else")
	end := e.ctx.AddBasicBlock(e.fn, "ternary.end")
	e.b.CreateCondBr(cond, then, otherwise)

	values := make([]llvm.Value, 2)
	blocks := make([]llvm.BasicBlock, 2)
	for i, block := range []llvm.BasicBlock{then, otherwise} {
		e.b.SetInsertPointAtEnd(block)
		v, err := e.emitExprAs(x.Exprs[i+1], e.ground(x.ResolvedType()))
		if err != nil {
			return llvm.Value{}, err
		}
		values[i], blocks[i] = v, e.b.GetInsertBlock()
		e.b.CreateBr(end)
	}
	e.b.SetInsertPointAtEnd(end)
	phi := e.b.CreatePHI(e.p.i64, "")
	phi.AddIncoming(values, blocks)
	return phi, nil
}

func (e *funcEmitter) emitNegation(x *syntax.ExprNAry) (llvm.Value, error) {
	v, err := e.emitExpr(x.Exprs[0])
	if err != nil {
		return llvm.Value{}, err
	}
	kind, err := primitiveKind(e.ground(x.ResolvedType()))
	if err != nil {
		return llvm.Value{}, err
	}
	switch {
	case x.Kind() == syntax.ExprKindLogicalNegate:
		return e.b.CreateZExt(e.b.CreateICmp(llvm.IntEQ, v, e.const64(0), ""), e.p.i64, ""), nil
	case x.Kind() == syntax.ExprKindBinaryNegate && kind == syntax.TypeKindBool:
		return e.b.CreateXor(v, e.const64(1), ""), nil
	case x.Kind() == syntax.ExprKindBinaryNegate:
		return e.narrow(e.b.CreateXor(v, e.const64(^uint64(0)), ""), kind), nil
	case kind == syntax.TypeKindFloat:
		return e.fromFloat(e.b.CreateFNeg(e.toFloat(v), "")), nil
	}
	return e.narrow(e.b.CreateSub(e.const64(0), v, ""), kind), nil
}

// signed reports whether a kind compares and divides as a signed integer.
func signed(kind syntax.TypeKind) bool { return kind == syntax.TypeKindInt }

func (e *funcEmitter) emitComparison(x *syntax.ExprNAry) (llvm.Value, error) {
	if len(x.Exprs) != 2 {
		return llvm.Value{}, unsupported("a chain of %d comparisons", len(x.Exprs)-1)
	}
	if kind, err := valueKind(e.ground(x.Exprs[0].ResolvedType())); err == nil && (kind == syntax.TypeKindString || kind == syntax.TypeKindTuple || kind == kindStruct || kind == syntax.TypeKindType || kind == syntax.TypeKindArray || kind == syntax.TypeKindFunction || kind == syntax.TypeKindThread || kind == kindInstance || kind == syntax.TypeKindAny || kind == kindMemberEnum) {
		return e.emitRuntimeComparison(x)
	}
	if kind, err := valueKind(e.ground(x.Exprs[0].ResolvedType())); err == nil && (kind == syntax.TypeKindPointer || kind == kindEnum) {
		if _, ok := objectDecl(e.ground(x.Exprs[0].ResolvedType())); ok {
			return e.emitRuntimeComparison(x)
		}
		return e.emitPointerComparison(x)
	}
	kind, err := primitiveKind(e.ground(x.Exprs[0].ResolvedType()))
	if err != nil {
		return llvm.Value{}, err
	}
	left, err := e.emitExprAs(x.Exprs[0], e.ground(x.Exprs[0].ResolvedType()))
	if err != nil {
		return llvm.Value{}, err
	}
	right, err := e.emitExprAs(x.Exprs[1], e.ground(x.Exprs[0].ResolvedType()))
	if err != nil {
		return llvm.Value{}, err
	}
	predicates := map[syntax.ExprKind][2]llvm.IntPredicate{
		syntax.ExprKindEqual:              {llvm.IntEQ, llvm.IntEQ},
		syntax.ExprKindNotEqual:           {llvm.IntNE, llvm.IntNE},
		syntax.ExprKindLessThan:           {llvm.IntSLT, llvm.IntULT},
		syntax.ExprKindGreaterThan:        {llvm.IntSGT, llvm.IntUGT},
		syntax.ExprKindLessThanOrEqual:    {llvm.IntSLE, llvm.IntULE},
		syntax.ExprKindGreaterThanOrEqual: {llvm.IntSGE, llvm.IntUGE},
	}
	if kind == syntax.TypeKindFloat {
		return e.b.CreateZExt(e.floatComparison(x.Kind(), left, right), e.p.i64, ""), nil
	}
	predicate := predicates[x.Kind()][1]
	if signed(kind) || kind == syntax.TypeKindPosit {
		predicate = predicates[x.Kind()][0]
	}
	return e.b.CreateZExt(e.b.CreateICmp(predicate, left, right, ""), e.p.i64, ""), nil
}

// floatComparison compares two floats: equality as numbers, and order through (l > r) - (l < r), under which NaN orders as
// equal to everything.
func (e *funcEmitter) floatComparison(op syntax.ExprKind, left, right llvm.Value) llvm.Value {
	l, r := e.toFloat(left), e.toFloat(right)
	switch op {
	case syntax.ExprKindEqual:
		return e.b.CreateFCmp(llvm.FloatOEQ, l, r, "")
	case syntax.ExprKindNotEqual:
		return e.b.CreateFCmp(llvm.FloatUNE, l, r, "")
	}
	greater := e.b.CreateFCmp(llvm.FloatOGT, l, r, "")
	less := e.b.CreateFCmp(llvm.FloatOLT, l, r, "")
	switch op {
	case syntax.ExprKindLessThan:
		return less
	case syntax.ExprKindGreaterThan:
		return greater
	case syntax.ExprKindLessThanOrEqual:
		return e.b.CreateNot(greater, "")
	}
	return e.b.CreateNot(less, "")
}

// emitPointerComparison compares two pointers as addresses, or two enum values
// by their discriminants.
func (e *funcEmitter) emitPointerComparison(x *syntax.ExprNAry) (llvm.Value, error) {
	typ := e.ground(x.Exprs[0].ResolvedType())
	left, err := e.emitExprAs(x.Exprs[0], typ)
	if err != nil {
		return llvm.Value{}, err
	}
	right, err := e.emitExprAs(x.Exprs[1], typ)
	if err != nil {
		return llvm.Value{}, err
	}
	predicates := map[syntax.ExprKind]llvm.IntPredicate{
		syntax.ExprKindEqual:              llvm.IntEQ,
		syntax.ExprKindNotEqual:           llvm.IntNE,
		syntax.ExprKindLessThan:           llvm.IntULT,
		syntax.ExprKindGreaterThan:        llvm.IntUGT,
		syntax.ExprKindLessThanOrEqual:    llvm.IntULE,
		syntax.ExprKindGreaterThanOrEqual: llvm.IntUGE,
	}
	return e.b.CreateZExt(e.b.CreateICmp(predicates[x.Kind()], left, right, ""), e.p.i64, ""), nil
}

// emitRuntimeComparison compares two values through the runtime, by their
// contents or by the Eq and Cmp of the instances they point at.
func (e *funcEmitter) emitRuntimeComparison(x *syntax.ExprNAry) (llvm.Value, error) {
	left, err := e.emitExpr(x.Exprs[0])
	if err != nil {
		return llvm.Value{}, err
	}
	right, err := e.emitExpr(x.Exprs[1])
	if err != nil {
		return llvm.Value{}, err
	}
	leftType, rightType := e.p.typeID(e.ground(x.Exprs[0].ResolvedType())), e.p.typeID(e.ground(x.Exprs[1].ResolvedType()))
	l, r := e.wordsOf(left, e.ground(x.Exprs[0].ResolvedType())), e.wordsOf(right, e.ground(x.Exprs[1].ResolvedType()))
	switch x.Kind() {
	case syntax.ExprKindEqual, syntax.ExprKindNotEqual:
		equal := e.b.CreateICmp(llvm.IntNE, e.call("zamrt_eq", e.thread, leftType, l, rightType, r),
			llvm.ConstInt(e.ctx.Int8Type(), 0, false), "")
		if x.Kind() == syntax.ExprKindNotEqual {
			equal = e.b.CreateNot(equal, "")
		}
		return e.b.CreateZExt(equal, e.p.i64, ""), nil
	}
	order := e.call("zamrt_cmp", e.thread, leftType, l, rightType, r)
	predicates := map[syntax.ExprKind]llvm.IntPredicate{
		syntax.ExprKindLessThan:           llvm.IntSLT,
		syntax.ExprKindGreaterThan:        llvm.IntSGT,
		syntax.ExprKindLessThanOrEqual:    llvm.IntSLE,
		syntax.ExprKindGreaterThanOrEqual: llvm.IntSGE,
	}
	return e.b.CreateZExt(e.b.CreateICmp(predicates[x.Kind()], order, e.const64(0), ""), e.p.i64, ""), nil
}

// emitPointerArithmetic offsets a pointer by integers, each counting elements
// of what the pointer points at.
func (e *funcEmitter) emitPointerArithmetic(x *syntax.ExprNAry) (llvm.Value, error) {
	return e.pointerArithmetic(x, false)
}

// pointerArithmetic is emitPointerArithmetic, panicking first when checked
// and a pointer it offsets is null.
func (e *funcEmitter) pointerArithmetic(x *syntax.ExprNAry, checked bool) (llvm.Value, error) {
	if x.Kind() != syntax.ExprKindAdd && x.Kind() != syntax.ExprKindSubtract {
		return llvm.Value{}, unsupported("pointer %s", x.Kind())
	}
	pointee := syntax.StripConst(e.ground(x.ResolvedType())).(*syntax.TypeWrapped).Type
	if pointee.ContainsGenericArg() {
		return llvm.Value{}, unsupported("arithmetic on a pointer to %s", pointee)
	}
	stride := byteSize(pointee)
	var v llvm.Value
	for i, operand := range x.Exprs {
		word, err := e.emitExpr(operand)
		if err != nil {
			return llvm.Value{}, err
		}
		switch kind, err := valueKind(e.ground(operand.ResolvedType())); {
		case err != nil:
			return llvm.Value{}, err
		case kind == syntax.TypeKindInt || kind == syntax.TypeKindUint:
			word = e.b.CreateMul(word, e.const64(stride), "")
		case kind == syntax.TypeKindArray:
			word = e.b.CreatePtrToInt(word, e.p.i64, "")
		case kind != syntax.TypeKindPointer:
			return llvm.Value{}, unsupported("offsetting a pointer by %s", e.ground(operand.ResolvedType()))
		case checked:
			e.nullCheck(word, panicNullDereference)
		}
		switch {
		case i == 0:
			v = word
		case x.Kind() == syntax.ExprKindAdd:
			v = e.b.CreateAdd(v, word, "")
		default:
			v = e.b.CreateSub(v, word, "")
		}
	}
	return v, nil
}

// emitConcat joins every operand into one string, left to right.
func (e *funcEmitter) emitConcat(x *syntax.ExprNAry) (llvm.Value, error) {
	v, err := e.emitExprAs(x.Exprs[0], e.ground(x.ResolvedType()))
	if err != nil {
		return llvm.Value{}, err
	}
	for _, operand := range x.Exprs[1:] {
		right, err := e.emitExprAs(operand, e.ground(x.ResolvedType()))
		if err != nil {
			return llvm.Value{}, err
		}
		joined := e.call("zamrt_concat", e.thread,
			e.b.CreateIntToPtr(v, e.p.ptr, ""), e.b.CreateIntToPtr(right, e.p.ptr, ""))
		v = e.keep(e.b.CreatePtrToInt(joined, e.p.i64, ""), e.ground(x.ResolvedType()))
	}
	return v, nil
}

func (e *funcEmitter) emitBinary(x *syntax.ExprNAry) (llvm.Value, error) {
	typ := e.ground(x.ResolvedType())
	if kind, err := valueKind(typ); err == nil && kind == syntax.TypeKindPointer {
		return e.emitPointerArithmetic(x)
	}
	if kind, err := valueKind(typ); err == nil && kind == syntax.TypeKindString && x.Kind() == syntax.ExprKindAdd {
		return e.emitConcat(x)
	}
	kind, err := primitiveKind(typ)
	if err != nil {
		return llvm.Value{}, err
	}
	v, err := e.emitExprAs(x.Exprs[0], typ)
	if err != nil {
		return llvm.Value{}, err
	}
	for _, operand := range x.Exprs[1:] {
		right, err := e.emitExprAs(operand, typ)
		if err != nil {
			return llvm.Value{}, err
		}
		if v, err = e.binaryOp(x.Kind(), kind, v, right); err != nil {
			return llvm.Value{}, err
		}
	}
	return v, nil
}

// positOperators are the posit runtime functions behind each operator.
var positOperators = map[syntax.ExprKind]string{
	syntax.ExprKindAdd:      "zam_posit_add",
	syntax.ExprKindSubtract: "zam_posit_sub",
	syntax.ExprKindMultiply: "zam_posit_mul",
	syntax.ExprKindDivide:   "zam_posit_div",
}

// positOne is the posit 1.
const positOne = 1 << 62

// binaryOp applies the binary operator of kind op to left and right, words of
// the primitive kind.
func (e *funcEmitter) binaryOp(op syntax.ExprKind, kind syntax.TypeKind, left, right llvm.Value) (llvm.Value, error) {
	switch kind {
	case syntax.TypeKindPosit:
		name, ok := positOperators[op]
		if !ok {
			return llvm.Value{}, unsupported("posit %s", op)
		}
		return e.call(name, left, right), nil
	case syntax.TypeKindFloat:
		l, r := e.toFloat(left), e.toFloat(right)
		switch op {
		case syntax.ExprKindAdd:
			return e.fromFloat(e.b.CreateFAdd(l, r, "")), nil
		case syntax.ExprKindSubtract:
			return e.fromFloat(e.b.CreateFSub(l, r, "")), nil
		case syntax.ExprKindMultiply:
			return e.fromFloat(e.b.CreateFMul(l, r, "")), nil
		case syntax.ExprKindDivide:
			return e.fromFloat(e.b.CreateFDiv(l, r, "")), nil
		}
		return llvm.Value{}, unsupported("float %s", op)
	}

	var v llvm.Value
	switch op {
	case syntax.ExprKindAdd:
		v = e.b.CreateAdd(left, right, "")
	case syntax.ExprKindSubtract:
		v = e.b.CreateSub(left, right, "")
	case syntax.ExprKindMultiply:
		v = e.b.CreateMul(left, right, "")
	case syntax.ExprKindDivide, syntax.ExprKindModulus:
		v = e.emitDivision(left, right, signed(kind), op == syntax.ExprKindDivide)
	case syntax.ExprKindBinaryAND:
		v = e.b.CreateAnd(left, right, "")
	case syntax.ExprKindBinaryOR:
		v = e.b.CreateOr(left, right, "")
	case syntax.ExprKindBinaryXOR:
		v = e.b.CreateXor(left, right, "")
	case syntax.ExprKindBinaryShiftLeft, syntax.ExprKindBinaryShiftRight, syntax.ExprKindBinaryShiftRightRight:
		v = e.shift(op, kind, left, right)
	}
	return e.narrow(v, kind), nil
}

// shift shifts left by count as op says, or, by a negative int count, the
// other way by its magnitude: a right shift for a left one, and a left shift
// for either right one. The magnitude is kept to the word.
func (e *funcEmitter) shift(op syntax.ExprKind, kind syntax.TypeKind, left, count llvm.Value) llvm.Value {
	by := func(op syntax.ExprKind, count llvm.Value) llvm.Value {
		count = e.b.CreateAnd(count, e.const64(63), "")
		switch op {
		case syntax.ExprKindBinaryShiftLeft:
			return e.b.CreateShl(left, count, "")
		case syntax.ExprKindBinaryShiftRight:
			return e.b.CreateLShr(left, count, "")
		}
		return e.b.CreateAShr(left, count, "")
	}
	forward := by(op, count)
	if !signed(kind) {
		return forward
	}
	back := syntax.ExprKindBinaryShiftLeft
	if op == syntax.ExprKindBinaryShiftLeft {
		back = syntax.ExprKindBinaryShiftRight
	}
	backward := by(back, e.b.CreateSub(e.const64(0), count, ""))
	negative := e.b.CreateICmp(llvm.IntSLT, count, e.const64(0), "")
	return e.b.CreateSelect(negative, backward, forward, "")
}

// emitDivision divides or takes the remainder, panicking on a zero divisor.
// The most negative integer over -1 answers itself and a zero remainder.
func (e *funcEmitter) emitDivision(left, right llvm.Value, isSigned, quotient bool) llvm.Value {
	zero := e.ctx.AddBasicBlock(e.fn, "div.zero")
	ok := e.ctx.AddBasicBlock(e.fn, "div.ok")
	e.b.CreateCondBr(e.b.CreateICmp(llvm.IntEQ, right, e.const64(0), ""), zero, ok)
	e.b.SetInsertPointAtEnd(zero)
	e.call("zamrt_panic", e.thread, llvm.ConstInt(e.p.i32, 0, false))
	e.b.CreateUnreachable()
	e.b.SetInsertPointAtEnd(ok)

	if !isSigned {
		if quotient {
			return e.b.CreateUDiv(left, right, "")
		}
		return e.b.CreateURem(left, right, "")
	}

	minInt := e.const64(1 << 63)
	overflows := e.b.CreateAnd(
		e.b.CreateICmp(llvm.IntEQ, left, minInt, ""),
		e.b.CreateICmp(llvm.IntEQ, right, e.const64(^uint64(0)), ""), "")
	// Dividing by one instead leaves that answer, and keeps the instruction
	// from trapping.
	divisor := e.b.CreateSelect(overflows, e.const64(1), right, "")
	if quotient {
		return e.b.CreateSDiv(left, divisor, "")
	}
	return e.b.CreateSRem(left, divisor, "")
}

// staticCallee is the static method callee names, where it names one
// directly, and the type arguments it is named with.
func staticCallee(callee syntax.Expr) (*syntax.FieldMethod, []syntax.Type, bool) {
	t, ok := callee.(*syntax.ExprType)
	if !ok || t.Field == "" {
		return nil, nil, false
	}
	resolvable, ok := t.Type.(syntax.TypeResolvable)
	if !ok {
		return nil, nil, false
	}
	field, ok := resolvable.Decl().GetStaticField(t.Field)
	if !ok {
		return nil, nil, false
	}
	method, ok := field.(*syntax.FieldMethod)
	if !ok || (method.Generics() == nil) != (len(t.TypeArgs) == 0) {
		return nil, nil, false
	}
	return method, t.TypeArgs, true
}

// emitCall calls a function of type ft through call, over its arguments and
// result, which lie in rooted slots of the caller's frame so nothing they hold
// is collected while the call runs.
func (e *funcEmitter) emitCall(arguments []syntax.Expr, ft *syntax.TypeFunction, receiver *llvmReceiver, call func(args, result llvm.Value)) (llvm.Value, error) {
	if ft == nil {
		return llvm.Value{}, unsupported("a call of an unknown type")
	}
	spawn := e.armed
	e.armed = nil
	params := ft.ParameterTypes
	var element syntax.Type
	fixed := len(params)
	if fixed > 0 {
		if el, ok := syntax.VariadicElement(params[fixed-1]); ok {
			element, fixed = el, fixed-1
		}
	}
	if (element == nil && len(arguments) != len(params)) || len(arguments) < fixed {
		return llvm.Value{}, fmt.Errorf("%d arguments to a function of %d parameters", len(arguments), len(params))
	}
	if element != nil {
		params = append(params[:fixed:fixed], syntax.ParameterBindingType(params[fixed]))
	}

	receiverSlot := uint64(0)
	if receiver != nil {
		receiverSlot = e.declareLocal("", receiver.typ)
	}
	argSlots := make([]uint64, len(params))
	for i, param := range params {
		if _, err := valueKind(param); err != nil {
			return llvm.Value{}, err
		}
		argSlots[i] = e.declareLocal("", param)
	}
	result := llvm.ConstNull(e.p.ptr)
	// A spawned call's pack lies right after its arguments, so that the
	// thread's frame holds it rooted as the caller's would.
	var reserved *uint64
	packed := 0
	if spawn != nil && element != nil && packs(arguments[fixed:]) && rooted(element) {
		if stackWords(element)*8 != byteSize(element) {
			return llvm.Value{}, unsupported("a pack of %s", element)
		}
		first := e.declareLocal("", element)
		for range arguments[fixed+1:] {
			e.declareLocal("", element)
		}
		reserved, packed = &first, len(arguments[fixed:])
	}
	if !isVoid(ft.ReturnType) {
		if _, err := valueKind(ft.ReturnType); err != nil {
			return llvm.Value{}, err
		}
		result = e.slot(e.declareLocal("", ft.ReturnType))
	}
	for i, param := range params[:fixed] {
		v, err := e.emitExprAs(arguments[i], param)
		if err != nil {
			return llvm.Value{}, err
		}
		e.store(e.slotPlace(argSlots[i], param), v)
	}
	if element != nil {
		pack, err := e.emitPack(arguments[fixed:], element, reserved)
		if err != nil {
			return llvm.Value{}, err
		}
		e.b.CreateStore(pack, e.slot(argSlots[fixed]))
	}

	args := llvm.ConstNull(e.p.ptr)
	if receiver != nil {
		e.b.CreateStore(receiver.value, e.slot(receiverSlot))
		args = e.slot(receiverSlot)
	} else if len(argSlots) > 0 {
		args = e.slot(argSlots[0])
	}
	if spawn != nil {
		e.spawning = spawn
		spawn.info = e.spawnInfo(ft, receiver, params, element, packed)
		if !isVoid(ft.ReturnType) {
			spawn.result = ft.ReturnType
		}
	}
	call(args, result)
	e.spawning = nil
	if isVoid(ft.ReturnType) {
		return e.const64(0), nil
	}
	if isAggregate(ft.ReturnType) {
		return result, nil
	}
	return e.b.CreateLoad(e.p.i64, result, ""), nil
}

// emitPack lays the arguments a variadic parameter takes out back to back in
// the caller's frame, rooted where they are references, and answers where
// they begin, or null for none. A lone spread passes on the run it spreads.
func (e *funcEmitter) emitPack(rest []syntax.Expr, element syntax.Type, reserved *uint64) (llvm.Value, error) {
	if len(rest) == 1 {
		if inner := spreadInner(rest[0]); inner != nil {
			if kind, err := valueKind(e.ground(inner.ResolvedType())); err != nil || kind != syntax.TypeKindPointer {
				return llvm.Value{}, unsupported("spreading %s", inner.ResolvedType())
			}
			return e.emitExpr(inner)
		}
	}
	if len(rest) == 0 {
		return e.const64(0), nil
	}
	if _, err := valueKind(element); err != nil {
		return llvm.Value{}, err
	}
	stride := byteSize(element)
	var first uint64
	if rooted(element) {
		if stackWords(element)*8 != stride {
			return llvm.Value{}, unsupported("a pack of %s", element)
		}
		if reserved != nil {
			first = *reserved
		} else {
			first = e.declareLocal("", element)
			for range rest[1:] {
				e.declareLocal("", element)
			}
		}
	} else {
		first = e.reserve((uint64(len(rest))*stride + 7) / 8)
	}
	base := e.slot(first)
	for i, argument := range rest {
		v, err := e.emitExprAs(argument, element)
		if err != nil {
			return llvm.Value{}, err
		}
		address := e.b.CreateInBoundsGEP(e.ctx.Int8Type(), base, []llvm.Value{e.const64(uint64(i) * stride)}, "")
		e.store(llvmPlace{address: address, typ: element, width: stride}, v)
	}
	// The callee may keep or free the run, so it is copied to the heap; the
	// frame's copy keeps what it holds rooted until the call returns.
	bytes := uint64(len(rest)) * stride
	run := e.call("zamrt_new_run", e.thread, e.p.typeID(element), e.const64(uint64(len(rest))))
	e.copyBytes(run, base, bytes)
	if rooted(element) {
		e.call("zamrt_record_write", e.thread, run, e.const64(bytes))
	}
	return e.b.CreatePtrToInt(run, e.p.i64, ""), nil
}

// copyWords copies words words from src to dst.
func (e *funcEmitter) copyWords(dst, src llvm.Value, words uint64) {
	e.copyBytes(dst, src, words*8)
}

// emitUnpack declares one local per element of a tuple and copies each
// element into its local.
func (e *funcEmitter) emitUnpack(s *syntax.StmtUnpack) error {
	tuple, ok := syntax.StripConst(e.ground(s.Expr.ResolvedType())).(*syntax.TypeTuple)
	if !ok || len(tuple.Types) != len(s.Variables) {
		return unsupported("unpacking %s", e.ground(s.Expr.ResolvedType()))
	}
	if _, err := valueKind(tuple); err != nil {
		return err
	}
	v, err := e.emitExpr(s.Expr)
	if err != nil {
		return err
	}
	_, _, offsets := tupleLayout(tuple)
	values := make([]llvm.Value, len(s.Variables))
	for i, element := range tuple.Types {
		address := e.b.CreateGEP(e.ctx.Int8Type(), v, []llvm.Value{e.const64(offsets[i])}, "")
		value := e.load(llvmPlace{address: address, typ: element, width: byteSize(element)})
		if values[i], err = e.convert(value, element, e.ground(s.Variables[i].Type)); err != nil {
			return err
		}
	}
	for i, variable := range s.Variables {
		if variable.Name() == syntax.BlankName {
			continue
		}
		typ := e.ground(variable.Type)
		e.store(e.slotPlace(e.declareLocal(variable.Name(), typ), typ), values[i])
	}
	return nil
}

// emitTuple builds a tuple of type tuple in a rooted slot of its own from the
// literal x, converting each element to the tuple's, and answers its address.
func (e *funcEmitter) emitTuple(x *syntax.ExprNAry, tuple *syntax.TypeTuple) (llvm.Value, error) {
	if len(x.Exprs) != len(tuple.Types) {
		return llvm.Value{}, unsupported("a tuple of %d elements as %s", len(x.Exprs), tuple)
	}
	if _, err := valueKind(tuple); err != nil {
		return llvm.Value{}, err
	}
	values := make([]llvm.Value, len(x.Exprs))
	for i, element := range x.Exprs {
		v, err := e.emitExprAs(element, tuple.Types[i])
		if err != nil {
			return llvm.Value{}, err
		}
		values[i] = v
	}
	base := e.slot(e.declareLocal("", tuple))
	_, _, offsets := tupleLayout(tuple)
	for i, element := range tuple.Types {
		address := e.b.CreateInBoundsGEP(e.ctx.Int8Type(), base, []llvm.Value{e.const64(offsets[i])}, "")
		e.store(llvmPlace{address: address, typ: element, width: byteSize(element)}, values[i])
	}
	return base, nil
}

// llvmReceiver is the object a method is called on, and its type.
type llvmReceiver struct {
	value llvm.Value
	typ   syntax.Type
}

// methodCallee is the instance method callee selects and the object it is
// selected on, nil for the receiver of the method being compiled.
func (e *funcEmitter) methodCallee(callee syntax.Expr) (syntax.Expr, syntax.Decl, *syntax.FieldMethod, bool) {
	if object, name, ok := selection(callee); ok {
		resolvable, ok := syntax.SelectionTarget(e.ground(object.ResolvedType()))
		if !ok || resolvable.Decl() == nil {
			return nil, nil, nil, false
		}
		decl := resolvable.Decl()
		field, ok := decl.GetInstanceField(name)
		if !ok {
			return nil, nil, nil, false
		}
		method, ok := field.(*syntax.FieldMethod)
		if !ok {
			return nil, nil, nil, false
		}
		return object, decl, method, true
	}
	symbol, ok := callee.(*syntax.ExprSymbol)
	if !ok || symbol.Kind() != syntax.ExprKindIdentifier {
		return nil, nil, nil, false
	}
	name := symbol.Start().Value.(string)
	if _, ok := e.lookup(name); ok {
		return nil, nil, nil, false
	}
	if _, ok := e.lookup(thisVariableName); !ok {
		return nil, nil, nil, false
	}
	method, ok := e.decl.GetInstanceMethod(name)
	return nil, e.decl, method, ok
}

// emitMethodCall calls method on object: directly for a class, and through
// the class of the object for an abstract or an interface.
func (e *funcEmitter) emitMethodCall(x *syntax.ExprNAry, object syntax.Expr, decl syntax.Decl, method *syntax.FieldMethod) (llvm.Value, error) {
	receiver, ft, entryOf, err := e.methodTarget(object, decl, method)
	if err != nil {
		return llvm.Value{}, err
	}
	return e.emitCall(x.Exprs[1:], ft, &receiver, func(args, result llvm.Value) {
		e.invoke(entryOf(), llvm.ConstNull(e.p.ptr), args, result)
	})
}

// emitMethodValue is method selected on object as a function value: a
// closure binding the method's entry to the receiver.
func (e *funcEmitter) emitMethodValue(x syntax.Expr, object syntax.Expr, decl syntax.Decl, method *syntax.FieldMethod) (llvm.Value, error) {
	if object != nil {
		if _, ok := objectDecl(e.ground(object.ResolvedType())); !ok {
			return llvm.Value{}, unsupported("a method of %s as a value", e.ground(object.ResolvedType()))
		}
	}
	ft, ok := syntax.StripConst(e.ground(x.ResolvedType())).(*syntax.TypeFunction)
	if !ok {
		return llvm.Value{}, unsupported("a method value of type %s", x.ResolvedType())
	}
	receiver, _, entryOf, err := e.methodTarget(object, decl, method)
	if err != nil {
		return llvm.Value{}, err
	}
	e.nullCheck(receiver.value, panicNullMember)
	closure := e.call("zamrt_new_bound_closure", e.thread, e.p.typeID(ft), e.p.typeID(receiver.typ),
		receiver.value, entryOf())
	return e.keep(e.b.CreatePtrToInt(closure, e.p.i64, ""), ft), nil
}

// methodTarget evaluates the receiver method is called on and answers it, the
// method's type, and what finds the entry to call: the method's own for a
// class, and the class of the receiver's for an abstract or an interface.
func (e *funcEmitter) methodTarget(object syntax.Expr, decl syntax.Decl, method *syntax.FieldMethod) (llvmReceiver, *syntax.TypeFunction, func() llvm.Value, error) {
	var receiver llvmReceiver
	fail := func(err error) (llvmReceiver, *syntax.TypeFunction, func() llvm.Value, error) {
		return llvmReceiver{}, nil, nil, err
	}
	if method.Generics() != nil {
		return fail(unsupported("generic methods"))
	}
	if err := supportedObject(decl); err != nil {
		return fail(err)
	}
	if object == nil {
		this, _ := e.lookup(thisVariableName)
		receiver = llvmReceiver{value: e.b.CreateLoad(e.p.i64, e.slot(this.slot), ""), typ: this.typ}
	} else if kind, err := valueKind(e.ground(object.ResolvedType())); err == nil && (kind == kindStruct || kind == kindInstance || kind == kindMemberEnum) {
		v, err := e.emitExpr(object)
		if err != nil {
			return fail(err)
		}
		receiver = llvmReceiver{
			value: e.b.CreatePtrToInt(v, e.p.i64, ""),
			typ:   syntax.NewWrappedType(syntax.TypeKindPointer, e.ground(object.ResolvedType())),
		}
	} else if kind, err := valueKind(e.ground(object.ResolvedType())); err == nil && kind == kindEnum {
		// A value with no address of its own is given one for the call.
		address := llvm.Value{}
		if pl, err := e.place(object); err == nil {
			address = pl.address
		} else {
			v, err := e.emitExpr(object)
			if err != nil {
				return fail(err)
			}
			address = e.spill(v)
		}
		receiver = llvmReceiver{
			value: e.b.CreatePtrToInt(address, e.p.i64, ""),
			typ:   syntax.NewWrappedType(syntax.TypeKindPointer, e.ground(object.ResolvedType())),
		}
	} else {
		if _, ok := objectDecl(e.ground(object.ResolvedType())); !ok {
			if w, isPointer := syntax.StripConst(e.ground(object.ResolvedType())).(*syntax.TypeWrapped); !isPointer || w.Kind() != syntax.TypeKindPointer {
				return fail(unsupported("methods of %s", e.ground(object.ResolvedType())))
			} else if _, isStruct := structDecl(w.Type); !isStruct {
				return fail(unsupported("methods of %s", e.ground(object.ResolvedType())))
			}
		}
		v, err := e.emitExpr(object)
		if err != nil {
			return fail(err)
		}
		receiver = llvmReceiver{value: v, typ: e.ground(object.ResolvedType())}
	}
	ft, ok := method.FieldType().(*syntax.TypeFunction)
	if !ok {
		return fail(unsupported("method %s of type %s", method.Name(), method.FieldType()))
	}

	origin, ok := originMethod(decl, method.Name())
	if !ok {
		return fail(fmt.Errorf("no method %q", method.Name()))
	}
	index := uint64(*origin.Index())
	entryOf := func() llvm.Value {
		pointer := func() llvm.Value { return e.b.CreateIntToPtr(receiver.value, e.p.ptr, "") }
		switch decl.(type) {
		case *syntax.DeclInterface, *syntax.DeclConcreteInterface:
			a, _, _ := e.p.typeInstance(decl.(syntax.DeclTypeable).DeclType()).packed()
			return e.call("zamrt_interface_entry", e.thread, pointer(),
				llvm.ConstInt(e.p.i32, a>>32, false), llvm.ConstInt(e.p.i32, index, false))
		case *syntax.DeclAbstract, *syntax.DeclConcreteAbstract:
			return e.call("zamrt_virtual_entry", e.thread, pointer(), e.const64(index))
		}
		return e.p.methodFunction(origin, decl)
	}
	return receiver, ft, entryOf, nil
}

// emitNew allocates a class instance and runs its initializer over it.
func (e *funcEmitter) emitNew(x *syntax.ExprNew) (llvm.Value, error) {
	element := e.ground(x.Type)
	pointer := syntax.NewWrappedType(syntax.TypeKindPointer, element)
	if x.Tail != nil {
		return e.emitNewTailed(x, element, pointer)
	}
	if x.Size != nil {
		return e.emitNewRun(x, pointer)
	}
	if _, ok := structDecl(element); ok {
		if _, err := valueKind(element); err != nil {
			return llvm.Value{}, err
		}
		object := e.call("zamrt_new_of", e.thread, e.p.typeID(element), e.const64(1))
		if err := e.initializeStruct(object, element); err != nil {
			return llvm.Value{}, err
		}
		return e.b.CreatePtrToInt(object, e.p.i64, ""), nil
	}
	d, ok := objectDecl(pointer)
	if !ok {
		return llvm.Value{}, unsupported("new %s", x.Type)
	}
	switch d.(type) {
	case *syntax.DeclClass, *syntax.DeclConcreteClass:
	default:
		return llvm.Value{}, unsupported("new %s", x.Type)
	}
	if err := supportedObject(d); err != nil {
		return llvm.Value{}, err
	}
	object := e.call("zamrt_new_of", e.thread, e.p.typeID(element), e.const64(1))
	v := e.keep(e.b.CreatePtrToInt(object, e.p.i64, ""), pointer)

	init, err := e.p.initializerFor(d)
	if err != nil {
		return llvm.Value{}, err
	}
	if !init.IsNil() {
		args := e.slot(e.declareLocal("", pointer))
		e.b.CreateStore(v, args)
		e.b.CreateCall(e.p.entryType, init, []llvm.Value{e.thread, llvm.ConstNull(e.p.ptr), args, llvm.ConstNull(e.p.ptr)}, "")
	}
	return v, nil
}

// emitNewTailed allocates values of a tailed type whose tail length new is
// told rather than the type, a run of them when new is told a count.
func (e *funcEmitter) emitNewTailed(x *syntax.ExprNew, element, pointer syntax.Type) (llvm.Value, error) {
	d, ok := structDecl(element)
	if !ok {
		if d, ok = objectDecl(pointer); !ok {
			return llvm.Value{}, unsupported("new %s", x.Type)
		}
	}
	if err := supportedObject(d); err != nil {
		return llvm.Value{}, err
	}
	init, err := e.p.initializerFor(d)
	if err != nil {
		return llvm.Value{}, err
	}
	if !init.IsNil() {
		return llvm.Value{}, unsupported("a tailed %s, which initializes members", element)
	}
	count, err := e.emitExpr(x.Size)
	if err != nil {
		return llvm.Value{}, err
	}
	tail, err := e.emitExprAs(x.Tail, syntax.TypeUint)
	if err != nil {
		return llvm.Value{}, err
	}
	object := e.call("zamrt_new_tailed", e.thread, e.p.typeID(element), count, tail)
	return e.keep(e.b.CreatePtrToInt(object, e.p.i64, ""), pointer), nil
}

// isTailed reports whether d is a tailed declaration, whose instances carry
// their tail's length in their header.
func isTailed(d syntax.Decl) bool {
	t, ok := d.(interface{ IsTailed() bool })
	return ok && t.IsTailed()
}

// initializeStruct runs the initializer of the struct t at address, which is
// zeroed; it does nothing for anything else, or a struct with nothing to
// initialize.
func (e *funcEmitter) initializeStruct(address llvm.Value, t syntax.Type) error {
	d, ok := structDecl(t)
	if !ok {
		return nil
	}
	init, err := e.p.initializerFor(d)
	if err != nil || init.IsNil() {
		return err
	}
	args := e.slot(e.declareLocal("", syntax.NewWrappedType(syntax.TypeKindPointer, t)))
	e.b.CreateStore(e.b.CreatePtrToInt(address, e.p.i64, ""), args)
	e.b.CreateCall(e.p.entryType, init, []llvm.Value{e.thread, llvm.ConstNull(e.p.ptr), args, llvm.ConstNull(e.p.ptr)}, "")
	return nil
}

// initializeInstance stamps the zeroed class instance t at address, standing
// in a frame, and runs its initializer over it.
func (e *funcEmitter) initializeInstance(address llvm.Value, t syntax.Type, d syntax.Decl) error {
	e.call("zamrt_stamp", e.thread, e.p.typeID(t), address)
	init, err := e.p.initializerFor(d)
	if err != nil || init.IsNil() {
		return err
	}
	args := e.slot(e.declareLocal("", syntax.NewWrappedType(syntax.TypeKindPointer, t)))
	e.b.CreateStore(e.b.CreatePtrToInt(address, e.p.i64, ""), args)
	e.b.CreateCall(e.p.entryType, init, []llvm.Value{e.thread, llvm.ConstNull(e.p.ptr), args, llvm.ConstNull(e.p.ptr)}, "")
	return nil
}

// emitNewRun allocates a run of values the program releases with free.
func (e *funcEmitter) emitNewRun(x *syntax.ExprNew, pointer syntax.Type) (llvm.Value, error) {
	element := syntax.StripConst(pointer).(*syntax.TypeWrapped).Type
	if _, err := valueKind(element); err != nil {
		return llvm.Value{}, err
	}
	if d, ok := structDecl(element); ok {
		init, err := e.p.initializerFor(d)
		if err != nil {
			return llvm.Value{}, err
		}
		if !init.IsNil() {
			return llvm.Value{}, unsupported("a run of %s, which initializes members", element)
		}
	}
	if d, ok := objectDecl(pointer); ok {
		if err := supportedObject(d); err != nil {
			return llvm.Value{}, err
		}
		init, err := e.p.initializerFor(d)
		if err != nil {
			return llvm.Value{}, err
		}
		if !init.IsNil() {
			return llvm.Value{}, unsupported("a run of %s, which initializes members", element)
		}
	}
	count, err := e.emitExpr(x.Size)
	if err != nil {
		return llvm.Value{}, err
	}
	run := e.call("zamrt_new_run", e.thread, e.p.typeID(element), count)
	return e.keep(e.b.CreatePtrToInt(run, e.p.i64, ""), pointer), nil
}

// initializerFor is the compiled routine that sets the members of a fresh
// instance of d that have initializers, compiling it the first time; nil
// when no member has one.
func (p *llvmProgram) initializerFor(d syntax.Decl) (llvm.Value, error) {
	p.live[d] = true
	if fn, ok := p.initializers[d]; ok {
		return fn, nil
	}
	var members []*syntax.FieldMember
	for _, member := range instanceFields[*syntax.FieldMember](d) {
		if member.Expr != nil {
			members = append(members, member)
		}
	}
	if len(members) == 0 {
		p.initializers[d] = llvm.Value{}
		return llvm.Value{}, nil
	}

	name := qualifiedName(d)
	if c, ok := d.(syntax.DeclConcrete); ok {
		name += "[" + c.DeclType().String() + "]"
	}
	fn := p.newFunction(name + ".init")
	p.initializers[d] = fn
	e := p.newFunc(fn, d)
	e.named(qualifiedName(d)+".<init>", d.Start())
	if c, ok := d.(syntax.DeclConcrete); ok {
		e.mapper = c.MapType
	}
	return p.compiles(e, func() error {
		this := e.declareLocal(thisVariableName, receiverType(d))
		e.b.CreateStore(e.b.CreateLoad(p.i64, e.fn.Param(2), ""), e.slot(this))
		e.call("zamrt_safepoint", e.thread)
		for _, member := range members {
			e.decl = member.OuterDecl()
			if _, ok := d.(syntax.DeclConcrete); ok {
				e.decl = d
			}
			pl, err := e.memberPlace(e.b.CreateLoad(p.i64, e.slot(this), ""), receiverType(d), member.Name())
			if err != nil {
				return err
			}
			v, err := e.emitExprAs(member.Expr, member.FieldType())
			if err != nil {
				return err
			}
			e.store(pl, v)
		}
		return nil
	})
}

// emitTypeof is the type of a value: what an any holds, the class of the
// instance a pointer to an abstract or interface points at, and otherwise the
// type it was written with.
func (e *funcEmitter) emitTypeof(x *syntax.ExprNAry) (llvm.Value, error) {
	operand := x.Exprs[0]
	t := e.ground(operand.ResolvedType())
	v, err := e.emitExpr(operand)
	if err != nil {
		return llvm.Value{}, err
	}
	if syntax.StripConst(t).Kind() == syntax.TypeKindAny {
		out := e.slot(e.declareLocal("", syntax.TypeType))
		e.call("zamrt_any_type_words", e.thread, e.b.CreateIntToPtr(v, e.p.ptr, ""), out)
		return out, nil
	}
	if !virtuallyTyped(t) {
		return e.p.typeID(t), nil
	}
	out := e.slot(e.declareLocal("", syntax.TypeType))
	e.copyBytes(out, e.p.typeID(t), byteSize(syntax.TypeType))
	known := e.ctx.AddBasicBlock(e.fn, "typeof.instance")
	done := e.ctx.AddBasicBlock(e.fn, "typeof.done")
	e.b.CreateCondBr(e.b.CreateICmp(llvm.IntNE, v, e.const64(0), ""), known, done)
	e.b.SetInsertPointAtEnd(known)
	e.call("zamrt_type_of_words", e.thread, e.b.CreateIntToPtr(v, e.p.ptr, ""), out)
	e.b.CreateBr(done)
	e.b.SetInsertPointAtEnd(done)
	return out, nil
}

// emitSpaceship orders two values as -1, 0 or 1.
func (e *funcEmitter) emitSpaceship(x *syntax.ExprNAry) (llvm.Value, error) {
	typ := e.ground(x.Exprs[0].ResolvedType())
	kind, err := primitiveKind(typ)
	if err != nil {
		if _, err := valueKind(typ); err != nil {
			return llvm.Value{}, err
		}
		left, err := e.emitExpr(x.Exprs[0])
		if err != nil {
			return llvm.Value{}, err
		}
		right, err := e.emitExpr(x.Exprs[1])
		if err != nil {
			return llvm.Value{}, err
		}
		leftType, rightType := e.ground(x.Exprs[0].ResolvedType()), e.ground(x.Exprs[1].ResolvedType())
		return e.call("zamrt_cmp", e.thread, e.p.typeID(leftType), e.wordsOf(left, leftType),
			e.p.typeID(rightType), e.wordsOf(right, rightType)), nil
	}
	left, err := e.emitExprAs(x.Exprs[0], typ)
	if err != nil {
		return llvm.Value{}, err
	}
	right, err := e.emitExprAs(x.Exprs[1], typ)
	if err != nil {
		return llvm.Value{}, err
	}
	var greater, less llvm.Value
	switch {
	case kind == syntax.TypeKindFloat:
		greater = e.b.CreateFCmp(llvm.FloatOGT, e.toFloat(left), e.toFloat(right), "")
		less = e.b.CreateFCmp(llvm.FloatOLT, e.toFloat(left), e.toFloat(right), "")
	case signed(kind) || kind == syntax.TypeKindPosit:
		greater = e.b.CreateICmp(llvm.IntSGT, left, right, "")
		less = e.b.CreateICmp(llvm.IntSLT, left, right, "")
	default:
		greater = e.b.CreateICmp(llvm.IntUGT, left, right, "")
		less = e.b.CreateICmp(llvm.IntULT, left, right, "")
	}
	return e.b.CreateSub(e.b.CreateZExt(greater, e.p.i64, ""), e.b.CreateZExt(less, e.p.i64, ""), ""), nil
}

// emitGenericStaticMethodValue is a closure over one instantiation of a
// generic static method, named with its type arguments.
func (e *funcEmitter) emitGenericStaticMethodValue(x *syntax.ExprType) (llvm.Value, error) {
	resolvable, ok := x.Type.(syntax.TypeResolvable)
	if !ok {
		return llvm.Value{}, unsupported("statics of %s", x.Type)
	}
	field, ok := resolvable.Decl().GetStaticField(x.Field)
	method, isMethod := field.(*syntax.FieldMethod)
	if !ok || !isMethod || method.Generics() == nil {
		return llvm.Value{}, unsupported("type arguments to static %q", x.Field)
	}
	grounded := make([]syntax.Type, len(x.TypeArgs))
	for i, t := range x.TypeArgs {
		grounded[i] = e.ground(t)
	}
	concrete := e.p.concreteOf(method, grounded)
	if concrete == nil {
		return llvm.Value{}, fmt.Errorf("no instantiation of %s for %v", method.Name(), grounded)
	}
	closure := e.call("zamrt_new_function_closure", e.thread, e.p.typeID(concrete.FieldType()),
		e.p.concreteFunction(concrete), e.const64(0), e.const64(0))
	return e.keep(e.b.CreatePtrToInt(closure, e.p.i64, ""), concrete.FieldType()), nil
}

// emitStaticValue is the value of a static named through its declaration's
// type: a member's value, or the closure a static method is stored as.
func (e *funcEmitter) emitStaticValue(x *syntax.ExprType) (llvm.Value, error) {
	if x.Field == "" && len(x.TypeArgs) == 0 {
		// The runtime may make an instance of a type it is handed.
		t := e.ground(x.Type)
		if r, ok := syntax.StripConst(t).(syntax.TypeResolvable); ok && r.Decl() != nil {
			e.p.live[r.Decl()] = true
		}
		return e.p.typeID(t), nil
	}
	if x.Field != "" && len(x.TypeArgs) > 0 {
		return e.emitGenericStaticMethodValue(x)
	}
	if x.Field == "" || x.Concrete != nil {
		return llvm.Value{}, unsupported("type values")
	}
	resolvable, ok := x.Type.(syntax.TypeResolvable)
	if !ok {
		return llvm.Value{}, unsupported("statics of %s", x.Type)
	}
	field, ok := resolvable.Decl().GetStaticField(x.Field)
	if !ok {
		return llvm.Value{}, fmt.Errorf("no static %q", x.Field)
	}
	switch f := field.(type) {
	case *syntax.FieldMember:
		if _, err := valueKind(f.FieldType()); err != nil {
			return llvm.Value{}, err
		}
		return e.load(e.staticPlace(f)), nil
	case *syntax.FieldMethod:
		if f.Generics() != nil {
			return llvm.Value{}, unsupported("generic methods as values")
		}
		e.p.staticValue(f)
	case *syntax.FieldEnum:
		if stackWords(f.OuterDecl().(*syntax.DeclEnum).DeclType()) != 1 {
			return e.staticAddress(*f.Index()), nil
		}
	default:
		return llvm.Value{}, unsupported("statics of kind %T", field)
	}
	return e.b.CreateLoad(e.p.i64, e.staticAddress(*field.Index()), ""), nil
}

// emitLambda compiles a lambda into a function of its own and allocates a
// closure over it, holding a copy of each local it captures.
func (e *funcEmitter) emitLambda(x *syntax.ExprLambda) (llvm.Value, error) {
	names := x.Captures
	if _, ok := e.lookup(thisVariableName); ok && !slices.Contains(names, thisVariableName) {
		names = append(slices.Clip(names), thisVariableName)
	}
	captures := make([]llvmCapture, len(names))
	words := uint64(0)
	for i, name := range names {
		local, ok := e.lookup(name)
		if !ok {
			return llvm.Value{}, unsupported("capturing %q", name)
		}
		if _, err := valueKind(local.typ); err != nil {
			return llvm.Value{}, err
		}
		captures[i] = llvmCapture{name: name, typ: local.typ}
		words += typeIDWords + stackWords(local.typ)
	}

	e.p.lambdas++
	fn := e.p.newFunction(fmt.Sprintf("%s.lambda%d", e.fn.Name(), e.p.lambdas))
	inner := e.p.newFunc(fn, e.decl)
	inner.named(e.name+".lambda", x.Start())
	inner.mapper = e.mapper
	lambdaType, _ := e.ground(x.Type).(*syntax.TypeFunction)
	if lambdaType == nil {
		inner.b.Dispose()
		return llvm.Value{}, unsupported("a lambda of type %s", x.Type)
	}
	fn, err := e.p.compiles(inner, func() error {
		return inner.emitBody(lambdaType, nil, x.Parameters, captures, x.Body)
	})
	if err != nil {
		return llvm.Value{}, err
	}

	closure := e.call("zamrt_new_function_closure", e.thread, e.p.typeID(lambdaType), fn,
		e.const64(uint64(len(captures))), e.const64(words))
	entries := e.call("zamrt_closure_captures", closure)
	base := uint64(0)
	for _, capture := range captures {
		local, _ := e.lookup(capture.name)
		at := func(w uint64) llvm.Value {
			return e.b.CreateInBoundsGEP(e.p.i64, entries, []llvm.Value{e.const64(base + w)}, "")
		}
		e.copyWords(at(0), e.p.typeID(capture.typ), typeIDWords)
		e.copyWords(at(typeIDWords), e.slot(local.slot), stackWords(capture.typ))
		base += typeIDWords + stackWords(capture.typ)
	}
	return e.keep(e.b.CreatePtrToInt(closure, e.p.i64, ""), lambdaType), nil
}

// keep roots a fresh reference of type t in a slot of its own for the rest of
// the function, so that it outlives any collection the code using it reaches.
func (e *funcEmitter) keep(v llvm.Value, t syntax.Type) llvm.Value {
	if rooted(t) {
		e.b.CreateStore(v, e.slot(e.declareLocal("", t)))
	}
	return v
}

// emitNativeCall calls a native over its arguments, laid out as words.
func (e *funcEmitter) emitNativeCall(x *syntax.ExprNAry) (llvm.Value, error) {
	cast, ok := x.Exprs[0].(*syntax.ExprCast)
	if !ok {
		return llvm.Value{}, unsupported("a native called without a signature")
	}
	native, ok := cast.Expr.(*syntax.ExprNative)
	if !ok {
		return llvm.Value{}, unsupported("a native called through a cast of a cast")
	}
	ft, ok := cast.Type.(*syntax.TypeFunction)
	if !ok {
		return llvm.Value{}, unsupported("a native called as %s", cast.Type)
	}
	if intrinsic, ok := nativeIntrinsics[native.ID]; ok {
		return e.emitNativeIntrinsic(x, ft, intrinsic)
	}
	index, ok := e.p.natives[native.ID]
	if !ok {
		return llvm.Value{}, fmt.Errorf("native %q is not in the native table", native.ID)
	}
	if len(ft.ParameterTypes) != len(x.Exprs)-1 {
		return llvm.Value{}, unsupported("a native call with variadic arguments")
	}

	argSlots := make([]uint64, len(ft.ParameterTypes))
	argWords := uint64(0)
	for i, param := range ft.ParameterTypes {
		if _, err := valueKind(param); err != nil {
			return llvm.Value{}, err
		}
		argSlots[i] = e.declareLocal("", param)
		argWords += stackWords(param)
	}
	result := llvm.ConstNull(e.p.ptr)
	resultWords := uint64(0)
	if !isVoid(ft.ReturnType) {
		if _, err := valueKind(ft.ReturnType); err != nil {
			return llvm.Value{}, err
		}
		resultWords = stackWords(ft.ReturnType)
		result = e.slot(e.declareLocal("", ft.ReturnType))
	}
	for i, param := range ft.ParameterTypes {
		v, err := e.emitExprAs(x.Exprs[i+1], param)
		if err != nil {
			return llvm.Value{}, err
		}
		e.store(e.slotPlace(argSlots[i], param), v)
	}

	args := llvm.ConstNull(e.p.ptr)
	if len(argSlots) > 0 {
		args = e.slot(argSlots[0])
	}
	e.call("zamrt_call_native", e.thread, e.const64(index), args, e.const64(argWords), result, e.const64(resultWords))
	switch {
	case resultWords == 0:
		return e.const64(0), nil
	case isAggregate(ft.ReturnType):
		return result, nil
	}
	return e.b.CreateLoad(e.p.i64, result, ""), nil
}

// isVoid reports whether t is void.
func isVoid(t syntax.Type) bool {
	p, ok := syntax.StripConst(t).(*syntax.TypePrimitive)
	return ok && p.Kind() == syntax.TypeKindVoid
}
