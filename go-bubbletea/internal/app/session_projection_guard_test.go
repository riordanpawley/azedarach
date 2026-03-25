package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSessionProjectionGuard_NoLocalMonitorStart(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}

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

		recv, ok := sel.X.(*ast.SelectorExpr)
		if !ok || recv.Sel == nil || recv.Sel.Name != "sessionMonitor" {
			return true
		}

		if ident, ok := recv.X.(*ast.Ident); !ok || ident == nil || ident.Name != "m" {
			return true
		}

		violations = append(violations, fset.Position(call.Pos()).String())
		return true
	})

	if len(violations) > 0 {
		t.Fatalf("model.go must not start local session monitors; found Start call(s) at %v", violations)
	}
}

func TestSessionProjectionGuard_NoPiecemealSessionAuthorityWrites(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "model.go", nil, 0)
	if err != nil {
		t.Fatalf("parse model.go: %v", err)
	}

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range stmt.Lhs {
				index, ok := lhs.(*ast.IndexExpr)
				if !ok {
					continue
				}

				sel, ok := index.X.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "sessions" {
					continue
				}

				recv, ok := sel.X.(*ast.Ident)
				if !ok || recv.Name != "m" {
					continue
				}

				violations = append(violations, fset.Position(lhs.Pos()).String())
			}
		case *ast.CallExpr:
			fun, ok := stmt.Fun.(*ast.Ident)
			if !ok || fun.Name != "delete" || len(stmt.Args) == 0 {
				return true
			}

			sel, ok := stmt.Args[0].(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "sessions" {
				return true
			}

			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "m" {
				return true
			}

			violations = append(violations, fset.Position(stmt.Pos()).String())
		}

		return true
	})

	if len(violations) > 0 {
		t.Fatalf("model.go must keep session authority writes daemon-owned and projection-only; found violations at %v", violations)
	}
}
