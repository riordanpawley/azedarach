package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestProjectionDeltaErrorEnvelopePreservesTypedRetrySemantics(t *testing.T) {
	gap := ProjectionDeltaErrorEnvelope(&domain.ProjectionGapError{ProjectID: "p", Expected: 1, Actual: 3})
	if gap.Code != protocol.ErrorCodeRevisionGap || !gap.Retryable {
		t.Fatalf("gap envelope=%+v", gap)
	}
	canceled := ProjectionDeltaErrorEnvelope(&domain.ProjectionCanceledError{Cause: context.Canceled})
	if canceled.Code != protocol.ErrorCodeTimeout || !canceled.Retryable {
		t.Fatalf("canceled envelope=%+v", canceled)
	}
	internal := ProjectionDeltaErrorEnvelope(errors.New("broken"))
	if internal.Code != protocol.ErrorCodeInternal || internal.Retryable {
		t.Fatalf("internal envelope=%+v", internal)
	}
	retryable := ProjectionDeltaErrorEnvelope(&domain.ProjectionRetryableError{Cause: errors.New("busy")})
	if retryable.Code != protocol.ErrorCodeUnavailable || !retryable.Retryable {
		t.Fatalf("retryable envelope=%+v", retryable)
	}
}

func TestProjectionDeltaCommandsReadActiveIssueMutation(t *testing.T) {
	ctx := context.Background()
	projectID := "projection-command-project"
	client := issues.NewClientAtPath(filepath.Join(t.TempDir(), "issues.db"), slog.Default())
	t.Cleanup(func() { _ = client.CloseDB() })
	issueID, err := client.Create(ctx, issues.CreateTaskParams{Title: "active command delta", Type: domain.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		cfg:                   Config{Logger: slog.Default()},
		issueClientsByProject: map[string]*issues.Client{projectID: client},
	}
	body, _ := json.Marshal(protocol.ProjectionDeltaReadRequest{ProjectID: naming.ProjectID(projectID), Limit: 10})
	resp, err := d.command(ctx, protocol.RequestEnvelope{Command: protocol.CommandProjectionDeltaList, RequestID: "delta-list", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: body})
	if err != nil || !resp.OK {
		t.Fatalf("list response=%+v transport=%v", resp.Error, err)
	}
	var batch protocol.ProjectionDeltaBatch
	if err := json.Unmarshal(resp.Body, &batch); err != nil {
		t.Fatal(err)
	}
	if batch.ProjectID.String() != projectID || batch.HeadCursor != 1 || len(batch.Deltas) != 1 || batch.Deltas[0].Key != issueID || batch.Deltas[0].ProjectID.String() != projectID {
		t.Fatalf("active-path batch=%+v", batch)
	}
	snapshotBody, _ := json.Marshal(protocol.ProjectionSnapshotRequest{ProjectID: naming.ProjectID(projectID), Cursor: 1})
	snapshotResp, err := d.command(ctx, protocol.RequestEnvelope{Command: protocol.CommandProjectionSnapshot, RequestID: "delta-snapshot", Meta: protocol.Metadata{ProjectID: naming.ProjectID(projectID)}, Body: snapshotBody})
	if err != nil || !snapshotResp.OK {
		t.Fatalf("snapshot response=%+v transport=%v", snapshotResp.Error, err)
	}
	var snapshot protocol.ProjectionSnapshot
	if err := json.Unmarshal(snapshotResp.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ProjectID.String() != projectID || snapshot.Cursor != 1 || len(snapshot.Values) != 1 || snapshot.Values[0].Key != issueID {
		t.Fatalf("active-path snapshot=%+v", snapshot)
	}
}
