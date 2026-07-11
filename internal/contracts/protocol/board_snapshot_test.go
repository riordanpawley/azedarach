package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestBoardSnapshotV4UsesSingleProjectionRepresentation(t *testing.T) {
	view := domain.DefaultBoardView()
	projection, err := domain.ProjectTasksByBoardView(view, []domain.Task{{ID: "az-1", Title: "One", Status: domain.StatusOpen}})
	if err != nil {
		t.Fatal(err)
	}
	payload := BoardSnapshotPayload{
		SchemaVersion: BoardSnapshotSchemaVersion, ProtocolVersion: CurrentVersion,
		ProjectID: "project", LastCheckedAt: time.Now().UTC(), Freshness: TaskListFreshnessFresh,
		Projection: BoardViewProjectionFromDomain(projection),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []string{"tasks", "columns", "ordered"} {
		if _, exists := shape[duplicate]; exists {
			t.Fatalf("wire payload contains duplicate top-level field %q", duplicate)
		}
	}
	var projectionShape map[string]json.RawMessage
	if err := json.Unmarshal(shape["projection"], &projectionShape); err != nil {
		t.Fatal(err)
	}
	if _, exists := projectionShape["ordered"]; exists {
		t.Fatal("projection contains duplicate ordered task collection")
	}
	if _, err := DecodeBoardSnapshotPayload(data); err != nil {
		t.Fatalf("decode single projection: %v", err)
	}
}

func TestBoardSnapshotProjectionRejectsUnknownMembership(t *testing.T) {
	view := domain.DefaultBoardView()
	view.Layout = domain.BoardViewLayoutHorizontalGrid
	p := BoardViewProjection{View: view, KnownTaskIDs: nil,
		Items: []BoardViewProjectedItem{{Task: BoardTaskSummary{ID: "az-1"}}}}
	if err := p.Validate(); err == nil {
		t.Fatal("Validate error = nil")
	}
}
