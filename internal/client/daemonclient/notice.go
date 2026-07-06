package daemonclient

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type NoticeListOptions struct {
	States       []protocol.NoticeState
	Read         *bool
	Severity     protocol.NoticeSeverity
	Category     string
	ScopeType    string
	ScopeID      string
	OperationID  string
	UpdatedAfter *time.Time
	Limit        int
}

func (c *Client) ListNotices(ctx context.Context, opts NoticeListOptions) ([]protocol.NoticeRecord, error) {
	var operationID naming.OperationID
	if trimmed := strings.TrimSpace(opts.OperationID); trimmed != "" {
		parsedOperationID, err := naming.ParseOperationID(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid operation id: %w", err)
		}
		operationID = parsedOperationID
	}
	var out protocol.NoticeListResponseBody
	if err := c.commandJSON(ctx, protocol.CommandNoticeList, protocol.NoticeListRequestBody{
		ProjectID:    c.projectID,
		States:       opts.States,
		Read:         opts.Read,
		Severity:     opts.Severity,
		Category:     opts.Category,
		ScopeType:    opts.ScopeType,
		ScopeID:      opts.ScopeID,
		OperationID:  operationID,
		UpdatedAfter: opts.UpdatedAfter,
		Limit:        opts.Limit,
	}, &out); err != nil {
		return nil, err
	}
	return out.Notices, nil
}

func (c *Client) GetNotice(ctx context.Context, noticeID string) (protocol.NoticeRecord, error) {
	noticeID = strings.TrimSpace(noticeID)
	if noticeID == "" {
		return protocol.NoticeRecord{}, fmt.Errorf("notice id is required")
	}
	var out protocol.NoticeGetResponseBody
	if err := c.commandJSON(ctx, protocol.CommandNoticeGet, protocol.NoticeGetRequestBody{
		ProjectID: c.projectID,
		NoticeID:  noticeID,
	}, &out); err != nil {
		return protocol.NoticeRecord{}, err
	}
	return out.Notice, nil
}

func (c *Client) UpdateNotice(ctx context.Context, noticeID string, read *bool, state protocol.NoticeState) (protocol.NoticeRecord, error) {
	noticeID = strings.TrimSpace(noticeID)
	if noticeID == "" {
		return protocol.NoticeRecord{}, fmt.Errorf("notice id is required")
	}
	var out protocol.NoticeUpdateResponseBody
	if err := c.commandJSON(ctx, protocol.CommandNoticeUpdate, protocol.NoticeUpdateRequestBody{
		ProjectID: c.projectID,
		NoticeID:  noticeID,
		Read:      read,
		State:     state,
	}, &out); err != nil {
		return protocol.NoticeRecord{}, err
	}
	return out.Notice, nil
}

func (c *Client) RunNoticeAction(ctx context.Context, noticeID, actionID string, input map[string]string) (protocol.NoticeRecord, error) {
	noticeID = strings.TrimSpace(noticeID)
	actionID = strings.TrimSpace(actionID)
	if noticeID == "" {
		return protocol.NoticeRecord{}, fmt.Errorf("notice id is required")
	}
	if actionID == "" {
		return protocol.NoticeRecord{}, fmt.Errorf("notice action id is required")
	}
	var out protocol.NoticeActionResponseBody
	if err := c.commandJSON(ctx, protocol.CommandNoticeAction, protocol.NoticeActionRequestBody{
		ProjectID: c.projectID,
		NoticeID:  noticeID,
		ActionID:  actionID,
		Input:     input,
	}, &out); err != nil {
		return protocol.NoticeRecord{}, err
	}
	return out.Notice, nil
}
