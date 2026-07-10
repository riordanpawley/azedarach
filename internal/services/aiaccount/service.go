package aiaccount

import (
	"context"
	"crypto/sha256"
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
	HomeDir   string
	CodexHome string
	VaultDir  string
}

type Service struct {
	homeDir   string
	codexHome string
	vaultDir  string
	mu        sync.Mutex
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
	vaultDir := strings.TrimSpace(cfg.VaultDir)
	if vaultDir == "" {
		vaultDir = filepath.Join(homeDir, ".local", "share", "azedarach", "accounts")
	}
	return &Service{homeDir: homeDir, codexHome: codexHome, vaultDir: vaultDir}, nil
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

	credential, err := readCredentialFile(s.credentialPath(req.Provider))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("%w: %s has no current credentials", ErrNotFound, req.Provider)
		}
		return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("read current %s credentials: %w", req.Provider, err)
	}
	if err := s.ensureVaultProfileDir(req.Provider, req.Name); err != nil {
		return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("prepare account vault: %w", err)
	}
	root, err := s.openVaultRoot()
	if err != nil {
		return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("open account vault: %w", err)
	}
	defer root.Close()
	profilePath := profileRelativePath(req.Provider, req.Name)
	if !req.Force {
		if _, err := root.Lstat(profilePath); err == nil {
			return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("%w: %s/%s", ErrConflict, req.Provider, req.Name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountBackupResponseBody{}, fmt.Errorf("inspect profile: %w", err)
		}
	}
	if err := atomicWrite(s.profilePath(req.Provider, req.Name), credential); err != nil {
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
		activeHash, hasActive, err := hashFile(s.credentialPath(provider))
		if err != nil {
			return protocol.AIAccountListResponseBody{}, fmt.Errorf("hash current %s credentials: %w", provider, err)
		}
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
			profileData, exists, err := readRootProfile(root, provider, entry.Name())
			if err != nil {
				return protocol.AIAccountListResponseBody{}, fmt.Errorf("hash %s profile %q: %w", provider, entry.Name(), err)
			}
			if !exists {
				continue
			}
			profiles = append(profiles, protocol.AIAccountProfile{
				Provider: provider,
				Name:     entry.Name(),
				Active:   hasActive && sha256.Sum256(profileData) == activeHash,
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
		_, authenticated, err := hashFile(s.credentialPath(provider))
		if err != nil {
			return protocol.AIAccountStatusResponseBody{}, fmt.Errorf("inspect current %s credentials: %w", provider, err)
		}
		status := protocol.AIAccountProviderStatus{Provider: provider, Authenticated: authenticated}
		for _, profile := range listed.Profiles {
			if profile.Provider == provider && profile.Active {
				status.ActiveProfile = profile.Name
				break
			}
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
	s.mu.Lock()
	defer s.mu.Unlock()

	root, err := s.openVaultRoot()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("%w: %s/%s", ErrNotFound, req.Provider, req.Name)
		}
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("inspect account vault: %w", err)
	}
	defer root.Close()
	credential, exists, err := readRootProfile(root, req.Provider, req.Name)
	if err != nil {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("read profile %s/%s: %w", req.Provider, req.Name, err)
	}
	if !exists {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("%w: %s/%s", ErrNotFound, req.Provider, req.Name)
	}
	if err := atomicWrite(s.credentialPath(req.Provider), credential); err != nil {
		return protocol.AIAccountActivateResponseBody{}, fmt.Errorf("activate profile %s/%s: %w", req.Provider, req.Name, err)
	}
	return protocol.AIAccountActivateResponseBody{Profile: protocol.AIAccountProfile{
		Provider: req.Provider,
		Name:     req.Name,
		Active:   true,
	}, RestartExistingProcesses: true}, nil
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

func (s *Service) credentialPath(provider protocol.AIAccountProvider) string {
	if provider == protocol.AIAccountProviderCodex {
		return filepath.Join(s.codexHome, "auth.json")
	}
	return filepath.Join(s.homeDir, ".claude", ".credentials.json")
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

func readRootProfile(root *os.Root, provider protocol.AIAccountProvider, name string) ([]byte, bool, error) {
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
	path := profileRelativePath(provider, name)
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

func hashFile(path string) ([sha256.Size]byte, bool, error) {
	data, err := readCredentialFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return [sha256.Size]byte{}, false, nil
		}
		return [sha256.Size]byte{}, false, err
	}
	return sha256.Sum256(data), true, nil
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
