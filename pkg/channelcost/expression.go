package channelcost

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/tidwall/gjson"
)

// Expression retains the upstream contract, including every conditional branch.
// Task expressions return balance units; token/fixed expressions use the host's
// million-unit convention. These representations must never be interchanged.
type Expression struct {
	Text        string                     `json:"text"`
	Task        bool                       `json:"task,omitempty"`
	UsageSchema map[string]json.RawMessage `json:"usage_schema,omitempty"`
}

func ParseExpression(text string, schema map[string]json.RawMessage) (*Expression, error) {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "v1:"))
	if len(text) == 0 || len(text) > 16384 {
		return nil, errors.New("Invalid upstream billing expression")
	}
	tree, err := parser.Parse(text)
	if err != nil {
		return nil, errors.New("Invalid upstream billing expression")
	}
	nodes := 0
	task := false
	tokens := false
	var check func(ast.Node, int) error
	check = func(node ast.Node, depth int) error {
		nodes++
		if nodes > 2048 || depth > 64 {
			return errors.New("Upstream expression is too large")
		}
		var children []ast.Node
		switch n := node.(type) {
		case *ast.IntegerNode:
			if n.Value < 0 {
				return errors.New("Invalid upstream billing expression")
			}
		case *ast.FloatNode:
			if n.Value < 0 || math.IsNaN(n.Value) || math.IsInf(n.Value, 0) {
				return errors.New("Invalid upstream billing expression")
			}
		case *ast.StringNode:
			if len(n.Value) > 512 {
				return errors.New("Upstream expression is too large")
			}
		case *ast.BoolNode, *ast.NilNode:
		case *ast.IdentifierNode:
			switch n.Value {
			case "p", "c", "len", "cr", "cc", "cc1h", "img", "img_cr", "img_o", "ai", "ao", "image_count":
				tokens = true
			default:
				return errors.New("Unsupported upstream token variable")
			}
		case *ast.UnaryNode:
			if n.Operator != "!" && n.Operator != "not" {
				return errors.New("Unsupported upstream billing expression")
			}
			children = []ast.Node{n.Node}
		case *ast.BinaryNode:
			switch n.Operator {
			case "+", "*", "/", "==", "!=", "<", "<=", ">", ">=", "&&", "||", "and", "or":
			default:
				return errors.New("Unsupported upstream billing expression")
			}
			children = []ast.Node{n.Left, n.Right}
		case *ast.ConditionalNode:
			children = []ast.Node{n.Cond, n.Exp1, n.Exp2}
		case *ast.CallNode:
			fn, ok := n.Callee.(*ast.IdentifierNode)
			if !ok {
				return errors.New("Unsupported upstream billing expression")
			}
			argc := 1
			switch fn.Value {
			case "tier", "has", "max", "min":
				argc = 2
			case "fixed", "param", "header", "hour", "minute", "weekday", "month", "day", "abs", "ceil", "floor":
			case "u":
				task = true
				if len(n.Arguments) != 1 {
					return errors.New("Invalid upstream usage reference")
				}
				key, ok := n.Arguments[0].(*ast.StringNode)
				if !ok || key.Value == "" || strings.TrimSpace(key.Value) != key.Value || len(key.Value) > 128 {
					return errors.New("Invalid upstream usage reference")
				}
				field, ok := schema[key.Value]
				if !ok {
					return errors.New("Upstream expression usage schema is missing or invalid")
				}
				kind := gjson.GetBytes(field, "type").String()
				values := gjson.GetBytes(field, "enum").Array()
				if len(values) > 0 {
					if len(values) > 64 {
						return errors.New("Upstream expression usage schema is missing or invalid")
					}
					for _, value := range values {
						if value.Type != gjson.String || value.String() == "" || len(value.String()) > 128 {
							return errors.New("Upstream expression usage schema is missing or invalid")
						}
					}
				} else if kind == "number" {
					switch gjson.GetBytes(field, "unit").String() {
					case "second", "count", "token", "credit":
					default:
						return errors.New("Upstream expression usage schema is missing or invalid")
					}
				} else if kind != "boolean" {
					return errors.New("Upstream expression usage schema is missing or invalid")
				}
			default:
				return errors.New("Unsupported upstream billing function")
			}
			if len(n.Arguments) != argc {
				return errors.New("Invalid upstream billing expression")
			}
			children = n.Arguments
		default:
			return errors.New("Unsupported upstream billing expression")
		}
		for _, child := range children {
			if err := check(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err = check(tree.Node, 0); err != nil {
		return nil, err
	}
	if task && tokens {
		return nil, errors.New("Upstream expression mixes task and token billing units")
	}
	if err = validateCostTree(tree.Node, schema); err != nil {
		return nil, err
	}
	if _, err = billingexpr.CompileFromCache(text); err != nil {
		return nil, errors.New("Invalid upstream billing expression")
	}
	return &Expression{Text: text, Task: task, UsageSchema: schema}, nil
}

func containsTier(node ast.Node) bool {
	return ast.Find(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallNode)
		if !ok {
			return false
		}
		fn, ok := call.Callee.(*ast.IdentifierNode)
		return ok && (fn.Value == "tier" || fn.Value == "fixed")
	}) != nil
}
func validateCostTree(node ast.Node, schema map[string]json.RawMessage) error {
	switch n := node.(type) {
	case *ast.ConditionalNode:
		if containsTier(n.Cond) {
			break
		}
		if err := validateCostTree(n.Exp1, schema); err != nil {
			return err
		}
		return validateCostTree(n.Exp2, schema)
	case *ast.BinaryNode:
		if n.Operator != "*" {
			break
		}
		if containsTier(n.Left) && !containsTier(n.Right) {
			if err := validateCostTree(n.Left, schema); err != nil {
				return err
			}
			return validatePriceTerm(n.Right, schema, true)
		}
		if containsTier(n.Right) && !containsTier(n.Left) {
			if err := validateCostTree(n.Right, schema); err != nil {
				return err
			}
			return validatePriceTerm(n.Left, schema, true)
		}
	case *ast.CallNode:
		fn, ok := n.Callee.(*ast.IdentifierNode)
		if !ok || fn.Value != "tier" || len(n.Arguments) != 2 {
			break
		}
		label, ok := n.Arguments[0].(*ast.StringNode)
		if !ok || label.Value == "" || len(label.Value) > 128 {
			break
		}
		return validatePriceTerm(n.Arguments[1], schema, false)
	}
	return errors.New("Unsupported upstream billing expression")
}
func validatePriceTerm(node ast.Node, schema map[string]json.RawMessage, multiplier bool) error {
	if n, ok := literalNumber(node); ok && n >= 0 && n <= 1e12 {
		return nil
	}
	switch n := node.(type) {
	case *ast.IdentifierNode:
		if multiplier && n.Value != "image_count" {
			break
		}
		return nil // Identifier allowlist is checked by ParseExpression.
	case *ast.BinaryNode:
		if n.Operator != "+" && n.Operator != "*" && n.Operator != "/" {
			break
		}
		if n.Operator == "/" {
			den, ok := literalNumber(n.Right)
			if !ok || !Positive(den) {
				break
			}
			return validatePriceTerm(n.Left, schema, multiplier)
		}
		if err := validatePriceTerm(n.Left, schema, multiplier); err != nil {
			return err
		}
		return validatePriceTerm(n.Right, schema, multiplier)
	case *ast.ConditionalNode:
		if containsTier(n.Cond) {
			break
		}
		if err := validatePriceTerm(n.Exp1, schema, multiplier); err != nil {
			return err
		}
		return validatePriceTerm(n.Exp2, schema, multiplier)
	case *ast.CallNode:
		fn, ok := n.Callee.(*ast.IdentifierNode)
		if !ok {
			break
		}
		if fn.Value == "fixed" && !multiplier {
			if len(n.Arguments) == 1 {
				_, ok := literalNumber(n.Arguments[0])
				if ok {
					return nil
				}
			}
			break
		}
		if fn.Value == "u" && !multiplier {
			key, ok := n.Arguments[0].(*ast.StringNode)
			if ok && gjson.GetBytes(schema[key.Value], "type").String() == "number" {
				return nil
			}
			break
		}
		if fn.Value == "max" || fn.Value == "min" || fn.Value == "abs" || fn.Value == "ceil" || fn.Value == "floor" {
			for _, arg := range n.Arguments {
				if err := validatePriceTerm(arg, schema, multiplier); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return errors.New("Upstream expression price must be finite and non-negative")
}

type expressionScaleVisitor struct {
	factor float64
	err    error
}

func (v *expressionScaleVisitor) Visit(node *ast.Node) {
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	fn, ok := call.Callee.(*ast.IdentifierNode)
	if !ok || fn.Value != "tier" || len(call.Arguments) != 2 {
		return
	}
	if fixed, ok := call.Arguments[1].(*ast.CallNode); ok {
		ident, ok := fixed.Callee.(*ast.IdentifierNode)
		if ok && ident.Value == "fixed" && len(fixed.Arguments) == 1 {
			amount, ok := literalNumber(fixed.Arguments[0])
			if !ok || !(Rates{Input: amount * v.factor}).Valid() {
				v.err = errors.New("Invalid channel cost conversion")
				return
			}
			fixed.Arguments[0] = &ast.FloatNode{Value: amount * v.factor}
			return
		}
	}
	call.Arguments[1] = scaleCostTerm(call.Arguments[1], v.factor)
}

// Scale changes only price leaves. Thresholds, durations, quantities, dates,
// request multipliers and the fixed() million-unit convention stay intact.
func (e *Expression) Scale(factor float64) (string, error) {
	if !Positive(factor) {
		return "", errors.New("Invalid channel cost conversion")
	}
	tree, err := parser.Parse(e.Text)
	if err != nil {
		return "", errors.New("Invalid upstream billing expression")
	}
	visitor := &expressionScaleVisitor{factor: factor}
	ast.Walk(&tree.Node, visitor)
	if visitor.err != nil {
		return "", visitor.err
	}
	text := tree.Node.String()
	if _, err = billingexpr.CompileFromCache(text); err != nil {
		return "", fmt.Errorf("Invalid converted billing expression")
	}
	return text, nil
}

type taskExpressionVisitor struct{ err error }

func (v *taskExpressionVisitor) Visit(node *ast.Node) {
	switch n := (*node).(type) {
	case *ast.IdentifierNode:
		switch n.Value {
		case "p", "c", "len", "cr", "cc", "cc1h", "img", "img_cr", "img_o", "ai", "ao":
			v.err = errors.New("Token expression has no compatible task usage meter")
		case "image_count":
			*node = &ast.CallNode{Callee: &ast.IdentifierNode{Value: "u"}, Arguments: []ast.Node{&ast.StringNode{Value: "image_count"}}}
		}
	case *ast.CallNode:
		fn, ok := n.Callee.(*ast.IdentifierNode)
		if !ok {
			return
		}
		if fn.Value == "fixed" && len(n.Arguments) == 1 {
			*node = n.Arguments[0]
		}
		if fn.Value == "param" || fn.Value == "header" {
			v.err = errors.New("Task cost expression requires unsupported request context")
		}
	}
}

// TaskExpression removes fixed()'s million-unit convention for task billing.
// Per-image quantities remain separate usage facts, never extra fixed calls.
func TaskExpression(text string) (string, error) {
	tree, err := parser.Parse(text)
	if err != nil {
		return "", errors.New("Invalid upstream billing expression")
	}
	visitor := &taskExpressionVisitor{}
	ast.Walk(&tree.Node, visitor)
	if visitor.err != nil {
		return "", visitor.err
	}
	result := tree.Node.String()
	if _, err = billingexpr.CompileFromCache(result); err != nil {
		return "", errors.New("Invalid converted billing expression")
	}
	return result, nil
}

func scaleCostTerm(node ast.Node, factor float64) ast.Node {
	if value, ok := literalNumber(node); ok {
		return &ast.FloatNode{Value: value * factor}
	}
	switch n := node.(type) {
	case *ast.BinaryNode:
		if n.Operator == "+" {
			n.Left = scaleCostTerm(n.Left, factor)
			n.Right = scaleCostTerm(n.Right, factor)
			return n
		}
		if n.Operator == "*" {
			if value, ok := literalNumber(n.Right); ok {
				n.Right = &ast.FloatNode{Value: value * factor}
				return n
			}
			if value, ok := literalNumber(n.Left); ok {
				n.Left = &ast.FloatNode{Value: value * factor}
				return n
			}
		}
		if n.Operator == "/" {
			n.Left = scaleCostTerm(n.Left, factor)
			return n
		}
	case *ast.ConditionalNode:
		n.Exp1 = scaleCostTerm(n.Exp1, factor)
		n.Exp2 = scaleCostTerm(n.Exp2, factor)
		return n
	}
	return &ast.BinaryNode{Operator: "*", Left: node, Right: &ast.FloatNode{Value: factor}}
}

// ValidateTaskMeter refuses a matching name with a different physical unit or
// enum vocabulary; an upstream "credit" must never be billed as one token.
func ValidateTaskMeter(text string, expected, actual map[string]json.RawMessage) error {
	for key := range billingexpr.UsedUsageKeys(text) {
		want, exists := expected[key]
		if !exists {
			continue
		}
		have, exists := actual[key]
		if !exists {
			return errors.New("Task usage meter does not match upstream billing units")
		}
		if gjson.GetBytes(want, "type").String() != gjson.GetBytes(have, "type").String() || gjson.GetBytes(want, "unit").String() != gjson.GetBytes(have, "unit").String() {
			return errors.New("Task usage meter does not match upstream billing units")
		}
		enums := gjson.GetBytes(want, "enum").Array()
		if len(enums) > 0 {
			allowed := map[string]bool{}
			for _, value := range enums {
				allowed[value.String()] = true
			}
			choices := gjson.GetBytes(have, "enum").Array()
			if len(choices) == 0 {
				return errors.New("Task usage meter does not match upstream billing units")
			}
			for _, value := range choices {
				if !allowed[value.String()] {
					return errors.New("Task usage meter does not match upstream billing units")
				}
			}
		}
	}
	return nil
}

// ModelPriceRule adds a documented condition that an upstream pricing endpoint
// omits. Placeholders always bind to this exact model's freshly synchronized
// base prices; they cannot read another model, channel or arbitrary source data.
type ModelPriceRule struct {
	Expression  string                     `json:"expression"`
	UsageSchema map[string]json.RawMessage `json:"usage_schema"`
	DocumentURL string                     `json:"document_url"`
}

func (r ModelPriceRule) Resolve(price Price) (*Expression, error) {
	if len(r.Expression) > 16384 || len(r.DocumentURL) > 2048 {
		return nil, errors.New("Invalid documented model price rules")
	}
	document, err := url.Parse(r.DocumentURL)
	if err != nil || document.Scheme != "https" || document.Host == "" || document.User != nil || document.Fragment != "" {
		return nil, errors.New("Invalid documented model price rules")
	}
	text := r.Expression
	for key, value := range map[string]*float64{"{{request_price}}": price.RequestPrice, "{{input_per_million}}": &price.OffPeak.Input, "{{output_per_million}}": &price.OffPeak.Output} {
		if !strings.Contains(text, key) {
			continue
		}
		if value == nil || !((Rates{Input: *value}).Valid()) {
			return nil, errors.New("Documented model price rule has no synchronized base price")
		}
		text = strings.ReplaceAll(text, key, strconv.FormatFloat(*value, 'g', 17, 64))
	}
	return ParseExpression(text, r.UsageSchema)
}
