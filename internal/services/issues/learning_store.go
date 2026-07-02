package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

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

type Learning struct {
	LocalID         string
	ProjectID       string
	IssueID         *string
	RequirementID   *string
	SessionID       *string
	Summary         string
	Evidence        string
	EvidencePrivate bool
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
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
}

type CreateLearningParams struct {
	ProjectID       string
	IssueID         *string
	RequirementID   *string
	SessionID       *string
	Summary         string
	Evidence        string
	EvidencePrivate bool
	Status          LearningStatus
	Tags            []string
	Files           []string
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
	ExcludePrivate  bool
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
			summary, evidence, evidence_private, status, tags_json, files_json,
			created_at, updated_at, deleted_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, fmt.Sprintf("pending-%d", now.UnixNano()), normalized.ProjectID, nullableTextPtr(normalized.IssueID), reqRowID, nullableTextPtr(normalized.SessionID), normalized.Summary, normalized.Evidence, boolInt(normalized.EvidencePrivate), string(normalized.Status), mustMarshalJSONSlice(tags), mustMarshalJSONSlice(files), formatTimestamp(now), formatTimestamp(now))
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
		LocalID:         localID,
		ProjectID:       normalized.ProjectID,
		IssueID:         cloneStringPointer(normalized.IssueID),
		RequirementID:   cloneStringPointer(normalized.RequirementID),
		SessionID:       cloneStringPointer(normalized.SessionID),
		Summary:         normalized.Summary,
		Evidence:        normalized.Evidence,
		EvidencePrivate: normalized.EvidencePrivate,
		Status:          normalized.Status,
		Tags:            tags,
		Files:           files,
		CreatedAt:       now,
		UpdatedAt:       now,
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
			l.summary, l.evidence, l.evidence_private, l.status, l.review_note, l.reviewed_at,
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
	if filter.ExcludePrivate {
		query.WriteString(` AND l.evidence_private = 0`)
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

func normalizeCreateLearningParams(params CreateLearningParams) (CreateLearningParams, error) {
	params.ProjectID = strings.TrimSpace(params.ProjectID)
	if params.ProjectID == "" {
		params.ProjectID = "default"
	}
	params.IssueID = normalizeOptionalString(params.IssueID)
	params.RequirementID = normalizeOptionalString(params.RequirementID)
	params.SessionID = normalizeOptionalString(params.SessionID)
	if hasDisallowedControlRune(params.Evidence) {
		return params, errors.New("evidence contains disallowed control characters")
	}
	if hasDisallowedControlRune(params.Summary) {
		return params, errors.New("summary contains disallowed control characters")
	}
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
			l.summary, l.evidence, l.evidence_private, l.status, l.review_note, l.reviewed_at,
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

func scanLearningRecord(scanner interface {
	Scan(dest ...any) error
}) (learningRecord, error) {
	var record learningRecord
	var issueRaw, reqRaw, sessionRaw, reviewNoteRaw, reviewedRaw, targetRaw, targetIDRaw, targetNoteRaw, promotedRaw sql.NullString
	var expiresRaw, staleRaw, lastRecalledRaw, supersededRaw, targetRetiredRaw, deletedRaw sql.NullString
	var tagsJSON, filesJSON string
	var createdRaw, updatedRaw string
	var evidencePrivateRaw int
	if err := scanner.Scan(
		&record.rowID,
		&record.LocalID,
		&record.ProjectID,
		&issueRaw,
		&reqRaw,
		&sessionRaw,
		&record.Summary,
		&record.Evidence,
		&evidencePrivateRaw,
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
	record.EvidencePrivate = evidencePrivateRaw != 0
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

func hasDisallowedControlRune(value string) bool {
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
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

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
