package issues

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const defaultIssueObservationEventLimit = 500

const legacyReviewEvidenceCloseFenceOwnerKind = "review-evidence-close-fence"

// ReviewEvidencePin identifies the exact durable evidence accepted by a
// reviewer. Terminal close supports only issue-event evidence so the pin can
// be revalidated in the same SQLite transaction as the terminal state write.
type ReviewEvidencePin struct {
	Source  string `json:"source"`
	EventID int64  `json:"event_id"`
	Seq     int64  `json:"seq,omitempty"`
	Digest  string `json:"digest"`
}

// ReviewAdmissionPin identifies the exact durable review episode inspected by
// an orchestrator before it claims review authority. A nil Evidence value is
// an explicit assertion that the episode had no complete worker evidence.
type ReviewAdmissionPin struct {
	ReviewEpochEventID int64              `json:"review_epoch_event_id"`
	Evidence           *ReviewEvidencePin `json:"evidence,omitempty"`
}

// ReviewPublicationAuthority is the complete immutable authorization carried
// from review acceptance through publication close.
type ReviewPublicationAuthority struct {
	Reviewer                       domain.ReviewerIdentity `json:"reviewer"`
	ReviewEpochEventID             int64                   `json:"review_epoch_event_id"`
	AcceptedReviewEventID          int64                   `json:"accepted_review_event_id"`
	PublicationOperationID         string                  `json:"publication_operation_id"`
	AcceptedPublicationOperationID string                  `json:"accepted_publication_operation_id"`
}

// CaptureReviewAdmissionPin reads the current review epoch and evidence
// identity in one SQLite transaction so a later lease claim can compare the
// exact inspected episode without depending on a global project revision.
func (c *Client) CaptureReviewAdmissionPin(ctx context.Context, issueID string) (ReviewAdmissionPin, error) {
	db, err := c.dbHandle()
	if err != nil {
		return ReviewAdmissionPin{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReviewAdmissionPin{}, c.wrapError("capture-review-admission", strings.TrimSpace(issueID), err)
	}
	defer tx.Rollback()
	pin, err := captureReviewAdmissionPin(ctx, tx, issueID)
	if err != nil {
		return ReviewAdmissionPin{}, c.wrapError("capture-review-admission", strings.TrimSpace(issueID), err)
	}
	if err := tx.Commit(); err != nil {
		return ReviewAdmissionPin{}, c.wrapError("capture-review-admission", strings.TrimSpace(issueID), err)
	}
	return pin, nil
}

// AppendIssueObservationEventWithReviewAdmission publishes a review side
// effect only while the exact exported admission and matching reviewer lease
// still hold in the same SQLite transaction.
func (c *Client) AppendIssueObservationEventWithReviewAdmission(ctx context.Context, issueID string, params IssueObservationEventParams, expected ReviewAdmissionPin, expectedParentID, reviewerID string) (int64, error) {
	var eventID int64
	err := c.withMutationLock(ctx, func(ctx context.Context) error {
		db, err := c.dbHandle()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return c.wrapError("append-review-outcome", strings.TrimSpace(issueID), err)
		}
		defer tx.Rollback()
		if err := validateReviewAdmissionPin(ctx, tx, issueID, expected); err != nil {
			return c.wrapError("append-review-outcome", strings.TrimSpace(issueID), err)
		}
		if err := validateReviewAdmissionParent(ctx, tx, issueID, expectedParentID); err != nil {
			return c.wrapError("append-review-outcome", strings.TrimSpace(issueID), err)
		}
		lease, err := coordinationLeaseForUpdate(ctx, tx, issueID, domain.CoordinationLeaseReview)
		if err != nil {
			return c.wrapError("append-review-outcome", strings.TrimSpace(issueID), err)
		}
		if lease == nil || lease.IsExpired(time.Now().UTC()) || !strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(reviewerID)) {
			return c.wrapError("append-review-outcome", strings.TrimSpace(issueID), fmt.Errorf("%w: matching active review lease is required", domain.ErrConflict))
		}
		eventID, err = c.insertIssueObservationEvent(ctx, tx, issueID, params)
		if err != nil {
			return c.wrapError("append-review-outcome", strings.TrimSpace(issueID), err)
		}
		if err := tx.Commit(); err != nil {
			return c.wrapError("append-review-outcome", strings.TrimSpace(issueID), err)
		}
		return nil
	})
	return eventID, err
}

func captureReviewAdmissionPin(ctx context.Context, tx *sql.Tx, issueID string) (ReviewAdmissionPin, error) {
	issueID = strings.TrimSpace(issueID)
	var disposition, engagement, visibility string
	if err := tx.QueryRowContext(ctx, `SELECT disposition,engagement,visibility FROM issues WHERE id=?`, issueID).Scan(&disposition, &engagement, &visibility); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReviewAdmissionPin{}, domain.ErrNotFound
		}
		return ReviewAdmissionPin{}, err
	}
	if disposition != string(domain.IssueDispositionReady) || engagement != string(domain.IssueEngagementReviewRequested) || visibility != string(domain.IssueVisibilityLive) {
		return ReviewAdmissionPin{}, fmt.Errorf("%w: issue is not in the requested review episode", domain.ErrConflict)
	}
	events, err := reviewAdmissionEvents(ctx, tx, issueID)
	if err != nil {
		return ReviewAdmissionPin{}, err
	}
	pin := ReviewAdmissionPin{}
	for i := len(events) - 1; i >= 0; i-- {
		if domain.IsReviewRequestTransition(events[i]) {
			pin.ReviewEpochEventID = events[i].ID
			break
		}
	}
	if pin.ReviewEpochEventID <= 0 {
		return ReviewAdmissionPin{}, errors.New("current review episode has no durable review epoch")
	}
	reduced := domain.ReduceReviewReadyEvidence(events)
	if reduced.LatestEvidence != nil && reduced.LatestEvidence.Validation.Complete {
		body, err := json.Marshal(reduced.LatestEvidence.Evidence)
		if err != nil {
			return ReviewAdmissionPin{}, fmt.Errorf("encode review admission evidence: %w", err)
		}
		pin.Evidence = &ReviewEvidencePin{
			Source:  "issue_event",
			EventID: reduced.LatestEvidence.SourceEvent.ID,
			Digest:  fmt.Sprintf("%x", sha256.Sum256(body)),
		}
	}
	return pin, nil
}

func reviewAdmissionEvents(ctx context.Context, tx *sql.Tx, issueID string) ([]domain.IssueObservationEvent, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id = ?
		  AND (
			event_type IN (?, ?)
			OR LOWER(REPLACE(REPLACE(TRIM(event_type), '_', '.'), '-', '.')) IN (?, ?, ?, ?)
		  )
		ORDER BY observed_at ASC, id ASC
	`, strings.TrimSpace(issueID),
		string(domain.IssueEventIssueCreated),
		string(domain.IssueEventIssueStatusChanged),
		string(domain.IssueEventEvidenceSubmitted),
		"worker.integration.ready",
		"worker.ready",
		"worker.complete",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 16)
	for rows.Next() {
		event, err := scanIssueObservationEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func validateReviewAdmissionPin(ctx context.Context, tx *sql.Tx, issueID string, expected ReviewAdmissionPin) error {
	if expected.ReviewEpochEventID <= 0 {
		return errors.New("review admission requires a positive review epoch event id")
	}
	current, err := captureReviewAdmissionPin(ctx, tx, issueID)
	if err != nil {
		return err
	}
	if current.ReviewEpochEventID != expected.ReviewEpochEventID {
		return fmt.Errorf("%w: review epoch changed from event %d to event %d", domain.ErrConflict, expected.ReviewEpochEventID, current.ReviewEpochEventID)
	}
	if (current.Evidence == nil) != (expected.Evidence == nil) {
		return fmt.Errorf("%w: review evidence identity changed", domain.ErrConflict)
	}
	if current.Evidence != nil && *current.Evidence != *expected.Evidence {
		return fmt.Errorf("%w: review evidence identity changed", domain.ErrConflict)
	}
	return nil
}

func reviewEvidenceCloseFenceToken(pin ReviewEvidencePin) string {
	return fmt.Sprintf("review-evidence:%d:%s", pin.EventID, strings.TrimSpace(pin.Digest))
}

func reviewAuthorityPayloadInt64(value any) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 64)
	return parsed
}

// ReviewEvidenceCloseFenceMatches reports whether lease preserves the accepted
// reviewer identity or is a legacy synthetic fence for the exact evidence pin.
// Accepted-intent replay uses the legacy match only to restore reviewer
// identity after upgrading a close that crashed under the old representation.
func ReviewEvidenceCloseFenceMatches(lease *domain.CoordinationLease, pin ReviewEvidencePin, reviewerID string) bool {
	return lease != nil && lease.Purpose == domain.CoordinationLeaseReview &&
		(strings.EqualFold(strings.TrimSpace(lease.OwnerID), strings.TrimSpace(reviewerID)) ||
			(lease.OwnerKind == legacyReviewEvidenceCloseFenceOwnerKind && lease.OwnerID == reviewEvidenceCloseFenceToken(pin)))
}

func validateReviewPublicationAuthority(ctx context.Context, tx *sql.Tx, issueID string, authority ReviewPublicationAuthority) error {
	reviewer, err := domain.CanonicalReviewerIdentity(authority.Reviewer.OwnerID, authority.Reviewer.OwnerKind)
	if err != nil || authority.ReviewEpochEventID <= 0 || authority.AcceptedReviewEventID <= 0 || strings.TrimSpace(authority.PublicationOperationID) == "" || strings.TrimSpace(authority.AcceptedPublicationOperationID) == "" {
		return fmt.Errorf("%w: complete typed publication review authority is required", domain.ErrConflict)
	}
	current, err := captureReviewAdmissionPin(ctx, tx, issueID)
	if err != nil {
		return err
	}
	if current.ReviewEpochEventID != authority.ReviewEpochEventID {
		return fmt.Errorf("%w: publication review epoch changed", domain.ErrConflict)
	}
	var event domain.IssueObservationEvent
	event, err = scanIssueObservationEvent(tx.QueryRowContext(ctx, `
		SELECT id,issue_id,event_type,observed_at,source,source_command,operation_id,session_id,worktree_path,payload_json
		FROM issue_observation_events WHERE id=? AND issue_id=?`, authority.AcceptedReviewEventID, strings.TrimSpace(issueID)))
	if err != nil {
		return fmt.Errorf("%w: accepted review event is missing", domain.ErrConflict)
	}
	outcome, trusted := domain.TrustedReviewOutcome(event)
	if !trusted || outcome != domain.ReviewOutcomeAccepted ||
		!reviewer.Matches(fmt.Sprint(event.Payload["actor_id"]), fmt.Sprint(event.Payload["actor_kind"])) ||
		reviewAuthorityPayloadInt64(event.Payload["review_epoch_event_id"]) != authority.ReviewEpochEventID ||
		strings.TrimSpace(fmt.Sprint(event.Payload["publication_operation_id"])) != strings.TrimSpace(authority.AcceptedPublicationOperationID) {
		return fmt.Errorf("%w: accepted review event authority does not match publication", domain.ErrConflict)
	}
	var operationCount int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM daemon_publication_operations
		WHERE operation_id=? AND issue_id=? AND LOWER(actor_id)=? AND actor_kind=?
		  AND review_epoch_event_id=? AND accepted_review_event_id=? AND accepted_publication_operation_id=?`,
		strings.TrimSpace(authority.PublicationOperationID), strings.TrimSpace(issueID), reviewer.OwnerID, reviewer.OwnerKind,
		authority.ReviewEpochEventID, authority.AcceptedReviewEventID, strings.TrimSpace(authority.AcceptedPublicationOperationID)).Scan(&operationCount)
	if err != nil || operationCount != 1 {
		return fmt.Errorf("%w: durable publication operation authority does not match", domain.ErrConflict)
	}
	return nil
}

// BeginReviewPublicationClose revalidates the exact review epoch, accepted
// event, publication operation, and typed lease before external apply/cleanup.
func (c *Client) BeginReviewPublicationClose(ctx context.Context, issueID string, authority ReviewPublicationAuthority, pin *ReviewEvidencePin) error {
	return c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if pin != nil {
				if err := validateTerminalReviewEvidencePin(ctx, tx, issueID, *pin); err != nil {
					return err
				}
			}
			if err := validateReviewPublicationAuthority(ctx, tx, issueID, authority); err != nil {
				return err
			}
			lease, err := coordinationLeaseForUpdate(ctx, tx, issueID, domain.CoordinationLeaseReview)
			if err != nil {
				return err
			}
			if lease == nil || lease.IsExpired(time.Now().UTC()) || !authority.Reviewer.Matches(lease.OwnerID, lease.OwnerKind) {
				return fmt.Errorf("%w: exact typed reviewer lease is required", domain.ErrConflict)
			}
			return tx.Commit()
		})
	})
}

// BeginReviewEvidenceClose verifies the durable reviewer-owned write fence for
// one accepted evidence epoch. Evidence producers cannot supersede the pin
// while external integration and resource cleanup are in flight. Legacy
// synthetic fences are atomically restored to the accepted reviewer so a
// daemon restart can recover operations created by older versions.
func (c *Client) BeginReviewEvidenceClose(ctx context.Context, issueID string, pin ReviewEvidencePin, reviewerID string) (string, error) {
	issueID = strings.TrimSpace(issueID)
	reviewerID = strings.TrimSpace(reviewerID)
	if strings.TrimSpace(pin.Source) != "issue_event" || pin.EventID <= 0 || pin.Seq != 0 || strings.TrimSpace(pin.Digest) == "" {
		return "", c.wrapError("begin-review-evidence-close", issueID, errors.New("review evidence pin must identify one durable issue event"))
	}
	if reviewerID == "" {
		return "", c.wrapError("begin-review-evidence-close", issueID, errors.New("accepted reviewer id is required"))
	}
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("begin-review-evidence-close", issueID, err)
			}
			defer tx.Rollback()
			if err := validateTerminalReviewEvidencePin(ctx, tx, issueID, pin); err != nil {
				return c.wrapError("begin-review-evidence-close", issueID, err)
			}
			if err := validateAcceptedReviewerOutcome(ctx, tx, issueID, reviewerID, &pin); err != nil {
				return c.wrapError("begin-review-evidence-close", issueID, err)
			}
			lease, err := coordinationLeaseForUpdate(ctx, tx, issueID, domain.CoordinationLeaseReview)
			if err != nil {
				return c.wrapError("begin-review-evidence-close", issueID, err)
			}
			if lease == nil || lease.IsExpired(time.Now().UTC()) {
				return c.wrapError("begin-review-evidence-close", issueID, fmt.Errorf("%w: active accepted reviewer lease is required", domain.ErrConflict))
			}
			if strings.EqualFold(strings.TrimSpace(lease.OwnerID), reviewerID) {
				// The accepted reviewer already is the durable fence. Preserve the
				// exact lease identity used by validation authorization.
			} else if lease.OwnerKind == legacyReviewEvidenceCloseFenceOwnerKind && lease.OwnerID == reviewEvidenceCloseFenceToken(pin) {
				if _, err := tx.ExecContext(ctx, `UPDATE issue_coordination_leases
					SET owner_id=?,owner_kind=?
					WHERE issue_id=? AND purpose=? AND owner_id=? AND owner_kind=?`, reviewerID, "orchestrator",
					issueID, domain.CoordinationLeaseReview, lease.OwnerID, legacyReviewEvidenceCloseFenceOwnerKind); err != nil {
					return c.wrapError("begin-review-evidence-close", issueID, err)
				}
			} else {
				return c.wrapError("begin-review-evidence-close", issueID, fmt.Errorf("%w: review lease owned by %s", domain.ErrConflict, lease.OwnerID))
			}
			if err := tx.Commit(); err != nil {
				return c.wrapError("begin-review-evidence-close", issueID, err)
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	return reviewerID, nil
}

func validateAcceptedReviewerOutcome(ctx context.Context, tx *sql.Tx, issueID, reviewerID string, pin *ReviewEvidencePin) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id=? AND event_type IN (?,?)
		ORDER BY id DESC
	`, strings.TrimSpace(issueID), domain.IssueEventReviewCompleted, domain.IssueEventIssueStatusChanged)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		event, err := scanIssueObservationEvent(rows)
		if err != nil {
			return err
		}
		if domain.IsReviewRequestTransition(event) {
			break
		}
		outcome, trusted := domain.TrustedReviewOutcome(event)
		if !trusted {
			continue
		}
		if outcome == domain.ReviewOutcomeIntegrationFailed {
			continue
		}
		if outcome != domain.ReviewOutcomeAccepted {
			return fmt.Errorf("%w: current review outcome is %s", domain.ErrConflict, outcome)
		}
		if !strings.EqualFold(strings.TrimSpace(fmt.Sprint(event.Payload["actor_id"])), strings.TrimSpace(reviewerID)) {
			return fmt.Errorf("%w: accepted review actor does not match reviewer lease", domain.ErrConflict)
		}
		if pin != nil && (strings.TrimSpace(fmt.Sprint(event.Payload["reviewed_evidence_source"])) != strings.TrimSpace(pin.Source) ||
			strings.TrimSpace(fmt.Sprint(event.Payload["reviewed_evidence_event_id"])) != strconv.FormatInt(pin.EventID, 10) ||
			strings.TrimSpace(fmt.Sprint(event.Payload["reviewed_evidence_seq"])) != strconv.FormatInt(pin.Seq, 10) ||
			strings.TrimSpace(fmt.Sprint(event.Payload["reviewed_evidence_digest"])) != strings.TrimSpace(pin.Digest)) {
			return fmt.Errorf("%w: accepted review evidence does not match close fence", domain.ErrConflict)
		}
		return nil
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: current review epoch has no matching accepted reviewer outcome", domain.ErrConflict)
}

type IssueObservationEventParams struct {
	Type          domain.IssueObservationEventType
	ObservedAt    time.Time
	Source        string
	SourceCommand string
	OperationID   string
	SessionID     string
	WorktreePath  string
	Payload       map[string]any
}

type IssueObservationEventListOptions struct {
	Types         []domain.IssueObservationEventType
	Limit         int
	NewestFirst   bool
	NewestIDFirst bool
}

type LatestIssueObservationEventOptions struct {
	IssueIDs                []string
	Type                    domain.IssueObservationEventType
	Source                  string
	SourceCommands          []string
	CommandOutcomePairs     []IssueObservationCommandOutcomePair
	RequiredPayloadTextKeys []string
	PayloadTextEquals       map[string]string
	CurrentReviewEpoch      bool
	InvalidatedByStatuses   []domain.Status
}

type IssueObservationCommandOutcomePair struct {
	SourceCommand string
	Outcomes      []string
}

type ProjectIssueObservationCapture struct {
	RecentByIssue      map[string][]domain.IssueObservationEvent
	StewardshipByIssue map[string][]domain.IssueObservationEvent
}

// ListProjectIssueObservationEvents returns the durable project event stream
// after a cursor. Each issues.Client is already scoped to one project database,
// so the global event id is a stable project watch cursor across daemon restarts.
func (c *Client) ListProjectIssueObservationEvents(ctx context.Context, afterID int64, limit int) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	if afterID < 0 {
		afterID = 0
	}
	if limit <= 0 {
		limit = defaultIssueObservationEventLimit
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE id > ?
		ORDER BY id ASC
		LIMIT ?
	`, afterID, limit)
	if err != nil {
		return nil, c.wrapError("list-project-observation-events", "", err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, min(limit, 64))
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-project-observation-events", "", scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-project-observation-events", "", err)
	}
	return events, nil
}

// GetProjectIssueObservationEventsByIDs resolves exact durable source
// positions without scanning the history between sparse positions.
func (c *Client) GetProjectIssueObservationEventsByIDs(ctx context.Context, ids []int64) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			wanted[id] = struct{}{}
		}
	}
	ordered := make([]int64, 0, len(wanted))
	for id := range wanted {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	const observationIDBatchSize = 400
	events := make([]domain.IssueObservationEvent, 0, len(ordered))
	for start := 0; start < len(ordered); start += observationIDBatchSize {
		end := min(start+observationIDBatchSize, len(ordered))
		args := make([]any, 0, end-start)
		for _, id := range ordered[start:end] {
			args = append(args, id)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
		rows, queryErr := db.QueryContext(ctx, `
			SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
			FROM issue_observation_events
			WHERE id IN (`+placeholders+`)
			ORDER BY id ASC
		`, args...)
		if queryErr != nil {
			return nil, c.wrapError("get-project-observation-events-by-id", "", queryErr)
		}
		for rows.Next() {
			event, scanErr := scanIssueObservationEvent(rows)
			if scanErr != nil {
				_ = rows.Close()
				return nil, c.wrapError("get-project-observation-events-by-id", "", scanErr)
			}
			events = append(events, event)
		}
		rowsErr := rows.Err()
		_ = rows.Close()
		if rowsErr != nil {
			return nil, c.wrapError("get-project-observation-events-by-id", "", rowsErr)
		}
	}
	return events, nil
}

func (c *Client) AppendIssueObservationEvent(ctx context.Context, issueID string, params IssueObservationEventParams) (domain.IssueObservationEvent, error) {
	var eventID int64
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("append-observation-event", issueID, err)
			}
			defer func() {
				if tx != nil {
					_ = tx.Rollback()
				}
			}()
			if err := c.requireIssueExists(ctx, tx, issueID, "append-observation-event"); err != nil {
				return err
			}
			id, err := c.insertIssueObservationEvent(ctx, tx, issueID, params)
			if err != nil {
				return c.wrapError("append-observation-event", issueID, err)
			}
			if err := tx.Commit(); err != nil {
				return c.wrapError("append-observation-event", issueID, err)
			}
			tx = nil
			eventID = id
			return nil
		})
	})
	if err != nil {
		return domain.IssueObservationEvent{}, err
	}
	event, err := c.getIssueObservationEventByID(ctx, eventID)
	if err != nil {
		return domain.IssueObservationEvent{}, c.wrapError("append-observation-event", issueID, err)
	}
	return event, nil
}

// TaskIntegrationPublicationBinding identifies one already-committed exact
// configured-base integration receipt and the publication operation that owns
// its validation and acceptance continuation.
type TaskIntegrationPublicationBinding struct {
	ProjectID              string
	SourceBranch           string
	TargetBranch           string
	TargetID               string
	BaseOID                string
	SourceOID              string
	TargetOID              string
	PublicationOperationID string
	WorktreePath           string
}

// TaskIntegrationHistoricalBinding identifies the exact legacy review and
// validation observations that authorize recovery of a configured-base
// integration created before publication operations existed.
type TaskIntegrationHistoricalBinding struct {
	ProjectID     string
	SourceBranch  string
	TargetBranch  string
	TargetID      string
	BaseOID       string
	SourceOID     string
	TargetOID     string
	BindingID     string
	Authorization domain.HistoricalPublicationAuthorization
	WorktreePath  string
}

// BindTaskIntegrationHistoricalRecovery appends one historical correction.
// Receipt and evidence revalidation share the append transaction so separate
// daemons cannot bind different evidence to the same exact integration.
func (c *Client) BindTaskIntegrationHistoricalRecovery(ctx context.Context, issueID string, binding TaskIntegrationHistoricalBinding) (bool, error) {
	issueID = strings.TrimSpace(issueID)
	binding.ProjectID = strings.TrimSpace(binding.ProjectID)
	binding.SourceBranch = strings.TrimSpace(binding.SourceBranch)
	binding.TargetBranch = strings.TrimSpace(binding.TargetBranch)
	binding.TargetID = strings.TrimSpace(binding.TargetID)
	binding.BaseOID = strings.TrimSpace(binding.BaseOID)
	binding.SourceOID = strings.TrimSpace(binding.SourceOID)
	binding.TargetOID = strings.TrimSpace(binding.TargetOID)
	binding.BindingID = strings.TrimSpace(binding.BindingID)
	binding.WorktreePath = strings.TrimSpace(binding.WorktreePath)
	if issueID == "" || binding.ProjectID == "" || binding.SourceBranch == "" || binding.TargetBranch == "" || binding.TargetID == "" ||
		binding.BaseOID == "" || binding.SourceOID == "" || binding.TargetOID == "" || binding.BindingID == "" {
		return false, errors.New("exact task integration historical binding is incomplete")
	}
	if err := binding.Authorization.Validate(); err != nil {
		return false, fmt.Errorf("invalid historical publication authorization: %w", err)
	}
	appended := false
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			defer tx.Rollback()
			if err := c.requireIssueExists(ctx, tx, issueID, "bind-task-integration-historical"); err != nil {
				return err
			}
			rows, err := tx.QueryContext(ctx, `SELECT id, source, source_command, payload_json FROM issue_observation_events WHERE issue_id=? AND event_type=? ORDER BY id DESC`, issueID, string(domain.IssueEventReviewCompleted))
			if err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			for rows.Next() {
				var id int64
				var source, sourceCommand, payloadJSON string
				if err := rows.Scan(&id, &source, &sourceCommand, &payloadJSON); err != nil {
					_ = rows.Close()
					return c.wrapError("bind-task-integration-historical", issueID, err)
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
					_ = rows.Close()
					return c.wrapError("bind-task-integration-historical", issueID, err)
				}
				if _, trusted := domain.TrustedReviewOutcome(domain.IssueObservationEvent{ID: id, Type: domain.IssueEventReviewCompleted, Source: source, SourceCommand: sourceCommand, Payload: payload}); trusted {
					_ = rows.Close()
					return c.wrapError("bind-task-integration-historical", issueID, errors.New("historical recovery cannot replace daemon-owned review publication authority"))
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			if err := rows.Close(); err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			readEvent := func(id int64, eventType domain.IssueObservationEventType) (domain.IssueObservationEvent, error) {
				var payloadJSON, source, sourceCommand string
				err := tx.QueryRowContext(ctx, `SELECT payload_json, source, source_command FROM issue_observation_events WHERE id=? AND issue_id=? AND event_type=?`, id, issueID, string(eventType)).Scan(&payloadJSON, &source, &sourceCommand)
				if errors.Is(err, sql.ErrNoRows) {
					return domain.IssueObservationEvent{}, fmt.Errorf("historical %s evidence %d is absent", eventType, id)
				}
				if err != nil {
					return domain.IssueObservationEvent{}, err
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
					return domain.IssueObservationEvent{}, err
				}
				return domain.IssueObservationEvent{ID: id, Type: eventType, Source: source, SourceCommand: sourceCommand, Payload: payload}, nil
			}
			review, err := readEvent(binding.Authorization.ReviewEventID, domain.IssueEventHistoricalReviewAccepted)
			if err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			validation, err := readEvent(binding.Authorization.ValidationEventID, domain.IssueEventHistoricalValidationCompleted)
			if err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			payloadString := func(payload map[string]any, key string) string {
				value, ok := payload[key]
				if !ok || value == nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprint(value))
			}
			if err := domain.ValidateHistoricalPublicationReviewEvidence(review, binding.BaseOID, binding.TargetOID); err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			if err := domain.ValidateHistoricalPublicationValidationEvidence(validation, binding.BaseOID, binding.TargetOID); err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			var laterReviewID int64
			err = tx.QueryRowContext(ctx, `SELECT id FROM issue_observation_events WHERE issue_id=? AND id>? AND event_type IN (?,?) ORDER BY id DESC LIMIT 1`, issueID, binding.Authorization.ReviewEventID, string(domain.IssueEventHistoricalReviewAccepted), string(domain.IssueEventHistoricalReviewReturned)).Scan(&laterReviewID)
			if err == nil {
				return c.wrapError("bind-task-integration-historical", issueID, fmt.Errorf("historical review evidence %d was superseded by review event %d", binding.Authorization.ReviewEventID, laterReviewID))
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			var laterValidationID int64
			err = tx.QueryRowContext(ctx, `SELECT id FROM issue_observation_events WHERE issue_id=? AND id>? AND event_type IN (?,?) ORDER BY id DESC LIMIT 1`, issueID, binding.Authorization.ValidationEventID, string(domain.IssueEventHistoricalValidationCompleted), string(domain.IssueEventValidationFailed)).Scan(&laterValidationID)
			if err == nil {
				return c.wrapError("bind-task-integration-historical", issueID, fmt.Errorf("historical validation evidence %d was superseded by validation event %d", binding.Authorization.ValidationEventID, laterValidationID))
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}

			var receiptID int64
			var payloadJSON, receiptSource, receiptCommand string
			err = tx.QueryRowContext(ctx, `SELECT id, source, source_command, payload_json FROM issue_observation_events
				WHERE id=? AND issue_id=? AND event_type=?`, binding.Authorization.ReceiptEventID, issueID, string(domain.IssueEventTaskIntegrationCompleted)).Scan(&receiptID, &receiptSource, &receiptCommand, &payloadJSON)
			if errors.Is(err, sql.ErrNoRows) {
				return c.wrapError("bind-task-integration-historical", issueID, errors.New("exact task integration receipt is absent"))
			}
			if err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			if receiptSource != "daemon-task-close" || receiptCommand != "integrate-before-close" {
				return c.wrapError("bind-task-integration-historical", issueID, fmt.Errorf("historical receipt %d has untrusted provenance %s/%s", receiptID, receiptSource, receiptCommand))
			}
			if binding.Authorization.ValidationEventID >= binding.Authorization.ReviewEventID || binding.Authorization.ReviewEventID >= receiptID {
				return c.wrapError("bind-task-integration-historical", issueID, fmt.Errorf("historical evidence must be validation then acceptance before integration receipt: validation=%d review=%d receipt=%d", binding.Authorization.ValidationEventID, binding.Authorization.ReviewEventID, receiptID))
			}
			var receipt map[string]any
			if err := json.Unmarshal([]byte(payloadJSON), &receipt); err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			integrated, _ := receipt["integrated"].(bool)
			configuredBase, _ := receipt["configured_base_target"].(bool)
			if !integrated || !configuredBase || payloadString(receipt, "project_id") != binding.ProjectID || payloadString(receipt, "source_branch") != binding.SourceBranch || payloadString(receipt, "target_branch") != binding.TargetBranch || payloadString(receipt, "target_id") != binding.TargetID || payloadString(receipt, "base_oid") != binding.BaseOID || payloadString(receipt, "source_oid") != binding.SourceOID || payloadString(receipt, "target_oid") != binding.TargetOID {
				return c.wrapError("bind-task-integration-historical", issueID, errors.New("pinned task integration receipt does not match exact historical revisions and target identity"))
			}
			if payloadString(receipt, "publication_operation_id") != "" {
				return c.wrapError("bind-task-integration-historical", issueID, errors.New("task integration receipt already has publication operation authority"))
			}
			if payloadString(receipt, "historical_recovery_binding_id") != "" {
				return c.wrapError("bind-task-integration-historical", issueID, errors.New("pinned original receipt is already a historical correction"))
			}

			var correctedPayloadJSON, correctedSource, correctedCommand string
			err = tx.QueryRowContext(ctx, `SELECT payload_json, source, source_command FROM issue_observation_events WHERE issue_id=? AND event_type=? AND json_extract(payload_json,'$.historical_original_receipt_event_id')=? ORDER BY id DESC LIMIT 1`, issueID, string(domain.IssueEventTaskIntegrationCompleted), receiptID).Scan(&correctedPayloadJSON, &correctedSource, &correctedCommand)
			if err == nil {
				var corrected map[string]any
				if err := json.Unmarshal([]byte(correctedPayloadJSON), &corrected); err != nil {
					return c.wrapError("bind-task-integration-historical", issueID, err)
				}
				if payloadString(corrected, "publication_operation_id") != "" {
					return c.wrapError("bind-task-integration-historical", issueID, errors.New("historical correction contains mixed modern publication authority"))
				}
				correctedIntegrated, _ := corrected["integrated"].(bool)
				correctedConfiguredBase, _ := corrected["configured_base_target"].(bool)
				if correctedSource != "daemon-task-close" || correctedCommand != "historical-integration-recovery" || !correctedIntegrated || !correctedConfiguredBase || payloadString(corrected, "project_id") != binding.ProjectID || payloadString(corrected, "source_branch") != binding.SourceBranch || payloadString(corrected, "target_branch") != binding.TargetBranch || payloadString(corrected, "target_id") != binding.TargetID || payloadString(corrected, "base_oid") != binding.BaseOID || payloadString(corrected, "source_oid") != binding.SourceOID || payloadString(corrected, "target_oid") != binding.TargetOID {
					return c.wrapError("bind-task-integration-historical", issueID, errors.New("historical correction provenance or exact integration identity is invalid"))
				}
				var correctionCount int
				err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_observation_events WHERE issue_id=? AND event_type=? AND json_extract(payload_json,'$.historical_original_receipt_event_id')=?`, issueID, string(domain.IssueEventTaskIntegrationCompleted), receiptID).Scan(&correctionCount)
				if err != nil {
					return c.wrapError("bind-task-integration-historical", issueID, err)
				}
				if correctionCount > 1 {
					return c.wrapError("bind-task-integration-historical", issueID, fmt.Errorf("original receipt %d has multiple competing historical corrections", receiptID))
				}
				if existingBindingID := payloadString(corrected, "historical_recovery_binding_id"); existingBindingID != binding.BindingID {
					return c.wrapError("bind-task-integration-historical", issueID, fmt.Errorf("original receipt %d already has competing historical authorization %s", receiptID, existingBindingID))
				}
				if payloadString(corrected, "historical_original_receipt_event_id") != strconv.FormatInt(receiptID, 10) || payloadString(corrected, "historical_review_event_id") != strconv.FormatInt(binding.Authorization.ReviewEventID, 10) || payloadString(corrected, "historical_validation_event_id") != strconv.FormatInt(binding.Authorization.ValidationEventID, 10) {
					return c.wrapError("bind-task-integration-historical", issueID, errors.New("historical correction does not match pinned authorization"))
				}
				authorizationID, parseErr := strconv.ParseInt(payloadString(corrected, "historical_authorization_event_id"), 10, 64)
				if parseErr != nil || authorizationID <= 0 {
					return c.wrapError("bind-task-integration-historical", issueID, errors.New("historical correction is missing its authorization event"))
				}
				var authorizationSource, authorizationCommand, authorizationPayloadJSON string
				err = tx.QueryRowContext(ctx, `SELECT source, source_command, payload_json FROM issue_observation_events WHERE id=? AND issue_id=? AND event_type=?`, authorizationID, issueID, string(domain.IssueEventTaskIntegrationHistoricalAuthorized)).Scan(&authorizationSource, &authorizationCommand, &authorizationPayloadJSON)
				if err != nil || authorizationSource != "daemon-task-close" || authorizationCommand != "historical-integration-authorize" {
					return c.wrapError("bind-task-integration-historical", issueID, errors.New("historical correction authorization provenance is invalid"))
				}
				var authorized map[string]any
				if err := json.Unmarshal([]byte(authorizationPayloadJSON), &authorized); err != nil {
					return c.wrapError("bind-task-integration-historical", issueID, err)
				}
				evidencePresent, _ := authorized["evidence_present"].(bool)
				attestsMissingLegacySemantics, _ := authorized["attests_missing_legacy_semantics"].(bool)
				if payloadString(authorized, "binding_id") != binding.BindingID || payloadString(authorized, "project_id") != binding.ProjectID || payloadString(authorized, "source_branch") != binding.SourceBranch || payloadString(authorized, "target_branch") != binding.TargetBranch || payloadString(authorized, "target_id") != binding.TargetID || payloadString(authorized, "base_oid") != binding.BaseOID || payloadString(authorized, "source_oid") != binding.SourceOID || payloadString(authorized, "target_oid") != binding.TargetOID || payloadString(authorized, "review_event_id") != strconv.FormatInt(binding.Authorization.ReviewEventID, 10) || payloadString(authorized, "validation_event_id") != strconv.FormatInt(binding.Authorization.ValidationEventID, 10) || payloadString(authorized, "reviewer_id") != strings.TrimSpace(binding.Authorization.ReviewerID) || payloadString(authorized, "authoritative_evidence_id") != strings.TrimSpace(binding.Authorization.AuthoritativeEvidenceID) || payloadString(authorized, "original_receipt_event_id") != strconv.FormatInt(receiptID, 10) || payloadString(authorized, "validation_class") != string(binding.Authorization.Class) || payloadString(authorized, "validation_scope") != string(binding.Authorization.Scope) || payloadString(authorized, "validation_purpose") != string(binding.Authorization.Purpose) || payloadString(authorized, "validation_execution") != string(binding.Authorization.Execution) || payloadString(authorized, "validation_override") != string(binding.Authorization.Override) || evidencePresent != binding.Authorization.EvidencePresent || attestsMissingLegacySemantics != binding.Authorization.AttestsMissingLegacySemantics {
					return c.wrapError("bind-task-integration-historical", issueID, errors.New("historical correction authorization does not match exact attestation"))
				}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}

			authorizationEventID, err := c.insertIssueObservationEvent(ctx, tx, issueID, IssueObservationEventParams{
				Type: domain.IssueEventTaskIntegrationHistoricalAuthorized, Source: "daemon-task-close", SourceCommand: "historical-integration-authorize", WorktreePath: binding.WorktreePath,
				Payload: map[string]any{
					"binding_id": binding.BindingID, "project_id": binding.ProjectID, "source_branch": binding.SourceBranch, "target_branch": binding.TargetBranch, "target_id": binding.TargetID,
					"base_oid": binding.BaseOID, "source_oid": binding.SourceOID, "target_oid": binding.TargetOID,
					"review_event_id": binding.Authorization.ReviewEventID, "validation_event_id": binding.Authorization.ValidationEventID, "original_receipt_event_id": receiptID,
					"reviewer_id": strings.TrimSpace(binding.Authorization.ReviewerID), "authoritative_evidence_id": strings.TrimSpace(binding.Authorization.AuthoritativeEvidenceID),
					"validation_class": binding.Authorization.Class, "validation_scope": binding.Authorization.Scope, "validation_purpose": binding.Authorization.Purpose,
					"validation_execution": binding.Authorization.Execution, "validation_override": binding.Authorization.Override, "evidence_present": binding.Authorization.EvidencePresent, "attests_missing_legacy_semantics": binding.Authorization.AttestsMissingLegacySemantics,
				},
			})
			if err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			_, err = c.insertIssueObservationEvent(ctx, tx, issueID, IssueObservationEventParams{
				Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "historical-integration-recovery", WorktreePath: binding.WorktreePath,
				Payload: map[string]any{
					"project_id": binding.ProjectID, "source_branch": binding.SourceBranch, "target_branch": binding.TargetBranch,
					"integrated": true, "configured_base_target": true, "target_id": binding.TargetID,
					"base_oid": binding.BaseOID, "source_oid": binding.SourceOID, "target_oid": binding.TargetOID,
					"publication_operation_id": "", "historical_recovery_binding_id": binding.BindingID,
					"historical_authorization_event_id": authorizationEventID, "historical_original_receipt_event_id": receiptID,
					"historical_review_event_id": binding.Authorization.ReviewEventID, "historical_validation_event_id": binding.Authorization.ValidationEventID,
				},
			})
			if err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			if err := tx.Commit(); err != nil {
				return c.wrapError("bind-task-integration-historical", issueID, err)
			}
			appended = true
			return nil
		})
	})
	return appended, err
}

// BindTaskIntegrationPublicationOperation appends one corrected receipt when
// the latest exact integration receipt predates its publication-operation
// binding. The read and append share one SQLite transaction so concurrent
// daemon retries converge on one append-only correction.
func (c *Client) BindTaskIntegrationPublicationOperation(ctx context.Context, issueID string, binding TaskIntegrationPublicationBinding) (bool, error) {
	issueID = strings.TrimSpace(issueID)
	binding.ProjectID = strings.TrimSpace(binding.ProjectID)
	binding.SourceBranch = strings.TrimSpace(binding.SourceBranch)
	binding.TargetBranch = strings.TrimSpace(binding.TargetBranch)
	binding.TargetID = strings.TrimSpace(binding.TargetID)
	binding.BaseOID = strings.TrimSpace(binding.BaseOID)
	binding.SourceOID = strings.TrimSpace(binding.SourceOID)
	binding.TargetOID = strings.TrimSpace(binding.TargetOID)
	binding.PublicationOperationID = strings.TrimSpace(binding.PublicationOperationID)
	binding.WorktreePath = strings.TrimSpace(binding.WorktreePath)
	if issueID == "" || binding.ProjectID == "" || binding.SourceBranch == "" || binding.TargetBranch == "" || binding.TargetID == "" ||
		binding.BaseOID == "" || binding.SourceOID == "" || binding.TargetOID == "" || binding.PublicationOperationID == "" {
		return false, errors.New("exact task integration publication binding is incomplete")
	}
	appended := false
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("bind-task-integration-publication", issueID, err)
			}
			defer tx.Rollback()
			if err := c.requireIssueExists(ctx, tx, issueID, "bind-task-integration-publication"); err != nil {
				return err
			}
			var payloadJSON, receiptSource, receiptCommand string
			err = tx.QueryRowContext(ctx, `SELECT payload_json, source, source_command FROM issue_observation_events
				WHERE issue_id=? AND event_type=?
				  AND json_extract(payload_json,'$.project_id')=?
				  AND json_extract(payload_json,'$.source_branch')=?
				  AND json_extract(payload_json,'$.target_branch')=?
				ORDER BY id DESC LIMIT 1`, issueID, string(domain.IssueEventTaskIntegrationCompleted), binding.ProjectID, binding.SourceBranch, binding.TargetBranch).Scan(&payloadJSON, &receiptSource, &receiptCommand)
			if errors.Is(err, sql.ErrNoRows) {
				return c.wrapError("bind-task-integration-publication", issueID, errors.New("exact task integration receipt is absent"))
			}
			if err != nil {
				return c.wrapError("bind-task-integration-publication", issueID, err)
			}
			if receiptSource != "daemon-task-close" || receiptCommand != "integrate-before-close" {
				return c.wrapError("bind-task-integration-publication", issueID, fmt.Errorf("task integration receipt has untrusted provenance %s/%s", receiptSource, receiptCommand))
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
				return c.wrapError("bind-task-integration-publication", issueID, fmt.Errorf("decode exact task integration receipt: %w", err))
			}
			payloadString := func(key string) string {
				value, ok := payload[key]
				if !ok || value == nil {
					return ""
				}
				return strings.TrimSpace(fmt.Sprint(value))
			}
			integrated, _ := payload["integrated"].(bool)
			configuredBase, _ := payload["configured_base_target"].(bool)
			if !integrated || !configuredBase ||
				payloadString("target_id") != binding.TargetID || payloadString("base_oid") != binding.BaseOID ||
				payloadString("source_oid") != binding.SourceOID || payloadString("target_oid") != binding.TargetOID {
				return c.wrapError("bind-task-integration-publication", issueID, errors.New("latest task integration receipt does not match exact publication revisions and target identity"))
			}
			existingOperationID := payloadString("publication_operation_id")
			if existingOperationID != "" && payloadString("historical_recovery_binding_id") != "" {
				return c.wrapError("bind-task-integration-publication", issueID, errors.New("task integration receipt contains mixed modern and historical authority"))
			}
			if existingOperationID != "" {
				if existingOperationID != binding.PublicationOperationID {
					return c.wrapError("bind-task-integration-publication", issueID, fmt.Errorf("task integration receipt is already bound to publication operation %s", existingOperationID))
				}
				return nil
			}
			_, err = c.insertIssueObservationEvent(ctx, tx, issueID, IssueObservationEventParams{
				Type: domain.IssueEventTaskIntegrationCompleted, Source: "daemon-task-close", SourceCommand: "integrate-before-close", WorktreePath: binding.WorktreePath,
				Payload: map[string]any{
					"project_id": binding.ProjectID, "source_branch": binding.SourceBranch, "target_branch": binding.TargetBranch,
					"integrated": true, "configured_base_target": true, "target_id": binding.TargetID,
					"base_oid": binding.BaseOID, "source_oid": binding.SourceOID, "target_oid": binding.TargetOID,
					"publication_operation_id": binding.PublicationOperationID,
				},
			})
			if err != nil {
				return c.wrapError("bind-task-integration-publication", issueID, err)
			}
			if err := tx.Commit(); err != nil {
				return c.wrapError("bind-task-integration-publication", issueID, err)
			}
			appended = true
			return nil
		})
	})
	return appended, err
}

// AppendAcceptedReviewAndPublication atomically records the immutable accepted
// review decision and its daemon-owned publication intent. The queue table is
// owned by the daemon-operations migration authority but shares this project
// database specifically so no accepted base-target patch can exist without its
// continuation intent (or vice versa).
// AcceptedReviewPublicationCommit identifies both sides of the atomic durable
// acceptance boundary without requiring a fallible post-commit read.
type AcceptedReviewPublicationCommit struct {
	EventID                int64
	PublicationOperationID string
}

type acceptedReviewPublicationCommitHookKey struct{}

// WithAcceptedReviewPublicationCommitHookForTest installs a deterministic
// barrier immediately after the acceptance transaction commits. Production
// callers should not use it.
func WithAcceptedReviewPublicationCommitHookForTest(ctx context.Context, hook func()) context.Context {
	return context.WithValue(ctx, acceptedReviewPublicationCommitHookKey{}, hook)
}

// AppendAcceptedReviewAndPublicationWithReviewAdmission atomically requires
// the immutable exported review episode and its reviewer lease while recording
// both the accepted outcome and publication continuation.
func (c *Client) AppendAcceptedReviewAndPublicationWithReviewAdmission(ctx context.Context, issueID string, params IssueObservationEventParams, operation domain.PublicationOperation, coalesceKey string, expected ReviewAdmissionPin, expectedParentID, reviewerID string) (AcceptedReviewPublicationCommit, error) {
	return c.appendAcceptedReviewAndPublication(ctx, issueID, params, operation, coalesceKey, &expected, expectedParentID, reviewerID)
}

func (c *Client) appendAcceptedReviewAndPublication(ctx context.Context, issueID string, params IssueObservationEventParams, operation domain.PublicationOperation, coalesceKey string, expected *ReviewAdmissionPin, expectedParentID, reviewerID string) (AcceptedReviewPublicationCommit, error) {
	if params.Type != domain.IssueEventReviewCompleted || strings.TrimSpace(fmt.Sprint(params.Payload["outcome"])) != string(domain.ReviewOutcomeAccepted) {
		return AcceptedReviewPublicationCommit{}, c.wrapError("append-accepted-review-publication", issueID, errors.New("accepted review publication requires accepted review.completed event"))
	}
	if strings.TrimSpace(operation.AcceptedPublicationOperationID) == "" {
		operation.AcceptedPublicationOperationID = strings.TrimSpace(operation.OperationID)
	}
	if err := operation.ValidatePreparedIntent(); err != nil {
		return AcceptedReviewPublicationCommit{}, c.wrapError("append-accepted-review-publication", issueID, err)
	}
	coalesceKey = strings.TrimSpace(coalesceKey)
	if coalesceKey == "" {
		return AcceptedReviewPublicationCommit{}, c.wrapError("append-accepted-review-publication", issueID, errors.New("coalesce key is required"))
	}
	var eventID int64
	var publicationOperationID string
	err := c.retrySQLiteBusy(ctx, func() error {
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("append-accepted-review-publication", issueID, err)
			}
			defer func() { _ = tx.Rollback() }()
			if err := c.requireIssueExists(ctx, tx, issueID, "append-accepted-review-publication"); err != nil {
				return err
			}
			if expected != nil {
				if err := validateReviewAdmissionPin(ctx, tx, issueID, *expected); err != nil {
					return c.wrapError("append-accepted-review-publication", issueID, err)
				}
				if err := validateReviewAdmissionParent(ctx, tx, issueID, expectedParentID); err != nil {
					return c.wrapError("append-accepted-review-publication", issueID, err)
				}
				lease, err := coordinationLeaseForUpdate(ctx, tx, issueID, domain.CoordinationLeaseReview)
				if err != nil {
					return c.wrapError("append-accepted-review-publication", issueID, err)
				}
				reviewer, identityErr := domain.CanonicalReviewerIdentity(reviewerID, domain.ReviewerOwnerKindOrchestrator)
				if identityErr != nil || lease == nil || lease.IsExpired(time.Now().UTC()) || !reviewer.Matches(lease.OwnerID, lease.OwnerKind) {
					return c.wrapError("append-accepted-review-publication", issueID, fmt.Errorf("%w: matching active review lease is required", domain.ErrConflict))
				}
				if !reviewer.Matches(operation.ActorID, operation.ActorKind) || operation.ReviewEpochEventID != expected.ReviewEpochEventID {
					return c.wrapError("append-accepted-review-publication", issueID, fmt.Errorf("%w: publication authority does not match review admission", domain.ErrConflict))
				}
			}
			now := operation.CreatedAt.UTC()
			if now.IsZero() {
				now = time.Now().UTC()
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO daemon_publication_operations(
				operation_id,project_id,issue_id,intent_key,request_fingerprint,actor_id,actor_kind,review_epoch_event_id,accepted_review_event_id,accepted_publication_operation_id,target_id,target_branch,
				source_revision,base_revision,candidate_revision,policy_version,environment_fingerprint,validation_command,
				evidence_source,evidence_event_id,evidence_seq,evidence_digest,coalesce_key,state,created_at,updated_at
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
				operation.OperationID, operation.ProjectID, operation.IssueID, operation.IntentKey, operation.RequestFingerprint,
				operation.ActorID, operation.ActorKind, operation.ReviewEpochEventID, operation.AcceptedReviewEventID, operation.AcceptedPublicationOperationID, operation.TargetID, operation.TargetBranch, operation.SourceRevision, operation.BaseRevision,
				operation.CandidateRevision, operation.PolicyVersion, operation.EnvironmentFingerprint, operation.ValidationCommand,
				operation.EvidenceSource, operation.EvidenceEventID, operation.EvidenceSeq, operation.EvidenceDigest, coalesceKey,
				string(domain.PublicationOperationQueued), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
			if err != nil {
				return c.wrapError("append-accepted-review-publication", issueID, err)
			}
			// Resolve the canonical operation after INSERT OR IGNORE. A concurrent
			// identical request may have won the coalesce key with a different
			// intent/operation ID; the accepted event must point at that durable
			// continuation rather than a row that does not exist.
			err = tx.QueryRowContext(ctx, `SELECT operation_id FROM daemon_publication_operations
				WHERE project_id=? AND (coalesce_key=? OR (issue_id=? AND intent_key=?))
				  AND issue_id=? AND request_fingerprint=? AND actor_id=? AND actor_kind=? AND review_epoch_event_id=? AND accepted_publication_operation_id=? AND target_id=? AND target_branch=?
				  AND source_revision=? AND base_revision=? AND policy_version=?
				  AND environment_fingerprint=? AND validation_command=? AND evidence_digest=?
				ORDER BY created_at,operation_id LIMIT 1`,
				operation.ProjectID, coalesceKey, operation.IssueID, operation.IntentKey,
				operation.IssueID, operation.RequestFingerprint, operation.ActorID, operation.ActorKind, operation.ReviewEpochEventID, operation.AcceptedPublicationOperationID, operation.TargetID, operation.TargetBranch,
				operation.SourceRevision, operation.BaseRevision, operation.PolicyVersion,
				operation.EnvironmentFingerprint, operation.ValidationCommand, operation.EvidenceDigest,
			).Scan(&publicationOperationID)
			if errors.Is(err, sql.ErrNoRows) {
				return c.wrapError("append-accepted-review-publication", issueID, errors.New("publication intent conflicts with an existing operation"))
			}
			if err != nil {
				return c.wrapError("append-accepted-review-publication", issueID, err)
			}
			if params.Payload == nil {
				params.Payload = make(map[string]any)
			}
			params.Payload["actor_id"] = operation.ActorID
			params.Payload["actor_kind"] = operation.ActorKind
			params.Payload["review_epoch_event_id"] = operation.ReviewEpochEventID
			params.Payload["publication_operation_id"] = publicationOperationID
			intentKey := strings.TrimSpace(fmt.Sprint(params.Payload["intent_key"]))
			fingerprint := strings.TrimSpace(fmt.Sprint(params.Payload["request_fingerprint"]))
			err = tx.QueryRowContext(ctx, `SELECT id FROM issue_observation_events
				WHERE issue_id=? AND event_type=? AND source='daemon-orchestration' AND source_command='review-accept'
				  AND json_extract(payload_json,'$.outcome')=? AND json_extract(payload_json,'$.intent_key')=?
				  AND json_extract(payload_json,'$.request_fingerprint')=?
				  AND LOWER(TRIM(json_extract(payload_json,'$.actor_id')))=LOWER(?)
				  AND json_extract(payload_json,'$.actor_kind')=?
				  AND CAST(json_extract(payload_json,'$.review_epoch_event_id') AS INTEGER)=?
				  AND json_extract(payload_json,'$.publication_operation_id')=?
				ORDER BY id DESC LIMIT 1`, issueID, string(domain.IssueEventReviewCompleted), string(domain.ReviewOutcomeAccepted), intentKey, fingerprint,
				operation.ActorID, operation.ActorKind, operation.ReviewEpochEventID, publicationOperationID).Scan(&eventID)
			if errors.Is(err, sql.ErrNoRows) {
				eventID, err = c.insertIssueObservationEvent(ctx, tx, issueID, params)
			}
			if err != nil {
				return c.wrapError("append-accepted-review-publication", issueID, err)
			}
			result, err := tx.ExecContext(ctx, `UPDATE daemon_publication_operations
				SET accepted_review_event_id=?
				WHERE operation_id=? AND actor_id=? AND actor_kind=? AND review_epoch_event_id=?
				  AND (accepted_review_event_id=0 OR accepted_review_event_id=?)`,
				eventID, publicationOperationID, operation.ActorID, operation.ActorKind, operation.ReviewEpochEventID, eventID)
			if err != nil {
				return c.wrapError("append-accepted-review-publication", issueID, err)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return c.wrapError("append-accepted-review-publication", issueID, fmt.Errorf("%w: publication operation acceptance binding changed", domain.ErrConflict))
			}
			if err := tx.Commit(); err != nil {
				return c.wrapError("append-accepted-review-publication", issueID, err)
			}
			return nil
		})
	})
	if err != nil {
		return AcceptedReviewPublicationCommit{}, err
	}
	if hook, _ := ctx.Value(acceptedReviewPublicationCommitHookKey{}).(func()); hook != nil {
		hook()
	}
	// The immutable receipt is fully determined inside the committed
	// transaction. Do not perform a caller-context read after this boundary:
	// cancellation or a transient projection read must not turn durable queue
	// ownership into an apparent acceptance failure.
	return AcceptedReviewPublicationCommit{EventID: eventID, PublicationOperationID: publicationOperationID}, nil
}

// IssueObservationMailEventExists supports idempotent filesystem-mail mirroring
// while the caller holds the mailbox's per-parent serialization lock.
func (c *Client) IssueObservationMailEventExists(ctx context.Context, issueID string, eventType domain.IssueObservationEventType, parentIssue string, sequence int64) (bool, error) {
	db, err := c.dbHandle()
	if err != nil {
		return false, err
	}
	var exists bool
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM issue_observation_events
			WHERE issue_id = ? AND event_type = ?
			  AND json_extract(payload_json, '$.mail_event.parent_issue') = ?
			  AND CAST(json_extract(payload_json, '$.mail_event.seq') AS INTEGER) = ?
		)
	`, strings.TrimSpace(issueID), strings.TrimSpace(string(eventType)), strings.TrimSpace(parentIssue), sequence).Scan(&exists)
	if err != nil {
		return false, c.wrapError("find-mail-observation-event", issueID, err)
	}
	return exists, nil
}

// ListIssueObservationMailEvents returns the durable mailbox outbox for one
// parent in mailbox sequence order. The nested mail_event is the canonical
// payload written back to JSONL after a SQLite-first crash.
func (c *Client) ListIssueObservationMailEvents(ctx context.Context, parentIssue string) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	parentIssue = strings.TrimSpace(parentIssue)
	if parentIssue == "" {
		return nil, c.wrapError("list-mail-observation-events", "", errors.New("parent issue is required"))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE (
			(source_command = 'mail.send' AND TRIM(COALESCE(json_extract(payload_json, '$.mail_delivery_id'), '')) <> '')
			OR source_command = 'mailbox.cutover'
		  )
		  AND LOWER(REPLACE(REPLACE(TRIM(event_type), '_', '-'), '.', '-')) IN (
			'worker-progress', 'worker-blocked', 'worker-integration-ready', 'worker-ready', 'worker-complete'
		  )
		  AND json_extract(payload_json, '$.mail_event.parent_issue') = ?
		  AND CAST(json_extract(payload_json, '$.mail_event.seq') AS INTEGER) > 0
		ORDER BY CAST(json_extract(payload_json, '$.mail_event.seq') AS INTEGER) ASC, id ASC
	`, parentIssue)
	if err != nil {
		return nil, c.wrapError("list-mail-observation-events", parentIssue, err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 16)
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-mail-observation-events", parentIssue, scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-mail-observation-events", parentIssue, err)
	}
	return events, nil
}

// AppendIssueObservationMailDelivery atomically reserves a daemon request ID
// and appends its outbox event. The project-wide issue-store write lock makes
// the identity check safe even when equal request IDs reach different mailbox
// parent locks in separate daemon processes.
func (c *Client) AppendIssueObservationMailDelivery(ctx context.Context, deliveryID, issueID string, params IssueObservationEventParams) (domain.IssueObservationEvent, bool, error) {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return domain.IssueObservationEvent{}, false, c.wrapError("append-mail-observation-delivery", issueID, errors.New("delivery id is required"))
	}
	payloadDeliveryID, _ := params.Payload["mail_delivery_id"].(string)
	if strings.TrimSpace(payloadDeliveryID) != deliveryID {
		return domain.IssueObservationEvent{}, false, c.wrapError("append-mail-observation-delivery", issueID, errors.New("payload mail_delivery_id does not match delivery id"))
	}
	var durable domain.IssueObservationEvent
	inserted := false
	err := c.retrySQLiteBusy(ctx, func() error {
		durable = domain.IssueObservationEvent{}
		inserted = false
		return c.withMutationLock(ctx, func(ctx context.Context) error {
			db, err := c.dbHandle()
			if err != nil {
				return err
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return c.wrapError("append-mail-observation-delivery", issueID, err)
			}
			defer tx.Rollback()
			existing, found, err := findIssueObservationMailDelivery(ctx, tx, deliveryID)
			if err != nil {
				return c.wrapError("append-mail-observation-delivery", issueID, err)
			}
			if found {
				durable = existing
				inserted = false
				return nil
			}
			if err := c.requireIssueExists(ctx, tx, issueID, "append-mail-observation-delivery"); err != nil {
				return err
			}
			id, err := c.insertIssueObservationEvent(ctx, tx, issueID, params)
			if err != nil {
				return c.wrapError("append-mail-observation-delivery", issueID, err)
			}
			durable, err = scanIssueObservationEvent(tx.QueryRowContext(ctx, `
				SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
				FROM issue_observation_events
				WHERE id = ?
			`, id))
			if err != nil {
				return c.wrapError("append-mail-observation-delivery", issueID, err)
			}
			if err := tx.Commit(); err != nil {
				return c.wrapError("append-mail-observation-delivery", issueID, err)
			}
			inserted = true
			return nil
		})
	})
	if err != nil {
		return domain.IssueObservationEvent{}, false, err
	}
	return durable, inserted, nil
}

func findIssueObservationMailDelivery(ctx context.Context, queryer sqlIssueQueryer, deliveryID string) (domain.IssueObservationEvent, bool, error) {
	event, err := scanIssueObservationEvent(queryer.QueryRowContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE source_command = 'mail.send'
		  AND json_extract(payload_json, '$.mail_delivery_id') = ?
		ORDER BY id DESC
		LIMIT 1
	`, deliveryID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IssueObservationEvent{}, false, nil
	}
	if err != nil {
		return domain.IssueObservationEvent{}, false, err
	}
	return event, true, nil
}

func (c *Client) ListIssueObservationEvents(ctx context.Context, issueID string, opts IssueObservationEventListOptions) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-observation-events", "", errors.New("issue id is required"))
	}
	exists, err := c.issueIDExistsIncludingDeleted(ctx, db, issueID)
	if err != nil {
		return nil, c.wrapError("list-observation-events", issueID, err)
	}
	if !exists {
		return nil, c.wrapError("list-observation-events", issueID, domain.ErrNotFound)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultIssueObservationEventLimit
	}
	if limit > 5000 {
		limit = 5000
	}
	args := []any{issueID}
	typeFilters := make([]string, 0, len(opts.Types))
	for _, eventType := range opts.Types {
		trimmed := strings.TrimSpace(string(eventType))
		if trimmed == "" {
			continue
		}
		typeFilters = append(typeFilters, trimmed)
		args = append(args, trimmed)
	}
	filterSQL := ""
	if len(typeFilters) > 0 {
		filterSQL = " AND event_type IN (" + strings.TrimSuffix(strings.Repeat("?,", len(typeFilters)), ",") + ")"
	}
	args = append(args, limit)
	orderBy := "id ASC"
	if opts.NewestIDFirst {
		orderBy = "id DESC"
	} else if opts.NewestFirst {
		orderBy = "observed_at DESC, id DESC"
	}
	rows, err := db.QueryContext(ctx, `
        SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
        FROM issue_observation_events
        WHERE issue_id = ?`+filterSQL+`
        ORDER BY `+orderBy+`
        LIMIT ?
    `, args...)
	if err != nil {
		return nil, c.wrapError("list-observation-events", issueID, err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 16)
	for rows.Next() {
		event, err := scanIssueObservationEvent(rows)
		if err != nil {
			return nil, c.wrapError("list-observation-events", issueID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-observation-events", issueID, err)
	}
	return events, nil
}

// ListIssueObservationEventsForIssues returns the newest bounded event slice
// for every requested issue with one SQLite query.
func (c *Client) ListIssueObservationEventsForIssues(ctx context.Context, issueIDs []string, perIssueLimit int) (map[string][]domain.IssueObservationEvent, error) {
	capture, err := c.CaptureProjectIssueObservationEvents(ctx, issueIDs, perIssueLimit, 0)
	return capture.RecentByIssue, err
}

// CaptureProjectIssueObservationEvents returns one bounded snapshot of recent
// issue evidence plus stewardship events. Stewardship classification happens
// before its per-issue limit so unrelated lifecycle noise cannot evict mail.
func (c *Client) CaptureProjectIssueObservationEvents(ctx context.Context, issueIDs []string, perIssueLimit, stewardshipPerIssueLimit int) (ProjectIssueObservationCapture, error) {
	db, err := c.dbHandle()
	if err != nil {
		return ProjectIssueObservationCapture{}, err
	}
	return c.captureProjectIssueObservationEvents(ctx, db, issueIDs, perIssueLimit, stewardshipPerIssueLimit)
}

func (c *Client) captureProjectIssueObservationEvents(ctx context.Context, q sqlIssueDBTX, issueIDs []string, perIssueLimit, stewardshipPerIssueLimit int) (ProjectIssueObservationCapture, error) {
	issueIDs = uniqueIssueIDStrings(issueIDs)
	out := ProjectIssueObservationCapture{
		RecentByIssue:      make(map[string][]domain.IssueObservationEvent, len(issueIDs)),
		StewardshipByIssue: make(map[string][]domain.IssueObservationEvent, len(issueIDs)),
	}
	if len(issueIDs) == 0 {
		return out, nil
	}
	if perIssueLimit <= 0 {
		perIssueLimit = defaultIssueObservationEventLimit
	}
	if perIssueLimit > 5000 {
		perIssueLimit = 5000
	}
	if stewardshipPerIssueLimit <= 0 {
		stewardshipPerIssueLimit = perIssueLimit
	}
	if stewardshipPerIssueLimit > 5000 {
		stewardshipPerIssueLimit = 5000
	}
	issueIDsJSON, err := json.Marshal(issueIDs)
	if err != nil {
		return ProjectIssueObservationCapture{}, c.wrapError("list-project-observation-events", "", err)
	}
	stewardshipTypesJSON, err := json.Marshal([]string{
		string(domain.IssueEventProgressRecorded), string(domain.IssueEventBlockerReported), string(domain.IssueEventEvidenceSubmitted),
		"worker-progress", "worker.progress", "worker-blocked", "worker.blocked",
		"worker-integration-ready", "worker.integration.ready", "worker-ready", "worker.ready", "worker-complete", "worker.complete",
	})
	if err != nil {
		return ProjectIssueObservationCapture{}, c.wrapError("list-project-observation-events", "", err)
	}
	rows, err := q.QueryContext(ctx, `
		WITH candidate_issues(issue_id) AS (
			SELECT DISTINCT TRIM(CAST(value AS TEXT))
			FROM json_each(?)
			WHERE type = 'text' AND TRIM(CAST(value AS TEXT)) <> ''
		), stewardship_types(event_type) AS (
			SELECT DISTINCT TRIM(CAST(value AS TEXT))
			FROM json_each(?)
			WHERE type = 'text' AND TRIM(CAST(value AS TEXT)) <> ''
		), classified AS (
			SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json,
				CASE WHEN event_type IN (SELECT event_type FROM stewardship_types) THEN 1 ELSE 0 END AS stewardship
			FROM issue_observation_events
			JOIN candidate_issues USING (issue_id)
		), ranked AS (
			SELECT *,
				ROW_NUMBER() OVER (PARTITION BY issue_id ORDER BY observed_at DESC, id DESC) AS issue_rank,
				ROW_NUMBER() OVER (PARTITION BY issue_id, stewardship ORDER BY observed_at DESC, id DESC) AS class_rank
			FROM classified
		)
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json,
			issue_rank, stewardship, class_rank
		FROM ranked
		WHERE issue_rank <= ? OR (stewardship = 1 AND class_rank <= ?)
		ORDER BY issue_id ASC, observed_at DESC, id DESC
	`, string(issueIDsJSON), string(stewardshipTypesJSON), perIssueLimit, stewardshipPerIssueLimit)
	if err != nil {
		return ProjectIssueObservationCapture{}, c.wrapError("list-project-observation-events", "", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event domain.IssueObservationEvent
		var issueID, eventType, observedRaw, payloadRaw string
		var issueRank, stewardship, classRank int
		scanErr := rows.Scan(&event.ID, &issueID, &eventType, &observedRaw, &event.Source, &event.SourceCommand, &event.OperationID, &event.SessionID, &event.WorktreePath, &payloadRaw, &issueRank, &stewardship, &classRank)
		if scanErr != nil {
			return ProjectIssueObservationCapture{}, c.wrapError("list-project-observation-events", "", scanErr)
		}
		event, scanErr = decodeIssueObservationEvent(event, issueID, eventType, observedRaw, payloadRaw)
		if scanErr != nil {
			return ProjectIssueObservationCapture{}, c.wrapError("list-project-observation-events", "", scanErr)
		}
		key := event.IssueID.String()
		if issueRank <= perIssueLimit {
			out.RecentByIssue[key] = append(out.RecentByIssue[key], event)
		}
		if stewardship == 1 && classRank <= stewardshipPerIssueLimit {
			out.StewardshipByIssue[key] = append(out.StewardshipByIssue[key], event)
		}
	}
	if err := rows.Err(); err != nil {
		return ProjectIssueObservationCapture{}, c.wrapError("list-project-observation-events", "", err)
	}
	return out, nil
}

// ListIssueReviewReadyObservationEvents returns the complete typed event set
// used to reduce review-ready publications and acceptance evidence. It is
// intentionally uncapped: callers require one authoritative decision across
// the issue's full durable history.
func (c *Client) ListIssueReviewReadyObservationEvents(ctx context.Context, issueID string) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-review-ready-observation-events", "", errors.New("issue id is required"))
	}
	exists, err := c.issueIDExistsIncludingDeleted(ctx, db, issueID)
	if err != nil {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, err)
	}
	if !exists {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, domain.ErrNotFound)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id = ?
		  AND (
			event_type = ?
			OR LOWER(REPLACE(REPLACE(TRIM(event_type), '_', '.'), '-', '.')) IN (?, ?, ?, ?)
		  )
		ORDER BY id ASC
	`, issueID,
		string(domain.IssueEventIssueStatusChanged),
		string(domain.IssueEventEvidenceSubmitted),
		"worker.integration.ready",
		"worker.ready",
		"worker.complete",
	)
	if err != nil {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 16)
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-review-ready-observation-events", issueID, scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-review-ready-observation-events", issueID, err)
	}
	return events, nil
}

// ListIssueDecisionObservationEvents returns the complete replay authority for
// material decision changes and exact-revision acknowledgements on one issue.
func (c *Client) ListIssueDecisionObservationEvents(ctx context.Context, issueID string) ([]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, c.wrapError("list-decision-observation-events", "", errors.New("issue id is required"))
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id = ? AND event_type IN (?, ?)
		ORDER BY observed_at ASC, id ASC
	`, issueID, string(domain.IssueEventDecisionChanged), string(domain.IssueEventDecisionAcknowledged))
	if err != nil {
		return nil, c.wrapError("list-decision-observation-events", issueID, err)
	}
	defer rows.Close()
	events := make([]domain.IssueObservationEvent, 0, 8)
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-decision-observation-events", issueID, scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-decision-observation-events", issueID, err)
	}
	return events, nil
}

// ListIssueDecisionObservationEventsByIssue batches orchestration snapshot
// replay for many issues so decision visibility does not add one query per
// candidate on large boards.
func (c *Client) ListIssueDecisionObservationEventsByIssue(ctx context.Context, issueIDs []string) (map[string][]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	return c.listIssueDecisionObservationEventsByIssue(ctx, db, issueIDs)
}

func (c *Client) listIssueDecisionObservationEventsByIssue(ctx context.Context, q sqlIssueDBTX, issueIDs []string) (map[string][]domain.IssueObservationEvent, error) {
	issueIDs = normalizeOrderedIDs(issueIDs)
	out := make(map[string][]domain.IssueObservationEvent, len(issueIDs))
	if len(issueIDs) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(issueIDs)
	if err != nil {
		return nil, c.wrapError("list-decision-observation-events-batch", "", err)
	}
	rows, err := q.QueryContext(ctx, `
		WITH requested(issue_id) AS (SELECT value FROM json_each(?))
		SELECT e.id, e.issue_id, e.event_type, e.observed_at, e.source, e.source_command, e.operation_id, e.session_id, e.worktree_path, e.payload_json
		FROM issue_observation_events e
		JOIN requested r ON r.issue_id = e.issue_id
		WHERE e.event_type IN (?, ?)
		ORDER BY e.issue_id, e.id ASC
	`, string(encoded), string(domain.IssueEventDecisionChanged), string(domain.IssueEventDecisionAcknowledged))
	if err != nil {
		return nil, c.wrapError("list-decision-observation-events-batch", "", err)
	}
	defer rows.Close()
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-decision-observation-events-batch", "", scanErr)
		}
		issueID := event.IssueID.String()
		out[issueID] = append(out[issueID], event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-decision-observation-events-batch", "", err)
	}
	return out, nil
}

// ListLatestIssueObservationEventsByIssue returns at most one matching event
// per issue in one SQLite query. Callers retain authority for interpreting the
// candidate; filters only keep the persistent read bounded and indexable.
func (c *Client) ListLatestIssueObservationEventsByIssue(ctx context.Context, opts LatestIssueObservationEventOptions) (map[string]domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	return c.listLatestIssueObservationEventsByIssue(ctx, db, opts)
}

func (c *Client) listLatestIssueObservationEventsByIssue(ctx context.Context, q sqlIssueDBTX, opts LatestIssueObservationEventOptions) (map[string]domain.IssueObservationEvent, error) {
	eventType := strings.TrimSpace(string(opts.Type))
	if eventType == "" {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", errors.New("event type is required"))
	}
	issueIDs := make([]string, 0, len(opts.IssueIDs))
	seenIssueIDs := make(map[string]struct{}, len(opts.IssueIDs))
	for _, issueID := range opts.IssueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" {
			continue
		}
		if _, exists := seenIssueIDs[issueID]; exists {
			continue
		}
		seenIssueIDs[issueID] = struct{}{}
		issueIDs = append(issueIDs, issueID)
	}
	if len(issueIDs) == 0 {
		return map[string]domain.IssueObservationEvent{}, nil
	}
	issueIDsJSON, err := json.Marshal(issueIDs)
	if err != nil {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", err)
	}
	clauses := []string{"event_type = ?"}
	args := []any{string(issueIDsJSON), eventType}
	if source := strings.TrimSpace(opts.Source); source != "" {
		clauses = append(clauses, "TRIM(source) = ?")
		args = append(args, source)
	}
	commands := make([]string, 0, len(opts.SourceCommands))
	for _, command := range opts.SourceCommands {
		if command = strings.TrimSpace(command); command != "" {
			commands = append(commands, command)
		}
	}
	if len(commands) > 0 {
		clauses = append(clauses, "TRIM(source_command) IN ("+strings.TrimSuffix(strings.Repeat("?,", len(commands)), ",")+")")
		for _, command := range commands {
			args = append(args, command)
		}
	}
	pairClauses := make([]string, 0, len(opts.CommandOutcomePairs))
	for _, pair := range opts.CommandOutcomePairs {
		command := strings.TrimSpace(pair.SourceCommand)
		outcomes := make([]string, 0, len(pair.Outcomes))
		for _, outcome := range pair.Outcomes {
			if outcome = strings.TrimSpace(outcome); outcome != "" {
				outcomes = append(outcomes, outcome)
			}
		}
		if command == "" || len(outcomes) == 0 {
			continue
		}
		pairClauses = append(pairClauses, "(TRIM(events.source_command) = ? AND TRIM(CAST(json_extract(events.payload_json, '$.outcome') AS TEXT)) IN ("+strings.TrimSuffix(strings.Repeat("?,", len(outcomes)), ",")+"))")
		args = append(args, command)
		for _, outcome := range outcomes {
			args = append(args, outcome)
		}
	}
	if len(pairClauses) > 0 {
		clauses = append(clauses, "("+strings.Join(pairClauses, " OR ")+")")
	}
	for _, key := range opts.RequiredPayloadTextKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		path := `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
		clauses = append(clauses, "json_type(payload_json, ?) = 'text' AND NULLIF(TRIM(CAST(json_extract(payload_json, ?) AS TEXT)), '') IS NOT NULL")
		args = append(args, path, path)
	}
	payloadEquals := make(map[string]string, len(opts.PayloadTextEquals))
	for rawKey, rawValue := range opts.PayloadTextEquals {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		value := strings.TrimSpace(rawValue)
		if existing, found := payloadEquals[key]; found && existing != value {
			return nil, c.wrapError("list-latest-observation-events-by-issue", "", fmt.Errorf("conflicting payload text equality filters for %q", key))
		}
		payloadEquals[key] = value
	}
	payloadEqualsKeys := make([]string, 0, len(payloadEquals))
	for key := range payloadEquals {
		payloadEqualsKeys = append(payloadEqualsKeys, key)
	}
	sort.Strings(payloadEqualsKeys)
	for _, key := range payloadEqualsKeys {
		value := payloadEquals[key]
		path := `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
		clauses = append(clauses, "json_type(payload_json, ?) = 'text' AND TRIM(CAST(json_extract(payload_json, ?) AS TEXT)) = ?")
		args = append(args, path, path, value)
	}
	epochStatuses := make([]string, 0, len(opts.InvalidatedByStatuses)+1)
	seenEpochStatuses := make(map[string]struct{}, len(opts.InvalidatedByStatuses)+1)
	addEpochStatus := func(status domain.Status) {
		value := strings.ToLower(strings.TrimSpace(string(status)))
		if value == "" {
			return
		}
		if _, exists := seenEpochStatuses[value]; exists {
			return
		}
		seenEpochStatuses[value] = struct{}{}
		epochStatuses = append(epochStatuses, value)
	}
	if opts.CurrentReviewEpoch {
		addEpochStatus(domain.StatusInReview)
	}
	for _, status := range opts.InvalidatedByStatuses {
		addEpochStatus(status)
	}
	if len(epochStatuses) > 0 {
		clauses = append(clauses, `NOT EXISTS (
			SELECT 1
			FROM issue_observation_events AS epoch
			WHERE epoch.issue_id = events.issue_id
			  AND epoch.id > events.id
			  AND epoch.event_type = ?
			  AND TRIM(epoch.source) = 'issue-store'
			  AND LOWER(TRIM(CAST(json_extract(epoch.payload_json, '$.to_status') AS TEXT))) IN (`+strings.TrimSuffix(strings.Repeat("?,", len(epochStatuses)), ",")+`)
		)`)
		args = append(args, string(domain.IssueEventIssueStatusChanged))
		for _, status := range epochStatuses {
			args = append(args, status)
		}
	}
	rows, err := q.QueryContext(ctx, `
		WITH candidate_issues(issue_id) AS (
			SELECT DISTINCT TRIM(CAST(value AS TEXT))
			FROM json_each(?)
			WHERE type = 'text' AND TRIM(CAST(value AS TEXT)) <> ''
		), ranked AS (
			SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json,
				ROW_NUMBER() OVER (PARTITION BY issue_id ORDER BY id DESC) AS event_rank
			FROM issue_observation_events AS events
			JOIN candidate_issues USING (issue_id)
			WHERE `+strings.Join(clauses, " AND ")+`
		)
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM ranked
		WHERE event_rank = 1
		ORDER BY issue_id ASC
	`, args...)
	if err != nil {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", err)
	}
	defer rows.Close()
	events := make(map[string]domain.IssueObservationEvent)
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-latest-observation-events-by-issue", "", scanErr)
		}
		events[event.IssueID.String()] = event
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-latest-observation-events-by-issue", "", err)
	}
	return events, nil
}

// InvestigationAcceptances reads the durable evidence needed to decide who
// has authority over each investigation's findings. The store selects the
// evidence; the domain owns its meaning.
func (c *Client) InvestigationAcceptances(ctx context.Context, tasks []domain.Task) (map[string]domain.InvestigationAcceptance, error) {
	db, err := c.dbHandle()
	if err != nil {
		return nil, err
	}
	return c.investigationAcceptances(ctx, db, tasks)
}

func (c *Client) investigationAcceptances(ctx context.Context, q sqlIssueDBTX, tasks []domain.Task) (map[string]domain.InvestigationAcceptance, error) {
	ids := make([]string, 0, len(tasks))
	tasksByID := make(map[string]domain.Task)
	for _, task := range tasks {
		if task.Type != domain.TypeInvestigation {
			continue
		}
		id := strings.TrimSpace(task.ID.String())
		if id == "" {
			continue
		}
		ids = append(ids, id)
		tasksByID[id] = task
	}
	if len(ids) == 0 {
		return map[string]domain.InvestigationAcceptance{}, nil
	}
	args := make([]any, 0, len(ids)+4)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args,
		string(domain.IssueEventInvestigationDisposition),
		string(domain.IssueEventReviewCompleted),
		string(domain.IssueEventHumanInputProvided),
		string(domain.IssueEventIssueStatusChanged),
	)
	rows, err := q.QueryContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE issue_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)
		  AND event_type IN (?,?,?,?)
		ORDER BY id ASC
	`, args...)
	if err != nil {
		return nil, c.wrapError("list-investigation-acceptances", "", err)
	}
	defer rows.Close()
	eventsByID := make(map[string][]domain.IssueObservationEvent, len(ids))
	for rows.Next() {
		event, scanErr := scanIssueObservationEvent(rows)
		if scanErr != nil {
			return nil, c.wrapError("list-investigation-acceptances", event.IssueID.String(), scanErr)
		}
		id := event.IssueID.String()
		eventsByID[id] = append(eventsByID[id], event)
	}
	if err := rows.Err(); err != nil {
		return nil, c.wrapError("list-investigation-acceptances", "", err)
	}
	out := make(map[string]domain.InvestigationAcceptance, len(ids))
	for _, id := range ids {
		out[id] = domain.EvaluateInvestigationAcceptance(tasksByID[id], eventsByID[id])
	}
	return out, nil
}

func (c *Client) appendIssueObservationEvent(ctx context.Context, execer sqlIssueDBTX, issueID string, eventType domain.IssueObservationEventType, payload map[string]any) error {
	_, err := c.insertIssueObservationEvent(ctx, execer, issueID, IssueObservationEventParams{
		Type:    eventType,
		Source:  "issue-store",
		Payload: payload,
	})
	return err
}

func (c *Client) insertIssueObservationEvent(ctx context.Context, execer sqlIssueDBTX, issueID string, params IssueObservationEventParams) (int64, error) {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return 0, errors.New("issue id is required")
	}
	eventType := strings.TrimSpace(string(params.Type))
	if eventType == "" {
		return 0, errors.New("event type is required")
	}
	observedAt := params.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	payloadJSON, err := marshalObservationPayload(params.Payload)
	if err != nil {
		return 0, err
	}
	insertSQL := `
		INSERT INTO issue_observation_events (
			issue_id,
			event_type,
			observed_at,
			source,
			source_command,
			operation_id,
			session_id,
			worktree_path,
			payload_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	args := []any{issueID, eventType, observedAt.UTC().Format(time.RFC3339Nano), strings.TrimSpace(params.Source), strings.TrimSpace(params.SourceCommand), strings.TrimSpace(params.OperationID), strings.TrimSpace(params.SessionID), strings.TrimSpace(params.WorktreePath), payloadJSON}
	if isReviewEvidenceEventType(params.Type) {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		insertSQL = `
			INSERT INTO issue_observation_events (
				issue_id,event_type,observed_at,source,source_command,operation_id,session_id,worktree_path,payload_json
			)
			SELECT ?,?,?,?,?,?,?,?,?
			WHERE NOT EXISTS (
				SELECT 1 FROM issue_coordination_leases
				WHERE issue_id=? AND purpose=? AND (expires_at IS NULL OR expires_at>?)
			)`
		args = append(args, issueID, domain.CoordinationLeaseReview, now)
	}
	result, err := execer.ExecContext(ctx, insertSQL, args...)
	if err != nil {
		return 0, err
	}
	if isReviewEvidenceEventType(params.Type) {
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected == 0 {
			var ownerKind string
			leaseErr := execer.QueryRowContext(ctx, `
				SELECT owner_kind
				FROM issue_coordination_leases
				WHERE issue_id=? AND purpose=? AND (expires_at IS NULL OR expires_at>?)
				ORDER BY claimed_at DESC
				LIMIT 1
			`, issueID, domain.CoordinationLeaseReview, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&ownerKind)
			if leaseErr == nil && ownerKind == legacyReviewEvidenceCloseFenceOwnerKind {
				return 0, fmt.Errorf("%w: accepted review evidence is fenced for authoritative close", domain.ErrConflict)
			}
			return 0, fmt.Errorf("%w: active review admission lease fences evidence replacement", domain.ErrConflict)
		}
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read observation event id: %w", err)
	}
	operation := domain.ProjectionDeltaUpsert
	if params.Type == domain.IssueEventIssueDeleted {
		operation = domain.ProjectionDeltaDelete
	}
	if !issueEventChangesIssueProjection(params.Type) {
		if _, err := appendProjectionEmptyAdvance(ctx, execer, ProjectionSourceAdvance{
			ProjectID: "default", SourceAuthority: "legacy_issue_observation", SourcePosition: fmt.Sprint(id),
			IdempotencyKey: fmt.Sprintf("issue-observation:%d", id), CommittedAt: observedAt,
		}); err != nil {
			return 0, fmt.Errorf("append issue observation empty projection advance: %w", err)
		}
		return id, nil
	}
	deltaPayload, err := c.issueProjectionDeltaPayload(ctx, execer, issueID, operation)
	if err != nil {
		return 0, fmt.Errorf("build issue projection delta: %w", err)
	}
	if _, err := appendProjectionDelta(ctx, execer, ProjectionDeltaParams{
		ProjectID:      "default",
		Kind:           domain.ProjectionKindIssue,
		Key:            issueID,
		Operation:      operation,
		IdempotencyKey: fmt.Sprintf("issue-observation:%d", id),
		Payload:        deltaPayload,
		CommittedAt:    observedAt,
	}); err != nil {
		return 0, fmt.Errorf("append issue observation projection delta: %w", err)
	}
	return id, nil
}

func isReviewEvidenceEventType(eventType domain.IssueObservationEventType) bool {
	normalized := strings.ToLower(strings.TrimSpace(string(eventType)))
	normalized = strings.NewReplacer("_", ".", "-", ".").Replace(normalized)
	switch normalized {
	case string(domain.IssueEventEvidenceSubmitted), "worker.integration.ready", "worker.ready", "worker.complete":
		return true
	default:
		return false
	}
}

func issueEventChangesIssueProjection(eventType domain.IssueObservationEventType) bool {
	switch eventType {
	case domain.IssueEventIssueCreated, domain.IssueEventIssueStatusChanged, domain.IssueEventIssueDetailsChanged,
		domain.IssueEventIssueNotesAppended, domain.IssueEventIssueDependencyAdded, domain.IssueEventIssueDependencyRemoved,
		domain.IssueEventIssueArchived, domain.IssueEventIssueUnarchived, domain.IssueEventIssueDeleted:
		return true
	default:
		return false
	}
}

func (c *Client) issueProjectionDeltaPayload(ctx context.Context, db sqlIssueDBTX, issueID string, operation domain.ProjectionDeltaOperation) ([]byte, error) {
	payload := domain.IssueProjectionDeltaPayload{
		SchemaVersion: domain.IssueProjectionDeltaSchemaVersion,
		IssueID:       strings.TrimSpace(issueID),
		Deleted:       operation == domain.ProjectionDeltaDelete,
	}
	if operation != domain.ProjectionDeltaDelete {
		tasks, err := c.queryTasks(ctx, db, `
			SELECT id,title,COALESCE(description,''),COALESCE(notes,''),COALESCE(design,''),COALESCE(acceptance,''),
				COALESCE(assignee,''),COALESCE(labels_json,'[]'),estimate,status,COALESCE(disposition,''),
				COALESCE(engagement,''),COALESCE(visibility,''),archived_at,priority,issue_type,
				COALESCE(implementations_json,'[]'),created_at,updated_at
			FROM issues WHERE id=?
		`, issueID)
		if err != nil {
			return nil, err
		}
		if len(tasks) != 1 {
			return nil, fmt.Errorf("canonical issue projection %s: %w", issueID, domain.ErrNotFound)
		}
		canonical := domain.CanonicalIssueProjectionTask(tasks[0])
		payload.Issue = &canonical
	}
	return json.Marshal(payload)
}

func (c *Client) getIssueObservationEventByID(ctx context.Context, id int64) (domain.IssueObservationEvent, error) {
	db, err := c.dbHandle()
	if err != nil {
		return domain.IssueObservationEvent{}, err
	}
	return scanIssueObservationEvent(db.QueryRowContext(ctx, `
		SELECT id, issue_id, event_type, observed_at, source, source_command, operation_id, session_id, worktree_path, payload_json
		FROM issue_observation_events
		WHERE id = ?
	`, id))
}

func (c *Client) requireIssueExists(ctx context.Context, queryer sqlIssueQueryer, issueID, operation string) error {
	exists, err := c.issueExists(ctx, queryer, strings.TrimSpace(issueID))
	if err != nil {
		return c.wrapError(operation, issueID, err)
	}
	if !exists {
		return c.wrapError(operation, issueID, domain.ErrNotFound)
	}
	return nil
}

func (c *Client) issueIDExistsIncludingDeleted(ctx context.Context, queryer sqlIssueQueryer, issueID string) (bool, error) {
	var exists bool
	if err := queryer.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM issues
			WHERE id = ?
			UNION ALL
			SELECT 1
			FROM issue_observation_events
			WHERE issue_id = ?
		)
	`, issueID, issueID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

type issueObservationEventScanner interface {
	Scan(...any) error
}

func scanIssueObservationEvent(scanner issueObservationEventScanner) (domain.IssueObservationEvent, error) {
	var event domain.IssueObservationEvent
	var issueID string
	var eventType string
	var observedRaw string
	var payloadRaw string
	if err := scanner.Scan(
		&event.ID,
		&issueID,
		&eventType,
		&observedRaw,
		&event.Source,
		&event.SourceCommand,
		&event.OperationID,
		&event.SessionID,
		&event.WorktreePath,
		&payloadRaw,
	); err != nil {
		return domain.IssueObservationEvent{}, err
	}
	return decodeIssueObservationEvent(event, issueID, eventType, observedRaw, payloadRaw)
}

func decodeIssueObservationEvent(event domain.IssueObservationEvent, issueID, eventType, observedRaw, payloadRaw string) (domain.IssueObservationEvent, error) {
	parsedIssueID, err := naming.ParseIssueID(issueID)
	if err != nil {
		return domain.IssueObservationEvent{}, fmt.Errorf("parse issue id %q: %w", issueID, err)
	}
	event.IssueID = parsedIssueID
	event.Type = domain.IssueObservationEventType(eventType)
	event.ObservedAt = parseTimestamp(observedRaw)
	payload := map[string]any{}
	if strings.TrimSpace(payloadRaw) != "" {
		if err := json.Unmarshal([]byte(payloadRaw), &payload); err != nil {
			return domain.IssueObservationEvent{}, fmt.Errorf("decode payload: %w", err)
		}
	}
	event.Payload = payload
	return event, nil
}

func marshalObservationPayload(payload map[string]any) (string, error) {
	if payload == nil {
		return "{}", nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal observation payload: %w", err)
	}
	if len(data) == 0 || string(data) == "null" {
		return "{}", nil
	}
	return string(data), nil
}
