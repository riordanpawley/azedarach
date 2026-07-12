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
	rows, err := db.QueryContext(ctx, `SELECT local_id, summary FROM agent_learnings WHERE project_id = ? AND deleted_at IS NULL AND consolidated_into_id IS NULL ORDER BY local_id`, projectID)
	if err != nil {
		return nil, c.wrapError("suggest-learning-consolidations", "", err)
	}
	type candidate struct{ id, summary string }
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err := rows.Scan(&v.id, &v.summary); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	now := time.Now().UTC()
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			kind, score, reason, ok := classifyLearningPair(candidates[i].summary, candidates[j].summary)
			if !ok {
				continue
			}
			left, err := c.lookupLearningByLocalID(ctx, tx, candidates[i].id, false)
			if err != nil {
				return nil, err
			}
			right, err := c.lookupLearningByLocalID(ctx, tx, candidates[j].id, false)
			if err != nil {
				return nil, err
			}
			result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_learning_suggestions(local_id,project_id,kind,left_learning_id,right_learning_id,score,reason,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, "pending", projectID, kind, left.rowID, right.rowID, score, reason, LearningSuggestionPending, formatTimestamp(now), formatTimestamp(now))
			if err != nil {
				return nil, err
			}
			id, _ := result.LastInsertId()
			if id > 0 {
				localID := fmt.Sprintf("learn-sug-%d", id)
				if _, err := tx.ExecContext(ctx, `UPDATE agent_learning_suggestions SET local_id=? WHERE id=?`, localID, id); err != nil {
					return nil, err
				}
				detail, _ := json.Marshal(map[string]any{"kind": kind, "left": left.LocalID, "right": right.LocalID, "score": score, "reason": reason})
				if _, err := tx.ExecContext(ctx, `INSERT INTO agent_learning_consolidation_audit(suggestion_id,action,detail_json,created_at) VALUES(?,?,?,?)`, id, "suggested", string(detail), formatTimestamp(now)); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return c.ListLearningSuggestions(ctx, LearningSuggestionFilter{ProjectID: projectID, Status: LearningSuggestionPending})
}

func classifyLearningPair(a, b string) (LearningSuggestionKind, int, string, bool) {
	aw, an := learningMatchWords(a)
	bw, bn := learningMatchWords(b)
	if len(aw) == 0 || len(bw) == 0 {
		return "", 0, "", false
	}
	intersection := 0
	union := map[string]struct{}{}
	for w := range aw {
		union[w] = struct{}{}
		if _, ok := bw[w]; ok {
			intersection++
		}
	}
	for w := range bw {
		union[w] = struct{}{}
	}
	score := intersection * 100 / len(union)
	if score < 60 {
		return "", 0, "", false
	}
	if an != bn {
		return LearningSuggestionConflict, score, "high summary overlap with opposite negation", true
	}
	if score >= 75 {
		return LearningSuggestionDuplicate, score, "high normalized summary overlap", true
	}
	return "", 0, "", false
}

func learningMatchWords(s string) (map[string]struct{}, bool) {
	words := map[string]struct{}{}
	var b strings.Builder
	neg := false
	flush := func() {
		if b.Len() == 0 {
			return
		}
		w := b.String()
		b.Reset()
		switch w {
		case "not", "never", "avoid", "without", "mustnt", "dont", "cannot":
			neg = true
		default:
			if len([]rune(w)) > 2 {
				words[w] = struct{}{}
			}
		}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return words, neg
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
	if status != string(LearningSuggestionPending) {
		return LearningSuggestion{}, fmt.Errorf("%w: suggestion is already %s", domain.ErrConflict, status)
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
		if rec.ProjectID != project || (rec.rowID != fmt.Sprint(leftID) && rec.rowID != fmt.Sprint(rightID)) {
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
