package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonnotices "github.com/riordanpawley/azedarach/internal/daemon/notices"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestNoticeCommandsListGetUpdateAndAction(t *testing.T) {
	ctx := context.Background()
	hub := publish.NewHub(16, 8, nil)
	store := daemonnotices.NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), nil)
	d := &Daemon{
		hub:      hub,
		revision: map[string]uint64{},
	}
	d.noticeService = daemonnotices.NewService(daemonnotices.ServiceConfig{
		Repository:   store,
		Hub:          hub,
		NextRevision: d.nextRevision,
	})
	defer d.noticeService.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	record, _, _, err := d.noticeService.Upsert(ctx, daemonnotices.Candidate{
		NoticeID:  "notice-1",
		ProjectID: "proj-1",
		Scope: daemonnotices.Scope{
			Type: "task",
			ID:   "az-1",
		},
		Source: &daemonnotices.Source{
			OperationID:    naming.OperationID("op-1"),
			OperationKind:  "task.update_status",
			OperationState: protocol.OperationStateFailed,
			Producer:       "test",
		},
		Severity:       daemonnotices.SeverityError,
		Category:       "mutation_rejected",
		Title:          "Mutation failed",
		Summary:        "first failure",
		DedupeKey:      "proj-1:task:az-1:mutation_rejected",
		OccurredAt:     now,
		RetentionClass: daemonnotices.RetentionError,
	})
	if err != nil {
		t.Fatalf("seed notice: %v", err)
	}

	listResp := d.handleNoticeList(ctx, testRequest(protocol.CommandNoticeList, protocol.NoticeListRequestBody{
		ProjectID: "proj-1",
		States:    []protocol.NoticeState{protocol.NoticeStateActive},
	}))
	if !listResp.OK {
		t.Fatalf("list response = %+v", listResp)
	}
	var listBody protocol.NoticeListResponseBody
	if err := json.Unmarshal(listResp.Body, &listBody); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(listBody.Notices) != 1 || listBody.Notices[0].NoticeID != record.NoticeID {
		t.Fatalf("list notices = %+v, want seeded notice", listBody.Notices)
	}

	getResp := d.handleNoticeGet(ctx, testRequest(protocol.CommandNoticeGet, protocol.NoticeGetRequestBody{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
	}))
	if !getResp.OK {
		t.Fatalf("get response = %+v", getResp)
	}
	var getBody protocol.NoticeGetResponseBody
	if err := json.Unmarshal(getResp.Body, &getBody); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if getBody.Notice.NoticeID != record.NoticeID || getBody.Notice.Scope.ID != "az-1" {
		t.Fatalf("get notice = %+v, want seeded notice", getBody.Notice)
	}

	read := true
	updateResp := d.handleNoticeUpdate(ctx, testRequest(protocol.CommandNoticeUpdate, protocol.NoticeUpdateRequestBody{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		Read:      &read,
	}))
	if !updateResp.OK {
		t.Fatalf("update response = %+v", updateResp)
	}
	var updateBody protocol.NoticeUpdateResponseBody
	if err := json.Unmarshal(updateResp.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}
	if !updateBody.Notice.Read || updateResp.Revision == 0 {
		t.Fatalf("update notice read=%v revision=%d, want read with revision", updateBody.Notice.Read, updateResp.Revision)
	}

	actionResp := d.handleNoticeAction(ctx, testRequest(protocol.CommandNoticeAction, protocol.NoticeActionRequestBody{
		ProjectID: "proj-1",
		NoticeID:  record.NoticeID,
		ActionID:  "dismiss",
	}))
	if !actionResp.OK {
		t.Fatalf("action response = %+v", actionResp)
	}
	var actionBody protocol.NoticeActionResponseBody
	if err := json.Unmarshal(actionResp.Body, &actionBody); err != nil {
		t.Fatalf("unmarshal action: %v", err)
	}
	if actionBody.Notice.State != protocol.NoticeStateDismissed || actionResp.Revision <= updateResp.Revision {
		t.Fatalf("action notice state=%s revision=%d, want dismissed after update rev %d", actionBody.Notice.State, actionResp.Revision, updateResp.Revision)
	}
}
