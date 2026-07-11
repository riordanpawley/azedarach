package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func (d *Daemon) handleBoardViewList(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var body protocol.BoardViewListRequestBody
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(body.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "issue store unavailable"), nil
	}
	views, err := client.ListBoardViews(ctx, projectID)
	if err != nil {
		return d.errorResponse(req, projectIssueStoreHealthErrorCode(err), err.Error()), nil
	}
	respBody := protocol.BoardViewListResponseBody{
		ProjectID:      naming.ProjectID(projectID),
		SelectedViewID: d.selectedBoardViewID(projectID),
		Views:          views,
	}
	return d.marshalBoardResponse(req, respBody)
}

func (d *Daemon) handleBoardViewGet(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var body protocol.BoardViewGetRequestBody
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(body.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	viewID := domain.NormalizeBoardViewID(body.ViewID)
	if viewID == "" {
		viewID = d.selectedBoardViewID(projectID)
	}
	record, err := d.boardViewRecord(ctx, projectID, viewID)
	if err != nil {
		return d.boardViewErrorResponse(req, err), nil
	}
	return d.marshalBoardResponse(req, protocol.BoardViewResponseBody{
		ProjectID: naming.ProjectID(projectID),
		View:      record,
	})
}

func (d *Daemon) handleBoardViewSave(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var body protocol.BoardViewSaveRequestBody
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(body.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "issue store unavailable"), nil
	}
	record, err := client.SaveBoardView(ctx, projectID, body.View)
	if err != nil {
		return d.boardViewErrorResponse(req, err), nil
	}
	resp, err := d.marshalBoardResponse(req, protocol.BoardViewResponseBody{
		ProjectID: naming.ProjectID(projectID),
		View:      record,
	})
	if err != nil {
		return resp, err
	}
	resp.Revision = d.nextRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleBoardViewDelete(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var body protocol.BoardViewDeleteRequestBody
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(body.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	client := d.issueClientForProject(projectID)
	if client == nil {
		return d.errorResponse(req, protocol.ErrorCodeUnavailable, "issue store unavailable"), nil
	}
	viewID := domain.NormalizeBoardViewID(body.ViewID)
	if err := client.DeleteBoardView(ctx, projectID, viewID); err != nil {
		return d.boardViewErrorResponse(req, err), nil
	}
	if d.selectedBoardViewID(projectID) == viewID {
		if err := d.setSelectedBoardViewID(projectID, domain.DefaultBoardViewID); err != nil {
			return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("persist selected board view: %v", err)), nil
		}
	}
	resp := d.successResponse(req)
	resp.Revision = d.nextRevision(projectID)
	return resp, nil
}

func (d *Daemon) handleBoardViewSelect(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	projectID := d.projectID(req.Meta)
	var body protocol.BoardViewSelectRequestBody
	if err := decodeOptionalJSON(req.Body, &body); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, fmt.Sprintf("invalid command body: %v", err)), nil
	}
	if strings.TrimSpace(body.ProjectID.String()) != "" {
		projectID = d.canonicalProjectID(body.ProjectID.String())
	}
	viewID := domain.NormalizeBoardViewID(body.ViewID)
	if viewID == "" {
		viewID = domain.DefaultBoardViewID
	}
	if _, err := d.boardViewRecord(ctx, projectID, viewID); err != nil {
		return d.boardViewErrorResponse(req, err), nil
	}
	updatedAt := time.Now().UTC()
	if err := d.setSelectedBoardViewID(projectID, viewID); err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, fmt.Sprintf("persist selected board view: %v", err)), nil
	}
	resp, err := d.marshalBoardResponse(req, protocol.BoardViewSelectResponseBody{
		ProjectID: naming.ProjectID(projectID),
		ViewID:    viewID,
		UpdatedAt: updatedAt,
	})
	if err != nil {
		return resp, err
	}
	resp.Revision = d.nextRevision(projectID)
	return resp, nil
}

func (d *Daemon) boardViewRecord(ctx context.Context, projectID, viewID string) (domain.BoardViewRecord, error) {
	client := d.issueClientForProject(projectID)
	if client == nil {
		return domain.BoardViewRecord{}, fmt.Errorf("issue store unavailable")
	}
	return client.GetBoardView(ctx, projectID, viewID)
}

func (d *Daemon) selectedBoardViewID(projectID string) string {
	value, found := d.getUIStateValue(projectID, protocol.UIStateKeyBoardSelectedView)
	if !found || strings.TrimSpace(value) == "" {
		if persisted, ok := d.loadSelectedBoardViewPreference(projectID); ok {
			d.setUIStateValue(projectID, protocol.UIStateKeyBoardSelectedView, persisted)
			return persisted
		}
		if legacy, ok := d.getUIStateValue(projectID, protocol.UIStateKeyUIViewMode); ok {
			if migrated, recognized := domain.BoardViewIDFromLegacyUIMode(legacy); recognized {
				d.setUIStateValue(projectID, protocol.UIStateKeyBoardSelectedView, migrated)
				_ = d.saveSelectedBoardViewPreference(projectID, migrated)
				return migrated
			}
		}
		return domain.DefaultBoardViewID
	}
	return domain.NormalizeBoardViewID(value)
}

func (d *Daemon) setSelectedBoardViewID(projectID, viewID string) error {
	viewID = domain.NormalizeBoardViewID(viewID)
	if viewID == "" {
		viewID = domain.DefaultBoardViewID
	}
	d.setUIStateValue(projectID, protocol.UIStateKeyBoardSelectedView, viewID)
	return d.saveSelectedBoardViewPreference(projectID, viewID)
}

func (d *Daemon) boardViewErrorResponse(req protocol.RequestEnvelope, err error) protocol.ResponseEnvelope {
	if errors.Is(err, issues.ErrBoardViewNotFound) {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error())
	}
	if errors.Is(err, issues.ErrBoardViewBuiltIn) {
		return d.errorResponse(req, protocol.ErrorCodeInvalidRequest, err.Error())
	}
	return d.errorResponse(req, projectIssueStoreHealthErrorCode(err), err.Error())
}

func (d *Daemon) marshalBoardResponse(req protocol.RequestEnvelope, body any) (protocol.ResponseEnvelope, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return d.errorResponse(req, protocol.ErrorCodeInternal, err.Error()), nil
	}
	resp := d.successResponse(req)
	resp.Body = payload
	return resp, nil
}

func decodeOptionalJSON(data []byte, out any) error {
	if len(data) == 0 || strings.TrimSpace(string(data)) == "" || strings.TrimSpace(string(data)) == "null" {
		return nil
	}
	return json.Unmarshal(data, out)
}
