package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestIntegrationBoundaryGuard_NoDirectGitExecInAppOrCli(t *testing.T) {
	t.Helper()

	repoRoot, err := repoRootFromTestFile()
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	violations, err := findDirectGitExecViolations(repoRoot)
	if err != nil {
		t.Fatalf("scan runtime source: %v", err)
	}

	if len(violations) == 0 {
		return
	}

	sort.Strings(violations)
	t.Fatalf("runtime app/cli code must route direct git subprocesses through sanctioned helpers; found violations at %v", violations)
}

func TestIntegrationBoundaryGuard_NoAuthorityServiceOrDaemonImportsInAppOrCli(t *testing.T) {
	t.Helper()

	repoRoot, err := repoRootFromTestFile()
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	violations, err := findAuthorityBoundaryImportViolations(repoRoot)
	if err != nil {
		t.Fatalf("scan runtime source: %v", err)
	}

	if len(violations) == 0 {
		return
	}

	sort.Strings(violations)
	t.Fatalf("runtime app/cli code must route writes through daemon command paths; found forbidden imports at %v", violations)
}

func repoRootFromTestFile() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrInvalid
	}
	// This file lives in internal/tui, so two parents up is the repo root.
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

func findDirectGitExecViolations(repoRoot string) ([]string, error) {
	targetRoots := []string{
		filepath.Join(repoRoot, "internal", "tui"),
		filepath.Join(repoRoot, "internal", "cli"),
	}

	fset := token.NewFileSet()
	violations := make([]string, 0, 4)
	for _, root := range targetRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			relPath, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			relPath = filepath.ToSlash(relPath)

			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}

				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || !isExecCommandCall(call.Fun) || len(call.Args) == 0 {
						return true
					}

					cmdName, ok := stringLiteral(call.Args[0])
					if !ok || cmdName != "git" {
						return true
					}
					if isAllowedGitExec(relPath, fn.Name.Name) {
						return true
					}

					position := fset.Position(call.Pos())
					violations = append(violations, relPath+":"+fn.Name.Name+":"+strconv.Itoa(position.Line))
					return true
				})
			}

			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return violations, nil
}

func findAuthorityBoundaryImportViolations(repoRoot string) ([]string, error) {
	targetRoots := []string{
		filepath.Join(repoRoot, "internal", "tui"),
		filepath.Join(repoRoot, "internal", "cli"),
	}

	fset := token.NewFileSet()
	violations := make([]string, 0, 8)
	for _, root := range targetRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			relPath, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			relPath = filepath.ToSlash(relPath)

			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}

			for _, imp := range file.Imports {
				imported, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return err
				}
				if !isForbiddenAuthorityImport(imported) {
					continue
				}

				violations = append(violations, relPath+":"+imported)
			}

			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return violations, nil
}

func isForbiddenAuthorityImport(imported string) bool {
	switch imported {
	case "github.com/riordanpawley/azedarach/internal/daemon":
		return true
	case "github.com/riordanpawley/azedarach/internal/services/git",
		"github.com/riordanpawley/azedarach/internal/services/issues",
		"github.com/riordanpawley/azedarach/internal/services/worktree",
		"github.com/riordanpawley/azedarach/internal/services/tmux",
		"github.com/riordanpawley/azedarach/internal/services/devserver",
		"github.com/riordanpawley/azedarach/internal/services/pr":
		return true
	default:
		return false
	}
}

func isAllowedGitExec(path, function string) bool {
	allowedFunctions, ok := allowedGitExec[path]
	if !ok {
		return false
	}
	_, ok = allowedFunctions[function]
	return ok
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

var allowedGitExec = map[string]map[string]struct{}{
	// Intentionally empty: runtime app/cli should not shell out to git directly.
}
