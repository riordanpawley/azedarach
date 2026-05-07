package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

type specRecordingTransport struct {
	lastReq protocol.RequestEnvelope
	replyFn func(protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
}

func (t *specRecordingTransport) Handshake(context.Context, protocol.Hello) (protocol.HelloAck, error) {
	return protocol.HelloAck{Accepted: true}, nil
}

func (t *specRecordingTransport) Command(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	t.lastReq = req
	if t.replyFn != nil {
		return t.replyFn(req)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		OK:              true,
	}, nil
}

func (t *specRecordingTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func assertSpecProjectID(t *testing.T, req protocol.RequestEnvelope, want string) {
	t.Helper()
	if req.Meta.ProjectID.String() != want {
		t.Fatalf("project_id = %q, want %q", req.Meta.ProjectID.String(), want)
	}
}

func ptr(s string) *string {
	return &s
}

func TestSpecRequirementCommandsEncodeAndDecode(t *testing.T) {
	const wantProjectID = "proj-spec"

	t.Run("list", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecRequirementList {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecRequirementList)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecRequirementListRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.IssueID != "az-1" || body.Status != "open" || len(body.RequirementIDs) != 2 {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecRequirementListResult{
					Requirements: []SpecRequirement{{
						ID:       "req-1",
						LocalID:  "r1",
						Title:    "Requirement 1",
						Body:     "Body",
						Kind:     SpecRequirementKindFunctional,
						Status:   "open",
						Priority: 2,
					}},
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
		out, err := client.ListSpecRequirements(context.Background(), SpecRequirementListRequest{
			IssueID:        "az-1",
			Status:         "open",
			RequirementIDs: []string{"req-1", "req-2"},
		})
		if err != nil {
			t.Fatalf("ListSpecRequirements error: %v", err)
		}
		if len(out.Requirements) != 1 || out.Requirements[0].ID != "req-1" {
			t.Fatalf("requirements = %+v", out.Requirements)
		}
	})

	t.Run("get", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecRequirementGet {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecRequirementGet)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecRequirementGetRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.RequirementID != "req-1" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecRequirementGetResult{
					Requirement: &SpecRequirement{
						ID:       "req-1",
						LocalID:  "r1",
						Title:    "Requirement 1",
						Body:     "Body",
						Kind:     SpecRequirementKindFunctional,
						Status:   "open",
						Priority: 2,
					},
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
		out, err := client.GetSpecRequirement(context.Background(), SpecRequirementGetRequest{RequirementID: "req-1"})
		if err != nil {
			t.Fatalf("GetSpecRequirement error: %v", err)
		}
		if out.Requirement == nil || out.Requirement.ID != "req-1" {
			t.Fatalf("requirement = %+v", out.Requirement)
		}
	})

	t.Run("create", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecRequirementCreate {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecRequirementCreate)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecRequirementCreateRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.RequirementID != "req-2" || body.Title != "Create me" || body.Description != "Long body" || body.IssueID != "az-2" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecRequirementCreateResult{
					Requirement: SpecRequirement{
						ID:       "req-2",
						LocalID:  "r2",
						Title:    body.Title,
						Body:     body.Description,
						Kind:     SpecRequirementKindAcceptance,
						Status:   "open",
						Priority: 1,
					},
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
		out, err := client.CreateSpecRequirement(context.Background(), SpecRequirementCreateRequest{
			RequirementID: "req-2",
			Title:         "Create me",
			Description:   "Long body",
			IssueID:       "az-2",
		})
		if err != nil {
			t.Fatalf("CreateSpecRequirement error: %v", err)
		}
		if out.Requirement.ID != "req-2" || out.Requirement.Body != "Long body" {
			t.Fatalf("requirement = %+v", out.Requirement)
		}
	})

	t.Run("update pending operation", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecRequirementUpdate {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecRequirementUpdate)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecRequirementUpdateRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.RequirementID != "req-3" || body.Title != "Updated" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(map[string]any{
					"operation_id": "op-spec-update",
					"state":        string(protocol.OperationStateQueued),
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
		_, err := client.UpdateSpecRequirement(context.Background(), SpecRequirementUpdateRequest{
			RequirementID: "req-3",
			Title:         "Updated",
		})
		var pending *OperationPendingError
		if !errors.As(err, &pending) {
			t.Fatalf("UpdateSpecRequirement error = %v, want OperationPendingError", err)
		}
		if pending.OperationID != "op-spec-update" {
			t.Fatalf("operation id = %q, want op-spec-update", pending.OperationID)
		}
	})

	t.Run("delete command error", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecRequirementDelete {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecRequirementDelete)
				}
				assertSpecProjectID(t, req, wantProjectID)
				return protocol.ResponseEnvelope{
					OK: false,
					Error: &protocol.ErrorEnvelope{
						Code:      protocol.ErrorCodeConflict,
						Message:   "busy",
						Retryable: false,
					},
				}, nil
			},
		}

		client := New(transport).WithProjectID(wantProjectID)
		_, err := client.DeleteSpecRequirement(context.Background(), SpecRequirementDeleteRequest{
			RequirementID: "req-4",
			Confirm:       true,
		})
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) {
			t.Fatalf("DeleteSpecRequirement error = %v, want CommandError", err)
		}
		if cmdErr.Code != protocol.ErrorCodeConflict {
			t.Fatalf("command error code = %q, want conflict", cmdErr.Code)
		}
	})
}

func TestSpecLinkAndReadParitySyncCommandsEncodeAndDecode(t *testing.T) {
	const wantProjectID = "proj-spec"

	t.Run("link list", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecLinkList {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecLinkList)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecLinkListRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.IssueID != "az-1" || body.RequirementID != "req-1" || len(body.LinkIDs) != 2 {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecLinkListResult{
					Links: []SpecIssueLink{{
						IssueID:            "az-1",
						RequirementID:      "req-1",
						RequirementLocalID: "r1",
						LinkType:           SpecLinkTypeImplements,
					}},
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
		out, err := client.ListSpecIssueLinks(context.Background(), SpecLinkListRequest{
			IssueID:       "az-1",
			RequirementID: "req-1",
			LinkIDs:       []string{"link-1", "link-2"},
		})
		if err != nil {
			t.Fatalf("ListSpecIssueLinks error: %v", err)
		}
		if len(out.Links) != 1 || out.Links[0].IssueID != "az-1" {
			t.Fatalf("links = %+v", out.Links)
		}
	})

	t.Run("link add", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecLinkAdd {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecLinkAdd)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecLinkAddRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.IssueID != "az-1" || body.RequirementID != "req-1" || body.Role != "implements" || body.Note != "trace" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecLinkAddResult{
					Added: true,
					Link: &SpecIssueLink{
						IssueID:            body.IssueID,
						RequirementID:      body.RequirementID,
						RequirementLocalID: "r1",
						LinkType:           SpecLinkTypeImplements,
					},
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
		out, err := client.AddSpecIssueLink(context.Background(), SpecLinkAddRequest{
			IssueID:       "az-1",
			RequirementID: "req-1",
			Role:          "implements",
			Note:          "trace",
		})
		if err != nil {
			t.Fatalf("AddSpecIssueLink error: %v", err)
		}
		if !out.Added || out.Link == nil || out.Link.LinkType != SpecLinkTypeImplements {
			t.Fatalf("link add result = %+v", out)
		}
	})

	t.Run("link remove", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecLinkRemove {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecLinkRemove)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecLinkRemoveRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.IssueID != "az-1" || body.RequirementID != "req-1" {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecLinkRemoveResult{Removed: true})
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
		out, err := client.RemoveSpecIssueLink(context.Background(), SpecLinkRemoveRequest{
			IssueID:       "az-1",
			RequirementID: "req-1",
		})
		if err != nil {
			t.Fatalf("RemoveSpecIssueLink error: %v", err)
		}
		if !out.Removed {
			t.Fatalf("remove result = %+v", out)
		}
	})

	t.Run("read long-running result", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecRead {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecRead)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecReadRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.IssueID != "az-1" || body.RequirementID != "req-1" {
					t.Fatalf("request body = %+v", body)
				}
				resultBody, err := json.Marshal(SpecReadResult{
					Requirements: []SpecRequirement{{
						ID:       "req-1",
						LocalID:  "r1",
						Title:    "Requirement 1",
						Body:     "Body",
						Kind:     SpecRequirementKindFunctional,
						Status:   "open",
						Priority: 2,
					}},
					Links: []SpecIssueLink{{
						IssueID:            "az-1",
						RequirementID:      "req-1",
						RequirementLocalID: "r1",
						LinkType:           SpecLinkTypeImplements,
					}},
					Coverage: SpecCoverageReport{
						Requirements: []SpecRequirementWithStats{{
							SpecRequirement: SpecRequirement{
								ID:       "req-1",
								LocalID:  "r1",
								Title:    "Requirement 1",
								Body:     "Body",
								Kind:     SpecRequirementKindFunctional,
								Status:   "open",
								Priority: 2,
							},
							LinkedIssueCount:      1,
							ImplementedIssueCount: 1,
						}},
						UnlinkedRequirementIDs:             []string{},
						FullyImplementedRequirementIDs:     []string{"r1"},
						PartiallyImplementedRequirementIDs: []string{},
						IntegrityGaps:                      []SpecCoverageGap{},
					},
				})
				if err != nil {
					t.Fatalf("marshal result: %v", err)
				}
				respBody, err := json.Marshal(map[string]any{
					"operation_id": "op-read",
					"state":        string(protocol.OperationStateDone),
					"result":       json.RawMessage(resultBody),
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
		out, err := client.ReadSpec(context.Background(), SpecReadRequest{
			IssueID:       "az-1",
			RequirementID: "req-1",
		})
		if err != nil {
			t.Fatalf("ReadSpec error: %v", err)
		}
		if len(out.Requirements) != 1 || len(out.Links) != 1 || out.Coverage.Requirements[0].LinkedIssueCount != 1 {
			t.Fatalf("read result = %+v", out)
		}
	})

	t.Run("lint", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecLint {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecLint)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecLintRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if !body.Strict {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecLintResult{
					OK: true,
					Diagnostics: []SpecDiagnostic{{
						Code:     "missing-link",
						Message:  "requirement has no link",
						Severity: "warning",
						ReqID:    ptr("r1"),
					}},
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
		out, err := client.LintSpec(context.Background(), SpecLintRequest{Strict: true})
		if err != nil {
			t.Fatalf("LintSpec error: %v", err)
		}
		if !out.OK || len(out.Diagnostics) != 1 || out.Diagnostics[0].Code != "missing-link" {
			t.Fatalf("lint result = %+v", out)
		}
	})

	t.Run("parity", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecParity {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecParity)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecParityRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Implementation != "ts-opentui" || !body.FailOnOut {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecParityResult{
					OK: true,
					Findings: []SpecParityFinding{{
						ReqID:    "r1",
						Severity: "warning",
						Message:  "requirement has no verifying link",
					}},
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
		out, err := client.ParitySpec(context.Background(), SpecParityRequest{
			Implementation: "ts-opentui",
			FailOnOut:      true,
		})
		if err != nil {
			t.Fatalf("ParitySpec error: %v", err)
		}
		if !out.OK || len(out.Findings) != 1 || out.Findings[0].ReqID != "r1" {
			t.Fatalf("parity result = %+v", out)
		}
	})

	t.Run("export", func(t *testing.T) {
		transport := &specRecordingTransport{
			replyFn: func(req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != CommandSpecExport {
					t.Fatalf("command = %q, want %q", req.Command, CommandSpecExport)
				}
				assertSpecProjectID(t, req, wantProjectID)
				var body SpecSyncRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.Target != "md" || !body.Check {
					t.Fatalf("request body = %+v", body)
				}
				respBody, err := json.Marshal(SpecMarkdownSyncResult{
					OutDir:           "docs/spec",
					Check:            true,
					Ok:               true,
					TotalDocuments:   2,
					ChangedDocuments: 0,
					Documents: []SpecMarkdownSyncDocumentResult{
						{Key: "overview", Path: "docs/spec/overview.md", Status: "unchanged", Changed: false},
					},
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
		out, err := client.ExportSpecMarkdown(context.Background(), SpecSyncRequest{
			Target: "md",
			Check:  true,
		})
		if err != nil {
			t.Fatalf("ExportSpecMarkdown error: %v", err)
		}
		if !out.Ok || out.OutDir != "docs/spec" || len(out.Documents) != 1 {
			t.Fatalf("sync result = %+v", out)
		}
	})
}
