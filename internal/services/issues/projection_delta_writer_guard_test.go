package issues

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIssueAuthorityWritersEmitProjectionDeltas prevents a new direct issue or
// relationship writer from bypassing the atomic observation/delta boundary.
func TestIssueAuthorityWritersEmitProjectionDeltas(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") || path == "migrations.go" {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		source, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			body := string(source[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			writesAuthority := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					return true
				}
				normalized := strings.ToLower(strings.Join(strings.Fields(value), " "))
				for _, marker := range []string{"insert into issues", "update issues", "delete from issues", "insert into issue_dependencies", "update issue_dependencies", "delete from issue_dependencies"} {
					writesAuthority = writesAuthority || strings.Contains(normalized, marker)
				}
				return true
			})
			if !writesAuthority {
				continue
			}
			emits := strings.Contains(body, "appendIssueObservationEvent(") || strings.Contains(body, "appendProjectionDelta(")
			if !emits {
				t.Errorf("%s:%s writes issue authority without an atomic projection delta", path, fn.Name.Name)
			}
		}
	}
}
