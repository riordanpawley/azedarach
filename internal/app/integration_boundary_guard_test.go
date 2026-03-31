package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestIntegrationBoundaryGuard_NoDirectTmuxOrGitExecInApp(t *testing.T) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read app dir: %v", err)
	}

	fset := token.NewFileSet()
	var violations []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			if !isExecCommandCall(call.Fun) || len(call.Args) == 0 {
				return true
			}

			cmdName, ok := stringLiteral(call.Args[0])
			if !ok {
				return true
			}

			if cmdName != "tmux" && cmdName != "git" {
				return true
			}

			violations = append(violations, fset.Position(call.Pos()).String())
			return true
		})
	}

	if len(violations) > 0 {
		t.Fatalf("internal/app must route tmux/git process calls through integration services; found direct exec at %v", violations)
	}
}

func isExecCommandCall(fun ast.Expr) bool {
	switch node := fun.(type) {
	case *ast.SelectorExpr:
		if node.Sel == nil {
			return false
		}
		name := node.Sel.Name
		return name == "Command" || name == "CommandContext"
	case *ast.Ident:
		// Defensive fallback in case helper aliases are reintroduced.
		return node.Name == "execCommand" || node.Name == "execCommandContext"
	default:
		return false
	}
}

func stringLiteral(expr ast.Expr) (string, bool) {
	basic, ok := expr.(*ast.BasicLit)
	if !ok || basic.Kind != token.STRING {
		return "", false
	}
	unquoted, err := strconv.Unquote(strings.TrimSpace(basic.Value))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(unquoted), true
}
