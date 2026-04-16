package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type prGenerationRequest struct {
	Worktree         string
	IssueID          string
	IssueTitle       string
	IssueDescription string
	Branch           string
	BaseBranch       string
	Tool             string
}

type prGeneratedContent struct {
	Title string
	Body  string
}

var generatePRContent = generatePRContentWithConfiguredTool

func generatePRContentWithConfiguredTool(ctx context.Context, req prGenerationRequest) (prGeneratedContent, error) {
	tool := strings.ToLower(strings.TrimSpace(req.Tool))
	if tool == "" {
		tool = "codex"
	}

	switch tool {
	case "codex":
		return generatePRContentWithCodex(ctx, req)
	default:
		if _, err := exec.LookPath("codex"); err == nil {
			return generatePRContentWithCodex(ctx, req)
		}
		return prGeneratedContent{}, fmt.Errorf("automatic PR generation requires codex; configured cliTool=%q", tool)
	}
}

func generatePRContentWithCodex(ctx context.Context, req prGenerationRequest) (prGeneratedContent, error) {
	worktree := strings.TrimSpace(req.Worktree)
	if worktree == "" {
		worktree = "."
	}
	baseBranch := strings.TrimSpace(req.BaseBranch)
	if baseBranch == "" {
		baseBranch = "main"
	}

	commitLog := "(derive from repository context)"
	diffStat := "(derive from repository context)"

	tmpDir, err := os.MkdirTemp("", "az-pr-gen-*")
	if err != nil {
		return prGeneratedContent{}, fmt.Errorf("create temp dir for PR generation: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	schemaPath := filepath.Join(tmpDir, "schema.json")
	outputPath := filepath.Join(tmpDir, "output.json")
	schema := `{"type":"object","additionalProperties":false,"required":["title","body"],"properties":{"title":{"type":"string","minLength":1,"maxLength":200},"body":{"type":"string","minLength":1,"maxLength":12000}}}`
	if err := os.WriteFile(schemaPath, []byte(schema), 0o600); err != nil {
		return prGeneratedContent{}, fmt.Errorf("write PR generation schema: %w", err)
	}

	prompt := buildPRGenerationPrompt(req, baseBranch, commitLog, diffStat)
	cmd := exec.CommandContext(ctx,
		"codex",
		"exec",
		"--cd", worktree,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--output-schema", schemaPath,
		"--output-last-message", outputPath,
		prompt,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return prGeneratedContent{}, fmt.Errorf("generate PR title/body with codex: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return prGeneratedContent{}, fmt.Errorf("read codex PR generation output: %w", err)
	}

	var generated prGeneratedContent
	if err := json.Unmarshal(raw, &generated); err != nil {
		return prGeneratedContent{}, fmt.Errorf("parse codex PR generation output: %w", err)
	}

	generated.Title = strings.TrimSpace(generated.Title)
	generated.Body = strings.TrimSpace(generated.Body)
	if generated.Title == "" || generated.Body == "" {
		return prGeneratedContent{}, fmt.Errorf("codex returned empty PR title/body")
	}

	return generated, nil
}

func buildPRGenerationPrompt(req prGenerationRequest, baseBranch, commitLog, diffStat string) string {
	issueTitle := strings.TrimSpace(req.IssueTitle)
	issueDescription := strings.TrimSpace(req.IssueDescription)
	if issueTitle == "" {
		issueTitle = "(unknown)"
	}
	if issueDescription == "" {
		issueDescription = "(none)"
	}

	return fmt.Sprintf(`Generate GitHub pull request metadata.

Return strict JSON with this exact shape:
{
  "title": "<single line>",
  "body": "<markdown body>"
}

Requirements:
- Keep title <= 72 chars and descriptive.
- Body must be valid GitHub markdown with sections: Summary, Changes, Testing.
- Be factual from the provided context only.
- Do not include JSON fences or extra keys.

Issue:
- ID: %s
- Title: %s
- Description: %s

Git context:
- Base branch: %s
- Head branch: %s
- Commits (%s..HEAD):
%s

- Diff stat (%s...HEAD):
%s
`,
		req.IssueID,
		issueTitle,
		issueDescription,
		baseBranch,
		strings.TrimSpace(req.Branch),
		baseBranch,
		commitLog,
		baseBranch,
		diffStat,
	)
}
