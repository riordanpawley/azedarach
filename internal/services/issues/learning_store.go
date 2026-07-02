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

const (
	maxLearningSummaryRunes  = 240
	maxLearningEvidenceRunes = 8000
)

type LearningStatus string

const (
	LearningStatusCandidate LearningStatus = "candidate"
	LearningStatusAccepted  LearningStatus = "accepted"
	LearningStatusRejected  LearningStatus = "rejected"
	LearningStatusPromoted  LearningStatus = "promoted"
	LearningStatusStale     LearningStatus = "stale"
)

type LearningPromotionTarget string

const (
	LearningPromotionTargetRulesync LearningPromotionTarget = "rulesync"
	LearningPromotionTargetAgents   LearningPromotionTarget = "agents"
	LearningPromotionTargetSkill    LearningPromotionTarget = "skill"
	LearningPromotionTargetSpec     LearningPromotionTarget = "spec"
	LearningPromotionTargetDecision LearningPromotionTarget = "decision"
)

type LearningRelationType string

const (
	LearningRelationSupersedes LearningRelationType = "supersedes"
	LearningRelationConflicts  LearningRelationType = "conflicts"
)

type Learning struct {
	LocalID         string
	ProjectID       string
	IssueID         *string
	RequirementID   *string
	SessionID       *string
	Summary         string
	Evidence        string
	Status          LearningStatus
	ReviewNote      string
	ReviewedAt      *time.Time
	Tags            []string
	Files           []string
	Target          *LearningPromotionTarget
	TargetID        string
	TargetNote      string
	PromotedAt      *time.Time
	ExpiresAt       *time.Time
	StaleAt         *time.Time
	LastRecalledAt  *time.Time
	RecallCount     int
	SupersededAt    *time.Time
	TargetRetiredAt *time.Time
	Relations       []LearningRelation
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

type LearningRelation struct {
	LocalID            string
	Type               LearningRelationType
	SourceLearningID   string
	TargetLearningID   string
	Note               string
	ScopeIssueID       *string
	ScopeRequirementID *string
	ScopeSessionID     *string
	ScopeTags          []string
	ScopeFiles         []string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

type CreateLearningParams struct {
	ProjectID     string
	IssueID       *string
	RequirementID *string
	SessionID     *string
	Summary       string
	Evidence      string
	Status        LearningStatus
	Tags          []string
	Files         []string
}

type ReviewLearningParams struct {
	Status LearningStatus
	Note   string
}

type PromoteLearningParams struct {
	Target   LearningPromotionTarget
	TargetID string
	Note     string
}

type RelateLearningParams struct {
	Type               LearningRelationType
	SourceLearningID   string
	TargetLearningID   string
	Note               string
	ScopeIssueID       *string
	ScopeRequirementID *string
	ScopeSessionID     *string
	ScopeTags          []string
	ScopeFiles         []string
}

type LearningFilter struct {
	ProjectID       string
	IssueID         string
	RequirementID   string
	Query           string
	Statuses        []LearningStatus
	Tags            []string
	Files           []string
	Limit           int
	IncludeEvidence bool
	IncludeDeleted  bool
	ActiveOnly      bool
}

type learningRecord struct {
	rowID string
	Learning
}

func (s LearningStatus) Valid() bool {
	switch s {
	case LearningStatusCandidate, LearningStatusAccepted, LearningStatusRejected, LearningStatusPromoted, LearningStatusStale:
		return true
	default:
		return false
	}
}

func (t LearningPromotionTarget) Valid() bool {
	switch t {
	case LearningPromotionTargetRulesync, LearningPromotionTargetAgents, LearningPromotionTargetSkill, LearningPromotionTargetSpec, LearningPromotionTargetDecision:
		return true
	default:
		return false
	}
}

func (t LearningRelationType) Valid() bool {
	switch t {
	case LearningRelationSupersedes, LearningRelationConflicts:
		return true
	default:
		return false
	}
}

func (c *Client) CreateLearning(ctx context.Context, params CreateLearningParams) (Learning, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Learning{}, err
	}
	normalized, err := normalizeCreateLearningParams(params)
	if err != nil {
		return Learning{}, c.wrapError("create-learning", "", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Learning{}, c.wrapError("create-learning", "", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if err := ensureIssueExists(ctx, tx, normalized.IssueID); err != nil {
		return Learning{}, c.wrapError("create-learning", "", err)
	}
	reqRowID, err := learningRequirementRowID(ctx, tx, normalized.RequirementID)
	if err != nil {
		return Learning{}, c.wrapError("create-learning", "", err)
	}

	now := time.Now().UTC()
	tags := normalizeStringSlice(normalized.Tags)
	files := normalizeStringSlice(normalized.Files)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_learnings (
			local_id, project_id, issue_id, requirement_id, session_id,
			summary, evidence, status, tags_json, files_json,
			created_at, updated_at, deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, fmt.Sprintf("pending-%d", now.UnixNano()), normalized.ProjectID, nullableTextPtr(normalized.IssueID), reqRowID, nullableTextPtr(normalized.SessionID), normalized.Summary, normalized.Evidence, string(normalized.Status), mustMarshalJSONSlice(tags), mustMarshalJSONSlice(files), formatTimestamp(now), formatTimestamp(now))
	if err != nil {
		return Learning{}, c.wrapError("create-learning", "", classifySQLiteConstraint(err))
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		return Learning{}, c.wrapError("create-learning", "", err)
	}
	localID := fmt.Sprintf("learn-%d", rowID)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_learnings SET local_id = ?, updated_at = ? WHERE id = ?`, localID, formatTimestamp(now), rowID); err != nil {
		return Learning{}, c.wrapError("create-learning", localID, classifySQLiteConstraint(err))
	}
	if err := tx.Commit(); err != nil {
		return Learning{}, c.wrapError("create-learning", localID, err)
	}
	tx = nil
	return Learning{
		LocalID:       localID,
		ProjectID:     normalized.ProjectID,
		IssueID:       cloneStringPointer(normalized.IssueID),
		RequirementID: cloneStringPointer(normalized.RequirementID),
		SessionID:     cloneStringPointer(normalized.SessionID),
		Summary:       normalized.Summary,
		Evidence:      normalized.Evidence,
		Status:        normalized.Status,
		Tags:          tags,
		Files:         files,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (c *Client) GetLearning(ctx context.Context, selector string) (Learning, error) {
	db, err := c.dbHandle()
	if err != nil {
		return Learning{}, err
	}
	record, err := c.lookupLearningByLocalID(ctx, db, selector, false)
	if err != nil {
		return Learning{}, c.wrapError("get-learning", strings.TrimSpace(selector), err)
	}
	relations, err := c.listLearningRelationsForRow(ctx, db, record.rowID, false)
	if err != nil {
		return Learning{}, c.wrapError("get-learning", record.LocalID, err)
	}
	record.Relations = relations
	return record.Learning, nil
}

func (c *Client) ListLearnings(ctx context.Context, filter LearningFilter) ([]Learning, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	query := strings.Builder{}
	args := make([]any, 0, 8)
	query.WriteString(`
		SELECT l.id, l.local_id, l.project_id, l.issue_id, r.local_id, l.session_id,
			l.summary, l.evidence, l.status, l.review_note, l.reviewed_at,
			COALESCE(l.tags_json, '[]'), COALESCE(l.files_json, '[]'),
			l.promotion_target, l.promotion_target_id, l.promotion_note, l.promoted_at,
			l.expires_at, l.stale_at, l.last_recalled_at, l.recall_count,
			l.superseded_at, l.target_retired_at,
			l.created_at, l.updated_at, l.deleted_at
		FROM agent_learnings l
		LEFT JOIN spec_requirements r ON r.id = l.requirement_id
	`)
	if match := learningFTSMatchQuery(filter.Query); match != "" {
		query.WriteString(` JOIN agent_learning_search_fts fts ON fts.rowid = l.id AND agent_learning_search_fts MATCH ?`)
		args = append(args, match)
	}
	query.WriteString(` WHERE 1 = 1`)
	if !filter.IncludeDeleted {
		query.WriteString(` AND l.deleted_at IS NULL`)
	}
	if filter.ActiveOnly {
		query.WriteString(`
			AND l.deleted_at IS NULL
			AND l.status IN (?, ?)
			AND l.superseded_at IS NULL
			AND l.target_retired_at IS NULL
		`)
		args = append(args, string(LearningStatusAccepted), string(LearningStatusPromoted))
	}
	if trimmed := strings.TrimSpace(filter.ProjectID); trimmed != "" {
		query.WriteString(` AND l.project_id = ?`)
		args = append(args, trimmed)
	}
	if trimmed := strings.TrimSpace(filter.IssueID); trimmed != "" {
		query.WriteString(` AND l.issue_id = ?`)
		args = append(args, trimmed)
	}
	if trimmed := strings.TrimSpace(filter.RequirementID); trimmed != "" {
		requirement, err := c.lookupRequirementBySelector(ctx, db, trimmed, filter.IncludeDeleted)
		if err != nil {
			return nil, c.wrapError("list-learnings", "", err)
		}
		query.WriteString(` AND l.requirement_id = ?`)
		args = append(args, requirement.rowID)
	}
	if len(filter.Statuses) > 0 {
		query.WriteString(` AND l.status IN (` + placeholders(len(filter.Statuses)) + `)`)
		for _, status := range filter.Statuses {
			args = append(args, string(status))
		}
	}
	if strings.TrimSpace(filter.Query) != "" {
		query.WriteString(` ORDER BY bm25(agent_learning_search_fts), l.updated_at DESC, l.local_id ASC`)
	} else {
		query.WriteString(` ORDER BY l.updated_at DESC, l.local_id ASC`)
	}
	sqlLimit := filter.Limit
	if len(filter.Tags) > 0 || len(filter.Files) > 0 || filter.ActiveOnly {
		sqlLimit = 0
	}
	if sqlLimit > 0 {
		query.WriteString(` LIMIT ?`)
		args = append(args, sqlLimit)
	}
	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, c.wrapError("list-learnings", "", err)
	}
	defer rows.Close()
	activeAt := time.Now().UTC()
	records := make([]learningRecord, 0, 16)
	for rows.Next() {
		record, scanErr := scanLearningRecord(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-learnings", "", scanErr)
		}
		learning := record.Learning
		if !filter.IncludeEvidence {
			learning.Evidence = ""
		}
		if filter.ActiveOnly && !learningActiveAt(learning, activeAt) {
			continue
		}
		if learningHasAll(learning.Tags, filter.Tags) && learningHasAll(learning.Files, filter.Files) {
			record.Learning = learning
			records = append(records, record)
			if filter.Limit > 0 && len(records) >= filter.Limit {
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-learnings", "", err)
	}
	if filter.ActiveOnly && len(records) > 0 {
		recalledAt := time.Now().UTC()
		for i := range records {
			if _, err := db.ExecContext(ctx, `
				UPDATE agent_learnings
				SET last_recalled_at = ?, recall_count = recall_count + 1
				WHERE id = ?
			`, formatTimestamp(recalledAt), records[i].rowID); err != nil {
				return nil, c.wrapError("list-learnings", records[i].LocalID, err)
			}
			records[i].LastRecalledAt = &recalledAt
			records[i].RecallCount++
		}
	}
	out := make([]Learning, 0, len(records))
	for _, record := range records {
		out = append(out, record.Learning)
	}
	return out, nil
}

func (c *Client) UpdateLearningStatus(ctx context.Context, selector string, status LearningStatus, note string) (Learning, error) {
	normalized := ReviewLearningParams{
		Status: status,
		Note:   strings.TrimSpace(note),
	}
	if !normalized.Status.Valid() {
		return Learning{}, c.wrapError("update-learning-status", selector, errors.New("invalid learning status"))
	}
	if normalized.Status == LearningStatusCandidate {
		return Learning{}, c.wrapError("update-learning-status", selector, errors.New("review status must be accepted, rejected, or stale"))
	}
	if normalized.Status == LearningStatusPromoted {
		return Learning{}, c.wrapError("update-learning-status", selector, fmt.Errorf("%w: use promote-learning to mark a learning promoted", domain.ErrConflict))
	}
	if normalized.Note == "" {
		return Learning{}, c.wrapError("update-learning-status", selector, errors.New("review note is required"))
	}
	db, err := c.dbHandle()
	if err != nil {
		return Learning{}, err
	}
	record, err := c.lookupLearningByLocalID(ctx, db, selector, false)
	if err != nil {
		return Learning{}, c.wrapError("update-learning-status", selector, err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		UPDATE agent_learnings
		SET status = ?, review_note = ?, reviewed_at = ?, updated_at = ?
		WHERE id = ?
	`, string(normalized.Status), nullableString(normalized.Note), formatTimestamp(now), formatTimestamp(now), record.rowID); err != nil {
		return Learning{}, c.wrapError("update-learning-status", record.LocalID, classifySQLiteConstraint(err))
	}
	return c.GetLearning(ctx, record.LocalID)
}

func (c *Client) PromoteLearning(ctx context.Context, selector string, params PromoteLearningParams) (Learning, error) {
	params.TargetID = strings.TrimSpace(params.TargetID)
	params.Note = strings.TrimSpace(params.Note)
	if !params.Target.Valid() {
		return Learning{}, c.wrapError("promote-learning", selector, errors.New("invalid promotion target"))
	}
	if params.TargetID == "" {
		return Learning{}, c.wrapError("promote-learning", selector, errors.New("promotion target id is required"))
	}
	db, err := c.dbHandle()
	if err != nil {
		return Learning{}, err
	}
	record, err := c.lookupLearningByLocalID(ctx, db, selector, false)
	if err != nil {
		return Learning{}, c.wrapError("promote-learning", selector, err)
	}
	if record.Status != LearningStatusAccepted && record.Status != LearningStatusPromoted {
		return Learning{}, c.wrapError("promote-learning", record.LocalID, fmt.Errorf("%w: learning must be accepted before promotion", domain.ErrConflict))
	}
	if err := c.validateLearningPromotionTarget(ctx, db, params); err != nil {
		return Learning{}, c.wrapError("promote-learning", record.LocalID, err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		UPDATE agent_learnings
		SET status = ?, promotion_target = ?, promotion_target_id = ?, promotion_note = ?, promoted_at = ?, updated_at = ?
		WHERE id = ?
	`, string(LearningStatusPromoted), string(params.Target), params.TargetID, nullableString(params.Note), formatTimestamp(now), formatTimestamp(now), record.rowID); err != nil {
		return Learning{}, c.wrapError("promote-learning", record.LocalID, classifySQLiteConstraint(err))
	}
	return c.GetLearning(ctx, record.LocalID)
}

func (c *Client) RelateLearning(ctx context.Context, params RelateLearningParams) (LearningRelation, error) {
	db, err := c.dbHandle()
	if err != nil {
		return LearningRelation{}, err
	}
	normalized, err := normalizeRelateLearningParams(params)
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", "", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", "", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	source, err := c.lookupLearningByLocalID(ctx, tx, normalized.SourceLearningID, false)
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", normalized.SourceLearningID, err)
	}
	target, err := c.lookupLearningByLocalID(ctx, tx, normalized.TargetLearningID, false)
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", normalized.TargetLearningID, err)
	}
	if source.rowID == target.rowID {
		return LearningRelation{}, c.wrapError("relate-learning", source.LocalID, fmt.Errorf("%w: learning cannot relate to itself", domain.ErrConflict))
	}
	if source.ProjectID != target.ProjectID {
		return LearningRelation{}, c.wrapError("relate-learning", source.LocalID, fmt.Errorf("%w: learning relations must stay within one project", domain.ErrConflict))
	}
	if normalized.Type == LearningRelationSupersedes {
		now := time.Now().UTC()
		if !learningActiveAt(source.Learning, now) {
			return LearningRelation{}, c.wrapError("relate-learning", source.LocalID, fmt.Errorf("%w: superseding learning must be active", domain.ErrConflict))
		}
		if target.SupersededAt != nil {
			return LearningRelation{}, c.wrapError("relate-learning", target.LocalID, fmt.Errorf("%w: target learning is already superseded", domain.ErrConflict))
		}
	}
	if err := ensureIssueExists(ctx, tx, normalized.ScopeIssueID); err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", "", err)
	}
	scopeReqRowID, err := learningRequirementRowID(ctx, tx, normalized.ScopeRequirementID)
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", "", err)
	}

	now := time.Now().UTC()
	scopeTags := normalizeStringSlice(normalized.ScopeTags)
	scopeFiles := normalizeStringSlice(normalized.ScopeFiles)
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_learning_relations (
			local_id, relation_type, source_learning_id, target_learning_id, note,
			scope_issue_id, scope_requirement_id, scope_session_id, scope_tags_json, scope_files_json,
			created_at, updated_at, deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, fmt.Sprintf("pending-%d", now.UnixNano()), string(normalized.Type), source.rowID, target.rowID, normalized.Note,
		nullableTextPtr(normalized.ScopeIssueID), scopeReqRowID, nullableTextPtr(normalized.ScopeSessionID),
		mustMarshalJSONSlice(scopeTags), mustMarshalJSONSlice(scopeFiles), formatTimestamp(now), formatTimestamp(now))
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", source.LocalID, classifySQLiteConstraint(err))
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", source.LocalID, err)
	}
	localID := fmt.Sprintf("learn-rel-%d", rowID)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_learning_relations SET local_id = ?, updated_at = ? WHERE id = ?`, localID, formatTimestamp(now), rowID); err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", localID, classifySQLiteConstraint(err))
	}
	if normalized.Type == LearningRelationSupersedes {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_learnings
			SET superseded_at = ?, updated_at = ?
			WHERE id = ?
		`, formatTimestamp(now), formatTimestamp(now), target.rowID); err != nil {
			return LearningRelation{}, c.wrapError("relate-learning", target.LocalID, classifySQLiteConstraint(err))
		}
	}
	relation, err := c.lookupLearningRelationByRowID(ctx, tx, rowID)
	if err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", localID, err)
	}
	if err := tx.Commit(); err != nil {
		return LearningRelation{}, c.wrapError("relate-learning", localID, err)
	}
	tx = nil
	return relation, nil
}

func (c *Client) validateLearningPromotionTarget(ctx context.Context, db sqlIssueDBTX, params PromoteLearningParams) error {
	switch params.Target {
	case LearningPromotionTargetDecision:
		_, err := c.lookupDecisionByLocalID(ctx, db, params.TargetID, false)
		return err
	case LearningPromotionTargetSpec:
		_, err := c.lookupRequirementBySelector(ctx, db, params.TargetID, false)
		return err
	case LearningPromotionTargetRulesync, LearningPromotionTargetAgents, LearningPromotionTargetSkill:
		return nil
	default:
		return errors.New("invalid promotion target")
	}
}

func normalizeRelateLearningParams(params RelateLearningParams) (RelateLearningParams, error) {
	params.Type = LearningRelationType(strings.TrimSpace(string(params.Type)))
	params.SourceLearningID = strings.TrimSpace(params.SourceLearningID)
	params.TargetLearningID = strings.TrimSpace(params.TargetLearningID)
	params.Note = strings.TrimSpace(params.Note)
	params.ScopeIssueID = normalizeOptionalString(params.ScopeIssueID)
	params.ScopeRequirementID = normalizeOptionalString(params.ScopeRequirementID)
	params.ScopeSessionID = normalizeOptionalString(params.ScopeSessionID)
	if !params.Type.Valid() {
		return params, errors.New("invalid learning relation type")
	}
	if params.SourceLearningID == "" || params.TargetLearningID == "" {
		return params, errors.New("source and target learning ids are required")
	}
	if params.Note == "" {
		return params, errors.New("relation note is required")
	}
	return params, nil
}

func normalizeCreateLearningParams(params CreateLearningParams) (CreateLearningParams, error) {
	params.ProjectID = strings.TrimSpace(params.ProjectID)
	if params.ProjectID == "" {
		params.ProjectID = "default"
	}
	params.IssueID = normalizeOptionalString(params.IssueID)
	params.RequirementID = normalizeOptionalString(params.RequirementID)
	params.SessionID = normalizeOptionalString(params.SessionID)
	params.Summary = strings.TrimSpace(params.Summary)
	params.Evidence = strings.TrimSpace(params.Evidence)
	if params.Evidence == "" {
		return params, errors.New("evidence is required")
	}
	if len([]rune(params.Evidence)) > maxLearningEvidenceRunes {
		return params, fmt.Errorf("evidence exceeds %d rune limit", maxLearningEvidenceRunes)
	}
	if params.Summary == "" {
		params.Summary = summarizeLearningEvidence(params.Evidence)
	}
	if len([]rune(params.Summary)) > maxLearningSummaryRunes {
		return params, fmt.Errorf("summary exceeds %d rune limit", maxLearningSummaryRunes)
	}
	if params.Status == "" {
		params.Status = LearningStatusCandidate
	}
	if !params.Status.Valid() {
		return params, errors.New("invalid learning status")
	}
	if params.Status != LearningStatusCandidate {
		return params, errors.New("new learnings must start as candidate")
	}
	return params, nil
}

func summarizeLearningEvidence(evidence string) string {
	const maxRunes = 120
	fields := strings.Fields(evidence)
	if len(fields) == 0 {
		return ""
	}
	summary := strings.Join(fields, " ")
	runes := []rune(summary)
	if len(runes) <= maxRunes {
		return summary
	}
	return strings.TrimSpace(string(runes[:maxRunes]))
}

func learningRequirementRowID(ctx context.Context, tx *sql.Tx, requirementID *string) (any, error) {
	if requirementID == nil || strings.TrimSpace(*requirementID) == "" {
		return nil, nil
	}
	var rowID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM spec_requirements WHERE local_id = ? AND deleted_at IS NULL`, strings.TrimSpace(*requirementID)).Scan(&rowID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return rowID, nil
}

func (c *Client) lookupLearningByLocalID(ctx context.Context, db sqlIssueDBTX, selector string, includeDeleted bool) (learningRecord, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return learningRecord{}, errors.New("learning id is required")
	}
	query := `
		SELECT l.id, l.local_id, l.project_id, l.issue_id, r.local_id, l.session_id,
			l.summary, l.evidence, l.status, l.review_note, l.reviewed_at,
			COALESCE(l.tags_json, '[]'), COALESCE(l.files_json, '[]'),
			l.promotion_target, l.promotion_target_id, l.promotion_note, l.promoted_at,
			l.expires_at, l.stale_at, l.last_recalled_at, l.recall_count,
			l.superseded_at, l.target_retired_at,
			l.created_at, l.updated_at, l.deleted_at
		FROM agent_learnings l
		LEFT JOIN spec_requirements r ON r.id = l.requirement_id
		WHERE l.local_id = ?
	`
	if !includeDeleted {
		query += ` AND l.deleted_at IS NULL`
	}
	record, err := scanLearningRecord(db.QueryRowContext(ctx, query, selector))
	if errors.Is(err, sql.ErrNoRows) {
		return learningRecord{}, domain.ErrNotFound
	}
	return record, err
}

func (c *Client) lookupLearningRelationByRowID(ctx context.Context, db sqlIssueDBTX, rowID int64) (LearningRelation, error) {
	query := `
		SELECT rel.local_id, rel.relation_type, source.local_id, target.local_id, rel.note,
			rel.scope_issue_id, req.local_id, rel.scope_session_id,
			COALESCE(rel.scope_tags_json, '[]'), COALESCE(rel.scope_files_json, '[]'),
			rel.created_at, rel.updated_at, rel.deleted_at
		FROM agent_learning_relations rel
		JOIN agent_learnings source ON source.id = rel.source_learning_id
		JOIN agent_learnings target ON target.id = rel.target_learning_id
		LEFT JOIN spec_requirements req ON req.id = rel.scope_requirement_id
		WHERE rel.id = ?
	`
	return scanLearningRelation(db.QueryRowContext(ctx, query, rowID))
}

func (c *Client) listLearningRelationsForRow(ctx context.Context, db sqlIssueDBTX, rowID string, includeDeleted bool) ([]LearningRelation, error) {
	query := `
		SELECT rel.local_id, rel.relation_type, source.local_id, target.local_id, rel.note,
			rel.scope_issue_id, req.local_id, rel.scope_session_id,
			COALESCE(rel.scope_tags_json, '[]'), COALESCE(rel.scope_files_json, '[]'),
			rel.created_at, rel.updated_at, rel.deleted_at
		FROM agent_learning_relations rel
		JOIN agent_learnings source ON source.id = rel.source_learning_id
		JOIN agent_learnings target ON target.id = rel.target_learning_id
		LEFT JOIN spec_requirements req ON req.id = rel.scope_requirement_id
		WHERE (rel.source_learning_id = ? OR rel.target_learning_id = ?)
	`
	if !includeDeleted {
		query += ` AND rel.deleted_at IS NULL`
	}
	query += ` ORDER BY rel.created_at DESC, rel.local_id ASC`
	rows, err := db.QueryContext(ctx, query, rowID, rowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	relations := make([]LearningRelation, 0, 4)
	for rows.Next() {
		relation, scanErr := scanLearningRelation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return relations, nil
}

func scanLearningRelation(scanner interface {
	Scan(dest ...any) error
}) (LearningRelation, error) {
	var relation LearningRelation
	var scopeIssueRaw, scopeReqRaw, scopeSessionRaw, deletedRaw sql.NullString
	var scopeTagsJSON, scopeFilesJSON string
	var createdRaw, updatedRaw string
	if err := scanner.Scan(
		&relation.LocalID,
		&relation.Type,
		&relation.SourceLearningID,
		&relation.TargetLearningID,
		&relation.Note,
		&scopeIssueRaw,
		&scopeReqRaw,
		&scopeSessionRaw,
		&scopeTagsJSON,
		&scopeFilesJSON,
		&createdRaw,
		&updatedRaw,
		&deletedRaw,
	); err != nil {
		return LearningRelation{}, err
	}
	relation.ScopeIssueID = nullableStringPointer(scopeIssueRaw)
	relation.ScopeRequirementID = nullableStringPointer(scopeReqRaw)
	relation.ScopeSessionID = nullableStringPointer(scopeSessionRaw)
	relation.ScopeTags = unmarshalJSONStringSlice(scopeTagsJSON)
	relation.ScopeFiles = unmarshalJSONStringSlice(scopeFilesJSON)
	relation.CreatedAt = parseTimestamp(createdRaw)
	relation.UpdatedAt = parseTimestamp(updatedRaw)
	relation.DeletedAt = nullableTimePointer(deletedRaw)
	return relation, nil
}

func scanLearningRecord(scanner interface {
	Scan(dest ...any) error
}) (learningRecord, error) {
	var record learningRecord
	var issueRaw, reqRaw, sessionRaw, reviewNoteRaw, reviewedRaw, targetRaw, targetIDRaw, targetNoteRaw, promotedRaw sql.NullString
	var expiresRaw, staleRaw, lastRecalledRaw, supersededRaw, targetRetiredRaw, deletedRaw sql.NullString
	var tagsJSON, filesJSON string
	var createdRaw, updatedRaw string
	if err := scanner.Scan(
		&record.rowID,
		&record.LocalID,
		&record.ProjectID,
		&issueRaw,
		&reqRaw,
		&sessionRaw,
		&record.Summary,
		&record.Evidence,
		&record.Status,
		&reviewNoteRaw,
		&reviewedRaw,
		&tagsJSON,
		&filesJSON,
		&targetRaw,
		&targetIDRaw,
		&targetNoteRaw,
		&promotedRaw,
		&expiresRaw,
		&staleRaw,
		&lastRecalledRaw,
		&record.RecallCount,
		&supersededRaw,
		&targetRetiredRaw,
		&createdRaw,
		&updatedRaw,
		&deletedRaw,
	); err != nil {
		return learningRecord{}, err
	}
	record.IssueID = nullableStringPointer(issueRaw)
	record.RequirementID = nullableStringPointer(reqRaw)
	record.SessionID = nullableStringPointer(sessionRaw)
	record.ReviewNote = strings.TrimSpace(reviewNoteRaw.String)
	record.ReviewedAt = nullableTimePointer(reviewedRaw)
	record.Tags = unmarshalJSONStringSlice(tagsJSON)
	record.Files = unmarshalJSONStringSlice(filesJSON)
	if targetRaw.Valid && strings.TrimSpace(targetRaw.String) != "" {
		target := LearningPromotionTarget(targetRaw.String)
		record.Target = &target
	}
	record.TargetID = strings.TrimSpace(targetIDRaw.String)
	record.TargetNote = strings.TrimSpace(targetNoteRaw.String)
	record.PromotedAt = nullableTimePointer(promotedRaw)
	record.ExpiresAt = nullableTimePointer(expiresRaw)
	record.StaleAt = nullableTimePointer(staleRaw)
	record.LastRecalledAt = nullableTimePointer(lastRecalledRaw)
	record.SupersededAt = nullableTimePointer(supersededRaw)
	record.TargetRetiredAt = nullableTimePointer(targetRetiredRaw)
	record.CreatedAt = parseTimestamp(createdRaw)
	record.UpdatedAt = parseTimestamp(updatedRaw)
	record.DeletedAt = nullableTimePointer(deletedRaw)
	return record, nil
}

func learningFTSMatchQuery(raw string) string {
	tokens := strings.Fields(strings.TrimSpace(raw))
	if len(tokens) == 0 {
		return ""
	}
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.Trim(token, `"*`)
		token = strings.ReplaceAll(token, `"`, `""`)
		if token == "" {
			continue
		}
		out = append(out, fmt.Sprintf(`"%s"`, token))
	}
	return strings.Join(out, " AND ")
}

func learningHasAll(values []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(values))
	for _, value := range values {
		have[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range required {
		if _, ok := have[strings.ToLower(strings.TrimSpace(value))]; !ok {
			return false
		}
	}
	return true
}

func learningActiveAt(learning Learning, now time.Time) bool {
	if learning.DeletedAt != nil {
		return false
	}
	switch learning.Status {
	case LearningStatusAccepted, LearningStatusPromoted:
	default:
		return false
	}
	if learning.ExpiresAt != nil && !learning.ExpiresAt.After(now) {
		return false
	}
	if learning.StaleAt != nil && !learning.StaleAt.After(now) {
		return false
	}
	return learning.SupersededAt == nil && learning.TargetRetiredAt == nil
}

func unmarshalJSONStringSlice(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return normalizeStringSlice(out)
}

func nullableStringPointer(raw sql.NullString) *string {
	if !raw.Valid {
		return nil
	}
	value := strings.TrimSpace(raw.String)
	if value == "" {
		return nil
	}
	return &value
}

func nullableTimePointer(raw sql.NullString) *time.Time {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	parsed := parseTimestamp(raw.String)
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	parts := make([]string, count)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ", ")
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
