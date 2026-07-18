package issues

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	legacyAttachmentFilenamePrefixBytes = 17
	legacyAttachmentFilenameMaxBytes    = 255
	currentAttachmentTableDDL           = `CREATE TABLE issue_attachments (
		issue_id TEXT NOT NULL,
		attachment_id TEXT NOT NULL,
		filename TEXT NOT NULL,
		relative_path TEXT NOT NULL,
		mime_type TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		PRIMARY KEY (issue_id, attachment_id)
	)`
	legacyAttachmentTableDDL = `CREATE TABLE issue_attachments (
		issue_id TEXT NOT NULL,
		attachment_id TEXT NOT NULL,
		filename TEXT NOT NULL,
		original_path TEXT NOT NULL,
		mime_type TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		content_blob BLOB NOT NULL,
		PRIMARY KEY (issue_id, attachment_id)
	)`
)

type attachmentSchemaColumn struct {
	name       string
	typeName   string
	notNull    int
	defaultSQL sql.NullString
	primaryKey int
	hidden     int
}

type attachmentSchemaReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type attachmentSchemaDefinition struct {
	tableDDL string
	indexes  []attachmentIndexDefinition
}

type attachmentIndexDefinition struct {
	name    string
	unique  int
	origin  string
	partial int
	ddl     string
	columns []attachmentIndexColumn
}

type attachmentIndexColumn struct {
	cid       int
	name      sql.NullString
	desc      int
	collation string
	key       int
}

var currentAttachmentSchema = attachmentSchemaDefinition{
	tableDDL: currentAttachmentTableDDL,
	indexes:  canonicalAttachmentIndexes(false),
}

var legacyAttachmentSchema = attachmentSchemaDefinition{
	tableDDL: legacyAttachmentTableDDL,
	indexes:  canonicalAttachmentIndexes(true),
}

type legacyAttachmentRow struct {
	issueID      string
	attachmentID string
	filename     string
	originalPath string
	mimeType     string
	size         int64
	createdAt    string
	data         []byte
	relativePath string
}

var legacyAttachmentColumns = []attachmentSchemaColumn{
	{name: "issue_id", typeName: "TEXT", notNull: 1, primaryKey: 1},
	{name: "attachment_id", typeName: "TEXT", notNull: 1, primaryKey: 2},
	{name: "filename", typeName: "TEXT", notNull: 1},
	{name: "original_path", typeName: "TEXT", notNull: 1},
	{name: "mime_type", typeName: "TEXT", notNull: 1},
	{name: "size_bytes", typeName: "INTEGER", notNull: 1},
	{name: "created_at", typeName: "TEXT", notNull: 1},
	{name: "content_blob", typeName: "BLOB", notNull: 1},
}

var currentAttachmentColumns = []attachmentSchemaColumn{
	{name: "issue_id", typeName: "TEXT", notNull: 1, primaryKey: 1},
	{name: "attachment_id", typeName: "TEXT", notNull: 1, primaryKey: 2},
	{name: "filename", typeName: "TEXT", notNull: 1},
	{name: "relative_path", typeName: "TEXT", notNull: 1},
	{name: "mime_type", typeName: "TEXT", notNull: 1},
	{name: "size", typeName: "INTEGER", notNull: 1, defaultSQL: sql.NullString{String: "0", Valid: true}},
	{name: "created_at", typeName: "TEXT", notNull: 1},
}

func (c *Client) applyLegacyAttachmentBlobForwardMigration(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy attachment forward migration: %w", err)
	}
	defer tx.Rollback()

	columns, exists, err := readIssueAttachmentSchema(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect legacy attachment schema: %w", err)
	}
	createdFiles := make([]string, 0)
	commitAttempted := false
	defer func() {
		if commitAttempted {
			return
		}
		for i := len(createdFiles) - 1; i >= 0; i-- {
			_ = os.Remove(createdFiles[i])
		}
		if len(createdFiles) > 0 {
			_ = c.syncLegacyAttachmentDirectory(filepath.Dir(createdFiles[0]))
		}
	}()

	switch {
	case !exists:
		return fmt.Errorf("issue attachment schema is absent; refuse migration %s", id)
	case attachmentColumnsEqual(columns, currentAttachmentColumns):
		if err := validateIssueAttachmentSchemaObjects(ctx, tx, currentAttachmentSchema); err != nil {
			return fmt.Errorf("current issue attachment schema drift: %w", err)
		}
	case attachmentColumnsEqual(columns, legacyAttachmentColumns):
		if err := validateIssueAttachmentSchemaObjects(ctx, tx, legacyAttachmentSchema); err != nil {
			return fmt.Errorf("legacy issue attachment schema drift: %w", err)
		}
		rows, err := readLegacyAttachmentRows(ctx, tx)
		if err != nil {
			return err
		}
		createdFiles, err = c.materializeLegacyAttachmentRows(rows)
		if err != nil {
			return err
		}
		if len(createdFiles) > 0 {
			if err := c.syncLegacyAttachmentDirectory(filepath.Dir(createdFiles[0])); err != nil {
				return fmt.Errorf("sync canonical attachment directory: %w", err)
			}
		}
		if err := c.runLegacyAttachmentMigrationFailureHook("after_files"); err != nil {
			return err
		}
		if err := replaceLegacyAttachmentTable(ctx, tx, rows); err != nil {
			return err
		}
	default:
		return fmt.Errorf("issue attachment schema has unknown or partial shape; refuse migration %s", id)
	}

	if err := validateLegacyAttachmentBlobForwardSchemaReader(ctx, tx); err != nil {
		return err
	}
	if err := c.runLegacyAttachmentMigrationFailureHook("before_record"); err != nil {
		return err
	}
	if err := recordAppliedMigration(ctx, tx, id); err != nil {
		return fmt.Errorf("record legacy attachment forward migration: %w", err)
	}
	if err := c.runLegacyAttachmentMigrationFailureHook("before_commit"); err != nil {
		return err
	}
	commitAttempted = true
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy attachment forward migration: %w", err)
	}
	return nil
}

func (c *Client) materializeLegacyAttachmentRows(rows []legacyAttachmentRow) ([]string, error) {
	attachmentDir := filepath.Join(filepath.Dir(c.dbPath), "attachments")
	if len(rows) > 0 {
		if err := c.ensureDurableLegacyAttachmentDirectory(attachmentDir); err != nil {
			return nil, fmt.Errorf("create canonical attachment directory: %w", err)
		}
	}
	created := make([]string, 0, len(rows))
	for i := range rows {
		name := safeLegacyAttachmentName(rows[i].filename, rows[i].originalPath)
		name = truncateUTF8Bytes(name, legacyAttachmentFilenameMaxBytes-legacyAttachmentFilenamePrefixBytes)
		contentName := legacyAttachmentContentID(rows[i].data) + "-" + name
		destination := filepath.Join(attachmentDir, contentName)
		wasCreated, err := installLegacyAttachmentFile(destination, rows[i].data)
		if err != nil {
			for j := len(created) - 1; j >= 0; j-- {
				_ = os.Remove(created[j])
			}
			if len(created) > 0 {
				if syncErr := c.syncLegacyAttachmentDirectory(attachmentDir); syncErr != nil {
					return nil, errors.Join(
						fmt.Errorf("materialize legacy attachment %q/%q: %w", rows[i].issueID, rows[i].attachmentID, err),
						fmt.Errorf("sync canonical attachment directory after cleanup: %w", syncErr),
					)
				}
			}
			return nil, fmt.Errorf("materialize legacy attachment %q/%q: %w", rows[i].issueID, rows[i].attachmentID, err)
		}
		if wasCreated {
			created = append(created, destination)
		}
		rows[i].filename = contentName
		rows[i].relativePath = filepath.ToSlash(filepath.Join(".azedarach", "attachments", contentName))
	}
	return created, nil
}

func readLegacyAttachmentRows(ctx context.Context, tx *sql.Tx) ([]legacyAttachmentRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT issue_id, attachment_id, filename, original_path, mime_type, size_bytes, created_at, content_blob FROM issue_attachments ORDER BY created_at, filename, issue_id, attachment_id`)
	if err != nil {
		return nil, fmt.Errorf("read legacy attachment rows: %w", err)
	}
	defer rows.Close()
	result := make([]legacyAttachmentRow, 0)
	for rows.Next() {
		var row legacyAttachmentRow
		if err := rows.Scan(&row.issueID, &row.attachmentID, &row.filename, &row.originalPath, &row.mimeType, &row.size, &row.createdAt, &row.data); err != nil {
			return nil, fmt.Errorf("scan legacy attachment row: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy attachment rows: %w", err)
	}
	return result, nil
}

func replaceLegacyAttachmentTable(ctx context.Context, tx *sql.Tx, rows []legacyAttachmentRow) error {
	if _, err := tx.ExecContext(ctx, `
		DROP TABLE IF EXISTS issue_attachments_0056_new;
		CREATE TABLE issue_attachments_0056_new (
			issue_id TEXT NOT NULL,
			attachment_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			PRIMARY KEY (issue_id, attachment_id)
		)`); err != nil {
		return fmt.Errorf("create legacy attachment replacement table: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO issue_attachments_0056_new (issue_id, attachment_id, filename, relative_path, mime_type, size, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare legacy attachment replacement rows: %w", err)
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx, row.issueID, row.attachmentID, row.filename, row.relativePath, row.mimeType, row.size, row.createdAt); err != nil {
			return fmt.Errorf("copy legacy attachment %q/%q: %w", row.issueID, row.attachmentID, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return fmt.Errorf("close legacy attachment replacement statement: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE issue_attachments; ALTER TABLE issue_attachments_0056_new RENAME TO issue_attachments; CREATE INDEX idx_issue_attachments_attachment_id ON issue_attachments(attachment_id)`); err != nil {
		return fmt.Errorf("install legacy attachment replacement table: %w", err)
	}
	return nil
}

func validateLegacyAttachmentBlobForwardSchema(ctx context.Context, db *sql.DB) error {
	return validateLegacyAttachmentBlobForwardSchemaReader(ctx, db)
}

func validateLegacyAttachmentBlobForwardSchemaReader(ctx context.Context, reader attachmentSchemaReader) error {
	columns, exists, err := readIssueAttachmentSchema(ctx, reader)
	if err != nil {
		return fmt.Errorf("inspect migrated attachment schema: %w", err)
	}
	if !exists || !attachmentColumnsEqual(columns, currentAttachmentColumns) {
		return fmt.Errorf("legacy attachment forward migration target schema drift")
	}
	if err := validateIssueAttachmentSchemaObjects(ctx, reader, currentAttachmentSchema); err != nil {
		return fmt.Errorf("legacy attachment forward migration target object drift: %w", err)
	}
	return nil
}

func readIssueAttachmentSchema(ctx context.Context, reader attachmentSchemaReader) ([]attachmentSchemaColumn, bool, error) {
	var exists bool
	if err := reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='issue_attachments')`).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	rows, err := reader.QueryContext(ctx, `PRAGMA table_xinfo(issue_attachments)`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	columns := make([]attachmentSchemaColumn, 0)
	for rows.Next() {
		var cid int
		var column attachmentSchemaColumn
		if err := rows.Scan(&cid, &column.name, &column.typeName, &column.notNull, &column.defaultSQL, &column.primaryKey, &column.hidden); err != nil {
			return nil, false, err
		}
		column.typeName = strings.ToUpper(strings.TrimSpace(column.typeName))
		columns = append(columns, column)
	}
	return columns, true, rows.Err()
}

func attachmentColumnsEqual(got, want []attachmentSchemaColumn) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].name != want[i].name || got[i].typeName != want[i].typeName || got[i].notNull != want[i].notNull || got[i].primaryKey != want[i].primaryKey || got[i].hidden != want[i].hidden || got[i].defaultSQL.Valid != want[i].defaultSQL.Valid {
			return false
		}
		if got[i].defaultSQL.Valid && strings.TrimSpace(got[i].defaultSQL.String) != strings.TrimSpace(want[i].defaultSQL.String) {
			return false
		}
	}
	return true
}

func canonicalAttachmentIndexes(includeLegacyIssueIndex bool) []attachmentIndexDefinition {
	indexes := []attachmentIndexDefinition{
		{
			name: "sqlite_autoindex_issue_attachments_1", unique: 1, origin: "pk",
			columns: []attachmentIndexColumn{
				{cid: 0, name: sql.NullString{String: "issue_id", Valid: true}, collation: "BINARY", key: 1},
				{cid: 1, name: sql.NullString{String: "attachment_id", Valid: true}, collation: "BINARY", key: 1},
				{cid: -1, collation: "BINARY"},
			},
		},
		{
			name: "idx_issue_attachments_attachment_id", origin: "c",
			ddl: `CREATE INDEX idx_issue_attachments_attachment_id ON issue_attachments(attachment_id)`,
			columns: []attachmentIndexColumn{
				{cid: 1, name: sql.NullString{String: "attachment_id", Valid: true}, collation: "BINARY", key: 1},
				{cid: -1, collation: "BINARY"},
			},
		},
	}
	if includeLegacyIssueIndex {
		indexes = append(indexes, attachmentIndexDefinition{
			name: "idx_issue_attachments_issue", origin: "c",
			ddl: `CREATE INDEX idx_issue_attachments_issue ON issue_attachments(issue_id, created_at, attachment_id)`,
			columns: []attachmentIndexColumn{
				{cid: 0, name: sql.NullString{String: "issue_id", Valid: true}, collation: "BINARY", key: 1},
				{cid: 6, name: sql.NullString{String: "created_at", Valid: true}, collation: "BINARY", key: 1},
				{cid: 1, name: sql.NullString{String: "attachment_id", Valid: true}, collation: "BINARY", key: 1},
				{cid: -1, collation: "BINARY"},
			},
		})
	}
	return indexes
}

func validateIssueAttachmentSchemaObjects(ctx context.Context, reader attachmentSchemaReader, definition attachmentSchemaDefinition) error {
	var withoutRowID, strict int
	if err := reader.QueryRowContext(ctx, `SELECT wr,strict FROM pragma_table_list WHERE schema='main' AND name='issue_attachments' AND type='table'`).Scan(&withoutRowID, &strict); err != nil {
		return fmt.Errorf("inspect attachment table flags: %w", err)
	}
	if withoutRowID != 0 || strict != 0 {
		return fmt.Errorf("attachment table flags drift: without_rowid=%d strict=%d", withoutRowID, strict)
	}
	var foreignKeys int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('issue_attachments')`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("inspect attachment foreign keys: %w", err)
	}
	if foreignKeys != 0 {
		return fmt.Errorf("attachment table has %d unexpected foreign keys", foreignKeys)
	}
	expectedObjects := map[string]string{"table:issue_attachments": definition.tableDDL}
	for _, index := range definition.indexes {
		expectedObjects["index:"+index.name] = index.ddl
	}
	rows, err := reader.QueryContext(ctx, `SELECT type,name,COALESCE(sql,'') FROM sqlite_master WHERE tbl_name='issue_attachments' ORDER BY type,name`)
	if err != nil {
		return fmt.Errorf("inspect attachment schema objects: %w", err)
	}
	seen := make(map[string]bool, len(expectedObjects))
	for rows.Next() {
		var objectType, name, ddl string
		if err := rows.Scan(&objectType, &name, &ddl); err != nil {
			rows.Close()
			return fmt.Errorf("scan attachment schema object: %w", err)
		}
		key := objectType + ":" + name
		expectedDDL, ok := expectedObjects[key]
		if !ok {
			rows.Close()
			return fmt.Errorf("unexpected attachment schema object %s", key)
		}
		if expectedDDL == "" {
			if strings.TrimSpace(ddl) != "" {
				rows.Close()
				return fmt.Errorf("implicit attachment index %q unexpectedly has DDL", name)
			}
		} else if err := compareSQLiteDDL(ddl, expectedDDL); err != nil {
			rows.Close()
			return fmt.Errorf("attachment schema object %s DDL drift: %w", key, err)
		}
		seen[key] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for key := range expectedObjects {
		if !seen[key] {
			return fmt.Errorf("attachment schema object %s is missing", key)
		}
	}
	return validateIssueAttachmentIndexes(ctx, reader, definition.indexes)
}

func validateIssueAttachmentIndexes(ctx context.Context, reader attachmentSchemaReader, expected []attachmentIndexDefinition) error {
	expectedByName := make(map[string]attachmentIndexDefinition, len(expected))
	for _, index := range expected {
		expectedByName[index.name] = index
	}
	rows, err := reader.QueryContext(ctx, `PRAGMA index_list(issue_attachments)`)
	if err != nil {
		return fmt.Errorf("inspect attachment indexes: %w", err)
	}
	seen := make(map[string]bool, len(expected))
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return fmt.Errorf("scan attachment index: %w", err)
		}
		index, ok := expectedByName[name]
		if !ok {
			rows.Close()
			return fmt.Errorf("unexpected attachment index %q", name)
		}
		if unique != index.unique || origin != index.origin || partial != index.partial {
			rows.Close()
			return fmt.Errorf("attachment index %q flags drift: unique=%d origin=%q partial=%d", name, unique, origin, partial)
		}
		seen[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, index := range expected {
		if !seen[index.name] {
			return fmt.Errorf("attachment index %q is missing", index.name)
		}
		if err := validateIssueAttachmentIndexColumns(ctx, reader, index); err != nil {
			return err
		}
	}
	return nil
}

func validateIssueAttachmentIndexColumns(ctx context.Context, reader attachmentSchemaReader, expected attachmentIndexDefinition) error {
	indexRows, err := reader.QueryContext(ctx, `PRAGMA index_xinfo('`+expected.name+`')`)
	if err != nil {
		return fmt.Errorf("inspect attachment index %q columns: %w", expected.name, err)
	}
	defer indexRows.Close()
	columns := make([]attachmentIndexColumn, 0, len(expected.columns))
	for indexRows.Next() {
		var sequence int
		var column attachmentIndexColumn
		if err := indexRows.Scan(&sequence, &column.cid, &column.name, &column.desc, &column.collation, &column.key); err != nil {
			return err
		}
		if sequence != len(columns) {
			return fmt.Errorf("attachment index %q sequence drift: got %d want %d", expected.name, sequence, len(columns))
		}
		columns = append(columns, column)
	}
	if err := indexRows.Err(); err != nil {
		return err
	}
	if len(columns) != len(expected.columns) {
		return fmt.Errorf("attachment index %q column count drift: got %d want %d", expected.name, len(columns), len(expected.columns))
	}
	for i := range columns {
		want := expected.columns[i]
		got := columns[i]
		if got.cid != want.cid || got.name != want.name || got.desc != want.desc || !strings.EqualFold(got.collation, want.collation) || got.key != want.key {
			return fmt.Errorf("attachment index %q column %d drift: cid=%d name=%q desc=%d coll=%q key=%d", expected.name, i, got.cid, got.name.String, got.desc, got.collation, got.key)
		}
	}
	return nil
}

func compareSQLiteDDL(got, want string) error {
	gotTokens, err := tokenizeSQLiteDDL(got)
	if err != nil {
		return fmt.Errorf("tokenize stored DDL: %w", err)
	}
	wantTokens, err := tokenizeSQLiteDDL(want)
	if err != nil {
		return fmt.Errorf("tokenize canonical DDL: %w", err)
	}
	if len(gotTokens) != len(wantTokens) {
		return fmt.Errorf("token count got %d want %d", len(gotTokens), len(wantTokens))
	}
	for i := range gotTokens {
		if gotTokens[i] != wantTokens[i] {
			return fmt.Errorf("token %d got %q want %q", i, gotTokens[i], wantTokens[i])
		}
	}
	return nil
}

func tokenizeSQLiteDDL(source string) ([]string, error) {
	tokens := make([]string, 0, len(source)/4)
	for offset := 0; offset < len(source); {
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("invalid UTF-8 at byte %d", offset)
		}
		if unicode.IsSpace(r) {
			offset += size
			continue
		}
		if strings.HasPrefix(source[offset:], "--") {
			if end := strings.IndexByte(source[offset+2:], '\n'); end >= 0 {
				offset += end + 3
			} else {
				offset = len(source)
			}
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated block comment")
			}
			offset += end + 4
			continue
		}
		if r == '\'' {
			value, next, err := scanSQLiteQuotedToken(source, offset, '\'', '\'', "string")
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, "string:"+value)
			offset = next
			continue
		}
		if r == '"' || r == '`' {
			value, next, err := scanSQLiteQuotedToken(source, offset, r, r, "identifier")
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, "word:"+strings.ToLower(value))
			offset = next
			continue
		}
		if r == '[' {
			end := strings.IndexByte(source[offset+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated bracket identifier")
			}
			value := source[offset+1 : offset+1+end]
			tokens = append(tokens, "word:"+strings.ToLower(value))
			offset += end + 2
			continue
		}
		if isSQLiteWordRune(r) {
			start := offset
			for offset += size; offset < len(source); {
				next, nextSize := utf8.DecodeRuneInString(source[offset:])
				if !isSQLiteWordRune(next) {
					break
				}
				offset += nextSize
			}
			tokens = append(tokens, "word:"+strings.ToLower(source[start:offset]))
			continue
		}
		if strings.ContainsRune("(),.;+-*/%=<>!|&~", r) {
			tokens = append(tokens, "symbol:"+string(r))
			offset += size
			continue
		}
		return nil, fmt.Errorf("unsupported token %q at byte %d", r, offset)
	}
	for len(tokens) > 0 && tokens[len(tokens)-1] == "symbol:;" {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens, nil
}

func scanSQLiteQuotedToken(source string, offset int, quote, escape rune, kind string) (string, int, error) {
	var value strings.Builder
	offset += utf8.RuneLen(quote)
	for offset < len(source) {
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == utf8.RuneError && size == 1 {
			return "", 0, fmt.Errorf("invalid UTF-8 in quoted %s", kind)
		}
		if r == quote {
			nextOffset := offset + size
			if nextOffset < len(source) {
				next, nextSize := utf8.DecodeRuneInString(source[nextOffset:])
				if next == escape {
					value.WriteRune(quote)
					offset = nextOffset + nextSize
					continue
				}
			}
			return value.String(), nextOffset, nil
		}
		value.WriteRune(r)
		offset += size
	}
	return "", 0, fmt.Errorf("unterminated quoted %s", kind)
}

func isSQLiteWordRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func installLegacyAttachmentFile(destination string, data []byte) (bool, error) {
	exists, matches, err := inspectLegacyAttachmentFile(destination, data)
	if err != nil {
		return false, err
	}
	if exists {
		if !matches {
			return false, fmt.Errorf("destination exists with different content")
		}
		return false, nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".0056-attachment-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		exists, matches, inspectErr := inspectLegacyAttachmentFile(destination, data)
		if inspectErr != nil {
			return false, inspectErr
		}
		if exists && matches {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func inspectLegacyAttachmentFile(path string, data []byte) (bool, bool, error) {
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !before.Mode().IsRegular() {
		return true, false, fmt.Errorf("destination is not a regular file")
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		return true, false, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return true, false, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return true, false, fmt.Errorf("destination changed during validation")
	}
	return true, bytes.Equal(existing, data), nil
}

func safeLegacyAttachmentName(filename, originalPath string) string {
	name := filepath.Base(filepath.FromSlash(strings.TrimSpace(filename)))
	if name == "" || name == "." || name == string(os.PathSeparator) {
		name = filepath.Base(filepath.FromSlash(strings.TrimSpace(originalPath)))
	}
	if name == "" || name == "." || name == string(os.PathSeparator) {
		name = "attachment"
	}
	return name
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "_")
	if len(value) <= maxBytes {
		return value
	}
	end := 0
	for offset := range value {
		if offset > maxBytes {
			break
		}
		end = offset
	}
	if end == 0 {
		return "attachment"
	}
	return value[:end]
}

func legacyAttachmentContentID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func syncLegacyAttachmentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (c *Client) syncLegacyAttachmentDirectory(path string) error {
	if c.legacyAttachmentDirectorySyncHook != nil {
		return c.legacyAttachmentDirectorySyncHook(path)
	}
	return syncLegacyAttachmentDirectory(path)
}

func (c *Client) ensureDurableLegacyAttachmentDirectory(path string) error {
	err := os.Mkdir(path, 0o755)
	if err != nil && !os.IsExist(err) {
		return err
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return statErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("attachment storage path is not a directory")
	}
	if err := c.syncLegacyAttachmentDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync attachment directory parent: %w", err)
	}
	return nil
}

func (c *Client) runLegacyAttachmentMigrationFailureHook(stage string) error {
	if c.legacyAttachmentMigrationFailureHook == nil {
		return nil
	}
	if err := c.legacyAttachmentMigrationFailureHook(stage); err != nil {
		return fmt.Errorf("legacy attachment migration hook %s: %w", stage, err)
	}
	return nil
}
