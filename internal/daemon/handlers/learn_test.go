package handlers

import (
	"context"
	"fmt"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type fakeLearnService struct {
	addFn     func(context.Context, protocol.LearnAddRequestBody) (protocol.LearnAddResponseBody, error)
	recallFn  func(context.Context, protocol.LearnRecallRequestBody) (protocol.LearnRecallResponseBody, error)
	showFn    func(context.Context, protocol.LearnShowRequestBody) (protocol.LearnShowResponseBody, error)
	reviewFn  func(context.Context, protocol.LearnReviewRequestBody) (protocol.LearnReviewResponseBody, error)
	promoteFn func(context.Context, protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error)
}

func (f *fakeLearnService) Add(ctx context.Context, req protocol.LearnAddRequestBody) (protocol.LearnAddResponseBody, error) {
	if f.addFn != nil {
		return f.addFn(ctx, req)
	}
	return protocol.LearnAddResponseBody{}, nil
}

func (f *fakeLearnService) Recall(ctx context.Context, req protocol.LearnRecallRequestBody) (protocol.LearnRecallResponseBody, error) {
	if f.recallFn != nil {
		return f.recallFn(ctx, req)
	}
	return protocol.LearnRecallResponseBody{}, nil
}

func (f *fakeLearnService) Show(ctx context.Context, req protocol.LearnShowRequestBody) (protocol.LearnShowResponseBody, error) {
	if f.showFn != nil {
		return f.showFn(ctx, req)
	}
	return protocol.LearnShowResponseBody{}, nil
}

func (f *fakeLearnService) Review(ctx context.Context, req protocol.LearnReviewRequestBody) (protocol.LearnReviewResponseBody, error) {
	if f.reviewFn != nil {
		return f.reviewFn(ctx, req)
	}
	return protocol.LearnReviewResponseBody{}, nil
}

func (f *fakeLearnService) Promote(ctx context.Context, req protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error) {
	if f.promoteFn != nil {
		return f.promoteFn(ctx, req)
	}
	return protocol.LearnPromoteResponseBody{}, nil
}

func TestLearnHandlerRoutesAndValidates(t *testing.T) {
	var gotAdd protocol.LearnAddRequestBody
	var gotRecall protocol.LearnRecallRequestBody
	var gotReview protocol.LearnReviewRequestBody
	var gotPromote protocol.LearnPromoteRequestBody
	handler := NewLearnHandler(&fakeLearnService{
		addFn: func(_ context.Context, req protocol.LearnAddRequestBody) (protocol.LearnAddResponseBody, error) {
			gotAdd = req
			return protocol.LearnAddResponseBody{Learning: protocol.Learning{ID: "learn-1"}}, nil
		},
		recallFn: func(_ context.Context, req protocol.LearnRecallRequestBody) (protocol.LearnRecallResponseBody, error) {
			gotRecall = req
			return protocol.LearnRecallResponseBody{Learnings: []protocol.Learning{{ID: "learn-1"}}}, nil
		},
		reviewFn: func(_ context.Context, req protocol.LearnReviewRequestBody) (protocol.LearnReviewResponseBody, error) {
			gotReview = req
			updated := protocol.Learning{ID: req.ID, Status: req.Status}
			return protocol.LearnReviewResponseBody{Updated: &updated}, nil
		},
		promoteFn: func(_ context.Context, req protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error) {
			gotPromote = req
			return protocol.LearnPromoteResponseBody{Learning: protocol.Learning{ID: req.ID, Target: req.Target}}, nil
		},
	})

	addResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnAdd, protocol.LearnAddRequestBody{
		IssueID:  naming.IssueID("csk"),
		Evidence: "evidence",
	}))
	if !addResp.OK || gotAdd.IssueID != "csk" || gotAdd.Evidence != "evidence" {
		t.Fatalf("add response=%+v got=%+v", addResp, gotAdd)
	}

	recallResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnRecall, protocol.LearnRecallRequestBody{
		Query:    "daemon",
		Statuses: []protocol.LearningStatus{protocol.LearningStatusPromoted},
		Limit:    2,
	}))
	if !recallResp.OK || gotRecall.Query != "daemon" || gotRecall.Limit != 2 {
		t.Fatalf("recall response=%+v got=%+v", recallResp, gotRecall)
	}

	reviewResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnReview, protocol.LearnReviewRequestBody{
		ID:     "learn-1",
		Status: protocol.LearningStatusRejected,
		Note:   "Not durable enough.",
	}))
	if !reviewResp.OK || gotReview.ID != "learn-1" || gotReview.Status != protocol.LearningStatusRejected || gotReview.Note != "Not durable enough." {
		t.Fatalf("review response=%+v got=%+v", reviewResp, gotReview)
	}

	promoteResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnPromote, protocol.LearnPromoteRequestBody{
		ID:       "learn-1",
		Target:   protocol.LearningPromotionTargetDecision,
		TargetID: "dec-1",
	}))
	if !promoteResp.OK || gotPromote.Target != protocol.LearningPromotionTargetDecision || gotPromote.TargetID != "dec-1" {
		t.Fatalf("promote response=%+v got=%+v", promoteResp, gotPromote)
	}

	badResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnAdd, protocol.LearnAddRequestBody{}))
	if badResp.OK || badResp.Error == nil || badResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad add response=%+v", badResp)
	}

	badPromotedResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnReview, protocol.LearnReviewRequestBody{
		ID:     "learn-1",
		Status: protocol.LearningStatusPromoted,
		Note:   "bad",
	}))
	if badPromotedResp.OK || badPromotedResp.Error == nil || badPromotedResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad promoted review response=%+v", badPromotedResp)
	}

	badListShortcutResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnReview, protocol.LearnReviewRequestBody{
		Status: protocol.LearningStatusAccepted,
		Note:   "missing id",
	}))
	if badListShortcutResp.OK || badListShortcutResp.Error == nil || badListShortcutResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad list shortcut review response=%+v", badListShortcutResp)
	}

	conflictHandler := NewLearnHandler(&fakeLearnService{
		promoteFn: func(context.Context, protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error) {
			return protocol.LearnPromoteResponseBody{}, fmt.Errorf("%w: learning must be accepted before promotion", domain.ErrConflict)
		},
	})
	conflictResp := conflictHandler.Handle(context.Background(), specRequest(t, protocol.CommandLearnPromote, protocol.LearnPromoteRequestBody{
		ID:       "learn-1",
		Target:   protocol.LearningPromotionTargetDecision,
		TargetID: "dec-1",
	}))
	if conflictResp.OK || conflictResp.Error == nil || conflictResp.Error.Code != protocol.ErrorCodeConflict {
		t.Fatalf("conflict promote response=%+v", conflictResp)
	}
}
