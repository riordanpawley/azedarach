package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonnotices "github.com/riordanpawley/azedarach/internal/daemon/notices"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func (d *Daemon) handleNoticeList(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.NoticeListRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	projectID := d.projectID(req.Meta)
	if body.ProjectID != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	records, err := d.noticeService.List(ctx, daemonnotices.Query{
		ProjectID:    projectID,
		States:       mapNoticeStates(body.States),
		Read:         body.Read,
		Severity:     body.Severity,
		Category:     strings.TrimSpace(body.Category),
		ScopeType:    strings.TrimSpace(body.ScopeType),
		ScopeID:      strings.TrimSpace(body.ScopeID),
		OperationID:  strings.TrimSpace(body.OperationID.String()),
		UpdatedAfter: body.UpdatedAfter,
		Limit:        body.Limit,
	})
	if err != nil {
		return d.errorResponse(req, noticeErrorCode(err), err.Error())
	}
	encoded, err := json.Marshal(protocol.NoticeListResponseBody{
		ProjectID: naming.ProjectID(projectID),
		Notices:   daemonnotices.RecordsToProtocol(records),
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal notice list response: %v", err))
	}
	resp := d.successResponse(req)
	resp.Body = encoded
	return resp
}

func (d *Daemon) handleNoticeGet(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.NoticeGetRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	projectID := d.projectID(req.Meta)
	if body.ProjectID != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	noticeID := strings.TrimSpace(body.NoticeID)
	if noticeID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: notice_id")
	}
	record, err := d.noticeService.Get(ctx, projectID, noticeID)
	if err != nil {
		return d.errorResponse(req, noticeErrorCode(err), err.Error())
	}
	encoded, err := json.Marshal(protocol.NoticeGetResponseBody{Notice: daemonnotices.ToProtocol(record)})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal notice get response: %v", err))
	}
	resp := d.successResponse(req)
	resp.Body = encoded
	return resp
}

func (d *Daemon) handleNoticeUpdate(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.NoticeUpdateRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	projectID := d.projectID(req.Meta)
	if body.ProjectID != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	noticeID := strings.TrimSpace(body.NoticeID)
	if noticeID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required field: notice_id")
	}
	record, _, rev, err := d.noticeService.Update(ctx, daemonnotices.UpdateParams{
		ProjectID: projectID,
		NoticeID:  noticeID,
		Read:      body.Read,
		State:     body.State,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		return d.errorResponse(req, noticeErrorCode(err), err.Error())
	}
	encoded, err := json.Marshal(protocol.NoticeUpdateResponseBody{Notice: daemonnotices.ToProtocol(record)})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal notice update response: %v", err))
	}
	resp := d.successResponse(req)
	resp.Revision = rev
	resp.Body = encoded
	return resp
}

func (d *Daemon) handleNoticeAction(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.NoticeActionRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	projectID := d.projectID(req.Meta)
	if body.ProjectID != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	noticeID := strings.TrimSpace(body.NoticeID)
	actionID := strings.TrimSpace(body.ActionID)
	if noticeID == "" || actionID == "" {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, "missing required fields: notice_id/action_id")
	}
	record, _, rev, err := d.noticeService.ExecuteAction(ctx, projectID, noticeID, actionID, time.Now().UTC())
	if err != nil {
		return d.errorResponse(req, noticeErrorCode(err), err.Error())
	}
	encoded, err := json.Marshal(protocol.NoticeActionResponseBody{Notice: daemonnotices.ToProtocol(record)})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal notice action response: %v", err))
	}
	resp := d.successResponse(req)
	resp.Revision = rev
	resp.Body = encoded
	return resp
}

func mapNoticeStates(states []protocol.NoticeState) []daemonnotices.State {
	out := make([]daemonnotices.State, 0, len(states))
	for _, state := range states {
		if state == "" {
			continue
		}
		out = append(out, state)
	}
	return out
}

func noticeErrorCode(err error) protocol.ErrorCode {
	switch {
	case errors.Is(err, daemonnotices.ErrNotFound):
		return protocol.ErrorCodeInvalidRequest
	case errors.Is(err, daemonnotices.ErrConflict), errors.Is(err, daemonnotices.ErrInvalidTransition):
		return protocol.ErrorCodeConflict
	case errors.Is(err, daemonnotices.ErrInvalidNotice):
		return protocol.ErrorCodeInvalidRequest
	default:
		return protocol.ErrorCodeInternal
	}
}
