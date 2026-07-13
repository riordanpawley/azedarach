package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestFlattenFanoutAndPlan(t *testing.T) {
	spec := fanoutSpec{
		ParentIssue: "az-root",
		Nodes: []fanoutNode{
			{
				Key:   "group-a",
				Kind:  "group",
				Title: "Group A",
				Children: []fanoutNode{
					{
						Key:        "leaf-a1",
						Kind:       "work",
						Title:      "Leaf A1",
						DependsOn:  []string{"leaf-a2"},
						FileBudget: []string{"go-bubbletea/internal/cli/**"},
					},
					{
						Key:   "leaf-a2",
						Kind:  "work",
						Title: "Leaf A2",
					},
				},
			},
		},
	}

	flat, warnings, err := flattenFanout(spec)
	if err != nil {
		t.Fatalf("flattenFanout error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want empty", warnings)
	}
	if len(flat) != 3 {
		t.Fatalf("flat len = %d, want 3", len(flat))
	}
	plan := buildFanoutPlan(spec.ParentIssue, flat, warnings)
	if plan.NodeCount != 3 {
		t.Fatalf("plan node_count = %d, want 3", plan.NodeCount)
	}
	if len(plan.Blocks) != 1 {
		t.Fatalf("plan blocks len = %d, want 1", len(plan.Blocks))
	}
	if plan.Blocks[0].IssueKey != "leaf-a1" || plan.Blocks[0].DependsOnKey != "leaf-a2" {
		t.Fatalf("blocks[0] = %+v", plan.Blocks[0])
	}
}

func TestMailboxRoundTrip(t *testing.T) {
	repoDir := t.TempDir()
	parent := "az-parent"
	first := mailEvent{
		Seq:         1,
		ParentIssue: parent,
		Type:        "dependency-ready",
		Body:        "ready",
	}
	second := mailEvent{
		Seq:         2,
		ParentIssue: parent,
		Type:        "handoff",
		Body:        "handoff",
	}
	if err := appendMailboxEvent(repoDir, first); err != nil {
		t.Fatalf("appendMailboxEvent first: %v", err)
	}
	if err := appendMailboxEvent(repoDir, second); err != nil {
		t.Fatalf("appendMailboxEvent second: %v", err)
	}

	events, err := readMailboxEvents(repoDir, parent)
	if err != nil {
		t.Fatalf("readMailboxEvents error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("seqs = [%d,%d], want [1,2]", events[0].Seq, events[1].Seq)
	}
	path := mailboxPath(repoDir, parent)
	if filepath.Ext(path) != ".jsonl" {
		t.Fatalf("mailbox path %q missing .jsonl suffix", path)
	}
}

func TestFlattenFanout_NestedLogicalParentTree(t *testing.T) {
	spec := fanoutSpec{
		ParentIssue: "az-root",
		Nodes: []fanoutNode{
			{
				Key:   "lane-a",
				Kind:  "group",
				Title: "Lane A",
				Children: []fanoutNode{
					{
						Key:   "phase-1",
						Kind:  "group",
						Title: "Phase 1",
						Children: []fanoutNode{
							{
								Key:   "leaf-1",
								Kind:  "work",
								Title: "Leaf 1",
							},
							{
								Key:       "leaf-2",
								Kind:      "work",
								Title:     "Leaf 2",
								DependsOn: []string{"leaf-1"},
							},
						},
					},
				},
			},
		},
	}

	flat, warnings, err := flattenFanout(spec)
	if err != nil {
		t.Fatalf("flattenFanout error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want empty", warnings)
	}
	if len(flat) != 4 {
		t.Fatalf("flat len = %d, want 4", len(flat))
	}
	byKey := map[string]fanoutFlatNode{}
	for _, node := range flat {
		byKey[node.Key] = node
	}
	if byKey["phase-1"].ParentKey != "lane-a" {
		t.Fatalf("phase-1 parent = %q, want lane-a", byKey["phase-1"].ParentKey)
	}
	if byKey["leaf-1"].ParentKey != "phase-1" {
		t.Fatalf("leaf-1 parent = %q, want phase-1", byKey["leaf-1"].ParentKey)
	}
	if byKey["leaf-2"].ParentKey != "phase-1" {
		t.Fatalf("leaf-2 parent = %q, want phase-1", byKey["leaf-2"].ParentKey)
	}
	plan := buildFanoutPlan(spec.ParentIssue, flat, nil)
	if len(plan.Blocks) != 1 {
		t.Fatalf("blocks len = %d, want 1", len(plan.Blocks))
	}
	if plan.Blocks[0].IssueKey != "leaf-2" || plan.Blocks[0].DependsOnKey != "leaf-1" {
		t.Fatalf("blocks[0] = %+v, want leaf-2 blocks leaf-1", plan.Blocks[0])
	}
}

func TestFlattenFanout_WorkNodeWithChildrenWarning(t *testing.T) {
	spec := fanoutSpec{
		ParentIssue: "az-root",
		Nodes: []fanoutNode{
			{
				Key:   "leaf-parent",
				Kind:  "work",
				Title: "Leaf Parent",
				Children: []fanoutNode{
					{
						Key:   "leaf-child",
						Kind:  "work",
						Title: "Leaf Child",
					},
				},
			},
		},
	}

	flat, warnings, err := flattenFanout(spec)
	if err != nil {
		t.Fatalf("flattenFanout error: %v", err)
	}
	if len(flat) != 2 {
		t.Fatalf("flat len = %d, want 2", len(flat))
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings len = %d, want 1", len(warnings))
	}
	if !strings.Contains(warnings[0], "kind=work and children") {
		t.Fatalf("warning = %q, want work+children warning", warnings[0])
	}
}

func TestFlattenFanout_UnknownDependencyFails(t *testing.T) {
	spec := fanoutSpec{
		ParentIssue: "az-root",
		Nodes: []fanoutNode{
			{
				Key:       "leaf-1",
				Kind:      "work",
				Title:     "Leaf 1",
				DependsOn: []string{"leaf-missing"},
			},
		},
	}

	_, _, err := flattenFanout(spec)
	if err == nil || !strings.Contains(err.Error(), "depends_on unknown key") {
		t.Fatalf("error = %v, want unknown dependency error", err)
	}
}

func TestOutOfBudgetFiles_MixedPatterns(t *testing.T) {
	changed := []string{
		"go-bubbletea/internal/cli/fanout_mail.go",
		"go-bubbletea/cmd/az/main.go",
		"README.md",
	}
	budget := []string{
		"go-bubbletea/internal/cli/**",
		"go-bubbletea/cmd/az/main.go",
	}
	out := outOfBudgetFiles(changed, budget)
	if len(out) != 1 || out[0] != "README.md" {
		t.Fatalf("out = %v, want [README.md]", out)
	}
}

func TestMailSendCommandSerializesSequenceNumbers(t *testing.T) {
	const attempts = 8
	for attempt := 0; attempt < attempts; attempt++ {
		repoDir := t.TempDir()
		var mu sync.Mutex
		events := make([]protocol.MailEvent, 0, 8)
		deps := &Dependencies{
			RepoDir: repoDir,
			DaemonClient: daemonclient.New(&fakeDaemonTransport{
				commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					switch req.Command {
					case protocol.CommandMailSend:
						var body protocol.MailSendCommandBody
						if err := json.Unmarshal(req.Body, &body); err != nil {
							t.Fatalf("decode mail.send body: %v", err)
						}
						mu.Lock()
						defer mu.Unlock()
						evt := protocol.MailEvent{
							Seq:         int64(len(events) + 1),
							ParentIssue: body.ParentIssue,
							IssueID:     body.IssueID,
							Type:        body.Type,
							Body:        body.Body,
							CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
						}
						events = append(events, evt)
						respBody, err := json.Marshal(evt)
						if err != nil {
							t.Fatalf("encode mail.send response: %v", err)
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							OK:              true,
							Body:            respBody,
						}, nil
					case protocol.CommandMailList:
						var body protocol.MailListCommandBody
						if err := json.Unmarshal(req.Body, &body); err != nil {
							t.Fatalf("decode mail.list body: %v", err)
						}
						mu.Lock()
						defer mu.Unlock()
						filtered := make([]protocol.MailEvent, 0, len(events))
						for _, evt := range events {
							if evt.Seq >= body.SinceSeq {
								filtered = append(filtered, evt)
							}
						}
						if body.Limit > 0 && len(filtered) > body.Limit {
							filtered = filtered[len(filtered)-body.Limit:]
						}
						respBody, err := json.Marshal(filtered)
						if err != nil {
							t.Fatalf("encode mail.list response: %v", err)
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							OK:              true,
							Body:            respBody,
						}, nil
					default:
						t.Fatalf("unexpected command: %s", req.Command)
						return protocol.ResponseEnvelope{}, nil
					}
				},
			}),
		}
		parent := "az-parent"
		start := make(chan struct{})
		errs := make(chan error, 2)

		send := func(issue string) {
			<-start
			errs <- MailSendCommand(deps, MailSendOptions{
				ParentIssueID: parent,
				IssueID:       issue,
				Type:          "handoff",
				Body:          issue,
			})
		}

		go send("az-1")
		go send("az-2")
		close(start)

		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("MailSendCommand attempt %d error: %v", attempt, err)
			}
		}

		listed, err := deps.DaemonClient.MailList(context.Background(), protocol.MailListCommandBody{
			RepoDir:     repoDir,
			ParentIssue: parent,
			SinceSeq:    0,
			Limit:       200,
		})
		if err != nil {
			t.Fatalf("MailList attempt %d: %v", attempt, err)
		}
		if len(listed) != 2 {
			t.Fatalf("attempt %d events len = %d, want 2", attempt, len(listed))
		}
		if listed[0].Seq != 1 || listed[1].Seq != 2 {
			t.Fatalf("attempt %d seqs = [%d,%d], want [1,2]", attempt, listed[0].Seq, listed[1].Seq)
		}
	}
}

func TestMailListAndWatchUseCase_SinceAndOnce(t *testing.T) {
	repoDir := t.TempDir()
	var mu sync.Mutex
	events := make([]protocol.MailEvent, 0, 4)
	deps := &Dependencies{
		RepoDir: repoDir,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case protocol.CommandMailSend:
					var body protocol.MailSendCommandBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode mail.send body: %v", err)
					}
					mu.Lock()
					defer mu.Unlock()
					evt := protocol.MailEvent{
						Seq:         int64(len(events) + 1),
						ParentIssue: body.ParentIssue,
						Type:        body.Type,
						Body:        body.Body,
						CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
					}
					events = append(events, evt)
					respBody, err := json.Marshal(evt)
					if err != nil {
						t.Fatalf("encode mail.send response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case protocol.CommandMailList:
					var body protocol.MailListCommandBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode mail.list body: %v", err)
					}
					mu.Lock()
					defer mu.Unlock()
					filtered := make([]protocol.MailEvent, 0, len(events))
					for _, evt := range events {
						if evt.Seq >= body.SinceSeq {
							filtered = append(filtered, evt)
						}
					}
					if body.Limit > 0 && len(filtered) > body.Limit {
						filtered = filtered[len(filtered)-body.Limit:]
					}
					respBody, err := json.Marshal(filtered)
					if err != nil {
						t.Fatalf("encode mail.list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				case protocol.CommandMailWatch:
					var body protocol.MailWatchCommandBody
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode mail.watch body: %v", err)
					}
					mu.Lock()
					defer mu.Unlock()
					filtered := make([]protocol.MailEvent, 0, len(events))
					for _, evt := range events {
						if evt.Seq >= body.SinceSeq {
							filtered = append(filtered, evt)
						}
					}
					respBody, err := json.Marshal(filtered)
					if err != nil {
						t.Fatalf("encode mail.watch response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            respBody,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
	}
	parent := "az-parent"
	for _, body := range []string{"first", "second"} {
		if err := MailSendCommand(deps, MailSendOptions{
			ParentIssueID: parent,
			Type:          "handoff",
			Body:          body,
		}); err != nil {
			t.Fatalf("MailSendCommand(%q): %v", body, err)
		}
	}

	listOutput := captureStdout(t, func() error {
		return MailListCommand(deps, MailListOptions{
			ParentIssueID: parent,
			SinceSeq:      2,
			Limit:         200,
			JSON:          true,
		})
	})
	var listed []mailEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(listOutput)), &listed); err != nil {
		t.Fatalf("decode MailListCommand output: %v", err)
	}
	if len(listed) != 1 || listed[0].Seq != 2 {
		t.Fatalf("listed = %+v, want seq=2 only", listed)
	}

	watchOutput := captureStdout(t, func() error {
		return MailWatchCommand(deps, MailWatchOptions{
			ParentIssueID: parent,
			SinceSeq:      2,
			JSONL:         true,
			Once:          true,
		})
	})
	lines := strings.Split(strings.TrimSpace(watchOutput), "\n")
	if len(lines) != 1 {
		t.Fatalf("watch lines = %d, want 1", len(lines))
	}
	var watched mailEvent
	if err := json.Unmarshal([]byte(lines[0]), &watched); err != nil {
		t.Fatalf("decode MailWatchCommand output: %v", err)
	}
	if watched.Seq != 2 || watched.Type != "handoff" {
		t.Fatalf("watched = %+v, want seq=2 type=handoff", watched)
	}
}

func TestMailWatchCommandSkipsPreflightDaemonAttachWhenReadSucceeds(t *testing.T) {
	repoDir := t.TempDir()
	events := []protocol.MailEvent{
		{
			Seq:         1,
			ParentIssue: "az-parent",
			Type:        "handoff",
			Body:        "ready",
			CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	handshakes := 0
	deps := &Dependencies{
		RepoDir: repoDir,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				handshakes++
				return protocol.HelloAck{Accepted: true}, nil
			},
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != protocol.CommandMailWatch {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				respBody, err := json.Marshal(events)
				if err != nil {
					t.Fatalf("encode mail.watch response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					OK:              true,
					Body:            respBody,
				}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return MailWatchCommand(deps, MailWatchOptions{
			ParentIssueID: "az-parent",
			SinceSeq:      1,
			JSONL:         true,
			Once:          true,
		})
	})
	if handshakes != 0 {
		t.Fatalf("handshakes = %d, want 0", handshakes)
	}
	if !strings.Contains(output, `"seq":1`) {
		t.Fatalf("output = %q, want watched event", output)
	}
}

func TestEvidenceValidateCommandJSONReportsPointers(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Ready for integration.",
		"commands_run": ["just test"],
		"key_assertions": ["validation passed"],
		"files_changed": ["internal/cli/fanout_mail.go"],
		"review": {"status": "shipit", "findings": []},
		"risks": ["none"]
	}`

	output, err := captureStdoutAllowError(t, func() error {
		return EvidenceValidateCommand(nil, EvidenceValidateOptions{Body: body, JSON: true})
	})
	if err == nil {
		t.Fatal("EvidenceValidateCommand unexpectedly succeeded")
	}
	var result domain.WorkerEvidenceValidationResult
	if decodeErr := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); decodeErr != nil {
		t.Fatalf("decode validation result: %v\noutput=%s", decodeErr, output)
	}
	if result.Complete {
		t.Fatalf("result = %+v, want incomplete", result)
	}
	var found bool
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Path == "/review/status" && slices.Contains(diagnostic.AllowedValues, "clean") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want review.status pointer with allowed values", result.Diagnostics)
	}
}

func TestEvidenceValidateCommandFixPrintsCanonicalPacket(t *testing.T) {
	body := `{
		"schema": "worker_evidence.v1",
		"summary": "Ready for integration.",
		"commands_run": [{"command": "just test", "result": "passed"}],
		"key_assertions": ["validation passed"],
		"files_changed": ["internal/cli/fanout_mail.go"],
		"review": {"status": "pass"},
		"risks": ["none"],
		"artifact_links": ["https://example.test/run/1"]
	}`

	output := captureStdout(t, func() error {
		return EvidenceValidateCommand(nil, EvidenceValidateOptions{Body: body, Fix: true})
	})
	var packet domain.WorkerEvidencePacket
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &packet); err != nil {
		t.Fatalf("decode fixed packet: %v\noutput=%s", err, output)
	}
	if packet.Review.Status != "clean" || len(packet.Review.Findings) != 0 {
		t.Fatalf("review = %+v, want clean with empty findings", packet.Review)
	}
	if len(packet.CommandsRun) != 1 || packet.CommandsRun[0] != "just test (passed)" {
		t.Fatalf("commands_run = %+v, want normalized command", packet.CommandsRun)
	}
	if len(packet.ArtifactLinks) != 1 || packet.ArtifactLinks[0].Label != "artifact 1" {
		t.Fatalf("artifact_links = %+v, want generated object link", packet.ArtifactLinks)
	}
}

func TestParseIssueFanoutDriftTicketFlagAndLegacyAliasMatch(t *testing.T) {
	canonical, err := ParseIssueFanoutDriftArgs([]string{"--ticket", "az-1"})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ParseIssueFanoutDriftArgs([]string{"--issue", "az-1"})
	if err != nil {
		t.Fatal(err)
	}
	if canonical.IssueID != "az-1" || legacy.IssueID != canonical.IssueID {
		t.Fatalf("canonical = %+v, legacy = %+v", canonical, legacy)
	}
}
