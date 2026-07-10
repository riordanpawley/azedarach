package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/daemon/notices"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestInteractionNoticeAdapterDismissalDoesNotResolveRequest(t *testing.T) {
	ctx := context.Background()
	service := notices.NewService(notices.ServiceConfig{Repository: notices.NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), nil)})
	defer service.Close()
	adapter := interactionNoticeAdapter{projectID: "proj-1", notices: service}
	request := testInteractionNoticeRequest()
	if err := adapter.ProjectInteractionNotice(ctx, request); err != nil {
		t.Fatalf("project open request: %v", err)
	}
	record, err := service.Get(ctx, "proj-1", "interaction-req-1")
	if err != nil {
		t.Fatalf("get projected notice: %v", err)
	}
	if record.State != notices.StateActive || record.Severity != notices.SeverityError || len(record.Actions) != 1 {
		t.Fatalf("projected notice = %+v", record)
	}
	if _, _, _, err := service.Update(ctx, notices.UpdateParams{ProjectID: "proj-1", NoticeID: record.NoticeID, State: notices.StateDismissed, Now: request.UpdatedAt.Add(time.Minute)}); err != nil {
		t.Fatalf("dismiss notice: %v", err)
	}
	if err := adapter.ProjectInteractionNotice(ctx, request); err != nil {
		t.Fatalf("reproject dismissed request: %v", err)
	}
	record, err = service.Get(ctx, "proj-1", record.NoticeID)
	if err != nil || record.State != notices.StateDismissed || !request.Unresolved() {
		t.Fatalf("dismissal coupled to request: notice=%+v request=%+v err=%v", record, request, err)
	}
}

func TestInteractionNoticeAdapterResolvesOnlyActiveProjection(t *testing.T) {
	ctx := context.Background()
	service := notices.NewService(notices.ServiceConfig{Repository: notices.NewAtPath(filepath.Join(t.TempDir(), "azedarach.db"), nil)})
	defer service.Close()
	adapter := interactionNoticeAdapter{projectID: "proj-1", notices: service}
	request := testInteractionNoticeRequest()
	if err := adapter.ProjectInteractionNotice(ctx, request); err != nil {
		t.Fatalf("project open request: %v", err)
	}
	request.State = domain.InteractionResolved
	request.Revision++
	request.UpdatedAt = request.UpdatedAt.Add(time.Minute)
	request.FinalAnswer = &domain.InteractionAnswerAudit{Answer: "yes", Actor: "human", CreatedAt: request.UpdatedAt}
	if err := adapter.ProjectInteractionNotice(ctx, request); err != nil {
		t.Fatalf("project resolved request: %v", err)
	}
	record, err := service.Get(ctx, "proj-1", "interaction-req-1")
	if err != nil || record.State != notices.StateResolved || record.ResolvedAt == nil {
		t.Fatalf("resolved projection = %+v err=%v", record, err)
	}
}

func testInteractionNoticeRequest() domain.InteractionRequest {
	now := time.Unix(1_700_000_000, 0).UTC()
	return domain.InteractionRequest{
		ID: "req-1", IssueID: "az-1", DecisionKey: "deploy", OrchestrationScope: "root",
		Question: "Deploy now?", Why: "Production impact", RequiredDecisions: []string{"yes or no"},
		Significance: domain.InteractionSignificanceCritical, Respondent: "human",
		DecisionPacket: domain.InteractionDecisionPacket{Summary: "Choose deployment timing"},
		State:          domain.InteractionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
}
