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

	"github.com/riordanpawley/azedarach/internal/domain"
)

type LearningSuggestionKind string
type LearningSuggestionStatus string

const (
	LearningSuggestionDuplicate LearningSuggestionKind   = "duplicate"
	LearningSuggestionConflict  LearningSuggestionKind   = "conflict"
	LearningSuggestionPending   LearningSuggestionStatus = "pending"
	LearningSuggestionRejected  LearningSuggestionStatus = "rejected"
	LearningSuggestionConfirmed LearningSuggestionStatus = "confirmed"
)

type LearningSuggestion struct {
	LocalID, ProjectID              string
	Kind                            LearningSuggestionKind
	LeftLearningID, RightLearningID string
	Score                           int
	Reason                          string
	Status                          LearningSuggestionStatus
	ReviewNote, CanonicalLearningID string
	CreatedAt, UpdatedAt            time.Time
}

type LearningSuggestionFilter struct {
	ProjectID string
	ID        string
	Status    LearningSuggestionStatus
	Limit     int
}
type ConfirmLearningConsolidationParams struct{ SuggestionID, CanonicalLearningID, Summary, Note string }

func (c *Client) SuggestLearningConsolidations(ctx context.Context, projectID string) ([]LearningSuggestion, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	var out []LearningSuggestion
	err := retrySQLiteBusy(ctx, func() error {
		var err error
		out, err = c.suggestLearningConsolidationsOnce(ctx, projectID)
		return err
	})
	if err == nil {
		c.maybeMaintainSQLiteWAL(ctx)
	}
	return out, err
}

func (c *Client) suggestLearningConsolidationsOnce(ctx context.Context, projectID string) ([]LearningSuggestion, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	const seedLimit = 128
	now := time.Now().UTC()
	eligibleSQL, eligibleArgs := learningConsolidationEligibleSQL("l", now)
	var cursor string
	err = db.QueryRowContext(ctx, `SELECT cursor_local_id FROM agent_learning_consolidation_scan_state WHERE project_id=?`, projectID).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, c.wrapError("suggest-learning-consolidations", projectID, err)
	}
	query := `SELECT l.id,l.local_id,l.summary FROM agent_learnings l WHERE l.project_id=? AND l.local_id>? AND ` + eligibleSQL + ` ORDER BY l.local_id LIMIT ?`
	args := append([]any{projectID, cursor}, eligibleArgs...)
	args = append(args, seedLimit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, c.wrapError("suggest-learning-consolidations", "", err)
	}
	type candidate struct {
		rowID       int64
		id, summary string
	}
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err := rows.Scan(&v.rowID, &v.id, &v.summary); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// A cursor past the current eligible tail wraps deterministically.
	if len(candidates) == 0 && cursor != "" {
		args = append([]any{projectID, ""}, eligibleArgs...)
		args = append(args, seedLimit)
		rows, err = db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, c.wrapError("suggest-learning-consolidations", projectID, err)
		}
		for rows.Next() {
			var v candidate
			if err := rows.Scan(&v.rowID, &v.id, &v.summary); err != nil {
				rows.Close()
				return nil, err
			}
			candidates = append(candidates, v)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	type proposed struct {
		left, right candidate
		match       domain.LearningConsolidationMatch
	}
	proposals := map[string]proposed{}
	const matchesPerSeed = 12
	for _, seed := range candidates {
		terms := domain.LearningConsolidationSearchTerms(seed.summary)
		if len(terms) == 0 {
			continue
		}
		quoted := make([]string, len(terms))
		for i, term := range terms {
			quoted[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
		}
		matchEligible, matchEligibleArgs := learningConsolidationEligibleSQL("l", now)
		matchQuery := `SELECT agent_learning_search_fts.rowid,l.local_id,l.summary FROM agent_learning_search_fts JOIN agent_learnings l ON l.id=agent_learning_search_fts.rowid WHERE agent_learning_search_fts MATCH ? AND agent_learning_search_fts.rowid!=? AND l.project_id=? AND ` + matchEligible + ` ORDER BY rank,agent_learning_search_fts.rowid LIMIT ?`
		matchArgs := []any{strings.Join(quoted, " AND "), seed.rowID, projectID}
		matchArgs = append(matchArgs, matchEligibleArgs...)
		matchArgs = append(matchArgs, matchesPerSeed)
		matchRows, queryErr := db.QueryContext(ctx, matchQuery, matchArgs...)
		if queryErr != nil {
			return nil, c.wrapError("suggest-learning-consolidations", seed.id, queryErr)
		}
		for matchRows.Next() {
			var other candidate
			if err := matchRows.Scan(&other.rowID, &other.id, &other.summary); err != nil {
				matchRows.Close()
				return nil, err
			}
			left, right := seed, other
			if left.id > right.id {
				left, right = right, left
			}
			match, ok := domain.ClassifyLearningConsolidation(left.summary, right.summary)
			if ok {
				proposals[left.id+"\x00"+right.id] = proposed{left: left, right: right, match: match}
			}
		}
		if err := matchRows.Close(); err != nil {
			return nil, err
		}
	}
	ordered := make([]proposed, 0, len(proposals))
	for _, p := range proposals {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].left.id != ordered[j].left.id {
			return ordered[i].left.id < ordered[j].left.id
		}
		return ordered[i].right.id < ordered[j].right.id
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	for _, p := range ordered {
		leftEligible, leftArgs := learningConsolidationEligibleSQL("l", now)
		rightEligible, rightArgs := learningConsolidationEligibleSQL("r", now)
		insert := `INSERT OR IGNORE INTO agent_learning_suggestions(local_id,project_id,kind,left_learning_id,right_learning_id,score,reason,status,created_at,updated_at)
			SELECT ?,?,?,?,?,?,?,?,?,? FROM agent_learnings l,agent_learnings r
			WHERE l.id=? AND r.id=? AND l.project_id=? AND r.project_id=? AND ` + leftEligible + ` AND ` + rightEligible
		insertArgs := []any{"pending", projectID, p.match.Kind, p.left.rowID, p.right.rowID, p.match.Score, p.match.Reason, LearningSuggestionPending, formatTimestamp(now), formatTimestamp(now), p.left.rowID, p.right.rowID, projectID, projectID}
		insertArgs = append(insertArgs, leftArgs...)
		insertArgs = append(insertArgs, rightArgs...)
		result, err := tx.ExecContext(ctx, insert, insertArgs...)
		if err != nil {
			return nil, err
		}
		id, _ := result.LastInsertId()
		if id > 0 {
			localID := fmt.Sprintf("learn-sug-%d", id)
			if _, err := tx.ExecContext(ctx, `UPDATE agent_learning_suggestions SET local_id=? WHERE id=?`, localID, id); err != nil {
				return nil, err
			}
			detail, _ := json.Marshal(map[string]any{"kind": p.match.Kind, "left": p.left.id, "right": p.right.id, "score": p.match.Score, "reason": p.match.Reason})
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_learning_consolidation_audit(suggestion_id,action,detail_json,created_at) VALUES(?,?,?,?)`, id, "suggested", string(detail), formatTimestamp(now)); err != nil {
				return nil, err
			}
		}
	}
	if len(candidates) > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_learning_consolidation_scan_state(project_id,cursor_local_id,updated_at) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET cursor_local_id=excluded.cursor_local_id,updated_at=excluded.updated_at`, projectID, candidates[len(candidates)-1].id, formatTimestamp(now)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return c.ListLearningSuggestions(ctx, LearningSuggestionFilter{ProjectID: projectID, Status: LearningSuggestionPending})
}

func learningConsolidationEligibleSQL(alias string, now time.Time) (string, []any) {
	p := strings.TrimSpace(alias)
	if p != "" {
		p += "."
	}
	f := formatTimestamp(now.UTC())
	return p + `evidence_private=0 AND ` + p + `deleted_at IS NULL AND ` + p + `consolidated_into_id IS NULL AND ` + p + `status IN (?,?,?) AND (` + p + `expires_at IS NULL OR ` + p + `expires_at>?) AND (` + p + `stale_at IS NULL OR ` + p + `stale_at>?) AND ` + p + `superseded_at IS NULL AND ` + p + `target_retired_at IS NULL AND ` + p + `target_drifted_at IS NULL AND (` + p + `status!=? OR COALESCE(NULLIF(` + p + `target_state,''),?)=?)`, []any{string(LearningStatusCandidate), string(LearningStatusAccepted), string(LearningStatusPromoted), f, f, string(LearningStatusPromoted), string(LearningTargetStateActive), string(LearningTargetStateActive)}
}

func (c *Client) ListLearningSuggestions(ctx context.Context, filter LearningSuggestionFilter) ([]LearningSuggestion, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	query := `SELECT s.local_id,s.project_id,s.kind,l.local_id,r.local_id,s.score,s.reason,s.status,COALESCE(s.review_note,''),COALESCE(c.local_id,''),s.created_at,s.updated_at FROM agent_learning_suggestions s JOIN agent_learnings l ON l.id=s.left_learning_id JOIN agent_learnings r ON r.id=s.right_learning_id LEFT JOIN agent_learnings c ON c.id=s.canonical_learning_id WHERE s.project_id=?`
	args := []any{strings.TrimSpace(filter.ProjectID)}
	if filter.ID != "" {
		query += ` AND s.local_id=?`
		args = append(args, strings.TrimSpace(filter.ID))
	}
	if filter.Status != "" {
		query += ` AND s.status=?`
		args = append(args, string(filter.Status))
	}
	query += ` ORDER BY s.score DESC,s.kind,s.local_id LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LearningSuggestion
	for rows.Next() {
		var v LearningSuggestion
		var created, updated string
		if err := rows.Scan(&v.LocalID, &v.ProjectID, &v.Kind, &v.LeftLearningID, &v.RightLearningID, &v.Score, &v.Reason, &v.Status, &v.ReviewNote, &v.CanonicalLearningID, &created, &updated); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTimestamp(created)
		v.UpdatedAt = parseTimestamp(updated)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (c *Client) RejectLearningSuggestion(ctx context.Context, id, note string) (LearningSuggestion, error) {
	return c.resolveLearningSuggestion(ctx, id, "", note, false)
}
func (c *Client) ConfirmLearningConsolidation(ctx context.Context, p ConfirmLearningConsolidationParams) (LearningSuggestion, error) {
	return c.resolveLearningSuggestion(ctx, p.SuggestionID, p.CanonicalLearningID, p.Note, true, p.Summary)
}

func (c *Client) resolveLearningSuggestion(ctx context.Context, id, canonical, note string, confirm bool, summary ...string) (LearningSuggestion, error) {
	id = strings.TrimSpace(id)
	canonical = strings.TrimSpace(canonical)
	note = strings.TrimSpace(note)
	if id == "" || note == "" {
		return LearningSuggestion{}, errors.New("suggestion id and note are required")
	}
	if confirm && canonical == "" {
		return LearningSuggestion{}, errors.New("canonical learning id is required")
	}
	db, err := c.dbHandle()
	if err != nil {
		return LearningSuggestion{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LearningSuggestion{}, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	var rowID, leftID, rightID int64
	var project, kind, status string
	if err := tx.QueryRowContext(ctx, `SELECT id,project_id,kind,left_learning_id,right_learning_id,status FROM agent_learning_suggestions WHERE local_id=?`, id).Scan(&rowID, &project, &kind, &leftID, &rightID, &status); err != nil {
		return LearningSuggestion{}, err
	}
	var leftLocalID, rightLocalID string
	if err := tx.QueryRowContext(ctx, `SELECT l.local_id,r.local_id FROM agent_learnings l,agent_learnings r WHERE l.id=? AND r.id=?`, leftID, rightID).Scan(&leftLocalID, &rightLocalID); err != nil {
		return LearningSuggestion{}, err
	}
	if err := domain.ValidateLearningConsolidationResolution(domain.LearningConsolidationResolution{SuggestionStatus: status, Confirm: confirm, CanonicalID: canonical, MemberIDs: []string{leftLocalID, rightLocalID}, ReviewNote: note}); err != nil {
		return LearningSuggestion{}, fmt.Errorf("%w: %v", domain.ErrConflict, err)
	}
	now := time.Now().UTC()
	action := "rejected"
	newStatus := LearningSuggestionRejected
	var canonicalRow *learningRecord
	if confirm {
		action = "confirmed"
		newStatus = LearningSuggestionConfirmed
		rec, err := c.lookupLearningByLocalID(ctx, tx, canonical, false)
		if err != nil {
			return LearningSuggestion{}, err
		}
		if rec.ProjectID != project {
			return LearningSuggestion{}, fmt.Errorf("%w: canonical learning must be a suggestion member", domain.ErrConflict)
		}
		canonicalRow = &rec
		ids := []int64{leftID, rightID}
		allTags := append([]string(nil), rec.Tags...)
		allFiles := append([]string(nil), rec.Files...)
		for _, memberID := range ids {
			var consolidatedInto sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT consolidated_into_id FROM agent_learnings WHERE id=?`, memberID).Scan(&consolidatedInto); err != nil {
				return LearningSuggestion{}, err
			}
			if consolidatedInto.Valid {
				return LearningSuggestion{}, fmt.Errorf("%w: suggestion member is already consolidated", domain.ErrConflict)
			}
			member, err := c.lookupLearningByRowID(ctx, tx, memberID)
			if err != nil {
				return LearningSuggestion{}, err
			}
			snapshot, _ := json.Marshal(member.Learning)
			role := "source"
			if member.rowID == rec.rowID {
				role = "canonical"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_learning_consolidation_members(suggestion_id,learning_id,role,snapshot_json) VALUES(?,?,?,?)`, rowID, memberID, role, string(snapshot)); err != nil {
				return LearningSuggestion{}, err
			}
			allTags = append(allTags, member.Tags...)
			allFiles = append(allFiles, member.Files...)
			if member.rowID != rec.rowID {
				if _, err := tx.ExecContext(ctx, `UPDATE agent_learnings SET consolidated_into_id=?,updated_at=? WHERE id=?`, rec.rowID, formatTimestamp(now), memberID); err != nil {
					return LearningSuggestion{}, err
				}
			}
		}
		mergedSummary := rec.Summary
		if len(summary) > 0 && strings.TrimSpace(summary[0]) != "" {
			mergedSummary = strings.TrimSpace(summary[0])
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_learnings SET summary=?,tags_json=?,files_json=?,updated_at=? WHERE id=?`, mergedSummary, mustMarshalJSONSlice(normalizeStringSlice(allTags)), mustMarshalJSONSlice(normalizeStringSlice(allFiles)), formatTimestamp(now), rec.rowID); err != nil {
			return LearningSuggestion{}, err
		}
	}
	var canonicalID any
	if canonicalRow != nil {
		canonicalID = canonicalRow.rowID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_learning_suggestions SET status=?,review_note=?,canonical_learning_id=?,updated_at=? WHERE id=?`, newStatus, note, canonicalID, formatTimestamp(now), rowID); err != nil {
		return LearningSuggestion{}, err
	}
	detail, _ := json.Marshal(map[string]any{"note": note, "canonical_learning_id": canonical})
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_learning_consolidation_audit(suggestion_id,action,detail_json,created_at) VALUES(?,?,?,?)`, rowID, action, string(detail), formatTimestamp(now)); err != nil {
		return LearningSuggestion{}, err
	}
	if err := tx.Commit(); err != nil {
		return LearningSuggestion{}, err
	}
	tx = nil
	rows, err := c.ListLearningSuggestions(ctx, LearningSuggestionFilter{ProjectID: project, ID: id})
	if err != nil {
		return LearningSuggestion{}, err
	}
	for _, v := range rows {
		if v.LocalID == id {
			return v, nil
		}
	}
	return LearningSuggestion{}, sql.ErrNoRows
}

func (c *Client) lookupLearningByRowID(ctx context.Context, db sqlIssueDBTX, id int64) (learningRecord, error) {
	query := `SELECT l.id,l.local_id,l.project_id,l.issue_id,r.local_id,l.session_id,l.summary,l.evidence,l.evidence_private,l.status,l.review_note,l.reviewed_at,COALESCE(l.tags_json,'[]'),COALESCE(l.files_json,'[]'),l.promotion_target,l.promotion_target_id,l.promotion_note,l.promoted_at,l.target_state,l.target_hash,COALESCE(l.target_metadata_json,'{}'),l.expires_at,l.stale_at,l.last_recalled_at,l.recall_count,l.superseded_at,l.target_retired_at,l.target_drifted_at,l.created_at,l.updated_at,l.deleted_at FROM agent_learnings l LEFT JOIN spec_requirements r ON r.id=l.requirement_id WHERE l.id=? AND l.deleted_at IS NULL`
	return scanLearningRecord(db.QueryRowContext(ctx, query, id))
}
