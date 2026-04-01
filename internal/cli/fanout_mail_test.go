package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

func TestComputeRunnableLeaves(t *testing.T) {
	root := "az-root"
	group := "az-group"
	leafA := "az-a"
	leafB := "az-b"
	groupParent := root
	leafAParent := group
	leafBParent := group

	tasks := []domain.Task{
		{ID: root, Type: domain.TypeFeature, Status: domain.StatusInProgress},
		{ID: group, Type: domain.TypeEpic, Status: domain.StatusOpen, ParentID: &groupParent},
		{ID: leafA, Type: domain.TypeTask, Status: domain.StatusDone, ParentID: &leafAParent},
		{
			ID:       leafB,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &leafBParent,
			Dependencies: []domain.Dependency{
				{ID: leafA, Type: domain.DependencyBlocks},
			},
		},
	}

	result, err := computeRunnableLeaves(root, tasks)
	if err != nil {
		t.Fatalf("computeRunnableLeaves error: %v", err)
	}
	if len(result.Runnable) != 1 || result.Runnable[0] != leafB {
		t.Fatalf("runnable = %v, want [%s]", result.Runnable, leafB)
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

func TestComputeRunnableLeaves_DependencyGatingTimeline(t *testing.T) {
	root := "az-root"
	a := "az-a"
	b := "az-b"
	aParent := root
	bParent := root
	done := domain.StatusDone

	base := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{ID: a, Type: domain.TypeTask, Status: domain.StatusInProgress, ParentID: &aParent},
		{
			ID:       b,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &bParent,
			Dependencies: []domain.Dependency{
				{ID: a, Type: domain.DependencyBlocks},
			},
		},
	}

	before, err := computeRunnableLeaves(root, base)
	if err != nil {
		t.Fatalf("computeRunnableLeaves before error: %v", err)
	}
	if len(before.Runnable) != 1 || before.Runnable[0] != a {
		t.Fatalf("before runnable = %v, want [%s]", before.Runnable, a)
	}
	if got := before.Blocked[b]; !strings.Contains(got, a) {
		t.Fatalf("before blocked[%s] = %q, want blocker %s", b, got, a)
	}

	after := append([]domain.Task(nil), base...)
	after[1].Status = done
	gotAfter, err := computeRunnableLeaves(root, after)
	if err != nil {
		t.Fatalf("computeRunnableLeaves after error: %v", err)
	}
	if len(gotAfter.Runnable) != 1 || gotAfter.Runnable[0] != b {
		t.Fatalf("after runnable = %v, want [%s]", gotAfter.Runnable, b)
	}
}

func TestComputeRunnableLeaves_MissingDependencyReported(t *testing.T) {
	root := "az-root"
	leaf := "az-leaf"
	parent := root
	tasks := []domain.Task{
		{ID: root, Type: domain.TypeEpic, Status: domain.StatusInProgress},
		{
			ID:       leaf,
			Type:     domain.TypeTask,
			Status:   domain.StatusOpen,
			ParentID: &parent,
			Dependencies: []domain.Dependency{
				{ID: "az-missing", Type: domain.DependencyBlocks},
			},
		},
	}

	result, err := computeRunnableLeaves(root, tasks)
	if err != nil {
		t.Fatalf("computeRunnableLeaves error: %v", err)
	}
	if len(result.Runnable) != 0 {
		t.Fatalf("runnable = %v, want empty", result.Runnable)
	}
	if got := result.Blocked[leaf]; !strings.Contains(got, "missing") {
		t.Fatalf("blocked[%s] = %q, want missing marker", leaf, got)
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

func TestGitChangedFilesIncludesStagedAndUntracked(t *testing.T) {
	repoDir := t.TempDir()
	runGitCommand(t, repoDir, "init")

	stagedPath := filepath.Join(repoDir, "staged.txt")
	untrackedPath := filepath.Join(repoDir, "untracked.txt")
	if err := os.WriteFile(stagedPath, []byte("staged\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	if err := os.WriteFile(untrackedPath, []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	runGitCommand(t, repoDir, "add", "staged.txt")

	got, err := gitChangedFiles(repoDir)
	if err != nil {
		t.Fatalf("gitChangedFiles error: %v", err)
	}
	want := []string{"staged.txt", "untracked.txt"}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("gitChangedFiles output not sorted: %v", got)
	}
	if len(got) != len(want) {
		t.Fatalf("gitChangedFiles len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, path := range want {
		if got[i] != path {
			t.Fatalf("gitChangedFiles[%d] = %q, want %q (full=%v)", i, got[i], path, got)
		}
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

func runGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") ||
			strings.HasPrefix(entry, "GIT_WORK_TREE=") ||
			strings.HasPrefix(entry, "GIT_INDEX_FILE=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	cmd.Env = filtered
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
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
