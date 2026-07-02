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
	addFn       func(context.Context, protocol.LearnAddRequestBody) (protocol.LearnAddResponseBody, error)
	recallFn    func(context.Context, protocol.LearnRecallRequestBody) (protocol.LearnRecallResponseBody, error)
	showFn      func(context.Context, protocol.LearnShowRequestBody) (protocol.LearnShowResponseBody, error)
	reviewFn    func(context.Context, protocol.LearnReviewRequestBody) (protocol.LearnReviewResponseBody, error)
	staleFn     func(context.Context, protocol.LearnStaleRequestBody) (protocol.LearnStaleResponseBody, error)
	demoteFn    func(context.Context, protocol.LearnDemoteRequestBody) (protocol.LearnDemoteResponseBody, error)
	promoteFn   func(context.Context, protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error)
	retireFn    func(context.Context, protocol.LearnRetireRequestBody) (protocol.LearnRetireResponseBody, error)
	relateFn    func(context.Context, protocol.LearnRelateRequestBody) (protocol.LearnRelateResponseBody, error)
	supersedeFn func(context.Context, protocol.LearnSupersedeRequestBody) (protocol.LearnSupersedeResponseBody, error)
	doctorFn    func(context.Context, protocol.LearnDoctorRequestBody) (protocol.LearnDoctorResponseBody, error)
	gcFn        func(context.Context, protocol.LearnGCRequestBody) (protocol.LearnGCResponseBody, error)
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

func (f *fakeLearnService) Stale(ctx context.Context, req protocol.LearnStaleRequestBody) (protocol.LearnStaleResponseBody, error) {
	if f.staleFn != nil {
		return f.staleFn(ctx, req)
	}
	return protocol.LearnStaleResponseBody{}, nil
}

func (f *fakeLearnService) Demote(ctx context.Context, req protocol.LearnDemoteRequestBody) (protocol.LearnDemoteResponseBody, error) {
	if f.demoteFn != nil {
		return f.demoteFn(ctx, req)
	}
	return protocol.LearnDemoteResponseBody{}, nil
}

func (f *fakeLearnService) Promote(ctx context.Context, req protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error) {
	if f.promoteFn != nil {
		return f.promoteFn(ctx, req)
	}
	return protocol.LearnPromoteResponseBody{}, nil
}

func (f *fakeLearnService) Retire(ctx context.Context, req protocol.LearnRetireRequestBody) (protocol.LearnRetireResponseBody, error) {
	if f.retireFn != nil {
		return f.retireFn(ctx, req)
	}
	return protocol.LearnRetireResponseBody{}, nil
}

func (f *fakeLearnService) Relate(ctx context.Context, req protocol.LearnRelateRequestBody) (protocol.LearnRelateResponseBody, error) {
	if f.relateFn != nil {
		return f.relateFn(ctx, req)
	}
	return protocol.LearnRelateResponseBody{}, nil
}

func (f *fakeLearnService) Supersede(ctx context.Context, req protocol.LearnSupersedeRequestBody) (protocol.LearnSupersedeResponseBody, error) {
	if f.supersedeFn != nil {
		return f.supersedeFn(ctx, req)
	}
	return protocol.LearnSupersedeResponseBody{}, nil
}

func (f *fakeLearnService) Doctor(ctx context.Context, req protocol.LearnDoctorRequestBody) (protocol.LearnDoctorResponseBody, error) {
	if f.doctorFn != nil {
		return f.doctorFn(ctx, req)
	}
	return protocol.LearnDoctorResponseBody{}, nil
}

func (f *fakeLearnService) GC(ctx context.Context, req protocol.LearnGCRequestBody) (protocol.LearnGCResponseBody, error) {
	if f.gcFn != nil {
		return f.gcFn(ctx, req)
	}
	return protocol.LearnGCResponseBody{}, nil
}

func TestLearnHandlerRoutesAndValidates(t *testing.T) {
	var gotAdd protocol.LearnAddRequestBody
	var gotRecall protocol.LearnRecallRequestBody
	var gotReview protocol.LearnReviewRequestBody
	var gotStale protocol.LearnStaleRequestBody
	var gotDemote protocol.LearnDemoteRequestBody
	var gotPromote protocol.LearnPromoteRequestBody
	var gotRetire protocol.LearnRetireRequestBody
	var gotRelate protocol.LearnRelateRequestBody
	var gotSupersede protocol.LearnSupersedeRequestBody
	var gotDoctor protocol.LearnDoctorRequestBody
	var gotGC protocol.LearnGCRequestBody
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
		staleFn: func(_ context.Context, req protocol.LearnStaleRequestBody) (protocol.LearnStaleResponseBody, error) {
			gotStale = req
			return protocol.LearnStaleResponseBody{Learning: protocol.Learning{ID: req.ID, Status: protocol.LearningStatusStale, ReviewNote: req.Note}}, nil
		},
		demoteFn: func(_ context.Context, req protocol.LearnDemoteRequestBody) (protocol.LearnDemoteResponseBody, error) {
			gotDemote = req
			return protocol.LearnDemoteResponseBody{Learning: protocol.Learning{ID: req.ID, Status: protocol.LearningStatusCandidate, ReviewNote: req.Note}}, nil
		},
		promoteFn: func(_ context.Context, req protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error) {
			gotPromote = req
			return protocol.LearnPromoteResponseBody{Learning: protocol.Learning{ID: req.ID, Target: req.Target}}, nil
		},
		retireFn: func(_ context.Context, req protocol.LearnRetireRequestBody) (protocol.LearnRetireResponseBody, error) {
			gotRetire = req
			return protocol.LearnRetireResponseBody{Learning: protocol.Learning{ID: req.ID, TargetState: protocol.LearningTargetStateRetired}}, nil
		},
		relateFn: func(_ context.Context, req protocol.LearnRelateRequestBody) (protocol.LearnRelateResponseBody, error) {
			gotRelate = req
			return protocol.LearnRelateResponseBody{Relation: protocol.LearningRelation{ID: "learn-rel-1", Type: req.Type}}, nil
		},
		supersedeFn: func(_ context.Context, req protocol.LearnSupersedeRequestBody) (protocol.LearnSupersedeResponseBody, error) {
			gotSupersede = req
			return protocol.LearnSupersedeResponseBody{Relation: protocol.LearningRelation{ID: "learn-rel-2", Type: protocol.LearningRelationSupersedes, SourceLearningID: req.NewLearningID, TargetLearningID: req.OldLearningID}}, nil
		},
		doctorFn: func(_ context.Context, req protocol.LearnDoctorRequestBody) (protocol.LearnDoctorResponseBody, error) {
			gotDoctor = req
			return protocol.LearnDoctorResponseBody{Findings: []protocol.LearnMaintenanceFinding{{LearningID: "learn-1", Type: "old_candidate"}}}, nil
		},
		gcFn: func(_ context.Context, req protocol.LearnGCRequestBody) (protocol.LearnGCResponseBody, error) {
			gotGC = req
			return protocol.LearnGCResponseBody{DryRun: !req.Confirm, Deleted: []protocol.LearnMaintenanceFinding{{LearningID: "learn-1", Type: "old_candidate"}}}, nil
		},
	})

	addResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnAdd, protocol.LearnAddRequestBody{
		IssueID:  naming.IssueID("csk"),
		Evidence: "evidence",
		Private:  true,
	}))
	if !addResp.OK || gotAdd.IssueID != "csk" || gotAdd.Evidence != "evidence" || !gotAdd.Private {
		t.Fatalf("add response=%+v got=%+v", addResp, gotAdd)
	}

	recallResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnRecall, protocol.LearnRecallRequestBody{
		Query:          "daemon",
		Statuses:       []protocol.LearningStatus{protocol.LearningStatusPromoted},
		Limit:          2,
		IncludePrivate: true,
	}))
	if !recallResp.OK || gotRecall.Query != "daemon" || gotRecall.Limit != 2 || !gotRecall.IncludePrivate {
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

	staleResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnStale, protocol.LearnStaleRequestBody{
		ID:   " learn-1 ",
		Note: "No longer accurate.",
	}))
	if !staleResp.OK || gotStale.ID != "learn-1" || gotStale.Note != "No longer accurate." {
		t.Fatalf("stale response=%+v got=%+v", staleResp, gotStale)
	}

	demoteResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnDemote, protocol.LearnDemoteRequestBody{
		ID:   " learn-1 ",
		Note: "Needs another review.",
	}))
	if !demoteResp.OK || gotDemote.ID != "learn-1" || gotDemote.Note != "Needs another review." {
		t.Fatalf("demote response=%+v got=%+v", demoteResp, gotDemote)
	}

	promoteResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnPromote, protocol.LearnPromoteRequestBody{
		ID:                   "learn-1",
		Target:               protocol.LearningPromotionTargetDecision,
		TargetID:             "dec-1",
		TargetHash:           "sha256:target",
		TargetMetadata:       map[string]string{"path": "docs/decisions/dec-1.md"},
		TargetTitle:          "Updated decision",
		DecisionRationale:    "Updated rationale.",
		DecisionConsequences: "Updated consequences.",
	}))
	if !promoteResp.OK || gotPromote.Target != protocol.LearningPromotionTargetDecision || gotPromote.TargetID != "dec-1" || gotPromote.TargetHash != "sha256:target" || gotPromote.TargetMetadata["path"] != "docs/decisions/dec-1.md" || gotPromote.DecisionRationale != "Updated rationale." {
		t.Fatalf("promote response=%+v got=%+v", promoteResp, gotPromote)
	}

	createDecisionResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnPromote, protocol.LearnPromoteRequestBody{
		ID:                "learn-1",
		Target:            protocol.LearningPromotionTargetDecision,
		CreateTarget:      true,
		TargetTitle:       "Created decision",
		DecisionRationale: "Created rationale.",
	}))
	if !createDecisionResp.OK || !gotPromote.CreateTarget || gotPromote.TargetID != "" || gotPromote.TargetTitle != "Created decision" {
		t.Fatalf("create decision promote response=%+v got=%+v", createDecisionResp, gotPromote)
	}

	retireResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnRetire, protocol.LearnRetireRequestBody{
		ID:   " learn-1 ",
		Note: "Target removed.",
	}))
	if !retireResp.OK || gotRetire.ID != "learn-1" || gotRetire.Note != "Target removed." {
		t.Fatalf("retire response=%+v got=%+v", retireResp, gotRetire)
	}

	relateResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnRelate, protocol.LearnRelateRequestBody{
		Type:             protocol.LearningRelationSupersedes,
		SourceLearningID: "learn-2",
		TargetLearningID: "learn-1",
		Note:             "Newer guidance replaces older guidance.",
		ScopeIssueID:     naming.IssueID("csp"),
	}))
	if !relateResp.OK || gotRelate.Type != protocol.LearningRelationSupersedes || gotRelate.SourceLearningID != "learn-2" || gotRelate.TargetLearningID != "learn-1" {
		t.Fatalf("relate response=%+v got=%+v", relateResp, gotRelate)
	}

	supersedeResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnSupersede, protocol.LearnSupersedeRequestBody{
		NewLearningID: "learn-3",
		OldLearningID: "learn-1",
		Note:          "Newer guidance replaces older guidance.",
		ScopeIssueID:  naming.IssueID("cst"),
	}))
	if !supersedeResp.OK || gotSupersede.NewLearningID != "learn-3" || gotSupersede.OldLearningID != "learn-1" || gotSupersede.ScopeIssueID != "cst" {
		t.Fatalf("supersede response=%+v got=%+v", supersedeResp, gotSupersede)
	}

	doctorResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnDoctor, protocol.LearnDoctorRequestBody{
		ProjectID:              " proj ",
		CandidateOlderThanDays: 14,
		InactiveOlderThanDays:  60,
		Limit:                  5,
	}))
	if !doctorResp.OK || gotDoctor.ProjectID != "proj" || gotDoctor.CandidateOlderThanDays != 14 || gotDoctor.Limit != 5 {
		t.Fatalf("doctor response=%+v got=%+v", doctorResp, gotDoctor)
	}

	gcResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnGC, protocol.LearnGCRequestBody{
		ProjectID:              " proj ",
		CandidateOlderThanDays: 14,
		InactiveOlderThanDays:  60,
		Limit:                  5,
		Confirm:                true,
	}))
	if !gcResp.OK || gotGC.ProjectID != "proj" || gotGC.CandidateOlderThanDays != 14 || gotGC.Limit != 5 || !gotGC.Confirm {
		t.Fatalf("gc response=%+v got=%+v", gcResp, gotGC)
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

	badStaleResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnStale, protocol.LearnStaleRequestBody{
		ID: "learn-1",
	}))
	if badStaleResp.OK || badStaleResp.Error == nil || badStaleResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad stale response=%+v", badStaleResp)
	}

	badDemoteResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnDemote, protocol.LearnDemoteRequestBody{
		ID: "learn-1",
	}))
	if badDemoteResp.OK || badDemoteResp.Error == nil || badDemoteResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad demote response=%+v", badDemoteResp)
	}

	badRelateResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnRelate, protocol.LearnRelateRequestBody{
		Type:             protocol.LearningRelationType("replaces"),
		SourceLearningID: "learn-2",
		TargetLearningID: "learn-1",
		Note:             "bad",
	}))
	if badRelateResp.OK || badRelateResp.Error == nil || badRelateResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad relate response=%+v", badRelateResp)
	}

	badSupersedeResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnSupersede, protocol.LearnSupersedeRequestBody{
		NewLearningID: "learn-2",
		OldLearningID: "learn-1",
	}))
	if badSupersedeResp.OK || badSupersedeResp.Error == nil || badSupersedeResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad supersede response=%+v", badSupersedeResp)
	}

	badDoctorResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnDoctor, protocol.LearnDoctorRequestBody{
		CandidateOlderThanDays: -1,
	}))
	if badDoctorResp.OK || badDoctorResp.Error == nil || badDoctorResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad doctor response=%+v", badDoctorResp)
	}

	badCreateSpecResp := handler.Handle(context.Background(), specRequest(t, protocol.CommandLearnPromote, protocol.LearnPromoteRequestBody{
		ID:           "learn-1",
		Target:       protocol.LearningPromotionTargetSpec,
		CreateTarget: true,
	}))
	if badCreateSpecResp.OK || badCreateSpecResp.Error == nil || badCreateSpecResp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("bad create spec response=%+v", badCreateSpecResp)
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
