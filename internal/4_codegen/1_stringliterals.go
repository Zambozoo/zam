package codegen

import (
	"encoding/binary"
	"io"

	base "github.com/zambozoo/zam/internal/0_base"
	syntax "github.com/zambozoo/zam/internal/2_syntax"
)

type (
	// stringLiteral is a length-prefixed string as it is written anywhere in the
	// image: an 8-byte length followed by its bytes, padded up to a word
	// boundary. A native's name in the natives table is one of these and nothing
	// more, since nothing at run time holds a pointer to one.
	stringLiteral string

	// internedString is a string literal as it lies in the static region: a mark
	// word, which is what the program's pointer names, and then the
	// stringLiteral it stands in front of.
	//
	// The word is there because every markable value begins with one — see
	// ZamMarkWord in internal/5_build/runtime/src/instance.h — and a literal is pushed as its slot
	// address with nothing copied, so if a literal lacked the word it would be
	// the one string in the system that could not be asked what it was.
	//
	// The word is read, and by more than the collector: it is where a literal
	// says it is not an allocation, which is what stops a delete of one from
	// handing free a pointer into the static region. See staticStringMarkWord.
	internedString stringLiteral

	// stringLiteralVisitor walks every field body ahead of the static-field
	// layout and interns each string literal into the head of the static
	// region. Interning first means each literal's slot is known while
	// operations are emitted, and the static fields laid out after them need no
	// later fix-up.
	stringLiteralVisitor struct {
		reader *codegenReader
		// offset is the running byte offset of the literal region.
		offset uint64
	}
)

// The widths of the mark word fields, mirroring ZAM_MARK_STATE_BITS,
// ZAM_OBJECT_KIND_BITS and the contained and reserved fields of ZamMarkWord in
// internal/5_build/runtime/src/instance.h. The upvalue holds what is left of the word.
const (
	markStateBits      = 2
	markObjectKindBits = 3
	markContainedBits  = 1
	markReservedBits   = 2
)

// The object kinds the compiler lays down, mirroring ZamObjectKind. These three
// are all it names: the rest are allocations the runtime makes for itself, and a
// kind it never writes is a kind it has no bytes to write.
const (
	objectKindString = iota
	objectKindInstance
	objectKindTailedInstance
)

// staticMarkWord is the mark word every markable value laid into the static
// region carries: white, of the given kind, contained, and that far back to what
// holds it.
//
// White because the state is the collector's and the collector has not run.
// Contained because the bit is what says the slot is not an allocation — see
// ZamMarkWord, where it decides both whether the upvalue is a sweep-list link
// and whether the block may be freed — and freeing a static would release memory
// malloc never returned. The distance is zero for a value the region holds
// directly, contained-with-no-distance being how the same bit covers a value
// inside something and a value inside nothing.
//
// This is the one place the compiler spells out the runtime's mark bits, which is
// why it is pinned from the C side rather than only asserted here.
func staticMarkWord(kind, backDistance uint64) uint64 {
	const containedShift = markStateBits + markObjectKindBits
	const upvalueShift = containedShift + markContainedBits + markReservedBits
	return kind<<markStateBits | 1<<containedShift | backDistance<<upvalueShift
}

// staticStringMarkWord is the mark word every interned literal carries, which is
// the general rule with nothing to say: ZAM_OBJECT_STRING is zero and nothing
// contains a literal, so the contained bit is the whole of it.
var staticStringMarkWord = staticMarkWord(objectKindString, 0)

// populateStringLiterals interns every string literal in the AST and returns the
// byte size of the literal region, i.e. the offset the static fields start at.
func populateStringLiterals(ast *syntax.AST, reader *codegenReader) uint64 {
	visitor := &stringLiteralVisitor{reader: reader}
	reader.stringLiterals = base.NewStaticOrderedMap[stringLiteral, uint64]()
	for _, entry := range ast.CodeFiles.All() {
		entry.Value.Decl.Accept(visitor)
	}
	return visitor.offset
}

// ByteCount implements the [byteCountable] interface. The +1 is the trailing
// NUL every string carries one byte past its last content byte, matching
// zam_string_bytes on the runtime side; both halves have to size the slot the
// same way, because the compiler lays this slot out and the runtime reads it
// directly without copying.
func (s stringLiteral) ByteCount() int {
	return int(alignUp(uint64(wordByteSize+len(s)+1), wordByteSize))
}

// Read implements the [byteCountable] interface.
func (s stringLiteral) Read(buf []byte) (int, error) {
	if len(buf) < s.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	binary.LittleEndian.PutUint64(buf, uint64(len(s)))
	copy(buf[wordByteSize:], s)
	for i := wordByteSize + len(s); i < s.ByteCount(); i++ {
		buf[i] = 0
	}
	return s.ByteCount(), nil
}

// ByteCount implements the [byteCountable] interface.
func (s internedString) ByteCount() int {
	return wordByteSize + stringLiteral(s).ByteCount()
}

// Read implements the [byteCountable] interface.
//
// The mark word says the one thing about a literal that has to be said: it is not
// an allocation. A literal lies in the static region, so freeing it would release
// memory malloc never returned and collecting it would drop a value that outlives
// every frame naming it — and the state bits cannot say either, every colour
// being a colour of something the collector owns.
func (s internedString) Read(buf []byte) (int, error) {
	if len(buf) < s.ByteCount() {
		return 0, io.ErrShortBuffer
	}
	binary.LittleEndian.PutUint64(buf, staticStringMarkWord)
	if _, err := stringLiteral(s).Read(buf[wordByteSize:]); err != nil {
		return 0, err
	}
	return s.ByteCount(), nil
}

// intern lays out a static slot holding the literal's length-prefixed bytes,
// unless the literal already has one.
//
// The recorded offset is the slot's own, which is the mark word: that is where a
// pointer to any markable value points, and the length lies a word past it. A
// literal is therefore pushable as its offset with nothing copied.
func (v *stringLiteralVisitor) intern(literal stringLiteral) {
	r := v.reader
	if _, index := r.stringLiterals.Get(literal); index >= 0 {
		return
	}

	r.stringLiterals.Set(literal, uint64(len(r.staticInstanceMaps)))
	r.staticInstanceMaps = append(r.staticInstanceMaps, int(v.offset))

	interned := internedString(literal)
	byteCount := uint64(interned.ByteCount())
	if _, err := interned.Read(r.staticImageAt(v.offset, byteCount)); err != nil {
		panic("unreachable")
	}
	v.offset += byteCount
}

// visitDecl mirrors the operations pass: every static field, every nested
// declaration, every instance method body, and every instance member, whose
// initializer is emitted too — into the declaration's instance initializer
// routine rather than into a body of its own.
//
// Members are walked flattened, the way [operationVisitor.emitInstanceInitializer]
// emits them, so an inherited member's expression is reached through the
// subclass as well as through the declaration that wrote it. That is a
// duplicate, not a second slot: interning the same literal twice is interning
// it once.
func (v *stringLiteralVisitor) visitDecl(d syntax.Decl) error {
	for _, f := range d.StaticFields() {
		f.Accept(v)
	}
	for child := range d.Decls() {
		child.Accept(v)
	}
	for _, m := range d.InstanceMethods() {
		m.Accept(v)
	}
	for _, m := range instanceFields[*syntax.FieldMember](d) {
		m.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitDeclStruct(d *syntax.DeclStruct) error { return v.visitDecl(d) }
func (v *stringLiteralVisitor) VisitDeclEnum(d *syntax.DeclEnum) error     { return v.visitDecl(d) }
func (v *stringLiteralVisitor) VisitDeclClass(d *syntax.DeclClass) error   { return v.visitDecl(d) }
func (v *stringLiteralVisitor) VisitDeclAbstract(d *syntax.DeclAbstract) error {
	return v.visitDecl(d)
}
func (v *stringLiteralVisitor) VisitDeclInterface(d *syntax.DeclInterface) error {
	return v.visitDecl(d)
}
func (v *stringLiteralVisitor) VisitDeclNamespace(d *syntax.DeclNamespace) error {
	return v.visitDecl(d)
}

func (v *stringLiteralVisitor) VisitFieldMember(f *syntax.FieldMember) error {
	if f.Expr != nil {
		return f.Expr.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitFieldMethod(f *syntax.FieldMethod) error {
	if f.Body != nil {
		return f.Body.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitFieldEnum(f *syntax.FieldEnum) error {
	for _, parameter := range f.Parameters {
		parameter.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitStmtBlock(s *syntax.StmtBlock) error {
	for _, stmt := range s.Stmts {
		stmt.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitStmtExpr(s *syntax.StmtExpr) error {
	if s.Expr != nil {
		return s.Expr.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitStmtVariable(s *syntax.StmtVariable) error {
	if s.Expr != nil {
		return s.Expr.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitStmtIf(s *syntax.StmtIf) error {
	s.Condition.Accept(v)
	s.Then.Accept(v)
	if s.Else != nil {
		s.Else.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitStmtFor(s *syntax.StmtFor) error {
	s.Condition.Accept(v)
	s.Body.Accept(v)
	if s.Step != nil {
		s.Step.Accept(v)
	}
	if s.Else != nil {
		s.Else.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitStmtLabel(s *syntax.StmtLabel) error {
	return s.LoopStmt.Accept(v)
}

func (v *stringLiteralVisitor) VisitStmtLoopJump(s *syntax.StmtLoopJump) error { return nil }

func (v *stringLiteralVisitor) VisitExprNAry(e *syntax.ExprNAry) error {
	for _, operand := range e.Exprs {
		operand.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitExprField(e *syntax.ExprField) error { return e.Expr.Accept(v) }
func (v *stringLiteralVisitor) VisitExprCast(e *syntax.ExprCast) error   { return e.Expr.Accept(v) }

func (v *stringLiteralVisitor) VisitExprNew(e *syntax.ExprNew) error {
	if e.Size != nil {
		e.Size.Accept(v)
	}
	if e.Tail != nil {
		e.Tail.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitExprLambda(e *syntax.ExprLambda) error {
	if e.Body != nil {
		return e.Body.Accept(v)
	}
	return nil
}

func (v *stringLiteralVisitor) VisitExprNative(e *syntax.ExprNative) error { return nil }
func (v *stringLiteralVisitor) VisitExprType(e *syntax.ExprType) error     { return nil }

func (v *stringLiteralVisitor) VisitExprSymbol(e *syntax.ExprSymbol) error {
	if e.Kind() == syntax.ExprKindLiteralString {
		v.intern(stringLiteral(e.Start().Value.(string)))
	}
	return nil
}
