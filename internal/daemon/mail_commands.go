package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"golang.org/x/sys/unix"
)

type daemonMailEvent struct {
	Seq         int64                  `json:"seq"`
	ParentIssue string                 `json:"parent_issue"`
	IssueID     string                 `json:"issue_id,omitempty"`
	Type        string                 `json:"type"`
	From        string                 `json:"from,omitempty"`
	To          string                 `json:"to,omitempty"`
	Body        string                 `json:"body"`
	CreatedAt   time.Time              `json:"created_at"`
	Payload     map[string]interface{} `json:"payload,omitempty"`
}

func (d *Daemon) handleMailSend(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.MailSendCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	repoDir := strings.TrimSpace(cmd.RepoDir)
	parentIssue := strings.TrimSpace(cmd.ParentIssue)
	eventType := strings.TrimSpace(cmd.Type)
	if repoDir == "" || parentIssue == "" || eventType == "" || strings.TrimSpace(cmd.Body) == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: repo_dir/parent_issue/type/body"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon mail send requested",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"issue_id", strings.TrimSpace(cmd.IssueID.String()),
			"type", eventType,
			"body_bytes", len(cmd.Body),
		)
	}
	if msg := invalidWorkerEvidenceMessage(cmd.Body); msg != "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, msg), nil
	}
	unlock, err := lockMailbox(repoDir, parentIssue)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("lock mailbox: %v", err)), nil
	}
	defer unlock()

	if err := repairTrailingMailboxFragment(repoDir, parentIssue); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	existing, err := readMailboxEvents(repoDir, parentIssue)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	nextSeq := int64(1)
	if len(existing) > 0 {
		nextSeq = existing[len(existing)-1].Seq + 1
	}

	event := daemonMailEvent{
		Seq:         nextSeq,
		ParentIssue: parentIssue,
		IssueID:     strings.TrimSpace(cmd.IssueID.String()),
		Type:        eventType,
		From:        strings.TrimSpace(cmd.From),
		To:          strings.TrimSpace(cmd.To),
		Body:        cmd.Body,
		CreatedAt:   time.Now().UTC(),
	}
	if err := appendMailboxEvent(repoDir, event); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}

	out, err := json.Marshal(mailEventToProtocol(event))
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = out
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon mail send completed",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"issue_id", event.IssueID,
			"type", event.Type,
			"seq", event.Seq,
		)
	}
	return resp, nil
}

func (d *Daemon) handleMailList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.MailListCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	repoDir := strings.TrimSpace(cmd.RepoDir)
	parentIssue := strings.TrimSpace(cmd.ParentIssue)
	if repoDir == "" || parentIssue == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: repo_dir/parent_issue"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon mail list requested",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"since_seq", cmd.SinceSeq,
			"limit", cmd.Limit,
		)
	}
	events, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, req, repoDir, parentIssue)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	filtered := filterMailEvents(events, cmd.SinceSeq, cmd.Limit)
	body, err := json.Marshal(filtered)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	if d.cfg.Logger != nil {
		d.cfg.Logger.Info("daemon mail list completed",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"result_count", len(filtered),
		)
	}
	return resp, nil
}

func (d *Daemon) handleMailWatch(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd protocol.MailWatchCommandBody
	if err := json.Unmarshal(req.Body, &cmd); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	repoDir := strings.TrimSpace(cmd.RepoDir)
	parentIssue := strings.TrimSpace(cmd.ParentIssue)
	if repoDir == "" || parentIssue == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: repo_dir/parent_issue"), nil
	}
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon mail watch requested",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"since_seq", cmd.SinceSeq,
		)
	}
	events, err := d.readMailboxEventsWithReviewReadyRecovery(ctx, req, repoDir, parentIssue)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	filtered := filterMailEvents(events, cmd.SinceSeq, 0)
	body, err := json.Marshal(filtered)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	if d.cfg.Logger != nil {
		d.cfg.Logger.Debug("daemon mail watch completed",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"result_count", len(filtered),
		)
	}
	return resp, nil
}

const reviewReadyReplayPublication = "review_ready_observation_replay.v1"

func (d *Daemon) readMailboxEventsWithReviewReadyRecovery(ctx context.Context, req protocol.RequestEnvelope, repoDir, parentIssue string) ([]daemonMailEvent, error) {
	unlock, err := lockMailbox(repoDir, parentIssue)
	if err != nil {
		return nil, fmt.Errorf("lock mailbox for review-ready recovery: %w", err)
	}
	if err := repairTrailingMailboxFragment(repoDir, parentIssue); err != nil {
		unlock()
		return nil, err
	}
	events, err := readMailboxEvents(repoDir, parentIssue)
	unlock()
	if err != nil {
		return nil, err
	}
	recovered, err := d.recoverReviewReadyMailboxEvents(ctx, req, repoDir, parentIssue, events)
	if err != nil {
		if d.cfg.Logger != nil {
			d.cfg.Logger.Warn("rooted review-ready publication recovery failed",
				"project_id", d.projectID(req.Meta),
				"root_issue_id", parentIssue,
				"mailbox_cursor", lastMailboxSequence(events),
				"error", err,
			)
		}
		return events, nil
	}
	return recovered, nil
}

func (d *Daemon) recoverReviewReadyMailboxEvents(ctx context.Context, req protocol.RequestEnvelope, repoDir, rootIssueID string, existing []daemonMailEvent) ([]daemonMailEvent, error) {
	projectID := d.projectID(req.Meta)
	d.issueClientsMu.Lock()
	hasConfiguredIssueStore := d.issues != nil || len(d.issueClientsByProject) > 0 || len(d.issueClientsByRoot) > 0
	d.issueClientsMu.Unlock()
	if !hasConfiguredIssueStore {
		return existing, nil
	}
	issueClient := d.issueClientForProject(projectID)
	if issueClient == nil {
		return existing, nil
	}
	if d.reviewReadyRecoveryBeforeLoad != nil {
		d.reviewReadyRecoveryBeforeLoad()
	}
	recoveryKey := projectID + "\x00" + repoDir + "\x00" + rootIssueID
	d.reviewReadyRecoveryMu.Lock()
	previousCursor := d.reviewReadyRecoveryCursor[recoveryKey]
	d.reviewReadyRecoveryMu.Unlock()
	if previousCursor > 0 {
		advanced, listErr := issueClient.ListProjectIssueObservationEvents(ctx, previousCursor, 1)
		if listErr != nil {
			return nil, fmt.Errorf("check rooted observation replay cursor %d: %w", previousCursor, listErr)
		}
		if len(advanced) == 0 {
			return existing, nil
		}
	}
	tasks, err := issueClient.ListParentChildSubtreeWithRuntime(ctx, projectID, rootIssueID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return existing, nil
		}
		return nil, fmt.Errorf("load rooted issue scope: %w", err)
	}
	inScope := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		inScope[task.ID.String()] = struct{}{}
	}

	byIssue := make(map[string][]domain.IssueObservationEvent, len(inScope))
	afterID := int64(0)
	for {
		batch, listErr := issueClient.ListProjectIssueObservationEvents(ctx, afterID, 5000)
		if listErr != nil {
			return nil, fmt.Errorf("replay issue observation stream after %d: %w", afterID, listErr)
		}
		if len(batch) == 0 {
			break
		}
		for _, event := range batch {
			if _, ok := inScope[event.IssueID.String()]; ok {
				byIssue[event.IssueID.String()] = append(byIssue[event.IssueID.String()], event)
			}
		}
		afterID = batch[len(batch)-1].ID
		if len(batch) < 5000 {
			break
		}
	}

	candidates := make([]daemonMailEvent, 0)
	for _, task := range tasks {
		issueID := task.ID.String()
		for _, publication := range domain.DeriveReviewReadyPublications(byIssue[issueID]) {
			source := publication.SourceEvent
			key := fmt.Sprintf("%s:%d", projectID, source.ID)
			body, marshalErr := json.Marshal(publication.Evidence)
			if marshalErr != nil {
				return nil, fmt.Errorf("encode review-ready publication %s: %w", key, marshalErr)
			}
			event := daemonMailEvent{
				ParentIssue: rootIssueID, IssueID: issueID,
				Type: "worker-integration-ready", From: "daemon-observation-replay",
				Body: string(body), CreatedAt: source.ObservedAt.UTC(),
				Payload: map[string]interface{}{
					"publication":                reviewReadyReplayPublication,
					"publication_key":            key,
					"source_event_id":            source.ID,
					"source_event_type":          string(source.Type),
					"worker_evidence":            publication.Evidence,
					"worker_evidence_validation": publication.Validation,
				},
			}
			if event.CreatedAt.IsZero() {
				event.CreatedAt = time.Now().UTC()
			}
			candidates = append(candidates, event)
		}
	}

	// Observation loading can involve a full durable-stream scan. Keep it outside
	// the mailbox critical section, then reread publication keys under the lock so
	// concurrent sends and concurrent replay attempts retain one monotonic cursor.
	unlock, err := lockMailbox(repoDir, rootIssueID)
	if err != nil {
		return nil, fmt.Errorf("lock mailbox to publish review-ready recovery: %w", err)
	}
	defer unlock()
	if err := repairTrailingMailboxFragment(repoDir, rootIssueID); err != nil {
		return nil, err
	}
	existing, err = readMailboxEvents(repoDir, rootIssueID)
	if err != nil {
		return nil, err
	}
	published := make(map[string]struct{}, len(existing))
	for _, event := range existing {
		if event.Payload == nil || event.Payload["publication"] != reviewReadyReplayPublication {
			continue
		}
		if key, _ := event.Payload["publication_key"].(string); strings.TrimSpace(key) != "" {
			published[key] = struct{}{}
		}
	}
	nextSeq := lastMailboxSequence(existing) + 1
	for _, event := range candidates {
		key, _ := event.Payload["publication_key"].(string)
		if _, ok := published[key]; ok {
			continue
		}
		event.Seq = nextSeq
		if err := appendMailboxEvent(repoDir, event); err != nil {
			return nil, fmt.Errorf("append recovered review-ready publication %s: %w", key, err)
		}
		existing = append(existing, event)
		published[key] = struct{}{}
		nextSeq++
		if d.cfg.Logger != nil {
			d.cfg.Logger.Info("rooted review-ready publication recovered",
				"project_id", projectID,
				"root_issue_id", rootIssueID,
				"issue_id", event.IssueID,
				"source_event_id", event.Payload["source_event_id"],
				"source_event_type", event.Payload["source_event_type"],
				"mailbox_seq", event.Seq,
			)
		}
	}
	d.reviewReadyRecoveryMu.Lock()
	if d.reviewReadyRecoveryCursor == nil {
		d.reviewReadyRecoveryCursor = make(map[string]int64)
	}
	if afterID > d.reviewReadyRecoveryCursor[recoveryKey] {
		d.reviewReadyRecoveryCursor[recoveryKey] = afterID
	}
	d.reviewReadyRecoveryMu.Unlock()
	return existing, nil
}

func lastMailboxSequence(events []daemonMailEvent) int64 {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq
}

func filterMailEvents(events []daemonMailEvent, since int64, limit int) []protocol.MailEvent {
	filtered := make([]protocol.MailEvent, 0, len(events))
	for _, evt := range events {
		if evt.Seq >= since {
			filtered = append(filtered, mailEventToProtocol(evt))
		}
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered
}

func mailEventToProtocol(evt daemonMailEvent) protocol.MailEvent {
	payload := evt.Payload
	if parsed := workerEvidencePayload(evt.Body); len(parsed) > 0 {
		payload = make(map[string]interface{}, len(evt.Payload)+len(parsed))
		for key, value := range evt.Payload {
			payload[key] = value
		}
		for key, value := range parsed {
			if _, exists := payload[key]; !exists {
				payload[key] = value
			}
		}
	}
	return protocol.MailEvent{
		Seq:         evt.Seq,
		ParentIssue: evt.ParentIssue,
		IssueID:     naming.IssueID(evt.IssueID),
		Type:        evt.Type,
		From:        evt.From,
		To:          evt.To,
		Body:        evt.Body,
		CreatedAt:   evt.CreatedAt.UTC().Format(time.RFC3339Nano),
		Payload:     payload,
	}
}

func workerEvidencePayload(body string) map[string]interface{} {
	packet, validation := domain.ParseWorkerEvidencePacketBody(body)
	if !validation.Found {
		return nil
	}
	payload := map[string]interface{}{
		"worker_evidence_validation": validation,
	}
	if validation.Complete {
		payload["worker_evidence"] = packet
	}
	return payload
}

func invalidWorkerEvidenceMessage(body string) string {
	_, validation := domain.ParseWorkerEvidencePacketBody(body)
	if !validation.Found || validation.Complete {
		return ""
	}
	problems := workerEvidenceProblemSummary(validation)
	if strings.TrimSpace(problems) == "" {
		problems = "packet is incomplete"
	}
	return "invalid worker_evidence.v1 packet: " + problems + `. Omit artifact_links unless links are needed. Run az mail validate-evidence --fix --body '<json>' for repairable schema mismatches, or az mail validate-evidence --template for the canonical shape.`
}

func workerEvidenceProblemSummary(validation domain.WorkerEvidenceParseResult) string {
	if len(validation.Diagnostics) == 0 {
		return strings.Join(validation.Problems(), "; ")
	}
	parts := make([]string, 0, len(validation.Diagnostics))
	seen := map[string]struct{}{}
	for _, diagnostic := range validation.Diagnostics {
		path := strings.TrimSpace(diagnostic.Path)
		if path == "" {
			path = "/"
		}
		part := path + ": " + diagnostic.Message
		if len(diagnostic.AllowedValues) > 0 {
			part += " (allowed: " + strings.Join(diagnostic.AllowedValues, ", ") + ")"
		}
		if strings.TrimSpace(diagnostic.Suggestion) != "" {
			part += " (fix: " + strings.TrimSpace(diagnostic.Suggestion) + ")"
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func sanitizeMailboxKey(parentIssue string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSpace(parentIssue))
}

func mailboxPath(repoDir, parentIssue string) string {
	return filepath.Join(repoDir, ".azedarach", "mailbox", sanitizeMailboxKey(parentIssue)+".jsonl")
}

func mailboxLockPath(repoDir, parentIssue string) string {
	return filepath.Join(repoDir, ".azedarach", "mailbox", sanitizeMailboxKey(parentIssue)+".lock")
}

func lockMailbox(repoDir, parentIssue string) (func(), error) {
	path := mailboxLockPath(repoDir, parentIssue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create mailbox lock dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open mailbox lock file: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire mailbox lock: %w", err)
	}
	released := false
	return func() {
		if released {
			return
		}
		released = true
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}

func readMailboxEvents(repoDir, parentIssue string) ([]daemonMailEvent, error) {
	path := mailboxPath(repoDir, parentIssue)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open mailbox: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	out := make([]daemonMailEvent, 0, 64)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt daemonMailEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			return nil, fmt.Errorf("decode mailbox event: %w", err)
		}
		out = append(out, evt)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan mailbox: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func appendMailboxEvent(repoDir string, evt daemonMailEvent) error {
	path := mailboxPath(repoDir, evt.ParentIssue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create mailbox dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open mailbox file: %w", err)
	}
	defer file.Close()
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("encode mailbox event: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write mailbox event: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync mailbox event: %w", err)
	}
	return nil
}

// repairTrailingMailboxFragment removes only bytes after the last committed
// newline. appendMailboxEvent writes one JSON record plus its newline in a
// single write, so a missing final newline is an interrupted append; every
// earlier record remains immutable.
func repairTrailingMailboxFragment(repoDir, parentIssue string) error {
	path := mailboxPath(repoDir, parentIssue)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read mailbox for trailing-fragment repair: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return nil
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	truncateAt := int64(lastNewline + 1)
	if err := os.Truncate(path, truncateAt); err != nil {
		return fmt.Errorf("truncate interrupted mailbox append: %w", err)
	}
	return nil
}
