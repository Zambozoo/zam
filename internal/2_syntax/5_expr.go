package syntax

import (
	"fmt"
	"maps"
	"slices"
)

type (
	// ExprKind is the kind of expression.
	ExprKind int

	ExprVisitor interface {
		VisitExprCast(e *ExprCast) error
		VisitExprField(e *ExprField) error
		VisitExprLambda(e *ExprLambda) error
		VisitExprNAry(e *ExprNAry) error
		VisitExprNative(e *ExprNative) error
		VisitExprNew(e *ExprNew) error
		VisitExprSymbol(e *ExprSymbol) error
		VisitExprType(e *ExprType) error
	}

	// Expr is an expression in the AST.
	Expr interface {
		// Start returns the start token of the expression.
		Start() *Token
		// ExprKind returns the kind of the expression.
		Kind() ExprKind
		// ResolvedType returns the type the expression evaluates to. An
		// expression that cannot state its own type carries one that static
		// semantic validation resolved.
		ResolvedType() Type

		// Accept visits the expression with the given visitor.
		Accept(visitor ExprVisitor) error
	}

	// ExprCast is a cast expression in the AST.
	ExprCast struct {
		// Expr is the expression of the cast.
		Expr Expr
		// Type is the type of the cast.
		Type Type
	}

	// ExprField is a field expression in the AST.
	ExprField struct {
		// Expr is the expression of the field.
		Expr Expr
		// Field is the field of the expression.
		Field string
		// ComputedType is the type of the field, resolved by static semantic
		// validation.
		ComputedType Type
	}

	// ExprLambda is a lambda expression in the AST.
	ExprLambda struct {
		// start is the start token of the expression.
		start *Token
		// Type is the type of the lambda expression.
		Type *TypeFunction
		// Parameters is the parameters of the lambda expression.
		Parameters []string
		// Body is the body of the lambda expression.
		Body Stmt
		// Captures are the variables the body names from outside itself, in the
		// order they were first named, resolved by static semantic validation.
		// A closure outlives the frame it was written in, so these are what it
		// has to carry a copy of.
		Captures []string
	}

	// ExprNAry is a n-ary expression in the AST.
	ExprNAry struct {
		// start is the start token of the expression.
		start *Token
		// kind is the kind of the expression.
		kind ExprKind
		// Exprs is the operands of the expression.
		Exprs []Expr
		// ComputedType is the type the operator evaluates to, resolved by static
		// semantic validation.
		ComputedType Type
	}

	// ExprNative is a native expression in the AST.
	ExprNative struct {
		// start is the start token of the expression.
		start *Token
		// ID is the ID of the native expression.
		ID string
		// ComputedType is the type the native call evaluates to, resolved by
		// static semantic validation.
		ComputedType Type
	}

	// ExprNew is a new expression in the AST.
	ExprNew struct {
		// start is the start token of the expression.
		start *Token
		// Type is the type of the new expression.
		Type Type
		// Size is the size of the new expression.
		Size Expr
		// Tail is the tail of the new expression.
		Tail Expr
	}

	// ExprSymbol is a single token expression in the AST.
	ExprSymbol struct {
		// start is the start token of the expression.
		start *Token
		// kind is the kind of the expression.
		kind ExprKind
		// ComputedType is the type a named symbol evaluates to, resolved by
		// static semantic validation. A literal states its own type instead.
		ComputedType Type
	}

	// ExprType is a type expression in the AST.
	ExprType struct {
		// start is the start token of the expression.
		start *Token
		// Type is the type of the expression.
		Type Type

		// Field is the static Field of the expression, if any.
		Field string
		// TypeArgs is the type arguments of the static field, if the
		// selection was written as `[T,...].Name`. A nil list is a plain
		// static selection; a non-nil list binds a generic static method's
		// own parameters and is what codegen looks the concrete up by.
		TypeArgs []Type
		// Concrete is the method instantiation Field+TypeArgs resolved to,
		// filled in by static semantic validation and read by codegen.
		Concrete *MethodConcrete
		// ComputedType is the type of that static field, resolved by static
		// semantic validation.
		ComputedType Type
	}
)

// newExprKind creates a new expression kind.
func newExprKind(s string) ExprKind {
	exprKind := ExprKind(len(exprKindStrings))
	exprKindStrings = append(exprKindStrings, s)
	return exprKind
}

// newNAryExprKind creates a new n-ary expression kind.
func newNAryExprKind(s string, tokenKind TokenKind) ExprKind {
	exprKind := newExprKind(s)
	naryExprKindMap[tokenKind] = exprKind
	return exprKind
}

// String implements the [fmt.Stringer] interface.
func (e ExprKind) String() string {
	return exprKindStrings[e]
}

var (
	// exprKindStrings is the strings of the expression kinds.
	exprKindStrings []string
	// naryExprKindMap is the map of token kinds to n-ary expression kinds.
	naryExprKindMap = map[TokenKind]ExprKind{}

	// Ternary expression for conditional evaluation
	ExprKindTernary = newExprKind("ternary")
	// Call expression for calling a function
	ExprKindCall = newExprKind("call")
	// Typeof expression for getting the type of an expression
	ExprKindTypeof = newExprKind("typeof")
	// Type expression for getting the type of a type
	ExprKindType = newExprKind("type")
	// Hash expression for getting the hash of an expression
	ExprKindHash = newExprKind("hash")
	// Binary negation expression for flipping the bits of a boolean or integer
	ExprKindBinaryNegate = newExprKind("binary_negate")
	// Logical negation expression for asking whether a value is zero
	ExprKindLogicalNegate = newExprKind("logical_negate")
	// Arithmetic negation expression for negating a float or integer
	ExprKindArithmeticNegate = newExprKind("arithmetic_negate")
	// Pre-increment expression for incrementing a variable before use
	ExprKindPreIncrement = newExprKind("pre_increment")
	// Pre-decrement expression for decrementing a variable before use
	ExprKindPreDecrement = newExprKind("pre_decrement")
	// Post-increment expression for incrementing a variable after use
	ExprKindPostIncrement = newExprKind("post_increment")
	// Post-decrement expression for decrementing a variable after use
	ExprKindPostDecrement = newExprKind("post_decrement")
	// Address of expression for getting the address of an expression
	ExprKindAddressOf = newExprKind("address_of")
	// Dereference expression for dereferencing a pointer
	ExprKindDereference = newExprKind("dereference")
	// Spread expression for forwarding a pointer or array as a variadic payload
	// at a call site. The spread is a modifier on the argument it wraps and is
	// only legal as the last argument of a call.
	ExprKindSpread = newExprKind("spread")

	// New expression for mallocking memory
	ExprKindNew = newExprKind("new")
	// Wait expression for waiting for a thread to finish
	ExprKindWait = newExprKind("wait")
	// Async expression for running a thread
	ExprKindAsync = newExprKind("async")
	// Cast expression for converting an expression to a type
	ExprKindCast = newExprKind("cast")
	// Field expression for accessing a field
	ExprKindField = newExprKind("field")
	// Literal string expression for a string literal
	ExprKindLiteralString = newExprKind("literal_string")
	// Literal char expression for a char literal
	ExprKindLiteralChar = newExprKind("literal_char")
	// Literal integer expression for an integer literal
	ExprKindLiteralInteger = newExprKind("literal_integer")
	// Literal uint expression for an integer literal written with a trailing
	// `u` or `U`. The value is laid down the same way as an int — a word of
	// bits — but the type is uint, so the usual int/uint rules decide what
	// may be done with it.
	ExprKindLiteralUint = newExprKind("literal_uint")
	// Literal float expression for a float literal
	ExprKindLiteralFloat = newExprKind("literal_float")
	// Literal boolean expression for a boolean literal
	ExprKindLiteralBoolean = newExprKind("literal_boolean")
	// Lambda expression for a lambda function
	ExprKindLambda = newExprKind("lambda")
	// This expression for the current object
	ExprKindThis = newExprKind("this")
	// Super expression for the parent object
	ExprKindSuper = newExprKind("super")
	// Null expression for a null value
	ExprKindLiteralNull = newExprKind("null")
	// Native expression for a native function
	ExprKindIdentifier = newExprKind("identifier")
	// Native expression for a native function
	ExprKindNative = newExprKind("native")

	// Assign expression for assigning a value to a variable
	ExprKindAssign = newNAryExprKind("assign", TTEqual)
	// Null coalesce expression for returning the first non-null value
	ExprKindNullCoalesce = newNAryExprKind("null_coalesce", TTQuestionQuestion)
	// Logical OR expression for logical OR of two expressions
	ExprKindLogicalOR = newNAryExprKind("logical_or", TTOrOr)
	// Logical AND expression for logical AND of two expressions
	ExprKindLogicalAND = newNAryExprKind("logical_and", TTAndAnd)
	// Binary OR expression for binary OR of two expressions
	ExprKindBinaryOR = newNAryExprKind("binary_or", TTOr)
	// Binary AND expression for binary AND of two expressions
	ExprKindBinaryAND = newNAryExprKind("binary_and", TTAnd)
	// Binary XOR expression for binary XOR of two expressions
	ExprKindBinaryXOR = newNAryExprKind("binary_xor", TTCaret)
	// Binary shift left expression for shifting a value left
	ExprKindBinaryShiftLeft = newNAryExprKind("binary_shift_left", TTLessLess)
	// Binary shift right expression for shifting a value right
	ExprKindBinaryShiftRight = newNAryExprKind("binary_shift_right", TTGreaterGreater)
	// Binary shift right right expression for arithmetic shift right
	ExprKindBinaryShiftRightRight = newNAryExprKind("arithmetic_shift", TTGreaterGreaterGreater)
	// Equal expression for checking if two expressions are equal
	ExprKindEqual = newNAryExprKind("equal", TTEqualEqual)
	// Not equal expression for checking if two expressions are not equal
	ExprKindNotEqual = newNAryExprKind("not_equal", TTBangEqual)
	// Greater than expression for checking if one expression is greater than another
	ExprKindGreaterThan = newNAryExprKind("greater_than", TTGreater)
	// Less than expression for checking if one expression is less than another
	ExprKindLessThan = newNAryExprKind("less_than", TTLess)
	// Greater than or equal expression for checking if one expression is greater than or equal to another
	ExprKindGreaterThanOrEqual = newNAryExprKind("greater_than_or_equal", TTGreaterEqual)
	// Less than or equal expression for checking if one expression is less than or equal to another
	ExprKindLessThanOrEqual = newNAryExprKind("less_than_or_equal", TTLessEqual)
	// Spaceship expression for checking if two expressions are equal, less than, or greater than
	ExprKindSpaceship = newNAryExprKind("spaceship", TTSpaceship)
	// Subtype expression for asking whether one type stands below another
	ExprKindSubtypeOf = newNAryExprKind("subtype_of", TTSubtype)
	// Add expression for adding two expressions
	ExprKindAdd = newNAryExprKind("add", TTPlus)
	// Subtract expression for subtracting two expressions
	ExprKindSubtract = newNAryExprKind("subtract", TTMinus)
	// Multiply expression for multiplying two expressions
	ExprKindMultiply = newNAryExprKind("multiply", TTStar)
	// Divide expression for dividing two expressions
	ExprKindDivide = newNAryExprKind("divide", TTSlash)
	// Modulus expression for getting the remainder of two expressions
	ExprKindModulus = newNAryExprKind("modulus", TTPercent)

	nAryPrecedence = [][]TokenKind{
		0: {TTStar, TTSlash, TTPercent},                            // *, /, %
		1: {TTPlus, TTMinus},                                       // +, -
		2: {TTLessLess, TTGreaterGreater, TTGreaterGreaterGreater}, // <<, >>, >>>
		// Descent is asked at the same level as order, since both relate two
		// operands and answer with something that is neither of them.
		3:  {TTLess, TTGreater, TTLessEqual, TTGreaterEqual, TTSpaceship, TTSubtype}, // <, >, <=, >=, <=>, <:
		4:  {TTEqualEqual, TTBangEqual},                                              // ==, !=
		5:  {TTAnd},                                                                  // &
		6:  {TTCaret},                                                                // ^
		7:  {TTOr},                                                                   // |
		8:  {TTAndAnd},                                                               // &&
		9:  {TTOrOr},                                                                 // ||
		10: {TTQuestionQuestion},                                                     // ??
	}

	// prefixKindMap is the map of token kinds to prefix expression kinds.
	prefixKindMap = map[TokenKind]ExprKind{
		TTPlusPlus:   ExprKindPreIncrement,
		TTMinusMinus: ExprKindPreDecrement,
		TTBang:       ExprKindLogicalNegate,
		TTTilde:      ExprKindBinaryNegate,
		TTMinus:      ExprKindArithmeticNegate,
		TTPlus:       0,
		TTStar:       ExprKindDereference,
		TTAnd:        ExprKindAddressOf,
		TTHash:       ExprKindHash,
		TTAt:         ExprKindTypeof,
	}
	// prefixTokenKinds is the token kinds of the prefix expression.
	prefixTokenKinds = slices.Collect(maps.Keys(prefixKindMap))

	// postfixTokenKinds is the token kinds of the postfix expression.
	postfixTokenKinds = []TokenKind{
		TTPlusPlus,
		TTMinusMinus,
		TTPeriod,
		TTLeftBracket,
		TTLeftParen,
		TTLeftBrace,
	}

	// primaryKindMap is the map of token kinds to primary expression kinds.
	primaryKindMap = map[TokenKind]ExprKind{
		TTIntLiteral:    ExprKindLiteralInteger,
		TTUintLiteral:   ExprKindLiteralUint,
		TTFloatLiteral:  ExprKindLiteralFloat,
		TTTrue:          ExprKindLiteralBoolean,
		TTFalse:         ExprKindLiteralBoolean,
		TTCharLiteral:   ExprKindLiteralChar,
		TTStringLiteral: ExprKindLiteralString,
		TTIdentifier:    ExprKindIdentifier,
		TTThis:          ExprKindThis,
		TTSuper:         ExprKindSuper,
		TTNull:          ExprKindLiteralNull,
		TTNative:        ExprKindNative,
		TTNew:           ExprKindNew,
		TTType:          ExprKindType,
		TTWait:          ExprKindWait,
		TTAsync:         ExprKindAsync,
		TTLambda:        ExprKindLambda,
	}
	// primaryTokenKinds is the token kinds of the primary expression.
	primaryTokenKinds = append(slices.Collect(maps.Keys(primaryKindMap)), TTLeftParen)
)

// newExpr creates a new expression.
func newExpr(parser SyntaxParser) (Expr, error) {
	expr, err := newExprAssign(parser)
	if err != nil {
		return nil, err
	}

	return expr, nil
}

// newExprAssign creates a new assignment expression.
func newExprAssign(parser SyntaxParser) (Expr, error) {
	left, err := newExprTernary(parser)
	if err != nil {
		return nil, err
	}

	tok, err := parser.Match(TTEqual)
	if err != nil {
		return nil, err
	} else if tok == nil {
		return left, nil
	}

	right, err := newExprTernary(parser)
	if err != nil {
		return nil, err
	}

	return &ExprNAry{
		kind:  ExprKindAssign,
		Exprs: []Expr{left, right},
	}, nil
}

// newExprTernary creates a new ternary expression.
func newExprTernary(parser SyntaxParser) (Expr, error) {
	first, err := newExprNAry(parser, len(nAryPrecedence)-1)
	if err != nil {
		return nil, err
	}

	if tok, err := parser.Match(TTQuestion); err != nil {
		return nil, err
	} else if tok == nil {
		return first, nil
	}

	second, err := newExprNAry(parser, len(nAryPrecedence)-1)
	if err != nil {
		return nil, err
	}

	if _, err := parser.Consume(TTColon); err != nil {
		return nil, err
	}

	third, err := newExprNAry(parser, len(nAryPrecedence)-1)
	if err != nil {
		return nil, err
	}

	return &ExprNAry{
		kind:  ExprKindTernary,
		Exprs: []Expr{first, second, third},
	}, nil
}

// newExprNAry creates a new n-ary expression.
func newExprNAry(parser SyntaxParser, precedence int) (Expr, error) {
	if precedence < 0 {
		return newExprPrefix(parser)
	}

	first, err := newExprNAry(parser, precedence-1)
	if err != nil {
		return nil, err
	}

	expectedTokenKinds := nAryPrecedence[precedence]
	tok, err := parser.Match(expectedTokenKinds...)
	if err != nil {
		return nil, err
	} else if tok == nil {
		return first, nil
	}

	second, err := newExprNAry(parser, precedence-1)
	if err != nil {
		return nil, err
	}

	expr := &ExprNAry{
		kind:  naryExprKindMap[tok.Kind],
		Exprs: []Expr{first, second},
	}

	for {
		tok, err := parser.Match(expectedTokenKinds...)
		if err != nil {
			return nil, err
		} else if tok == nil {
			return expr, nil
		}

		operand, err := newExprNAry(parser, precedence-1)
		if err != nil {
			return nil, err
		}

		if expr.kind == naryExprKindMap[tok.Kind] {
			expr.Exprs = append(expr.Exprs, operand)
		} else {
			expr = &ExprNAry{
				kind:  naryExprKindMap[tok.Kind],
				Exprs: []Expr{expr, operand},
			}
		}
	}
}

// newExprPrefix creates a new prefix expression.
func newExprPrefix(parser SyntaxParser) (Expr, error) {
	tok, err := parser.Match(prefixTokenKinds...)
	if err != nil {
		return nil, err
	} else if tok == nil {
		return newExprPostfix(parser)
	} else if tok.Kind == TTAt {
		if brace, err := parser.Match(TTLeftBrace); err != nil {
			return nil, err
		} else if brace != nil {
			_type, err := newType(parser)
			if err != nil {
				return nil, err
			}

			if _, err := parser.Consume(TTRightBrace); err != nil {
				return nil, err
			}

			return &ExprType{
				start: tok,
				Type:  _type,
			}, nil
		}
	}

	expr, err := newExprPrefix(parser)
	if err != nil {
		return nil, err
	} else if tok.Kind == TTPlus {
		return expr, nil
	}

	return &ExprNAry{
		start: tok,
		kind:  prefixKindMap[tok.Kind],
		Exprs: []Expr{expr},
	}, nil
}

// newExprPostfix creates a new postfix expression.
func newExprPostfix(parser SyntaxParser) (Expr, error) {
	expr, err := newExprPrimary(parser)
	if err != nil {
		return nil, err
	}

	for {
		tok, err := parser.Match(postfixTokenKinds...)
		if err != nil {
			return nil, err
		} else if tok == nil {
			return expr, nil
		}

		switch tok.Kind {
		case TTPlusPlus:
			expr = &ExprNAry{
				kind:  ExprKindPostIncrement,
				Exprs: []Expr{expr},
			}
		case TTMinusMinus:
			expr = &ExprNAry{
				kind:  ExprKindPostDecrement,
				Exprs: []Expr{expr},
			}
		case TTPeriod:
			field, err := parser.Consume(TTIdentifier)
			if err != nil {
				return nil, err
			}
			expr = &ExprField{
				Expr:  expr,
				Field: field.Value.(string),
			}
		case TTLeftBracket: // equivalent to *(arr + i)
			index, err := newExpr(parser)
			if err != nil {
				return nil, err
			}
			if _, err := parser.Consume(TTRightBracket); err != nil {
				return nil, err
			}
			expr = &ExprNAry{
				kind: ExprKindDereference,
				Exprs: []Expr{
					&ExprNAry{
						kind:  ExprKindAdd,
						Exprs: []Expr{expr, index},
					},
				},
			}
		case TTLeftParen:
			expr, err = newExprCall(parser, expr)
			if err != nil {
				return nil, err
			}
		case TTLeftBrace:
			_type, err := newType(parser)
			if err != nil {
				return nil, err
			}
			if _, err := parser.Consume(TTRightBrace); err != nil {
				return nil, err
			}
			expr = &ExprCast{
				Type: _type,
				Expr: expr,
			}
		}
	}
}

// newExprPrimary creates a new primary expression.
func newExprPrimary(parser SyntaxParser) (Expr, error) {
	tok, err := parser.ConsumeAny(primaryTokenKinds...)
	if err != nil {
		return nil, err
	}

	var expr Expr
	switch tok.Kind {
	case TTIdentifier, TTThis, TTSuper, TTNull, TTTrue, TTFalse, TTCharLiteral, TTStringLiteral, TTIntLiteral, TTUintLiteral, TTFloatLiteral:
		expr = &ExprSymbol{
			start: tok,
			kind:  primaryKindMap[tok.Kind],
		}
	case TTNative:
		expr, err = newExprNative(parser, tok)
	case TTNew:
		expr, err = newExprNew(parser, tok)
	case TTAsync:
		if _, err := parser.Consume(TTLeftParen); err != nil {
			return nil, err
		}

		if expr, err = newExpr(parser); err != nil {
			return nil, err
		} else if expr.Kind() != ExprKindCall {
			return nil, fmt.Errorf("async expression must be a call expression")
		}
		if _, err := parser.Consume(TTRightParen); err != nil {
			return nil, err
		}
		expr = &ExprNAry{
			start: tok,
			kind:  primaryKindMap[tok.Kind],
			Exprs: []Expr{expr},
		}
	case TTType:
		if _, err := parser.Consume(TTLeftParen); err != nil {
			return nil, err
		}

		_type, err := newType(parser)
		if err != nil {
			return nil, err
		}

		if _, err := parser.Consume(TTRightParen); err != nil {
			return nil, err
		}

		var field string
		var typeArgs []Type
		if lb, err := parser.Match(TTLeftBracket); err != nil {
			return nil, err
		} else if lb != nil {
			typeArgs, err = parseTypeList(parser)
			if err != nil {
				return nil, err
			}
		}
		if period, err := parser.Match(TTPeriod); err != nil {
			return nil, err
		} else if period != nil {
			fieldTok, err := parser.Consume(TTIdentifier)
			if err != nil {
				return nil, err
			}
			field = fieldTok.Value.(string)
		} else if typeArgs != nil {
			return nil, fmt.Errorf("type arguments on type() must be followed by '.field'")
		}

		expr = &ExprType{
			start:    tok,
			Type:     _type,
			Field:    field,
			TypeArgs: typeArgs,
		}
	case TTWait:
		if _, err := parser.Consume(TTLeftParen); err != nil {
			return nil, err
		}

		if expr, err = newExpr(parser); err != nil {
			return nil, err
		}

		if _, err := parser.Consume(TTRightParen); err != nil {
			return nil, err
		}
		expr = &ExprNAry{
			start: tok,
			kind:  primaryKindMap[tok.Kind],
			Exprs: []Expr{expr},
		}
	case TTLambda:
		expr, err = newExprLambda(parser, tok)
	case TTLeftParen:
		if expr, err = newExpr(parser); err != nil {
			return nil, err
		}
		if _, err := parser.Consume(TTRightParen); err != nil {
			return nil, err
		}
	}

	if err != nil {
		return nil, err
	}

	return expr, nil
}

// parseTypeList reads a comma-separated list of types up to and including the
// closing right bracket, which the caller has already consumed the opening
// one of. It is used by the generic field selection parse, where `[` has been
// matched and the parser is positioned at the first type.
func parseTypeList(parser SyntaxParser) ([]Type, error) {
	var types []Type
	for {
		t, err := newType(parser)
		if err != nil {
			return nil, err
		}
		types = append(types, t)

		tok, err := parser.ConsumeAny(TTComma, TTRightBracket)
		if err != nil {
			return nil, err
		}
		if tok.Kind == TTRightBracket {
			return types, nil
		}
	}
}

// newExprCall creates a new call expression.
func newExprCall(parser SyntaxParser, expr Expr) (Expr, error) {
	exprs := []Expr{expr}
	if tok, err := parser.Match(TTRightParen); err != nil {
		return nil, err
	} else if tok != nil {
		return &ExprNAry{
			kind:  ExprKindCall,
			Exprs: exprs,
		}, nil
	}

	for {
		expr, err := newExpr(parser)
		if err != nil {
			return nil, err
		}
		if tok, err := parser.Match(TTPeriodPeriodPeriod); err != nil {
			return nil, err
		} else if tok != nil {
			expr = &ExprNAry{
				start: tok,
				kind:  ExprKindSpread,
				Exprs: []Expr{expr},
			}
		}
		exprs = append(exprs, expr)

		if tok, err := parser.ConsumeAny(TTComma, TTRightParen); err != nil {
			return nil, err
		} else if tok.Kind == TTRightParen {
			break
		}
	}

	return &ExprNAry{
		kind:  ExprKindCall,
		Exprs: exprs,
	}, nil
}

// newExprNative creates a new native expression.
func newExprNative(parser SyntaxParser, start *Token) (Expr, error) {
	toks, err := parser.ConsumeAll(TTLeftParen, TTIdentifier, TTRightParen)
	if err != nil {
		return nil, err
	}
	return &ExprNative{
		start: start,
		ID:    toks[1].Value.(string),
	}, nil
}

// newExprNew creates a new new expression.
func newExprNew(parser SyntaxParser, start *Token) (Expr, error) {
	if _, err := parser.Consume(TTLeftParen); err != nil {
		return nil, err
	}

	// An allocation is the one place an unsized tail may be named, since it is
	// where the length it is missing is supplied. Everywhere else a type has to
	// say how wide it is, which is why nothing but a field may otherwise write
	// one.
	_type, err := newTailableType(parser)
	if err != nil {
		return nil, err
	}
	if tok, err := parser.ConsumeAny(TTComma, TTRightParen); err != nil {
		return nil, err
	} else if tok.Kind == TTRightParen {
		return &ExprNew{
			start: start,
			Type:  _type,
		}, nil
	}

	size, err := newExpr(parser)
	if err != nil {
		return nil, err
	}
	if tok, err := parser.ConsumeAny(TTComma, TTRightParen); err != nil {
		return nil, err
	} else if tok.Kind == TTRightParen {
		return &ExprNew{
			start: start,
			Type:  _type,
			Size:  size,
		}, nil
	}

	tail, err := newExpr(parser)
	if err != nil {
		return nil, err
	}
	if _, err := parser.Consume(TTRightParen); err != nil {
		return nil, err
	}
	return &ExprNew{
		start: start,
		Type:  _type,
		Size:  size,
		Tail:  tail,
	}, nil
}

// newExprLambda creates a new lambda expression.
func newExprLambda(parser SyntaxParser, start *Token) (Expr, error) {
	var _type *TypeFunction
	if t, err := newType(parser); err != nil {
		return nil, err
	} else if ft, ok := t.(*TypeFunction); ok {
		_type = ft
	} else {
		return nil, fmt.Errorf("lambda type must be a function type")
	}

	if _, err := parser.ConsumeAll(TTColon, TTLeftParen); err != nil {
		return nil, err
	}

	parameters, err := parseParameterNames(parser)
	if err != nil {
		return nil, err
	}

	if len(parameters) != len(_type.ParameterTypes) {
		return nil, fmt.Errorf("lambda parameter length mismatch, expected %d, got %d", len(_type.ParameterTypes), len(parameters))
	}

	body, err := newStmt(parser)
	if err != nil {
		return nil, err
	}

	return &ExprLambda{
		start:      start,
		Type:       _type,
		Parameters: parameters,
		Body:       body,
	}, nil
}

// Start implements the [Expr] interface.
func (e *ExprCast) Start() *Token {
	return e.Expr.Start()
}

// ExprKind implements the [Expr] interface.
func (e *ExprCast) Kind() ExprKind {
	return ExprKindCast
}

// Accept implements the [Expr] interface.
func (e *ExprCast) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprCast(e)
}

// ResolvedType implements the [Expr] interface. A cast evaluates to the type
// it names.
func (e *ExprCast) ResolvedType() Type {
	return e.Type
}

// Start implements the [Expr] interface.
func (e *ExprField) Start() *Token {
	return e.Expr.Start()
}

// ExprKind implements the [Expr] interface.
func (e *ExprField) Kind() ExprKind {
	return ExprKindField
}

// Accept implements the [Expr] interface.
func (e *ExprField) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprField(e)
}

// ResolvedType implements the [Expr] interface.
func (e *ExprField) ResolvedType() Type {
	return e.ComputedType
}

// Start implements the [Expr] interface.
func (e *ExprLambda) Start() *Token {
	return e.start
}

// ExprKind implements the [Expr] interface.
func (e *ExprLambda) Kind() ExprKind {
	return ExprKindLambda
}

// Accept implements the [Expr] interface.
func (e *ExprLambda) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprLambda(e)
}

// ResolvedType implements the [Expr] interface. A lambda evaluates to the
// function type it declares.
func (e *ExprLambda) ResolvedType() Type {
	return e.Type
}

// Start implements the [Expr] interface.
func (e *ExprNAry) Start() *Token {
	if e.start != nil {
		return e.start
	}
	return e.Exprs[0].Start()
}

// ExprKind implements the [Expr] interface.
func (e *ExprNAry) Kind() ExprKind {
	return e.kind
}

// Accept implements the [Expr] interface.
func (e *ExprNAry) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprNAry(e)
}

// ResolvedType implements the [Expr] interface.
func (e *ExprNAry) ResolvedType() Type {
	return e.ComputedType
}

// Start implements the [Expr] interface.
func (e *ExprNative) Start() *Token {
	return e.start
}

// ExprKind implements the [Expr] interface.
func (e *ExprNative) Kind() ExprKind {
	return ExprKindNative
}

// Accept implements the [Expr] interface.
func (e *ExprNative) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprNative(e)
}

// ResolvedType implements the [Expr] interface.
func (e *ExprNative) ResolvedType() Type {
	return e.ComputedType
}

// Start implements the [Expr] interface.
func (e *ExprNew) Start() *Token {
	return e.start
}

// ExprKind implements the [Expr] interface.
func (e *ExprNew) Kind() ExprKind {
	return ExprKindNew
}

// Accept implements the [Expr] interface.
func (e *ExprNew) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprNew(e)
}

// ResolvedType implements the [Expr] interface. new evaluates to a pointer to
// the type it allocates, not to that type: what it allocates lies in the heap
// rather than where the expression was written, so the one word it leaves is the
// address of the allocation and never the allocation itself.
//
// A class is a value, so a variable of one is storage of its own and a second
// name for an allocation cannot be a variable of that type. It is a pointer, and
// saying so here is what makes every subtype relation reachable from a new: those
// are written between pointers, and this is the pointer they are written between.
func (e *ExprNew) ResolvedType() Type {
	return NewWrappedType(TypeKindPointer, e.Type)
}

// Start implements the [Expr] interface.
func (e *ExprSymbol) Start() *Token {
	return e.start
}

// ExprKind implements the [Expr] interface.
func (e *ExprSymbol) Kind() ExprKind {
	return e.kind
}

// Accept implements the [Expr] interface.
func (e *ExprSymbol) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprSymbol(e)
}

// ResolvedType implements the [Expr] interface. A literal is typed by the token
// it was written as; a name is whatever it was declared as.
func (e *ExprSymbol) ResolvedType() Type {
	switch e.kind {
	case ExprKindLiteralInteger:
		return TypeInt
	case ExprKindLiteralUint:
		return TypeUint
	case ExprKindLiteralFloat:
		return TypeFloat
	case ExprKindLiteralChar:
		return TypeChar
	case ExprKindLiteralString:
		return TypeString
	case ExprKindLiteralBoolean:
		return TypeBool
	case ExprKindLiteralNull:
		return TypeNull
	}
	return e.ComputedType
}

// Start implements the [Expr] interface.
func (e *ExprType) Start() *Token {
	return e.start
}

// ExprKind implements the [Expr] interface.
func (e *ExprType) Kind() ExprKind {
	return ExprKindType
}

// Accept implements the [Expr] interface.
func (e *ExprType) Accept(visitor ExprVisitor) error {
	return visitor.VisitExprType(e)
}

// ResolvedType implements the [Expr] interface. Naming a type alone evaluates to
// the type itself; naming one of its static fields evaluates to that field.
func (e *ExprType) ResolvedType() Type {
	if e.Field == "" {
		return TypeType
	}
	return e.ComputedType
}
