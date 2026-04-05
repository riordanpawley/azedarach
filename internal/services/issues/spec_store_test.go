package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_SpecMigrationsFreshDBAndIdempotency(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	requirement, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "REQ-1",
		ExternalCode: stringPtr("EXT-1"),
		Title:        "Fresh migration",
		Status:       RequirementStatusOpen,
	})
	require.NoError(t, err)
	assert.Equal(t, "REQ-1", requirement.LocalID)

	db, err := client.dbHandle()
	require.NoError(t, err)

	assertSpecIndexesPresent(t, db)
	assertSpecAuditIndexesPresent(t, db)

	require.NoError(t, client.CloseDB())
	_, err = client.dbHandle()
	require.NoError(t, err)
	db, err = client.dbHandle()
	require.NoError(t, err)

	var migrationCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id IN ('0004_spec_tables', '0005_spec_audit_log')`).Scan(&migrationCount))
	assert.Equal(t, 2, migrationCount)

	_, err = client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "REQ-1",
		ExternalCode: stringPtr("EXT-2"),
		Title:        "duplicate id",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConflict)

	_, err = client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "REQ-2",
		ExternalCode: stringPtr("EXT-1"),
		Title:        "duplicate external code",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConflict)

	require.NoError(t, client.DeleteRequirement(ctx, "REQ-1"))

	recreated, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "REQ-1",
		ExternalCode: stringPtr("EXT-1"),
		Title:        "recreated after delete",
		Status:       RequirementStatusAccepted,
	})
	require.NoError(t, err)
	assert.Equal(t, RequirementStatusAccepted, recreated.Status)
}

func TestClient_MigratesLegacySpecSchemaWithoutDataLoss(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy-spec.db")
	db := openSQLiteDB(t, dbPath)

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS spec_requirements (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			local_id TEXT NOT NULL,
			external_code TEXT,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		);
		INSERT INTO spec_requirements (local_id, external_code, title, description, status, created_at, updated_at, deleted_at)
		VALUES ('REQ-LEGACY', 'LEG-1', 'Legacy requirement', 'kept during migration', 'open', '2026-03-31T00:00:00Z', '2026-03-31T00:00:00Z', NULL);
	`)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	got, err := client.GetRequirement(ctx, "REQ-LEGACY")
	require.NoError(t, err)
	assert.Equal(t, "Legacy requirement", got.Title)

	assertSpecIndexesPresent(t, db)
	assertSpecAuditIndexesPresent(t, db)

	rows, err := db.Query(`SELECT id FROM schema_migrations WHERE id IN ('0004_spec_tables', '0005_spec_audit_log') ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()

	var gotMigrations []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		gotMigrations = append(gotMigrations, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"0004_spec_tables", "0005_spec_audit_log"}, gotMigrations)
}

func TestClient_ListRequirements_WithTextPrimaryKeyIDs_AutoMigratesSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "text-id-spec.db")
	db := openSQLiteDB(t, dbPath)

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS spec_requirements (
			id TEXT PRIMARY KEY,
			local_id TEXT NOT NULL,
			external_code TEXT,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT
		);
		INSERT INTO spec_requirements (id, local_id, external_code, title, description, status, created_at, updated_at, deleted_at)
		VALUES ('sr_bf420304b9e84e75a5f8b594c494cf0f', 'REQ-TEXT-ID', NULL, 'Text ID requirement', 'compatible with text PK', 'open', '2026-04-01T00:00:00Z', '2026-04-01T00:00:00Z', NULL);
	`)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	requirements, err := client.ListRequirements(ctx, RequirementFilter{})
	require.NoError(t, err)
	require.Len(t, requirements, 1)
	assert.Equal(t, "REQ-TEXT-ID", requirements[0].LocalID)

	rows, err := db.Query(`PRAGMA table_info('spec_requirements')`)
	require.NoError(t, err)
	defer rows.Close()

	var (
		idType string
		idPK   int
	)
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey))
		if name == "id" {
			idType = strings.ToUpper(columnType)
			idPK = primaryKey
		}
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 1, idPK)
	assert.Contains(t, idType, "INT")
}

func TestClient_CreateRequirement_WithLegacySpecTableShape(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy-shape-create.db")
	db := openSQLiteDB(t, dbPath)

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			closed_at TEXT,
			assignee TEXT,
			labels_json TEXT,
			implementations_json TEXT,
			design TEXT,
			notes TEXT,
			acceptance TEXT,
			estimate INTEGER,
			deleted_at TEXT
		);
		CREATE TABLE IF NOT EXISTS issue_dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL,
			tombstoned_at TEXT,
			PRIMARY KEY (issue_id, depends_on_id, dependency_type)
		);
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS spec_requirements (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			body_md TEXT NOT NULL,
			kind TEXT NOT NULL,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			local_id TEXT,
			external_code TEXT,
			description TEXT,
			issue_id TEXT
		);
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, deleted_at)
		VALUES ('bpq', 'legacy issue', '', 'open', 2, 'task', '2026-04-01T00:00:00Z', '2026-04-01T00:00:00Z', NULL);
	`)
	require.NoError(t, err)

	client := NewClientAtPath(dbPath, slog.Default())
	t.Cleanup(func() {
		require.NoError(t, client.CloseDB())
	})

	created, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "bpq-log-integration",
		Title:        "Integrate charmbracelet/log as canonical structured logger",
		Description:  "Runtime and service logging should use charmbracelet/log consistently.",
		IssueID:      stringPtr("bpq"),
		ExternalCode: stringPtr("bpq-log-integration"),
	})
	require.NoError(t, err)
	assert.Equal(t, "bpq-log-integration", created.LocalID)

	got, err := client.GetRequirement(ctx, "bpq-log-integration")
	require.NoError(t, err)
	assert.Equal(t, "bpq", derefString(got.IssueID))
	assert.Equal(t, "Integrate charmbracelet/log as canonical structured logger", got.Title)
}

func TestClient_RequirementCRUDAndSelectorResolution(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "linked issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	requirement, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "REQ-100",
		ExternalCode: stringPtr("PAYLOAD-100"),
		Title:        "Keep bfs storage self-contained",
		Description:  "Only mutate storage package code",
		IssueID:      &issueID,
	})
	require.NoError(t, err)
	assert.Equal(t, issueID, derefString(requirement.IssueID))

	resolved, err := client.GetRequirement(ctx, "PAYLOAD-100")
	require.NoError(t, err)
	assert.Equal(t, requirement.LocalID, resolved.LocalID)

	updated, err := client.UpdateRequirement(ctx, "REQ-100", UpdateRequirementParams{
		Title:        stringPtr("Keep storage and audit self-contained"),
		Description:  stringPtr("Requirement updates stay in internal/services/issues"),
		Status:       requirementStatusPtr(RequirementStatusAccepted),
		ExternalCode: stringPtr("PAYLOAD-101"),
	})
	require.NoError(t, err)
	assert.Equal(t, RequirementStatusAccepted, updated.Status)
	assert.Equal(t, "PAYLOAD-101", derefString(updated.ExternalCode))

	listed, err := client.ListRequirements(ctx, RequirementFilter{
		LocalIDs: []string{"REQ-100"},
		Query:    "storage and audit",
	})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "REQ-100", listed[0].LocalID)

	require.NoError(t, client.DeleteRequirement(ctx, "PAYLOAD-101"))

	_, err = client.GetRequirement(ctx, "REQ-100")
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrNotFound)
}

func TestClient_SpecLinksCRUDAndAuditOrdering(t *testing.T) {
	ctx := WithSpecAuditActorSource(context.Background(), "spec-store-test")
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P1,
	})
	require.NoError(t, err)

	_, err = client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "REQ-LINK",
		ExternalCode: stringPtr("EXT-LINK"),
		Title:        "Link traceability",
		Status:       RequirementStatusOpen,
	})
	require.NoError(t, err)

	_, err = client.UpdateRequirement(ctx, "REQ-LINK", UpdateRequirementParams{
		Status: requirementStatusPtr(RequirementStatusAccepted),
	})
	require.NoError(t, err)

	link, err := client.AddSpecLink(ctx, AddSpecLinkParams{
		IssueID:           issueID,
		RequirementID:     "EXT-LINK",
		Role:              LinkRoleImplements,
		Note:              stringPtr("initial coverage"),
		Implementations:   []string{"go-bubbletea"},
		FulfillmentStatus: stringPtr("partial"),
	})
	require.NoError(t, err)
	assert.Equal(t, issueID+":REQ-LINK", link.ID)

	link, err = client.AddSpecLink(ctx, AddSpecLinkParams{
		IssueID:           issueID,
		RequirementID:     "REQ-LINK",
		Role:              LinkRoleVerifies,
		Note:              stringPtr("verified"),
		Implementations:   []string{"go-bubbletea", "go-bubbletea"},
		FulfillmentStatus: stringPtr("complete"),
	})
	require.NoError(t, err)
	assert.Equal(t, LinkRoleVerifies, link.Role)
	assert.Equal(t, []string{"go-bubbletea"}, link.Implementations)

	gotLink, err := client.GetSpecLink(ctx, issueID, "REQ-LINK")
	require.NoError(t, err)
	assert.Equal(t, LinkRoleVerifies, gotLink.Role)

	links, err := client.ListSpecLinks(ctx, SpecLinkFilter{
		LinkIDs: []string{issueID + ":REQ-LINK"},
	})
	require.NoError(t, err)
	require.Len(t, links, 1)

	require.NoError(t, client.RemoveSpecLink(ctx, issueID, "REQ-LINK"))

	entries, err := client.ListSpecAuditEntries(ctx, SpecAuditFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 5)

	var operations []string
	for _, entry := range entries {
		operations = append(operations, entry.Operation)
		assert.True(t, json.Valid(entry.BeforeJSON))
		assert.True(t, json.Valid(entry.AfterJSON))
		assert.Equal(t, "spec-store-test", entry.ActorSource)
	}
	assert.Equal(t, []string{"create", "update", "create", "update", "delete"}, operations)

	assert.Equal(t, specAuditEntityRequirement, entries[0].EntityType)
	assert.Equal(t, specAuditEntityRequirement, entries[1].EntityType)
	assert.Equal(t, specAuditEntityLink, entries[2].EntityType)
	assert.Equal(t, specAuditEntityLink, entries[3].EntityType)
	assert.Equal(t, specAuditEntityLink, entries[4].EntityType)

	filtered, err := client.ListSpecAuditEntries(ctx, SpecAuditFilter{
		EntityType: specAuditEntityLink,
		EntityID:   issueID + ":REQ-LINK",
	})
	require.NoError(t, err)
	require.Len(t, filtered, 3)
}

func TestClient_AuditInsertFailureRollsBackRequirementMutation(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	db, err := client.dbHandle()
	require.NoError(t, err)

	_, err = db.Exec(`
		CREATE TRIGGER spec_audit_log_fail_requirement
		BEFORE INSERT ON spec_audit_log
		WHEN NEW.entity_type = 'spec_requirement'
		BEGIN
			SELECT RAISE(ABORT, 'blocked requirement audit');
		END;
	`)
	require.NoError(t, err)

	_, err = client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "REQ-FAIL",
		Title:   "should rollback",
	})
	require.Error(t, err)

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM spec_requirements WHERE local_id = 'REQ-FAIL'`).Scan(&count))
	assert.Equal(t, 0, count)
}

func TestClient_AuditInsertFailureRollsBackLinkMutation(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	_, err = client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "REQ-ATOMIC",
		Title:   "atomic links",
	})
	require.NoError(t, err)

	db, err := client.dbHandle()
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TRIGGER spec_audit_log_fail_link
		BEFORE INSERT ON spec_audit_log
		WHEN NEW.entity_type = 'spec_link'
		BEGIN
			SELECT RAISE(ABORT, 'blocked link audit');
		END;
	`)
	require.NoError(t, err)

	_, err = client.AddSpecLink(ctx, AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-ATOMIC",
		Role:          LinkRoleImplements,
	})
	require.Error(t, err)

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM spec_links`).Scan(&count))
	assert.Equal(t, 0, count)
}

func TestClient_ListSpecAuditEntriesSupportsTimeWindow(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	_, err := client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "REQ-WINDOW",
		Title:   "time window",
	})
	require.NoError(t, err)

	entries, err := client.ListSpecAuditEntries(ctx, SpecAuditFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	from := entries[0].CreatedAt.Add(-time.Second)
	to := entries[0].CreatedAt.Add(time.Second)
	filtered, err := client.ListSpecAuditEntries(ctx, SpecAuditFilter{
		EntityType:  specAuditEntityRequirement,
		EntityID:    "REQ-WINDOW",
		CreatedFrom: &from,
		CreatedTo:   &to,
	})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
}

func TestClient_ListSpecLinksRequirementSelectorRejectsAmbiguousLocalIDAndExternalCode(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	issueID, err := client.Create(ctx, CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	require.NoError(t, err)

	// Requirement A: selector value appears as local_id.
	_, err = client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID: "REQ-AMB",
		Title:   "local-id requirement",
	})
	require.NoError(t, err)

	_, err = client.AddSpecLink(ctx, AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-AMB",
		Role:          LinkRoleImplements,
	})
	require.NoError(t, err)

	// Requirement B: same selector value appears as external_code.
	ext := "REQ-AMB"
	_, err = client.CreateRequirement(ctx, CreateRequirementParams{
		LocalID:      "REQ-OTHER",
		ExternalCode: &ext,
		Title:        "external-code requirement",
	})
	require.NoError(t, err)

	_, err = client.AddSpecLink(ctx, AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-OTHER",
		Role:          LinkRoleRelates,
	})
	require.NoError(t, err)

	_, err = client.ListSpecLinks(ctx, SpecLinkFilter{
		IssueID:       issueID,
		RequirementID: "REQ-AMB",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrConflict)
}

func openSQLiteDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertSpecIndexesPresent(t *testing.T, db *sql.DB) {
	t.Helper()
	indexes := []string{
		"idx_spec_requirements_active_local_id",
		"idx_spec_requirements_active_external_code",
		"idx_spec_requirements_issue_status_updated",
		"idx_spec_requirements_updated",
		"idx_spec_links_active_issue_requirement",
		"idx_spec_links_issue_role_updated",
		"idx_spec_links_requirement_role_updated",
	}
	assertIndexesPresent(t, db, indexes)
}

func assertSpecAuditIndexesPresent(t *testing.T, db *sql.DB) {
	t.Helper()
	assertIndexesPresent(t, db, []string{
		"idx_spec_audit_entity_created_at",
		"idx_spec_audit_created_at",
	})
}

func assertIndexesPresent(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	args := make([]any, 0, len(want))
	placeholders := make([]string, 0, len(want))
	for _, name := range want {
		placeholders = append(placeholders, "?")
		args = append(args, name)
	}

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND name IN (`+strings.Join(placeholders, ",")+`) ORDER BY name`, args...)
	require.NoError(t, err)
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		got = append(got, name)
	}
	require.NoError(t, rows.Err())
	assert.ElementsMatch(t, want, got)
}

func stringPtr(value string) *string {
	return &value
}

func requirementStatusPtr(value RequirementStatus) *RequirementStatus {
	return &value
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
