package codegen

import (
	"fmt"
	"slices"

	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
	"github.com/zambozoo/zam/internal/compiler/2_codegen/llvm"
)

// nativeIntrinsic is a native codegen computes in place rather than calls: one
// over arity words, writing the word it answers through the pointer passed
// after them.
type nativeIntrinsic struct {
	arity int
	emit  func(e *funcEmitter, args []llvm.Value) llvm.Value
}

// nativeIntrinsics are the natives codegen computes in place, with an LLVM
// intrinsic or by calling the C library's math function directly, which the
// runtime has no entry for.
var nativeIntrinsics = map[string]nativeIntrinsic{
	"STD_BITS_ONES":           {1, intIntrinsic("llvm.ctpop.i64")},
	"STD_BITS_LEADING_ZEROS":  {1, countIntrinsic("llvm.ctlz.i64")},
	"STD_BITS_TRAILING_ZEROS": {1, countIntrinsic("llvm.cttz.i64")},
	"STD_BITS_REVERSE_BYTES":  {1, intIntrinsic("llvm.bswap.i64")},
	"STD_BITS_FROM_FLT":       {1, sameWord},
	"STD_BITS_TO_FLT":         {1, sameWord},
	"STD_BITS_FROM_PST":       {1, sameWord},
	"STD_BITS_TO_PST":         {1, sameWord},
	"STD_MATH_FLOOR":          {1, floatIntrinsic("llvm.floor.f64")},
	"STD_MATH_CEIL":           {1, floatIntrinsic("llvm.ceil.f64")},
	"STD_MATH_TRUNC":          {1, floatIntrinsic("llvm.trunc.f64")},
	"STD_MATH_ROUND":          {1, floatIntrinsic("llvm.round.f64")},
	"STD_MATH_SQRT":           {1, floatIntrinsic("llvm.sqrt.f64")},
	"STD_MATH_FMA":            {3, floatIntrinsic("llvm.fma.f64")},
	"STD_MATH_CBRT":           {1, floatIntrinsic("cbrt")},
	"STD_MATH_EXP":            {1, floatIntrinsic("exp")},
	"STD_MATH_EXP2":           {1, floatIntrinsic("exp2")},
	"STD_MATH_EXPM1":          {1, floatIntrinsic("expm1")},
	"STD_MATH_LOG":            {1, floatIntrinsic("log")},
	"STD_MATH_LOG2":           {1, floatIntrinsic("log2")},
	"STD_MATH_LOG10":          {1, floatIntrinsic("log10")},
	"STD_MATH_LOG1P":          {1, floatIntrinsic("log1p")},
	"STD_MATH_SIN":            {1, floatIntrinsic("sin")},
	"STD_MATH_COS":            {1, floatIntrinsic("cos")},
	"STD_MATH_TAN":            {1, floatIntrinsic("tan")},
	"STD_MATH_ASIN":           {1, floatIntrinsic("asin")},
	"STD_MATH_ACOS":           {1, floatIntrinsic("acos")},
	"STD_MATH_ATAN":           {1, floatIntrinsic("atan")},
	"STD_MATH_SINH":           {1, floatIntrinsic("sinh")},
	"STD_MATH_COSH":           {1, floatIntrinsic("cosh")},
	"STD_MATH_TANH":           {1, floatIntrinsic("tanh")},
	"STD_MATH_ASINH":          {1, floatIntrinsic("asinh")},
	"STD_MATH_ACOSH":          {1, floatIntrinsic("acosh")},
	"STD_MATH_ATANH":          {1, floatIntrinsic("atanh")},
	"STD_MATH_POW":            {2, floatIntrinsic("pow")},
	"STD_MATH_MOD":            {2, floatIntrinsic("fmod")},
	"STD_MATH_REMAINDER":      {2, floatIntrinsic("remainder")},
	"STD_MATH_HYPOT":          {2, floatIntrinsic("hypot")},
	"STD_MATH_ATAN2":          {2, floatIntrinsic("atan2")},
}

// sameWord answers its argument's word unchanged, which reinterprets a flt or
// pst as its bits or bits as one.
func sameWord(_ *funcEmitter, args []llvm.Value) llvm.Value {
	return args[0]
}

// intIntrinsic calls the LLVM intrinsic name over its word arguments.
func intIntrinsic(name string) func(*funcEmitter, []llvm.Value) llvm.Value {
	return func(e *funcEmitter, args []llvm.Value) llvm.Value {
		return e.call(e.p.intrinsic(name, e.p.i64, slices.Repeat([]llvm.Type{e.p.i64}, len(args))...), args...)
	}
}

// countIntrinsic calls the LLVM bit-counting intrinsic name over its word
// argument, defined as 64 for zero.
func countIntrinsic(name string) func(*funcEmitter, []llvm.Value) llvm.Value {
	return func(e *funcEmitter, args []llvm.Value) llvm.Value {
		f := e.p.intrinsic(name, e.p.i64, e.p.i64, e.p.i1)
		return e.call(f, args[0], llvm.ConstInt(e.p.i1, 0, false))
	}
}

// floatIntrinsic calls the LLVM intrinsic or C math function name over its
// arguments as flts.
func floatIntrinsic(name string) func(*funcEmitter, []llvm.Value) llvm.Value {
	return func(e *funcEmitter, args []llvm.Value) llvm.Value {
		floats := make([]llvm.Value, len(args))
		for i, arg := range args {
			floats[i] = e.toFloat(arg)
		}
		return e.fromFloat(e.call(e.p.intrinsic(name, e.p.f64, slices.Repeat([]llvm.Type{e.p.f64}, len(args))...), floats...))
	}
}

// intrinsic declares the LLVM intrinsic or C function name the first time it
// is asked for, and answers its name.
func (p *llvmProgram) intrinsic(name string, ret llvm.Type, params ...llvm.Type) string {
	if _, ok := p.runtime[name]; !ok {
		p.declare(name, ret, params...)
	}
	return name
}

// emitNativeIntrinsic computes the native intrinsic over x's arguments and
// writes what it answers through the pointer passed last.
func (e *funcEmitter) emitNativeIntrinsic(x *syntax.ExprNAry, ft *syntax.TypeFunction, intrinsic nativeIntrinsic) (llvm.Value, error) {
	if !isVoid(ft.ReturnType) || len(ft.ParameterTypes) != intrinsic.arity+1 || len(x.Exprs)-1 != len(ft.ParameterTypes) {
		return llvm.Value{}, unsupported("native %s as %s", x.Exprs[0], ft)
	}
	args := make([]llvm.Value, len(ft.ParameterTypes))
	for i, param := range ft.ParameterTypes {
		if _, err := valueKind(param); err != nil {
			return llvm.Value{}, err
		}
		v, err := e.emitExprAs(x.Exprs[i+1], param)
		if err != nil {
			return llvm.Value{}, err
		}
		args[i] = v
	}
	out := e.b.CreateIntToPtr(args[intrinsic.arity], e.p.ptr, "")
	e.b.CreateStore(intrinsic.emit(e, args[:intrinsic.arity]), out)
	return e.const64(0), nil
}

// enumIntrinsics are the natives codegen computes from the enum their first
// argument names, a type value type(E), once a generic E is grounded. Each writes
// what it answers through the pointer passed last:
//   - STD_ENUM_LEN, void(type, uint*): how many values the enum has
//   - STD_ENUM_ORDINAL, void(type, E, uint*): where the value stands among them
//   - STD_ENUM_VALUES, void(type, E*): every value, in order, into the buffer
//   - STD_ENUM_ADDRESS, void(type, E, E$**): the static the value is kept in
var enumIntrinsics = map[string]int{
	"STD_ENUM_LEN":     0,
	"STD_ENUM_ORDINAL": 1,
	"STD_ENUM_VALUES":  0,
	"STD_ENUM_ADDRESS": 1,
}

// emitEnumIntrinsic computes the enum intrinsic id over x's arguments.
func (e *funcEmitter) emitEnumIntrinsic(x *syntax.ExprNAry, ft *syntax.TypeFunction, id string) (llvm.Value, error) {
	arity := enumIntrinsics[id]
	if !isVoid(ft.ReturnType) || len(ft.ParameterTypes) != arity+2 || len(x.Exprs)-1 != len(ft.ParameterTypes) {
		return llvm.Value{}, unsupported("native %s as %s", id, ft)
	}
	named, ok := x.Exprs[1].(*syntax.ExprType)
	if !ok || named.Field != "" || len(named.TypeArgs) > 0 {
		return llvm.Value{}, fmt.Errorf("native %s must be passed an enum's type value first", id)
	}
	t := e.ground(named.Type)
	r, ok := syntax.StripConst(t).(syntax.TypeResolvable)
	if !ok {
		return llvm.Value{}, fmt.Errorf("native %s passed %s, which is not an enum", id, t)
	}
	enum, ok := r.Decl().(*syntax.DeclEnum)
	if !ok {
		return llvm.Value{}, fmt.Errorf("native %s passed %s, which is not an enum", id, t)
	}

	args := make([]llvm.Value, arity+1)
	for i, param := range ft.ParameterTypes[1:] {
		param = e.ground(param)
		if _, err := valueKind(param); err != nil {
			return llvm.Value{}, err
		}
		v, err := e.emitExprAs(x.Exprs[i+2], param)
		if err != nil {
			return llvm.Value{}, err
		}
		args[i] = v
	}
	out := e.b.CreateIntToPtr(args[arity], e.p.ptr, "")
	values := enumValues(enum)
	_, members := memberEnum(t)
	stride := uint64(1)
	if primitive := enum.Primitive(); primitive != nil {
		stride, _ = fieldByteInfo(primitive)
	}

	switch id {
	case "STD_ENUM_LEN":
		e.b.CreateStore(e.const64(uint64(len(values))), out)
	case "STD_ENUM_ORDINAL":
		e.b.CreateStore(e.enumOrdinal(args[0], members, stride), out)
	case "STD_ENUM_ADDRESS":
		if len(values) == 0 {
			return llvm.Value{}, unsupported("native %s of %s, which has no values", id, t)
		}
		slots := make([]llvm.Value, len(values))
		for i, value := range values {
			slots[i] = e.const64(uint64(*value.Index()))
		}
		table := llvm.AddGlobal(e.p.mod, llvm.ArrayType(e.p.i64, len(slots)), "")
		table.SetInitializer(llvm.ConstArray(e.p.i64, slots))
		table.SetGlobalConstant(true)
		table.SetLinkage(llvm.PrivateLinkage)
		at := e.b.CreateInBoundsGEP(e.p.i64, table, []llvm.Value{e.enumOrdinal(args[0], members, stride)}, "")
		address := e.call("zamrt_static", e.thread, e.b.CreateLoad(e.p.i64, at, ""))
		e.b.CreateStore(e.b.CreatePtrToInt(address, e.p.i64, ""), out)
	case "STD_ENUM_VALUES":
		width, _ := fieldByteInfo(t)
		if isAggregate(t) {
			width = byteSize(t)
		}
		for i, value := range values {
			at := e.b.CreateGEP(e.ctx.Int8Type(), out, []llvm.Value{e.const64(uint64(i) * byteSize(t))}, "")
			pl := llvmPlace{address: at, typ: t, width: width, heap: true}
			if members {
				e.store(pl, e.staticAddress(*value.Index()))
			} else {
				e.store(pl, e.const64(enumDiscriminant(value)))
			}
		}
	}
	return e.const64(0), nil
}

// enumOrdinal answers where the enum value v stands among its enum's values:
// its discriminant, the first word it points at when it has members, over
// the stride its backing values lie at.
func (e *funcEmitter) enumOrdinal(v llvm.Value, members bool, stride uint64) llvm.Value {
	if members {
		v = e.b.CreateLoad(e.p.i64, v, "")
	}
	return e.b.CreateUDiv(v, e.const64(stride), "")
}
