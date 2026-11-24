package codegen

import (
	"github.com/zambozoo/zam/internal/base"
	syntax "github.com/zambozoo/zam/internal/compiler/0_syntax"
)

// collectNativesAndLiterals records in reader every native the program calls
// and interns every string literal it writes, in the order they are written.
func collectNativesAndLiterals(ast *syntax.AST, reader *codegenReader) {
	reader.natives = base.NewStaticOrderedMap[string, uint64]()
	reader.stringLiterals = base.NewStaticOrderedMap[stringLiteral, uint64]()
	c := &collector{reader: reader}
	for _, entry := range ast.CodeFiles.All() {
		c.decl(entry.Value.Decl)
	}
}

// collector walks declarations for what collectNativesAndLiterals records.
type collector struct {
	reader *codegenReader
}

func (c *collector) decl(d syntax.Decl) {
	for _, field := range d.StaticFields() {
		c.field(field)
	}
	for _, field := range d.InstanceFields() {
		c.field(field)
	}
	for child := range d.Decls() {
		c.decl(child)
	}
}

func (c *collector) field(f syntax.Field) {
	switch f := f.(type) {
	case *syntax.FieldEnum:
		c.exprs(f.Parameters)
		c.expr(f.Value)
	case *syntax.FieldMember:
		c.expr(f.Expr)
	case *syntax.FieldMethod:
		c.stmt(f.Body)
	}
}

func (c *collector) stmt(s syntax.Stmt) {
	switch s := s.(type) {
	case *syntax.StmtBlock:
		for _, stmt := range s.Stmts {
			c.stmt(stmt)
		}
	case *syntax.StmtExpr:
		c.expr(s.Expr)
	case *syntax.StmtFor:
		c.expr(s.Condition)
		c.stmt(s.Body)
		c.stmt(s.Step)
		c.stmt(s.Else)
	case *syntax.StmtIf:
		c.expr(s.Condition)
		c.stmt(s.Then)
		c.stmt(s.Else)
	case *syntax.StmtLabel:
		c.stmt(s.LoopStmt)
	case *syntax.StmtDefer:
		c.stmt(s.Stmt)
	case *syntax.StmtSwitch:
		c.expr(s.Expr)
		for _, switchCase := range s.Cases {
			c.exprs(switchCase.Values)
		}
		for body := range s.Bodies() {
			c.stmt(body)
		}
	case *syntax.StmtUnpack:
		c.expr(s.Expr)
	case *syntax.StmtVariable:
		c.expr(s.Expr)
	}
}

func (c *collector) exprs(es []syntax.Expr) {
	for _, e := range es {
		c.expr(e)
	}
}

func (c *collector) expr(e syntax.Expr) {
	switch e := e.(type) {
	case *syntax.ExprCast:
		c.expr(e.Expr)
	case *syntax.ExprField:
		c.expr(e.Expr)
	case *syntax.ExprLambda:
		c.stmt(e.Body)
	case *syntax.ExprNAry:
		c.exprs(e.Exprs)
	case *syntax.ExprNative:
		if _, intrinsic := nativeIntrinsics[e.ID]; intrinsic {
			break
		}
		if _, intrinsic := enumIntrinsics[e.ID]; intrinsic {
			break
		}
		if _, at := c.reader.natives.Get(e.ID); at < 0 {
			c.reader.natives.Set(e.ID, uint64(c.reader.natives.Len()))
		}
	case *syntax.ExprNew:
		c.expr(e.Size)
		c.expr(e.Tail)
	case *syntax.ExprSymbol:
		if e.Kind() == syntax.ExprKindLiteralString {
			c.reader.internString(stringLiteral(e.Start().Value.(string)))
		}
	}
}
