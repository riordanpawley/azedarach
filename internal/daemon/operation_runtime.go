package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	daemonnotices "github.com/riordanpawley/azedarach/internal/daemon/notices"
	daemonops "github.com/riordanpawley/azedarach/internal/daemon/operations"
	opmanager "github.com/riordanpawley/azedarach/internal/daemon/operations/manager"
	opstore "github.com/riordanpawley/azedarach/internal/daemon/operations/store"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/naming"
)

const (
	defaultOperationProjectID   = "default"
	defaultOperationPollDelay   = 10 * time.Millisecond
	interruptedOperationMessage = "operation interrupted by daemon restart"

	heavySessionStartResourcePrefix = "heavy-session-start:"
)

type operationRuntimeConfig struct {
	repoDir                 string
	logger                  *slog.Logger
	hub                     *publish.Hub
	nextRevision            func(string) uint64
	sessionStart            func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionStop             func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionResolveConflict  func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionRestartAll       func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	taskBulkCleanup         func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	globalProjectionRebuild func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	onTerminal              func(context.Context, daemonops.Record)
	recoverInterrupted      func(context.Context, daemonops.Record) (interruptedOperationRecovery, bool)
	gitHandler              *daemonhandlers.GitHandler
	worktreeHandler         *daemonhandlers.WorktreeHandler
	noticeService           *daemonnotices.Service
}

type operationRuntime struct {
	logger                  *slog.Logger
	hub                     *publish.Hub
	nextRevision            func(string) uint64
	store                   *opstore.SQLiteStore
	operationStore          daemonops.Store
	manager                 *opmanager.Manager
	recoverInterrupted      func(context.Context, daemonops.Record) (interruptedOperationRecovery, bool)
	interruptedMu           sync.Mutex
	retryableInterrupted    map[string]struct{}
	sessionStart            func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionStop             func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionResolveConflict  func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	sessionRestartAll       func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	taskBulkCleanup         func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	globalProjectionRebuild func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	gitHandler              *daemonhandlers.GitHandler
	worktreeHandler         *daemonhandlers.WorktreeHandler
	pollInterval            time.Duration
	repoDir                 string
	repoNameProject         string
	canonicalProject        string
}

type operationCommandExecutor struct {
	runtime *operationRuntime
}

type sessionOperationExecutor struct {
	runtime *operationRuntime
}

type operationStoreAdapter struct {
	repo                  opstore.Repository
	hub                   *publish.Hub
	nextRevision          func(string) uint64
	logger                *slog.Logger
	canonicalizeProjectID func(string) string
	noticeService         *daemonnotices.Service
	onTerminal            func(context.Context, daemonops.Record)
}

type operationResultEnvelope struct {
	OperationID string          `json:"operation_id,omitempty"`
	State       string          `json:"state,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

type operationErrorPayload struct {
	Message string `json:"message"`
}

type interruptedOperationRecovery struct {
	State         daemonops.State
	ResultPayload []byte
	ErrorMessage  string
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
	repoNameProjectID := protocol.NormalizeProjectID(filepath.Base(repoDir))
	canonicalProjectID := repoNameProjectID
	if projectID, err := appconfig.ProjectIDForRoot(repoDir); err == nil {
		canonicalProjectID = protocol.NormalizeProjectID(projectID)
	}
	if canonicalProjectID == "" {
		canonicalProjectID = protocol.DefaultProjectID
	}
	canonicalizeProjectID := func(projectID string) string {
		normalized := protocol.NormalizeProjectID(projectID)
		if normalized == canonicalProjectID {
			return canonicalProjectID
		}
		if normalized == protocol.DefaultProjectID {
			return canonicalProjectID
		}
		if repoNameProjectID != "" && normalized == repoNameProjectID {
			return canonicalProjectID
		}
		return normalized
	}
	adapter := &operationStoreAdapter{
		repo:                  store,
		hub:                   cfg.hub,
		nextRevision:          cfg.nextRevision,
		logger:                logger,
		canonicalizeProjectID: canonicalizeProjectID,
		noticeService:         cfg.noticeService,
		onTerminal:            cfg.onTerminal,
	}
	reconcileInterruptedOperations(context.Background(), adapter, logger, cfg.recoverInterrupted)
	retryableInterrupted := make(map[string]struct{})
	if records, err := adapter.List(context.Background(), daemonops.Query{States: []daemonops.State{daemonops.StateQueued, daemonops.StateRunning}}); err != nil {
		logger.Warn("failed to index retryable interrupted daemon operations", "error", err)
	} else {
		for _, record := range records {
			retryableInterrupted[record.ID] = struct{}{}
		}
	}
	manager := opmanager.New(adapter, opmanager.Config{Logger: logger})
	return &operationRuntime{
		logger:                  logger,
		hub:                     cfg.hub,
		nextRevision:            cfg.nextRevision,
		store:                   store,
		operationStore:          adapter,
		manager:                 manager,
		recoverInterrupted:      cfg.recoverInterrupted,
		retryableInterrupted:    retryableInterrupted,
		sessionStart:            cfg.sessionStart,
		sessionStop:             cfg.sessionStop,
		sessionResolveConflict:  cfg.sessionResolveConflict,
		sessionRestartAll:       cfg.sessionRestartAll,
		taskBulkCleanup:         cfg.taskBulkCleanup,
		globalProjectionRebuild: cfg.globalProjectionRebuild,
		gitHandler:              cfg.gitHandler,
		worktreeHandler:         cfg.worktreeHandler,
		pollInterval:            defaultOperationPollDelay,
		repoDir:                 repoDir,
		repoNameProject:         repoNameProjectID,
		canonicalProject:        canonicalProjectID,
	}
}

func reconcileInterruptedOperations(ctx context.Context, store daemonops.Store, logger *slog.Logger, recover func(context.Context, daemonops.Record) (interruptedOperationRecovery, bool)) {
	if store == nil {
		return
	}
	records, err := store.List(ctx, daemonops.Query{
		States: []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
	})
	if err != nil {
		if logger != nil {
			logger.Warn("failed to list interrupted daemon operations", "error", err)
		}
		return
	}
	for _, record := range records {
		finished := time.Now().UTC()
		if recover != nil {
			recoveryCtx := daemonops.WithProgressReporter(ctx, func(progressCtx context.Context, progress daemonops.Progress) error {
				progressCopy := progress
				_, updateErr := store.Update(progressCtx, daemonops.UpdateParams{
					ID: record.ID, ToState: record.State, Progress: &progressCopy,
				})
				return updateErr
			})
			if recovery, ok := recover(recoveryCtx, record); ok {
				if updateInterruptedOperation(ctx, store, record, recovery, finished, logger) {
					continue
				}
			}
		}
		message := interruptedOperationMessage
		if _, err := store.Update(ctx, daemonops.UpdateParams{
			ID:           record.ID,
			ToState:      daemonops.StateFailed,
			FinishedAt:   &finished,
			ErrorMessage: &message,
		}); err != nil && logger != nil {
			logger.Warn("failed to mark interrupted daemon operation failed",
				"operation_id", record.ID,
				"state", record.State,
				"error", err,
			)
		}
	}
}

func updateInterruptedOperation(ctx context.Context, store daemonops.Store, record daemonops.Record, recovery interruptedOperationRecovery, finished time.Time, logger *slog.Logger) bool {
	switch recovery.State {
	case daemonops.StateDone:
		if record.State == daemonops.StateQueued {
			started := finished
			updated, err := store.Update(ctx, daemonops.UpdateParams{
				ID:        record.ID,
				ToState:   daemonops.StateRunning,
				StartedAt: &started,
			})
			if err != nil {
				if logger != nil {
					logger.Warn("failed to advance interrupted daemon operation before recovery",
						"operation_id", record.ID,
						"state", record.State,
						"recovery_state", recovery.State,
						"error", err,
					)
				}
				return false
			}
			record = updated
		}
		if _, err := store.Update(ctx, daemonops.UpdateParams{
			ID:            record.ID,
			ToState:       daemonops.StateDone,
			FinishedAt:    &finished,
			ResultPayload: recovery.ResultPayload,
		}); err != nil {
			if logger != nil {
				logger.Warn("failed to recover interrupted daemon operation",
					"operation_id", record.ID,
					"state", record.State,
					"recovery_state", recovery.State,
					"error", err,
				)
			}
			return false
		}
		return true
	case daemonops.StateFailed:
		message := strings.TrimSpace(recovery.ErrorMessage)
		if message == "" {
			message = interruptedOperationMessage
		}
		if _, err := store.Update(ctx, daemonops.UpdateParams{
			ID:           record.ID,
			ToState:      daemonops.StateFailed,
			FinishedAt:   &finished,
			ErrorMessage: &message,
		}); err != nil {
			if logger != nil {
				logger.Warn("failed to recover interrupted daemon operation",
					"operation_id", record.ID,
					"state", record.State,
					"recovery_state", recovery.State,
					"error", err,
				)
			}
			return false
		}
		return true
	case daemonops.StateQueued, daemonops.StateRunning:
		// Recovery can deliberately retain an interrupted operation as active when
		// its exact side effect is complete but a retryable acknowledgement write
		// has not converged. A matching submission will retry recovery against this
		// durable record before any new runner is admitted.
		return true
	default:
		return false
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
	case protocol.CommandOperationQueue:
		return r.handleOperationQueue(ctx, req)
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
	ctx, endSpan := latencytrace.StartSpan(ctx, "daemon", "operation.execute_legacy", "command", command, "request_id", req.RequestID, "project_id", req.Meta.ProjectID.String())
	var spanErr error
	defer func() { endSpan(spanErr) }()
	submitReq, err := r.buildSubmitRequest(command, req.Meta.ProjectID.String(), req.Body, operationSubmitOverrides{})
	if err != nil {
		spanErr = err
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	completed := make(chan protocol.ResponseEnvelope, 1)
	submitResult, submitErr := r.manager.Submit(ctx, submitReq, func(runCtx context.Context) ([]byte, error) {
		runCtx, endRunSpan := latencytrace.StartSpan(runCtx, "daemon", "operation.run", "command", command, "project_id", req.Meta.ProjectID.String())
		var runSpanErr error
		defer func() { endRunSpan(runSpanErr) }()
		resp, runErr := runner(runCtx, req)
		if runErr != nil {
			runSpanErr = runErr
			resp = r.errorResponse(req, protocol.ErrorCodeInternal, runErr.Error())
		}
		select {
		case completed <- resp:
		default:
		}
		if !resp.OK {
			if err := runCtx.Err(); err != nil {
				runSpanErr = err
				return nil, err
			}
			if resp.Error != nil {
				runSpanErr = errors.New(resp.Error.Message)
				return nil, runSpanErr
			}
			runSpanErr = errors.New("operation failed")
			return nil, runSpanErr
		}
		return append([]byte(nil), resp.Body...), nil
	})
	if submitErr != nil {
		spanErr = submitErr
		return r.errorResponse(req, mapOperationSubmitErrorCode(submitErr), submitErr.Error()), nil
	}

	terminal, waitErr := r.waitForTerminal(ctx, submitResult.Record.ID)
	if waitErr != nil {
		if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
			if record, getErr := r.manager.Get(context.Background(), submitResult.Record.ID); getErr == nil {
				return r.wrapLegacyPending(req, record), nil
			}
		}
		spanErr = waitErr
		return r.errorResponse(req, mapOperationSubmitErrorCode(waitErr), waitErr.Error()), nil
	}

	select {
	case resp := <-completed:
		if resp.OK {
			return r.wrapLegacySuccess(req, terminal, resp.Body), nil
		}
		if resp.Error != nil {
			spanErr = fmt.Errorf("operation response error: %s", resp.Error.Code)
			return r.errorResponse(req, resp.Error.Code, resp.Error.Message), nil
		}
		spanErr = errors.New("operation failed")
		return r.errorResponse(req, protocol.ErrorCodeInternal, "operation failed"), nil
	default:
	}

	if terminal.State == daemonops.StateDone {
		return r.wrapLegacySuccess(req, terminal, terminal.ResultPayload), nil
	}
	spanErr = errors.New(operationErrorMessage(terminal))
	return r.errorResponse(req, mapOperationRecordErrorCode(terminal), operationErrorMessage(terminal)), nil
}

func (r *operationRuntime) handleOperationSubmit(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	ctx, endSpan := latencytrace.StartSpan(ctx, "daemon", "operation.submit", "request_id", req.RequestID, "project_id", req.Meta.ProjectID.String())
	var spanErr error
	defer func() { endSpan(spanErr) }()
	var body protocol.OperationSubmitRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		spanErr = err
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	ctx, endKindSpan := latencytrace.StartSpan(ctx, "daemon", "operation.submit_kind", "command", body.Kind, "project_id", req.Meta.ProjectID.String())
	defer func() { endKindSpan(spanErr) }()
	projectID := r.coalesceProjectID(body.ProjectID.String(), req.Meta.ProjectID.String())
	if r.logger != nil {
		r.logger.Info("daemon operation submit requested",
			"project_id", projectID,
			"kind", body.Kind,
			"issue_id", strings.TrimSpace(body.IssueID.String()),
		)
	}
	submitReq, err := r.buildSubmitRequest(body.Kind, r.coalesceProjectID(body.ProjectID.String(), req.Meta.ProjectID.String()), body.Payload, operationSubmitOverrides{
		IssueID:      body.IssueID,
		DedupeKey:    body.DedupeKey,
		ResourceKeys: body.ResourceKeys,
	})
	if err != nil {
		spanErr = err
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error())
	}
	runner, err := r.directRunnerForKind(body.Kind)
	if err != nil {
		spanErr = err
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error())
	}
	if recovered, found, recoverErr := r.reconcileMatchingInterruptedOperation(ctx, submitReq, body.Payload); recoverErr != nil {
		spanErr = recoverErr
		return r.errorResponse(req, protocol.ErrorCodeInternal, recoverErr.Error())
	} else if found {
		return r.operationSubmitResponse(req, body.Payload, daemonops.SubmitResult{Record: recovered, Deduped: true})
	}
	submitResult, submitErr := r.manager.Submit(ctx, submitReq, func(runCtx context.Context) ([]byte, error) {
		runCtx, endRunSpan := latencytrace.StartSpan(runCtx, "daemon", "operation.run", "command", body.Kind, "project_id", projectID)
		var runSpanErr error
		defer func() { endRunSpan(runSpanErr) }()
		runReq := protocol.RequestEnvelope{
			ProtocolVersion: req.ProtocolVersion,
			RequestID:       req.RequestID,
			Kind:            protocol.EnvelopeKindCommand,
			Meta: protocol.Metadata{
				ProjectID: naming.ProjectID(r.coalesceProjectID(body.ProjectID.String(), req.Meta.ProjectID.String())),
			},
			Command: body.Kind,
			Body:    append([]byte(nil), body.Payload...),
		}
		resp, runErr := runner(runCtx, runReq)
		if runErr != nil {
			runSpanErr = runErr
			return nil, runErr
		}
		if !resp.OK {
			if err := runCtx.Err(); err != nil {
				runSpanErr = err
				return nil, err
			}
			if resp.Error != nil {
				runSpanErr = errors.New(resp.Error.Message)
				return nil, runSpanErr
			}
			runSpanErr = errors.New("operation failed")
			return nil, runSpanErr
		}
		return append([]byte(nil), resp.Body...), nil
	})
	if submitErr != nil {
		spanErr = submitErr
		return r.errorResponse(req, mapOperationSubmitErrorCode(submitErr), submitErr.Error())
	}

	return r.operationSubmitResponse(req, body.Payload, submitResult)
}

func (r *operationRuntime) reconcileMatchingInterruptedOperation(ctx context.Context, req daemonops.SubmitRequest, payload []byte) (daemonops.Record, bool, error) {
	if req.Kind != protocol.CommandSessionRestartAll || r.recoverInterrupted == nil || r.operationStore == nil {
		return daemonops.Record{}, false, nil
	}
	r.interruptedMu.Lock()
	defer r.interruptedMu.Unlock()
	records, err := r.operationStore.List(ctx, daemonops.Query{
		Kind:   req.Kind,
		States: []daemonops.State{daemonops.StateQueued, daemonops.StateRunning},
	})
	if err != nil {
		return daemonops.Record{}, false, fmt.Errorf("list retryable interrupted operations: %w", err)
	}
	var conflictingID string
	for _, record := range records {
		if _, retryable := r.retryableInterrupted[record.ID]; !retryable {
			continue
		}
		if !r.sessionRestartRecoveryRequestMatches(record, req.ProjectID, payload) {
			conflictingID = record.ID
			continue
		}
		recoveryCtx := daemonops.WithProgressReporter(ctx, func(progressCtx context.Context, progress daemonops.Progress) error {
			progressCopy := progress
			_, updateErr := r.operationStore.Update(progressCtx, daemonops.UpdateParams{
				ID: record.ID, ToState: record.State, Progress: &progressCopy,
			})
			return updateErr
		})
		recovery, ok := r.recoverInterrupted(recoveryCtx, record)
		if !ok {
			return record, true, nil
		}
		if !updateInterruptedOperation(ctx, r.operationStore, record, recovery, time.Now().UTC(), r.logger) {
			if current, readErr := r.operationStore.Get(ctx, record.ID); readErr == nil && (current.State == daemonops.StateDone || current.State == daemonops.StateFailed || current.State == daemonops.StateCancelled) {
				delete(r.retryableInterrupted, record.ID)
				return current, true, nil
			}
			return daemonops.Record{}, true, fmt.Errorf("persist retried interrupted operation %s", record.ID)
		}
		updated, err := r.operationStore.Get(ctx, record.ID)
		if err != nil {
			return daemonops.Record{}, true, fmt.Errorf("read retried interrupted operation %s: %w", record.ID, err)
		}
		if updated.State == daemonops.StateDone || updated.State == daemonops.StateFailed || updated.State == daemonops.StateCancelled {
			delete(r.retryableInterrupted, record.ID)
		}
		return updated, true, nil
	}
	if conflictingID != "" {
		return daemonops.Record{}, true, fmt.Errorf("interrupted restart operation %s must be recovered before a different restart request", conflictingID)
	}
	return daemonops.Record{}, false, nil
}

func (r *operationRuntime) sessionRestartRecoveryRequestMatches(record daemonops.Record, submittedProjectID string, payload []byte) bool {
	batch, ok := decodeSessionRestartBatchPlan(record)
	if !ok {
		return false
	}
	var submitted protocol.SessionRestartAllRequestBody
	if err := json.Unmarshal(payload, &submitted); err != nil {
		return false
	}
	if strings.TrimSpace(submitted.ProjectID.String()) != "" {
		submittedProjectID = submitted.ProjectID.String()
	}
	durableProjectID := batch.Request.ProjectID.String()
	if strings.TrimSpace(durableProjectID) == "" {
		durableProjectID = record.ProjectID
	}
	if r.canonicalizeProjectID(submittedProjectID) != r.canonicalizeProjectID(durableProjectID) ||
		submitted.ForceBusy != batch.Request.ForceBusy || submitted.Yolo != batch.Request.Yolo ||
		len(submitted.ImagePaths) != len(batch.Request.ImagePaths) {
		return false
	}
	for i := range submitted.ImagePaths {
		if submitted.ImagePaths[i] != batch.Request.ImagePaths[i] {
			return false
		}
	}
	return true
}

func (r *operationRuntime) operationSubmitResponse(req protocol.RequestEnvelope, payload []byte, submitResult daemonops.SubmitResult) protocol.ResponseEnvelope {
	resp := r.successResponse(req)
	record := r.toProtocolRecord(submitResult.Record)
	record.Payload = append(json.RawMessage(nil), payload...)
	encoded, err := json.Marshal(protocol.OperationSubmitResponseBody{
		Created:   !submitResult.Deduped,
		Operation: record,
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation submit response: %v", err))
	}
	resp.Body = encoded
	if r.logger != nil {
		r.logger.Info("daemon operation submit completed",
			"project_id", submitResult.Record.ProjectID,
			"kind", submitResult.Record.Kind,
			"operation_id", record.OperationID,
			"created", !submitResult.Deduped,
			"state", record.State,
		)
	}
	return resp
}

func (r *operationRuntime) handleOperationGet(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationGetRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	if r.logger != nil {
		r.logger.Info("daemon operation get requested", "operation_id", body.OperationID.String())
	}
	record, err := r.manager.Get(ctx, body.OperationID.String())
	if err != nil {
		return r.errorResponse(req, mapOperationStoreErrorCode(err), err.Error())
	}
	resp := r.successResponse(req)
	encoded, err := json.Marshal(protocol.OperationGetResponseBody{Operation: r.toProtocolRecord(record)})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation get response: %v", err))
	}
	resp.Body = encoded
	if r.logger != nil {
		r.logger.Info("daemon operation get completed",
			"operation_id", record.ID,
			"kind", record.Kind,
			"state", record.State,
			"project_id", record.ProjectID,
		)
	}
	return resp
}

func (r *operationRuntime) handleOperationList(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationListRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	projectID := r.coalesceProjectID(body.ProjectID.String(), req.Meta.ProjectID.String())
	if r.logger != nil {
		r.logger.Info("daemon operation list requested",
			"project_id", projectID,
			"issue_id", strings.TrimSpace(body.IssueID.String()),
			"kind", strings.TrimSpace(body.Kind),
			"limit", body.Limit,
		)
	}
	records, err := r.manager.List(ctx, daemonops.Query{
		ProjectID: projectID,
		IssueID:   strings.TrimSpace(body.IssueID.String()),
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
		ProjectID:  naming.ProjectID(projectID),
		Operations: operations,
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation list response: %v", err))
	}
	resp.Body = encoded
	if r.logger != nil {
		r.logger.Info("daemon operation list completed", "project_id", projectID, "result_count", len(operations))
	}
	return resp
}

func (r *operationRuntime) handleOperationQueue(_ context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationQueueRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	projectID := r.coalesceProjectID(body.ProjectID.String(), req.Meta.ProjectID.String())
	if r.logger != nil {
		r.logger.Info("daemon operation queue requested",
			"project_id", projectID,
			"issue_id", strings.TrimSpace(body.IssueID.String()),
			"kind", strings.TrimSpace(body.Kind),
			"limit", body.Limit,
		)
	}
	snapshot := r.manager.Queue(daemonops.Query{
		ProjectID: projectID,
		IssueID:   strings.TrimSpace(body.IssueID.String()),
		Kind:      strings.TrimSpace(body.Kind),
		States:    mapOperationStates(body.States),
		Limit:     body.Limit,
	})
	resp := r.successResponse(req)
	encoded, err := json.Marshal(protocol.OperationQueueResponseBody{
		ProjectID: naming.ProjectID(projectID),
		Running:   r.toProtocolQueueEntries(snapshot.Running),
		Queued:    r.toProtocolQueueEntries(snapshot.Queued),
	})
	if err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal operation queue response: %v", err))
	}
	resp.Body = encoded
	if r.logger != nil {
		r.logger.Info("daemon operation queue completed",
			"project_id", projectID,
			"running_count", len(snapshot.Running),
			"queued_count", len(snapshot.Queued),
		)
	}
	return resp
}

func (r *operationRuntime) handleOperationCancel(ctx context.Context, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	var body protocol.OperationCancelRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return r.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err))
	}
	if r.logger != nil {
		r.logger.Info("daemon operation cancel requested", "operation_id", body.OperationID.String(), "reason", strings.TrimSpace(body.Reason))
	}
	record, err := r.manager.Cancel(ctx, body.OperationID.String(), strings.TrimSpace(body.Reason))
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
	if r.logger != nil {
		r.logger.Info("daemon operation cancel completed",
			"operation_id", record.ID,
			"kind", record.Kind,
			"state", record.State,
			"project_id", record.ProjectID,
		)
	}
	return resp
}

type operationSubmitOverrides struct {
	IssueID      naming.IssueID
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
	projectID = r.coalesceProjectID(projectID, "")
	issueID, resourceKeys, dedupeKey, err := r.deriveOperationRouting(kind, projectID, payload)
	if err != nil {
		return daemonops.SubmitRequest{}, err
	}
	if overrideIssueID := strings.TrimSpace(overrides.IssueID.String()); overrideIssueID != "" {
		issueID = overrideIssueID
	}
	if len(overrides.ResourceKeys) > 0 {
		resourceKeys = overriddenOperationResourceKeys(kind, resourceKeys, overrides.ResourceKeys)
	}
	if overrideDedupeKey := strings.TrimSpace(overrides.DedupeKey); overrideDedupeKey != "" {
		dedupeKey = overrideDedupeKey
	}
	if len(resourceKeys) == 0 {
		resourceKeys = []string{"operation:" + kind}
	}
	recentDedupeWindow := 30 * time.Second
	if kind == protocol.CommandTaskBulkCleanup || kind == protocol.CommandSessionRestartAll || kind == daemonhandlers.CommandGitMerge {
		// Coalesce identical concurrent submissions, but let a completed batch be
		// retried immediately so per-item failures, lost restart completion checkpoints,
		// and revision-sensitive typed merges can make progress against refreshed authority.
		// Active dedupe still returns the one in-flight operation.
		recentDedupeWindow = 0
	}
	if kind == protocol.CommandGlobalProjectionRebuild {
		recentDedupeWindow = 0
	}
	return daemonops.SubmitRequest{
		ProjectID:          projectID,
		IssueID:            issueID,
		Kind:               kind,
		DedupeKey:          dedupeKey,
		ResourceKeys:       resourceKeys,
		RecentDedupeWindow: recentDedupeWindow,
	}, nil
}

func heavySessionStartResourceKey(projectID string) string {
	return heavySessionStartResourcePrefix + coalesceProjectID(projectID)
}

func (r *operationRuntime) sessionNamingScope(projectID string) string {
	projectID = r.coalesceProjectID(projectID, "")
	switch projectID {
	case "", protocol.DefaultProjectID, r.canonicalProject, r.repoNameProject:
		if repoDir := strings.TrimSpace(r.repoDir); repoDir != "" {
			return repoDir
		}
	}
	return projectID
}

func (r *operationRuntime) deriveOperationRouting(kind, projectID string, payload []byte) (issueID string, resourceKeys []string, dedupeKey string, err error) {
	projectID = r.coalesceProjectID(projectID, "")
	switch kind {
	case "session.start", "session.stop":
		var body struct {
			ProjectID string `json:"project_id"`
			SessionID string `json:"session_id"`
			IssueID   string `json:"issue_id"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		projectID = r.coalesceProjectID(body.ProjectID, projectID)
		issueID, sessionID, parseErr := r.sessionOperationIDs(kind, projectID, body.SessionID, body.IssueID)
		if parseErr != nil {
			return "", nil, "", errors.New("missing required fields: project_id/session_id")
		}
		resourceKeys = []string{"issue:" + projectID + ":" + issueID}
		if kind == "session.start" {
			resourceKeys = append(resourceKeys, "session:"+sessionID)
		}
		dedupeKey = kind + ":" + issueID
		return issueID, resourceKeys, dedupeKey, nil
	case protocol.CommandSessionResolveConflict:
		var body struct {
			ProjectID string `json:"project_id"`
			IssueID   string `json:"issue_id"`
			Worktree  string `json:"worktree"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		projectID = r.coalesceProjectID(body.ProjectID, projectID)
		parsedIssueID, parseErr := naming.ParseIssueID(strings.TrimSpace(body.IssueID))
		if parseErr != nil {
			return "", nil, "", errors.New("missing required fields: project_id/issue_id")
		}
		issueID = parsedIssueID.String()
		resourceKeys = []string{"issue:" + projectID + ":" + issueID}
		if worktree := normalizeOperationWorktree(body.Worktree); worktree != "" {
			resourceKeys = append(resourceKeys, "worktree:"+worktree)
		}
		dedupeKey = kind + ":" + issueID
		return issueID, resourceKeys, dedupeKey, nil
	case protocol.CommandSessionRestartAll:
		var body protocol.SessionRestartAllRequestBody
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		projectID = r.coalesceProjectID(body.ProjectID.String(), projectID)
		digest := sha256.Sum256(payload)
		return "", []string{"project:" + projectID + ":session-restart"}, fmt.Sprintf("%s:%x", kind, digest), nil
	case daemonhandlers.CommandGitFetch, daemonhandlers.CommandGitPullBase, daemonhandlers.CommandGitPush:
		var body struct {
			Worktree   string `json:"worktree"`
			Remote     string `json:"remote"`
			BaseBranch string `json:"base_branch"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		body.Worktree = normalizeOperationWorktree(body.Worktree)
		body.Remote = strings.TrimSpace(body.Remote)
		if body.Worktree == "" {
			return "", nil, "", errors.New("missing required fields: worktree")
		}
		if kind == daemonhandlers.CommandGitPullBase {
			body.BaseBranch = strings.TrimSpace(body.BaseBranch)
			if body.BaseBranch == "" {
				return "", nil, "", errors.New("missing required fields: worktree/base_branch")
			}
		}
		if body.Remote == "" {
			body.Remote = "origin"
		}
		resourceKeys = []string{"worktree:" + body.Worktree}
		if kind == daemonhandlers.CommandGitPullBase {
			dedupeKey = kind + ":" + body.Worktree + ":" + body.Remote + ":" + body.BaseBranch
		} else if kind == daemonhandlers.CommandGitPush {
			var pushBody struct {
				Branch string `json:"branch"`
			}
			_ = json.Unmarshal(payload, &pushBody)
			pushBody.Branch = strings.TrimSpace(pushBody.Branch)
			if pushBody.Branch == "" {
				return "", nil, "", errors.New("missing required fields: worktree/branch")
			}
			dedupeKey = kind + ":" + body.Worktree + ":" + body.Remote + ":" + pushBody.Branch
		} else {
			dedupeKey = kind + ":" + body.Worktree + ":" + body.Remote
		}
		return "", resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandGitMerge:
		var body struct {
			SourceID string `json:"source_id"`
			TargetID string `json:"target_id"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		sourceID, sourceErr := naming.ParseIssueID(strings.TrimSpace(body.SourceID))
		if sourceErr != nil || strings.TrimSpace(body.TargetID) == "" {
			return "", nil, "", errors.New("missing required fields: source_id/target_id")
		}
		issueID = sourceID.String()
		resourceKeys = []string{"issue:" + projectID + ":" + issueID}
		if strings.EqualFold(strings.TrimSpace(body.TargetID), "base") {
			body.TargetID = "base"
			resourceKeys = append(resourceKeys, "repository:"+projectID)
		} else {
			targetID, targetErr := naming.ParseIssueID(strings.TrimSpace(body.TargetID))
			if targetErr != nil {
				return "", nil, "", errors.New("invalid target_id")
			}
			body.TargetID = targetID.String()
			resourceKeys = append(resourceKeys, "issue:"+projectID+":"+body.TargetID)
		}
		dedupeKey = kind + ":" + issueID + ":" + body.TargetID
		return issueID, resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandGitMergeRef, daemonhandlers.CommandGitCheckout:
		var body struct {
			Worktree string `json:"worktree"`
			Branch   string `json:"branch"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		body.Worktree = normalizeOperationWorktree(body.Worktree)
		body.Branch = strings.TrimSpace(body.Branch)
		if body.Worktree == "" || body.Branch == "" {
			return "", nil, "", errors.New("missing required fields: worktree/branch")
		}
		resourceKeys = []string{"worktree:" + body.Worktree}
		dedupeKey = kind + ":" + body.Worktree + ":" + body.Branch
		return "", resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandGitAbortMerge:
		var body struct {
			Worktree string `json:"worktree"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		body.Worktree = normalizeOperationWorktree(body.Worktree)
		if body.Worktree == "" {
			return "", nil, "", errors.New("missing required fields: worktree")
		}
		resourceKeys = []string{"worktree:" + body.Worktree}
		dedupeKey = kind + ":" + body.Worktree
		return "", resourceKeys, dedupeKey, nil
	case daemonhandlers.CommandWorktreeCreate, daemonhandlers.CommandWorktreeRemove:
		var body struct {
			ProjectID string `json:"project_id"`
			IssueID   string `json:"issue_id"`
		}
		if err = json.Unmarshal(payload, &body); err != nil {
			return "", nil, "", fmt.Errorf("decode %s payload: %w", kind, err)
		}
		projectID = r.coalesceProjectID(body.ProjectID, projectID)
		parsedIssueID, parseErr := naming.ParseIssueID(strings.TrimSpace(body.IssueID))
		if parseErr != nil {
			return "", nil, "", errors.New("missing required fields: project_id/issue_id")
		}
		issueID = parsedIssueID.String()
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
		projectID = r.coalesceProjectID(body.ProjectID, projectID)
		resourceKeys = []string{"project:" + projectID + ":worktree.cleanup"}
		dedupeKey = kind + ":" + projectID
		return "", resourceKeys, dedupeKey, nil
	case protocol.CommandTaskBulkCleanup:
		if len(payload) == 0 {
			return "", nil, "", errors.New("missing required fields: payload")
		}
		resourceKeys = []string{"project:" + projectID + ":issue-lifecycle-cleanup"}
		digest := sha256.Sum256(payload)
		dedupeKey = fmt.Sprintf("%s:%x", kind, digest)
		return "", resourceKeys, dedupeKey, nil
	case protocol.CommandGlobalProjectionRebuild:
		return "", []string{"user-projection:rebuild"}, kind, nil
	default:
		return "", nil, "", fmt.Errorf("unsupported operation kind: %s", kind)
	}
}

func (r *operationRuntime) sessionOperationIDs(kind, projectID, sessionInput, issueInput string) (issueID, sessionID string, err error) {
	sessionInput = strings.TrimSpace(sessionInput)
	if sessionInput == "" {
		return "", "", errors.New("missing session_id")
	}
	issueInput = strings.TrimSpace(issueInput)
	namingScope := r.sessionNamingScope(projectID)
	if issueInput != "" {
		parsedIssueID, parseErr := naming.ParseIssueID(issueInput)
		if parseErr != nil {
			return "", "", parseErr
		}
		if _, sessionErr := naming.ParseSessionIDLoose(sessionInput); sessionErr != nil {
			return "", "", sessionErr
		}
		issueID = parsedIssueID.String()
		sessionID = sessionInput
		return issueID, sessionID, nil
	}
	if parsedIssueID, ok := naming.ParseIssueIDFromSessionName(sessionInput, namingScope); ok {
		sessionInput = parsedIssueID
	}
	parsedIssueID, parseErr := naming.ParseIssueID(sessionInput)
	if parseErr != nil {
		return "", "", parseErr
	}
	issueID = parsedIssueID.String()
	if kind == "session.start" {
		sessionID = naming.CanonicalSessionIDForIssue(namingScope, parsedIssueID).String()
	} else {
		sessionID = strings.TrimSpace(sessionInput)
	}
	return issueID, sessionID, nil
}

func overriddenOperationResourceKeys(kind string, derived, override []string) []string {
	override = normalizeOperationResourceKeys(override)
	if strings.TrimSpace(kind) != "session.start" {
		return override
	}
	return normalizeOperationResourceKeys(append(append([]string(nil), derived...), override...))
}

func normalizeOperationWorktree(worktree string) string {
	trimmed := strings.TrimSpace(worktree)
	if trimmed == "" {
		return ""
	}
	return filepath.Clean(trimmed)
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
	case protocol.CommandSessionResolveConflict:
		if r.sessionResolveConflict == nil {
			return nil, errors.New("session.resolve_conflict handler unavailable")
		}
		return r.sessionResolveConflict, nil
	case protocol.CommandSessionRestartAll:
		if r.sessionRestartAll == nil {
			return nil, errors.New("session.restart_all handler unavailable")
		}
		return r.sessionRestartAll, nil
	case daemonhandlers.CommandGitFetch,
		daemonhandlers.CommandGitPullBase,
		daemonhandlers.CommandGitPush,
		daemonhandlers.CommandGitMerge,
		daemonhandlers.CommandGitMergeRef,
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
	case protocol.CommandTaskBulkCleanup:
		if r.taskBulkCleanup == nil {
			return nil, errors.New("task.bulk_cleanup handler unavailable")
		}
		return r.taskBulkCleanup, nil
	case protocol.CommandGlobalProjectionRebuild:
		if r.globalProjectionRebuild == nil {
			return nil, errors.New("global projection rebuild handler unavailable")
		}
		return r.globalProjectionRebuild, nil
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

func (r *operationRuntime) wrapLegacyPending(req protocol.RequestEnvelope, record daemonops.Record) protocol.ResponseEnvelope {
	resp := r.successResponse(req)
	body, err := json.Marshal(operationResultEnvelope{
		OperationID: record.ID,
		State:       string(record.State),
	})
	if err != nil {
		resp.Error = &protocol.ErrorEnvelope{
			Code:      protocol.ErrorCodeInternal,
			Message:   fmt.Sprintf("marshal pending operation response: %v", err),
			Retryable: false,
		}
		resp.OK = false
		return resp
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
		OperationID:  parseOperationIDOrZero(record.ID),
		ProjectID:    naming.ProjectID(record.ProjectID),
		IssueID:      naming.IssueID(strings.TrimSpace(record.IssueID)),
		Kind:         record.Kind,
		DedupeKey:    record.DedupeKey,
		ResourceKeys: append([]string(nil), record.ResourceKeys...),
		State:        protocol.OperationState(record.State),
		Result:       append(json.RawMessage(nil), record.ResultPayload...),
		EnqueuedAt:   record.CreatedAt,
		StartedAt:    record.StartedAt,
		FinishedAt:   record.FinishedAt,
	}
	progress := protocolOperationProgress(record)
	out.Progress = &progress
	if record.ErrorMessage != "" {
		out.Error = &protocol.OperationError{
			Code:      mapOperationRecordErrorCode(record),
			Message:   record.ErrorMessage,
			Retryable: mapOperationRecordErrorCode(record).Retryable(),
		}
	}
	return out
}

func (r *operationRuntime) toProtocolQueueEntries(entries []daemonops.QueueEntry) []protocol.OperationQueueEntry {
	out := make([]protocol.OperationQueueEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, protocol.OperationQueueEntry{
			Operation:            r.toProtocolRecord(entry.Record),
			QueueIndex:           entry.QueueIndex,
			BlockingOperationIDs: parseOperationIDs(entry.BlockingOperationIDs),
			BlockedResourceKeys:  append([]string(nil), entry.BlockedResourceKeys...),
		})
	}
	return out
}

func (s *operationStoreAdapter) Create(ctx context.Context, record daemonops.Record) (daemonops.Record, error) {
	projectID := coalesceProjectID(record.ProjectID, "")
	if s.canonicalizeProjectID != nil {
		projectID = s.canonicalizeProjectID(projectID)
	}
	created, err := s.repo.Create(ctx, opstore.CreateParams{
		OperationID:  record.ID,
		ProjectID:    projectID,
		IssueID:      strings.TrimSpace(record.IssueID),
		Kind:         record.Kind,
		DedupeKey:    record.DedupeKey,
		ResourceKeys: sanitizeOperationResourceKeys(record.ResourceKeys, record.Kind),
		State:        toStoreState(record.State),
		SubmittedAt:  record.CreatedAt,
		StartedAt:    record.StartedAt,
		FinishedAt:   record.FinishedAt,
		ResultJSON:   append(json.RawMessage(nil), record.ResultPayload...),
		ErrorJSON:    marshalOperationErrorJSON(record.ErrorMessage),
		ProgressJSON: marshalOperationProgressJSON(record.Progress),
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
	progressOnly := params.Progress != nil &&
		params.StartedAt == nil &&
		params.FinishedAt == nil &&
		params.ResultPayload == nil &&
		params.ErrorMessage == nil
	updated, err := s.repo.Transition(ctx, opstore.TransitionParams{
		OperationID:  params.ID,
		ToState:      toStoreState(params.ToState),
		StartedAt:    params.StartedAt,
		FinishedAt:   params.FinishedAt,
		ResultJSON:   append(json.RawMessage(nil), params.ResultPayload...),
		ErrorJSON:    marshalOperationErrorJSON(derefString(params.ErrorMessage)),
		ProgressJSON: marshalOperationProgressJSON(params.Progress),
	})
	if err != nil {
		return daemonops.Record{}, err
	}
	out := fromStoreRecord(updated)
	if progressOnly {
		s.publishProgress(out)
		return out, nil
	}
	s.publish(out)
	s.publishOperationNotice(ctx, out)
	if s.onTerminal != nil && (out.State == daemonops.StateDone || out.State == daemonops.StateFailed || out.State == daemonops.StateCancelled) {
		s.onTerminal(ctx, out)
	}
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
	if s.canonicalizeProjectID != nil {
		projectID = s.canonicalizeProjectID(projectID)
	}
	eventRevision := s.nextRevision(projectID)
	s.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(projectID),
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Revision:        eventRevision,
		Event:           eventName,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            body,
	})
	s.publishProgress(record)
}

func (s *operationStoreAdapter) publishProgress(record daemonops.Record) {
	if s.hub == nil || s.nextRevision == nil {
		return
	}
	projectID := coalesceProjectID(record.ProjectID, "")
	if s.canonicalizeProjectID != nil {
		projectID = s.canonicalizeProjectID(projectID)
	}
	progressBody, err := json.Marshal(protocol.OperationProgressEventBody{
		OperationID: parseOperationIDOrZero(record.ID),
		ProjectID:   naming.ProjectID(projectID),
		State:       protocol.OperationState(record.State),
		Progress:    protocolOperationProgress(record),
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("marshal operation progress event body failed", "operation_id", record.ID, "error", err)
		}
		return
	}
	s.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(projectID),
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(projectID)},
		Revision:        s.nextRevision(projectID),
		Event:           protocol.EventOperationProgress,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            progressBody,
	})
}

func (s *operationStoreAdapter) publishOperationNotice(ctx context.Context, record daemonops.Record) {
	if s.noticeService == nil {
		return
	}
	switch record.State {
	case daemonops.StateFailed:
		s.upsertOperationFailureNotice(ctx, record)
	case daemonops.StateDone:
		s.resolveOperationFailureNotices(ctx, record)
	}
}

func (s *operationStoreAdapter) upsertOperationFailureNotice(ctx context.Context, record daemonops.Record) {
	dedupeKey := operationNoticeDedupeKey(record)
	projectID := s.operationNoticeProjectID(record.ProjectID)
	if s.staleOperationFailureNotice(ctx, projectID, dedupeKey, record) {
		return
	}
	candidate := daemonnotices.Candidate{
		ProjectID: projectID,
		Scope:     operationNoticeScope(record),
		Source: &daemonnotices.Source{
			OperationID:    parseOperationIDOrZero(record.ID),
			OperationKind:  strings.TrimSpace(record.Kind),
			OperationState: protocol.OperationState(record.State),
			Producer:       "daemon.operation",
		},
		Severity:       daemonnotices.SeverityError,
		Category:       "operation_failed",
		Title:          operationFailureNoticeTitle(record),
		Summary:        operationFailureNoticeSummary(record),
		Detail:         operationFailureNoticeDetail(record),
		Cause:          operationFailureNoticeCause(record),
		Actions:        operationFailureNoticeActions(record),
		DedupeKey:      dedupeKey,
		OccurredAt:     operationNoticeOccurredAt(record),
		RetentionClass: daemonnotices.RetentionError,
	}
	if _, _, _, err := s.noticeService.Upsert(ctx, candidate); err != nil && s.logger != nil {
		s.logger.Warn("failed to upsert operation failure notice",
			"operation_id", record.ID,
			"project_id", projectID,
			"kind", record.Kind,
			"error", err,
		)
	}
}

func (s *operationStoreAdapter) staleOperationFailureNotice(ctx context.Context, projectID, dedupeKey string, record daemonops.Record) bool {
	if dedupeKey == "" {
		return false
	}
	records, err := s.noticeService.List(ctx, daemonnotices.Query{
		ProjectID: projectID,
		Category:  "operation_failed",
		DedupeKey: dedupeKey,
		Limit:     1,
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("failed to inspect operation failure notice freshness",
				"operation_id", record.ID,
				"project_id", projectID,
				"kind", record.Kind,
				"error", err,
			)
		}
		return false
	}
	if len(records) == 0 {
		return false
	}
	latest := records[0]
	if latest.State == daemonnotices.StateActive {
		return false
	}
	generation := record.CreatedAt
	if record.StartedAt != nil {
		generation = *record.StartedAt
	}
	return !generation.IsZero() && !latest.UpdatedAt.IsZero() && !generation.After(latest.UpdatedAt)
}

func (s *operationStoreAdapter) resolveOperationFailureNotices(ctx context.Context, record daemonops.Record) {
	dedupeKey := operationNoticeDedupeKey(record)
	if dedupeKey == "" {
		return
	}
	projectID := s.operationNoticeProjectID(record.ProjectID)
	records, err := s.noticeService.List(ctx, daemonnotices.Query{
		ProjectID: projectID,
		States:    []daemonnotices.State{daemonnotices.StateActive},
		Category:  "operation_failed",
		DedupeKey: dedupeKey,
		Limit:     16,
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("failed to list operation failure notices for resolution",
				"operation_id", record.ID,
				"project_id", projectID,
				"kind", record.Kind,
				"error", err,
			)
		}
		return
	}
	for _, notice := range records {
		if _, _, _, err := s.noticeService.Update(ctx, daemonnotices.UpdateParams{
			ProjectID: projectID,
			NoticeID:  notice.NoticeID,
			State:     daemonnotices.StateResolved,
			Now:       operationNoticeOccurredAt(record),
		}); err != nil && s.logger != nil {
			s.logger.Warn("failed to resolve operation failure notice",
				"operation_id", record.ID,
				"notice_id", notice.NoticeID,
				"project_id", projectID,
				"kind", record.Kind,
				"error", err,
			)
		}
	}
}

func (s *operationStoreAdapter) operationNoticeProjectID(projectID string) string {
	projectID = coalesceProjectID(projectID, "")
	if s.canonicalizeProjectID != nil {
		projectID = s.canonicalizeProjectID(projectID)
	}
	return projectID
}

func operationNoticeDedupeKey(record daemonops.Record) string {
	kind := strings.TrimSpace(record.Kind)
	if kind == "" {
		kind = "operation"
	}
	intent := strings.TrimSpace(record.DedupeKey)
	switch {
	case intent != "":
		return "operation_failed:" + kind + ":" + intent
	case strings.TrimSpace(record.IssueID) != "":
		return "operation_failed:" + kind + ":issue:" + strings.TrimSpace(record.IssueID)
	case len(record.ResourceKeys) > 0:
		return "operation_failed:" + kind + ":resources:" + strings.Join(normalizeOperationResourceKeys(record.ResourceKeys), ",")
	default:
		return "operation_failed:" + kind + ":" + strings.TrimSpace(record.ID)
	}
}

func operationNoticeScope(record daemonops.Record) daemonnotices.Scope {
	if issueID := strings.TrimSpace(record.IssueID); issueID != "" {
		return daemonnotices.Scope{Type: "task", ID: issueID}
	}
	if operationID := strings.TrimSpace(record.ID); operationID != "" {
		return daemonnotices.Scope{Type: "operation", ID: operationID}
	}
	return daemonnotices.Scope{Type: "project"}
}

func operationNoticeOccurredAt(record daemonops.Record) time.Time {
	if record.FinishedAt != nil && !record.FinishedAt.IsZero() {
		return record.FinishedAt.UTC()
	}
	if !record.UpdatedAt.IsZero() {
		return record.UpdatedAt.UTC()
	}
	return time.Now().UTC()
}

func operationFailureNoticeTitle(record daemonops.Record) string {
	return operationDisplayName(record.Kind) + " failed"
}

func operationFailureNoticeSummary(record daemonops.Record) string {
	action := strings.ToLower(operationDisplayName(record.Kind))
	if issueID := strings.TrimSpace(record.IssueID); issueID != "" {
		return fmt.Sprintf("Could not complete %s for %s", action, issueID)
	}
	return "Could not complete " + action
}

func operationFailureNoticeDetail(record daemonops.Record) string {
	parts := []string{
		fmt.Sprintf("Operation %s (%s) failed.", strings.TrimSpace(record.ID), strings.TrimSpace(record.Kind)),
	}
	if msg := strings.TrimSpace(record.ErrorMessage); msg != "" {
		parts = append(parts, "Reason: "+msg+".")
	}
	if len(record.ResourceKeys) > 0 {
		parts = append(parts, "Resources: "+strings.Join(normalizeOperationResourceKeys(record.ResourceKeys), ", ")+".")
	}
	if next := operationFailureRecovery(record); next != "" {
		parts = append(parts, "Next: "+next+".")
	}
	return strings.Join(parts, " ")
}

func operationFailureNoticeCause(record daemonops.Record) *daemonnotices.Cause {
	code := mapOperationRecordErrorCode(record)
	return &daemonnotices.Cause{
		Code:      operationFailureCauseCode(record),
		Message:   operationErrorMessage(record),
		Retryable: code.Retryable(),
		ErrorCode: code,
	}
}

func operationFailureCauseCode(record daemonops.Record) string {
	msg := strings.ToLower(strings.TrimSpace(record.ErrorMessage))
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return "timeout"
	case strings.Contains(msg, "dirty"), strings.Contains(msg, "uncommitted"), strings.Contains(msg, "modified or untracked"):
		return "worktree_dirty"
	case strings.Contains(msg, "conflict"):
		return "conflict"
	case strings.Contains(msg, "permission denied"), strings.Contains(msg, "operation not permitted"):
		return "permission_denied"
	case strings.Contains(msg, "not found"):
		return "not_found"
	default:
		return "operation_failed"
	}
}

func operationFailureRecovery(record daemonops.Record) string {
	msg := strings.ToLower(strings.TrimSpace(record.ErrorMessage))
	switch {
	case strings.Contains(msg, "dirty"), strings.Contains(msg, "uncommitted"), strings.Contains(msg, "modified or untracked"):
		return "commit, discard, or merge local worktree changes, then retry"
	case strings.Contains(msg, "conflict"):
		return "resolve the conflict, refresh, and retry"
	case strings.Contains(msg, "permission denied"), strings.Contains(msg, "operation not permitted"):
		return "check filesystem permissions or the lock owner, then retry"
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return "refresh the project and retry if the operation is still needed"
	default:
		return "open the task or operation details, fix the reported blocker, and retry"
	}
}

func operationFailureNoticeActions(record daemonops.Record) []daemonnotices.Action {
	scope := operationNoticeScope(record)
	actions := []daemonnotices.Action{
		{ActionID: "dismiss", Kind: "dismiss", Label: "Dismiss", Enabled: true, TargetScope: scope},
		{ActionID: "mark_read", Kind: "mark_read", Label: "Mark read", Enabled: true, TargetScope: scope},
		{ActionID: "copy_details", Kind: "client.copy_details", Label: "Copy details", Enabled: true, TargetScope: scope},
	}
	if strings.TrimSpace(record.IssueID) != "" {
		actions = append(actions, daemonnotices.Action{ActionID: "open_task", Kind: "client.open_task", Label: "Open task", Enabled: true, TargetScope: scope})
	}
	return actions
}

func operationDisplayName(kind string) string {
	switch strings.TrimSpace(kind) {
	case "session.start":
		return "Session start"
	case "session.stop":
		return "Session stop"
	case protocol.CommandSessionResolveConflict:
		return "Session conflict recovery"
	case daemonhandlers.CommandGitFetch:
		return "Git fetch"
	case daemonhandlers.CommandGitPullBase:
		return "Git pull base"
	case daemonhandlers.CommandGitPush:
		return "Git push"
	case daemonhandlers.CommandGitMerge:
		return "Git merge"
	case daemonhandlers.CommandGitMergeRef:
		return "Git ref merge"
	case daemonhandlers.CommandGitCheckout:
		return "Git checkout"
	case daemonhandlers.CommandGitAbortMerge:
		return "Git abort merge"
	case daemonhandlers.CommandWorktreeCreate:
		return "Worktree create"
	case daemonhandlers.CommandWorktreeRemove:
		return "Worktree remove"
	case daemonhandlers.CommandWorktreeCleanupOrphaned:
		return "Worktree cleanup"
	case taskDeferredWorktreeCleanupOperationKind:
		return "Deferred worktree cleanup"
	default:
		if trimmed := strings.TrimSpace(kind); trimmed != "" {
			return trimmed
		}
		return "Operation"
	}
}

func operationProgressForState(state daemonops.State, kind string) protocol.OperationProgress {
	progress := protocol.OperationProgress{
		Phase: string(state),
		Unit:  "percent",
	}
	action := strings.TrimSpace(kind)
	if kind == protocol.CommandSessionResolveConflict {
		action = "agent conflict resolution"
	}
	switch state {
	case daemonops.StateQueued:
		progress.Message = "queued " + action
		progress.Current = 0
		progress.Total = 100
		progress.Percent = 0
	case daemonops.StateRunning:
		progress.Message = "running " + action
		progress.Current = 50
		progress.Total = 100
		progress.Percent = 50
	case daemonops.StateFailed:
		progress.Message = "failed " + action
		progress.Current = 100
		progress.Total = 100
		progress.Percent = 100
	case daemonops.StateCancelled:
		progress.Message = "cancelled " + action
		progress.Current = 100
		progress.Total = 100
		progress.Percent = 100
	default:
		progress.Message = "completed " + action
		progress.Current = 100
		progress.Total = 100
		progress.Percent = 100
	}
	return progress
}

func protocolOperationProgress(record daemonops.Record) protocol.OperationProgress {
	fallback := operationProgressForState(record.State, record.Kind)
	switch record.State {
	case daemonops.StateDone, daemonops.StateFailed, daemonops.StateCancelled:
		return fallback
	}
	if record.Progress == nil {
		return fallback
	}
	progress := protocol.OperationProgress{
		Phase:   strings.TrimSpace(record.Progress.Phase),
		Message: strings.TrimSpace(record.Progress.Message),
		Current: record.Progress.Current,
		Total:   record.Progress.Total,
		Unit:    strings.TrimSpace(record.Progress.Unit),
		Percent: record.Progress.Percent,
	}
	if progress.Phase == "" {
		progress.Phase = fallback.Phase
	}
	if progress.Message == "" {
		progress.Message = fallback.Message
	}
	if progress.Unit == "" {
		progress.Unit = fallback.Unit
	}
	if progress.Total == 0 {
		progress.Total = fallback.Total
	}
	return progress
}

func daemonOperationRecord(record daemonops.Record) protocol.OperationRecord {
	state := protocol.OperationState(record.State)
	out := protocol.OperationRecord{
		OperationID:  parseOperationIDOrZero(record.ID),
		ProjectID:    naming.ProjectID(record.ProjectID),
		IssueID:      naming.IssueID(strings.TrimSpace(record.IssueID)),
		Kind:         record.Kind,
		DedupeKey:    record.DedupeKey,
		ResourceKeys: append([]string(nil), record.ResourceKeys...),
		State:        state,
		Result:       append(json.RawMessage(nil), record.ResultPayload...),
		EnqueuedAt:   record.CreatedAt,
		StartedAt:    record.StartedAt,
		FinishedAt:   record.FinishedAt,
	}
	progress := protocolOperationProgress(record)
	out.Progress = &progress
	if record.ErrorMessage != "" {
		code := mapOperationRecordErrorCode(record)
		out.Error = &protocol.OperationError{Code: code, Message: record.ErrorMessage, Retryable: code.Retryable()}
	}
	return out
}

func parseOperationIDOrZero(raw string) naming.OperationID {
	parsed, err := naming.ParseOperationID(raw)
	if err != nil {
		return ""
	}
	return parsed
}

func parseOperationIDs(raw []string) []naming.OperationID {
	out := make([]naming.OperationID, 0, len(raw))
	for _, value := range raw {
		parsed := parseOperationIDOrZero(value)
		if parsed == "" {
			continue
		}
		out = append(out, parsed)
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
		Progress:      unmarshalOperationProgress(record.ProgressJSON),
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

func marshalOperationProgressJSON(progress *daemonops.Progress) json.RawMessage {
	if progress == nil {
		return nil
	}
	body, err := json.Marshal(protocol.OperationProgress{
		Phase:   strings.TrimSpace(progress.Phase),
		Message: strings.TrimSpace(progress.Message),
		Current: progress.Current,
		Total:   progress.Total,
		Unit:    strings.TrimSpace(progress.Unit),
		Percent: progress.Percent,
	})
	if err != nil {
		return nil
	}
	return body
}

func unmarshalOperationProgress(payload []byte) *daemonops.Progress {
	if len(payload) == 0 {
		return nil
	}
	var body protocol.OperationProgress
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil
	}
	progress := daemonops.Progress{
		Phase:   strings.TrimSpace(body.Phase),
		Message: strings.TrimSpace(body.Message),
		Current: body.Current,
		Total:   body.Total,
		Unit:    strings.TrimSpace(body.Unit),
		Percent: body.Percent,
	}
	if progress.Phase == "" && progress.Message == "" && progress.Current == 0 && progress.Total == 0 && progress.Unit == "" && progress.Percent == 0 {
		return nil
	}
	return &progress
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
		if trimmed := protocol.TrimProjectID(value); trimmed != "" {
			return trimmed
		}
	}
	return protocol.NormalizeProjectID("")
}

func (r *operationRuntime) coalesceProjectID(values ...string) string {
	return r.canonicalizeProjectID(coalesceProjectID(values...))
}

func (r *operationRuntime) canonicalizeProjectID(projectID string) string {
	normalized := protocol.NormalizeProjectID(projectID)
	if r == nil {
		return normalized
	}
	canonical := protocol.NormalizeProjectID(r.canonicalProject)
	if canonical == "" {
		return normalized
	}
	if normalized == canonical {
		return canonical
	}
	if normalized == protocol.DefaultProjectID {
		return canonical
	}
	if repoName := protocol.NormalizeProjectID(r.repoNameProject); repoName != "" && normalized == repoName {
		return canonical
	}
	return normalized
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
