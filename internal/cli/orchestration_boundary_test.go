package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// This is the deletion test for the migration: the active CLI start adapter
// must remain valid without redistributing claim/session policy to another CLI
// helper or caller.
func TestOrchestrateStartContainsNoAuthorityPolicy(t *testing.T) {
	source, err := os.ReadFile("orchestrate.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "orchestrate.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "orchestrateStart" {
			body = string(source[fn.Body.Pos()-1 : fn.Body.End()])
			break
		}
	}
	if body == "" {
		t.Fatal("orchestrateStart not found")
	}
	for _, forbidden := range []string{"ClaimTaskOwnership", "ReleaseTaskOwnership", "StartSessionOperation", "runnableSet", "activeSet"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("orchestrateStart contains daemon authority policy %q", forbidden)
		}
	}
	if !strings.Contains(body, "ApplyOrchestrationIntent") {
		t.Fatal("orchestrateStart does not delegate to daemon intent interface")
	}
}
