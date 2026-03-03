package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const SchemaVersion = "1"

type Settings struct {
	SchemaVersion string `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	Lane          string `json:"lane"`
}

func DefaultSettings() Settings {
	return Settings{
		SchemaVersion: SchemaVersion,
		Lane:          "A",
	}
}

type Service struct {
	path     string
	defaults Settings
}

func NewService(path string, defaults Settings) *Service {
	if defaults.SchemaVersion == "" {
		defaults.SchemaVersion = SchemaVersion
	}

	return &Service{path: path, defaults: defaults}
}

func (s *Service) Load() (Settings, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.defaults, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}

	if strings.TrimSpace(string(data)) == "" {
		return s.defaults, nil
	}

	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}

	if loaded.SchemaVersion == "" {
		loaded.SchemaVersion = s.defaults.SchemaVersion
	}
	if loaded.Lane == "" {
		loaded.Lane = s.defaults.Lane
	}

	return loaded, nil
}

func (s *Service) Save(settings Settings) error {
	if settings.SchemaVersion == "" {
		settings.SchemaVersion = s.defaults.SchemaVersion
	}
	if settings.Lane == "" {
		settings.Lane = s.defaults.Lane
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}

	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write settings temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("commit settings file: %w", err)
	}

	return nil
}
