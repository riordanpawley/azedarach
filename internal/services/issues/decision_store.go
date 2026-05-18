package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	decisionEntityKind     = "decision"
	decisionLinkEntityKind = "decision_link"

	decisionOpCreate = "create"
	decisionOpUpdate = "update"
	decisionOpDelete = "delete"
)

func (c *Client) insertDecisionAuditRow(ctx context.Context, execer sqlRequirementExecer, entityType, entityID, operation string, before, after any) error {
	beforeJSON, err := marshalAuditSnapshot(before)
	if err != nil {
		return err
	}
	afterJSON, err := marshalAuditSnapshot(after)
	if err != nil {
		return err
	}
	_, err = execer.ExecContext(ctx, `
		INSERT INTO decision_audit_log (
			entity_type, entity_id, operation, actor_source,
			before_json, after_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, entityType, entityID, operation, specAuditActorSource(ctx), string(beforeJSON), string(afterJSON), formatTimestamp(time.Now().UTC()))
	return err
}

type DecisionStatus string

const (
	DecisionStatusProposed   DecisionStatus = "proposed"
	DecisionStatusAccepted   DecisionStatus = "accepted"
	DecisionStatusRejected   DecisionStatus = "rejected"
	DecisionStatusDeprecated DecisionStatus = "deprecated"
	DecisionStatusSuperseded DecisionStatus = "superseded"
)

func ValidDecisionStatus(s DecisionStatus) bool {
	switch s {
	case DecisionStatusProposed,
		DecisionStatusAccepted,
		DecisionStatusRejected,
		DecisionStatusDeprecated,
		DecisionStatusSuperseded:
		return true
	}
	return false
}

type DecisionTargetKind string

const (
	DecisionTargetIssue       DecisionTargetKind = "issue"
	DecisionTargetRequirement DecisionTargetKind = "requirement"
)

func ValidDecisionTargetKind(k DecisionTargetKind) bool {
	switch k {
	case DecisionTargetIssue, DecisionTargetRequirement:
		return true
	}
	return false
}

type DecisionRelation string

const (
	DecisionRelationRelates       DecisionRelation = "relates"
	DecisionRelationImplements    DecisionRelation = "implements"
	DecisionRelationSupersedes    DecisionRelation = "supersedes"
	DecisionRelationSupersededBy  DecisionRelation = "superseded-by"
)

func ValidDecisionRelation(r DecisionRelation) bool {
	switch r {
	case DecisionRelationRelates,
		DecisionRelationImplements,
		DecisionRelationSupersedes,
		DecisionRelationSupersededBy:
		return true
	}
	return false
}

type Decision struct {
	LocalID      string         `json:"id"`
	Title        string         `json:"title"`
	Context      string         `json:"context"`
	Decision     string         `json:"decision"`
	Consequences string         `json:"consequences"`
	Status       DecisionStatus `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    *time.Time     `json:"deleted_at,omitempty"`
}

type DecisionLink struct {
	ID         string             `json:"id"`
	DecisionID string             `json:"decision_id"`
	TargetKind DecisionTargetKind `json:"target_kind"`
	TargetID   string             `json:"target_id"`
	Relation   DecisionRelation   `json:"relation"`
	Note       *string            `json:"note,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
	DeletedAt  *time.Time         `json:"deleted_at,omitempty"`
}

type CreateDecisionParams struct {
	LocalID      string
	Title        string
	Context      string
	Decision     string
	Consequences string
	Status       DecisionStatus
}

type UpdateDecisionParams struct {
	Title        *string
	Context      *string
	Decision     *string
	Consequences *string
	Status       *DecisionStatus
}

type DecisionFilter struct {
	Statuses       []DecisionStatus
	LocalIDs       []string
	IssueID        string
	RequirementID  string
	Query          string
	IncludeDeleted bool
}

type AddDecisionLinkParams struct {
	DecisionID string
	TargetKind DecisionTargetKind
	TargetID   string
	Relation   DecisionRelation
	Note       *string
}

type DecisionLinkFilter struct {
	DecisionID     string
	TargetKind     DecisionTargetKind
	TargetID       string
	IncludeDeleted bool
}

type decisionRecord struct {
	rowID string
	Decision
}

type decisionLinkRecord struct {
	rowID      string
	decisionPK string
	DecisionLink
}

func (c *Client) CreateDecision(ctx context.Context, params CreateDecisionParams) (Decision, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Decision{}, err
	}

	normalized, err := normalizeCreateDecisionParams(params)
	if err != nil {
		return Decision{}, c.wrapError("create-decision", normalized.LocalID, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, c.wrapError("create-decision", normalized.LocalID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	decision := Decision{
		LocalID:      normalized.LocalID,
		Title:        normalized.Title,
		Context:      normalized.Context,
		Decision:     normalized.Decision,
		Consequences: normalized.Consequences,
		Status:       normalized.Status,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO decisions (
			local_id, title, context, decision, consequences,
			status, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`,
		decision.LocalID,
		decision.Title,
		nullableString(decision.Context),
		nullableString(decision.Decision),
		nullableString(decision.Consequences),
		string(decision.Status),
		formatTimestamp(decision.CreatedAt),
		formatTimestamp(decision.UpdatedAt),
	); err != nil {
		return Decision{}, c.wrapError("create-decision", decision.LocalID, classifySQLiteConstraint(err))
	}

	if err := c.insertDecisionAuditRow(ctx, tx, decisionEntityKind, decision.LocalID, decisionOpCreate, nil, decision); err != nil {
		return Decision{}, c.wrapError("create-decision", decision.LocalID, err)
	}

	if err := tx.Commit(); err != nil {
		return Decision{}, c.wrapError("create-decision", decision.LocalID, err)
	}
	tx = nil

	return decision, nil
}

func (c *Client) GetDecision(ctx context.Context, selector string) (Decision, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Decision{}, err
	}
	record, err := c.lookupDecisionByLocalID(ctx, db, selector, false)
	if err != nil {
		return Decision{}, c.wrapError("get-decision", strings.TrimSpace(selector), err)
	}
	return record.Decision, nil
}

func (c *Client) ListDecisions(ctx context.Context, filter DecisionFilter) ([]Decision, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT DISTINCT
			d.id, d.local_id, d.title,
			COALESCE(d.context, ''),
			COALESCE(d.decision, ''),
			COALESCE(d.consequences, ''),
			d.status, d.created_at, d.updated_at, d.deleted_at
		FROM decisions d
	`)

	args := make([]any, 0, 8)
	joinedLinks := false
	joinLinks := func() {
		if joinedLinks {
			return
		}
		query.WriteString(` JOIN decision_links l ON l.decision_id = d.id AND l.deleted_at IS NULL`)
		joinedLinks = true
	}

	if trimmed := strings.TrimSpace(filter.IssueID); trimmed != "" {
		joinLinks()
	}
	if trimmed := strings.TrimSpace(filter.RequirementID); trimmed != "" {
		joinLinks()
	}

	query.WriteString(` WHERE 1 = 1`)
	if !filter.IncludeDeleted {
		query.WriteString(` AND d.deleted_at IS NULL`)
	}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, 0, len(filter.Statuses))
		for _, status := range dedupeDecisionStatuses(filter.Statuses) {
			placeholders = append(placeholders, "?")
			args = append(args, string(status))
		}
		query.WriteString(` AND d.status IN (` + strings.Join(placeholders, ",") + `)`)
	}
	if trimmed := strings.TrimSpace(filter.IssueID); trimmed != "" {
		query.WriteString(` AND l.target_kind = ? AND l.target_id = ?`)
		args = append(args, string(DecisionTargetIssue), trimmed)
	}
	if trimmed := strings.TrimSpace(filter.RequirementID); trimmed != "" {
		query.WriteString(` AND l.target_kind = ? AND l.target_id = ?`)
		args = append(args, string(DecisionTargetRequirement), trimmed)
	}
	if trimmed := strings.TrimSpace(filter.Query); trimmed != "" {
		like := "%" + trimmed + "%"
		query.WriteString(` AND (d.local_id LIKE ? OR d.title LIKE ? OR COALESCE(d.context, '') LIKE ? OR COALESCE(d.decision, '') LIKE ?)`)
		args = append(args, like, like, like, like)
	}

	query.WriteString(` ORDER BY d.updated_at DESC, d.local_id ASC`)

	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, c.wrapError("list-decisions", "", err)
	}
	defer rows.Close()

	records := make([]decisionRecord, 0, 16)
	for rows.Next() {
		record, scanErr := scanDecisionRecord(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-decisions", "", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-decisions", "", err)
	}

	out := make([]Decision, 0, len(records))
	for _, record := range records {
		out = append(out, record.Decision)
	}
	if len(filter.LocalIDs) > 0 {
		out = filterDecisionsByLocalID(out, normalizeOrderedIDs(filter.LocalIDs))
	}
	return out, nil
}

func (c *Client) UpdateDecision(ctx context.Context, selector string, params UpdateDecisionParams) (Decision, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Decision{}, err
	}

	selector = strings.TrimSpace(selector)
	if selector == "" {
		return Decision{}, c.wrapError("update-decision", selector, errors.New("decision id is required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, c.wrapError("update-decision", selector, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	before, err := c.lookupDecisionByLocalID(ctx, tx, selector, false)
	if err != nil {
		return Decision{}, c.wrapError("update-decision", selector, err)
	}

	after, err := applyDecisionUpdate(before.Decision, params)
	if err != nil {
		return Decision{}, c.wrapError("update-decision", before.LocalID, err)
	}

	after.UpdatedAt = time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE decisions
		SET title = ?, context = ?, decision = ?, consequences = ?, status = ?, updated_at = ?
		WHERE id = ?
	`,
		after.Title,
		nullableString(after.Context),
		nullableString(after.Decision),
		nullableString(after.Consequences),
		string(after.Status),
		formatTimestamp(after.UpdatedAt),
		before.rowID,
	); err != nil {
		return Decision{}, c.wrapError("update-decision", before.LocalID, classifySQLiteConstraint(err))
	}

	if err := c.insertDecisionAuditRow(ctx, tx, decisionEntityKind, before.LocalID, decisionOpUpdate, before.Decision, after); err != nil {
		return Decision{}, c.wrapError("update-decision", before.LocalID, err)
	}

	if err := tx.Commit(); err != nil {
		return Decision{}, c.wrapError("update-decision", before.LocalID, err)
	}
	tx = nil
	return after, nil
}

func (c *Client) DeleteDecision(ctx context.Context, selector string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return c.wrapError("delete-decision", selector, errors.New("decision id is required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("delete-decision", selector, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	decision, err := c.lookupDecisionByLocalID(ctx, tx, selector, false)
	if err != nil {
		return c.wrapError("delete-decision", selector, err)
	}

	links, err := c.listDecisionLinksForDecisionRow(ctx, tx, decision.rowID, false)
	if err != nil {
		return c.wrapError("delete-decision", decision.LocalID, err)
	}

	now := time.Now().UTC()
	for _, link := range links {
		after := link.DecisionLink
		after.DeletedAt = timePointer(now)
		after.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			UPDATE decision_links
			SET deleted_at = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL
		`, formatTimestamp(now), formatTimestamp(now), link.rowID); err != nil {
			return c.wrapError("delete-decision", decision.LocalID, err)
		}
		if err := c.insertDecisionAuditRow(ctx, tx, decisionLinkEntityKind, link.DecisionLink.ID, decisionOpDelete, link.DecisionLink, after); err != nil {
			return c.wrapError("delete-decision", decision.LocalID, err)
		}
	}

	afterDecision := decision.Decision
	afterDecision.DeletedAt = timePointer(now)
	afterDecision.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `
		UPDATE decisions
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, formatTimestamp(now), formatTimestamp(now), decision.rowID); err != nil {
		return c.wrapError("delete-decision", decision.LocalID, err)
	}
	if err := c.insertDecisionAuditRow(ctx, tx, decisionEntityKind, decision.LocalID, decisionOpDelete, decision.Decision, afterDecision); err != nil {
		return c.wrapError("delete-decision", decision.LocalID, err)
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("delete-decision", decision.LocalID, err)
	}
	tx = nil
	return nil
}

func (c *Client) AddDecisionLink(ctx context.Context, params AddDecisionLinkParams) (DecisionLink, error) {
	db, err := c.dbHandle()
	if err != nil {
		return DecisionLink{}, err
	}

	normalized, err := normalizeAddDecisionLinkParams(params)
	if err != nil {
		return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	decision, err := c.lookupDecisionByLocalID(ctx, tx, normalized.DecisionID, false)
	if err != nil {
		return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, err)
	}
	if err := ensureDecisionLinkTargetExists(ctx, tx, normalized.TargetKind, normalized.TargetID); err != nil {
		return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, err)
	}

	existing, err := c.lookupDecisionLink(ctx, tx, decision.rowID, normalized.TargetKind, normalized.TargetID, true)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, err)
	}

	now := time.Now().UTC()
	link := DecisionLink{
		ID:         decisionLinkID(decision.LocalID, normalized.TargetKind, normalized.TargetID),
		DecisionID: decision.LocalID,
		TargetKind: normalized.TargetKind,
		TargetID:   normalized.TargetID,
		Relation:   normalized.Relation,
		Note:       normalized.Note,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	operation := decisionOpCreate
	var before any
	if err == nil {
		before = existing.DecisionLink
		if existing.DeletedAt == nil {
			operation = decisionOpUpdate
		}
		link.CreatedAt = existing.CreatedAt
		if _, updateErr := tx.ExecContext(ctx, `
			UPDATE decision_links
			SET relation = ?, note = ?, updated_at = ?, deleted_at = NULL
			WHERE id = ?
		`, string(link.Relation), nullableTextPtr(link.Note), formatTimestamp(link.UpdatedAt), existing.rowID); updateErr != nil {
			return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, classifySQLiteConstraint(updateErr))
		}
	} else {
		if _, insertErr := tx.ExecContext(ctx, `
			INSERT INTO decision_links (
				decision_id, target_kind, target_id, relation,
				note, created_at, updated_at, deleted_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
		`,
			decision.rowID,
			string(link.TargetKind),
			link.TargetID,
			string(link.Relation),
			nullableTextPtr(link.Note),
			formatTimestamp(link.CreatedAt),
			formatTimestamp(link.UpdatedAt),
		); insertErr != nil {
			return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, classifySQLiteConstraint(insertErr))
		}
	}

	if err := c.insertDecisionAuditRow(ctx, tx, decisionLinkEntityKind, link.ID, operation, before, link); err != nil {
		return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, err)
	}

	if err := tx.Commit(); err != nil {
		return DecisionLink{}, c.wrapError("add-decision-link", normalized.DecisionID, err)
	}
	tx = nil
	return link, nil
}

func (c *Client) RemoveDecisionLink(ctx context.Context, decisionSelector string, targetKind DecisionTargetKind, targetID string) error {
	db, err := c.dbHandle()
	if err != nil {
		return err
	}
	decisionSelector = strings.TrimSpace(decisionSelector)
	targetID = strings.TrimSpace(targetID)
	if decisionSelector == "" || targetID == "" {
		return c.wrapError("remove-decision-link", decisionSelector, errors.New("decision id and target id are required"))
	}
	if !ValidDecisionTargetKind(targetKind) {
		return c.wrapError("remove-decision-link", decisionSelector, fmt.Errorf("invalid target kind %q", targetKind))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return c.wrapError("remove-decision-link", decisionSelector, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	decision, err := c.lookupDecisionByLocalID(ctx, tx, decisionSelector, false)
	if err != nil {
		return c.wrapError("remove-decision-link", decisionSelector, err)
	}
	link, err := c.lookupDecisionLink(ctx, tx, decision.rowID, targetKind, targetID, false)
	if err != nil {
		return c.wrapError("remove-decision-link", decisionSelector, err)
	}

	now := time.Now().UTC()
	after := link.DecisionLink
	after.DeletedAt = timePointer(now)
	after.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `
		UPDATE decision_links
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, formatTimestamp(now), formatTimestamp(now), link.rowID); err != nil {
		return c.wrapError("remove-decision-link", decisionSelector, err)
	}
	if err := c.insertDecisionAuditRow(ctx, tx, decisionLinkEntityKind, link.DecisionLink.ID, decisionOpDelete, link.DecisionLink, after); err != nil {
		return c.wrapError("remove-decision-link", decisionSelector, err)
	}

	if err := tx.Commit(); err != nil {
		return c.wrapError("remove-decision-link", decisionSelector, err)
	}
	tx = nil
	return nil
}

func (c *Client) ListDecisionLinks(ctx context.Context, filter DecisionLinkFilter) ([]DecisionLink, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT
			l.id, l.decision_id, d.local_id,
			l.target_kind, l.target_id, l.relation, l.note,
			l.created_at, l.updated_at, l.deleted_at
		FROM decision_links l
		JOIN decisions d ON d.id = l.decision_id
		WHERE 1 = 1
	`)
	args := make([]any, 0, 4)
	if !filter.IncludeDeleted {
		query.WriteString(` AND l.deleted_at IS NULL AND d.deleted_at IS NULL`)
	}
	if trimmed := strings.TrimSpace(filter.DecisionID); trimmed != "" {
		decision, err := c.lookupDecisionByLocalID(ctx, db, trimmed, filter.IncludeDeleted)
		if err != nil {
			return nil, c.wrapError("list-decision-links", "", err)
		}
		query.WriteString(` AND l.decision_id = ?`)
		args = append(args, decision.rowID)
	}
	if filter.TargetKind != "" {
		query.WriteString(` AND l.target_kind = ?`)
		args = append(args, string(filter.TargetKind))
	}
	if trimmed := strings.TrimSpace(filter.TargetID); trimmed != "" {
		query.WriteString(` AND l.target_id = ?`)
		args = append(args, trimmed)
	}
	query.WriteString(` ORDER BY l.updated_at DESC, l.id ASC`)

	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, c.wrapError("list-decision-links", "", err)
	}
	defer rows.Close()

	out := make([]DecisionLink, 0, 16)
	for rows.Next() {
		record, scanErr := scanDecisionLinkRecord(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-decision-links", "", scanErr)
		}
		out = append(out, record.DecisionLink)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-decision-links", "", err)
	}
	return out, nil
}

func (c *Client) lookupDecisionByLocalID(ctx context.Context, queryer sqlRequirementQueryer, selector string, includeDeleted bool) (decisionRecord, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return decisionRecord{}, errors.New("decision id is required")
	}
	query := `
		SELECT
			id, local_id, title,
			COALESCE(context, ''),
			COALESCE(decision, ''),
			COALESCE(consequences, ''),
			status, created_at, updated_at, deleted_at
		FROM decisions
		WHERE local_id = ?
	`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	query += ` ORDER BY CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END, updated_at DESC, id DESC LIMIT 1`

	row := queryer.QueryRowContext(ctx, query, selector)
	record, err := scanDecisionRecord(row)
	if err != nil {
		return decisionRecord{}, err
	}
	return record, nil
}

func (c *Client) lookupDecisionLink(ctx context.Context, queryer sqlRequirementQueryer, decisionPK string, targetKind DecisionTargetKind, targetID string, includeDeleted bool) (decisionLinkRecord, error) {
	query := `
		SELECT
			l.id, l.decision_id, d.local_id,
			l.target_kind, l.target_id, l.relation, l.note,
			l.created_at, l.updated_at, l.deleted_at
		FROM decision_links l
		JOIN decisions d ON d.id = l.decision_id
		WHERE l.decision_id = ? AND l.target_kind = ? AND l.target_id = ?
	`
	if !includeDeleted {
		query += ` AND l.deleted_at IS NULL AND d.deleted_at IS NULL`
	}
	query += ` ORDER BY CASE WHEN l.deleted_at IS NULL THEN 0 ELSE 1 END, l.updated_at DESC, l.id DESC LIMIT 1`

	row := queryer.QueryRowContext(ctx, query, decisionPK, string(targetKind), strings.TrimSpace(targetID))
	record, err := scanDecisionLinkRecord(row)
	if err != nil {
		return decisionLinkRecord{}, err
	}
	return record, nil
}

func (c *Client) listDecisionLinksForDecisionRow(ctx context.Context, queryer sqlRequirementQueryer, decisionPK string, includeDeleted bool) ([]decisionLinkRecord, error) {
	query := `
		SELECT
			l.id, l.decision_id, d.local_id,
			l.target_kind, l.target_id, l.relation, l.note,
			l.created_at, l.updated_at, l.deleted_at
		FROM decision_links l
		JOIN decisions d ON d.id = l.decision_id
		WHERE l.decision_id = ?
	`
	if !includeDeleted {
		query += ` AND l.deleted_at IS NULL`
	}
	query += ` ORDER BY l.updated_at DESC, l.id DESC`

	rows, err := queryer.QueryContext(ctx, query, decisionPK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]decisionLinkRecord, 0, 8)
	for rows.Next() {
		record, scanErr := scanDecisionLinkRecord(rows)
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

func scanDecisionRecord(scanner interface {
	Scan(dest ...any) error
}) (decisionRecord, error) {
	var record decisionRecord
	var rowID any
	var statusRaw string
	var createdRaw string
	var updatedRaw string
	var deletedRaw sql.NullString
	if err := scanner.Scan(
		&rowID,
		&record.Decision.LocalID,
		&record.Decision.Title,
		&record.Decision.Context,
		&record.Decision.Decision,
		&record.Decision.Consequences,
		&statusRaw,
		&createdRaw,
		&updatedRaw,
		&deletedRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return decisionRecord{}, domain.ErrNotFound
		}
		return decisionRecord{}, err
	}
	record.rowID = normalizeDBID(rowID)
	record.Decision.Status = DecisionStatus(statusRaw)
	record.Decision.CreatedAt = parseTimestamp(createdRaw)
	record.Decision.UpdatedAt = parseTimestamp(updatedRaw)
	record.Decision.DeletedAt = parseNullableTimestamp(deletedRaw)
	return record, nil
}

func scanDecisionLinkRecord(scanner interface {
	Scan(dest ...any) error
}) (decisionLinkRecord, error) {
	var record decisionLinkRecord
	var rowID any
	var decisionPK any
	var decisionLocalID string
	var targetKindRaw string
	var relationRaw string
	var note sql.NullString
	var createdRaw string
	var updatedRaw string
	var deletedRaw sql.NullString

	if err := scanner.Scan(
		&rowID,
		&decisionPK,
		&decisionLocalID,
		&targetKindRaw,
		&record.TargetID,
		&relationRaw,
		&note,
		&createdRaw,
		&updatedRaw,
		&deletedRaw,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return decisionLinkRecord{}, domain.ErrNotFound
		}
		return decisionLinkRecord{}, err
	}
	record.rowID = normalizeDBID(rowID)
	record.decisionPK = normalizeDBID(decisionPK)
	record.DecisionID = decisionLocalID
	record.TargetKind = DecisionTargetKind(targetKindRaw)
	record.Relation = DecisionRelation(relationRaw)
	record.Note = nullStringPointer(note)
	record.CreatedAt = parseTimestamp(createdRaw)
	record.UpdatedAt = parseTimestamp(updatedRaw)
	record.DeletedAt = parseNullableTimestamp(deletedRaw)
	record.ID = decisionLinkID(record.DecisionID, record.TargetKind, record.TargetID)
	return record, nil
}

func ensureDecisionLinkTargetExists(ctx context.Context, queryer sqlRequirementQueryer, kind DecisionTargetKind, targetID string) error {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return errors.New("target id is required")
	}
	switch kind {
	case DecisionTargetIssue:
		var exists bool
		if err := queryer.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM issues WHERE id = ? AND deleted_at IS NULL
			)
		`, targetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: issue %q", domain.ErrNotFound, targetID)
		}
		return nil
	case DecisionTargetRequirement:
		var exists bool
		if err := queryer.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM spec_requirements WHERE local_id = ? AND deleted_at IS NULL
			)
		`, targetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: requirement %q", domain.ErrNotFound, targetID)
		}
		return nil
	default:
		return fmt.Errorf("invalid target kind %q", kind)
	}
}

func normalizeCreateDecisionParams(params CreateDecisionParams) (CreateDecisionParams, error) {
	params.LocalID = strings.TrimSpace(params.LocalID)
	params.Title = strings.TrimSpace(params.Title)
	params.Context = strings.TrimSpace(params.Context)
	params.Decision = strings.TrimSpace(params.Decision)
	params.Consequences = strings.TrimSpace(params.Consequences)
	if params.LocalID == "" {
		return params, errors.New("decision id is required")
	}
	if params.Title == "" {
		return params, errors.New("decision title is required")
	}
	if params.Status == "" {
		params.Status = DecisionStatusProposed
	}
	if !ValidDecisionStatus(params.Status) {
		return params, fmt.Errorf("invalid decision status %q", params.Status)
	}
	return params, nil
}

func applyDecisionUpdate(current Decision, params UpdateDecisionParams) (Decision, error) {
	after := current
	if params.Title != nil {
		title := strings.TrimSpace(*params.Title)
		if title == "" {
			return Decision{}, errors.New("decision title cannot be empty")
		}
		after.Title = title
	}
	if params.Context != nil {
		after.Context = strings.TrimSpace(*params.Context)
	}
	if params.Decision != nil {
		after.Decision = strings.TrimSpace(*params.Decision)
	}
	if params.Consequences != nil {
		after.Consequences = strings.TrimSpace(*params.Consequences)
	}
	if params.Status != nil {
		if !ValidDecisionStatus(*params.Status) {
			return Decision{}, fmt.Errorf("invalid decision status %q", *params.Status)
		}
		after.Status = *params.Status
	}
	return after, nil
}

func normalizeAddDecisionLinkParams(params AddDecisionLinkParams) (AddDecisionLinkParams, error) {
	params.DecisionID = strings.TrimSpace(params.DecisionID)
	params.TargetID = strings.TrimSpace(params.TargetID)
	params.Note = normalizeOptionalString(params.Note)
	if params.DecisionID == "" {
		return params, errors.New("decision id is required")
	}
	if params.TargetID == "" {
		return params, errors.New("target id is required")
	}
	if !ValidDecisionTargetKind(params.TargetKind) {
		return params, fmt.Errorf("invalid target kind %q", params.TargetKind)
	}
	if params.Relation == "" {
		params.Relation = DecisionRelationRelates
	}
	if !ValidDecisionRelation(params.Relation) {
		return params, fmt.Errorf("invalid relation %q", params.Relation)
	}
	return params, nil
}

func dedupeDecisionStatuses(values []DecisionStatus) []DecisionStatus {
	seen := make(map[DecisionStatus]struct{}, len(values))
	out := make([]DecisionStatus, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func filterDecisionsByLocalID(decisions []Decision, ids []string) []Decision {
	if len(ids) == 0 {
		return decisions
	}
	byID := make(map[string]Decision, len(decisions))
	for _, d := range decisions {
		byID[d.LocalID] = d
	}
	out := make([]Decision, 0, len(ids))
	for _, id := range ids {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		}
	}
	return out
}

func decisionLinkID(decisionLocalID string, kind DecisionTargetKind, targetID string) string {
	return decisionLocalID + ":" + string(kind) + ":" + targetID
}
