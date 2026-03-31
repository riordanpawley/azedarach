package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/issues"
)

func TestIssueSpecServiceReadResolvesExternalCodeSelector(t *testing.T) {
	ctx := context.Background()
	client := newTestIssueClient(t)

	issueID, err := client.Create(ctx, issues.CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	ext := "EXT-123"
	_, err = client.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID:      "REQ-LOCAL",
		ExternalCode: &ext,
		Title:        "requirement",
	})
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}

	_, err = client.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: ext,
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	service := issueSpecService{client: client}
	out, err := service.Read(ctx, protocol.SpecReadRequestBody{
		IssueID: issueID,
		ReqID:   ext,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(out.Requirements) != 1 {
		t.Fatalf("requirements len = %d, want 1", len(out.Requirements))
	}
	if out.Requirements[0].ID != "REQ-LOCAL" {
		t.Fatalf("requirement id = %q, want REQ-LOCAL", out.Requirements[0].ID)
	}
	if len(out.Links) != 1 {
		t.Fatalf("links len = %d, want 1", len(out.Links))
	}
	if out.Links[0].ReqID != "REQ-LOCAL" {
		t.Fatalf("link req_id = %q, want REQ-LOCAL", out.Links[0].ReqID)
	}
}

func TestIssueSpecServiceLintDoesNotFailOnOverlappingLocalAndExternalCodes(t *testing.T) {
	ctx := context.Background()
	client := newTestIssueClient(t)

	issueID, err := client.Create(ctx, issues.CreateTaskParams{
		Title:    "implementation issue",
		Type:     domain.TypeTask,
		Priority: domain.P2,
	})
	if err != nil {
		t.Fatalf("create issue: %v", err)
	}

	_, err = client.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID: "REQ-A",
		Title:   "requirement A",
	})
	if err != nil {
		t.Fatalf("create requirement A: %v", err)
	}

	_, err = client.AddSpecLink(ctx, issues.AddSpecLinkParams{
		IssueID:       issueID,
		RequirementID: "REQ-A",
		Role:          issues.LinkRoleImplements,
	})
	if err != nil {
		t.Fatalf("add spec link: %v", err)
	}

	ext := "REQ-A"
	_, err = client.CreateRequirement(ctx, issues.CreateRequirementParams{
		LocalID:      "REQ-B",
		ExternalCode: &ext,
		Title:        "requirement B",
	})
	if err != nil {
		t.Fatalf("create requirement B: %v", err)
	}

	service := issueSpecService{client: client}
	out, err := service.Lint(ctx, protocol.SpecLintRequestBody{})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	if out.OK {
		t.Fatalf("lint OK = true, want false due to unlinked REQ-B")
	}
	if len(out.Diagnostics) == 0 {
		t.Fatalf("diagnostics empty, want unlinked requirement diagnostic")
	}
}

func newTestIssueClient(t *testing.T) *issues.Client {
	t.Helper()
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".azedarach"), 0o755); err != nil {
		t.Fatalf("mkdir .azedarach: %v", err)
	}
	client := issues.NewClient(repoDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = client.CloseDB() })
	return client
}
