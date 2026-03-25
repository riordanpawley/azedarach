package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionProjectionGuard_NoLocalMonitorStart(t *testing.T) {
	var violations []string

	for _, path := range runtimeSourceFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

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
	}

	if len(violations) > 0 {
		t.Fatalf("internal/app must not start local session monitors; found Start call(s) at %v", violations)
	}
}

func TestSessionProjectionGuard_NoPiecemealSessionAuthorityWrites(t *testing.T) {
	var violations []string

	for _, path := range runtimeSourceFiles(t) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range stmt.Lhs {
					if !containsSessionEntry(lhs) {
						continue
					}

					violations = append(violations, fset.Position(lhs.Pos()).String())
				}
			case *ast.CallExpr:
				fun, ok := stmt.Fun.(*ast.Ident)
				if !ok || fun.Name != "delete" || len(stmt.Args) == 0 {
					return true
				}

				if !isSessionsField(stmt.Args[0]) {
					return true
				}

				violations = append(violations, fset.Position(stmt.Pos()).String())
			case *ast.IncDecStmt:
				if !containsSessionEntry(stmt.X) {
					return true
				}

				violations = append(violations, fset.Position(stmt.Pos()).String())
			}

			return true
		})
	}

	if len(violations) > 0 {
		t.Fatalf("internal/app must keep session authority writes daemon-owned and projection-only; found violations at %v", violations)
	}
}

func runtimeSourceFiles(t *testing.T) []string {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list app source files: %v", err)
	}

	var runtimeFiles []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		runtimeFiles = append(runtimeFiles, path)
	}

	return runtimeFiles
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
		return containsSessionEntry(node.X)
	case *ast.ParenExpr:
		return containsSessionEntry(node.X)
	default:
		return false
	}
}
