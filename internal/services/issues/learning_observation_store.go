package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type LearningObservationProvenance struct{ Source, Actor, Ref string }

type CaptureLearningObservationParams struct {
	ProjectID                                            string
	IssueID, RequirementID, SessionID                    *string
	ObservedBehavior, PreferredBehavior, Outcome, Impact string
	Context                                              map[string]string
	Provenance                                           LearningObservationProvenance
	Sensitivity                                          domain.LearningSensitivity
	Tags, Files                                          []string
}

type LearningObservation struct {
	LocalID, LearningID, ObservedBehavior, PreferredBehavior, Outcome, Impact string
	Context                                                                   map[string]string
	Provenance                                                                LearningObservationProvenance
	Sensitivity                                                               domain.LearningSensitivity
	SafeFingerprint                                                           string
	DuplicateLearningIDs                                                      []string
	CreatedAt                                                                 time.Time
	Learning                                                                  Learning
}

func (c *Client) CaptureLearningObservation(ctx context.Context, params CaptureLearningObservationParams) (LearningObservation, error) {
	var out LearningObservation
	err := c.retrySQLiteBusy(ctx, func() error { var err error; out, err = c.captureLearningObservationOnce(ctx, params); return err })
	if err == nil {
		c.maybeMaintainSQLiteWAL(ctx)
	}
	return out, err
}

func (c *Client) captureLearningObservationOnce(ctx context.Context, p CaptureLearningObservationParams) (LearningObservation, error) {
	p.IssueID, p.RequirementID, p.SessionID = normalizeOptionalString(p.IssueID), normalizeOptionalString(p.RequirementID), normalizeOptionalString(p.SessionID)
	decision, err := domain.NormalizeLearningCapture(domain.LearningCaptureInput{ProjectID: p.ProjectID, ObservedBehavior: p.ObservedBehavior, PreferredBehavior: p.PreferredBehavior, Outcome: p.Outcome, Impact: p.Impact, Context: p.Context, Provenance: domain.LearningObservationProvenance{Source: p.Provenance.Source, Actor: p.Provenance.Actor, Ref: p.Provenance.Ref}, Sensitivity: p.Sensitivity, Tags: p.Tags, Files: p.Files})
	if err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", err)
	}
	p.ProjectID, p.ObservedBehavior, p.PreferredBehavior, p.Outcome, p.Impact, p.Context = decision.ProjectID, decision.ObservedBehavior, decision.PreferredBehavior, decision.Outcome, decision.Impact, decision.Context
	p.Provenance = LearningObservationProvenance{Source: decision.Provenance.Source, Actor: decision.Provenance.Actor, Ref: decision.Provenance.Ref}
	fingerprint := decision.SafeFingerprint
	db, err := c.dbHandle()
	if err != nil {
		return LearningObservation{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", err)
	}
	defer tx.Rollback()
	if err := ensureIssueExists(ctx, tx, p.IssueID); err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", err)
	}
	reqRowID, err := learningRequirementRowID(ctx, tx, p.RequirementID)
	if err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", err)
	}
	now := time.Now().UTC()
	tags, files := decision.Tags, decision.Files
	summary := decision.Summary
	private := p.Sensitivity == domain.LearningSensitivityPrivate
	normalizedLearning, err := normalizeCreateLearningParams(CreateLearningParams{ProjectID: p.ProjectID, IssueID: p.IssueID, RequirementID: p.RequirementID, SessionID: p.SessionID, Summary: summary, Evidence: p.ObservedBehavior, EvidencePrivate: private, Status: LearningStatusCandidate, Tags: tags, Files: files})
	if err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", err)
	}
	p.ProjectID, p.IssueID, p.RequirementID, p.SessionID = normalizedLearning.ProjectID, normalizedLearning.IssueID, normalizedLearning.RequirementID, normalizedLearning.SessionID
	summary, p.ObservedBehavior = normalizedLearning.Summary, normalizedLearning.Evidence
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_learnings(local_id,project_id,issue_id,requirement_id,session_id,summary,evidence,evidence_private,status,tags_json,files_json,created_at,updated_at,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)`, fmt.Sprintf("pending-%d", now.UnixNano()), p.ProjectID, nullableTextPtr(p.IssueID), reqRowID, nullableTextPtr(p.SessionID), summary, p.ObservedBehavior, boolInt(private), string(LearningStatusCandidate), mustMarshalJSONSlice(tags), mustMarshalJSONSlice(files), formatTimestamp(now), formatTimestamp(now))
	if err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", classifySQLiteConstraint(err))
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		return LearningObservation{}, err
	}
	learningID := fmt.Sprintf("learn-%d", rowID)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_learnings SET local_id=? WHERE id=?`, learningID, rowID); err != nil {
		return LearningObservation{}, err
	}
	contextJSON, err := json.Marshal(p.Context)
	if err != nil {
		return LearningObservation{}, fmt.Errorf("encode observation context: %w", err)
	}
	obsResult, err := tx.ExecContext(ctx, `INSERT INTO learning_observations(local_id,learning_id,observed_behavior,preferred_behavior,outcome,impact,context_json,provenance_source,provenance_actor,provenance_ref,sensitivity,safe_fingerprint,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("pending-%d", now.UnixNano()), rowID, p.ObservedBehavior, p.PreferredBehavior, p.Outcome, p.Impact, string(contextJSON), p.Provenance.Source, p.Provenance.Actor, p.Provenance.Ref, string(p.Sensitivity), fingerprint, formatTimestamp(now))
	if err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", classifySQLiteConstraint(err))
	}
	obsRowID, err := obsResult.LastInsertId()
	if err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", "", err)
	}
	obsID := fmt.Sprintf("learn-obs-%d", obsRowID)
	if _, err = tx.ExecContext(ctx, `UPDATE learning_observations SET local_id=? WHERE id=?`, obsID, obsRowID); err != nil {
		return LearningObservation{}, err
	}
	duplicateCandidates := []string{}
	if fingerprint != "" {
		rows, qerr := tx.QueryContext(ctx, `SELECT l.local_id FROM learning_observations o JOIN agent_learnings l ON l.id=o.learning_id WHERE o.safe_fingerprint=? AND o.id<>? AND o.sensitivity='public' AND l.evidence_private=0 AND l.deleted_at IS NULL AND l.consolidated_into_id IS NULL ORDER BY o.created_at DESC,l.local_id LIMIT ?`, fingerprint, obsRowID, domain.LearningObservationDuplicateHintLimit)
		if qerr != nil {
			return LearningObservation{}, qerr
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return LearningObservation{}, err
			}
			duplicateCandidates = append(duplicateCandidates, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return LearningObservation{}, c.wrapError("capture-learning-observation", obsID, err)
		}
		rows.Close()
	}
	duplicates := domain.LearningObservationDuplicateHints(decision, duplicateCandidates)
	if err := tx.Commit(); err != nil {
		return LearningObservation{}, c.wrapError("capture-learning-observation", obsID, err)
	}
	return LearningObservation{LocalID: obsID, LearningID: learningID, ObservedBehavior: p.ObservedBehavior, PreferredBehavior: p.PreferredBehavior, Outcome: p.Outcome, Impact: p.Impact, Context: p.Context, Provenance: p.Provenance, Sensitivity: p.Sensitivity, SafeFingerprint: fingerprint, DuplicateLearningIDs: duplicates, CreatedAt: now, Learning: Learning{LocalID: learningID, ProjectID: p.ProjectID, IssueID: cloneStringPointer(p.IssueID), RequirementID: cloneStringPointer(p.RequirementID), SessionID: cloneStringPointer(p.SessionID), Summary: summary, Evidence: p.ObservedBehavior, EvidencePrivate: private, Status: LearningStatusCandidate, Tags: tags, Files: files, CreatedAt: now, UpdatedAt: now}}, nil
}
