package sqliteutil

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type testCodedError struct {
	code int
}

func (e testCodedError) Error() string { return "coded sqlite failure" }
func (e testCodedError) Code() int     { return e.code }

func TestSQLiteErrorDetailsPreserveExtendedCode(t *testing.T) {
	err := fmt.Errorf("outer: %w", testCodedError{code: SQLiteIOErrorShortRead})
	details, ok := Details(err)
	if !ok {
		t.Fatal("Details did not recognize wrapped coded error")
	}
	if details.PrimaryCode != SQLitePrimaryIOError || details.ExtendedCode != SQLiteIOErrorShortRead || details.Symbol != "SQLITE_IOERR_SHORT_READ" {
		t.Fatalf("Details = %+v", details)
	}
	if !IsIOErrorShortRead(err) {
		t.Fatal("IsIOErrorShortRead returned false")
	}
}

func TestWrapSQLiteErrorIncludesBoundedDiagnostics(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "consumer.db")
	cause := testCodedError{code: SQLiteIOErrorShortRead}
	err := WrapError(dbPath, "read projection delta head", cause)
	for _, want := range []string{
		"read projection delta head",
		"db_path=" + CanonicalPath(dbPath),
		"sqlite_code=10",
		"sqlite_extended_code=522",
		"sqlite_symbol=SQLITE_IOERR_SHORT_READ",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("WrapError() = %q, want %q", err, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Fatal("WrapError did not preserve cause")
	}
}
