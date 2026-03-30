package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	opmanager "github.com/riordanpawley/azedarach/internal/daemon/operations/manager"
	opstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
)

const (
	defaultOperationProjectID = "default"
	defaultOperationPollDelay = 10 * time.Millisecond
)

type operationRuntimeConfig struct {
	repoDir         string
	logger          *slog.Logger
	hub             *publish.Hub
	nextRevision    func(string) uint64
	sessionStart    func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionStop     func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	gitHandler      *daemonhandlers.GitHandler
	worktreeHandler *daemonhandlers.WorktreeHandler
}

type operationRuntime struct {
	logger          *slog.Logger
	hub             *publish.Hub
	nextRevision    func(string) uint64
	store           *opstore.SQLiteStore
	manager         *opmanager.Manager
	sessionStart    func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionStop     func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	gitHandler      *daemonhandlers.GitHandler
	worktreeHandler *daemonhandlers.WorktreeHandler
	pollInterval    time.Duration
}

type operationCommandExecutor struct {
	runtime *operationRuntime
}

type sessionOperationExecutor struct {
	runtime *operationRuntime
}

type operationStoreAdapter struct {
	repo         opstore.Repository
	hub          *publish.Hub
	nextRevision func(string) uint64
	logger       *slog.Logger
}

type operationResultEnvelope struct {
	OperationID string          `json:"operation_id,omitempty"`
	State       string          `json:"state,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type operationErrorPayload struct {
	Message string `json:"message"`
}

type operationDirectRunner func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)

func newOperationRuntime(cfg operationRuntimeConfig) *operationRuntime {
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}
	repoDir := strings.TrimSpace(cfg.repoDir)
	if normalizedRepoDir, err := appconfig.ResolveProjectRoot(repoDir); err == nil {
		repoDir = normalizedRepoDir
	}
	if repoDir != "" {
		if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil && logger != nil {
			logger.Warn("failed to prepare daemon operation database directory", "repo_dir", repoDir, "error", err)
		}
	}
	store := opstore.New(repoDir, logger)
	adapter := &operationStoreAdapter{
		repo:         store,
		hub:          cfg.hub,
		nextRevision: cfg.nextRevision,
		logger:       logger,
	}
	manager := opmanager.New(adapter, opmanager.Config{})
	return &operationRuntime{
		logger:          logger,
		hub:             cfg.hub,
		nextRevision:    cfg.nextRevision,
		store:           store,
		manager:         manager,
		sessionStart:    cfg.sessionStart,
		sessionStop:     cfg.sessionStop,
		gitHandler:      cfg.gitHandler,
		worktreeHandler: cfg.worktreeHandler,
		pollInterval:    defaultOperationPollDelay,
	}
}

func (r *operationRuntime) HandlesOperationCommands() {}

func (r *operationRuntime) Handle(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	switch req.Command {
	case protocol.CommandOperationSubmit:
		return r.handleOperationSubmit(ctx, req)
	case protocol.CommandOperationGet:
		return r.handleOperationGet(ctx, req)
	case protocol.CommandOperationList:
		return r.handleOperationList(ctx, req)
	case protocol.CommandOperationCancel:
		return r.handleOperationCancel(ctx, req)
	default:
		return r.errorResponse(req, protocol.ErrorCodeUnsupportedCommand, "unsupported operation command")
	}
}

func (r *operationRuntime) Close() error {
	if r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *operationRuntime) StopIntake() error {
	if r.manager == nil {
		return nil
	}
	return r.manager.StopIntake()
}

func (r *operationRuntime) CancelQueued(ctx context.Context, reason string) error {
	if r.manager == nil {
		return nil
	}
	return r.manager.CancelQueued(ctx, reason)
}

func (r *operationRuntime) Drain(ctx context.Context) error {
	if r.manager == nil {
		return nil
	}
	return r.manager.Drain(ctx)
}

func (e operationCommandExecutor) Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) protocol.ResponseEnvelope) protocol.ResponseEnvelope {
	resp, _ := e.runtime.executeLegacy(ctx, req, command, func(execCtx context.Context, _ protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return exec(execCtx), nil
	})
	return resp
}

func (e sessionOperationExecutor) Execute(ctx context.Context, req protocol.RequestEnvelope, command string, exec func(context.Context) (protocol.ResponseEnvelope, error)) (protocol.ResponseEnvelope, error) {
	return e.runtime.executeLegacy(ctx, req, command, func(execCtx context.Context, _ protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		return exec(execCtx)
	})
}

func (r *operationRuntime) executeLegacy(ctx context.Context, req protocol.RequestEnvelope, command string, runner operationDirectRunner) (protocol.ResponseEnvelope, error) {
	submitReq, err := r.buildSubmitRequest(command, req.Meta.ProjectID, req.Body, operationSubmitOverrides{})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	completed := make(chan protocol.ResponseEnvelope, 1)
	submitResult, submitErr := r.manager.Submit(ctx, submitReq, func(runCtx context.Context) ([]byte, error) {
		resp, runErr := runner(runCtx, req)
		if runErr != nil {
			resp = r.errorResponse(req, protocol.ErrorCodeInternal, runErr.Error())
		}
		select {
		case completed <- resp:
		default:
		}
		if !resp.OK {
			if resp.Error != nil {
				return nil, errors.New(resp.Error.Message)
			}
			return nil, errors.New("operation failed")
		}
		return append([]byte(nil), resp.Body...), nil
	})
	if submitErr != nil {
		return r.errorResponse(req, mapOperationSubmitErrorCode(submitErr), submitErr.Error()), nil
	}

	terminal, waitErr := r.waitForTerminal(ctx, submitResult.Record.ID)
	if waitErr != nil {
		return r.errorResponse(req, mapOperationSubmitErrorCode(waitErr), waitErr.Error()), nil
	}

	select {
	case resp := <-completed:
		if resp.OK {
			return r.wrapLegacySuccess(req, terminal, resp.Body), nil
		}
		if resp.Error != nil {
			return r.errorResponse(req, resp.Error.Code, resp.Error.Message), nil
		}
		return r.errorResponse(req, protocol.ErrorCodeInternal, "operation failed"), nil
	default:
	}

	if terminal.State == daemonops.StateDone {
		return r.wrapLegacySuccess(req, terminal, terminal.ResultPayload), nil
	}
	return r.errorResponse(req, mapOperationRecordErrorCode(terminal), operationErrorMessage(terminal)), nil
}

func (r *operationRuntime) handleOperationSubmit(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationSubmitRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	submitReq, err := r.buildSubmitRequest(body.Kind, coalesceProjectID(body.ProjectID, req.Meta.ProjectID), body.Payload, operationSubmitOverrides{
		IssueID:      body.IssueID,
		DedupeKey:    body.DedupeKey,
		ResourceKeys: body.ResourceKeys,
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error())
	}
	runner, err := r.directRunnerForKind(body.Kind)
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error())
	}
	submitResult, submitErr := r.manager.Submit(ctx, submitReq, func(runCtx context.Context) ([]byte, error) {
		runReq := protocol.RequestEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindCommand,
			Meta: protocol.Metadata{
				ProjectID: coalesceProjectID(body.ProjectID, req.Meta.ProjectID),
			},
			Command: body.Kind,
			Body:    append([]byte(nil), body.Payload...),
		}
		resp, runErr := runner(runCtx, runReq)
		if runErr != nil {
			return nil, runErr
		}
		if !resp.OK {
			if resp.Error != nil {
				return nil, errors.New(resp.Error.Message)
			}
			return nil, errors.New("operation failed")
		}
		return append([]byte(nil), resp.Body...), nil
	})
	if submitErr != nil {
		return r.errorResponse(req, mapOperationSubmitErrorCode(submitErr), submitErr.Error())
	}

	resp := r.successResponse(req)
	record := r.toProtocolRecord(submitResult.Record)
	record.Payload = append(json.RawMessage(nil), body.Payload...)
	encoded, err := json.Marshal(protocol.OperationSubmitResponseBody{
		Created:   !submitResult.Deduped,
		Operation: record,
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation submit response: %v", err))
	}
	resp.Body = encoded
	return resp
}

func (r *operationRuntime) handleOperationGet(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationGetRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	record, err := r.manager.Get(ctx, strings.TrimSpace(body.OperationID))
	if err != nil {
		return r.errorResponse(req, mapOperationStoreErrorCode(err), err.Error())
	}
	resp := r.successResponse(req)
	encoded, err := json.Marshal(protocol.OperationGetResponseBody{Operation: r.toProtocolRecord(record)})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation get response: %v", err))
	}
	resp.Body = encoded
	return resp
}

func (r *operationRuntime) handleOperationList(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationListRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	projectID := coalesceProjectID(body.ProjectID, req.Meta.ProjectID)
	records, err := r.manager.List(ctx, daemonops.Query{
		ProjectID: projectID,
		IssueID:   strings.TrimSpace(body.IssueID),
		Kind:      strings.TrimSpace(body.Kind),
		States:    mapOperationStates(body.States),
		Limit:     body.Limit,
	})
	if err != nil {
		return r.errorResponse(req, mapOperationStoreErrorCode(err), err.Error())
	}
	operations := make([]protocol.OperationRecord, 0, len(records))
	for _, record := range records {
		operations = append(operations, r.toProtocolRecord(record))
	}
	resp := r.successResponse(req)
	encoded, err := json.Marshal(protocol.OperationListResponseBody{
		ProjectID:  projectID,
		Operations: operations,
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation list response: %v", err))
	}
	resp.Body = encoded
	return resp
}

func (r *operationRuntime) handleOperationCancel(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationCancelRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	record, err := r.manager.Cancel(ctx, strings.TrimSpace(body.OperationID), strings.TrimSpace(body.Reason))
	if err != nil {
		return r.errorResponse(req, mapOperationStoreErrorCode(err), err.Error())
	}
	fresh, getErr := r.manager.Get(ctx, record.ID)
	if getErr == nil {
		record = fresh
	}
	resp := r.successResponse(req)
	encoded, err := json.Marshal(protocol.OperationCancelResponseBody{
		Cancelled: true,
		Operation: r.toProtocolRecord(record),
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation cancel response: %v", err))
	}
	resp.Body = encoded
	return resp
}

type operationSubmitOverrides struct {
	IssueID      string
	DedupeKey    string
	ResourceKeys []string
}

func (r *operationRuntime) buildSubmitRequest(kind, projectID string, payload []byte, overrides operationSubmitOverrides) (daemonops.SubmitRequest, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return daemonops.SubmitRequest{}, errors.New("missing required fields: kind")
	}
	if _, err := r.directRunnerForKind(kind); err != nil {
		return daemonops.SubmitRequest{}, err
	}
	projectID = coalesceProjectID(projectID, "")
	issueID, resourceKeys, dedupeKey, err := deriveOperationRouting(kind, projectID, payload)
	if err != nil {
		return daemonops.SubmitRequest{}, err
	}
	if overrideIssueID := strings.TrimSpace(overrides.IssueID); overrideIssueID != "" {
		issueID = overrideIssueID
	}
	if len(overrides.ResourceKeys) > 0 {
		resourceKeys = normalizeOperationResourceKeys(overrides.ResourceKeys)
	}
	if overrideDedupeKey := strings.TrimSpace(overrides.DedupeKey); overrideDedupeKey != "" {
		dedupeKey = overrideDedupeKey
	}
	if len(resourceKeys) == 0 {
		resourceKeys = []string{"operation:" + kind}
	}
	if issueID == "" {
		issueID = resourceKeys[0]
	}
	return daemonops.SubmitRequest{
		ProjectID:          projectID,
		IssueID:            issueID,
		Kind:               kind,
		DedupeKey:          dedupeKey,
		ResourceKeys:       resourceKeys,
		RecentDedupeWindow: 30 * time.Second,
	}, nil
}

func deriveOperationRouting(kind, projectID string, payload []byte) (issueID string, resourceKeys []string, dedupeKey string, err error) {
	projectID = coalesceProjectID(projectID, "")
	switch kind {
	case "session.start", "session.stop":
		var body struct {
			ProjectID string `json:"project_id"`
			SessionID string `json:"session_id"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		projectID = coalesceProjectID(body.ProjectID, projectID)
		issueID = strings.TrimSpace(body.SessionID)
		if issueID == "" {
			return "", nil, "", errors.New("missing required fields: project_id/session_id")
		}
		resourceKeys = []string{"issue:" + projectID + ":" + issueID}
		dedupeKey = kind + ":" + issueID
		return issueID, resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandGitFetch:
		var body struct {
			Worktree string `json:"worktree"`
			Remote   string `json:"remote"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		if strings.TrimSpace(body.Worktree) == "" || strings.TrimSpace(body.Remote) == "" {
			return "", nil, "", errors.New("missing required fields: worktree/remote")
		}
		issueID = body.Worktree
		resourceKeys = []string{"worktree:" + body.Worktree}
		dedupeKey = kind + ":" + body.Worktree + ":" + body.Remote
		return issueID, resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandGitMerge, daemonhandlers.CommandGitCheckout:
		var body struct {
			Worktree string `json:"worktree"`
			Branch   string `json:"branch"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		if strings.TrimSpace(body.Worktree) == "" || strings.TrimSpace(body.Branch) == "" {
			return "", nil, "", errors.New("missing required fields: worktree/branch")
		}
		issueID = body.Worktree
		resourceKeys = []string{"worktree:" + body.Worktree}
		dedupeKey = kind + ":" + body.Worktree + ":" + body.Branch
		return issueID, resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandGitAbortMerge:
		var body struct {
			Worktree string `json:"worktree"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		if strings.TrimSpace(body.Worktree) == "" {
			return "", nil, "", errors.New("missing required fields: worktree")
		}
		issueID = body.Worktree
		resourceKeys = []string{"worktree:" + body.Worktree}
		dedupeKey = kind + ":" + body.Worktree
		return issueID, resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandWorktreeCreate, daemonhandlers.CommandWorktreeRemove:
		var body struct {
			ProjectID string `json:"project_id"`
			IssueID   string `json:"issue_id"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		projectID = coalesceProjectID(body.ProjectID, projectID)
		issueID = strings.TrimSpace(body.IssueID)
		if issueID == "" {
			return "", nil, "", errors.New("missing required fields: project_id/issue_id")
		}
		resourceKeys = []string{"issue:" + projectID + ":" + issueID}
		dedupeKey = kind + ":" + issueID
		return issueID, resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandWorktreeCleanupOrphaned:
		var body struct {
			ProjectID string `json:"project_id"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		projectID = coalesceProjectID(body.ProjectID, projectID)
		issueID = "__project__"
		resourceKeys = []string{"project:" + projectID + ":worktree.cleanup"}
		dedupeKey = kind + ":" + projectID
		return issueID, resourceKeys, dedupeKey, nil
	default:
		return "", nil, "", fmt.Errorf("unsupported operation kind: %s", kind)
	}
}

func (r *operationRuntime) directRunnerForKind(kind string) (operationDirectRunner, error) {
	switch kind {
	case "session.start":
		if r.sessionStart == nil {
			return nil, errors.New("session.start handler unavailable")
		}
		return r.sessionStart, nil
	case "session.stop":
		if r.sessionStop == nil {
			return nil, errors.New("session.stop handler unavailable")
		}
		return r.sessionStop, nil
	case daemonhandlers.CommandGitFetch,
		daemonhandlers.CommandGitMerge,
		daemonhandlers.CommandGitCheckout,
		daemonhandlers.CommandGitAbortMerge:
		if r.gitHandler == nil {
			return nil, errors.New("git handler unavailable")
		}
		return func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return r.gitHandler.HandleDirect(ctx, req), nil
		}, nil
	case daemonhandlers.CommandWorktreeCreate,
		daemonhandlers.CommandWorktreeRemove,
		daemonhandlers.CommandWorktreeCleanupOrphaned:
		if r.worktreeHandler == nil {
			return nil, errors.New("worktree handler unavailable")
		}
		return func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			return r.worktreeHandler.HandleDirect(ctx, req), nil
		}, nil
	default:
		return nil, fmt.Errorf("unsupported operation kind: %s", kind)
	}
}

func (r *operationRuntime) waitForTerminal(ctx context.Context, operationID string) (daemonops.Record, error) {
	record, err := r.manager.Get(ctx, operationID)
	if err != nil {
		return daemonops.Record{}, err
	}
	if isOperationTerminal(record.State) {
		return record, nil
	}
	pollInterval := r.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultOperationPollDelay
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return daemonops.Record{}, ctx.Err()
		case <-ticker.C:
			record, err = r.manager.Get(ctx, operationID)
			if err != nil {
				return daemonops.Record{}, err
			}
			if isOperationTerminal(record.State) {
				return record, nil
			}
		}
	}
}

func (r *operationRuntime) wrapLegacySuccess(req protocol.RequestEnvelope, record daemonops.Record, result []byte) protocol.ResponseEnvelope {
	resp := r.successResponse(req)
	body, err := json.Marshal(operationResultEnvelope{
		OperationID: record.ID,
		State:       string(record.State),
		Result:      append(json.RawMessage(nil), result...),
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation envelope: %v", err))
	}
	resp.Body = body
	return resp
}

func (r *operationRuntime) successResponse(req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     time.Now().UTC(),
		OK:              true,
	}
}

func (r *operationRuntime) errorResponse(req protocol.RequestEnvelope, code protocol.ErrorCode, message string) protocol.ResponseEnvelope {
	resp := r.successResponse(req)
	resp.OK = false
	resp.Error = &protocol.ErrorEnvelope{Code: code, Message: message, Retryable: code.Retryable()}
	return resp
}

func (r *operationRuntime) toProtocolRecord(record daemonops.Record) protocol.OperationRecord {
	out := protocol.OperationRecord{
		OperationID:  record.ID,
		ProjectID:    record.ProjectID,
		IssueID:      record.IssueID,
		Kind:         record.Kind,
		DedupeKey:    record.DedupeKey,
		ResourceKeys: append([]string(nil), record.ResourceKeys...),
		State:        protocol.OperationState(record.State),
		Result:       append(json.RawMessage(nil), record.ResultPayload...),
		EnqueuedAt:   record.CreatedAt,
		StartedAt:    record.StartedAt,
		FinishedAt:   record.FinishedAt,
	}
	if record.ErrorMessage != "" {
		out.Error = &protocol.OperationError{
			Code:      mapOperationRecordErrorCode(record),
			Message:   record.ErrorMessage,
			Retryable: mapOperationRecordErrorCode(record).Retryable(),
		}
	}
	return out
}

func (s *operationStoreAdapter) Create(ctx context.Context, record daemonops.Record) (daemonops.Record, error) {
	created, err := s.repo.Create(ctx, opstore.CreateParams{
		OperationID:  record.ID,
		ProjectID:    coalesceProjectID(record.ProjectID, ""),
		IssueID:      sanitizeOperationIssueID(record),
		Kind:         record.Kind,
		DedupeKey:    record.DedupeKey,
		ResourceKeys: sanitizeOperationResourceKeys(record.ResourceKeys, record.Kind),
		State:        toStoreState(record.State),
		SubmittedAt:  record.CreatedAt,
		StartedAt:    record.StartedAt,
		FinishedAt:   record.FinishedAt,
		ResultJSON:   append(json.RawMessage(nil), record.ResultPayload...),
		ErrorJSON:    marshalOperationErrorJSON(record.ErrorMessage),
	})
	if err != nil {
		return daemonops.Record{}, err
	}
	out := fromStoreRecord(created)
	s.publish(out)
	return out, nil
}

func (s *operationStoreAdapter) Get(ctx context.Context, operationID string) (daemonops.Record, error) {
	record, err := s.repo.Get(ctx, operationID)
	if err != nil {
		return daemonops.Record{}, err
	}
	return fromStoreRecord(record), nil
}

func (s *operationStoreAdapter) List(ctx context.Context, query daemonops.Query) ([]daemonops.Record, error) {
	records, err := s.repo.List(ctx, opstore.Query{
		ProjectID: query.ProjectID,
		IssueID:   query.IssueID,
		Kind:      query.Kind,
		States:    toStoreStates(query.States),
		Limit:     query.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]daemonops.Record, 0, len(records))
	for _, record := range records {
		out = append(out, fromStoreRecord(record))
	}
	return out, nil
}

func (s *operationStoreAdapter) Update(ctx context.Context, params daemonops.UpdateParams) (daemonops.Record, error) {
	updated, err := s.repo.Transition(ctx, opstore.TransitionParams{
		OperationID: params.ID,
		ToState:     toStoreState(params.ToState),
		StartedAt:   params.StartedAt,
		FinishedAt:  params.FinishedAt,
		ResultJSON:  append(json.RawMessage(nil), params.ResultPayload...),
		ErrorJSON:   marshalOperationErrorJSON(derefString(params.ErrorMessage)),
	})
	if err != nil {
		return daemonops.Record{}, err
	}
	out := fromStoreRecord(updated)
	s.publish(out)
	return out, nil
}

func (s *operationStoreAdapter) publish(record daemonops.Record) {
	if s.hub == nil || s.nextRevision == nil {
		return
	}
	eventName := operationEventName(record.State)
	if eventName == "" {
		return
	}
	body, err := json.Marshal(protocol.OperationEventBody{Operation: daemonOperationRecord(record)})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("marshal operation event body failed", "operation_id", record.ID, "error", err)
		}
		return
	}
	projectID := coalesceProjectID(record.ProjectID, "")
	s.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       projectID,
		Meta:            protocol.Metadata{ProjectID: projectID},
		Revision:        s.nextRevision(projectID),
		Event:           eventName,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
}

func daemonOperationRecord(record daemonops.Record) protocol.OperationRecord {
	state := protocol.OperationState(record.State)
	out := protocol.OperationRecord{
		OperationID:  record.ID,
		ProjectID:    record.ProjectID,
		IssueID:      record.IssueID,
		Kind:         record.Kind,
		DedupeKey:    record.DedupeKey,
		ResourceKeys: append([]string(nil), record.ResourceKeys...),
		State:        state,
		Result:       append(json.RawMessage(nil), record.ResultPayload...),
		EnqueuedAt:   record.CreatedAt,
		StartedAt:    record.StartedAt,
		FinishedAt:   record.FinishedAt,
	}
	if record.ErrorMessage != "" {
		code := mapOperationRecordErrorCode(record)
		out.Error = &protocol.OperationError{Code: code, Message: record.ErrorMessage, Retryable: code.Retryable()}
	}
	return out
}

func fromStoreRecord(record opstore.Record) daemonops.Record {
	return daemonops.Record{
		ID:            record.OperationID,
		ProjectID:     record.ProjectID,
		IssueID:       record.IssueID,
		Kind:          record.Kind,
		DedupeKey:     record.DedupeKey,
		ResourceKeys:  append([]string(nil), record.ResourceKeys...),
		State:         daemonops.State(record.State),
		CreatedAt:     record.SubmittedAt,
		UpdatedAt:     record.UpdatedAt,
		StartedAt:     record.StartedAt,
		FinishedAt:    record.FinishedAt,
		ErrorMessage:  unmarshalOperationErrorMessage(record.ErrorJSON),
		ResultPayload: append([]byte(nil), record.ResultJSON...),
	}
}

func toStoreState(state daemonops.State) opstore.State {
	return opstore.State(state)
}

func toStoreStates(states []daemonops.State) []opstore.State {
	out := make([]opstore.State, 0, len(states))
	for _, state := range states {
		out = append(out, toStoreState(state))
	}
	return out
}

func mapOperationStates(states []protocol.OperationState) []daemonops.State {
	out := make([]daemonops.State, 0, len(states))
	for _, state := range states {
		out = append(out, daemonops.State(state))
	}
	return out
}

func operationEventName(state daemonops.State) string {
	switch state {
	case daemonops.StateQueued:
		return protocol.EventOperationQueued
	case daemonops.StateRunning:
		return protocol.EventOperationRunning
	case daemonops.StateDone:
		return protocol.EventOperationDone
	case daemonops.StateFailed:
		return protocol.EventOperationFailed
	case daemonops.StateCancelled:
		return protocol.EventOperationCancelled
	default:
		return ""
	}
}

func mapOperationSubmitErrorCode(err error) protocol.ErrorCode {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return protocol.ErrorCodeTimeout
	case errors.Is(err, context.Canceled):
		return protocol.ErrorCodeUnavailable
	case errors.Is(err, daemonops.ErrIntakeClosed):
		return protocol.ErrorCodeUnavailable
	case errors.Is(err, daemonops.ErrInvalidOperation):
		return protocol.ErrorCodeInvalidRequest
	case errors.Is(err, daemonops.ErrOperationActive):
		return protocol.ErrorCodeConflict
	default:
		return protocol.ErrorCodeInternal
	}
}

func mapOperationStoreErrorCode(err error) protocol.ErrorCode {
	switch {
	case errors.Is(err, daemonops.ErrNotFound), errors.Is(err, opstore.ErrNotFound):
		return protocol.ErrorCodeInvalidRequest
	case errors.Is(err, daemonops.ErrInvalidOperation), errors.Is(err, opstore.ErrInvalidTransition):
		return protocol.ErrorCodeInvalidRequest
	case errors.Is(err, daemonops.ErrIntakeClosed):
		return protocol.ErrorCodeUnavailable
	case errors.Is(err, opstore.ErrConflict), errors.Is(err, daemonops.ErrOperationActive):
		return protocol.ErrorCodeConflict
	default:
		return protocol.ErrorCodeInternal
	}
}

func mapOperationRecordErrorCode(record daemonops.Record) protocol.ErrorCode {
	switch record.State {
	case daemonops.StateCancelled:
		return protocol.ErrorCodeUnavailable
	case daemonops.StateFailed:
		return protocol.ErrorCodeInternal
	default:
		return protocol.ErrorCodeUnknown
	}
}

func operationErrorMessage(record daemonops.Record) string {
	if record.ErrorMessage != "" {
		return record.ErrorMessage
	}
	switch record.State {
	case daemonops.StateCancelled:
		return "operation cancelled"
	case daemonops.StateFailed:
		return "operation failed"
	default:
		return "operation unavailable"
	}
}

func marshalOperationErrorJSON(message string) json.RawMessage {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	body, err := json.Marshal(operationErrorPayload{Message: message})
	if err != nil {
		return nil
	}
	return body
}

func unmarshalOperationErrorMessage(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var body operationErrorPayload
	if err := json.Unmarshal(payload, &body); err == nil {
		return body.Message
	}
	return strings.TrimSpace(string(payload))
}

func sanitizeOperationIssueID(record daemonops.Record) string {
	if issueID := strings.TrimSpace(record.IssueID); issueID != "" {
		return issueID
	}
	for _, key := range record.ResourceKeys {
		if key = strings.TrimSpace(key); key != "" {
			return key
		}
	}
	return "operation:" + record.Kind
}

func sanitizeOperationResourceKeys(keys []string, kind string) []string {
	keys = normalizeOperationResourceKeys(keys)
	if len(keys) > 0 {
		return keys
	}
	return []string{"operation:" + kind}
}

func normalizeOperationResourceKeys(keys []string) []string {
	uniq := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		if _, ok := uniq[trimmed]; ok {
			continue
		}
		uniq[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func coalesceProjectID(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return defaultOperationProjectID
}

func isOperationTerminal(state daemonops.State) bool {
	switch state {
	case daemonops.StateDone, daemonops.StateFailed, daemonops.StateCancelled:
		return true
	default:
		return false
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
