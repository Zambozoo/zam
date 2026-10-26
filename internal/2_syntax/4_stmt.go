package syntax

type (
	// StmtKind is the kind of statement.
	StmtKind uint8

	StmtVisitor interface {
		VisitStmtLoopJump(s *StmtLoopJump) error
		VisitStmtExpr(s *StmtExpr) error
		VisitStmtFor(s *StmtFor) error
		VisitStmtIf(s *StmtIf) error
		VisitStmtLabel(s *StmtLabel) error
		VisitStmtBlock(s *StmtBlock) error
		VisitStmtVariable(s *StmtVariable) error
	}

	// Stmt is a statement in the AST.
	Stmt interface {
		// StmtKind returns the kind of statement.
		Kind() StmtKind
		// Start returns the token that started the statement.
		Start() *Token

		// Accept visits the statement with the given visitor.
		Accept(visitor StmtVisitor) error
	}

	// StmtLoopJump is a statement that jumps during a loop.
	StmtLoopJump struct {
		// start is the token that started the statement.
		start *Token
		// kind is the kind of statement.
		kind StmtKind
		// Label is the label of the statement.
		Label string
	}

	// StmtExpr is a statement that is an expression.
	StmtExpr struct {
		// start is the token that started the statement.
		start *Token
		// kind is the kind of statement.
		kind StmtKind
		// Expr is the expression of the statement.
		Expr Expr
	}

	// StmtFor is a statement that is a for statement.
	StmtFor struct {
		// start is the token that started the statement.
		start *Token
		// Condition is the condition of the statement.
		Condition Expr
		// Body is the body of the statement.
		Body Stmt
		// Step is the statement run after each iteration of the body. It is
		// what a continue jumps to, so it is kept apart from the body rather
		// than appended to it.
		Step Stmt
		// Else is the else statement of the statement.
		Else Stmt
	}

	// StmtIf is a statement that is an if statement.
	StmtIf struct {
		// start is the token that started the statement.
		start *Token
		// Condition is the condition of the statement.
		Condition Expr
		// Then is the then statement of the statement.
		Then Stmt
		// Else is the else statement of the statement.
		Else Stmt
	}

	// StmtLabel is a statement that is a label statement.
	StmtLabel struct {
		// start is the token that started the statement.
		start *Token
		// Label is the label of the statement.
		Label string
		// LoopStmt is the loop statement of the statement.
		LoopStmt Stmt
	}

	// StmtBlock is a statement that is a block of statements.
	StmtBlock struct {
		// start is the token that started the statement.
		start *Token
		// Stmts is the statements of the block.
		Stmts []Stmt
		// Broken says a statement of the block failed to parse and is missing
		// from Stmts, which may leave it looking as though control runs past
		// the end of the block where what was written does not.
		Broken bool
	}

	// StmtVariable is a statement that is a variable declaration.
	StmtVariable struct {
		// start is the token that started the statement.
		start *Token
		// name is the name of the variable.
		name string
		// nameToken is the token the name is written at.
		nameToken *Token
		// _type is the type of the variable.
		Type Type
		// Expr is the expression of the variable.
		Expr Expr
	}
)

// newStmtKind creates a new statement kind.
func newStmtKind(s string, token TokenKind) StmtKind {
	stmtKind := StmtKind(len(stmtKindStrings))
	stmtKindStrings = append(stmtKindStrings, s)
	stmtKindTokens = append(stmtKindTokens, token)
	return stmtKind
}

var (
	// stmtKindStrings is the strings of the statement kinds.
	stmtKindStrings []string
	// stmtKindTokens is the tokens of the statement kinds.
	stmtKindTokens []TokenKind

	// Returns from a function with or without a value
	StmtKindReturn StmtKind = newStmtKind("return", TTReturn)
	// Breaks out of a loop
	StmtKindBreak StmtKind = newStmtKind("break", TTBreak)
	// Continues to the next iteration of a loop
	StmtKindContinue StmtKind = newStmtKind("continue", TTContinue)
	// Labels a statement for later reference by break/continue statements
	StmtKindLabel StmtKind = newStmtKind("label", TTLabel)
	// If statement with or without an else statement
	StmtKindIf StmtKind = newStmtKind("if", TTIf)
	// For statement with or without an initializer, condition, step, and else statement
	StmtKindFor StmtKind = newStmtKind("for", TTFor)
	// Variable declaration with or without an initializer
	StmtKindVariable StmtKind = newStmtKind("variable", TTVar)
	// Block of statements with new scope
	StmtKindBlock StmtKind = newStmtKind("block", TTLeftBrace)
	// Runs the Delete override on an object, defaulting to Obj.Delete, and
	// does nothing for a receiver no override reaches.
	StmtKindDelete StmtKind = newStmtKind("delete", TTDelete)
	// Expression statement
	StmtKindExpr StmtKind = newStmtKind("expr", TTNone)
	// Mark statement for garbage collection
	StmtKindMark StmtKind = newStmtKind("mark", TTMark)
	// Releases the memory a pointer addresses: a mark-dead for a tracked
	// allocation (string, closure, thread, run of class instances) and a raw
	// free otherwise.
	StmtKindFree StmtKind = newStmtKind("free", TTFree)
)

// newStmt creates a new statement.
func newStmt(parser SyntaxParser) (Stmt, error) {
	var stmt Stmt
	tok, err := parser.Match(stmtKindTokens...)
	if err != nil {
		return nil, err
	} else if tok == nil {
		tok, _ = parser.Peek()
		stmt, err = newStmtExpr(parser, tok, StmtKindExpr)
	}

	switch tok.Kind {
	case TTBreak, TTContinue:
		stmt, err = newStmtLoopJump(parser, tok)
	case TTDelete:
		stmt, err = newStmtExpr(parser, tok, StmtKindDelete)
	case TTFree:
		stmt, err = newStmtExpr(parser, tok, StmtKindFree)
	case TTFor:
		stmt, err = newStmtFor(parser, tok)
	case TTIf:
		stmt, err = newStmtIf(parser, tok)
	case TTLabel:
		stmt, err = newStmtLabel(parser, tok)
	case TTLeftBrace:
		stmt, err = newStmtBlock(parser, tok)
	case TTMark:
		stmt, err = newStmtExpr(parser, tok, StmtKindMark)
	case TTReturn:
		stmt, err = newStmtReturn(parser, tok)
	case TTVar:
		stmt, err = newStmtVariable(parser, tok)
	}

	if err != nil {
		return nil, err
	}
	return stmt, nil
}

// newStmtLoopJump creates a new statement that jumps during a loop.
func newStmtLoopJump(parser SyntaxParser, start *Token) (*StmtLoopJump, error) {
	kind := StmtKindBreak
	if start.Kind == TTContinue {
		kind = StmtKindContinue
	}

	tok, err := parser.Match(TTIdentifier)
	if err != nil {
		return nil, err
	}

	var label string
	if tok != nil {
		str := tok.Value.(string)
		label = str
	}

	if _, err := parser.Consume(TTSemiColon); err != nil {
		return nil, err
	}

	return &StmtLoopJump{
		start: start,
		kind:  kind,
		Label: label,
	}, nil
}

// StmtKind implements the [Stmt] interface.
func (s *StmtLoopJump) Kind() StmtKind {
	return s.kind
}

// Start implements the [Stmt] interface.
func (s *StmtLoopJump) Start() *Token {
	return s.start
}

// Accept implements the [Stmt] interface.
func (s *StmtLoopJump) Accept(visitor StmtVisitor) error {
	return visitor.VisitStmtLoopJump(s)
}

// newStmtReturn creates a new statement that returns from a function.
func newStmtReturn(parser SyntaxParser, start *Token) (*StmtExpr, error) {
	if tok, err := parser.Match(TTSemiColon); err != nil {
		return nil, err
	} else if tok != nil {
		return &StmtExpr{
			start: start,
			kind:  StmtKindReturn,
		}, nil
	}
	return newStmtExpr(parser, start, StmtKindReturn)
}

// newStmtExpr creates a new statement that is an expression.
func newStmtExpr(parser SyntaxParser, start *Token, kind StmtKind) (*StmtExpr, error) {
	expr, err := newExpr(parser)
	if err != nil {
		return nil, err
	}

	if _, err := parser.Consume(TTSemiColon); err != nil {
		return nil, err
	}

	return &StmtExpr{
		start: start,
		kind:  kind,
		Expr:  expr,
	}, nil
}

// StmtKind implements the [Stmt] interface.
func (s *StmtExpr) Kind() StmtKind {
	return s.kind
}

// Start implements the [Stmt] interface.
func (s *StmtExpr) Start() *Token {
	if s.start != nil {
		return s.start
	}
	return s.Expr.Start()
}

// Accept implements the [Stmt] interface.
func (s *StmtExpr) Accept(visitor StmtVisitor) error {
	return visitor.VisitStmtExpr(s)
}

// newStmtFor creates a new statement that is a for statement.
func newStmtFor(parser SyntaxParser, start *Token) (Stmt, error) {
	tok, err := parser.Match(TTLeftParen)
	if err != nil {
		return nil, err
	}

	var init Stmt
	var condition Expr
	var step Expr
	var body Stmt
	if tok != nil {
		if tok, err := parser.Match(TTVar, TTSemiColon); err != nil {
			return nil, err
		} else if tok != nil {
			if tok.Kind == TTVar {
				if init, err = newStmtVariable(parser, tok); err != nil {
					return nil, err
				}
			}
		} else {
			start, _ := parser.Peek()
			if init, err = newStmtExpr(parser, start, StmtKindExpr); err != nil {
				return nil, err
			}
		}

		if tok, err := parser.Match(TTSemiColon); err != nil {
			return nil, err
		} else if tok == nil {
			if condition, err = newExpr(parser); err != nil {
				return nil, err
			}

			if _, err := parser.Consume(TTSemiColon); err != nil {
				return nil, err
			}
		} else {
			condition = &ExprSymbol{
				start: newToken(TTTrue, start.Offset, nil),
				kind:  ExprKindLiteralBoolean,
			}
		}

		if tok, err := parser.Match(TTRightParen); err != nil {
			return nil, err
		} else if tok == nil {
			if step, err = newExpr(parser); err != nil {
				return nil, err
			}

			if _, err := parser.Consume(TTRightParen); err != nil {
				return nil, err
			}
		}
	} else {
		condition = &ExprSymbol{
			start: newToken(TTTrue, start.Offset, nil),
			kind:  ExprKindLiteralBoolean,
		}
	}

	if body, err = newStmt(parser); err != nil {
		return nil, err
	}

	var _else Stmt
	if tok, err := parser.Match(TTElse); err != nil {
		return nil, err
	} else if tok != nil {
		if _else, err = newStmt(parser); err != nil {
			return nil, err
		}
	}

	var stepStmt Stmt
	if step != nil {
		stepStmt = &StmtExpr{
			start: step.Start(),
			kind:  StmtKindExpr,
			Expr:  step,
		}
	}

	forStmt := &StmtFor{
		start:     start,
		Condition: condition,
		Body:      body,
		Step:      stepStmt,
		Else:      _else,
	}

	// The initializer belongs to the scope enclosing the loop, not to the loop,
	// so it is hoisted into a block that the loop is the rest of.
	if init != nil {
		return &StmtBlock{
			start: start,
			Stmts: []Stmt{init, forStmt},
		}, nil
	}

	return forStmt, nil
}

// StmtKind implements the [Stmt] interface.
func (s *StmtFor) Kind() StmtKind {
	return StmtKindFor
}

// Start implements the [Stmt] interface.
func (s *StmtFor) Start() *Token {
	return s.start
}

// Accept implements the [Stmt] interface.
func (s *StmtFor) Accept(visitor StmtVisitor) error {
	return visitor.VisitStmtFor(s)
}

// newStmtIf creates a new statement that is an if statement.
func newStmtIf(parser SyntaxParser, start *Token) (*StmtIf, error) {
	if _, err := parser.Consume(TTLeftParen); err != nil {
		return nil, err
	}

	condition, err := newExpr(parser)
	if err != nil {
		return nil, err
	}

	if _, err := parser.Consume(TTRightParen); err != nil {
		return nil, err
	}

	stmt, err := newStmt(parser)
	if err != nil {
		return nil, err
	}

	var _else Stmt
	if tok, err := parser.Match(TTElse); err != nil {
		return nil, err
	} else if tok != nil {
		if _else, err = newStmt(parser); err != nil {
			return nil, err
		}
	}

	return &StmtIf{
		start:     start,
		Condition: condition,
		Then:      stmt,
		Else:      _else,
	}, nil
}

// StmtKind implements the [Stmt] interface.
func (s *StmtIf) Kind() StmtKind {
	return StmtKindIf
}

// Start implements the [Stmt] interface.
func (s *StmtIf) Start() *Token {
	return s.start
}

// Accept implements the [Stmt] interface.
func (s *StmtIf) Accept(visitor StmtVisitor) error {
	return visitor.VisitStmtIf(s)
}

// newStmtLabel creates a new statement that is a label statement.
func newStmtLabel(parser SyntaxParser, start *Token) (*StmtLabel, error) {
	tok, err := parser.Consume(TTIdentifier)
	if err != nil {
		return nil, err
	}
	label := tok.Value.(string)

	if _, err := parser.Consume(TTColon); err != nil {
		return nil, err
	}

	forTok, err := parser.Consume(TTFor)
	if err != nil {
		return nil, err
	}

	forStmt, err := newStmtFor(parser, forTok)
	if err != nil {
		return nil, err
	}

	return &StmtLabel{
		start:    start,
		Label:    label,
		LoopStmt: forStmt,
	}, nil
}

// StmtKind implements the [Stmt] interface.
func (s *StmtLabel) Kind() StmtKind {
	return StmtKindLabel
}

// Start implements the [Stmt] interface.
func (s *StmtLabel) Start() *Token {
	return s.start
}

// Accept implements the [Stmt] interface.
func (s *StmtLabel) Accept(visitor StmtVisitor) error {
	return visitor.VisitStmtLabel(s)
}

// newStmtBlock creates a new statement that is a block of statements.
func newStmtBlock(parser SyntaxParser, start *Token) (*StmtBlock, error) {
	var stmts []Stmt
	broken := false
	for {
		if tok, err := parser.Match(TTRightBrace); err != nil {
			return nil, err
		} else if tok != nil {
			break
		}

		recoverFrom := parser.Checkpoint()
		stmt, err := newStmt(parser)
		if err != nil {
			broken = true
			if stmt, ok := kept[Stmt](err); ok {
				stmts = append(stmts, stmt)
			}
			if err := recoverFrom(err); err != nil {
				return nil, err
			}
			continue
		}
		stmts = append(stmts, stmt)
	}

	return &StmtBlock{
		start:  start,
		Stmts:  stmts,
		Broken: broken,
	}, nil
}

// StmtKind implements the [Stmt] interface.
func (s *StmtBlock) Kind() StmtKind {
	return StmtKindBlock
}

// Start implements the [Stmt] interface.
func (s *StmtBlock) Start() *Token {
	return s.start
}

// Accept implements the [Stmt] interface.
func (s *StmtBlock) Accept(visitor StmtVisitor) error {
	return visitor.VisitStmtBlock(s)
}

// newStmtVariable creates a new statement that is a variable declaration.
func newStmtVariable(parser SyntaxParser, start *Token) (*StmtVariable, error) {
	_type, err := newType(parser)
	if err != nil {
		return nil, err
	}

	nameToken, err := parser.ConsumeAny(TTIdentifier, TTUnderscore)
	if err != nil {
		return nil, err
	}
	name := BlankName
	if nameToken.Kind == TTIdentifier {
		name = nameToken.Value.(string)
	}

	variable := &StmtVariable{
		start:     start,
		name:      name,
		nameToken: nameToken,
		Type:      _type,
	}
	if tok, err := parser.ConsumeAny(TTEqual, TTSemiColon); err != nil {
		return nil, keep(variable, err)
	} else if tok.Kind == TTSemiColon {
		return variable, nil
	}

	expr, err := newExpr(parser)
	if err != nil {
		return nil, keep(variable, err)
	}

	if _, err := parser.Consume(TTSemiColon); err != nil {
		return nil, keep(variable, err)
	}

	variable.Expr = expr
	return variable, nil
}

// StmtKind implements the [Stmt] interface.
func (s *StmtVariable) Kind() StmtKind {
	return StmtKindVariable
}

// Start implements the [Stmt] interface.
func (s *StmtVariable) Start() *Token {
	return s.start
}

// Name returns the name the variable is declared under. The start token is the
// var keyword the declaration opens with, which carries no name of its own.
func (s *StmtVariable) Name() string {
	return s.name
}

// NameToken returns the token the variable's name is written at.
func (s *StmtVariable) NameToken() *Token {
	return s.nameToken
}

// Accept implements the [Stmt] interface.
func (s *StmtVariable) Accept(visitor StmtVisitor) error {
	return visitor.VisitStmtVariable(s)
}
