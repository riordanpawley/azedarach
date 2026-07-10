package aiaccount

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	vaultPrimaryCredential = "credentials.json"
	vaultClaudeState       = "claude-state.json"
	vaultClaudeConfigAuth  = "config-auth.json"
	vaultClaudeSettings    = "settings.json"
	vaultClaudeDesktop     = "desktop-oauth.json"
	vaultMetadata          = "meta.json"
	originalProfileName    = "_original"
	safetyBackupPrefix     = "_backup_"
	maxSafetyBackups       = 5
)

var codexCredentialStorePattern = regexp.MustCompile(`^\s*cli_auth_credentials_store\s*=\s*"[^"]*"\s*$`)

type authFileKind int

const (
	authFileWhole authFileKind = iota
	authFileClaudeDesktop
	authFileJSONFields
)

type authFileSpec struct {
	livePath  string
	vaultName string
	required  bool
	kind      authFileKind
	fields    []string
}

type authState map[string][]byte

type profileMetadata struct {
	Provider   protocol.AIAccountProvider `json:"provider"`
	Name       string                     `json:"name"`
	BackedUpAt time.Time                  `json:"backed_up_at"`
	Files      []string                   `json:"files"`
	System     bool                       `json:"system"`
}

func (s *Service) authFileSpecs(provider protocol.AIAccountProvider) []authFileSpec {
	if provider == protocol.AIAccountProviderCodex {
		return []authFileSpec{{
			livePath:  filepath.Join(s.codexHome, "auth.json"),
			vaultName: vaultPrimaryCredential,
			required:  true,
		}}
	}
	return []authFileSpec{
		{livePath: filepath.Join(s.homeDir, ".claude", ".credentials.json"), vaultName: vaultPrimaryCredential, required: true},
		{livePath: filepath.Join(s.homeDir, ".claude.json"), vaultName: vaultClaudeState, kind: authFileJSONFields, fields: []string{"oauthAccount", "userID"}},
		{livePath: filepath.Join(s.claudeConfigDir, "auth.json"), vaultName: vaultClaudeConfigAuth},
		{livePath: filepath.Join(s.homeDir, ".claude", "settings.json"), vaultName: vaultClaudeSettings, kind: authFileJSONFields, fields: []string{"apiKeyHelper"}},
		{livePath: filepath.Join(s.homeDir, "Library", "Application Support", "Claude", "config.json"), vaultName: vaultClaudeDesktop, kind: authFileClaudeDesktop},
	}
}

func (s *Service) readCurrentState(provider protocol.AIAccountProvider) (authState, bool, error) {
	state := make(authState)
	for _, spec := range s.authFileSpecs(provider) {
		data, exists, err := readAuthFile(spec)
		if err != nil {
			return nil, false, err
		}
		if !exists {
			continue
		}
		state[spec.vaultName] = data
	}
	return state, authStateAuthenticated(provider, state), nil
}

func readAuthFile(spec authFileSpec) ([]byte, bool, error) {
	data, err := readCredentialFile(spec.livePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if spec.kind == authFileClaudeDesktop {
		fields, ok, err := claudeDesktopOAuthFields(data)
		if err != nil || !ok {
			return nil, false, err
		}
		data, err = json.Marshal(fields)
		if err != nil {
			return nil, false, fmt.Errorf("marshal Claude Desktop OAuth cache: %w", err)
		}
	} else if spec.kind == authFileJSONFields {
		fields, ok, err := selectJSONFields(data, spec.fields)
		if err != nil || !ok {
			return nil, false, err
		}
		data, err = json.Marshal(fields)
		if err != nil {
			return nil, false, fmt.Errorf("marshal scoped auth fields: %w", err)
		}
	}
	return data, true, nil
}

func (s *Service) readProfileState(root *os.Root, provider protocol.AIAccountProvider, name string) (authState, bool, error) {
	state := make(authState)
	for _, spec := range s.authFileSpecs(provider) {
		data, exists, err := readRootProfileFile(root, provider, name, spec.vaultName)
		if err != nil {
			return nil, false, err
		}
		if exists {
			state[spec.vaultName] = data
		}
	}
	return state, authStateAuthenticated(provider, state), nil
}

func (s *Service) writeProfileState(provider protocol.AIAccountProvider, name string, state authState) error {
	if err := s.ensureVaultProfileDir(provider, name); err != nil {
		return err
	}
	files := make([]string, 0, len(state))
	for _, spec := range s.authFileSpecs(provider) {
		data, exists := state[spec.vaultName]
		path := filepath.Join(s.vaultDir, string(provider), name, spec.vaultName)
		if !exists {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove stale profile file %s: %w", spec.vaultName, err)
			}
			continue
		}
		if err := atomicWrite(path, data); err != nil {
			return fmt.Errorf("write profile file %s: %w", spec.vaultName, err)
		}
		files = append(files, spec.vaultName)
	}
	sort.Strings(files)
	meta, err := json.Marshal(profileMetadata{
		Provider:   provider,
		Name:       name,
		BackedUpAt: time.Now().UTC(),
		Files:      files,
		System:     isSystemProfile(name),
	})
	if err != nil {
		return fmt.Errorf("marshal profile metadata: %w", err)
	}
	return atomicWrite(filepath.Join(s.vaultDir, string(provider), name, vaultMetadata), meta)
}

func (s *Service) restoreState(provider protocol.AIAccountProvider, state authState, preservePrimary bool) error {
	for _, spec := range s.authFileSpecs(provider) {
		data, exists := state[spec.vaultName]
		if !exists {
			continue
		}
		if preservePrimary && spec.required {
			continue
		}
		if spec.kind == authFileClaudeDesktop {
			if err := mergeClaudeDesktopOAuth(spec.livePath, data); err != nil {
				return err
			}
			continue
		}
		if spec.kind == authFileJSONFields {
			if err := mergeJSONFields(spec.livePath, data, spec.fields); err != nil {
				return fmt.Errorf("restore %s: %w", spec.vaultName, err)
			}
			continue
		}
		if err := atomicWrite(spec.livePath, data); err != nil {
			return fmt.Errorf("restore %s: %w", spec.vaultName, err)
		}
	}
	return nil
}

func readRootProfileFile(root *os.Root, provider protocol.AIAccountProvider, name, filename string) ([]byte, bool, error) {
	profileDir := filepath.Join(string(provider), name)
	if err := validateRootDir(root, string(provider)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := validateRootDir(root, profileDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	path := filepath.Join(profileDir, filename)
	info, err := root.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%w: credential profile is not a regular file", ErrInvalid)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := readBoundedCredential(file)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func stateIdentity(provider protocol.AIAccountProvider, state authState) ([sha256.Size]byte, bool) {
	if provider == protocol.AIAccountProviderClaude {
		if primary := state[vaultPrimaryCredential]; len(primary) > 0 {
			if data := state[vaultClaudeState]; len(data) > 0 {
				var value map[string]any
				if json.Unmarshal(data, &value) == nil {
					identity := firstString(value, "oauthAccount", "userID")
					if identity != "" {
						return sha256.Sum256([]byte("claude:" + identity)), true
					}
				}
			}
			var value map[string]any
			if json.Unmarshal(primary, &value) == nil {
				if oauth, ok := value["claudeAiOauth"].(map[string]any); ok {
					identity := firstString(oauth, "accountUuid", "organizationUuid", "accessToken", "refreshToken")
					if identity != "" {
						return sha256.Sum256([]byte("claude:" + identity)), true
					}
				}
			}
			return sha256.Sum256(primary), true
		}
		if data := state[vaultClaudeConfigAuth]; len(data) > 0 {
			return sha256.Sum256(append([]byte("claude:config-auth:"), data...)), true
		}
		if data := state[vaultClaudeSettings]; len(data) > 0 {
			var value map[string]any
			if json.Unmarshal(data, &value) == nil {
				if helper := firstString(value, "apiKeyHelper"); helper != "" {
					return sha256.Sum256([]byte("claude:api-key-helper:" + helper)), true
				}
			}
		}
		if data := state[vaultClaudeDesktop]; len(data) > 0 {
			return sha256.Sum256(append([]byte("claude:desktop:"), data...)), true
		}
		return [sha256.Size]byte{}, false
	}

	data := state[vaultPrimaryCredential]
	if len(data) == 0 {
		return [sha256.Size]byte{}, false
	}
	var value map[string]any
	if json.Unmarshal(data, &value) == nil {
		if identity := codexIdentity(value); identity != "" {
			return sha256.Sum256([]byte("codex:" + identity)), true
		}
	}
	return sha256.Sum256(data), true
}

func codexIdentity(value map[string]any) string {
	for _, token := range codexTokenValues(value) {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims map[string]any
		if json.Unmarshal(payload, &claims) != nil {
			continue
		}
		maps := []map[string]any{claims}
		if nested, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			maps = append(maps, nested)
		}
		var email, account, organization string
		for _, claims := range maps {
			if email == "" {
				email = firstString(claims, "email", "preferred_username", "upn")
			}
			if account == "" {
				account = firstString(claims, "sub", "account_id", "accountId", "user_id", "userId")
			}
			if organization == "" {
				organization = firstString(claims, "organization", "org", "org_name")
			}
		}
		if email != "" || account != "" {
			return email + "|" + account + "|" + organization
		}
	}
	return ""
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if result, ok := value[key].(string); ok && strings.TrimSpace(result) != "" {
			return strings.TrimSpace(result)
		}
	}
	return ""
}

func optionalClaudeAuth(name string, data []byte) bool {
	switch name {
	case vaultClaudeConfigAuth, vaultClaudeDesktop:
		return len(data) > 0
	case vaultClaudeSettings:
		var value map[string]any
		return json.Unmarshal(data, &value) == nil && firstString(value, "apiKeyHelper") != ""
	default:
		return false
	}
}

func authStateAuthenticated(provider protocol.AIAccountProvider, state authState) bool {
	if len(state[vaultPrimaryCredential]) > 0 {
		return true
	}
	if provider != protocol.AIAccountProviderClaude {
		return false
	}
	for name, data := range state {
		if optionalClaudeAuth(name, data) {
			return true
		}
	}
	return false
}

func codexLiveNewer(live, snapshot authState) bool {
	liveID, liveOK := stateIdentity(protocol.AIAccountProviderCodex, live)
	snapshotID, snapshotOK := stateIdentity(protocol.AIAccountProviderCodex, snapshot)
	if !liveOK || !snapshotOK || liveID != snapshotID {
		return false
	}
	liveTime, liveTimeOK := codexFreshness(live[vaultPrimaryCredential])
	snapshotTime, snapshotTimeOK := codexFreshness(snapshot[vaultPrimaryCredential])
	return liveTimeOK && snapshotTimeOK && liveTime.After(snapshotTime)
}

func codexFreshness(data []byte) (time.Time, bool) {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return time.Time{}, false
	}
	if raw, ok := value["last_refresh"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed, true
		}
	}
	for _, token := range codexTokenValues(value) {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims map[string]any
		if json.Unmarshal(payload, &claims) != nil {
			continue
		}
		switch issued := claims["iat"].(type) {
		case float64:
			return time.Unix(int64(issued), 0).UTC(), true
		case string:
			if parsed, err := time.Parse(time.RFC3339, issued); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

func codexTokenValues(value map[string]any) []string {
	tokens := make([]string, 0, 4)
	if nested, ok := value["tokens"].(map[string]any); ok {
		for _, key := range []string{"id_token", "idToken", "access_token", "accessToken"} {
			if token, ok := nested[key].(string); ok {
				tokens = append(tokens, token)
			}
		}
	}
	for _, key := range []string{"id_token", "idToken", "access_token", "accessToken"} {
		if token, ok := value[key].(string); ok {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func (s *Service) ensureCodexFileCredentialStore() error {
	if err := ensurePrivateDir(s.codexHome); err != nil {
		return fmt.Errorf("prepare Codex home: %w", err)
	}
	path := filepath.Join(s.codexHome, "config.toml")
	const setting = `cli_auth_credentials_store = "file"`
	data, err := readCredentialFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return atomicWrite(path, []byte("# Managed by Azedarach for account switching\n"+setting+"\n"))
	}
	if err != nil {
		return fmt.Errorf("read Codex config: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	tableAt := len(lines)
	rootSettingAt := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			tableAt = i
			break
		}
		if codexCredentialStorePattern.MatchString(line) {
			rootSettingAt = i
		}
	}
	if rootSettingAt >= 0 {
		if strings.Contains(lines[rootSettingAt], `"file"`) {
			return nil
		}
		lines[rootSettingAt] = setting
		return atomicWrite(path, []byte(strings.Join(lines, "\n")))
	}
	lines = append(lines, "")
	copy(lines[tableAt+1:], lines[tableAt:])
	lines[tableAt] = setting
	return atomicWrite(path, []byte(strings.Join(lines, "\n")))
}

func claudeDesktopOAuthFields(data []byte) (map[string]any, bool, error) {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, false, fmt.Errorf("parse Claude Desktop config: %w", err)
	}
	fields := make(map[string]any)
	for _, key := range []string{"oauth:tokenCache", "oauth:tokenCacheV2"} {
		if field, ok := value[key]; ok {
			fields[key] = field
		}
	}
	return fields, len(fields) > 0, nil
}

func selectJSONFields(data []byte, keys []string) (map[string]any, bool, error) {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, false, fmt.Errorf("parse auth-bearing JSON fields: %w", err)
	}
	fields := make(map[string]any)
	for _, key := range keys {
		if field, ok := value[key]; ok {
			fields[key] = field
		}
	}
	return fields, len(fields) > 0, nil
}

func mergeJSONFields(path string, snapshot []byte, keys []string) error {
	var fields map[string]any
	if err := json.Unmarshal(snapshot, &fields); err != nil {
		return fmt.Errorf("parse saved auth fields: %w", err)
	}
	live := make(map[string]any)
	if data, err := readCredentialFile(path); err == nil {
		if err := json.Unmarshal(data, &live); err != nil {
			return fmt.Errorf("parse live JSON: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read live JSON: %w", err)
	}
	for _, key := range keys {
		delete(live, key)
	}
	for key, value := range fields {
		live[key] = value
	}
	data, err := json.Marshal(live)
	if err != nil {
		return fmt.Errorf("marshal merged JSON: %w", err)
	}
	return atomicWrite(path, data)
}

func mergeClaudeDesktopOAuth(path string, snapshot []byte) error {
	var fields map[string]any
	if err := json.Unmarshal(snapshot, &fields); err != nil {
		return fmt.Errorf("parse saved Claude Desktop OAuth cache: %w", err)
	}
	live := make(map[string]any)
	if data, err := readCredentialFile(path); err == nil {
		if err := json.Unmarshal(data, &live); err != nil {
			return fmt.Errorf("parse live Claude Desktop config: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read live Claude Desktop config: %w", err)
	}
	delete(live, "oauth:tokenCache")
	delete(live, "oauth:tokenCacheV2")
	for key, value := range fields {
		live[key] = value
	}
	data, err := json.Marshal(live)
	if err != nil {
		return fmt.Errorf("marshal Claude Desktop config: %w", err)
	}
	return atomicWrite(path, data)
}

func isSystemProfile(name string) bool {
	return strings.HasPrefix(name, "_")
}

func safetyBackupName() string {
	return safetyBackupPrefix + time.Now().UTC().Format("20060102T150405.000000000Z")
}

func (s *Service) rotateSafetyBackups(provider protocol.AIAccountProvider) error {
	dir := filepath.Join(s.vaultDir, string(provider))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), safetyBackupPrefix) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > maxSafetyBackups {
		if err := os.RemoveAll(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}
