package aiaccount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const maxCredentialBytes = 4 << 20

var (
	ErrInvalid  = errors.New("invalid AI account profile")
	ErrNotFound = domain.ErrNotFound
	ErrConflict = domain.ErrConflict
)

type Config struct {
	HomeDir         string
	CodexHome       string
	ClaudeConfigDir string
	VaultDir        string
	CodexDaemons    CodexDaemonController
}

type Service struct {
	homeDir         string
	codexHome       string
	claudeConfigDir string
	vaultDir        string
	mu              sync.Mutex
	codexDaemons    CodexDaemonController
}

func New(cfg Config) (*Service, error) {
	homeDir := strings.TrimSpace(cfg.HomeDir)
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user home: %w", err)
		}
	}
	codexHome := strings.TrimSpace(cfg.CodexHome)
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if codexHome == "" {
		codexHome = filepath.Join(homeDir, ".codex")
	}
	claudeConfigDir := strings.TrimSpace(cfg.ClaudeConfigDir)
	if claudeConfigDir == "" {
		claudeConfigDir = strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	}
	if claudeConfigDir == "" {
		xdgConfig := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if xdgConfig == "" {
			xdgConfig = filepath.Join(homeDir, ".config")
		}
		claudeConfigDir = filepath.Join(xdgConfig, "claude-code")
	}
	vaultDir := strings.TrimSpace(cfg.VaultDir)
	if vaultDir == "" {
		vaultDir = filepath.Join(homeDir, ".local", "share", "azedarach", "accounts")
	}
	controller := cfg.CodexDaemons
	if controller == nil {
		controller = newSystemCodexDaemonController()
	}
	return &Service{homeDir: homeDir, codexHome: codexHome, claudeConfigDir: claudeConfigDir, vaultDir: vaultDir, codexDaemons: controller}, nil
}

func (s *Service) Backup(ctx context.Context, req protocol.AIAccountBackupRequestBody) (protocol.AIAccountBackupResponseBody, error) {
	if err := ctx.Err(); err != nil {
		return protocol.AIAccountBackupResponseBody{}, err
	}
	if err := validate(req.Provider, req.Name); err != nil {
		return protocol.AIAccountBackupResponseBody{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if isSystemProfile(req.Name) {
		return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("%w: profile names beginning with underscore are reserved", ErrInvalid)
	}
	if req.Provider == protocol.AIAccountProviderCodex {
		if err := s.ensureCodexFileCredentialStore(); err != nil {
			return protocol.AIAccountBackupResponseBody{}, err
		}
	}
	state, authenticated, err := s.readCurrentState(req.Provider)
	if err != nil {
		return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("read current %s credentials: %w", req.Provider, err)
	}
	if !authenticated {
		return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("%w: %s has no current credentials", ErrNotFound, req.Provider)
	}
	if !req.Force {
		if _, err := os.Lstat(filepath.Join(s.vaultDir, string(req.Provider), req.Name)); err == nil {
			return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("%w: %s/%s", ErrConflict, req.Provider, req.Name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("inspect profile: %w", err)
		}
	}
	if err := s.writeProfileState(req.Provider, req.Name, state); err != nil {
		return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("save %s profile %q: %w", req.Provider, req.Name, err)
	}
	return protocol.AIAccountBackupResponseBody{Profile: protocol.AIAccountProfile{
		Provider: req.Provider,
		Name:     req.Name,
		Active:   true,
	}}, nil
}

func (s *Service) List(ctx context.Context, req protocol.AIAccountListRequestBody) (protocol.AIAccountListResponseBody, error) {
	if err := ctx.Err(); err != nil {
		return protocol.AIAccountListResponseBody{}, err
	}
	if req.Provider != "" && !req.Provider.Valid() {
		return protocol.AIAccountListResponseBody{}, fmt.Errorf("%w: unsupported provider %q", ErrInvalid, req.Provider)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(req.Provider)
}

func (s *Service) listLocked(providerFilter protocol.AIAccountProvider) (protocol.AIAccountListResponseBody, error) {
	providers := requestedProviders(providerFilter)
	profiles := make([]protocol.AIAccountProfile, 0)
	root, err := s.openVaultRoot()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountListResponseBody{Profiles: profiles}, nil
		}
		return protocol.AIAccountListResponseBody{}, fmt.Errorf("inspect account vault: %w", err)
	}
	defer root.Close()
	for _, provider := range providers {
		currentState, authenticated, err := s.readCurrentState(provider)
		if err != nil {
			return protocol.AIAccountListResponseBody{}, fmt.Errorf("inspect current %s credentials: %w", provider, err)
		}
		activeIdentity, hasIdentity := stateIdentity(provider, currentState)
		providerDir := string(provider)
		if err := validateRootDir(root, providerDir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return protocol.AIAccountListResponseBody{}, fmt.Errorf("inspect %s account vault: %w", provider, err)
		}
		entries, err := fs.ReadDir(root.FS(), providerDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return protocol.AIAccountListResponseBody{}, fmt.Errorf("list %s profiles: %w", provider, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || validateName(entry.Name()) != nil {
				continue
			}
			profileState, exists, err := s.readProfileState(root, provider, entry.Name())
			if err != nil {
				return protocol.AIAccountListResponseBody{}, fmt.Errorf("hash %s profile %q: %w", provider, entry.Name(), err)
			}
			if !exists {
				continue
			}
			profileIdentity, profileHasIdentity := stateIdentity(provider, profileState)
			profiles = append(profiles, protocol.AIAccountProfile{
				Provider: provider,
				Name:     entry.Name(),
				Active:   authenticated && hasIdentity && profileHasIdentity && profileIdentity == activeIdentity,
				System:   isSystemProfile(entry.Name()),
			})
		}
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Provider != profiles[j].Provider {
			return profiles[i].Provider < profiles[j].Provider
		}
		return profiles[i].Name < profiles[j].Name
	})
	return protocol.AIAccountListResponseBody{Profiles: profiles}, nil
}

func (s *Service) Status(ctx context.Context, req protocol.AIAccountStatusRequestBody) (protocol.AIAccountStatusResponseBody, error) {
	if err := ctx.Err(); err != nil {
		return protocol.AIAccountStatusResponseBody{}, err
	}
	if req.Provider != "" && !req.Provider.Valid() {
		return protocol.AIAccountStatusResponseBody{}, fmt.Errorf("%w: unsupported provider %q", ErrInvalid, req.Provider)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	listed, err := s.listLocked(req.Provider)
	if err != nil {
		return protocol.AIAccountStatusResponseBody{}, err
	}

	providers := requestedProviders(req.Provider)
	statuses := make([]protocol.AIAccountProviderStatus, 0, len(providers))
	for _, provider := range providers {
		_, authenticated, err := s.readCurrentState(provider)
		if err != nil {
			return protocol.AIAccountStatusResponseBody{}, fmt.Errorf("inspect current %s credentials: %w", provider, err)
		}
		status := protocol.AIAccountProviderStatus{Provider: provider, Authenticated: authenticated}
		var systemMatch string
		for _, profile := range listed.Profiles {
			if profile.Provider != provider || !profile.Active {
				continue
			}
			if !profile.System {
				status.ActiveProfile = profile.Name
				break
			}
			if systemMatch == "" {
				systemMatch = profile.Name
			}
		}
		if status.ActiveProfile == "" {
			status.ActiveProfile = systemMatch
		}
		statuses = append(statuses, status)
	}
	return protocol.AIAccountStatusResponseBody{Providers: statuses}, nil

}

func (s *Service) Activate(ctx context.Context, req protocol.AIAccountActivateRequestBody) (protocol.AIAccountActivateResponseBody, error) {
	if err := ctx.Err(); err != nil {
		return protocol.AIAccountActivateResponseBody{}, err
	}
	if err := validate(req.Provider, req.Name); err != nil {
		return protocol.AIAccountActivateResponseBody{}, err
	}
	if req.ReloadCodexDaemon && req.Provider != protocol.AIAccountProviderCodex {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("%w: daemon reload is only supported for Codex", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.Provider == protocol.AIAccountProviderCodex {
		if err := s.ensureCodexFileCredentialStore(); err != nil {
			return protocol.AIAccountActivateResponseBody{}, err
		}
	}
	root, err := s.openVaultRoot()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("%w: %s/%s", ErrNotFound, req.Provider, req.Name)
		}
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("inspect account vault: %w", err)
	}
	defer root.Close()
	targetState, exists, err := s.readProfileState(root, req.Provider, req.Name)
	if err != nil {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("read profile %s/%s: %w", req.Provider, req.Name, err)
	}
	if !exists {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("%w: %s/%s", ErrNotFound, req.Provider, req.Name)
	}
	liveState, authenticated, err := s.readCurrentState(req.Provider)
	if err != nil {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("read current %s credentials: %w", req.Provider, err)
	}
	listed, err := s.listLocked(req.Provider)
	if err != nil {
		return protocol.AIAccountActivateResponseBody{}, err
	}
	activeProfile := ""
	anyActiveProfile := ""
	for _, profile := range listed.Profiles {
		if !profile.Active {
			continue
		}
		if anyActiveProfile == "" {
			anyActiveProfile = profile.Name
		}
		if !profile.System && activeProfile == "" {
			activeProfile = profile.Name
		}
	}

	result := protocol.AIAccountActivateResponseBody{
		Profile:                  protocol.AIAccountProfile{Provider: req.Provider, Name: req.Name, Active: true, System: isSystemProfile(req.Name)},
		RestartExistingProcesses: true,
	}
	if authenticated && anyActiveProfile == "" {
		safetyName := originalProfileName
		if _, err := os.Lstat(filepath.Join(s.vaultDir, string(req.Provider), safetyName)); err == nil {
			safetyName = safetyBackupName()
		} else if !errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("inspect safety profile: %w", err)
		}
		if err := s.writeProfileState(req.Provider, safetyName, liveState); err != nil {
			return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("preserve current credentials: %w", err)
		}
		if err := s.rotateSafetyBackups(req.Provider); err != nil {
			return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("rotate safety profiles: %w", err)
		}
		result.SafetyBackupProfile = safetyName
	}
	if authenticated && activeProfile != "" {
		if err := s.writeProfileState(req.Provider, activeProfile, liveState); err != nil {
			return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("re-snapshot outgoing profile %s: %w", activeProfile, err)
		}
		result.OutgoingResnapshotted = activeProfile
		if activeProfile == req.Name {
			targetState = liveState
		}
	}
	preservePrimary := req.Provider == protocol.AIAccountProviderCodex && authenticated && codexLiveNewer(liveState, targetState)
	if err := s.restoreState(req.Provider, targetState, preservePrimary); err != nil {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("activate profile %s/%s: %w", req.Provider, req.Name, err)
	}
	result.FreshLivePreserved = preservePrimary
	if req.Provider == protocol.AIAccountProviderCodex {
		reload, err := s.codexDaemons.Reload(ctx, s.codexHome, req.ReloadCodexDaemon)
		if err != nil {
			reload = protocol.AIAccountCodexDaemonReload{InspectionFailed: true}
		}
		result.CodexDaemonReload = &reload
	}
	return result, nil
}

func (s *Service) Delete(ctx context.Context, req protocol.AIAccountDeleteRequestBody) (protocol.AIAccountDeleteResponseBody, error) {
	if err := ctx.Err(); err != nil {
		return protocol.AIAccountDeleteResponseBody{}, err
	}
	if err := validate(req.Provider, req.Name); err != nil {
		return protocol.AIAccountDeleteResponseBody{}, err
	}
	if !req.Confirm {
		return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("%w: delete requires confirmation", ErrInvalid)
	}
	if isSystemProfile(req.Name) {
		return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("%w: protected system profiles cannot be deleted", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	root, err := s.openVaultRoot()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("%w: %s/%s", ErrNotFound, req.Provider, req.Name)
		}
		return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("inspect account vault: %w", err)
	}
	defer root.Close()
	profileDir := filepath.Join(string(req.Provider), req.Name)
	info, err := root.Lstat(profileDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("%w: %s/%s", ErrNotFound, req.Provider, req.Name)
		}
		return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("inspect profile: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("%w: profile directory is not a regular directory", ErrInvalid)
	}
	if err := os.RemoveAll(filepath.Join(s.vaultDir, profileDir)); err != nil {
		return protocol.AIAccountDeleteResponseBody{}, fmt.Errorf("delete profile: %w", err)
	}
	return protocol.AIAccountDeleteResponseBody{Provider: req.Provider, Name: req.Name, Deleted: true}, nil
}

func profileRelativePath(provider protocol.AIAccountProvider, name string) string {
	return filepath.Join(string(provider), name, "credentials.json")
}

func (s *Service) profilePath(provider protocol.AIAccountProvider, name string) string {
	return filepath.Join(s.vaultDir, profileRelativePath(provider, name))
}

func (s *Service) ensureVaultProfileDir(provider protocol.AIAccountProvider, name string) error {
	for _, dir := range []string{
		s.vaultDir,
		filepath.Join(s.vaultDir, string(provider)),
		filepath.Join(s.vaultDir, string(provider), name),
	} {
		if err := ensurePrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) openVaultRoot() (*os.Root, error) {
	root, err := os.OpenRoot(s.vaultDir)
	if err != nil {
		return nil, err
	}
	rootInfo, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	namedInfo, err := os.Lstat(s.vaultDir)
	if err != nil {
		root.Close()
		return nil, err
	}
	if !namedInfo.IsDir() || namedInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(rootInfo, namedInfo) {
		root.Close()
		return nil, fmt.Errorf("%w: account vault path is not a stable regular directory", ErrInvalid)
	}
	return root, nil
}

func validateRootDir(root *os.Root, path string) error {
	info, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: account vault path is not a regular directory", ErrInvalid)
	}
	return nil
}

func requestedProviders(provider protocol.AIAccountProvider) []protocol.AIAccountProvider {
	if provider != "" {
		return []protocol.AIAccountProvider{provider}
	}
	return []protocol.AIAccountProvider{protocol.AIAccountProviderClaude, protocol.AIAccountProviderCodex}
}

func validate(provider protocol.AIAccountProvider, name string) error {
	if !provider.Valid() {
		return fmt.Errorf("%w: unsupported provider %q", ErrInvalid, provider)
	}
	return validateName(name)
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return fmt.Errorf("%w: invalid profile name", ErrInvalid)
	}
	for i, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._@+-", r) {
			if i == 0 && r == '.' {
				return fmt.Errorf("%w: profile name cannot start with a dot", ErrInvalid)
			}
			continue
		}
		return fmt.Errorf("%w: profile name contains unsupported characters", ErrInvalid)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: account vault path is not a regular directory", ErrInvalid)
	}
	return os.Chmod(path, 0o700)
}

func readCredentialFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBoundedCredential(file)
}

func readBoundedCredential(file *os.File) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: credential is not a regular file", ErrInvalid)
	}
	if info.Size() > maxCredentialBytes {
		return nil, fmt.Errorf("%w: credential exceeds %d bytes", ErrInvalid, maxCredentialBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCredentialBytes {
		return nil, fmt.Errorf("%w: credential exceeds %d bytes", ErrInvalid, maxCredentialBytes)
	}
	return data, nil
}
