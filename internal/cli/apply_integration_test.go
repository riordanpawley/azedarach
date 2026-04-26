package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	daemonclient "github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

type applyIntegrationTransport struct {
	handler *daemonhandlers.ApplyHandler
}

func (t *applyIntegrationTransport) Handshake(_ context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	return protocol.NegotiateHello(hello, "daemon-test"), nil
}

func (t *applyIntegrationTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	return t.handler.Handle(ctx, req), nil
}

func (t *applyIntegrationTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, fmt.Errorf("not implemented")
}

type applyIntegrationService struct {
	calls     []string
	deleteErr error
}

func (s *applyIntegrationService) Create(_ context.Context, params issues.CreateTaskParams) (string, error) {
	parentID := ""
	if params.ParentID != nil {
		parentID = *params.ParentID
	}
	s.calls = append(s.calls, fmt.Sprintf("create:%s:%s:%s:%s", params.Title, params.Priority.String(), params.Type, parentID))
	return "az-new", nil
}

func (s *applyIntegrationService) Update(_ context.Context, id string, status domain.Status) error {
	s.calls = append(s.calls, fmt.Sprintf("status:%s:%s", id, status))
	return nil
}

func (s *applyIntegrationService) UpdateDetails(_ context.Context, id string, params issues.UpdateTaskParams) error {
	s.calls = append(s.calls, fmt.Sprintf("update:%s:%s:%s:%s", id, params.Title, params.Priority.String(), params.Type))
	return nil
}

func (s *applyIntegrationService) Delete(_ context.Context, id string) error {
	s.calls = append(s.calls, fmt.Sprintf("delete:%s", id))
	return s.deleteErr
}

func (s *applyIntegrationService) Archive(_ context.Context, id string) error {
	s.calls = append(s.calls, fmt.Sprintf("archive:%s", id))
	return nil
}

func (s *applyIntegrationService) AddDependency(_ context.Context, issueID, dependsOnID, dependencyType string) error {
	s.calls = append(s.calls, fmt.Sprintf("dep-add:%s:%s:%s", issueID, dependsOnID, dependencyType))
	return nil
}

func (s *applyIntegrationService) RemoveDependency(_ context.Context, issueID, dependsOnID, dependencyType string) error {
	s.calls = append(s.calls, fmt.Sprintf("dep-remove:%s:%s:%s", issueID, dependsOnID, dependencyType))
	return nil
}

type applyIntegrationRevisions struct {
	current   uint64
	published []string
}

func (r *applyIntegrationRevisions) CurrentRevision(string) uint64 {
	return r.current
}

func (r *applyIntegrationRevisions) NextRevision(string) uint64 {
	r.current++
	return r.current
}

func (r *applyIntegrationRevisions) PublishTaskEvent(_ protocol.RequestEnvelope, eventName string, rev uint64, _ ...protocol.TaskEventBody) {
	r.published = append(r.published, fmt.Sprintf("%s:%d", eventName, rev))
}

func TestApplyPartialFailureIntegrationPreservesOutcomeOrderAndExitCode(t *testing.T) {
	service := &applyIntegrationService{
		deleteErr: fmt.Errorf("delete failed"),
	}
	revisions := &applyIntegrationRevisions{current: 3}
	transport := &applyIntegrationTransport{
		handler: daemonhandlers.NewApplyHandler(service, revisions),
	}

	client := daemonclient.New(transport).WithProjectID("proj-apply")
	reqBody := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 3,
		DryRun:           false,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: "task.create",
				Body: mustApplyJSON(t, map[string]any{
					"title":       "First",
					"description": "Draft",
					"type":        "task",
					"priority":    "high",
				}),
			},
			{
				Command: "task.delete",
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-2",
				}),
			},
			{
				Command: "task.archive",
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-3",
				}),
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := client.Command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-apply",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		SentAt:          time.Now().UTC(),
		Body:            body,
	})
	if err != nil {
		t.Fatalf("client command: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response error = %+v", resp.Error)
	}

	var result daemonhandlers.ApplyExecutionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result.ProjectID != "proj-apply" {
		t.Fatalf("ProjectID = %q, want proj-apply", result.ProjectID)
	}
	if result.SnapshotRevision != 3 {
		t.Fatalf("SnapshotRevision = %d, want 3", result.SnapshotRevision)
	}
	if result.Revision != 5 {
		t.Fatalf("Revision = %d, want 5", result.Revision)
	}
	if got, want := result.Summary, (daemonhandlers.ApplyExecutionSummary{Total: 3, Succeeded: 2, Failed: 1}); got != want {
		t.Fatalf("Summary = %+v, want %+v", got, want)
	}
	if got, want := len(result.Operations), 2; got != want {
		t.Fatalf("Operations len = %d, want %d", got, want)
	}
	if got, want := len(result.Outcomes), 3; got != want {
		t.Fatalf("Outcomes len = %d, want %d", got, want)
	}

	for i, outcome := range result.Outcomes {
		if outcome.Index != i {
			t.Fatalf("Outcomes[%d].Index = %d, want %d", i, outcome.Index, i)
		}
	}
	if result.Outcomes[0].Status != "success" || result.Outcomes[0].Revision != 4 {
		t.Fatalf("Outcomes[0] = %+v, want success/revision 4", result.Outcomes[0])
	}
	if result.Outcomes[1].Status != "failure" || result.Outcomes[1].Error != "delete failed" {
		t.Fatalf("Outcomes[1] = %+v, want failure/delete failed", result.Outcomes[1])
	}
	if result.Outcomes[2].Status != "success" || result.Outcomes[2].Revision != 5 {
		t.Fatalf("Outcomes[2] = %+v, want success/revision 5", result.Outcomes[2])
	}

	if got, want := service.calls, []string{
		"create:First:P1:task:",
		"delete:az-2",
		"archive:az-3",
	}; !equalStrings(got, want) {
		t.Fatalf("service calls = %v, want %v", got, want)
	}
	if got, want := revisions.published, []string{
		"task.created:4",
		"task.archived:5",
	}; !equalStrings(got, want) {
		t.Fatalf("published events = %v, want %v", got, want)
	}
	if got := applyResponseExitCode(resp); got != 2 {
		t.Fatalf("applyResponseExitCode() = %d, want 2", got)
	}
}

func TestApplyStatusMutationIntegrationPropagatesStatusAndWorkflowEvents(t *testing.T) {
	service := &applyIntegrationService{}
	revisions := &applyIntegrationRevisions{current: 8}
	transport := &applyIntegrationTransport{
		handler: daemonhandlers.NewApplyHandler(service, revisions),
	}

	client := daemonclient.New(transport).WithProjectID("proj-status")
	reqBody := protocol.ApplyRequestBody{
		SchemaVersion:    protocol.ApplySchemaVersion,
		SnapshotRevision: 8,
		DryRun:           false,
		Operations: []protocol.ApplyOperationBody{
			{
				Command: "task.update_status",
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-11",
					"status":  "closed",
				}),
			},
			{
				Command: "task.archive",
				Body: mustApplyJSON(t, map[string]any{
					"task_id": "az-12",
				}),
			},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := client.Command(context.Background(), protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "req-status",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         protocol.CommandTaskBulkApply,
		SentAt:          time.Now().UTC(),
		Body:            body,
	})
	if err != nil {
		t.Fatalf("client command: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response error = %+v", resp.Error)
	}

	var result daemonhandlers.ApplyExecutionResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result.ProjectID != "proj-status" {
		t.Fatalf("ProjectID = %q, want proj-status", result.ProjectID)
	}
	if result.SnapshotRevision != 8 {
		t.Fatalf("SnapshotRevision = %d, want 8", result.SnapshotRevision)
	}
	if result.Revision != 10 {
		t.Fatalf("Revision = %d, want 10", result.Revision)
	}
	if got, want := result.Summary, (daemonhandlers.ApplyExecutionSummary{Total: 2, Succeeded: 2, Failed: 0}); got != want {
		t.Fatalf("Summary = %+v, want %+v", got, want)
	}
	if got, want := len(result.Operations), 2; got != want {
		t.Fatalf("Operations len = %d, want %d", got, want)
	}
	if got, want := len(result.Outcomes), 2; got != want {
		t.Fatalf("Outcomes len = %d, want %d", got, want)
	}
	if result.Outcomes[0].Command != "task.update_status" || result.Outcomes[0].Status != "success" || result.Outcomes[0].Revision != 9 {
		t.Fatalf("Outcomes[0] = %+v, want success/revision 9", result.Outcomes[0])
	}
	if result.Outcomes[1].Command != "task.archive" || result.Outcomes[1].Status != "success" || result.Outcomes[1].Revision != 10 {
		t.Fatalf("Outcomes[1] = %+v, want success/revision 10", result.Outcomes[1])
	}

	if got, want := service.calls, []string{
		"status:az-11:closed",
		"archive:az-12",
	}; !equalStrings(got, want) {
		t.Fatalf("service calls = %v, want %v", got, want)
	}
	if got, want := revisions.published, []string{
		"task.updated:9",
		"task.archived:10",
	}; !equalStrings(got, want) {
		t.Fatalf("published events = %v, want %v", got, want)
	}
	if got := applyResponseExitCode(resp); got != 0 {
		t.Fatalf("applyResponseExitCode() = %d, want 0", got)
	}
}

func mustApplyJSON(t *testing.T, v any) []byte {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal apply body: %v", err)
	}
	return b
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
