package daemon

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This structural inventory covers every non-test daemon source file. New raw
// text-input calls fail until explicitly classified. The allowlist contains
// only lifecycle/bootstrap control paths; agent messages have no exemption.
func TestDaemonRawTmuxInputCallersAreStructurallyBounded(t *testing.T) {
	allowed := map[string]bool{
		"orchestrator_session.go:gracefullyStopOrchestratorRuntime:PasteTextAndSubmit": true,
		"orchestrator_session.go:gracefullyStopOrchestratorRuntime:SendKeys":           true,
		"session_commands.go:handleSessionRestartAll:SendKeys":                         true,
		"session_commands.go:handleSessionRestartAll:PasteTextAndSubmit":               true,
		"session_commands.go:startSessionAsyncInitCommands:SendKeys":                   true,
		"session_commands.go:exportIssueResourceSessionEnv:SendKeys":                   true,
		"session_commands.go:exportSessionContextEnv:SendKeys":                         true,
	}
	var found []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "SendKeys", "PasteTextAndSubmit":
					found = append(found, fmt.Sprintf("%s:%s:%s", filepath.Base(path), fn.Name.Name, sel.Sel.Name))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	for _, call := range found {
		if !allowed[call] {
			t.Errorf("unclassified raw tmux input caller: %s", call)
		}
		delete(allowed, call)
	}
	for missing := range allowed {
		t.Errorf("stale raw tmux input allowlist entry: %s", missing)
	}
}

func TestAgentDeliveryImplementationContainsNoTerminalInferenceOrTmuxWrite(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "agent_input_delivery.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "CapturePane", "ObserveAgentInputTarget", "SendKeys", "PasteTextAndSubmit", "PasteAgentTextAndSubmit":
			t.Errorf("agent delivery uses non-authoritative terminal operation %s", sel.Sel.Name)
		}
		return true
	})
}
