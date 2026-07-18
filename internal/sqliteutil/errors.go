package sqliteutil

import (
	"errors"
	"fmt"
	"strings"

	sqlite "modernc.org/sqlite"
)

const (
	SQLitePrimaryIOError   = 10
	SQLiteIOErrorShortRead = SQLitePrimaryIOError | (2 << 8)
)

type sqliteCodedError interface {
	Code() int
}

// ErrorDetails exposes bounded SQLite result-code diagnostics without relying
// on error strings. ExtendedCode retains the subtype while PrimaryCode is the
// low byte defined by SQLite's result-code contract.
type ErrorDetails struct {
	PrimaryCode  int
	ExtendedCode int
	Symbol       string
}

func Details(err error) (ErrorDetails, bool) {
	var coded sqliteCodedError
	if !errors.As(err, &coded) {
		return ErrorDetails{}, false
	}
	extended := coded.Code()
	return ErrorDetails{
		PrimaryCode:  extended & 0xff,
		ExtendedCode: extended,
		Symbol:       sqliteErrorSymbol(extended),
	}, true
}

func IsIOErrorShortRead(err error) bool {
	details, ok := Details(err)
	return ok && details.ExtendedCode == SQLiteIOErrorShortRead
}

// WrapError adds the database identity and logical operation needed to
// diagnose failures while preserving the original error for typed matching.
func WrapError(dbPath, operation string, err error) error {
	if err == nil {
		return nil
	}
	dbPath = CanonicalPath(dbPath)
	operation = strings.TrimSpace(operation)
	if details, ok := Details(err); ok {
		return fmt.Errorf("%s [db_path=%s sqlite_code=%d sqlite_extended_code=%d sqlite_symbol=%s]: %w",
			operation, dbPath, details.PrimaryCode, details.ExtendedCode, details.Symbol, err)
	}
	return fmt.Errorf("%s [db_path=%s]: %w", operation, dbPath, err)
}

func sqliteErrorSymbol(code int) string {
	description := sqlite.ErrorCodeString[code]
	open := strings.LastIndex(description, "(SQLITE_")
	if open < 0 {
		return "SQLITE_UNKNOWN"
	}
	close := strings.IndexByte(description[open:], ')')
	if close < 0 {
		return "SQLITE_UNKNOWN"
	}
	return description[open+1 : open+close]
}
