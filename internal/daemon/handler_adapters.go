package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/daemon/lifecycle"
	daemonstate "github.com/riordanpawley/azedarach/internal/daemon/state"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"

	"github.com/riordanpawley/azedarach/internal/services/git"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type issueSpecService struct {
	daemon *Daemon
}

type issueLearnService struct {
	daemon *Daemon
}

func (s issueSpecService) issueClient(ctx context.Context) (*issues.Client, error) {
	if s.daemon == nil {
		return nil, errors.New("issue store unavailable")
	}
	client := s.daemon.issueClientForProject(daemonProjectIDFromContext(ctx))
	if client == nil {
		return nil, errors.New("issue store unavailable")
	}
	return client, nil
}

func (s issueLearnService) issueClient(ctx context.Context) (*issues.Client, error) {
	if s.daemon == nil {
		return nil, errors.New("issue store unavailable")
	}
	client := s.daemon.issueClientForProject(daemonProjectIDFromContext(ctx))
	if client == nil {
		return nil, errors.New("issue store unavailable")
	}
	return client, nil
}

func (s issueLearnService) Add(ctx context.Context, req protocol.LearnAddRequestBody) (protocol.LearnAddResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnAddResponseBody{}, err
	}
	params := issues.CreateLearningParams{
		ProjectID:       firstNonEmptyDaemon(req.ProjectID, daemonProjectIDFromContext(ctx)),
		Summary:         req.Summary,
		Evidence:        req.Evidence,
		EvidencePrivate: req.Private,
		Tags:            req.Tags,
		Files:           req.Files,
	}
	if req.IssueID != "" {
		issueID := req.IssueID.String()
		params.IssueID = &issueID
	}
	if req.ReqID != "" {
		reqID := req.ReqID.String()
		params.RequirementID = &reqID
	}
	if req.SessionID != "" {
		sessionID := req.SessionID.String()
		params.SessionID = &sessionID
	}
	learning, err := client.CreateLearning(ctx, params)
	if err != nil {
		return protocol.LearnAddResponseBody{}, err
	}
	return protocol.LearnAddResponseBody{Learning: mapLearningToProtocol(learning, true)}, nil
}

func (s issueLearnService) Recall(ctx context.Context, req protocol.LearnRecallRequestBody) (protocol.LearnRecallResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnRecallResponseBody{}, err
	}
	statuses := make([]issues.LearningStatus, 0, len(req.Statuses))
	for _, status := range req.Statuses {
		statuses = append(statuses, issues.LearningStatus(status))
	}
	if len(statuses) == 0 {
		statuses = []issues.LearningStatus{issues.LearningStatusAccepted, issues.LearningStatusPromoted}
	}
	filter := issues.LearningFilter{
		ProjectID:       firstNonEmptyDaemon(req.ProjectID, daemonProjectIDFromContext(ctx)),
		IssueID:         req.IssueID.String(),
		RequirementID:   req.ReqID.String(),
		ContextIssueID:  req.ContextIssueID.String(),
		ContextReqID:    req.ContextReqID.String(),
		ContextTags:     req.ContextTags,
		ContextFiles:    req.ContextFiles,
		Query:           req.Query,
		Statuses:        statuses,
		Tags:            req.Tags,
		Files:           req.Files,
		Limit:           req.Limit,
		IncludeEvidence: req.IncludeEvidence,
		ExcludePrivate:  !req.IncludePrivate,
		ActiveOnly:      true,
	}
	if filter.Limit == 0 {
		filter.Limit = 5
	}
	rows, err := client.ListLearnings(ctx, filter)
	if err != nil {
		return protocol.LearnRecallResponseBody{}, err
	}
	out := make([]protocol.Learning, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapLearningToProtocol(row, req.IncludeEvidence))
	}
	return protocol.LearnRecallResponseBody{Learnings: out}, nil
}

func (s issueLearnService) Show(ctx context.Context, req protocol.LearnShowRequestBody) (protocol.LearnShowResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnShowResponseBody{}, err
	}
	row, err := client.GetLearning(ctx, req.ID)
	if err != nil {
		return protocol.LearnShowResponseBody{}, err
	}
	return protocol.LearnShowResponseBody{Learning: mapLearningToProtocol(row, true)}, nil
}

func (s issueLearnService) Review(ctx context.Context, req protocol.LearnReviewRequestBody) (protocol.LearnReviewResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnReviewResponseBody{}, err
	}
	ids := append([]string(nil), req.IDs...)
	if req.ID != "" {
		ids = append([]string{req.ID}, ids...)
	}
	ids = uniqueTrimmedStrings(ids)
	if len(ids) > 0 {
		updated, err := client.BulkReviewLearnings(ctx, issues.BulkReviewLearningsParams{
			ProjectID: daemonProjectIDFromContext(ctx),
			IDs:       ids,
			Status:    issues.LearningStatus(req.Status),
			Note:      req.Note,
		})
		if err != nil {
			return protocol.LearnReviewResponseBody{}, err
		}
		mapped := mapLearningsToProtocol(updated, false)
		resp := protocol.LearnReviewResponseBody{UpdatedLearnings: mapped}
		if len(mapped) == 1 {
			resp.Updated = &mapped[0]
		}
		return resp, nil
	}

	filter := learningReviewFilterFromRequest(ctx, req)
	if req.BulkStale {
		filter.Statuses = []issues.LearningStatus{issues.LearningStatusCandidate}
		rows, err := client.ListLearnings(ctx, filter)
		if err != nil {
			return protocol.LearnReviewResponseBody{}, err
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.LocalID)
		}
		if len(ids) == 0 {
			return protocol.LearnReviewResponseBody{}, nil
		}
		updated, err := client.BulkReviewLearnings(ctx, issues.BulkReviewLearningsParams{
			ProjectID: daemonProjectIDFromContext(ctx),
			IDs:       ids,
			Status:    issues.LearningStatusStale,
			Note:      req.Note,
		})
		if err != nil {
			return protocol.LearnReviewResponseBody{}, err
		}
		return protocol.LearnReviewResponseBody{UpdatedLearnings: mapLearningsToProtocol(updated, false)}, nil
	}
	rows, err := client.ListLearnings(ctx, filter)
	if err != nil {
		return protocol.LearnReviewResponseBody{}, err
	}
	return protocol.LearnReviewResponseBody{Learnings: mapLearningsToProtocol(rows, false)}, nil
}

func (s issueLearnService) Stale(ctx context.Context, req protocol.LearnStaleRequestBody) (protocol.LearnStaleResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnStaleResponseBody{}, err
	}
	updated, err := client.UpdateLearningStatus(ctx, req.ID, issues.LearningStatusStale, req.Note)
	if err != nil {
		return protocol.LearnStaleResponseBody{}, err
	}
	return protocol.LearnStaleResponseBody{Learning: mapLearningToProtocol(updated, false)}, nil
}

func (s issueLearnService) Demote(ctx context.Context, req protocol.LearnDemoteRequestBody) (protocol.LearnDemoteResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnDemoteResponseBody{}, err
	}
	updated, err := client.DemoteLearning(ctx, req.ID, req.Note)
	if err != nil {
		return protocol.LearnDemoteResponseBody{}, err
	}
	return protocol.LearnDemoteResponseBody{Learning: mapLearningToProtocol(updated, false)}, nil
}

func (s issueLearnService) Promote(ctx context.Context, req protocol.LearnPromoteRequestBody) (protocol.LearnPromoteResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnPromoteResponseBody{}, err
	}
	target := protocol.LearningPromotionTarget(req.Target)
	targetID := strings.TrimSpace(req.TargetID)
	targetHash := strings.TrimSpace(req.TargetHash)
	targetMetadata := cloneStringMap(req.TargetMetadata)
	if learningPromotionFileBacked(target) {
		current, err := client.GetLearning(ctx, req.ID)
		if err != nil {
			return protocol.LearnPromoteResponseBody{}, err
		}
		if current.Status != issues.LearningStatusAccepted && current.Status != issues.LearningStatusPromoted {
			return protocol.LearnPromoteResponseBody{}, fmt.Errorf("%w: learning must be accepted before promotion", domain.ErrConflict)
		}
		repoDir := s.repoDir(ctx)
		expectedHash := targetHash
		if expectedHash == "" && current.Target != nil && protocol.LearningPromotionTarget(*current.Target) == target && current.TargetID == targetID {
			expectedHash = current.TargetHash
		}
		mappedCurrent := mapLearningToProtocol(current, false)
		mappedCurrent.Target = target
		mappedCurrent.TargetID = targetID
		result, err := upsertManagedGuidanceBlock(repoDir, mappedCurrent, req.Note, expectedHash)
		if err != nil {
			_, _ = client.UpdateLearningTargetState(ctx, req.ID, issues.UpdateLearningTargetStateParams{
				State: stateForManagedGuidanceFailure(result),
			})
			return protocol.LearnPromoteResponseBody{}, err
		}
		targetHash = result.TargetHash
		if targetMetadata == nil {
			targetMetadata = map[string]string{}
		}
		targetMetadata["path"] = result.Path
		targetMetadata["managed_block"] = managedGuidanceBlockKind
		targetMetadata["source_learning_id"] = current.LocalID
	}
	params := issues.PromoteLearningParams{
		Target:               issues.LearningPromotionTarget(target),
		TargetID:             targetID,
		Note:                 req.Note,
		TargetHash:           targetHash,
		TargetMetadata:       targetMetadata,
		CreateTarget:         req.CreateTarget,
		TargetTitle:          req.TargetTitle,
		TargetDescription:    req.TargetDescription,
		DecisionRationale:    req.DecisionRationale,
		DecisionContext:      req.DecisionContext,
		DecisionConsequences: req.DecisionConsequences,
	}
	if req.TargetIssueID != "" {
		issueID := req.TargetIssueID.String()
		params.TargetIssueID = &issueID
	}
	row, err := client.PromoteLearning(ctx, req.ID, params)
	if err != nil {
		return protocol.LearnPromoteResponseBody{}, err
	}
	mapped := mapLearningToProtocol(row, false)
	return protocol.LearnPromoteResponseBody{
		Learning: mapped,
		Guidance: learningPromotionGuidance(mapped),
	}, nil
}

func (s issueLearnService) Retire(ctx context.Context, req protocol.LearnRetireRequestBody) (protocol.LearnRetireResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnRetireResponseBody{}, err
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Note == "" {
		return protocol.LearnRetireResponseBody{}, errors.New("retirement note is required")
	}
	current, err := client.GetLearning(ctx, req.ID)
	if err != nil {
		return protocol.LearnRetireResponseBody{}, err
	}
	if current.Status != issues.LearningStatusPromoted || current.Target == nil {
		return protocol.LearnRetireResponseBody{}, fmt.Errorf("%w: learning must be promoted before retirement", domain.ErrConflict)
	}
	target := protocol.LearningPromotionTarget(*current.Target)
	var updated issues.Learning
	if learningPromotionFileBacked(target) {
		result, err := removeManagedGuidanceBlock(s.repoDir(ctx), mapLearningToProtocol(current, false), current.TargetHash)
		if err != nil {
			_, _ = client.UpdateLearningTargetState(ctx, req.ID, issues.UpdateLearningTargetStateParams{
				State: stateForManagedGuidanceFailure(result),
			})
			return protocol.LearnRetireResponseBody{}, err
		}
		if result.TargetHash != "" {
			current.TargetHash = result.TargetHash
		}
		updated, err = client.UpdateLearningTargetState(ctx, req.ID, issues.UpdateLearningTargetStateParams{
			State:          issues.LearningTargetStateRetired,
			TargetHash:     current.TargetHash,
			TargetMetadata: current.TargetMetadata,
			Note:           req.Note,
		})
		if err != nil {
			return protocol.LearnRetireResponseBody{}, err
		}
	} else {
		updated, err = client.RetireLearningTarget(ctx, req.ID, req.Note)
		if err != nil {
			return protocol.LearnRetireResponseBody{}, err
		}
	}
	mapped := mapLearningToProtocol(updated, false)
	return protocol.LearnRetireResponseBody{
		Learning: mapped,
		Guidance: learningRetireGuidance(mapped),
	}, nil
}

func (s issueLearnService) Relate(ctx context.Context, req protocol.LearnRelateRequestBody) (protocol.LearnRelateResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnRelateResponseBody{}, err
	}
	params := issues.RelateLearningParams{
		Type:             issues.LearningRelationType(req.Type),
		SourceLearningID: req.SourceLearningID,
		TargetLearningID: req.TargetLearningID,
		Note:             req.Note,
		ScopeTags:        req.ScopeTags,
		ScopeFiles:       req.ScopeFiles,
	}
	if req.ScopeIssueID != "" {
		issueID := req.ScopeIssueID.String()
		params.ScopeIssueID = &issueID
	}
	if req.ScopeReqID != "" {
		reqID := req.ScopeReqID.String()
		params.ScopeRequirementID = &reqID
	}
	if req.ScopeSessionID != "" {
		sessionID := req.ScopeSessionID.String()
		params.ScopeSessionID = &sessionID
	}
	relation, err := client.RelateLearning(ctx, params)
	if err != nil {
		return protocol.LearnRelateResponseBody{}, err
	}
	return protocol.LearnRelateResponseBody{Relation: mapLearningRelationToProtocol(relation)}, nil
}

func (s issueLearnService) Supersede(ctx context.Context, req protocol.LearnSupersedeRequestBody) (protocol.LearnSupersedeResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnSupersedeResponseBody{}, err
	}
	params := issues.RelateLearningParams{
		Type:             issues.LearningRelationSupersedes,
		SourceLearningID: req.NewLearningID,
		TargetLearningID: req.OldLearningID,
		Note:             req.Note,
		ScopeTags:        req.ScopeTags,
		ScopeFiles:       req.ScopeFiles,
	}
	if req.ScopeIssueID != "" {
		issueID := req.ScopeIssueID.String()
		params.ScopeIssueID = &issueID
	}
	if req.ScopeReqID != "" {
		reqID := req.ScopeReqID.String()
		params.ScopeRequirementID = &reqID
	}
	if req.ScopeSessionID != "" {
		sessionID := req.ScopeSessionID.String()
		params.ScopeSessionID = &sessionID
	}
	relation, err := client.RelateLearning(ctx, params)
	if err != nil {
		return protocol.LearnSupersedeResponseBody{}, err
	}
	return protocol.LearnSupersedeResponseBody{Relation: mapLearningRelationToProtocol(relation)}, nil
}

func (s issueLearnService) Doctor(ctx context.Context, req protocol.LearnDoctorRequestBody) (protocol.LearnDoctorResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnDoctorResponseBody{}, err
	}
	report, err := client.DoctorLearnings(ctx, issues.LearningMaintenanceParams{
		ProjectID:          firstNonEmptyDaemon(req.ProjectID, daemonProjectIDFromContext(ctx)),
		CandidateOlderThan: learningMaintenanceAge(req.CandidateOlderThanDays),
		InactiveOlderThan:  learningMaintenanceAge(req.InactiveOlderThanDays),
	})
	if err != nil {
		return protocol.LearnDoctorResponseBody{}, err
	}
	findings := mapLearningMaintenanceFindings(report.Findings)
	fileRows, err := client.ListLearnings(ctx, issues.LearningFilter{
		ProjectID: firstNonEmptyDaemon(req.ProjectID, daemonProjectIDFromContext(ctx)),
		Statuses:  []issues.LearningStatus{issues.LearningStatusPromoted},
	})
	if err != nil {
		return protocol.LearnDoctorResponseBody{}, err
	}
	fileFindings := s.doctorManagedGuidanceTargets(ctx, fileRows)
	findings = append(findings, fileFindings...)
	if req.Limit > 0 && len(findings) > req.Limit {
		findings = findings[:req.Limit]
	}
	return protocol.LearnDoctorResponseBody{Findings: findings}, nil
}

func (s issueLearnService) GC(ctx context.Context, req protocol.LearnGCRequestBody) (protocol.LearnGCResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.LearnGCResponseBody{}, err
	}
	report, err := client.GCLearnings(ctx, issues.LearningGCParams{
		LearningMaintenanceParams: issues.LearningMaintenanceParams{
			ProjectID:          firstNonEmptyDaemon(req.ProjectID, daemonProjectIDFromContext(ctx)),
			CandidateOlderThan: learningMaintenanceAge(req.CandidateOlderThanDays),
			InactiveOlderThan:  learningMaintenanceAge(req.InactiveOlderThanDays),
			Limit:              req.Limit,
		},
		Confirm: req.Confirm,
	})
	if err != nil {
		return protocol.LearnGCResponseBody{}, err
	}
	return protocol.LearnGCResponseBody{
		DryRun:  report.DryRun,
		Deleted: mapLearningMaintenanceFindings(report.Deleted),
		Skipped: mapLearningMaintenanceFindings(report.Skipped),
	}, nil
}

func (s issueLearnService) repoDir(ctx context.Context) string {
	if s.daemon == nil {
		return ""
	}
	projectID := daemonProjectIDFromContext(ctx)
	if repoDir := strings.TrimSpace(s.daemon.resolveRepoDirForProjectExact(projectID)); repoDir != "" {
		return repoDir
	}
	if repoDir := strings.TrimSpace(s.daemon.resolveRepoDirForProject(projectID)); repoDir != "" {
		return repoDir
	}
	return strings.TrimSpace(s.daemon.cfg.RepoDir)
}

func (s issueLearnService) doctorManagedGuidanceTargets(ctx context.Context, learnings []issues.Learning) []protocol.LearnMaintenanceFinding {
	if len(learnings) == 0 {
		return nil
	}
	repoDir := s.repoDir(ctx)
	out := make([]protocol.LearnMaintenanceFinding, 0)
	seen := make(map[string]struct{})
	for _, learning := range learnings {
		if _, ok := seen[learning.LocalID]; ok {
			continue
		}
		seen[learning.LocalID] = struct{}{}
		if learning.Target == nil || !learningPromotionFileBacked(protocol.LearningPromotionTarget(*learning.Target)) {
			continue
		}
		if learning.Status != issues.LearningStatusPromoted {
			continue
		}
		if learning.TargetState != "" && learning.TargetState != issues.LearningTargetStateActive {
			continue
		}
		result, err := inspectManagedGuidanceBlock(repoDir, mapLearningToProtocol(learning, false))
		if err == nil {
			continue
		}
		findingType := "drifted_managed_block"
		message := err.Error()
		if result.BlockMissing {
			findingType = "missing_target"
			message = "managed guidance block is missing"
		}
		out = append(out, protocol.LearnMaintenanceFinding{
			Type:       findingType,
			Severity:   "error",
			LearningID: learning.LocalID,
			Message:    message,
			Action:     "inspect the managed block and re-promote or retire explicitly",
			Learning:   mapLearningToProtocol(learning, false),
		})
	}
	return out
}

func learningMaintenanceAge(days int) time.Duration {
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

func mapLearningMaintenanceFindings(findings []issues.LearningMaintenanceFinding) []protocol.LearnMaintenanceFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]protocol.LearnMaintenanceFinding, 0, len(findings))
	for _, finding := range findings {
		out = append(out, protocol.LearnMaintenanceFinding{
			Type:       finding.Type,
			Severity:   finding.Severity,
			LearningID: finding.LearningID,
			Message:    finding.Message,
			Action:     finding.Action,
			Learning:   mapLearningToProtocol(finding.Learning, false),
		})
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func learningReviewFilterFromRequest(ctx context.Context, req protocol.LearnReviewRequestBody) issues.LearningFilter {
	statuses := make([]issues.LearningStatus, 0, len(req.QueueStatuses))
	for _, status := range req.QueueStatuses {
		statuses = append(statuses, issues.LearningStatus(status))
	}
	if len(statuses) == 0 {
		statuses = []issues.LearningStatus{issues.LearningStatusCandidate}
	}
	targetStates := make([]issues.LearningTargetState, 0, len(req.TargetStates))
	for _, state := range req.TargetStates {
		targetStates = append(targetStates, issues.LearningTargetState(state))
	}
	limit := req.Limit
	if limit == 0 {
		limit = 20
	}
	filter := issues.LearningFilter{
		ProjectID:       daemonProjectIDFromContext(ctx),
		IssueID:         req.IssueID.String(),
		RequirementID:   req.ReqID.String(),
		Statuses:        statuses,
		TargetStates:    targetStates,
		Tags:            req.Tags,
		Files:           req.Files,
		Limit:           limit,
		IncludeEvidence: false,
	}
	if req.OlderThanSeconds > 0 {
		cutoff := time.Now().UTC().Add(-time.Duration(req.OlderThanSeconds) * time.Second)
		filter.UpdatedBefore = &cutoff
	}
	return filter
}

func mapLearningsToProtocol(rows []issues.Learning, includeEvidence bool) []protocol.Learning {
	out := make([]protocol.Learning, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapLearningToProtocol(row, includeEvidence))
	}
	return out
}

func uniqueTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stateForManagedGuidanceFailure(result managedGuidanceResult) issues.LearningTargetState {
	if result.BlockMissing {
		return issues.LearningTargetStateMissing
	}
	return issues.LearningTargetStateDrifted
}

func (s issueSpecService) ListRequirements(ctx context.Context, req protocol.SpecRequirementListRequestBody) (protocol.SpecRequirementListResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementListResponseBody{}, err
	}
	filter := issues.RequirementFilter{
		IssueID:    req.IssueID.String(),
		LocalIDs:   requirementIDsToStrings(req.IDs),
		Query:      req.Query,
		QueryMatch: issues.RequirementQueryMatch(req.Match),
		Limit:      req.Limit,
	}
	if req.Status != "" {
		filter.Statuses = []issues.RequirementStatus{issues.RequirementStatus(req.Status)}
	}
	rows, err := client.ListRequirements(ctx, filter)
	if err != nil {
		return protocol.SpecRequirementListResponseBody{}, err
	}
	out := make([]protocol.SpecRequirement, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapRequirementToProtocol(row))
	}
	return protocol.SpecRequirementListResponseBody{Requirements: out}, nil
}

func (s issueSpecService) GetRequirement(ctx context.Context, req protocol.SpecRequirementGetRequestBody) (protocol.SpecRequirementGetResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementGetResponseBody{}, err
	}
	row, err := client.GetRequirement(ctx, req.ID.String())
	if err != nil {
		return protocol.SpecRequirementGetResponseBody{}, err
	}
	return protocol.SpecRequirementGetResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) CreateRequirement(ctx context.Context, req protocol.SpecRequirementCreateRequestBody) (protocol.SpecRequirementCreateResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementCreateResponseBody{}, err
	}
	params := issues.CreateRequirementParams{
		LocalID:     req.ID.String(),
		Title:       req.Title,
		Description: req.Description,
		Status:      issues.RequirementStatusOpen,
	}
	if req.IssueID != "" {
		issueID := req.IssueID.String()
		params.IssueID = &issueID
	}
	row, err := client.CreateRequirement(ctx, params)
	if err != nil {
		return protocol.SpecRequirementCreateResponseBody{}, err
	}
	return protocol.SpecRequirementCreateResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) UpdateRequirement(ctx context.Context, req protocol.SpecRequirementUpdateRequestBody) (protocol.SpecRequirementUpdateResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementUpdateResponseBody{}, err
	}
	params := issues.UpdateRequirementParams{
		Title:       req.Title,
		Description: req.Description,
	}
	if req.Status != nil {
		status := issues.RequirementStatus(*req.Status)
		params.Status = &status
	}
	row, err := client.UpdateRequirement(ctx, req.ID.String(), params)
	if err != nil {
		return protocol.SpecRequirementUpdateResponseBody{}, err
	}
	return protocol.SpecRequirementUpdateResponseBody{Requirement: mapRequirementToProtocol(row)}, nil
}

func (s issueSpecService) DeleteRequirement(ctx context.Context, req protocol.SpecRequirementDeleteRequestBody) (protocol.SpecRequirementDeleteResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecRequirementDeleteResponseBody{}, err
	}
	if err := client.DeleteRequirement(ctx, req.ID.String()); err != nil {
		return protocol.SpecRequirementDeleteResponseBody{}, err
	}
	return protocol.SpecRequirementDeleteResponseBody{
		ID:      req.ID,
		Deleted: true,
	}, nil
}

func (s issueSpecService) ListLinks(ctx context.Context, req protocol.SpecLinkListRequestBody) (protocol.SpecLinkListResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLinkListResponseBody{}, err
	}
	links, err := client.ListSpecLinks(ctx, issues.SpecLinkFilter{
		IssueID:       req.IssueID.String(),
		RequirementID: req.ReqID.String(),
		LinkIDs:       specLinkIDsToStrings(req.IDs),
	})
	if err != nil {
		return protocol.SpecLinkListResponseBody{}, err
	}
	out := make([]protocol.SpecLink, 0, len(links))
	for _, link := range links {
		out = append(out, mapLinkToProtocol(link))
	}
	return protocol.SpecLinkListResponseBody{Links: out}, nil
}

func (s issueSpecService) AddLink(ctx context.Context, req protocol.SpecLinkAddRequestBody) (protocol.SpecLinkAddResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLinkAddResponseBody{}, err
	}
	params := issues.AddSpecLinkParams{
		IssueID:       req.IssueID.String(),
		RequirementID: req.ReqID.String(),
		Role:          issues.LinkRole(req.Role),
	}
	if req.Note != "" {
		params.Note = &req.Note
	}
	link, err := client.AddSpecLink(ctx, params)
	if err != nil {
		return protocol.SpecLinkAddResponseBody{}, err
	}
	return protocol.SpecLinkAddResponseBody{Link: mapLinkToProtocol(link)}, nil
}

func (s issueSpecService) RemoveLink(ctx context.Context, req protocol.SpecLinkRemoveRequestBody) (protocol.SpecLinkRemoveResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLinkRemoveResponseBody{}, err
	}
	if err := client.RemoveSpecLink(ctx, req.IssueID.String(), req.ReqID.String()); err != nil {
		return protocol.SpecLinkRemoveResponseBody{}, err
	}
	return protocol.SpecLinkRemoveResponseBody{
		IssueID: req.IssueID,
		ReqID:   req.ReqID,
		Removed: true,
	}, nil
}

func (s issueSpecService) Read(ctx context.Context, req protocol.SpecReadRequestBody) (protocol.SpecReadResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecReadResponseBody{}, err
	}
	resolvedReqID := req.ReqID
	requirements := make([]issues.Requirement, 0, 1)
	if req.ReqID != "" {
		requirement, err := client.GetRequirement(ctx, req.ReqID.String())
		if err != nil {
			return protocol.SpecReadResponseBody{}, err
		}
		resolvedReqID = naming.RequirementID(requirement.LocalID)
		requirements = append(requirements, requirement)
	}

	if req.ReqID == "" {
		reqFilter := issues.RequirementFilter{IssueID: req.IssueID.String()}
		rows, err := client.ListRequirements(ctx, reqFilter)
		if err != nil {
			return protocol.SpecReadResponseBody{}, err
		}
		requirements = rows
	}
	linkFilter := issues.SpecLinkFilter{IssueID: req.IssueID.String(), RequirementID: resolvedReqID.String()}
	links, err := client.ListSpecLinks(ctx, linkFilter)
	if err != nil {
		return protocol.SpecReadResponseBody{}, err
	}

	// For issue-scoped reads without explicit requirement selector, include requirements
	// referenced by links even when requirement.issue_id is empty.
	if req.ReqID == "" && req.IssueID != "" {
		present := make(map[string]struct{}, len(requirements))
		for _, row := range requirements {
			present[row.LocalID] = struct{}{}
		}
		for _, link := range links {
			if _, ok := present[link.RequirementID]; ok {
				continue
			}
			row, err := client.GetRequirement(ctx, link.RequirementID)
			if err != nil {
				return protocol.SpecReadResponseBody{}, err
			}
			requirements = append(requirements, row)
			present[row.LocalID] = struct{}{}
		}
	}

	outReqs := make([]protocol.SpecRequirement, 0, len(requirements))
	for _, row := range requirements {
		outReqs = append(outReqs, mapRequirementToProtocol(row))
	}
	outLinks := make([]protocol.SpecLink, 0, len(links))
	for _, link := range links {
		outLinks = append(outLinks, mapLinkToProtocol(link))
	}
	return protocol.SpecReadResponseBody{
		Requirements: outReqs,
		Links:        outLinks,
	}, nil
}

func (s issueSpecService) Pack(ctx context.Context, req protocol.SpecPackRequestBody) (protocol.SpecPackResponseBody, error) {
	read, err := s.Read(ctx, protocol.SpecReadRequestBody{
		IssueID: req.IssueID,
		ReqID:   req.ReqID,
	})
	if err != nil {
		return protocol.SpecPackResponseBody{}, err
	}
	stage := req.Stage
	if stage == "" {
		stage = protocol.SpecPackStageBrownfield
	}
	sharding, err := s.loadSpecPackSharding(ctx, read.Requirements)
	if err != nil {
		return protocol.SpecPackResponseBody{}, err
	}
	return protocol.SpecPackResponseBody{
		Stage:        stage,
		IssueID:      req.IssueID,
		Requirements: read.Requirements,
		Links:        read.Links,
		Sharding:     sharding,
		Guidance:     specPackGuidance(stage),
		Gates:        specPackGates(),
	}, nil
}

func (s issueSpecService) loadSpecPackSharding(ctx context.Context, requirements []protocol.SpecRequirement) (protocol.SpecPackSharding, error) {
	sharding := protocol.SpecPackSharding{}
	if s.daemon == nil {
		return sharding, nil
	}

	projectID := daemonProjectIDFromContext(ctx)
	repoDir := strings.TrimSpace(s.daemon.resolveRepoDirForProjectExact(projectID))
	if repoDir == "" {
		repoDir = strings.TrimSpace(s.daemon.resolveRepoDirForProject(projectID))
	}
	if repoDir == "" {
		repoDir = strings.TrimSpace(s.daemon.cfg.RepoDir)
	}
	sourcePath := filepath.Join(repoDir, ".azedarach", "spec-shards.json")
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return specPackShardingMissing(requirements), nil
		}
		return protocol.SpecPackSharding{}, fmt.Errorf("read spec sharding sidecar: %w", err)
	}

	var byRequirement map[naming.RequirementID]protocol.SpecShardEntry
	if err := json.Unmarshal(content, &byRequirement); err != nil {
		return protocol.SpecPackSharding{}, fmt.Errorf("decode spec sharding sidecar: %w", err)
	}

	sharding.ByRequirement = make(map[naming.RequirementID]protocol.SpecShardEntry, len(requirements))
	for _, req := range requirements {
		entry, ok := byRequirement[req.ID]
		if !ok {
			continue
		}
		sharding.ByRequirement[req.ID] = entry
	}
	sharding.SourcePath = ".azedarach/spec-shards.json"
	sharding.Missing = missingShardingRequirementIDs(requirements, sharding.ByRequirement)
	if len(sharding.ByRequirement) == 0 {
		sharding.ByRequirement = nil
	}
	if len(sharding.Missing) == 0 {
		sharding.Missing = nil
	}
	return sharding, nil
}

func specPackShardingMissing(requirements []protocol.SpecRequirement) protocol.SpecPackSharding {
	missing := make([]naming.RequirementID, 0, len(requirements))
	for _, req := range requirements {
		missing = append(missing, req.ID)
	}
	return protocol.SpecPackSharding{Missing: missing}
}

func missingShardingRequirementIDs(requirements []protocol.SpecRequirement, byRequirement map[naming.RequirementID]protocol.SpecShardEntry) []naming.RequirementID {
	missing := make([]naming.RequirementID, 0, len(requirements))
	for _, req := range requirements {
		if _, ok := byRequirement[req.ID]; ok {
			continue
		}
		missing = append(missing, req.ID)
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return missing
}

func (s issueSpecService) Lint(ctx context.Context, _ protocol.SpecLintRequestBody) (protocol.SpecLintResponseBody, error) {
	client, err := s.issueClient(ctx)
	if err != nil {
		return protocol.SpecLintResponseBody{}, err
	}
	requirements, err := client.ListRequirements(ctx, issues.RequirementFilter{})
	if err != nil {
		return protocol.SpecLintResponseBody{}, err
	}
	diagnostics := make([]protocol.SpecDiagnostic, 0)
	for _, req := range requirements {
		links, err := client.ListSpecLinksByRequirementLocalID(ctx, req.LocalID)
		if err != nil {
			return protocol.SpecLintResponseBody{}, err
		}
		if len(links) == 0 {
			diagnostics = append(diagnostics, protocol.SpecDiagnostic{
				Code:     "unlinked_requirement",
				Message:  "requirement has no linked issue",
				Severity: "warning",
				ReqID:    naming.RequirementID(req.LocalID),
			})
		}
	}
	return protocol.SpecLintResponseBody{
		OK:          len(diagnostics) == 0,
		Diagnostics: diagnostics,
	}, nil
}

func (s issueSpecService) Parity(ctx context.Context, req protocol.SpecParityRequestBody) (protocol.SpecParityResponseBody, error) {
	lint, err := s.Lint(ctx, protocol.SpecLintRequestBody{})
	if err != nil {
		return protocol.SpecParityResponseBody{}, err
	}
	findings := make([]protocol.SpecParityFinding, 0, len(lint.Diagnostics))
	for _, d := range lint.Diagnostics {
		findings = append(findings, protocol.SpecParityFinding{
			ReqID:    d.ReqID,
			Severity: d.Severity,
			Message:  d.Message,
		})
	}
	ok := len(findings) == 0
	if req.FailOnOut && !ok {
		return protocol.SpecParityResponseBody{OK: false, Findings: findings}, domain.ErrConflict
	}
	return protocol.SpecParityResponseBody{OK: ok, Findings: findings}, nil
}

func (s issueSpecService) SyncMD(context.Context, protocol.SpecSyncMDRequestBody) (protocol.SpecSyncMDResponseBody, error) {
	return protocol.SpecSyncMDResponseBody{}, daemonhandlers.ErrSpecUnavailable
}

func specPackGuidance(stage protocol.SpecPackStage) []string {
	switch stage {
	case protocol.SpecPackStageGreenfield:
		return []string{
			"Create the smallest source slice that satisfies the listed requirements.",
			"Add implementation and verification links before closing the issue.",
			"Keep generated scaffolding behind tests that prove the requirement behavior.",
		}
	case protocol.SpecPackStageRepair:
		return []string{
			"Compare current source behavior against every listed requirement.",
			"Patch only the divergent behavior and preserve compatible existing contracts.",
			"Record evidence for the repaired requirement links before closing the issue.",
		}
	case protocol.SpecPackStageVerify:
		return []string{
			"Treat source as provisionally complete and focus on verification evidence.",
			"Add or update tests for each listed requirement before marking links verified.",
			"Report unresolved gaps instead of broadening implementation scope silently.",
		}
	default:
		return []string{
			"Inspect existing source first, then reconcile only the requirement gaps.",
			"Preserve established architecture and route authority through existing boundaries.",
			"Update requirement links and issue notes with concrete implementation evidence.",
		}
	}
}

func specPackGates() []string {
	return []string{
		"az spec lint",
		"az spec parity --fail-on-out",
		"go test ./...",
	}
}

func mapRequirementToProtocol(req issues.Requirement) protocol.SpecRequirement {
	out := protocol.SpecRequirement{
		ID:          naming.RequirementID(req.LocalID),
		Title:       req.Title,
		Description: req.Description,
		Status:      protocol.SpecRequirementStatus(req.Status),
	}
	if req.IssueID != nil {
		out.IssueID = naming.IssueID(*req.IssueID)
	}
	return out
}

func mapLinkToProtocol(link issues.SpecLink) protocol.SpecLink {
	out := protocol.SpecLink{
		ID:      naming.SpecLinkID(link.ID),
		IssueID: naming.IssueID(link.IssueID),
		ReqID:   naming.RequirementID(link.RequirementID),
		Role:    protocol.SpecLinkRole(link.Role),
	}
	if link.Note != nil {
		out.Note = *link.Note
	}
	return out
}

func mapLearningToProtocol(learning issues.Learning, includeEvidence bool) protocol.Learning {
	out := protocol.Learning{
		ID:              learning.LocalID,
		ProjectID:       learning.ProjectID,
		Summary:         learning.Summary,
		EvidencePrivate: learning.EvidencePrivate,
		Status:          protocol.LearningStatus(learning.Status),
		ReviewNote:      learning.ReviewNote,
		Tags:            append([]string(nil), learning.Tags...),
		Files:           append([]string(nil), learning.Files...),
		CreatedAt:       formatProtocolTime(learning.CreatedAt),
		UpdatedAt:       formatProtocolTime(learning.UpdatedAt),
		RecallScore:     learning.RecallScore,
		RecallReason:    learning.RecallReason,
	}
	if includeEvidence {
		out.Evidence = learning.Evidence
	}
	if learning.IssueID != nil {
		out.IssueID = naming.IssueID(*learning.IssueID)
	}
	if learning.RequirementID != nil {
		out.ReqID = naming.RequirementID(*learning.RequirementID)
	}
	if learning.SessionID != nil {
		out.SessionID = naming.SessionID(*learning.SessionID)
	}
	if learning.ReviewedAt != nil {
		out.ReviewedAt = formatProtocolTime(*learning.ReviewedAt)
	}
	if learning.Target != nil {
		out.Target = protocol.LearningPromotionTarget(*learning.Target)
	}
	out.TargetID = learning.TargetID
	out.TargetNote = learning.TargetNote
	if learning.PromotedAt != nil {
		out.PromotedAt = formatProtocolTime(*learning.PromotedAt)
	}
	out.TargetState = protocol.LearningTargetState(learning.TargetState)
	out.TargetHash = learning.TargetHash
	if len(learning.TargetMetadata) > 0 {
		out.TargetMetadata = make(map[string]string, len(learning.TargetMetadata))
		for key, value := range learning.TargetMetadata {
			out.TargetMetadata[key] = value
		}
	}
	if learning.ExpiresAt != nil {
		out.ExpiresAt = formatProtocolTime(*learning.ExpiresAt)
	}
	if learning.StaleAt != nil {
		out.StaleAt = formatProtocolTime(*learning.StaleAt)
	}
	if learning.LastRecalledAt != nil {
		out.LastRecalledAt = formatProtocolTime(*learning.LastRecalledAt)
	}
	out.RecallCount = learning.RecallCount
	if learning.SupersededAt != nil {
		out.SupersededAt = formatProtocolTime(*learning.SupersededAt)
	}
	if learning.TargetRetiredAt != nil {
		out.TargetRetiredAt = formatProtocolTime(*learning.TargetRetiredAt)
	}
	if len(learning.Relations) > 0 {
		out.Relations = make([]protocol.LearningRelation, 0, len(learning.Relations))
		for _, relation := range learning.Relations {
			out.Relations = append(out.Relations, mapLearningRelationToProtocol(relation))
		}
	}
	if learning.TargetDriftedAt != nil {
		out.TargetDriftedAt = formatProtocolTime(*learning.TargetDriftedAt)
	}
	return out
}

func mapLearningRelationToProtocol(relation issues.LearningRelation) protocol.LearningRelation {
	out := protocol.LearningRelation{
		ID:               relation.LocalID,
		Type:             protocol.LearningRelationType(relation.Type),
		SourceLearningID: relation.SourceLearningID,
		TargetLearningID: relation.TargetLearningID,
		Note:             relation.Note,
		ScopeTags:        append([]string(nil), relation.ScopeTags...),
		ScopeFiles:       append([]string(nil), relation.ScopeFiles...),
		CreatedAt:        formatProtocolTime(relation.CreatedAt),
		UpdatedAt:        formatProtocolTime(relation.UpdatedAt),
	}
	if relation.ScopeIssueID != nil {
		out.ScopeIssueID = naming.IssueID(*relation.ScopeIssueID)
	}
	if relation.ScopeRequirementID != nil {
		out.ScopeReqID = naming.RequirementID(*relation.ScopeRequirementID)
	}
	if relation.ScopeSessionID != nil {
		out.ScopeSessionID = naming.SessionID(*relation.ScopeSessionID)
	}
	return out
}

func formatProtocolTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func firstNonEmptyDaemon(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func learningPromotionGuidance(learning protocol.Learning) string {
	switch learning.Target {
	case protocol.LearningPromotionTargetDecision:
		return fmt.Sprintf("Promotion recorded against decision %s. Keep the decision linked to the owning issue/requirement.", learning.TargetID)
	case protocol.LearningPromotionTargetSpec:
		return fmt.Sprintf("Promotion recorded against spec requirement %s. Keep the issue/spec link current.", learning.TargetID)
	case protocol.LearningPromotionTargetRulesync:
		return fmt.Sprintf("Promotion wrote an Az-managed guidance block to Rulesync target %s. Refresh generated agent instructions from the source guidance when needed.", learning.TargetID)
	case protocol.LearningPromotionTargetAgents:
		return fmt.Sprintf("Promotion wrote an Az-managed guidance block to AGENTS target %s.", learning.TargetID)
	case protocol.LearningPromotionTargetSkill:
		return fmt.Sprintf("Promotion wrote an Az-managed guidance block to skill target %s.", learning.TargetID)
	default:
		return "Promotion recorded; update the selected curated guidance target and keep this learning id as evidence."
	}
}

func learningRetireGuidance(learning protocol.Learning) string {
	path := strings.TrimSpace(learning.TargetMetadata["path"])
	if path == "" {
		path = strings.TrimSpace(learning.TargetID)
	}
	if learningPromotionFileBacked(learning.Target) && path != "" {
		return fmt.Sprintf("Retired Az-managed guidance block for %s from %s. Human-authored content was preserved.", learning.ID, path)
	}
	return fmt.Sprintf("Retired promoted learning target for %s.", learning.ID)
}

func requirementIDsToStrings(ids []naming.RequirementID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, id.String())
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func specLinkIDsToStrings(ids []naming.SpecLinkID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, id.String())
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapSpecStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrConflict) {
		return err
	}
	return err
}

type worktreeServiceAdapter struct {
	manager                            *git.WorktreeManager
	managerForProject                  func(string) *git.WorktreeManager
	runtimeStateStore                  *daemonstate.RuntimeStateStore
	runtimeStateStoreForProject        func(string) *daemonstate.RuntimeStateStore
	runtimeProjectionWriter            runtimeProjectionWriter
	ensureRuntimeFreshForMutation      func(context.Context, string, string) error
	ensureRuntimeFreshForIssueMutation func(context.Context, string, string, string) error
	runtimeIssueTasks                  func(context.Context, string, []string) map[string]domain.Task
	runWorktreeSyncInit                func(context.Context, worktreeInitContext) error
	startWorktreeAsyncInit             func(worktreeInitContext)
	logger                             *slog.Logger
	pollInterval                       time.Duration
	onProjectionUpdate                 func(ctx context.Context, projectID, issueID, path string)
	onWorktreeObserved                 func(ctx context.Context, projectID, issueID, path string)

	mu      sync.Mutex
	pollers map[string]context.CancelFunc
}

func (a *worktreeServiceAdapter) runtimeStore(projectID string) *daemonstate.RuntimeStateStore {
	if a == nil {
		return nil
	}
	if a.runtimeStateStoreForProject != nil {
		if store := a.runtimeStateStoreForProject(projectID); store != nil {
			return store
		}
	}
	return a.runtimeStateStore
}

func (a *worktreeServiceAdapter) managerFor(projectID string) *git.WorktreeManager {
	if a == nil {
		return nil
	}
	if a.managerForProject != nil {
		if manager := a.managerForProject(projectID); manager != nil {
			return manager
		}
	}
	return a.manager
}

func (a *worktreeServiceAdapter) List(ctx context.Context, projectID string) ([]git.Worktree, error) {
	projectID = normalizedProjectID(projectID)
	manager := a.managerFor(projectID)
	if manager == nil {
		return nil, errors.New("worktree manager unavailable")
	}
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		if a.logger != nil {
			a.logger.Debug("worktree projection store unavailable for projection-only read",
				"project_id", projectID,
			)
		}
		return []git.Worktree{}, nil
	}

	cached, cacheErr := runtimeStore.ListWorktreeStates(ctx, projectID)
	if cacheErr != nil {
		if a.logger != nil {
			a.logger.Debug("worktree list cache read failed", "project_id", projectID, "error", cacheErr)
		}
		return nil, cacheErr
	}
	taskByIssue := a.runtimeIssueTaskSnapshot(ctx, projectID, worktreeIssueIDsFromStates(cached))
	worktrees := filterRuntimeEligibleGitWorktrees(mapProjectionWorktrees(cached), taskByIssue)
	if len(worktrees) > 0 {
		a.observeWorktrees(ctx, projectID, worktrees, taskByIssue)
	}
	a.ensureBackgroundPoller(projectID)
	if a.logger != nil {
		a.logger.Debug("using projection-backed worktree runtime state",
			"project_id", projectID,
			"cached_worktrees", len(worktrees),
		)
	}
	return worktrees, nil
}

func (a *worktreeServiceAdapter) Create(ctx context.Context, projectID string, issueID string, baseBranch string) (*git.Worktree, string, error) {
	manager := a.managerFor(projectID)
	if manager == nil {
		return nil, "", errors.New("worktree manager unavailable")
	}
	if a.ensureRuntimeFreshForIssueMutation != nil {
		if err := a.ensureRuntimeFreshForIssueMutation(ctx, normalizedProjectID(projectID), issueID, daemonhandlers.CommandWorktreeCreate); err != nil {
			return nil, "", err
		}
	}
	taskByIssue := a.runtimeIssueTaskSnapshot(ctx, projectID, []string{issueID})
	title := ""
	parentIssueID := ""
	parentWorktreePath := ""
	if task, ok := taskByIssueIDLocal(taskByIssue, issueID); ok {
		title = task.Title
		ensured, err := ensureAncestorWorktrees(ctx, projectID, task, baseBranch, manager, taskLookupFromMap(taskByIssue), a.runtimeProjectionWriter, func(ctx context.Context, initCtx worktreeInitContext) error {
			if a.runWorktreeSyncInit != nil {
				if err := a.runWorktreeSyncInit(ctx, initCtx); err != nil {
					cleanupNote := cleanupWorktreeAfterInitFailure(ctx, manager, initCtx.IssueID, initCtx.WorktreePath, a.logger)
					return fmt.Errorf("%w%s", err, cleanupNote)
				}
			}
			if a.startWorktreeAsyncInit != nil {
				a.startWorktreeAsyncInit(initCtx)
			}
			return nil
		})
		if err != nil {
			return nil, "", err
		}
		baseBranch = ensured.BaseBranch
		parentIssueID = ensured.AncestorIssueID
		parentWorktreePath = ensured.AncestorWorktreePath
	}
	worktree, created, err := createOrLoadIssueWorktree(ctx, manager, issueID, title, baseBranch)
	if err != nil {
		return nil, "", err
	}
	if created && worktree != nil {
		initCtx := worktreeInitContext{
			ProjectID:          normalizedProjectID(projectID),
			IssueID:            issueID,
			WorktreePath:       worktree.Path,
			ParentIssueID:      parentIssueID,
			ParentWorktreePath: parentWorktreePath,
		}
		if a.runWorktreeSyncInit != nil {
			if err := a.runWorktreeSyncInit(ctx, initCtx); err != nil {
				cleanupNote := cleanupWorktreeAfterInitFailure(ctx, manager, initCtx.IssueID, initCtx.WorktreePath, a.logger)
				return nil, "", fmt.Errorf("%w%s", err, cleanupNote)
			}
		}
		if a.startWorktreeAsyncInit != nil {
			a.startWorktreeAsyncInit(initCtx)
		}
	}
	if a.runtimeStore(projectID) != nil && worktree != nil {
		if !runtimeWorktreeIssueEligible(worktree.IssueID, taskByIssue) {
			return worktree, baseBranch, nil
		}
		if a.runtimeProjectionWriter != nil {
			a.runtimeProjectionWriter.PersistWorktreeProjectionAndPublish(ctx, normalizedProjectID(projectID), worktree.IssueID, worktree.Path, worktree.Branch)
		}
		a.observeWorktrees(ctx, normalizedProjectID(projectID), []git.Worktree{*worktree}, taskByIssue)
	}
	return worktree, baseBranch, nil
}

func cleanupWorktreeAfterInitFailure(ctx context.Context, manager *git.WorktreeManager, issueID, worktreePath string, logger *slog.Logger) string {
	if manager == nil {
		return ""
	}
	if _, err := manager.DeleteWithOptions(ctx, issueID, git.WorktreeDeleteOptions{
		Force:         true,
		BranchCleanup: git.WorktreeBranchCleanupRequired,
	}); err != nil {
		if logger != nil {
			logger.Warn("failed to cleanup worktree after init failure",
				"issue_id", issueID,
				"worktree", worktreePath,
				"error", err,
			)
		}
		return fmt.Sprintf(" (cleanup failed for worktree %s: %v)", worktreePath, err)
	}
	return fmt.Sprintf(" (removed failed worktree %s)", worktreePath)
}

func (a *worktreeServiceAdapter) Delete(ctx context.Context, projectID string, issueID string, force bool) error {
	manager := a.managerFor(projectID)
	if manager == nil {
		return errors.New("worktree manager unavailable")
	}
	projectID = normalizedProjectID(projectID)
	projectedWorktree, hasProjectedWorktree, err := a.projectedWorktreeForIssue(ctx, projectID, issueID)
	if err != nil {
		return err
	}
	opts := git.WorktreeDeleteOptions{
		Force:         force,
		BranchCleanup: git.WorktreeBranchCleanupRequired,
	}
	if hasProjectedWorktree {
		opts.FallbackBranch = projectedWorktree.Branch
	}
	removedWorktree, err := manager.DeleteWithOptions(ctx, issueID, opts)
	if err != nil {
		if errors.Is(err, git.ErrWorktreeNotFound) {
			if a.runtimeProjectionWriter != nil {
				a.runtimeProjectionWriter.DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID)
			}
			return nil
		}
		return err
	}
	if removedWorktree != nil {
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(removedWorktree.Path))
	}
	if a.runtimeProjectionWriter != nil {
		a.runtimeProjectionWriter.DeleteWorktreeProjectionAndPublish(ctx, projectID, issueID)
	}
	return nil
}

func (a *worktreeServiceAdapter) projectedWorktreeForIssue(ctx context.Context, projectID, issueID string) (daemonstate.WorktreeState, bool, error) {
	store := a.runtimeStore(projectID)
	if store == nil {
		return daemonstate.WorktreeState{}, false, nil
	}
	worktree, found, err := store.GetWorktreeStateByIssueID(ctx, projectID, issueID)
	if err != nil {
		return daemonstate.WorktreeState{}, false, fmt.Errorf("read worktree projection for %s: %w", issueID, err)
	}
	return worktree, found, nil
}

func (a *worktreeServiceAdapter) CleanupOrphaned(ctx context.Context, projectID string) (*daemonhandlers.CleanupOrphanedResult, error) {
	manager := a.managerFor(projectID)
	if manager == nil {
		return nil, errors.New("worktree manager unavailable")
	}
	if a.ensureRuntimeFreshForMutation != nil {
		if err := a.ensureRuntimeFreshForMutation(ctx, normalizedProjectID(projectID), daemonhandlers.CommandWorktreeCleanupOrphaned); err != nil {
			return nil, err
		}
	}
	worktrees, err := manager.List(ctx)
	if err != nil {
		return nil, err
	}

	result := &daemonhandlers.CleanupOrphanedResult{
		ProjectID: projectID,
	}
	for _, wt := range worktrees {
		if err := manager.Delete(ctx, wt.IssueID); err != nil {
			result.Skipped = append(result.Skipped, wt)
			continue
		}
		_ = lifecycle.TerminateLockOwner(appconfig.ScopedDaemonLockPath(wt.Path))
		result.Removed = append(result.Removed, wt)
		if a.runtimeProjectionWriter != nil {
			a.runtimeProjectionWriter.DeleteWorktreeProjectionAndPublish(ctx, normalizedProjectID(projectID), wt.IssueID)
		}
	}

	return result, nil
}

func (a *worktreeServiceAdapter) ensureBackgroundPoller(projectID string) {
	if a.runtimeStore(projectID) == nil {
		return
	}
	interval := a.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	projectID = normalizedProjectID(projectID)

	a.mu.Lock()
	if a.pollers == nil {
		a.pollers = make(map[string]context.CancelFunc)
	}
	if _, exists := a.pollers[projectID]; exists {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pollers[projectID] = cancel
	a.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil && a.logger != nil {
				a.logger.Error("worktree background poller panicked", "project_id", projectID, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		a.pollAndPersistWorktrees(ctx, projectID)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.pollAndPersistWorktrees(ctx, projectID)
			}
		}
	}()
}

func (a *worktreeServiceAdapter) pollAndPersistWorktrees(ctx context.Context, projectID string) {
	manager := a.managerFor(projectID)
	if manager == nil {
		return
	}
	var previous []daemonstate.WorktreeState
	if runtimeStore := a.runtimeStore(projectID); runtimeStore != nil {
		if cached, err := runtimeStore.ListWorktreeStates(ctx, projectID); err == nil {
			previous = cached
		}
	}
	worktrees, err := manager.List(ctx)
	if err != nil {
		if a.logger != nil {
			a.logger.Debug("worktree projection poll failed", "project_id", projectID, "error", err)
		}
		return
	}
	taskByIssue := a.runtimeIssueTaskSnapshot(ctx, projectID, worktreeIssueIDsFromGitWorktrees(worktrees))
	a.writeWorktreeProjectionSnapshot(ctx, projectID, worktrees, taskByIssue)
	a.observeWorktrees(ctx, projectID, worktrees, taskByIssue)
	if a.onProjectionUpdate != nil {
		publishWorktreeProjectionDelta(ctx, a.onProjectionUpdate, projectID, previous, filterRuntimeEligibleGitWorktrees(worktrees, taskByIssue))
	}
}

func (a *worktreeServiceAdapter) runtimeIssueTaskSnapshot(ctx context.Context, projectID string, issueIDs []string) map[string]domain.Task {
	if a == nil || a.runtimeIssueTasks == nil {
		return nil
	}
	return a.runtimeIssueTasks(ctx, normalizedProjectID(projectID), issueIDs)
}

func (a *worktreeServiceAdapter) observeWorktrees(ctx context.Context, projectID string, worktrees []git.Worktree, taskByIssue map[string]domain.Task) {
	if a.onWorktreeObserved == nil || len(worktrees) == 0 {
		return
	}
	projectID = normalizedProjectID(projectID)
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		path := strings.TrimSpace(wt.Path)
		if issueID == "" || path == "" {
			continue
		}
		if !runtimeWorktreeIssueEligible(issueID, taskByIssue) {
			continue
		}
		if runtimeStore := a.runtimeStore(projectID); runtimeStore != nil {
			row, found, err := runtimeStore.GetWorktreeStateByIssueID(ctx, projectID, issueID)
			if err != nil {
				if a.logger != nil {
					a.logger.Debug("worktree observation lookup failed", "project_id", projectID, "issue_id", issueID, "error", err)
				}
				continue
			}
			if found && len(row.GitStatusRaw) > 0 {
				continue
			}
		}
		a.onWorktreeObserved(ctx, projectID, issueID, path)
	}
}

func (a *worktreeServiceAdapter) writeWorktreeProjectionSnapshot(ctx context.Context, projectID string, worktrees []git.Worktree, taskByIssue map[string]domain.Task) {
	runtimeStore := a.runtimeStore(projectID)
	if runtimeStore == nil {
		return
	}
	rows := make([]daemonstate.WorktreeState, 0, len(worktrees))
	now := time.Now().UTC()
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		if !runtimeWorktreeIssueEligible(issueID, taskByIssue) {
			continue
		}
		rows = append(rows, daemonstate.WorktreeState{
			ProjectID: normalizedProjectID(projectID),
			IssueID:   issueID,
			Path:      wt.Path,
			Branch:    wt.Branch,
			UpdatedAt: now,
		})
	}
	if a.runtimeProjectionWriter != nil {
		if err := a.runtimeProjectionWriter.ReplaceWorktreeProjectionSnapshot(ctx, normalizedProjectID(projectID), rows); err != nil && a.logger != nil {
			a.logger.Warn("replace worktree projection snapshot failed", "project_id", projectID, "error", err)
		}
		return
	}
	if err := runtimeStore.ReplaceWorktreeStates(ctx, normalizedProjectID(projectID), rows); err != nil && a.logger != nil {
		a.logger.Warn("replace worktree projection snapshot failed", "project_id", projectID, "error", err)
	}
}

func filterRuntimeEligibleGitWorktrees(worktrees []git.Worktree, taskByIssue map[string]domain.Task) []git.Worktree {
	if len(worktrees) == 0 {
		return nil
	}
	out := make([]git.Worktree, 0, len(worktrees))
	for _, wt := range worktrees {
		if runtimeWorktreeIssueEligible(wt.IssueID, taskByIssue) {
			out = append(out, wt)
		}
	}
	return out
}

func mapProjectionWorktrees(projections []daemonstate.WorktreeState) []git.Worktree {
	out := make([]git.Worktree, 0, len(projections))
	for _, projection := range projections {
		out = append(out, git.Worktree{
			Path:    projection.Path,
			Branch:  projection.Branch,
			IssueID: projection.IssueID,
		})
	}
	return out
}

func worktreeIssueIDsFromStates(worktrees []daemonstate.WorktreeState) []string {
	if len(worktrees) == 0 {
		return nil
	}
	ids := make([]string, 0, len(worktrees))
	seen := map[string]struct{}{}
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		key := strings.ToLower(issueID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, issueID)
	}
	return ids
}

func worktreeIssueIDsFromGitWorktrees(worktrees []git.Worktree) []string {
	if len(worktrees) == 0 {
		return nil
	}
	ids := make([]string, 0, len(worktrees))
	seen := map[string]struct{}{}
	for _, wt := range worktrees {
		issueID := strings.TrimSpace(wt.IssueID)
		if issueID == "" {
			continue
		}
		key := strings.ToLower(issueID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, issueID)
	}
	return ids
}

func publishWorktreeProjectionDelta(ctx context.Context, publish func(ctx context.Context, projectID, issueID, path string), projectID string, previous []daemonstate.WorktreeState, next []git.Worktree) {
	prevByIssue := make(map[string]string, len(previous))
	for _, row := range previous {
		prevByIssue[row.IssueID] = row.Path + "|" + row.Branch
	}
	nextByIssue := make(map[string]git.Worktree, len(next))
	for _, row := range next {
		nextByIssue[row.IssueID] = row
	}
	for issueID, wt := range nextByIssue {
		nextKey := wt.Path + "|" + wt.Branch
		if prevByIssue[issueID] != nextKey {
			publish(ctx, projectID, issueID, wt.Path)
		}
		delete(prevByIssue, issueID)
	}
	for issueID := range prevByIssue {
		publish(ctx, projectID, issueID, "")
	}
}

func normalizedProjectID(projectID string) string {
	return protocol.NormalizeProjectID(projectID)
}
