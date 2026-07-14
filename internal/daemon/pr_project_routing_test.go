package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	daemonhandlers "github.com/riordanpawley/azedarach/internal/daemon/handlers"
	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestDaemonPRCommandExecutesInSelectedProjectRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AZEDARACH_DISABLE_USER_DB", "1")
	defaultRepo := filepath.Join(home, "default-repo")
	selectedRepo := filepath.Join(home, "selected-repo")
	for _, repoDir := range []string{defaultRepo, selectedRepo} {
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("create repository fixture: %v", err)
		}
	}
	defaultID, err := appconfig.ProjectIDForRoot(defaultRepo)
	if err != nil {
		t.Fatalf("default project ID: %v", err)
	}
	selectedID, err := appconfig.ProjectIDForRoot(selectedRepo)
	if err != nil {
		t.Fatalf("selected project ID: %v", err)
	}
	if err := appconfig.SaveProjectsRegistry(&appconfig.ProjectsRegistry{
		DefaultProject: "Default",
		Projects: []appconfig.Project{
			{ID: defaultID, Name: "Default", Path: defaultRepo},
			{ID: selectedID, Name: "Selected", Path: selectedRepo},
		},
	}); err != nil {
		t.Fatalf("save projects registry: %v", err)
	}

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create fake gh bin dir: %v", err)
	}
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\npwd > \"$PR_PWD_FILE\"\nprintf '[]'\n"), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	pwdFile := filepath.Join(home, "gh-pwd")
	t.Setenv("PR_PWD_FILE", pwdFile)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := New(Config{RepoDir: defaultRepo, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(d.closeIssueClients)
	request := protocol.RequestEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		RequestID:       "pr-selected-project",
		Kind:            protocol.EnvelopeKindCommand,
		Command:         daemonhandlers.CommandPRList,
		Meta:            protocol.Metadata{ProjectID: naming.ProjectID(selectedID)},
		Body:            []byte(`{"state":"all","limit":20}`),
		SentAt:          time.Now().UTC(),
	}
	resp, err := d.command(context.Background(), request)
	if err != nil {
		t.Fatalf("PR command transport error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("PR command response error: %+v", resp.Error)
	}
	commandDir, err := os.ReadFile(pwdFile)
	if err != nil {
		t.Fatalf("read fake gh working directory: %v", err)
	}
	wantDir, _ := filepath.EvalSymlinks(selectedRepo)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(commandDir)))
	if filepath.Clean(gotDir) != filepath.Clean(wantDir) {
		t.Fatalf("gh working directory = %q, want selected repository %q", gotDir, wantDir)
	}

	if err := os.Remove(pwdFile); err != nil {
		t.Fatalf("reset fake gh evidence: %v", err)
	}
	request.RequestID = "pr-unknown-project"
	request.Meta.ProjectID = naming.ProjectID("unknown-project")
	resp, err = d.command(context.Background(), request)
	if err != nil {
		t.Fatalf("unknown-project transport error: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrorCodeInvalidRequest || !strings.Contains(resp.Error.Message, "refusing repository fallback") {
		t.Fatalf("unknown-project response = %+v", resp)
	}
	if _, err := os.Stat(pwdFile); !os.IsNotExist(err) {
		t.Fatalf("fake gh executed for unknown project: stat error=%v", err)
	}
}
