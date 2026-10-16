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

type Tier struct {
	Label      string `json:"label"`
	MaxContext int64  `json:"max_context,omitempty"`
	Rates      Rates  `json:"rates"`
}

// ParseTokenTiers accepts only linear token prices and increasing context tiers.
// Request-dependent, negative and unknown pricing remains unavailable.
// Flat fixed prices are recognized separately by ParseRequestPrice.
func ParseTokenTiers(expression string) ([]Tier, error) {
	expression = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expression), "v1:"))
	if len(expression) > 16384 {
		return nil, errors.New("Upstream expression is too large")
	}
	tree, err := parser.Parse(expression)
	if err != nil {
		return nil, errors.New("Invalid upstream billing expression")
	}
	node := tree.Node
	var tiers []Tier
	var previous int64
	for len(tiers) < 32 {
		var maximum int64
		leaf := node
		conditional, conditionalOK := node.(*ast.ConditionalNode)
		if conditionalOK {
			condition, ok := conditional.Cond.(*ast.BinaryNode)
			if !ok || (condition.Operator != "<=" && condition.Operator != "<") {
				return nil, errors.New("Unsupported upstream tier condition")
			}
			variable, ok := condition.Left.(*ast.IdentifierNode)
			n, number := literalNumber(condition.Right)
			if !ok || variable.Value != "len" || !number || n != math.Trunc(n) || n > 1e9 {
				return nil, errors.New("Unsupported upstream tier condition")
			}
			maximum = int64(n)
			if condition.Operator == "<" {
				maximum--
			}
			if maximum <= previous {
				return nil, errors.New("Upstream context tiers must increase")
			}
			leaf = conditional.Exp1
		}
		label := "standard"
		if call, ok := leaf.(*ast.CallNode); ok {
			fn, ok := call.Callee.(*ast.IdentifierNode)
			if !ok || fn.Value != "tier" || len(call.Arguments) != 2 {
				return nil, errors.New("Unsupported upstream billing expression")
			}
			name, ok := call.Arguments[0].(*ast.StringNode)
			if !ok {
				return nil, errors.New("Unsupported upstream billing expression")
			}
			label = strings.TrimSpace(name.Value)
			leaf = call.Arguments[1]
		}
		coefficients := map[string]float64{}
		if err := linearTokenRates(leaf, coefficients); err != nil {
			return nil, err
		}
		if _, ok := coefficients["p"]; !ok {
			return nil, errors.New("Upstream expression has no input price")
		}
		rates := Rates{Input: coefficients["p"], Output: coefficients["c"], CacheRead: coefficients["cr"], CacheWrite: coefficients["cc"], CacheWrite1h: coefficients["cc1h"]}
		// Omitted categories are charged at their fallback token rate by the host.
		if _, ok := coefficients["cr"]; !ok {
			rates.CacheRead = rates.Input
		}
		if _, ok := coefficients["cc"]; !ok {
			rates.CacheWrite = rates.Input
		}
		if _, ok := coefficients["cc1h"]; !ok {
			rates.CacheWrite1h = rates.CacheWrite
		}
		if !rates.Valid() {
			return nil, errors.New("Invalid upstream token prices")
		}
		tiers = append(tiers, Tier{Label: label, MaxContext: maximum, Rates: rates})
		if !conditionalOK {
			return tiers, nil
		}
		previous = maximum
		node = conditional.Exp2
	}
	return nil, errors.New("Too many upstream price tiers")
}
func literalNumber(node ast.Node) (float64, bool) {
	switch n := node.(type) {
	case *ast.IntegerNode:
		return float64(n.Value), n.Value >= 0
	case *ast.FloatNode:
		return n.Value, n.Value >= 0 && !math.IsNaN(n.Value) && !math.IsInf(n.Value, 0)
	}
	return 0, false
}
func linearTokenRates(node ast.Node, coefficients map[string]float64) error {
	if sum, ok := node.(*ast.BinaryNode); ok && sum.Operator == "+" {
		if err := linearTokenRates(sum.Left, coefficients); err != nil {
			return err
		}
		return linearTokenRates(sum.Right, coefficients)
	}
	coefficient := 1.0
	variable, ok := node.(*ast.IdentifierNode)
	if !ok {
		term, valid := node.(*ast.BinaryNode)
		if !valid || term.Operator != "*" {
			return errors.New("Unsupported upstream billing expression")
		}
		variable, ok = term.Left.(*ast.IdentifierNode)
		var number bool
		coefficient, number = literalNumber(term.Right)
		if !ok || !number {
			variable, ok = term.Right.(*ast.IdentifierNode)
			coefficient, number = literalNumber(term.Left)
		}
		if !ok || !number {
			return errors.New("Unsupported upstream billing expression")
		}
	}
	switch variable.Value {
	case "p", "c", "cr", "cc", "cc1h":
	default:
		return errors.New("Unsupported upstream token variable")
	}
	coefficients[variable.Value] += coefficient
	return nil
}
func (r Rates) Scale(factor float64) Rates {
	return Rates{r.Input * factor, r.Output * factor, r.CacheRead * factor, r.CacheWrite * factor, r.CacheWrite1h * factor}
}
func (r Rates) Expression() string {
	return fmt.Sprintf("p * %.17g + c * %.17g + cr * %.17g + cc * %.17g + cc1h * %.17g", r.Input, r.Output, r.CacheRead, r.CacheWrite, r.CacheWrite1h)
}
func TierExpression(tiers []Tier, factor float64) string {
	var out strings.Builder
	for _, tier := range tiers {
		if tier.MaxContext > 0 {
			fmt.Fprintf(&out, "len <= %d ? ", tier.MaxContext)
		}
		fmt.Fprintf(&out, "tier(%s, %s)", strconv.Quote(tier.Label), tier.Rates.Scale(factor).Expression())
		if tier.MaxContext > 0 {
			out.WriteString(" : ")
		}
	}
	return out.String()
}
