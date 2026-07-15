package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	clitext "github.com/riordanpawley/azedarach/internal/cli/text"
	autoclient "github.com/riordanpawley/azedarach/internal/client"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/client/reconnect"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/logging"
	"github.com/riordanpawley/azedarach/internal/naming"
	"github.com/riordanpawley/azedarach/internal/observability"
	gitservice "github.com/riordanpawley/azedarach/internal/services/git"
	prservice "github.com/riordanpawley/azedarach/internal/services/pr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	_ "modernc.org/sqlite"
)

type fakeDaemonTransport struct {
	handshakeFn             func(context.Context, protocol.Hello) (protocol.HelloAck, error)
	commandFn               func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error)
	passOrchestrationIntent bool
	lastGraphReadiness      daemonclient.TaskGraphReadiness
}

func TestRenderPrimeOrchestrationSectionExplainsRuntimeContinuationGuard(t *testing.T) {
	scope, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	section := renderPrimeOrchestrationSection(protocol.OrchestrationSnapshot{
		Scope: scope, Cursor: 17, ContinuationRequired: true,
		ContinuationReason:   "direct nested root active",
		ContinuationContract: "consume the durable cursor and continue",
		ValidationCapacity:   &domain.ValidationSnapshot{Revision: 4, Active: []domain.ValidationRequest{{RequestID: "gate"}}, Queued: []domain.ValidationRequest{{RequestID: "waiter"}}},
	})
	for _, want := range []string{"Runtime persistence guard: wake-required", "direct nested root active", "consume the durable cursor and continue", "cursor=17", "Validation capacity: active=1 queued=1 revision=4"} {
		if !strings.Contains(section, want) {
			t.Fatalf("prime orchestration section missing %q:\n%s", want, section)
		}
	}
}

func openSQLiteDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func registerCLIProjects(t *testing.T, names ...string) map[string]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := &config.ProjectsRegistry{}
	routes := make(map[string]string, len(names))
	for _, name := range names {
		path := filepath.Join(home, name)
		route, err := config.ProjectIDForRoot(path)
		if err != nil {
			t.Fatalf("derive project ID for %s: %v", name, err)
		}
		registry.Projects = append(registry.Projects, config.Project{ID: route, Name: name, Path: path})
		routes[name] = route
	}
	if len(names) > 0 {
		registry.DefaultProject = names[0]
	}
	if err := config.SaveProjectsRegistry(registry); err != nil {
		t.Fatalf("save project registry: %v", err)
	}
	return routes
}

func ptrToString(v string) *string {
	return &v
}

func marshalTaskListBody(tasks []domain.Task) ([]byte, error) {
	return json.Marshal(protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 0,
		ProjectID:        naming.ProjectID(protocol.DefaultProjectID),
		LastCheckedAt:    time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            tasks,
	})
}

func marshalTaskListBodyForProject(projectID string, tasks []domain.Task) ([]byte, error) {
	return json.Marshal(protocol.TaskListSnapshotPayload{
		SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 0,
		ProjectID:        naming.ProjectID(projectID),
		LastCheckedAt:    time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC),
		Freshness:        protocol.TaskListFreshnessFresh,
		Tasks:            tasks,
	})
}

func TestParsePRArgsListAcceptsProjectStateLimitAndJSON(t *testing.T) {
	opts, err := ParsePRArgs([]string{"list", "--project", "proj", "--state", "all", "--limit", "12", "--json"})
	if err != nil {
		t.Fatalf("ParsePRArgs returned error: %v", err)
	}
	if opts.Command != "list" || opts.Project != "proj" || opts.State != "all" || opts.Limit != 12 || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParsePRArgsListRejectsTargetSelectors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "positional issue", args: []string{"list", "cxs"}, want: "does not accept issue or pull request selectors"},
		{name: "issue flag", args: []string{"list", "--issue", "cxs"}, want: "--issue"},
		{name: "branch flag", args: []string{"list", "--branch", "feature"}, want: "--branch"},
		{name: "number flag", args: []string{"list", "--number", "12"}, want: "--number"},
		{name: "pr alias", args: []string{"list", "--pr", "12"}, want: "--pr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePRArgs(tt.args)
			if err == nil {
				t.Fatalf("ParsePRArgs returned nil error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestPRCommandExplicitProjectRoutesEveryVerbToRegisteredRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "default-repo")
	repoB := filepath.Join(home, "selected-repo")
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("default project id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("selected project id: %v", err)
	}
	defaultConfig := config.DefaultConfig()
	defaultConfig.Git.BaseBranch = "trunk"
	if err := config.SaveConfig(defaultConfig, filepath.Join(repoA, ".azedarach", "config.json")); err != nil {
		t.Fatalf("save default config: %v", err)
	}
	selectedConfig := config.DefaultConfig()
	selectedConfig.Git.BaseBranch = "release"
	if err := config.SaveConfig(selectedConfig, filepath.Join(repoB, ".azedarach", "config.json")); err != nil {
		t.Fatalf("save selected config: %v", err)
	}
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		DefaultProject: "Default",
		Projects: []config.Project{
			{ID: projectA, Name: "Default", Path: repoA},
			{ID: "selected-id", Name: "Selected", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	tests := []struct {
		name string
		opts PROptions
	}{
		{name: "list", opts: PROptions{Command: "list", Project: "Selected", State: "all", Limit: 20, JSON: true}},
		{name: "status", opts: PROptions{Command: "status", Project: "selected-id", Number: 12, JSON: true}},
		{name: "checks", opts: PROptions{Command: "checks", Project: "selected-repo", Branch: "feature", JSON: true}},
		{name: "open", opts: PROptions{Command: "open", Project: "Selected", Branch: "feature", JSON: true}},
		{name: "create", opts: PROptions{Command: "create", Project: projectB, IssueID: "dha", Branch: "feature", Title: "Fix routing", Body: "Body", JSON: true}},
		{name: "merge", opts: PROptions{Command: "merge", Project: "Selected", Number: 12, Strategy: "squash", Confirm: true, JSON: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var routedProjects []string
			var createBaseBranch string
			transport := &fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				routedProjects = append(routedProjects, req.Meta.ProjectID.String())
				switch req.Command {
				case daemonclient.CommandPRList:
					return responseWithJSON(req, daemonclient.PullRequestListResult{State: "all"}), nil
				case daemonclient.CommandPRGet:
					return responseWithJSON(req, daemonclient.PullRequestGetResult{PullRequest: prservice.PRInfo{Number: 12, Branch: "feature", BaseRef: "main"}}), nil
				case daemonclient.CommandPRChecks:
					return responseWithJSON(req, daemonclient.PullRequestChecksResult{Ref: "feature", ChecksStatus: "passing"}), nil
				case daemonclient.CommandPROpen:
					return responseWithJSON(req, map[string]string{"branch": "feature"}), nil
				case daemonclient.CommandPRCreate:
					var body struct {
						BaseBranch string `json:"base_branch"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("decode PR create: %v", err)
					}
					createBaseBranch = body.BaseBranch
					return responseWithJSON(req, daemonclient.CreatePullRequestResult{IssueID: "dha", PullRequest: prservice.PRInfo{Number: 12, Branch: "feature"}}), nil
				case daemonclient.CommandPRMerge:
					return responseWithJSON(req, daemonclient.PullRequestMergeResult{Number: 12, Strategy: "squash"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			}}
			deps := &Dependencies{
				Config:       defaultConfig,
				DaemonClient: daemonclient.New(transport).WithProjectID(projectA),
				Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID:    projectA,
				RepoDir:      repoA,
			}
			output := captureStdout(t, func() error { return PRCommand(deps, tt.opts) })
			if len(routedProjects) == 0 {
				t.Fatal("PR command sent no daemon requests")
			}
			for _, got := range routedProjects {
				if got != projectB {
					t.Fatalf("routed project = %q, want %q (all routes: %v)", got, projectB, routedProjects)
				}
			}
			if !strings.Contains(output, `"project_id": "`+projectB+`"`) || !strings.Contains(output, `"repository": "`+repoB+`"`) {
				t.Fatalf("JSON output missing resolved routing:\n%s", output)
			}
			if deps.ProjectID != projectA {
				t.Fatalf("dependency project after command = %q, want restored %q", deps.ProjectID, projectA)
			}
			if tt.name == "create" && createBaseBranch != "release" {
				t.Fatalf("create base branch = %q, want selected project config release", createBaseBranch)
			}
			if deps.Config != defaultConfig {
				t.Fatal("dependency config was not restored")
			}
		})
	}
}

func TestPRCommandInvalidExplicitProjectFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var commands int
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			commands++
			return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected PR request: %s", req.Command)
		}}).WithProjectID("default-project"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "default-project",
		RepoDir:   "/default/repo",
	}
	err := PRCommand(deps, PROptions{Command: "merge", Project: "missing", Number: 12, Confirm: true})
	if !errors.Is(err, config.ErrProjectNotFound) {
		t.Fatalf("error = %v, want project-not-found type", err)
	}
	if !strings.Contains(err.Error(), "refusing repository fallback") {
		t.Fatalf("error = %q, want actionable fallback refusal", err)
	}
	if commands != 0 {
		t.Fatalf("daemon commands = %d, want zero", commands)
	}
}

func TestApplyExplicitProjectOverrideCanonicalizesAndRestoresCommandContext(t *testing.T) {
	routes := registerCLIProjects(t, "Default", "Selected")
	selectedRepo := filepath.Join(os.Getenv("HOME"), "Selected")
	var routedProject string
	transport := &fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		routedProject = req.Meta.ProjectID.String()
		return responseWithJSON(req, map[string]any{}), nil
	}}
	deps := &Dependencies{
		ProjectID:      routes["Default"],
		RepoDir:        filepath.Join(os.Getenv("HOME"), "Default"),
		RuntimeRepoDir: "/runtime/default",
		DaemonClient:   daemonclient.New(transport).WithProjectID(routes["Default"]),
	}

	restore, err := applyExplicitProjectOverride(deps, "Selected")
	if err != nil {
		t.Fatalf("apply explicit project: %v", err)
	}
	if deps.ProjectID != routes["Selected"] || deps.RepoDir != selectedRepo {
		t.Fatalf("routed dependencies = project %q repo %q, want %q %q", deps.ProjectID, deps.RepoDir, routes["Selected"], selectedRepo)
	}
	_, err = deps.DaemonClient.Command(context.Background(), protocol.RequestEnvelope{Command: daemonclient.CommandTaskList})
	if err != nil {
		t.Fatalf("send routed command: %v", err)
	}
	if routedProject != routes["Selected"] {
		t.Fatalf("daemon metadata project = %q, want %q", routedProject, routes["Selected"])
	}

	restore()
	if deps.ProjectID != routes["Default"] || deps.RepoDir != filepath.Join(os.Getenv("HOME"), "Default") || deps.RuntimeRepoDir != "/runtime/default" {
		t.Fatalf("restored dependencies = project %q repo %q runtime %q", deps.ProjectID, deps.RepoDir, deps.RuntimeRepoDir)
	}
}

func TestApplyExplicitProjectOverrideUnknownProjectFailsWithoutMutation(t *testing.T) {
	routes := registerCLIProjects(t, "Default")
	deps := &Dependencies{ProjectID: routes["Default"], RepoDir: "/default", RuntimeRepoDir: "/runtime/default"}
	restore, err := applyExplicitProjectOverride(deps, "missing")
	if !errors.Is(err, config.ErrProjectNotFound) {
		t.Fatalf("error = %v, want project-not-found type", err)
	}
	if restore != nil {
		t.Fatal("restore function returned for rejected project")
	}
	if deps.ProjectID != routes["Default"] || deps.RepoDir != "/default" || deps.RuntimeRepoDir != "/runtime/default" {
		t.Fatalf("dependencies mutated on failure: %+v", deps)
	}
	if _, err := applyExplicitProjectOverride(nil, "Default"); err == nil {
		t.Fatal("nil dependencies accepted")
	}
}

func TestExplicitProjectEntryPointsRejectUnknownProjectBeforeDaemon(t *testing.T) {
	routes := registerCLIProjects(t, "Default")
	for _, tc := range []struct {
		name string
		call func(*Dependencies) error
	}{
		{name: "session start", call: func(d *Dependencies) error {
			return StartCommandWithOptions(d, "issue-1", SessionCommandOptions{Project: "missing"})
		}},
		{name: "worktree create", call: func(d *Dependencies) error {
			return WorktreeCreateCommand(d, WorktreeCreateOptions{IssueID: "issue-1", Project: "missing"})
		}},
		{name: "worktree delete", call: func(d *Dependencies) error {
			return WorktreeDeleteCommand(d, WorktreeDeleteOptions{IssueID: "issue-1", Project: "missing"})
		}},
		{name: "session capture", call: func(d *Dependencies) error {
			return SessionCaptureCommand(d, SessionCaptureOptions{IssueID: "issue-1", Project: "missing"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			deps := &Dependencies{
				Config: config.DefaultConfig(), ProjectID: routes["Default"], RepoDir: "/default", RuntimeRepoDir: "/runtime/default",
				DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(context.Context, protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					requests++
					return protocol.ResponseEnvelope{}, nil
				}}).WithProjectID(routes["Default"]),
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			err := tc.call(deps)
			if !errors.Is(err, config.ErrProjectNotFound) {
				t.Fatalf("error = %v, want project-not-found", err)
			}
			if requests != 0 {
				t.Fatalf("daemon requests = %d, want zero", requests)
			}
			if deps.ProjectID != routes["Default"] || deps.RepoDir != "/default" || deps.RuntimeRepoDir != "/runtime/default" {
				t.Fatalf("dependencies mutated: project=%q repo=%q runtime=%q", deps.ProjectID, deps.RepoDir, deps.RuntimeRepoDir)
			}
		})
	}
}

func TestPRCommandProjectPrecedenceWithoutExplicitSelection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	currentRepo := filepath.Join(home, "current")
	defaultRepo := filepath.Join(home, "configured-default")
	currentID, _ := config.ProjectIDForRoot(currentRepo)
	defaultID, _ := config.ProjectIDForRoot(defaultRepo)
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		DefaultProject: "Default",
		Projects: []config.Project{
			{ID: currentID, Name: "Current", Path: currentRepo},
			{ID: defaultID, Name: "Default", Path: defaultRepo},
		},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	tests := []struct {
		name      string
		projectID string
		repoDir   string
		wantID    string
		wantRepo  string
	}{
		{name: "current project beats configured default", projectID: currentID, repoDir: currentRepo, wantID: currentID, wantRepo: currentRepo},
		{name: "configured default used without current context", wantID: defaultID, wantRepo: defaultRepo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var routedProject string
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					routedProject = req.Meta.ProjectID.String()
					return responseWithJSON(req, daemonclient.PullRequestListResult{State: "open"}), nil
				}}).WithProjectID(tt.projectID),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: tt.projectID,
				RepoDir:   tt.repoDir,
			}
			output := captureStdout(t, func() error {
				return PRCommand(deps, PROptions{Command: "list", State: "open", Limit: 20})
			})
			if routedProject != tt.wantID {
				t.Fatalf("routed project = %q, want %q", routedProject, tt.wantID)
			}
			for _, want := range []string{"Project:", tt.wantID, "Repository: " + tt.wantRepo} {
				if !strings.Contains(output, want) {
					t.Fatalf("human output missing %q:\n%s", want, output)
				}
			}
		})
	}
}

func TestPRCommandStatusUsesPullRequestNumber(t *testing.T) {
	var refs []string
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandPRGet:
					var body daemonclient.PullRequestBranchParams
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal pr get body: %v", err)
					}
					refs = append(refs, "get:"+body.Branch)
					return responseWithJSON(req, daemonclient.PullRequestGetResult{
						PullRequest: prservice.PRInfo{Number: 12, Title: "Example", URL: "https://example.test/pr/12", State: "open", Branch: "feature", BaseRef: "main"},
					}), nil
				case daemonclient.CommandPRChecks:
					var body daemonclient.PullRequestChecksParams
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal pr checks body: %v", err)
					}
					refs = append(refs, "checks:"+body.Ref)
					return responseWithJSON(req, daemonclient.PullRequestChecksResult{Ref: body.Ref, ChecksStatus: "passing"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	if err := PRCommand(deps, PROptions{Command: "status", Number: 12, JSON: true}); err != nil {
		t.Fatalf("PRCommand returned error: %v", err)
	}
	want := []string{"get:12", "checks:12"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
}

func TestPRCreateRejectsPullRequestNumber(t *testing.T) {
	err := prCreateCommand(context.Background(), &Dependencies{}, PROptions{Number: 12, IssueID: "cxs"})
	if err == nil {
		t.Fatalf("prCreateCommand returned nil error")
	}
	if !strings.Contains(err.Error(), "does not accept pull request number") {
		t.Fatalf("error = %q", err.Error())
	}
}

func assertMetadataOnlyTaskGetManyRequest(t *testing.T, req protocol.RequestEnvelope, issueID string) {
	t.Helper()
	if req.Command != daemonclient.CommandTaskGetMany {
		t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskGetMany)
	}
	var body daemonclient.TaskIDsRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal task.get_many request body: %v", err)
	}
	if len(body.TaskIDs) != 1 || body.TaskIDs[0].String() != issueID {
		t.Fatalf("task_ids = %+v, want [%s]", body.TaskIDs, issueID)
	}
	if !body.IncludeAncestors || !body.ExcludeDependents || !body.MetadataOnly {
		t.Fatalf("request flags ancestors=%v exclude_dependents=%v metadata_only=%v, want all true", body.IncludeAncestors, body.ExcludeDependents, body.MetadataOnly)
	}
}

func assertSessionStartOperationSubmitRequest(t *testing.T, req protocol.RequestEnvelope, projectID, repoDir, issueID, baseBranch string) protocol.OperationSubmitRequestBody {
	t.Helper()
	if req.Command != protocol.CommandOperationSubmit {
		t.Fatalf("command = %q, want %q", req.Command, protocol.CommandOperationSubmit)
	}
	var body protocol.OperationSubmitRequestBody
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("unmarshal operation submit body: %v", err)
	}
	if body.ProjectID.String() != projectID || body.Kind != commandSessionStart || body.IssueID.String() != issueID {
		t.Fatalf("operation submit body = %+v, want project=%s kind=%s issue=%s", body, projectID, commandSessionStart, issueID)
	}
	var payload sessionRequestBody
	if err := json.Unmarshal(body.Payload, &payload); err != nil {
		t.Fatalf("unmarshal session start payload: %v", err)
	}
	if payload.ProjectID != projectID || payload.SessionID != issueID || payload.BaseBranch != baseBranch {
		t.Fatalf("session start payload = %+v, want project=%s session=%s base=%s", payload, projectID, issueID, baseBranch)
	}
	wantKeys := []string{
		"issue:" + projectID + ":" + issueID,
		"worktree:" + issueID,
	}
	if strings.TrimSpace(repoDir) != "" {
		wantKeys = append(wantKeys, "session:"+naming.CanonicalSessionID(repoDir, issueID))
	}
	for _, key := range wantKeys {
		if !containsString(body.ResourceKeys, key) {
			t.Fatalf("resource keys = %+v, want %s", body.ResourceKeys, key)
		}
	}
	if body.DedupeKey != commandSessionStart+":"+issueID {
		t.Fatalf("dedupe key = %q, want %q", body.DedupeKey, commandSessionStart+":"+issueID)
	}
	return body
}

func sessionStartOperationSubmitResponse(req protocol.RequestEnvelope, projectID, issueID string, state protocol.OperationState) protocol.ResponseEnvelope {
	return responseWithJSON(req, protocol.OperationSubmitResponseBody{
		Created: true,
		Operation: protocol.OperationRecord{
			OperationID: "op-start",
			ProjectID:   naming.ProjectID(projectID),
			Kind:        commandSessionStart,
			IssueID:     naming.IssueID(issueID),
			State:       state,
			EnqueuedAt:  time.Date(2026, time.June, 25, 5, 46, 21, 0, time.UTC),
		},
	})
}

func TestNewDependenciesAtUsesBaseProjectAndGlobalRuntimeForLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.RepoDir != repo {
		t.Fatalf("RepoDir = %q, want %q", deps.RepoDir, repo)
	}
	if deps.RuntimeRepoDir != repo {
		t.Fatalf("RuntimeRepoDir = %q, want %q", deps.RuntimeRepoDir, repo)
	}
	wantProjectID, err := config.ProjectIDForRoot(repo)
	if err != nil {
		t.Fatalf("ProjectIDForRoot() error = %v", err)
	}
	if deps.ProjectID != wantProjectID {
		t.Fatalf("ProjectID = %q, want %q", deps.ProjectID, wantProjectID)
	}
	if deps.DaemonSocket != config.GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.GlobalDaemonSocketPath())
	}
}

func TestNewDependenciesAtUsesGlobalRuntimeForLinkedWorktreeWithoutEnv(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.DaemonSocket != config.GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.GlobalDaemonSocketPath())
	}
	if deps.RuntimeRepoDir != repo {
		t.Fatalf("RuntimeRepoDir = %q, want %q", deps.RuntimeRepoDir, repo)
	}
}

func TestNewDependenciesAtExpandsTildeSessionLogDirOutsideWorktree(t *testing.T) {
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "global")
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	repo := filepath.Join(t.TempDir(), "app-worktree")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("MkdirAll(repo): %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = "~/.azedarach/logs"

	deps, err := NewDependenciesAt(cfg, repo)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.Logger == nil {
		t.Fatal("NewDependenciesAt() returned nil logger")
	}

	if _, err := os.Stat(filepath.Join(homeDir, ".azedarach", "logs", logging.CLILogFileName)); err != nil {
		t.Fatalf("Stat(home CLI log) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "~")); !os.IsNotExist(err) {
		t.Fatalf("repo literal tilde dir stat error = %v, want not exist", err)
	}
}

func TestNewDependenciesAtRejectsSharedDaemonFromLinkedAzedarachWorktreeBinary(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")
	executable := filepath.Join(worktree, "bin", "az")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatalf("MkdirAll(bin): %v", err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(executable): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	_, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err == nil {
		t.Fatal("NewDependenciesAt() error = nil, want shared daemon fence error")
	}
	if !strings.Contains(err.Error(), "refusing to use the shared production daemon") {
		t.Fatalf("NewDependenciesAt() error = %q, want shared daemon fence error", err)
	}
}

func TestNewDependenciesAtAllowsSharedDaemonFromCanonicalBinaryOutsideLinkedWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")
	executable := filepath.Join(repo, "bin", "az")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatalf("MkdirAll(bin): %v", err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(executable): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	previousExecutable := currentExecutable
	currentExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { currentExecutable = previousExecutable })
	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")

	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.DaemonSocket != config.GlobalDaemonSocketPath() {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.GlobalDaemonSocketPath())
	}
}

func TestNewDependenciesAtUsesScopedSocketForLinkedWorktreeWhenExplicit(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	start := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("MkdirAll(start): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "")
	deps, err := NewDependenciesAt(config.DefaultConfig(), start)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.DaemonSocket != config.ScopedDaemonSocketPath(start) {
		t.Fatalf("DaemonSocket = %q, want %q", deps.DaemonSocket, config.ScopedDaemonSocketPath(start))
	}
	if deps.RuntimeRepoDir != worktree {
		t.Fatalf("RuntimeRepoDir = %q, want %q", deps.RuntimeRepoDir, worktree)
	}
}

func TestNewDependenciesAtUsesDistinctProjectIDsForDistinctRoots(t *testing.T) {
	base := t.TempDir()
	startA := filepath.Join(base, "a", "repo")
	startB := filepath.Join(base, "b", "repo")

	if err := os.MkdirAll(filepath.Join(startA, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(startA .git): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(startB, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(startB .git): %v", err)
	}

	t.Setenv("PATH", "")

	depsA, err := NewDependenciesAt(config.DefaultConfig(), startA)
	if err != nil {
		t.Fatalf("NewDependenciesAt(startA) error = %v", err)
	}
	depsB, err := NewDependenciesAt(config.DefaultConfig(), startB)
	if err != nil {
		t.Fatalf("NewDependenciesAt(startB) error = %v", err)
	}

	if depsA.ProjectID == depsB.ProjectID {
		t.Fatalf("ProjectID collision: %q", depsA.ProjectID)
	}
}

func TestNewDependenciesAtIgnoresAmbientGitDirRoutingVars(t *testing.T) {
	base := t.TempDir()
	repoA := filepath.Join(base, "repo-a")
	repoB := filepath.Join(base, "repo-b")

	if err := os.MkdirAll(filepath.Join(repoA, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repoA .git): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoB, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repoB .git): %v", err)
	}

	t.Setenv("PATH", "")
	t.Setenv("GIT_DIR", filepath.Join(repoA, ".git"))
	t.Setenv("GIT_WORK_TREE", repoA)

	deps, err := NewDependenciesAt(config.DefaultConfig(), repoB)
	if err != nil {
		t.Fatalf("NewDependenciesAt() error = %v", err)
	}
	if deps.RepoDir != repoB {
		t.Fatalf("RepoDir = %q, want %q", deps.RepoDir, repoB)
	}
}

func TestMergePreflightDirtyFilesIgnoreUntrackedOnlyStatus(t *testing.T) {
	status := daemonclient.GitStatus{
		HasChanges: true,
		Untracked:  []string{".azedarach/images/", "docs/"},
	}

	if got := dirtyFilesFromGitStatus(status); len(got) != 0 {
		t.Fatalf("dirty files = %v, want none for untracked-only status", got)
	}
	if got := summarizeGitStatusCounts(status); got != "clean" {
		t.Fatalf("summary = %q, want clean", got)
	}
}

func TestMergePreflightDirtyFilesKeepTrackedChanges(t *testing.T) {
	status := daemonclient.GitStatus{
		HasChanges: true,
		Modified:   []string{"b.go"},
		Staged:     []string{"a.go"},
		Untracked:  []string{"scratch/"},
	}

	got := dirtyFilesFromGitStatus(status)
	want := []string{"a.go", "b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dirty files = %v, want %v", got, want)
	}
	if got := summarizeGitStatusCounts(status); got != "1 staged, 1 modified" {
		t.Fatalf("summary = %q, want tracked-only summary", got)
	}
}

func (f *fakeDaemonTransport) Handshake(ctx context.Context, hello protocol.Hello) (protocol.HelloAck, error) {
	if f.handshakeFn != nil {
		return f.handshakeFn(ctx, hello)
	}
	return protocol.HelloAck{Accepted: true}, nil
}

func (f *fakeDaemonTransport) Command(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	if req.Command == protocol.CommandOrchestrationIntent && !f.passOrchestrationIntent {
		return f.emulateOrchestrationIntent(ctx, req)
	}
	if f.commandFn != nil {
		resp, err := f.commandFn(ctx, req)
		if err == nil && req.Command == daemonclient.CommandTaskGraphReadiness && resp.OK {
			_ = json.Unmarshal(resp.Body, &f.lastGraphReadiness)
		}
		return resp, err
	}
	return protocol.ResponseEnvelope{}, nil
}

func (f *fakeDaemonTransport) emulateOrchestrationIntent(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	var intent protocol.OrchestrationIntentRequest
	if err := json.Unmarshal(req.Body, &intent); err != nil {
		return protocol.ResponseEnvelope{}, err
	}
	requested := append([]string(nil), intent.IssueIDs...)
	if len(requested) == 0 {
		requested = append(requested, f.lastGraphReadiness.Runnable...)
	}
	runnable := make(map[string]struct{}, len(f.lastGraphReadiness.Runnable))
	active := make(map[string]struct{}, len(f.lastGraphReadiness.Active))
	nestedRoots := make(map[string]struct{}, len(f.lastGraphReadiness.NestedRoots))
	for _, id := range f.lastGraphReadiness.Runnable {
		runnable[id] = struct{}{}
	}
	for _, id := range f.lastGraphReadiness.Active {
		active[id] = struct{}{}
	}
	for _, nested := range f.lastGraphReadiness.NestedRoots {
		nestedRoots[nested.IssueID] = struct{}{}
	}
	result := protocol.OrchestrationIntentResult{Scope: intent.Scope, Kind: intent.Kind, IntentKey: intent.IntentKey, Requested: requested, Skipped: map[string]string{}, Failed: map[string]string{}}
	limit := intent.Limit
	if limit <= 0 {
		limit = 3
	}
	for _, issueID := range requested {
		if _, ok := runnable[issueID]; !ok {
			if _, ok := nestedRoots[issueID]; ok {
				result.Skipped[issueID] = fmt.Sprintf("nested-root-start-orchestrator-session: az orchestrator-session start --root %s", issueID)
			} else if _, ok := active[issueID]; ok {
				result.Skipped[issueID] = "session-already-running"
			} else if reason := f.lastGraphReadiness.Blocked[issueID]; reason != "" {
				result.Skipped[issueID] = reason
			} else {
				result.Skipped[issueID] = "not-runnable"
			}
			continue
		}
		if len(result.Launched) >= limit {
			result.Skipped[issueID] = "limit-reached"
			continue
		}
		claimBody, _ := json.Marshal(daemonclient.TaskOwnershipRequest{TaskID: naming.IssueID(issueID), OwnerID: intent.ActorID, OwnerKind: "agent"})
		claimReq := req
		claimReq.Command, claimReq.Body = daemonclient.CommandTaskClaimOwnership, claimBody
		claimResp, err := f.commandFn(ctx, claimReq)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		if !claimResp.OK {
			if claimResp.Error != nil && claimResp.Error.Code == protocol.ErrorCodeConflict {
				result.Skipped[issueID] = claimResp.Error.Message
			} else if claimResp.Error != nil {
				result.Failed[issueID] = claimResp.Error.Message
			} else {
				result.Failed[issueID] = "ownership claim failed"
			}
			continue
		}
		parsed := naming.IssueID(issueID)
		sessionID := naming.CanonicalSessionID(intent.RepoDir, issueID)
		payload, _ := json.Marshal(sessionRequestBody{ProjectID: req.Meta.ProjectID.String(), SessionID: sessionID, BaseBranch: intent.BaseBranch})
		submitBody, _ := json.Marshal(protocol.OperationSubmitRequestBody{ProjectID: req.Meta.ProjectID, Kind: commandSessionStart, IssueID: parsed, DedupeKey: commandSessionStart + ":" + issueID, ResourceKeys: []string{"issue:" + req.Meta.ProjectID.String() + ":" + issueID, "worktree:" + issueID, "session:" + sessionID}, Payload: payload})
		submitReq := req
		submitReq.Command, submitReq.Body = protocol.CommandOperationSubmit, submitBody
		submitResp, err := f.commandFn(ctx, submitReq)
		if err != nil {
			result.Failed[issueID] = err.Error()
			continue
		}
		if !submitResp.OK {
			if submitResp.Error != nil {
				result.Failed[issueID] = submitResp.Error.Message
			} else {
				result.Failed[issueID] = "operation submit failed"
			}
			continue
		}
		var submitted protocol.OperationSubmitResponseBody
		if err := json.Unmarshal(submitResp.Body, &submitted); err != nil {
			return protocol.ResponseEnvelope{}, err
		}
		launch := protocol.OrchestrationLaunch{IssueID: issueID, SessionID: sessionID, OperationID: submitted.Operation.OperationID.String(), OperationState: string(submitted.Operation.State)}
		result.Started = append(result.Started, issueID)
		result.Launched = append(result.Launched, launch)
	}
	return responseWithJSON(req, result), nil
}

func (f *fakeDaemonTransport) Subscribe(context.Context, string, uint64) (<-chan protocol.EventEnvelope, error) {
	return nil, errors.New("not implemented")
}

func TestStartCommandSubmitsDaemonOperation(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					gotReq = req
					return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateQueued), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	if gotReq.Command != protocol.CommandOperationSubmit {
		t.Fatalf("command = %q, want %q", gotReq.Command, protocol.CommandOperationSubmit)
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandTaskGetMany, daemonclient.CommandTaskMergeBaseTarget, protocol.CommandOperationSubmit}) {
		t.Fatalf("commands = %v", commands)
	}
	assertSessionStartOperationSubmitRequest(t, gotReq, "proj", "/repo", "issue-1", "main")
	for _, want := range []string{
		"Session start is still queued for issue-1.",
		"Operation: op-start (queued)",
		"Follow up: az operation get --id op-start --wait",
		"Follow up: az session status issue-1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "context deadline exceeded") {
		t.Fatalf("output = %q", output)
	}
}

func TestStartCommandValidatesIssueFromMetadataOnlyLocalSnapshot(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Local issue", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					<-ctx.Done()
					return protocol.ResponseEnvelope{}, ctx.Err()
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					gotReq = req
					return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateQueued), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj").WithReadWaitPolicy(daemonclient.ReadWaitPolicy{
			Default:  time.Nanosecond,
			Explicit: 2 * time.Nanosecond,
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	if err := StartCommand(deps, "issue-1"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}
	if gotReq.Command != protocol.CommandOperationSubmit {
		t.Fatalf("command = %q, want %q", gotReq.Command, protocol.CommandOperationSubmit)
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandTaskGetMany, daemonclient.CommandTaskMergeBaseTarget, protocol.CommandOperationSubmit}) {
		t.Fatalf("commands = %v", commands)
	}
}

func TestParseWorktreeCreateArgs(t *testing.T) {
	opts, err := ParseWorktreeCreateArgs([]string{"--project", "proj-a", "--base", "parent-branch", "--json", "az-1"})
	if err != nil {
		t.Fatalf("ParseWorktreeCreateArgs error: %v", err)
	}
	if opts.Project != "proj-a" || opts.BaseBranch != "parent-branch" || opts.IssueID != "az-1" || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseWorktreeDeleteArgs(t *testing.T) {
	opts, err := ParseWorktreeDeleteArgs([]string{"--project", "proj-a", "--force", "--delete-branch", "--json", "az-1"})
	if err != nil {
		t.Fatalf("ParseWorktreeDeleteArgs error: %v", err)
	}
	if opts.Project != "proj-a" || opts.IssueID != "az-1" || !opts.Force || !opts.DeleteBranch || !opts.JSON {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestWorktreeCreateCommandCreatesWorktreeWithoutStartingSession(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "az-1", Title: "Parent", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var gotCreateReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case daemonclient.CommandWorktreeCreate:
					gotCreateReq = req
					return responseWithJSON(req, map[string]any{
						"project_id":  "proj",
						"base_branch": "az/parent",
						"worktree": map[string]any{
							"path":     "/tmp/az-1",
							"branch":   "az/az-1",
							"issue_id": "az-1",
						},
					}), nil
				case daemonclient.CommandSessionStart:
					t.Fatalf("unexpected session start command")
					return protocol.ResponseEnvelope{}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	output := captureStdout(t, func() error {
		return WorktreeCreateCommand(deps, WorktreeCreateOptions{IssueID: "az-1"})
	})

	if gotCreateReq.Command != daemonclient.CommandWorktreeCreate {
		t.Fatalf("command = %q, want %q", gotCreateReq.Command, daemonclient.CommandWorktreeCreate)
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandTaskGetMany, daemonclient.CommandTaskMergeBaseTarget, daemonclient.CommandWorktreeCreate}) {
		t.Fatalf("commands = %v", commands)
	}
	var body map[string]any
	if err := json.Unmarshal(gotCreateReq.Body, &body); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if body["project_id"] != "proj" || body["issue_id"] != "az-1" || body["base_branch"] != "main" {
		t.Fatalf("create body = %+v", body)
	}
	if output != "Worktree ready: /tmp/az-1\nBranch: az/az-1\nBase: az/parent\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestWorktreeDeleteCommandCanDeleteBranch(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "az-1", Title: "Task", Status: domain.StatusInProgress},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var gotRemoveReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandWorktreeRemove:
					gotRemoveReq = req
					return responseWithJSON(req, map[string]any{
						"project_id":     "proj",
						"issue_id":       "az-1",
						"branch":         "riordan/az-1/task",
						"branch_deleted": true,
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	output := captureStdout(t, func() error {
		return WorktreeDeleteCommand(deps, WorktreeDeleteOptions{IssueID: "az-1", Force: true, DeleteBranch: true})
	})

	if gotRemoveReq.Command != daemonclient.CommandWorktreeRemove {
		t.Fatalf("command = %q, want %q", gotRemoveReq.Command, daemonclient.CommandWorktreeRemove)
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandTaskGetMany, daemonclient.CommandWorktreeRemove}) {
		t.Fatalf("commands = %v", commands)
	}
	var body struct {
		ProjectID    string `json:"project_id"`
		IssueID      string `json:"issue_id"`
		Force        bool   `json:"force"`
		DeleteBranch bool   `json:"delete_branch"`
	}
	if err := json.Unmarshal(gotRemoveReq.Body, &body); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if body.ProjectID != "proj" || body.IssueID != "az-1" || !body.Force || !body.DeleteBranch {
		t.Fatalf("remove body = %+v, want force/delete_branch", body)
	}
	if output != "Worktree deleted for az-1\nBranch deleted: riordan/az-1/task\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestWorktreeCreateCommandJSONErrorIncludesRollbackDetails(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "az-1", Title: "Parent", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	detail := "worktree create failed for az-1: failed to create worktree with git worktree add -b user/az-1/task /tmp/az-1 main: hook failed (rolled back worktree /tmp/az-1)"
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case daemonclient.CommandWorktreeCreate:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              false,
						Error: &protocol.ErrorEnvelope{
							Code:      protocol.ErrorCodeInternal,
							Message:   detail,
							Retryable: false,
						},
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	output, err := captureStdoutAllowError(t, func() error {
		return WorktreeCreateCommand(deps, WorktreeCreateOptions{IssueID: "az-1", JSON: true})
	})
	if err == nil {
		t.Fatal("expected worktree create error")
	}
	for _, want := range []string{"failed to create worktree for az-1", "git worktree add", "hook failed", "rolled back worktree /tmp/az-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(output), &body); err != nil {
		t.Fatalf("unmarshal output JSON: %v\noutput:\n%s", err, output)
	}
	if body["ok"] != false || body["project_id"] != "proj" || body["issue_id"] != "az-1" || body["base_branch"] != "main" {
		t.Fatalf("json body = %+v", body)
	}
	errorMessage, _ := body["error"].(string)
	for _, want := range []string{"failed to create worktree for az-1", "git worktree add", "hook failed", "rolled back worktree /tmp/az-1"} {
		if !strings.Contains(errorMessage, want) {
			t.Fatalf("json error = %q, want substring %q", errorMessage, want)
		}
	}
}

func TestStartCommandSubmitsOperationWithDaemonTimeout(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	var submitDeadline time.Time
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					var ok bool
					submitDeadline, ok = ctx.Deadline()
					if !ok {
						t.Fatal("operation submit context missing deadline")
					}
					return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateQueued), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	if err := StartCommand(deps, "issue-1"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}

	remaining := time.Until(submitDeadline)
	if remaining < daemonCommandTimeout-2*time.Second {
		t.Fatalf("operation submit deadline too short: remaining=%s", remaining)
	}
	if remaining > daemonCommandTimeout+2*time.Second {
		t.Fatalf("operation submit deadline too long: remaining=%s", remaining)
	}
}

func TestStartCommandPrintsPendingOperationStatus(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateRunning), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})
	if !strings.Contains(output, "Session start is still running for issue-1.") || !strings.Contains(output, "Operation: op-start (running)") {
		t.Fatalf("output = %q, want pending operation status", output)
	}
}

func TestStartCommandUsesParentWorktreeBranchForChildIssue(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	parentID := naming.IssueID("az-parent")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: parentID, Title: "Parent", Status: domain.StatusInProgress},
		{ID: "az-child", Title: "Child", Status: domain.StatusOpen, ParentID: &parentID},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-child")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-parent",
						Branch:         "az/az-parent",
						WorktreePath:   "/tmp/parent",
						BranchAttached: true,
						Reason:         "selected closest ancestor worktree branch",
						AncestorChain:  []string{"az-parent"},
					}), nil
				case protocol.CommandOperationSubmit:
					gotReq = req
					return sessionStartOperationSubmitResponse(req, "proj", "az-child", protocol.OperationStateQueued), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	if err := StartCommand(deps, "az-child"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}
	if len(commands) != 3 || commands[0] != daemonclient.CommandTaskGetMany || commands[1] != daemonclient.CommandTaskMergeBaseTarget || commands[2] != protocol.CommandOperationSubmit {
		t.Fatalf("commands = %v", commands)
	}
	assertSessionStartOperationSubmitRequest(t, gotReq, "proj", "/repo", "az-child", "az/az-parent")
}

func TestStartCommandUsesNearestAncestorWorktreeBranchForNestedChildIssue(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	commands := []string{}
	rootID := naming.IssueID("az-root")
	planningID := naming.IssueID("az-plan")
	childID := naming.IssueID("az-child")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: rootID, Title: "Root", Status: domain.StatusInProgress},
		{ID: planningID, Title: "Planning", Status: domain.StatusOpen, ParentID: &rootID},
		{ID: childID, Title: "Child", Status: domain.StatusOpen, ParentID: &planningID},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-child")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-root",
						Branch:         "user/az-root/root-branch",
						WorktreePath:   "/tmp/root",
						BranchAttached: true,
						Reason:         "selected closest ancestor worktree branch",
						AncestorChain:  []string{"az-plan", "az-root"},
					}), nil
				case protocol.CommandOperationSubmit:
					gotReq = req
					return sessionStartOperationSubmitResponse(req, "proj", "az-child", protocol.OperationStateQueued), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	if err := StartCommand(deps, "az-child"); err != nil {
		t.Fatalf("StartCommand error: %v", err)
	}
	if len(commands) != 3 ||
		commands[0] != daemonclient.CommandTaskGetMany ||
		commands[1] != daemonclient.CommandTaskMergeBaseTarget ||
		commands[2] != protocol.CommandOperationSubmit {
		t.Fatalf("commands = %v", commands)
	}
	assertSessionStartOperationSubmitRequest(t, gotReq, "proj", "/repo", "az-child", "user/az-root/root-branch")
}

func TestAttachKillAndStatusCommandsUseDaemonEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		command     func(*Dependencies, string) error
		sessionID   string
		wantCommand string
		response    string
	}{
		{
			name:        "attach",
			command:     AttachCommand,
			sessionID:   "issue-2",
			wantCommand: commandSessionAttach,
			response:    "Attaching to session: issue-2\n(Press Ctrl+B then D to detach)\n",
		},
		{
			name:        "kill",
			command:     KillCommand,
			sessionID:   "issue-3",
			wantCommand: commandSessionStop,
			response:    "Killing session: issue-3\n✓ Session killed: issue-3\n  Note: Worktree is preserved. Use 'git worktree remove' to clean up.\n",
		},
		{
			name:        "status",
			command:     StatusCommand,
			sessionID:   "",
			wantCommand: commandSessionStatus,
			response:    "Active Sessions (1):\n\nISSUE ID  STATUS   TITLE\n-------  ------   -----\nbead-4   active   Example task\n\nUse 'az attach <issue-id>' to attach to a session\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			commands := []string{}
			taskListBody, err := marshalTaskListBody([]domain.Task{
				{ID: naming.IssueID(tt.sessionID), Title: "Example task", Status: domain.StatusOpen},
			})
			if err != nil {
				t.Fatalf("marshal task list: %v", err)
			}
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						commands = append(commands, req.Command)
						if tt.wantCommand != commandSessionStatus && req.Command == daemonclient.CommandTaskGetMany {
							assertMetadataOnlyTaskGetManyRequest(t, req, tt.sessionID)
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								CompletedAt:     req.SentAt,
								OK:              true,
								Body:            taskListBody,
							}, nil
						}
						if req.Command != tt.wantCommand {
							t.Fatalf("unexpected command: %s", req.Command)
						}
						gotReq = req
						return responseWithOutput(req, tt.response), nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			output := captureStdout(t, func() error {
				return tt.command(deps, tt.sessionID)
			})

			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			switch tt.wantCommand {
			case commandSessionAttach, commandSessionStop:
				if len(commands) != 2 || commands[0] != daemonclient.CommandTaskGetMany || commands[1] != tt.wantCommand {
					t.Fatalf("commands = %v", commands)
				}
			case commandSessionStatus:
				if len(commands) != 1 || commands[0] != commandSessionStatus {
					t.Fatalf("commands = %v", commands)
				}
			}
			if gotReq.Meta.ProjectID != "proj" {
				t.Fatalf("meta project_id = %q, want proj", gotReq.Meta.ProjectID)
			}
			if output != tt.response {
				t.Fatalf("output = %q, want %q", output, tt.response)
			}
		})
	}
}

func TestSessionCaptureCommandUsesTypedDaemonClient(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandSessionCapture {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				gotReq = req
				var body protocol.SessionCaptureRequestBody
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal request: %v", err)
				}
				if body.ProjectID != "proj" || body.SessionID != "az-2" || body.Lines != 33 {
					t.Fatalf("request body = %+v, want project/session/lines", body)
				}
				respBody, err := json.Marshal(protocol.SessionCaptureResponseBody{
					ProjectID: "proj",
					IssueID:   "az-2",
					SessionID: "proj-az-2",
					Lines:     33,
					Output:    "worker output\n",
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					CompletedAt:     req.SentAt,
					OK:              true,
					Body:            respBody,
				}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return SessionCaptureCommand(deps, SessionCaptureOptions{IssueID: "az-2", Lines: 33})
	})

	if gotReq.Command != daemonclient.CommandSessionCapture {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandSessionCapture)
	}
	if output != "worker output\n" {
		t.Fatalf("output = %q, want pane output", output)
	}
}

func TestParseSessionCaptureArgsSupportsIssueFlag(t *testing.T) {
	opts, err := ParseSessionCaptureArgs([]string{"--issue", "az-2", "--project", "proj", "--lines", "33", "--json"})
	if err != nil {
		t.Fatalf("ParseSessionCaptureArgs error = %v", err)
	}
	if opts.IssueID != "az-2" || opts.Project != "proj" || opts.Lines != 33 || !opts.JSON {
		t.Fatalf("opts = %+v, want issue/project/lines/json", opts)
	}

	opts, err = ParseSessionCaptureArgs([]string{"az-3"})
	if err != nil {
		t.Fatalf("ParseSessionCaptureArgs positional error = %v", err)
	}
	if opts.IssueID != "az-3" || opts.Lines != 120 {
		t.Fatalf("positional opts = %+v, want default lines", opts)
	}
}

func TestSessionRestartAllCommandUsesDaemonEnvelope(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandSessionRestartAll {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				gotReq = req
				body, err := json.Marshal(protocol.SessionRestartAllResponseBody{
					ProjectID: naming.ProjectID("proj"),
					ForceBusy: false,
					Restarted: 1,
					Skipped:   1,
					Sessions: []protocol.SessionRestartAllItem{
						{IssueID: naming.IssueID("az-1"), SessionID: naming.SessionID("proj-az-1"), Activity: "idle", Restarted: true},
						{IssueID: naming.IssueID("az-2"), SessionID: naming.SessionID("proj-az-2"), Activity: "busy", Skipped: true, Reason: "busy"},
					},
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					CompletedAt:     req.SentAt,
					OK:              true,
					Body:            body,
				}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return SessionRestartAllCommand(deps, SessionRestartAllOptions{Yolo: true})
	})

	if gotReq.Command != daemonclient.CommandSessionRestartAll {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandSessionRestartAll)
	}
	var body protocol.SessionRestartAllRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.ForceBusy || !body.Yolo {
		t.Fatalf("request body = %+v, want project proj force false", body)
	}
	if !strings.Contains(output, "Restarted 1 session(s) (1 skipped, 0 failed)") ||
		!strings.Contains(output, "az-2") ||
		!strings.Contains(output, "skipped: busy") {
		t.Fatalf("output = %q, want restart summary and skipped session", output)
	}
}

func TestSessionRestartAllCommandPrintsFailuresBeforeReturningError(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := json.Marshal(protocol.SessionRestartAllResponseBody{
					ProjectID: naming.ProjectID("proj"),
					Failed:    1,
					Sessions: []protocol.SessionRestartAllItem{{
						IssueID:   naming.IssueID("az-1"),
						SessionID: naming.SessionID("proj-az-1"),
						Activity:  "idle",
						Error:     "send-keys failed",
					}},
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					CompletedAt:     req.SentAt,
					OK:              true,
					Body:            body,
				}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	var commandErr error
	output := captureStdout(t, func() error {
		commandErr = SessionRestartAllCommand(deps, SessionRestartAllOptions{})
		return nil
	})

	if commandErr == nil || !strings.Contains(commandErr.Error(), "failed to restart 1 session") {
		t.Fatalf("error = %v, want failed restart error", commandErr)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "failed: send-keys failed") {
		t.Fatalf("output = %q, want failed session detail", output)
	}
}

func TestStatusCommandSkipsTaskValidationReadWait(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var gotReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					<-ctx.Done()
					return protocol.ResponseEnvelope{}, ctx.Err()
				case commandSessionStatus:
					gotReq = req
					return responseWithOutput(req, "ok\n"), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj").WithReadWaitPolicy(daemonclient.ReadWaitPolicy{
			Default:  time.Nanosecond,
			Explicit: 2 * time.Nanosecond,
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StatusCommand(deps, "eqa")
	})

	if output != "ok\n" {
		t.Fatalf("output = %q, want ok", output)
	}
	if gotReq.Command != commandSessionStatus {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandSessionStatus)
	}
	if commands[0] == daemonclient.CommandTaskList {
		t.Fatalf("commands = %v, want status to avoid task validation read wait", commands)
	}
	var body sessionRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.SessionID != "eqa" {
		t.Fatalf("session request body = %+v, want project proj session eqa", body)
	}
}

func TestSessionCommandsRejectInvalidOrUnknownIssueIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name       string
		command    func(*Dependencies, string) error
		issueID    string
		taskIDs    []string
		wantSubstr string
	}{
		{
			name:       "start invalid id format",
			command:    StartCommand,
			issueID:    "bad id",
			wantSubstr: "invalid issue id",
		},
		{
			name:       "start unknown id",
			command:    StartCommand,
			issueID:    "az-missing",
			taskIDs:    []string{"az-1"},
			wantSubstr: "issue not found: az-missing",
		},
		{
			name:       "attach unknown id",
			command:    AttachCommand,
			issueID:    "az-missing",
			taskIDs:    []string{"az-1"},
			wantSubstr: "issue not found: az-missing",
		},
		{
			name:       "kill unknown id",
			command:    KillCommand,
			issueID:    "az-missing",
			taskIDs:    []string{"az-1"},
			wantSubstr: "issue not found: az-missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commands := []string{}
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						commands = append(commands, req.Command)
						if req.Command != daemonclient.CommandTaskGetMany {
							t.Fatalf("unexpected command: %s", req.Command)
						}
						assertMetadataOnlyTaskGetManyRequest(t, req, tt.issueID)
						tasks := make([]domain.Task, 0, len(tt.taskIDs))
						for _, id := range tt.taskIDs {
							tasks = append(tasks, domain.Task{ID: naming.IssueID(id), Title: id, Status: domain.StatusOpen})
						}
						body, err := marshalTaskListBody(tasks)
						if err != nil {
							t.Fatalf("marshal task list: %v", err)
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							Meta:            req.Meta,
							CompletedAt:     req.SentAt,
							OK:              true,
							Body:            body,
						}, nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			err := tt.command(deps, tt.issueID)
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("err = %v, want substring %q", err, tt.wantSubstr)
			}

			if strings.Contains(tt.wantSubstr, "invalid issue id") {
				if len(commands) != 0 {
					t.Fatalf("commands for invalid ID = %v, want none", commands)
				}
				return
			}
			if len(commands) != 1 || commands[0] != daemonclient.CommandTaskGetMany {
				t.Fatalf("commands for unknown ID = %v, want [%s]", commands, daemonclient.CommandTaskGetMany)
			}
		})
	}
}

func TestSessionCommandsResolveProjectPrefixedIssueIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "project-a")
	repoB := filepath.Join(home, "project-b")
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "project-a", Path: repoA},
			{Name: "project-b", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("project A id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("project B id: %v", err)
	}

	tests := []struct {
		name        string
		command     func(*Dependencies, string) error
		arg         string
		wantCommand string
	}{
		{
			name:        "stop explicit project issue",
			command:     KillCommand,
			arg:         "project-b:bxc",
			wantCommand: commandSessionStop,
		},
		{
			name:        "status explicit project issue",
			command:     StatusCommand,
			arg:         "project-b:bxc",
			wantCommand: commandSessionStatus,
		},
		{
			name:        "stop default tmux session form",
			command:     KillCommand,
			arg:         naming.CanonicalSessionID(repoB, "bxc"),
			wantCommand: commandSessionStop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			resolvedIssueID := "bxc"
			commands := []string{}
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						commands = append(commands, req.Command+":"+req.Meta.ProjectID.String())
						switch req.Command {
						case daemonclient.CommandTaskGetMany:
							var requestBody daemonclient.TaskIDsRequest
							if err := json.Unmarshal(req.Body, &requestBody); err != nil {
								t.Fatalf("unmarshal task.get_many request body: %v", err)
							}
							if len(requestBody.TaskIDs) != 1 {
								t.Fatalf("task_ids = %+v, want one", requestBody.TaskIDs)
							}
							if !requestBody.IncludeAncestors || !requestBody.ExcludeDependents || !requestBody.MetadataOnly {
								t.Fatalf("request flags ancestors=%v exclude_dependents=%v metadata_only=%v, want all true", requestBody.IncludeAncestors, requestBody.ExcludeDependents, requestBody.MetadataOnly)
							}
							resolvedIssueID = requestBody.TaskIDs[0].String()
							projectID := req.Meta.ProjectID.String()
							tasks := []domain.Task{{ID: "local-only", Title: "Local", Status: domain.StatusOpen}}
							if projectID == projectB {
								tasks = []domain.Task{{ID: naming.IssueID(resolvedIssueID), Title: "Remote", Status: domain.StatusOpen}}
							}
							body, err := marshalTaskListBodyForProject(projectID, tasks)
							if err != nil {
								t.Fatalf("marshal task list: %v", err)
							}
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								CompletedAt:     req.SentAt,
								OK:              true,
								Body:            body,
							}, nil
						case tt.wantCommand:
							gotReq = req
							return responseWithOutput(req, "ok\n"), nil
						default:
							t.Fatalf("unexpected command: %s", req.Command)
							return protocol.ResponseEnvelope{}, nil
						}
					},
				}).WithProjectID(projectA),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: projectA,
				RepoDir:   repoA,
			}

			output := captureStdout(t, func() error {
				return tt.command(deps, tt.arg)
			})

			if output != "ok\n" {
				t.Fatalf("output = %q, want ok", output)
			}
			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			if gotReq.Meta.ProjectID.String() != projectB {
				t.Fatalf("meta project_id = %q, want %q", gotReq.Meta.ProjectID, projectB)
			}
			var body sessionRequestBody
			if err := json.Unmarshal(gotReq.Body, &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body.ProjectID != projectB || body.SessionID != resolvedIssueID {
				t.Fatalf("session request body = %+v, want project %s issue %s", body, projectB, resolvedIssueID)
			}
			if len(commands) == 0 || !strings.Contains(commands[len(commands)-1], tt.wantCommand+":"+projectB) {
				t.Fatalf("commands = %v, want final %s:%s", commands, tt.wantCommand, projectB)
			}
		})
	}
}

func TestParseSessionStartArgsSupportsProjectFlag(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantIssueID string
		wantProject string
		wantWait    bool
	}{
		{
			name:        "project before issue",
			args:        []string{"--project", "azedarach", "cif"},
			wantIssueID: "cif",
			wantProject: "azedarach",
		},
		{
			name:        "project after issue with wait",
			args:        []string{"cif", "--project", "azedarach", "--wait"},
			wantIssueID: "cif",
			wantProject: "azedarach",
			wantWait:    true,
		},
		{
			name:        "project equals form",
			args:        []string{"--project=azedarach", "cif"},
			wantIssueID: "cif",
			wantProject: "azedarach",
		},
		{
			name:        "wait before issue",
			args:        []string{"--wait", "cif"},
			wantIssueID: "cif",
			wantWait:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIssueID, gotOpts, err := ParseSessionStartArgs(tt.args, true, "usage")
			if err != nil {
				t.Fatalf("ParseSessionStartArgs error = %v", err)
			}
			if gotIssueID != tt.wantIssueID || gotOpts.Project != tt.wantProject || gotOpts.Wait != tt.wantWait {
				t.Fatalf("issueID=%q opts=%+v, want issueID=%q project=%q wait=%v", gotIssueID, gotOpts, tt.wantIssueID, tt.wantProject, tt.wantWait)
			}
		})
	}
}

func TestParseSessionStartArgsRejectsProjectFlagOnAlias(t *testing.T) {
	_, _, err := ParseSessionStartArgs([]string{"--project", "azedarach", "cif"}, false, "usage: az start <issue-id> [--wait]")
	if err == nil || !strings.Contains(err.Error(), "usage: az start <issue-id> [--wait]") {
		t.Fatalf("err = %v, want alias usage", err)
	}
}

func TestStartCommandWithProjectOptionTargetsRegisteredProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "project-a")
	repoB := filepath.Join(home, "project-b")
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "azedarach", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("project A id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("project B id: %v", err)
	}

	var gotReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command+":"+req.Meta.ProjectID.String())
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "cif")
					projectID := req.Meta.ProjectID.String()
					tasks := []domain.Task{{ID: "local-only", Title: "Local", Status: domain.StatusOpen}}
					if projectID == projectB {
						tasks = []domain.Task{{ID: "cif", Title: "Remote", Status: domain.StatusOpen}}
					}
					body, err := marshalTaskListBodyForProject(projectID, tasks)
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            body,
					}, nil
				case protocol.CommandOperationSubmit:
					gotReq = req
					return sessionStartOperationSubmitResponse(req, projectB, "cif", protocol.OperationStateQueued), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "cif",
						TargetID: "base",
						Branch:   "main",
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID(projectA),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: projectA,
		RepoDir:   repoA,
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "cif", SessionCommandOptions{Project: "azedarach"})
	})

	if !strings.Contains(output, "Operation: op-start (queued)") {
		t.Fatalf("output = %q, want queued operation", output)
	}
	if gotReq.Command != protocol.CommandOperationSubmit {
		t.Fatalf("command = %q, want %q", gotReq.Command, protocol.CommandOperationSubmit)
	}
	if gotReq.Meta.ProjectID.String() != projectB {
		t.Fatalf("meta project_id = %q, want %q", gotReq.Meta.ProjectID, projectB)
	}
	assertSessionStartOperationSubmitRequest(t, gotReq, projectB, repoB, "cif", "main")
	if !reflect.DeepEqual(commands, []string{
		daemonclient.CommandTaskGetMany + ":" + projectB,
		daemonclient.CommandTaskMergeBaseTarget + ":" + projectB,
		protocol.CommandOperationSubmit + ":" + projectB,
	}) {
		t.Fatalf("commands = %v, want task list and start scoped to %s", commands, projectB)
	}
}

func TestStartCommandWithProjectOptionAutostartsFromRequestedProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "project-a")
	repoB := filepath.Join(home, "project-b")
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "azedarach", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("project A id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("project B id: %v", err)
	}

	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })
	fake := &fakeLauncher{}
	var gotLauncherRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotLauncherRepoDir = repoDir
		return fake
	}

	var gotStartReq protocol.RequestEnvelope
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				if !fake.startCalled {
					return protocol.HelloAck{}, errors.New("daemon socket unavailable")
				}
				return protocol.HelloAck{Accepted: true}, nil
			},
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command+":"+req.Meta.ProjectID.String())
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "cif")
					projectID := req.Meta.ProjectID.String()
					tasks := []domain.Task{}
					if projectID == projectB {
						tasks = []domain.Task{{ID: "cif", Title: "Remote", Status: domain.StatusOpen}}
					}
					body, err := marshalTaskListBodyForProject(projectID, tasks)
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						CompletedAt:     req.SentAt,
						OK:              true,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "cif",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					gotStartReq = req
					return sessionStartOperationSubmitResponse(req, projectB, "cif", protocol.OperationStateQueued), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID(projectA),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      projectA,
		RepoDir:        repoA,
		RuntimeRepoDir: repoA,
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "cif", SessionCommandOptions{Project: "azedarach"})
	})

	if !strings.Contains(output, "Operation: op-start (queued)") {
		t.Fatalf("output = %q, want queued operation", output)
	}
	if !fake.startCalled {
		t.Fatal("expected daemon autostart")
	}
	if gotLauncherRepoDir != repoB {
		t.Fatalf("launcher repoDir = %q, want requested project repo %q", gotLauncherRepoDir, repoB)
	}
	if gotStartReq.Meta.ProjectID.String() != projectB {
		t.Fatalf("session start project = %q, want %q", gotStartReq.Meta.ProjectID, projectB)
	}
	assertSessionStartOperationSubmitRequest(t, gotStartReq, projectB, repoB, "cif", "main")
	if !reflect.DeepEqual(commands, []string{
		daemonclient.CommandTaskGetMany + ":" + projectB,
		daemonclient.CommandTaskMergeBaseTarget + ":" + projectB,
		protocol.CommandOperationSubmit + ":" + projectB,
	}) {
		t.Fatalf("commands = %v, want all scoped to %s", commands, projectB)
	}
	if deps.ProjectID != projectA || deps.RepoDir != repoA || deps.RuntimeRepoDir != repoA {
		t.Fatalf("deps after start = project %q repo %q runtime %q, want restored %q/%q/%q",
			deps.ProjectID, deps.RepoDir, deps.RuntimeRepoDir, projectA, repoA, repoA)
	}
}

func TestSessionCommandsKeepBareIssueIDsCurrentProjectScoped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoA := filepath.Join(home, "project-a")
	repoB := filepath.Join(home, "project-b")
	if err := config.SaveProjectsRegistry(&config.ProjectsRegistry{
		Projects: []config.Project{
			{Name: "project-b", Path: repoB},
		},
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	projectA, err := config.ProjectIDForRoot(repoA)
	if err != nil {
		t.Fatalf("project A id: %v", err)
	}
	projectB, err := config.ProjectIDForRoot(repoB)
	if err != nil {
		t.Fatalf("project B id: %v", err)
	}

	seenProjects := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskGetMany {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				assertMetadataOnlyTaskGetManyRequest(t, req, "bxc")
				projectID := req.Meta.ProjectID.String()
				seenProjects = append(seenProjects, projectID)
				tasks := []domain.Task{}
				if projectID == projectB {
					tasks = []domain.Task{{ID: "bxc", Title: "Remote", Status: domain.StatusOpen}}
				}
				body, err := marshalTaskListBodyForProject(projectID, tasks)
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					CompletedAt:     req.SentAt,
					OK:              true,
					Body:            body,
				}, nil
			},
		}).WithProjectID(projectA),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: projectA,
		RepoDir:   repoA,
	}

	err = KillCommand(deps, "bxc")
	if err == nil || !strings.Contains(err.Error(), "issue not found: bxc") {
		t.Fatalf("err = %v, want current-project issue not found", err)
	}
	if len(seenProjects) != 1 || seenProjects[0] != projectA {
		t.Fatalf("task get projects = %v, want only current project %s", seenProjects, projectA)
	}
}

func TestStartCommandWithWaitPrintsSubmittedOperationOutput(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	calls := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				calls = append(calls, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateQueued), nil
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-start",
							ProjectID:   "proj",
							Kind:        commandSessionStart,
							IssueID:     "issue-1",
							State:       protocol.OperationStateDone,
							Result:      mustJSON(t, commandOutputBody{Output: "wrapped output\n"}),
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "issue-1", SessionCommandOptions{Wait: true, PollInterval: time.Millisecond})
	})

	if output != "wrapped output\n" {
		t.Fatalf("output = %q, want wrapped output", output)
	}
	if !reflect.DeepEqual(calls, []string{
		daemonclient.CommandTaskGetMany,
		daemonclient.CommandTaskMergeBaseTarget,
		protocol.CommandOperationSubmit,
		protocol.CommandOperationGet,
	}) {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestBranchMergeToBaseCommandUsesDaemonGitFlow(t *testing.T) {
	commands := make([]string, 0, 8)
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-123",
								"branch":   "riordan/az-123/some-change",
								"issue_id": "az-123",
							},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "trunk",
						Reason:   "no ancestor chain; selected default base target",
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree != "/tmp/azedarach-az-123" && body.Worktree != baseWorktree {
						t.Fatalf("git status worktree = %q", body.Worktree)
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "trunk",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Branch:   "trunk",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
						Branch:   "riordan/az-123/some-change",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	output := captureStdout(t, func() error {
		return BranchMergeToBaseCommand(deps, "az-123")
	})
	if !strings.Contains(output, "merge complete") {
		t.Fatalf("output = %q, want merge output", output)
	}
	if !strings.Contains(output, "Merged riordan/az-123/some-change into trunk (az-123)") {
		t.Fatalf("output = %q, want final summary", output)
	}
	if !strings.Contains(output, "- Phase timings:") || !strings.Contains(output, "merge:") {
		t.Fatalf("output = %q, want phase timings", output)
	}

	want := []string{
		daemonclient.CommandWorktreeList,
		daemonclient.CommandTaskMergeBaseTarget,
		daemonclient.CommandGitWorktreeForBranch,
		daemonclient.CommandGitStatus,
		daemonclient.CommandGitStatus,
		daemonclient.CommandGitCheckout,
		daemonclient.CommandGitMerge,
	}
	filtered := make([]string, 0, len(commands))
	for _, cmd := range commands {
		if cmd == protocol.CommandOperationGet {
			continue
		}
		filtered = append(filtered, cmd)
	}
	if !reflect.DeepEqual(filtered, want) {
		t.Fatalf("commands = %#v, want %#v", filtered, want)
	}
}

func TestBranchMergeToBaseCommandCreatesFixerIssueForHookFailure(t *testing.T) {
	commands := make([]string, 0, 12)
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	var createBody daemonclient.TaskCreateParams
	var depBody daemonclient.TaskDependencyParams
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-123", Status: domain.StatusInReview}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-123",
								"branch":   "riordan/az-123/some-change",
								"issue_id": "az-123",
							},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "trunk",
						Reason:   "no ancestor chain; selected default base target",
					}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "trunk",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: baseWorktree,
						Branch:   "trunk",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
						Branch:   "riordan/az-123/some-change",
						Result: gitservice.MergeResult{
							Success: false,
							Message: "commit-msg hook failed\nmissing trailer",
						},
					}), nil
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createBody); err != nil {
						t.Fatalf("unmarshal task create body: %v", err)
					}
					return responseWithJSON(req, daemonclient.TaskIDResponse{TaskID: "az-fix"}), nil
				case daemonclient.CommandTaskDependencyAdd:
					if err := json.Unmarshal(req.Body, &depBody); err != nil {
						t.Fatalf("unmarshal dependency body: %v", err)
					}
					return responseWithJSON(req, map[string]any{}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-123")
	if err == nil {
		t.Fatal("BranchMergeToBaseCommand error = nil, want hook failure")
	}
	errText := err.Error()
	for _, want := range []string{"commit-msg hook failed", "created fixer issue az-fix"} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error = %q, want %q", errText, want)
		}
	}
	if createBody.Title != "Fix merge hook/check failure for az-123" {
		t.Fatalf("fixer title = %q", createBody.Title)
	}
	for _, want := range []string{
		"Source issue: az-123",
		"Source branch: riordan/az-123/some-change",
		"Target branch: trunk",
		"Target worktree: " + baseWorktree,
		"Retry: az issue close --id az-123",
		"missing trailer",
	} {
		if !strings.Contains(createBody.Notes, want) {
			t.Fatalf("fixer notes missing %q:\n%s", want, createBody.Notes)
		}
	}
	if depBody.TaskID.String() != "az-123" || depBody.DependsOnID.String() != "az-fix" || depBody.Type != string(domain.DependencyBlocks) {
		t.Fatalf("dependency body = %+v, want source blocked by fixer", depBody)
	}
	if !containsString(commands, daemonclient.CommandTaskCreate) || !containsString(commands, daemonclient.CommandTaskDependencyAdd) {
		t.Fatalf("commands = %v, want fixer create and dependency add", commands)
	}
}

func TestBranchMergeToBaseCommandUsesAttachedTargetBranchWorktree(t *testing.T) {
	commands := make([]string, 0, 8)
	baseWorktree := t.TempDir()
	targetWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-123",
								"branch":   "riordan/az-123/some-change",
								"issue_id": "az-123",
							},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "trunk",
						Reason:   "no ancestor chain; selected default base target",
					}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git worktree branch body: %v", err)
					}
					if body.Branch != "trunk" {
						t.Fatalf("branch lookup = %q, want trunk", body.Branch)
					}
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch:   "trunk",
						Worktree: targetWorktree,
						Found:    true,
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree != "/tmp/azedarach-az-123" && body.Worktree != targetWorktree {
						t.Fatalf("git status worktree = %q", body.Worktree)
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git fetch body: %v", err)
					}
					if body.Worktree != targetWorktree {
						t.Fatalf("fetch worktree = %q, want %q", body.Worktree, targetWorktree)
					}
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: targetWorktree,
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitMerge:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git merge body: %v", err)
					}
					if body.Worktree != targetWorktree || body.Branch != "riordan/az-123/some-change" {
						t.Fatalf("merge body = %+v", body)
					}
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: targetWorktree,
						Branch:   "riordan/az-123/some-change",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				case daemonclient.CommandGitCheckout:
					t.Fatalf("checkout should not run when target branch is already attached to %s", targetWorktree)
					return protocol.ResponseEnvelope{}, nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	output := captureStdout(t, func() error {
		return BranchMergeToBaseCommand(deps, "az-123")
	})
	if !strings.Contains(output, "Merged riordan/az-123/some-change into trunk (az-123)") {
		t.Fatalf("output = %q, want final summary", output)
	}
	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitCheckout {
			t.Fatalf("checkout command should not be issued, commands=%v", commands)
		}
	}
}

func TestBranchMergeCommandExplicitDescendantTargetIgnoresCallerWorktree(t *testing.T) {
	sourceWorktree := t.TempDir()
	descendantWorktree := t.TempDir()
	t.Chdir(sourceWorktree)
	var mergeBody daemonclient.GitCommandRequest
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				return responseWithJSON(req, map[string]any{"worktrees": []map[string]any{
					{"path": sourceWorktree, "branch": "riordan/ancestor/work", "issue_id": "ancestor"},
					{"path": descendantWorktree, "branch": "riordan/descendant/work", "issue_id": "descendant"},
				}}), nil
			case daemonclient.CommandTaskMergeBaseTarget:
				t.Fatal("explicit issue target must not invoke inferred merge-base resolution")
			case daemonclient.CommandGitStatus:
				return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{HasChanges: false}}), nil
			case daemonclient.CommandGitMerge:
				if err := json.Unmarshal(req.Body, &mergeBody); err != nil {
					t.Fatalf("unmarshal merge body: %v", err)
				}
				return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
					Worktree: descendantWorktree,
					Branch:   "riordan/ancestor/work",
					Result:   gitservice.MergeResult{Success: true, Message: "merged"},
				}), nil
			default:
				return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
			}
			return protocol.ResponseEnvelope{}, nil
		}}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	if err := BranchMergeToBaseCommandWithOptions(deps, BranchMergeToBaseOptions{IssueID: "ancestor", Target: "descendant"}); err != nil {
		t.Fatalf("explicit descendant merge: %v", err)
	}
	if mergeBody.Worktree != descendantWorktree || mergeBody.Branch != "riordan/ancestor/work" {
		t.Fatalf("merge body = %+v, want source branch merged in named descendant worktree %q", mergeBody, descendantWorktree)
	}
}

func TestBranchMergeCommandExplicitBaseRequiresDaemonHumanAcceptance(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandWorktreeList:
				return responseWithJSON(req, map[string]any{"worktrees": []map[string]any{{
					"path": t.TempDir(), "branch": "riordan/root/work", "issue_id": "root",
				}}}), nil
			case daemonclient.CommandTaskMergeBaseTarget:
				var body map[string]any
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal merge target body: %v", err)
				}
				if required, _ := body["require_human_acceptance"].(bool); !required {
					t.Fatalf("merge target request = %#v, want require_human_acceptance=true", body)
				}
				return protocol.ResponseEnvelope{}, fmt.Errorf("refusing root issue root integration into base without durable human acceptance")
			default:
				t.Fatalf("command %s must not run after acceptance refusal", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		}}).WithProjectID("proj"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ProjectID: "proj", RepoDir: t.TempDir(),
	}
	err := BranchMergeToBaseCommandWithOptions(deps, BranchMergeToBaseOptions{IssueID: "root", Target: "base"})
	if err == nil || !strings.Contains(err.Error(), "without durable human acceptance") {
		t.Fatalf("error = %v, want daemon acceptance refusal", err)
	}
}

func TestBranchMergeToBaseCommandFailsOnDirtyPreflight(t *testing.T) {
	commands := make([]string, 0, 8)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-123",
								"branch":   "riordan/az-123/some-change",
								"issue_id": "az-123",
							},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "az-123",
						TargetID: "base",
						Branch:   "main",
						Reason:   "no ancestor chain; selected default base target",
					}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree == "/tmp/azedarach-az-123" {
						return responseWithJSON(req, map[string]any{
							"status": gitservice.GitStatus{
								HasChanges: true,
								Modified:   []string{"foo.go"},
							},
						}), nil
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	err := BranchMergeToBaseCommand(deps, "az-123")
	if err == nil || !strings.Contains(err.Error(), "merge preflight failed") {
		t.Fatalf("err = %v, want preflight failure", err)
	}
	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitFetch || cmd == daemonclient.CommandGitCheckout || cmd == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected post-preflight command: %s", cmd)
		}
	}
}

func TestBranchMergeToBaseCommandUsesEnvIssueIDWhenArgumentMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-999")

	commands := make([]string, 0, 8)
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-999", Status: domain.StatusOpen}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-999",
								"branch":   "riordan/az-999/some-change",
								"issue_id": "az-999",
							},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-999", TargetID: "base", Branch: "trunk"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: ".", Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: ".", Branch: "trunk"}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: ".",
						Branch:   "riordan/az-999/some-change",
						Result: gitservice.MergeResult{
							Success: true,
						},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	if err := BranchMergeToBaseCommand(deps, ""); err != nil {
		t.Fatalf("BranchMergeToBaseCommand error = %v", err)
	}

	foundMerge := false
	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitMerge {
			foundMerge = true
			break
		}
	}
	if !foundMerge {
		t.Fatalf("expected git merge command in flow, commands=%v", commands)
	}
}

func TestBranchMergeToBaseCommandTreatsAzedarachRuntimeConfigAsDirtyInPreflight(t *testing.T) {
	commands := make([]string, 0, 8)
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "bhv", Status: domain.StatusOpen}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-bhv",
								"branch":   "riordan/bhv/fix-mtm-timeout",
								"issue_id": "bhv",
							},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "bhv", TargetID: "base", Branch: "trunk"}), nil
				case daemonclient.CommandGitStatus:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git status body: %v", err)
					}
					if body.Worktree == baseWorktree {
						return responseWithJSON(req, map[string]any{
							"status": gitservice.GitStatus{
								HasChanges: true,
								Modified:   []string{".azedarach/config.json"},
							},
						}), nil
					}
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Branch: "trunk"}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
						Branch:   "riordan/bhv/fix-mtm-timeout",
						Result: gitservice.MergeResult{
							Success: true,
						},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "bhv")
	if err == nil || !strings.Contains(err.Error(), "merge preflight failed") {
		t.Fatalf("err = %v, want preflight failure", err)
	}

	for _, cmd := range commands {
		if cmd == daemonclient.CommandGitMerge {
			t.Fatalf("unexpected post-preflight command: %s", cmd)
		}
	}
}

func TestBranchMergeToBaseCommandUsesNearestNonClosedAncestorBranch(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	var mergedIn string
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusInProgress},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
							{"path": "/tmp/az-parent", "branch": "riordan/az-parent/work", "issue_id": "az-parent"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					var body map[string]any
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal merge target request: %v", err)
					}
					if required, _ := body["require_human_acceptance"].(bool); !required {
						t.Fatalf("merge target request = %#v, want protected mutation authorization", body)
					}
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-parent",
						Branch:         "riordan/az-parent/work",
						WorktreePath:   "/tmp/az-parent",
						BranchAttached: true,
						AncestorChain:  []string{"az-parent"},
					}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git fetch body: %v", err)
					}
					if body.Worktree != "/tmp/az-parent" {
						t.Fatalf("fetch worktree = %q, want /tmp/az-parent", body.Worktree)
					}
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: "/tmp/az-parent", Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					t.Fatalf("checkout should not run when parent branch is already attached")
					return protocol.ResponseEnvelope{}, nil
				case daemonclient.CommandGitMerge:
					var body daemonclient.GitCommandRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal git merge body: %v", err)
					}
					mergedIn = body.Worktree
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: body.Worktree,
						Branch:   "riordan/az-child/work",
						Result:   gitservice.MergeResult{Success: true},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	if err := BranchMergeToBaseCommand(deps, "az-child"); err != nil {
		t.Fatalf("BranchMergeToBaseCommand error = %v", err)
	}
	if mergedIn != "/tmp/az-parent" {
		t.Fatalf("merge worktree = %q, want /tmp/az-parent", mergedIn)
	}
}

func TestBranchMergeToBaseCommandBlocksChildWithoutAncestorWorktreeUnlessOverride(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusInProgress},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("refusing to merge child issue az-child directly into base: no active ancestor worktree branch was found; run `az worktree create az-parent`, then close the child into that target")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "no active ancestor worktree branch was found") || strings.Contains(err.Error(), "--allow-base-for-child") {
		t.Fatalf("err = %v, want child base merge refusal without override suggestion", err)
	}
}

func TestBranchMergeToBaseCommandRefusesOriginBaseBeforeGitMutation(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "preview"
	cfg.Git.WorkflowMode = "origin"
	commands := make([]string, 0, 2)
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"worktrees": []map[string]any{{
						"path": baseWorktree, "branch": "riordan/az-root/work", "issue_id": "az-root",
					}}}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("refusing direct base integration for az-root because git workflow mode is origin; run `az pr create --issue az-root`, `az pr status --issue az-root`, and `az pr merge --issue az-root --confirm`")
				default:
					t.Fatalf("origin refusal reached mutating command %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommandWithOptions(deps, BranchMergeToBaseOptions{IssueID: "az-root", Target: "base"})
	if err == nil {
		t.Fatal("BranchMergeToBaseCommandWithOptions error = nil, want origin-mode refusal")
	}
	for _, want := range []string{"workflow mode is origin", "az pr create", "az pr status", "az pr merge"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandWorktreeList, daemonclient.CommandTaskMergeBaseTarget}) {
		t.Fatalf("commands = %v, want target refusal before git mutation", commands)
	}
}

func TestBranchMergeToBaseCommandDefaultRefusesOriginBaseBeforeGitMutation(t *testing.T) {
	baseWorktree := t.TempDir()
	commands := make([]string, 0, 2)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{"worktrees": []map[string]any{{
						"path": baseWorktree, "branch": "riordan/az-root/work", "issue_id": "az-root",
					}}}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					var body map[string]any
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal merge target request: %v", err)
					}
					if required, _ := body["require_human_acceptance"].(bool); !required {
						t.Fatalf("merge target request = %#v, want protected mutation authorization", body)
					}
					return protocol.ResponseEnvelope{}, fmt.Errorf("refusing direct base integration for az-root because git workflow mode is origin")
				default:
					t.Fatalf("origin refusal reached mutating command %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-root")
	if err == nil || !strings.Contains(err.Error(), "workflow mode is origin") {
		t.Fatalf("BranchMergeToBaseCommand error = %v, want origin-mode refusal", err)
	}
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandWorktreeList, daemonclient.CommandTaskMergeBaseTarget}) {
		t.Fatalf("commands = %v, want refusal before git mutation", commands)
	}
}

func TestResolveParentWorktreeBaseBranchRemainsReadOnly(t *testing.T) {
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskMergeBaseTarget {
					t.Fatalf("command = %s, want merge target resolution", req.Command)
				}
				var body map[string]any
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal merge target request: %v", err)
				}
				if required, _ := body["require_human_acceptance"].(bool); required {
					t.Fatalf("read-only merge target request = %#v, want no integration authorization", body)
				}
				return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{TargetID: "az-parent", Branch: "riordan/az-parent/work"}), nil
			},
		}).WithProjectID("proj"),
	}

	branch, err := resolveParentWorktreeBaseBranch(context.Background(), deps, "main", "az-child")
	if err != nil {
		t.Fatalf("resolveParentWorktreeBaseBranch error: %v", err)
	}
	if branch != "riordan/az-parent/work" {
		t.Fatalf("branch = %q, want parent worktree branch", branch)
	}
}

func TestBranchMergeToBaseCommandFailsWhenIssueMissingFromTaskSnapshot(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-other", Status: domain.StatusOpen}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("cannot resolve merge target for az-child: issue not found in task projection; refusing fallback to base")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "issue not found in task projection") {
		t.Fatalf("err = %v, want missing issue projection error", err)
	}
}

func TestBranchMergeToBaseCommandFailsWhenParentMissingFromTaskSnapshot(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID}},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("cannot resolve merge target for az-child: parent issue az-parent missing from task projection; refusing fallback to base")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "parent issue az-parent missing from task projection") {
		t.Fatalf("err = %v, want missing parent projection error", err)
	}
}

func TestBranchMergeToBaseCommandBlocksChildBaseMergeWithoutOverride(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusDone},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return protocol.ResponseEnvelope{}, fmt.Errorf("refusing to merge child issue az-child directly into base: no active ancestor worktree branch was found; run `az worktree create az-parent`, then close the child into that target")
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommand(deps, "az-child")
	if err == nil || !strings.Contains(err.Error(), "az worktree create az-parent") || strings.Contains(err.Error(), "--allow-base-for-child") {
		t.Fatalf("err = %v, want child base merge refusal without override suggestion", err)
	}
}

func TestBranchMergeToBaseCommandAllowsChildBaseMergeWithOverride(t *testing.T) {
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					parentID := naming.IssueID("az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{
							{ID: "az-child", Status: domain.StatusOpen, ParentID: &parentID},
							{ID: "az-parent", Status: domain.StatusDone},
						},
					}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:       "az-child",
						TargetID:      "base",
						Branch:        "trunk",
						AncestorChain: []string{"az-parent"},
					}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{
						"status": gitservice.GitStatus{HasChanges: false},
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Remote: "origin"}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{Worktree: baseWorktree, Branch: "trunk"}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: baseWorktree,
						Branch:   "riordan/az-child/work",
						Result:   gitservice.MergeResult{Success: true},
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	err := BranchMergeToBaseCommandWithOptions(deps, BranchMergeToBaseOptions{
		IssueID:           "az-child",
		AllowBaseForChild: true,
	})
	if err != nil {
		t.Fatalf("BranchMergeToBaseCommandWithOptions error = %v", err)
	}
}

func TestBranchAgentMergeCommandLaunchesAgentWhenPreflightConflicts(t *testing.T) {
	commands := make([]string, 0, 4)
	var resolveBody protocol.SessionResolveConflictRequestBody
	baseWorktree := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Git.BaseBranch = "trunk"
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{
								"path":     "/tmp/azedarach-az-123",
								"branch":   "riordan/az-123/some-change",
								"issue_id": "az-123",
							},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-123", TargetID: "base", Branch: "trunk"}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "trunk",
						Found:  false,
					}), nil
				case daemonclient.CommandGitMergePreflight:
					var body daemonclient.GitMergePreflightRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal preflight body: %v", err)
					}
					if body.SourceID != "az-123" || body.TargetID != "base" || body.TargetRef != "trunk" || body.SourceBranch != "riordan/az-123/some-change" {
						t.Fatalf("preflight body = %+v", body)
					}
					return responseWithJSON(req, daemonclient.GitMergePreflightResponse{
						SourceID:       "az-123",
						SourceWorktree: "/tmp/azedarach-az-123",
						TargetID:       "base",
						TargetWorktree: baseWorktree,
						Clean:          false,
						Reasons:        []string{"Merge would conflict in 1 files: README.md"},
						ConflictFiles:  []string{"README.md"},
					}), nil
				case daemonclient.CommandSessionResolveConflict:
					if err := json.Unmarshal(req.Body, &resolveBody); err != nil {
						t.Fatalf("unmarshal resolve body: %v", err)
					}
					return responseWithJSON(req, protocol.SessionResolveConflictResponseBody{
						ProjectID:  naming.ProjectID("proj"),
						IssueID:    naming.IssueID("az-123"),
						SessionID:  naming.SessionID("az-123"),
						Worktree:   "/tmp/azedarach-az-123",
						WindowName: "resolve-conflict",
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   baseWorktree,
	}

	output := captureStdout(t, func() error {
		return BranchAgentMergeCommand(deps, BranchAgentMergeOptions{IssueID: "az-123", Target: "base"})
	})
	if !strings.Contains(output, "Agent merge launched for az-123 -> base") {
		t.Fatalf("output = %q, want launched summary", output)
	}
	if resolveBody.IssueID != "az-123" || resolveBody.Worktree != "/tmp/azedarach-az-123" {
		t.Fatalf("resolve body = %+v", resolveBody)
	}
	if !reflect.DeepEqual(resolveBody.ConflictFiles, []string{"README.md"}) {
		t.Fatalf("resolve conflict files = %+v", resolveBody.ConflictFiles)
	}
	if !strings.Contains(resolveBody.Prompt, "merge trunk into riordan/az-123/some-change") {
		t.Fatalf("prompt = %q, want base merge instruction", resolveBody.Prompt)
	}
	want := []string{daemonclient.CommandWorktreeList, daemonclient.CommandTaskMergeBaseTarget, daemonclient.CommandGitWorktreeForBranch, daemonclient.CommandGitMergePreflight, daemonclient.CommandSessionResolveConflict}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestBranchAgentMergeCommandCleanPreflightDoesNotLaunchAgent(t *testing.T) {
	commands := make([]string, 0, 4)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/azedarach-az-123", "branch": "az/az-123", "issue_id": "az-123"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-123", TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitMergePreflight:
					return responseWithJSON(req, daemonclient.GitMergePreflightResponse{Clean: true}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return BranchAgentMergeCommand(deps, BranchAgentMergeOptions{IssueID: "az-123"})
	})
	if !strings.Contains(output, "Merge preflight clean for az-123 -> base; no agent needed.") {
		t.Fatalf("output = %q, want clean preflight message", output)
	}
	for _, command := range commands {
		if command == daemonclient.CommandSessionResolveConflict {
			t.Fatalf("unexpected resolve conflict command: %v", commands)
		}
	}
}

func TestBranchAgentMergeCommandBaseTargetUsesNearestNonClosedAncestorBranch(t *testing.T) {
	var preflightBody daemonclient.GitMergePreflightRequest
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"worktrees": []map[string]any{
							{"path": "/tmp/az-child", "branch": "riordan/az-child/work", "issue_id": "az-child"},
							{"path": "/tmp/az-parent", "branch": "riordan/az-parent/work", "issue_id": "az-parent"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        "az-child",
						TargetID:       "az-parent",
						Branch:         "riordan/az-parent/work",
						WorktreePath:   "/tmp/az-parent",
						BranchAttached: true,
						AncestorChain:  []string{"az-parent"},
					}), nil
				case daemonclient.CommandGitMergePreflight:
					if err := json.Unmarshal(req.Body, &preflightBody); err != nil {
						t.Fatalf("unmarshal preflight body: %v", err)
					}
					return responseWithJSON(req, daemonclient.GitMergePreflightResponse{Clean: true}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return BranchAgentMergeCommand(deps, BranchAgentMergeOptions{IssueID: "az-child", Target: "base"})
	})
	if !strings.Contains(output, "Merge preflight clean for az-child -> base; no agent needed.") {
		t.Fatalf("output = %q, want clean preflight summary", output)
	}
	if preflightBody.TargetID != "az-parent" || preflightBody.TargetRef != "riordan/az-parent/work" {
		t.Fatalf("preflight body = %+v", preflightBody)
	}
}

func TestStartCommandPrintsPendingOperationState(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskGetMany {
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				}
				if req.Command == daemonclient.CommandTaskMergeBaseTarget {
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				}
				if req.Command != protocol.CommandOperationSubmit {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateQueued), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommand(deps, "issue-1")
	})

	for _, want := range []string{
		"Session start is still queued for issue-1.",
		"Operation: op-start (queued)",
		"Follow up: az operation get --id op-start --wait",
		"Follow up: az session status issue-1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestStartCommandWithWaitReportsPendingOperationOnWaitDeadline(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	commands := []string{}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateRunning), nil
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-start",
							ProjectID:   "proj",
							Kind:        commandSessionStart,
							IssueID:     "issue-1",
							State:       protocol.OperationStateRunning,
							Progress: &protocol.OperationProgress{
								Phase:   "init_commands",
								Message: "launch sent; configured init commands likely running before agent hooks",
								Percent: 90,
							},
							EnqueuedAt: time.Date(2026, time.June, 25, 5, 46, 21, 0, time.UTC),
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "issue-1", SessionCommandOptions{
			Wait:         true,
			PollInterval: time.Hour,
			WaitTimeout:  time.Millisecond,
		})
	})

	if !reflect.DeepEqual(commands, []string{
		daemonclient.CommandTaskGetMany,
		daemonclient.CommandTaskMergeBaseTarget,
		protocol.CommandOperationSubmit,
		protocol.CommandOperationGet,
	}) {
		t.Fatalf("commands = %+v", commands)
	}
	for _, want := range []string{
		"Session start is still running for issue-1.",
		"Operation: op-start (running)",
		"Progress: launch sent; configured init commands likely running before agent hooks (90%)",
		"Follow up: az operation get --id op-start --wait",
		"Follow up: az session status issue-1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestStartCommandWithWaitPrintsTerminalOperationOutput(t *testing.T) {
	var calls int
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				calls++
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				case protocol.CommandOperationSubmit:
					return sessionStartOperationSubmitResponse(req, "proj", "issue-1", protocol.OperationStateQueued), nil
				case protocol.CommandOperationGet:
					body, err := json.Marshal(protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-start",
							ProjectID:   "proj",
							Kind:        commandSessionStart,
							State:       protocol.OperationStateDone,
							Result:      mustJSON(t, commandOutputBody{Output: "started\n"}),
						},
					})
					if err != nil {
						t.Fatalf("marshal get response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            body,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return StartCommandWithOptions(deps, "issue-1", SessionCommandOptions{
			Wait:         true,
			PollInterval: time.Millisecond,
		})
	})

	if output != "started\n" {
		t.Fatalf("output = %q, want terminal operation output", output)
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
}

func TestOperationCommandsParseAndRender(t *testing.T) {
	getOpts, err := ParseOperationGetArgs([]string{"--wait", "--poll-interval", "500ms", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationGetArgs error: %v", err)
	}
	if getOpts.OperationID != "op-1" || !getOpts.Wait || getOpts.PollInterval != 500*time.Millisecond {
		t.Fatalf("get opts = %+v", getOpts)
	}

	logsOpts, err := ParseOperationLogsArgs([]string{"--json", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationLogsArgs error: %v", err)
	}
	if logsOpts.OperationID != "op-1" || !logsOpts.JSON {
		t.Fatalf("logs opts = %+v", logsOpts)
	}

	listOpts, err := ParseOperationListArgs([]string{"--issue", "az-1", "--kind", "session.start", "--state", "queued", "--states", "running", "--limit", "3"})
	if err != nil {
		t.Fatalf("ParseOperationListArgs error: %v", err)
	}
	if listOpts.IssueID != "az-1" || listOpts.Kind != "session.start" || listOpts.Limit != 3 || len(listOpts.States) != 2 {
		t.Fatalf("list opts = %+v", listOpts)
	}

	queueOpts, err := ParseOperationQueueArgs([]string{"--issue", "az-1", "--kind", "session.start", "--state", "queued", "--limit", "3", "--tree"})
	if err != nil {
		t.Fatalf("ParseOperationQueueArgs error: %v", err)
	}
	if queueOpts.IssueID != "az-1" || queueOpts.Kind != "session.start" || queueOpts.Limit != 3 || len(queueOpts.States) != 1 || !queueOpts.Tree {
		t.Fatalf("queue opts = %+v", queueOpts)
	}

	cancelOpts, err := ParseOperationCancelArgs([]string{"--reason", "user request", "op-1"})
	if err != nil {
		t.Fatalf("ParseOperationCancelArgs error: %v", err)
	}
	if cancelOpts.OperationID != "op-1" || cancelOpts.Reason != "user request" {
		t.Fatalf("cancel opts = %+v", cancelOpts)
	}
}

func TestParseLogArgs(t *testing.T) {
	opts, err := ParseLogArgs([]string{"--source", "daemon,tui", "--lines", "50", "--no-follow", "cli"})
	if err != nil {
		t.Fatalf("ParseLogArgs() error = %v", err)
	}
	if opts.Lines != 50 || opts.Follow {
		t.Fatalf("ParseLogArgs() lines/follow = %+v", opts)
	}
	if got, want := opts.Sources, []string{"daemon", "tui", "cli"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLogArgs() sources = %v, want %v", got, want)
	}

	defaultOpts, err := ParseLogArgs(nil)
	if err != nil {
		t.Fatalf("ParseLogArgs(nil) error = %v", err)
	}
	if got, want := defaultOpts.Sources, []string{"daemon", "tui", "cli"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLogArgs(nil) sources = %v, want %v", got, want)
	}

	_, err = ParseLogArgs([]string{"daemon", "tui", "--no-follow", "--lines", "100"})
	if err == nil || !strings.Contains(err.Error(), "flags must come before positional sources") {
		t.Fatalf("ParseLogArgs(interspersed) error = %v, want ordering guidance", err)
	}

	if _, err := ParseLogArgs([]string{"worker"}); err == nil {
		t.Fatal("expected unknown source error")
	}
	if _, err := ParseLogArgs([]string{"--lines", "0"}); err == nil {
		t.Fatal("expected lines validation error")
	}
}

func TestOperationCommandsUseDaemonClient(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   "proj",
							Kind:        "session.start",
							State:       protocol.OperationStateFailed,
							Payload:     mustJSON(t, map[string]string{"project_id": "proj", "session_id": "az-1"}),
							Result:      mustJSON(t, map[string]string{"output": "tmux attach failed"}),
							Error: &protocol.OperationError{
								Code:    protocol.ErrorCodeInternal,
								Message: "tmux attach failed: exited 1",
							},
						},
					}), nil
				case protocol.CommandOperationList:
					return responseWithJSON(req, protocol.OperationListResponseBody{
						ProjectID: "proj",
						Operations: []protocol.OperationRecord{
							{
								OperationID: "op-1",
								ProjectID:   "proj",
								Kind:        "session.start",
								State:       protocol.OperationStateQueued,
							},
						},
					}), nil
				case protocol.CommandOperationQueue:
					return responseWithJSON(req, protocol.OperationQueueResponseBody{
						ProjectID: "proj",
						Running: []protocol.OperationQueueEntry{{
							Operation: protocol.OperationRecord{
								OperationID: "op-running",
								ProjectID:   "proj",
								Kind:        "git.merge",
								IssueID:     "az-1",
								State:       protocol.OperationStateRunning,
							},
						}},
						Queued: []protocol.OperationQueueEntry{{
							Operation: protocol.OperationRecord{
								OperationID: "op-queued",
								ProjectID:   "proj",
								Kind:        "worktree.cleanup",
								IssueID:     "az-2",
								State:       protocol.OperationStateQueued,
							},
							BlockingOperationIDs: []naming.OperationID{"op-running"},
							BlockedResourceKeys:  []string{"worktree:/tmp/wt"},
						}},
					}), nil
				case protocol.CommandOperationCancel:
					return responseWithJSON(req, protocol.OperationCancelResponseBody{
						Cancelled: true,
						Operation: protocol.OperationRecord{
							OperationID: "op-1",
							ProjectID:   "proj",
							Kind:        "session.start",
							State:       protocol.OperationStateCancelled,
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		if err := OperationGetCommand(deps, OperationGetOptions{OperationID: "op-1"}); err != nil {
			return err
		}
		if err := OperationLogsCommand(deps, OperationLogsOptions{OperationID: "op-1"}); err != nil {
			return err
		}
		if err := OperationListCommand(deps, OperationListOptions{IssueID: "az-1", Limit: 5}); err != nil {
			return err
		}
		if err := OperationQueueCommand(deps, OperationQueueOptions{OperationListOptions: OperationListOptions{IssueID: "az-1", Limit: 5}, Tree: true}); err != nil {
			return err
		}
		return OperationCancelCommand(deps, OperationCancelOptions{OperationID: "op-1"})
	})

	for _, needle := range []string{"ID", "STATE", "KIND", "op-1", "failed", "queued", "cancelled", "Payload:", "Result (raw JSON):", "tmux attach failed: exited 1", "op-running running git.merge az-1", "`- op-queued queued worktree.cleanup az-2 blocked=worktree:/tmp/wt"} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output = %q, want %q", output, needle)
		}
	}
}

func TestSessionDiagnoseCommandPrintsBoundedDiagnostics(t *testing.T) {
	repoDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "az-1", Title: "Diagnose session", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: cfg,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				case commandSessionStatus:
					return responseWithJSON(req, commandOutputBody{Output: "no active session found for issue: az-1\n"}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": "proj",
						"worktrees": []map[string]string{{
							"path":     filepath.Join(repoDir, "../repo-az-1"),
							"branch":   "user/az-1/diagnose-session",
							"issue_id": "az-1",
						}},
					}), nil
				case protocol.CommandOperationList:
					return responseWithJSON(req, protocol.OperationListResponseBody{
						ProjectID: "proj",
						Operations: []protocol.OperationRecord{{
							OperationID: "op-start",
							ProjectID:   "proj",
							Kind:        commandSessionStart,
							IssueID:     "az-1",
							State:       protocol.OperationStateFailed,
							Progress: &protocol.OperationProgress{
								Phase:   "tmux_launch",
								Message: "creating tmux session",
								Percent: 70,
							},
							Error: &protocol.OperationError{
								Code:    protocol.ErrorCodeInternal,
								Message: "session start failed during tmux_launch\ncause=tmux new-session [az-1]: exit status 1",
							},
						}},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   repoDir,
	}

	output := captureStdout(t, func() error {
		return SessionDiagnoseCommand(deps, "az-1")
	})

	for _, needle := range []string{
		"Session diagnose: az-1",
		"Session status:",
		"no active session found for issue: az-1",
		"Worktree:",
		"user/az-1/diagnose-session",
		"Recent session.start operations:",
		"op-start failed phase=tmux_launch",
		"session start failed during tmux_launch cause=tmux new-session",
		"AI hook status:",
		"Logs:",
		filepath.Join(cfg.Session.LogDir, logging.DaemonLogFileName),
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("output = %q, missing %q", output, needle)
		}
	}
}

func TestLogCommandPrintsSourcePrefixedPrettyLines(t *testing.T) {
	repoDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	deps := &Dependencies{
		Config:  cfg,
		RepoDir: repoDir,
	}
	daemonLogPath := filepath.Join(repoDir, ".azedarach", logging.DaemonLogFileName)
	tuiLogPath := filepath.Join(cfg.Session.LogDir, logging.TUILogFileName)
	if err := os.MkdirAll(filepath.Dir(daemonLogPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(daemon log dir): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(tuiLogPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(tui log dir): %v", err)
	}
	if err := os.WriteFile(daemonLogPath, []byte("2026/04/01 16:50:04 INFO daemon started\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(daemon log): %v", err)
	}
	if err := os.WriteFile(tuiLogPath, []byte("time=2026-04-01T16:50:15.468+11:00 level=INFO msg=\"hello from tui\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tui log): %v", err)
	}

	output := captureStdout(t, func() error {
		return LogCommand(deps, LogOptions{
			Sources: []string{"daemon", "tui", "cli"},
			Lines:   25,
			Follow:  false,
		})
	})
	if !strings.Contains(output, "[daemon] 2026-04-01 16:50:04") {
		t.Fatalf("output = %q, want daemon timestamp in normalized format", output)
	}
	if !strings.Contains(output, "INFO daemon started") {
		t.Fatalf("output = %q, want daemon message", output)
	}
	if !strings.Contains(output, "[tui]") {
		t.Fatalf("output = %q, want tui source prefix", output)
	}
	if strings.Contains(output, "time=2026-04-01T16:50:15.468+11:00") {
		t.Fatalf("output = %q, want tui time field removed from message body", output)
	}
	if !strings.Contains(output, "level=INFO msg=\"hello from tui\"") {
		t.Fatalf("output = %q, want tui message payload", output)
	}
}

func TestResolveSessionLogDirFor_UsesScopedWorktreeDirInJustRunMode(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	t.Setenv("PATH", "")
	t.Chdir(nested)

	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	got := resolveSessionLogDirFor(cfg, nested)
	want := filepath.Join(worktree, ".azedarach")
	if got != want {
		t.Fatalf("resolveSessionLogDirFor() = %q, want %q", got, want)
	}
}

func TestLogCommandReadsScopedWorktreeDaemonAndTUILogs(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo worktrees): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/riordanpawley/azedarach\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	t.Setenv("AZEDARACH_DAEMON_SCOPE", "worktree")
	t.Setenv("AZEDARACH_DAEMON_SCOPE_SOURCE", "just-run")
	t.Setenv("PATH", "")
	t.Chdir(nested)

	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	deps := &Dependencies{
		Config:  cfg,
		RepoDir: nested,
	}

	daemonLogPath := filepath.Join(worktree, ".azedarach", logging.DaemonLogFileName)
	tuiLogPath := filepath.Join(worktree, ".azedarach", logging.TUILogFileName)
	if err := os.MkdirAll(filepath.Dir(daemonLogPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(log dir): %v", err)
	}
	if err := os.WriteFile(daemonLogPath, []byte("2026/04/01 16:50:04 INFO daemon started scoped\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(daemon log): %v", err)
	}
	if err := os.WriteFile(tuiLogPath, []byte("time=2026-04-01T16:50:15.468+11:00 level=INFO msg=\"hello from scoped tui\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(tui log): %v", err)
	}

	output := captureStdout(t, func() error {
		return LogCommand(deps, LogOptions{
			Sources: []string{"daemon", "tui"},
			Lines:   25,
			Follow:  false,
		})
	})
	if !strings.Contains(output, "daemon started scoped") {
		t.Fatalf("output = %q, want scoped daemon log line", output)
	}
	if !strings.Contains(output, "hello from scoped tui") {
		t.Fatalf("output = %q, want scoped tui log line", output)
	}
}

func TestLogCommandErrorsWhenAllSelectedLogsMissing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Session.LogDir = filepath.Join(t.TempDir(), "logs")
	deps := &Dependencies{
		Config:  cfg,
		RepoDir: t.TempDir(),
	}

	err := LogCommand(deps, LogOptions{
		Sources: []string{"daemon", "tui", "cli"},
		Lines:   10,
		Follow:  false,
	})
	if err == nil || !strings.Contains(err.Error(), "none of the selected log files exist yet") {
		t.Fatalf("LogCommand() error = %v, want missing files error", err)
	}
}

func TestCommandErrorUsesTransportMessage(t *testing.T) {
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{ID: "issue-1", Title: "Example", Status: domain.StatusOpen},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskGetMany {
					assertMetadataOnlyTaskGetManyRequest(t, req, "issue-1")
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              true,
						Body:            taskListBody,
					}, nil
				}
				if req.Command == daemonclient.CommandTaskMergeBaseTarget {
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:  "issue-1",
						TargetID: "base",
						Branch:   "main",
					}), nil
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: protocol.CurrentVersion,
					RequestID:       "req",
					Kind:            protocol.EnvelopeKindResponse,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:      protocol.ErrorCodeConflict,
						Message:   "session already exists: issue-1 (use 'az attach issue-1' to connect)",
						Retryable: false,
					},
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err = StartCommand(deps, "issue-1")
	if err == nil || !strings.Contains(err.Error(), "failed to submit session start") || !strings.Contains(err.Error(), "session already exists: issue-1 (use 'az attach issue-1' to connect)") {
		t.Fatalf("error = %v", err)
	}
}

func TestSessionResolveConflictCommandUsesDaemonClient(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				if req.Command != daemonclient.CommandSessionResolveConflict {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
				}
				return responseWithJSON(req, protocol.SessionResolveConflictResponseBody{
					ProjectID:  naming.ProjectID("proj"),
					IssueID:    naming.IssueID("bxc"),
					SessionID:  naming.SessionID("bxc"),
					Worktree:   "/tmp/bxc",
					WindowName: "resolve-conflict",
				}), nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return SessionResolveConflictCommand(deps, SessionResolveConflictOptions{
			IssueID:       " bxc ",
			Worktree:      "/tmp/bxc",
			ConflictFiles: []string{"README.md", "cmd/az/main.go"},
			Prompt:        "Resolve the conflict and keep tests green.",
		})
	})

	if gotReq.Command != daemonclient.CommandSessionResolveConflict {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandSessionResolveConflict)
	}
	var body protocol.SessionResolveConflictRequestBody
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.ProjectID != "proj" || body.IssueID != "bxc" || body.Worktree != "/tmp/bxc" {
		t.Fatalf("body route fields = %+v", body)
	}
	if !reflect.DeepEqual(body.ConflictFiles, []string{"README.md", "cmd/az/main.go"}) {
		t.Fatalf("conflict files = %+v", body.ConflictFiles)
	}
	if body.Prompt != "Resolve the conflict and keep tests green." {
		t.Fatalf("prompt = %q", body.Prompt)
	}
	wantOutput := "Conflict resolution agent launched for bxc\nWorktree: /tmp/bxc\nWindow: resolve-conflict\n"
	if output != wantOutput {
		t.Fatalf("output = %q, want %q", output, wantOutput)
	}
}

func TestSessionResolveConflictCommandReturnsDaemonError(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandSessionResolveConflict {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandSessionResolveConflict)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					CompletedAt:     req.SentAt,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:    protocol.ErrorCodeConflict,
						Message: "session is not attached",
					},
				}, nil
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := SessionResolveConflictCommand(deps, SessionResolveConflictOptions{IssueID: "bxc"})
	if err == nil || err.Error() != "failed to resolve conflicts for bxc: conflict: session is not attached" {
		t.Fatalf("error = %v", err)
	}
}

func TestResponseExitCode(t *testing.T) {
	tests := []struct {
		name string
		resp protocol.ResponseEnvelope
		want int
	}{
		{
			name: "success response",
			resp: protocol.ResponseEnvelope{OK: true},
			want: 0,
		},
		{
			name: "dry-run preview response",
			resp: protocol.ResponseEnvelope{
				OK: true,
				Body: mustApplyDryRunPreviewBody(t, applyDryRunPreviewBody{
					SchemaVersion:    protocol.ApplySchemaVersion,
					SnapshotRevision: 7,
					DryRun:           true,
					Operations: []applyDryRunPreviewOperationBody{
						{
							Index:   0,
							Command: "task.create",
							Body:    json.RawMessage(`{"title":"First task","description":"Draft","type":"task","priority":"high"}`),
						},
					},
				}),
			},
			want: 0,
		},
		{
			name: "partial failure response",
			resp: protocol.ResponseEnvelope{
				OK: true,
				Body: mustApplyResultBody(t, applyExecutionSummaryBody{
					Total:     3,
					Succeeded: 2,
					Failed:    1,
				}),
			},
			want: 2,
		},
		{
			name: "contract failure",
			resp: protocol.ResponseEnvelope{
				OK:   true,
				Body: []byte(`{"summary":`),
			},
			want: 1,
		},
		{
			name: "transport failure",
			resp: protocol.ResponseEnvelope{OK: false},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyResponseExitCode(tt.resp); got != tt.want {
				t.Fatalf("applyResponseExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseExportArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ExportOptions
		errContains string
	}{
		{
			name: "defaults",
			want: ExportOptions{
				Format: "json",
				Out:    "",
			},
		},
		{
			name: "explicit out path",
			args: []string{"--format", "json", "--out", "snapshot.json"},
			want: ExportOptions{
				Format: "json",
				Out:    "snapshot.json",
			},
		},
		{
			name:        "rejects unsupported format",
			args:        []string{"--format", "yaml"},
			errContains: "unsupported export format: yaml",
		},
		{
			name:        "rejects extra arguments",
			args:        []string{"unexpected"},
			errContains: "unexpected argument: unexpected",
		},
		{
			name:        "rejects unknown flag",
			args:        []string{"--bogus"},
			errContains: "flag provided but not defined: -bogus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseExportArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseExportArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseExportArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseConfigSetArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ConfigSetOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"spec.enabled", "false"},
			want: ConfigSetOptions{Key: "spec.enabled", Value: "false", ProjectDir: ""},
		},
		{
			name: "project dir option",
			args: []string{"--project-dir", "workspace", "spec.enabled", "yes"},
			want: ConfigSetOptions{Key: "spec.enabled", Value: "yes", ProjectDir: "workspace"},
		},
		{
			name:        "rejects missing args",
			args:        []string{"spec.enabled"},
			errContains: "usage: az config set <key> <value> [--project-dir <dir>]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConfigSetArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseConfigSetArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseConfigSetArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSyncArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        SyncOptions
		errContains string
	}{
		{
			name: "defaults",
			want: SyncOptions{},
		},
		{
			name: "all flag",
			args: []string{"--all"},
			want: SyncOptions{All: true},
		},
		{
			name: "positional project dir",
			args: []string{"workspace"},
			want: SyncOptions{ProjectDir: "workspace"},
		},
		{
			name: "project dir option",
			args: []string{"--project-dir", "workspace"},
			want: SyncOptions{ProjectDir: "workspace"},
		},
		{
			name:        "rejects conflicting project dir inputs",
			args:        []string{"--project-dir", "workspace", "other"},
			errContains: "usage: az sync [conflicts] [--all] [<directory>] [--project-dir <dir>] [--json]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSyncArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSyncArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseSyncArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseImplDeleteArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ImplDeleteOptions
		errContains string
	}{
		{
			name: "valid",
			args: []string{"--confirm", "go-bubbletea"},
			want: ImplDeleteOptions{Implementation: "go-bubbletea", Confirm: true},
		},
		{
			name:        "missing confirm",
			args:        []string{"go-bubbletea"},
			errContains: "missing required flag: --confirm",
		},
		{
			name:        "missing implementation",
			args:        []string{"--confirm"},
			errContains: "usage: az impl delete --confirm <implementation>",
		},
		{
			name:        "extra args",
			args:        []string{"go-bubbletea", "extra", "--confirm"},
			errContains: "usage: az impl delete --confirm <implementation>",
		},
		{
			name:        "reject positional before flag",
			args:        []string{"go-bubbletea", "--confirm"},
			errContains: "usage: az impl delete --confirm <implementation>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseImplDeleteArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImplDeleteArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseImplDeleteArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseImplListArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		errContains string
	}{
		{name: "valid"},
		{
			name:        "rejects extra args",
			args:        []string{"extra"},
			errContains: "usage: az impl list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseImplListArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImplListArgs() error = %v", err)
			}
		})
	}
}

func TestParseImplMigrateArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        ImplMigrateOptions
		errContains string
	}{
		{
			name: "valid",
			args: []string{"ts-opentui", "default"},
			want: ImplMigrateOptions{
				FromImplementation: "ts-opentui",
				ToImplementation:   "default",
			},
		},
		{
			name:        "missing destination",
			args:        []string{"ts-opentui"},
			errContains: "usage: az impl migrate <from-implementation> <to-implementation>",
		},
		{
			name:        "same source and destination",
			args:        []string{"default", "default"},
			errContains: "source and destination implementations must differ",
		},
		{
			name:        "extra args",
			args:        []string{"ts-opentui", "default", "extra"},
			errContains: "usage: az impl migrate <from-implementation> <to-implementation>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseImplMigrateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseImplMigrateArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseImplMigrateArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestExportCommandWritesStdoutByDefault(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 7,
	})

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return ExportCommand(deps, ExportOptions{Format: "json"})
	})

	if gotReq.Command != commandTaskSnapshotExport {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandTaskSnapshotExport)
	}
	if gotReq.Meta.ProjectID != "proj" {
		t.Fatalf("meta project_id = %q, want proj", gotReq.Meta.ProjectID)
	}
	if output != string(payload) {
		t.Fatalf("stdout = %q, want %q", output, string(payload))
	}
}

func TestExportCommandWritesFileWhenOutIsSet(t *testing.T) {
	var gotReq protocol.RequestEnvelope
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 11,
	})
	outPath := filepath.Join(t.TempDir(), "snapshot.json")

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	if err := ExportCommand(deps, ExportOptions{Format: "json", Out: outPath}); err != nil {
		t.Fatalf("ExportCommand() error = %v", err)
	}
	if gotReq.Command != commandTaskSnapshotExport {
		t.Fatalf("command = %q, want %q", gotReq.Command, commandTaskSnapshotExport)
	}

	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(written) != string(payload) {
		t.Fatalf("file = %q, want %q", string(written), string(payload))
	}
}

func TestExportCommandSurfacesFileWriteErrors(t *testing.T) {
	payload := mustSnapshotPayloadJSON(t, protocol.SnapshotPayload{
		SchemaVersion:    protocol.SnapshotSchemaVersion,
		ProtocolVersion:  protocol.CurrentVersion,
		SnapshotRevision: 23,
	})
	outPath := filepath.Join(t.TempDir(), "missing", "snapshot.json")

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	err := ExportCommand(deps, ExportOptions{Format: "json", Out: outPath})
	if err == nil || !strings.Contains(err.Error(), "write export output to") {
		t.Fatalf("error = %v, want write failure", err)
	}
}

func TestConfigSetCommandWritesSpecEnabledConfig(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	output := captureStdout(t, func() error {
		return ConfigSetCommand(deps, ConfigSetOptions{Key: "spec.enabled", Value: "off"})
	})

	if !strings.Contains(output, "Updated ") || !strings.Contains(output, "spec.enabled=false") {
		t.Fatalf("config output missing update line: %q", output)
	}
	if !strings.Contains(output, "Spec workflows are disabled.") {
		t.Fatalf("config output missing spec-disabled note: %q", output)
	}

	cfg, err := config.LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Spec.Enabled {
		t.Fatalf("Spec.Enabled = true, want false")
	}
}

func TestConfigSetCommandWritesLatencyTraceConfig(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	output := captureStdout(t, func() error {
		return ConfigSetCommand(deps, ConfigSetOptions{Key: "diagnostics.latencyTrace", Value: "on"})
	})

	if !strings.Contains(output, "diagnostics.latencyTrace=true") {
		t.Fatalf("config output missing diagnostics update: %q", output)
	}
	cfg, err := config.LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.Diagnostics.LatencyTrace {
		t.Fatalf("Diagnostics.LatencyTrace = false, want true")
	}
}

func TestConfigSetCommandWritesIssueAutoArchiveConfig(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	for _, opts := range []ConfigSetOptions{
		{Key: "issues.autoArchive.enabled", Value: "on"},
		{Key: "issues.autoArchive.closedAfterDays", Value: "45"},
		{Key: "issues.autoArchive.interval", Value: "12h"},
	} {
		_ = captureStdout(t, func() error {
			return ConfigSetCommand(deps, opts)
		})
	}

	cfg, err := config.LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.Issues.AutoArchive.Enabled {
		t.Fatalf("Issues.AutoArchive.Enabled = false, want true")
	}
	if cfg.Issues.AutoArchive.ClosedAfterDays != 45 {
		t.Fatalf("Issues.AutoArchive.ClosedAfterDays = %d, want 45", cfg.Issues.AutoArchive.ClosedAfterDays)
	}
	if cfg.Issues.AutoArchive.Interval != "12h" {
		t.Fatalf("Issues.AutoArchive.Interval = %q, want 12h", cfg.Issues.AutoArchive.Interval)
	}
}

func TestConfigSetCommandRejectsRemovedCloseCleanupConfig(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "issues.autoFinalizeOnClose", Value: "yes"})
	if err == nil || !strings.Contains(err.Error(), "Unsupported config key") || strings.Contains(err.Error(), "Supported keys: spec.enabled, diagnostics.latencyTrace, issues.autoFinalizeOnClose") {
		t.Fatalf("ConfigSetCommand error = %v, want removed setting rejected", err)
	}
}

func TestConfigSetCommandRejectsInvalidLatencyTraceBoolean(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "diagnostics.latencyTrace", Value: "maybe"})
	if err == nil || !strings.Contains(err.Error(), "Invalid boolean value 'maybe' for diagnostics.latencyTrace") {
		t.Fatalf("error = %v, want invalid latency trace boolean failure", err)
	}
}

func TestConfigSetCommandRejectsInvalidBoolean(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "spec.enabled", Value: "maybe"})
	if err == nil || !strings.Contains(err.Error(), "Invalid boolean value 'maybe' for spec.enabled") {
		t.Fatalf("error = %v, want invalid boolean failure", err)
	}
}

func TestConfigSetCommandRejectsUnsupportedKey(t *testing.T) {
	projectDir := t.TempDir()

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      projectDir,
	}

	err := ConfigSetCommand(deps, ConfigSetOptions{Key: "git.baseBranch", Value: "main"})
	if err == nil || !strings.Contains(err.Error(), "Unsupported config key 'git.baseBranch'") {
		t.Fatalf("error = %v, want unsupported key failure", err)
	}
}

func TestSyncCommandAllUsesDaemonWorktreeTargetsAndDaemonSyncRun(t *testing.T) {
	var gotWorktreeReq protocol.RequestEnvelope
	var gotSyncReq protocol.RequestEnvelope
	payload, err := json.Marshal(daemonclient.IssueSyncSummary{
		Provider:     "linear",
		Enabled:      true,
		RemoteIssues: 2,
		LocalIssues:  2,
		Imported:     1,
		UpdatedLocal: 1,
		PushedRemote: 1,
	})
	if err != nil {
		t.Fatalf("marshal sync summary: %v", err)
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					gotWorktreeReq = req
					body, err := json.Marshal(map[string]any{
						"project_id": "proj",
						"worktrees": []map[string]any{
							{
								"path":     filepath.Join("worktree-a"),
								"branch":   "az/worktree-a",
								"issue_id": "az-1",
							},
							{
								"path":     filepath.Join("worktree-b"),
								"branch":   "az/worktree-b",
								"issue_id": "az-2",
							},
						},
					})
					if err != nil {
						t.Fatalf("marshal worktrees: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        41,
						Body:            body,
					}, nil
				case daemonclient.CommandSyncRun:
					gotSyncReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            payload,
					}, nil
				default:
					t.Fatalf("unexpected command = %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   t.TempDir(),
	}

	output := captureStdout(t, func() error {
		return SyncCommand(deps, SyncOptions{All: true})
	})

	if gotWorktreeReq.Command != daemonclient.CommandWorktreeList {
		t.Fatalf("worktree command = %q, want %q", gotWorktreeReq.Command, daemonclient.CommandWorktreeList)
	}
	if gotWorktreeReq.Meta.ProjectID != "proj" {
		t.Fatalf("worktree project_id = %q, want proj", gotWorktreeReq.Meta.ProjectID)
	}
	var worktreeBody struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(gotWorktreeReq.Body, &worktreeBody); err != nil {
		t.Fatalf("unmarshal worktree request: %v", err)
	}
	if worktreeBody.ProjectID != "proj" {
		t.Fatalf("worktree request project_id = %q, want proj", worktreeBody.ProjectID)
	}
	if gotSyncReq.Command != daemonclient.CommandSyncRun {
		t.Fatalf("sync command = %q, want %q", gotSyncReq.Command, daemonclient.CommandSyncRun)
	}
	if !strings.Contains(output, "Syncing issue tracker state...") {
		t.Fatalf("sync output missing heading: %q", output)
	}
	if !strings.Contains(output, "Targets: 2 worktree(s)") {
		t.Fatalf("sync output missing target count: %q", output)
	}
	if !strings.Contains(output, "worktree-a") || !strings.Contains(output, "worktree-b") {
		t.Fatalf("sync output missing worktree paths: %q", output)
	}
	if !strings.Contains(output, "Linear: remote=2 local=2 imported=1 updated_local=1 pushed_remote=1 conflicts=0") {
		t.Fatalf("sync output missing sync summary: %q", output)
	}
}

func TestImplDeleteCommandRemovesAssignmentsAcrossIssues(t *testing.T) {
	tasks := []domain.Task{
		{ID: "az-1", Title: "One", Description: "desc", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, Implementations: []string{"go-bubbletea", "ts-opentui"}},
		{ID: "az-2", Title: "Two", Description: "desc", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug, Implementations: []string{"ts-opentui"}},
		{ID: "az-3", Title: "Three", Description: "desc", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeFeature, Implementations: []string{"go-bubbletea"}},
	}
	payload, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}

	type updateReq struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	updates := make([]updateReq, 0, 2)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            payload,
					}, nil
				case daemonclient.CommandTaskGet:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            payload,
					}, nil
				case daemonclient.CommandTaskUpdate:
					var body updateReq
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal update request: %v", err)
					}
					updates = append(updates, body)
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return ImplDeleteCommand(deps, ImplDeleteOptions{Implementation: "ts-opentui", Confirm: true})
	})
	if !strings.Contains(output, "Deleted implementation assignment: ts-opentui") {
		t.Fatalf("output missing delete summary: %q", output)
	}
	if !strings.Contains(output, "Updated issues: 2") {
		t.Fatalf("output missing update count: %q", output)
	}
	if len(updates) != 2 {
		t.Fatalf("update call count = %d, want 2", len(updates))
	}

	got := map[string][]string{}
	for _, update := range updates {
		got[update.TaskID] = update.Implementations
	}
	if !reflect.DeepEqual(got["az-1"], []string{"go-bubbletea"}) {
		t.Fatalf("az-1 implementations = %+v, want [go-bubbletea]", got["az-1"])
	}
	for _, update := range updates {
		if update.Description != "desc" {
			t.Fatalf("update %s description = %q, want preserved desc", update.TaskID, update.Description)
		}
	}
	if len(got["az-2"]) != 0 {
		t.Fatalf("az-2 implementations = %+v, want empty", got["az-2"])
	}
	if _, ok := got["az-3"]; ok {
		t.Fatalf("did not expect az-3 update, got map=%+v", got)
	}
}

func TestImplListCommandPrintsSortedUniqueImplementations(t *testing.T) {
	tasks := []domain.Task{
		{ID: "az-1", Implementations: []string{"go-bubbletea", "ts-opentui"}},
		{ID: "az-2", Implementations: []string{"default", "go-bubbletea"}},
		{ID: "az-3", Implementations: []string{" ", ""}},
	}
	payload, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return ImplListCommand(deps, ImplListOptions{})
	})
	if !strings.Contains(output, "Implementations: 3") {
		t.Fatalf("output missing implementation count: %q", output)
	}
	if !strings.Contains(output, "default\ngo-bubbletea\nts-opentui\n") {
		t.Fatalf("output missing sorted implementation rows: %q", output)
	}
}

func TestImplMigrateCommandMigratesAssignmentsAcrossIssues(t *testing.T) {
	tasks := []domain.Task{
		{ID: "az-1", Title: "One", Description: "desc", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, Implementations: []string{"default", "ts-opentui"}},
		{ID: "az-2", Title: "Two", Description: "desc", Status: domain.StatusOpen, Priority: domain.P1, Type: domain.TypeBug, Implementations: []string{"ts-opentui", "go-bubbletea", "default"}},
		{ID: "az-3", Title: "Three", Description: "desc", Status: domain.StatusOpen, Priority: domain.P3, Type: domain.TypeFeature, Implementations: []string{"go-bubbletea"}},
	}
	payload, err := marshalTaskListBody(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}

	type updateReq struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	updates := make([]updateReq, 0, 2)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            payload,
					}, nil
				case daemonclient.CommandTaskGet:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            payload,
					}, nil
				case daemonclient.CommandTaskUpdate:
					var body updateReq
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal update request: %v", err)
					}
					updates = append(updates, body)
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return ImplMigrateCommand(deps, ImplMigrateOptions{FromImplementation: "ts-opentui", ToImplementation: "default"})
	})
	if !strings.Contains(output, "Migrated implementation assignment: ts-opentui -> default") {
		t.Fatalf("output missing migrate summary: %q", output)
	}
	if !strings.Contains(output, "Updated issues: 2") {
		t.Fatalf("output missing update count: %q", output)
	}
	if len(updates) != 2 {
		t.Fatalf("update call count = %d, want 2", len(updates))
	}

	got := map[string][]string{}
	for _, update := range updates {
		got[update.TaskID] = update.Implementations
		if update.Description != "desc" {
			t.Fatalf("update %s description = %q, want preserved desc", update.TaskID, update.Description)
		}
	}
	if !reflect.DeepEqual(got["az-1"], []string{"default"}) {
		t.Fatalf("az-1 implementations = %+v, want [default]", got["az-1"])
	}
	if !reflect.DeepEqual(got["az-2"], []string{"default", "go-bubbletea"}) {
		t.Fatalf("az-2 implementations = %+v, want [default go-bubbletea]", got["az-2"])
	}
	if _, ok := got["az-3"]; ok {
		t.Fatalf("did not expect az-3 update, got map=%+v", got)
	}
}

func TestParseIssueListArgs(t *testing.T) {
	createdAfter := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	updatedBefore := time.Date(2026, 3, 27, 23, 59, 59, 0, time.UTC)
	tests := []struct {
		name        string
		args        []string
		want        IssueListOptions
		errContains string
	}{
		{
			name: "defaults",
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit},
		},
		{
			name: "json output",
			args: []string{"--json"},
			want: IssueListOptions{JSON: true, Deps: false, Limit: defaultIssueListLimit},
		},
		{
			name: "deps projection",
			args: []string{"--deps"},
			want: IssueListOptions{JSON: false, Deps: true, Limit: defaultIssueListLimit},
		},
		{
			name: "archived include",
			args: []string{"--archived", "include"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, Archived: "include"},
		},
		{
			name: "limit override",
			args: []string{"--limit", "25"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: 25},
		},
		{
			name: "query and date filters",
			args: []string{"-q", "runtime cache", "--created-after", "2026-03-25T10:00:00Z", "--updated-before", "2026-03-27T23:59:59Z"},
			want: IssueListOptions{
				JSON:          false,
				Deps:          false,
				Limit:         defaultIssueListLimit,
				Query:         "runtime cache",
				CreatedAfter:  &createdAfter,
				UpdatedBefore: &updatedBefore,
			},
		},
		{
			name: "status filters",
			args: []string{"--status", "open", "--status", "in_review", "--statuses", "in_progress,open"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, States: []domain.IssueDisplayPhase{domain.IssueDisplayOpen, domain.IssueDisplayReview, domain.IssueDisplayActive}},
		},
		{
			name: "v2 status filter aliases",
			args: []string{"--status", "backlog", "--status", "cancelled", "--statuses", "done,review"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, States: []domain.IssueDisplayPhase{domain.IssueDisplayBacklog, domain.IssueDisplayCancelled, domain.IssueDisplayDone, domain.IssueDisplayReview}},
		},
		{
			name: "state aliases",
			args: []string{"--state", "open", "--states", "in_review"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, States: []domain.IssueDisplayPhase{domain.IssueDisplayOpen, domain.IssueDisplayReview}},
		},
		{
			name: "id filters",
			args: []string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4"},
			want: IssueListOptions{JSON: false, Deps: false, Limit: defaultIssueListLimit, IDs: []string{"az-1", "az-2", "az-3", "az-4"}},
		},
		{
			name: "parent and dependency filters",
			args: []string{"--parent", "az-parent-1", "--parents", "az-parent-2", "--depends-on", "az-upstream-1", "--depends-on-ids", "az-upstream-2"},
			want: IssueListOptions{
				JSON:         false,
				Deps:         false,
				Limit:        defaultIssueListLimit,
				ParentIDs:    []string{"az-parent-1", "az-parent-2"},
				DependsOnIDs: []string{"az-upstream-1", "az-upstream-2"},
			},
		},
		{
			name:        "invalid limit",
			args:        []string{"--limit", "0"},
			errContains: "limit must be >= 1",
		},
		{
			name:        "invalid date filter",
			args:        []string{"--created-after", "yesterday-ish"},
			errContains: "invalid --created-after",
		},
		{
			name:        "rejects extra args",
			args:        []string{"extra"},
			errContains: "unexpected argument: extra",
		},
		{
			name:        "invalid state",
			args:        []string{"--status", "queued"},
			errContains: "invalid status: queued",
		},
		{
			name:        "invalid archived mode",
			args:        []string{"--archived", "maybe"},
			errContains: "archived must be one of",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueListArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueListArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseIssueListArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueGetArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueGetOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"az-1"},
			want: IssueGetOptions{IssueID: "az-1", JSON: false},
		},
		{
			name: "json output",
			args: []string{"--json", "az-2"},
			want: IssueGetOptions{IssueID: "az-2", JSON: true},
		},
		{
			name: "include notes",
			args: []string{"--with-notes", "az-2"},
			want: IssueGetOptions{IssueID: "az-2", IncludeNotes: true},
		},
		{
			name:        "missing issue id",
			args:        []string{},
			errContains: "usage: az ticket get [--project <project-id>] [--id <ticket-id>] [--json] [--with-notes] [--archived exclude|include|only] [<ticket-id>]",
		},
		{
			name:        "too many args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az ticket get [--project <project-id>] [--id <ticket-id>] [--json] [--with-notes] [<ticket-id>]",
		},
		{
			name:        "deps flag rejected",
			args:        []string{"--deps", "az-3"},
			errContains: "flag provided but not defined: -deps",
		},
		{
			name: "named id",
			args: []string{"--id", "az-4"},
			want: IssueGetOptions{IssueID: "az-4", JSON: false},
		},
		{
			name: "archived only",
			args: []string{"--archived", "only", "az-4"},
			want: IssueGetOptions{IssueID: "az-4", Archived: "only"},
		},
		{
			name:        "invalid archived mode",
			args:        []string{"--archived", "maybe", "az-4"},
			errContains: "archived must be one of",
		},
		{
			name:        "single-dash long flag rejected",
			args:        []string{"-id", "az-4"},
			errContains: "invalid flag \"-id\": use --id",
		},
		{
			name: "interspersed json flag after positional id",
			args: []string{"az-4", "--json"},
			want: IssueGetOptions{IssueID: "az-4", JSON: true},
		},
		{
			name:        "single-dash long interspersed flag rejected",
			args:        []string{"az-4", "-json"},
			errContains: "invalid flag \"-json\": use --json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueGetArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueGetArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueGetArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueEventsArgs(t *testing.T) {
	got, err := ParseIssueEventsArgs([]string{
		"az-1",
		"--json",
		"--type", "issue.status_changed",
		"--types", "validation.passed,review.completed",
		"--limit", "25",
	})
	if err != nil {
		t.Fatalf("ParseIssueEventsArgs() error = %v", err)
	}
	if got.IssueID != "az-1" || !got.JSON || got.Limit != 25 {
		t.Fatalf("ParseIssueEventsArgs() = %+v", got)
	}
	if !reflect.DeepEqual(got.EventTypes, []string{"issue.status_changed", "validation.passed", "review.completed"}) {
		t.Fatalf("event types = %+v", got.EventTypes)
	}

	got, err = ParseIssueEventsArgs([]string{"az-1", "--tail", "20", "--before-id", "500", "--source", "daemon", "--source-command", "review", "--operation", "op-1", "--session", "s-1", "--worktree", "/tmp/wt", "--since", "2026-07-01", "--until", "2026-07-15", "--query", "projection checkpoint", "--payload", "outcome=accepted", "--payload", "revision=2"})
	if err != nil {
		t.Fatalf("ParseIssueEventsArgs(full query) error = %v", err)
	}
	if got.Order != "desc" || got.Limit != 20 || got.BeforeID != 500 || got.Source != "daemon" || got.Query != "projection checkpoint" || len(got.PayloadEquals) != 2 {
		t.Fatalf("ParseIssueEventsArgs(full query) = %+v", got)
	}
	if got.PayloadEquals[0].Value != "accepted" || got.PayloadEquals[1].Value != float64(2) {
		t.Fatalf("payload filters = %+v", got.PayloadEquals)
	}

	got, err = ParseIssueEventsArgs([]string{"--id", "az-2", "--type", "issue.created"})
	if err != nil {
		t.Fatalf("ParseIssueEventsArgs(named id) error = %v", err)
	}
	if got.IssueID != "az-2" || !reflect.DeepEqual(got.EventTypes, []string{"issue.created"}) {
		t.Fatalf("ParseIssueEventsArgs(named id) = %+v", got)
	}

	_, err = ParseIssueEventsArgs([]string{"--json"})
	if err == nil || !strings.Contains(err.Error(), "usage: az ticket events") {
		t.Fatalf("expected missing id usage error, got %v", err)
	}
	_, err = ParseIssueEventsArgs([]string{"az-1", "--limit", "-1"})
	if err == nil || !strings.Contains(err.Error(), "--limit must be non-negative") {
		t.Fatalf("expected negative limit error, got %v", err)
	}
	_, err = ParseIssueEventsArgs([]string{"az-1", "--tail", "10", "--limit", "2"})
	if err == nil || !strings.Contains(err.Error(), "--tail cannot") {
		t.Fatalf("expected tail conflict error, got %v", err)
	}

	got, err = ParseIssueEventsArgs([]string{"--jq-help"})
	if err != nil {
		t.Fatalf("ParseIssueEventsArgs(jq help) error = %v", err)
	}
	if !got.JQHelp || got.IssueID != "" {
		t.Fatalf("ParseIssueEventsArgs(jq help) = %+v", got)
	}
}

func TestParseIssueRecordArgsFollowUpsDefaultToFollowUpEvent(t *testing.T) {
	got, err := ParseIssueRecordArgs([]string{
		"az-parent",
		"--summary", "Created follow-up issues",
		"--follow-up", "az-child-1",
		"--follow-up", "az-child-2",
		"--json",
	})
	if err != nil {
		t.Fatalf("ParseIssueRecordArgs() error = %v", err)
	}
	if got.IssueID != "az-parent" || got.EventType != string(domain.IssueEventFollowupCreated) || !got.JSON {
		t.Fatalf("ParseIssueRecordArgs() = %+v", got)
	}
	if !reflect.DeepEqual(got.FollowUpIssueIDs, []string{"az-child-1", "az-child-2"}) {
		t.Fatalf("follow-up ids = %+v", got.FollowUpIssueIDs)
	}

	got, err = ParseIssueRecordArgs([]string{
		"--id", "az-2",
		"--type", "validation.passed",
		"--summary", "just test passed",
		"--data", `{"commands":["just test"]}`,
	})
	if err != nil {
		t.Fatalf("ParseIssueRecordArgs(explicit type) error = %v", err)
	}
	if got.IssueID != "az-2" || got.EventType != string(domain.IssueEventValidationPassed) {
		t.Fatalf("ParseIssueRecordArgs(explicit type) = %+v", got)
	}

	_, err = ParseIssueRecordArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "at least one of --summary") {
		t.Fatalf("expected missing payload error, got %v", err)
	}
}

func TestParseIssueContextRiskArgsSummaryAndFull(t *testing.T) {
	got, err := ParseIssueContextRiskArgs([]string{"az-1", "--json", "--summary", "--since", "2w"})
	if err != nil {
		t.Fatalf("ParseIssueContextRiskArgs() error = %v", err)
	}
	if got.IssueID != "az-1" || !got.JSON || !got.Summary || got.Full || got.Since.IsZero() {
		t.Fatalf("ParseIssueContextRiskArgs() = %+v, want summary JSON for az-1", got)
	}

	got, err = ParseIssueContextRiskArgs([]string{"--id", "az-2", "--full"})
	if err != nil {
		t.Fatalf("ParseIssueContextRiskArgs(full) error = %v", err)
	}
	if got.IssueID != "az-2" || !got.Full || got.Summary {
		t.Fatalf("ParseIssueContextRiskArgs(full) = %+v", got)
	}

	_, err = ParseIssueContextRiskArgs([]string{"az-1", "--summary", "--full"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected summary/full conflict, got %v", err)
	}

}

func TestParseIssueCheckAndDoctorArgs(t *testing.T) {
	check, err := ParseIssueCheckArgs([]string{"az-1"})
	if err != nil {
		t.Fatalf("ParseIssueCheckArgs() error = %v", err)
	}
	if check.IssueID != "az-1" || check.JSON {
		t.Fatalf("ParseIssueCheckArgs() = %+v", check)
	}
	_, err = ParseIssueCheckArgs([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage: az ticket check [--project <project-id>] [--id <ticket-id>] [--json] [<ticket-id>]") {
		t.Fatalf("expected check usage error, got %v", err)
	}

	doctor, err := ParseIssueDoctorArgs([]string{"az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() error = %v", err)
	}
	if doctor.IssueID != "az-2" {
		t.Fatalf("ParseIssueDoctorArgs() = %+v", doctor)
	}
	doctor, err = ParseIssueDoctorArgs([]string{"--id", "az-3"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() named id error = %v", err)
	}
	if doctor.IssueID != "az-3" {
		t.Fatalf("ParseIssueDoctorArgs() named id = %+v", doctor)
	}
	check, err = ParseIssueCheckArgs([]string{"az-1", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueCheckArgs() interspersed json error = %v", err)
	}
	if check.IssueID != "az-1" || !check.JSON {
		t.Fatalf("ParseIssueCheckArgs() interspersed json = %+v", check)
	}
	doctor, err = ParseIssueDoctorArgs([]string{"az-2", "--id", "az-3"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() interspersed named id error = %v", err)
	}
	if doctor.IssueID != "az-3" {
		t.Fatalf("ParseIssueDoctorArgs() interspersed named id = %+v", doctor)
	}
	doctor, err = ParseIssueDoctorArgs([]string{"--checkpoint-wal", "--id", "az-3"})
	if err != nil {
		t.Fatalf("ParseIssueDoctorArgs() wal flags error = %v", err)
	}
	if doctor.IssueID != "az-3" || !doctor.CheckpointWAL || doctor.TruncateWAL {
		t.Fatalf("ParseIssueDoctorArgs() wal flags = %+v", doctor)
	}
	_, err = ParseIssueDoctorArgs([]string{"--checkpoint-wal", "--truncate-wal", "--id", "az-3"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually exclusive wal flag error, got %v", err)
	}
	_, err = ParseIssueDoctorArgs([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage: az ticket doctor [--project <project-id>] [--id <ticket-id>] [--checkpoint-wal] [--truncate-wal] [--json] [<ticket-id>]") {
		t.Fatalf("expected doctor usage error, got %v", err)
	}
}

func TestParseIssueSearchArgs(t *testing.T) {
	updatedAfter := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		args        []string
		want        IssueListOptions
		errContains string
	}{
		{
			name: "positional query",
			args: []string{"--status", "open", "--updated-after", "2026-03-25T10:00:00Z", "runtime", "cache"},
			want: IssueListOptions{
				Limit:        defaultIssueListLimit,
				Query:        "runtime cache",
				States:       []domain.IssueDisplayPhase{domain.IssueDisplayOpen},
				UpdatedAfter: &updatedAfter,
			},
		},
		{
			name: "query flag",
			args: []string{"--query", "linear error"},
			want: IssueListOptions{Limit: defaultIssueListLimit, Query: "linear error"},
		},
		{
			name: "archived include",
			args: []string{"--archived", "include", "--query", "linear error"},
			want: IssueListOptions{Limit: defaultIssueListLimit, Query: "linear error", Archived: "include"},
		},
		{
			name: "short query flag",
			args: []string{"-q", "linear error"},
			want: IssueListOptions{Limit: defaultIssueListLimit, Query: "linear error"},
		},
		{
			name:        "missing query",
			errContains: "usage: az ticket search",
		},
		{
			name:        "duplicate query sources",
			args:        []string{"--query", "linear", "runtime"},
			errContains: "provide query either as --query or as positional text, not both",
		},
		{
			name:        "rejects flags after positional query",
			args:        []string{"runtime", "--status", "open"},
			errContains: "flags/options must appear before positional query text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueSearchArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueSearchArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseIssueSearchArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueGetManyArgs(t *testing.T) {
	got, err := ParseIssueGetManyArgs([]string{"--id", "az-1", "--id", "az-2", "--ids", "az-3,az-4", "--json", "--with-notes"})
	if err != nil {
		t.Fatalf("ParseIssueGetManyArgs() error = %v", err)
	}
	if !got.JSON {
		t.Fatalf("expected json output flag to be set")
	}
	if !got.IncludeNotes {
		t.Fatalf("expected with-notes flag to be set")
	}
	if !reflect.DeepEqual(got.IssueIDs, []string{"az-1", "az-2", "az-3", "az-4"}) {
		t.Fatalf("ParseIssueGetManyArgs() ids = %+v", got.IssueIDs)
	}

	_, err = ParseIssueGetManyArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "unexpected argument: az-1") {
		t.Fatalf("expected positional arg rejection, got %v", err)
	}

	_, err = ParseIssueGetManyArgs([]string{"--json"})
	if err == nil || !strings.Contains(err.Error(), "usage: az ticket get-many [--project <project-id>] --id <ticket-id>") {
		t.Fatalf("expected usage error for missing ids, got %v", err)
	}
}

func TestParseIssueCreateArgs(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-parent")
	tests := []struct {
		name        string
		args        []string
		want        IssueCreateOptions
		errContains string
	}{
		{
			name: "defaults",
			args: []string{"Title"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P2,
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "explicit options",
			args: []string{"--impl", "go-bubbletea", "--type", "bug", "--priority", "P0", "--description", "details", "Title"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Description:            "details",
				Type:                   domain.TypeBug,
				Priority:               domain.P0,
				PriorityExplicit:       true,
				Implementations:        []string{"go-bubbletea"},
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "title flag",
			args: []string{"--title", "Title", "--impl", "go-bubbletea"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P2,
				Implementations:        []string{"go-bubbletea"},
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "explicit parent",
			args: []string{"--parent", "az-explicit", "Title"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P2,
				AutoParentFromIssueID:  ptrToString("az-explicit"),
				AutoCreatedFromIssueID: ptrToString("az-explicit"),
				ExplicitParent:         true,
			},
		},
		{
			name: "interspersed flags after title",
			args: []string{"Title", "--impl", "go-bubbletea", "--priority", "P1"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P1,
				PriorityExplicit:       true,
				Implementations:        []string{"go-bubbletea"},
				AutoParentFromIssueID:  ptrToString("az-parent"),
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "deferred defaults priority",
			args: []string{"--deferred", "Title"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P4,
				Lifecycle:              domain.IssueWorkflowBacklog,
				Deferred:               true,
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name: "deferred explicit priority keeps backlog lifecycle",
			args: []string{"--deferred", "--priority", "P0", "Title"},
			want: IssueCreateOptions{
				Title:                  "Title",
				Type:                   domain.TypeTask,
				Priority:               domain.P0,
				PriorityExplicit:       true,
				Lifecycle:              domain.IssueWorkflowBacklog,
				Deferred:               true,
				AutoCreatedFromIssueID: ptrToString("az-parent"),
			},
		},
		{
			name:        "invalid priority",
			args:        []string{"--priority", "high", "Title"},
			errContains: "invalid priority: high",
		},
		{
			name:        "missing title",
			args:        []string{},
			errContains: "usage: az ticket create [--project <project-id>] [--parent <ticket-id>] [--impl <implementation> ...] [--deferred]",
		},
		{
			name:        "title flag and positional are ambiguous",
			args:        []string{"--title", "Flag title", "Positional title"},
			errContains: "provide title either as --title or as a positional argument, not both",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueCreateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueCreateArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseIssueCreateArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueSplitArgsDefaultsParentFromEnv(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-parent")
	opts, err := ParseIssueSplitArgs([]string{"--description", "do this elsewhere", "--priority", "P1", "Child work"})
	if err != nil {
		t.Fatalf("ParseIssueSplitArgs error = %v", err)
	}
	if opts.ParentIssueID != "az-parent" || opts.Title != "Child work" || opts.Description != "do this elsewhere" {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.Priority != domain.P1 || !opts.PriorityExplicit {
		t.Fatalf("priority = %s explicit=%v, want P1 explicit", opts.Priority, opts.PriorityExplicit)
	}
}

func TestParseIssueSplitArgsRequiresParent(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	_, err := ParseIssueSplitArgs([]string{"Child work"})
	if err == nil || !strings.Contains(err.Error(), "missing parent issue") {
		t.Fatalf("error = %v, want missing parent issue", err)
	}
}

func TestParseIssueCloseArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueCloseOptions
		errContains string
	}{
		{
			name: "valid",
			args: []string{"az-1"},
			want: IssueCloseOptions{IssueID: "az-1"},
		},
		{
			name:        "forbid impl",
			args:        []string{"--impl", "go-bubbletea", "az-1"},
			errContains: "--impl is not supported for issue close",
		},
		{
			name:        "missing id",
			args:        []string{},
			errContains: "usage: az ticket close [--project <project-id>] [--id <ticket-id>|-i <ticket-id>] [--json] [--force-worktree] [--close-clean-children] [<ticket-id>]",
		},
		{
			name:        "extra args",
			args:        []string{"az-1", "extra"},
			errContains: "usage: az ticket close [--project <project-id>] [--id <ticket-id>|-i <ticket-id>] [--json] [--force-worktree] [--close-clean-children] [<ticket-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
		},
		{
			name: "short id",
			args: []string{"-i", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
		},
		{
			name:        "cleanup flag removed",
			args:        []string{"--id", "az-2", "--cleanup"},
			errContains: "flag provided but not defined: -cleanup",
		},
		{
			name: "force worktree",
			args: []string{"--id", "az-2", "--force-worktree"},
			want: IssueCloseOptions{IssueID: "az-2", ForceWorktree: true},
		},
		{
			name: "close clean children",
			args: []string{"--id", "az-2", "--close-clean-children"},
			want: IssueCloseOptions{IssueID: "az-2", CloseCleanChildren: true},
		},
		{
			name:        "allow base for child unsupported on close",
			args:        []string{"--id", "az-2", "--allow-base-for-child"},
			errContains: "flag provided but not defined: -allow-base-for-child",
		},
		{
			name: "interspersed named id overrides positional",
			args: []string{"az-1", "--id", "az-2"},
			want: IssueCloseOptions{IssueID: "az-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueCloseArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueCloseArgs() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseIssueCloseArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseIssueCleanupArgs(t *testing.T) {
	opts, err := ParseIssueCleanupArgs([]string{"--id", "az-1", "--statuses", "open,in_review", "--action", "cancelled", "--dry-run", "--per-ticket-timeout", "2s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.IDs) != 1 || len(opts.Statuses) != 2 || opts.Action != "cancelled" || !opts.DryRun || opts.PerIssueTimeout != 2*time.Second {
		t.Fatalf("opts = %+v", opts)
	}
	legacy, err := ParseIssueCleanupArgs([]string{"--id", "az-1", "--per-issue-timeout", "2s"})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.PerIssueTimeout != opts.PerIssueTimeout {
		t.Fatalf("legacy timeout = %s, canonical timeout = %s", legacy.PerIssueTimeout, opts.PerIssueTimeout)
	}
	if _, err := ParseIssueCleanupArgs(nil); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("missing selector error = %v", err)
	}
	if _, err := ParseIssueCleanupArgs([]string{"--id", "az-1", "--action", "deleted"}); err == nil {
		t.Fatal("invalid action accepted")
	}
	if _, err := ParseIssueCleanupArgs([]string{"--query", "--"}); err == nil || !strings.Contains(err.Error(), "searchable term") {
		t.Fatalf("punctuation-only query error = %v", err)
	}
}

func TestIssueCleanupBatchParentDoesNotConsumePerIssueBudget(t *testing.T) {
	ctx, cancel := newIssueCleanupBatchContext()
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("batch parent has deadline; later items can lose their per-issue budget")
	}
}

func TestParseIssueDeleteArgs(t *testing.T) {
	got, err := ParseIssueDeleteArgs([]string{"--confirm", "az-1"})
	if err != nil {
		t.Fatalf("ParseIssueDeleteArgs() error = %v", err)
	}
	if got.IssueID != "az-1" || !got.Confirm {
		t.Fatalf("ParseIssueDeleteArgs() = %+v", got)
	}
	_, err = ParseIssueDeleteArgs([]string{"az-1"})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --confirm") {
		t.Fatalf("expected missing confirm error, got %v", err)
	}
	_, err = ParseIssueDeleteArgs([]string{"--impl", "go-bubbletea", "--confirm", "az-1"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue delete") {
		t.Fatalf("expected impl forbidden error, got %v", err)
	}
	got, err = ParseIssueDeleteArgs([]string{"az-1", "--confirm"})
	if err != nil {
		t.Fatalf("ParseIssueDeleteArgs() interspersed confirm error = %v", err)
	}
	if got.IssueID != "az-1" || !got.Confirm {
		t.Fatalf("ParseIssueDeleteArgs() interspersed confirm = %+v", got)
	}
	got, err = ParseIssueDeleteArgs([]string{"az-1", "--confirm", "--cleanup", "--force-worktree"})
	if err != nil {
		t.Fatalf("ParseIssueDeleteArgs() cleanup error = %v", err)
	}
	if got.IssueID != "az-1" || !got.StopSession || !got.RemoveWorktree || !got.ForceWorktree {
		t.Fatalf("ParseIssueDeleteArgs() cleanup = %+v", got)
	}
	_, err = ParseIssueDeleteArgs([]string{"az-1", "--confirm", "--force-worktree"})
	if err == nil || !strings.Contains(err.Error(), "--force-worktree requires --remove-worktree or --cleanup") {
		t.Fatalf("expected force-worktree dependency error, got %v", err)
	}
}

func TestParseIssueUnarchiveArgs(t *testing.T) {
	got, err := ParseIssueUnarchiveArgs([]string{"az-1", "--json", "--with-parents", "--cascade-children"})
	if err != nil {
		t.Fatalf("ParseIssueUnarchiveArgs() error = %v", err)
	}
	if got.IssueID != "az-1" || !got.JSON || !got.WithParents || !got.CascadeChildren {
		t.Fatalf("ParseIssueUnarchiveArgs() = %+v", got)
	}

	got, err = ParseIssueUnarchiveArgs([]string{"--id", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueUnarchiveArgs(named id) error = %v", err)
	}
	if got.IssueID != "az-2" {
		t.Fatalf("ParseIssueUnarchiveArgs(named id) = %+v", got)
	}

	_, err = ParseIssueUnarchiveArgs([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage: az ticket unarchive") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestParseIssueUpdateArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		want        IssueUpdateOptions
		errContains string
	}{
		{
			name: "update title",
			args: []string{"--title", "Renamed", "az-1"},
			want: IssueUpdateOptions{
				IssueID: "az-1",
				Title:   "Renamed",
			},
		},
		{
			name: "update description",
			args: []string{"--description", "New details", "az-1"},
			want: IssueUpdateOptions{
				IssueID:        "az-1",
				Description:    "New details",
				DescriptionSet: true,
			},
		},
		{
			name: "clear description",
			args: []string{"--description", "", "az-1"},
			want: IssueUpdateOptions{
				IssueID:        "az-1",
				DescriptionSet: true,
			},
		},
		{
			name: "update type and priority",
			args: []string{"--type", "epic", "--priority", "P0", "az-1"},
			want: func() IssueUpdateOptions {
				tt := domain.TypeEpic
				p := domain.P0
				return IssueUpdateOptions{
					IssueID:  "az-1",
					Type:     &tt,
					Priority: &p,
				}
			}(),
		},
		{
			name: "update investigation type",
			args: []string{"--type", "investigation", "az-1"},
			want: func() IssueUpdateOptions {
				tt := domain.TypeInvestigation
				return IssueUpdateOptions{IssueID: "az-1", Type: &tt}
			}(),
		},
		{
			name: "append notes",
			args: []string{"--append-notes", "Follow-up", "az-1"},
			want: IssueUpdateOptions{
				IssueID:     "az-1",
				AppendNotes: "Follow-up",
			},
		},
		{
			name: "replace notes",
			args: []string{"--notes", "Replacement", "az-1"},
			want: func() IssueUpdateOptions {
				notes := "Replacement"
				return IssueUpdateOptions{
					IssueID: "az-1",
					Notes:   &notes,
				}
			}(),
		},
		{
			name: "clear notes",
			args: []string{"--notes", "", "az-1"},
			want: func() IssueUpdateOptions {
				notes := ""
				return IssueUpdateOptions{
					IssueID: "az-1",
					Notes:   &notes,
				}
			}(),
		},
		{
			name:        "forbid impl",
			args:        []string{"--impl", "go-bubbletea", "--title", "Renamed", "az-1"},
			errContains: "--impl is not supported for issue update",
		},
		{
			name:        "no update fields",
			args:        []string{"az-1"},
			errContains: "no update fields provided",
		},
		{
			name:        "invalid status arg count",
			args:        []string{},
			errContains: "usage: az ticket update [--project <project-id>] [--id <ticket-id>]",
		},
		{
			name: "named id",
			args: []string{"--id", "az-9", "--title", "Renamed"},
			want: IssueUpdateOptions{
				IssueID: "az-9",
				Title:   "Renamed",
			},
		},
		{
			name: "interspersed positional then status flag",
			args: []string{"az-1", "--status", "in_review"},
			want: func() IssueUpdateOptions {
				status := domain.StatusInReview
				return IssueUpdateOptions{
					IssueID: "az-1",
					Status:  &status,
				}
			}(),
		},
		{
			name: "backlog status is lifecycle mutation",
			args: []string{"az-1", "--status", "backlog"},
			want: func() IssueUpdateOptions {
				lifecycle := domain.IssueWorkflowBacklog
				return IssueUpdateOptions{
					IssueID:   "az-1",
					Lifecycle: &lifecycle,
				}
			}(),
		},
		{
			name: "open status is lifecycle mutation",
			args: []string{"az-1", "--status", "open"},
			want: func() IssueUpdateOptions {
				lifecycle := domain.IssueWorkflowOpen
				return IssueUpdateOptions{
					IssueID:   "az-1",
					Lifecycle: &lifecycle,
				}
			}(),
		},
		{
			name: "cascade children on in_review status",
			args: []string{"az-1", "--status", "in_review", "--cascade-children"},
			want: func() IssueUpdateOptions {
				status := domain.StatusInReview
				return IssueUpdateOptions{
					IssueID:         "az-1",
					Status:          &status,
					CascadeChildren: true,
				}
			}(),
		},
		{
			name:        "cascade children requires in_review",
			args:        []string{"az-1", "--status", "open", "--cascade-children"},
			errContains: "--cascade-children is only supported with --status in_review",
		},
		{
			name: "force worktree on closed status",
			args: []string{"az-1", "--status", "closed", "--force-worktree"},
			want: func() IssueUpdateOptions {
				status := domain.StatusDone
				return IssueUpdateOptions{
					IssueID:       "az-1",
					Status:        &status,
					ForceWorktree: true,
				}
			}(),
		},
		{
			name: "cancelled status",
			args: []string{"az-1", "--status", "cancelled", "--force-worktree"},
			want: func() IssueUpdateOptions {
				status := domain.StatusCancelled
				return IssueUpdateOptions{
					IssueID:       "az-1",
					Status:        &status,
					ForceWorktree: true,
				}
			}(),
		},
		{
			name:        "cleanup flag removed",
			args:        []string{"az-1", "--status", "closed", "--cleanup"},
			errContains: "flag provided but not defined: -cleanup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIssueUpdateArgs(tt.args)
			if tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error = %v, want substring %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIssueUpdateArgs() error = %v", err)
			}
			if got.IssueID != tt.want.IssueID || got.Title != tt.want.Title || got.Description != tt.want.Description || got.DescriptionSet != tt.want.DescriptionSet || got.AppendNotes != tt.want.AppendNotes {
				t.Fatalf("ParseIssueUpdateArgs() = %+v, want %+v", got, tt.want)
			}
			if got.ForceWorktree != tt.want.ForceWorktree || got.CascadeChildren != tt.want.CascadeChildren {
				t.Fatalf("flag mismatch: got force=%v cascade=%v want force=%v cascade=%v", got.ForceWorktree, got.CascadeChildren, tt.want.ForceWorktree, tt.want.CascadeChildren)
			}
			if (got.Status == nil) != (tt.want.Status == nil) {
				t.Fatalf("status presence mismatch: got=%v want=%v", got.Status, tt.want.Status)
			}
			if got.Status != nil && *got.Status != *tt.want.Status {
				t.Fatalf("status mismatch: got=%v want=%v", *got.Status, *tt.want.Status)
			}
			if (got.Type == nil) != (tt.want.Type == nil) {
				t.Fatalf("type presence mismatch: got=%v want=%v", got.Type, tt.want.Type)
			}
			if got.Type != nil && *got.Type != *tt.want.Type {
				t.Fatalf("type mismatch: got=%v want=%v", *got.Type, *tt.want.Type)
			}
			if (got.Priority == nil) != (tt.want.Priority == nil) {
				t.Fatalf("priority presence mismatch: got=%v want=%v", got.Priority, tt.want.Priority)
			}
			if got.Priority != nil && *got.Priority != *tt.want.Priority {
				t.Fatalf("priority mismatch: got=%v want=%v", *got.Priority, *tt.want.Priority)
			}
		})
	}
}

func TestParseIssueDependencyArgs(t *testing.T) {
	add, err := ParseIssueDependencyAddArgs([]string{"--type", "related", "az-1", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" || add.Type != "related" {
		t.Fatalf("ParseIssueDependencyAddArgs() = %+v", add)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"--type", "created-in", "az-1", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs(created-in) error = %v", err)
	}
	if add.Type != "created-in" {
		t.Fatalf("created-in dependency type = %q", add.Type)
	}
	_, err = ParseIssueDependencyAddArgs([]string{"--impl", "go-bubbletea", "az-1", "az-2"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep add") {
		t.Fatalf("expected impl forbidden error for add, got %v", err)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"--issue-id", "az-1", "--depends-on-id", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() named flags error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" {
		t.Fatalf("ParseIssueDependencyAddArgs() named flags = %+v", add)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"az-1", "--depends-on-id", "az-2"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() interspersed id+flag error = %v", err)
	}
	if add.IssueID != "az-1" || add.DependsOnID != "az-2" {
		t.Fatalf("ParseIssueDependencyAddArgs() interspersed id+flag = %+v", add)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"az-1", "az-2", "--type", "parent-child", "--force-parent-change"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() force parent change error = %v", err)
	}
	if !add.ForceParentChange {
		t.Fatalf("ParseIssueDependencyAddArgs() force parent change not set: %+v", add)
	}
	add, err = ParseIssueDependencyAddArgs([]string{"chefy:az-1", "chefy:az-2", "--type", "blocks"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() project-qualified refs error = %v", err)
	}
	if add.Project != "chefy" || add.IssueID != "az-1" || add.DependsOnID != "az-2" || add.IssueProjectID != "chefy" || add.DependsOnProjectID != "chefy" {
		t.Fatalf("ParseIssueDependencyAddArgs() project-qualified refs = %+v", add)
	}
	_, err = ParseIssueDependencyAddArgs([]string{"chefy:az-1", "azedarach:az-2"})
	if err == nil || !strings.Contains(err.Error(), "dependency endpoints must be in the same project") {
		t.Fatalf("expected cross-project dependency rejection, got %v", err)
	}
	_, err = ParseIssueDependencyAddArgs([]string{"--project", "azedarach", "chefy:az-1", "chefy:az-2"})
	if err == nil || !strings.Contains(err.Error(), "does not match --project") {
		t.Fatalf("expected endpoint/project mismatch rejection, got %v", err)
	}

	remove, err := ParseIssueDependencyRemoveArgs([]string{"--type", "blocks", "--confirm", "--confirm-parent-orphan", "az-3", "az-4"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" || remove.Type != "blocks" || !remove.Confirm || !remove.ConfirmParentOrphan {
		t.Fatalf("ParseIssueDependencyRemoveArgs() = %+v", remove)
	}
	_, err = ParseIssueDependencyRemoveArgs([]string{"--impl", "go-bubbletea", "az-3"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep remove") {
		t.Fatalf("expected impl forbidden error for remove, got %v", err)
	}
	remove, err = ParseIssueDependencyRemoveArgs([]string{"--issue-id", "az-3", "--depends-on-id", "az-4"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() named flags error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" {
		t.Fatalf("ParseIssueDependencyRemoveArgs() named flags = %+v", remove)
	}
	remove, err = ParseIssueDependencyRemoveArgs([]string{"az-3", "--depends-on-id", "az-4", "--confirm"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyRemoveArgs() interspersed id+flags error = %v", err)
	}
	if remove.IssueID != "az-3" || remove.DependsOnID != "az-4" || !remove.Confirm {
		t.Fatalf("ParseIssueDependencyRemoveArgs() interspersed id+flags = %+v", remove)
	}
}

func TestTicketNamedFlagsAndLegacyAliasesMatch(t *testing.T) {
	assertIssueID := func(t *testing.T, want string, parse func([]string) (string, error), canonical, legacy []string) {
		t.Helper()
		for name, args := range map[string][]string{"canonical": canonical, "legacy": legacy} {
			got, err := parse(args)
			if err != nil {
				t.Fatalf("%s args %v: %v", name, args, err)
			}
			if got != want {
				t.Fatalf("%s args resolved %q, want %q", name, got, want)
			}
		}
	}
	assertIssueID(t, "az-1", func(args []string) (string, error) {
		opts, err := ParseIssueDependencyAddArgs(args)
		return opts.IssueID, err
	}, []string{"--ticket-id", "az-1", "--depends-on-id", "az-2"}, []string{"--issue-id", "az-1", "--depends-on-id", "az-2"})
	assertIssueID(t, "az-1", func(args []string) (string, error) {
		opts, err := ParseIssueImageAddArgs(args)
		return opts.IssueID, err
	}, []string{"--ticket-id", "az-1", "--path", "image.png"}, []string{"--issue-id", "az-1", "--path", "image.png"})
	assertIssueID(t, "az-1", func(args []string) (string, error) {
		opts, err := ParseIssueDocumentListArgs(args)
		return opts.IssueID, err
	}, []string{"--ticket-id", "az-1"}, []string{"--issue-id", "az-1"})
}

func TestParseIssueImageArgs(t *testing.T) {
	add, err := ParseIssueImageAddArgs([]string{"--issue-id", "az-1", "--path", "image.png"})
	if err != nil {
		t.Fatalf("ParseIssueImageAddArgs() error = %v", err)
	}
	if add.IssueID != "az-1" || add.SourcePath != "image.png" {
		t.Fatalf("ParseIssueImageAddArgs() = %+v", add)
	}
	add, err = ParseIssueImageAddArgs([]string{"az-2", "--path", "snap.png"})
	if err != nil {
		t.Fatalf("ParseIssueImageAddArgs() interspersed args error = %v", err)
	}
	if add.IssueID != "az-2" || add.SourcePath != "snap.png" {
		t.Fatalf("ParseIssueImageAddArgs() interspersed args = %+v", add)
	}
	_, err = ParseIssueImageAddArgs([]string{"--impl", "go-bubbletea", "az-1", "image.png"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue image add") {
		t.Fatalf("expected impl forbidden error for image add, got %v", err)
	}

	remove, err := ParseIssueImageRemoveArgs([]string{"--issue-id", "az-1", "--attachment-id", "abc123"})
	if err != nil {
		t.Fatalf("ParseIssueImageRemoveArgs() error = %v", err)
	}
	if remove.IssueID != "az-1" || remove.AttachmentID != "abc123" {
		t.Fatalf("ParseIssueImageRemoveArgs() = %+v", remove)
	}
	remove, err = ParseIssueImageRemoveArgs([]string{"az-2", "--attachment-id", "def456"})
	if err != nil {
		t.Fatalf("ParseIssueImageRemoveArgs() interspersed args error = %v", err)
	}
	if remove.IssueID != "az-2" || remove.AttachmentID != "def456" {
		t.Fatalf("ParseIssueImageRemoveArgs() interspersed args = %+v", remove)
	}
	_, err = ParseIssueImageRemoveArgs([]string{"--impl", "go-bubbletea", "az-1", "abc123"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue image remove") {
		t.Fatalf("expected impl forbidden error for image remove, got %v", err)
	}
}

func TestParseIssueDocumentArgs(t *testing.T) {
	add, err := ParseIssueDocumentAddArgs([]string{"--issue-id", "az-1", "--path", "report.md"})
	if err != nil {
		t.Fatalf("ParseIssueDocumentAddArgs() error = %v", err)
	}
	if add.IssueID != "az-1" || add.SourcePath != "report.md" {
		t.Fatalf("ParseIssueDocumentAddArgs() = %+v", add)
	}
	add, err = ParseIssueDocumentAddArgs([]string{"az-2", "--path", "notes.md"})
	if err != nil {
		t.Fatalf("ParseIssueDocumentAddArgs() interspersed args error = %v", err)
	}
	if add.IssueID != "az-2" || add.SourcePath != "notes.md" {
		t.Fatalf("ParseIssueDocumentAddArgs() interspersed args = %+v", add)
	}
	_, err = ParseIssueDocumentAddArgs([]string{"--impl", "go-bubbletea", "az-1", "report.md"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue document add") {
		t.Fatalf("expected impl forbidden error for document add, got %v", err)
	}

	list, err := ParseIssueDocumentListArgs([]string{"--issue-id", "az-1"})
	if err != nil {
		t.Fatalf("ParseIssueDocumentListArgs() error = %v", err)
	}
	if list.IssueID != "az-1" {
		t.Fatalf("ParseIssueDocumentListArgs() = %+v", list)
	}
	list, err = ParseIssueDocumentListArgs([]string{"az-2", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueDocumentListArgs() interspersed args error = %v", err)
	}
	if list.IssueID != "az-2" || !list.JSON {
		t.Fatalf("ParseIssueDocumentListArgs() interspersed args = %+v", list)
	}
	_, err = ParseIssueDocumentListArgs([]string{"--impl", "go-bubbletea", "az-1"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue document list") {
		t.Fatalf("expected impl forbidden error for document list, got %v", err)
	}

	remove, err := ParseIssueDocumentRemoveArgs([]string{"--issue-id", "az-1", "--attachment-id", "abc123"})
	if err != nil {
		t.Fatalf("ParseIssueDocumentRemoveArgs() error = %v", err)
	}
	if remove.IssueID != "az-1" || remove.AttachmentID != "abc123" {
		t.Fatalf("ParseIssueDocumentRemoveArgs() = %+v", remove)
	}
	remove, err = ParseIssueDocumentRemoveArgs([]string{"az-2", "--attachment-id", "def456"})
	if err != nil {
		t.Fatalf("ParseIssueDocumentRemoveArgs() interspersed args error = %v", err)
	}
	if remove.IssueID != "az-2" || remove.AttachmentID != "def456" {
		t.Fatalf("ParseIssueDocumentRemoveArgs() interspersed args = %+v", remove)
	}
	_, err = ParseIssueDocumentRemoveArgs([]string{"--impl", "go-bubbletea", "az-1", "abc123"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue document remove") {
		t.Fatalf("expected impl forbidden error for document remove, got %v", err)
	}
}

func TestParseIssueBulkArgs(t *testing.T) {
	create, err := ParseIssueBulkCreateArgs([]string{"--impl", "go-bubbletea", "--input", "bulk-create.json", "--dry-run"})
	if err != nil {
		t.Fatalf("ParseIssueBulkCreateArgs() error = %v", err)
	}
	if create.Implementation != "go-bubbletea" || create.InputPath != "bulk-create.json" || !create.DryRun {
		t.Fatalf("ParseIssueBulkCreateArgs() = %+v", create)
	}
	createNoImpl, err := ParseIssueBulkCreateArgs([]string{"--input", "bulk-create.json"})
	if err != nil {
		t.Fatalf("ParseIssueBulkCreateArgs() missing impl should parse, got %v", err)
	}
	if createNoImpl.Implementation != "" || createNoImpl.InputPath != "bulk-create.json" || createNoImpl.DryRun {
		t.Fatalf("ParseIssueBulkCreateArgs() missing impl = %+v", createNoImpl)
	}

	update, err := ParseIssueBulkUpdateArgs([]string{"--impl", "go-bubbletea", "--input", "bulk-update.json"})
	if err != nil {
		t.Fatalf("ParseIssueBulkUpdateArgs() error = %v", err)
	}
	if update.Implementation != "go-bubbletea" || update.InputPath != "bulk-update.json" || update.DryRun {
		t.Fatalf("ParseIssueBulkUpdateArgs() = %+v", update)
	}
	updateNoImpl, err := ParseIssueBulkUpdateArgs([]string{"--input", "bulk-update.json"})
	if err != nil {
		t.Fatalf("ParseIssueBulkUpdateArgs() missing impl should parse, got %v", err)
	}
	if updateNoImpl.Implementation != "" || updateNoImpl.InputPath != "bulk-update.json" || updateNoImpl.DryRun {
		t.Fatalf("ParseIssueBulkUpdateArgs() missing impl = %+v", updateNoImpl)
	}
	_, err = ParseIssueBulkUpdateArgs([]string{"--impl", "go-bubbletea"})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --input") {
		t.Fatalf("expected missing input error for bulk-update, got %v", err)
	}

	depBulk, err := ParseIssueDependencyBulkApplyArgs([]string{"--input", "dep-bulk.json", "--dry-run", "--json"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyBulkApplyArgs() error = %v", err)
	}
	if depBulk.InputPath != "dep-bulk.json" || !depBulk.DryRun || !depBulk.JSON {
		t.Fatalf("ParseIssueDependencyBulkApplyArgs() = %+v", depBulk)
	}
	_, err = ParseIssueDependencyBulkApplyArgs([]string{"--impl", "go-bubbletea", "--input", "dep-bulk.json"})
	if err == nil || !strings.Contains(err.Error(), "--impl is not supported for issue dep bulk apply") {
		t.Fatalf("expected impl forbidden error for dep bulk apply, got %v", err)
	}
}

func TestResolveIssueWriteImplementation(t *testing.T) {
	depsWithTasks := func(t *testing.T, tasks []domain.Task) *Dependencies {
		t.Helper()
		return &Dependencies{
			Config: config.DefaultConfig(),
			DaemonClient: daemonclient.New(&fakeDaemonTransport{
				commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
					if req.Command != daemonclient.CommandTaskList {
						t.Fatalf("unexpected command %q", req.Command)
					}
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal tasks: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				},
			}),
		}
	}

	t.Run("explicit implementation wins", func(t *testing.T) {
		deps := depsWithTasks(t, []domain.Task{
			{ID: "az-1", Implementations: []string{"go-bubbletea"}},
		})
		impl, err := resolveIssueWriteImplementation(context.Background(), deps, "go-bubbletea")
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementation() error = %v", err)
		}
		if impl != "go-bubbletea" {
			t.Fatalf("resolveIssueWriteImplementation() = %q, want go-bubbletea", impl)
		}
	})

	t.Run("defaults to configured single implementation", func(t *testing.T) {
		deps := depsWithTasks(t, []domain.Task{
			{ID: "az-1", Implementations: []string{"go-bubbletea"}},
		})
		impl, err := resolveIssueWriteImplementation(context.Background(), deps, "")
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementation() error = %v", err)
		}
		if impl != "go-bubbletea" {
			t.Fatalf("resolveIssueWriteImplementation() = %q, want go-bubbletea", impl)
		}
	})

	t.Run("falls back to default when no implementations are configured", func(t *testing.T) {
		deps := depsWithTasks(t, []domain.Task{{ID: "az-1"}})
		impl, err := resolveIssueWriteImplementation(context.Background(), deps, "")
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementation() error = %v", err)
		}
		if impl != "default" {
			t.Fatalf("resolveIssueWriteImplementation() = %q, want default", impl)
		}
	})

	t.Run("requires explicit selection when multiple implementations are configured", func(t *testing.T) {
		deps := depsWithTasks(t, []domain.Task{
			{ID: "az-1", Implementations: []string{"go-bubbletea"}},
			{ID: "az-2", Implementations: []string{"default"}},
		})
		_, err := resolveIssueWriteImplementation(context.Background(), deps, "")
		if err == nil || !strings.Contains(err.Error(), "missing required flag: --impl (implementation is ambiguous; valid --impl values: default, go-bubbletea)") {
			t.Fatalf("expected multi-implementation error, got %v", err)
		}
	})

	t.Run("rejects explicit unknown implementation", func(t *testing.T) {
		deps := depsWithTasks(t, []domain.Task{
			{ID: "az-1", Implementations: []string{"default"}},
			{ID: "az-2", Implementations: []string{"go-bubbletea"}},
		})
		_, err := resolveIssueWriteImplementation(context.Background(), deps, "cif")
		if err == nil {
			t.Fatal("resolveIssueWriteImplementation() error = nil, want unknown implementation")
		}
		for _, want := range []string{
			`unknown implementation "cif"`,
			"valid --impl values: default, go-bubbletea",
			"Run `az impl list`",
			"parent work under",
			"--parent cif",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want substring %q", err, want)
			}
		}
	})

	t.Run("allows only default when no implementations are configured", func(t *testing.T) {
		deps := depsWithTasks(t, []domain.Task{{ID: "az-1"}})
		impl, err := resolveIssueWriteImplementation(context.Background(), deps, "default")
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementation(default) error = %v", err)
		}
		if impl != "default" {
			t.Fatalf("resolveIssueWriteImplementation(default) = %q, want default", impl)
		}
		_, err = resolveIssueWriteImplementation(context.Background(), deps, "cif")
		if err == nil || !strings.Contains(err.Error(), `unknown implementation "cif" (valid --impl values: default)`) {
			t.Fatalf("resolveIssueWriteImplementation(cif) error = %v, want unknown default-only implementation", err)
		}
	})

	t.Run("validates repeated implementation assignments", func(t *testing.T) {
		deps := depsWithTasks(t, []domain.Task{
			{ID: "az-1", Implementations: []string{"default"}},
			{ID: "az-2", Implementations: []string{"go-bubbletea"}},
		})
		impls, err := resolveIssueWriteImplementations(context.Background(), deps, []string{" go-bubbletea ", "default", "go-bubbletea"})
		if err != nil {
			t.Fatalf("resolveIssueWriteImplementations() error = %v", err)
		}
		if !reflect.DeepEqual(impls, []string{"go-bubbletea", "default"}) {
			t.Fatalf("resolveIssueWriteImplementations() = %+v, want deduped implementations", impls)
		}
		_, err = resolveIssueWriteImplementations(context.Background(), deps, []string{"default", "cif"})
		if err == nil || !strings.Contains(err.Error(), `unknown implementation "cif"`) {
			t.Fatalf("resolveIssueWriteImplementations() error = %v, want unknown implementation", err)
		}
	})
}

func TestIssueListCommandUsesDaemonTaskList(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	estimateThree := 3
	estimateEight := 8
	tasks := []domain.Task{
		{
			ID:       "az-2",
			Title:    "Older issue",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Assignee: "alex",
			Estimate: &estimateThree,
			Implementations: []string{
				"default",
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:       "az-1",
			Title:    "Newest issue",
			Status:   domain.StatusInProgress,
			Priority: domain.P1,
			Type:     domain.TypeFeature,
			Assignee: "sam",
			Estimate: &estimateEight,
			Implementations: []string{
				"go-bubbletea",
			},
			CreatedAt: now,
			UpdatedAt: now.Add(1 * time.Hour),
		},
	}

	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{})
	})

	if gotReq.Command != daemonclient.CommandTaskList {
		t.Fatalf("command = %q, want %q", gotReq.Command, daemonclient.CommandTaskList)
	}
	if !strings.Contains(output, "ID") || !strings.Contains(output, "STATUS") || !strings.Contains(output, "PRIORITY") || !strings.Contains(output, "ASSIGNEE") || !strings.Contains(output, "EST") || !strings.Contains(output, "IMPL") || !strings.Contains(output, "TITLE") {
		t.Fatalf("output missing header: %q", output)
	}
	for _, want := range []string{"go-bubbletea", "default", "sam", "alex", "8", "3"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
	if first, second := strings.Index(output, "az-1"), strings.Index(output, "az-2"); !(first >= 0 && second > first) {
		t.Fatalf("expected newest issue first in output: %q", output)
	}
}

func TestIssueListCommandDepsProjection(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:       "az-1",
			Title:    "Dependent issue",
			Status:   domain.StatusInProgress,
			Priority: domain.P1,
			Type:     domain.TypeFeature,
			Dependencies: []domain.Dependency{
				{ID: "az-2", Type: domain.DependencyBlocks},
				{ID: "az-3", Type: domain.DependencyBlockedBy},
				{ID: "az-4", Type: domain.DependencyBlockedBy},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Deps: true})
	})
	if !strings.Contains(output, "DEPS") {
		t.Fatalf("deps output missing DEPS column: %q", output)
	}
	if !strings.Contains(output, "blocks:1,blocked-by:2") {
		t.Fatalf("deps output missing summary: %q", output)
	}
}

func TestIssueListCommand_IncludesListWindowMetadata(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-1",
			Title:     "Newest issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeFeature,
			CreatedAt: now,
			UpdatedAt: now.Add(1 * time.Hour),
		},
		{
			ID:        "az-2",
			Title:     "Older issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Limit: 1})
	})
	if !strings.Contains(output, "List window: listed=1 limit=1 total=2 truncated=yes") {
		t.Fatalf("list metadata missing expected window summary: %q", output)
	}
	if !strings.Contains(output, "Window note: additional matching issues may exist beyond current limit.") {
		t.Fatalf("list metadata missing truncated window note: %q", output)
	}
}

func TestIssueListCommand_IDSetFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
		{ID: "az-2", Title: "Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
		{ID: "az-3", Title: "Three", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{IDs: []string{"az-3", "az-1"}})
	})
	if strings.Contains(output, "az-2") {
		t.Fatalf("id-set filter should exclude az-2: %q", output)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "az-3") {
		t.Fatalf("id-set filter should include requested issues: %q", output)
	}
}

func TestIssueListCommand_StatusFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "Open", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
		{ID: "az-2", Title: "Review", Status: domain.StatusInReview, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
		{ID: "az-3", Title: "Closed", Status: domain.StatusDone, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
		{ID: "az-4", Title: "Backlog", Status: domain.StatusOpen, State: mustCLICommandIssueState(t, domain.IssueWorkflowBacklog), Priority: domain.P0, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(4 * time.Hour)},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{States: []domain.IssueDisplayPhase{domain.IssueDisplayOpen, domain.IssueDisplayReview}})
	})
	if strings.Contains(output, "az-3") || strings.Contains(output, "az-4") {
		t.Fatalf("status filter should exclude closed and backlog issues: %q", output)
	}
	if !strings.Contains(output, "az-1") || !strings.Contains(output, "az-2") {
		t.Fatalf("status filter should include matching issues: %q", output)
	}
}

func TestIssueListCommand_StatusFilterBacklogDisplaysStateDerivedStatus(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "Open", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
		{ID: "az-2", Title: "Backlog", Status: domain.StatusOpen, State: mustCLICommandIssueState(t, domain.IssueWorkflowBacklog), Priority: domain.P0, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return responseWithBody(req, body), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{States: []domain.IssueDisplayPhase{domain.IssueDisplayBacklog}})
	})
	if strings.Contains(output, "az-1") || !strings.Contains(output, "az-2") || !strings.Contains(output, "backlog") {
		t.Fatalf("backlog filter output = %q, want only az-2 with state-derived backlog status", output)
	}
}

func TestIssueListCommand_ContentQueryDelegatesToTaskList(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	filteredTasks := []domain.Task{
		{ID: "az-1", Title: "Alpha", Description: "Contains runtime cache evidence", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
	}
	commands := make([]string, 0, 1)

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskList:
					var got struct {
						Query string `json:"query"`
					}
					if err := json.Unmarshal(req.Body, &got); err != nil {
						t.Fatalf("unmarshal task.list request: %v", err)
					}
					if got.Query != "runtime CACHE" {
						t.Fatalf("task.list query = %q, want runtime CACHE", got.Query)
					}
					body, err := marshalTaskListBody(filteredTasks)
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return responseWithBody(req, body), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Query: "runtime CACHE"})
	})
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandTaskList}) {
		t.Fatalf("commands = %v, want only task.list", commands)
	}
	if !strings.Contains(output, "az-1") || strings.Contains(output, "az-2") {
		t.Fatalf("content query output = %q, want only az-1", output)
	}
}

func TestIssueSearchGlobalUsesScopedProjectionConsumer(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			if req.Command != protocol.CommandGlobalSnapshot {
				t.Fatalf("command = %q, want global.snapshot", req.Command)
			}
			var request protocol.GlobalSnapshotRequestBody
			if err := json.Unmarshal(req.Body, &request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if request.Consumer != protocol.GlobalViewConsumerSearch || request.Query != "runtime cache" {
				t.Fatalf("request = %+v", request)
			}
			body, err := json.Marshal(protocol.GlobalSnapshotResponseBody{
				SchemaVersion: protocol.GlobalProjectionSchemaVersion,
				Projection: protocol.GlobalViewProjection{Items: []protocol.GlobalViewProjectedItem{{
					Identity: protocol.ScopedIssueID{ProjectID: "alpha", IssueID: "ddm"},
					Task:     domain.Task{ID: "ddm", Title: "Runtime cache", Status: domain.StatusOpen},
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			return responseWithBody(req, body), nil
		}}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Project: "global", Query: "runtime cache"})
	})
	if !strings.Contains(output, "alpha:ddm") || !strings.Contains(output, "Runtime cache") {
		t.Fatalf("global search output = %q", output)
	}
}

func TestIssueSearchGlobalRejectsUnsupportedDependencyAndArchiveFlags(t *testing.T) {
	deps := &Dependencies{DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		t.Fatalf("unexpected daemon command %s", req.Command)
		return protocol.ResponseEnvelope{}, nil
	}})}
	if err := IssueListCommand(deps, IssueListOptions{Project: "global", Deps: true}); err == nil || !strings.Contains(err.Error(), "--deps") {
		t.Fatalf("deps error = %v", err)
	}
	if err := IssueListCommand(deps, IssueListOptions{Project: "global", Archived: "include"}); err == nil || !strings.Contains(err.Error(), "--archived exclude") {
		t.Fatalf("archive error = %v", err)
	}
}

func TestIssueListCommand_DateRangeFilters(t *testing.T) {
	base := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "Inside", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: base, UpdatedAt: base.Add(1 * time.Hour)},
		{ID: "az-2", Title: "Too old", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: base.Add(-48 * time.Hour), UpdatedAt: base.Add(1 * time.Hour)},
		{ID: "az-3", Title: "Too new", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: base, UpdatedAt: base.Add(48 * time.Hour)},
	}
	createdAfter := base.Add(-24 * time.Hour)
	updatedBefore := base.Add(24 * time.Hour)

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return responseWithBody(req, body), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{CreatedAfter: &createdAfter, UpdatedBefore: &updatedBefore})
	})
	if !strings.Contains(output, "az-1") || strings.Contains(output, "az-2") || strings.Contains(output, "az-3") {
		t.Fatalf("date range output = %q, want only az-1", output)
	}
}

func TestIssueListCommand_ParentFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")
	otherParentID := naming.IssueID("az-other-parent")
	tasks := []domain.Task{
		{ID: "az-1", ParentID: &parentID, Title: "Child One", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour)},
		{ID: "az-2", ParentID: &otherParentID, Title: "Child Two", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour)},
		{ID: "az-3", Title: "Top Level", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour)},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{ParentIDs: []string{"az-parent"}})
	})
	if strings.Contains(output, "az-2") || strings.Contains(output, "az-3") {
		t.Fatalf("parent filter should exclude non-matching tasks: %q", output)
	}
	if !strings.Contains(output, "az-1") {
		t.Fatalf("parent filter should include matching task: %q", output)
	}
}

func TestIssueListCommand_DependencyTargetFilter(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID: "az-1", Title: "Depends on az-100", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(3 * time.Hour),
			Dependencies: []domain.Dependency{{ID: "az-100", Type: domain.DependencyBlocks}},
		},
		{
			ID: "az-2", Title: "Depends on az-200", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(2 * time.Hour),
			Dependencies: []domain.Dependency{{ID: "az-200", Type: domain.DependencyRelatedTo}},
		},
		{
			ID: "az-3", Title: "No dependencies", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now.Add(1 * time.Hour),
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        2,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{DependsOnIDs: []string{"az-100"}})
	})
	if strings.Contains(output, "az-2") || strings.Contains(output, "az-3") {
		t.Fatalf("dependency filter should exclude non-matching tasks: %q", output)
	}
	if !strings.Contains(output, "az-1") {
		t.Fatalf("dependency filter should include matching task: %q", output)
	}
}

func TestIssueListCommandDepsProjection_IncludesTopLevelGraphContext(t *testing.T) {
	now := time.Date(2026, 3, 26, 2, 10, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")
	tasks := []domain.Task{
		{
			ID:        parentID,
			Title:     "Parent issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeEpic,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        naming.IssueID("az-child"),
			Title:     "Child issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &parentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:       naming.IssueID("az-dependent"),
			Title:    "Depends on parent via blocks",
			Status:   domain.StatusOpen,
			Priority: domain.P2,
			Type:     domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: parentID, Type: domain.DependencyBlocks},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueListCommand(deps, IssueListOptions{Deps: true, Limit: 10})
	})
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Top-level issues:") {
		t.Fatalf("deps output should start with top-level context, got: %q", output)
	}
	for _, want := range []string{
		"Top-level issues:",
		"Dependency links (listed issues):",
		"- az-child -> az-parent (parent-child)",
		"- az-dependent -> az-parent (blocks)",
		"List window: listed=3 limit=10 total=3 truncated=no",
		"Window note: all matching issues are shown.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps graph context missing %q: %q", want, output)
		}
	}
}

func TestIssueGetCommandJSON(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Status:      domain.StatusInReview,
			Priority:    domain.P0,
			Type:        domain.TypeBug,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5", JSON: true})
	})
	if !strings.Contains(output, "\"id\": \"az-5\"") || !strings.Contains(output, "\"title\": \"Lookup issue\"") {
		t.Fatalf("output missing issue json fields: %q", output)
	}
}

func TestIssueEventsCommandJSON(t *testing.T) {
	observedAt := time.Date(2026, 7, 6, 2, 0, 0, 0, time.UTC)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskEvents {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskEvents)
				}
				var body daemonclient.TaskEventsRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal task events body: %v", err)
				}
				if body.TaskID != "az-5" || body.Limit != 10 || !reflect.DeepEqual(body.Types, []string{"issue.status_changed"}) {
					t.Fatalf("task events body = %+v", body)
				}
				next := int64(8)
				respBody, err := json.Marshal(protocol.TaskEventsPage{
					Order: "asc", Limit: 10, HasMore: true, FirstID: 7, LastID: 8, NextAfterID: &next,
					Events: []domain.IssueObservationEvent{{
						ID:         7,
						IssueID:    "az-5",
						Type:       domain.IssueEventIssueStatusChanged,
						ObservedAt: observedAt,
						Source:     "issue-store",
						Payload: map[string]any{
							"from_status": "open",
							"to_status":   "in_progress",
						},
					}, {
						ID:         8,
						IssueID:    "az-5",
						Type:       domain.IssueEventEvidenceSubmitted,
						ObservedAt: observedAt.Add(time.Second),
					}},
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            respBody,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueEventsCommand(deps, IssueEventsOptions{
			IssueID:    "az-5",
			JSON:       true,
			EventTypes: []string{"issue.status_changed"},
			Limit:      10,
		})
	})

	var got struct {
		SchemaVersion string              `json:"schema_version"`
		IssueID       string              `json:"issue_id"`
		Page          issueEventsPageJSON `json:"page"`
		Events        []struct {
			ID         int64          `json:"id"`
			IssueID    string         `json:"issue_id"`
			Type       string         `json:"type"`
			EventType  string         `json:"event_type"`
			CreatedAt  time.Time      `json:"created_at"`
			ObservedAt time.Time      `json:"observed_at"`
			Source     string         `json:"source"`
			Body       *string        `json:"body"`
			Notes      *string        `json:"notes"`
			Data       map[string]any `json:"data"`
			Payload    map[string]any `json:"payload"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal issue events json: %v\n%s", err, output)
	}
	if got.SchemaVersion != "issue_events.v2" || got.IssueID != "az-5" || len(got.Events) != 2 {
		t.Fatalf("issue events json header = %+v", got)
	}
	if got.Page.Order != "asc" || got.Page.Limit != 10 || !got.Page.HasMore || got.Page.NextAfterID == nil || *got.Page.NextAfterID != 8 {
		t.Fatalf("issue events page = %+v", got.Page)
	}
	event := got.Events[0]
	if event.ID != 7 || event.IssueID != "az-5" {
		t.Fatalf("event identity = %+v", event)
	}
	if event.Type != "issue.status_changed" || event.EventType != event.Type {
		t.Fatalf("event type fields = type %q event_type %q", event.Type, event.EventType)
	}
	if !event.CreatedAt.Equal(observedAt) || !event.ObservedAt.Equal(observedAt) {
		t.Fatalf("event timestamp fields = created_at %s observed_at %s, want %s", event.CreatedAt, event.ObservedAt, observedAt)
	}
	if event.Source != "issue-store" {
		t.Fatalf("event source = %q", event.Source)
	}
	if event.Body == nil || event.Notes == nil {
		t.Fatalf("event body/notes fields missing: body=%v notes=%v", event.Body, event.Notes)
	}
	if event.Data["to_status"] != "in_progress" || event.Payload["to_status"] != "in_progress" {
		t.Fatalf("event data aliases = data %+v payload %+v", event.Data, event.Payload)
	}
	emptyPayloadEvent := got.Events[1]
	if emptyPayloadEvent.Type != "evidence.submitted" {
		t.Fatalf("empty payload event type = %q", emptyPayloadEvent.Type)
	}
	if emptyPayloadEvent.Body == nil || emptyPayloadEvent.Notes == nil {
		t.Fatalf("empty payload event body/notes fields missing: body=%v notes=%v", emptyPayloadEvent.Body, emptyPayloadEvent.Notes)
	}
	if emptyPayloadEvent.Data == nil || emptyPayloadEvent.Payload == nil || len(emptyPayloadEvent.Data) != 0 || len(emptyPayloadEvent.Payload) != 0 {
		t.Fatalf("empty payload aliases = data %+v payload %+v, want empty objects", emptyPayloadEvent.Data, emptyPayloadEvent.Payload)
	}
}

func TestIssueRecordCommandAppendsDurableObservationEvent(t *testing.T) {
	observedAt := time.Date(2026, 7, 8, 1, 0, 0, 0, time.UTC)
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskEventAppend {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskEventAppend)
				}
				var body daemonclient.TaskEventAppendRequest
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal task event append body: %v", err)
				}
				if body.TaskID != "az-5" || body.Type != string(domain.IssueEventFollowupCreated) {
					t.Fatalf("append body identity = %+v", body)
				}
				if body.SourceCommand != "az issue record" {
					t.Fatalf("source command = %q", body.SourceCommand)
				}
				if body.Payload["summary"] != "Created follow-up issues" {
					t.Fatalf("payload summary = %+v", body.Payload)
				}
				followUps, ok := body.Payload["follow_up_issue_ids"].([]any)
				if !ok || len(followUps) != 2 || followUps[0] != "az-6" || followUps[1] != "az-7" {
					t.Fatalf("payload follow ups = %#v", body.Payload["follow_up_issue_ids"])
				}
				return responseWithJSON(req, struct {
					Event domain.IssueObservationEvent `json:"event"`
				}{
					Event: domain.IssueObservationEvent{
						ID:         42,
						IssueID:    "az-5",
						Type:       domain.IssueEventFollowupCreated,
						ObservedAt: observedAt,
						Source:     "agent",
						Payload:    body.Payload,
					},
				}), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/repo",
	}

	output := captureStdout(t, func() error {
		return IssueRecordCommand(deps, IssueRecordOptions{
			IssueID:          "az-5",
			EventType:        string(domain.IssueEventFollowupCreated),
			Summary:          "Created follow-up issues",
			FollowUpIssueIDs: []string{"az-6", "az-7"},
			Source:           "agent",
			JSON:             true,
		})
	})

	var got issueEventJSON
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal issue record json: %v\n%s", err, output)
	}
	if got.ID != 42 || got.Type != string(domain.IssueEventFollowupCreated) || got.Data["summary"] != "Created follow-up issues" {
		t.Fatalf("recorded event = %+v", got)
	}
}

func TestIssueEventsCommandJQHelp(t *testing.T) {
	output := captureStdout(t, func() error {
		return IssueEventsCommand(&Dependencies{}, IssueEventsOptions{JQHelp: true})
	})
	for _, want := range []string{
		"az issue events az-123 --json | jq '.events[0]'",
		"jq '[.events[] | {type, created_at, source, data}]'",
		"jq '.events[] | select(.type == \"issue.status_changed\")'",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("jq help missing %q in:\n%s", want, output)
		}
	}
}

func TestIssueGetCommandUsesSingleIssueDaemonRead(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	var taskGetCalled bool
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					taskGetCalled = true
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task get body: %v", err)
					}
					if body.TaskID != "az-5" {
						t.Fatalf("task_id = %q, want az-5", body.TaskID)
					}
					bodyBytes, err := marshalTaskListBody([]domain.Task{{
						ID:        "az-5",
						Title:     "Lookup issue",
						Status:    domain.StatusOpen,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						CreatedAt: now,
						UpdatedAt: now,
					}})
					if err != nil {
						t.Fatalf("marshal tasks: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        4,
						Body:            bodyBytes,
					}, nil
				case daemonclient.CommandDecisionLinkList:
					body, _ := json.Marshal(daemonclient.DecisionLinkListResult{})
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					t.Fatalf("unexpected daemon command: %q", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	_ = captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5"})
	})
	if !taskGetCalled {
		t.Fatalf("expected task.get to be invoked")
	}
}

func TestIssueGetCommandTextHidesNotesByDefault(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Notes:       "First note line\nSecond note line",
			Status:      domain.StatusInReview,
			Priority:    domain.P0,
			Type:        domain.TypeBug,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5"})
	})
	if !strings.Contains(output, "Notes: present (hidden in text output; use `az issue get az-5 --with-notes`") {
		t.Fatalf("output missing hidden notes sentinel: %q", output)
	}
	if strings.Contains(output, "First note line") || strings.Contains(output, "Second note line") {
		t.Fatalf("output should not include full notes by default: %q", output)
	}
}

func TestIssueGetCommandTextIncludesNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-5",
			Title:       "Lookup issue",
			Description: "Detailed context",
			Notes:       "First note line\nSecond note line",
			Status:      domain.StatusInReview,
			Priority:    domain.P0,
			Type:        domain.TypeBug,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5", IncludeNotes: true})
	})
	if !strings.Contains(output, "Notes:\nFirst note line\nSecond note line\n") {
		t.Fatalf("output missing notes section: %q", output)
	}
}

// issueGetDecisionMock answers task.get with the given task list and
// decision.link.list with the given enriched result. Other commands fail
// the test.
func issueGetDecisionMock(t *testing.T, tasks []domain.Task, decisionResult daemonclient.DecisionLinkListResult) *fakeDaemonTransport {
	t.Helper()
	return &fakeDaemonTransport{
		commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			switch req.Command {
			case daemonclient.CommandTaskGet, daemonclient.CommandTaskList:
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			case daemonclient.CommandDecisionLinkList:
				body, err := json.Marshal(decisionResult)
				if err != nil {
					t.Fatalf("marshal decision result: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			default:
				t.Fatalf("unexpected command: %s", req.Command)
				return protocol.ResponseEnvelope{}, nil
			}
		},
	}
}

func TestIssueGetCommandTextRendersDecisions(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-5",
			Title:     "Lookup issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	decisions := daemonclient.DecisionLinkListResult{
		Links: []daemonclient.DecisionLink{
			{DecisionID: "dec-1", TargetKind: daemonclient.DecisionTargetIssue, TargetID: "az-5", Relation: "applies-to"},
			{DecisionID: "dec-2", TargetKind: daemonclient.DecisionTargetIssue, TargetID: "az-5", Relation: "informs", Note: "discussed at sync"},
		},
		Decisions: []daemonclient.Decision{
			{ID: "dec-1", Title: "Use SQLite for decision store"},
			{ID: "dec-2", Title: "Polymorphic decision_links table"},
		},
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(issueGetDecisionMock(t, tasks, decisions)),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5"})
	})

	for _, want := range []string{
		"Decisions: 2",
		"applies-to",
		"dec-1",
		"Use SQLite for decision store",
		"informs",
		"dec-2",
		"Polymorphic decision_links table",
		"discussed at sync",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q\nfull output:\n%s", want, output)
		}
	}
}

func TestIssueGetCommandJSONIncludesDecisions(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-5",
			Title:     "Lookup issue",
			Status:    domain.StatusInProgress,
			Priority:  domain.P1,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	decisions := daemonclient.DecisionLinkListResult{
		Links: []daemonclient.DecisionLink{
			{DecisionID: "dec-1", TargetKind: daemonclient.DecisionTargetIssue, TargetID: "az-5", Relation: "applies-to"},
		},
		Decisions: []daemonclient.Decision{
			{ID: "dec-1", Title: "Use SQLite for decision store"},
		},
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(issueGetDecisionMock(t, tasks, decisions)),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5", JSON: true})
	})

	if !strings.Contains(output, "\"id\": \"az-5\"") {
		t.Fatalf("output missing task id: %q", output)
	}
	if !strings.Contains(output, "\"decisions\"") {
		t.Fatalf("output missing decisions key: %q", output)
	}
	for _, want := range []string{
		"\"id\": \"dec-1\"",
		"\"title\": \"Use SQLite for decision store\"",
		"\"relation\": \"applies-to\"",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("decisions json missing %q\nfull output:\n%s", want, output)
		}
	}

	// And the JSON must still parse with the task fields at the top level so
	// existing consumers that unmarshal into a Task-like shape keep working.
	var parsed struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Decisions []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("decode envelope: %v\nraw=%s", err, output)
	}
	if parsed.ID != "az-5" || parsed.Title != "Lookup issue" {
		t.Fatalf("envelope task fields = %+v", parsed)
	}
	if len(parsed.Decisions) != 1 || parsed.Decisions[0].ID != "dec-1" {
		t.Fatalf("envelope decisions = %+v", parsed.Decisions)
	}
}

func TestIssueGetCommandTextIncludesRuntimeGitAndImplementations(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	started := now.Add(-15 * time.Minute)
	estimate := 13
	tasks := []domain.Task{
		{
			ID:                    "az-5",
			Title:                 "Lookup issue",
			Design:                "Design notes",
			Acceptance:            "- acceptance one",
			Assignee:              "sam",
			Labels:                []string{"cli", "notes"},
			Estimate:              &estimate,
			Status:                domain.StatusInReview,
			Priority:              domain.P0,
			Type:                  domain.TypeBug,
			Implementations:       []string{"default", "go-bubbletea"},
			Session:               &domain.Session{IssueID: "az-5", State: domain.SessionBusy, TmuxAttached: true, TmuxAttachedCount: 1, StartedAt: &started},
			HasTmuxSession:        true,
			HasWorktree:           true,
			HasUncommittedChanges: true,
			GitAdditions:          12,
			GitDeletions:          3,
			GitAheadCount:         2,
			GitBehindCount:        1,
			CreatedAt:             now,
			UpdatedAt:             now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-5"})
	})
	for _, want := range []string{
		"Implementations: default, go-bubbletea",
		"Assignee: sam",
		"Labels: cli, notes",
		"Estimate: 13",
		"Runtime: session=busy tmux_attached=yes since 2026-03-25T10:45:00Z, worktree=yes",
		"Git: dirty, +12/-3, ahead=2 behind=1",
		"Design:\nDesign notes\n",
		"Acceptance:\n- acceptance one\n",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
}

func TestIssueGetCommandNotFound(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody([]domain.Task{})
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        1,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueGetCommand(deps, IssueGetOptions{IssueID: "az-missing"})
	if err == nil || !strings.Contains(err.Error(), "issue not found: az-missing") {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestIssueGetCommandDepsProjection(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-8",
			Title:       "Dependency detail",
			Description: "Detail context",
			Status:      domain.StatusOpen,
			Priority:    domain.P2,
			Type:        domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: "az-2", Type: domain.DependencyBlocks},
				{ID: "az-5", Type: domain.DependencyRelatedTo},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-8"})
	})
	if !strings.Contains(output, "Dependency edges:") {
		t.Fatalf("deps output missing dependency section: %q", output)
	}
	if !strings.Contains(output, "- az-2 (blocks, status=unknown)") || !strings.Contains(output, "- az-5 (related, status=unknown)") {
		t.Fatalf("deps output missing dependency rows: %q", output)
	}
}

func TestIssueGetCommandDepsProjectionCanonicalTypes(t *testing.T) {
	now := time.Date(2026, 3, 25, 11, 30, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:          "az-9",
			Title:       "Dependency matrix",
			Description: "Ensure deps output labels are canonical",
			Status:      domain.StatusOpen,
			Priority:    domain.P2,
			Type:        domain.TypeTask,
			Dependencies: []domain.Dependency{
				{ID: "az-a", Type: domain.DependencyBlocks},
				{ID: "az-b", Type: domain.DependencyParentChild},
				{ID: "az-c", Type: domain.DependencyRelatedTo},
				{ID: "az-d", Type: domain.DependencyDiscovered},
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        5,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: "az-9"})
	})
	if !strings.Contains(output, "Dependency edges:") {
		t.Fatalf("deps output missing dependency section: %q", output)
	}
	for _, want := range []string{
		"- az-a (blocks, status=unknown)",
		"- az-b (parent-child, status=unknown)",
		"- az-c (related, status=unknown)",
		"- az-d (discovered-from, status=unknown)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps output missing %q: %q", want, output)
		}
	}
}

func TestIssueGetCommandDepsProjectionIncludesDependentsAndParentEdge(t *testing.T) {
	now := time.Date(2026, 3, 26, 1, 15, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")
	targetID := naming.IssueID("az-target")
	childParentID := targetID
	tasks := []domain.Task{
		{
			ID:        parentID,
			Title:     "Parent issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeEpic,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        targetID,
			Title:     "Target issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &parentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        naming.IssueID("az-child"),
			Title:     "Child issue",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			ParentID:  &childParentID,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        6,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetCommand(deps, IssueGetOptions{IssueID: targetID.String()})
	})
	for _, want := range []string{
		"Dependency edges:",
		"- az-parent (parent-child, status=open)",
		"Dependents:",
		"- az-child (parent-child, status=open)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("deps projection missing %q: %q", want, output)
		}
	}
}

func TestIssueGetManyCommand_JSONStableOrderWithPartialMisses(t *testing.T) {
	now := time.Date(2026, 3, 26, 3, 15, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "First", Notes: "first notes", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "az-2", Title: "Second", Notes: "second notes", Status: domain.StatusInProgress, Priority: domain.P1, Type: domain.TypeFeature, Dependencies: []domain.Dependency{{ID: "az-1", Type: domain.DependencyBlocks}}, CreatedAt: now, UpdatedAt: now},
	}
	var commands []string
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				if req.Command != daemonclient.CommandTaskGetMany {
					t.Fatalf("command = %q, want %q", req.Command, daemonclient.CommandTaskGetMany)
				}
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetManyCommand(deps, IssueGetManyOptions{
			IssueIDs: []string{"az-2", "az-missing", "az-1"},
			JSON:     true,
		})
	})
	var got issueGetManyResult
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal get-many output: %v", err)
	}
	if got.Requested != 3 || got.Found != 2 || got.Missing != 1 {
		t.Fatalf("unexpected summary: %+v", got)
	}
	if len(commands) != 1 {
		t.Fatalf("commands = %v, want one batch read", commands)
	}
	if len(got.Results) != 3 {
		t.Fatalf("results length = %d, want 3", len(got.Results))
	}
	if got.Results[0].ID != "az-2" || got.Results[0].Status != "found" {
		t.Fatalf("result[0] = %+v", got.Results[0])
	}
	if got.Results[0].Issue == nil || got.Results[0].Issue.Notes != "" {
		t.Fatalf("result[0] notes should be omitted by default: %+v", got.Results[0].Issue)
	}
	if len(got.Results[0].Dependencies) != 1 || got.Results[0].Dependencies[0].ID != "az-1" {
		t.Fatalf("result[0] dependencies = %+v", got.Results[0].Dependencies)
	}
	if got.Results[1].ID != "az-missing" || got.Results[1].Status != "not_found" {
		t.Fatalf("result[1] = %+v", got.Results[1])
	}
	if got.Results[2].ID != "az-1" || got.Results[2].Status != "found" {
		t.Fatalf("result[2] = %+v", got.Results[2])
	}
	if len(got.Results[2].Dependents) != 1 || got.Results[2].Dependents[0].ID != "az-2" {
		t.Fatalf("result[2] dependents = %+v", got.Results[2].Dependents)
	}
}

func TestIssueGetManyCommand_JSONIncludesNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 26, 3, 15, 0, 0, time.UTC)
	tasks := []domain.Task{
		{ID: "az-1", Title: "First", Notes: "first notes", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        3,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueGetManyCommand(deps, IssueGetManyOptions{
			IssueIDs:     []string{"az-1"},
			JSON:         true,
			IncludeNotes: true,
		})
	})
	var got issueGetManyResult
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal get-many output: %v", err)
	}
	if got.Results[0].Issue == nil || got.Results[0].Issue.Notes != "first notes" {
		t.Fatalf("result notes = %+v, want included notes", got.Results[0].Issue)
	}
}

func TestIssueDependencyBulkApplyCommand_DryRunOutcomes(t *testing.T) {
	now := time.Date(2026, 3, 26, 4, 0, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-a",
			Title:     "A",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []domain.Dependency{
				{ID: "az-b", Type: domain.DependencyBlocks},
			},
		},
		{ID: "az-b", Title: "B", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "az-c", Title: "C", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				body, err := marshalTaskListBody(tasks)
				if err != nil {
					t.Fatalf("marshal tasks: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Revision:        4,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "dep-bulk.json")
	payload := `{
  "mutations": [
    {"action":"add","issue_id":"az-a","depends_on_id":"az-b","type":"blocks"},
    {"action":"add","issue_id":"az-a","depends_on_id":"az-c","type":"blocks"},
    {"action":"remove","issue_id":"az-a","depends_on_id":"az-z","type":"blocks"},
    {"action":"retarget","issue_id":"az-a","from_id":"az-b","to_id":"az-c","type":"blocks"},
    {"action":"add","issue_id":"az-missing","depends_on_id":"az-b","type":"blocks"}
  ]
}`
	if err := os.WriteFile(inputPath, []byte(payload), 0644); err != nil {
		t.Fatalf("write dep bulk file: %v", err)
	}

	output := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			DryRun:    true,
			JSON:      true,
		})
	})
	var result dependencyBulkResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("unmarshal dry-run output: %v", err)
	}
	if result.Summary.Requested != 5 || result.Summary.Planned != 2 || result.Summary.NoOp != 2 || result.Summary.Invalid != 1 {
		t.Fatalf("unexpected dry-run summary: %+v", result.Summary)
	}
	statuses := make([]string, 0, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		statuses = append(statuses, outcome.Status)
	}
	if !reflect.DeepEqual(statuses, []string{"no-op", "planned", "no-op", "planned", "invalid"}) {
		t.Fatalf("unexpected dry-run statuses: %+v", statuses)
	}
}

func TestIssueDependencyBulkApplyCommand_IdempotentReplayNoOp(t *testing.T) {
	now := time.Date(2026, 3, 26, 4, 45, 0, 0, time.UTC)
	tasks := []domain.Task{
		{
			ID:        "az-a",
			Title:     "A",
			Status:    domain.StatusOpen,
			Priority:  domain.P2,
			Type:      domain.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{ID: "az-b", Title: "B", Status: domain.StatusOpen, Priority: domain.P2, Type: domain.TypeTask, CreatedAt: now, UpdatedAt: now},
	}
	applyCalls := 0
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal tasks: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        5,
						Body:            body,
					}, nil
				case protocol.CommandTaskBulkApply:
					applyCalls++
					tasks[0].Dependencies = append(tasks[0].Dependencies, domain.Dependency{
						ID:   "az-b",
						Type: domain.DependencyBlocks,
					})
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        6,
						Body:            []byte(`{}`),
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	tempDir := t.TempDir()
	inputPath := filepath.Join(tempDir, "dep-bulk-apply.json")
	payload := `{"mutations":[{"action":"add","issue_id":"az-a","depends_on_id":"az-b","type":"blocks"}]}`
	if err := os.WriteFile(inputPath, []byte(payload), 0644); err != nil {
		t.Fatalf("write dep bulk file: %v", err)
	}

	first := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			JSON:      true,
		})
	})
	var firstResult dependencyBulkResult
	firstJSON := extractTrailingJSONResult(first)
	if err := json.Unmarshal([]byte(firstJSON), &firstResult); err != nil {
		t.Fatalf("unmarshal first result: %v", err)
	}
	if firstResult.Summary.Planned != 1 || firstResult.Summary.Applied != 1 || applyCalls != 1 {
		t.Fatalf("unexpected first apply result=%+v calls=%d", firstResult.Summary, applyCalls)
	}

	second := captureStdout(t, func() error {
		return IssueDependencyBulkApplyCommand(deps, IssueDependencyBulkApplyOptions{
			InputPath: inputPath,
			JSON:      true,
		})
	})
	var secondResult dependencyBulkResult
	secondJSON := extractTrailingJSONResult(second)
	if err := json.Unmarshal([]byte(secondJSON), &secondResult); err != nil {
		t.Fatalf("unmarshal second result: %v", err)
	}
	if secondResult.Summary.NoOp != 1 || secondResult.Summary.Applied != 0 || applyCalls != 1 {
		t.Fatalf("unexpected second apply result=%+v calls=%d", secondResult.Summary, applyCalls)
	}
}

func extractTrailingJSONResult(output string) string {
	needle := "{\n  \"dry_run\""
	idx := strings.LastIndex(output, needle)
	if idx < 0 {
		return output
	}
	return output[idx:]
}

func TestIssueCreateAndCloseCommandsUseDaemonTaskCommands(t *testing.T) {
	tests := []struct {
		name        string
		run         func(*Dependencies) error
		wantCommand string
		wantText    string
	}{
		{
			name: "create",
			run: func(deps *Dependencies) error {
				return IssueCreateCommand(deps, IssueCreateOptions{
					Implementations: []string{"go-bubbletea"},
					Title:           "New issue",
					Description:     "Context",
					Type:            domain.TypeFeature,
					Priority:        domain.P1,
				})
			},
			wantCommand: daemonclient.CommandTaskCreate,
			wantText:    "Created issue: az-42",
		},
		{
			name: "close",
			run: func(deps *Dependencies) error {
				return IssueCloseCommand(deps, IssueCloseOptions{
					IssueID: "az-9",
				})
			},
			wantCommand: daemonclient.CommandTaskClose,
			wantText:    "Closed issue: az-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq protocol.RequestEnvelope
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						gotReq = req
						body := []byte{}
						if req.Command == daemonclient.CommandTaskCreate {
							payload, err := json.Marshal(map[string]string{"task_id": "az-42"})
							if err != nil {
								t.Fatalf("marshal task create response: %v", err)
							}
							body = payload
						} else if req.Command == daemonclient.CommandTaskList {
							payload, err := marshalTaskListBody([]domain.Task{
								{ID: "az-9", Title: "Close me", Status: domain.StatusInReview, Implementations: []string{"go-bubbletea"}},
							})
							if err != nil {
								t.Fatalf("marshal task list response: %v", err)
							}
							body = payload
						} else if req.Command == daemonclient.CommandTaskClose {
							payload, err := json.Marshal(daemonclient.TaskCloseResult{
								TaskID: "az-9",
								Status: string(domain.StatusDone),
							})
							if err != nil {
								t.Fatalf("marshal task close response: %v", err)
							}
							body = payload
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							Meta:            req.Meta,
							OK:              true,
							CompletedAt:     req.SentAt,
							Body:            body,
						}, nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			output := captureStdout(t, func() error {
				return tt.run(deps)
			})
			if gotReq.Command != tt.wantCommand {
				t.Fatalf("command = %q, want %q", gotReq.Command, tt.wantCommand)
			}
			switch tt.name {
			case "create":
				var createReq daemonclient.TaskCreateParams
				if err := json.Unmarshal(gotReq.Body, &createReq); err != nil {
					t.Fatalf("unmarshal create body: %v", err)
				}
				if createReq.Title != "New issue" || createReq.Description != "Context" || createReq.Type != domain.TypeFeature || createReq.Priority != domain.P1 {
					t.Fatalf("create body = %+v", createReq)
				}
				if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
					t.Fatalf("create implementations = %+v, want [go-bubbletea]", createReq.Implementations)
				}
			case "close":
				var closeReq struct {
					TaskID naming.IssueID `json:"task_id"`
				}
				if err := json.Unmarshal(gotReq.Body, &closeReq); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				if closeReq.TaskID != "az-9" {
					t.Fatalf("close body = %+v, want task_id=az-9", closeReq)
				}
			}
			if !strings.Contains(output, tt.wantText) {
				t.Fatalf("output missing %q: %q", tt.wantText, output)
			}
		})
	}
}

func TestIssueCloseCommandConfirmedCleanupStopsClosesAndRemovesWorktree(t *testing.T) {
	var commands []string
	var closeForce bool
	worktreeListBody, err := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		Worktrees []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		} `json:"worktrees"`
	}{
		ProjectID: "proj",
		Worktrees: []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		}{
			{Path: "/tmp/az-9", Branch: "riordan/az-9/finish-flow", IssueID: "az-9"},
		},
	})
	if err != nil {
		t.Fatalf("marshal worktree list: %v", err)
	}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{
			ID:             "az-9",
			Title:          "Finish flow",
			Status:         domain.StatusInProgress,
			HasTmuxSession: true,
			HasWorktree:    true,
			Session:        &domain.Session{IssueID: naming.IssueID("az-9"), Worktree: "/tmp/az-9"},
		},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(ctx context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandWorktreeList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            worktreeListBody,
					}, nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-9", TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{HasChanges: false}}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "main",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Branch:   "main",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: ".",
						Branch:   "riordan/az-9/finish-flow",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				case daemonclient.CommandTaskClose:
					deadline, ok := ctx.Deadline()
					if !ok {
						t.Fatal("task close context has no deadline")
					}
					if remaining := time.Until(deadline); remaining < issueCloseCleanupTimeout-10*time.Second {
						t.Fatalf("task close timeout budget = %s, want near %s", remaining, issueCloseCleanupTimeout)
					}
					var body struct {
						ForceWorktree        bool `json:"force_worktree"`
						IntegrateBeforeClose bool `json:"integrate_before_close"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task close body: %v", err)
					}
					closeForce = body.ForceWorktree
					if !body.IntegrateBeforeClose {
						t.Fatalf("task close integrate_before_close = false, want true")
					}
					exitStatus := 0
					blocking := true
					return responseWithJSON(req, daemonclient.TaskCloseResult{
						TaskID:                 "az-9",
						Status:                 string(domain.StatusDone),
						IntegrationRequested:   true,
						Integrated:             true,
						IntegratedSourceBranch: "riordan/az-9/finish-flow",
						IntegratedTargetBranch: "main",
						SessionStopped:         true,
						WorktreeRemoved:        true,
						WorktreeForced:         body.ForceWorktree,
						Phases: []daemonclient.TaskClosePhaseTiming{
							{Name: "integrate_before_close", ElapsedMS: 123},
							{Name: "githook.commit-msg", Hook: "commit-msg", Command: "git merge --no-edit riordan/az-9/finish-flow", ElapsedMS: 42000, ExitStatus: &exitStatus, Blocking: &blocking},
							{Name: "session_cleanup", ElapsedMS: 0, Skipped: true},
						},
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueCloseCommand(deps, IssueCloseOptions{
			IssueID:       "az-9",
			ForceWorktree: true,
		})
	})

	wantCommands := []string{
		daemonclient.CommandTaskClose,
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if !closeForce {
		t.Fatal("task close force_worktree = false, want true")
	}
	if !strings.Contains(output, "Closed issue: az-9") || !strings.Contains(output, "- Integration requested") || !strings.Contains(output, "- Cleanup performed") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "- Phase timings:") ||
		!strings.Contains(output, "integrate_before_close: 123ms") ||
		!strings.Contains(output, "githook.commit-msg: 42s [hook=commit-msg command=git merge --no-edit riordan/az-9/finish-flow exit_status=0 blocking=true]") ||
		!strings.Contains(output, "session_cleanup: 0s (skipped)") {
		t.Fatalf("output = %q", output)
	}
}

func TestIssueCreateCommandAutoParentsAndInheritsImplsFromActiveIssue(t *testing.T) {
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				body := []byte{}
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-parent")
					tasks, err := marshalTaskListBody([]domain.Task{
						{
							ID:              "az-parent",
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"go-bubbletea"},
						},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					body = tasks
				case daemonclient.CommandTaskList:
					tasks, err := marshalTaskListBody([]domain.Task{
						{ID: "az-parent", Implementations: []string{"go-bubbletea"}},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					body = tasks
				case daemonclient.CommandTaskCreate:
					payload, err := json.Marshal(map[string]string{"task_id": "az-child"})
					if err != nil {
						t.Fatalf("marshal task create response: %v", err)
					}
					body = payload
				case daemonclient.CommandTaskDependencyAdd:
					body = []byte(`{}`)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		parentID := "az-parent"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:                  "Child issue",
			Type:                   domain.TypeTask,
			Priority:               domain.P2,
			AutoParentFromIssueID:  &parentID,
			AutoCreatedFromIssueID: &parentID,
		})
	})

	if len(requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskGetMany {
		t.Fatalf("requests[0].Command = %q, want %q", requests[0].Command, daemonclient.CommandTaskGetMany)
	}
	if requests[1].Command != daemonclient.CommandTaskList {
		t.Fatalf("requests[1].Command = %q, want %q", requests[1].Command, daemonclient.CommandTaskList)
	}
	if requests[2].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("requests[2].Command = %q, want %q", requests[2].Command, daemonclient.CommandTaskCreate)
	}
	if requests[3].Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("requests[3].Command = %q, want %q", requests[3].Command, daemonclient.CommandTaskDependencyAdd)
	}

	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[2].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID == nil || *createReq.ParentID != "az-parent" {
		t.Fatalf("create parent = %+v, want az-parent", createReq.ParentID)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("create implementations = %+v, want [go-bubbletea]", createReq.Implementations)
	}
	if createReq.Title != "Child issue" || createReq.Type != domain.TypeTask {
		t.Fatalf("create body = %+v", createReq)
	}
	if createReq.Priority != domain.P2 {
		t.Fatalf("create priority = %s, want P2", createReq.Priority.String())
	}
	var depReq daemonclient.TaskDependencyParams
	if err := json.Unmarshal(requests[3].Body, &depReq); err != nil {
		t.Fatalf("unmarshal dependency body: %v", err)
	}
	if depReq.TaskID != "az-child" || depReq.DependsOnID != "az-parent" || depReq.Type != string(domain.DependencyCreatedIn) {
		t.Fatalf("dependency body = %+v, want az-child created-in az-parent", depReq)
	}
	if !strings.Contains(output, "Created issue: az-child (parent: az-parent, auto-parent from AZEDARACH_ISSUE_ID) [created-from: az-parent]") {
		t.Fatalf("output missing auto-parent/provenance message: %q", output)
	}
}

func TestIssueCreateCommandUsesInheritedImplsWhenTaskSnapshotTimesOut(t *testing.T) {
	var requests []protocol.RequestEnvelope
	var createReq daemonclient.TaskCreateParams
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{{
							ID:              "az-parent",
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"default", "go-bubbletea"},
						}},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{}, &daemonclient.ReadWaitTimeoutError{
						Mode:   daemonclient.ReadWaitModeDefault,
						Budget: 2 * time.Second,
						Hint:   "Task snapshot read timed out after 2s; keeping current local view",
						Err:    context.DeadlineExceeded,
					}
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createReq); err != nil {
						t.Fatalf("unmarshal create request: %v", err)
					}
					return responseWithJSON(req, map[string]string{"task_id": "az-child"}), nil
				case daemonclient.CommandTaskDependencyAdd:
					return responseWithJSON(req, map[string]any{}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}
	deps.DaemonClient.WithReconnectPolicy(reconnect.Policy{MaxAttempts: 1})

	parentID := "az-parent"
	_ = captureStdout(t, func() error {
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:                  "Child issue",
			Type:                   domain.TypeTask,
			Priority:               domain.P2,
			AutoParentFromIssueID:  &parentID,
			AutoCreatedFromIssueID: &parentID,
		})
	})
	commands := commandNames(requests)
	if len(commands) < 4 || commands[0] != daemonclient.CommandTaskGetMany || commands[1] != daemonclient.CommandTaskList {
		t.Fatalf("commands = %+v, want parent lookup followed by implementation validation", commands)
	}
	if commands[len(commands)-2] != daemonclient.CommandTaskCreate || commands[len(commands)-1] != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("commands = %+v, want create and created-in edge after snapshot timeout fallback", commands)
	}
	if createReq.ParentID == nil || *createReq.ParentID != "az-parent" {
		t.Fatalf("create parent = %+v, want az-parent", createReq.ParentID)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"default", "go-bubbletea"}) {
		t.Fatalf("create implementations = %+v, want inherited implementations", createReq.Implementations)
	}
}

func TestIssueCreateCommandOmitsImplWhenTaskSnapshotTimesOutWithoutInferenceSignal(t *testing.T) {
	var requests []protocol.RequestEnvelope
	var createReq daemonclient.TaskCreateParams
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "proj",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{{
							ID:       "az-parent",
							Title:    "Parent",
							Status:   domain.StatusInProgress,
							Priority: domain.P1,
							Type:     domain.TypeTask,
						}},
					}), nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{}, &daemonclient.ReadWaitTimeoutError{
						Mode:   daemonclient.ReadWaitModeDefault,
						Budget: 2 * time.Second,
						Hint:   "Task snapshot read timed out after 2s; keeping current local view",
						Err:    context.DeadlineExceeded,
					}
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createReq); err != nil {
						t.Fatalf("unmarshal create request: %v", err)
					}
					return responseWithJSON(req, map[string]string{"task_id": "az-child"}), nil
				case daemonclient.CommandTaskDependencyAdd:
					return responseWithJSON(req, map[string]any{}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID("proj"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}
	deps.DaemonClient.WithReconnectPolicy(reconnect.Policy{MaxAttempts: 1})

	parentID := "az-parent"
	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:                  "Child issue",
		Type:                   domain.TypeTask,
		Priority:               domain.P2,
		AutoParentFromIssueID:  &parentID,
		AutoCreatedFromIssueID: &parentID,
	})
	if err != nil {
		t.Fatalf("IssueCreateCommand() error = %v", err)
	}
	commands := commandNames(requests)
	if len(commands) < 4 || commands[0] != daemonclient.CommandTaskGetMany || commands[1] != daemonclient.CommandTaskList {
		t.Fatalf("commands = %+v, want parent lookup followed by implementation validation", commands)
	}
	if commands[len(commands)-2] != daemonclient.CommandTaskCreate || commands[len(commands)-1] != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("commands = %+v, want create and created-in edge after snapshot timeout fallback", commands)
	}
	if createReq.ParentID == nil || *createReq.ParentID != "az-parent" {
		t.Fatalf("create parent = %+v, want az-parent", createReq.ParentID)
	}
	if len(createReq.Implementations) != 0 {
		t.Fatalf("create implementations = %+v, want omitted after timeout without inference signal", createReq.Implementations)
	}
}

func TestIssueCreateCommandAutoParentsFromTmuxSessionWhenEnvMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	previousTmuxPaneSessionName := tmuxPaneSessionName
	tmuxPaneSessionName = func(context.Context) (string, error) {
		return "pr-az-parent", nil
	}
	t.Cleanup(func() {
		tmuxPaneSessionName = previousTmuxPaneSessionName
	})

	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				body := []byte{}
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-parent")
					tasks, err := marshalTaskListBody([]domain.Task{{
						ID:              "az-parent",
						Title:           "Parent",
						Status:          domain.StatusInProgress,
						Priority:        domain.P1,
						Type:            domain.TypeTask,
						Implementations: []string{"go-bubbletea"},
					}})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					body = tasks
				case daemonclient.CommandTaskList:
					tasks, err := marshalTaskListBody([]domain.Task{
						{ID: "az-parent", Implementations: []string{"go-bubbletea"}},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					body = tasks
				case daemonclient.CommandTaskCreate:
					payload, err := json.Marshal(map[string]string{"task_id": "az-child"})
					if err != nil {
						t.Fatalf("marshal task create response: %v", err)
					}
					body = payload
				case daemonclient.CommandTaskDependencyAdd:
					body = []byte(`{}`)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   "/tmp/proj",
	}

	output := captureStdout(t, func() error {
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:    "Child issue",
			Type:     domain.TypeTask,
			Priority: domain.P2,
		})
	})

	if len(requests) != 5 {
		t.Fatalf("request count = %d, want 5", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskGetMany || requests[1].Command != daemonclient.CommandTaskGetMany || requests[2].Command != daemonclient.CommandTaskList {
		t.Fatalf("first requests = %s, %s, %s; want metadata confirmation, parent resolution, then implementation validation", requests[0].Command, requests[1].Command, requests[2].Command)
	}
	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[3].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID == nil || *createReq.ParentID != "az-parent" {
		t.Fatalf("create parent = %+v, want az-parent", createReq.ParentID)
	}
	if !strings.Contains(output, "Created issue: az-child (parent: az-parent, auto-parent from AZEDARACH_ISSUE_ID) [created-from: az-parent]") {
		t.Fatalf("output missing tmux auto-parent/provenance message: %q", output)
	}
}

func TestIssueCreateCommandDeferredIgnoresAutoParentFromIssueID(t *testing.T) {
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				if req.Command == daemonclient.CommandTaskList {
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				}
				if req.Command == daemonclient.CommandTaskDependencyAdd {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            []byte(`{}`),
					}, nil
				}
				payload, err := json.Marshal(map[string]string{"task_id": "az-child"})
				if err != nil {
					t.Fatalf("marshal task create response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		parentID := "az-parent"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:                  "Child issue",
			Type:                   domain.TypeTask,
			Priority:               domain.P4,
			Deferred:               true,
			AutoParentFromIssueID:  &parentID,
			AutoCreatedFromIssueID: &parentID,
			Implementations:        []string{"default"},
		})
	})

	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskList {
		t.Fatalf("requests[0].Command = %q, want %q", requests[0].Command, daemonclient.CommandTaskList)
	}
	if requests[1].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("requests[1].Command = %q, want %q", requests[1].Command, daemonclient.CommandTaskCreate)
	}
	if requests[2].Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("requests[2].Command = %q, want %q", requests[2].Command, daemonclient.CommandTaskDependencyAdd)
	}

	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[1].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID != nil {
		t.Fatalf("create parent = %+v, want nil", createReq.ParentID)
	}
	if createReq.Lifecycle != domain.IssueWorkflowBacklog {
		t.Fatalf("create lifecycle = %q, want backlog", createReq.Lifecycle)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"default"}) {
		t.Fatalf("create implementations = %+v, want [default]", createReq.Implementations)
	}
	var depReq daemonclient.TaskDependencyParams
	if err := json.Unmarshal(requests[2].Body, &depReq); err != nil {
		t.Fatalf("unmarshal dependency body: %v", err)
	}
	if depReq.TaskID != "az-child" || depReq.DependsOnID != "az-parent" || depReq.Type != string(domain.DependencyCreatedIn) {
		t.Fatalf("dependency body = %+v, want az-child created-in az-parent", depReq)
	}
	if !strings.Contains(output, "Created issue: az-child [created-from: az-parent] [deferred: standalone later work, not auto-parented]") {
		t.Fatalf("output missing deferred/provenance message: %q", output)
	}
}

func TestIssueCreateCommandCrossProjectSkipsImplicitAutoParentAndCreatedFrom(t *testing.T) {
	routes := registerCLIProjects(t, "chefy", "azedarach")
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				if req.Command == daemonclient.CommandTaskList {
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				}
				if req.Command == daemonclient.CommandTaskDependencyAdd {
					t.Fatalf("unexpected %s request for cross-project create", daemonclient.CommandTaskDependencyAdd)
				}
				payload, err := json.Marshal(map[string]string{"task_id": "cnd"})
				if err != nil {
					t.Fatalf("marshal task create response: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            payload,
				}, nil
			},
		}).WithProjectID(routes["chefy"]),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: routes["chefy"],
	}

	output := captureStdout(t, func() error {
		activeID := "eik"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Project:                "azedarach",
			Title:                  "Prevent worker self-delivery",
			Type:                   domain.TypeBug,
			Priority:               domain.P2,
			AutoParentFromIssueID:  &activeID,
			AutoCreatedFromIssueID: &activeID,
			Implementations:        []string{"default"},
		})
	})

	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].Command != daemonclient.CommandTaskList {
		t.Fatalf("requests[0].Command = %q, want %q", requests[0].Command, daemonclient.CommandTaskList)
	}
	if requests[1].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("requests[1].Command = %q, want %q", requests[1].Command, daemonclient.CommandTaskCreate)
	}
	if requests[1].Meta.ProjectID.String() != routes["azedarach"] {
		t.Fatalf("request project = %q, want %s", requests[1].Meta.ProjectID, routes["azedarach"])
	}
	var createReq daemonclient.TaskCreateParams
	if err := json.Unmarshal(requests[1].Body, &createReq); err != nil {
		t.Fatalf("unmarshal create body: %v", err)
	}
	if createReq.ParentID != nil {
		t.Fatalf("create parent = %+v, want nil", createReq.ParentID)
	}
	if !strings.Contains(output, "Created issue: "+routes["azedarach"]+":cnd") {
		t.Fatalf("output missing project-qualified created issue: %q", output)
	}
	if strings.Contains(output, "created-from") || strings.Contains(output, "parent:") {
		t.Fatalf("output should not mention implicit parent/provenance for cross-project create: %q", output)
	}
}

func TestIssueCreateCommandCrossProjectKeepsExplicitParent(t *testing.T) {
	routes := registerCLIProjects(t, "chefy", "azedarach")
	var requests []protocol.RequestEnvelope
	var createReq daemonclient.TaskCreateParams
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-parent")
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 1,
						ProjectID:        "azedarach",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks: []domain.Task{{
							ID:              "az-parent",
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"default"},
						}},
					}), nil
				case daemonclient.CommandTaskList:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 2,
						ProjectID:        "azedarach",
						LastCheckedAt:    time.Now().UTC(),
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{{ID: "az-parent", Implementations: []string{"default"}}},
					}), nil
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createReq); err != nil {
						t.Fatalf("unmarshal create request: %v", err)
					}
					return responseWithJSON(req, map[string]string{"task_id": "az-child"}), nil
				case daemonclient.CommandTaskDependencyAdd:
					return responseWithJSON(req, map[string]any{}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}).WithProjectID(routes["chefy"]),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: routes["chefy"],
	}

	parentID := "az-parent"
	output := captureStdout(t, func() error {
		return IssueCreateCommand(deps, IssueCreateOptions{
			Project:                "azedarach",
			Title:                  "Explicitly parented child",
			Type:                   domain.TypeTask,
			Priority:               domain.P2,
			AutoParentFromIssueID:  &parentID,
			AutoCreatedFromIssueID: &parentID,
			ExplicitParent:         true,
		})
	})

	if len(requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(requests))
	}
	if requests[0].Meta.ProjectID.String() != routes["azedarach"] || requests[2].Meta.ProjectID.String() != routes["azedarach"] {
		t.Fatalf("request projects = %q, %q; want %s", requests[0].Meta.ProjectID, requests[2].Meta.ProjectID, routes["azedarach"])
	}
	if createReq.ParentID == nil || *createReq.ParentID != "az-parent" {
		t.Fatalf("create parent = %+v, want az-parent", createReq.ParentID)
	}
	if !strings.Contains(output, "Created issue: "+routes["azedarach"]+":az-child (parent: az-parent, explicit --parent) [created-from: az-parent]") {
		t.Fatalf("output missing explicit parent message: %q", output)
	}
}

func TestIssueCreateCommandReportsPartialSuccessWhenCreatedFromEdgeFails(t *testing.T) {
	var requests []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				body := []byte{}
				switch req.Command {
				case daemonclient.CommandTaskList:
					payload, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					body = payload
				case daemonclient.CommandTaskCreate:
					payload, err := json.Marshal(map[string]string{"task_id": "az-child"})
					if err != nil {
						t.Fatalf("marshal task create response: %v", err)
					}
					body = payload
				case daemonclient.CommandTaskDependencyAdd:
					return protocol.ResponseEnvelope{}, fmt.Errorf("not found")
				default:
					t.Fatalf("unexpected command %s", req.Command)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}).WithProjectID("azedarach"),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "azedarach",
	}

	output, err := captureStdoutAllowError(t, func() error {
		activeID := "az-parent"
		return IssueCreateCommand(deps, IssueCreateOptions{
			Title:                  "Child issue",
			Type:                   domain.TypeTask,
			Priority:               domain.P2,
			Deferred:               true,
			AutoCreatedFromIssueID: &activeID,
			Implementations:        []string{"default"},
		})
	})

	if output != "" {
		t.Fatalf("stdout = %q, want empty on text partial error", output)
	}
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	var partial issueCreatePartialError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want issueCreatePartialError", err, err)
	}
	if partial.Result.IssueID != "az-child" || partial.Result.ProjectID != "azedarach" || partial.Result.CreatedFromID != "az-parent" {
		t.Fatalf("partial result = %+v", partial.Result)
	}
	if !strings.Contains(err.Error(), "issue creation partially succeeded: created azedarach:az-child") {
		t.Fatalf("error missing created issue: %v", err)
	}
	if !strings.Contains(err.Error(), "azedarach:az-child -> azedarach:az-parent") {
		t.Fatalf("error missing qualified edge ids: %v", err)
	}
}

func TestIssueSplitCommandCreatesChildAndStartsOrchestratedSession(t *testing.T) {
	root := naming.IssueID("az-parent")
	child := naming.IssueID("az-child")
	var requests []protocol.RequestEnvelope
	var createReq daemonclient.TaskCreateParams
	var intentReq protocol.OrchestrationIntentRequest
	taskListCalls := 0
	deps := &Dependencies{
		Config:    config.DefaultConfig(),
		RepoDir:   "/repo",
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: protocol.DefaultProjectID,
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			passOrchestrationIntent: true,
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				requests = append(requests, req)
				switch req.Command {
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: root.String(),
						Runnable:    []string{child.String()},
						Blocked:     map[string]string{},
					}), nil
				case protocol.CommandOrchestrationIntent:
					if err := json.Unmarshal(req.Body, &intentReq); err != nil {
						t.Fatalf("decode orchestration intent: %v", err)
					}
					return responseWithJSON(req, protocol.OrchestrationIntentResult{
						Scope:     intentReq.Scope,
						Kind:      protocol.OrchestrationIntentStart,
						IntentKey: intentReq.IntentKey,
						Requested: []string{child.String()},
						Started:   []string{child.String()},
						Launched: []protocol.OrchestrationLaunch{{
							IssueID:        child.String(),
							SessionID:      child.String(),
							OperationID:    "op-split",
							OperationState: string(protocol.OperationStateQueued),
						}},
					}), nil
				case daemonclient.CommandTaskList:
					taskListCalls++
					tasks := []domain.Task{{
						ID:              root,
						Title:           "Parent",
						Status:          domain.StatusInProgress,
						Priority:        domain.P1,
						Type:            domain.TypeTask,
						Implementations: []string{"go-bubbletea"},
					}}
					if taskListCalls > 1 {
						tasks = append(tasks, domain.Task{
							ID:       child,
							Title:    "Child work",
							Status:   domain.StatusOpen,
							Priority: domain.P2,
							Type:     domain.TypeTask,
							ParentID: &root,
						})
					}
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal task list response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandTaskGetMany:
					var getManyReq daemonclient.TaskIDsRequest
					if err := json.Unmarshal(req.Body, &getManyReq); err != nil {
						t.Fatalf("decode task.get_many request: %v", err)
					}
					if len(getManyReq.TaskIDs) != 1 || !getManyReq.IncludeAncestors || !getManyReq.ExcludeDependents || !getManyReq.MetadataOnly {
						t.Fatalf("task.get_many request = %+v, want one metadata-only issue with ancestors/no dependents", getManyReq)
					}
					var tasks []domain.Task
					switch getManyReq.TaskIDs[0] {
					case root:
						tasks = []domain.Task{{
							ID:              root,
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"go-bubbletea"},
						}}
					case child:
						tasks = []domain.Task{{
							ID:       child,
							Title:    "Child work",
							Status:   domain.StatusOpen,
							Priority: domain.P2,
							Type:     domain.TypeTask,
							ParentID: &root,
						}, {
							ID:              root,
							Title:           "Parent",
							Status:          domain.StatusInProgress,
							Priority:        domain.P1,
							Type:            domain.TypeTask,
							Implementations: []string{"go-bubbletea"},
						}}
					default:
						t.Fatalf("unexpected task.get_many id: %+v", getManyReq.TaskIDs)
					}
					body, err := marshalTaskListBody(tasks)
					if err != nil {
						t.Fatalf("marshal task get-many response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, Body: body}, nil
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createReq); err != nil {
						t.Fatalf("decode create request: %v", err)
					}
					return responseWithJSON(req, map[string]string{"task_id": child.String()}), nil
				case daemonclient.CommandTaskDependencyAdd:
					var depReq daemonclient.TaskDependencyParams
					if err := json.Unmarshal(req.Body, &depReq); err != nil {
						t.Fatalf("decode dependency request: %v", err)
					}
					if depReq.TaskID != child || depReq.DependsOnID != root || depReq.Type != string(domain.DependencyCreatedIn) {
						t.Fatalf("dependency request = %+v, want child created-in root", depReq)
					}
					return responseWithJSON(req, map[string]any{}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{}}), nil
				case daemonclient.CommandWorktreeList:
					return responseWithJSON(req, map[string]any{
						"project_id": protocol.DefaultProjectID,
						"worktrees": []map[string]string{
							{"issue_id": root.String(), "path": "/repo-az-parent", "branch": "user/az-parent/parent-work"},
							{"issue_id": child.String(), "path": "/repo-az-child", "branch": "user/az-child/child-work"},
						},
					}), nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{
						IssueID:        child.String(),
						TargetID:       root.String(),
						Branch:         "user/az-parent/parent-work",
						WorktreePath:   "/repo-az-parent",
						BranchAttached: true,
						AncestorChain:  []string{root.String()},
					}), nil
				case protocol.CommandOperationGet:
					return responseWithJSON(req, protocol.OperationGetResponseBody{
						Operation: protocol.OperationRecord{
							OperationID: "op-split",
							ProjectID:   protocol.DefaultProjectID,
							Kind:        commandSessionStart,
							IssueID:     child,
							State:       protocol.OperationStateDone,
						},
					}), nil
				case protocol.CommandMailSend:
					return responseWithJSON(req, protocol.MailEvent{Seq: 1, ParentIssue: root.String(), IssueID: child, Type: "session-started"}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
	}

	output := captureStdout(t, func() error {
		return IssueSplitCommand(deps, IssueSplitOptions{
			ParentIssueID: root.String(),
			Title:         "Child work",
			Type:          domain.TypeTask,
			Priority:      domain.P2,
			JSON:          true,
		})
	})

	var result issueSplitResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result.ChildIssueID != child.String() || len(result.Start.Started) != 1 || result.Start.Started[0] != child.String() {
		t.Fatalf("result = %+v", result)
	}
	if createReq.ParentID == nil || *createReq.ParentID != root {
		t.Fatalf("create parent = %+v, want %s", createReq.ParentID, root)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("create implementations = %+v, want inherited parent impl", createReq.Implementations)
	}
	if intentReq.Kind != protocol.OrchestrationIntentStart || !reflect.DeepEqual(intentReq.IssueIDs, []string{child.String()}) {
		t.Fatalf("intent = %+v", intentReq)
	}
	if intentReq.BaseBranch != "user/az-parent/parent-work" {
		t.Fatalf("intent base_branch = %q, want parent worktree branch", intentReq.BaseBranch)
	}
	if !strings.Contains(result.Advice.IntegrateCommand, child.String()) {
		t.Fatalf("advice = %+v, want child integration command", result.Advice)
	}
	if !strings.Contains(result.Advice.MergeCommand, child.String()) || !strings.Contains(result.Advice.Summary, "not merged at creation") {
		t.Fatalf("advice = %+v, want explicit review/close guidance", result.Advice)
	}
	commands := commandNames(requests)
	if !containsString(commands, protocol.CommandOrchestrationIntent) || containsString(commands, protocol.CommandOperationSubmit) || containsString(commands, protocol.CommandMailSend) {
		t.Fatalf("commands = %+v, want daemon orchestration intent without client-side operation submit or immediate mail send", commands)
	}

	textOutput := captureStdout(t, func() error {
		return IssueSplitCommand(deps, IssueSplitOptions{
			ParentIssueID: root.String(),
			Title:         "Child work",
			Type:          domain.TypeTask,
			Priority:      domain.P2,
		})
	})
	if !strings.Contains(textOutput, "`az ticket close` owns") || !strings.Contains(textOutput, "az ticket close --id "+child.String()) {
		t.Fatalf("split output missing canonical ticket close guidance: %s", textOutput)
	}
	if strings.Contains(textOutput, "az issue close") {
		t.Fatalf("split output contains legacy issue close guidance: %s", textOutput)
	}
}

func TestIssueCreateCommandAutoDefaultsImplWhenSingleConfigured(t *testing.T) {
	var createReq daemonclient.TaskCreateParams
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"go-bubbletea"}},
					})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskCreate:
					if err := json.Unmarshal(req.Body, &createReq); err != nil {
						t.Fatalf("unmarshal create request: %v", err)
					}
					body, err := json.Marshal(map[string]string{"task_id": "az-55"})
					if err != nil {
						t.Fatalf("marshal create response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:       "Auto impl",
		Description: "details",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	if err != nil {
		t.Fatalf("IssueCreateCommand() error = %v", err)
	}
	if !reflect.DeepEqual(createReq.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("create implementations = %+v, want [go-bubbletea]", createReq.Implementations)
	}
}

func TestIssueCreateCommandAutoParentEmptyImplFallsBackToGlobalInference(t *testing.T) {
	tests := []struct {
		name         string
		tasks        []domain.Task
		wantCreate   bool
		errSubstring string
	}{
		{
			name:       "default only",
			tasks:      []domain.Task{{ID: "az-1", Implementations: []string{"default"}}},
			wantCreate: true,
		},
		{
			name: "multiple configured implementations",
			tasks: []domain.Task{
				{ID: "az-1", Implementations: []string{"default"}},
				{ID: "az-2", Implementations: []string{"go-bubbletea"}},
			},
			errSubstring: "missing required flag: --impl (implementation is ambiguous; valid --impl values: default, go-bubbletea)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var createReq daemonclient.TaskCreateParams
			createCalled := false
			parentID := "az-parent"
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						switch req.Command {
						case daemonclient.CommandTaskGetMany:
							assertMetadataOnlyTaskGetManyRequest(t, req, parentID)
							body, err := marshalTaskListBody([]domain.Task{
								{
									ID:       "az-parent",
									Title:    "Implicit default parent",
									Status:   domain.StatusInProgress,
									Priority: domain.P2,
									Type:     domain.TypeTask,
									Implementations: []string{
										" ",
									},
								},
							})
							if err != nil {
								t.Fatalf("marshal parent response: %v", err)
							}
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								OK:              true,
								CompletedAt:     req.SentAt,
								Revision:        1,
								Body:            body,
							}, nil
						case daemonclient.CommandTaskList:
							body, err := marshalTaskListBody(tt.tasks)
							if err != nil {
								t.Fatalf("marshal list response: %v", err)
							}
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								OK:              true,
								CompletedAt:     req.SentAt,
								Revision:        1,
								Body:            body,
							}, nil
						case daemonclient.CommandTaskCreate:
							createCalled = true
							if err := json.Unmarshal(req.Body, &createReq); err != nil {
								t.Fatalf("unmarshal create request: %v", err)
							}
							body, err := json.Marshal(map[string]string{"task_id": "az-child"})
							if err != nil {
								t.Fatalf("marshal create response: %v", err)
							}
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								OK:              true,
								CompletedAt:     req.SentAt,
								Body:            body,
							}, nil
						case daemonclient.CommandTaskDependencyAdd:
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								OK:              true,
								CompletedAt:     req.SentAt,
								Body:            []byte(`{}`),
							}, nil
						default:
							t.Fatalf("unexpected command: %s", req.Command)
						}
						return protocol.ResponseEnvelope{}, nil
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
			}

			err := IssueCreateCommand(deps, IssueCreateOptions{
				Title:                  "Child issue",
				Type:                   domain.TypeTask,
				Priority:               domain.P2,
				AutoParentFromIssueID:  &parentID,
				AutoCreatedFromIssueID: &parentID,
			})
			if tt.errSubstring != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSubstring) {
					t.Fatalf("IssueCreateCommand() error = %v, want substring %q", err, tt.errSubstring)
				}
				if createCalled {
					t.Fatal("task.create should not be called when implementation selection is ambiguous")
				}
				return
			}
			if err != nil {
				t.Fatalf("IssueCreateCommand() error = %v", err)
			}
			if !tt.wantCreate || !createCalled {
				t.Fatalf("createCalled = %v, want %v", createCalled, tt.wantCreate)
			}
			if createReq.ParentID == nil || createReq.ParentID.String() != parentID {
				t.Fatalf("create parent = %+v, want %s", createReq.ParentID, parentID)
			}
			if !reflect.DeepEqual(createReq.Implementations, []string{"default"}) {
				t.Fatalf("create implementations = %+v, want [default]", createReq.Implementations)
			}
		})
	}
}

func TestIssueCreateCommandRejectsUnknownExplicitImplementation(t *testing.T) {
	createCalled := false
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskCreate:
					createCalled = true
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:           "Bad impl",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Implementations: []string{"cif"},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown implementation "cif"`) || !strings.Contains(err.Error(), "valid --impl values: default") {
		t.Fatalf("IssueCreateCommand() error = %v, want unknown implementation", err)
	}
	if createCalled {
		t.Fatal("task.create should not be called for an unknown implementation")
	}
}

func TestIssueCreateCommandRequiresImplWhenMultipleConfigured(t *testing.T) {
	createCalled := false
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"go-bubbletea"}},
						{ID: "az-2", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskCreate:
					createCalled = true
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:       "Needs impl",
		Description: "details",
		Type:        domain.TypeTask,
		Priority:    domain.P2,
	})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --impl (implementation is ambiguous; valid --impl values: default, go-bubbletea)") {
		t.Fatalf("expected multi-implementation error, got %v", err)
	}
	if createCalled {
		t.Fatalf("task.create should not be called when implementation selection is ambiguous")
	}
}

func TestIssueCreateCommandSuggestsImplWhenInferenceUnavailable(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{}, errors.New("transport unavailable")
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:    "Needs impl",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err == nil || !strings.Contains(err.Error(), "missing required flag: --impl (implementation inference unavailable:") || !strings.Contains(err.Error(), "Specify --impl <implementation>") {
		t.Fatalf("expected actionable impl inference failure, got %v", err)
	}
}

func TestIssueCheckDoctorAndDeleteCommandsUseDaemonTaskCommands(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotDeleteReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					var getBody daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &getBody); err != nil {
						t.Fatalf("unmarshal task get body: %v", err)
					}
					if getBody.TaskID != "az-1" {
						t.Fatalf("task_id = %q, want az-1", getBody.TaskID)
					}
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Check target",
							Description: "Desc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task get: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Check target",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusOpen,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskDelete:
					gotDeleteReq = req
					return responseWithJSON(req, daemonclient.TaskDeleteResult{
						TaskID:   "az-1",
						Deleted:  true,
						Revision: 3,
					}), nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	checkOut := captureStdout(t, func() error {
		return IssueCheckCommand(deps, IssueCheckOptions{IssueID: "az-1"})
	})
	if !strings.Contains(checkOut, "ID: az-1") {
		t.Fatalf("check output = %q", checkOut)
	}

	doctorOut := captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(doctorOut, "Doctor: OK az-1") {
		t.Fatalf("doctor output = %q", doctorOut)
	}
	cfg := config.DefaultConfig()
	cfg.IssueResources.PrepareCommands = []string{"just prepare"}
	cfg.IssueResources.ReconcileCommand = "just reconcile"
	deps.Config = cfg
	doctorOut = captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(doctorOut, "Doctor: WARN az-1") ||
		!strings.Contains(doctorOut, "issueResources config mixes reconcileCommand") {
		t.Fatalf("doctor mixed lifecycle output = %q", doctorOut)
	}

	deleteOut := captureStdout(t, func() error {
		return IssueDeleteCommand(deps, IssueDeleteOptions{
			IssueID: "az-1",
			Confirm: true,
		})
	})
	if gotDeleteReq.Command != daemonclient.CommandTaskDelete {
		t.Fatalf("delete command = %q, want %q", gotDeleteReq.Command, daemonclient.CommandTaskDelete)
	}
	if !strings.Contains(deleteOut, "Deleted issue: az-1") {
		t.Fatalf("delete output = %q", deleteOut)
	}
}

func TestIssueDoctorWarnsOnRuntimeSessionMetadata(t *testing.T) {
	now := time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Doctor runtime target",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusInReview,
							CreatedAt: now,
							UpdatedAt: now,
							Session: &domain.Session{
								IssueID:        "az-1",
								State:          domain.SessionBusy,
								Activity:       "busy",
								ActivitySource: "hooks",
								UpdatedAt:      now,
							},
							HasTmuxSession: true,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return responseWithBody(req, body), nil
				case daemonclient.CommandSessionStatus:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              false,
						CompletedAt:     req.SentAt,
						Error: &protocol.ErrorEnvelope{
							Code:    protocol.ErrorCodeInvalidRequest,
							Message: "no active session found for issue: az-1",
						},
					}, nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	doctorOut := captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(doctorOut, "Doctor: WARN az-1") ||
		!strings.Contains(doctorOut, "stale runtime session metadata remains: state=busy activity=busy source=hooks; live session status reports no active session") ||
		!strings.Contains(doctorOut, "az orchestrate close-session --issue az-1") {
		t.Fatalf("doctor output = %q", doctorOut)
	}
}

func TestIssueDoctorCommandEmitsDiagnosticPhaseSpans(t *testing.T) {
	now := time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	traceCtx, recorder, parentSpanID, cleanup := newCommandTraceContext(t)
	defer cleanup()
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Doctor trace target",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusOpen,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return responseWithBody(req, body), nil
				case daemonclient.CommandTaskSQLiteWAL:
					return responseWithJSON(req, protocol.TaskSQLiteWALResponse{
						DBPath:              "/repo/.azedarach/issues.db",
						WALPath:             "/repo/.azedarach/issues.db-wal",
						WALBytes:            12,
						CheckpointThreshold: 1048576,
						LargeThreshold:      67108864,
					}), nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		TraceContext: traceCtx,
	}

	output := captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(output, "Doctor: OK az-1") {
		t.Fatalf("doctor output = %q", output)
	}

	doctorSpan := findCommandSpan(t, recorder, "cli.issue_doctor")
	if doctorSpan.Parent().SpanID() != parentSpanID {
		t.Fatalf("doctor parent span = %s, want %s", doctorSpan.Parent().SpanID(), parentSpanID)
	}
	doctorAttrs := commandSpanAttrs(doctorSpan)
	if doctorAttrs.strings["issue_id"] != "az-1" {
		t.Fatalf("doctor issue_id attr = %q, want az-1", doctorAttrs.strings["issue_id"])
	}

	for _, name := range []string{
		"cli.issue_doctor.load_issue",
		"cli.issue_doctor.local_checks",
		"cli.issue_doctor.runtime_diagnostics",
		"cli.issue_doctor.sqlite_wal",
		"cli.issue_doctor.render",
	} {
		span := findCommandSpan(t, recorder, name)
		if span.Parent().SpanID() != doctorSpan.SpanContext().SpanID() {
			t.Fatalf("%s parent span = %s, want doctor span %s", name, span.Parent().SpanID(), doctorSpan.SpanContext().SpanID())
		}
		attrs := commandSpanAttrs(span)
		if attrs.strings["issue_id"] != "az-1" {
			t.Fatalf("%s issue_id attr = %q, want az-1", name, attrs.strings["issue_id"])
		}
	}

	loadAttrs := commandSpanAttrs(findCommandSpan(t, recorder, "cli.issue_doctor.load_issue"))
	if loadAttrs.strings["outcome"] != "found" {
		t.Fatalf("load outcome attr = %q, want found", loadAttrs.strings["outcome"])
	}
	renderAttrs := commandSpanAttrs(findCommandSpan(t, recorder, "cli.issue_doctor.render"))
	if renderAttrs.strings["outcome"] != "ok" {
		t.Fatalf("render outcome attr = %q, want ok", renderAttrs.strings["outcome"])
	}
	if renderAttrs.bools["json"] {
		t.Fatalf("render json attr = true, want false")
	}
}

func TestIssueDoctorCommandMarksWALPhaseSpanWhenDiagnosticsUnavailable(t *testing.T) {
	now := time.Date(2026, time.April, 2, 11, 2, 0, 0, time.UTC)
	traceCtx, recorder, _, cleanup := newCommandTraceContext(t)
	defer cleanup()
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Doctor WAL trace target",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusOpen,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return responseWithBody(req, body), nil
				case daemonclient.CommandTaskSQLiteWAL:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              false,
						CompletedAt:     req.SentAt,
						Error: &protocol.ErrorEnvelope{
							Code:    protocol.ErrorCodeUnsupportedCommand,
							Message: "unsupported command",
						},
					}, nil
				default:
					return protocol.ResponseEnvelope{}, fmt.Errorf("unexpected command: %s", req.Command)
				}
			},
		}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		TraceContext: traceCtx,
	}

	output := captureStdout(t, func() error {
		return IssueDoctorCommand(deps, IssueDoctorOptions{IssueID: "az-1"})
	})
	if !strings.Contains(output, "Doctor: WARN az-1") ||
		!strings.Contains(output, "sqlite wal diagnostics unavailable") {
		t.Fatalf("doctor output = %q", output)
	}

	doctorSpan := findCommandSpan(t, recorder, "cli.issue_doctor")
	if got := doctorSpan.Status().Code; got == codes.Error {
		t.Fatalf("doctor span status = Error, want non-fatal WAL diagnostic failure")
	}
	walSpan := findCommandSpan(t, recorder, "cli.issue_doctor.sqlite_wal")
	if got := walSpan.Status().Code; got != codes.Error {
		t.Fatalf("wal span status = %v, want Error", got)
	}
	walAttrs := commandSpanAttrs(walSpan)
	if !walAttrs.bools["error"] {
		t.Fatalf("wal span missing error=true attr")
	}
	if walAttrs.strings["outcome"] != "error" {
		t.Fatalf("wal outcome attr = %q, want error", walAttrs.strings["outcome"])
	}
}

func TestIssueDeleteCommandBlocksWhenRuntimeAttachmentsPresent(t *testing.T) {
	deleteCalled := false
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskDelete:
					deleteCalled = true
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              false,
						CompletedAt:     req.SentAt,
						Error: &protocol.ErrorEnvelope{
							Code:    protocol.ErrorCodeInternal,
							Message: "cannot delete issue az-1: runtime metadata fields still present (session, worktree); repair with az issue delete az-1 --confirm --cleanup --remove-worktree --force-worktree, or rerun with stop_session remove_worktree",
						},
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueDeleteCommand(deps, IssueDeleteOptions{
		IssueID: "az-1",
		Confirm: true,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot delete issue az-1: runtime metadata fields still present (session, worktree)") ||
		!strings.Contains(err.Error(), "az issue delete az-1 --confirm --cleanup --remove-worktree --force-worktree") {
		t.Fatalf("IssueDeleteCommand() error = %v", err)
	}
	if !deleteCalled {
		t.Fatal("IssueDeleteCommand() did not call daemon task.delete")
	}
}

func TestIssueDeleteCommandCleansRuntimeAttachmentsBeforeDelete(t *testing.T) {
	commands := []string{}
	var deleteCleanup bool
	var deleteStopSession bool
	var deleteRemoveWorktree bool
	var deleteForceWorktree bool
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskDelete:
					var body struct {
						Cleanup        bool `json:"cleanup"`
						StopSession    bool `json:"stop_session"`
						RemoveWorktree bool `json:"remove_worktree"`
						ForceWorktree  bool `json:"force_worktree"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task delete body: %v", err)
					}
					deleteCleanup = body.Cleanup
					deleteStopSession = body.StopSession
					deleteRemoveWorktree = body.RemoveWorktree
					deleteForceWorktree = body.ForceWorktree
					respBody, err := json.Marshal(daemonclient.TaskDeleteResult{
						TaskID:          "az-1",
						Deleted:         true,
						SessionStopped:  true,
						WorktreeRemoved: true,
						WorktreeForced:  true,
						Revision:        3,
					})
					if err != nil {
						t.Fatalf("marshal task delete response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            respBody,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueDeleteCommand(deps, IssueDeleteOptions{
			IssueID:        "az-1",
			Confirm:        true,
			StopSession:    true,
			RemoveWorktree: true,
			ForceWorktree:  true,
		})
	})

	wantCommands := []string{
		daemonclient.CommandTaskDelete,
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if deleteCleanup || !deleteStopSession || !deleteRemoveWorktree || !deleteForceWorktree {
		t.Fatalf("task delete cleanup flags cleanup=%v stop=%v remove=%v force=%v, want stop/remove/force only", deleteCleanup, deleteStopSession, deleteRemoveWorktree, deleteForceWorktree)
	}
	for _, want := range []string{"Deleted issue: az-1", "- Session stopped", "- Worktree removed", "- Worktree removal forced"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want substring %q", output, want)
		}
	}
}

func TestIssueUnarchiveCommandCallsDaemon(t *testing.T) {
	var gotCommand string
	var gotTaskID string
	var gotWithParents bool
	var gotCascadeChildren bool
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotCommand = req.Command
				var body struct {
					TaskID          string `json:"task_id"`
					WithParents     bool   `json:"with_parents"`
					CascadeChildren bool   `json:"cascade_children"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal task unarchive body: %v", err)
				}
				gotTaskID = body.TaskID
				gotWithParents = body.WithParents
				gotCascadeChildren = body.CascadeChildren
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueUnarchiveCommand(deps, IssueUnarchiveOptions{IssueID: "az-1", WithParents: true, CascadeChildren: true})
	})
	if gotCommand != daemonclient.CommandTaskUnarchive || gotTaskID != "az-1" || !gotWithParents || !gotCascadeChildren {
		t.Fatalf("command=%s task_id=%s with_parents=%v cascade_children=%v, want %s az-1 true true", gotCommand, gotTaskID, gotWithParents, gotCascadeChildren, daemonclient.CommandTaskUnarchive)
	}
	if !strings.Contains(output, "Unarchived issue: az-1 (with parents, with children)") {
		t.Fatalf("output = %q", output)
	}
}

func TestIssueUpdateCommandUsesDaemonTaskUpdateCommand(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdate:
					gotUpdateReq = req
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID: "az-1",
			Title:   "New",
		})
	})
	if gotUpdateReq.Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update command = %q, want %q", gotUpdateReq.Command, daemonclient.CommandTaskUpdate)
	}
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	if err := json.Unmarshal(gotUpdateReq.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	if updateBody.Title != "New" || updateBody.Description != "OldDesc" {
		t.Fatalf("update body = %+v, want new title with preserved description", updateBody)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueUpdateCommandSendsLifecycleMutation(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P0,
							Status:      domain.StatusOpen,
							State:       mustCLICommandIssueState(t, domain.IssueWorkflowOpen),
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdate:
					gotUpdateReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	lifecycle := domain.IssueWorkflowBacklog
	if err := IssueUpdateCommand(deps, IssueUpdateOptions{IssueID: "az-1", Lifecycle: &lifecycle}); err != nil {
		t.Fatalf("IssueUpdateCommand() error = %v", err)
	}
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	if err := json.Unmarshal(gotUpdateReq.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	if updateBody.Lifecycle == nil || *updateBody.Lifecycle != domain.IssueWorkflowBacklog {
		t.Fatalf("update lifecycle = %+v, want backlog", updateBody.Lifecycle)
	}
	if updateBody.Priority != domain.P0 {
		t.Fatalf("update priority = %s, want preserved P0", updateBody.Priority)
	}
}

func mustCLICommandIssueState(t *testing.T, workflow domain.IssueWorkflow) domain.IssueState {
	t.Helper()
	state, err := domain.NewIssueState(domain.IssueStateParts{Workflow: workflow})
	if err != nil {
		t.Fatalf("NewIssueState(%s): %v", workflow, err)
	}
	return state
}

func TestIssueUpdateCommandRejectsUnknownImplementationAssignment(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	updateCalled := false
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdate:
					updateCalled = true
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueUpdateCommand(deps, IssueUpdateOptions{
		IssueID:     "az-1",
		UpdateImpls: []string{"cif"},
	})
	if err == nil || !strings.Contains(err.Error(), `invalid implementation update: unknown implementation "cif"`) || !strings.Contains(err.Error(), "valid --impl values: default") {
		t.Fatalf("IssueUpdateCommand() error = %v, want unknown implementation", err)
	}
	if updateCalled {
		t.Fatal("task.update should not be called for an unknown implementation")
	}
}

func TestIssueUpdateCommandPassesCascadeChildrenForInReviewStatus(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotStatusReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Parent",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusInProgress,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdateStatus:
					gotStatusReq = req
				case daemonclient.CommandTaskContextRisk:
					return responseWithJSON(req, domain.IssueContextRiskPacket{
						IssueID: "az-1",
						Level:   domain.IssueContextRiskNone,
					}), nil
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}
	status := domain.StatusInReview
	output := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{IssueID: "az-1", Status: &status, CascadeChildren: true})
	})
	if strings.Contains(output, "Context risk:") {
		t.Fatalf("output = %q, did not expect context-risk text for none risk", output)
	}
	if gotStatusReq.Command != daemonclient.CommandTaskUpdateStatus {
		t.Fatalf("status command = %q, want %q", gotStatusReq.Command, daemonclient.CommandTaskUpdateStatus)
	}
	var body daemonclient.TaskStatusRequest
	if err := json.Unmarshal(gotStatusReq.Body, &body); err != nil {
		t.Fatalf("unmarshal status body: %v", err)
	}
	if body.TaskID.String() != "az-1" || body.Status != domain.StatusInReview || !body.CascadeChildren {
		t.Fatalf("status body = %+v, want cascade in_review for az-1", body)
	}
}

func TestIssueUpdateCommandRoutesCancelledThroughCloseWithoutIntegration(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	commands := make([]string, 0, 3)
	var gotCloseReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Cancel",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusInReview,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskClose:
					gotCloseReq = req
					return responseWithJSON(req, daemonclient.TaskCloseResult{
						TaskID: "az-1",
						Status: string(domain.StatusCancelled),
					}), nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}
	status := domain.StatusCancelled
	output := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{IssueID: "az-1", Status: &status, ForceWorktree: true})
	})
	if !strings.Contains(output, "Updated issue: az-1") {
		t.Fatalf("output = %q, want update message", output)
	}
	if strings.Join(commands, ",") != strings.Join([]string{daemonclient.CommandTaskGet, daemonclient.CommandTaskUpdate, daemonclient.CommandTaskClose}, ",") {
		t.Fatalf("commands = %v, want get/update/close", commands)
	}
	var body struct {
		TaskID               string `json:"task_id"`
		ForceWorktree        bool   `json:"force_worktree"`
		IntegrateBeforeClose bool   `json:"integrate_before_close"`
		CloseOutcome         string `json:"closed_outcome"`
	}
	if err := json.Unmarshal(gotCloseReq.Body, &body); err != nil {
		t.Fatalf("unmarshal close body: %v", err)
	}
	if body.TaskID != "az-1" || !body.ForceWorktree || body.IntegrateBeforeClose || body.CloseOutcome != string(domain.IssueCloseCancelled) {
		t.Fatalf("close body = %+v, want cancelled close without integration", body)
	}
}

func TestIssueContextRiskCommandJSONSummaryIsBoundedByDefault(t *testing.T) {
	var compactRequested bool
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskContextRisk {
					return responseWithJSON(req, map[string]any{}), nil
				}
				var body struct {
					Compact bool `json:"compact,omitempty"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal context-risk request: %v", err)
				}
				compactRequested = body.Compact
				return responseWithJSON(req, contextRiskTestPacket()), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueContextRiskCommand(deps, IssueContextRiskOptions{IssueID: "az-1", JSON: true})
	})
	if !compactRequested {
		t.Fatal("context-risk request compact=false, want compact summary request by default")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		t.Fatalf("unmarshal summary output: %v\n%s", err, output)
	}
	if _, ok := raw["evidence"]; ok {
		t.Fatalf("summary output unexpectedly contains raw evidence: %s", output)
	}
	snippets, ok := raw["evidence_snippets"].([]any)
	if !ok || len(snippets) != 3 {
		t.Fatalf("evidence_snippets = %#v, want three snippets", raw["evidence_snippets"])
	}
}

func TestIssueContextRiskCommandJSONFullKeepsRawEvidence(t *testing.T) {
	var compactRequested bool
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				var body struct {
					Compact bool `json:"compact,omitempty"`
				}
				if err := json.Unmarshal(req.Body, &body); err != nil {
					t.Fatalf("unmarshal context-risk request: %v", err)
				}
				compactRequested = body.Compact
				return responseWithJSON(req, contextRiskTestPacket()), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueContextRiskCommand(deps, IssueContextRiskOptions{IssueID: "az-1", JSON: true, Full: true})
	})
	if compactRequested {
		t.Fatal("context-risk --full request compact=true, want full daemon packet")
	}
	var packet domain.IssueContextRiskPacket
	if err := json.Unmarshal([]byte(output), &packet); err != nil {
		t.Fatalf("unmarshal full output: %v\n%s", err, output)
	}
	if len(packet.Evidence) != 4 {
		t.Fatalf("full evidence = %d, want raw evidence retained", len(packet.Evidence))
	}
}

func TestIssueContextRiskCommandTextFullPrintsEvidence(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return responseWithJSON(req, contextRiskTestPacket()), nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueContextRiskCommand(deps, IssueContextRiskOptions{IssueID: "az-1", Full: true})
	})
	if !strings.Contains(output, "Evidence:") || !strings.Contains(output, "az-4 sibling files=internal/daemon/task_commands.go") {
		t.Fatalf("full text output missing raw evidence:\n%s", output)
	}
}

func TestIssueContextRiskCommandTimeoutReturnsDegradedSummary(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{}, context.DeadlineExceeded
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}
	deps.DaemonClient.WithReconnectPolicy(reconnect.Policy{MaxAttempts: 1})

	output := captureStdout(t, func() error {
		return IssueContextRiskCommand(deps, IssueContextRiskOptions{IssueID: "az-1", JSON: true})
	})
	var summary domain.IssueContextRiskSummary
	if err := json.Unmarshal([]byte(output), &summary); err != nil {
		t.Fatalf("unmarshal degraded summary: %v\n%s", err, output)
	}
	if !summary.Degraded || !summary.Timeout || summary.IssueID != "az-1" {
		t.Fatalf("summary = %+v, want degraded timeout summary for az-1", summary)
	}
}

func TestShouldAutostartAfterDaemonReadErrorSeparatesReadWaitFromTransportFailure(t *testing.T) {
	readWait := &daemonclient.ReadWaitTimeoutError{
		Mode:   daemonclient.ReadWaitModeDefault,
		Budget: 2 * time.Second,
		Err:    context.DeadlineExceeded,
	}
	if shouldAutostartAfterDaemonReadError(readWait) {
		t.Fatal("typed snapshot read timeout triggered daemon autostart")
	}
	if !shouldAutostartAfterDaemonReadError(errors.New("dial unix /tmp/azedarach.sock: connection refused")) {
		t.Fatal("transport connection failure did not trigger daemon autostart")
	}
}

func TestIssueUpdateCommandBlocksInReviewForHighContextRiskBeforeMutating(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var commands []string
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Repeated local failure",
							Type:      domain.TypeTask,
							Priority:  domain.P2,
							Status:    domain.StatusInProgress,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskContextRisk:
					return responseWithJSON(req, domain.IssueContextRiskPacket{
						IssueID:    "az-1",
						Level:      domain.IssueContextRiskHigh,
						Confidence: 75,
						Evidence: []domain.IssueContextRiskEvidence{
							{IssueID: "az-1", Files: []string{"internal/daemon/task_commands.go"}},
							{IssueID: "az-2", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure repeated"}},
						},
					}), nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}
	status := domain.StatusInReview
	err := IssueUpdateCommand(deps, IssueUpdateOptions{IssueID: "az-1", Status: &status, Title: "Should not mutate"})
	if err == nil || !strings.Contains(err.Error(), "context risk is high") {
		t.Fatalf("IssueUpdateCommand() error = %v, want context risk block", err)
	}
	for _, forbidden := range []string{daemonclient.CommandTaskUpdate, daemonclient.CommandTaskUpdateStatus} {
		if containsString(commands, forbidden) {
			t.Fatalf("commands = %v, did not expect %s after context risk block", commands, forbidden)
		}
	}
}

func TestIssueUpdateCommandCanClearDescription(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdate:
					gotUpdateReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID:        "az-1",
			DescriptionSet: true,
		})
	})
	if gotUpdateReq.Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update command = %q, want %q", gotUpdateReq.Command, daemonclient.CommandTaskUpdate)
	}
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	if err := json.Unmarshal(gotUpdateReq.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	if updateBody.TaskID != "az-1" || updateBody.Title != "Old" || updateBody.Description != "" {
		t.Fatalf("update body = %+v, want preserved title and cleared description", updateBody)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueUpdateCommandConfirmedClosedCleansBeforeStatus(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	status := domain.StatusDone
	commands := make([]string, 0, 5)
	var closeForce bool
	worktreeListBody, err := json.Marshal(struct {
		ProjectID string `json:"project_id"`
		Worktrees []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		} `json:"worktrees"`
	}{
		ProjectID: "proj",
		Worktrees: []struct {
			Path    string `json:"path"`
			Branch  string `json:"branch"`
			IssueID string `json:"issue_id"`
		}{
			{Path: "/tmp/az-1", Branch: "riordan/az-1/ready", IssueID: "az-1"},
		},
	})
	if err != nil {
		t.Fatalf("marshal worktree list: %v", err)
	}
	taskListBody, err := marshalTaskListBody([]domain.Task{
		{
			ID:             "az-1",
			Title:          "Ready",
			Type:           domain.TypeTask,
			Priority:       domain.P2,
			Status:         domain.StatusInReview,
			CreatedAt:      now,
			UpdatedAt:      now,
			HasTmuxSession: true,
			HasWorktree:    true,
			Session:        &domain.Session{IssueID: naming.IssueID("az-1"), Worktree: "/tmp/az-1"},
		},
	})
	if err != nil {
		t.Fatalf("marshal task list: %v", err)
	}
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGet:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskUpdate:
					return responseWithJSON(req, map[string]any{}), nil
				case daemonclient.CommandWorktreeList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            worktreeListBody,
					}, nil
				case daemonclient.CommandTaskList:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            taskListBody,
					}, nil
				case daemonclient.CommandTaskMergeBaseTarget:
					return responseWithJSON(req, daemonclient.TaskMergeBaseTarget{IssueID: "az-1", TargetID: "base", Branch: "main"}), nil
				case daemonclient.CommandGitStatus:
					return responseWithJSON(req, map[string]any{"status": gitservice.GitStatus{HasChanges: false}}), nil
				case daemonclient.CommandGitWorktreeForBranch:
					return responseWithJSON(req, daemonclient.GitWorktreeForBranchResponse{
						Branch: "main",
						Found:  false,
					}), nil
				case daemonclient.CommandGitFetch:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Remote:   "origin",
					}), nil
				case daemonclient.CommandGitCheckout:
					return responseWithJSON(req, daemonclient.GitCommandResponse{
						Worktree: ".",
						Branch:   "main",
					}), nil
				case daemonclient.CommandGitMerge:
					return responseWithJSON(req, daemonclient.GitMergeCommandResponse{
						Worktree: ".",
						Branch:   "riordan/az-1/ready",
						Result: gitservice.MergeResult{
							Success: true,
							Message: "merge complete",
						},
					}), nil
				case daemonclient.CommandTaskClose:
					var body struct {
						ForceWorktree        bool `json:"force_worktree"`
						IntegrateBeforeClose bool `json:"integrate_before_close"`
					}
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task close body: %v", err)
					}
					closeForce = body.ForceWorktree
					if !body.IntegrateBeforeClose {
						t.Fatalf("task close integrate_before_close = false, want true")
					}
					return responseWithJSON(req, daemonclient.TaskCloseResult{
						TaskID:                 "az-1",
						Status:                 string(domain.StatusDone),
						IntegrationRequested:   true,
						Integrated:             true,
						IntegratedSourceBranch: "riordan/az-1/ready",
						IntegratedTargetBranch: "main",
						SessionStopped:         true,
						WorktreeRemoved:        true,
						WorktreeForced:         body.ForceWorktree,
					}), nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID:       "az-1",
			Status:        &status,
			ForceWorktree: true,
		})
	})

	wantCommands := []string{
		daemonclient.CommandTaskGet,
		daemonclient.CommandTaskUpdate,
		daemonclient.CommandTaskClose,
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if !closeForce {
		t.Fatal("task close force_worktree = false, want true")
	}
	if !strings.Contains(output, "Updated issue: az-1") {
		t.Fatalf("output = %q", output)
	}
}

func TestIssueUpdateCommandReplacesNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotUpdateReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Notes:       "Existing notes",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskUpdate:
					gotUpdateReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	notes := "Replacement notes"
	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID: "az-1",
			Notes:   &notes,
		})
	})
	if gotUpdateReq.Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update command = %q, want %q", gotUpdateReq.Command, daemonclient.CommandTaskUpdate)
	}
	var updateBody struct {
		TaskID string `json:"task_id"`
		daemonclient.TaskUpdateParams
	}
	if err := json.Unmarshal(gotUpdateReq.Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update body: %v", err)
	}
	if updateBody.TaskID != "az-1" || updateBody.Notes == nil || *updateBody.Notes != "Replacement notes" {
		t.Fatalf("update body = %+v", updateBody)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueUpdateCommandAppendsNotesWhenRequested(t *testing.T) {
	now := time.Date(2026, 3, 25, 10, 0, 0, 0, time.UTC)
	var gotAppendReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:          "az-1",
							Title:       "Old",
							Description: "OldDesc",
							Type:        domain.TypeTask,
							Priority:    domain.P2,
							Status:      domain.StatusOpen,
							CreatedAt:   now,
							UpdatedAt:   now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskAppendNotes:
					gotAppendReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	updateOut := captureStdout(t, func() error {
		return IssueUpdateCommand(deps, IssueUpdateOptions{
			IssueID:     "az-1",
			AppendNotes: "Follow-up detail",
		})
	})
	if gotAppendReq.Command != daemonclient.CommandTaskAppendNotes {
		t.Fatalf("append command = %q, want %q", gotAppendReq.Command, daemonclient.CommandTaskAppendNotes)
	}
	var appendBody daemonclient.TaskAppendNotesRequest
	if err := json.Unmarshal(gotAppendReq.Body, &appendBody); err != nil {
		t.Fatalf("unmarshal append body: %v", err)
	}
	if appendBody.TaskID != "az-1" || appendBody.Line != "Follow-up detail" {
		t.Fatalf("append body = %+v", appendBody)
	}
	if !strings.Contains(updateOut, "Updated issue: az-1") {
		t.Fatalf("update output = %q", updateOut)
	}
}

func TestIssueDependencyCommandsUseDaemonTaskCommands(t *testing.T) {
	var gotAddReq protocol.RequestEnvelope
	var gotRemoveReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskDependencyAdd:
					gotAddReq = req
				case daemonclient.CommandTaskDependencyRemove:
					gotRemoveReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	addOut := captureStdout(t, func() error {
		return IssueDependencyAddCommand(deps, IssueDependencyAddOptions{
			IssueID:           "az-5",
			DependsOnID:       "az-2",
			Type:              "parent-child",
			ForceParentChange: true,
		})
	})
	if gotAddReq.Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("add command = %q, want %q", gotAddReq.Command, daemonclient.CommandTaskDependencyAdd)
	}
	var addBody daemonclient.TaskDependencyParams
	if err := json.Unmarshal(gotAddReq.Body, &addBody); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if addBody.TaskID != "az-5" || addBody.DependsOnID != "az-2" || addBody.Type != "parent-child" || !addBody.ForceParentChange {
		t.Fatalf("add body = %+v", addBody)
	}
	if !strings.Contains(addOut, "Added dependency: az-5 --(parent-child)--> az-2") {
		t.Fatalf("add output = %q", addOut)
	}
	if !strings.Contains(addOut, "This makes az-5 a child of az-2.") {
		t.Fatalf("add output = %q", addOut)
	}

	removeOut := captureStdout(t, func() error {
		return IssueDependencyRemoveCommand(deps, IssueDependencyRemoveOptions{
			IssueID:             "az-5",
			DependsOnID:         "az-2",
			Type:                "parent-child",
			Confirm:             true,
			ConfirmParentOrphan: true,
		})
	})
	if gotRemoveReq.Command != daemonclient.CommandTaskDependencyRemove {
		t.Fatalf("remove command = %q, want %q", gotRemoveReq.Command, daemonclient.CommandTaskDependencyRemove)
	}
	var removeBody daemonclient.TaskDependencyRemoveParams
	if err := json.Unmarshal(gotRemoveReq.Body, &removeBody); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if removeBody.TaskID != "az-5" || removeBody.DependsOnID != "az-2" || removeBody.Type != "parent-child" || !removeBody.Confirm || !removeBody.ConfirmParentOrphan {
		t.Fatalf("remove body = %+v", removeBody)
	}
	if !strings.Contains(removeOut, "Removed dependency: az-5 --(parent-child)--> az-2") {
		t.Fatalf("remove output = %q", removeOut)
	}
}

func TestIssueDependencyAddCommandCarriesProjectQualifiedEndpointMetadata(t *testing.T) {
	routes := registerCLIProjects(t, "chefy")
	var gotReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				gotReq = req
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	opts, err := ParseIssueDependencyAddArgs([]string{"chefy:az-5", "chefy:az-2", "--type", "blocks"})
	if err != nil {
		t.Fatalf("ParseIssueDependencyAddArgs() error = %v", err)
	}
	_ = captureStdout(t, func() error {
		return IssueDependencyAddCommand(deps, opts)
	})

	if gotReq.Meta.ProjectID.String() != routes["chefy"] {
		t.Fatalf("request project = %q, want %s", gotReq.Meta.ProjectID, routes["chefy"])
	}
	var body daemonclient.TaskDependencyParams
	if err := json.Unmarshal(gotReq.Body, &body); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if body.TaskID != "az-5" || body.DependsOnID != "az-2" || body.IssueProjectID != "chefy" || body.DependsOnProjectID != "chefy" {
		t.Fatalf("dependency body = %+v", body)
	}
}

func TestIssueDependencyAddParentChildErrorIncludesDirectionGuidance(t *testing.T) {
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              false,
					Error: &protocol.ErrorEnvelope{
						Code:    protocol.ErrorCodeInternal,
						Message: "refusing to change parent for az-5: current parent az-1, requested parent az-2",
					},
					CompletedAt: req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	err := IssueDependencyAddCommand(deps, IssueDependencyAddOptions{
		IssueID:     "az-5",
		DependsOnID: "az-2",
		Type:        "parent-child",
	})
	if err == nil {
		t.Fatal("IssueDependencyAddCommand() error = nil, want parent-change guidance")
	}
	msg := err.Error()
	for _, want := range []string{
		"This would make az-5 a child of az-2",
		"az issue dep add az-2 az-5 --type parent-child",
		"--force-parent-change",
		"current parent az-1, requested parent az-2",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestIssueImageCommands(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	tempRepo := t.TempDir()
	dbPath := filepath.Join(tempRepo, ".azedarach", "azedarach.db")
	t.Setenv("AZEDARACH_DB_PATH", dbPath)
	sourceImage := filepath.Join(t.TempDir(), "screenshot.png")
	if err := os.WriteFile(sourceImage, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatalf("write source image: %v", err)
	}

	var appendNotesReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Task",
							Status:    domain.StatusOpen,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskAppendNotes:
					appendNotesReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   tempRepo,
	}

	addOut := captureStdout(t, func() error {
		return IssueImageAddCommand(deps, IssueImageAddOptions{
			IssueID:    "az-1",
			SourcePath: sourceImage,
		})
	})
	if !strings.Contains(addOut, "Attached image to issue az-1:") {
		t.Fatalf("add output = %q", addOut)
	}
	if appendNotesReq.Command != daemonclient.CommandTaskAppendNotes {
		t.Fatalf("append notes command = %q, want %q", appendNotesReq.Command, daemonclient.CommandTaskAppendNotes)
	}
	var appendBody daemonclient.TaskAppendNotesRequest
	if err := json.Unmarshal(appendNotesReq.Body, &appendBody); err != nil {
		t.Fatalf("unmarshal append notes body: %v", err)
	}
	if appendBody.TaskID != "az-1" || !strings.Contains(appendBody.Line, ".azedarach/attachments/") || strings.Contains(appendBody.Line, ".azedarach/images/") || strings.Contains(appendBody.Line, ".azedarach/attachments/az-1/") {
		t.Fatalf("append body = %+v", appendBody)
	}

	files, err := filepath.Glob(filepath.Join(tempRepo, ".azedarach", "attachments", "*-screenshot.png"))
	if err != nil {
		t.Fatalf("glob attachments: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("attachment files = %v, want 1 file", files)
	}
	filename := filepath.Base(files[0])
	parts := strings.SplitN(filename, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("attachment filename = %q, want <id>-<name> format", filename)
	}
	if _, err := os.Stat(filepath.Join(tempRepo, ".azedarach", "images", "az-1")); !os.IsNotExist(err) {
		t.Fatalf("image command should not write old image directory: %v", err)
	}
	db := openSQLiteDB(t, dbPath)
	var linkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_attachments WHERE issue_id = ? AND attachment_id = ?`, "az-1", parts[0]).Scan(&linkCount); err != nil {
		t.Fatalf("query attachment link: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("attachment link count = %d, want 1", linkCount)
	}

	removeOut := captureStdout(t, func() error {
		return IssueImageRemoveCommand(deps, IssueImageRemoveOptions{
			IssueID:      "az-1",
			AttachmentID: parts[0],
		})
	})
	if !strings.Contains(removeOut, "Removed image attachment "+parts[0]+" from issue az-1") {
		t.Fatalf("remove output = %q", removeOut)
	}
	if _, statErr := os.Stat(files[0]); statErr != nil {
		t.Fatalf("shared attachment file should remain after remove: statErr=%v", statErr)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_attachments WHERE issue_id = ? AND attachment_id = ?`, "az-1", parts[0]).Scan(&linkCount); err != nil {
		t.Fatalf("query attachment link after remove: %v", err)
	}
	if linkCount != 0 {
		t.Fatalf("attachment link count after remove = %d, want 0", linkCount)
	}
}

func TestIssueDocumentCommands(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	tempRepo := t.TempDir()
	dbPath := filepath.Join(tempRepo, ".azedarach", "azedarach.db")
	t.Setenv("AZEDARACH_DB_PATH", dbPath)
	sourceDoc := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(sourceDoc, []byte("# Report\n\nSession result.\n"), 0o644); err != nil {
		t.Fatalf("write source document: %v", err)
	}

	var appendNotesReq protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGetMany:
					assertMetadataOnlyTaskGetManyRequest(t, req, "az-1")
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:        "az-1",
							Title:     "Task",
							Status:    domain.StatusOpen,
							CreatedAt: now,
							UpdatedAt: now,
						},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        1,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskAppendNotes:
					appendNotesReq = req
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
				}, nil
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
		RepoDir:   tempRepo,
	}

	addOut := captureStdout(t, func() error {
		return IssueDocumentAddCommand(deps, IssueDocumentAddOptions{
			IssueID:    "az-1",
			SourcePath: sourceDoc,
		})
	})
	if !strings.Contains(addOut, "Attached document to issue az-1:") {
		t.Fatalf("add output = %q", addOut)
	}
	var appendBody daemonclient.TaskAppendNotesRequest
	if err := json.Unmarshal(appendNotesReq.Body, &appendBody); err != nil {
		t.Fatalf("unmarshal append notes body: %v", err)
	}
	if appendBody.TaskID != "az-1" || !strings.Contains(appendBody.Line, ".azedarach/attachments/") || strings.Contains(appendBody.Line, ".azedarach/attachments/az-1/") {
		t.Fatalf("append body = %+v", appendBody)
	}

	files, err := filepath.Glob(filepath.Join(tempRepo, ".azedarach", "attachments", "*-report.md"))
	if err != nil {
		t.Fatalf("glob attachments: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("attachment files = %v, want 1 file", files)
	}
	filename := filepath.Base(files[0])
	parts := strings.SplitN(filename, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("attachment filename = %q, want <id>-<name> format", filename)
	}
	db := openSQLiteDB(t, dbPath)
	var linkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_attachments WHERE issue_id = ? AND attachment_id = ?`, "az-1", parts[0]).Scan(&linkCount); err != nil {
		t.Fatalf("query attachment link: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("attachment link count = %d, want 1", linkCount)
	}

	listOut := captureStdout(t, func() error {
		return IssueDocumentListCommand(deps, IssueDocumentListOptions{
			IssueID: "az-1",
		})
	})
	for _, want := range []string{
		"Document attachments for issue az-1:",
		parts[0],
		filename,
		"text/markdown",
		".azedarach/attachments/",
	} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("list output missing %q: %q", want, listOut)
		}
	}

	removeOut := captureStdout(t, func() error {
		return IssueDocumentRemoveCommand(deps, IssueDocumentRemoveOptions{
			IssueID:      "az-1",
			AttachmentID: parts[0],
		})
	})
	if !strings.Contains(removeOut, "Removed document attachment "+parts[0]+" from issue az-1") {
		t.Fatalf("remove output = %q", removeOut)
	}
	if _, statErr := os.Stat(files[0]); statErr != nil {
		t.Fatalf("shared attachment file should remain after remove: statErr=%v", statErr)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_attachments WHERE issue_id = ? AND attachment_id = ?`, "az-1", parts[0]).Scan(&linkCount); err != nil {
		t.Fatalf("query attachment link after remove: %v", err)
	}
	if linkCount != 0 {
		t.Fatalf("attachment link count after remove = %d, want 0", linkCount)
	}
}

func TestIssueBulkCommandsUseApplyCommand(t *testing.T) {
	tempDir := t.TempDir()
	bulkCreatePath := filepath.Join(tempDir, "bulk-create.json")
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update.json")
	if err := os.WriteFile(bulkCreatePath, []byte(`[{"title":"Bulk epic","description":"Parent","type":"epic","priority":"P2","children":[{"title":"Bulk child","description":"Child","type":"task","priority":"P3"}]}]`), 0o644); err != nil {
		t.Fatalf("write bulk-create file: %v", err)
	}
	if err := os.WriteFile(bulkUpdatePath, []byte(`[{"task_id":"az-1","title":"Renamed","priority":"P1"}]`), 0o644); err != nil {
		t.Fatalf("write bulk-update file: %v", err)
	}

	var commands []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req)
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:              "az-1",
						Title:           "Old",
						Type:            domain.TypeTask,
						Priority:        domain.P2,
						Status:          domain.StatusOpen,
						Implementations: []string{"go-bubbletea"},
					}})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:              "az-1",
						Title:           "Old",
						Description:     "Desc",
						Type:            domain.TypeTask,
						Priority:        domain.P2,
						Status:          domain.StatusOpen,
						Implementations: []string{"go-bubbletea"},
					}})
					if err != nil {
						t.Fatalf("marshal get response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case protocol.CommandTaskBulkApply:
					body, err := json.Marshal(applyExecutionResultBody{
						Summary: applyExecutionSummaryBody{Total: 1, Succeeded: 1, Failed: 0},
					})
					if err != nil {
						t.Fatalf("marshal apply response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	_ = captureStdout(t, func() error {
		return IssueBulkCreateCommand(deps, IssueBulkCreateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkCreatePath,
			DryRun:         false,
		})
	})
	_ = captureStdout(t, func() error {
		return IssueBulkUpdateCommand(deps, IssueBulkUpdateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkUpdatePath,
			DryRun:         true,
		})
	})

	applyReqs := make([]protocol.RequestEnvelope, 0, 2)
	for _, req := range commands {
		if req.Command == protocol.CommandTaskBulkApply {
			applyReqs = append(applyReqs, req)
		}
	}
	if len(applyReqs) != 2 {
		t.Fatalf("bulk apply command count = %d, want 2", len(applyReqs))
	}
	var createBody protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReqs[0].Body, &createBody); err != nil {
		t.Fatalf("unmarshal create apply body: %v", err)
	}
	if createBody.DryRun {
		t.Fatalf("create body dry_run = true, want false")
	}
	if len(createBody.Operations) != 2 || createBody.Operations[0].Command != daemonclient.CommandTaskCreate || createBody.Operations[1].Command != daemonclient.CommandTaskCreate {
		t.Fatalf("create operations = %+v", createBody.Operations)
	}
	var parentCreate struct {
		Title           string   `json:"title"`
		Description     string   `json:"description"`
		Type            string   `json:"type"`
		Priority        string   `json:"priority"`
		Implementations []string `json:"implementations,omitempty"`
		Ref             string   `json:"ref,omitempty"`
	}
	if err := json.Unmarshal(createBody.Operations[0].Body, &parentCreate); err != nil {
		t.Fatalf("unmarshal parent create: %v", err)
	}
	if parentCreate.Type != string(domain.TypeEpic) || parentCreate.Priority != "P2" || parentCreate.Ref == "" || !equalStrings(parentCreate.Implementations, []string{"go-bubbletea"}) {
		t.Fatalf("parent create = %+v, want epic P2 with generated ref and implementation", parentCreate)
	}
	var childCreate struct {
		Title     string `json:"title"`
		Priority  string `json:"priority"`
		ParentRef string `json:"parent_ref,omitempty"`
	}
	if err := json.Unmarshal(createBody.Operations[1].Body, &childCreate); err != nil {
		t.Fatalf("unmarshal child create: %v", err)
	}
	if childCreate.Title != "Bulk child" || childCreate.ParentRef != parentCreate.Ref || childCreate.Priority != "P3" {
		t.Fatalf("child create = %+v, want parent_ref %q and P3", childCreate, parentCreate.Ref)
	}
	var updateBody protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReqs[1].Body, &updateBody); err != nil {
		t.Fatalf("unmarshal update apply body: %v", err)
	}
	if !updateBody.DryRun {
		t.Fatalf("update body dry_run = false, want true")
	}
	if len(updateBody.Operations) != 1 || updateBody.Operations[0].Command != daemonclient.CommandTaskUpdate {
		t.Fatalf("update operations = %+v", updateBody.Operations)
	}
	var updateParams struct {
		TaskID      string `json:"task_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
	}
	if err := json.Unmarshal(updateBody.Operations[0].Body, &updateParams); err != nil {
		t.Fatalf("unmarshal update operation body: %v", err)
	}
	if updateParams.Title != "Renamed" || updateParams.Description != "Desc" || updateParams.Priority != "P1" {
		t.Fatalf("update params = %+v, want renamed title with preserved description and P1", updateParams)
	}
}

func TestIssueBulkUpdateCommandDescriptionPresenceControlsApplyPayload(t *testing.T) {
	tempDir := t.TempDir()
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update-description.json")
	if err := os.WriteFile(
		bulkUpdatePath,
		[]byte(`[{"task_id":"az-clear","description":""},{"task_id":"az-preserve","title":"Retitled"}]`),
		0o644,
	); err != nil {
		t.Fatalf("write bulk-update file: %v", err)
	}

	var applyReq *protocol.RequestEnvelope
	var taskGetIDs []string
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{
							ID:              "az-clear",
							Title:           "Clear",
							Type:            domain.TypeTask,
							Priority:        domain.P2,
							Status:          domain.StatusOpen,
							Implementations: []string{"go-bubbletea"},
						},
						{
							ID:              "az-preserve",
							Title:           "Preserve",
							Type:            domain.TypeTask,
							Priority:        domain.P2,
							Status:          domain.StatusOpen,
							Implementations: []string{"go-bubbletea"},
						},
					})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskGet:
					var body daemonclient.TaskIDRequest
					if err := json.Unmarshal(req.Body, &body); err != nil {
						t.Fatalf("unmarshal task.get body: %v", err)
					}
					taskGetIDs = append(taskGetIDs, body.TaskID.String())
					task := domain.Task{
						ID:              body.TaskID,
						Title:           "Preserve",
						Description:     "Existing description",
						Type:            domain.TypeTask,
						Priority:        domain.P2,
						Status:          domain.StatusOpen,
						Implementations: []string{"go-bubbletea"},
					}
					if body.TaskID.String() == "az-clear" {
						task.Title = "Clear"
					}
					responseBody, err := marshalTaskListBody([]domain.Task{task})
					if err != nil {
						t.Fatalf("marshal get response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            responseBody,
					}, nil
				case protocol.CommandTaskBulkApply:
					applyReq = &req
					body, err := json.Marshal(applyExecutionResultBody{
						Summary: applyExecutionSummaryBody{Total: 2, Succeeded: 2, Failed: 0},
					})
					if err != nil {
						t.Fatalf("marshal apply response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					t.Fatalf("unexpected command: %s", req.Command)
					return protocol.ResponseEnvelope{}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	_ = captureStdout(t, func() error {
		return IssueBulkUpdateCommand(deps, IssueBulkUpdateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkUpdatePath,
			DryRun:         true,
		})
	})

	if !reflect.DeepEqual(taskGetIDs, []string{"az-clear", "az-preserve"}) {
		t.Fatalf("task.get ids = %v, want clear then preserve", taskGetIDs)
	}
	if applyReq == nil {
		t.Fatalf("expected bulk apply command")
	}
	var applyBody protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReq.Body, &applyBody); err != nil {
		t.Fatalf("unmarshal apply body: %v", err)
	}
	if !applyBody.DryRun {
		t.Fatalf("dry_run = false, want true")
	}
	if len(applyBody.Operations) != 2 {
		t.Fatalf("operation count = %d, want 2", len(applyBody.Operations))
	}

	updatesByID := make(map[string]struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    string `json:"priority"`
	})
	for _, op := range applyBody.Operations {
		if op.Command != daemonclient.CommandTaskUpdate {
			t.Fatalf("operation command = %q, want %q", op.Command, daemonclient.CommandTaskUpdate)
		}
		var update struct {
			TaskID      string `json:"task_id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Priority    string `json:"priority"`
		}
		if err := json.Unmarshal(op.Body, &update); err != nil {
			t.Fatalf("unmarshal update operation body: %v", err)
		}
		updatesByID[update.TaskID] = struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Priority    string `json:"priority"`
		}{
			Title:       update.Title,
			Description: update.Description,
			Priority:    update.Priority,
		}
	}

	if got := updatesByID["az-clear"]; got.Title != "Clear" || got.Description != "" || got.Priority != "P2" {
		t.Fatalf("clear update = %+v, want existing title/P2 with empty description", got)
	}
	if got := updatesByID["az-preserve"]; got.Title != "Retitled" || got.Description != "Existing description" || got.Priority != "P2" {
		t.Fatalf("preserve update = %+v, want retitled with existing description/P2", got)
	}
}

func TestCompileBulkCreateItemRejectsDuplicateRefs(t *testing.T) {
	input := issueBulkCreateInputItem{
		Title:       "Epic",
		Description: "Parent",
		Type:        "epic",
		Priority:    "P2",
		Ref:         "same",
		Children: []issueBulkCreateInputItem{{
			Title:       "Child",
			Description: "Nested",
			Type:        "task",
			Priority:    "P2",
			Ref:         "same",
		}},
	}
	_, err := compileBulkCreateItem(input, "bulk-create item 0", "go-bubbletea", "", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `duplicate ref "same"`) {
		t.Fatalf("compileBulkCreateItem() error = %v, want duplicate ref", err)
	}
}

func TestCompileBulkCreateItemRejectsInvalidParentID(t *testing.T) {
	parentID := "not/an/issue"
	input := issueBulkCreateInputItem{
		Title:       "Child",
		Description: "Bad parent",
		Type:        "task",
		Priority:    "P2",
		ParentID:    &parentID,
	}
	_, err := compileBulkCreateItem(input, "bulk-create item 0", "go-bubbletea", "", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `bulk-create item 0: invalid parent_id "not/an/issue"`) {
		t.Fatalf("compileBulkCreateItem() error = %v, want invalid parent_id", err)
	}
}

func TestCompileBulkCreateItemRejectsEmptyParentID(t *testing.T) {
	parentID := "  "
	input := issueBulkCreateInputItem{
		Title:       "Child",
		Description: "Blank parent",
		Type:        "task",
		Priority:    "P2",
		ParentID:    &parentID,
	}
	_, err := compileBulkCreateItem(input, "bulk-create item 0.children[0]", "go-bubbletea", "parent-ref", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), `bulk-create item 0.children[0]: parent_id cannot be empty`) {
		t.Fatalf("compileBulkCreateItem() error = %v, want empty parent_id", err)
	}
}

func TestIssueBulkUpdateCommand_DependencyRetargetBuildsApplyOps(t *testing.T) {
	tempDir := t.TempDir()
	bulkUpdatePath := filepath.Join(tempDir, "bulk-update-retarget.json")
	if err := os.WriteFile(
		bulkUpdatePath,
		[]byte(`[{"task_id":"az-1","dependency_retargets":[{"from_id":"az-old","to_id":"az-new","type":"blocks"}]}]`),
		0o644,
	); err != nil {
		t.Fatalf("write bulk-update file: %v", err)
	}

	var commands []protocol.RequestEnvelope
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req)
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:              "az-1",
						Title:           "Existing",
						Description:     "Desc",
						Type:            domain.TypeTask,
						Priority:        domain.P2,
						Status:          domain.StatusOpen,
						Implementations: []string{"go-bubbletea"},
					}})
					if err != nil {
						t.Fatalf("marshal list response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Revision:        2,
						Body:            body,
					}, nil
				case protocol.CommandTaskBulkApply:
					body, err := json.Marshal(applyExecutionResultBody{
						Summary: applyExecutionSummaryBody{Total: 2, Succeeded: 2, Failed: 0},
					})
					if err != nil {
						t.Fatalf("marshal apply response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}

	_ = captureStdout(t, func() error {
		return IssueBulkUpdateCommand(deps, IssueBulkUpdateOptions{
			Implementation: "go-bubbletea",
			InputPath:      bulkUpdatePath,
			DryRun:         true,
		})
	})

	var applyReq *protocol.RequestEnvelope
	for i := range commands {
		if commands[i].Command == protocol.CommandTaskBulkApply {
			applyReq = &commands[i]
			break
		}
	}
	if applyReq == nil {
		t.Fatalf("expected bulk apply command")
	}
	var body protocol.ApplyRequestBody
	if err := json.Unmarshal(applyReq.Body, &body); err != nil {
		t.Fatalf("unmarshal apply body: %v", err)
	}
	if !body.DryRun {
		t.Fatalf("dry_run = false, want true")
	}
	if len(body.Operations) != 2 {
		t.Fatalf("operation count = %d, want 2", len(body.Operations))
	}
	if body.Operations[0].Command != daemonclient.CommandTaskDependencyRemove {
		t.Fatalf("operation[0] command = %q, want %q", body.Operations[0].Command, daemonclient.CommandTaskDependencyRemove)
	}
	if body.Operations[1].Command != daemonclient.CommandTaskDependencyAdd {
		t.Fatalf("operation[1] command = %q, want %q", body.Operations[1].Command, daemonclient.CommandTaskDependencyAdd)
	}

	var removeBody daemonclient.TaskDependencyRemoveParams
	if err := json.Unmarshal(body.Operations[0].Body, &removeBody); err != nil {
		t.Fatalf("unmarshal remove body: %v", err)
	}
	if removeBody.TaskID != "az-1" || removeBody.DependsOnID != "az-old" || removeBody.Type != "blocks" || !removeBody.Confirm {
		t.Fatalf("remove body = %+v", removeBody)
	}

	var addBody daemonclient.TaskDependencyParams
	if err := json.Unmarshal(body.Operations[1].Body, &addBody); err != nil {
		t.Fatalf("unmarshal add body: %v", err)
	}
	if addBody.TaskID != "az-1" || addBody.DependsOnID != "az-new" || addBody.Type != "blocks" {
		t.Fatalf("add body = %+v", addBody)
	}
}

func TestPrimeCommandWithoutIssueContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	setPrimeTmuxAvailable(t, true)

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{})
	})

	if output == "" {
		t.Fatal("prime output is empty")
	}
	if !strings.Contains(output, "your first action must always be to encode the approved plan into Azedarach before editing code") {
		t.Fatalf("prime output missing approved-plan encoding order: %q", output)
	}
	if !strings.Contains(output, "Do not create needless decomposition for a single-scope plan") {
		t.Fatalf("prime output missing single-scope plan guidance: %q", output)
	}
	if strings.Contains(output, "Active ticket ID:") || strings.Contains(output, "Active ticket context (ticket=") {
		t.Fatalf("prime without issue rendered active issue context: %q", output)
	}
	for _, want := range []string{"Ticket Types", "`investigation` for research", "az ticket update --type <type>", "explicit, ticket-specific human acceptance"} {
		if !strings.Contains(output, want) {
			t.Fatalf("prime output missing investigation guidance %q: %q", want, output)
		}
	}
}

func TestPrimeCommandRendersPersistedProjectOrchestratorSnapshotOnly(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "stale-worker-scope")
	setPrimeTmuxAvailable(t, true)
	previous := tmuxPaneSessionName
	tmuxPaneSessionName = func(context.Context) (string, error) { return "az-project-orchestrator", nil }
	t.Cleanup(func() { tmuxPaneSessionName = previous })
	commands := []string{}
	deps := &Dependencies{
		ProjectID: "proj",
		Config:    config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
			commands = append(commands, req.Command)
			if req.Command != daemonclient.CommandOrchestrationSnapshot {
				t.Fatalf("unexpected daemon command %q", req.Command)
			}
			return responseWithJSON(req, protocol.OrchestrationSnapshot{
				Role: "orchestrator", SessionID: "az-project-orchestrator", Lifecycle: domain.OrchestratorWorking,
				Scope: domain.ProjectOrchestrationScope(), Revision: 41, Cursor: 9,
				Capacity:           protocol.OrchestrationCapacity{DirectRunnableCount: 2, DirectActiveCount: 1, TotalCountingCapacityCount: 3},
				Candidates:         []protocol.OrchestrationCandidate{{IssueID: "az-ready", Classification: "runnable", Reason: "included: ready for worker start"}},
				Reviews:            []protocol.OrchestrationCandidate{{IssueID: "az-review", Classification: "review-ready", Reason: "excluded: review requested"}},
				OwnershipConflicts: []protocol.OrchestrationCandidate{{IssueID: "az-owned", Classification: "owned-elsewhere", Reason: "excluded: owned by another actor"}},
				Blocked:            map[string]string{"az-blocked": "dependency open"},
				Interactions:       []domain.InteractionRequest{{ID: "int-1", IssueID: "az-wait", Question: "Choose rollout?"}},
				RecentEvents:       []protocol.MailEvent{{Seq: 9, IssueID: "az-ready", Type: "worker-progress", Body: "tests running"}},
				Constraints:        protocol.OrchestrationConstraints{StartLimit: 4, AgentCapacity: 12, Commands: []string{"az orchestrate status"}, RoleGuardrails: []string{"remain in the active orchestration loop"}},
			}), nil
		}}),
	}
	output := captureStdout(t, func() error { return PrimeCommand(deps) })
	if !reflect.DeepEqual(commands, []string{daemonclient.CommandOrchestrationSnapshot}) {
		t.Fatalf("commands = %v, want one coherent orchestration snapshot", commands)
	}
	for _, want := range []string{
		"role=orchestrator scope=project lifecycle=working revision=41 cursor=9",
		"daemon identifies this session as the project orchestrator",
		"az-ready: runnable", "az-review: review-ready", "az-owned: owned-elsewhere",
		"az-blocked: dependency open", "int-1 (az-wait): Choose rollout?", "#9 worker-progress az-ready: tests running",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("prime output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "stale-worker-scope") {
		t.Fatalf("project orchestrator output leaked environment worker scope: %s", output)
	}
}

func TestPrimeCommandRendersPersistedRootedOrchestratorScope(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "stale-root")
	setPrimeTmuxAvailable(t, true)
	previous := tmuxPaneSessionName
	tmuxPaneSessionName = func(context.Context) (string, error) { return "az-root-orchestrator", nil }
	t.Cleanup(func() { tmuxPaneSessionName = previous })
	rooted, err := domain.RootedOrchestrationScope("az-root")
	if err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{ProjectID: "proj", Config: config.DefaultConfig(), DaemonClient: daemonclient.New(&fakeDaemonTransport{commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
		if req.Command != daemonclient.CommandOrchestrationSnapshot {
			t.Fatalf("unexpected daemon command %q", req.Command)
		}
		return responseWithJSON(req, protocol.OrchestrationSnapshot{
			Role: "orchestrator", Scope: rooted, Lifecycle: domain.OrchestratorWorking, Revision: 7,
			Roots: []string{"az-root"}, Blocked: map[string]string{},
			Constraints: protocol.OrchestrationConstraints{Commands: []string{"az orchestrate status --root az-root"}},
		}), nil
	}})}
	output := captureStdout(t, func() error { return PrimeCommand(deps) })
	for _, want := range []string{"Active ticket ID: `az-root`", "scope=rooted:az-root", "rooted orchestrator for `az-root`", "az orchestrate status --root az-root", "Orchestrator Exit Contract (root az-root)"} {
		if !strings.Contains(output, want) {
			t.Fatalf("rooted prime output missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "stale-root") {
		t.Fatalf("rooted prime leaked environment scope: %s", output)
	}
}

func TestPrimeCommandDoesNotEmitDiagnosticsToStderr(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	setPrimeTmuxAvailable(t, false)

	taskListCalls := 0
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskGet {
					taskListCalls++
					return protocol.ResponseEnvelope{}, errors.New("snapshot still blocked")
				}
				return responseWithJSON(req, map[string]any{}), nil
			},
		}),
		ProjectID: "proj",
		Config:    config.DefaultConfig(),
	}

	stderr := captureStderr(t, func() error {
		_ = captureStdout(t, func() error {
			return PrimeCommand(deps)
		})
		return nil
	})
	if stderr != "" {
		t.Fatalf("PrimeCommand stderr = %q, want no diagnostics", stderr)
	}
	if taskListCalls != 1 {
		t.Fatalf("task snapshot calls = %d, want 1", taskListCalls)
	}
}

func TestPrimeCommandWithActiveIssueUsesTargetedSnapshot(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	t.Setenv("AZEDARACH_AUDIT_ACTOR", "agent-prime")
	setPrimeTmuxAvailable(t, false)
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	taskGetCalls := 0
	commands := []string{}
	var claimReq daemonclient.TaskOwnershipRequest
	var readinessReq struct {
		TaskID  naming.IssueID `json:"task_id"`
		ActorID string         `json:"actor_id,omitempty"`
	}
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				commands = append(commands, req.Command)
				switch req.Command {
				case daemonclient.CommandTaskGet:
					taskGetCalls++
					body, err := marshalTaskListBody([]domain.Task{{
						ID:          "az-1",
						Title:       "Prime issue",
						Description: "Targeted detail",
						Status:      domain.StatusInProgress,
						Priority:    domain.P1,
						Type:        domain.TypeBug,
						CreatedAt:   now,
						UpdatedAt:   now,
					}})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskList:
					t.Fatalf("prime with active issue must not load full task list")
				case daemonclient.CommandTaskClaimOwnership:
					if err := json.Unmarshal(req.Body, &claimReq); err != nil {
						t.Fatalf("decode ownership claim request: %v", err)
					}
					return responseWithPrimeOwnershipClaim(t, req), nil
				case daemonclient.CommandTaskGraphReadiness:
					if err := json.Unmarshal(req.Body, &readinessReq); err != nil {
						t.Fatalf("decode readiness request: %v", err)
					}
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: "az-1",
						Blocked:     map[string]string{},
					}), nil
				case protocol.CommandLearnContextualActivate:
					return responseWithJSON(req, protocol.LearnContextualActivateResponseBody{}), nil
				default:
					t.Fatalf("unexpected daemon command: %q", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		ProjectID: "proj",
		Config:    config.DefaultConfig(),
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if taskGetCalls != 1 {
		t.Fatalf("task.get calls = %d, want 1", taskGetCalls)
	}
	wantFirstCommands := []string{
		daemonclient.CommandTaskGet,
		daemonclient.CommandTaskClaimOwnership,
		daemonclient.CommandTaskGraphReadiness,
	}
	if len(commands) < len(wantFirstCommands) || !reflect.DeepEqual(commands[:len(wantFirstCommands)], wantFirstCommands) {
		t.Fatalf("first commands = %+v, want targeted get, ownership claim, actor-aware readiness", commands)
	}
	if claimReq.TaskID != "az-1" || claimReq.OwnerID != "agent-prime" || claimReq.OwnerKind != "agent" || claimReq.Force {
		t.Fatalf("claim request = %+v, want non-forced agent-prime claim for az-1", claimReq)
	}
	if readinessReq.TaskID != "az-1" || readinessReq.ActorID != "agent-prime" {
		t.Fatalf("readiness request = %+v, want actor-aware readiness for agent-prime", readinessReq)
	}
	if !strings.Contains(output, "Prime issue") {
		t.Fatalf("prime output missing targeted issue title: %q", output)
	}
	if !strings.Contains(output, "Targeted detail") {
		t.Fatalf("prime output missing targeted issue detail: %q", output)
	}
}

func TestPrimeCommandStopsOnActiveIssueOwnershipConflict(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	t.Setenv("AZEDARACH_AUDIT_ACTOR", "agent-prime")
	setPrimeTmuxAvailable(t, false)
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskGet:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:        "az-1",
						Title:     "Prime issue",
						Status:    domain.StatusInProgress,
						Priority:  domain.P1,
						Type:      domain.TypeTask,
						CreatedAt: now,
						UpdatedAt: now,
					}})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return responseWithBody(req, body), nil
				case daemonclient.CommandTaskClaimOwnership:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						OK:              false,
						Error:           &protocol.ErrorEnvelope{Code: protocol.ErrorCodeConflict, Message: "owned by agent-a"},
					}, nil
				case daemonclient.CommandTaskGraphReadiness:
					t.Fatal("prime must not continue to readiness after ownership conflict")
				case protocol.CommandLearnContextualActivate:
					t.Fatal("prime must not continue to contextual learning activation after ownership conflict")
				default:
					t.Fatalf("unexpected daemon command: %q", req.Command)
				}
				return protocol.ResponseEnvelope{}, nil
			},
		}),
		ProjectID: "proj",
		Config:    config.DefaultConfig(),
	}

	output, err := captureStdoutAllowError(t, func() error {
		return PrimeCommand(deps)
	})
	if err == nil {
		t.Fatal("PrimeCommand error = nil, want ownership conflict")
	}
	if !strings.Contains(err.Error(), "active issue az-1 is already claimed: owned by agent-a") {
		t.Fatalf("PrimeCommand error = %v, want ownership conflict", err)
	}
	if strings.Contains(output, "Azedarach Session Primer") {
		t.Fatalf("prime rendered output despite ownership conflict: %q", output)
	}
}

func TestPrimeCommandWithActiveIssueContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	parentID := naming.IssueID("az-parent")
	task := domain.Task{
		ID:              "az-1",
		Title:           "Prime issue",
		Description:     "Structured description survives prime context.",
		Notes:           "private scratch notes should stay hidden",
		Design:          "Daemon projection supplies evidence.",
		Acceptance:      "Prime shows acceptance criteria.",
		Status:          domain.StatusOpen,
		Priority:        domain.P2,
		Type:            domain.TypeTask,
		ParentID:        &parentID,
		Implementations: []string{"go-bubbletea"},
		CreatedAt:       now,
		UpdatedAt:       now,
		Dependencies: []domain.Dependency{
			{ID: "az-2", Type: domain.DependencyBlocks},
		},
	}

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{task})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskGet:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 0,
						ProjectID:        naming.ProjectID(protocol.DefaultProjectID),
						LastCheckedAt:    now,
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{task},
					}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithPrimeOwnershipClaim(t, req), nil
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: "az-1",
						Runnable:    []string{"az-1"},
						Blocked:     map[string]string{},
						WorkerObservations: []domain.WorkerObservation{{
							IssueID:         "az-1",
							State:           domain.WorkerObservationRunnable,
							Reason:          "ready to start",
							EvidenceSummary: []string{"last worker evidence was clean"},
							Risks:           []string{"missing final validation"},
							NextActions:     []string{"run az orchestrate start --root az-1 --issue az-1"},
							LastEvent: &domain.WorkerObservationEventSummary{
								Kind:    "mailbox",
								Type:    "worker-integration-ready",
								Summary: "structured evidence packet accepted",
								Seq:     7,
							},
						}},
					}), nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		ProjectID: "proj",
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Active ticket ID: `az-1`") {
		t.Fatalf("prime output missing explicit active issue id: %q", output)
	}
	if !strings.Contains(output, "Run `az spec read --issue az-1` before behavior changes") {
		t.Fatalf("prime output missing active-issue spec read command: %q", output)
	}
	if !strings.Contains(output, "Active ticket context (ticket=az-1)") {
		t.Fatalf("prime output missing active issue section: %q", output)
	}
	if !strings.Contains(output, "az-1: Prime issue [status=open priority=P2 type=task impl=go-bubbletea]") {
		t.Fatalf("prime output missing issue summary: %q", output)
	}
	if !strings.Contains(output, "Dependencies:\n- blocks: az-2") {
		t.Fatalf("prime output missing dependency summary: %q", output)
	}
	if !strings.Contains(output, "Parent: az-parent") {
		t.Fatalf("prime output missing active issue parent: %q", output)
	}
	if !strings.Contains(output, "Description: Structured description survives prime context.") {
		t.Fatalf("prime output missing structured description: %q", output)
	}
	if !strings.Contains(output, "Acceptance: Prime shows acceptance criteria.") {
		t.Fatalf("prime output missing acceptance context: %q", output)
	}
	if !strings.Contains(output, "Design: Daemon projection supplies evidence.") {
		t.Fatalf("prime output missing design context: %q", output)
	}
	if strings.Contains(output, "private scratch notes should stay hidden") {
		t.Fatalf("prime output should not include generic issue notes: %q", output)
	}
	if !strings.Contains(output, "Observation/evidence projection:") {
		t.Fatalf("prime output missing observation projection: %q", output)
	}
	if !strings.Contains(output, "az-1: runnable - ready to start") {
		t.Fatalf("prime output missing observation state/reason: %q", output)
	}
	if !strings.Contains(output, "Last event: worker-integration-ready - structured evidence packet accepted") {
		t.Fatalf("prime output missing observation event summary: %q", output)
	}
	if !strings.Contains(output, "Evidence: last worker evidence was clean") {
		t.Fatalf("prime output missing evidence summary: %q", output)
	}
	if !strings.Contains(output, "Worker coordination parent: `az-parent`") || !strings.Contains(output, "az mail list --parent az-parent --since 0 --json") {
		t.Fatalf("prime output missing worker mailbox receive guidance: %q", output)
	}
	if strings.Contains(output, "Parent context: `az-1` is an epic or has children") {
		t.Fatalf("prime output should not show parent-context recommendation for task without children: %q", output)
	}
	if strings.Contains(output, "Orchestrator Exit Contract") {
		t.Fatalf("prime output should not show root exit contract for a leaf worker: %q", output)
	}
}

func TestRenderPrimeContainmentRiskSectionShowsStaleChildBranch(t *testing.T) {
	output := renderPrimeContainmentRiskSection("fsy", []daemonclient.TaskContainmentRisk{
		{
			IssueID:                "fsy",
			RootIssueID:            "fmd",
			RootBranch:             "riordan/fmd/profile-and-worker-mater-cif-merge",
			ActiveBranch:           "riordan/fsy/reconcile",
			ClosedChildIssueID:     "frv",
			EvidenceCommit:         "67cc4c5cad123456",
			RootContainsEvidence:   true,
			ActiveContainsEvidence: false,
			Classification:         "stale_child_branch",
			Message:                "stale child branch: parent branch contains closed child evidence",
			OverlapFiles:           []string{"internal/rpc/materializer.go"},
			SuggestedCommand:       "merge or rebase parent before continuing",
		},
	})
	for _, want := range []string{
		"Stale child branch",
		"root_contains=true active_contains=false",
		"67cc4c5cad12",
		"internal/rpc/materializer.go",
		"merge or rebase parent before continuing",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestPrimeCommandShowsRootExitContractForAzOrchestrationRoot(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-root")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	rootID := naming.IssueID("az-root")
	root := domain.Task{
		ID:          rootID,
		Title:       "Root issue",
		Notes:       "notes are not the exit contract source",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeEpic,
		Description: "Root description",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	child := domain.Task{
		ID:        "az-child",
		Title:     "Child worker",
		Status:    domain.StatusInReview,
		Priority:  domain.P2,
		Type:      domain.TypeTask,
		ParentID:  &rootID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{root, child})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return responseWithBody(req, body), nil
				case daemonclient.CommandTaskGet:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 0,
						ProjectID:        naming.ProjectID(protocol.DefaultProjectID),
						LastCheckedAt:    now,
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{root, child},
					}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithPrimeOwnershipClaim(t, req), nil
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: "az-root",
						Blocked:     map[string]string{},
						WorkerObservations: []domain.WorkerObservation{{
							IssueID:         "az-child",
							State:           domain.WorkerObservationReviewReady,
							Reason:          "worker reported integration-ready evidence",
							EvidenceSummary: []string{"commands_run and key_assertions present"},
							NextActions:     []string{"inspect evidence and close accepted worker"},
						}},
					}), nil
				default:
					return responseWithJSON(req, map[string]any{}), nil
				}
			},
		}),
		ProjectID: "proj",
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	contractIndex := strings.Index(output, "Orchestrator Exit Contract (root az-root):")
	if contractIndex < 0 {
		t.Fatalf("prime output missing root exit contract: %q", output)
	}
	for _, guidance := range []string{
		"Remain in the active orchestration turn/loop after starting workers, nested orchestrators, or a background watch; startup is not a completed handoff to the human.",
		"Continuously consume the root watch and react to worker/nested-orchestrator progress, blocked, and integration-ready evidence while graph work remains.",
		"Supervise nested epic/root orchestrators as direct children while they own their descendant workers; do not flatten or take over those descendants unless explicitly requested.",
		"repeat status/start/watch/review until `az orchestrate complete-check --root az-root` passes",
	} {
		if !strings.Contains(output, guidance) {
			t.Fatalf("prime output missing persistent parent-orchestrator guidance %q: %q", guidance, output)
		}
	}
	if !strings.Contains(output, "az-child: review_ready - worker reported integration-ready evidence") {
		t.Fatalf("prime output missing dynamic child observation: %q", output)
	}
}

func TestPrimeCommandShowsRootExitContractForTaskRootWithActiveReadiness(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-root")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	root := domain.Task{
		ID:          "az-root",
		Title:       "Task root",
		Status:      domain.StatusInProgress,
		Priority:    domain.P2,
		Type:        domain.TypeTask,
		Description: "Root description",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{root})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return responseWithBody(req, body), nil
				case daemonclient.CommandTaskGet:
					return responseWithJSON(req, protocol.TaskListSnapshotPayload{
						SchemaVersion:    protocol.TaskListSnapshotSchemaVersion,
						ProtocolVersion:  protocol.CurrentVersion,
						SnapshotRevision: 0,
						ProjectID:        naming.ProjectID(protocol.DefaultProjectID),
						LastCheckedAt:    now,
						Freshness:        protocol.TaskListFreshnessFresh,
						Tasks:            []domain.Task{root},
					}), nil
				case daemonclient.CommandTaskClaimOwnership:
					return responseWithPrimeOwnershipClaim(t, req), nil
				case daemonclient.CommandTaskGraphReadiness:
					return responseWithJSON(req, daemonclient.TaskGraphReadiness{
						RootIssueID: "az-root",
						Active:      []string{"az-child"},
						ActiveSessions: []daemonclient.TaskActiveSession{{
							IssueID:        "az-child",
							Activity:       "busy",
							ActivitySource: "hooks",
							State:          "busy",
							Status:         "active",
						}},
						Blocked: map[string]string{},
					}), nil
				default:
					return responseWithJSON(req, map[string]any{}), nil
				}
			},
		}),
		ProjectID: "proj",
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	contractIndex := strings.Index(output, "Orchestrator Exit Contract (root az-root):")
	if contractIndex < 0 {
		t.Fatalf("prime output missing root exit contract for task root with active readiness: %q", output)
	}
}

func TestPrimeCommandSurfacesBoundedLearningSummaries(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	var learnReq protocol.LearnContextualActivateRequestBody
	var confirmReq protocol.LearnActivationConfirmRequestBody
	var confirmCalls int
	var abandonCalls int

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:        "az-1",
						Title:     "Prime issue",
						Status:    domain.StatusOpen,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						CreatedAt: now,
						UpdatedAt: now,
					}})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, CompletedAt: req.SentAt, Body: body}, nil
				case protocol.CommandLearnContextualActivate:
					if err := json.Unmarshal(req.Body, &learnReq); err != nil {
						t.Fatalf("decode learn recall request: %v", err)
					}
					learnings := []protocol.Learning{{
						ID:           "learn-1",
						IssueID:      naming.IssueID("az-1"),
						Summary:      "Keep durable choices in decisions",
						Evidence:     "raw evidence should not be injected",
						Status:       protocol.LearningStatusAccepted,
						RecallReason: "issue=az-1; query",
					}}
					body, err := json.Marshal(protocol.LearnContextualActivateResponseBody{Proposal: &protocol.LearningActivationProposal{ActivationID: "act-prime", LearningIDs: []string{"learn-1"}}, Learnings: learnings})
					if err != nil {
						t.Fatalf("marshal learn recall response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, CompletedAt: req.SentAt, Body: body}, nil
				case protocol.CommandLearnActivationConfirm:
					confirmCalls++
					if err := json.Unmarshal(req.Body, &confirmReq); err != nil {
						t.Fatal(err)
					}
					return responseWithJSON(req, protocol.LearnActivationConfirmResponseBody{Activation: protocol.LearningActivation{ActivationID: "act-prime"}}), nil
				case protocol.CommandLearnActivationAbandon:
					abandonCalls++
					return responseWithJSON(req, protocol.LearnActivationAbandonResponseBody{Abandoned: true}), nil
				default:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, CompletedAt: req.SentAt}, nil
				}
			},
		}).WithProjectID("proj"),
		ProjectID: "proj",
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if learnReq.ContextIssueID != naming.IssueID("az-1") {
		t.Fatalf("learn recall context issue = %q, want az-1", learnReq.ContextIssueID)
	}
	if learnReq.Purpose != string(domain.LearningPurposeSessionStart) || learnReq.Surface != "prime" || learnReq.TokenBudget != 256 {
		t.Fatalf("contextual activation request = %+v", learnReq)
	}
	if !strings.Contains(output, "Relevant accepted/promoted learnings [activation: act-prime]:") ||
		!strings.Contains(output, "- learn-1 [accepted]: Keep durable choices in decisions (why: issue=az-1; query)") {
		t.Fatalf("prime output missing learning section: %q", output)
	}
	if strings.Contains(output, "raw evidence should not be injected") {
		t.Fatalf("prime output injected raw learning evidence: %q", output)
	}
	if strings.Contains(output, "Private local handling detail") || strings.Contains(output, "private raw evidence should not be injected") {
		t.Fatalf("prime output injected private learning: %q", output)
	}
	wantSection := "\nRelevant accepted/promoted learnings [activation: act-prime]:\n- learn-1 [accepted]: Keep durable choices in decisions (why: issue=az-1; query)\nUse `az learn show <learning-id>` for evidence; long evidence is not injected by default.\nRecord the activation outcome with `az learn feedback --idempotency-key <key> --outcome helpful|followed|contradicted|unknown act-prime`."
	if confirmCalls != 1 || confirmReq.ActivationID != "act-prime" || confirmReq.TokenCost != domain.RenderedLearningTokenCost(wantSection) {
		t.Fatalf("prime confirmation=%+v calls=%d want token cost=%d", confirmReq, confirmCalls, domain.RenderedLearningTokenCost(wantSection))
	}
	if err := primeCommandTo(deps, failingHookWriter{}, clitext.Render); err == nil {
		t.Fatal("expected output write failure")
	}
	if confirmCalls != 1 {
		t.Fatalf("write failure confirmed activation: calls=%d", confirmCalls)
	}
	var rendered bytes.Buffer
	if err := primeCommandTo(deps, &rendered, func(string, any) (string, error) { return "", errors.New("render failed") }); err == nil {
		t.Fatal("expected render failure")
	}
	if confirmCalls != 1 {
		t.Fatalf("render failure confirmed activation: calls=%d", confirmCalls)
	}
	if abandonCalls != 2 {
		t.Fatalf("known prime delivery failures must abandon immediately: calls=%d", abandonCalls)
	}
}

func TestPrimeCommandShowsLearningCaptureGuidanceWithoutRecallRows(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{{
						ID:        "az-1",
						Title:     "Prime issue",
						Status:    domain.StatusOpen,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						CreatedAt: now,
						UpdatedAt: now,
					}})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, CompletedAt: req.SentAt, Body: body}, nil
				case protocol.CommandLearnContextualActivate:
					body, err := json.Marshal(protocol.LearnContextualActivateResponseBody{})
					if err != nil {
						t.Fatalf("marshal learn recall response: %v", err)
					}
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, CompletedAt: req.SentAt, Body: body}, nil
				default:
					return protocol.ResponseEnvelope{ProtocolVersion: req.ProtocolVersion, RequestID: req.RequestID, Kind: protocol.EnvelopeKindResponse, Meta: req.Meta, OK: true, CompletedAt: req.SentAt}, nil
				}
			},
		}).WithProjectID("proj"),
		ProjectID: "proj",
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if strings.Contains(output, "Relevant accepted/promoted learnings:") {
		t.Fatalf("prime output should not show recalled-learning heading without rows: %q", output)
	}
}

func TestPrimeCommandRecommendsChildIssuesForEpicContext(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	setPrimeTmuxAvailable(t, true)
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskClaimOwnership {
					return responseWithPrimeOwnershipClaim(t, req), nil
				}
				if req.Command != daemonclient.CommandTaskGet {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := marshalTaskListBody([]domain.Task{{
					ID:        "az-1",
					Title:     "Parent epic",
					Status:    domain.StatusInProgress,
					Priority:  domain.P2,
					Type:      domain.TypeEpic,
					CreatedAt: now,
					UpdatedAt: now,
				}})
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		Config: &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Parent context: `az-1` is an epic or has children") {
		t.Fatalf("prime output missing epic child-work recommendation: %q", output)
	}
	if !strings.Contains(output, "az ticket split \"Child task\"") {
		t.Fatalf("tmux-capable parent context omitted split option: %q", output)
	}
}

func TestPrimeCommandRecommendsChildIssuesWhenActiveIssueHasChildren(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	setPrimeTmuxAvailable(t, false)
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	parentID := naming.IssueID("az-1")

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskClaimOwnership {
					return responseWithPrimeOwnershipClaim(t, req), nil
				}
				if req.Command != daemonclient.CommandTaskGet {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := marshalTaskListBody([]domain.Task{
					{
						ID:        "az-1",
						Title:     "Parent task",
						Status:    domain.StatusInProgress,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						CreatedAt: now,
						UpdatedAt: now,
					},
					{
						ID:        "az-2",
						Title:     "Child task",
						Status:    domain.StatusOpen,
						Priority:  domain.P2,
						Type:      domain.TypeTask,
						ParentID:  &parentID,
						CreatedAt: now,
						UpdatedAt: now,
					},
				})
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		Config: &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Parent context: `az-1` is an epic or has children") {
		t.Fatalf("prime output missing child-work recommendation for parent task: %q", output)
	}
	if strings.Contains(output, "Parent context: `az-1` is an epic or has children. Keep implementation-sized scope in child tickets using `az ticket create \"Child task\"` for tracking-only work or `az ticket split") {
		t.Fatalf("prime output should not mention split command when tmux is unavailable: %q", output)
	}
}

func TestPrimeCommandUsesTmuxSessionContextWhenEnvMissing(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	previousTmuxPaneSessionName := tmuxPaneSessionName
	tmuxPaneSessionName = func(context.Context) (string, error) {
		return "pr-az-1", nil
	}
	t.Cleanup(func() {
		tmuxPaneSessionName = previousTmuxPaneSessionName
	})
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskClaimOwnership {
					return responseWithPrimeOwnershipClaim(t, req), nil
				}
				if req.Command != daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := marshalTaskListBody([]domain.Task{{
					ID:              "az-1",
					Title:           "Prime issue",
					Status:          domain.StatusOpen,
					Priority:        domain.P2,
					Type:            domain.TypeTask,
					Implementations: []string{"default"},
					CreatedAt:       now,
					UpdatedAt:       now,
				}})
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		ProjectID: "proj",
		RepoDir:   "/tmp/proj",
		Config:    &config.Config{Spec: config.SpecConfig{Enabled: true}},
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Active ticket ID: `az-1`") {
		t.Fatalf("prime output missing tmux-derived active issue id: %q", output)
	}
	if !strings.Contains(output, "`AZEDARACH_TICKET_ID` is absent, but the current tmux session resolves to ticket `az-1`") {
		t.Fatalf("prime output missing missing-env tmux warning: %q", output)
	}
	if !strings.Contains(output, "Active ticket context (ticket=az-1)") {
		t.Fatalf("prime output missing tmux-derived active issue section: %q", output)
	}
}

func TestPrimeCommandShowsImplementationOptionsWhenMultipleConfigured(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command != daemonclient.CommandTaskList {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := marshalTaskListBody([]domain.Task{
					{
						ID:              "az-1",
						Title:           "Default work",
						Status:          domain.StatusOpen,
						Priority:        domain.P2,
						Type:            domain.TypeTask,
						Implementations: []string{"default"},
						CreatedAt:       now,
						UpdatedAt:       now,
					},
					{
						ID:              "az-2",
						Title:           "Marketing work",
						Status:          domain.StatusOpen,
						Priority:        domain.P2,
						Type:            domain.TypeTask,
						Implementations: []string{"marketing"},
						CreatedAt:       now,
						UpdatedAt:       now,
					},
				})
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "Implementation selection (multi-implementation project):") {
		t.Fatalf("prime output missing implementation selection block: %q", output)
	}
	if !strings.Contains(output, "Available implementations: `default`, `marketing`") {
		t.Fatalf("prime output missing available implementation options: %q", output)
	}
}

func TestPrimeCommandTruncatesLargeIssueDescription(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "az-1")
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)
	longDescription := strings.Repeat("line content for noisy transcript output\n", 40)

	deps := &Dependencies{
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if req.Command == daemonclient.CommandTaskClaimOwnership {
					return responseWithPrimeOwnershipClaim(t, req), nil
				}
				if req.Command != daemonclient.CommandTaskGet {
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
				body, err := marshalTaskListBody([]domain.Task{{
					ID:              "az-1",
					Title:           "Prime issue",
					Description:     longDescription,
					Status:          domain.StatusOpen,
					Priority:        domain.P2,
					Type:            domain.TypeTask,
					Implementations: []string{"default"},
					CreatedAt:       now,
					UpdatedAt:       now,
				}})
				if err != nil {
					t.Fatalf("marshal task list: %v", err)
				}
				return protocol.ResponseEnvelope{
					ProtocolVersion: req.ProtocolVersion,
					RequestID:       req.RequestID,
					Kind:            protocol.EnvelopeKindResponse,
					Meta:            req.Meta,
					OK:              true,
					CompletedAt:     req.SentAt,
					Body:            body,
				}, nil
			},
		}),
		ProjectID: "proj",
	}

	output := captureStdout(t, func() error {
		return PrimeCommand(deps)
	})

	if !strings.Contains(output, "… (truncated; run `az ticket get az-1` for full context)") {
		t.Fatalf("prime output should include truncated description sentinel: %q", output)
	}
	if strings.Count(output, "line content for noisy transcript output") >= 12 {
		t.Fatalf("prime output should not include full long description: %q", output)
	}
}

func TestPrimeCommandWarnsWhenActiveIssueClosed(t *testing.T) {
	now := time.Date(2026, 3, 26, 11, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		id     naming.IssueID
		status domain.Status
		want   string
	}{
		{name: "completed", id: "az-closed", status: domain.StatusDone, want: "Active ticket `az-closed` is currently `closed`"},
		{name: "cancelled", id: "az-cancelled", status: domain.StatusCancelled, want: "Active ticket `az-cancelled` is currently `cancelled`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AZEDARACH_ISSUE_ID", tt.id.String())
			deps := &Dependencies{
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
						if req.Command == daemonclient.CommandTaskClaimOwnership {
							t.Fatal("prime must not claim a closed active issue")
						}
						if req.Command != daemonclient.CommandTaskGet {
							return protocol.ResponseEnvelope{
								ProtocolVersion: req.ProtocolVersion,
								RequestID:       req.RequestID,
								Kind:            protocol.EnvelopeKindResponse,
								Meta:            req.Meta,
								OK:              true,
								CompletedAt:     req.SentAt,
							}, nil
						}
						body, err := marshalTaskListBody([]domain.Task{{
							ID:              tt.id,
							Title:           "Closed issue",
							Status:          tt.status,
							Priority:        domain.P2,
							Type:            domain.TypeTask,
							Implementations: []string{"go-bubbletea"},
							CreatedAt:       now,
							UpdatedAt:       now,
						}})
						if err != nil {
							t.Fatalf("marshal task list: %v", err)
						}
						return protocol.ResponseEnvelope{
							ProtocolVersion: req.ProtocolVersion,
							RequestID:       req.RequestID,
							Kind:            protocol.EnvelopeKindResponse,
							Meta:            req.Meta,
							OK:              true,
							CompletedAt:     req.SentAt,
							Body:            body,
						}, nil
					},
				}),
				ProjectID: "proj",
			}

			output := captureStdout(t, func() error {
				return PrimeCommand(deps)
			})

			if !strings.Contains(output, tt.want) {
				t.Fatalf("prime output missing closed-issue warning: %q", output)
			}
		})
	}
}

func TestPrimeCommandQuestionFirstAndSpecBlock(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	t.Setenv("AZEDARACH_PRIME_MODE", "question-first")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{Config: &config.Config{Spec: config.SpecConfig{Enabled: true}}})
	})
	if !strings.Contains(output, "Question-first execution rules") {
		t.Fatal("prime output missing dynamic question-first section")
	}
	if !strings.Contains(output, "Spec Workflow") {
		t.Fatal("prime output missing dynamic enabled-spec section")
	}
}

func TestPrimeCommandExplainsOriginWorkflowAndCrossProjectHandoff(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	cfg := config.DefaultConfig()
	cfg.Git.WorkflowMode = "origin"

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{Config: cfg})
	})

	for _, want := range []string{
		"Active git workflow mode: `origin`",
		"Root integration is PR-only",
		"git push -u origin HEAD",
		"az pr create --issue <root>",
		"az pr status --issue <root>",
		"az pr merge --issue <root> --confirm",
		"fetch `origin/<base>`",
		"az ticket close --id <root>",
		"az session start --project <project> <ticket>",
		"do not treat `az worktree create` as a worker handoff",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("origin primer missing %q:\n%s", want, output)
		}
	}
}

func TestPrimeCommandSpecBlockDisabled(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")

	output := captureStdout(t, func() error {
		return PrimeCommand(&Dependencies{Config: &config.Config{Spec: config.SpecConfig{Enabled: false}}})
	})

	if strings.Contains(output, "Spec Workflow") {
		t.Fatalf("prime output should not include spec workflow block when disabled: %q", output)
	}
}

func TestPrimeCommandOrchestrationModes(t *testing.T) {
	t.Setenv("AZEDARACH_ISSUE_ID", "")
	tests := []struct {
		name        string
		via         string
		tmux        bool
		want        string
		notWant     string
		forbidStart bool
	}{
		{name: "native with tmux", via: "native", tmux: true, want: "- Mode is `native`:", notWant: "- Mode is `az`:"},
		{name: "az with tmux", via: "az", tmux: true, want: "- Mode is `az`:", notWant: "- Mode is `native`:"},
		{name: "az without tmux", via: "az", tmux: false, want: "- Mode is `az`, but", notWant: "- Mode is `native`:", forbidStart: true},
		{name: "native without tmux", via: "native", tmux: false, want: "- Mode is `native`:", notWant: "- Mode is `az`:", forbidStart: true},
		{name: "unsupported", via: "banana", tmux: true, want: "Unsupported `orchestration.via` value: `banana`", notWant: "- Mode is `native`:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setPrimeTmuxAvailable(t, tt.tmux)
			output := captureStdout(t, func() error {
				return PrimeCommand(&Dependencies{Config: &config.Config{Spec: config.SpecConfig{Enabled: true}, Orchestration: config.OrchestrationConfig{Via: tt.via}}})
			})
			if !strings.Contains(output, tt.want) {
				t.Fatalf("prime output missing dynamic mode marker %q", tt.want)
			}
			if tt.notWant != "" && strings.Contains(output, tt.notWant) {
				t.Fatalf("prime output included incompatible mode marker %q", tt.notWant)
			}
			if tt.forbidStart && strings.Contains(output, "az orchestrate start") {
				t.Fatal("prime output exposed CLI-managed start while tmux unavailable")
			}
		})
	}
}

type fakeLauncher struct {
	startErr      error
	stopErr       error
	replaceErr    error
	startCalled   bool
	stopCalled    bool
	replaceCalled bool
}

func (f *fakeLauncher) Start(context.Context) error {
	f.startCalled = true
	return f.startErr
}

func (f *fakeLauncher) Stop(context.Context) error {
	f.stopCalled = true
	return f.stopErr
}

func (f *fakeLauncher) Replace(context.Context) error {
	f.replaceCalled = true
	return f.replaceErr
}

type timeoutBudgetLauncher struct {
	minBudget time.Duration
	started   *bool
}

func (l *timeoutBudgetLauncher) Start(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fmt.Errorf("wait for daemon socket readiness: %w", context.DeadlineExceeded)
	}
	if time.Until(deadline) < l.minBudget {
		return fmt.Errorf("wait for daemon socket readiness: %w", context.DeadlineExceeded)
	}
	if l.started != nil {
		*l.started = true
	}
	return nil
}

func (l *timeoutBudgetLauncher) Replace(ctx context.Context) error {
	return l.Start(ctx)
}

func (l *timeoutBudgetLauncher) Stop(context.Context) error { return nil }

func TestIssueCreateCommandUsesExtendedDaemonAttachTimeout(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	started := false
	newLauncher = func(_, _ string) daemonStarter {
		return &timeoutBudgetLauncher{
			minBudget: 8 * time.Second,
			started:   &started,
		}
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				if !started {
					return protocol.HelloAck{}, errors.New("daemon socket unavailable")
				}
				return protocol.HelloAck{Accepted: true}, nil
			},
			commandFn: func(_ context.Context, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
				if !started {
					return protocol.ResponseEnvelope{}, errors.New("daemon socket unavailable")
				}
				switch req.Command {
				case daemonclient.CommandTaskList:
					body, err := marshalTaskListBody([]domain.Task{
						{ID: "az-1", Implementations: []string{"default"}},
					})
					if err != nil {
						t.Fatalf("marshal task list: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				case daemonclient.CommandTaskCreate:
					body, err := json.Marshal(map[string]string{"task_id": "az-timeout"})
					if err != nil {
						t.Fatalf("marshal create response: %v", err)
					}
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
						Body:            body,
					}, nil
				default:
					return protocol.ResponseEnvelope{
						ProtocolVersion: req.ProtocolVersion,
						RequestID:       req.RequestID,
						Kind:            protocol.EnvelopeKindResponse,
						Meta:            req.Meta,
						OK:              true,
						CompletedAt:     req.SentAt,
					}, nil
				}
			},
		}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID: "proj",
	}
	deps.AutostartRetryPolicy = &autoclient.AutostartRetryPolicy{}
	deps.DaemonClient.WithReconnectPolicy(reconnect.Policy{MaxAttempts: 1})

	err := IssueCreateCommand(deps, IssueCreateOptions{
		Title:           "timeout budget",
		Type:            domain.TypeTask,
		Priority:        domain.P2,
		Implementations: []string{"default"},
	})
	if err != nil {
		t.Fatalf("IssueCreateCommand() error = %v", err)
	}
}

func TestRestartDaemonCommand(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
		}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	output := captureStdout(t, func() error {
		return RestartDaemonCommand(deps)
	})

	if !fake.replaceCalled {
		t.Fatalf("expected replace to be called")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if !strings.Contains(output, "Daemon restarted successfully.") {
		t.Fatalf("output missing restart success: %q", output)
	}
}

func TestStartDaemonCommand(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				return protocol.HelloAck{Accepted: true}, nil
			},
		}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	output := captureStdout(t, func() error {
		return StartDaemonCommand(deps)
	})

	if !fake.startCalled {
		t.Fatalf("expected start to be called")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if !strings.Contains(output, "Daemon started successfully.") {
		t.Fatalf("output missing start success: %q", output)
	}
}

func TestStartDaemonCommandStartFailure(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{startErr: errors.New("boom")}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      t.TempDir(),
	}

	err := StartDaemonCommand(deps)
	if err == nil || !strings.Contains(err.Error(), "start daemon: boom") {
		t.Fatalf("error = %v, want start daemon boom", err)
	}
}

func TestDaemonStartAndRestartRejectDaemonThatDoesNotRemainAttachable(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	tests := []struct {
		name string
		run  func(*Dependencies) error
	}{
		{name: "start", run: StartDaemonCommand},
		{name: "restart", run: RestartDaemonCommand},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeLauncher{}
			newLauncher = func(_, _ string) daemonStarter { return fake }

			handshakes := 0
			deps := &Dependencies{
				Config: config.DefaultConfig(),
				DaemonClient: daemonclient.New(&fakeDaemonTransport{
					handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
						handshakes++
						if handshakes == 1 {
							return protocol.HelloAck{Accepted: true}, nil
						}
						return protocol.HelloAck{}, &net.OpError{Op: "dial", Net: "unix", Err: os.ErrNotExist}
					},
				}),
				Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
				ProjectID: "proj",
				RepoDir:   t.TempDir(),
			}
			deps.AutostartRetryPolicy = &autoclient.AutostartRetryPolicy{}

			err := tt.run(deps)
			if err == nil || !strings.Contains(err.Error(), "stable") {
				t.Fatalf("daemon %s error = %v, want stable attach failure", tt.name, err)
			}
			if handshakes < 2 {
				t.Fatalf("handshakes = %d, want at least 2", handshakes)
			}
		})
	}
}

func TestRestartDaemonCommandReplaceFailure(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{replaceErr: errors.New("boom")}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      t.TempDir(),
	}

	err := RestartDaemonCommand(deps)
	if err == nil || !strings.Contains(err.Error(), "restart daemon: boom") {
		t.Fatalf("error = %v, want restart daemon boom", err)
	}
}

func TestStopDaemonCommand(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	deps := &Dependencies{
		Config:         config.DefaultConfig(),
		DaemonClient:   daemonclient.New(&fakeDaemonTransport{}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	output := captureStdout(t, func() error {
		return StopDaemonCommand(deps)
	})

	if !fake.stopCalled {
		t.Fatalf("expected stop to be called")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if !strings.Contains(output, "Daemon stopped successfully.") {
		t.Fatalf("output missing stop success: %q", output)
	}
}

func TestStopDaemonCommandFailure(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{stopErr: errors.New("boom")}
	newLauncher = func(_, _ string) daemonStarter {
		return fake
	}

	deps := &Dependencies{
		Config:       config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{}),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:    "proj",
		RepoDir:      t.TempDir(),
	}

	err := StopDaemonCommand(deps)
	if err == nil || !strings.Contains(err.Error(), "stop daemon: boom") {
		t.Fatalf("error = %v, want stop daemon boom", err)
	}
}

func TestEnsureDaemonDoesNotReplaceOnAcceptedHandshake(t *testing.T) {
	oldLauncher := newLauncher
	t.Cleanup(func() { newLauncher = oldLauncher })

	fake := &fakeLauncher{}
	var gotRepoDir string
	newLauncher = func(repoDir, _ string) daemonStarter {
		gotRepoDir = repoDir
		return fake
	}

	handshakes := 0
	deps := &Dependencies{
		Config: config.DefaultConfig(),
		DaemonClient: daemonclient.New(&fakeDaemonTransport{
			handshakeFn: func(context.Context, protocol.Hello) (protocol.HelloAck, error) {
				handshakes++
				return protocol.HelloAck{Accepted: true}, nil
			},
		}),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ProjectID:      "proj",
		RepoDir:        t.TempDir(),
		RuntimeRepoDir: "/tmp/runtime-worktree",
	}

	if err := ensureDaemon(context.Background(), deps, "cli"); err != nil {
		t.Fatalf("ensureDaemon() error = %v", err)
	}
	if fake.replaceCalled {
		t.Fatalf("expected replace to remain false for accepted handshake")
	}
	if gotRepoDir != deps.RuntimeRepoDir {
		t.Fatalf("launcher repoDir = %q, want %q", gotRepoDir, deps.RuntimeRepoDir)
	}
	if handshakes != 1 {
		t.Fatalf("handshakes = %d, want 1", handshakes)
	}
}

func responseWithOutput(req protocol.RequestEnvelope, output string) protocol.ResponseEnvelope {
	payload, err := json.Marshal(commandOutputBody{Output: output})
	if err != nil {
		panic(err)
	}
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     req.SentAt,
		OK:              true,
		Body:            payload,
	}
}

func responseWithJSON(req protocol.RequestEnvelope, body any) protocol.ResponseEnvelope {
	payload, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return responseWithBody(req, payload)
}

func responseWithPrimeOwnershipClaim(t *testing.T, req protocol.RequestEnvelope) protocol.ResponseEnvelope {
	t.Helper()
	var body daemonclient.TaskOwnershipRequest
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode prime ownership claim: %v", err)
	}
	if body.TaskID == "" {
		t.Fatal("prime ownership claim missing task_id")
	}
	if strings.TrimSpace(body.OwnerID) == "" {
		t.Fatal("prime ownership claim missing owner_id")
	}
	if body.OwnerKind != "agent" {
		t.Fatalf("prime ownership claim owner_kind = %q, want agent", body.OwnerKind)
	}
	if body.Force {
		t.Fatal("prime ownership claim must not force takeover")
	}
	return responseWithJSON(req, domain.Task{
		ID: body.TaskID,
		Ownership: &domain.IssueOwnership{
			OwnerID:   body.OwnerID,
			OwnerKind: body.OwnerKind,
		},
	})
}

func contextRiskTestPacket() domain.IssueContextRiskPacket {
	return domain.IssueContextRiskPacket{
		IssueID:           "az-1",
		ParentIssueID:     "az-root",
		Level:             domain.IssueContextRiskHigh,
		Confidence:        80,
		CandidateCount:    4,
		OverlapIssueCount: 3,
		RelatedIssueIDs:   []string{"az-2", "az-3", "az-4"},
		Signals: []string{
			`file overlap "internal/daemon/task_commands.go" with az-2, az-3, az-4`,
			"az-2 has recorded risk evidence",
			"az-3 has recorded risk evidence",
			"az-4 has recorded risk evidence",
		},
		CloseoutPrompts: []string{"Record a diagnosis or structured risk note before marking this issue ready for closeout."},
		HandoffFields: domain.IssueContextHandoffFields{
			StructuredFields: []string{"files_changed", "root_cause", "invariant", "changed_symbols", "tests_changed", "related_consumers_audited", "regression_validation"},
			Missing:          []string{"root_cause", "invariant", "regression_validation"},
		},
		Evidence: []domain.IssueContextRiskEvidence{
			{IssueID: "az-1", Relationship: "target", Files: []string{"internal/daemon/task_commands.go"}},
			{IssueID: "az-2", Relationship: "sibling", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure"}},
			{IssueID: "az-3", Relationship: "sibling", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure"}},
			{IssueID: "az-4", Relationship: "sibling", Files: []string{"internal/daemon/task_commands.go"}, RiskNotes: []string{"same failure"}},
		},
	}
}

func newCommandTraceContext(t *testing.T) (context.Context, *tracetest.SpanRecorder, oteltrace.SpanID, func()) {
	t.Helper()
	t.Setenv(latencytrace.EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	latencytrace.SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	ctx, span := otel.Tracer("cli_commands_test").Start(context.Background(), "cli.command")
	return ctx, recorder, span.SpanContext().SpanID(), func() {
		span.End()
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		latencytrace.SetConfigEnabled(false)
	}
}

func findCommandSpan(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("ended span %q not found; got %d spans", name, len(recorder.Ended()))
	return nil
}

type commandRecordedSpanAttrs struct {
	strings map[string]string
	bools   map[string]bool
}

func commandSpanAttrs(span sdktrace.ReadOnlySpan) commandRecordedSpanAttrs {
	out := commandRecordedSpanAttrs{
		strings: map[string]string{},
		bools:   map[string]bool{},
	}
	for _, attr := range span.Attributes() {
		switch attr.Value.Type().String() {
		case "STRING":
			out.strings[string(attr.Key)] = attr.Value.AsString()
		case "BOOL":
			out.bools[string(attr.Key)] = attr.Value.AsBool()
		}
	}
	return out
}

func responseWithBody(req protocol.RequestEnvelope, body []byte) protocol.ResponseEnvelope {
	return protocol.ResponseEnvelope{
		ProtocolVersion: req.ProtocolVersion,
		RequestID:       req.RequestID,
		Kind:            protocol.EnvelopeKindResponse,
		Meta:            req.Meta,
		CompletedAt:     req.SentAt,
		OK:              true,
		Body:            body,
	}
}

func commandNames(requests []protocol.RequestEnvelope) []string {
	out := make([]string, 0, len(requests))
	for _, req := range requests {
		out = append(out, req.Command)
	}
	return out
}

func mustJSON(t *testing.T, body any) json.RawMessage {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal json body: %v", err)
	}
	return payload
}

func mustSnapshotPayloadJSON(t *testing.T, payload protocol.SnapshotPayload) []byte {
	t.Helper()

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal snapshot payload: %v", err)
	}
	return data
}

func mustApplyResultBody(t *testing.T, summary applyExecutionSummaryBody) []byte {
	t.Helper()

	data, err := json.Marshal(applyExecutionResultBody{Summary: summary})
	if err != nil {
		t.Fatalf("marshal apply result body: %v", err)
	}
	return data
}

type applyDryRunPreviewBody struct {
	SchemaVersion    uint16                            `json:"schema_version"`
	SnapshotRevision uint64                            `json:"snapshot_revision"`
	DryRun           bool                              `json:"dry_run"`
	Operations       []applyDryRunPreviewOperationBody `json:"operations"`
}

type applyDryRunPreviewOperationBody struct {
	Index   int             `json:"index"`
	Command string          `json:"command"`
	Body    json.RawMessage `json:"body,omitempty"`
}

func mustApplyDryRunPreviewBody(t *testing.T, preview applyDryRunPreviewBody) []byte {
	t.Helper()

	data, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("marshal dry-run preview body: %v", err)
	}
	return data
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- fn()
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)

	os.Stdout = oldStdout
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stdout: %v", copyErr)
	}
	if runErr != nil {
		t.Fatalf("command error: %v", runErr)
	}

	return buf.String()
}

func setPrimeTmuxAvailable(t *testing.T, available bool) {
	t.Helper()
	previous := primeLookPath
	primeLookPath = func(file string) (string, error) {
		if file != "tmux" {
			return "", errors.New("unexpected lookup: " + file)
		}
		if available {
			return "/usr/bin/tmux", nil
		}
		return "", errors.New("tmux not found")
	}
	t.Cleanup(func() {
		primeLookPath = previous
	})
}

func captureStderr(t *testing.T, fn func() error) string {
	t.Helper()

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- fn()
		_ = w.Close()
	}()

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, r)

	os.Stderr = oldStderr
	runErr := <-resultCh
	if copyErr != nil {
		t.Fatalf("copy stderr: %v", copyErr)
	}
	if runErr != nil {
		t.Fatalf("command error: %v", runErr)
	}

	return buf.String()
}
