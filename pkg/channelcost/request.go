package channelcost

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// RequestExpression is the sole representation for a flat per-request charge.
// fixed() already supplies the engine's million-unit scaling; do not apply it here.
func RequestExpression(amount float64) (string, error) {
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount*1e6, 0) {
		return "", errors.New("Invalid upstream request price")
	}
	if amount == 0 {
		amount = 0
	}
	return fmt.Sprintf("tier(\"request\", fixed(%s))", strconv.FormatFloat(amount, 'g', -1, 64)), nil
}

// ParseRequestPrice recognizes fixed request leaves without evaluating upstream
// code. Other expressions continue through the token-tier parser.
func ParseRequestPrice(expression string) (*float64, error) {
	expression = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expression), "v1:"))
	if len(expression) > 16384 {
		return nil, errors.New("Upstream expression is too large")
	}
	tree, err := parser.Parse(expression)
	if err != nil {
		return nil, errors.New("Invalid upstream billing expression")
	}
	node := tree.Node
	if call, ok := node.(*ast.CallNode); ok {
		fn, ok := call.Callee.(*ast.IdentifierNode)
		if ok && fn.Value == "tier" && len(call.Arguments) == 2 {
			if _, ok := call.Arguments[0].(*ast.StringNode); !ok {
				return nil, errors.New("Invalid upstream billing expression")
			}
			node = call.Arguments[1]
		}
	}
	call, ok := node.(*ast.CallNode)
	if !ok {
		return nil, nil
	}
	fn, ok := call.Callee.(*ast.IdentifierNode)
	if !ok || fn.Value != "fixed" {
		return nil, nil
	}
	if len(call.Arguments) != 1 {
		return nil, errors.New("Invalid upstream request price")
	}
	amount, ok := literalNumber(call.Arguments[0])
	if !ok {
		return nil, errors.New("Invalid upstream request price")
	}
	if _, err := RequestExpression(amount); err != nil {
		return nil, err
	}
	return &amount, nil
}
