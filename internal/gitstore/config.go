package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/dgoings/workbook/internal/core"
	"github.com/oklog/ulid/v2"
)

const (
	configPath     = ".workbook/config.json"
	projectFormat  = "workbook.project"
	projectVersion = 1
	projectGuard   = "project.json"
)

// Init creates a repository's tracked Workbook configuration when absent. An
// existing valid configuration is returned unchanged when it has the same key.
func (r *Repository) Init(ctx context.Context, key string, ids core.IDSource) (core.ProjectConfig, bool, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.ProjectConfig{}, false, err
	}
	if err := core.ValidateProjectKey(key); err != nil {
		return core.ProjectConfig{}, false, err
	}
	if err := r.ensurePrivateCache(); err != nil {
		return core.ProjectConfig{}, false, err
	}

	tracked, trackedExists, err := r.readConfig()
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	guard, guardExists, err := r.readProjectGuard()
	if err != nil {
		return core.ProjectConfig{}, false, err
	}

	switch {
	case trackedExists && guardExists:
		if tracked != guard {
			return core.ProjectConfig{}, false, core.Errorf(core.CategoryCorruptData, "tracked Workbook configuration does not match common project guard")
		}
		if err := validateRequestedProjectKey(key, tracked); err != nil {
			return core.ProjectConfig{}, false, err
		}
		return tracked, false, nil
	case trackedExists:
		if err := validateRequestedProjectKey(key, tracked); err != nil {
			return core.ProjectConfig{}, false, err
		}
		persisted, _, err := r.publishProjectGuard(tracked)
		if err != nil {
			return core.ProjectConfig{}, false, err
		}
		if persisted != tracked {
			return core.ProjectConfig{}, false, core.Errorf(core.CategoryCorruptData, "tracked Workbook configuration does not match concurrently published project guard")
		}
		return tracked, false, nil
	case guardExists:
		if err := validateRequestedProjectKey(key, guard); err != nil {
			return core.ProjectConfig{}, false, err
		}
		if err := r.writeConfig(guard); err != nil {
			return core.ProjectConfig{}, false, err
		}
		return guard, false, nil
	}

	if ids == nil {
		return core.ProjectConfig{}, false, core.Errorf(core.CategoryInvocation, "project ID source is required")
	}
	projectID, err := ids.New()
	if err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryInvocation, "cannot generate project ID", err)
	}
	if err := validateProjectID(projectID); err != nil {
		return core.ProjectConfig{}, false, err
	}
	candidate := core.ProjectConfig{
		Format:    projectFormat,
		Version:   projectVersion,
		ProjectID: projectID,
		Key:       key,
	}
	persisted, published, err := r.publishProjectGuard(candidate)
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	if err := r.writeConfig(persisted); err != nil {
		return core.ProjectConfig{}, false, err
	}
	if err := validateRequestedProjectKey(key, persisted); err != nil {
		return core.ProjectConfig{}, false, err
	}
	return persisted, published, nil
}

// LoadConfig returns the repository's validated Workbook configuration.
func (r *Repository) LoadConfig() (core.ProjectConfig, error) {
	config, exists, err := r.readConfig()
	if err != nil {
		return core.ProjectConfig{}, err
	}
	if !exists {
		return core.ProjectConfig{}, core.Errorf(core.CategoryNotInitialized, "Workbook is not initialized")
	}
	return config, nil
}

func (r *Repository) readConfig() (core.ProjectConfig, bool, error) {
	return readConfigFile(filepath.Join(r.Root, configPath), "Workbook configuration")
}

func (r *Repository) readProjectGuard() (core.ProjectConfig, bool, error) {
	return readConfigFile(filepath.Join(r.CommonGitDir, "workbook", projectGuard), "Workbook common project guard")
}

func readConfigFile(path, description string) (core.ProjectConfig, bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return core.ProjectConfig{}, false, nil
	}
	if err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryCorruptData, "cannot read "+description, err)
	}
	config, err := decodeConfig(contents)
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	return config, true, nil
}

func (r *Repository) publishProjectGuard(candidate core.ProjectConfig) (core.ProjectConfig, bool, error) {
	contents, err := encodeConfig(candidate)
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	cacheDir := filepath.Join(r.CommonGitDir, "workbook")
	temporary, err := os.CreateTemp(cacheDir, ".project-*.tmp")
	if err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot create temporary Workbook project guard", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot write Workbook project guard", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot sync Workbook project guard", err)
	}
	if err := temporary.Close(); err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot close Workbook project guard", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot set Workbook project guard permissions", err)
	}

	guardPath := filepath.Join(cacheDir, projectGuard)
	if err := os.Link(temporaryPath, guardPath); err == nil {
		if err := syncDirectory(cacheDir); err != nil {
			return core.ProjectConfig{}, false, err
		}
		return candidate, true, nil
	} else if !errors.Is(err, os.ErrExist) {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot publish Workbook project guard", err)
	}

	persisted, exists, err := r.readProjectGuard()
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	if !exists {
		return core.ProjectConfig{}, false, core.Errorf(core.CategoryOperational, "Workbook project guard disappeared during initialization")
	}
	return persisted, false, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot open Workbook private cache for sync", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot sync Workbook private cache", err)
	}
	return nil
}

func validateRequestedProjectKey(requested string, config core.ProjectConfig) error {
	if config.Key != requested {
		return core.Errorf(core.CategoryValidation, "repository is already initialized with project key %q", config.Key)
	}
	return nil
}

func (r *Repository) writeConfig(config core.ProjectConfig) error {
	contents, err := encodeConfig(config)
	if err != nil {
		return err
	}
	configDir := filepath.Join(r.Root, ".workbook")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return core.Wrap(core.CategoryInvocation, "cannot create Workbook configuration directory", err)
	}

	temporary, err := os.CreateTemp(configDir, ".config-*.tmp")
	if err != nil {
		return core.Wrap(core.CategoryInvocation, "cannot create temporary Workbook configuration", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryInvocation, "cannot write Workbook configuration", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryInvocation, "cannot sync Workbook configuration", err)
	}
	if err := temporary.Close(); err != nil {
		return core.Wrap(core.CategoryInvocation, "cannot close Workbook configuration", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return core.Wrap(core.CategoryInvocation, "cannot set Workbook configuration permissions", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(r.Root, configPath)); err != nil {
		return core.Wrap(core.CategoryInvocation, "cannot install Workbook configuration", err)
	}
	return nil
}

func (r *Repository) ensurePrivateCache() error {
	cacheDir := filepath.Join(r.CommonGitDir, "workbook")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return core.Wrap(core.CategoryInvocation, "cannot create Workbook private cache", err)
	}
	if err := os.Chmod(cacheDir, 0o755); err != nil {
		return core.Wrap(core.CategoryInvocation, "cannot set Workbook private cache permissions", err)
	}
	return nil
}

func encodeConfig(config core.ProjectConfig) ([]byte, error) {
	contents, err := json.Marshal(config)
	if err != nil {
		return nil, core.Wrap(core.CategoryValidation, "cannot encode Workbook configuration", err)
	}
	return append(contents, '\n'), nil
}

func decodeConfig(contents []byte) (core.ProjectConfig, error) {
	var config core.ProjectConfig
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "cannot decode Workbook configuration", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "Workbook configuration contains more than one JSON value")
		}
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "cannot decode Workbook configuration suffix", err)
	}
	if config.Format != projectFormat {
		return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "unsupported Workbook configuration format %q", config.Format)
	}
	if config.Version != projectVersion {
		return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "unsupported Workbook configuration version %d", config.Version)
	}
	if err := validateProjectID(config.ProjectID); err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "Workbook configuration project ID is invalid", err)
	}
	if err := core.ValidateProjectKey(config.Key); err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "Workbook configuration project key is invalid", err)
	}
	canonical, err := encodeConfig(config)
	if err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "cannot canonicalize Workbook configuration", err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "Workbook configuration is not canonical")
	}
	return config, nil
}

func validateProjectID(projectID string) error {
	parsed, err := ulid.ParseStrict(projectID)
	if err != nil {
		return core.Wrap(core.CategoryValidation, "project ID must contain a canonical ULID", err)
	}
	if parsed.String() != projectID {
		return core.Errorf(core.CategoryValidation, "project ID must contain a canonical uppercase ULID")
	}
	return nil
}
