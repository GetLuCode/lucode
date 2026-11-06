package flows

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
)

// Expressions are CEL: they can read the run's data and compute a value, but they have
// no way to touch the disk, the network or the hub, and a cost limit stops runaway ones.
// What they can read: card, event, run and nodes (nodes.<id>.<output field>).

const exprCostLimit = 100_000

var (
	exprEnvOnce sync.Once
	exprEnv     *cel.Env
	exprEnvErr  error
	templateRe  = regexp.MustCompile(`\{\{(.*?)\}\}`)
)

func env() (*cel.Env, error) {
	exprEnvOnce.Do(func() {
		exprEnv, exprEnvErr = cel.NewEnv(
			cel.Variable("card", cel.DynType),
			cel.Variable("event", cel.DynType),
			cel.Variable("run", cel.DynType),
			cel.Variable("nodes", cel.DynType),
		)
	})
	return exprEnv, exprEnvErr
}

func compile(expr string) (cel.Program, error) {
	e, err := env()
	if err != nil {
		return nil, err
	}
	ast, iss := e.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, errors.New(strings.TrimSpace(iss.Err().Error()))
	}
	return e.Program(ast, cel.CostLimit(exprCostLimit))
}

// CheckExpr reports whether an expression compiles.
func CheckExpr(expr string) error {
	_, err := compile(expr)
	return err
}

// CheckTemplate reports whether every {{ expression }} of a text compiles.
func CheckTemplate(text string) error {
	for _, m := range templateRe.FindAllStringSubmatch(text, -1) {
		if err := CheckExpr(strings.TrimSpace(m[1])); err != nil {
			return fmt.Errorf("{{%s}}: %w", m[1], err)
		}
	}
	return nil
}

// Scope is the data an expression can read.
type Scope map[string]any

func evalExpr(expr string, scope Scope) (ref.Val, error) {
	p, err := compile(expr)
	if err != nil {
		return nil, err
	}
	out, _, err := p.Eval(map[string]any(scope))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EvalBool evaluates a condition.
func EvalBool(expr string, scope Scope) (bool, error) {
	v, err := evalExpr(expr, scope)
	if err != nil {
		return false, fmt.Errorf("%s: %w", expr, err)
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("%s: gives %v, not true or false", expr, v.Value())
	}
	return b, nil
}

// EvalString evaluates an expression into text.
func EvalString(expr string, scope Scope) (string, error) {
	v, err := evalExpr(expr, scope)
	if err != nil {
		return "", fmt.Errorf("%s: %w", expr, err)
	}
	return fmt.Sprint(v.Value()), nil
}

// RenderTemplate fills the {{ expressions }} of a text. It makes one pass: a value that
// happens to contain {{ }}, such as a card's title, stays text and is never evaluated.
func RenderTemplate(text string, scope Scope) (string, error) {
	var firstErr error
	out := templateRe.ReplaceAllStringFunc(text, func(m string) string {
		s, err := EvalString(strings.TrimSpace(m[2:len(m)-2]), scope)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return s
	})
	return out, firstErr
}
