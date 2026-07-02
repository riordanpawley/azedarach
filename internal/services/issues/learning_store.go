package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

type LearningRelationType string

const (
	LearningRelationSupersedes LearningRelationType = "supersedes"
	LearningRelationConflicts  LearningRelationType = "conflicts"
)

type LearningTargetState string

const (
	LearningTargetStateActive  LearningTargetState = "active"
	LearningTargetStateRetired LearningTargetState = "retired"
	LearningTargetStateDrifted LearningTargetState = "drifted"
	LearningTargetStateMissing LearningTargetState = "missing"
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
	TargetState     LearningTargetState
	TargetHash      string
	TargetMetadata  map[string]string
	ExpiresAt       *time.Time
	StaleAt         *time.Time
	LastRecalledAt  *time.Time
	RecallCount     int
	SupersededAt    *time.Time
	TargetRetiredAt *time.Time
	Relations       []LearningRelation
	TargetDriftedAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       *time.Time
	RecallScore     int
	RecallReason    string
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
	Target         LearningPromotionTarget
	TargetID       string
	Note           string
	TargetHash     string
	TargetMetadata map[string]string
	CreateTarget   bool

	TargetTitle          string
	TargetDescription    string
	TargetIssueID        *string
	DecisionRationale    string
	DecisionContext      string
	DecisionConsequences string
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
	ContextIssueID  string
	ContextReqID    string
	ContextTags     []string
	ContextFiles    []string
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

func (t LearningRelationType) Valid() bool {
	switch t {
	case LearningRelationSupersedes, LearningRelationConflicts:
		return true
	default:
		return false
	}
}

func (s LearningTargetState) Valid() bool {
	switch s {
	case LearningTargetStateActive, LearningTargetStateRetired, LearningTargetStateDrifted, LearningTargetStateMissing:
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
			l.summary, l.evidence, l.evidence_private, l.status, l.review_note, l.reviewed_at,
			COALESCE(l.tags_json, '[]'), COALESCE(l.files_json, '[]'),
			l.promotion_target, l.promotion_target_id, l.promotion_note, l.promoted_at,
			l.target_state, l.target_hash, COALESCE(l.target_metadata_json, '{}'),
			l.expires_at, l.stale_at, l.last_recalled_at, l.recall_count,
			l.superseded_at, l.target_retired_at, l.target_drifted_at,
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
	for _, tagKey := range normalizeLearningFilterKeys(filter.Tags) {
		query.WriteString(` AND l.id IN (SELECT learning_id FROM agent_learning_tags WHERE tag_key = ?)`)
		args = append(args, tagKey)
	}
	for _, fileKey := range normalizeLearningFilterKeys(filter.Files) {
		query.WriteString(` AND l.id IN (SELECT learning_id FROM agent_learning_files WHERE file_key = ?)`)
		args = append(args, fileKey)
	}
	if filter.ActiveOnly {
		query.WriteString(`
			AND l.deleted_at IS NULL
			AND l.status IN (?, ?)
			AND l.superseded_at IS NULL
			AND l.target_retired_at IS NULL
			AND l.target_drifted_at IS NULL
			AND (l.status != ? OR COALESCE(NULLIF(l.target_state, ''), ?) = ?)
		`)
		args = append(args, string(LearningStatusAccepted), string(LearningStatusPromoted), string(LearningStatusPromoted), string(LearningTargetStateActive), string(LearningTargetStateActive))
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
	if filter.ActiveOnly {
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
		record.Learning = learning
		records = append(records, record)
		if !filter.ActiveOnly && filter.Limit > 0 && len(records) >= filter.Limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-learnings", "", err)
	}
	rankLearningRecords(records, filter, strings.TrimSpace(filter.Query) != "")
	if filter.ActiveOnly && filter.Limit > 0 && len(records) > filter.Limit {
		records = records[:filter.Limit]
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

func rankLearningRecords(records []learningRecord, filter LearningFilter, hasQuery bool) {
	if len(records) == 0 {
		return
	}
	context := learningRecallContext{
		issueID:       strings.TrimSpace(filter.ContextIssueID),
		requirementID: strings.TrimSpace(filter.ContextReqID),
		query:         strings.TrimSpace(filter.Query),
		tags:          mergeLearningFilterKeys(filter.ContextTags, filter.Tags),
		files:         mergeLearningFilterKeys(filter.ContextFiles, filter.Files),
		hasQuery:      hasQuery,
	}
	for i := range records {
		score, reason := learningRecallRank(records[i].Learning, context)
		records[i].RecallScore = score
		records[i].RecallReason = reason
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].RecallScore != records[j].RecallScore {
			return records[i].RecallScore > records[j].RecallScore
		}
		if !records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].UpdatedAt.After(records[j].UpdatedAt)
		}
		return records[i].LocalID < records[j].LocalID
	})
}

type learningRecallContext struct {
	issueID       string
	requirementID string
	query         string
	tags          []string
	files         []string
	hasQuery      bool
}

func learningRecallRank(learning Learning, context learningRecallContext) (int, string) {
	score := 0
	reasons := make([]string, 0, 6)
	if context.issueID != "" && learning.IssueID != nil && strings.EqualFold(*learning.IssueID, context.issueID) {
		score += 1000
		reasons = append(reasons, "issue="+*learning.IssueID)
	}
	if context.requirementID != "" && learning.RequirementID != nil && strings.EqualFold(*learning.RequirementID, context.requirementID) {
		score += 900
		reasons = append(reasons, "req="+*learning.RequirementID)
	}
	if matched := matchingLearningKeys(context.files, learning.Files); len(matched) > 0 {
		score += 250 * len(matched)
		reasons = append(reasons, "file="+strings.Join(matched, ","))
	}
	if matched := matchingLearningKeys(context.tags, learning.Tags); len(matched) > 0 {
		score += 125 * len(matched)
		reasons = append(reasons, "tag="+strings.Join(matched, ","))
	}
	if context.hasQuery {
		matches := learningQueryTokenMatches(context.query, learning)
		if matches > 0 {
			score += 75 * matches
			reasons = append(reasons, "query")
		}
	}
	switch learning.Status {
	case LearningStatusPromoted:
		score += 100
		if learning.TargetState == "" || learning.TargetState == LearningTargetStateActive {
			reasons = append(reasons, "active-target")
		}
	case LearningStatusAccepted:
		score += 50
	}
	score += learningRecencyScore(learning.UpdatedAt)
	if len(reasons) == 0 {
		reasons = append(reasons, "recent")
	}
	return score, strings.Join(reasons, "; ")
}

func matchingLearningKeys(want, got []string) []string {
	if len(want) == 0 || len(got) == 0 {
		return nil
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, key := range normalizeLearningFilterKeys(got) {
		gotSet[key] = struct{}{}
	}
	matched := make([]string, 0, len(want))
	for _, key := range want {
		if _, ok := gotSet[key]; ok {
			matched = append(matched, key)
		}
	}
	return matched
}

func mergeLearningFilterKeys(groups ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, group := range groups {
		for _, key := range normalizeLearningFilterKeys(group) {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func learningQueryTokenMatches(query string, learning Learning) int {
	tokens := normalizeLearningFilterKeys(strings.Fields(query))
	if len(tokens) == 0 {
		return 0
	}
	haystack := strings.ToLower(strings.Join([]string{
		learning.LocalID,
		learning.Summary,
		strings.Join(learning.Tags, " "),
		strings.Join(learning.Files, " "),
	}, " "))
	matches := 0
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			matches++
		}
	}
	return matches
}

func learningRecencyScore(updatedAt time.Time) int {
	if updatedAt.IsZero() {
		return 0
	}
	age := time.Since(updatedAt)
	switch {
	case age < 0:
		return 40
	case age <= 24*time.Hour:
		return 40
	case age <= 7*24*time.Hour:
		return 25
	case age <= 30*24*time.Hour:
		return 10
	default:
		return 0
	}
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
	params.TargetHash = strings.TrimSpace(params.TargetHash)
	params.TargetMetadata = normalizeStringMap(params.TargetMetadata)
	params.TargetTitle = strings.TrimSpace(params.TargetTitle)
	params.TargetDescription = strings.TrimSpace(params.TargetDescription)
	params.TargetIssueID = normalizeOptionalString(params.TargetIssueID)
	params.DecisionRationale = strings.TrimSpace(params.DecisionRationale)
	params.DecisionContext = strings.TrimSpace(params.DecisionContext)
	params.DecisionConsequences = strings.TrimSpace(params.DecisionConsequences)
	if !params.Target.Valid() {
		return Learning{}, c.wrapError("promote-learning", selector, errors.New("invalid promotion target"))
	}
	if params.TargetID == "" && (!params.CreateTarget || params.Target != LearningPromotionTargetDecision) {
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
	targetID, err := c.prepareLearningPromotionTarget(ctx, record.Learning, params)
	if err != nil {
		return Learning{}, c.wrapError("promote-learning", record.LocalID, err)
	}
	params.TargetID = targetID
	if err := c.validateLearningPromotionTarget(ctx, db, params); err != nil {
		return Learning{}, c.wrapError("promote-learning", record.LocalID, err)
	}
	now := time.Now().UTC()
	targetMetadata := params.TargetMetadata
	if targetMetadata == nil {
		targetMetadata = map[string]string{}
	}
	metadataJSON, err := json.Marshal(targetMetadata)
	if err != nil {
		return Learning{}, c.wrapError("promote-learning", record.LocalID, fmt.Errorf("marshal target metadata: %w", err))
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE agent_learnings
		SET status = ?, promotion_target = ?, promotion_target_id = ?, promotion_note = ?, promoted_at = ?,
			target_state = ?, target_hash = ?, target_metadata_json = ?, target_retired_at = NULL,
			target_drifted_at = NULL, updated_at = ?
		WHERE id = ?
	`, string(LearningStatusPromoted), string(params.Target), params.TargetID, nullableString(params.Note), formatTimestamp(now), string(LearningTargetStateActive), nullableString(params.TargetHash), string(metadataJSON), formatTimestamp(now), record.rowID); err != nil {
		return Learning{}, c.wrapError("promote-learning", record.LocalID, classifySQLiteConstraint(err))
	}
	return c.GetLearning(ctx, record.LocalID)
}

func (c *Client) RetireLearningTarget(ctx context.Context, selector string, note string) (Learning, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return Learning{}, c.wrapError("retire-learning-target", selector, errors.New("retirement note is required"))
	}
	db, err := c.dbHandle()
	if err != nil {
		return Learning{}, err
	}
	record, err := c.lookupLearningByLocalID(ctx, db, selector, false)
	if err != nil {
		return Learning{}, c.wrapError("retire-learning-target", selector, err)
	}
	if record.Status != LearningStatusPromoted || record.Target == nil || record.TargetID == "" {
		return Learning{}, c.wrapError("retire-learning-target", record.LocalID, fmt.Errorf("%w: learning has no promoted target", domain.ErrConflict))
	}
	if err := c.retireStructuredLearningTarget(ctx, record.Learning); err != nil {
		return Learning{}, c.wrapError("retire-learning-target", record.LocalID, err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `
		UPDATE agent_learnings
		SET target_state = ?, target_retired_at = ?, promotion_note = ?, updated_at = ?
		WHERE id = ?
	`, string(LearningTargetStateRetired), formatTimestamp(now), nullableString(note), formatTimestamp(now), record.rowID); err != nil {
		return Learning{}, c.wrapError("retire-learning-target", record.LocalID, classifySQLiteConstraint(err))
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

func (c *Client) prepareLearningPromotionTarget(ctx context.Context, learning Learning, params PromoteLearningParams) (string, error) {
	switch params.Target {
	case LearningPromotionTargetDecision:
		return c.prepareDecisionPromotionTarget(ctx, learning, params)
	case LearningPromotionTargetSpec:
		return c.prepareSpecPromotionTarget(ctx, learning, params)
	default:
		return params.TargetID, nil
	}
}

func (c *Client) prepareDecisionPromotionTarget(ctx context.Context, learning Learning, params PromoteLearningParams) (string, error) {
	targetID := params.TargetID
	if targetID == "" && learning.Target != nil && *learning.Target == LearningPromotionTargetDecision {
		targetID = learning.TargetID
	}
	auditCtx := WithSpecAuditActorSource(ctx, "learn.promote")
	if targetID == "" {
		if !params.CreateTarget {
			return "", errors.New("promotion target id is required")
		}
		if params.TargetTitle == "" || params.DecisionRationale == "" {
			return "", errors.New("decision target creation requires target title and decision rationale")
		}
		decision, err := c.RecordDecision(auditCtx, RecordDecisionParams{
			Title:        params.TargetTitle,
			Rationale:    params.DecisionRationale,
			Context:      params.DecisionContext,
			Consequences: params.DecisionConsequences,
		})
		if err != nil {
			return "", err
		}
		targetID = decision.LocalID
	} else {
		if _, err := c.GetDecision(ctx, targetID); err != nil {
			return "", err
		}
		if decisionPromotionHasUpdates(params) {
			update := UpdateDecisionParams{}
			if params.TargetTitle != "" {
				update.Title = &params.TargetTitle
			}
			if params.DecisionRationale != "" {
				update.Rationale = &params.DecisionRationale
			}
			if params.DecisionContext != "" {
				update.Context = &params.DecisionContext
			}
			if params.DecisionConsequences != "" {
				update.Consequences = &params.DecisionConsequences
			}
			if _, err := c.UpdateDecision(auditCtx, targetID, update); err != nil {
				return "", err
			}
		}
	}
	if learning.IssueID != nil {
		if _, err := c.AddDecisionLink(auditCtx, AddDecisionLinkParams{
			DecisionID: targetID,
			TargetKind: DecisionTargetIssue,
			TargetID:   *learning.IssueID,
			Relation:   DecisionRelationAppliesTo,
			Note:       learningPromotionAuditNote(learning.LocalID, params.Note),
		}); err != nil {
			return "", err
		}
	}
	if learning.RequirementID != nil {
		if _, err := c.AddDecisionLink(auditCtx, AddDecisionLinkParams{
			DecisionID: targetID,
			TargetKind: DecisionTargetRequirement,
			TargetID:   *learning.RequirementID,
			Relation:   DecisionRelationInforms,
			Note:       learningPromotionAuditNote(learning.LocalID, params.Note),
		}); err != nil {
			return "", err
		}
	}
	return targetID, nil
}

func (c *Client) prepareSpecPromotionTarget(ctx context.Context, learning Learning, params PromoteLearningParams) (string, error) {
	if params.TargetID == "" {
		return "", errors.New("spec target id is required")
	}
	auditCtx := WithSpecAuditActorSource(ctx, "learn.promote")
	_, err := c.GetRequirement(ctx, params.TargetID)
	if errors.Is(err, domain.ErrNotFound) && params.CreateTarget {
		if params.TargetTitle == "" {
			return "", errors.New("spec target creation requires target title")
		}
		issueID := params.TargetIssueID
		if issueID == nil {
			issueID = learning.IssueID
		}
		if _, err := c.CreateRequirement(auditCtx, CreateRequirementParams{
			LocalID:     params.TargetID,
			Title:       params.TargetTitle,
			Description: params.TargetDescription,
			IssueID:     cloneStringPointer(issueID),
			Status:      RequirementStatusOpen,
		}); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if specPromotionHasUpdates(params) {
		update := UpdateRequirementParams{}
		if params.TargetTitle != "" {
			update.Title = &params.TargetTitle
		}
		if params.TargetDescription != "" {
			update.Description = &params.TargetDescription
		}
		if params.TargetIssueID != nil {
			update.IssueID = params.TargetIssueID
		}
		if _, err := c.UpdateRequirement(auditCtx, params.TargetID, update); err != nil {
			return "", err
		}
	}
	linkIssueID := params.TargetIssueID
	if linkIssueID == nil {
		linkIssueID = learning.IssueID
	}
	if linkIssueID != nil {
		if _, err := c.AddSpecLink(auditCtx, AddSpecLinkParams{
			IssueID:       *linkIssueID,
			RequirementID: params.TargetID,
			Role:          LinkRoleImplements,
			Note:          learningPromotionAuditNote(learning.LocalID, params.Note),
		}); err != nil {
			return "", err
		}
	}
	return params.TargetID, nil
}

func (c *Client) retireStructuredLearningTarget(ctx context.Context, learning Learning) error {
	if learning.Target == nil {
		return nil
	}
	auditCtx := WithSpecAuditActorSource(ctx, "learn.retire")
	switch *learning.Target {
	case LearningPromotionTargetDecision:
		_, err := c.GetDecision(ctx, learning.TargetID)
		return err
	case LearningPromotionTargetSpec:
		req, err := c.GetRequirement(ctx, learning.TargetID)
		if err != nil {
			return err
		}
		if req.Status == RequirementStatusSuperseded {
			return nil
		}
		status := RequirementStatusSuperseded
		_, err = c.UpdateRequirement(auditCtx, learning.TargetID, UpdateRequirementParams{Status: &status})
		return err
	default:
		return nil
	}
}

func decisionPromotionHasUpdates(params PromoteLearningParams) bool {
	return params.TargetTitle != "" ||
		params.DecisionRationale != "" ||
		params.DecisionContext != "" ||
		params.DecisionConsequences != ""
}

func specPromotionHasUpdates(params PromoteLearningParams) bool {
	return params.TargetTitle != "" || params.TargetDescription != "" || params.TargetIssueID != nil
}

func learningPromotionAuditNote(learningID, note string) *string {
	parts := []string{"promoted from learning " + strings.TrimSpace(learningID)}
	if trimmed := strings.TrimSpace(note); trimmed != "" {
		parts = append(parts, trimmed)
	}
	out := strings.Join(parts, ": ")
	return &out
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
			l.target_state, l.target_hash, COALESCE(l.target_metadata_json, '{}'),
			l.expires_at, l.stale_at, l.last_recalled_at, l.recall_count,
			l.superseded_at, l.target_retired_at, l.target_drifted_at,
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
	var targetStateRaw, targetHashRaw, expiresRaw, staleRaw, lastRecalledRaw, supersededRaw, targetRetiredRaw, targetDriftedRaw, deletedRaw sql.NullString
	var tagsJSON, filesJSON, targetMetadataJSON string
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
		&targetStateRaw,
		&targetHashRaw,
		&targetMetadataJSON,
		&expiresRaw,
		&staleRaw,
		&lastRecalledRaw,
		&record.RecallCount,
		&supersededRaw,
		&targetRetiredRaw,
		&targetDriftedRaw,
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
	if targetStateRaw.Valid && strings.TrimSpace(targetStateRaw.String) != "" {
		record.TargetState = LearningTargetState(strings.TrimSpace(targetStateRaw.String))
	}
	record.TargetHash = strings.TrimSpace(targetHashRaw.String)
	record.TargetMetadata = unmarshalJSONStringMap(targetMetadataJSON)
	record.ExpiresAt = nullableTimePointer(expiresRaw)
	record.StaleAt = nullableTimePointer(staleRaw)
	record.LastRecalledAt = nullableTimePointer(lastRecalledRaw)
	record.SupersededAt = nullableTimePointer(supersededRaw)
	record.TargetRetiredAt = nullableTimePointer(targetRetiredRaw)
	record.TargetDriftedAt = nullableTimePointer(targetDriftedRaw)
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

func normalizeLearningFilterKeys(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	if learning.SupersededAt != nil || learning.TargetRetiredAt != nil || learning.TargetDriftedAt != nil {
		return false
	}
	if learning.Status == LearningStatusPromoted {
		switch learning.TargetState {
		case "", LearningTargetStateActive:
			return true
		case LearningTargetStateRetired, LearningTargetStateDrifted, LearningTargetStateMissing:
			return false
		default:
			return false
		}
	}
	return true
}

func unmarshalJSONStringSlice(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return normalizeStringSlice(out)
}

func unmarshalJSONStringMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return normalizeStringMap(out)
}

func normalizeStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
