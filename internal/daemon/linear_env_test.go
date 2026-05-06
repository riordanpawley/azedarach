package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLinearAPIKeyPrefersEnvironment(t *testing.T) {
	repoDir := t.TempDir()
	t.Setenv(linearAPIKeyEnv, " env-key ")
	writeLinearEnvLocal(t, repoDir, "LINEAR_API_KEY=file-key\n")

	if got, want := resolveLinearAPIKey(repoDir), "env-key"; got != want {
		t.Fatalf("resolveLinearAPIKey() = %q, want %q", got, want)
	}
}

func TestResolveLinearAPIKeyReadsProjectEnvLocal(t *testing.T) {
	repoDir := t.TempDir()
	t.Setenv(linearAPIKeyEnv, "")
	writeLinearEnvLocal(t, repoDir, `
# ignored
OTHER=value
export LINEAR_API_KEY="file-key"
`)

	if got, want := resolveLinearAPIKey(repoDir), "file-key"; got != want {
		t.Fatalf("resolveLinearAPIKey() = %q, want %q", got, want)
	}
}

func TestReadDotEnvValueSupportsSingleQuotesAndInlineComment(t *testing.T) {
	repoDir := t.TempDir()
	writeLinearEnvLocal(t, repoDir, "LINEAR_API_KEY='quoted-key' # comment\nOTHER=value # comment\n")

	got, ok := readDotEnvValue(filepath.Join(repoDir, ".env.local"), linearAPIKeyEnv)
	if !ok {
		t.Fatal("expected LINEAR_API_KEY to be found")
	}
	if want := "quoted-key"; got != want {
		t.Fatalf("readDotEnvValue() = %q, want %q", got, want)
	}
}

func writeLinearEnvLocal(t *testing.T, repoDir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, ".env.local"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write .env.local: %v", err)
	}
}
