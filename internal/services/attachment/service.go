package attachment

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/riordanpawley/azedarach/internal/dbpathguard"
	"github.com/riordanpawley/azedarach/internal/observability/tracesqlite"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

const (
	legacyImageCollection = "images"
	attachmentCollection  = "attachments"
	clipboardReadTimeout  = 20 * time.Second
)

var errAttachmentNotFound = errors.New("attachment not found")

// Service manages file attachments for issues.
type Service struct {
	issuesPath string
	dbPath     string
	dbPathErr  error
	logger     *slog.Logger
}

// Attachment represents a file attachment
type Attachment struct {
	ID       string    `json:"id"`
	IssueID  string    `json:"issue_id"`
	Filename string    `json:"filename"`
	Path     string    `json:"path"`
	MimeType string    `json:"mime_type"`
	Size     int64     `json:"size"`
	Created  time.Time `json:"created"`
	Relative string    `json:"relative_path,omitempty"`
}

func IsMarkdown(att Attachment) bool {
	mimeType := strings.ToLower(strings.TrimSpace(att.MimeType))
	if mimeType == "text/markdown" || mimeType == "text/x-markdown" {
		return true
	}
	switch strings.ToLower(filepath.Ext(att.Filename)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	default:
		return false
	}
}

func IsImage(att Attachment) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(att.MimeType)), "image/")
}

// NewService creates a generic attachment service. Images use the same shared
// attachment storage as documents. Legacy imports are explicit write-side
// operations; reads preserve legacy visibility without mutating storage.
func NewService(issuesPath string, logger *slog.Logger) *Service {
	return newService(issuesPath, logger)
}

// NewDocumentService creates a service for non-image report/document attachments.
func NewDocumentService(issuesPath string, logger *slog.Logger) *Service {
	return newService(issuesPath, logger)
}

// NewUnifiedService creates a service for the TUI attachment flows.
func NewUnifiedService(issuesPath string, logger *slog.Logger) *Service {
	return newService(issuesPath, logger)
}

func newService(issuesPath string, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	dbPath, dbPathErr := resolveDBPath(issuesPath)
	return &Service{
		issuesPath: issuesPath,
		dbPath:     dbPath,
		dbPathErr:  dbPathErr,
		logger:     logger,
	}
}

// Attach copies a file from sourcePath to shared attachment storage.
func (s *Service) Attach(ctx context.Context, issueID string, sourcePath string) (*Attachment, error) {
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	s.logger.Debug("attaching file", "issue_id", issueID, "source", sourcePath)

	// Verify source file exists
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source file not found: %w", err)
	}

	// Open source file
	src, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open source file: %w", err)
	}
	defer src.Close()

	// Read file content
	data, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("failed to read source file: %w", err)
	}

	// Get base filename
	filename := filepath.Base(sourcePath)

	// Create attachment
	return s.createAttachment(ctx, issueID, filename, data, info.Size())
}

// AttachFromClipboard reads an image from the clipboard and attaches it
func (s *Service) AttachFromClipboard(ctx context.Context, issueID string) (*Attachment, error) {
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	s.logger.Info("attaching image from clipboard", "issue_id", issueID)
	readCtx, cancel := context.WithTimeout(ctx, clipboardReadTimeout)
	defer cancel()

	// Read image from clipboard
	data, err := ReadImageFromClipboard(readCtx)
	if err != nil {
		s.logger.Warn("clipboard image read failed", "issue_id", issueID, "error", err)
		return nil, fmt.Errorf("failed to read clipboard: %w", err)
	}

	if len(data) == 0 {
		s.logger.Warn("clipboard image read returned empty payload", "issue_id", issueID)
		return nil, fmt.Errorf("clipboard is empty or does not contain an image")
	}

	// Determine mime type from data
	mimeType := detectMimeType(data, "")
	ext := mimeTypeToExt(mimeType)

	// Generate filename with timestamp
	filename := fmt.Sprintf("clipboard-%s%s", time.Now().Format("20060102-150405"), ext)
	s.logger.Info("clipboard image read succeeded", "issue_id", issueID, "bytes", len(data), "mime_type", mimeType, "filename", filename)
	attachment, err := s.createAttachment(ctx, issueID, filename, data, int64(len(data)))
	if err != nil {
		s.logger.Warn("clipboard attachment write failed", "issue_id", issueID, "filename", filename, "error", err)
		return nil, err
	}
	s.logger.Info("clipboard image attached", "issue_id", issueID, "attachment_id", attachment.ID, "path", attachment.Path)
	return attachment, nil
}

// List returns all attachments for a given issue
func (s *Service) List(ctx context.Context, issueID string) ([]Attachment, error) {
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	s.logger.Debug("listing attachments", "issue_id", issueID)
	attachments, err := s.listAttachmentReferences(ctx, issueID)
	if err != nil {
		return nil, err
	}
	legacy, err := s.listLegacyImageReferences(issueID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(attachments))
	for _, attachment := range attachments {
		seen[attachment.ID] = struct{}{}
	}
	for _, attachment := range legacy {
		if _, found := seen[attachment.ID]; found {
			continue
		}
		attachments = append(attachments, attachment)
	}
	sort.SliceStable(attachments, func(i, j int) bool {
		if attachments[i].Created.Equal(attachments[j].Created) {
			return attachments[i].Filename < attachments[j].Filename
		}
		return attachments[i].Created.Before(attachments[j].Created)
	})
	s.logger.Debug("found attachments", "count", len(attachments))
	return attachments, nil
}

func (s *Service) listLegacyImageReferences(issueID string) ([]Attachment, error) {
	dir := s.getIssueCollectionDir(issueID, legacyImageCollection)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read legacy image attachments: %w", err)
	}
	attachments := make([]Attachment, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect legacy image attachment %q: %w", entry.Name(), err)
		}
		attachmentID, filename := parseLegacyAttachmentFilename(entry.Name())
		if attachmentID == "" {
			sum := sha256.Sum256([]byte(entry.Name()))
			attachmentID = "legacy-" + hex.EncodeToString(sum[:4])
		}
		attachments = append(attachments, Attachment{
			ID: attachmentID, IssueID: issueID, Filename: filename,
			Path: filepath.Join(dir, entry.Name()), MimeType: detectMimeType(nil, filename),
			Size: info.Size(), Created: info.ModTime().UTC(),
			Relative: filepath.ToSlash(filepath.Join(".azedarach", legacyImageCollection, issueID, entry.Name())),
		})
	}
	return attachments, nil
}

// MigrateLegacy performs the explicit write-side compatibility migration for
// legacy attachment schemas and image files. Read paths must never call it.
func (s *Service) MigrateLegacy(ctx context.Context, issueID string) error {
	if err := s.checkReady(); err != nil {
		return err
	}
	db, err := s.openDB()
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close attachment database after schema migration: %w", err)
	}
	return s.migrateLegacyImages(ctx, issueID)
}

func (s *Service) listAttachmentReferences(ctx context.Context, issueID string) ([]Attachment, error) {
	db, err := s.openReadOnlyDB()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Attachment{}, nil
		}
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT attachment_id, filename, relative_path, mime_type, size, created_at
		FROM issue_attachments
		WHERE issue_id = ?
		ORDER BY created_at, filename
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("query issue attachment references: %w", err)
	}
	defer rows.Close()

	attachments := make([]Attachment, 0)
	for rows.Next() {
		var (
			id        string
			filename  string
			relative  string
			mimeType  string
			size      int64
			createdAt string
		)
		if err := rows.Scan(&id, &filename, &relative, &mimeType, &size, &createdAt); err != nil {
			return nil, fmt.Errorf("scan issue attachment reference: %w", err)
		}
		fullPath := s.pathFromRelative(relative)
		mimeType = strings.TrimSpace(mimeType)
		if mimeType == "" {
			mimeType = detectMimeTypeFromFile(fullPath)
		}
		created, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			created = time.Time{}
		}
		if info, err := os.Stat(fullPath); err == nil {
			if size == 0 {
				size = info.Size()
			}
			if created.IsZero() {
				created = info.ModTime()
			}
		}
		if created.IsZero() {
			created = time.Now()
		}

		attachments = append(attachments, Attachment{
			ID:       id,
			IssueID:  issueID,
			Filename: filename,
			Path:     fullPath,
			MimeType: mimeType,
			Size:     size,
			Created:  created,
			Relative: relative,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate issue attachment references: %w", err)
	}

	return attachments, nil
}

func (s *Service) openReadOnlyDB() (*sql.DB, error) {
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.dbPath) == "" {
		return nil, fmt.Errorf("attachment database path is empty")
	}
	if err := dbpathguard.Check(s.dbPath); err != nil {
		return nil, fmt.Errorf("refuse attachment database: %w", err)
	}
	if _, err := os.Stat(s.dbPath); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf(
		"file:%s?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=query_only(ON)",
		filepath.ToSlash(s.dbPath),
	)
	db, err := tracesqlite.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("open attachment database read-only: %w", err)
	}
	columns, err := issueAttachmentColumns(db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("inspect issue attachment schema: %w", err)
	}
	if !issueAttachmentSchemaIsCurrent(columns) {
		_ = db.Close()
		return nil, fmt.Errorf("issue attachment schema is unavailable or incompatible; run project database migrations")
	}
	return db, nil
}

// Delete removes an attachment by ID
func (s *Service) Delete(ctx context.Context, issueID, attachmentID string) error {
	if err := s.checkReady(); err != nil {
		return err
	}
	s.logger.Debug("deleting attachment", "issue_id", issueID, "attachment_id", attachmentID)

	if err := s.migrateLegacyImages(ctx, issueID); err != nil {
		return err
	}
	if err := s.deleteAttachmentReference(ctx, issueID, attachmentID); err != nil {
		if errors.Is(err, errAttachmentNotFound) {
			return fmt.Errorf("attachment not found: %s", attachmentID)
		}
		return err
	}
	return nil
}

// GetPath returns the full path to an attachment
func (s *Service) GetPath(issueID, filename string) string {
	return filepath.Join(s.getSharedAttachmentDir(), filename)
}

// createAttachment creates a new attachment file
func (s *Service) createAttachment(ctx context.Context, issueID, filename string, data []byte, size int64) (*Attachment, error) {
	return s.createSharedAttachment(ctx, issueID, filename, data, size, time.Now(), "")
}

func (s *Service) createSharedAttachment(ctx context.Context, issueID, filename string, data []byte, size int64, created time.Time, attachmentID string) (*Attachment, error) {
	dir := s.getSharedAttachmentDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create attachments directory: %w", err)
	}

	contentID := contentID(data)
	if strings.TrimSpace(attachmentID) == "" {
		attachmentID = contentID
	}
	newFilename := fmt.Sprintf("%s-%s", contentID, filename)
	destPath := filepath.Join(dir, newFilename)
	if _, err := os.Stat(destPath); os.IsNotExist(err) {
		if err := os.WriteFile(destPath, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to write attachment: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to inspect attachment: %w", err)
	}

	mimeType := detectMimeType(data, filename)
	if created.IsZero() {
		created = time.Now()
	}
	relative := filepath.ToSlash(filepath.Join(".azedarach", attachmentCollection, newFilename))
	attachment := &Attachment{
		ID:       attachmentID,
		IssueID:  issueID,
		Filename: newFilename,
		Path:     destPath,
		MimeType: mimeType,
		Size:     size,
		Created:  created,
		Relative: relative,
	}
	if err := s.writeAttachmentReference(ctx, *attachment); err != nil {
		return nil, err
	}

	s.logger.Debug("attachment created", "id", attachmentID, "path", destPath)
	return attachment, nil
}

func (s *Service) writeAttachmentReference(ctx context.Context, att Attachment) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, err := s.openDB()
		if err != nil {
			return err
		}
		defer db.Close()

		_, err = db.ExecContext(ctx, `
			INSERT INTO issue_attachments (
				issue_id, attachment_id, filename, relative_path, mime_type, size, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(issue_id, attachment_id) DO UPDATE SET
				filename = excluded.filename,
				relative_path = excluded.relative_path,
				mime_type = excluded.mime_type,
				size = excluded.size,
				created_at = excluded.created_at
		`,
			att.IssueID,
			att.ID,
			att.Filename,
			att.Relative,
			att.MimeType,
			att.Size,
			att.Created.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("write issue attachment reference: %w", err)
		}
		return nil
	})
}

func (s *Service) deleteAttachmentReference(ctx context.Context, issueID, attachmentID string) error {
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, err := s.openDB()
		if err != nil {
			return err
		}
		defer db.Close()

		result, err := db.ExecContext(ctx, `
			DELETE FROM issue_attachments
			WHERE issue_id = ? AND attachment_id = ?
		`, issueID, attachmentID)
		if err != nil {
			return fmt.Errorf("delete issue attachment reference: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect deleted issue attachment reference: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("%w: %s", errAttachmentNotFound, attachmentID)
		}
		s.logger.Debug("attachment reference deleted", "issue_id", issueID, "attachment_id", attachmentID)
		return nil
	})
}

func (s *Service) migrateLegacyImages(ctx context.Context, issueID string) error {
	dir := s.getIssueCollectionDir(issueID, legacyImageCollection)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read legacy image attachments: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		legacyPath := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect legacy image attachment %q: %w", legacyPath, err)
		}
		data, err := os.ReadFile(legacyPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read legacy image attachment %q: %w", legacyPath, err)
		}

		attachmentID, originalFilename := parseLegacyAttachmentFilename(entry.Name())
		created := info.ModTime()
		attachment, err := s.createSharedAttachment(ctx, issueID, originalFilename, data, info.Size(), created, attachmentID)
		if err != nil {
			return fmt.Errorf("migrate legacy image attachment %q: %w", legacyPath, err)
		}
		oldRelative := filepath.ToSlash(filepath.Join(".azedarach", legacyImageCollection, issueID, entry.Name()))
		if err := s.rewriteLegacyImageNoteLink(ctx, issueID, oldRelative, attachment.Relative); err != nil {
			return fmt.Errorf("rewrite legacy image note link %q: %w", oldRelative, err)
		}
		if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove migrated legacy image attachment %q: %w", legacyPath, err)
		}
	}

	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) && !errors.Is(err, os.ErrNotExist) {
		if !errors.Is(err, syscall.ENOTEMPTY) {
			return fmt.Errorf("remove migrated legacy image directory %q: %w", dir, err)
		}
	}
	legacyRoot := filepath.Join(s.issuesPath, legacyImageCollection)
	if err := os.Remove(legacyRoot); err != nil && !os.IsNotExist(err) && !errors.Is(err, os.ErrNotExist) {
		if !errors.Is(err, syscall.ENOTEMPTY) {
			return fmt.Errorf("remove empty legacy image root %q: %w", legacyRoot, err)
		}
	}
	return nil
}

func (s *Service) rewriteLegacyImageNoteLink(ctx context.Context, issueID, oldRelative, newRelative string) error {
	oldRelative = strings.TrimSpace(oldRelative)
	newRelative = strings.TrimSpace(newRelative)
	if oldRelative == "" || newRelative == "" {
		return nil
	}
	return sqliteutil.WithWriteLock(s.dbPath, func() error {
		db, err := s.openDB()
		if err != nil {
			return err
		}
		defer db.Close()

		var tableName string
		err = db.QueryRowContext(ctx, `
			SELECT name
			FROM sqlite_master
			WHERE type = 'table' AND name = 'issues'
		`).Scan(&tableName)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect issues table: %w", err)
		}

		_, err = db.ExecContext(ctx, `
			UPDATE issues
			SET notes = REPLACE(notes, ?, ?)
			WHERE id = ? AND notes LIKE ?
		`, oldRelative, newRelative, issueID, "%"+oldRelative+"%")
		if err != nil {
			return fmt.Errorf("rewrite issue note attachment link: %w", err)
		}
		return nil
	})
}

func parseLegacyAttachmentFilename(filename string) (attachmentID, originalFilename string) {
	parts := strings.SplitN(filename, "-", 2)
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return parts[0], parts[1]
	}
	return "", filename
}

func (s *Service) openDB() (*sql.DB, error) {
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.dbPath) == "" {
		return nil, fmt.Errorf("attachment database path is empty")
	}
	if err := dbpathguard.Check(s.dbPath); err != nil {
		return nil, fmt.Errorf("refuse attachment database: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create attachment database directory: %w", err)
	}
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		filepath.ToSlash(s.dbPath),
	)
	db, err := tracesqlite.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("open attachment database: %w", err)
	}
	if err := s.ensureIssueAttachmentSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Service) checkReady() error {
	if s == nil {
		return fmt.Errorf("attachment service is nil")
	}
	if s.dbPathErr != nil {
		return fmt.Errorf("resolve attachment database path: %w", s.dbPathErr)
	}
	return nil
}

func (s *Service) ensureIssueAttachmentSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issue_attachments (
			issue_id TEXT NOT NULL,
			attachment_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			PRIMARY KEY (issue_id, attachment_id)
		);
		CREATE INDEX IF NOT EXISTS idx_issue_attachments_attachment_id
			ON issue_attachments(attachment_id);
	`)
	if err != nil {
		return fmt.Errorf("ensure issue attachment schema: %w", err)
	}
	columns, err := issueAttachmentColumns(db)
	if err != nil {
		return fmt.Errorf("inspect issue attachment schema: %w", err)
	}
	if issueAttachmentSchemaNeedsBlobMigration(columns) {
		if err := s.migrateBlobAttachmentSchema(db); err != nil {
			return err
		}
		columns, err = issueAttachmentColumns(db)
		if err != nil {
			return fmt.Errorf("inspect migrated issue attachment schema: %w", err)
		}
	}
	if !issueAttachmentSchemaIsCurrent(columns) {
		return fmt.Errorf("issue attachment schema is incompatible")
	}
	return nil
}

func resolveDBPath(issuesPath string) (string, error) {
	candidate := filepath.Join(issuesPath, "azedarach.db")
	if fromEnv := strings.TrimSpace(os.Getenv("AZEDARACH_DB_PATH")); fromEnv != "" {
		useOverride, err := dbpathguard.UseProjectOverride(candidate, fromEnv)
		if err != nil {
			return "", fmt.Errorf("resolve test database override: %w", err)
		}
		if useOverride {
			return fromEnv, nil
		}
	}
	return candidate, nil
}

func (s *Service) pathFromRelative(relative string) string {
	trimmed := strings.TrimPrefix(filepath.ToSlash(relative), ".azedarach/")
	if trimmed == relative {
		return filepath.FromSlash(relative)
	}
	return filepath.Join(s.issuesPath, filepath.FromSlash(trimmed))
}

func (s *Service) getIssueCollectionDir(issueID, collection string) string {
	return filepath.Join(s.issuesPath, collection, issueID)
}

func (s *Service) getSharedAttachmentDir() string {
	return filepath.Join(s.issuesPath, attachmentCollection)
}

func contentID(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

type issueAttachmentColumnSet map[string]bool

type blobAttachmentReference struct {
	IssueID      string
	AttachmentID string
	Filename     string
	Relative     string
	MimeType     string
	Size         int64
	CreatedAt    string
}

func issueAttachmentColumns(db *sql.DB) (issueAttachmentColumnSet, error) {
	rows, err := db.Query(`PRAGMA table_info(issue_attachments)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := issueAttachmentColumnSet{}
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func issueAttachmentSchemaNeedsBlobMigration(columns issueAttachmentColumnSet) bool {
	return columns["content_blob"]
}

func issueAttachmentSchemaIsCurrent(columns issueAttachmentColumnSet) bool {
	for _, required := range []string{"issue_id", "attachment_id", "filename", "relative_path", "mime_type", "size", "created_at"} {
		if !columns[required] {
			return false
		}
	}
	return true
}

func (s *Service) migrateBlobAttachmentSchema(db *sql.DB) error {
	refs, err := s.readBlobAttachmentReferences(db)
	if err != nil {
		return err
	}
	if err := replaceIssueAttachmentTable(db, refs); err != nil {
		return err
	}
	return nil
}

func (s *Service) readBlobAttachmentReferences(db *sql.DB) ([]blobAttachmentReference, error) {
	columns, err := issueAttachmentColumns(db)
	if err != nil {
		return nil, fmt.Errorf("inspect legacy issue attachment columns: %w", err)
	}
	if !columns["content_blob"] {
		return nil, nil
	}

	rows, err := db.Query(`
		SELECT issue_id, attachment_id, filename, original_path, mime_type, size_bytes, created_at, content_blob
		FROM issue_attachments
		ORDER BY created_at, filename
	`)
	if err != nil {
		return nil, fmt.Errorf("read legacy issue attachment rows: %w", err)
	}
	defer rows.Close()

	if err := os.MkdirAll(s.getSharedAttachmentDir(), 0755); err != nil {
		return nil, fmt.Errorf("create attachments directory for legacy rows: %w", err)
	}

	refs := make([]blobAttachmentReference, 0)
	for rows.Next() {
		var (
			issueID      string
			attachmentID string
			filename     string
			originalPath sql.NullString
			mimeType     sql.NullString
			sizeBytes    sql.NullInt64
			createdAt    sql.NullString
			data         []byte
		)
		if err := rows.Scan(&issueID, &attachmentID, &filename, &originalPath, &mimeType, &sizeBytes, &createdAt, &data); err != nil {
			return nil, fmt.Errorf("scan legacy issue attachment row: %w", err)
		}
		name := strings.TrimSpace(filename)
		if name == "" {
			name = strings.TrimSpace(filepath.Base(originalPath.String))
		}
		name = filepath.Base(filepath.FromSlash(name))
		if name == "" || name == "." || name == string(os.PathSeparator) {
			name = "attachment" + mimeTypeToExt(strings.TrimSpace(mimeType.String))
		}

		newFilename := fmt.Sprintf("%s-%s", contentID(data), name)
		destPath := filepath.Join(s.getSharedAttachmentDir(), newFilename)
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			if err := os.WriteFile(destPath, data, 0644); err != nil {
				return nil, fmt.Errorf("write legacy attachment blob %q: %w", destPath, err)
			}
		} else if err != nil {
			return nil, fmt.Errorf("inspect legacy attachment blob %q: %w", destPath, err)
		}

		size := sizeBytes.Int64
		if size <= 0 {
			size = int64(len(data))
		}
		created := strings.TrimSpace(createdAt.String)
		if created == "" {
			created = time.Now().UTC().Format(time.RFC3339Nano)
		}
		mime := strings.TrimSpace(mimeType.String)
		if mime == "" {
			mime = detectMimeType(data, name)
		}

		refs = append(refs, blobAttachmentReference{
			IssueID:      issueID,
			AttachmentID: attachmentID,
			Filename:     newFilename,
			Relative:     filepath.ToSlash(filepath.Join(".azedarach", attachmentCollection, newFilename)),
			MimeType:     mime,
			Size:         size,
			CreatedAt:    created,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy issue attachment rows: %w", err)
	}
	return refs, nil
}

func replaceIssueAttachmentTable(db *sql.DB, refs []blobAttachmentReference) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin issue attachment schema migration: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(`
		DROP TABLE IF EXISTS issue_attachments_migration_new;
		CREATE TABLE issue_attachments_migration_new (
			issue_id TEXT NOT NULL,
			attachment_id TEXT NOT NULL,
			filename TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			PRIMARY KEY (issue_id, attachment_id)
		);
	`); err != nil {
		return fmt.Errorf("create replacement issue attachment table: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO issue_attachments_migration_new (
			issue_id, attachment_id, filename, relative_path, mime_type, size, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare replacement issue attachment insert: %w", err)
	}

	for _, ref := range refs {
		if _, err := stmt.Exec(ref.IssueID, ref.AttachmentID, ref.Filename, ref.Relative, ref.MimeType, ref.Size, ref.CreatedAt); err != nil {
			_ = stmt.Close()
			return fmt.Errorf("copy issue attachment reference %q/%q: %w", ref.IssueID, ref.AttachmentID, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return fmt.Errorf("close replacement issue attachment insert: %w", err)
	}

	if _, err := tx.Exec(`
		DROP TABLE issue_attachments;
		ALTER TABLE issue_attachments_migration_new RENAME TO issue_attachments;
		CREATE INDEX IF NOT EXISTS idx_issue_attachments_attachment_id
			ON issue_attachments(attachment_id);
	`); err != nil {
		return fmt.Errorf("replace issue attachment table: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit issue attachment schema migration: %w", err)
	}
	tx = nil
	return nil
}

// detectMimeType detects the MIME type from file data
func detectMimeType(data []byte, filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return "text/markdown"
	case ".txt", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".yaml", ".yml":
		return "application/yaml"
	}

	if len(data) == 0 {
		return "application/octet-stream"
	}

	// PNG signature (8 bytes)
	if len(data) >= 8 && data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}

	// JPEG signature (3 bytes)
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// GIF signature (6 bytes)
	if len(data) >= 6 && (string(data[0:6]) == "GIF89a" || string(data[0:6]) == "GIF87a") {
		return "image/gif"
	}

	// WebP signature (12 bytes)
	if len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}

	// TIFF signature (4 bytes): little-endian II*\x00 or big-endian MM\x00*
	if len(data) >= 4 {
		if (data[0] == 0x49 && data[1] == 0x49 && data[2] == 0x2A && data[3] == 0x00) ||
			(data[0] == 0x4D && data[1] == 0x4D && data[2] == 0x00 && data[3] == 0x2A) {
			return "image/tiff"
		}
	}

	// BMP signature (2 bytes)
	if len(data) >= 2 && data[0] == 0x42 && data[1] == 0x4D {
		return "image/bmp"
	}

	return "application/octet-stream"
}

// detectMimeTypeFromFile detects MIME type from file path
func detectMimeTypeFromFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".markdown", ".mdown", ".mkd":
		return "text/markdown"
	case ".txt", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		// Try to read file and detect
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			return "application/octet-stream"
		}
		return detectMimeType(data, path)
	}
}

// mimeTypeToExt converts MIME type to file extension
func mimeTypeToExt(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tiff"
	case "image/bmp":
		return ".bmp"
	default:
		return ".bin"
	}
}
