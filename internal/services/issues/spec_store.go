package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type specAuditActorSourceKey struct{}

const defaultSpecAuditActorSource = "internal/services/issues"

const (
	specAuditEntityRequirement = "spec_requirement"
	specAuditEntityLink        = "spec_link"
	specAuditOpCreate          = "create"
	specAuditOpUpdate          = "update"
	specAuditOpDelete          = "delete"
)

type RequirementStatus string

const (
	RequirementStatusOpen       RequirementStatus = "open"
	RequirementStatusAccepted   RequirementStatus = "accepted"
	RequirementStatusSuperseded RequirementStatus = "superseded"
)

type Requirement struct {
	LocalID      string            `json:"id"`
	ExternalCode *string           `json:"external_code"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	IssueID      *string           `json:"issue_id"`
	Status       RequirementStatus `json:"status"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	DeletedAt    *time.Time        `json:"deleted_at"`
}

type CreateRequirementParams struct {
	LocalID      string
	ExternalCode *string
	Title        string
	Description  string
	IssueID      *string
	Status       RequirementStatus
}

type UpdateRequirementParams struct {
	ExternalCode *string
	Title        *string
	Description  *string
	IssueID      *string
	Status       *RequirementStatus
}

type RequirementFilter struct {
	IssueID        string
	Statuses       []RequirementStatus
	LocalIDs       []string
	Query          string
	IncludeDeleted bool
}

type LinkRole string

const (
	LinkRoleImplements LinkRole = "implements"
	LinkRoleVerifies   LinkRole = "verifies"
	LinkRoleRelates    LinkRole = "relates"
)

type SpecLink struct {
	ID                string     `json:"id"`
	IssueID           string     `json:"issue_id"`
	RequirementID     string     `json:"req_id"`
	Role              LinkRole   `json:"role"`
	Note              *string    `json:"note"`
	Implementations   []string   `json:"implementations"`
	FulfillmentStatus *string    `json:"fulfillment_status"`
	FulfilledAt       *time.Time `json:"fulfilled_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	DeletedAt         *time.Time `json:"deleted_at"`
}

type AddSpecLinkParams struct {
	IssueID           string
	RequirementID     string
	Role              LinkRole
	Note              *string
	Implementations   []string
	FulfillmentStatus *string
	FulfilledAt       *time.Time
}

type SpecLinkFilter struct {
	IssueID        string
	RequirementID  string
	LinkIDs        []string
	IncludeDeleted bool
}

type SpecAuditEntry struct {
	ID          int64           `json:"id"`
	EntityType  string          `json:"entity_type"`
	EntityID    string          `json:"entity_id"`
	Operation   string          `json:"operation"`
	ActorSource string          `json:"actor_source"`
	BeforeJSON  json.RawMessage `json:"before_json"`
	AfterJSON   json.RawMessage `json:"after_json"`
	CreatedAt   time.Time       `json:"created_at"`
}

type SpecAuditFilter struct {
	EntityType  string
	EntityID    string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	Limit       int
}

type specRequirementRecord struct {
	rowID string
	Requirement
}

type specLinkRecord struct {
	rowID         string
	requirementPK string
	SpecLink
}

type sqlRequirementQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type sqlRequirementExecer interface {
	sqlRequirementQueryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func WithSpecAuditActorSource(ctx context.Context, source string) context.Context {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, specAuditActorSourceKey{}, trimmed)
}

func (c *Client) CreateRequirement(ctx context.Context, params CreateRequirementParams) (Requirement, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Requirement{}, err
	}

	normalized, err := normalizeCreateRequirementParams(params)
	if err != nil {
		return Requirement{}, c.wrapError("create-requirement", normalized.LocalID, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Requirement{}, c.wrapError("create-requirement", normalized.LocalID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if err := ensureIssueExists(ctx, tx, normalized.IssueID); err != nil {
		return Requirement{}, c.wrapError("create-requirement", normalized.LocalID, err)
	}

	now := time.Now().UTC()
	requirement := Requirement{
		LocalID:      normalized.LocalID,
		ExternalCode: normalized.ExternalCode,
		Title:        normalized.Title,
		Description:  normalized.Description,
		IssueID:      normalized.IssueID,
		Status:       normalized.Status,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO spec_requirements (
			local_id,
			external_code,
			title,
			description,
			issue_id,
			status,
			created_at,
			updated_at,
			deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, requirement.LocalID, nullableTextPtr(requirement.ExternalCode), requirement.Title, nullableString(requirement.Description), nullableTextPtr(requirement.IssueID), string(requirement.Status), formatTimestamp(requirement.CreatedAt), formatTimestamp(requirement.UpdatedAt)); err != nil {
		return Requirement{}, c.wrapError("create-requirement", normalized.LocalID, classifySQLiteConstraint(err))
	}

	if err := c.insertSpecAuditRow(ctx, tx, specAuditEntityRequirement, requirement.LocalID, specAuditOpCreate, nil, requirement); err != nil {
		return Requirement{}, c.wrapError("create-requirement", normalized.LocalID, err)
	}

	if err := tx.Commit(); err != nil {
		return Requirement{}, c.wrapError("create-requirement", normalized.LocalID, err)
	}
	tx = nil

	return requirement, nil
}

func (c *Client) GetRequirement(ctx context.Context, selector string) (Requirement, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Requirement{}, err
	}

	record, err := c.lookupRequirementBySelector(ctx, db, selector, false)
	if err != nil {
		return Requirement{}, c.wrapError("get-requirement", strings.TrimSpace(selector), err)
	}
	return record.Requirement, nil
}

func (c *Client) ListRequirements(ctx context.Context, filter RequirementFilter) ([]Requirement, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT
			id,
			local_id,
			external_code,
			title,
			COALESCE(description, ''),
			issue_id,
			status,
			created_at,
			updated_at,
			deleted_at
		FROM spec_requirements
		WHERE 1 = 1
	`)
	args := make([]any, 0, 8)

	if !filter.IncludeDeleted {
		query.WriteString(` AND deleted_at IS NULL`)
	}
	if trimmed := strings.TrimSpace(filter.IssueID); trimmed != "" {
		query.WriteString(` AND issue_id = ?`)
		args = append(args, trimmed)
	}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, 0, len(filter.Statuses))
		for _, status := range dedupeRequirementStatuses(filter.Statuses) {
			placeholders = append(placeholders, "?")
			args = append(args, string(status))
		}
		query.WriteString(` AND status IN (` + strings.Join(placeholders, ",") + `)`)
	}
	if trimmed := strings.TrimSpace(filter.Query); trimmed != "" {
		like := "%" + trimmed + "%"
		query.WriteString(` AND (local_id LIKE ? OR COALESCE(external_code, '') LIKE ? OR title LIKE ? OR COALESCE(description, '') LIKE ?)`)
		args = append(args, like, like, like, like)
	}

	query.WriteString(` ORDER BY updated_at DESC, local_id ASC`)

	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, c.wrapError("list-requirements", "", err)
	}
	defer rows.Close()

	records := make([]specRequirementRecord, 0, 16)
	for rows.Next() {
		record, scanErr := scanRequirementRecord(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-requirements", "", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-requirements", "", err)
	}

	requirements := recordsToRequirements(records)
	if len(filter.LocalIDs) > 0 {
		requirements = filterRequirementsByLocalID(requirements, normalizeOrderedIDs(filter.LocalIDs))
	}
	return requirements, nil
}

func (c *Client) UpdateRequirement(ctx context.Context, selector string, params UpdateRequirementParams) (Requirement, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Requirement{}, err
	}

	selector = strings.TrimSpace(selector)
	if selector == "" {
		return Requirement{}, c.wrapError("update-requirement", selector, errors.New("requirement selector is required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Requirement{}, c.wrapError("update-requirement", selector, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	before, err := c.lookupRequirementBySelector(ctx, tx, selector, false)
	if err != nil {
		return Requirement{}, c.wrapError("update-requirement", selector, err)
	}

	after, err := applyRequirementUpdate(before.Requirement, params)
	if err != nil {
		return Requirement{}, c.wrapError("update-requirement", before.LocalID, err)
	}
	if err := ensureIssueExists(ctx, tx, after.IssueID); err != nil {
		return Requirement{}, c.wrapError("update-requirement", before.LocalID, err)
	}

	after.UpdatedAt = time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE spec_requirements
		SET
			external_code = ?,
			title = ?,
			description = ?,
			issue_id = ?,
			status = ?,
			updated_at = ?
		WHERE id = ?
	`, nullableTextPtr(after.ExternalCode), after.Title, nullableString(after.Description), nullableTextPtr(after.IssueID), string(after.Status), formatTimestamp(after.UpdatedAt), before.rowID); err != nil {
		return Requirement{}, c.wrapError("update-requirement", before.LocalID, classifySQLiteConstraint(err))
	}

	if err := c.insertSpecAuditRow(ctx, tx, specAuditEntityRequirement, before.LocalID, specAuditOpUpdate, before.Requirement, after); err != nil {
		return Requirement{}, c.wrapError("update-requirement", before.LocalID, err)
	}

	if err := tx.Commit(); err != nil {
		return Requirement{}, c.wrapError("update-requirement", before.LocalID, err)
	}
	tx = nil

	return after, nil
}

func (c *Client) DeleteRequirement(ctx context.Context, selector string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}

	selector = strings.TrimSpace(selector)
	if selector == "" {
		return c.wrapError("delete-requirement", selector, errors.New("requirement selector is required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("delete-requirement", selector, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	requirement, err := c.lookupRequirementBySelector(ctx, tx, selector, false)
	if err != nil {
		return c.wrapError("delete-requirement", selector, err)
	}

	activeLinks, err := c.listLinksForRequirementRow(ctx, tx, requirement.rowID, false)
	if err != nil {
		return c.wrapError("delete-requirement", requirement.LocalID, err)
	}

	now := time.Now().UTC()
	for _, link := range activeLinks {
		after := link.SpecLink
		after.DeletedAt = timePointer(now)
		after.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			UPDATE spec_links
			SET deleted_at = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL
		`, formatTimestamp(now), formatTimestamp(now), link.rowID); err != nil {
			return c.wrapError("delete-requirement", requirement.LocalID, err)
		}
		if err := c.insertSpecAuditRow(ctx, tx, specAuditEntityLink, link.SpecLink.ID, specAuditOpDelete, link.SpecLink, after); err != nil {
			return c.wrapError("delete-requirement", requirement.LocalID, err)
		}
	}

	afterRequirement := requirement.Requirement
	afterRequirement.DeletedAt = timePointer(now)
	afterRequirement.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `
		UPDATE spec_requirements
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, formatTimestamp(now), formatTimestamp(now), requirement.rowID); err != nil {
		return c.wrapError("delete-requirement", requirement.LocalID, err)
	}
	if err := c.insertSpecAuditRow(ctx, tx, specAuditEntityRequirement, requirement.LocalID, specAuditOpDelete, requirement.Requirement, afterRequirement); err != nil {
		return c.wrapError("delete-requirement", requirement.LocalID, err)
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("delete-requirement", requirement.LocalID, err)
	}
	tx = nil
	return nil
}

func (c *Client) AddSpecLink(ctx context.Context, params AddSpecLinkParams) (SpecLink, error) {
	db, err := c.dbHandle()
	if err != nil {
		return SpecLink{}, err
	}

	normalized, err := normalizeAddSpecLinkParams(params)
	if err != nil {
		return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if err := ensureIssueExists(ctx, tx, timeStringPointer(normalized.IssueID)); err != nil {
		return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, err)
	}

	requirement, err := c.lookupRequirementBySelector(ctx, tx, normalized.RequirementID, false)
	if err != nil {
		return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, err)
	}

	existing, err := c.lookupLinkByIssueAndRequirement(ctx, tx, normalized.IssueID, requirement.rowID, true)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, err)
	}

	now := time.Now().UTC()
	link := SpecLink{
		ID:                specLinkID(normalized.IssueID, requirement.LocalID),
		IssueID:           normalized.IssueID,
		RequirementID:     requirement.LocalID,
		Role:              normalized.Role,
		Note:              normalized.Note,
		Implementations:   normalizeStringSlice(normalized.Implementations),
		FulfillmentStatus: normalizeOptionalString(normalized.FulfillmentStatus),
		FulfilledAt:       cloneTimePointer(normalized.FulfilledAt),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	operation := specAuditOpCreate
	var before any
	if err == nil {
		before = existing.SpecLink
		operation = specAuditOpUpdate
		if existing.DeletedAt != nil {
			operation = specAuditOpCreate
			link.CreatedAt = existing.CreatedAt
		} else {
			link.CreatedAt = existing.CreatedAt
		}
		if _, updateErr := tx.ExecContext(ctx, `
			UPDATE spec_links
			SET
				role = ?,
				note = ?,
				implementations_json = ?,
				fulfillment_status = ?,
				fulfilled_at = ?,
				updated_at = ?,
				deleted_at = NULL
			WHERE id = ?
		`, string(link.Role), nullableTextPtr(link.Note), mustMarshalJSONSlice(link.Implementations), nullableTextPtr(link.FulfillmentStatus), nullableTimePtr(link.FulfilledAt), formatTimestamp(link.UpdatedAt), existing.rowID); updateErr != nil {
			return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, classifySQLiteConstraint(updateErr))
		}
	} else {
		if _, insertErr := tx.ExecContext(ctx, `
			INSERT INTO spec_links (
				issue_id,
				requirement_id,
				role,
				note,
				implementations_json,
				fulfillment_status,
				fulfilled_at,
				created_at,
				updated_at,
				deleted_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
		`, link.IssueID, requirement.rowID, string(link.Role), nullableTextPtr(link.Note), mustMarshalJSONSlice(link.Implementations), nullableTextPtr(link.FulfillmentStatus), nullableTimePtr(link.FulfilledAt), formatTimestamp(link.CreatedAt), formatTimestamp(link.UpdatedAt)); insertErr != nil {
			return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, classifySQLiteConstraint(insertErr))
		}
	}

	if err := c.insertSpecAuditRow(ctx, tx, specAuditEntityLink, link.ID, operation, before, link); err != nil {
		return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, err)
	}

	if err := tx.Commit(); err != nil {
		return SpecLink{}, c.wrapError("add-spec-link", normalized.IssueID, err)
	}
	tx = nil

	return link, nil
}

func (c *Client) GetSpecLink(ctx context.Context, issueID, requirementSelector string) (SpecLink, error) {
	db, err := c.dbHandle()
	if err != nil {
		return SpecLink{}, err
	}

	requirement, err := c.lookupRequirementBySelector(ctx, db, requirementSelector, false)
	if err != nil {
		return SpecLink{}, c.wrapError("get-spec-link", strings.TrimSpace(issueID), err)
	}

	record, err := c.lookupLinkByIssueAndRequirement(ctx, db, strings.TrimSpace(issueID), requirement.rowID, false)
	if err != nil {
		return SpecLink{}, c.wrapError("get-spec-link", strings.TrimSpace(issueID), err)
	}
	return record.SpecLink, nil
}

func (c *Client) ListSpecLinks(ctx context.Context, filter SpecLinkFilter) ([]SpecLink, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT
			l.id,
			l.requirement_id,
			l.issue_id,
			r.local_id,
			l.role,
			l.note,
			COALESCE(l.implementations_json, '[]'),
			l.fulfillment_status,
			l.fulfilled_at,
			l.created_at,
			l.updated_at,
			l.deleted_at
		FROM spec_links l
		JOIN spec_requirements r ON r.id = l.requirement_id
		WHERE 1 = 1
	`)
	args := make([]any, 0, 4)

	if !filter.IncludeDeleted {
		query.WriteString(` AND l.deleted_at IS NULL AND r.deleted_at IS NULL`)
	}
	if trimmed := strings.TrimSpace(filter.IssueID); trimmed != "" {
		query.WriteString(` AND l.issue_id = ?`)
		args = append(args, trimmed)
	}
	if trimmed := strings.TrimSpace(filter.RequirementID); trimmed != "" {
		requirement, err := c.lookupRequirementBySelector(ctx, db, trimmed, filter.IncludeDeleted)
		if err != nil {
			return nil, c.wrapError("list-spec-links", "", err)
		}
		query.WriteString(` AND l.requirement_id = ?`)
		args = append(args, requirement.rowID)
	}

	query.WriteString(` ORDER BY l.updated_at DESC, l.id ASC`)

	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, c.wrapError("list-spec-links", "", err)
	}
	defer rows.Close()

	records := make([]specLinkRecord, 0, 16)
	for rows.Next() {
		record, scanErr := scanSpecLinkRecord(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-spec-links", "", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-spec-links", "", err)
	}

	links := recordsToSpecLinks(records)
	if len(filter.LinkIDs) > 0 {
		links = filterSpecLinksByID(links, normalizeOrderedIDs(filter.LinkIDs))
	}
	return links, nil
}

// ListSpecLinksByRequirementLocalID returns links for an exact requirement local_id match.
// Unlike selector-based filters, this does not consider external_code and therefore avoids
// ambiguity when local_id/external_code values overlap across requirements.
func (c *Client) ListSpecLinksByRequirementLocalID(ctx context.Context, localID string) ([]SpecLink, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	localID = strings.TrimSpace(localID)
	if localID == "" {
		return nil, c.wrapError("list-spec-links-by-local-id", "", errors.New("requirement local id is required"))
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			l.id,
			l.requirement_id,
			l.issue_id,
			r.local_id,
			l.role,
			l.note,
			COALESCE(l.implementations_json, '[]'),
			l.fulfillment_status,
			l.fulfilled_at,
			l.created_at,
			l.updated_at,
			l.deleted_at
		FROM spec_links l
		JOIN spec_requirements r ON r.id = l.requirement_id
		WHERE r.local_id = ? AND l.deleted_at IS NULL AND r.deleted_at IS NULL
		ORDER BY l.updated_at DESC, l.id ASC
	`, localID)
	if err != nil {
		return nil, c.wrapError("list-spec-links-by-local-id", localID, err)
	}
	defer rows.Close()

	records := make([]specLinkRecord, 0, 8)
	for rows.Next() {
		record, scanErr := scanSpecLinkRecord(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-spec-links-by-local-id", localID, scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-spec-links-by-local-id", localID, err)
	}
	return recordsToSpecLinks(records), nil
}

func (c *Client) RemoveSpecLink(ctx context.Context, issueID, requirementSelector string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}

	issueID = strings.TrimSpace(issueID)
	requirementSelector = strings.TrimSpace(requirementSelector)
	if issueID == "" || requirementSelector == "" {
		return c.wrapError("remove-spec-link", issueID, errors.New("issue id and requirement selector are required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("remove-spec-link", issueID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	requirement, err := c.lookupRequirementBySelector(ctx, tx, requirementSelector, false)
	if err != nil {
		return c.wrapError("remove-spec-link", issueID, err)
	}
	link, err := c.lookupLinkByIssueAndRequirement(ctx, tx, issueID, requirement.rowID, false)
	if err != nil {
		return c.wrapError("remove-spec-link", issueID, err)
	}

	now := time.Now().UTC()
	after := link.SpecLink
	after.DeletedAt = timePointer(now)
	after.UpdatedAt = now

	if _, err := tx.ExecContext(ctx, `
		UPDATE spec_links
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, formatTimestamp(now), formatTimestamp(now), link.rowID); err != nil {
		return c.wrapError("remove-spec-link", issueID, err)
	}

	if err := c.insertSpecAuditRow(ctx, tx, specAuditEntityLink, link.SpecLink.ID, specAuditOpDelete, link.SpecLink, after); err != nil {
		return c.wrapError("remove-spec-link", issueID, err)
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("remove-spec-link", issueID, err)
	}
	tx = nil
	return nil
}

func (c *Client) ListSpecAuditEntries(ctx context.Context, filter SpecAuditFilter) ([]SpecAuditEntry, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT
			id,
			entity_type,
			entity_id,
			operation,
			actor_source,
			before_json,
			after_json,
			created_at
		FROM spec_audit_log
		WHERE 1 = 1
	`)
	args := make([]any, 0, 4)

	if trimmed := strings.TrimSpace(filter.EntityType); trimmed != "" {
		query.WriteString(` AND entity_type = ?`)
		args = append(args, trimmed)
	}
	if trimmed := strings.TrimSpace(filter.EntityID); trimmed != "" {
		query.WriteString(` AND entity_id = ?`)
		args = append(args, trimmed)
	}
	if filter.CreatedFrom != nil {
		query.WriteString(` AND created_at >= ?`)
		args = append(args, formatTimestamp(*filter.CreatedFrom))
	}
	if filter.CreatedTo != nil {
		query.WriteString(` AND created_at <= ?`)
		args = append(args, formatTimestamp(*filter.CreatedTo))
	}

	query.WriteString(` ORDER BY created_at ASC, id ASC`)
	if filter.Limit > 0 {
		query.WriteString(` LIMIT ?`)
		args = append(args, filter.Limit)
	}

	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, c.wrapError("list-spec-audit", "", err)
	}
	defer rows.Close()

	entries := make([]SpecAuditEntry, 0, 16)
	for rows.Next() {
		var entry SpecAuditEntry
		var beforeRaw string
		var afterRaw string
		var createdRaw string
		if err := rows.Scan(&entry.ID, &entry.EntityType, &entry.EntityID, &entry.Operation, &entry.ActorSource, &beforeRaw, &afterRaw, &createdRaw); err != nil {
			return nil, c.wrapError("list-spec-audit", "", err)
		}
		entry.BeforeJSON = json.RawMessage(beforeRaw)
		entry.AfterJSON = json.RawMessage(afterRaw)
		entry.CreatedAt = parseTimestamp(createdRaw)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-spec-audit", "", err)
	}
	return entries, nil
}

func (c *Client) insertSpecAuditRow(ctx context.Context, execer sqlRequirementExecer, entityType, entityID, operation string, before, after any) error {
	beforeJSON, err := marshalAuditSnapshot(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalAuditSnapshot(after)
	if err != nil {
		return err
	}

	_, err = execer.ExecContext(ctx, `
		INSERT INTO spec_audit_log (
			entity_type,
			entity_id,
			operation,
			actor_source,
			before_json,
			after_json,
			created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, entityType, entityID, operation, specAuditActorSource(ctx), string(beforeJSON), string(afterJSON), formatTimestamp(time.Now().UTC()))
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) lookupRequirementBySelector(ctx context.Context, queryer sqlRequirementQueryer, selector string, includeDeleted bool) (specRequirementRecord, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return specRequirementRecord{}, errors.New("requirement selector is required")
	}

	query := `
		SELECT
			id,
			local_id,
			external_code,
			title,
			COALESCE(description, ''),
			issue_id,
			status,
			created_at,
			updated_at,
			deleted_at
		FROM spec_requirements
		WHERE (local_id = ? OR external_code = ?)
	`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY CASE WHEN local_id = ? THEN 0 ELSE 1 END, updated_at DESC, id DESC LIMIT 2`

	rows, err := queryer.QueryContext(ctx, query, selector, selector, selector)
	if err != nil {
		return specRequirementRecord{}, err
	}
	defer rows.Close()

	matches := make([]specRequirementRecord, 0, 2)
	for rows.Next() {
		record, scanErr := scanRequirementRecord(rows)
		if scanErr != nil {
			return specRequirementRecord{}, scanErr
		}
		matches = append(matches, record)
	}
	if err := rows.Err(); err != nil {
		return specRequirementRecord{}, err
	}
	if len(matches) == 0 {
		return specRequirementRecord{}, domain.ErrNotFound
	}
	if len(matches) > 1 {
		return specRequirementRecord{}, fmt.Errorf("%w: requirement selector %q is ambiguous", domain.ErrConflict, selector)
	}
	return matches[0], nil
}

func (c *Client) lookupLinkByIssueAndRequirement(ctx context.Context, queryer sqlRequirementQueryer, issueID string, requirementPK string, includeDeleted bool) (specLinkRecord, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return specLinkRecord{}, errors.New("issue id is required")
	}

	query := `
		SELECT
			l.id,
			l.requirement_id,
			l.issue_id,
			r.local_id,
			l.role,
			l.note,
			COALESCE(l.implementations_json, '[]'),
			l.fulfillment_status,
			l.fulfilled_at,
			l.created_at,
			l.updated_at,
			l.deleted_at
		FROM spec_links l
		JOIN spec_requirements r ON r.id = l.requirement_id
		WHERE l.issue_id = ? AND l.requirement_id = ?
	`
	if !includeDeleted {
		query += ` AND l.deleted_at IS NULL AND r.deleted_at IS NULL`
	}
	query += ` ORDER BY CASE WHEN l.deleted_at IS NULL THEN 0 ELSE 1 END, l.updated_at DESC, l.id DESC LIMIT 1`

	row := queryer.QueryRowContext(ctx, query, issueID, requirementPK)
	record, err := scanSpecLinkRecord(row)
	if err != nil {
		return specLinkRecord{}, err
	}
	return record, nil
}

func (c *Client) listLinksForRequirementRow(ctx context.Context, queryer sqlRequirementQueryer, requirementPK string, includeDeleted bool) ([]specLinkRecord, error) {
	query := `
		SELECT
			l.id,
			l.requirement_id,
			l.issue_id,
			r.local_id,
			l.role,
			l.note,
			COALESCE(l.implementations_json, '[]'),
			l.fulfillment_status,
			l.fulfilled_at,
			l.created_at,
			l.updated_at,
			l.deleted_at
		FROM spec_links l
		JOIN spec_requirements r ON r.id = l.requirement_id
		WHERE l.requirement_id = ?
	`
	if !includeDeleted {
		query += ` AND l.deleted_at IS NULL`
	}
	query += ` ORDER BY l.updated_at DESC, l.id DESC`

	rows, err := queryer.QueryContext(ctx, query, requirementPK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]specLinkRecord, 0, 8)
	for rows.Next() {
		record, scanErr := scanSpecLinkRecord(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func scanRequirementRecord(scanner interface {
	Scan(dest ...any) error
}) (specRequirementRecord, error) {
	var record specRequirementRecord
	var rowID any
	var externalCode sql.NullString
	var issueID sql.NullString
	var createdRaw string
	var updatedRaw string
	var deletedRaw sql.NullString
	if err := scanner.Scan(&rowID, &record.LocalID, &externalCode, &record.Title, &record.Description, &issueID, &record.Status, &createdRaw, &updatedRaw, &deletedRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return specRequirementRecord{}, domain.ErrNotFound
		}
		return specRequirementRecord{}, err
	}
	record.rowID = normalizeDBID(rowID)
	record.ExternalCode = nullStringPointer(externalCode)
	record.IssueID = nullStringPointer(issueID)
	record.CreatedAt = parseTimestamp(createdRaw)
	record.UpdatedAt = parseTimestamp(updatedRaw)
	record.DeletedAt = parseNullableTimestamp(deletedRaw)
	return record, nil
}

func scanSpecLinkRecord(scanner interface {
	Scan(dest ...any) error
}) (specLinkRecord, error) {
	var record specLinkRecord
	var rowID any
	var requirementPK any
	var roleRaw string
	var note sql.NullString
	var implementationsRaw string
	var fulfillmentStatus sql.NullString
	var fulfilledAt sql.NullString
	var createdRaw string
	var updatedRaw string
	var deletedRaw sql.NullString

	if err := scanner.Scan(&rowID, &requirementPK, &record.IssueID, &record.RequirementID, &roleRaw, &note, &implementationsRaw, &fulfillmentStatus, &fulfilledAt, &createdRaw, &updatedRaw, &deletedRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return specLinkRecord{}, domain.ErrNotFound
		}
		return specLinkRecord{}, err
	}
	record.rowID = normalizeDBID(rowID)
	record.requirementPK = normalizeDBID(requirementPK)

	record.ID = specLinkID(record.IssueID, record.RequirementID)
	record.Role = LinkRole(roleRaw)
	record.Note = nullStringPointer(note)
	record.Implementations = decodeImplementationsJSON(implementationsRaw)
	record.FulfillmentStatus = nullStringPointer(fulfillmentStatus)
	record.FulfilledAt = parseNullableTimestamp(fulfilledAt)
	record.CreatedAt = parseTimestamp(createdRaw)
	record.UpdatedAt = parseTimestamp(updatedRaw)
	record.DeletedAt = parseNullableTimestamp(deletedRaw)
	return record, nil
}

func ensureIssueExists(ctx context.Context, queryer sqlRequirementQueryer, issueID *string) error {
	if issueID == nil || strings.TrimSpace(*issueID) == "" {
		return nil
	}
	var exists bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM issues
			WHERE id = ? AND deleted_at IS NULL
		)
	`, strings.TrimSpace(*issueID)).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

func normalizeCreateRequirementParams(params CreateRequirementParams) (CreateRequirementParams, error) {
	params.LocalID = strings.TrimSpace(params.LocalID)
	params.Title = strings.TrimSpace(params.Title)
	params.Description = strings.TrimSpace(params.Description)
	params.ExternalCode = normalizeOptionalString(params.ExternalCode)
	params.IssueID = normalizeOptionalString(params.IssueID)
	if params.LocalID == "" {
		return params, errors.New("requirement id is required")
	}
	if params.Title == "" {
		return params, errors.New("requirement title is required")
	}
	if params.Status == "" {
		params.Status = RequirementStatusOpen
	}
	if err := validateRequirementStatus(params.Status); err != nil {
		return params, err
	}
	return params, nil
}

func applyRequirementUpdate(requirement Requirement, params UpdateRequirementParams) (Requirement, error) {
	after := requirement
	if params.ExternalCode != nil {
		after.ExternalCode = normalizeOptionalString(params.ExternalCode)
	}
	if params.Title != nil {
		title := strings.TrimSpace(*params.Title)
		if title == "" {
			return Requirement{}, errors.New("requirement title is required")
		}
		after.Title = title
	}
	if params.Description != nil {
		after.Description = strings.TrimSpace(*params.Description)
	}
	if params.IssueID != nil {
		after.IssueID = normalizeOptionalString(params.IssueID)
	}
	if params.Status != nil {
		if err := validateRequirementStatus(*params.Status); err != nil {
			return Requirement{}, err
		}
		after.Status = *params.Status
	}
	return after, nil
}

func normalizeAddSpecLinkParams(params AddSpecLinkParams) (AddSpecLinkParams, error) {
	params.IssueID = strings.TrimSpace(params.IssueID)
	params.RequirementID = strings.TrimSpace(params.RequirementID)
	params.Note = normalizeOptionalString(params.Note)
	params.FulfillmentStatus = normalizeOptionalString(params.FulfillmentStatus)
	if params.IssueID == "" {
		return params, errors.New("issue id is required")
	}
	if params.RequirementID == "" {
		return params, errors.New("requirement selector is required")
	}
	if params.Role == "" {
		params.Role = LinkRoleImplements
	}
	if err := validateLinkRole(params.Role); err != nil {
		return params, err
	}
	params.Implementations = normalizeStringSlice(params.Implementations)
	if params.FulfilledAt != nil {
		value := params.FulfilledAt.UTC()
		params.FulfilledAt = &value
	}
	return params, nil
}

func validateRequirementStatus(status RequirementStatus) error {
	switch status {
	case RequirementStatusOpen, RequirementStatusAccepted, RequirementStatusSuperseded:
		return nil
	default:
		return fmt.Errorf("unsupported requirement status %q", status)
	}
}

func validateLinkRole(role LinkRole) error {
	switch role {
	case LinkRoleImplements, LinkRoleVerifies, LinkRoleRelates:
		return nil
	default:
		return fmt.Errorf("unsupported spec link role %q", role)
	}
}

func classifySQLiteConstraint(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "constraint failed") || strings.Contains(message, "unique constraint") {
		return fmt.Errorf("%w: %v", domain.ErrConflict, err)
	}
	return err
}

func marshalAuditSnapshot(value any) ([]byte, error) {
	if value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(value)
}

func specAuditActorSource(ctx context.Context) string {
	if source, ok := ctx.Value(specAuditActorSourceKey{}).(string); ok && strings.TrimSpace(source) != "" {
		return strings.TrimSpace(source)
	}
	return defaultSpecAuditActorSource
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseNullableTimestamp(raw sql.NullString) *time.Time {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	parsed := parseTimestamp(raw.String)
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}

func nullableTextPtr(value *string) any {
	if value == nil {
		return nil
	}
	return nullableString(*value)
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeOrderedIDs(values []string) []string {
	return normalizeStringSlice(values)
}

func dedupeRequirementStatuses(values []RequirementStatus) []RequirementStatus {
	seen := make(map[RequirementStatus]struct{}, len(values))
	deduped := make([]RequirementStatus, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		deduped = append(deduped, value)
	}
	return deduped
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	trimmed := strings.TrimSpace(value.String)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func mustMarshalJSONSlice(values []string) any {
	payload, err := marshalOptionalStringSlice(values)
	if err != nil {
		return "[]"
	}
	if payload == nil {
		return "[]"
	}
	return payload
}

func nullableTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTimestamp(value.UTC())
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	return timePointer(*value)
}

func timeStringPointer(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeDBID(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func specLinkID(issueID, requirementID string) string {
	return issueID + ":" + requirementID
}

func recordsToRequirements(records []specRequirementRecord) []Requirement {
	requirements := make([]Requirement, 0, len(records))
	for _, record := range records {
		requirements = append(requirements, record.Requirement)
	}
	return requirements
}

func filterRequirementsByLocalID(requirements []Requirement, ids []string) []Requirement {
	if len(ids) == 0 {
		return requirements
	}
	byID := make(map[string]Requirement, len(requirements))
	for _, requirement := range requirements {
		byID[requirement.LocalID] = requirement
	}
	ordered := make([]Requirement, 0, len(ids))
	for _, id := range ids {
		if requirement, ok := byID[id]; ok {
			ordered = append(ordered, requirement)
		}
	}
	return ordered
}

func recordsToSpecLinks(records []specLinkRecord) []SpecLink {
	links := make([]SpecLink, 0, len(records))
	for _, record := range records {
		links = append(links, record.SpecLink)
	}
	return links
}

func filterSpecLinksByID(links []SpecLink, ids []string) []SpecLink {
	if len(ids) == 0 {
		return links
	}
	byID := make(map[string]SpecLink, len(links))
	for _, link := range links {
		byID[link.ID] = link
	}
	ordered := make([]SpecLink, 0, len(ids))
	for _, id := range ids {
		if link, ok := byID[id]; ok {
			ordered = append(ordered, link)
		}
	}
	return ordered
}
