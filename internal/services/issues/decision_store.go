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

// DecisionTargetKind enumerates what a decision link points at. Decisions can
// link to issues, requirements, or other decisions (the latter is how revisions
// are recorded).
type DecisionTargetKind string

const (
	DecisionTargetIssue       DecisionTargetKind = "issue"
	DecisionTargetRequirement DecisionTargetKind = "requirement"
	DecisionTargetDecision    DecisionTargetKind = "decision"
)

func ValidDecisionTargetKind(k DecisionTargetKind) bool {
	switch k {
	case DecisionTargetIssue, DecisionTargetRequirement, DecisionTargetDecision:
		return true
	}
	return false
}

// DecisionRelation is the role a link plays. Defaults to applies-to. The
// revises relation is what we use to express "decision X replaced decision Y";
// the existence of such a link is the single source of truth for revisiting.
type DecisionRelation string

const (
	DecisionRelationAppliesTo DecisionRelation = "applies-to"
	DecisionRelationRevises   DecisionRelation = "revises"
	DecisionRelationInforms   DecisionRelation = "informs"
)

func ValidDecisionRelation(r DecisionRelation) bool {
	switch r {
	case DecisionRelationAppliesTo, DecisionRelationRevises, DecisionRelationInforms:
		return true
	}
	return false
}

// Decision is the recorded fact of a choice plus the reasoning behind it.
// Status is intentionally absent — whether a decision was revisited is
// inferred from the presence of an incoming revises link, not from a column.
type Decision struct {
	LocalID      string     `json:"id"`
	Title        string     `json:"title"`
	Rationale    string     `json:"rationale"`
	Context      string     `json:"context,omitempty"`
	Consequences string     `json:"consequences,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

// DecisionLink connects a decision to an issue, a requirement, or another
// decision. The latter case (target_kind=decision, relation=revises) is how
// supersession is expressed.
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

type RecordDecisionParams struct {
	Title        string
	Rationale    string
	Context      string
	Consequences string
}

// ImportDecisionParams creates a decision at an explicit local_id (and the
// matching numeric rowid). Used by the markdown importer so a `dec-N` from
// another machine keeps its identity locally; without explicit ids the
// SQLite AUTOINCREMENT would assign a different rowid and the dec-N name
// would drift relative to the rowid.
type ImportDecisionParams struct {
	LocalID      string
	NumericID    int64
	Title        string
	Rationale    string
	Context      string
	Consequences string
}

type UpdateDecisionParams struct {
	Title        *string
	Rationale    *string
	Context      *string
	Consequences *string
}

type DecisionFilter struct {
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

// RecordDecision creates a new decision with an auto-allocated dec-N id.
// Title and rationale are required; context is optional.
func (c *Client) RecordDecision(ctx context.Context, params RecordDecisionParams) (Decision, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Decision{}, err
	}

	normalized, err := normalizeRecordDecisionParams(params)
	if err != nil {
		return Decision{}, c.wrapError("record-decision", "", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, c.wrapError("record-decision", "", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO decisions (
			local_id, title, rationale, context, consequences, created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
	`,
		"", // placeholder; filled in via UPDATE below using the assigned rowid
		normalized.Title,
		nullableString(normalized.Rationale),
		nullableString(normalized.Context),
		nullableString(normalized.Consequences),
		formatTimestamp(now),
		formatTimestamp(now),
	)
	if err != nil {
		return Decision{}, c.wrapError("record-decision", "", classifySQLiteConstraint(err))
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		return Decision{}, c.wrapError("record-decision", "", err)
	}
	localID := fmt.Sprintf("dec-%d", rowID)
	if _, err := tx.ExecContext(ctx, `UPDATE decisions SET local_id = ? WHERE id = ?`, localID, rowID); err != nil {
		return Decision{}, c.wrapError("record-decision", localID, classifySQLiteConstraint(err))
	}

	decision := Decision{
		LocalID:      localID,
		Title:        normalized.Title,
		Rationale:    normalized.Rationale,
		Context:      normalized.Context,
		Consequences: normalized.Consequences,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := c.insertDecisionAuditRow(ctx, tx, decisionEntityKind, localID, decisionOpCreate, nil, decision); err != nil {
		return Decision{}, c.wrapError("record-decision", localID, err)
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, c.wrapError("record-decision", localID, err)
	}
	tx = nil
	return decision, nil
}

// ImportDecision inserts a decision with an explicit local_id and numeric
// rowid. Errors with domain.ErrConflict if a row with that local_id already
// exists; callers should check via GetDecision first and fall back to
// UpdateDecision for existing rows.
func (c *Client) ImportDecision(ctx context.Context, params ImportDecisionParams) (Decision, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Decision{}, err
	}

	params.LocalID = strings.TrimSpace(params.LocalID)
	params.Title = strings.TrimSpace(params.Title)
	params.Rationale = strings.TrimSpace(params.Rationale)
	params.Context = strings.TrimSpace(params.Context)
	params.Consequences = strings.TrimSpace(params.Consequences)
	if params.LocalID == "" {
		return Decision{}, c.wrapError("import-decision", "", errors.New("local_id is required"))
	}
	if params.NumericID <= 0 {
		return Decision{}, c.wrapError("import-decision", params.LocalID, errors.New("numeric id must be positive"))
	}
	if params.Title == "" {
		return Decision{}, c.wrapError("import-decision", params.LocalID, errors.New("title is required"))
	}
	if params.Rationale == "" {
		return Decision{}, c.wrapError("import-decision", params.LocalID, errors.New("rationale is required"))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Decision{}, c.wrapError("import-decision", params.LocalID, err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO decisions (
			id, local_id, title, rationale, context, consequences,
			created_at, updated_at, deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`,
		params.NumericID,
		params.LocalID,
		params.Title,
		nullableString(params.Rationale),
		nullableString(params.Context),
		nullableString(params.Consequences),
		formatTimestamp(now),
		formatTimestamp(now),
	); err != nil {
		return Decision{}, c.wrapError("import-decision", params.LocalID, classifySQLiteConstraint(err))
	}

	decision := Decision{
		LocalID:      params.LocalID,
		Title:        params.Title,
		Rationale:    params.Rationale,
		Context:      params.Context,
		Consequences: params.Consequences,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := c.insertDecisionAuditRow(ctx, tx, decisionEntityKind, decision.LocalID, decisionOpCreate, nil, decision); err != nil {
		return Decision{}, c.wrapError("import-decision", decision.LocalID, err)
	}
	if err := tx.Commit(); err != nil {
		return Decision{}, c.wrapError("import-decision", decision.LocalID, err)
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
			COALESCE(d.rationale, ''),
			COALESCE(d.context, ''),
			COALESCE(d.consequences, ''),
			d.created_at, d.updated_at, d.deleted_at
		FROM decisions d
	`)
	args := make([]any, 0, 6)
	joinedLinks := false
	joinLinks := func() {
		if joinedLinks {
			return
		}
		query.WriteString(` JOIN decision_links l ON l.decision_id = d.id AND l.deleted_at IS NULL`)
		joinedLinks = true
	}
	if strings.TrimSpace(filter.IssueID) != "" || strings.TrimSpace(filter.RequirementID) != "" {
		joinLinks()
	}

	query.WriteString(` WHERE 1 = 1`)
	if !filter.IncludeDeleted {
		query.WriteString(` AND d.deleted_at IS NULL`)
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
		query.WriteString(` AND (d.local_id LIKE ? OR d.title LIKE ? OR COALESCE(d.rationale, '') LIKE ? OR COALESCE(d.context, '') LIKE ? OR COALESCE(d.consequences, '') LIKE ?)`)
		args = append(args, like, like, like, like, like)
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
		SET title = ?, rationale = ?, context = ?, consequences = ?, updated_at = ?
		WHERE id = ?
	`,
		after.Title,
		nullableString(after.Rationale),
		nullableString(after.Context),
		nullableString(after.Consequences),
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
		UPDATE decisions SET deleted_at = ?, updated_at = ?
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
		UPDATE decision_links SET deleted_at = ?, updated_at = ?
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
			COALESCE(rationale, ''),
			COALESCE(context, ''),
			COALESCE(consequences, ''),
			created_at, updated_at, deleted_at
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
	var createdRaw string
	var updatedRaw string
	var deletedRaw sql.NullString
	if err := scanner.Scan(
		&rowID,
		&record.Decision.LocalID,
		&record.Decision.Title,
		&record.Decision.Rationale,
		&record.Decision.Context,
		&record.Decision.Consequences,
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

// ensureDecisionLinkTargetExists validates that the link target exists. For
// decision-to-decision links the target must be an undeleted decision other
// than the source (self-revising would be confusing); issues and requirements
// use their existing tables.
func ensureDecisionLinkTargetExists(ctx context.Context, queryer sqlRequirementQueryer, kind DecisionTargetKind, targetID string) error {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return errors.New("target id is required")
	}
	switch kind {
	case DecisionTargetIssue:
		var exists bool
		if err := queryer.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM issues WHERE id = ? AND deleted_at IS NULL)
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
			SELECT EXISTS(SELECT 1 FROM spec_requirements WHERE local_id = ? AND deleted_at IS NULL)
		`, targetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: requirement %q", domain.ErrNotFound, targetID)
		}
		return nil
	case DecisionTargetDecision:
		var exists bool
		if err := queryer.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM decisions WHERE local_id = ? AND deleted_at IS NULL)
		`, targetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: decision %q", domain.ErrNotFound, targetID)
		}
		return nil
	default:
		return fmt.Errorf("invalid target kind %q", kind)
	}
}

func normalizeRecordDecisionParams(params RecordDecisionParams) (RecordDecisionParams, error) {
	params.Title = strings.TrimSpace(params.Title)
	params.Rationale = strings.TrimSpace(params.Rationale)
	params.Context = strings.TrimSpace(params.Context)
	params.Consequences = strings.TrimSpace(params.Consequences)
	if params.Title == "" {
		return params, errors.New("decision title is required")
	}
	if params.Rationale == "" {
		return params, errors.New("decision rationale is required")
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
	if params.Rationale != nil {
		rationale := strings.TrimSpace(*params.Rationale)
		if rationale == "" {
			return Decision{}, errors.New("decision rationale cannot be empty")
		}
		after.Rationale = rationale
	}
	if params.Context != nil {
		after.Context = strings.TrimSpace(*params.Context)
	}
	if params.Consequences != nil {
		after.Consequences = strings.TrimSpace(*params.Consequences)
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
	if params.TargetKind == DecisionTargetDecision && params.TargetID == params.DecisionID {
		return params, errors.New("a decision cannot link to itself")
	}
	if params.Relation == "" {
		params.Relation = DecisionRelationAppliesTo
	}
	if !ValidDecisionRelation(params.Relation) {
		return params, fmt.Errorf("invalid relation %q", params.Relation)
	}
	return params, nil
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
