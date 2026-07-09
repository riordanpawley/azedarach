package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riordanpawley/azedarach/internal/latencytrace"
	"github.com/riordanpawley/azedarach/internal/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestResolveProjectRootReturnsBaseRepoForWorktreePath(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	got, err := ResolveProjectRoot(nested)
	if err != nil {
		t.Fatalf("ResolveProjectRoot() error = %v", err)
	}
	if got != repo {
		t.Fatalf("ResolveProjectRoot() = %q, want %q", got, repo)
	}
}

func TestResolveProjectRootFallsBackToAbsolutePathOutsideGit(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}

	got, err := ResolveProjectRoot(nested)
	if err != nil {
		t.Fatalf("ResolveProjectRoot() error = %v", err)
	}
	if got != nested {
		t.Fatalf("ResolveProjectRoot() = %q, want %q", got, nested)
	}
}

func TestResolveBaseGitRootContextParentsSuccessfulGitExecSpan(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	nested := filepath.Join(repo, "nested")
	cmd := exec.Command("git", "init", repo)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	cmd = exec.Command("git", "-C", nested, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git rev-parse precondition: %v\n%s", err, output)
	}

	ctx, recorder, parentSpanID, endParent := newGitRootTraceContext(t)
	got, err := ResolveBaseGitRootContext(ctx, nested)
	endParent()
	if err != nil {
		t.Fatalf("ResolveBaseGitRootContext() error = %v", err)
	}
	if !sameTestPath(got, repo) {
		t.Fatalf("ResolveBaseGitRootContext() = %q, want %q", got, repo)
	}

	span := findEndedSpan(t, recorder, "dependency.git_root")
	if span.Parent().SpanID() != parentSpanID {
		t.Fatalf("git_root parent span = %s, want %s", span.Parent().SpanID(), parentSpanID)
	}
	attrs := spanAttrs(span)
	if attrs.strings["outcome"] != "success" {
		t.Fatalf("outcome = %q, want success", attrs.strings["outcome"])
	}
	if attrs.bools["error"] {
		t.Fatal("git_root span has error=true for successful git exec")
	}
}

func TestResolveBaseGitRootContextMarksGitExecFallbackWithoutSpanError(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "nested")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	t.Setenv("PATH", "")

	ctx, recorder, parentSpanID, endParent := newGitRootTraceContext(t)
	got, err := ResolveBaseGitRootContext(ctx, nested)
	endParent()
	if err != nil {
		t.Fatalf("ResolveBaseGitRootContext() error = %v", err)
	}
	if got != repo {
		t.Fatalf("ResolveBaseGitRootContext() = %q, want %q", got, repo)
	}

	span := findEndedSpan(t, recorder, "dependency.git_root")
	if span.Parent().SpanID() != parentSpanID {
		t.Fatalf("git_root parent span = %s, want %s", span.Parent().SpanID(), parentSpanID)
	}
	attrs := spanAttrs(span)
	if attrs.strings["outcome"] != "fallback" {
		t.Fatalf("outcome = %q, want fallback", attrs.strings["outcome"])
	}
	if attrs.bools["error"] {
		t.Fatal("git_root span has error=true for marker fallback")
	}
	if got := span.Status().Code; got == codes.Error {
		t.Fatal("git_root span status = Error for marker fallback")
	}
}

func TestResolveBaseGitRootContextMarksTrueFailureSpanError(t *testing.T) {
	startDir := t.TempDir()
	t.Setenv("PATH", "")

	ctx, recorder, parentSpanID, endParent := newGitRootTraceContext(t)
	if _, err := ResolveBaseGitRootContext(ctx, startDir); err == nil {
		t.Fatal("ResolveBaseGitRootContext() error = nil, want failure")
	}
	endParent()

	span := findEndedSpan(t, recorder, "dependency.git_root")
	if span.Parent().SpanID() != parentSpanID {
		t.Fatalf("git_root parent span = %s, want %s", span.Parent().SpanID(), parentSpanID)
	}
	attrs := spanAttrs(span)
	if attrs.strings["outcome"] != "failure" {
		t.Fatalf("outcome = %q, want failure", attrs.strings["outcome"])
	}
	if !attrs.bools["error"] {
		t.Fatal("git_root span missing error=true for true failure")
	}
	if got := span.Status().Code; got != codes.Error {
		t.Fatalf("git_root span status = %v, want Error", got)
	}
}

func TestResolveWorktreeRootReturnsWorktreeTopLevel(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}

	t.Setenv("PATH", "")
	got, err := ResolveWorktreeRoot(nested)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot() error = %v", err)
	}
	if got != worktree {
		t.Fatalf("ResolveWorktreeRoot() = %q, want %q", got, worktree)
	}
}

func TestResolveWorktreeRootContextMarksMarkerFallbackWithoutSpanError(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	worktree := filepath.Join(base, "wt")
	nested := filepath.Join(worktree, "go-bubbletea")

	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo .git worktrees): %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(worktree .git): %v", err)
	}
	t.Setenv("PATH", "")

	ctx, recorder, parentSpanID, endParent := newGitRootTraceContext(t)
	got, err := ResolveWorktreeRootContext(ctx, nested)
	endParent()
	if err != nil {
		t.Fatalf("ResolveWorktreeRootContext() error = %v", err)
	}
	if got != worktree {
		t.Fatalf("ResolveWorktreeRootContext() = %q, want %q", got, worktree)
	}

	span := findEndedSpan(t, recorder, "dependency.git_root")
	if span.Parent().SpanID() != parentSpanID {
		t.Fatalf("git_root parent span = %s, want %s", span.Parent().SpanID(), parentSpanID)
	}
	attrs := spanAttrs(span)
	if attrs.strings["outcome"] != "fallback" {
		t.Fatalf("outcome = %q, want fallback", attrs.strings["outcome"])
	}
	if attrs.bools["error"] {
		t.Fatal("git_root span has error=true for worktree marker fallback")
	}
	if got := span.Status().Code; got == codes.Error {
		t.Fatal("git_root span status = Error for worktree marker fallback")
	}
}

func TestGitExecEnvStripsAmbientGitRouting(t *testing.T) {
	in := []string{
		"PATH=/usr/bin:/bin",
		"GIT_DIR=/tmp/repo/.git",
		"GIT_WORK_TREE=/tmp/repo",
		"GIT_COMMON_DIR=/tmp/repo/.git",
		"GIT_INDEX_FILE=/tmp/repo/.git/index",
		"GIT_OBJECT_DIRECTORY=/tmp/repo/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/tmp/objects",
		"HOME=/tmp/home",
	}
	got := gitExecEnv(in)

	for _, kv := range got {
		if strings.HasPrefix(kv, "GIT_DIR=") ||
			strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_COMMON_DIR=") ||
			strings.HasPrefix(kv, "GIT_INDEX_FILE=") ||
			strings.HasPrefix(kv, "GIT_OBJECT_DIRECTORY=") ||
			strings.HasPrefix(kv, "GIT_ALTERNATE_OBJECT_DIRECTORIES=") {
			t.Fatalf("unexpected git routing var in env: %s", kv)
		}
	}
}

func newGitRootTraceContext(t *testing.T) (context.Context, *tracetest.SpanRecorder, oteltrace.SpanID, func()) {
	t.Helper()
	t.Setenv(latencytrace.EnvVar, "")
	t.Setenv(observability.EnvVar, "true")
	latencytrace.SetConfigEnabled(false)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
		latencytrace.SetConfigEnabled(false)
	})
	ctx, span := otel.Tracer("git_root_test").Start(context.Background(), "cli.command")
	return ctx, recorder, span.SpanContext().SpanID(), func() {
		span.End()
	}
}

func findEndedSpan(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("ended span %q not found; got %d spans", name, len(recorder.Ended()))
	return nil
}

type recordedSpanAttrs struct {
	strings map[string]string
	bools   map[string]bool
}

func spanAttrs(span sdktrace.ReadOnlySpan) recordedSpanAttrs {
	out := recordedSpanAttrs{
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

func sameTestPath(a, b string) bool {
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
