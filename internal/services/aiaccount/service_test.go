package aiaccount

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestServiceClaudeCompleteStateRoundTrip(t *testing.T) {
	home := t.TempDir()
	claudeConfig := filepath.Join(home, "custom-claude-config")
	vault := filepath.Join(t.TempDir(), "vault")
	service, err := New(Config{HomeDir: home, ClaudeConfigDir: claudeConfig, VaultDir: vault})
	if err != nil {
		t.Fatal(err)
	}

	paths := map[string]string{
		"primary":  filepath.Join(home, ".claude", ".credentials.json"),
		"state":    filepath.Join(home, ".claude.json"),
		"config":   filepath.Join(claudeConfig, "auth.json"),
		"settings": filepath.Join(home, ".claude", "settings.json"),
		"desktop":  filepath.Join(home, "Library", "Application Support", "Claude", "config.json"),
	}
	writeTestCredential(t, paths["primary"], `{"claudeAiOauth":{"accountUuid":"alice","accessToken":"a1"}}`)
	writeTestCredential(t, paths["state"], `{"oauthAccount":"alice@example.com","numStartups":1}`)
	writeTestCredential(t, paths["config"], `{"account":"alice-config"}`)
	writeTestCredential(t, paths["settings"], `{"apiKeyHelper":"alice-helper","theme":"alice-theme"}`)
	writeTestCredential(t, paths["desktop"], `{"theme":"dark","oauth:tokenCache":"alice-cache"}`)
	if _, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{Provider: protocol.AIAccountProviderClaude, Name: "alice"}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	writeTestCredential(t, paths["primary"], `{"claudeAiOauth":{"accountUuid":"bob","accessToken":"b1"}}`)
	writeTestCredential(t, paths["state"], `{"oauthAccount":"bob@example.com","numStartups":9}`)
	writeTestCredential(t, paths["config"], `{"account":"bob-config"}`)
	writeTestCredential(t, paths["settings"], `{"apiKeyHelper":"bob-helper","theme":"bob-theme"}`)
	writeTestCredential(t, paths["desktop"], `{"theme":"light","oauth:tokenCache":"bob-cache","window":2}`)

	activated, err := service.Activate(context.Background(), protocol.AIAccountActivateRequestBody{Provider: protocol.AIAccountProviderClaude, Name: "alice"})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activated.SafetyBackupProfile != originalProfileName {
		t.Fatalf("safety backup = %q, want %q", activated.SafetyBackupProfile, originalProfileName)
	}
	assertFileContent(t, paths["primary"], `{"claudeAiOauth":{"accountUuid":"alice","accessToken":"a1"}}`)
	stateRaw, err := os.ReadFile(paths["state"])
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		t.Fatal(err)
	}
	if state["oauthAccount"] != "alice@example.com" || state["numStartups"] != float64(9) {
		t.Fatalf("Claude state merge = %+v", state)
	}
	assertFileContent(t, paths["config"], `{"account":"alice-config"}`)
	settingsRaw, err := os.ReadFile(paths["settings"])
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["apiKeyHelper"] != "alice-helper" || settings["theme"] != "bob-theme" {
		t.Fatalf("Claude settings merge = %+v", settings)
	}
	desktopRaw, err := os.ReadFile(paths["desktop"])
	if err != nil {
		t.Fatal(err)
	}
	var desktop map[string]any
	if err := json.Unmarshal(desktopRaw, &desktop); err != nil {
		t.Fatal(err)
	}
	if desktop["oauth:tokenCache"] != "alice-cache" || desktop["theme"] != "light" || desktop["window"] != float64(2) {
		t.Fatalf("desktop merge = %+v", desktop)
	}

	profiles, err := service.List(context.Background(), protocol.AIAccountListRequestBody{Provider: protocol.AIAccountProviderClaude})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles.Profiles) != 2 || !profiles.Profiles[0].System {
		t.Fatalf("profiles = %+v, want protected original plus alice", profiles.Profiles)
	}
}

func TestServiceCodexResnapshotFreshnessAndFileStore(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	service, err := New(Config{HomeDir: home, CodexHome: codexHome, VaultDir: filepath.Join(t.TempDir(), "vault")})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(codexHome, "config.toml")
	writeTestCredential(t, configPath, "# user config\n[mcp_servers.demo]\ncommand = \"demo\"\ncli_auth_credentials_store = \"keyring\"\n")
	old := codexAuthJSON(t, "alice@example.com", time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC))
	newer := codexAuthJSON(t, "alice@example.com", time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC))
	authPath := filepath.Join(codexHome, "auth.json")
	writeTestCredential(t, authPath, string(old))
	if _, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{Provider: protocol.AIAccountProviderCodex, Name: "outgoing"}); err != nil {
		t.Fatal(err)
	}
	if err := service.writeProfileState(protocol.AIAccountProviderCodex, "target", authState{vaultPrimaryCredential: old}); err != nil {
		t.Fatal(err)
	}
	writeTestCredential(t, authPath, string(newer))

	listed, err := service.List(context.Background(), protocol.AIAccountListRequestBody{Provider: protocol.AIAccountProviderCodex})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range listed.Profiles {
		if !profile.Active {
			t.Fatalf("rotated-token profile not detected as active: %+v", listed.Profiles)
		}
	}
	activated, err := service.Activate(context.Background(), protocol.AIAccountActivateRequestBody{Provider: protocol.AIAccountProviderCodex, Name: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if activated.OutgoingResnapshotted != "outgoing" || !activated.FreshLivePreserved {
		t.Fatalf("activation = %+v", activated)
	}
	assertFileContent(t, authPath, string(newer))
	assertFileContent(t, filepath.Join(service.vaultDir, "codex", "outgoing", vaultPrimaryCredential), string(newer))
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	settingIndex := strings.Index(string(config), `cli_auth_credentials_store = "file"`)
	tableIndex := strings.Index(string(config), "[mcp_servers.demo]")
	if settingIndex < 0 || settingIndex > tableIndex {
		t.Fatalf("credential store setting is not top-level before table: %s", config)
	}
	if !strings.Contains(string(config[tableIndex:]), `cli_auth_credentials_store = "keyring"`) {
		t.Fatalf("nested user setting was unexpectedly rewritten: %s", config)
	}
}

func TestServiceProtectsSystemProfiles(t *testing.T) {
	home := t.TempDir()
	service, err := New(Config{HomeDir: home, VaultDir: filepath.Join(t.TempDir(), "vault")})
	if err != nil {
		t.Fatal(err)
	}
	writeTestCredential(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accountUuid":"alice"}}`)
	if _, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{Provider: protocol.AIAccountProviderClaude, Name: originalProfileName}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("system-name backup error = %v", err)
	}
	if err := service.writeProfileState(protocol.AIAccountProviderClaude, originalProfileName, authState{vaultPrimaryCredential: []byte(`{"claudeAiOauth":{"accountUuid":"alice"}}`)}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Delete(context.Background(), protocol.AIAccountDeleteRequestBody{Provider: protocol.AIAccountProviderClaude, Name: originalProfileName, Confirm: true})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("system delete error = %v", err)
	}
}

func TestServiceClaudeOptionalAPIKeyState(t *testing.T) {
	home := t.TempDir()
	service, err := New(Config{HomeDir: home, VaultDir: filepath.Join(t.TempDir(), "vault")})
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	writeTestCredential(t, settingsPath, `{"apiKeyHelper":"security find-generic-password -w"}`)
	writeTestCredential(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":"stale-oauth@example.com"}`)
	if _, err := service.Backup(context.Background(), protocol.AIAccountBackupRequestBody{Provider: protocol.AIAccountProviderClaude, Name: "api-key"}); err != nil {
		t.Fatalf("Backup optional-only Claude auth: %v", err)
	}
	writeTestCredential(t, settingsPath, `{"apiKeyHelper":"different-helper"}`)
	if _, err := service.Activate(context.Background(), protocol.AIAccountActivateRequestBody{Provider: protocol.AIAccountProviderClaude, Name: "api-key"}); err != nil {
		t.Fatalf("Activate optional-only Claude auth: %v", err)
	}
	assertFileContent(t, settingsPath, `{"apiKeyHelper":"security find-generic-password -w"}`)
	status, err := service.Status(context.Background(), protocol.AIAccountStatusRequestBody{Provider: protocol.AIAccountProviderClaude})
	if err != nil || status.Providers[0].ActiveProfile != "api-key" {
		t.Fatalf("API-key profile status = %+v, err=%v", status, err)
	}
}

func codexAuthJSON(t *testing.T, email string, refreshed time.Time) []byte {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims, err := json.Marshal(map[string]any{"email": email, "sub": "user-" + email, "iat": refreshed.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	token := header + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	data, err := json.Marshal(map[string]any{
		"last_refresh": refreshed.Format(time.RFC3339),
		"tokens":       map[string]any{"id_token": token, "refresh_token": "rt-" + refreshed.Format("150405")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
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
