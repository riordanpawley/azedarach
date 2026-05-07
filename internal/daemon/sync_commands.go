package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/services/linearapi"
	"github.com/riordanpawley/azedarach/internal/services/linearsync"
)

const (
	commandSyncRun       = "sync.run"
	commandSyncConflicts = "sync.conflicts"
)

type syncRunRequest struct{}

type syncConflictsRequest struct {
	IncludeResolved bool `json:"include_resolved"`
}

func (d *Daemon) handleSyncRun(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd syncRunRequest
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode sync.run body: %v", err)), nil
		}
	}
	projectID := daemonProjectIDFromContext(ctx)
	service, summary, err := d.linearSyncService(ctx, projectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	if service != nil {
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		summary, err = service.Run(runCtx)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
	}
	body, err := json.Marshal(summary)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal sync.run response: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	resp.Revision = d.nextRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleSyncConflicts(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var cmd syncConflictsRequest
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &cmd); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("decode sync.conflicts body: %v", err)), nil
		}
	}
	projectID := daemonProjectIDFromContext(ctx)
	service, _, err := d.linearSyncService(ctx, projectID)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error()), nil
	}
	conflicts := []any{}
	if service != nil {
		list, err := service.Conflicts(ctx, cmd.IncludeResolved)
		if err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
		}
		for _, item := range list {
			conflicts = append(conflicts, item)
		}
	}
	body, err := json.Marshal(map[string]any{
		"provider":   linearsync.ProviderLinear,
		"project_id": projectID,
		"conflicts":  conflicts,
	})
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("marshal sync.conflicts response: %v", err)), nil
	}
	resp := d.successResponse(req)
	resp.Body = body
	return resp, nil
}

func (d *Daemon) linearSyncService(ctx context.Context, projectID string) (*linearsync.Service, linearsync.Summary, error) {
	projectID = d.canonicalProjectID(projectID)
	repoDir := d.resolveRepoDirForProject(projectID)
	cfg := appconfig.DefaultConfig().IssueTracker
	if strings.TrimSpace(repoDir) != "" {
		loaded, err := appconfig.LoadConfig(repoDir)
		if err != nil {
			return nil, linearsync.Summary{}, fmt.Errorf("load project config: %w", err)
		}
		if loaded != nil {
			cfg = loaded.IssueTracker
		}
	}
	summary := linearsync.Summary{
		Provider: linearsync.ProviderLinear,
		Enabled:  cfg.Sync.Enabled,
	}
	if strings.TrimSpace(strings.ToLower(cfg.Backend)) != linearsync.ProviderLinear || !cfg.Sync.Enabled {
		summary.Skipped = true
		summary.Reason = "linear sync is not enabled"
		return nil, summary, nil
	}
	apiKey := resolveLinearAPIKey(repoDir)
	if apiKey == "" {
		return nil, linearsync.Summary{}, fmt.Errorf("LINEAR_API_KEY is required for Linear sync; export it or set it in project .env.local")
	}
	store := d.issueClientForProject(projectID)
	if store == nil {
		return nil, linearsync.Summary{}, fmt.Errorf("issue store unavailable")
	}
	_ = ctx
	return &linearsync.Service{
		Store:     store,
		Linear:    &linearapi.Client{APIKey: apiKey},
		Config:    cfg,
		ProjectID: projectID,
	}, summary, nil
}
