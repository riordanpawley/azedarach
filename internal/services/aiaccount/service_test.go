package aiaccount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func TestServiceBackupActivateStatusDelete(t *testing.T) {
	home := t.TempDir()
	vault := filepath.Join(t.TempDir(), "vault")
	service, err := New(Config{HomeDir: home, VaultDir: vault})
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(home, ".claude", ".credentials.json")
	writeTestCredential(t, credentialPath, `{"account":"one","token":"secret-one"}`)

	backedUp, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "work@example.com",
	})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if !backedUp.Profile.Active {
		t.Fatal("newly backed-up matching profile should be active")
	}
	profilePath := filepath.Join(vault, "claude", "work@example.com", "credentials.json")
	if info, err := os.Stat(profilePath); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("profile mode = %o, want 600", got)
	}

	writeTestCredential(t, credentialPath, `{"account":"two","token":"secret-two"}`)
	status, err := service.Status(context.Background(), protocol.AIAccountStatusRequestBody{Provider: protocol.AIAccountProviderClaude})
	if err != nil {
		t.Fatalf("Status unmatched: %v", err)
	}
	if !status.Providers[0].Authenticated || status.Providers[0].ActiveProfile != "" {
		t.Fatalf("status = %+v, want authenticated without matching profile", status.Providers[0])
	}

	activated, err := service.Activate(context.Background(), protocol.AIAccountActivateRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "work@example.com",
	})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !activated.Profile.Active {
		t.Fatal("activated profile should be active")
	}
	if !activated.RestartExistingProcesses {
		t.Fatal("activation should warn that existing processes need a restart")
	}
	data, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"account":"one","token":"secret-one"}` {
		t.Fatalf("activated credential = %q", data)
	}

	deleted, err := service.Delete(context.Background(), protocol.AIAccountDeleteRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "work@example.com",
		Confirm:  true,
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted.Deleted {
		t.Fatal("delete result should report deletion")
	}
}

func TestServiceRejectsTraversalConflictAndSymlink(t *testing.T) {
	home := t.TempDir()
	vault := filepath.Join(t.TempDir(), "vault")
	service, err := New(Config{HomeDir: home, VaultDir: vault})
	if err != nil {
		t.Fatal(err)
	}
	writeTestCredential(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"secret"}`)

	for _, name := range []string{"../escape", ".hidden", "space name", ""} {
		_, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{
			Provider: protocol.AIAccountProviderCodex,
			Name:     name,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Backup(%q) error = %v, want ErrInvalid", name, err)
		}
	}

	req := protocol.AIAccountBackupRequestBody{Provider: protocol.AIAccountProviderCodex, Name: "work"}
	if _, err := service.Backup(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Backup(context.Background(), req); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate backup error = %v, want ErrConflict", err)
	}

	profilePath := filepath.Join(vault, "codex", "linked", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".codex", "auth.json"), profilePath); err != nil {
		t.Fatal(err)
	}
	_, err = service.Activate(context.Background(), protocol.AIAccountActivateRequestBody{
		Provider: protocol.AIAccountProviderCodex,
		Name:     "linked",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink activation error = %v, want ErrInvalid", err)
	}
}

func TestServiceUsesConfiguredCodexHome(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	service, err := New(Config{HomeDir: home, CodexHome: codexHome, VaultDir: filepath.Join(t.TempDir(), "vault")})
	if err != nil {
		t.Fatal(err)
	}
	writeTestCredential(t, filepath.Join(codexHome, "auth.json"), `{"token":"secret"}`)
	if _, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{
		Provider: protocol.AIAccountProviderCodex,
		Name:     "personal",
	}); err != nil {
		t.Fatalf("Backup with configured CODEX_HOME: %v", err)
	}
}

func TestServiceRejectsSymlinkedVaultRoot(t *testing.T) {
	home := t.TempDir()
	realVault := t.TempDir()
	vault := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(realVault, vault); err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{HomeDir: home, VaultDir: vault})
	if err != nil {
		t.Fatal(err)
	}
	writeTestCredential(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"secret"}`)

	if _, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "work",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Backup with symlinked vault error = %v, want ErrInvalid", err)
	}
	if _, err := service.List(context.Background(), protocol.AIAccountListRequestBody{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("List with symlinked vault error = %v, want ErrInvalid", err)
	}
}

func TestServiceRejectsOversizedCredential(t *testing.T) {
	home := t.TempDir()
	service, err := New(Config{HomeDir: home, VaultDir: filepath.Join(t.TempDir(), "vault")})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, maxCredentialBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{
		Provider: protocol.AIAccountProviderClaude,
		Name:     "oversized",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized backup error = %v, want ErrInvalid", err)
	}
}

func writeTestCredential(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
