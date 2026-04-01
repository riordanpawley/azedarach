package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSessionProjectionGuard_NoLocalMonitorStart(t *testing.T) {
	fset, file := parseSessionProjectionModel(t)

	var violations []string

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Start" {
			return true
		}

		if !containsSessionMonitor(sel.X) {
			return true
		}

		violations = append(violations, fset.Position(call.Pos()).String())
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("internal/tui/model.go must not start local session monitors; found Start call(s) at %v", violations)
	}
}

func TestSessionProjectionGuard_NoDirectSessionProjectionWrites(t *testing.T) {
	fset, file := parseSessionProjectionModel(t)

	var violations []string

	ast.Inspect(file, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range stmt.Lhs {
				if !containsSessionEntry(lhs) {
					continue
				}

				violations = append(violations, fset.Position(lhs.Pos()).String())
			}
		case *ast.IncDecStmt:
			if !containsSessionEntry(stmt.X) {
				return true
			}

			violations = append(violations, fset.Position(stmt.Pos()).String())
		}

		return true
	})

	if len(violations) > 0 {
		t.Fatalf("internal/tui/model.go must keep session projection writes local-only; found write(s) at %v", violations)
	}
}

func TestSessionProjectionGuard_NoDirectSessionProjectionDeletes(t *testing.T) {
	fset, file := parseSessionProjectionModel(t)

	var violations []string

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		fun, ok := call.Fun.(*ast.Ident)
		if !ok || fun.Name != "delete" || len(call.Args) == 0 {
			return true
		}

		if !isSessionsField(call.Args[0]) {
			return true
		}

		violations = append(violations, fset.Position(call.Pos()).String())
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("internal/tui/model.go must keep session projection deletes local-only; found delete(s) at %v", violations)
	}
}

func parseSessionProjectionModel(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}

	return fset, file
}

func containsSessionMonitor(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name == "sessionMonitor"
	case *ast.SelectorExpr:
		if node.Sel != nil && node.Sel.Name == "sessionMonitor" {
			return true
		}
		return containsSessionMonitor(node.X)
	default:
		return false
	}
}

func isSessionsField(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "sessions" {
		return false
	}

	return true
}

func containsSessionEntry(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.IndexExpr:
		return isSessionsField(node.X) || containsSessionEntry(node.X) || containsSessionEntry(node.Index)
	case *ast.SelectorExpr:
		if node.Sel != nil && node.Sel.Name == "sessions" {
			return true
		}
		return containsSessionEntry(node.X)
	case *ast.ParenExpr:
		return containsSessionEntry(node.X)
	default:
		return false
	}
}
