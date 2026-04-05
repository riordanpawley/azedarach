package daemon

import (
	"bufio"
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
	unlock, err := lockMailbox(repoDir, parentIssue)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("lock mailbox: %v", err)), nil
	}
	defer unlock()

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

func (d *Daemon) handleMailList(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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
	events, err := readMailboxEvents(repoDir, parentIssue)
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

func (d *Daemon) handleMailWatch(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
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
		d.cfg.Logger.Info("daemon mail watch requested",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"since_seq", cmd.SinceSeq,
		)
	}
	events, err := readMailboxEvents(repoDir, parentIssue)
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
		d.cfg.Logger.Info("daemon mail watch completed",
			"repo_dir", repoDir,
			"parent_issue", parentIssue,
			"result_count", len(filtered),
		)
	}
	return resp, nil
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
	return protocol.MailEvent{
		Seq:         evt.Seq,
		ParentIssue: evt.ParentIssue,
		IssueID:     naming.IssueID(evt.IssueID),
		Type:        evt.Type,
		From:        evt.From,
		To:          evt.To,
		Body:        evt.Body,
		CreatedAt:   evt.CreatedAt.UTC().Format(time.RFC3339Nano),
		Payload:     evt.Payload,
	}
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
	return nil
}
