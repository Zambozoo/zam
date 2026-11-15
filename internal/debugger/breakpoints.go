package debugger

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/google/go-dap"
	codegen "github.com/zambozoo/zam/internal/4_codegen"
)

// breakpointRule decides whether a stop at a breakpoint stands: after how many
// hits, on what condition, and whether it only logs.
type breakpointRule struct {
	condition, hitCondition, logMessage string
	hits                                int
}

// newBreakpointRule answers the rule of a breakpoint, or nil for one that
// always stops.
func newBreakpointRule(b dap.SourceBreakpoint) *breakpointRule {
	if b.Condition == "" && b.HitCondition == "" && b.LogMessage == "" {
		return nil
	}
	return &breakpointRule{
		condition:    strings.TrimSpace(b.Condition),
		hitCondition: strings.TrimSpace(b.HitCondition),
		logMessage:   b.LogMessage,
	}
}

// ruleAt answers the rule of the breakpoint at offset, if it has one. The
// caller holds the lock.
func (s *server) ruleAt(offset uint64) *breakpointRule {
	for _, rules := range s.rules {
		if rule, ok := rules[offset]; ok {
			return rule
		}
	}
	return nil
}

// stands reports whether a stop at a breakpoint of rule stands. A logpoint
// writes its message and never does. A rule that cannot be decided stands,
// with output saying why.
func (s *server) stands(rule *breakpointRule) bool {
	s.lock.Lock()
	rule.hits++
	hits := rule.hits
	s.lock.Unlock()

	if rule.hitCondition != "" {
		holds, err := hitHolds(rule.hitCondition, hits)
		if err != nil {
			s.output("console", fmt.Sprintf("hit condition %q: %v\n", rule.hitCondition, err))
			return true
		}
		if !holds {
			return false
		}
	}
	if rule.condition != "" {
		holds, err := s.holds(rule.condition)
		if err != nil {
			s.output("console", fmt.Sprintf("condition %q: %v\n", rule.condition, err))
			return true
		}
		if !holds {
			return false
		}
	}
	if rule.logMessage != "" {
		s.output("console", s.interpolate(rule.logMessage)+"\n")
		return false
	}
	return true
}

// hitHolds reports whether a hit condition, a count with an optional
// comparison or % before it, holds of the hits so far. A bare count is the
// hit it stops on.
func hitHolds(condition string, hits int) (bool, error) {
	op := ""
	for _, candidate := range []string{">=", "<=", "==", ">", "<", "%"} {
		if strings.HasPrefix(condition, candidate) {
			op = candidate
			break
		}
	}
	n, err := strconv.Atoi(strings.TrimSpace(condition[len(op):]))
	if err != nil {
		return false, errors.New("want a count, after one of == > >= < <= %")
	}
	switch op {
	case ">=":
		return hits >= n, nil
	case "<=":
		return hits <= n, nil
	case ">":
		return hits > n, nil
	case "<":
		return hits < n, nil
	case "%":
		if n <= 0 {
			return false, errors.New("want a positive count after %")
		}
		return hits%n == 0, nil
	}
	return hits == n, nil
}

// operandKind is what an operand of a condition is.
type operandKind int

const (
	operandOther operandKind = iota
	operandNumber
	operandString
	operandBool
	operandNull
)

// operand is a side of a condition: a value it can compare.
type operand struct {
	kind   operandKind
	number *big.Float
	text   string
}

// comparisons are the comparisons a condition may make, longest first so that
// one is not read as the start of another.
var comparisons = []string{"==", "!=", "<=", ">=", "<", ">"}

// holds reports whether condition holds where the program stopped: a path,
// true when its value is true, nonzero or not null, or a path compared with
// a path or a literal.
func (s *server) holds(condition string) (bool, error) {
	left, op, right := splitComparison(condition)
	a, err := s.operand(left)
	if err != nil {
		return false, err
	}
	if op == "" {
		switch a.kind {
		case operandBool:
			return a.text == "true", nil
		case operandNumber:
			return a.number.Sign() != 0, nil
		case operandNull:
			return false, nil
		}
		return true, nil
	}
	b, err := s.operand(right)
	if err != nil {
		return false, err
	}
	return compare(a, op, b)
}

// splitComparison splits condition at its comparison outside quotes, if it
// has one.
func splitComparison(condition string) (string, string, string) {
	quoted := false
	for i := 0; i < len(condition); i++ {
		switch {
		case condition[i] == '\\' && quoted:
			i++
		case condition[i] == '"':
			quoted = !quoted
		case !quoted:
			for _, op := range comparisons {
				if strings.HasPrefix(condition[i:], op) {
					return condition[:i], op, condition[i+len(op):]
				}
			}
		}
	}
	return condition, "", ""
}

// operand reads text as a literal, or as a path to a value.
func (s *server) operand(text string) (operand, error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return operand{}, errors.New("want a value on each side")
	case text == "true", text == "false":
		return operand{kind: operandBool, text: text}, nil
	case text == nullText:
		return operand{kind: operandNull}, nil
	case strings.HasPrefix(text, `"`):
		unquoted, err := strconv.Unquote(text)
		if err != nil {
			return operand{}, fmt.Errorf("%s is not a quoted string", text)
		}
		return operand{kind: operandString, text: unquoted}, nil
	case strings.ContainsAny(text[:1], "0123456789-+."):
		number, _, err := big.ParseFloat(text, 0, 128, big.ToNearestEven)
		if err != nil {
			return operand{}, fmt.Errorf("%s is not a number", text)
		}
		return operand{kind: operandNumber, number: number}, nil
	}

	c, err := s.resolve(text, 0)
	if err != nil {
		return operand{}, err
	}
	return valueOperand(c), nil
}

// valueOperand is the operand a variable's value is.
func valueOperand(c child) operand {
	value := c.Value
	if value == nullText {
		return operand{kind: operandNull}
	}
	switch c.typ.Kind {
	case codegen.ValueKindBool:
		return operand{kind: operandBool, text: value}
	case codegen.ValueKindInt, codegen.ValueKindUint, codegen.ValueKindFloat, codegen.ValueKindPosit, codegen.ValueKindByte:
		field, _, _ := strings.Cut(value, " ")
		if number, _, err := big.ParseFloat(field, 10, 128, big.ToNearestEven); err == nil {
			return operand{kind: operandNumber, number: number}
		}
	case codegen.ValueKindStr:
		if unquoted, err := strconv.Unquote(strings.TrimSuffix(value, "…")); err == nil {
			return operand{kind: operandString, text: unquoted}
		}
	case codegen.ValueKindChar:
		if r, _, _, err := strconv.UnquoteChar(strings.Trim(value, "'"), '\''); err == nil {
			return operand{kind: operandString, text: string(r)}
		}
	}
	return operand{kind: operandOther, text: value}
}

// compare answers a op b. Numbers and strings are ordered; anything else is
// only equal or not.
func compare(a operand, op string, b operand) (bool, error) {
	var order int
	switch {
	case a.kind == operandNumber && b.kind == operandNumber:
		order = a.number.Cmp(b.number)
	case a.kind == operandString && b.kind == operandString:
		order = strings.Compare(a.text, b.text)
	case op == "==":
		return a.kind == b.kind && a.text == b.text, nil
	case op == "!=":
		return a.kind != b.kind || a.text != b.text, nil
	default:
		return false, fmt.Errorf("%s cannot order these", op)
	}
	switch op {
	case "==":
		return order == 0, nil
	case "!=":
		return order != 0, nil
	case "<":
		return order < 0, nil
	case "<=":
		return order <= 0, nil
	case ">":
		return order > 0, nil
	}
	return order >= 0, nil
}

// interpolate writes message with each {path} in it replaced by its value.
func (s *server) interpolate(message string) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(message, '{')
		if open < 0 {
			break
		}
		end := strings.IndexByte(message[open:], '}')
		if end < 0 {
			break
		}
		b.WriteString(message[:open])
		if v, err := s.evaluate(message[open+1:open+end], 0); err == nil {
			b.WriteString(v.Value)
		} else {
			b.WriteString("<error: " + err.Error() + ">")
		}
		message = message[open+end+1:]
	}
	b.WriteString(message)
	return b.String()
}
