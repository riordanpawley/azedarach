package daemonclient

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestFanoutPlanAndApplyCommands(t *testing.T) {
	const wantProjectID = "proj-fanout"

	t.Run("plan", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != protocol.CommandIssueFanout {
					t.Fatalf("command = %q, want %q", req.Command, protocol.CommandIssueFanout)
				}
				var body protocol.FanoutCommandBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if body.Apply {
					t.Fatalf("apply = true, want false")
				}
				respBody, err := json.Marshal(protocol.FanoutPlan{
					ParentIssue: "az-root",
					NodeCount:   1,
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			},
		}
		client := New(transport).WithProjectID(wantProjectID)
		plan, err := client.FanoutPlan(context.Background(), protocol.FanoutCommandBody{
			RepoDir: "/tmp/repo",
			Spec:    json.RawMessage(`{"nodes":[{"key":"leaf","kind":"work","title":"Leaf"}]}`),
		})
		if err != nil {
			t.Fatalf("FanoutPlan error: %v", err)
		}
		if plan.ParentIssue != "az-root" || plan.NodeCount != 1 {
			t.Fatalf("plan = %+v", plan)
		}
	})

	t.Run("apply", func(t *testing.T) {
		transport := &taskRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				assertTaskProjectID(t, req, wantProjectID)
				if req.Command != protocol.CommandIssueFanout {
					t.Fatalf("command = %q, want %q", req.Command, protocol.CommandIssueFanout)
				}
				var body protocol.FanoutCommandBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if !body.Apply {
					t.Fatalf("apply = false, want true")
				}
				respBody, err := json.Marshal(protocol.FanoutApplyResult{
					ParentIssue: "az-root",
					Created:     map[string]string{"leaf": "az-1"},
					BlocksAdded: 1,
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			},
		}
		client := New(transport).WithProjectID(wantProjectID)
		result, err := client.FanoutApply(context.Background(), protocol.FanoutCommandBody{
			RepoDir: "/tmp/repo",
			Spec:    json.RawMessage(`{"nodes":[{"key":"leaf","kind":"work","title":"Leaf"}]}`),
		})
		if err != nil {
			t.Fatalf("FanoutApply error: %v", err)
		}
		if result.Created["leaf"] != "az-1" || result.BlocksAdded != 1 {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestFanoutDriftCommand(t *testing.T) {
	const wantProjectID = "proj-fanout"
	transport := &taskRecordingTransport{
		replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			assertTaskProjectID(t, req, wantProjectID)
			if req.Command != protocol.CommandIssueFanoutDrift {
				t.Fatalf("command = %q, want %q", req.Command, protocol.CommandIssueFanoutDrift)
			}
			var body protocol.FanoutDriftCommandBody
			if err := json.Unmarshal(req.Body, &body); err != nil {
				t.Fatalf("unmarshal request body: %v", err)
			}
			if body.IssueID != "az-1" || body.Worktree != "/tmp/worktree" {
				t.Fatalf("request body = %+v", body)
			}
			respBody, err := json.Marshal(protocol.FanoutDriftResult{
				IssueID:      "az-1",
				Worktree:     "/tmp/worktree",
				FileBudget:   []string{"internal/**"},
				ChangedFiles: []string{"README.md"},
				OutOfBudget:  []string{"README.md"},
				AdvisoryOnly: true,
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			return protocol.ResponseEnvelope{
				ProtocolVersion: req.ProtocolVersion,
				RequestID:       req.RequestID,
				Kind:            protocol.EnvelopeKindResponse,
				OK:              true,
				Body:            respBody,
			}, nil
		},
	}
	client := New(transport).WithProjectID(wantProjectID)
	got, err := client.FanoutDrift(context.Background(), protocol.FanoutDriftCommandBody{
		IssueID:  "az-1",
		RepoDir:  "/tmp/repo",
		Worktree: "/tmp/worktree",
	})
	if err != nil {
		t.Fatalf("FanoutDrift error: %v", err)
	}
	if len(got.OutOfBudget) != 1 || got.OutOfBudget[0] != "README.md" {
		t.Fatalf("drift = %+v", got)
	}
}
